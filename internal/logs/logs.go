// Package logs streams container logs to a browser.
//
// A person watching a workload wants lines as they arrive and wants them to
// stop the moment the panel closes, so this is a stream with an owner, and most
// of the package is about bounding it.
//
// Server-sent events rather than a WebSocket: logs go one way, EventSource
// reconnects on its own, and plain HTTP behaves predictably with a session
// cookie and a reverse proxy.
//
// Three bounds keep an unbounded log from taking the console down:
//
//   - a tail, so a pod that has talked for a month does not send a month,
//   - a byte ceiling per stream, so a pod printing a megabyte a second cannot
//     hold a connection and a goroutine indefinitely,
//   - a hard deadline, because a crashed tab does not always close its
//     connection.
package logs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/textutil"
)

// Defaults: useful, and cheap when wrong.
const (
	defaultTail = 200
	maxTail     = 5000
	// maxBytes bounds one stream. A crash-looping pod reaches it in seconds, by
	// which point the operator has seen what they needed.
	maxBytes = 8 << 20 // 8 MiB
	// DefaultFollowMax is a follow's ceiling under the server profile, where the
	// sixteen follows are every person's; DesktopFollowMax is the desktop's,
	// where there is one person. Either is overridable through KT_LOG_FOLLOW_MAX,
	// between MinFollowMax and MaxFollowMax.
	DefaultFollowMax = 30 * time.Minute
	DesktopFollowMax = 2 * time.Hour
	MinFollowMax     = time.Minute
	MaxFollowMax     = 24 * time.Hour
	// maxDuration bounds a follow, and is the only bound on one: the stream runs
	// on a client with no per-request timeout, since http.Client.Timeout covers
	// body reads and would cut every follow at 30 seconds (see
	// cluster.StreamingClient). Reaching it is therefore announced, not surfaced as
	// a read error.
	maxDuration = DefaultFollowMax
)

// Request is one stream.
type Request struct {
	Cluster   string
	Namespace string
	Pod       string
	// Container may be empty, in which case DefaultContainer chooses the first
	// main container. Kubernetes' default, the first in the spec, reads as an empty
	// log on a pod with a sidecar in front.
	Container string
	Tail      int64
	Previous  bool
	Follow    bool
	// Timestamps asks Kubernetes to prefix every line with the instant it was
	// written, RFC3339Nano and a space. It is a property of the request, not of the
	// rendering, so turning the panel's toggle on re-opens the stream.
	Timestamps bool
}

// Container is one container of a pod. A finished init container has logs to
// read and no stream to follow.
type Container struct {
	Name    string `json:"name"`
	Init    bool   `json:"init"`
	Running bool   `json:"running"`
	Ready   bool   `json:"ready"`
	// RestartCount is why the "previous" option exists: a restarted container
	// has logs from before it died, usually the ones holding the answer.
	RestartCount int32 `json:"restartCount"`
}

// Clients is the slice of the cluster layer this package needs. An interface
// so a test can pin which of Client and StreamingClient is asked for, a
// difference invisible in a compiled binary.
type Clients interface {
	Resolve(name string) (string, bool)
	// Client carries the per-request timeout: right for the pod GET behind the
	// container list.
	Client(name string) (*cluster.Client, error)
	// StreamingClient has none: right for a body held open for half an hour.
	StreamingClient(name string) (*cluster.Client, error)
}

// Archive is the slice of the archive layer this package tees into. Nil is
// valid and keeps nothing: a console whose archive failed to open must still
// show logs. CapturesLogs is asked here, rather than relying on LogWriter's
// discarding sink, so the decision stays readable in this package.
type Archive interface {
	CapturesLogs() bool
	LogWriter(cluster, namespace, pod, container string) io.WriteCloser
}

type Streamer struct {
	clusters Clients
	// gate reviews every read here for the person behind the request: the pod
	// behind the container list (`get pods`) and each log stream (`get pods/log`),
	// by name. A Streamer built without one refuses them all.
	gate    *access.Gate
	archive Archive
	logf    func(string, ...any)
	// deadline is maxDuration, overridden only by tests, so the sole bound on a
	// quiet follow can be watched firing.
	deadline time.Duration
	// live bounds how many follows are open at once. See limit.go.
	live *streamLimit
	// writeWait bounds each write of the SSE stream to the browser, shortened
	// only by tests. See boundedWriter.
	writeWait time.Duration
	// recheckEvery overrides the package's recheckEvery in watch, for the tests.
	recheckEvery time.Duration
	// startEvery overrides the package's startEvery in waiting.go, for the
	// tests: the longest pause between two looks at a pod whose container a follow
	// is waiting for.
	startEvery time.Duration
}

// New builds the streamer. archive may be nil; gate may not, or every read is
// refused.
func New(clusters Clients, gate *access.Gate, logf func(string, ...any), archive Archive) *Streamer {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Streamer{clusters: clusters, gate: gate, archive: archive, logf: logf, deadline: maxDuration,
		live: newStreamLimit(maxStreams), writeWait: writeWait}
}

// SetFollowMax sets the ceiling of every follow. A value outside
// [MinFollowMax, MaxFollowMax] is ignored. Called once, before anything is served.
func (s *Streamer) SetFollowMax(d time.Duration) {
	if d >= MinFollowMax && d <= MaxFollowMax {
		s.deadline = d
	}
}

// humanDuration writes d as a person would: "2h", "30m", "1h30m", not "2h0m0s".
func humanDuration(d time.Duration) string {
	out := d.String()
	if strings.HasSuffix(out, "m0s") {
		out = strings.TrimSuffix(out, "0s")
	}
	if strings.HasSuffix(out, "h0m") {
		out = strings.TrimSuffix(out, "0m")
	}
	return out
}

// SharePerPerson bounds one person's followed streams to perPerson of the
// process's ceiling. Called once, before anything is served, and only under the
// server profile; zero, or a share at or above the ceiling, sets none.
func (s *Streamer) SharePerPerson(perPerson int) {
	if perPerson > 0 && perPerson < s.live.limit {
		s.live.perPerson = perPerson
	}
}

// EndStreamsOf ends every log stream the named person follows, and answers
// how many. Called at sign-out: an ended session stops new requests, but a
// follow would otherwise run on to its deadline.
func (s *Streamer) EndStreamsOf(who identity.Who) int { return s.live.endFor(who) }

// Following is how many follows are open now, over both transports (the
// event stream and the framed `/api/logs` operation share the slots). The
// macOS window holds its App Nap guard while it is non-zero, so a hidden window
// does not slow the stream.
func (s *Streamer) Following() int { return s.live.open() }

// Containers lists what can be read on a pod, so the panel can offer a
// choice.
func (s *Streamer) Containers(ctx context.Context, contextName, namespace, pod string) ([]Container, error) {
	p, err := s.pod(ctx, contextName, namespace, pod)
	if err != nil {
		return nil, err
	}

	states := map[string]corev1.ContainerStatus{}
	for _, st := range p.Status.ContainerStatuses {
		states[st.Name] = st
	}
	for _, st := range p.Status.InitContainerStatuses {
		states[st.Name] = st
	}

	out := make([]Container, 0, len(p.Spec.Containers)+len(p.Spec.InitContainers))
	// Init containers first, in the order they ran: when a pod fails to start,
	// the answer is nearly always in the init container that did not finish.
	for _, c := range p.Spec.InitContainers {
		out = append(out, containerOf(c.Name, true, states[c.Name]))
	}
	for _, c := range p.Spec.Containers {
		out = append(out, containerOf(c.Name, false, states[c.Name]))
	}
	return out, nil
}

// pod reads one pod, reviewed as `get pods` by its name, on the client with
// the per-request timeout. Shared by the container list and by a follow waiting
// for its container (waiting.go), so both ask the same review.
func (s *Streamer) pod(ctx context.Context, contextName, namespace, name string) (*corev1.Pod, error) {
	client, err := s.client(contextName)
	if err != nil {
		return nil, err
	}
	var p *corev1.Pod
	question := access.Attributes{Verb: "get", Version: "v1", Resource: "pods", Namespace: namespace, Name: name}
	_, err = s.gate.Read(ctx, client.Context, question, func(ctx context.Context) error {
		var err error
		p, err = client.Clients.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		return err
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

func containerOf(name string, init bool, st corev1.ContainerStatus) Container {
	return Container{
		Name:         name,
		Init:         init,
		Running:      st.State.Running != nil,
		Ready:        st.Ready,
		RestartCount: st.RestartCount,
	}
}

// DefaultContainer picks the container to read when the caller named none:
// the first main container, never simply the first, since a finished init
// container or a sidecar in front would make the panel look empty.
func (s *Streamer) DefaultContainer(ctx context.Context, contextName, namespace, pod string) (string, error) {
	containers, err := s.Containers(ctx, contextName, namespace, pod)
	if err != nil {
		return "", err
	}
	if len(containers) == 0 {
		return "", fmt.Errorf("pod %s/%s has no containers", namespace, pod)
	}
	for _, c := range containers {
		if !c.Init {
			return c.Name, nil
		}
	}
	return containers[0].Name, nil
}

// Open starts the stream. The caller closes it.
//
// It returns the Request it actually used: the cluster resolved to its context
// name and the container chosen. The archive files a stream under those names,
// and a URL's spelling or an empty container would file one container in two
// places.
func (s *Streamer) Open(ctx context.Context, req Request) (io.ReadCloser, Request, error) {
	req, err := s.address(ctx, req)
	if err != nil {
		return nil, req, err
	}

	opts := logOptions(req)

	// The client follows from the request: the ordinary client's 30-second
	// timeout covers body reads and would cut a follow mid-line, reading as the
	// cluster having gone away. A fixed read keeps the ordinary bound.
	client, err := s.clientFor(req.Cluster, opts.Follow)
	if err != nil {
		return nil, req, err
	}

	// Reviewed when the stream opens, with the pod's name: `get pods/log` is what
	// `kubectl logs` is authorized as. A follow is reviewed again while open
	// (watch.go), so one held after a RoleBinding is removed ends once the gate's
	// held answer expires.
	var stream io.ReadCloser
	_, err = s.gate.Read(ctx, req.Cluster, logQuestion(req), func(ctx context.Context) error {
		var err error
		stream, err = client.Clients.CoreV1().Pods(req.Namespace).GetLogs(req.Pod, opts).Stream(ctx)
		return err
	})
	return stream, req, err
}

// logQuestion is the review's question for one container's log.
func logQuestion(req Request) access.Attributes {
	return access.Attributes{Verb: "get", Version: "v1", Resource: "pods", Subresource: "log",
		Namespace: req.Namespace, Name: req.Pod}
}

// address turns what a caller asked for into what a cluster can be asked: a
// resolved context name (a URL's cluster name is not always one) and a named
// container. Shared with earlier.go so both address a container the same way.
func (s *Streamer) address(ctx context.Context, req Request) (Request, error) {
	name, ok := s.clusters.Resolve(req.Cluster)
	if !ok {
		return req, fmt.Errorf("unknown cluster %q", req.Cluster)
	}
	req.Cluster = name

	// No container named: choose one rather than let the API answer "a container
	// name must be specified", which is about our plumbing, not the cluster.
	if req.Container == "" {
		chosen, err := s.DefaultContainer(ctx, req.Cluster, req.Namespace, req.Pod)
		if err != nil {
			return req, err
		}
		req.Container = chosen
	}
	return req, nil
}

// requestFrom reads the query the panel sends. A bare link gets follow on and
// everything else off: follow is "not 0" while the rest are "is 1".
func requestFrom(q url.Values) Request {
	req := Request{
		Cluster:    q.Get("cluster"),
		Namespace:  q.Get("namespace"),
		Pod:        q.Get("pod"),
		Container:  q.Get("container"),
		Previous:   q.Get("previous") == "1",
		Follow:     q.Get("follow") != "0",
		Timestamps: q.Get("timestamps") == "1",
	}
	if n, err := strconv.ParseInt(q.Get("tail"), 10, 64); err == nil {
		req.Tail = n
	}
	return req
}

// logOptions turns a Request into what the API expects. Separate from Open so
// the bounds can be asserted without a cluster.
func logOptions(req Request) *corev1.PodLogOptions {
	tail := req.Tail
	if tail <= 0 {
		tail = defaultTail
	}
	if tail > maxTail {
		tail = maxTail
	}

	// A previous run is finished and cannot gain a line; following it would hold
	// the connection to the deadline and show a complete log under a spinner.
	follow := req.Follow && !req.Previous

	return &corev1.PodLogOptions{
		Container:  req.Container,
		Follow:     follow,
		Previous:   req.Previous,
		TailLines:  &tail,
		Timestamps: req.Timestamps,
	}
}

func (s *Streamer) client(contextName string) (*cluster.Client, error) {
	name, ok := s.clusters.Resolve(contextName)
	if !ok {
		return nil, fmt.Errorf("unknown cluster %q", contextName)
	}
	return s.clusters.Client(name)
}

// clientFor picks between the two, on an already-resolved context name.
func (s *Streamer) clientFor(name string, streaming bool) (*cluster.Client, error) {
	if streaming {
		return s.clusters.StreamingClient(name)
	}
	return s.clusters.Client(name)
}

// sink is the archive tee for one stream, or nil for none.
//
// Followed streams only:
//
//   - a follow is what the archive exists for, running while nobody looks,
//     across the window in which a pod is replaced and takes its logs with it;
//   - a fixed read is opened repeatedly while somebody works, and would append
//     the same lines each time;
//   - `previous` is always a fixed read, and its lines are history: the archive
//     stamps a line with when it was received, so it would file old output
//     under today, interleaved with the live run.
//
// A follow's initial tail is archived again each time the panel reopens. That
// is bounded by the tail, the stream ceiling and the archive's daily ceiling,
// and guessing which first lines are history would drop live ones.
//
// CapturesLogs is asked per stream because the switch takes effect without a
// restart. Turning capture off stops a running stream's tee too, as the
// archive closes its sinks.
func (s *Streamer) sink(req Request, follow bool) io.WriteCloser {
	if s.archive == nil || !follow || !s.archive.CapturesLogs() {
		return nil
	}
	return s.archive.LogWriter(req.Cluster, req.Namespace, req.Pod, req.Container)
}

// ContainersHandler answers the container list as JSON, for the panel's
// container switcher.
func (s *Streamer) ContainersHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		namespace, pod := q.Get("namespace"), q.Get("pod")
		if namespace == "" || pod == "" {
			http.Error(w, "namespace and pod are required", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()

		containers, err := s.Containers(ctx, q.Get("cluster"), namespace, pod)
		if err != nil {
			http.Error(w, textutil.FirstLineN(err.Error(), errorLine), statusFor(err))
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(w).Encode(containers); err != nil {
			s.logf("writing the container list for %s/%s: %v", namespace, pod, err)
		}
	})
}

// Handler serves a stream as server-sent events. Mounted by the caller, so it
// sits inside whatever guards the rest of the UI.
// Query: cluster, namespace, pod, container, tail, previous, follow,
// timestamps, and `before` for a page of earlier lines (earlier.go).
func (s *Streamer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := requestFrom(r.URL.Query())
		if req.Namespace == "" || req.Pod == "" {
			http.Error(w, "namespace and pod are required", http.StatusBadRequest)
			return
		}

		// Paging backwards is the same question about the same container, so it is
		// this route with `before` rather than a second endpoint, which internal/web
		// would also have to mount. Handled before the flusher check and the stream
		// limit: a JSON answer needs no flusher, and a bounded read must not take a
		// follow slot.
		if before, page, paging, err := earlierFrom(r.URL.Query()); paging {
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			s.earlier(w, r, before, page)
			return
		}

		// Without flushing, the response sits in a buffer and a follow looks like a
		// hung request.
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "this server cannot stream", http.StatusInternalServerError)
			return
		}

		// The ceiling, taken before anything is opened against a cluster so a refusal
		// costs the API server nothing. Only a follow counts: a fixed read ends on its
		// own, and must not refuse a live panel.
		base := r.Context()
		if logOptions(req).Follow {
			held, release, err := s.live.hold(base)
			if err != nil {
				s.refuse(w, err)
				return
			}
			defer release()
			// Re-checked while open: the sign-in and the review (watch.go).
			watched, stop := s.watch(held, req)
			defer stop()
			base = watched
		}

		ctx, cancel := context.WithTimeout(base, s.deadline)
		defer cancel()

		// The response starts on the first thing to send: a line, or a notice that
		// the follow is waiting for its container. Lazily, because the notice can come
		// before there is a stream, and once anything is sent a status code cannot be.
		var out *boundedWriter
		begin := func() *boundedWriter {
			if out == nil {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				// Nothing in between may buffer this. nginx honors the header; Caddy does
				// not buffer by default.
				w.Header().Set("X-Accel-Buffering", "no")
				w.WriteHeader(http.StatusOK)
				flusher.Flush()
				out = newBoundedWriter(w, s.writeWait)
			}
			return out
		}
		defer func() {
			if out != nil {
				out.release()
			}
		}()
		send := func(event Event) error {
			began := begin()
			return sseEvent(began, began, event)
		}

		stream, opened, err := s.start(ctx, req, send)
		if errors.Is(err, errSaid) {
			return
		}
		if err != nil {
			// Nothing has been sent yet, so a status code still reaches the browser.
			http.Error(w, textutil.FirstLineN(err.Error(), errorLine), statusFor(err))
			return
		}

		// The archive tee, opened after the stream so a request that never starts
		// registers no sink, and closed here: an open sink holds a file handle and its
		// last partial line.
		sink := s.sink(opened, logOptions(opened).Follow)
		if sink != nil {
			defer func() { _ = sink.Close() }()
		}

		// carry closes the stream, and reads again on this response if a follow
		// ended before its first line on a pod no node has taken yet.
		_ = s.carry(ctx, stream, opened, sink, send)
	})
}

// writeWait bounds one write of an SSE stream to the browser, as
// internal/terminal's writeWait does for a socket.
const writeWait = 10 * time.Second

// boundedWriter is an SSE response whose every write and flush must finish
// within wait.
//
// The deadline context is only looked at between lines, and a write to a peer
// that stopped reading (a laptop asleep, a stalled proxy) blocks with no error
// once the TCP window is full. Bounded, the write fails, drive stops and the
// follow slot is given back. internal/terminal/stream.go bounds its socket the
// same way.
//
// A failed flush is reported on the next write: http.Flusher has no error
// return, and the next write is where drive looks.
type boundedWriter struct {
	w    io.Writer
	rc   *http.ResponseController
	wait time.Duration
	err  error
}

func newBoundedWriter(w http.ResponseWriter, wait time.Duration) *boundedWriter {
	if wait <= 0 {
		wait = writeWait
	}
	return &boundedWriter{w: w, rc: http.NewResponseController(w), wait: wait}
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	// Every writer net/http hands a handler takes a deadline; one that cannot is
	// written to without.
	_ = b.rc.SetWriteDeadline(time.Now().Add(b.wait))
	n, err := b.w.Write(p)
	if err != nil {
		b.err = err
	}
	return n, err
}

func (b *boundedWriter) Flush() {
	if b.err != nil {
		return
	}
	_ = b.rc.SetWriteDeadline(time.Now().Add(b.wait))
	if err := b.rc.Flush(); err != nil {
		b.err = err
	}
}

// release takes the deadline off the connection, which outlives this response
// when the browser keeps it alive.
func (b *boundedWriter) release() { _ = b.rc.SetWriteDeadline(time.Time{}) }

// endOfLog is the sentence a stream that ran out ends on. internal/web tells a
// natural finish from the deadline by it, and carry tells a follow that ended
// before its first line by it.
const endOfLog = "end of log"

// EventKind is which thing happened. An enumeration so a panel does not
// render "ended", "stopped at the ceiling" and "the read failed" alike,
// confusing an empty answer with a failed one; EventWaiting is not an end at
// all.
type EventKind int

const (
	// EventLine is one line the container printed, without its newline or any
	// carriage return.
	EventLine EventKind = iota
	// EventEnd is a stream that finished: the log ran out, or our own deadline
	// arrived. Text says which.
	EventEnd
	// EventTruncated is the byte ceiling: there is more log and this console will
	// not read it.
	EventTruncated
	// EventError is a read that failed: "the cluster stopped answering", not
	// "there is no more".
	EventError
	// EventRefused is a follow this console ended while open, because its
	// sign-in ended or its review now says no (watch.go). Text says which. The
	// cluster did not fail; the person may no longer read it.
	EventRefused
	// EventWaiting is a follow waiting on the same stream for its container to
	// start (waiting.go). Not an end: lines follow once the container runs. Text
	// is the notice with the status's reason, "waiting for the container to start:
	// ContainerCreating", sent again when the reason changes.
	EventWaiting
	// EventNotStarted is a follow that stopped waiting because the container
	// will not start on its own (an image that cannot be pulled, a configuration
	// error) or the pod went away. An end, and not EventError: nothing failed, the
	// console is reporting what the pod says.
	EventNotStarted
)

// Event is one thing that happened on a log stream, independent of transport.
// The console's `text/event-stream` handler and the framed `/api/logs`
// operation read the same stream, so the bounds, the archive tee and the end
// diagnosis are one piece of code.
type Event struct {
	Kind EventKind
	// Text is the line for EventLine, and the sentence for the other kinds.
	Text string
}

// pump copies lines out as SSE over a stream the caller already has; drive
// does the work and pump adds only the framing. Handler goes through carry
// (waiting.go), which frames with the same sseEvent.
func (s *Streamer) pump(ctx context.Context, w io.Writer, flusher http.Flusher, stream io.Reader, req Request, sink io.Writer) {
	// A write error means the browser went away, and drive stops on it.
	_ = s.drive(ctx, stream, req, sink, func(event Event) error {
		return sseEvent(w, flusher, event)
	})
}

// sseEvent writes one event as an SSE frame and flushes it, for lines and for
// waiting notices alike, so a kind is framed one way.
func sseEvent(w io.Writer, flusher http.Flusher, event Event) error {
	var err error
	switch event.Kind {
	case EventLine:
		_, err = fmt.Fprintf(w, "data: %s\n\n", event.Text)
	case EventTruncated:
		_, err = fmt.Fprintf(w, "event: truncated\ndata: %s\n\n", event.Text)
	case EventError:
		_, err = fmt.Fprintf(w, "event: error\ndata: %s\n\n", event.Text)
	case EventEnd:
		_, err = fmt.Fprintf(w, "event: end\ndata: %s\n\n", event.Text)
	case EventRefused, EventNotStarted:
		// The panel's EventSource knows three named events; a refusal and a container
		// that will not start are said as the error it already draws.
		_, err = fmt.Fprintf(w, "event: error\ndata: %s\n\n", event.Text)
	case EventWaiting:
		// An EventSource drops a named event nobody listens for, so this is for other
		// clients: the stream is open and says why it is quiet.
		_, err = fmt.Fprintf(w, "event: waiting\ndata: %s\n\n", event.Text)
	}
	flusher.Flush()
	return err
}

// drive copies lines out as events until something stops it, and says what.
//
// sink may be nil. When it is not, it gets the same lines, unframed and
// newline-terminated, so the archive reads as a plain log.
//
// emit's error stops the stream and is returned: the client has gone, so
// there is nobody to tell.
func (s *Streamer) drive(ctx context.Context, stream io.Reader, req Request, sink io.Writer, emit func(Event) error) error {
	scanner := bufio.NewScanner(stream)
	// One log line can be a serialized stack trace; the default 64 KiB token
	// limit would end the stream with "token too long".
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)

	var written int
	for scanner.Scan() {
		if ctx.Err() != nil {
			// Said below; otherwise a stream that talked up to the deadline would end in
			// silence.
			break
		}
		line := scanner.Text()

		// SSE frames are newline-delimited and Scanner has already split on "\n", but
		// carriage returns from Windows containers remain. A CR is not legal inside the
		// framed reader's JSON string either.
		line = strings.ReplaceAll(line, "\r", "")

		// The archive first: a line already read happened, and the browser leaving
		// mid-line is when the kept copy is worth most. The sink never fails and never
		// blocks; see internal/archive.
		if sink != nil {
			_, _ = io.WriteString(sink, line+"\n")
		}

		if err := emit(Event{Kind: EventLine, Text: line}); err != nil {
			return err // the reader went away
		}

		written += len(line)
		if written >= maxBytes {
			s.logf("log stream for %s/%s stopped at the %d MiB ceiling", req.Namespace, req.Pod, maxBytes>>20)
			return emit(Event{Kind: EventTruncated, Text: fmt.Sprintf(
				"stopped after %d MiB — narrow the tail or filter in the terminal", maxBytes>>20)})
		}
	}

	// The context before the scanner: when a deadline or cancellation ends a
	// stream, the in-flight read fails with a closed-connection error, which would
	// make our own ceiling look like the cluster dropping the connection.
	if cut := whyCut(ctx); cut != nil {
		// Ended from inside (watch.go): said, whatever the read failed with.
		return emit(*cut)
	}
	switch err := scanner.Err(); {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		s.logf("log stream for %s/%s reached the %s ceiling", req.Namespace, req.Pod, s.deadline)
		return emit(Event{Kind: EventEnd, Text: fmt.Sprintf(
			"stopped after %s — reopen the panel to carry on", humanDuration(s.deadline))})
	case ctx.Err() != nil:
		// The client went away. Nobody to tell.
		return nil
	case err != nil && !errors.Is(err, context.Canceled):
		return emit(Event{Kind: EventError, Text: textutil.FirstLineN(err.Error(), errorLine)})
	default:
		// A non-follow stream ending is normal, and the panel must know or it shows a
		// spinner over a complete log.
		return emit(Event{Kind: EventEnd, Text: endOfLog})
	}
}

// Follow is the framed reader: the stream Handler serves, delivered as events
// to a caller that is not an http.ResponseWriter. It holds what a caller must
// not reproduce:
//
//   - the ceiling on followed streams (limit.go), taken before anything is
//     opened against a cluster, and shared with Handler so it binds both;
//   - the deadline, the sole bound on a quiet follow;
//   - the archive tee, opened after the stream so a request that never starts
//     registers no sink.
//
// The returned error is a condition found before the stream opened (the
// ceiling, an unknown cluster, a missing pod). Anything later arrives as an
// Event.
func (s *Streamer) Follow(ctx context.Context, req Request, emit func(Event) error) error {
	// Only a follow counts: a fixed read ends on its own, and must not refuse a
	// live panel.
	if logOptions(req).Follow {
		held, release, err := s.live.hold(ctx)
		if err != nil {
			return err
		}
		defer release()
		watched, stop := s.watch(held, req)
		defer stop()
		ctx = watched
	}

	ctx, cancel := context.WithTimeout(ctx, s.deadline)
	defer cancel()

	// A follow whose container has not started waits for it here, inside the
	// slot and the deadline, and says so through emit (waiting.go). An end reached
	// while waiting has already been said.
	stream, opened, err := s.start(ctx, req, emit)
	if errors.Is(err, errSaid) {
		return nil
	}
	if err != nil {
		return err
	}

	sink := s.sink(opened, logOptions(opened).Follow)
	if sink != nil {
		defer func() { _ = sink.Close() }()
	}

	// carry closes the stream, and for a follow that ended before its first line
	// on a pod no node has taken yet, waits and reads again.
	return s.carry(ctx, stream, opened, sink, emit)
}

// EffectiveTail is how many lines this request will ask for, after the
// default and the clamp. A caller compares it with what arrived to know whether
// the read reached the start of what the cluster holds; keeping defaultTail and
// maxTail only here keeps that comparison right.
func EffectiveTail(req Request) int64 { return *logOptions(req).TailLines }

// Resolve answers what a request would be read as, without reading it. A
// caller that names no container gets the first main one, and the framed
// stream must put that name on every line.
func (s *Streamer) Resolve(ctx context.Context, req Request) (Request, error) {
	return s.address(ctx, req)
}

// statusFor maps an error to an HTTP status, so a missing pod does not look
// like a broken server.
//
// An EventSource never sees a failed handshake's status or body, so this
// reaches curl and the log, not the panel. Asking for the previous run of a
// container that never restarted lands on "not found", which is why the panel
// refuses that click itself from the restart count.
func statusFor(err error) int {
	// The typed predicates first: a Kubernetes error carries its own status, and
	// apierrors unwraps. The text matching below stays, since most of those
	// sentences are this package's own and carry no status.
	switch {
	case errors.Is(err, access.ErrUnreviewable):
		// The access review could not be made: neither a refusal nor an unreachable
		// cluster.
		return http.StatusServiceUnavailable
	case apierrors.IsNotFound(err):
		return http.StatusNotFound
	case apierrors.IsForbidden(err):
		return http.StatusForbidden
	case apierrors.IsBadRequest(err), apierrors.IsInvalid(err):
		return http.StatusBadRequest
	}

	message := strings.ToLower(err.Error())
	switch {
	// Covers `previous terminated container "x" in pod "y" not found` as well as
	// a missing pod.
	case strings.Contains(message, "not found"):
		return http.StatusNotFound
	case strings.Contains(message, "unknown cluster"),
		strings.Contains(message, "a container name must be specified"):
		return http.StatusBadRequest
	case strings.Contains(message, "forbidden"):
		return http.StatusForbidden
	}
	return http.StatusBadGateway
}

// errorLine bounds an error wherever this package shows one, in runes. The
// same as internal/forward and internal/health, so one cluster error reads the
// same everywhere; an http.Error body and an SSE `event: error` frame, which the
// panel prints verbatim on one line.
const errorLine = 200

// refuse says no in a way the panel can read. An EventSource gets neither the
// status nor the body of a failed handshake, so a 429 would arrive as the same
// blank failure a broken cluster produces; internal/terminal writes its ceiling
// over the socket after the upgrade for the same reason. The stream opens and
// says at once why it is closing; the header tells clients other than a browser,
// which would otherwise read a 200 as success.
func (s *Streamer) refuse(w http.ResponseWriter, err error) {
	s.logf("log stream refused: %s (%d open)", textutil.FirstLineN(err.Error(), errorLine), s.live.open())

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-KubeTower-Refusal", "log-stream-limit")
	w.WriteHeader(http.StatusOK)

	fmt.Fprintf(w, "event: error\ndata: %s\n\n", textutil.FirstLineN(err.Error(), errorLine))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}
