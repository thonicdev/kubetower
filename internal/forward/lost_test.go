package forward

// `Options.Lost`: which endings of a tunnel are a "lost" forward, for the macOS
// window's notifications. The rule is in `lose`: whoever
// takes the entry out of the map decides, under the one lock.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// lostRecorder is an `Options.Lost` that keeps what it was told.
type lostRecorder struct {
	mu   sync.Mutex
	seen []Tunnel
}

func (r *lostRecorder) lost(t Tunnel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, t)
}

func (r *lostRecorder) all() []Tunnel {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Tunnel(nil), r.seen...)
}

func lostManager(t *testing.T, d digger) (*Manager, *lostRecorder) {
	t.Helper()
	r := &lostRecorder{}
	return testManagerOn(t, d, Options{Lost: r.lost}), r
}

// settle gives a watcher that should stay silent the time to say something if
// it were going to. A negative assertion can only be bounded by time; a
// watcher that spoke late would make this pass wrongly, never fail wrongly.
func settle() { time.Sleep(50 * time.Millisecond) }

func TestATunnelThatDiesOnItsOwnIsLostOnce(t *testing.T) {
	d := &fakeDigger{}
	m, r := lostManager(t, d)

	opened, err := m.Open(context.Background(), dnsRequest())
	if err != nil {
		t.Fatal(err)
	}
	d.link(0).die(errors.New("lost connection to pod"))

	waitFor(t, "the lost tunnel to be reported", func() bool { return len(r.all()) == 1 })
	settle()
	got := r.all()
	if len(got) != 1 {
		t.Fatalf("a tunnel that died once was reported lost %d times", len(got))
	}
	if got[0].ID != opened.ID || got[0].Cluster != "docker-desktop" || got[0].Namespace != "kube-system" ||
		got[0].Kind != "service" || got[0].Name != "kube-dns" || got[0].Port != 9153 {
		t.Errorf("the lost tunnel is reported as %+v, not as the one that was opened (%+v)", got[0], opened)
	}
}

func TestATunnelTheOperatorClosedIsNeverLost(t *testing.T) {
	d := &fakeDigger{}
	m, r := lostManager(t, d)

	opened, err := m.Open(context.Background(), dnsRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !m.Close(opened.ID) {
		t.Fatal("the tunnel was not there to close")
	}
	// The link the operator closed reports an error as well, as a real stream
	// torn down from this side may: it is still the operator's close.
	d.link(0).die(errors.New("use of closed network connection"))
	settle()
	if got := r.all(); len(got) != 0 {
		t.Errorf("a tunnel the operator closed was reported lost: %+v", got)
	}
}

func TestTunnelsClosedOnShutdownAreNeverLost(t *testing.T) {
	d := &fakeDigger{}
	m, r := lostManager(t, d)

	for i := range 3 {
		req := dnsRequest()
		req.Port = 9000 + i
		if _, err := m.Open(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	m.Shutdown()
	settle()
	if got := r.all(); len(got) != 0 {
		t.Errorf("%d tunnels closed by the shutdown were reported lost", len(got))
	}
}

func TestATunnelTheSweepReapsIsLostOnce(t *testing.T) {
	d := &fakeDigger{}
	m, r := lostManager(t, d)
	m.gone = func(context.Context, string, string, string) bool { return true }

	if _, err := m.Open(context.Background(), dnsRequest()); err != nil {
		t.Fatal(err)
	}
	m.sweep(context.Background())
	// The sweep closes the link, which wakes `watch` too: it must find the
	// entry gone and stay silent, so the tunnel is lost once and not twice.
	settle()
	if got := r.all(); len(got) != 1 {
		t.Errorf("a tunnel whose pod the sweep found gone was reported lost %d times, want 1", len(got))
	}
}

// A refused or raced dig never reached the map, so nothing watches it and
// nothing can call it lost.
func TestATunnelThatNeverOpenedIsNeverLost(t *testing.T) {
	d := &fakeDigger{err: errors.New("no ready pod")}
	m, r := lostManager(t, d)

	if _, err := m.Open(context.Background(), dnsRequest()); err == nil {
		t.Fatal("the dig failed and the open did not")
	}
	settle()
	if got := r.all(); len(got) != 0 {
		t.Errorf("a tunnel that never opened was reported lost: %+v", got)
	}
}
