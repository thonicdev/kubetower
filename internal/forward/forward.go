// Package forward owns the port-forwards the operator has open.
//
// internal/terminal digs a tunnel (`Forwarder.Forward`) and hands the caller a
// Close; that suits the metrics layer, which opens and closes one inside a
// function. An operator instead opens one, walks away and later wants to know
// what is still open, so this package keeps the set: it opens tunnels, lists
// them, closes them one at a time or all at once, and reaps those whose pod has
// gone.
//
// # What an open tunnel exposes
//
// Inside a container, a listener on 127.0.0.1 is unreachable: the link the dock
// draws resolves in the browser to the host's loopback. See ports.go. When
// KT_FORWARD_PORTS is set (compose.dev.yaml sets it):
//
//   - a forward listens on a port in that range, on 0.0.0.0, because Docker
//     routes a published port to the container's interface, never to its
//     loopback (`LocalAddress` in internal/terminal/portforward.go);
//   - compose.dev.yaml publishes the same range on the host's loopback only,
//     like the console's own port, so the LAN cannot reach a tunnel;
//   - every local user on that host, and every container on the compose
//     network, can dial an open forward with no session: a raw TCP tunnel has no
//     front door of its own.
//
// That widening is the price of the feature working at all, bounded by a tunnel
// existing only while the operator leaves it open, and by the loopback-only
// publication.
//
// Running the binary on a host, KT_FORWARD_PORTS stays unset, the kernel picks
// the port and nothing is published. Every local user can still dial it, which
// matters on a shared jump box.
//
// # Read-only clusters
//
// A cluster in KT_READONLY_CLUSTERS is not forwarded to: Open refuses it and
// the handlers answer 403. See refuseOnReadOnlyClusters.
package forward

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/terminal"
	"github.com/thonicdev/kubetower/safego"
	"github.com/thonicdev/kubetower/textutil"
)

const (
	// maxOpen bounds the live set for the whole process. Each tunnel is a
	// goroutine, a listener and an SPDY stream held open; sixteen is far more than
	// anyone juggles deliberately, and clicking down a list of forty services stops
	// with a sentence naming the limit instead of accumulating forty streams.
	maxOpen = 16

	// sweepInterval is how often the sweep runs. A death is normally noticed at
	// once by watch; the sweep is the safety net.
	sweepInterval = 30 * time.Second

	// openTimeout bounds one open. Longer than the terminal's own 30 s readiness
	// wait, so its message, which names the pod, is the one the operator sees.
	openTimeout = 40 * time.Second
)

// Request is one tunnel asked for.
type Request struct {
	// Cluster is a kubeconfig context name, or the URL-safe form of one.
	Cluster string
	// Namespace of the service or pod.
	Namespace string
	// Service and Pod are alternatives; exactly one must be set.
	Service string
	Pod     string
	// Port is the service port, or the container port on a pod. Zero is allowed
	// only for a service exposing exactly one port.
	Port int
	// PortName and AppProtocol are what the object says about the port, used only
	// to choose http or https for the link (see scheme.go). They are not part of a
	// target's identity, so requestKey ignores them.
	PortName    string
	AppProtocol string
}

// Kind reports which of the two shapes this is, for display and for the key.
func (r Request) Kind() string {
	if r.Service != "" {
		return "service"
	}
	return "pod"
}

// Name is the service or the pod, whichever was given.
func (r Request) Name() string {
	if r.Service != "" {
		return r.Service
	}
	return r.Pod
}

func (r Request) validate() error {
	switch {
	case r.Cluster == "":
		return errors.New("a cluster is required")
	case r.Namespace == "":
		return errors.New("a namespace is required")
	case r.Service != "" && r.Pod != "":
		return errors.New("give either a service or a pod, not both")
	case r.Service == "" && r.Pod == "":
		return errors.New("give a service or a pod to forward to")
	case r.Pod != "" && r.Port <= 0:
		return errors.New("a pod forward needs a port")
	case r.Port < 0 || r.Port > 65535:
		return fmt.Errorf("%d is not a port", r.Port)
	}
	return nil
}

// Tunnel is one open forward, as the UI shows it.
type Tunnel struct {
	ID        string `json:"id"`
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	// Kind and Name are what was asked for; Target joins them for a table column.
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Target string `json:"target"`
	// Port is the port asked for, a service port for a service. PodPort is where
	// the tunnel lands, which differs for a service with a named targetPort.
	Port    int `json:"port"`
	PodPort int `json:"podPort"`
	// Pod is the replica reached: a service forward is not load balanced, it
	// picks one pod and stays on it.
	Pod       string `json:"pod"`
	LocalPort int    `json:"localPort"`
	// LocalAddr is what to dial, "127.0.0.1:51423", on the loopback of whatever
	// this process runs in; see the package comment.
	LocalAddr string `json:"localAddr"`
	// Scheme is "http" or "https", the one to put in front of LocalAddr; guessed
	// from the port's description, see scheme.go.
	Scheme string    `json:"scheme"`
	Opened time.Time `json:"opened"`
	// Age is Opened as a short duration a table can print.
	Age string `json:"age"`
	// Healthy is false once the tunnel has stopped and before it is reaped, so
	// the list never describes a dead listener as open.
	Healthy bool   `json:"healthy"`
	Error   string `json:"error,omitempty"`
	// ReadOnly repeats the cluster's marking. Open refuses a marked cluster, so a
	// tunnel this manager opened always says false; the field stays because the
	// contract carries it.
	ReadOnly bool `json:"readOnly"`
}

// link is one running tunnel, reduced to what this package needs of it. An
// interface rather than *terminal.Forward, whose unexported channels no test
// outside its package can build, because the behavior that matters here is
// what happens when a tunnel dies.
type link interface {
	Addr() string
	Pod() string
	PodPort() int
	Done() <-chan struct{}
	Err() error
	Close()
}

// digger opens one tunnel on a given local port: the terminal's Forwarder,
// behind an adapter. localPort is 0 to let the kernel choose (on a host), or a
// number from the published range (in a container). The manager allocates it,
// since the allocation must stay consistent with the live set it owns.
type digger interface {
	dig(ctx context.Context, req Request, localPort int) (link, error)
}

// gone reports whether a pod has definitely disappeared. See podChecker.
type gone func(ctx context.Context, cluster, namespace, pod string) bool

type entry struct {
	tunnel Tunnel
	link   link
	key    string
	// local is the port taken from the range, or 0 when there is none. Kept apart
	// from tunnel.LocalPort, which is read back from the listener: this is the
	// number that was taken and must be handed back.
	local int
	// freed guards the hand-back: two paths can each be the last to touch an
	// entry, and a second release would free a number since given to another
	// tunnel.
	freed sync.Once
}

// Options are what the manager needs. Everything but Clusters is optional.
type Options struct {
	Clusters *cluster.Manager
	// Limit overrides maxOpen; zero takes the default, and there is deliberately
	// no way to ask for none. A port range smaller than the limit lowers it, so the
	// panel never promises a slot that does not exist.
	Limit int
	// Ports is the range of local ports a forward may listen on, "30000-30031" or
	// "30000". Empty falls back to KT_FORWARD_PORTS; empty there too means the
	// kernel chooses and nothing is published. See ports.go.
	Ports string
	// ReadOnly reports whether a cluster is marked read-only. nil means none is.
	ReadOnly func(string) bool
	Logf     func(string, ...any)
	// Access reviews each forward before it is dug: may the person behind the
	// request `create pods/portforward` in that namespace. Nil refuses every
	// forward. See internal/access.
	Access *access.Gate
	// Lost is told about a tunnel that stopped without anybody here stopping it:
	// the pod went away, the stream broke, the cluster stopped answering. Never
	// about one closed by Close or Shutdown; see lose. Nil tells nobody.
	//
	// Called once per tunnel, on the goroutine that noticed, with no lock held; it
	// must not block.
	Lost func(Tunnel)
}

// Manager is the live set.
type Manager struct {
	digger   digger
	resolve  func(string) (string, bool)
	gone     gone
	readOnly func(string) bool
	limit    int
	logf     func(string, ...any)
	now      func() time.Time
	access   *access.Gate
	lost     func(Tunnel)

	// ports is the published range, or nil when the kernel chooses. It has its
	// own lock, taken without mu held: mu is held across List and every open's
	// bookkeeping, and a probe is a syscall.
	ports *portRange
	// portsErr is a KT_FORWARD_PORTS that could not be parsed, refused at Open
	// with the error itself: falling back to "the kernel chooses" would put every
	// tunnel on a port a container cannot publish.
	portsErr error

	// mu guards open, opening and closed.
	mu sync.Mutex
	// open is keyed by id. At sixteen entries a linear lookup by target is
	// cheaper to keep consistent than a second map.
	open map[string]*entry
	// opening counts the tunnels being dug right now, counted towards the limit
	// so a burst of clicks cannot all pass a check that sees an empty map.
	opening int
	// closed makes Shutdown final: a tunnel opened afterwards would outlive the
	// manager's bookkeeping.
	closed bool
}

// New builds the manager. It does not start the reaper; call Start for that.
func New(opts Options) *Manager {
	m := &Manager{
		readOnly: opts.ReadOnly,
		limit:    opts.Limit,
		logf:     opts.Logf,
		now:      time.Now,
		access:   opts.Access,
		lost:     opts.Lost,
		open:     map[string]*entry{},
	}
	if m.limit <= 0 {
		m.limit = maxOpen
	}
	if m.logf == nil {
		m.logf = func(string, ...any) {}
	}
	if m.readOnly == nil {
		m.readOnly = func(string) bool { return false }
	}
	m.readPorts(opts.Ports)
	if opts.Clusters != nil {
		forwarder := terminal.NewForwarder(opts.Clusters, opts.Access)
		forwarder.SetLogf(m.logf)
		m.digger = forwarderDigger{forwarder: forwarder, listenOn: m.listenOn()}
		m.resolve = opts.Clusters.Resolve
		m.gone = podChecker(opts.Clusters)
	}
	return m
}

// bindAll is what a forward binds when its port is published. Docker routes a
// published port to the container's interface, so a listener on the
// container's loopback would be unreachable through it (the same reason the
// image sets KT_LISTEN to ":8091").
const bindAll = "0.0.0.0"

// listenOn is the address a forward's listener binds: bindAll when ports are
// published, the terminal's loopback default otherwise.
func (m *Manager) listenOn() string {
	if m.ports == nil {
		return ""
	}
	return bindAll
}

// readPorts works out where local ports come from and logs it at startup,
// where a range that does not parse or is smaller than the limit can be fixed
// before anyone needs a forward.
func (m *Manager) readPorts(configured string) {
	raw := strings.TrimSpace(configured)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(EnvPorts))
	}
	low, high, err := parsePorts(raw)
	switch {
	case err != nil:
		m.portsErr = err
		m.logf("port-forward: %v - no forward can open until that is fixed", err)
		return
	case low == 0:
		// No range: the kernel chooses, nothing is published, and the dock's link
		// works only if 127.0.0.1 here is 127.0.0.1 in the browser, which holds on a
		// host and not in a container.
		m.logf("port-forward: %s is not set, so the kernel picks each local port. "+
			"Correct when this binary runs on your machine; in a container nothing publishes those ports and no forward can be dialled from the host", EnvPorts)
		return
	}

	m.ports = newPortRange(low, high, bindAll)
	// What publishes the range depends on how the container was started, so
	// compose.dev.yaml is named only as an example.
	if cluster.InContainer() {
		m.logf("port-forward: local ports %d-%d, bound on %s (%s); whatever runs this container must "+
			"publish the same range where the browser can reach it - on the host loopback, "+
			"as compose.dev.yaml does",
			low, high, bindAll, EnvPorts)
	} else {
		m.logf("port-forward: local ports %d-%d, bound on %s (%s)", low, high, bindAll, EnvPorts)
		// The wide bind is safe only because Docker publishes the range on the
		// host's loopback. Outside a container it is a listener on every interface
		// this machine has. Said rather than corrected: guessing the deployment wrong
		// the other way would put every forward on an unreachable port.
		m.logf("port-forward: %s is set but this does not look like a container, so each forward will listen on %s "+
			"and be reachable from your network. Unset it when you run the binary on your own machine: the kernel's choice is correct there",
			EnvPorts, bindAll)
	}
	if size := m.ports.size(); size < m.limit {
		m.logf("port-forward: %s gives %d port(s) and the live-set limit is %d, so %d is the real ceiling and the limit is lowered to it",
			EnvPorts, size, m.limit, size)
		m.limit = size
	}
}

// Start runs the reaper until the context ends, then closes everything, so
// the API server is not left holding streams until they time out.
func (m *Manager) Start(ctx context.Context) {
	// Through safego: a panic in one sweep is logged and the reaper goes on to
	// the next tick.
	safego.Go("port-forward: the reaper", func() error {
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				m.Shutdown()
				return nil
			case <-ticker.C:
				_ = safego.Call("port-forward: one sweep", func() error { m.sweep(ctx); return nil })
			}
		}
	}, nil)
}

// Open digs a tunnel, or returns the one already open on the same target.
//
// A second click on a port must hand back the first tunnel, not open another
// on a different local port. The key is the request as asked (cluster resolved
// to its context name, namespace, kind, name, port), so two clicks on the same
// button match and two ports of one service do not.
//
// The one seam: a single-port service asked with Port 0 and then with its port
// named opens two tunnels, since the requests only become identical after the
// service is read. The UI always sends the port it drew.
func (m *Manager) Open(ctx context.Context, req Request) (Tunnel, error) {
	if err := req.validate(); err != nil {
		return Tunnel{}, err
	}
	if m.digger == nil {
		return Tunnel{}, errors.New("port forwarding is not configured")
	}
	if m.portsErr != nil {
		return Tunnel{}, m.portsErr
	}
	name, ok := m.resolve(req.Cluster)
	if !ok {
		return Tunnel{}, fmt.Errorf("unknown cluster %q", req.Cluster)
	}
	req.Cluster = name
	if refuseOnReadOnlyClusters && m.readOnly(name) {
		return Tunnel{}, &ReadOnlyRefusal{Cluster: name}
	}
	// Review before a port is taken or a tunnel dug: `create pods/portforward` in
	// the namespace. A service forward only learns its pod inside the dig, so a
	// Role granting it on named pods alone is answered no here, the conservative
	// direction. Only the desktop offers these forwards, so the review is the
	// kubeconfig's own. Tunnels dug for an extra or the Prometheus panel do not come
	// through here; terminal.Forwarder.Forward reviews those.
	//
	// On the desktop, an answer that could not be established lets the forward
	// through as every other write does (access.Gate.LetsThrough); a denial
	// refuses.
	attrs := access.Attributes{Verb: "create", Version: "v1", Resource: "pods", Subresource: "portforward",
		Namespace: req.Namespace, Name: req.Pod}
	answer := m.access.Review(ctx, name, attrs)
	if !answer.Allowed() && !m.access.LetsThrough(answer) {
		return Tunnel{}, &access.Refused{Cluster: name, Attributes: attrs, Answer: answer}
	}

	key := requestKey(req)

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Tunnel{}, errors.New("KubeTower is shutting down")
	}
	if existing := m.liveByKey(key); existing != nil {
		tunnel := m.describe(existing)
		m.mu.Unlock()
		return tunnel, nil
	}
	if err := m.room(); err != nil {
		m.mu.Unlock()
		return Tunnel{}, err
	}
	m.opening++
	m.mu.Unlock()

	// The local port is chosen before the dig: in a container only a port in the
	// published range is reachable from the browser. Zero means the kernel chooses.
	// See ports.go. Taken outside mu, since it probes a socket.
	local, err := m.ports.take()
	if err != nil {
		m.mu.Lock()
		m.opening--
		m.mu.Unlock()
		return Tunnel{}, err
	}

	// Detached from the caller's context, bounded by our own: the tunnel outlives
	// the request that asked for it, and a request the browser abandoned must not
	// cancel a tunnel about to come up. It then still appears in the list.
	openCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openTimeout)
	defer cancel()

	dug, err := m.digger.dig(openCtx, req, local)

	m.mu.Lock()
	m.opening--
	switch {
	case err != nil:
		m.mu.Unlock()
		// Nothing was listening, so the port goes straight back. Every refusal below
		// does the same: these tunnels never reach the map, so no watcher will
		// release their port.
		m.ports.release(local)
		return Tunnel{}, err

	case m.closed:
		m.mu.Unlock()
		dug.Close()
		m.ports.release(local)
		return Tunnel{}, errors.New("KubeTower is shutting down")

	default:
		// Two identical clicks can both pass the check above and both dig. Keep
		// whichever landed first and close this one; holding the lock across the dig
		// would serialize every open behind the slowest cluster.
		if raced := m.liveByKey(key); raced != nil {
			tunnel := m.describe(raced)
			m.mu.Unlock()
			dug.Close()
			m.ports.release(local)
			return tunnel, nil
		}
		if err := m.room(); err != nil {
			m.mu.Unlock()
			dug.Close()
			m.ports.release(local)
			return Tunnel{}, err
		}
	}

	// The scheme is decided from the port asked for, which is what the name and
	// appProtocol describe; for a single-port service asked with none, the port it
	// landed on.
	port := displayPort(req.Port, dug.PodPort())

	e := &entry{
		key:   key,
		link:  dug,
		local: local,
		tunnel: Tunnel{
			ID:        newID(),
			Cluster:   req.Cluster,
			Namespace: req.Namespace,
			Kind:      req.Kind(),
			Name:      req.Name(),
			Target:    req.Kind() + "/" + req.Name(),
			Port:      port,
			PodPort:   dug.PodPort(),
			Pod:       dug.Pod(),
			LocalAddr: dug.Addr(),
			LocalPort: portOf(dug.Addr()),
			Scheme:    schemeFor(req.AppProtocol, req.PortName, port),
			Opened:    m.now(),
			ReadOnly:  m.readOnly(req.Cluster),
		},
	}
	m.open[e.tunnel.ID] = e
	tunnel := m.describe(e)
	m.mu.Unlock()

	go m.watch(e)

	// The scheme is logged because it is a guess. The line also names who asked,
	// from the request the guard verified: a tunnel is a TCP path into a pod, and
	// the close and reap lines refer to it by this id.
	m.logf("%s", identity.Line(ctx, "port-forward %s open: %s %s/%s -> %s://%s on %s (read-only cluster: %t) decision=%s",
		e.tunnel.ID, e.tunnel.Target, req.Namespace, e.tunnel.Pod,
		e.tunnel.Scheme, e.tunnel.LocalAddr, req.Cluster, e.tunnel.ReadOnly, answer.Decision))
	return tunnel, nil
}

// refuseOnReadOnlyClusters is the read-only decision, one constant so that
// reversing it is one edit.
//
// It is true: a port-forward is refused on a cluster in KT_READONLY_CLUSTERS,
// as the read-only terminal refuses `kubectl port-forward` (internal/terminal's
// mutatingVerbs). Two doors to the same socket, one refusing and one opening,
// would teach the operator that the marking is noise. What travels through a
// tunnel (a database client, an admin port, a DELETE) is invisible to this
// package, so "it creates nothing" is not the whole answer; the marking is about
// gestures, and is why this is a constant rather than an RBAC review.
//
// internal/metrics reaches Prometheus through terminal.Forwarder directly,
// never through Open, so the charts keep working on read-only clusters; only the
// Forward button and `POST /api/forwards` are refused. internal/web's portsFor
// draws the control disabled with the reason, and a request that arrives anyway
// gets this refusal.
//
// The marking is read once at start and the manager lives no longer than the
// process, so no tunnel predates its cluster's marking.
const refuseOnReadOnlyClusters = true

// ReadOnlyRefusal is Open's answer on a cluster marked read-only. A type, so a
// caller tells the marking from the cluster's own authorization without
// reading prose: `/api/forwards` answers it as `read_only`, never
// `rbac_denied`. StatusFor still reads it as a 403 by its words.
type ReadOnlyRefusal struct{ Cluster string }

func (r *ReadOnlyRefusal) Error() string {
	return r.Cluster + " is marked read-only in KT_READONLY_CLUSTERS, so port-forwarding to it " +
		"is refused, as kubectl port-forward is in its terminal"
}

// free hands one entry's local port back to the range, once: a second release
// would free the number after it had been given to another tunnel.
func (m *Manager) free(e *entry) {
	e.freed.Do(func() { m.ports.release(e.local) })
}

// room reports whether another tunnel fits. Called with the lock held.
func (m *Manager) room() error {
	if len(m.open)+m.opening >= m.limit {
		return fmt.Errorf("%d port-forwards are already open, which is the limit; close one first", m.limit)
	}
	return nil
}

// liveByKey finds an open tunnel on the same target, ignoring one that has
// died and not yet been reaped, so a second click gets a working tunnel. Called
// with the lock held.
func (m *Manager) liveByKey(key string) *entry {
	for _, e := range m.open {
		if e.key == key && alive(e.link) {
			return e
		}
	}
	return nil
}

// List reports what is open, oldest first, so a table has a stable order.
func (m *Manager) List() []Tunnel {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]Tunnel, 0, len(m.open))
	for _, e := range m.open {
		out = append(out, m.describe(e))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Opened.Equal(out[j].Opened) {
			return out[i].ID < out[j].ID
		}
		return out[i].Opened.Before(out[j].Opened)
	})
	return out
}

// Limit is how many may be open at once, so the UI can say "3 of 16".
func (m *Manager) Limit() int { return m.limit }

// Close tears down one tunnel by id and reports whether there was one.
func (m *Manager) Close(id string) bool {
	m.mu.Lock()
	e, ok := m.open[id]
	if ok {
		delete(m.open, id)
	}
	m.mu.Unlock()

	if !ok {
		return false
	}
	e.link.Close()
	m.logf("port-forward %s closed: %s was on %s", id, e.tunnel.Target, e.tunnel.LocalAddr)
	return true
}

// Shutdown closes everything and refuses to open more. Idempotent.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.closed = true
	entries := make([]*entry, 0, len(m.open))
	for id, e := range m.open {
		entries = append(entries, e)
		delete(m.open, id)
	}
	m.mu.Unlock()

	for _, e := range entries {
		e.link.Close()
	}
	if len(entries) > 0 {
		m.logf("port-forward: closed %d tunnel(s) on shutdown", len(entries))
	}
}

// describe renders one entry for the UI, asking the tunnel itself whether it
// is alive rather than trusting the map. Called with the lock held.
func (m *Manager) describe(e *entry) Tunnel {
	t := e.tunnel
	t.Age = age(m.now().Sub(t.Opened))
	if alive(e.link) {
		t.Healthy = true
		return t
	}
	t.Healthy = false
	if err := e.link.Err(); err != nil {
		t.Error = textutil.FirstLineN(err.Error(), errorLine)
	} else {
		t.Error = "the tunnel closed"
	}
	return t
}

// watch reaps a tunnel the moment it dies.
//
// client-go's ForwardPorts returns when the stream to the pod breaks, as it
// does when the pod goes away, and *terminal.Forward closes Done then, so
// nothing has to poll. A dead forward's listener still accepts and then resets
// connections, which reads as a broken workload, so it must leave the list.
func (m *Manager) watch(e *entry) {
	<-e.link.Done()

	// The port goes back here and nowhere else, for every tunnel that reached the
	// map, whoever closed it: only once Done is closed is the listener actually
	// down (client-go closes its listeners before ForwardPorts returns; Close only
	// asks). Releasing earlier would hand out a number the old socket still holds.
	m.free(e)

	m.mu.Lock()
	// Compare the entry, not the id alone: a stale watcher must never delete the
	// entry that replaced the one it was watching.
	if m.open[e.tunnel.ID] != e {
		m.mu.Unlock()
		return
	}
	delete(m.open, e.tunnel.ID)
	gone := m.describe(e)
	m.mu.Unlock()

	reason := "it was closed"
	if err := e.link.Err(); err != nil {
		reason = textutil.FirstLineN(err.Error(), errorLine)
	}
	m.logf("port-forward %s reaped: %s %s/%s - %s",
		e.tunnel.ID, e.tunnel.Target, e.tunnel.Namespace, e.tunnel.Pod, reason)
	m.lose(gone)
}

// lose tells `Options.Lost` about a tunnel that stopped on its own.
//
// A lost tunnel is told from a closed one by who took it out of the map,
// decided under mu, so there is one answer per tunnel:
//
//   - Close and Shutdown delete the entry first and close the link second, so
//     when Done fires watch finds the entry gone and says nothing.
//   - watch finding the entry still there means the stream ended under it;
//     reap, from the sweep, is the other way a tunnel nobody closed leaves.
//     Whichever deletes the entry tells; the other finds it gone.
//
// So a click on Stop never produces a "lost", whatever the timing.
//
// Never called under mu: the callback is somebody else's code.
func (m *Manager) lose(t Tunnel) {
	if m.lost != nil {
		m.lost(t)
	}
}

// sweep covers what watch cannot see: a pod that has gone while the idle
// stream to it has not yet noticed, during which the listener accepts
// connections that go nowhere. One pod GET per open tunnel per interval;
// nothing is written.
func (m *Manager) sweep(ctx context.Context) {
	m.mu.Lock()
	entries := make([]*entry, 0, len(m.open))
	for _, e := range m.open {
		entries = append(entries, e)
	}
	check := m.gone
	m.mu.Unlock()

	for _, e := range entries {
		switch {
		case !alive(e.link):
			// watch has this, or is about to. Closing again is safe, and covers a wedged
			// goroutine.
			m.reap(e, "the tunnel stopped")
		case check != nil && check(ctx, e.tunnel.Cluster, e.tunnel.Namespace, e.tunnel.Pod):
			m.reap(e, "the pod is gone")
		}
	}
}

// reap removes an entry the sweep found dead, and closes what is left of it.
func (m *Manager) reap(e *entry, why string) {
	m.mu.Lock()
	if m.open[e.tunnel.ID] != e {
		m.mu.Unlock()
		return
	}
	delete(m.open, e.tunnel.ID)
	gone := m.describe(e)
	m.mu.Unlock()

	e.link.Close()
	m.logf("port-forward %s reaped: %s %s/%s - %s",
		e.tunnel.ID, e.tunnel.Target, e.tunnel.Namespace, e.tunnel.Pod, why)
	m.lose(gone)
}

// podChecker asks the cluster whether a pod is still there. Only a definite
// "not found" counts: a lapsed SSO session, a timeout or a 403 says nothing
// about the pod, and must not tear down every forward.
func podChecker(clusters *cluster.Manager) gone {
	return func(ctx context.Context, name, namespace, pod string) bool {
		if pod == "" {
			return false
		}
		client, err := clusters.Client(name)
		if err != nil {
			return false
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_, err = client.Clients.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
		return apierrors.IsNotFound(err)
	}
}

// forwarderDigger is the adapter onto internal/terminal, which does the hard
// parts of digging a tunnel.
type forwarderDigger struct {
	forwarder *terminal.Forwarder
	// listenOn is the address the listener binds. Empty leaves the terminal's
	// default, 127.0.0.1; bindAll is what a published port needs.
	listenOn string
}

func (d forwarderDigger) dig(ctx context.Context, req Request, localPort int) (link, error) {
	f, err := d.forwarder.Forward(ctx, forwardRequest(req, localPort, d.listenOn))
	if err != nil {
		return nil, err
	}
	return forwardLink{f: f}, nil
}

// forwardRequest is what this package asks the terminal for. A pure function
// because these fields decide whether a tunnel can be dialled from the browser,
// and every manager test replaces the digger with a fake;
// TestTheTerminalIsAskedForThePortThatWasAllocated checks it.
func forwardRequest(req Request, localPort int, listenOn string) terminal.ForwardRequest {
	return terminal.ForwardRequest{
		Cluster:    req.Cluster,
		Namespace:  req.Namespace,
		Service:    req.Service,
		Pod:        req.Pod,
		RemotePort: req.Port,
		// The port taken from the published range, or 0 for the kernel's choice. The
		// range's bookkeeping guarantees no two live tunnels hold the same number.
		LocalPort: localPort,
		// Empty unless a port is published, so a host keeps the terminal's
		// loopback-only default.
		LocalAddress: listenOn,
	}
}

type forwardLink struct{ f *terminal.Forward }

func (l forwardLink) Addr() string          { return l.f.LocalAddr }
func (l forwardLink) Pod() string           { return l.f.Pod }
func (l forwardLink) PodPort() int          { return l.f.RemotePort }
func (l forwardLink) Done() <-chan struct{} { return l.f.Done() }
func (l forwardLink) Err() error            { return l.f.Err() }
func (l forwardLink) Close()                { l.f.Close() }

// alive is a non-blocking read of whether a tunnel is still running.
func alive(l link) bool {
	select {
	case <-l.Done():
		return false
	default:
		return true
	}
}

// requestKey is the identity of a target, for Open's idempotence. The
// cluster is already resolved, so the URL-safe form and the real name land on
// the same key.
func requestKey(req Request) string {
	return strings.Join([]string{
		req.Cluster, req.Namespace, req.Kind(), req.Name(), fmt.Sprint(req.Port),
	}, "\x00")
}

// displayPort is what the operator asked for, or where it landed when they asked
// for nothing.
func displayPort(asked, landed int) int {
	if asked > 0 {
		return asked
	}
	return landed
}

// portOf pulls the port back out of "127.0.0.1:51423"; zero if it cannot.
func portOf(addr string) int {
	i := strings.LastIndexByte(addr, ':')
	if i < 0 {
		return 0
	}
	var port int
	if _, err := fmt.Sscanf(addr[i+1:], "%d", &port); err != nil {
		return 0
	}
	return port
}

func newID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		// The id is a handle in a map, not a secret, so a clock reading is an
		// acceptable fallback.
		return fmt.Sprintf("%012x", time.Now().UnixNano()&0xffffffffffff)
	}
	return hex.EncodeToString(buf)
}

// age says how long ago in the shortest true form, as kubectl does.
func age(d time.Duration) string {
	switch {
	case d < time.Second:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// errorLine bounds an error wherever this package shows one, in runes (as
// textutil counts, so a non-Latin error is not cut mid-character). One number
// for the row's Error cell, the two http.Error bodies and the reap log line, so
// one cluster error reads the same everywhere.
const errorLine = 200
