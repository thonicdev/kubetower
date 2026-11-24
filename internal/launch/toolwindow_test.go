package launch

import (
	"errors"
	"slices"
	"sync"
	"testing"

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
}

func (h *fakeHost) openToolWindow(id int, target toolWindowTarget) {
	if h.opened == nil {
		h.opened = map[int]toolWindowTarget{}
	}
	h.opened[id] = target
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
	long := "/" + string(make([]byte, maxToolWindowPath))
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
	// Window 2 did not open f1, so closing the last window on it keeps it.
	tw.closed(2)
	if len(tunnels.closed) != 0 {
		t.Fatalf("closed %v: the last window on f1 did not open it", tunnels.closed)
	}

	if err := tw.OpenToolWindow("f1", "hubble", "/", true); err != nil {
		t.Fatal(err)
	}
	tw.closed(4)
	if !slices.Equal(tunnels.closed, []string{"f1"}) {
		t.Fatalf("closed %v; want f1, opened for the window just closed", tunnels.closed)
	}
	// A second report of the same window, or of one never opened, does nothing.
	tw.closed(4)
	tw.closed(99)
	if len(tunnels.closed) != 1 {
		t.Fatalf("closed %v after repeated reports", tunnels.closed)
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
