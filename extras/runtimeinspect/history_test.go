package runtimeinspect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

const historyPath = "/x/kind/runtime/pod/shop/billing-0/history"

// week is n samples a fifteen minutes apart, memory rising from lo to hi bytes.
func week(n int, lo, hi float64) []extension.Sample {
	out := make([]extension.Sample, n)
	for i := range out {
		f := 0.0
		if n > 1 {
			f = float64(i) / float64(n-1)
		}
		out[i] = extension.Sample{T: 1_700_000_000 + float64(i)*900, V: lo + (hi-lo)*f}
	}
	return out
}

func historyHost(t *testing.T, win extension.Window) *fakeHost {
	t.Helper()
	h := newHost(t, pod("shop", "billing-0",
		memory(corev1.Container{Name: "app", Image: "eclipse-temurin:21-jdk"}, "256Mi", "512Mi"),
		corev1.Container{Name: "sidecar", Image: "envoy"}))
	h.window = &win
	return h
}

func TestTheHistoryDrawsOnlyThisPodsContainers(t *testing.T) {
	const mi = 1 << 20
	h := historyHost(t, extension.Window{State: extension.WindowRead, Detail: "Prometheus, 7d at 15m", Containers: map[string]extension.ContainerWindow{
		extension.ContainerKey("shop", "billing-0", "app"):     {Memory: week(672, 200*mi, 300*mi), CPU: week(672, 1, 2)},
		extension.ContainerKey("shop", "billing-0", "sidecar"): {Memory: week(672, 20*mi, 30*mi)},
		// Another pod's: never in this answer.
		extension.ContainerKey("shop", "billing-1", "app"): {Memory: week(672, 900*mi, 950*mi)},
	}})
	rec := serve(t, h, historyPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d %s", rec.Code, rec.Body)
	}
	got := decode[historyAnswer](t, rec)
	if got.State != historyRead || len(got.Series) != 2 || got.Series[0].Container != "app" || got.Cluster != "kind" {
		t.Fatalf("answer = %+v", got)
	}
	app := got.Series[0]
	if len(app.Points) != MaxHistoryPoints || app.Max.Bytes != 300*mi || app.Min.Bytes < 200*mi || app.Limit == nil ||
		app.Limit.Bytes != 512*mi || app.LimitAt == nil || *app.LimitAt != 0 {
		t.Errorf("app = %+v (limit at %v)", app, app.LimitAt)
	}
	if app.Points[0][0] != 0 || app.Points[len(app.Points)-1][0] != 100 || got.Scale.Bytes != 512*mi || len(got.Ticks) != 3 ||
		got.Ticks[0].Y != 0 || got.Ticks[2].Y != 100 {
		t.Errorf("geometry: first %v, last %v, scale %+v, ticks %+v", app.Points[0], app.Points[len(app.Points)-1], got.Scale, got.Ticks)
	}
	if got.Series[1].Limit != nil || got.Series[1].LimitAt != nil {
		t.Errorf("a container with no limit has one: %+v", got.Series[1])
	}
	if len(h.windowAsked) != 1 || h.windowAsked[0] != "shop" {
		t.Errorf("the window was asked for %q: one namespace, never the cluster", h.windowAsked)
	}
}

// A spike survives the downsampling: each bucket keeps its peak.
func TestTheDownsamplingKeepsThePeak(t *testing.T) {
	s := week(672, 100, 100)
	s[301].V = 9999
	got := downsample(s, MaxHistoryPoints)
	peak := 0.0
	for _, p := range got {
		peak = max(peak, p.V)
	}
	if len(got) != MaxHistoryPoints || peak != 9999 {
		t.Errorf("%d points, peak %v", len(got), peak)
	}
	if short := week(10, 1, 2); len(downsample(short, MaxHistoryPoints)) != 10 {
		t.Error("a short series was cut")
	}
}

// The four states are four answers, and none is drawn as a pod that used nothing.
func TestTheHistoryStatesAreDistinct(t *testing.T) {
	cases := map[string]struct {
		win   extension.Window
		state string
	}{
		"absent":     {extension.Window{State: extension.WindowNoPrometheus, Detail: "none"}, historyAbsent},
		"unreadable": {extension.Window{State: extension.WindowUnreadable, Detail: "refused"}, historyUnreadable},
		"empty":      {extension.Window{State: extension.WindowEmpty}, historyEmpty},
		// Read for other pods, with nothing for this one.
		"other pods": {extension.Window{State: extension.WindowRead, Containers: map[string]extension.ContainerWindow{
			extension.ContainerKey("shop", "billing-1", "app"): {Memory: week(10, 1, 2)}}}, historyEmpty},
		// The memory half failed: unread, not empty.
		"memory failed": {extension.Window{State: extension.WindowRead, MemoryFailed: "timeout"}, historyUnreadable},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := decode[historyAnswer](t, serve(t, historyHost(t, c.win), historyPath))
			if got.State != c.state || len(got.Series) != 0 || got.Scale != nil {
				t.Errorf("answer = %+v", got)
			}
		})
	}
}

func TestTheHistoryNamesItsFailures(t *testing.T) {
	cases := map[string]struct {
		err    error
		status int
		code   string
	}{
		"unknown cluster": {extension.ErrUnknownCluster, 404, CodeUnknownCluster},
		"denied":          {extension.ErrDenied, 403, CodeListForbidden},
		"unreviewable":    {extension.ErrUnreviewable, 503, CodeListUnreviewed},
		"failed":          {errors.New("boom"), 502, CodeHistoryFailed},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := historyHost(t, extension.Window{})
			h.window, h.windowErr = nil, c.err
			rec := serve(t, h, historyPath)
			if got := decode[problem](t, rec); rec.Code != c.status || got.Code != c.code {
				t.Errorf("answered %d %+v", rec.Code, got)
			}
		})
	}
	// A pod the person cannot list has no history, and Prometheus is not asked.
	h := historyHost(t, extension.Window{State: extension.WindowEmpty})
	h.listErr = extension.ErrDenied
	if rec := serve(t, h, historyPath); rec.Code != http.StatusForbidden || len(h.windowAsked) != 0 {
		t.Errorf("answered %d, window asked %q", rec.Code, h.windowAsked)
	}
	h = historyHost(t, extension.Window{State: extension.WindowEmpty})
	if rec := serve(t, h, "/x/kind/runtime/pod/shop/nope/history"); rec.Code != http.StatusNotFound || len(h.windowAsked) != 0 {
		t.Errorf("an unknown pod answered %d", rec.Code)
	}
	if rec := serve(t, h, "/x/kind/runtime/pod/Bad_NS/billing-0/history"); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad namespace answered %d", rec.Code)
	}
}

// A window that is still being read is `pending`, and nothing is left running.
func TestAWindowStillBeingReadIsPending(t *testing.T) {
	old := historyWait
	historyWait = 50 * time.Millisecond
	defer func() { historyWait = old }()
	h := historyHost(t, extension.Window{})
	h.window, h.windowHangs = nil, true
	before := runtime.NumGoroutine()
	got := decode[historyAnswer](t, serve(t, h, historyPath))
	if got.State != historyPending || len(got.Series) != 0 {
		t.Errorf("answer = %+v", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("%d goroutines before, %d after", before, after)
	}
}

func TestACancelledHistoryAnswersNothing(t *testing.T) {
	h := historyHost(t, extension.Window{})
	h.window, h.windowHangs = nil, true
	mux := http.NewServeMux()
	for _, r := range New().Routes(h) {
		mux.Handle(r.Method+" /x/{cluster}/"+Key+r.Pattern, r.Handler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, historyPath, nil).WithContext(ctx))
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler did not return after its request was canceled")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("a gone request was answered: %s", rec.Body)
	}
}

// The answer is bounded whatever the window holds: sixteen lines of 336 points.
func TestAHistoryIsBounded(t *testing.T) {
	var cs []corev1.Container
	win := extension.Window{State: extension.WindowRead, Containers: map[string]extension.ContainerWindow{}}
	for i := 0; i < 40; i++ {
		name := fmt.Sprintf("c%d", i)
		cs = append(cs, corev1.Container{Name: name})
		win.Containers[extension.ContainerKey("shop", "billing-0", name)] = extension.ContainerWindow{Memory: week(2000, 1, float64(i+2))}
	}
	got := buildHistory(pod("shop", "billing-0", cs...), win)
	if len(got.Series) != MaxHistorySeries || !got.Cut {
		t.Fatalf("%d series, cut %v", len(got.Series), got.Cut)
	}
	for _, l := range got.Series {
		if len(l.Points) != MaxHistoryPoints {
			t.Fatalf("%s has %d points", l.Container, len(l.Points))
		}
	}
}

func BenchmarkHistoryOfALargeNamespace(b *testing.B) {
	win := extension.Window{State: extension.WindowRead, Containers: map[string]extension.ContainerWindow{}}
	for i := 0; i < 2000; i++ {
		win.Containers[extension.ContainerKey("shop", fmt.Sprintf("p%d", i), "app")] = extension.ContainerWindow{Memory: week(672, 1, 2)}
	}
	p := pod("shop", "p1999", corev1.Container{Name: "app"})
	b.ReportAllocs()
	for b.Loop() {
		buildHistory(p, win)
	}
}
