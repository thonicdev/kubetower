package runtimeinspect

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

// podHost is fakeHost with the console's pod tunnel, landing on a test server
// that plays pprof.
type podHost struct {
	*fakeHost
	addr    string
	err     error
	mu      sync.Mutex
	opened  []string
	stopped int
}

func (h *podHost) ForwardPod(_ *http.Request, cluster, namespace, pod string, port int) (extension.Tunnel, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.opened = append(h.opened, cluster+"/"+namespace+"/"+pod+":"+itoa(port))
	if h.err != nil {
		return extension.Tunnel{}, h.err
	}
	return extension.Tunnel{Addr: h.addr, Pod: pod, PodKnown: true, Stop: func() {
		h.mu.Lock()
		h.stopped++
		h.mu.Unlock()
	}}, nil
}

func itoa(n int) string { return strconv.Itoa(n) }

const goroutineDump = `goroutine 1 [running]:
main.main()
	/src/main.go:10 +0x1d

goroutine 18 [chan receive, 5 minutes]:
main.worker(0xc000010000)
	/src/main.go:42 +0x45
created by main.main in goroutine 1
	/src/main.go:20 +0x2a

goroutine 19 [chan receive, 12 minutes]:
main.worker(0xc000010010)
	/src/main.go:42 +0x45
created by main.main in goroutine 1
	/src/main.go:20 +0x2a

goroutine 20 [select]:
net/http.(*persistConn).writeLoop(0xc0001)
	/usr/local/go/src/net/http/transport.go:2421 +0xe5
`

// pprofServer plays net/http/pprof and records what it was asked.
func pprofServer(t *testing.T, serving bool) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	asked := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		if !serving {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/debug/pprof/":
			_, _ = w.Write([]byte("<html>profiles</html>"))
		case "/debug/pprof/goroutine":
			_, _ = w.Write([]byte(goroutineDump))
		case "/debug/pprof/heap", "/debug/pprof/profile", "/debug/pprof/trace":
			_, _ = w.Write([]byte("PROFILE:" + r.URL.RequestURI()))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

func goPod(annotations map[string]string, c corev1.Container) *corev1.Pod {
	p := pod("shop", "api-0", c, corev1.Container{Name: "web", Image: "nginx:1.27"})
	p.Annotations = annotations
	return p
}

func goContainer() corev1.Container {
	return corev1.Container{Name: "app", Image: "registry.example.com/api:1", Env: envNamed("GOMEMLIMIT")}
}

func newPodHost(t *testing.T, srv *httptest.Server, p *corev1.Pod) *podHost {
	return &podHost{fakeHost: newHost(t, p), addr: strings.TrimPrefix(srv.URL, "http://")}
}

// The port comes from the annotation, then a port named pprof, then 6060.
func TestThePprofPortIsReadFromTheSpec(t *testing.T) {
	c := goContainer()
	if port, source := pprofPort(goPod(nil, c), c); port != 6060 || source != "default" {
		t.Errorf("default = %d %s", port, source)
	}
	named := c
	named.Ports = []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}, {Name: "pprof", ContainerPort: 6061}}
	if port, source := pprofPort(goPod(nil, named), named); port != 6061 || source != "port" {
		t.Errorf("named port = %d %s", port, source)
	}
	for value, want := range map[string]int{"7070": 7070, "web=9090,app=7071": 7071, "web=9090, 7072": 7072, "app=x": 6061} {
		if port, _ := pprofPort(goPod(map[string]string{AnnotationPprofPort: value}, named), named); port != want {
			t.Errorf("annotation %q = %d, want %d", value, port, want)
		}
	}
}

// The state: the tunnel opened to the port, `/debug/pprof/` asked, the
// tunnel closed; a 404 is "not reachable" with why, not a failure.
func TestThePprofStateSaysWhetherItAnswers(t *testing.T) {
	srv, asked := pprofServer(t, true)
	h := newPodHost(t, srv, goPod(nil, goContainer()))
	rec := serve(t, h, "/x/kind/runtime/pod/shop/api-0/app/pprof")
	got := decode[pprofAnswer](t, rec)
	if rec.Code != http.StatusOK || !got.Reachable || got.Port != 6060 || got.Source != "default" {
		t.Fatalf("%d %+v", rec.Code, got)
	}
	if len(h.opened) != 1 || h.opened[0] != "kind/shop/api-0:6060" || h.stopped != 1 || (*asked)[0] != "GET /debug/pprof/" {
		t.Errorf("opened %v, stopped %d, asked %v", h.opened, h.stopped, *asked)
	}

	off, _ := pprofServer(t, false)
	h = newPodHost(t, off, goPod(nil, goContainer()))
	got = decode[pprofAnswer](t, serve(t, h, "/x/kind/runtime/pod/shop/api-0/app/pprof"))
	if got.Reachable || !strings.Contains(got.Detail, "404") {
		t.Errorf("unreachable = %+v", got)
	}
}

// The goroutine dump, grouped by state and stack, the largest group first,
// with the longest wait, and the raw dump for the download.
func TestTheGoroutinesAreGroupedByStateAndStack(t *testing.T) {
	srv, asked := pprofServer(t, true)
	h := newPodHost(t, srv, goPod(nil, goContainer()))
	rec := post(t, h, "/x/kind/runtime/pod/shop/api-0/app/pprof/goroutines", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	got := decode[goroutinesAnswer](t, rec)
	if got.Total != 4 || len(got.Groups) != 3 || got.Truncated || got.Raw != goroutineDump {
		t.Fatalf("answer = %+v", got)
	}
	first := got.Groups[0]
	if first.State != "chan receive" || first.Count != 2 || first.Longest != "12 minutes" ||
		strings.Join(first.Frames, "|") != "main.worker|created by main.main" {
		t.Errorf("first group = %+v", first)
	}
	if (*asked)[0] != "GET /debug/pprof/goroutine?debug=2" || h.stopped != 1 {
		t.Errorf("asked %v, stopped %d", *asked, h.stopped)
	}
}

// A profile is downloaded with a duration from its list; anything else is
// refused before a tunnel is opened.
func TestAProfileIsDownloadedWithADurationFromItsList(t *testing.T) {
	srv, asked := pprofServer(t, true)
	h := newPodHost(t, srv, goPod(nil, goContainer()))
	rec := post(t, h, "/x/kind/runtime/pod/shop/api-0/app/pprof/profile?seconds=10", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "PROFILE:/debug/pprof/profile?seconds=10" ||
		!strings.HasSuffix(strings.Trim(rec.Header().Get("Content-Disposition"), `"`), ".cpu.pb.gz") {
		t.Fatalf("%d %q %q", rec.Code, rec.Body, rec.Header().Get("Content-Disposition"))
	}
	rec = post(t, h, "/x/kind/runtime/pod/shop/api-0/app/pprof/heap", "")
	if rec.Body.String() != "PROFILE:/debug/pprof/heap" {
		t.Errorf("heap = %q", rec.Body)
	}
	before := len(h.opened)
	for _, path := range []string{
		"/x/kind/runtime/pod/shop/api-0/app/pprof/profile?seconds=7",
		"/x/kind/runtime/pod/shop/api-0/app/pprof/profile",
		"/x/kind/runtime/pod/shop/api-0/app/pprof/trace?seconds=60",
		"/x/kind/runtime/pod/shop/api-0/app/pprof/cmdline",
		"/x/kind/runtime/pod/shop/api-0/app/pprof/symbol",
	} {
		rec := post(t, h, path, "")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), CodeBadProfile) {
			t.Errorf("%s answered %d %s", path, rec.Code, rec.Body)
		}
	}
	if len(h.opened) != before {
		t.Errorf("a refused profile opened a tunnel: %v", h.opened[before:])
	}
	for _, a := range *asked {
		if strings.Contains(a, "cmdline") || strings.Contains(a, "symbol") {
			t.Errorf("pprof was asked %s", a)
		}
	}
}

// A container nothing says is Go, a console with no pod tunnel and a refused
// tunnel are answered, each with its own code.
func TestPprofRefusals(t *testing.T) {
	srv, _ := pprofServer(t, true)
	h := newPodHost(t, srv, goPod(nil, goContainer()))
	rec := serve(t, h, "/x/kind/runtime/pod/shop/api-0/web/pprof")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), CodeNotGo) || len(h.opened) != 0 {
		t.Errorf("not go: %d %s, opened %v", rec.Code, rec.Body, h.opened)
	}
	// The annotation makes it Go.
	h = newPodHost(t, srv, goPod(map[string]string{AnnotationRuntime: "web=go"}, goContainer()))
	if rec := serve(t, h, "/x/kind/runtime/pod/shop/api-0/web/pprof"); rec.Code != http.StatusOK {
		t.Errorf("annotated: %d %s", rec.Code, rec.Body)
	}

	plain := newHost(t, goPod(nil, goContainer()))
	if rec := serve(t, plain, "/x/kind/runtime/pod/shop/api-0/app/pprof"); rec.Code != http.StatusNotImplemented ||
		!strings.Contains(rec.Body.String(), CodeNoTunnel) {
		t.Errorf("no tunnel: %d %s", rec.Code, rec.Body)
	}

	h = newPodHost(t, srv, goPod(nil, goContainer()))
	h.err = extension.ErrDenied
	if rec := serve(t, h, "/x/kind/runtime/pod/shop/api-0/app/pprof"); rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), CodeTunnelForbidden) {
		t.Errorf("denied: %d %s", rec.Code, rec.Body)
	}
	h.err = errors.New("boom")
	if rec := post(t, h, "/x/kind/runtime/pod/shop/api-0/app/pprof/goroutines", ""); rec.Code != http.StatusBadGateway ||
		!strings.Contains(rec.Body.String(), CodeTunnelFailed) {
		t.Errorf("failed: %d %s", rec.Code, rec.Body)
	}
}

// setFor sets *p to v for the test.
func setFor[T any](t *testing.T, p *T, v T) {
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// A trace is downloaded for a duration from its list.
func TestATraceIsDownloaded(t *testing.T) {
	srv, _ := pprofServer(t, true)
	h := newPodHost(t, srv, goPod(nil, goContainer()))
	rec := post(t, h, "/x/kind/runtime/pod/shop/api-0/app/pprof/trace?seconds=5", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "PROFILE:/debug/pprof/trace?seconds=5" ||
		!strings.HasSuffix(strings.Trim(rec.Header().Get("Content-Disposition"), `"`), ".trace") {
		t.Fatalf("%d %q %q", rec.Code, rec.Body, rec.Header().Get("Content-Disposition"))
	}
}

// pprof answering an error is said as its answer, and told from pprof not
// answering at all.
func TestAnErrorFromPprofIsNotSilence(t *testing.T) {
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Could not enable CPU profiling: cpu profiling already in use", http.StatusInternalServerError)
	}))
	t.Cleanup(busy.Close)
	h := newPodHost(t, busy, goPod(nil, goContainer()))
	rec := post(t, h, "/x/kind/runtime/pod/shop/api-0/app/pprof/profile?seconds=10", "")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), CodePprofFailed) ||
		!strings.Contains(rec.Body.String(), "already in use") || !strings.Contains(rec.Body.String(), "500") {
		t.Errorf("busy: %d %s", rec.Code, rec.Body)
	}

	gone := httptest.NewServer(http.NotFoundHandler())
	h = newPodHost(t, gone, goPod(nil, goContainer()))
	gone.Close()
	rec = post(t, h, "/x/kind/runtime/pod/shop/api-0/app/pprof/goroutines", "")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), CodePprofUnreachable) {
		t.Errorf("gone: %d %s", rec.Code, rec.Body)
	}
}

// Every read through the tunnel ends within its budget.
func TestPprofReadsAreBoundedInTime(t *testing.T) {
	setFor(t, &pprofProbeBudget, 50*time.Millisecond)
	stuck := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(stuck.Close)
	h := newPodHost(t, stuck, goPod(nil, goContainer()))
	for _, path := range []string{
		"/x/kind/runtime/pod/shop/api-0/app/pprof/goroutines",
		"/x/kind/runtime/pod/shop/api-0/app/pprof/heap",
	} {
		start := time.Now()
		rec := post(t, h, path, "")
		if took := time.Since(start); took > 2*time.Second {
			t.Errorf("%s took %v", path, took)
		}
		if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), CodePprofUnreachable) {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	start := time.Now()
	rec := serve(t, h, "/x/kind/runtime/pod/shop/api-0/app/pprof")
	if took := time.Since(start); took > 2*time.Second || decode[pprofAnswer](t, rec).Reachable {
		t.Errorf("the probe took %v: %d %s", took, rec.Code, rec.Body)
	}
}

// A goroutine dump past its limit is cut and says so.
func TestAGoroutineDumpPastItsLimitIsCut(t *testing.T) {
	setFor(t, &pprofGoroutineLimit, 100)
	srv, _ := pprofServer(t, true)
	h := newPodHost(t, srv, goPod(nil, goContainer()))
	got := decode[goroutinesAnswer](t, post(t, h, "/x/kind/runtime/pod/shop/api-0/app/pprof/goroutines", ""))
	if !got.Truncated || len(got.Raw) != 100 {
		t.Errorf("truncated %v, %d bytes", got.Truncated, len(got.Raw))
	}

	// A dump that never ends is read up to the limit, not to the budget.
	setFor(t, &pprofProbeBudget, 3*time.Second)
	endless := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat(goroutineDump, 2)))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(endless.Close)
	h = newPodHost(t, endless, goPod(nil, goContainer()))
	start := time.Now()
	rec := post(t, h, "/x/kind/runtime/pod/shop/api-0/app/pprof/goroutines", "")
	if took := time.Since(start); rec.Code != http.StatusOK || took > time.Second {
		t.Errorf("an endless dump: %d after %v", rec.Code, took)
	}
}

// A line over 1 MiB stops the reading, and the answer says it is cut.
func TestALineTooLongCutsTheDump(t *testing.T) {
	dump := goroutineDump + "\ngoroutine 30 [select]:\n" + strings.Repeat("x", 1<<20+1) + "\n\ngoroutine 31 [select]:\nmain.late()\n"
	// Goroutine 30, whose header was read, counts; 31, past the line, does not.
	_, total, cut := parseGoroutines(dump)
	if !cut || total != 5 {
		t.Errorf("cut %v, total %d", cut, total)
	}
	if _, _, cut := parseGoroutines(goroutineDump); cut {
		t.Error("a whole dump read as cut")
	}
}

// downloadAt downloads the heap profile from a server that writes body,
// streamed when stream, and says whether the response was aborted.
func downloadAt(t *testing.T, body string, stream bool) (rec *httptest.ResponseRecorder, aborted bool) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !stream {
			_, _ = w.Write([]byte(body))
			return
		}
		for _, b := range []byte(body) {
			_, _ = w.Write([]byte{b})
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	h := newPodHost(t, srv, goPod(nil, goContainer()))
	defer func() {
		if r := recover(); r != nil {
			if r != http.ErrAbortHandler {
				panic(r)
			}
			aborted = true
		}
	}()
	return post(t, h, "/x/kind/runtime/pod/shop/api-0/app/pprof/heap", ""), false
}

// A profile larger than the limit fails rather than arrive cut.
func TestAProfilePastItsLimitFails(t *testing.T) {
	setFor(t, &pprofDownloadLimit, 10)
	rec, aborted := downloadAt(t, "0123456789abc", false)
	if aborted || rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), CodePprofFailed) {
		t.Errorf("sized: aborted %v, %d %s", aborted, rec.Code, rec.Body)
	}
	if _, aborted := downloadAt(t, "0123456789abc", true); !aborted {
		t.Error("a streamed profile past the limit ended as a whole file")
	}
	if rec, aborted := downloadAt(t, "0123456789", true); aborted || rec.Code != http.StatusOK || rec.Body.String() != "0123456789" {
		t.Errorf("a profile of exactly the limit: aborted %v, %d %q", aborted, rec.Code, rec.Body)
	}
}
