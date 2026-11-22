package kubeadjust

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/extension"
)

// The reads of one display go out together (gather.go), and what they answer
// does not depend on which finishes first.

// heldHost holds the first read of each line until all three are in flight,
// so a display that read them one after the other waits out its patience and
// fails the test, rather than hanging.
type heldHost struct {
	*fakeHost
	mu       sync.Mutex
	arrived  map[string]bool
	all      chan struct{}
	patience time.Duration
	waited   bool
}

func held(h *fakeHost) *heldHost {
	return &heldHost{fakeHost: h, arrived: map[string]bool{}, all: make(chan struct{}), patience: 5 * time.Second}
}

func (h *heldHost) hold(line string) {
	h.mu.Lock()
	if !h.arrived[line] {
		h.arrived[line] = true
		if len(h.arrived) == 3 {
			close(h.all)
		}
	}
	h.mu.Unlock()
	select {
	case <-h.all:
	case <-time.After(h.patience):
		h.mu.Lock()
		h.waited = true
		h.mu.Unlock()
	}
}

func (h *heldHost) UsageWindow(r *http.Request, cluster, namespace string) (extension.Window, error) {
	h.hold("window")
	return h.fakeHost.UsageWindow(r, cluster, namespace)
}

func (h *heldHost) List(r *http.Request, cluster string, q extension.ListQuery) (extension.Page, error) {
	switch q.GVR.Resource {
	case "pods":
		h.hold("pods")
	case "replicasets":
		h.hold("lists")
	}
	return h.fakeHost.List(r, cluster, q)
}

func TestTheReadsOfADisplayGoOutTogether(t *testing.T) {
	h := held(shop(t))
	h.window = quietWindow(32<<20, "shop/api-7d9f-a/app")
	a := page(t, h, url.Values{})
	if h.waited {
		t.Fatal("the window, the pods and the lists waited for one another: a display waits for their sum")
	}
	if !a.Ready || noteOf(t, a, "History").Detail == "" {
		t.Fatalf("the page drawn from reads made together is not the page: %+v", a.Notes)
	}
}

// cancelledHost answers the window only once the request is canceled, as a
// slow Prometheus does when another read has ended the request.
type cancelledHost struct {
	*fakeHost
	windowGone bool // the window itself answers ErrUnknownCluster at once
}

func (h *cancelledHost) UsageWindow(r *http.Request, cluster, namespace string) (extension.Window, error) {
	if h.windowGone {
		return extension.Window{}, h.gone()
	}
	select {
	case <-r.Context().Done():
		return extension.Window{}, r.Context().Err()
	case <-time.After(5 * time.Second):
		h.t.Error("the window was not canceled when the pods ended the request")
		return extension.Window{}, nil
	}
}

func TestARequestEndingReadIsTheAnswerAndNotTheCancellationItCaused(t *testing.T) {
	// The pods refused: 502, and never the silent answer of a canceled window.
	h := shop(t)
	h.listErr = map[string]error{"pods": errors.New("pods is forbidden")}
	rec := get(t, &cancelledHost{fakeHost: h}, url.Values{})
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), CodeListFailed) {
		t.Fatalf("pods refused = %d %s, want 502 %s", rec.Code, rec.Body.String(), CodeListFailed)
	}

	// The window's unknown cluster wins over the pods' cancellation.
	h = shop(t)
	rec = get(t, &cancelledHost{fakeHost: h, windowGone: true}, url.Values{})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), CodeUnknownCluster) {
		t.Fatalf("window on an unknown cluster = %d %s, want 404 %s", rec.Code, rec.Body.String(), CodeUnknownCluster)
	}

	// Both at once: the window's answer, as when they were read in turn.
	h = shop(t)
	h.listErr = map[string]error{"pods": errors.New("pods is forbidden")}
	rec = get(t, &cancelledHost{fakeHost: h, windowGone: true}, url.Values{})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("window unknown and pods refused = %d %s, want the window's 404", rec.Code, rec.Body.String())
	}
}

// panicHost panics listing the nodes, on the lists' line.
type panicHost struct{ *fakeHost }

func (h *panicHost) List(r *http.Request, cluster string, q extension.ListQuery) (extension.Page, error) {
	if q.GVR.Resource == "nodes" {
		panic("a list that could not be decoded")
	}
	return h.fakeHost.List(r, cluster, q)
}

func TestAPanicOnTheListsLineIsANoteAndNotTheEndOfThePage(t *testing.T) {
	a := page(t, &panicHost{shop(t)}, url.Values{})
	capacity := noteOf(t, a, "Node capacity")
	if capacity.OK || !strings.Contains(capacity.Detail, "could not be read") {
		t.Fatalf("Node capacity after a panic = %+v, want a capacity that could not be read", capacity)
	}
	if !a.Ready {
		t.Fatal("the page was not drawn after a panic on the lists' line")
	}
}

func TestTheDisplaySaysWhereItsTimeWent(t *testing.T) {
	rec := get(t, shop(t), url.Values{})
	timing := rec.Header().Get("Server-Timing")
	for _, phase := range []string{"usage;dur=", "window;dur=", "lists;dur=", "pods;dur=", "disk;dur=",
		"reads;dur=", "engine;dur=", "shape;dur=", "encode;dur="} {
		if !strings.Contains(timing, phase) {
			t.Errorf("Server-Timing %q does not say %s", timing, strings.TrimSuffix(phase, ";dur="))
		}
	}
}

// slowWindowHost answers the window only when its request gives up, as a
// Prometheus does when a week's read takes longer than the page waits.
type slowWindowHost struct{ *fakeHost }

func (h *slowWindowHost) UsageWindow(r *http.Request, _, _ string) (extension.Window, error) {
	select {
	case <-r.Context().Done():
		return extension.Window{State: extension.WindowUnreadable, Detail: "context deadline exceeded"}, r.Context().Err()
	case <-time.After(5 * time.Second):
		h.t.Error("the page waited for its window past windowWait")
		return extension.Window{}, nil
	}
}

func TestAPageIsDrawnBeforeASlowWindowAndSaysItIsComing(t *testing.T) {
	was := windowWait
	windowWait = 50 * time.Millisecond
	t.Cleanup(func() { windowWait = was })

	start := time.Now()
	a := page(t, &slowWindowHost{fakeHost: shop(t)}, url.Values{})
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the page took %s with a window that never came, want about %s", took, windowWait)
	}
	if a.Window != string(WindowPending) {
		t.Fatalf("window = %q, want %q: the front asks again only for a pending one", a.Window, WindowPending)
	}
	history := noteOf(t, a, "History")
	if history.OK || !strings.Contains(history.Detail, "still reading") {
		t.Fatalf("History = %+v, want a note that the week is still being read", history)
	}
	if !a.Ready {
		t.Fatal("a page with live usage and no window yet is not drawn")
	}
}

func TestAWindowReadInTimeIsNotPending(t *testing.T) {
	h := shop(t)
	h.window = quietWindow(32<<20, "shop/api-7d9f-a/app")
	a := page(t, h, url.Values{})
	if a.Window != string(extension.WindowRead) {
		t.Fatalf("window = %q, want read", a.Window)
	}
}

// getWindow serves the window's state route for a namespace.
func getWindow(t *testing.T, h extension.Host, ns string) windowAnswer {
	t.Helper()
	var handler http.Handler
	for _, r := range testExtra().Routes(h) {
		if r.Pattern == "/window" {
			handler = r.Handler
		}
	}
	if handler == nil {
		t.Fatal("no window route")
	}
	req := httptest.NewRequest(http.MethodGet, "/x/docker-desktop/kubeadjust/window?"+url.Values{"ns": {ns}}.Encode(), nil)
	req.SetPathValue("cluster", "docker-desktop")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /window = %d %s", rec.Code, rec.Body.String())
	}
	var out windowAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// What a pending page polls asks only for the window: no pod, no other list,
// no metrics-server; and it says pending until the window is there.
func TestPollingAPendingWindowListsNothing(t *testing.T) {
	was := windowWait
	windowWait = 20 * time.Millisecond
	t.Cleanup(func() { windowWait = was })

	h := shop(t)
	usage := &countingUsageHost{slowWindowHost: &slowWindowHost{fakeHost: h}}
	for range 3 {
		if got := getWindow(t, usage, "shop").Window; got != string(WindowPending) {
			t.Fatalf("window = %q while it is being read, want pending", got)
		}
	}
	if len(h.queries) != 0 || usage.asked != 0 {
		t.Errorf("three polls listed %d times and asked metrics-server %d times, want neither", len(h.queries), usage.asked)
	}

	h.window = quietWindow(32<<20, "shop/api-7d9f-a/app")
	if got := getWindow(t, h, "shop").Window; got != string(extension.WindowRead) {
		t.Errorf("window = %q once read, want read", got)
	}
	if len(h.queries) != 0 {
		t.Errorf("the state route listed %d times", len(h.queries))
	}
}

// countingUsageHost counts what metrics-server is asked.
type countingUsageHost struct {
	*slowWindowHost
	asked int
}

func (h *countingUsageHost) ContainerUsage(r *http.Request, cluster, ns string) (extension.LiveUsage, error) {
	h.asked++
	return h.fakeHost.ContainerUsage(r, cluster, ns)
}

// While the week is pending, a figure is from one metrics-server scrape: it is
// shown, and no command is offered to copy from it.
func TestAPendingPageOffersNoCommandFromOneScrape(t *testing.T) {
	if a := page(t, shop(t), url.Values{}); len(a.Commands) == 0 {
		t.Fatal("the control page, with no Prometheus, offers no command: the test proves nothing")
	}
	was := windowWait
	windowWait = 20 * time.Millisecond
	t.Cleanup(func() { windowWait = was })

	a := page(t, &slowWindowHost{fakeHost: shop(t)}, url.Values{})
	if a.Window != string(WindowPending) {
		t.Fatalf("window = %q, want pending", a.Window)
	}
	if len(a.Commands) != 0 {
		t.Errorf("a pending page offers %d commands from one scrape: %v", len(a.Commands), a.Commands)
	}
	said := 0
	for _, g := range a.Groups {
		for _, r := range g.Rows {
			if r.Kubectl != "" && r.Confidence != "" {
				t.Errorf("row %s %s carries %q while pending", r.Workload, r.Resource, r.Kubectl)
			}
			if slices.Contains(r.Warnings, pendingWithheld) {
				said++
			}
		}
	}
	if said == 0 {
		t.Error("no row says why its command is held back")
	}
}

// Every answer that draws the History says when its window was read: the
// window the core kept, not the display that was handed it. Pending says
// nothing, since nothing has been read yet.
func TestEveryAnswerSaysWhenItsWindowWasRead(t *testing.T) {
	h := spread(t)
	h.add("deployments", deployment("shop", "api"))
	readAt := time.Date(2026, 11, 10, 9, 30, 0, 0, time.FixedZone("CET", 3600))
	h.window.ReadAt = readAt
	want := "2026-11-10T08:30:00Z"

	if got := page(t, h, url.Values{}).WindowReadAt; got != want {
		t.Errorf("the page's windowReadAt = %q, want %q", got, want)
	}
	if got := workloadTab(t, h, "deployments", "shop", "api").WindowReadAt; got != want {
		t.Errorf("the workload tab's windowReadAt = %q, want %q", got, want)
	}
	if got := nodeTab(t, h, "worker-1").WindowReadAt; got != want {
		t.Errorf("the node tab's windowReadAt = %q, want %q", got, want)
	}
	if got := getWindow(t, h, "shop").WindowReadAt; got != want {
		t.Errorf("the window state's windowReadAt = %q, want %q", got, want)
	}

	was := windowWait
	windowWait = 20 * time.Millisecond
	t.Cleanup(func() { windowWait = was })
	slow := &slowWindowHost{fakeHost: h}
	if got := getWindow(t, slow, "shop"); got.Window != string(WindowPending) || got.WindowReadAt != "" {
		t.Errorf("a pending window says %q read at %q, want pending and no time", got.Window, got.WindowReadAt)
	}
}

// With the wait as shipped, a window that is not there yet holds the page for
// the wait and no longer: the first display of a large cluster is bounded by
// this, not by the week's read.
func TestTheShippedWaitDrawsAPendingPageWithinTwoSeconds(t *testing.T) {
	start := time.Now()
	a := page(t, &slowWindowHost{fakeHost: shop(t)}, url.Values{})
	took := time.Since(start)
	if took > 2*time.Second {
		t.Errorf("the page took %s with a window still being read, want under 2s", took)
	}
	if took < windowWait || a.Window != string(WindowPending) {
		t.Errorf("took %s, window %q: want the page to wait the %s and say pending", took, a.Window, windowWait)
	}
}
