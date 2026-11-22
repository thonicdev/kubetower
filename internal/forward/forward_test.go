package forward

// The manager on fakes.
//
// A real tunnel is SPDY, a credential plugin and a listener, and the integration
// test beside this one proves that end of it against a cluster. What a cluster
// will not reproduce on demand is the half that matters here: two clicks landing
// at once, a pod deleted underneath an open forward, the seventeenth tunnel. All
// of those are cheap against a fake and impossible to arrange otherwise.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/identity"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// --- fakes -------------------------------------------------------------------

type fakeLink struct {
	addr string
	pod  string
	port int

	done chan struct{}
	once sync.Once

	mu     sync.Mutex
	err    error
	closes int
}

func newFakeLink(addr, pod string, port int) *fakeLink {
	return &fakeLink{addr: addr, pod: pod, port: port, done: make(chan struct{})}
}

func (l *fakeLink) Addr() string          { return l.addr }
func (l *fakeLink) Pod() string           { return l.pod }
func (l *fakeLink) PodPort() int          { return l.port }
func (l *fakeLink) Done() <-chan struct{} { return l.done }

func (l *fakeLink) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

func (l *fakeLink) Close() {
	l.mu.Lock()
	l.closes++
	l.mu.Unlock()
	l.once.Do(func() { close(l.done) })
}

// die is a tunnel stopping on its own, which is what happens when the pod goes.
func (l *fakeLink) die(err error) {
	l.mu.Lock()
	l.err = err
	l.mu.Unlock()
	l.once.Do(func() { close(l.done) })
}

func (l *fakeLink) closeCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closes
}

type fakeDigger struct {
	mu    sync.Mutex
	calls int
	links []*fakeLink
	// asked records the local port each dig was told to listen on, in order. It
	// is the only way to tell "the manager allocated one" from "the kernel was
	// asked", which is the whole of KT_FORWARD_PORTS' behavior.
	asked []int
	err   error
	delay time.Duration
	// ctxs is the context of each dig, for the test that the request's
	// identity reaches the terminal's forwarder.
	ctxs []context.Context
}

func (d *fakeDigger) dig(ctx context.Context, req Request, localPort int) (link, error) {
	d.mu.Lock()
	d.ctxs = append(d.ctxs, ctx)
	d.calls++
	n := d.calls
	d.asked = append(d.asked, localPort)
	err := d.err
	delay := d.delay
	d.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if err != nil {
		return nil, err
	}

	port := req.Port
	if port == 0 {
		port = 8080
	}
	// A real tunnel listens on the port it was given, so the fake does too. Zero
	// is the kernel choosing, and the ephemeral-looking numbers below are what
	// the tests that predate the range assert on.
	local := localPort
	if local == 0 {
		local = 50000 + n
	}
	l := newFakeLink(fmt.Sprintf("127.0.0.1:%d", local), fmt.Sprintf("%s-pod-%d", req.Name(), n), port)

	d.mu.Lock()
	d.links = append(d.links, l)
	d.mu.Unlock()
	return l, nil
}

func (d *fakeDigger) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *fakeDigger) link(i int) *fakeLink {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.links[i]
}

// portsAsked is what the manager told the digger to listen on, in call order.
func (d *fakeDigger) portsAsked() []int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]int(nil), d.asked...)
}

// testManager builds a manager on a fake digger. New wires the real one from a
// cluster manager; this replaces those two fields and nothing else, so every
// other decision under test is the shipped one.
// The log goes through a gate that closes when the test does, and that is not
// tidiness — it prevents a race the detector reports intermittently.
//
// `Manager.watch` is a goroutine per tunnel that lives until the tunnel dies and
// then writes one line. Nothing joins it, which is right in production, where
// Logf is log.Printf. In a test it means the line can land *after* the test
// function has returned, and `t.Logf` from a finished test is a documented data
// race against the testing package's own state — one that fails
// `go test -race ./internal/forward` on some runs and not others, which is worse
// than failing every time.
func testManager(t *testing.T, d digger) *Manager {
	t.Helper()
	return testManagerOn(t, d, Options{})
}

// testManagerOn is the same on given options — the range, in practice.
//
// KT_FORWARD_PORTS is cleared either way, and that is not tidiness: New falls
// back to the environment, so a developer with a range exported would otherwise
// run a different manager from the one CI runs, and every assertion about a
// local port would depend on their shell.
func testManagerOn(t *testing.T, d digger, opts Options) *Manager {
	t.Helper()
	t.Setenv(EnvPorts, "")

	var (
		mu   sync.Mutex
		over bool
	)
	t.Cleanup(func() {
		mu.Lock()
		over = true
		mu.Unlock()
	})

	opts.Logf = func(format string, v ...any) {
		mu.Lock()
		defer mu.Unlock()
		if over {
			// The tunnel outlived the test. Dropping the line is the only safe
			// answer; the alternative is a panic in an unrelated test.
			return
		}
		t.Logf(format, v...)
	}
	if opts.Access == nil {
		opts.Access = accesstest.Allow()
	}
	m := New(opts)
	m.digger = d
	m.resolve = func(name string) (string, bool) {
		if name == "nowhere" {
			return "", false
		}
		return name, true
	}
	return m
}

func dnsRequest() Request {
	return Request{Cluster: "docker-desktop", Namespace: "kube-system", Service: "kube-dns", Port: 9153}
}

// waitFor polls until the condition holds, because reaping is another goroutine
// and a sleep long enough to be reliable is a sleep long enough to be annoying.
func waitFor(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

// --- opening -----------------------------------------------------------------

func TestOpenReportsWhereToDialAndWhichPodItLandedOn(t *testing.T) {
	m := testManager(t, &fakeDigger{})

	tunnel, err := m.Open(context.Background(), dnsRequest())
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if tunnel.ID == "" {
		t.Error("the tunnel has no id, so nothing can close it")
	}
	if tunnel.LocalAddr != "127.0.0.1:50001" || tunnel.LocalPort != 50001 {
		t.Errorf("local address is %q / %d", tunnel.LocalAddr, tunnel.LocalPort)
	}
	if tunnel.Pod != "kube-dns-pod-1" {
		t.Errorf("pod is %q; a service forward that does not name its replica is not an answer", tunnel.Pod)
	}
	if tunnel.Target != "service/kube-dns" || tunnel.Port != 9153 || tunnel.PodPort != 9153 {
		t.Errorf("target is %q %d→%d", tunnel.Target, tunnel.Port, tunnel.PodPort)
	}
	if !tunnel.Healthy {
		t.Error("a tunnel that just opened is reported as unhealthy")
	}
}

// The point of the whole feature: clicking twice must not leave the operator
// with two ports and no way to tell which is theirs.
func TestOpeningTheSameTargetTwiceReturnsTheSameTunnel(t *testing.T) {
	d := &fakeDigger{}
	m := testManager(t, d)

	first, err := m.Open(context.Background(), dnsRequest())
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Open(context.Background(), dnsRequest())
	if err != nil {
		t.Fatal(err)
	}

	if first.ID != second.ID || first.LocalAddr != second.LocalAddr {
		t.Errorf("two clicks produced %s on %s and %s on %s",
			first.ID, first.LocalAddr, second.ID, second.LocalAddr)
	}
	if d.count() != 1 {
		t.Errorf("dug %d tunnels for two identical clicks", d.count())
	}
	if got := len(m.List()); got != 1 {
		t.Errorf("%d tunnels are open after two identical clicks", got)
	}
}

// The URL-safe form of a context name and the real one are the same cluster, so
// they must land on the same tunnel. This is where the EKS ARNs get in.
func TestIdempotenceUsesTheResolvedClusterName(t *testing.T) {
	d := &fakeDigger{}
	m := testManager(t, d)
	m.resolve = func(name string) (string, bool) {
		if name == "arn--aws--eks..cluster--prod" || name == "arn:aws:eks:…:cluster/prod" {
			return "arn:aws:eks:…:cluster/prod", true
		}
		return "", false
	}

	req := Request{Cluster: "arn:aws:eks:…:cluster/prod", Namespace: "monitoring", Service: "prometheus", Port: 9090}
	first, err := m.Open(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.Cluster = "arn--aws--eks..cluster--prod"
	second, err := m.Open(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Errorf("the same cluster spelled two ways opened two tunnels: %s and %s", first.ID, second.ID)
	}
	if first.Cluster != "arn:aws:eks:…:cluster/prod" {
		t.Errorf("the tunnel reports the cluster as %q rather than the kubeconfig's own name", first.Cluster)
	}
}

func TestADifferentPortOnTheSameServiceIsADifferentTunnel(t *testing.T) {
	d := &fakeDigger{}
	m := testManager(t, d)

	first, err := m.Open(context.Background(), dnsRequest())
	if err != nil {
		t.Fatal(err)
	}
	other := dnsRequest()
	other.Port = 53
	second, err := m.Open(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("two ports of one service collapsed into one tunnel")
	}
	if got := len(m.List()); got != 2 {
		t.Errorf("%d tunnels open, wanted 2", got)
	}
}

// Two clicks arriving together must not both dig and both survive: the open is
// deliberately not holding the lock across the network call, so the collision is
// resolved afterwards.
func TestConcurrentIdenticalOpensLeaveOneTunnel(t *testing.T) {
	d := &fakeDigger{delay: 20 * time.Millisecond}
	m := testManager(t, d)

	const clicks = 8
	var wg sync.WaitGroup
	ids := make([]string, clicks)
	for i := range clicks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tunnel, err := m.Open(context.Background(), dnsRequest())
			if err != nil {
				t.Errorf("open %d: %v", i, err)
				return
			}
			ids[i] = tunnel.ID
		}()
	}
	wg.Wait()

	if got := len(m.List()); got != 1 {
		t.Fatalf("%d tunnels open after %d simultaneous identical clicks", got, clicks)
	}
	for i, id := range ids {
		if id != ids[0] {
			t.Fatalf("click %d got tunnel %s, click 0 got %s", i, id, ids[0])
		}
	}
	// Everything dug and then discarded must have been closed, or the process
	// keeps a listener nobody knows about.
	open := 0
	for i := range d.count() {
		if d.link(i).closeCount() == 0 {
			open++
		}
	}
	if open != 1 {
		t.Errorf("%d of %d dug tunnels are still open; the losers of the race leaked", open, d.count())
	}
}

// --- the bound ---------------------------------------------------------------

func TestTheNumberOfOpenTunnelsIsBounded(t *testing.T) {
	d := &fakeDigger{}
	m := testManager(t, d)
	m.limit = 3

	for i := range 3 {
		req := dnsRequest()
		req.Port = 9000 + i
		if _, err := m.Open(context.Background(), req); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
	}

	req := dnsRequest()
	req.Port = 9999
	_, err := m.Open(context.Background(), req)
	if err == nil {
		t.Fatal("the fourth tunnel opened past a limit of three")
	}
	if got := StatusFor(err); got != 409 {
		t.Errorf("the limit answers %d rather than 409", got)
	}
	if got := len(m.List()); got != 3 {
		t.Errorf("%d tunnels open after a refused fourth", got)
	}
	if d.count() != 3 {
		t.Errorf("the refused open still dug: %d digs", d.count())
	}
}

// The limit has to count the tunnels being dug as well, or a burst of clicks all
// passes a check that sees an empty map.
func TestTunnelsBeingDugCountTowardsTheLimit(t *testing.T) {
	d := &fakeDigger{delay: 30 * time.Millisecond}
	m := testManager(t, d)
	m.limit = 2

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := dnsRequest()
			req.Port = 9000 + i
			_, _ = m.Open(context.Background(), req)
		}()
	}
	wg.Wait()

	if got := len(m.List()); got > 2 {
		t.Errorf("%d tunnels open past a limit of two", got)
	}
	if d.count() > 2 {
		t.Errorf("%d tunnels were dug past a limit of two", d.count())
	}
}

// --- closing -----------------------------------------------------------------

func TestCloseRemovesTheTunnelAndTearsItDown(t *testing.T) {
	d := &fakeDigger{}
	m := testManager(t, d)

	tunnel, err := m.Open(context.Background(), dnsRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !m.Close(tunnel.ID) {
		t.Fatal("closing an open tunnel reported nothing to close")
	}
	if d.link(0).closeCount() == 0 {
		t.Error("the tunnel was forgotten but never torn down")
	}
	if got := len(m.List()); got != 0 {
		t.Errorf("%d tunnels still listed after the only one was closed", got)
	}
	if m.Close(tunnel.ID) {
		t.Error("closing the same tunnel twice reported a second close")
	}
}

func TestShutdownClosesEverythingAndRefusesMore(t *testing.T) {
	d := &fakeDigger{}
	m := testManager(t, d)

	for i := range 3 {
		req := dnsRequest()
		req.Port = 9000 + i
		if _, err := m.Open(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	m.Shutdown()
	m.Shutdown() // idempotent, and a second shutdown must not panic on an empty map

	if got := len(m.List()); got != 0 {
		t.Errorf("%d tunnels survived the shutdown", got)
	}
	for i := range 3 {
		if d.link(i).closeCount() == 0 {
			t.Errorf("tunnel %d was dropped from the list but never torn down", i)
		}
	}
	if _, err := m.Open(context.Background(), dnsRequest()); err == nil {
		t.Error("a tunnel opened into a manager that has shut down")
	}
}

// Start's context ending is the process ending, and everything must go with it.
func TestStartClosesEverythingWhenTheContextEnds(t *testing.T) {
	d := &fakeDigger{}
	m := testManager(t, d)

	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)

	if _, err := m.Open(context.Background(), dnsRequest()); err != nil {
		t.Fatal(err)
	}
	cancel()
	waitFor(t, "the tunnels to be closed on shutdown", func() bool { return len(m.List()) == 0 })
	if d.link(0).closeCount() == 0 {
		t.Error("shutdown forgot the tunnel without tearing it down")
	}
}

// --- death and reaping -------------------------------------------------------

// A tunnel whose pod has gone must leave the list. The listener is still bound
// at that point, so a connection to it is accepted and then reset — leaving the
// row on screen tells the operator their workload is broken.
func TestATunnelThatDiesIsReapedWithoutAnySweep(t *testing.T) {
	d := &fakeDigger{}
	m := testManager(t, d)

	tunnel, err := m.Open(context.Background(), dnsRequest())
	if err != nil {
		t.Fatal(err)
	}
	d.link(0).die(errors.New("lost connection to pod"))

	waitFor(t, "the dead tunnel to be reaped", func() bool { return len(m.List()) == 0 })
	if m.Close(tunnel.ID) {
		t.Error("the reaped tunnel is still closeable, so it is still in the map")
	}
}

// Between death and reaping there is a window. It must not describe a dead
// listener as open.
func TestADeadTunnelIsListedAsUnhealthyUntilItIsReaped(t *testing.T) {
	d := &fakeDigger{}
	m := testManager(t, d)

	// A manager with no watcher: build the entry through the ordinary path and
	// then stop the reaping from happening, so the window can be inspected.
	tunnel, err := m.Open(context.Background(), dnsRequest())
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	e := m.open[tunnel.ID]
	m.mu.Unlock()

	e.link.(*fakeLink).mu.Lock()
	e.link.(*fakeLink).err = errors.New("lost connection to pod 7 -> 9153")
	e.link.(*fakeLink).mu.Unlock()

	m.mu.Lock()
	view := m.describe(e)
	m.mu.Unlock()
	if !view.Healthy {
		t.Fatal("a live tunnel is described as unhealthy")
	}

	e.link.(*fakeLink).die(errors.New("lost connection to pod 7 -> 9153"))
	m.mu.Lock()
	view = m.describe(e)
	m.mu.Unlock()
	if view.Healthy {
		t.Error("a stopped tunnel is described as healthy")
	}
	if view.Error == "" {
		t.Error("a stopped tunnel is described with no reason")
	}
}

// Clicking again on a forward whose pod went away must dig a fresh one rather
// than hand back the corpse.
func TestOpeningAgainAfterADeathDoesNotReturnTheDeadTunnel(t *testing.T) {
	d := &fakeDigger{}
	m := testManager(t, d)

	first, err := m.Open(context.Background(), dnsRequest())
	if err != nil {
		t.Fatal(err)
	}
	// Kill it without letting the watcher run first: take the entry out of the
	// manager's reach is not possible, so instead assert on the id rather than
	// on the count.
	d.link(0).die(errors.New("lost connection to pod"))

	second, err := m.Open(context.Background(), dnsRequest())
	if err != nil {
		t.Fatalf("reopening after a death: %v", err)
	}
	if second.ID == first.ID {
		t.Error("the second open handed back the dead tunnel")
	}
	if !second.Healthy {
		t.Error("the reopened tunnel is not healthy")
	}
}

// The second opinion: a pod that has gone while the stream to it has not yet
// noticed.
func TestTheSweepReapsATunnelWhosePodHasGone(t *testing.T) {
	d := &fakeDigger{}
	m := testManager(t, d)

	var asked int
	m.gone = func(_ context.Context, cluster, namespace, pod string) bool {
		asked++
		if cluster != "docker-desktop" || namespace != "kube-system" || pod != "kube-dns-pod-1" {
			t.Errorf("the sweep asked about %s %s/%s", cluster, namespace, pod)
		}
		return true
	}

	if _, err := m.Open(context.Background(), dnsRequest()); err != nil {
		t.Fatal(err)
	}
	m.sweep(context.Background())

	if asked == 0 {
		t.Fatal("the sweep never asked whether the pod was there")
	}
	if got := len(m.List()); got != 0 {
		t.Errorf("%d tunnels survived a sweep that found the pod gone", got)
	}
	if d.link(0).closeCount() == 0 {
		t.Error("the reaped tunnel was never torn down")
	}
}

// An unreachable API server says nothing about whether a pod exists. A lapsed
// SSO session must not tear down every forward the operator has open.
func TestTheSweepKeepsTunnelsWhenTheClusterCannotBeAsked(t *testing.T) {
	m := testManager(t, &fakeDigger{})
	m.gone = func(context.Context, string, string, string) bool { return false }

	if _, err := m.Open(context.Background(), dnsRequest()); err != nil {
		t.Fatal(err)
	}
	m.sweep(context.Background())

	if got := len(m.List()); got != 1 {
		t.Errorf("%d tunnels after a sweep that could not answer; the forward was thrown away", got)
	}
}

// --- what the request has to say ---------------------------------------------

func TestOpenValidatesTheRequest(t *testing.T) {
	m := testManager(t, &fakeDigger{})

	cases := []struct {
		name string
		req  Request
		want int
	}{
		{"no cluster", Request{Namespace: "kube-system", Service: "kube-dns", Port: 9153}, 400},
		{"no namespace", Request{Cluster: "docker-desktop", Service: "kube-dns", Port: 9153}, 400},
		{"neither", Request{Cluster: "docker-desktop", Namespace: "kube-system", Port: 9153}, 400},
		{"both", Request{Cluster: "docker-desktop", Namespace: "kube-system", Service: "kube-dns", Pod: "coredns", Port: 9153}, 400},
		{"a pod with no port", Request{Cluster: "docker-desktop", Namespace: "kube-system", Pod: "coredns"}, 400},
		{"not a port", Request{Cluster: "docker-desktop", Namespace: "kube-system", Service: "kube-dns", Port: 70000}, 400},
		{"unknown cluster", Request{Cluster: "nowhere", Namespace: "kube-system", Service: "kube-dns", Port: 9153}, 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := m.Open(context.Background(), tc.req)
			if err == nil {
				t.Fatal("accepted")
			}
			if got := StatusFor(err); got != tc.want {
				t.Errorf("%v answers %d, wanted %d", err, got, tc.want)
			}
		})
	}
}

func TestAFailedDigIsNotRemembered(t *testing.T) {
	d := &fakeDigger{err: errors.New("no running pod behind service kube-system/kube-dns")}
	m := testManager(t, d)

	if _, err := m.Open(context.Background(), dnsRequest()); err == nil {
		t.Fatal("a failed dig reported success")
	}
	if got := len(m.List()); got != 0 {
		t.Errorf("%d tunnels listed after a dig that failed", got)
	}
	// And the slot it reserved has to come back, or a run of failures fills the
	// limit with tunnels that do not exist.
	d.mu.Lock()
	d.err = nil
	d.mu.Unlock()
	if _, err := m.Open(context.Background(), dnsRequest()); err != nil {
		t.Fatalf("opening after a failure: %v", err)
	}
}

// --- read-only clusters ------------------------------------------------------

// The decision is to refuse it, as the
// read-only terminal refuses `kubectl port-forward`. If that is ever reversed,
// this test is the one that has to change, and it names the reasoning's location.
//
// Refused before anything is dug and before a slot or a port is taken: the
// digger is never called, nothing is listed, and the refusal is the 403 the
// handlers give, with a sentence naming the marking.
func TestAReadOnlyClusterIsRefusedAForward(t *testing.T) {
	d := &fakeDigger{}
	m := testManager(t, d)
	m.readOnly = func(name string) bool { return name == "docker-desktop" }

	tunnel, err := m.Open(context.Background(), dnsRequest())
	if err == nil {
		t.Fatalf("a read-only cluster was forwarded to (%+v); see refuseOnReadOnlyClusters", tunnel)
	}
	for _, want := range []string{"docker-desktop", "marked read-only", "KT_READONLY_CLUSTERS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not say %q", err, want)
		}
	}
	var refusal *ReadOnlyRefusal
	if !errors.As(err, &refusal) || refusal.Cluster != "docker-desktop" {
		t.Errorf("the refusal %v is not a ReadOnlyRefusal naming the cluster", err)
	}
	if got := StatusFor(err); got != http.StatusForbidden {
		t.Errorf("the refusal is answered %d, want 403", got)
	}
	d.mu.Lock()
	digs := d.calls
	d.mu.Unlock()
	if digs != 0 {
		t.Errorf("the refusal came after %d digs", digs)
	}
	if got := len(m.List()); got != 0 {
		t.Errorf("%d tunnels listed after a refusal", got)
	}
}

// And the other cluster of the same manager still forwards: the refusal is the
// marking's, not the manager's.
func TestAnUnmarkedClusterBesideAMarkedOneIsForwardedTo(t *testing.T) {
	m := testManager(t, &fakeDigger{})
	m.readOnly = func(name string) bool { return name == "some-other-cluster" }

	if _, err := m.Open(context.Background(), dnsRequest()); err != nil {
		t.Fatalf("an unmarked cluster was refused a forward: %v", err)
	}
}

func TestATunnelToAnOrdinaryClusterIsNotMarkedReadOnly(t *testing.T) {
	m := testManager(t, &fakeDigger{})
	tunnel, err := m.Open(context.Background(), dnsRequest())
	if err != nil {
		t.Fatal(err)
	}
	if tunnel.ReadOnly {
		t.Error("an unmarked cluster produced a tunnel marked read-only")
	}
}

// --- the list ----------------------------------------------------------------

func TestListIsOldestFirst(t *testing.T) {
	m := testManager(t, &fakeDigger{})
	base := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	var tick int
	m.now = func() time.Time {
		tick++
		return base.Add(time.Duration(tick) * time.Minute)
	}

	for i := range 3 {
		req := dnsRequest()
		req.Port = 9000 + i
		if _, err := m.Open(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}

	list := m.List()
	if len(list) != 3 {
		t.Fatalf("%d tunnels listed", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i].Opened.Before(list[i-1].Opened) {
			t.Errorf("tunnel %d was opened before tunnel %d but sorts after it", i, i-1)
		}
	}
}

func TestAgeIsSaidTheWayATableWantsIt(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{300 * time.Millisecond, "0s"},
		{42 * time.Second, "42s"},
		{5 * time.Minute, "5m"},
		{90 * time.Minute, "1h30m"},
		{50 * time.Hour, "2d2h"},
	}
	for _, tc := range cases {
		if got := age(tc.d); got != tc.want {
			t.Errorf("age(%s) = %q, wanted %q", tc.d, got, tc.want)
		}
	}
}

func TestPortOfReadsTheListeningPortBack(t *testing.T) {
	if got := portOf("127.0.0.1:51423"); got != 51423 {
		t.Errorf("portOf gave %d", got)
	}
	if got := portOf("nonsense"); got != 0 {
		t.Errorf("portOf on a malformed address gave %d rather than 0", got)
	}
}

func TestLimitIsReported(t *testing.T) {
	m := testManager(t, &fakeDigger{})
	if m.Limit() != maxOpen {
		t.Errorf("the default limit reports as %d rather than %d", m.Limit(), maxOpen)
	}
}

// TestADugTunnelCarriesWhoAskedForIt: the open is bounded by its own timeout
// and detached from the request's cancellation, and must still carry the
// request's values - it is where internal/terminal reads who to name.
func TestADugTunnelCarriesWhoAskedForIt(t *testing.T) {
	d := &fakeDigger{}
	m := testManager(t, d)
	var lines []string
	var mu sync.Mutex
	inner := m.logf
	m.logf = func(format string, v ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, v...))
		mu.Unlock()
		inner(format, v...)
	}
	ctx := identity.With(context.Background(), identity.Who{Name: "bob@example.com", Method: identity.SSO})
	if _, err := m.Open(ctx, dnsRequest()); err != nil {
		t.Fatalf("opening: %v", err)
	}
	mu.Lock()
	opened := false
	for _, l := range lines {
		if strings.Contains(l, " open: ") {
			opened = strings.HasSuffix(l, `who="bob@example.com" via=sso`)
		}
	}
	mu.Unlock()
	if !opened {
		t.Errorf("the tunnel's open line does not name who asked for it: %q", lines)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.ctxs) != 1 {
		t.Fatalf("dug %d times", len(d.ctxs))
	}
	if who, ok := identity.From(d.ctxs[0]); !ok || who.Name != "bob@example.com" {
		t.Errorf("the dig's context names %+v; the tunnel's line would name nobody", who)
	}
}
