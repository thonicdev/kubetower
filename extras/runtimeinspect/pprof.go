package runtimeinspect

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// Go's diagnostics, read from net/http/pprof where the application serves
// it, through a tunnel to the pod's port (extension.PodForwarder).
//
// **Nothing is sent to the process but a GET of pprof's own endpoints**, from
// a fixed list held here: the routes take which pod, which container and,
// for a timed profile, a duration from a fixed list - never a path. No signal
// is ever sent (SIGQUIT ends a Go program after its dump) and no debugger
// attaches (delve stops it).
//
//   - `GET …/{container}/pprof` finds the port and asks `/debug/pprof/`: the
//     state the tab draws its buttons from;
//   - `POST …/{container}/pprof/goroutines` reads `goroutine?debug=2` and
//     answers it grouped by state and stack;
//   - `POST …/{container}/pprof/{heap|profile|trace}` answers the profile as
//     a download, for `go tool pprof` and `go tool trace`.

// AnnotationPprofPort names the port pprof listens on: `6060` for every Go
// container, or `app=6060,worker=6061` per container.
const AnnotationPprofPort = "kubetower.io/pprof-port"

// pprofDefaultPort is net/http/pprof's convention, the last guess.
const pprofDefaultPort = 6060

// The codes these routes add.
const (
	CodeNoTunnel         = "no_tunnel"
	CodeTunnelForbidden  = "tunnel_forbidden"
	CodeTunnelUnreviewed = "tunnel_unreviewed"
	CodeTunnelFailed     = "tunnel_failed"
	CodePprofUnreachable = "pprof_unreachable"
	CodePprofFailed      = "pprof_failed"
	CodeBadProfile       = "bad_profile"
	CodeNotGo            = "not_go"
)

// pprofProfile is one download this extra may ask for.
type pprofProfile struct {
	path    string
	ext     string
	seconds []int
}

// pprofProfiles are the downloads, by the route's last segment. A duration
// outside a profile's list is refused.
var pprofProfiles = map[string]pprofProfile{
	"heap":    {path: "heap", ext: ".heap.pb.gz"},
	"profile": {path: "profile", ext: ".cpu.pb.gz", seconds: []int{10, 30, 60}},
	"trace":   {path: "trace", ext: ".trace", seconds: []int{5, 10}},
}

// Variables so that the tests can reach them in milliseconds and bytes.
var (
	// pprofProbeBudget bounds the probe and the goroutine read.
	pprofProbeBudget = 15 * time.Second
	// pprofGoroutineLimit is the most bytes of a goroutine dump read: past
	// it the dump is cut and the answer says so.
	pprofGoroutineLimit = 16 << 20
	// pprofDownloadLimit is the most bytes a profile download carries: a
	// larger profile fails rather than arrive cut.
	pprofDownloadLimit int64 = 256 << 20
)

// pprofPort is where pprof listens in c of p, and what says so: the
// annotation, a container port named `pprof`, or the convention.
func pprofPort(p *corev1.Pod, c corev1.Container) (int, string) {
	if value := strings.TrimSpace(p.Annotations[AnnotationPprofPort]); value != "" {
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
	for _, port := range c.Ports {
		if port.Name == "pprof" && port.ContainerPort > 0 {
			return int(port.ContainerPort), "port"
		}
	}
	return pprofDefaultPort, "default"
}

// pprofAnswer is `GET …/pprof`.
type pprofAnswer struct {
	Port int `json:"port"`
	// Source is what named the port: `annotation`, `port` or `default`.
	Source string `json:"source"`
	// Reachable is whether `/debug/pprof/` answered 200 through the tunnel.
	Reachable bool `json:"reachable"`
	// Detail is why not, one line.
	Detail string `json:"detail,omitempty"`
}

// goroutineGroup is goroutines in one state with one stack.
type goroutineGroup struct {
	State  string   `json:"state"`
	Count  int      `json:"count"`
	Frames []string `json:"frames"`
	// Longest is the longest wait among them, as the dump says it
	// (`5 minutes`), empty where none waits.
	Longest string `json:"longest,omitempty"`
}

// goroutinesAnswer is `POST …/pprof/goroutines`.
type goroutinesAnswer struct {
	Total  int              `json:"total"`
	Groups []goroutineGroup `json:"groups"`
	// Truncated is a dump cut at pprofGoroutineLimit: the groups are those
	// of what was read.
	Truncated bool `json:"truncated"`
	// Raw is the dump as read, for the download.
	Raw string `json:"raw"`
}

// goTarget is the Go container a pprof route names, with its pod and port.
type goTarget struct {
	target
	port   int
	source string
}

// resolveGo is resolve, for a container the spec reads as Go (or the
// annotation names as Go), with its pprof port.
func resolveGo(w http.ResponseWriter, r *http.Request, h extension.Host) (goTarget, bool) {
	t := target{cluster: r.PathValue("cluster"), namespace: r.PathValue("namespace"), name: r.PathValue("name"),
		container: r.PathValue("container")}
	if len(validation.IsDNS1123Label(t.container)) > 0 {
		writeProblem(w, http.StatusBadRequest, CodeBadRequest, quote(t.container)+" is not a container name.", nil)
		return goTarget{}, false
	}
	p, ok := lookupPod(w, r, h, t.cluster, t.namespace, t.name)
	if !ok {
		return goTarget{}, false
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
		return goTarget{}, false
	}
	if detect(t.spec, annotatedRuntimes(p.Annotations, p.Spec.Containers)).runtime != runtimeGo {
		writeProblem(w, http.StatusUnprocessableEntity, CodeNotGo,
			"Nothing in the spec says "+quote(t.container)+" runs Go; "+AnnotationRuntime+" can say so.", nil)
		return goTarget{}, false
	}
	port, source := pprofPort(p, t.spec)
	return goTarget{target: t, port: port, source: source}, true
}

// tunnelTo opens the tunnel to port of the pod, or answers w.
func tunnelTo(w http.ResponseWriter, r *http.Request, h extension.Host, cluster, namespace, pod string, port int) (extension.Tunnel, bool) {
	pods, ok := h.(extension.PodForwarder)
	if !ok {
		writeProblem(w, http.StatusNotImplemented, CodeNoTunnel, "This console cannot open a tunnel to a pod.", nil)
		return extension.Tunnel{}, false
	}
	tunnel, err := pods.ForwardPod(r, cluster, namespace, pod, port)
	if err != nil {
		writeTunnelFailure(w, cluster, err)
		return extension.Tunnel{}, false
	}
	return tunnel, true
}

func writeTunnelFailure(w http.ResponseWriter, cluster string, err error) {
	switch {
	case errors.Is(err, extension.ErrUnknownCluster):
		writeProblem(w, http.StatusNotFound, CodeUnknownCluster, extension.UnknownClusterText(cluster), err)
	case errors.Is(err, context.Canceled):
	case errors.Is(err, extension.ErrDenied):
		writeProblem(w, http.StatusForbidden, CodeTunnelForbidden,
			"The cluster says you may not port-forward to this pod.", err)
	case errors.Is(err, extension.ErrUnreviewable):
		writeProblem(w, http.StatusServiceUnavailable, CodeTunnelUnreviewed,
			"The console could not ask the cluster whether you may port-forward to this pod, so it opened nothing. Try again.", err)
	default:
		writeProblem(w, http.StatusBadGateway, CodeTunnelFailed, "The tunnel to the pod could not be opened.", err)
	}
}

// pprofGet asks path of pprof through tunnel, with the budget ctx carries.
func pprofGet(ctx context.Context, tunnel extension.Tunnel, path string) (*http.Response, error) {
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", tunnel.Addr)
		},
		DisableKeepAlives: true,
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/debug/pprof/"+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		return nil, &pprofAnswered{path: path, status: resp.Status, body: firstLine(strings.TrimSpace(string(body)))}
	}
	return resp, nil
}

// pprofAnswered is pprof answering with something else than 200: a 500
// "cpu profiling already in use" is an answer, not a silence.
type pprofAnswered struct{ path, status, body string }

func (e *pprofAnswered) Error() string {
	msg := "/debug/pprof/" + e.path + " answered " + e.status
	if e.body != "" {
		msg += ": " + e.body
	}
	return msg
}

// writePprofError tells pprof answering with an error from pprof not
// answering at all.
func writePprofError(w http.ResponseWriter, port int, err error) {
	var answered *pprofAnswered
	if errors.As(err, &answered) {
		msg := "pprof on port " + strconv.Itoa(port) + " answered " + answered.status
		if answered.body != "" {
			msg += ": " + answered.body
		}
		writeProblem(w, http.StatusBadGateway, CodePprofFailed, msg+".", err)
		return
	}
	writeProblem(w, http.StatusBadGateway, CodePprofUnreachable, "pprof did not answer on port "+strconv.Itoa(port)+".", err)
}

func (JVM) pprofState(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g, ok := resolveGo(w, r, h)
		if !ok {
			return
		}
		tunnel, ok := tunnelTo(w, r, h, g.cluster, g.namespace, g.name, g.port)
		if !ok {
			return
		}
		defer tunnel.Stop()
		ctx, cancel := context.WithTimeout(r.Context(), pprofProbeBudget)
		defer cancel()
		answer := pprofAnswer{Port: g.port, Source: g.source}
		resp, err := pprofGet(ctx, tunnel, "")
		if err != nil {
			answer.Detail = firstLine(err.Error())
		} else {
			_ = resp.Body.Close()
			answer.Reachable = true
		}
		writeJSON(w, http.StatusOK, answer)
	})
}

func (JVM) pprofGoroutines(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g, ok := resolveGo(w, r, h)
		if !ok {
			return
		}
		tunnel, ok := tunnelTo(w, r, h, g.cluster, g.namespace, g.name, g.port)
		if !ok {
			return
		}
		defer tunnel.Stop()
		ctx, cancel := context.WithTimeout(r.Context(), pprofProbeBudget)
		defer cancel()
		resp, err := pprofGet(ctx, tunnel, "goroutine?debug=2")
		if err != nil {
			writePprofError(w, g.port, err)
			return
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(pprofGoroutineLimit)+1))
		if err != nil {
			writeProblem(w, http.StatusBadGateway, CodePprofFailed, "The goroutine dump could not be read.", err)
			return
		}
		truncated := len(raw) > pprofGoroutineLimit
		if truncated {
			raw = raw[:pprofGoroutineLimit]
		}
		groups, total, cut := parseGoroutines(string(raw))
		truncated = truncated || cut
		writeJSON(w, http.StatusOK, goroutinesAnswer{Total: total, Groups: groups, Truncated: truncated, Raw: string(raw)})
	})
}

func (JVM) pprofDownload(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		profile, known := pprofProfiles[r.PathValue("profile")]
		if !known {
			writeProblem(w, http.StatusBadRequest, CodeBadProfile, quote(r.PathValue("profile"))+" is not a profile this page downloads.", nil)
			return
		}
		seconds := 0
		if len(profile.seconds) > 0 {
			n, err := strconv.Atoi(r.URL.Query().Get("seconds"))
			if err != nil || !contains(profile.seconds, n) {
				writeProblem(w, http.StatusBadRequest, CodeBadProfile, fmt.Sprintf("seconds is one of %v.", profile.seconds), nil)
				return
			}
			seconds = n
		}
		g, ok := resolveGo(w, r, h)
		if !ok {
			return
		}
		tunnel, ok := tunnelTo(w, r, h, g.cluster, g.namespace, g.name, g.port)
		if !ok {
			return
		}
		defer tunnel.Stop()
		path := profile.path
		if seconds > 0 {
			path += "?seconds=" + strconv.Itoa(seconds)
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(seconds)*time.Second+pprofProbeBudget)
		defer cancel()
		resp, err := pprofGet(ctx, tunnel, path)
		if err != nil {
			writePprofError(w, g.port, err)
			return
		}
		defer resp.Body.Close()
		if resp.ContentLength > pprofDownloadLimit {
			writeProblem(w, http.StatusBadGateway, CodePprofFailed,
				fmt.Sprintf("The profile is larger than %d MiB.", pprofDownloadLimit>>20), nil)
			return
		}
		hd := w.Header()
		hd.Set("Content-Type", "application/octet-stream")
		hd.Set("Content-Disposition", `attachment; filename="`+g.name+"-"+g.container+"-"+
			time.Now().UTC().Format("20060102-150405")+profile.ext+`"`)
		hd.Set("Cache-Control", "no-store")
		hd.Set("X-Content-Type-Options", "nosniff")
		if resp.ContentLength >= 0 {
			hd.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
		}
		n, err := io.Copy(w, io.LimitReader(resp.Body, pprofDownloadLimit))
		if err == nil && n == pprofDownloadLimit {
			var one [1]byte
			if k, _ := io.ReadFull(resp.Body, one[:]); k > 0 {
				err = fmt.Errorf("the profile is larger than %d MiB", pprofDownloadLimit>>20)
			}
		}
		if err != nil {
			// The status is sent: end the response broken, so the download
			// fails rather than leave a cut file that looks whole.
			panic(http.ErrAbortHandler)
		}
	})
}

func contains(list []int, n int) bool {
	for _, v := range list {
		if v == n {
			return true
		}
	}
	return false
}

func firstLine(s string) string { return textutil.FirstLineN(s, 80) }

// parseGoroutines groups a `goroutine?debug=2` dump by state and stack (the
// functions, without their arguments and lines), the largest groups first,
// and counts every goroutine read. cut is a line too long to read (over
// 1 MiB): the dump past it is not counted.
//
//	goroutine 18 [chan receive, 5 minutes]:
//	main.worker(0xc000010000)
//		/src/main.go:42 +0x45
//	created by main.main in goroutine 1
//		/src/main.go:20 +0x2a
func parseGoroutines(dump string) ([]goroutineGroup, int, bool) {
	type key struct{ state, stack string }
	groups := map[key]*goroutineGroup{}
	var order []key
	total := 0
	scanner := bufio.NewScanner(strings.NewReader(dump))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	var state, wait string
	var frames []string
	in := false
	flush := func() {
		if !in {
			return
		}
		total++
		k := key{state, strings.Join(frames, "\n")}
		g := groups[k]
		if g == nil {
			g = &goroutineGroup{State: state, Frames: append([]string{}, frames...)}
			groups[k] = g
			order = append(order, k)
		}
		g.Count++
		if wait != "" && longer(wait, g.Longest) {
			g.Longest = wait
		}
		in, frames = false, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if rest, ok := strings.CutPrefix(line, "goroutine "); ok && strings.HasSuffix(line, "]:") {
			flush()
			open := strings.Index(rest, "[")
			inside := strings.TrimSuffix(rest[open+1:], "]:")
			state, wait, _ = strings.Cut(inside, ", ")
			wait = strings.TrimSpace(wait)
			in = true
			continue
		}
		if !in || line == "" || strings.HasPrefix(line, "\t") {
			continue
		}
		fn := line
		if strings.HasPrefix(fn, "created by ") {
			fn = strings.TrimSuffix(strings.SplitN(fn, " in goroutine ", 2)[0], " ")
		} else if open := strings.LastIndex(fn, "("); open > 0 {
			fn = fn[:open]
		}
		frames = append(frames, fn)
	}
	flush()
	out := make([]goroutineGroup, 0, len(order))
	for _, k := range order {
		out = append(out, *groups[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out, total, scanner.Err() != nil
}

// longer is whether wait a ("5 minutes") is longer than b; an empty b is
// shorter than anything.
func longer(a, b string) bool {
	return minutes(a) > minutes(b)
}

func minutes(wait string) int {
	n, _, _ := strings.Cut(wait, " ")
	v, err := strconv.Atoi(n)
	if err != nil {
		return -1
	}
	return v
}
