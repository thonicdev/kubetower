package logs

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/textutil"
)

// A stand-in apiserver that answers the pod-log endpoint and nothing else.
//
// beat is the pause between lines and lines is how many to write; a lines of 0
// means "keep writing until the caller goes away", which is what a follow on a
// talkative pod looks like and what the deadline has to end.
func fakeAPI(t *testing.T, beat time.Duration, lines int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/log") {
			http.Error(w, "not this test's endpoint: "+r.URL.Path, http.StatusNotFound)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("the test server cannot stream")
			return
		}
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		for i := 0; lines == 0 || i < lines; i++ {
			if _, err := fmt.Fprintf(w, "line %d\n", i); err != nil {
				return
			}
			flusher.Flush()
			select {
			case <-time.After(beat):
			case <-r.Context().Done():
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// fakeClients is the cluster layer reduced to what this package asks of it, and
// it records *which* client was asked for. That is the whole subject of the
// timeout fix and it is invisible in anything the compiler checks.
type fakeClients struct {
	t        *testing.T
	config   *rest.Config
	friendly string // the DNS-friendly spelling a saved URL carries

	mu    sync.Mutex
	asked []string
}

func newFakeClients(t *testing.T, host string, timeout time.Duration) *fakeClients {
	return &fakeClients{t: t, config: &rest.Config{Host: host, Timeout: timeout}}
}

func (f *fakeClients) Resolve(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	if f.friendly != "" && name == f.friendly {
		return "real/context", true
	}
	if name == "unknown" {
		return "", false
	}
	return name, true
}

func (f *fakeClients) Client(string) (*cluster.Client, error) {
	f.record("bounded")
	return f.build(f.config)
}

func (f *fakeClients) StreamingClient(string) (*cluster.Client, error) {
	f.record("streaming")
	// The real thing, not a hand-written zero — a fake that cleared the timeout
	// itself would pass whatever cluster.StreamingConfig did.
	return f.build(cluster.StreamingConfig(f.config))
}

func (f *fakeClients) record(kind string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, kind)
}

func (f *fakeClients) whatWasAsked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

func (f *fakeClients) build(cfg *rest.Config) (*cluster.Client, error) {
	f.t.Helper()
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &cluster.Client{Context: "test", Config: cfg, Clients: clientset}, nil
}

// A followed stream must outlive the per-request timeout, which covers body
// reads and would cut every follow at 30 seconds — the tail shows, and the
// panel dies half a minute later.
//
// Short timeout, slow server: the mechanism is the subject, and 30 seconds of
// waiting proves nothing extra.
func TestAFollowedStreamOutlivesThePerRequestTimeout(t *testing.T) {
	const (
		timeout = 250 * time.Millisecond
		beat    = 70 * time.Millisecond
		lines   = 9 // 630 ms of writing
	)
	server := fakeAPI(t, beat, lines)
	clients := newFakeClients(t, server.URL, timeout)

	// Far longer than the client timeout, so what ends the read is the client
	// and not this.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stream, opened, err := New(clients, accesstest.Allow(), nil, nil).Open(ctx, Request{
		Cluster: "docker-desktop", Namespace: "kube-system", Pod: "coredns-0",
		Container: "coredns", Follow: true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer stream.Close()

	if got := clients.whatWasAsked(); len(got) != 1 || got[0] != "streaming" {
		t.Fatalf("a follow asked for %v; it must ask for the client with no request timeout", got)
	}

	var got int
	scanner := bufio.NewScanner(stream)
	for scanner.Scan() {
		got++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("the follow was cut off after %d of %d lines: %v", got, lines, err)
	}
	if got != lines {
		t.Errorf("read %d lines, want %d — the stream ended early", got, lines)
	}
	if opened.Container != "coredns" {
		t.Errorf("Open reported container %q", opened.Container)
	}
}

// The other half of the same decision. A read that is not following is a
// bounded fetch, and it keeps the 30-second ceiling — dropping the timeout
// everywhere would leave a hung apiserver holding a request for the full
// deadline instead.
func TestAFixedReadKeepsThePerRequestTimeout(t *testing.T) {
	server := fakeAPI(t, time.Millisecond, 3)

	for _, c := range []struct {
		name string
		req  Request
		want string
	}{
		{"a follow", Request{Follow: true}, "streaming"},
		{"a fixed read", Request{Follow: false}, "bounded"},
		// A previous run is a finished file: logOptions refuses to follow it, so
		// the client must follow that refusal rather than the request's flag.
		{"a previous run", Request{Follow: true, Previous: true}, "bounded"},
	} {
		clients := newFakeClients(t, server.URL, 5*time.Second)
		req := c.req
		req.Cluster, req.Namespace, req.Pod, req.Container = "docker-desktop", "n", "p", "app"

		stream, _, err := New(clients, accesstest.Allow(), nil, nil).Open(context.Background(), req)
		if err != nil {
			t.Fatalf("%s: Open: %v", c.name, err)
		}
		_, _ = io.Copy(io.Discard, stream)
		stream.Close()

		if got := clients.whatWasAsked(); len(got) != 1 || got[0] != c.want {
			t.Errorf("%s asked for %v, want [%s]", c.name, got, c.want)
		}
	}
}

// The only bound on a follow: with no client timeout, a browser tab that
// crashed without closing its socket would otherwise hold a goroutine and a
// connection on a talking pod forever.
//
// The deadline is a field so this test can reach it. A 30-minute constant
// cannot be exercised, and an unbounded stream nobody has watched end is the
// state this package was in.
func TestAFollowStillEndsAtTheDeadline(t *testing.T) {
	server := fakeAPI(t, 20*time.Millisecond, 0) // never stops on its own

	clients := newFakeClients(t, server.URL, 0)
	s := New(clients, accesstest.Allow(), nil, nil)
	s.deadline = 300 * time.Millisecond

	// The request carries a cancelable context so that a *failure* here is a
	// failure and not a hung test: without the deadline the handler never
	// returns, and something has to let go of the connection afterwards.
	reqCtx, abandon := context.WithCancel(context.Background())
	defer abandon()
	r := httptest.NewRequest(http.MethodGet,
		"/kt-api/logs?cluster=docker-desktop&namespace=n&pod=p&container=app", nil).WithContext(reqCtx)

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Handler().ServeHTTP(rec, r)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		abandon()
		<-done
		t.Fatal("the handler outlived its deadline; a tab that crashed without closing its socket holds a goroutine and a connection on a talking pod forever")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "data: line 0") {
		t.Errorf("nothing was streamed before the deadline:\n%s", textutil.FirstLine(body))
	}
	// It has to say what happened. The read that was in flight fails with a
	// closed-connection error, and reported as one our own ceiling arrives in
	// the panel looking like the cluster having dropped out.
	if !strings.Contains(body, "event: end") || !strings.Contains(body, "stopped after 300ms") {
		t.Errorf("the deadline did not announce itself; tail of the body:\n%s", tailOf(body))
	}
	if strings.Contains(body, "event: error") {
		t.Errorf("our own ceiling was reported as an error:\n%s", tailOf(body))
	}
}

func tailOf(s string) string {
	if len(s) > 400 {
		return "…" + s[len(s)-400:]
	}
	return s
}

// --- the archive tee ---

// fakeArchive is the archive reduced to the two methods this package uses, and
// it counts the calls: "checked per stream, not once at construction" is a
// behavior, not a comment.
type fakeArchive struct {
	mu        sync.Mutex
	on        bool
	asked     int
	handedOut int
	sinks     map[string]*fakeSink
}

func newFakeArchive(on bool) *fakeArchive {
	return &fakeArchive{on: on, sinks: map[string]*fakeSink{}}
}

func (a *fakeArchive) CapturesLogs() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked++
	return a.on
}

func (a *fakeArchive) set(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.on = on
}

func (a *fakeArchive) LogWriter(cluster, namespace, pod, container string) io.WriteCloser {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.handedOut++
	key := strings.Join([]string{cluster, namespace, pod, container}, "|")
	s := a.sinks[key]
	if s == nil {
		s = &fakeSink{}
		a.sinks[key] = s
	}
	return s
}

func (a *fakeArchive) sink(key string) *fakeSink {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sinks[key]
}

func (a *fakeArchive) keys() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.sinks))
	for k := range a.sinks {
		out = append(out, k)
	}
	return out
}

func (a *fakeArchive) counts() (asked, handedOut int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.asked, a.handedOut
}

// fakeSink is the real sink's contract: never fails, and is closed once.
type fakeSink struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	closed int
}

func (s *fakeSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *fakeSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed++
	return nil
}

func (s *fakeSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *fakeSink) closes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func streamOnce(t *testing.T, s *Streamer, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/kt-api/logs?"+query, nil))
	return rec
}

// The settings page offers "Keep logs too" and draws a Logs-bytes row, so a
// followed stream must actually be teed into a sink. A feature the UI offers
// and the code does not implement is worse than a missing one, because it is
// believed.
func TestAFollowedStreamIsWrittenToTheArchive(t *testing.T) {
	server := fakeAPI(t, time.Millisecond, 4)
	clients := newFakeClients(t, server.URL, 0)
	// A saved link carries Headlamp's spelling of an EKS ARN. The archive files
	// a stream under four names, and filing the same container under two
	// spellings of its cluster would split its history in half.
	clients.friendly = "real--context"

	store := newFakeArchive(true)
	body := streamOnce(t, New(clients, accesstest.Allow(), nil, store),
		"cluster=real--context&namespace=kube-system&pod=coredns-0&container=coredns")

	if !strings.Contains(body.Body.String(), "data: line 0") {
		t.Fatalf("the browser got nothing:\n%s", body.Body.String())
	}

	const key = "real/context|kube-system|coredns-0|coredns"
	sink := store.sink(key)
	if sink == nil {
		t.Fatalf("nothing was archived under %q; sinks: %v", key, store.keys())
	}
	want := "line 0\nline 1\nline 2\nline 3\n"
	if got := sink.String(); got != want {
		t.Errorf("the archive holds %q, want %q", got, want)
	}
	// The archive stores the log, not the SSE around it: the files are meant to
	// be read with `less`.
	if strings.Contains(sink.String(), "data: ") {
		t.Error("SSE framing reached the archive")
	}
	if sink.closes() != 1 {
		t.Errorf("the sink was closed %d times, want once — an unclosed sink holds a file handle and its last partial line", sink.closes())
	}
}

// The switch is on the settings page and takes effect without a restart, so it
// is read per stream. Reading it once when the Streamer was built would mean
// ticking the box did nothing until the container was restarted.
func TestCaptureIsDecidedPerStreamAndNotAtConstruction(t *testing.T) {
	server := fakeAPI(t, time.Millisecond, 2)
	clients := newFakeClients(t, server.URL, 0)
	store := newFakeArchive(false)
	s := New(clients, accesstest.Allow(), nil, store)

	const query = "cluster=docker-desktop&namespace=n&pod=p&container=app"

	streamOnce(t, s, query)
	if _, handed := store.counts(); handed != 0 {
		t.Fatalf("a sink was opened with capture off (%d)", handed)
	}
	if store.sink("docker-desktop|n|p|app") != nil {
		t.Fatal("something was archived with capture off")
	}

	// The operator ticks the box. No restart.
	store.set(true)
	streamOnce(t, s, query)

	sink := store.sink("docker-desktop|n|p|app")
	if sink == nil {
		t.Fatal("turning capture on did not take effect on the next stream opened")
	}
	if got := sink.String(); got != "line 0\nline 1\n" {
		t.Errorf("the archive holds %q", got)
	}
	if asked, _ := store.counts(); asked < 2 {
		t.Errorf("capture was asked about %d times over two streams; it must be read per stream", asked)
	}
}

// What is archived and what is not, and the reasoning is in Streamer.sink: a
// previous run's lines are history, and the archive stamps a line with when
// KubeTower received it. Keeping them would file last week's output under today,
// interleaved in one file with the live run's — and that file is what somebody
// reads later to work out what happened when.
func TestAPreviousRunIsNotArchived(t *testing.T) {
	server := fakeAPI(t, time.Millisecond, 3)
	clients := newFakeClients(t, server.URL, 0)
	store := newFakeArchive(true)

	streamOnce(t, New(clients, accesstest.Allow(), nil, store),
		"cluster=docker-desktop&namespace=n&pod=p&container=app&previous=1")

	if _, handed := store.counts(); handed != 0 {
		t.Errorf("a previous run opened %d sinks; its lines would be stamped as though they had just arrived", handed)
	}
}

// A fixed read is a snapshot already on screen, opened and closed repeatedly
// while somebody works. Archiving it would append the same lines each time.
func TestAFixedReadIsNotArchived(t *testing.T) {
	server := fakeAPI(t, time.Millisecond, 3)
	clients := newFakeClients(t, server.URL, 0)
	store := newFakeArchive(true)

	streamOnce(t, New(clients, accesstest.Allow(), nil, store),
		"cluster=docker-desktop&namespace=n&pod=p&container=app&follow=0")

	if _, handed := store.counts(); handed != 0 {
		t.Errorf("a fixed read opened %d sinks", handed)
	}
}

// Nil is valid. A console whose archive failed to open must still show logs.
func TestNoArchiveIsNotAFailure(t *testing.T) {
	server := fakeAPI(t, time.Millisecond, 2)
	clients := newFakeClients(t, server.URL, 0)

	rec := streamOnce(t, New(clients, accesstest.Allow(), nil, nil),
		"cluster=docker-desktop&namespace=n&pod=p&container=app")

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "data: line 0") {
		t.Errorf("a nil archive broke the stream: %d\n%s", rec.Code, rec.Body.String())
	}
}
