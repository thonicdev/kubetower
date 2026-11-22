package terminal

// Port forwarding.
//
// Managed control planes refuse to proxy their own services (EKS answers `503
// Address is not allowed` to the service proxy path), so the service proxy does
// not work there. Port forwarding goes through the kubelet instead and works on
// a managed cluster and docker-desktop alike, so it is how anything in-cluster
// is reached from this process.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/safego"
)

// forwardReadyTimeout bounds how long a caller waits for the tunnel to come
// up: enough for an EKS credential fetch and an upgrade, short enough that a
// page asking for metrics does not hang on a dead cluster.
const forwardReadyTimeout = 30 * time.Second

// ForwardRequest describes one tunnel.
type ForwardRequest struct {
	// Cluster is a kubeconfig context name, or the URL-safe form of one.
	Cluster string
	// Namespace of the pod or service.
	Namespace string
	// Service and Pod are alternatives; exactly one must be set. A service is
	// resolved to one of its pods, which is what port forwarding talks to: there is
	// no load balancing, and the tunnel always lands on the same replica.
	Service string
	Pod     string
	// RemotePort is the service port, or the container port on a pod. Zero is
	// allowed for a service with exactly one port.
	RemotePort int
	// LocalPort is the port to listen on. Zero asks the kernel for a free one, as
	// internal/metrics does; internal/forward passes a number from a published
	// range, which only it can make dialable from the browser. Whoever passes a
	// number keeps two forwards off it.
	LocalPort int
	// LocalAddress is the address to listen on. Empty means 127.0.0.1: a tunnel is
	// normally for this process's own use, and 0.0.0.0 in a container is reachable
	// from every other container on its network. internal/forward sets 0.0.0.0 when
	// its port is published, since Docker routes a published port to the
	// container's interface, never to its loopback.
	LocalAddress string
}

// Forward is a running tunnel. Close it when done; nothing else will.
type Forward struct {
	// LocalAddr is what a client should dial, "127.0.0.1:34567".
	LocalAddr string
	LocalPort int
	// Pod is the pod actually reached, for a service one of its endpoints: worth
	// reporting, since which replica answered matters.
	Pod        string
	Namespace  string
	RemotePort int

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once

	mu  sync.Mutex
	err error
}

// Close tears the tunnel down. Safe to call more than once.
func (f *Forward) Close() {
	f.stopOnce.Do(func() { close(f.stop) })
}

// Done is closed when the tunnel has stopped, closed or failed. A long-lived
// caller should watch it rather than assume the tunnel lives on.
func (f *Forward) Done() <-chan struct{} { return f.done }

// Err reports why the tunnel stopped, or nil if it was closed deliberately.
func (f *Forward) Err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

// Forwarder opens tunnels into clusters. Separate from Terminal because its
// callers include background pollers (Prometheus for the metrics) with no
// browser.
//
// Every tunnel is reviewed for whoever's context it is dug in: the service and
// its pods are read (`get services`, `list pods`), and the tunnel is `create
// pods/portforward` on the pod it lands on, as `kubectl port-forward` is
// authorized. A tunnel the console digs for itself says so with
// access.AsConsole.
type Forwarder struct {
	clusters *cluster.Manager
	gate     *access.Gate
	logf     func(string, ...any)
}

// NewForwarder builds a Forwarder. A nil gate refuses every tunnel.
func NewForwarder(clusters *cluster.Manager, gate *access.Gate) *Forwarder {
	return &Forwarder{
		clusters: clusters,
		gate:     gate,
		logf:     func(format string, v ...any) { log.Printf("[kubetower] "+format, v...) },
	}
}

// SetLogf redirects the forwarder's log lines, to share its owner's prefix.
func (f *Forwarder) SetLogf(logf func(string, ...any)) {
	if logf != nil {
		f.logf = logf
	}
}

// Forwarder returns a forwarder sharing the terminal's clusters and log.
func (t *Terminal) Forwarder() *Forwarder {
	f := NewForwarder(t.clusters, t.opts.Access)
	f.SetLogf(t.logf)
	return f
}

// Forward opens a tunnel and returns once it is listening.
func (f *Forwarder) Forward(ctx context.Context, req ForwardRequest) (*Forward, error) {
	name, ok := f.clusters.Resolve(req.Cluster)
	if !ok {
		// Wrapped, so an extension's Host.Forward can test it with errors.Is.
		return nil, fmt.Errorf("%w %q", extension.ErrUnknownCluster, req.Cluster)
	}
	client, err := f.clusters.Client(name)
	if err != nil {
		return nil, err
	}
	if !validName(req.Namespace) {
		return nil, fmt.Errorf("namespace %q is not a Kubernetes name", req.Namespace)
	}

	pod, remotePort, err := f.resolveTarget(ctx, name, client, req)
	if err != nil {
		return nil, err
	}

	var fwd *Forward
	_, err = f.gate.Perform(ctx, name,
		access.Attributes{Verb: "create", Version: "v1", Resource: "pods", Subresource: "portforward",
			Namespace: req.Namespace, Name: pod},
		func(ctx context.Context) error {
			var err error
			fwd, err = f.dig(ctx, name, client, req, pod, remotePort)
			return err
		})
	return fwd, err
}

// dig opens the tunnel the review allowed. Only Forward calls it, from inside
// Gate.Perform.
func (f *Forwarder) dig(ctx context.Context, name string, client *cluster.Client, req ForwardRequest, pod string, remotePort int) (*Forward, error) {
	target := client.Clients.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(req.Namespace).
		Name(pod).
		SubResource("portforward").
		URL()

	dialer, err := newForwardDialer(ctx, client, target)
	if err != nil {
		return nil, err
	}

	fwd := &Forward{
		Pod:        pod,
		Namespace:  req.Namespace,
		RemotePort: remotePort,
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}

	ready := make(chan struct{})
	// 127.0.0.1, not "localhost", which on a dual-stack host also resolves to ::1
	// and opens two listeners while GetPorts reports one.
	listenOn := strings.TrimSpace(req.LocalAddress)
	if listenOn == "" {
		listenOn = "127.0.0.1"
	}
	pf, err := portforward.NewOnAddresses(
		dialer,
		[]string{listenOn},
		[]string{fmt.Sprintf("%d:%d", req.LocalPort, remotePort)},
		fwd.stop, ready,
		// Both must be drained: the forwarder writes a line per connection, and once
		// an unread pipe fills (about 64 KB) every new connection blocks inside it with
		// no error, a tunnel that works for twenty minutes and then stops.
		&logWriter{logf: f.logf, prefix: "port-forward " + pod},
		&logWriter{logf: f.logf, prefix: "port-forward " + pod},
	)
	if err != nil {
		return nil, fmt.Errorf("preparing the tunnel to %s/%s: %w", req.Namespace, pod, err)
	}

	// Through safego: a panic in client-go's forwarder becomes the tunnel's error,
	// and done is still closed.
	safego.Go("port-forward: the tunnel", pf.ForwardPorts, func(err error) {
		if err != nil {
			fwd.mu.Lock()
			fwd.err = err
			fwd.mu.Unlock()
		}
		close(fwd.done)
	})

	timeout := time.NewTimer(forwardReadyTimeout)
	defer timeout.Stop()

	select {
	case <-ready:
	case <-fwd.done:
		fwd.Close()
		if err := fwd.Err(); err != nil {
			return nil, fmt.Errorf("tunnel to %s/%s: %w", req.Namespace, pod, err)
		}
		return nil, fmt.Errorf("tunnel to %s/%s closed before it was ready", req.Namespace, pod)
	case <-ctx.Done():
		fwd.Close()
		return nil, ctx.Err()
	case <-timeout.C:
		fwd.Close()
		return nil, fmt.Errorf("tunnel to %s/%s did not come up within %s", req.Namespace, pod, forwardReadyTimeout)
	}

	ports, err := pf.GetPorts()
	if err != nil || len(ports) == 0 {
		fwd.Close()
		return nil, fmt.Errorf("tunnel to %s/%s reported no local port: %w", req.Namespace, pod, err)
	}
	fwd.LocalPort = int(ports[0].Local)
	// The loopback form whatever was bound: 0.0.0.0 includes the loopback, and
	// through a published port the client is on the host's. A caller binding
	// another single address would need this to follow it.
	fwd.LocalAddr = fmt.Sprintf("127.0.0.1:%d", fwd.LocalPort)

	f.logf("%s", forwardOpenedLine(ctx, fwd.LocalAddr, req.Namespace, pod, remotePort, name, listenOn))
	return fwd, nil
}

// resolveTarget turns the request into a pod name and a port on that pod.
// Both reads are reviewed for the person behind ctx: a tunnel onto a Service
// they may not read would reveal the pods behind it.
func (f *Forwarder) resolveTarget(ctx context.Context, name string, client *cluster.Client, req ForwardRequest) (string, int, error) {
	switch {
	case req.Pod != "" && req.Service != "":
		return "", 0, errors.New("give either a pod or a service, not both")

	case req.Pod != "":
		if !validName(req.Pod) {
			return "", 0, fmt.Errorf("pod %q is not a Kubernetes name", req.Pod)
		}
		if req.RemotePort <= 0 {
			return "", 0, errors.New("a pod forward needs a remote port")
		}
		return req.Pod, req.RemotePort, nil

	case req.Service != "":
		if !validName(req.Service) {
			return "", 0, fmt.Errorf("service %q is not a Kubernetes name", req.Service)
		}
		var svc *corev1.Service
		_, err := f.gate.Read(ctx, name,
			access.Attributes{Verb: "get", Version: "v1", Resource: "services", Namespace: req.Namespace, Name: req.Service},
			func(ctx context.Context) error {
				var err error
				svc, err = client.Clients.CoreV1().Services(req.Namespace).Get(ctx, req.Service, metav1.GetOptions{})
				return err
			})
		if err != nil {
			return "", 0, fmt.Errorf("reading service %s/%s: %w", req.Namespace, req.Service, err)
		}
		selector, err := serviceSelector(svc)
		if err != nil {
			return "", 0, err
		}
		var pods *corev1.PodList
		_, err = f.gate.Read(ctx, name,
			access.Attributes{Verb: "list", Version: "v1", Resource: "pods", Namespace: req.Namespace},
			func(ctx context.Context) error {
				var err error
				pods, err = client.Clients.CoreV1().Pods(req.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
				return err
			})
		if err != nil {
			return "", 0, fmt.Errorf("listing the pods behind %s/%s: %w", req.Namespace, req.Service, err)
		}
		return selectPod(svc, pods.Items, req.RemotePort)

	default:
		return "", 0, errors.New("give a pod or a service to forward to")
	}
}

// serviceSelector is the label query that finds a service's pods, and refuses
// the services that have none.
func serviceSelector(svc *corev1.Service) (string, error) {
	if len(svc.Spec.Selector) == 0 {
		// An ExternalName service, or one with hand-kept Endpoints: no pods to forward
		// to, rather than picking any pod in the namespace.
		return "", fmt.Errorf("service %s/%s has no pod selector, so there is nothing to forward to",
			svc.Namespace, svc.Name)
	}
	return labels.SelectorFromSet(svc.Spec.Selector).String(), nil
}

// selectPod picks which pod a service's traffic goes to, and which port on it.
// Pure, so the cases a live cluster will not reproduce on demand (a named
// target port, a ready pod that is not first) can be tested.
func selectPod(svc *corev1.Service, pods []corev1.Pod, requested int) (string, int, error) {
	port, err := servicePort(svc, requested)
	if err != nil {
		return "", 0, err
	}

	pod := pickReadyPod(pods)
	if pod == nil {
		return "", 0, fmt.Errorf("no running pod behind service %s/%s", svc.Namespace, svc.Name)
	}

	target, err := targetPort(pod, port)
	if err != nil {
		return "", 0, err
	}
	return pod.Name, target, nil
}

// servicePort finds the ServicePort the caller meant.
func servicePort(svc *corev1.Service, requested int) (*corev1.ServicePort, error) {
	if requested <= 0 {
		if len(svc.Spec.Ports) == 1 {
			return &svc.Spec.Ports[0], nil
		}
		return nil, fmt.Errorf("service %s/%s exposes %d ports, so one has to be named",
			svc.Namespace, svc.Name, len(svc.Spec.Ports))
	}
	for i := range svc.Spec.Ports {
		if int(svc.Spec.Ports[i].Port) == requested {
			return &svc.Spec.Ports[i], nil
		}
	}
	return nil, fmt.Errorf("service %s/%s does not expose port %d", svc.Namespace, svc.Name, requested)
}

// pickReadyPod prefers a ready pod and falls back to one merely running: a
// Prometheus failing readiness while it replays its write-ahead log is exactly
// the pod someone wants a tunnel to.
func pickReadyPod(pods []corev1.Pod) *corev1.Pod {
	var running *corev1.Pod
	for i := range pods {
		pod := &pods[i]
		if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
			continue
		}
		if running == nil {
			running = pod
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return pod
			}
		}
	}
	return running
}

// targetPort resolves a service port to the port on the pod, where the tunnel
// lands.
func targetPort(pod *corev1.Pod, port *corev1.ServicePort) (int, error) {
	switch port.TargetPort.Type {
	case intstr.String:
		// A named target port is resolved against the chosen pod: it names a container
		// port, and only the pod knows its number.
		name := port.TargetPort.StrVal
		for _, c := range pod.Spec.Containers {
			for _, cp := range c.Ports {
				if cp.Name == name {
					return int(cp.ContainerPort), nil
				}
			}
		}
		return 0, fmt.Errorf("pod %s has no container port named %q", pod.Name, name)
	default:
		if port.TargetPort.IntVal > 0 {
			return int(port.TargetPort.IntVal), nil
		}
		// An unset targetPort means the service port itself, a documented default
		// many charts rely on.
		return int(port.Port), nil
	}
}

// newForwardDialer prefers the WebSocket-tunnelled transport and falls back to
// SPDY, as kubectl and the exec executor do.
//
// ctx is the one Gate.Perform handed dig, and the dialers are built from it:
// neither passes a context to its request (SPDY uses http.NewRequest, the
// WebSocket tunnel context.Background() in client-go v0.37), so the review's
// stamp would not reach the access guard, which would refuse the tunnel just
// allowed. access.Carry puts the stamp on those requests.
func newForwardDialer(ctx context.Context, client *cluster.Client, target *url.URL) (httpstream.Dialer, error) {
	return newBoundedForwardDialer(ctx, client, target, forwardDialWait)
}

// forwardDialWait bounds one dial of a tunnel: the connection to the API
// server and its answer to the upgrade. forwardReadyTimeout stops the caller
// waiting but not the dial, which runs on a client-go goroutine with no
// deadline; against a server that accepts and never answers, each attempt would
// park a goroutine and a connection for good. The same duration, so a dial is
// dropped when its caller gives up.
const forwardDialWait = forwardReadyTimeout

// newBoundedForwardDialer is newForwardDialer with the dial's bound as a
// parameter, for the test that watches one run out.
func newBoundedForwardDialer(ctx context.Context, client *cluster.Client, target *url.URL, wait time.Duration) (httpstream.Dialer, error) {
	// A tunnel is held open while in use, so it needs the config with no request
	// timeout (cluster.StreamingConfig); the shared one's 30 seconds would cut the
	// stream.
	streaming := cluster.StreamingConfig(client.Config)
	streaming.Wrap(access.Carry(ctx))
	streaming.Wrap(boundUpgrade(wait))

	tunneling, err := portforward.NewSPDYOverWebsocketDialer(target, streaming)
	if err != nil {
		return nil, fmt.Errorf("preparing the tunnel: %w", err)
	}
	transport, upgrader, err := spdy.RoundTripperFor(streaming)
	if err != nil {
		return nil, fmt.Errorf("preparing the tunnel: %w", err)
	}
	spdyDialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", target)
	return portforward.NewFallbackDialer(tunneling, spdyDialer, func(err error) bool {
		return upgradeRefused(err) || httpstream.IsHTTPSProxyError(err)
	}), nil
}

// boundUpgrade puts wait on the context of every upgrade request a tunnel's
// dialers make: the only way the dial gets a deadline, since both dialers build
// their request without a context.
//
// What it bounds (client-go v0.37, gorilla/websocket): the WebSocket tunnel,
// tried first, applies the deadline to the TCP and TLS dial and the whole
// handshake, then clears it once answered, so a tunnel that came up is not cut.
// The SPDY fallback dials and handshakes TLS under it but reads the upgrade's
// answer without a deadline; it is reached only after a server refused the
// WebSocket upgrade.
//
// The context is not canceled when the round trip returns: the connection is
// the tunnel's by then.
func boundUpgrade(wait time.Duration) func(http.RoundTripper) http.RoundTripper {
	return func(next http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(req *http.Request) (*http.Response, error) {
			ctx, cancel := context.WithTimeout(req.Context(), wait)
			resp, err := next.RoundTrip(req.WithContext(ctx))
			if err != nil {
				cancel()
				return resp, err
			}
			// Released when the deadline passes; canceling now would reach a body
			// client-go may still be reading.
			context.AfterFunc(ctx, cancel)
			return resp, nil
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// logWriter turns the forwarder's output into log lines and never blocks the
// writer: a tunnel whose output buffer fills stalls.
type logWriter struct {
	logf   func(string, ...any)
	prefix string

	mu   sync.Mutex
	rest []byte
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.rest = append(w.rest, p...)
	for {
		i := bytes.IndexByte(w.rest, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(w.rest[:i]))
		w.rest = w.rest[i+1:]
		if line != "" {
			w.logf("%s: %s", w.prefix, line)
		}
	}
	// A stream with no newline in it must not grow without bound either.
	if len(w.rest) > 4096 {
		w.logf("%s: %s", w.prefix, strings.TrimSpace(string(w.rest)))
		w.rest = w.rest[:0]
	}
	return len(p), nil
}

// forwardOpenedLine is the line a tunnel leaves when it opens, naming who
// asked. A port-forward is a TCP path into a pod that outlives the page, held by
// this console's credentials, so the log must say whose it is. The console's own
// tunnels (Prometheus for the metrics) carry no session and say `who=-
// via=unattributed`.
func forwardOpenedLine(ctx context.Context, local, namespace, pod string, port int, cluster, listenOn string) string {
	return identity.Line(ctx, "port-forward %s -> %s/%s:%d on %s (listening on %s)",
		local, namespace, pod, port, cluster, listenOn)
}
