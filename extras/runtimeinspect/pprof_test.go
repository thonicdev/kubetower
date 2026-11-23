package runtimeinspect

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

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
