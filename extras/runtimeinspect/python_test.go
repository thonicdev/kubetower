package runtimeinspect

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

// The testdata below are real: the two readers run in kind against
// python:3.12-slim (sh) and gcr.io/distroless/python3-debian12, which has no
// shell (python3), and prometheus_client 0.21.1's /metrics.

func readTestdata(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func pythonPod(annotations map[string]string, c corev1.Container) *corev1.Pod {
	p := pod("shop", "py-0", c, corev1.Container{Name: "web", Image: "nginx:1.27"})
	p.Annotations = annotations
	return p
}

func pythonContainer() corev1.Container {
	return corev1.Container{Name: "app", Image: "python:3.12-slim", Command: []string{"python", "app.py"}}
}

func shArgv() []string      { return pythonReaders()[0] }
func python3Argv() []string { return pythonReaders()[1] }

func TestTheProcessesAreReadOutOfProcBySh(t *testing.T) {
	procs, others, truncated := parsePythonProcs(readTestdata(t, "python-proc-sh.txt"))
	if len(procs) != 1 || others != 0 || truncated {
		t.Fatalf("%d processes, %d others, truncated %v", len(procs), others, truncated)
	}
	p := procs[0]
	if p.Pid != 1 || p.Executable != "/usr/local/bin/python3.12" || p.Version != "3.12" || p.Comm != "python" {
		t.Errorf("process %+v", p)
	}
	if p.RSS != 24920*1024 || p.Peak != 25020*1024 || p.Threads != 4 || len(p.ThreadNames) != 4 {
		t.Errorf("memory %d / %d, threads %d %v", p.RSS, p.Peak, p.Threads, p.ThreadNames)
	}
	// btime 1795258953 + 45587104 ticks / 100
	if p.Started != "2026-11-26T17:40:24Z" {
		t.Errorf("started %q", p.Started)
	}
}

func TestTheProcessesAreReadOutOfProcByPythonWithoutAShell(t *testing.T) {
	procs, _, _ := parsePythonProcs(readTestdata(t, "python-proc-python3.txt"))
	if len(procs) != 1 || procs[0].Version != "3.11" || procs[0].Executable != "/usr/bin/python3.11" ||
		procs[0].Threads != 1 || procs[0].RSS != 5656*1024 || procs[0].Peak != 8416*1024 {
		t.Fatalf("%+v", procs)
	}
}

// A process is Python by its executable or a mapped libpython (uWSGI); the
// version comes from either; anything else is counted, not listed.
func TestWhatCountsAsAPythonProcess(t *testing.T) {
	out := strings.Join([]string{
		"P 1", "comm uwsgi", "exe /usr/local/bin/uwsgi", "lib /usr/lib/x86_64-linux-gnu/libpython3.11.so.1.0", "Threads: 8",
		"P 7", "comm gunicorn", "exe /usr/local/bin/python3.13", "Threads: 2",
		"P 9", "comm pypy3", "exe /opt/pypy/bin/pypy3.10", "Threads: 1",
		"P 12", "comm nginx", "exe /usr/sbin/nginx",
		"P 13", "comm sh", "exe /bin/dash",
	}, "\n")
	procs, others, _ := parsePythonProcs(out)
	if len(procs) != 3 || others != 2 {
		t.Fatalf("%d processes, %d others: %+v", len(procs), others, procs)
	}
	for i, want := range []string{"3.11", "3.13", "3.10"} {
		if procs[i].Version != want {
			t.Errorf("process %d version %q, want %q", procs[i].Pid, procs[i].Version, want)
		}
	}
	if procs[1].Comm != "gunicorn" {
		t.Errorf("comm %q", procs[1].Comm)
	}
}

func TestTheProcessesAndTheirThreadNamesAreBounded(t *testing.T) {
	var b strings.Builder
	for pid := 1; pid <= pythonProcessLimit+3; pid++ {
		b.WriteString("P " + itoa(pid) + "\nexe /usr/bin/python3.12\n")
	}
	b.WriteString("P 999\nexe /usr/bin/python3.12\n")
	for i := 0; i < pythonThreadNameLimit+5; i++ {
		b.WriteString("thread t\n")
	}
	procs, _, truncated := parsePythonProcs(b.String())
	if len(procs) != pythonProcessLimit || !truncated {
		t.Errorf("%d processes, truncated %v", len(procs), truncated)
	}
	procs, _, _ = parsePythonProcs("P 999\nexe /usr/bin/python3.12\n" + strings.Repeat("thread t\n", pythonThreadNameLimit+5))
	if len(procs[0].ThreadNames) != pythonThreadNameLimit {
		t.Errorf("%d thread names", len(procs[0].ThreadNames))
	}
}

func TestTheReadersArgvIsFixed(t *testing.T) {
	readers := pythonReaders()
	if len(readers) != 2 || strings.Join(readers[0][:2], " ") != "sh -c" || strings.Join(readers[1][:4], " ") != "python3 -I -S -c" {
		t.Fatalf("readers %q", readers)
	}
	for _, argv := range readers {
		script := argv[len(argv)-1]
		if strings.Contains(script, "THREADS") {
			t.Errorf("%s: the thread bound was not filled in", argv[0])
		}
		for _, forbidden := range []string{"kill", "ptrace", "SIGUSR", "environ", "cmdline"} {
			if strings.Contains(script, forbidden) {
				t.Errorf("the %s reader mentions %q: it reads status, stat, maps and comm only", argv[0], forbidden)
			}
		}
	}
	// The python3 reader imports os alone: nothing an image could shadow
	// beyond the standard library, which -I keeps first on sys.path.
	if !strings.HasPrefix(readers[1][4], "import os\n") {
		t.Errorf("the python3 reader starts %q", readers[1][4][:20])
	}
}

// A process that exits while it is read: its files are gone. The sh reader,
// run by every sh this machine has (dash is /bin/sh on Debian images), leaves
// it without a start time and exits 0, rather than failing the route.
func TestTheShReaderToleratesAProcessThatExits(t *testing.T) {
	root := t.TempDir()
	proc := root + "/proc"
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(path[:strings.LastIndexByte(path, '/')], 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, path string) {
		t.Helper()
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	// pid 1: a whole Python process.
	write(proc+"/1/comm", "python\n")
	write(proc+"/1/status", "Name:\tpython\nPPid:\t0\nVmHWM:\t 2048 kB\nVmRSS:\t 1024 kB\nThreads:\t2\n")
	write(proc+"/1/stat", "1 (python) S 0 1 1 0 -1 4194560 1 0 0 0 0 0 0 0 20 0 2 0 5000 0 0\n")
	write(proc+"/1/maps", "7f00-7f01 r-xp 0 08:01 1 /usr/local/lib/libpython3.12.so.1.0\n")
	write(proc+"/1/task/1/comm", "python\n")
	write(proc+"/1/task/2/comm", "worker\n")
	link("/usr/local/bin/python3.12", proc+"/1/exe")
	// pid 7: exited between its exe and the rest.
	if err := os.MkdirAll(proc+"/7", 0o755); err != nil {
		t.Fatal(err)
	}
	link("/usr/local/bin/python3.12", proc+"/7/exe")
	write(proc+"/stat", "cpu 1 2 3\nbtime 1700000000\n")

	script := strings.ReplaceAll(pythonReaders()[0][2], "/proc", proc)
	ran := 0
	for _, shell := range []string{"sh", "dash", "bash"} {
		path, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		ran++
		out, err := exec.Command(path, "-c", script).Output() //nolint:gosec // the reader's own fixed script, on a test tree
		if err != nil {
			t.Errorf("%s: %v", shell, err)
			continue
		}
		procs, _, _ := parsePythonProcs(string(out))
		if len(procs) != 2 || procs[0].Started != "2023-11-14T22:14:10Z" || procs[0].Threads != 2 || procs[0].Version != "3.12" ||
			procs[1].Pid != 7 || procs[1].Started != "" {
			t.Errorf("%s read %+v from:\n%s", shell, procs, out)
		}
	}
	if ran == 0 {
		t.Skip("no sh on this machine")
	}
}

func postPython(t *testing.T, h extension.Host, suffix string) (int, map[string]any) {
	t.Helper()
	rec := post(t, h, "/x/kind/runtime/pod/shop/py-0/app/python"+suffix, "")
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func TestTheProcessesRouteRunsShThenPython3(t *testing.T) {
	h := newHost(t, pythonPod(nil, pythonContainer()))
	h.execs = map[string]execAnswer{
		strings.Join(shArgv(), " "):      {err: extension.ErrNoSuchCommand},
		strings.Join(python3Argv(), " "): {res: extension.ExecResult{Stdout: []byte(readTestdata(t, "python-proc-python3.txt"))}},
	}
	code, body := postPython(t, h, "")
	if code != http.StatusOK || body["reader"] != "python3" {
		t.Fatalf("%d %v", code, body)
	}
	if procs := body["processes"].([]any); len(procs) != 1 {
		t.Errorf("processes %v", procs)
	}
	if len(h.ran) != 2 {
		t.Errorf("ran %d commands", len(h.ran))
	}
}

func TestAContainerWithNeitherShNorPython3SaysSo(t *testing.T) {
	h := newHost(t, pythonPod(nil, pythonContainer()))
	h.execs = map[string]execAnswer{
		strings.Join(shArgv(), " "):      {res: extension.ExecResult{ExitCode: 127}},
		strings.Join(python3Argv(), " "): {err: extension.ErrNoSuchCommand},
	}
	code, body := postPython(t, h, "")
	if code != http.StatusUnprocessableEntity || body["code"] != CodeNoProcessReader {
		t.Fatalf("%d %v", code, body)
	}
}

func TestAReaderThatFailsIsAFailureNotAnEmptyContainer(t *testing.T) {
	h := newHost(t, pythonPod(nil, pythonContainer()))
	h.execs = map[string]execAnswer{
		strings.Join(shArgv(), " "): {res: extension.ExecResult{ExitCode: 2, Stderr: []byte("permission denied")}},
	}
	code, body := postPython(t, h, "")
	if code != http.StatusBadGateway || body["code"] != CodeProcessReadFailed {
		t.Fatalf("%d %v", code, body)
	}
}

func TestAContainerNotReadAsPythonIsRefusedBeforeAnythingRuns(t *testing.T) {
	h := newHost(t, pythonPod(nil, corev1.Container{Name: "app", Image: "registry.example.com/api:1"}))
	for _, suffix := range []string{"", "/metrics"} {
		code, body := postPython(t, h, suffix)
		if code != http.StatusUnprocessableEntity || body["code"] != CodeNotPython {
			t.Errorf("%s: %d %v", suffix, code, body)
		}
	}
	if len(h.ran) != 0 {
		t.Errorf("ran %v", h.ran)
	}
}

func TestPrometheusClientsSeriesAreRead(t *testing.T) {
	m := readPythonMetrics(readTestdata(t, "prometheus-client-0.21.txt"))
	if m.Version != "3.12.16" || m.Implementation != "CPython" {
		t.Errorf("python_info %q %q", m.Version, m.Implementation)
	}
	if len(m.GC) != 3 || m.GC[0].Generation != "0" || *m.GC[0].Collections != 31 || *m.GC[0].Collected != 240 ||
		*m.GC[0].Uncollectable != 0 || *m.GC[1].Collections != 2 {
		t.Errorf("gc %+v", m.GC)
	}
	if m.RSS == nil || *m.RSS != 2.562048e+07 || m.OpenFDs == nil || *m.OpenFDs != 6 || m.MaxFDs == nil ||
		m.CPUSeconds == nil || m.StartTime == nil || m.Virtual == nil {
		t.Errorf("process series %+v", m)
	}
}

// An application that serves /metrics without prometheus_client's default
// collectors has none of these: absent, never zero.
func TestSeriesNotServedAreAbsentNotZero(t *testing.T) {
	m := readPythonMetrics("# HELP app_requests_total x\napp_requests_total 12\n")
	if m.Version != "" || len(m.GC) != 0 || m.RSS != nil || m.OpenFDs != nil {
		t.Errorf("%+v", m)
	}
}

func TestTheMetricsPortIsReadFromTheSpec(t *testing.T) {
	c := pythonContainer()
	if port, source := metricsPort(pythonPod(nil, c), c); port != 8000 || source != "default" {
		t.Errorf("default %d %s", port, source)
	}
	named := c
	named.Ports = []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}, {Name: "metrics", ContainerPort: 9100}}
	if port, source := metricsPort(pythonPod(nil, named), named); port != 9100 || source != "port" {
		t.Errorf("named %d %s", port, source)
	}
	for value, want := range map[string]int{"9200": 9200, "web=1,app=9201": 9201, "app=x": 9100} {
		if port, _ := metricsPort(pythonPod(map[string]string{AnnotationMetricsPort: value}, named), named); port != want {
			t.Errorf("annotation %q = %d, want %d", value, port, want)
		}
	}
}

func metricsServer(t *testing.T, body string, status int) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	asked := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

func TestTheMetricsRouteReadsThroughATunnel(t *testing.T) {
	srv, asked := metricsServer(t, readTestdata(t, "prometheus-client-0.21.txt"), http.StatusOK)
	h := newPodHost(t, srv, pythonPod(nil, pythonContainer()))
	code, body := postPython(t, h, "/metrics")
	if code != http.StatusOK || body["reachable"] != true || body["port"] != 8000.0 || body["source"] != "default" {
		t.Fatalf("%d %v", code, body)
	}
	metrics := body["metrics"].(map[string]any)
	if metrics["version"] != "3.12.16" || len(metrics["gc"].([]any)) != 3 {
		t.Errorf("metrics %v", metrics)
	}
	if len(*asked) != 1 || (*asked)[0] != "GET /metrics" || h.stopped != 1 || len(h.ran) != 0 {
		t.Errorf("asked %v, stopped %d, ran %v", *asked, h.stopped, h.ran)
	}
}

func TestMetricsNotServedAreUnreachableNotEmpty(t *testing.T) {
	srv, _ := metricsServer(t, "not here", http.StatusNotFound)
	h := newPodHost(t, srv, pythonPod(nil, pythonContainer()))
	code, body := postPython(t, h, "/metrics")
	if code != http.StatusOK || body["reachable"] != false || body["metrics"] != nil || !strings.Contains(body["detail"].(string), "404") {
		t.Fatalf("%d %v", code, body)
	}
}

// NaN and ±Inf parse as floats and are no JSON: such a series is not read,
// and the route still answers (#150's review).
func TestANonFiniteSeriesIsNotRead(t *testing.T) {
	m := readPythonMetrics("process_max_fds +Inf\nprocess_open_fds NaN\nprocess_resident_memory_bytes 1024\n")
	if m.MaxFDs != nil || m.OpenFDs != nil || m.RSS == nil {
		t.Fatalf("%+v", m)
	}
	srv, _ := metricsServer(t, "process_max_fds +Inf\npython_gc_collections_total{generation=\"0\"} -Inf\n", http.StatusOK)
	h := newPodHost(t, srv, pythonPod(nil, pythonContainer()))
	if code, body := postPython(t, h, "/metrics"); code != http.StatusOK || body["reachable"] != true {
		t.Fatalf("%d %v", code, body)
	}
}

// The tunnel is the only address /metrics is read from: a redirect to
// another host is still dialled to the pod, and that host is never asked.
func TestMetricsAreReadFromThePodAlone(t *testing.T) {
	elsewhere, asked := metricsServer(t, "python_info{version=\"9.9\"} 1\n", http.StatusOK)
	pod := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metrics":
			http.Redirect(w, r, elsewhere.URL+"/redirected", http.StatusFound)
		case "/redirected":
			_, _ = w.Write([]byte(`python_info{implementation="CPython",version="3.12.1"} 1` + "\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(pod.Close)
	h := newPodHost(t, pod, pythonPod(nil, pythonContainer()))
	code, body := postPython(t, h, "/metrics")
	if code != http.StatusOK || body["reachable"] != true {
		t.Fatalf("%d %v", code, body)
	}
	if version := body["metrics"].(map[string]any)["version"]; version != "3.12.1" {
		t.Errorf("version %v: read from somewhere other than the pod", version)
	}
	if len(*asked) != 0 {
		t.Errorf("another host was asked: %v", *asked)
	}
}

// What /metrics serves past pythonMetricsLimit is not read.
func TestMetricsAreReadUpToTheirLimit(t *testing.T) {
	defer func(limit int) { pythonMetricsLimit = limit }(pythonMetricsLimit)
	head := `python_info{implementation="CPython",version="3.12.1"} 1` + "\n"
	pythonMetricsLimit = len(head) + 64
	body := head + strings.Repeat("# padding\n", 100) + "process_open_fds 6\n"
	m := readPythonMetrics(body)
	if m.OpenFDs == nil {
		t.Fatal("the fixture does not hold the series past the limit")
	}
	srv, _ := metricsServer(t, body, http.StatusOK)
	h := newPodHost(t, srv, pythonPod(nil, pythonContainer()))
	_, answer := postPython(t, h, "/metrics")
	metrics, _ := answer["metrics"].(map[string]any)
	if metrics == nil || metrics["version"] != "3.12.1" || metrics["openFds"] != nil {
		t.Fatalf("read past the limit: %v", answer)
	}
}
