package runtimeinspect

import (
	"bufio"
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thonicdev/kubetower/extension"
)

// Python's diagnostics, read without installing anything in the pod and
// without ptrace.
//
//   - `POST …/{container}/python` reads the container's Python processes out
//     of /proc with a fixed script run by Host.Exec (`sh`, or the container's
//     own `python3` where there is no shell): the interpreter and its
//     version, resident and peak memory, the threads and their names, the
//     start time. It reads files; it sends the processes nothing.
//   - `POST …/{container}/python/metrics` reads the application's
//     prometheus_client series through a tunnel (extension.PodForwarder),
//     where it serves them: python_info, the GC per generation and the
//     process_* series.
//
// A Python stack needs ptrace (py-spy), which a container lacks by default;
// it is not read here. No signal is ever sent: SIGUSR1 and SIGUSR2 end a
// Python process that installed no handler.

// AnnotationMetricsPort names the port an application serves its Prometheus
// metrics on: `8000` for every Python container, or `app=8000,worker=9100`
// per container.
const AnnotationMetricsPort = "kubetower.io/metrics-port"

// pythonDefaultMetricsPort is prometheus_client's start_http_server default.
const pythonDefaultMetricsPort = 8000

// metricsPortNames are the container port names that serve metrics.
var metricsPortNames = []string{"metrics", "http-metrics", "prometheus"}

// The codes these routes add.
const (
	CodeNotPython          = "not_python"
	CodeNoProcessReader    = "no_process_reader"
	CodeProcessReadFailed  = "process_read_failed"
	CodeMetricsUnreachable = "metrics_unreachable"
	CodeMetricsFailed      = "metrics_failed"
)

// Bounds, variables so that the tests can reach them.
var (
	// pythonProcessLimit is the most processes reported, and
	// pythonThreadNameLimit the most thread names each.
	pythonProcessLimit    = 32
	pythonThreadNameLimit = 64
	// pythonMetricsLimit is the most bytes of /metrics read.
	pythonMetricsLimit = 4 << 20
)

// pythonProcScript reads every process of the container out of /proc and
// prints, per process, lines a Go parser reads (parsePythonProcs): `P <pid>`,
// then `comm`, `exe`, the status fields it needs, `start` (field 22 of stat,
// in clock ticks), `lib` (a mapped libpython, if any) and `thread` per
// thread name, then `btime` once. It reads files; it signals nothing. Every
// command it uses is a shell builtin or readlink, so a minimal image runs it.
// A process that exits while it is read leaves its files unreadable: what
// could not be read is left out, and the script goes on and exits 0.
const pythonProcScript = `for d in /proc/[0-9]*; do
  p=${d#/proc/}
  [ "$p" = "$$" ] && continue
  e=$(readlink "$d/exe" 2>/dev/null) || continue
  c=; { read -r c < "$d/comm"; } 2>/dev/null
  echo "P $p"; echo "comm $c"; echo "exe $e"
  { while read -r k v _; do
    case "$k" in PPid:|VmRSS:|VmHWM:|Threads:) echo "$k $v";; esac
  done < "$d/status"; } 2>/dev/null
  s=; { read -r s < "$d/stat"; } 2>/dev/null
  s=${s##*) }; set -- $s
  [ $# -ge 20 ] && { shift 19; echo "start $1"; }
  { while read -r a b c2 d2 e2 f _; do
    case "$f" in *libpython*) echo "lib $f"; break;; esac
  done < "$d/maps"; } 2>/dev/null
  n=0
  for t in "$d"/task/*; do
    n=$((n+1)); [ $n -gt ` + "THREADS" + ` ] && break
    tc=; { read -r tc < "$t/comm"; } 2>/dev/null && echo "thread $tc"
  done
done
{ while read -r k v; do [ "$k" = btime ] && echo "btime $v"; done < /proc/stat; } 2>/dev/null
exit 0
`

// pythonProcPython is the same reading in Python, for an image with no
// shell (distroless): the container's own interpreter reads /proc. It is run
// isolated (`-I -S`): no PYTHON* variable, no site, user site or .pth file,
// no working directory on sys.path, so nothing of the image's own code runs
// (a sitecustomize, an auto-instrumentation, an /app/os.py).
const pythonProcPython = `import os
me=os.getpid()
for p in sorted((int(x) for x in os.listdir('/proc') if x.isdigit())):
    if p==me: continue
    d='/proc/%d'%p
    try: e=os.readlink(d+'/exe')
    except OSError: continue
    try: c=open(d+'/comm').read().strip()
    except OSError: c=''
    print('P',p);print('comm',c);print('exe',e)
    try:
        for l in open(d+'/status'):
            k=l.split()
            if k and k[0] in ('PPid:','VmRSS:','VmHWM:','Threads:'): print(k[0],k[1])
    except OSError: pass
    try: print('start',open(d+'/stat').read().rsplit(') ',1)[1].split()[19])
    except (OSError,IndexError): pass
    try:
        for l in open(d+'/maps'):
            if 'libpython' in l: print('lib',l.split()[-1]); break
    except OSError: pass
    try:
        for t in sorted(os.listdir(d+'/task'))[:THREADS]:
            try: print('thread',open(d+'/task/'+t+'/comm').read().strip())
            except OSError: pass
    except OSError: pass
for l in open('/proc/stat'):
    if l.startswith('btime '): print('btime',l.split()[1])
`

// pythonReaders are the ways the script is run, in order: the next is tried
// where the container has no such program.
func pythonReaders() [][]string {
	threads := strconv.Itoa(pythonThreadNameLimit)
	return [][]string{
		{"sh", "-c", strings.ReplaceAll(pythonProcScript, "THREADS", threads)},
		{"python3", "-I", "-S", "-c", strings.ReplaceAll(pythonProcPython, "THREADS", threads)},
	}
}

// clockTicks is USER_HZ, which Linux fixes at 100 for /proc's tick counts on
// every architecture the console runs against.
const clockTicks = 100

// PythonProcess is one Python process of the container.
type PythonProcess struct {
	Pid  int `json:"pid"`
	PPid int `json:"ppid"`
	// Comm is its name, Executable the program it runs.
	Comm       string `json:"comm"`
	Executable string `json:"executable"`
	// Version is the interpreter's `major.minor`, read from the executable's
	// name or its mapped libpython, empty where neither says.
	Version string `json:"version"`
	// RSS and Peak are resident and peak resident memory, in bytes.
	RSS  int64 `json:"rss"`
	Peak int64 `json:"peak"`
	// Threads is the thread count; ThreadNames the first pythonThreadNameLimit
	// names, as the kernel holds them (15 characters).
	Threads     int      `json:"threads"`
	ThreadNames []string `json:"threadNames"`
	// Started is when it started, RFC 3339, empty where /proc did not say.
	Started string `json:"started,omitempty"`
}

// pythonProcsAnswer is `POST …/python`.
type pythonProcsAnswer struct {
	// Reader is what read /proc: `sh` or `python3`.
	Reader    string          `json:"reader"`
	Processes []PythonProcess `json:"processes"`
	// Others is how many other processes the container runs.
	Others int `json:"others"`
	// Truncated is more Python processes than pythonProcessLimit.
	Truncated bool `json:"truncated"`
}

var versionIn = regexp.MustCompile(`(?:python|libpython|pypy)(\d+\.\d+)`)

// isPythonProcess is a process running Python: its executable is an
// interpreter, or it maps libpython (uWSGI, an embedding program).
func isPythonProcess(executable, lib string) bool {
	base := path.Base(executable)
	return lib != "" || strings.HasPrefix(base, "python") || strings.HasPrefix(base, "pypy")
}

// parsePythonProcs reads the script's lines into the Python processes, and
// counts the others.
func parsePythonProcs(out string) (procs []PythonProcess, others int, truncated bool) {
	type raw struct {
		p     PythonProcess
		lib   string
		start int64
	}
	var all []*raw
	var btime int64
	var cur *raw
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		key, value, _ := strings.Cut(sc.Text(), " ")
		value = strings.TrimSpace(value)
		if key == "P" {
			pid, err := strconv.Atoi(value)
			if err != nil {
				cur = nil
				continue
			}
			cur = &raw{p: PythonProcess{Pid: pid, ThreadNames: []string{}}}
			all = append(all, cur)
			continue
		}
		if key == "btime" {
			btime, _ = strconv.ParseInt(value, 10, 64)
			continue
		}
		if cur == nil {
			continue
		}
		switch key {
		case "comm":
			cur.p.Comm = value
		case "exe":
			cur.p.Executable = strings.TrimSuffix(value, " (deleted)")
		case "PPid:":
			cur.p.PPid, _ = strconv.Atoi(value)
		case "VmRSS:":
			cur.p.RSS = kib(value)
		case "VmHWM:":
			cur.p.Peak = kib(value)
		case "Threads:":
			cur.p.Threads, _ = strconv.Atoi(value)
		case "start":
			cur.start, _ = strconv.ParseInt(value, 10, 64)
		case "lib":
			cur.lib = value
		case "thread":
			if len(cur.p.ThreadNames) < pythonThreadNameLimit {
				cur.p.ThreadNames = append(cur.p.ThreadNames, value)
			}
		}
	}
	for _, r := range all {
		if !isPythonProcess(r.p.Executable, r.lib) {
			others++
			continue
		}
		if m := versionIn.FindStringSubmatch(path.Base(r.p.Executable)); m != nil {
			r.p.Version = m[1]
		} else if m := versionIn.FindStringSubmatch(path.Base(r.lib)); m != nil {
			r.p.Version = m[1]
		}
		if btime > 0 && r.start > 0 {
			r.p.Started = time.Unix(btime+r.start/clockTicks, 0).UTC().Format(time.RFC3339)
		}
		if len(procs) == pythonProcessLimit {
			truncated = true
			continue
		}
		procs = append(procs, r.p)
	}
	sort.Slice(procs, func(i, j int) bool { return procs[i].Pid < procs[j].Pid })
	if procs == nil {
		procs = []PythonProcess{}
	}
	return procs, others, truncated
}

// kib reads `123` (a /proc status figure, in kB) as bytes.
func kib(value string) int64 {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n * 1024
}

// pythonTarget is the Python container a route names, with its pod.
type pythonTarget struct {
	target
	pod *corev1.Pod
}

// resolvePython is resolve, for a container the spec reads as Python (or the
// annotation names as Python).
func resolvePython(w http.ResponseWriter, r *http.Request, h extension.Host) (pythonTarget, bool) {
	t := target{cluster: r.PathValue("cluster"), namespace: r.PathValue("namespace"), name: r.PathValue("name"),
		container: r.PathValue("container")}
	if len(validation.IsDNS1123Label(t.container)) > 0 {
		writeProblem(w, http.StatusBadRequest, CodeBadRequest, quote(t.container)+" is not a container name.", nil)
		return pythonTarget{}, false
	}
	p, ok := lookupPod(w, r, h, t.cluster, t.namespace, t.name)
	if !ok {
		return pythonTarget{}, false
	}
	found := false
	for _, c := range append(append([]corev1.Container(nil), p.Spec.Containers...), p.Spec.InitContainers...) {
		if c.Name == t.container {
			t.spec, found = c, true
			break
		}
	}
	if !found {
		writeProblem(w, http.StatusNotFound, CodeUnknownContainer, "The pod has no container "+quote(t.container)+".", nil)
		return pythonTarget{}, false
	}
	if detect(t.spec, annotatedRuntimes(p.Annotations, p.Spec.Containers)).runtime != runtimePython {
		writeProblem(w, http.StatusUnprocessableEntity, CodeNotPython,
			"Nothing in the spec says "+quote(t.container)+" runs Python; "+AnnotationRuntime+" can say so.", nil)
		return pythonTarget{}, false
	}
	return pythonTarget{target: t, pod: p}, true
}

func (JVM) pythonProcesses(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t, ok := resolvePython(w, r, h)
		if !ok {
			return
		}
		var res extension.ExecResult
		var err error
		reader := ""
		for _, argv := range pythonReaders() {
			reader = argv[0]
			res, err = h.Exec(r, t.cluster, extension.ExecQuery{Namespace: t.namespace, Pod: t.name, Container: t.container,
				Command: argv})
			// An env with no such program on its PATH exits 127.
			if err == nil && res.ExitCode == 127 {
				err = extension.ErrNoSuchCommand
			}
			if !errors.Is(err, extension.ErrNoSuchCommand) {
				break
			}
		}
		if errors.Is(err, extension.ErrNoSuchCommand) {
			writeProblem(w, http.StatusUnprocessableEntity, CodeNoProcessReader,
				"This container has neither sh nor python3 to read its processes with.", nil)
			return
		}
		if err != nil {
			writePythonExecFailure(w, t.cluster, err)
			return
		}
		if res.ExitCode != 0 {
			writeProblem(w, http.StatusBadGateway, CodeProcessReadFailed, "The container's processes could not be read.",
				errors.New(firstLine(strings.TrimSpace(string(res.Stderr)))))
			return
		}
		procs, others, truncated := parsePythonProcs(string(res.Stdout))
		writeJSON(w, http.StatusOK, pythonProcsAnswer{Reader: reader, Processes: procs, Others: others,
			Truncated: truncated || res.Truncated})
	})
}

// writePythonExecFailure is writeExecFailure, worded for reading /proc.
func writePythonExecFailure(w http.ResponseWriter, cluster string, err error) {
	switch {
	case errors.Is(err, extension.ErrUnknownCluster):
		writeProblem(w, http.StatusNotFound, CodeUnknownCluster, extension.UnknownClusterText(cluster), err)
	case errors.Is(err, context.Canceled):
	case errors.Is(err, extension.ErrReadOnly):
		writeReadOnly(w, cluster, err)
	case errors.Is(err, extension.ErrDenied):
		writeProblem(w, http.StatusForbidden, CodeExecForbidden, execDeniedText, err)
	case errors.Is(err, extension.ErrUnreviewable):
		writeProblem(w, http.StatusServiceUnavailable, CodeExecUnreviewed,
			"The console could not ask the cluster whether you may run commands in this pod, so it ran none. Try again.", err)
	case errors.Is(err, extension.ErrExecBusy):
		writeProblem(w, http.StatusTooManyRequests, CodeExecBusy,
			"Too many commands run by extensions are already running; try again in a moment.", err)
	case errors.Is(err, extension.ErrExecTimedOut):
		writeProblem(w, http.StatusGatewayTimeout, CodeExecTimedOut,
			"Reading the container's processes did not finish within its deadline and was stopped.", err)
	default:
		writeProblem(w, http.StatusBadGateway, CodeExecFailed, "The container's processes could not be read.", err)
	}
}

// --- prometheus_client's series ---------------------------------------------

// metricsPort is where c of p serves its metrics, and what says so: the
// annotation, a container port named for metrics, or prometheus_client's
// default.
func metricsPort(p *corev1.Pod, c corev1.Container) (int, string) {
	if value := strings.TrimSpace(p.Annotations[AnnotationMetricsPort]); value != "" {
		bare := 0
		for _, part := range strings.Split(value, ",") {
			name, port, named := strings.Cut(strings.TrimSpace(part), "=")
			if !named {
				if n, err := strconv.Atoi(name); err == nil && n > 0 && n < 65536 {
					bare = n
				}
				continue
			}
			if strings.TrimSpace(name) == c.Name {
				if n, err := strconv.Atoi(strings.TrimSpace(port)); err == nil && n > 0 && n < 65536 {
					return n, "annotation"
				}
			}
		}
		if bare > 0 {
			return bare, "annotation"
		}
	}
	for _, want := range metricsPortNames {
		for _, port := range c.Ports {
			if port.Name == want && port.ContainerPort > 0 {
				return int(port.ContainerPort), "port"
			}
		}
	}
	return pythonDefaultMetricsPort, "default"
}

// PythonGC is one generation of the collector, as prometheus_client counts it.
type PythonGC struct {
	Generation    string   `json:"generation"`
	Collections   *float64 `json:"collections"`
	Collected     *float64 `json:"collected"`
	Uncollectable *float64 `json:"uncollectable"`
}

// PythonMetrics are the series read; a member is null where the application
// does not serve it, never a zero standing for one.
type PythonMetrics struct {
	Version        string     `json:"version,omitempty"`
	Implementation string     `json:"implementation,omitempty"`
	GC             []PythonGC `json:"gc"`
	RSS            *float64   `json:"rss"`
	Virtual        *float64   `json:"virtual"`
	CPUSeconds     *float64   `json:"cpuSeconds"`
	OpenFDs        *float64   `json:"openFds"`
	MaxFDs         *float64   `json:"maxFds"`
	StartTime      *float64   `json:"startTime"`
}

// pythonMetricsAnswer is `POST …/python/metrics`.
type pythonMetricsAnswer struct {
	Port   int    `json:"port"`
	Source string `json:"source"`
	// Reachable is /metrics answering 200; Detail why not.
	Reachable bool   `json:"reachable"`
	Detail    string `json:"detail,omitempty"`
	// Metrics is what was read, null where /metrics did not answer.
	Metrics *PythonMetrics `json:"metrics"`
}

// sample is one line of the text exposition format.
type expoSample struct {
	name   string
	labels map[string]string
	value  float64
}

var labelPair = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\.)*)"`)

// parseExposition reads the samples of the families wanted out of the text
// exposition format; everything else, comments included, is skipped.
func parseExposition(body string, wanted map[string]bool) []expoSample {
	var out []expoSample
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, rest := line, ""
		labels := map[string]string{}
		if i := strings.IndexByte(line, '{'); i >= 0 {
			j := strings.LastIndexByte(line, '}')
			if j < i {
				continue
			}
			name, rest = line[:i], strings.TrimSpace(line[j+1:])
			for _, m := range labelPair.FindAllStringSubmatch(line[i+1:j], -1) {
				labels[m[1]] = strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\n`, "\n").Replace(m[2])
			}
		} else if fields := strings.Fields(line); len(fields) >= 2 {
			name, rest = fields[0], strings.Join(fields[1:], " ")
		}
		if !wanted[name] {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		value, err := strconv.ParseFloat(fields[0], 64)
		// NaN and ±Inf parse, and no JSON carries them: a series without a
		// finite value is a series not read (process_max_fds +Inf).
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		out = append(out, expoSample{name: name, labels: labels, value: value})
	}
	return out
}

// pythonFamilies are prometheus_client's own families this reads.
var pythonFamilies = map[string]bool{
	"python_info":                           true,
	"python_gc_collections_total":           true,
	"python_gc_objects_collected_total":     true,
	"python_gc_objects_uncollectable_total": true,
	"process_resident_memory_bytes":         true,
	"process_virtual_memory_bytes":          true,
	"process_cpu_seconds_total":             true,
	"process_open_fds":                      true,
	"process_max_fds":                       true,
	"process_start_time_seconds":            true,
}

// readPythonMetrics turns the exposition into PythonMetrics.
func readPythonMetrics(body string) *PythonMetrics {
	out := &PythonMetrics{GC: []PythonGC{}}
	gens := map[string]*PythonGC{}
	gen := func(g string) *PythonGC {
		if gens[g] == nil {
			gens[g] = &PythonGC{Generation: g}
		}
		return gens[g]
	}
	for _, s := range parseExposition(body, pythonFamilies) {
		v := s.value
		switch s.name {
		case "python_info":
			out.Implementation = s.labels["implementation"]
			out.Version = s.labels["version"]
		case "python_gc_collections_total":
			gen(s.labels["generation"]).Collections = &v
		case "python_gc_objects_collected_total":
			gen(s.labels["generation"]).Collected = &v
		case "python_gc_objects_uncollectable_total":
			gen(s.labels["generation"]).Uncollectable = &v
		case "process_resident_memory_bytes":
			out.RSS = &v
		case "process_virtual_memory_bytes":
			out.Virtual = &v
		case "process_cpu_seconds_total":
			out.CPUSeconds = &v
		case "process_open_fds":
			out.OpenFDs = &v
		case "process_max_fds":
			out.MaxFDs = &v
		case "process_start_time_seconds":
			out.StartTime = &v
		}
	}
	names := make([]string, 0, len(gens))
	for g := range gens {
		names = append(names, g)
	}
	sort.Strings(names)
	for _, g := range names {
		out.GC = append(out.GC, *gens[g])
	}
	return out
}

// metricsGet asks /metrics through tunnel.
func metricsGet(ctx context.Context, tunnel extension.Tunnel) (string, error) {
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", tunnel.Addr)
		},
		DisableKeepAlives: true,
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/metrics", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(pythonMetricsLimit)))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("/metrics answered " + resp.Status)
	}
	return string(body), nil
}

func (JVM) pythonMetrics(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t, ok := resolvePython(w, r, h)
		if !ok {
			return
		}
		port, source := metricsPort(t.pod, t.spec)
		tunnel, ok := tunnelTo(w, r, h, t.cluster, t.namespace, t.name, port)
		if !ok {
			return
		}
		defer tunnel.Stop()
		ctx, cancel := context.WithTimeout(r.Context(), pprofProbeBudget)
		defer cancel()
		answer := pythonMetricsAnswer{Port: port, Source: source}
		body, err := metricsGet(ctx, tunnel)
		if err != nil {
			answer.Detail = firstLine(err.Error())
		} else {
			answer.Reachable = true
			answer.Metrics = readPythonMetrics(body)
		}
		writeJSON(w, http.StatusOK, answer)
	})
}
