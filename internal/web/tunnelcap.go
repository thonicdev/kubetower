package web

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/metrics"
)

// maxExtensionTunnels is how many tunnels extensions may hold open at once
// through extension.Host.Forward, across every cluster, extension and person. It
// is these tunnels' only ceiling: internal/forward counts the browser's forwards,
// and the forwarder is also the Prometheus panel's, whose pooled tunnels are the
// console's own. Sixteen, like the forwards and terminals: far more than a page
// needs, small enough that a page reopening in a loop ends in a refusal.
const maxExtensionTunnels = 16

// perPersonExtensionTunnels is one person's share of maxExtensionTunnels under the
// server profile, so one person cannot hold every slot. Four, like the shells' and
// log streams' shares; the tunnels are held for one request each, so four is a few
// pages at once. Set by web.New under --incluster only. The shared password
// account has no share (its name is everybody holding the password) and counts
// against the ceiling alone.
const perPersonExtensionTunnels = 4

// countTunnels puts the ceiling in front of a tunneller. Nil stays nil: that is
// how Host.Forward says the build has no forwarder.
func countTunnels(t metrics.Tunneller, limit int) metrics.Tunneller {
	if t == nil {
		return nil
	}
	return &countedTunnels{next: t, limit: limit}
}

type countedTunnels struct {
	next  metrics.Tunneller
	limit int

	mu   sync.Mutex
	open int

	// perPerson is one person's share, zero for none (the desktop); held counts
	// each person's tunnels by identity.Who.Key, as logs' streamLimit does.
	perPerson int
	held      map[string]int
}

// sharePerPerson bounds one person's tunnels to perPerson of the ceiling. Called
// once, by web.New under --incluster, before anything is served; zero, or a share
// at or above the ceiling, means none.
func (c *countedTunnels) sharePerPerson(perPerson int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if perPerson > 0 && perPerson < c.limit {
		c.perPerson = perPerson
		c.held = map[string]int{}
	}
}

// Open takes a slot before anything is dug, so a refusal costs the cluster
// nothing, and gives it back when the tunnel is closed or stops on its own (its
// pod went away). The slot is the person's ctx carries (identity.From), refused at
// the ceiling and, under the server profile, at their share; a ctx with nobody
// counts as one person, "".
func (c *countedTunnels) Open(ctx context.Context, cluster, namespace, service string, port int) (metrics.Tunnel, error) {
	who, _ := identity.From(ctx)
	key, share := who.Key(), !who.Shared()
	if err := c.take(key, share); err != nil {
		return nil, err
	}
	give := func() { c.give(key, share) }

	tunnel, err := c.next.Open(ctx, cluster, namespace, service, port)
	return counted(tunnel, err, give)
}

// OpenPod is Open to a port of one pod: the same slot, the same ceiling and
// share. A tunneller that cannot open one to a pod costs no slot.
func (c *countedTunnels) OpenPod(ctx context.Context, cluster, namespace, pod string, port int) (metrics.Tunnel, error) {
	pods, ok := c.next.(metrics.PodTunneller)
	if !ok {
		return nil, errNoPodTunnels
	}
	who, _ := identity.From(ctx)
	key, share := who.Key(), !who.Shared()
	if err := c.take(key, share); err != nil {
		return nil, err
	}
	give := func() { c.give(key, share) }
	tunnel, err := pods.OpenPod(ctx, cluster, namespace, pod, port)
	return counted(tunnel, err, give)
}

// errNoPodTunnels is a tunneller that opens tunnels to Services only.
var errNoPodTunnels = errors.New("this build's forwarder opens no tunnel to a pod's port")

// counted is what a tunneller answered, holding its slot until the tunnel is
// closed or stops on its own; a tunnel that did not open gives it back now.
func counted(tunnel metrics.Tunnel, err error, give func()) (metrics.Tunnel, error) {
	if err != nil {
		give()
		return nil, err
	}
	held := &countedTunnel{Tunnel: tunnel, give: give}
	// A stopped tunnel's Done is closed (terminal.Forward).
	if stops, ok := tunnel.(interface{ Done() <-chan struct{} }); ok {
		go func() {
			<-stops.Done()
			held.release()
		}()
	}
	return held, nil
}

// take claims a slot for one person. The process ceiling is said first: a full
// console is not this person's to fix by closing one of theirs.
func (c *countedTunnels) take(who string, share bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.open >= c.limit {
		return fmt.Errorf("%d tunnels opened by extensions are already open, which is the limit; "+
			"close a page that reaches into a cluster and try again", c.limit)
	}
	share = share && c.perPerson > 0
	if share && c.held[who] >= c.perPerson {
		return fmt.Errorf("your pages already hold %d tunnels opened by extensions, which is one person's "+
			"share of this shared console; close a page that reaches into a cluster and try again", c.perPerson)
	}
	c.open++
	if share {
		c.held[who]++
	}
	return nil
}

// give returns a slot take gave the same person.
func (c *countedTunnels) give(who string, share bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.open > 0 {
		c.open--
	}
	if share && c.perPerson > 0 && c.held[who] > 0 {
		c.held[who]--
		if c.held[who] == 0 {
			delete(c.held, who)
		}
	}
}

// live is how many are held, for the tests.
func (c *countedTunnels) live() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.open
}

// heldBy is how many one person holds against their share, for the tests.
func (c *countedTunnels) heldBy(who identity.Who) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.held[who.Key()]
}

// countedTunnel gives its slot back exactly once, whether it is closed, closed
// again, or stops on its own first.
type countedTunnel struct {
	metrics.Tunnel
	give func()
	once sync.Once
}

func (t *countedTunnel) Close() {
	t.Tunnel.Close()
	t.release()
}

func (t *countedTunnel) release() { t.once.Do(t.give) }

// Pod passes the counted tunnel's pod through: the embedded interface hides every
// method Tunnel does not name.
func (t *countedTunnel) Pod() (string, bool) { return metrics.PodOf(t.Tunnel) }
