package forward

// The published range.
//
// Two halves, and the second is the one that would be skipped: the allocator on
// its own, and the allocator *through Open*. A predicate tested alone and never
// at its call site misses wrong wiring — `forwarderDigger.dig` passing
// LocalPort 0 would pass every test of a port allocator, because nothing is
// wrong with the allocator. Every test below that ends in an assertion about a
// Tunnel drives the real path.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// setProbe swaps the "can this port be bound" question, under the lock so the
// race detector has nothing to say about a manager already running.
func (p *portRange) setProbe(probe func(host string, port int) bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.probe = probe
}

func alwaysFree(string, int) bool { return true }

// --- parsing -----------------------------------------------------------------

func TestParsePortsReadsARangeAPortOrNothing(t *testing.T) {
	for name, c := range map[string]struct {
		raw        string
		low, high  int
		wantErr    bool
		errMention string
	}{
		"a range":                {raw: "30000-30031", low: 30000, high: 30031},
		"one port":               {raw: "30000", low: 30000, high: 30000},
		"spaces around it":       {raw: "  30000 - 30031  ", low: 30000, high: 30031},
		"nothing at all":         {raw: "", low: 0, high: 0},
		"nothing but whitespace": {raw: "   ", low: 0, high: 0},

		// Reversed is refused and never swapped: compose.dev.yaml gets the
		// same string and does not reorder it, so a range KubeTower read one way
		// and Docker read the other is the drift one variable exists to prevent.
		"the bounds the wrong way round": {raw: "30031-30000", wantErr: true, errMention: "wrong way round"},
		"junk":                           {raw: "thirty thousand", wantErr: true, errMention: "not a port number"},
		"half a range":                   {raw: "30000-", wantErr: true, errMention: "wants a port"},
		"the other half":                 {raw: "-30031", wantErr: true, errMention: "wants a port"},
		"three bounds":                   {raw: "30000-30031-30040", wantErr: true, errMention: "not a port number"},
		"past the top":                   {raw: "70000", wantErr: true, errMention: "highest port"},
		"a privileged port":              {raw: "80-90", wantErr: true, errMention: "privileged"},
		"zero":                           {raw: "0", wantErr: true, errMention: "privileged"},
	} {
		t.Run(name, func(t *testing.T) {
			low, high, err := parsePorts(c.raw)
			switch {
			case c.wantErr && err == nil:
				t.Fatalf("parsePorts(%q) = %d-%d, want an error", c.raw, low, high)
			case c.wantErr:
				if !strings.Contains(err.Error(), c.errMention) {
					t.Errorf("parsePorts(%q) said %q, which does not mention %q", c.raw, err, c.errMention)
				}
				if !strings.Contains(err.Error(), EnvPorts) {
					t.Errorf("parsePorts(%q) said %q, which does not name the variable to fix", c.raw, err)
				}
			case err != nil:
				t.Fatalf("parsePorts(%q): %v", c.raw, err)
			case low != c.low || high != c.high:
				t.Errorf("parsePorts(%q) = %d-%d, want %d-%d", c.raw, low, high, c.low, c.high)
			}
		})
	}
}

// --- the allocator -----------------------------------------------------------

func TestTakeHandsOutEveryPortInTheRangeOnce(t *testing.T) {
	p := newPortRange(30000, 30003, "127.0.0.1")
	p.setProbe(alwaysFree)

	seen := map[int]bool{}
	for i := 0; i < 4; i++ {
		port, err := p.take()
		if err != nil {
			t.Fatalf("take %d of 4: %v", i+1, err)
		}
		if port < 30000 || port > 30003 {
			t.Fatalf("take gave %d, which is outside 30000-30003", port)
		}
		if seen[port] {
			t.Fatalf("take gave %d twice; two tunnels would collide on it", port)
		}
		seen[port] = true
	}
	if p.held() != 4 {
		t.Errorf("%d ports are recorded as held, want 4", p.held())
	}
}

// The tracking is the safety property, not the round-robin.
//
// Walking the range from where it left off is there so a closed forward's number
// is not immediately handed to the next one — a browser tab left open on it
// would reconnect to a different workload. It is *not* what stops two live
// tunnels sharing a port: rewind the search and only the taken set answers.
// Remove that set and this test is the one that fails.
func TestAPortAlreadyOutIsNotHandedOutAgain(t *testing.T) {
	p := newPortRange(30000, 30001, "127.0.0.1")
	p.setProbe(alwaysFree)

	first, err := p.take()
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.next = 0 // as though the search started from the bottom again
	p.mu.Unlock()

	second, err := p.take()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("both takes gave %d while the first was still held", first)
	}
}

func TestAFullRangeIsARefusalWithASentence(t *testing.T) {
	p := newPortRange(30000, 30001, "127.0.0.1")
	p.setProbe(alwaysFree)

	for i := 0; i < 2; i++ {
		if _, err := p.take(); err != nil {
			t.Fatalf("take %d: %v", i+1, err)
		}
	}
	port, err := p.take()
	if err == nil {
		t.Fatalf("a full range gave out %d; falling back to a port outside the range is the defect this exists to fix", port)
	}
	if port != 0 {
		t.Errorf("a refused take also returned port %d", port)
	}
	for _, want := range []string{"30000-30001", EnvPorts, "close one"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not mention %q", err, want)
		}
	}
}

func TestReleasePutsAPortBackInTheRange(t *testing.T) {
	p := newPortRange(30000, 30000, "127.0.0.1")
	p.setProbe(alwaysFree)

	first, err := p.take()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.take(); err == nil {
		t.Fatal("a one-port range gave out two ports")
	}

	p.release(first)
	if p.held() != 0 {
		t.Fatalf("%d ports still recorded as held after releasing the only one", p.held())
	}
	again, err := p.take()
	if err != nil {
		t.Fatalf("taking again after a release: %v", err)
	}
	if again != first {
		t.Errorf("the released port was %d and the next take gave %d", first, again)
	}
}

// Something else in the container can be sitting on a port inside the range —
// a kubectl port-forward the operator ran in the terminal, most likely. The
// tracking cannot know about it, which is why the probe is there as well.
func TestTakeSkipsAPortSomethingElseHolds(t *testing.T) {
	p := newPortRange(30000, 30001, "127.0.0.1")
	p.setProbe(func(_ string, port int) bool { return port != 30000 })

	port, err := p.take()
	if err != nil {
		t.Fatal(err)
	}
	if port != 30001 {
		t.Errorf("take gave %d, want 30001 — 30000 is held by something outside this process", port)
	}
}

// No range at all: a nil *portRange is the host deployment, and it answers 0,
// which is what the forwarder reads as "ask the kernel".
func TestNoRangeMeansTheKernelChooses(t *testing.T) {
	var p *portRange

	port, err := p.take()
	if err != nil {
		t.Fatalf("take on no range: %v", err)
	}
	if port != 0 {
		t.Errorf("take on no range gave %d, want 0", port)
	}
	p.release(0)
	if p.size() != 0 || p.held() != 0 {
		t.Errorf("no range reports size %d and %d held", p.size(), p.held())
	}
}

// canBind is the probe itself, and it has one job: say no when the port is
// taken. 127.0.0.1 here rather than 0.0.0.0 so the test binds nothing wider
// than the loopback.
func TestCanBindSeesAPortInUse(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on the loopback at all: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	if canBind("127.0.0.1", port) {
		t.Errorf("canBind says %d is free while this test is listening on it", port)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if !canBind("127.0.0.1", port) {
		t.Errorf("canBind says %d is taken after the listener closed", port)
	}
}

// --- through Open ------------------------------------------------------------

// rangeManager is a manager on a range, with the probe faked: a unit test must
// not depend on whether ports 30000-30003 happen to be free on the machine
// running it, and must not bind 0.0.0.0 to find out.
func rangeManager(t *testing.T, d digger, ports string) *Manager {
	t.Helper()
	m := testManagerOn(t, d, Options{Ports: ports})
	if m.ports == nil {
		t.Fatalf("Options.Ports = %q produced no range", ports)
	}
	m.ports.setProbe(alwaysFree)
	return m
}

func TestOpenTakesItsLocalPortFromTheRange(t *testing.T) {
	d := &fakeDigger{}
	m := rangeManager(t, d, "30000-30003")

	tunnel, err := m.Open(context.Background(), dnsRequest())
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if tunnel.LocalPort < 30000 || tunnel.LocalPort > 30003 {
		t.Errorf("the tunnel is on %d, which is outside the published range and cannot be dialled from the host", tunnel.LocalPort)
	}
	if want := fmt.Sprintf("127.0.0.1:%d", tunnel.LocalPort); tunnel.LocalAddr != want {
		t.Errorf("the address is %q, want %q", tunnel.LocalAddr, want)
	}
	// And the digger was told that number, rather than the manager recording
	// one and the tunnel listening somewhere else.
	if asked := d.portsAsked(); len(asked) != 1 || asked[0] != tunnel.LocalPort {
		t.Errorf("the digger was asked for %v and the tunnel reports %d", asked, tunnel.LocalPort)
	}
}

// The no-range path, unchanged: port 0, and the kernel answers. This is what
// `go run ./cmd/kubetower` on the operator's own machine does, and 127.0.0.1
// there is the same loopback the browser dials.
func TestWithNoRangeOpenStillAsksForPortZero(t *testing.T) {
	d := &fakeDigger{}
	m := testManager(t, d)

	tunnel, err := m.Open(context.Background(), dnsRequest())
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if asked := d.portsAsked(); len(asked) != 1 || asked[0] != 0 {
		t.Errorf("the digger was asked for %v, want [0] — the kernel chooses when no range is configured", asked)
	}
	if tunnel.LocalPort != 50001 {
		t.Errorf("the tunnel is on %d, so something other than the kernel picked it", tunnel.LocalPort)
	}
	if m.ports != nil {
		t.Error("a manager with no range built one anyway")
	}
}

// Forty clicks in two seconds, in the shape that finds a probe-only allocator:
// two opens on different targets at the same instant must not land on one port.
func TestTwoConcurrentOpensGetTwoDifferentPorts(t *testing.T) {
	d := &fakeDigger{}
	m := rangeManager(t, d, "30000-30003")

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		tunnels []Tunnel
		fails   []error
	)
	for _, service := range []string{"kube-dns", "metrics-server"} {
		wg.Add(1)
		go func(service string) {
			defer wg.Done()
			tunnel, err := m.Open(context.Background(), Request{
				Cluster: "docker-desktop", Namespace: "kube-system", Service: service, Port: 9153,
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fails = append(fails, err)
				return
			}
			tunnels = append(tunnels, tunnel)
		}(service)
	}
	wg.Wait()

	if len(fails) > 0 {
		t.Fatalf("two concurrent opens: %v", fails)
	}
	if len(tunnels) != 2 {
		t.Fatalf("got %d tunnels", len(tunnels))
	}
	if tunnels[0].LocalPort == tunnels[1].LocalPort {
		t.Errorf("both tunnels landed on %d; one of them is not reachable", tunnels[0].LocalPort)
	}
	for _, tunnel := range tunnels {
		if tunnel.LocalPort < 30000 || tunnel.LocalPort > 30003 {
			t.Errorf("%s is on %d, outside the range", tunnel.Target, tunnel.LocalPort)
		}
	}
	if held := m.ports.held(); held != 2 {
		t.Errorf("%d ports are held for two open tunnels", held)
	}
}

// Closing a forward has to give its port back, or a range of thirty-two is
// exhausted in an afternoon. The range here is one port wide, so the second
// open can only succeed if the first one's port came back.
func TestClosingATunnelGivesItsPortBack(t *testing.T) {
	d := &fakeDigger{}
	m := rangeManager(t, d, "30000")

	first, err := m.Open(context.Background(), dnsRequest())
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if !m.Close(first.ID) {
		t.Fatal("Close said there was no such tunnel")
	}
	waitFor(t, "the port to come back", func() bool { return m.ports.held() == 0 })

	second, err := m.Open(context.Background(), dnsRequest())
	if err != nil {
		t.Fatalf("opening again after a close: %v", err)
	}
	if second.LocalPort != first.LocalPort {
		t.Errorf("the second tunnel is on %d and the range only holds %d", second.LocalPort, first.LocalPort)
	}
}

// The same for a tunnel nobody closed: the pod went, the stream broke, the
// reaper took it out of the list. The port is as free as the tunnel is dead.
func TestAReapedTunnelGivesItsPortBack(t *testing.T) {
	d := &fakeDigger{}
	m := rangeManager(t, d, "30000")

	if _, err := m.Open(context.Background(), dnsRequest()); err != nil {
		t.Fatalf("opening: %v", err)
	}
	d.link(0).die(errors.New("lost connection to pod"))

	waitFor(t, "the reaped tunnel's port to come back", func() bool {
		return len(m.List()) == 0 && m.ports.held() == 0
	})
	if _, err := m.Open(context.Background(), dnsRequest()); err != nil {
		t.Fatalf("opening after a reaping: %v", err)
	}
}

// A dig that failed never had a listener, so its port must not stay out.
func TestAFailedDigGivesItsPortBack(t *testing.T) {
	d := &fakeDigger{err: errors.New("no running pod for service/kube-dns")}
	m := rangeManager(t, d, "30000")

	if _, err := m.Open(context.Background(), dnsRequest()); err == nil {
		t.Fatal("a failing dig produced a tunnel")
	}
	if held := m.ports.held(); held != 0 {
		t.Fatalf("%d port(s) still held after a dig that failed", held)
	}
	d.mu.Lock()
	d.err = nil
	d.mu.Unlock()
	if _, err := m.Open(context.Background(), dnsRequest()); err != nil {
		t.Fatalf("opening after a failure: %v", err)
	}
}

// A range with nothing free is a refusal, and specifically not a tunnel on port
// zero: that is the original defect, and it would come back invisibly — the open
// succeeds, the link is drawn, and only dialling it shows anything wrong.
func TestOpenIsRefusedWhenTheRangeIsFull(t *testing.T) {
	d := &fakeDigger{}
	m := rangeManager(t, d, "30000-30001")

	first, err := m.Open(context.Background(), dnsRequest())
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	// Everything else in the range is now held by something outside this
	// process, which is the only way a range can be full while the live set is
	// not: the limit is lowered to the size of the range.
	m.ports.setProbe(func(_ string, port int) bool { return port == first.LocalPort })

	tunnel, err := m.Open(context.Background(), Request{
		Cluster: "docker-desktop", Namespace: "kube-system", Service: "metrics-server", Port: 8443,
	})
	if err == nil {
		t.Fatalf("a full range produced a tunnel on %d", tunnel.LocalPort)
	}
	if !strings.Contains(err.Error(), "no local port is free") {
		t.Errorf("the refusal is %q, which does not say the range is full", err)
	}
	if got := d.count(); got != 1 {
		t.Errorf("the digger was called %d times; a refused open must not dig", got)
	}
	if got := StatusFor(err); got != http.StatusConflict {
		t.Errorf("a full range answers %d, want %d — nothing is wrong with the cluster", got, http.StatusConflict)
	}
}

// A range that does not parse refuses every open, and says which variable to
// fix. Falling back to "the kernel chooses" would put every tunnel back on an
// unpublished port and look like a working feature.
func TestABrokenRangeRefusesEveryOpenAndNamesItself(t *testing.T) {
	d := &fakeDigger{}
	m := testManagerOn(t, d, Options{Ports: "thirty thousand"})

	_, err := m.Open(context.Background(), dnsRequest())
	if err == nil {
		t.Fatal("a manager with an unreadable range opened a tunnel anyway")
	}
	if !strings.Contains(err.Error(), EnvPorts) {
		t.Errorf("the refusal is %q and does not name the variable to fix", err)
	}
	if got := d.count(); got != 0 {
		t.Errorf("the digger was called %d times despite the range being unreadable", got)
	}
	if got := StatusFor(err); got != http.StatusServiceUnavailable {
		t.Errorf("an unreadable range answers %d, want %d", got, http.StatusServiceUnavailable)
	}
}

// The real digger's own mapping, which no fake digger exercises.
//
// Every test above this one replaces the digger with a fake, so all of them
// pass with `LocalPort: 0` hard-coded in forwarderDigger, and a link that could
// not be dialled would go unnoticed. This is the assertion that discriminates
// it, and the integration test below dials the result on a real cluster.
func TestTheTerminalIsAskedForThePortThatWasAllocated(t *testing.T) {
	req := Request{Cluster: "docker-desktop", Namespace: "kube-system", Service: "kube-dns", Port: 9153}

	published := forwardRequest(req, 30007, bindAll)
	if published.LocalPort != 30007 {
		t.Errorf("the terminal was asked for local port %d, want 30007 — a tunnel outside the published range cannot be dialled from the host",
			published.LocalPort)
	}
	if published.LocalAddress != bindAll {
		t.Errorf("the listener would bind %q, want %q — Docker cannot route a published port to the container's own loopback",
			published.LocalAddress, bindAll)
	}
	if published.Cluster != req.Cluster || published.Namespace != req.Namespace ||
		published.Service != req.Service || published.RemotePort != req.Port {
		t.Errorf("the target was mangled on the way through: %+v", published)
	}

	// And with no range: port 0, and the narrow bind the host deployment had
	// before any of this existed.
	host := forwardRequest(req, 0, "")
	if host.LocalPort != 0 {
		t.Errorf("with no range the terminal was asked for port %d, want 0", host.LocalPort)
	}
	if host.LocalAddress != "" {
		t.Errorf("with no range the listener would bind %q, want the terminal's own 127.0.0.1 default", host.LocalAddress)
	}
}

// A range smaller than the live-set limit is what actually bounds the feature,
// so it becomes the limit. The panel prints this number as "3 of N"; a 16 with
// eight ports behind it is a number that gets believed.
func TestARangeSmallerThanTheLimitLowersIt(t *testing.T) {
	m := testManagerOn(t, &fakeDigger{}, Options{Ports: "30000-30003"})
	if m.Limit() != 4 {
		t.Errorf("the limit is %d on a four-port range, want 4", m.Limit())
	}

	wide := testManagerOn(t, &fakeDigger{}, Options{Ports: "30000-30063"})
	if wide.Limit() != maxOpen {
		t.Errorf("the limit is %d on a sixty-four-port range, want the default %d", wide.Limit(), maxOpen)
	}
}
