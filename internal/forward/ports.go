package forward

// Where a tunnel's local port comes from, and why in a container it is not
// the kernel's choice.
//
// Docker publishes only the ports named when the container was created, so a
// listener the kernel put on 56488 inside the container cannot be reached from
// the operator's browser: `http://127.0.0.1:56488` resolves to the host's
// loopback. The port therefore has to come from a range already published:
//
//	KT_FORWARD_PORTS=30000-30031
//
// read here and by compose.dev.yaml, one variable in two places, so the range
// allocated from and the range published cannot drift apart.
//
// This exists only because a container's network namespace is not the host's.
// A binary run on the operator's machine leaves KT_FORWARD_PORTS unset and the
// kernel's choice is correct. Without containers, this file, the
// `LocalAddress` plumbing in internal/terminal/portforward.go and the compose
// port range could go together.

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
)

// EnvPorts names the range. Read by this package and by compose.dev.yaml,
// which publishes the identical range on the host loopback.
const EnvPorts = "KT_FORWARD_PORTS"

// lowestPort refuses a privileged port up front: neither the container (uid
// 100) nor the host binary can bind below 1024, and `bind: permission denied`
// on a tunnel would not point at the setting.
const lowestPort = 1024

// parsePorts reads "30000-30031", or "30000" for a single port. An empty
// value is not an error: no range, the kernel chooses, nothing is published.
func parsePorts(raw string) (low, high int, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, 0, nil
	}
	lowText, highText, ranged := strings.Cut(raw, "-")
	if !ranged {
		highText = lowText
	}
	if low, err = parseOnePort(lowText); err != nil {
		return 0, 0, err
	}
	if high, err = parseOnePort(highText); err != nil {
		return 0, 0, err
	}
	// Refused rather than swapped: compose.dev.yaml reads the same string and
	// does not reorder it, so swapping here would make the two disagree.
	if high < low {
		return 0, 0, fmt.Errorf("%s=%q has its bounds the wrong way round: %d is below %d", EnvPorts, raw, high, low)
	}
	return low, high, nil
}

func parseOnePort(text string) (int, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, fmt.Errorf("%s wants a port or a range of ports, like 30000-30031", EnvPorts)
	}
	port, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a port number", EnvPorts, text)
	}
	switch {
	case port > 65535:
		return 0, fmt.Errorf("%s: %d is past the highest port there is", EnvPorts, port)
	case port < lowestPort:
		return 0, fmt.Errorf("%s: %d is a privileged port and KubeTower does not run as root", EnvPorts, port)
	}
	return port, nil
}

// portRange hands out one port at a time from the configured range.
//
// A nil *portRange is the no-range case and every method tolerates it: take
// answers 0, which the forwarder reads as "ask the kernel".
//
// It tracks and probes, and needs both:
//
//   - probing alone leaves a window between the probe's close and the
//     forwarder's bind, which two quick clicks land in, failing one with
//     `address already in use`;
//   - tracking alone assumes it owns the range, but another process (or a
//     `kubectl port-forward` in the terminal) may hold a number inside it.
type portRange struct {
	low, high int
	// host is the address the probe binds, which must be the address the forward
	// will bind: a port free on 127.0.0.1 may be taken on the container's
	// interface.
	host string

	mu    sync.Mutex
	taken map[int]bool
	// next is where the next search starts, so the range is walked round: a
	// forward opened after one closed does not get its number, which a browser tab
	// left on the old address would otherwise reach silently.
	next int
	// probe reports whether a port can be bound. A field so tests can drive
	// exhaustion and contention without real sockets.
	probe func(host string, port int) bool
}

func newPortRange(low, high int, host string) *portRange {
	return &portRange{
		low:   low,
		high:  high,
		host:  host,
		taken: map[int]bool{},
		probe: canBind,
	}
}

// size is how many ports the range holds. Zero for no range.
func (p *portRange) size() int {
	if p == nil {
		return 0
	}
	return p.high - p.low + 1
}

// take reserves a port, or says why it could not. It never falls back to port
// 0: a tunnel on an unpublished port would open and draw a link that cannot be
// dialled.
func (p *portRange) take() (int, error) {
	if p == nil {
		return 0, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	span := p.size()
	for i := 0; i < span; i++ {
		port := p.low + (p.next+i)%span
		if p.taken[port] || !p.probe(p.host, port) {
			continue
		}
		p.taken[port] = true
		p.next = (port - p.low + 1) % span
		return port, nil
	}
	return 0, fmt.Errorf(
		"no local port is free in %d-%d (%s), so a forward would not be reachable from your browser; close one first",
		p.low, p.high, EnvPorts)
}

// release hands a port back, once the tunnel that held it is actually dead
// (Manager.watch).
func (p *portRange) release(port int) {
	if p == nil || port == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.taken, port)
}

// held is how many ports are out, for the tests.
func (p *portRange) held() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.taken)
}

// canBind is the real probe. It closes the listener at once: it only asks
// whether the port is free, and the forwarder binds it a moment later.
func canBind(host string, port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}
