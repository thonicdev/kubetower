package launch

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/forward"
)

// fakeTunnels is a forward set: List answers live, Close removes and records.
type fakeTunnels struct {
	mu     sync.Mutex
	live   []forward.Tunnel
	closed []string
}

func (f *fakeTunnels) List() []forward.Tunnel {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.live)
}

func (f *fakeTunnels) Close(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, t := range f.live {
		if t.ID == id {
			f.live = slices.Delete(f.live, i, i+1)
			f.closed = append(f.closed, id)
			return true
		}
	}
	return false
}

func (f *fakeTunnels) drop(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.live = slices.DeleteFunc(f.live, func(t forward.Tunnel) bool { return t.ID == id })
}

// fakeHost records what the platform was asked to draw and close.
type fakeHost struct {
	opened map[int]toolWindowTarget
	closed []int
	// refuse is the platform's answer that the window could not be drawn.
	refuse error
}

func (h *fakeHost) openToolWindow(id int, target toolWindowTarget) error {
	if h.refuse != nil {
		return h.refuse
	}
	if h.opened == nil {
		h.opened = map[int]toolWindowTarget{}
	}
	h.opened[id] = target
	return nil
}

func (h *fakeHost) closeToolWindow(id int) { h.closed = append(h.closed, id) }

func hubbleTunnel() forward.Tunnel {
	return forward.Tunnel{ID: "f1", Cluster: "kind-demo", Namespace: "kube-system", Kind: "service", Name: "hubble-ui",
		LocalAddr: "127.0.0.1:30001", Scheme: "http", Healthy: true}
}

func whiskerTunnel() forward.Tunnel {
	return forward.Tunnel{ID: "f2", Cluster: "prod-eu", Namespace: "calico-system", Kind: "service", Name: "whisker",
		LocalAddr: "127.0.0.1:30002", Scheme: "https", Healthy: true}
}

func TestOnlyALiveTunnelOfThisConsoleOpensInAToolWindow(t *testing.T) {
	sick := forward.Tunnel{ID: "f3", Cluster: "kind-demo", LocalAddr: "127.0.0.1:30003", Scheme: "http", Healthy: false}
	live := []forward.Tunnel{hubbleTunnel(), whiskerTunnel(), sick}
	cases := []struct {
		raw     string
		forward string // "" for refused
	}{
		{"http://127.0.0.1:30001/", "f1"},
		{"http://127.0.0.1:30001/?namespace=team-a", "f1"},
		{"https://127.0.0.1:30002/flows", "f2"},
		// The console's own port, another port, another host.
		{"http://127.0.0.1:8091/", ""},
		{"http://127.0.0.1:30009/", ""},
		{"http://example.com:30001/", ""},
		{"http://10.0.0.5:30001/", ""},
		{"http://localhost:30001/", ""},
		{"http://[::1]:30001/", ""},
		// A dead tunnel, the wrong scheme, other schemes.
		{"http://127.0.0.1:30003/", ""},
		{"https://127.0.0.1:30001/", ""},
		{"http://127.0.0.1:30002/", ""},
		{"file:///etc/passwd", ""},
		{"javascript:alert(1)", ""},
		{"data:text/html,hi", ""},
		{"ftp://127.0.0.1:30001/", ""},
		// User information, a port missing, garbage.
		{"http://user:pass@127.0.0.1:30001/", ""},
		{"http://127.0.0.1/", ""},
		{"http://127.0.0.1:30001%2f/", ""},
		{"", ""},
		{"http//127.0.0.1:30001/", ""},
	}
	for _, c := range cases {
		got, err := toolWindowAllowed(c.raw, live)
		if c.forward == "" {
			if err == nil {
				t.Errorf("%q opened tunnel %s; want refused", c.raw, got.ID)
			} else if !errors.Is(err, api.ErrInvalid) {
				t.Errorf("%q refused with %v; want ErrInvalid", c.raw, err)
			}
			continue
		}
		if err != nil || got.ID != c.forward {
			t.Errorf("%q = %q, %v; want tunnel %s", c.raw, got.ID, err, c.forward)
		}
	}
}

func TestAToolWindowPathStaysOnTheTunnel(t *testing.T) {
	for _, ok := range []string{"", "/", "/?namespace=team-a", "/flows/a-b_c.d~e?x=1&y=%20"} {
		if err := toolWindowPath(ok); err != nil {
			t.Errorf("path %q refused: %v", ok, err)
		}
	}
	long := "/" + strings.Repeat("a", maxToolWindowPath)
	if err := toolWindowPath(long[:maxToolWindowPath]); err != nil {
		t.Errorf("a path of exactly %d bytes refused: %v", maxToolWindowPath, err)
	}
	for _, bad := range []string{"flows", "//evil.example.com/", "/\\evil", "/a b", "/a\tb", "/a\nb",
		"http://evil.example.com/", "/#frag", "/\x7f", long} {
		if err := toolWindowPath(bad); !errors.Is(err, api.ErrInvalid) {
			t.Errorf("path %q = %v; want ErrInvalid", bad, err)
		}
	}
}

func TestAToolWindowSaysKubeTowerTheClusterAndTheTool(t *testing.T) {
	if got := toolWindowTitle("kind-demo", "hubble"); got != "KubeTower · kind-demo · Hubble UI" {
		t.Errorf("hubble title = %q", got)
	}
	if got := toolWindowTitle("prod-eu", "whisker"); got != "KubeTower · prod-eu · Whisker" {
		t.Errorf("whisker title = %q", got)
	}
}

func TestAToolWindowOpensOnlyInTheDesktopWindow(t *testing.T) {
	tunnels := &fakeTunnels{live: []forward.Tunnel{hubbleTunnel()}}
	tw := newToolWindows(tunnels, nil)
	if tw.Available() {
		t.Fatal("available before the native window is drawn")
	}
	if err := tw.OpenToolWindow("f1", "hubble", "/", true); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("open with no native window = %v; want ErrConflict", err)
	}
	var none *toolWindows
	if none.Available() {
		t.Fatal("a nil set is available")
	}

	host := &fakeHost{}
	tw.attach(host)
	if !tw.Available() {
		t.Fatal("not available once the native window is drawn")
	}
	if err := tw.OpenToolWindow("f1", "hubble", "/?namespace=team-a", true); err != nil {
		t.Fatal(err)
	}
	want := toolWindowTarget{URL: "http://127.0.0.1:30001/?namespace=team-a", Origin: "http://127.0.0.1:30001",
		Title: "KubeTower · kind-demo · Hubble UI"}
	if got := host.opened[1]; got != want {
		t.Fatalf("drew %+v; want %+v", got, want)
	}

	tw.attach(nil)
	if tw.Available() || len(tw.open) != 0 {
		t.Fatal("still available, or still holding windows, after the native window closed")
	}
	if err := tw.OpenToolWindow("f1", "hubble", "/", true); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("open after the native window closed = %v; want ErrConflict", err)
	}
}

func TestAToolWindowRefusesWhatItCannotShow(t *testing.T) {
	sick := forward.Tunnel{ID: "f3", Cluster: "kind-demo", LocalAddr: "127.0.0.1:30003", Healthy: false}
	tunnels := &fakeTunnels{live: []forward.Tunnel{hubbleTunnel(), sick}}
	tw := newToolWindows(tunnels, nil)
	host := &fakeHost{}
	tw.attach(host)

	if err := tw.OpenToolWindow("f1", "grafana", "/", false); !errors.Is(err, api.ErrInvalid) {
		t.Errorf("unknown tool = %v; want ErrInvalid", err)
	}
	if err := tw.OpenToolWindow("f1", "hubble", "//evil.example.com/", false); !errors.Is(err, api.ErrInvalid) {
		t.Errorf("a path naming a host = %v; want ErrInvalid", err)
	}
	if err := tw.OpenToolWindow("nope", "hubble", "/", false); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("unknown forward = %v; want ErrNotFound", err)
	}
	if err := tw.OpenToolWindow("f3", "hubble", "/", false); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("a dead forward = %v; want ErrNotFound", err)
	}
	// A forward whose address is not a loopback one (a container's published
	// port) is refused by the allow-list, not trusted for being in the list.
	tunnels.live = append(tunnels.live, forward.Tunnel{ID: "f4", Cluster: "kind-demo", LocalAddr: "0.0.0.0:30004", Healthy: true})
	if err := tw.OpenToolWindow("f4", "hubble", "/", false); !errors.Is(err, api.ErrInvalid) {
		t.Errorf("a forward on 0.0.0.0 = %v; want ErrInvalid", err)
	}
	if len(host.opened) != 0 {
		t.Fatalf("drew %v after refusals", host.opened)
	}

	for range maxToolWindows {
		if err := tw.OpenToolWindow("f1", "hubble", "/", false); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.OpenToolWindow("f1", "hubble", "/", false); !errors.Is(err, api.ErrConflict) {
		t.Errorf("window %d = %v; want ErrConflict", maxToolWindows+1, err)
	}
}

func TestClosingAToolWindowClosesTheTunnelOpenedForIt(t *testing.T) {
	tunnels := &fakeTunnels{live: []forward.Tunnel{hubbleTunnel(), whiskerTunnel()}}
	tw := newToolWindows(tunnels, nil)
	tw.attach(&fakeHost{})

	// 1 and 2 show f1, opened for 1; 3 shows f2, which was open before.
	for _, open := range []struct {
		id    string
		owned bool
	}{{"f1", true}, {"f1", false}, {"f2", false}} {
		if err := tw.OpenToolWindow(open.id, "hubble", "/", open.owned); err != nil {
			t.Fatal(err)
		}
	}

	tw.closed(1)
	if len(tunnels.closed) != 0 {
		t.Fatalf("closed %v while window 2 still shows f1", tunnels.closed)
	}
	tw.closed(3)
	if len(tunnels.closed) != 0 {
		t.Fatalf("closed %v: f2 was not opened for its window", tunnels.closed)
	}
	// Window 2 took f1 over when window 1 closed, so the last window on it
	// closes it.
	tw.closed(2)
	if !slices.Equal(tunnels.closed, []string{"f1"}) {
		t.Fatalf("closed %v; want f1, handed to window 2 when window 1 closed", tunnels.closed)
	}

	// A second report of the same window, or of one never opened, does nothing.
	tw.closed(2)
	tw.closed(99)
	if len(tunnels.closed) != 1 {
		t.Fatalf("closed %v after repeated reports", tunnels.closed)
	}
}

func TestATunnelIsHandedToTheLowestRemainingWindow(t *testing.T) {
	tunnels := &fakeTunnels{live: []forward.Tunnel{hubbleTunnel()}}
	tw := newToolWindows(tunnels, nil)
	tw.attach(&fakeHost{})
	for _, owned := range []bool{true, false, false} {
		if err := tw.OpenToolWindow("f1", "hubble", "/", owned); err != nil {
			t.Fatal(err)
		}
	}
	tw.closed(1)
	if !tw.open[2].closeTunnel || tw.open[3].closeTunnel {
		t.Fatalf("after window 1 closed: window 2 owns %t, window 3 owns %t; want 2 only", tw.open[2].closeTunnel, tw.open[3].closeTunnel)
	}
	tw.closed(3)
	tw.closed(2)
	if !slices.Equal(tunnels.closed, []string{"f1"}) {
		t.Fatalf("closed %v; want f1 once the last window closed", tunnels.closed)
	}
}

func TestOnlyAClickedHttpLinkLeavesAToolWindowAndNotTooOften(t *testing.T) {
	b := &handOutBudget{max: 3, per: 10 * time.Second}
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	for _, refused := range []struct {
		raw  string
		user bool
	}{
		{"mailto:a@example.com", true},
		{"ms-settings:privacy", true},
		{"file:///etc/passwd", true},
		{"javascript:alert(1)", true},
		{"https://docs.cilium.io/", false},
	} {
		if ok, why := b.allow(refused.raw, refused.user, now); ok || why == "" {
			t.Errorf("%q (clicked %t) allowed; want refused with a reason", refused.raw, refused.user)
		}
	}
	for i := range 3 {
		if ok, why := b.allow("https://docs.cilium.io/", true, now.Add(time.Duration(i)*time.Second)); !ok {
			t.Fatalf("click %d refused: %s", i+1, why)
		}
	}
	if ok, _ := b.allow("http://example.com/", true, now.Add(3*time.Second)); ok {
		t.Fatal("a fourth address within 10 s was allowed")
	}
	if ok, why := b.allow("http://example.com/", true, now.Add(10*time.Second)); !ok {
		t.Fatalf("the first address left 10 s ago, yet the next is refused: %s", why)
	}
}

func TestEmbedGivesBackWhatItsLoopTook(t *testing.T) {
	for _, c := range []struct {
		name                           string
		returned, panicked, controller bool
		want                           embedOutcome
	}{
		{"built", true, false, true, embedOutcome{usable: true, wakeDispatch: true}},
		{"upstream: stopped on WM_QUIT, Init panicked", false, true, false, embedOutcome{repostQuit: true, wakeDispatch: true}},
		// The patched copy: stopped on WM_QUIT, which it posted again itself.
		{"the copy: stopped on WM_QUIT, reposted by it", false, false, false, embedOutcome{wakeDispatch: true}},
		{"stopped with no controller", true, false, false, embedOutcome{repostQuit: true, wakeDispatch: true}},
		{"refused before its loop", false, false, false, embedOutcome{wakeDispatch: true}},
		{"panicked once built", false, true, true, embedOutcome{wakeDispatch: true}},
	} {
		if got := afterEmbed(c.returned, c.panicked, c.controller); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestAToolWindowClosesWithItsTunnelOrItsCluster(t *testing.T) {
	tunnels := &fakeTunnels{live: []forward.Tunnel{hubbleTunnel(), whiskerTunnel()}}
	clusters := map[string]bool{"kind-demo": true, "prod-eu": true}
	var mu sync.Mutex
	known := func(c string) bool { mu.Lock(); defer mu.Unlock(); return clusters[c] }
	tw := newToolWindows(tunnels, known)
	host := &fakeHost{}
	tw.attach(host)
	if err := tw.OpenToolWindow("f1", "hubble", "/", true); err != nil {
		t.Fatal(err)
	}
	if err := tw.OpenToolWindow("f2", "whisker", "/", true); err != nil {
		t.Fatal(err)
	}

	if n := tw.sweep(); n != 0 || len(host.closed) != 0 {
		t.Fatalf("swept %d, closed %v with every tunnel and cluster live", n, host.closed)
	}

	// The tunnel went away (its pod, the Ports panel): its window closes.
	tunnels.drop("f1")
	if n := tw.sweep(); n != 1 || !slices.Equal(host.closed, []int{1}) {
		t.Fatalf("swept %d, closed windows %v; want window 1", n, host.closed)
	}

	// The cluster left the kubeconfig: its window closes, and so does the
	// tunnel opened for it.
	mu.Lock()
	delete(clusters, "prod-eu")
	mu.Unlock()
	if n := tw.sweep(); n != 1 || !slices.Equal(host.closed, []int{1, 2}) {
		t.Fatalf("swept %d, closed windows %v; want 1 then 2", n, host.closed)
	}
	if !slices.Equal(tunnels.closed, []string{"f2"}) {
		t.Fatalf("closed tunnels %v; want f2", tunnels.closed)
	}
	// The platform then reports the windows it closed: nothing more happens.
	tw.closed(1)
	tw.closed(2)
	if len(tunnels.closed) != 1 || len(tw.open) != 0 {
		t.Fatalf("closed %v, %d windows held after the reports", tunnels.closed, len(tw.open))
	}
}

func TestAToolWindowKeepsToItsTunnel(t *testing.T) {
	origin := "http://127.0.0.1:30001"
	for _, c := range []struct {
		target         string
		allow, handOut bool
	}{
		{"http://127.0.0.1:30001/", true, false},
		{"http://127.0.0.1:30001/?namespace=team-a#flows", true, false},
		{"about:blank", true, false},
		// Another port of the loopback (the console's own), another host.
		{"http://127.0.0.1:8091/", false, true},
		{"https://127.0.0.1:30001/", false, true},
		{"https://docs.cilium.io/", false, true},
		{"http://user@127.0.0.1:30001/", false, true},
		// Nothing else loads, and nothing else is handed out.
		{"file:///etc/passwd", false, false},
		{"data:text/html,hi", false, false},
		{"blob:http://127.0.0.1:30001/x", false, false},
		{"javascript:alert(1)", false, false},
		{"kubetower://c/demo", false, false},
		{"about:srcdoc", false, false},
	} {
		allow, handOut := toolNavigation(origin, c.target)
		if allow != c.allow || handOut != c.handOut {
			t.Errorf("%q = allow %t, hand out %t; want %t, %t", c.target, allow, handOut, c.allow, c.handOut)
		}
	}
}

// fakeBuilder records the steps of a build and, during embed, what a close
// request would have done.
type fakeBuilder struct {
	building                buildingWindows
	created, embedded, held bool
	steps                   []string
	closeHonoured           bool
}

func (f *fakeBuilder) create() (uintptr, bool) {
	f.steps = append(f.steps, "create")
	return 0x51, f.created
}

func (f *fakeBuilder) embed(h uintptr) bool {
	f.steps = append(f.steps, "embed")
	f.closeHonoured = f.building.closeHonoured(h)
	return f.embedded
}

func (f *fakeBuilder) hold(uintptr) bool {
	f.steps = append(f.steps, "hold")
	return f.held
}

func (f *fakeBuilder) show(uintptr)    { f.steps = append(f.steps, "show") }
func (f *fakeBuilder) destroy(uintptr) { f.steps = append(f.steps, "destroy") }

func TestAToolWindowIsShownOnlyOnceBuiltAndCannotBeClosedMeanwhile(t *testing.T) {
	for _, c := range []struct {
		name                    string
		created, embedded, held bool
		want                    string
		open                    bool
	}{
		{"built", true, true, true, "create embed hold show", true},
		{"no window", false, true, true, "create", false},
		{"no engine", true, false, true, "create embed destroy", false},
		{"no policy", true, true, false, "create embed hold destroy", false},
	} {
		building := buildingWindows{}
		f := &fakeBuilder{building: building, created: c.created, embedded: c.embedded, held: c.held}
		h, open := buildToolWindow(f, building)
		if got := strings.Join(f.steps, " "); got != c.want || open != c.open {
			t.Errorf("%s: steps %q open %v, want %q %v", c.name, got, open, c.want, c.open)
		}
		if c.created && f.closeHonoured {
			t.Errorf("%s: a close request during the build would have destroyed the window", c.name)
		}
		if len(building) != 0 {
			t.Errorf("%s: still building after the build: %v", c.name, building)
		}
		if open && !building.closeHonoured(h) {
			t.Errorf("%s: an open window refuses to close", c.name)
		}
	}
	if !(buildingWindows{}).closeHonoured(0x99) {
		t.Error("a window this process is not building refuses to close")
	}
}

func TestOnlyAFreshClickOrKeyPressCountsAsAGestureOnMacOS(t *testing.T) {
	for _, c := range []struct {
		name      string
		eventType int
		age       time.Duration
		want      bool
	}{
		{"click just now", nsEventLeftMouseUp, 30 * time.Millisecond, true},
		{"return key", nsEventKeyDown, 200 * time.Millisecond, true},
		{"middle click", nsEventOtherMouseUp, 0, true},
		{"click a second ago", nsEventLeftMouseUp, time.Second, true},
		{"click long ago, a timer's a.click()", nsEventLeftMouseUp, 5 * time.Second, false},
		{"mouse moved", 5, 10 * time.Millisecond, false},
		{"scroll", 22, 10 * time.Millisecond, false},
		{"no current event", 0, 0, false},
		{"a timestamp ahead of the clock", nsEventKeyDown, -time.Second, false},
	} {
		if got := aRealGesture(c.eventType, c.age); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// The macOS hand-out sheet (#84): the host as its title, the whole address as
// its detail, nothing else.
func TestTheHandOutSheetNamesTheHostAndTheAddress(t *testing.T) {
	message, detail := handOutQuestion("https://docs.cilium.io/en/stable/observability/hubble/")
	if message != "docs.cilium.io" || detail != "https://docs.cilium.io/en/stable/observability/hubble/" {
		t.Errorf("%q / %q", message, detail)
	}
	if message, _ := handOutQuestion("not a url"); message != "not a url" {
		t.Errorf("an address with no host: %q", message)
	}
}

// A script's a.click() after a real click elsewhere (#84) reaches at most a
// question, never the browser: only the person's Open in browser opens, and a
// second question while one is open is refused, not answered by the first.
func TestOnlyAConfirmedHandOutOpensAndOneIsAskedAtATime(t *testing.T) {
	var a handOutAsk
	if !a.ask("https://a.example/", 1) {
		t.Fatal("the first question was refused")
	}
	if a.ask("https://b.example/", 1) {
		t.Fatal("a second question while one is open")
	}
	if target, open := a.answer(false); open || target != "https://a.example/" {
		t.Fatalf("Cancel: %q %v", target, open)
	}
	if !a.ask("https://b.example/", 1) {
		t.Fatal("no question after the first was answered")
	}
	if target, open := a.answer(true); !open || target != "https://b.example/" {
		t.Fatalf("Open in browser: %q %v", target, open)
	}
	// An answer with no question open opens nothing.
	if _, open := a.answer(true); open {
		t.Error("an answer with no question opened something")
	}
}

// A tool window closed by KubeTower (its tunnel went) while its hand-out
// question is up frees the question: AppKit does not promise the sheet's
// handler then, and one question left open would refuse every later hand-out
// until a restart (#84). Another window's close leaves it, and the handler
// that may still come opens nothing.
func TestClosingTheAskingWindowFreesTheNextQuestion(t *testing.T) {
	var a handOutAsk
	a.ask("https://a.example/", 7)
	if a.closed(8) || !a.busy() {
		t.Fatal("another window's close freed the question")
	}
	if !a.closed(7) || a.busy() {
		t.Fatal("the asking window's close left the question open")
	}
	if _, open := a.answer(true); open {
		t.Error("the late handler of a closed window's sheet opened the browser")
	}
	if !a.ask("https://b.example/", 8) {
		t.Error("no question could be asked after the asking window closed")
	}
	if a.closed(7) {
		t.Error("a second close of the first window freed the new question")
	}
}

// A platform that could not draw the window (#84: WebView2 refused to create
// its environment or controller) is a 409, the window is forgotten, and the
// tunnel stays open even when the page opened it for the window.
func TestARefusedToolWindowIsAConflictAndKeepsItsTunnel(t *testing.T) {
	tunnels := &fakeTunnels{live: []forward.Tunnel{hubbleTunnel()}}
	tw := newToolWindows(tunnels, nil)
	host := &fakeHost{refuse: errToolWindowRefused}
	tw.attach(host)

	err := tw.OpenToolWindow("f1", "hubble", "/", true)
	if !errors.Is(err, api.ErrConflict) || !strings.Contains(err.Error(), "could not be drawn") {
		t.Fatalf("a refused window = %v; want ErrConflict saying it could not be drawn", err)
	}
	if len(tunnels.closed) != 0 {
		t.Errorf("the tunnel was closed: %v", tunnels.closed)
	}
	tw.mu.Lock()
	open := len(tw.open)
	tw.mu.Unlock()
	if open != 0 {
		t.Errorf("%d windows still counted after the refusal", open)
	}
	// The console carries on: once the platform draws again, a window opens.
	host.refuse = nil
	if err := tw.OpenToolWindow("f1", "hubble", "/", true); err != nil || len(host.opened) != 1 {
		t.Fatalf("after a refusal = %v, opened %v", err, host.opened)
	}
}

// refused forgets the window and closes nothing, whatever the page asked.
func TestARefusedWindowNeverClosesItsTunnel(t *testing.T) {
	tunnels := &fakeTunnels{live: []forward.Tunnel{hubbleTunnel()}}
	tw := newToolWindows(tunnels, nil)
	host := &fakeHost{}
	tw.attach(host)
	if err := tw.OpenToolWindow("f1", "hubble", "/", true); err != nil {
		t.Fatal(err)
	}
	tw.refused(1)
	if len(tunnels.closed) != 0 {
		t.Errorf("refused closed %v", tunnels.closed)
	}
	// closed, by contrast, closes the tunnel opened for the window.
	if err := tw.OpenToolWindow("f1", "hubble", "/", true); err != nil {
		t.Fatal(err)
	}
	tw.closed(2)
	if len(tunnels.closed) != 1 {
		t.Errorf("closed closed %v", tunnels.closed)
	}
}

// The patched go-webview2 (#84) keeps the two promises afterEmbed and
// openWindow rely on. Windows-only code, which no CI here runs, so the copy's
// source is read: a re-vendoring that drops either patch fails here.
//   - Embed's loop posts WM_QUIT again when it takes it, or closing the
//     console while a tool window is built leaves KubeTower running unseen.
//   - A window that failed to embed is destroyed at once, never left blank
//     on screen beside the browser fallback.
func TestTheWebView2CopyKeepsItsPatches(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "third_party", "go-webview2", p))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	chromium := read(filepath.Join("pkg", "edge", "chromium.go"))
	quit := strings.Index(chromium, "if r == 0 {")
	if quit < 0 || !strings.Contains(chromium[quit:quit+300], "User32PostQuitMessage.Call(msg.WParam)") {
		t.Error("Embed's loop no longer posts WM_QUIT again")
	}
	webview := read("webview.go")
	embed := strings.Index(webview, "if !w.browser.Embed(w.hwnd) {")
	if embed < 0 || !strings.Contains(webview[embed:embed+80], "w.discard()") {
		t.Error("a window that failed to embed is not discarded")
	}
	if !strings.Contains(webview, "setWindowContext(w.hwnd, nil)\n\t_, _, _ = w32.User32DestroyWindow.Call(w.hwnd)") {
		t.Error("discard no longer destroys the window at once, without posting WM_QUIT")
	}
}

// An HRESULT is a signed 32-bit value that a callback receives zero-extended
// in a uintptr: `int64(res) < 0` and `int(res) < 0` never see 0x80070005 as a
// failure, and the copy would go on to AddRef a nil environment. And a
// success code with a nil environment, controller or web view (a session with
// no desktop) is a failure, not a nil dereference (#84, as jchv/go-webview2#87
// raises). Windows-only code: its source is read.
func TestTheWebView2CopyReadsFailuresAsFailures(t *testing.T) {
	dir := filepath.Join("..", "..", "third_party", "go-webview2", "pkg", "edge")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := regexp.MustCompile(`\bint(64)?\((res|r|hr)\) < 0`)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if m := unsigned.Find(b); m != nil {
			t.Errorf("%s tests an HRESULT unsigned: %s", e.Name(), m)
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "chromium.go"))
	if err != nil {
		t.Fatal(err)
	}
	chromium := string(b)
	for _, want := range []string{
		"return int32(res) < 0",
		"if env == nil {",
		"if controller == nil {",
		"if hresultFailed(r) || e.webview == nil {",
	} {
		if !strings.Contains(chromium, want) {
			t.Errorf("chromium.go no longer has %q", want)
		}
	}
	if strings.Count(chromium, "if hresultFailed(res) {") != 2 {
		t.Error("the two completion handlers no longer both test their HRESULT signed")
	}
}
