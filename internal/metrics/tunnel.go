package metrics

// Getting a socket to a Service inside a cluster. This file adapts
// internal/terminal's forwarder rather than reimplementing it: it already
// prefers a ready replica yet reaches a Prometheus replaying its WAL, and
// drains the forwarder's output, whose pipe otherwise fills after about 64 KB
// and silently blocks every new connection. The interfaces let discovery be
// tested with a fake tunneller.

import (
	"context"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/terminal"
)

// Tunnel is a local address that reaches something inside a cluster.
type Tunnel interface {
	// Addr is what to dial, "127.0.0.1:34567".
	Addr() string
	// Close tears the tunnel down. Must be safe to call more than once.
	Close()
}

// PodOf is the pod a tunnel reached, and whether it could say.
// An optional method rather than one more on Tunnel, so its one caller
// (Host.Forward) can tell "this tunnel cannot say" from an empty name.
func PodOf(t Tunnel) (string, bool) {
	if named, ok := t.(interface{ Pod() (string, bool) }); ok {
		return named.Pod()
	}
	return "", false
}

// Tunneller opens tunnels to Services.
type Tunneller interface {
	Open(ctx context.Context, cluster, namespace, service string, port int) (Tunnel, error)
}

// PodTunneller opens tunnels to a port of one pod. The forwarder's tunneller
// implements it; a caller asks with a type assertion, so a fake that only
// opens Services still satisfies Tunneller.
type PodTunneller interface {
	OpenPod(ctx context.Context, cluster, namespace, pod string, port int) (Tunnel, error)
}

// managerTunnels wires the real forwarder to the real cluster manager.
func managerTunnels(manager *cluster.Manager, gate *access.Gate, logf func(string, ...any)) Tunneller {
	forwarder := terminal.NewForwarder(manager, gate)
	forwarder.SetLogf(logf)
	return Tunnels(forwarder)
}

// Tunnels adapts the terminal's forwarder to what this package needs, for a
// caller that already holds one and would rather not have a second.
func Tunnels(forwarder *terminal.Forwarder) Tunneller {
	return forwarderTunnels{forwarder: forwarder}
}

type forwarderTunnels struct {
	forwarder *terminal.Forwarder
}

func (t forwarderTunnels) Open(ctx context.Context, name, namespace, service string, port int) (Tunnel, error) {
	forward, err := t.forwarder.Forward(ctx, terminal.ForwardRequest{
		Cluster:   name,
		Namespace: namespace,
		Service:   service,
		// The service port: the forwarder resolves a named targetPort against
		// the pod it chose.
		RemotePort: port,
		// Zero asks the kernel for a free port, so two clusters' tunnels never
		// collide.
		LocalPort: 0,
	})
	if err != nil {
		return nil, err
	}
	return serviceTunnel{forward: forward}, nil
}

// OpenPod is Open to a container port of one pod, reviewed by the forwarder
// as `create pods/portforward` on it.
func (t forwarderTunnels) OpenPod(ctx context.Context, name, namespace, pod string, port int) (Tunnel, error) {
	forward, err := t.forwarder.Forward(ctx, terminal.ForwardRequest{
		Cluster:    name,
		Namespace:  namespace,
		Pod:        pod,
		RemotePort: port,
		LocalPort:  0,
	})
	if err != nil {
		return nil, err
	}
	return serviceTunnel{forward: forward}, nil
}

type serviceTunnel struct {
	forward *terminal.Forward
}

func (t serviceTunnel) Addr() string { return t.forward.LocalAddr }
func (t serviceTunnel) Close()       { t.forward.Close() }

// Pod is the pod the forwarder chose among the Service's (terminal.Forward.Pod).
func (t serviceTunnel) Pod() (string, bool) { return t.forward.Pod, t.forward.Pod != "" }

// Done is closed once the tunnel has stopped: its pod went, or the stream into
// the cluster failed. The pool reads it before handing a session out.
func (t serviceTunnel) Done() <-chan struct{} { return t.forward.Done() }
