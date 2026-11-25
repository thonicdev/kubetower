package runtimeinspect

import (
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

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/safego"
)

// Node.js's diagnostics, through its inspector (the Chrome DevTools Protocol)
// reached by a tunnel to the pod (extension.PodForwarder).
//
// **Opening the inspector is the one act here that changes the process**, and
// it is confirmed, and it stays: the inspector listens on the pod's loopback
// until the process restarts, and anyone who reaches that port can run code in
// the process. It is opened the way `node inspect -p` does,
// `process._debugProcess(pid)` (SIGUSR1), by a fixed script run with
// Host.Exec - reviewed as `create pods/exec` and recorded. A process started
// with `--disable-sigusr1` refuses it. **No SIGUSR2 is ever sent**: without
// `--report-on-signal` it ends the process.
//
// Once open, three reads, each a fixed DevTools method:
//
//   - a heap snapshot (`HeapProfiler.takeHeapSnapshot`), confirmed: it stops
//     the process while it is written and can double its memory;
//   - a CPU profile (`Profiler.start`, `Profiler.stop`) of 10, 30 or 60 s;
//   - the diagnostic report (`process.report.getReport()` through
//     `Runtime.evaluate` of a fixed expression), its secret-looking
//     environment values masked.
//
// No expression comes from the client.

// nodeDefaultInspectPort is the inspector's port when nothing sets another.
const nodeDefaultInspectPort = 9229

// The codes these routes add.
const (
	CodeNotNode           = "not_node"
	CodeNoNode            = "no_node"
	CodeSigusr1Disabled   = "sigusr1_disabled"
	CodeInspectorClosed   = "inspector_closed"
	CodeInspectorRefused  = "inspector_refused"
	CodeInspectorFailed   = "inspector_failed"
	CodeChooseProcess     = "choose_process"
	CodeNoNodeProcess     = "no_node_process"
	CodeBadInspectorQuery = "bad_inspector_query"
)

// nodeOpenScript opens the inspector of the one Node.js process of the
// container, or of the pid its first argument names among them, and says
// what it did as one JSON line. A process is Node.js by its name (comm) or,
// when it set process.title (Next.js's next-server), by its executable -
// except a package manager's (npm start, npx, pnpm, yarn), which runs on
// node too and only launches the application. It reads /proc and calls
// process._debugProcess; it sends nothing else.
const nodeOpenScript = `const fs=require('fs');` +
	`const self=process.pid,parent=process.ppid,want=Number(process.argv[1]||0);` +
	`const pids=fs.readdirSync('/proc').filter(d=>/^[0-9]+$/.test(d)).map(Number)` +
	`.filter(p=>p!==self&&p!==parent).filter(p=>{const n=x=>x==='node'||x==='nodejs';let c='';` +
	`try{c=fs.readFileSync('/proc/'+p+'/comm','utf8').trim()}catch(e){}` +
	`if(n(c))return true;if(/^(npm|npx|pnpm|yarn)( |$)/.test(c))return false;` +
	`try{return n(require('path').basename(fs.readlinkSync('/proc/'+p+'/exe')))}catch(e){return false}});` +
	`const pick=want?(pids.includes(want)?want:0):(pids.length===1?pids[0]:0);` +
	`if(!pick){console.log(JSON.stringify({state:'choose',pids}));process.exit(0)}` +
	`process._debugProcess(pick);console.log(JSON.stringify({state:'signalled',pid:pick}));`

// nodeLaunchers are the ways the script is started, tried in turn while the
// program is missing: without NODE_OPTIONS (an `--inspect` there would make
// the helper open an inspector of its own, a `--require` would load the
// application's hooks), then `node` as is, then distroless's path.
var nodeLaunchers = [][]string{
	{"env", "-u", "NODE_OPTIONS", "node"},
	{"node"},
	{"/nodejs/bin/node"},
}

// nodeOpenArgv is the argv of launcher with the script, and the pid when one
// was chosen.
func nodeOpenArgv(launcher []string, pid int) []string {
	argv := append(append([]string{}, launcher...), "-e", nodeOpenScript)
	if pid > 0 {
		argv = append(argv, strconv.Itoa(pid))
	}
	return argv
}

// nodeFlags is what the spec says about the inspector: the port it listens
// on and what set it, whether the process starts with it open, and whether
// SIGUSR1 is disabled.
type nodeFlags struct {
	port      int
	source    string
	started   bool
	noSigusr1 bool
}

func readNodeFlags(c corev1.Container) nodeFlags {
	f := nodeFlags{port: nodeDefaultInspectPort, source: "default"}
	read := func(tokens []string, source string) {
		for _, token := range tokens {
			name, value, _ := strings.Cut(token, "=")
			switch name {
			case "--inspect", "--inspect-brk", "--inspect-wait":
				f.started = true
				if value != "" {
					f.setPort(value, source)
				}
			case "--inspect-port", "--debug-port":
				if value != "" {
					f.setPort(value, source)
				}
			case "--disable-sigusr1":
				f.noSigusr1 = true
			}
		}
	}
	read(commandLine(c), "command")
	for _, e := range c.Env {
		if e.Name == "NODE_OPTIONS" {
			read(strings.Fields(e.Value), "env")
		}
	}
	return f
}

// setPort reads `9230` or `0.0.0.0:9230`.
func (f *nodeFlags) setPort(value, source string) {
	if i := strings.LastIndex(value, ":"); i >= 0 {
		value = value[i+1:]
	}
	if n, err := strconv.Atoi(value); err == nil && n > 0 && n < 65536 {
		f.port, f.source = n, source
	}
}

// nodeAnswer is `POST …/node`.
type nodeAnswer struct {
	Port int `json:"port"`
	// Source is what set the port: `command`, `env` or `default`.
	Source string `json:"source"`
	// Started is the process started with `--inspect`.
	Started bool `json:"started"`
	// Sigusr1Disabled is `--disable-sigusr1`: the inspector cannot be opened.
	Sigusr1Disabled bool `json:"sigusr1Disabled"`
	// Open is whether the inspector answered.
	Open bool `json:"open"`
	// Detail is why not, one line.
	Detail string `json:"detail,omitempty"`
	// Pid is the process just signalled, on the open route's answer.
	Pid int `json:"pid,omitempty"`
}

// nodeTarget is the Node.js container a route names.
type nodeTarget struct {
	target
	flags nodeFlags
}

func resolveNode(w http.ResponseWriter, r *http.Request, h extension.Host) (nodeTarget, bool) {
	t := target{cluster: r.PathValue("cluster"), namespace: r.PathValue("namespace"), name: r.PathValue("name"),
		container: r.PathValue("container")}
	if len(validation.IsDNS1123Label(t.container)) > 0 {
		writeProblem(w, http.StatusBadRequest, CodeBadRequest, quote(t.container)+" is not a container name.", nil)
		return nodeTarget{}, false
	}
	p, ok := lookupPod(w, r, h, t.cluster, t.namespace, t.name)
	if !ok {
		return nodeTarget{}, false
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
		return nodeTarget{}, false
	}
	if detect(t.spec, annotatedRuntimes(p.Annotations, p.Spec.Containers)).runtime != runtimeNode {
		writeProblem(w, http.StatusUnprocessableEntity, CodeNotNode,
			"Nothing in the spec says "+quote(t.container)+" runs Node.js; "+AnnotationRuntime+" can say so.", nil)
		return nodeTarget{}, false
	}
	return nodeTarget{target: t, flags: readNodeFlags(t.spec)}, true
}

// inspectorTarget is one entry of the inspector's `/json/list`.
type inspectorTarget struct {
	Type string `json:"type"`
	URL  string `json:"webSocketDebuggerUrl"`
}

// inspectorList asks `/json/list` through tunnel: the process's debugging
// targets, or why there are none.
func inspectorList(ctx context.Context, tunnel extension.Tunnel) ([]inspectorTarget, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+tunnel.Addr+"/json/list", nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("/json/list answered %s", resp.Status)
	}
	var targets []inspectorTarget
	if err := json.NewDecoder(io.LimitReader(resp.Body, inspectorListLimit)).Decode(&targets); err != nil {
		return nil, fmt.Errorf("/json/list is not the inspector's: %w", err)
	}
	if len(targets) == 0 {
		return nil, errors.New("the inspector lists no target")
	}
	return targets, nil
}

func (JVM) nodeState(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, ok := resolveNode(w, r, h)
		if !ok {
			return
		}
		answer := nodeAnswer{Port: n.flags.port, Source: n.flags.source, Started: n.flags.started,
			Sigusr1Disabled: n.flags.noSigusr1}
		tunnel, ok := tunnelTo(w, r, h, n.cluster, n.namespace, n.name, n.flags.port)
		if !ok {
			return
		}
		defer tunnel.Stop()
		ctx, cancel := context.WithTimeout(r.Context(), pprofProbeBudget)
		defer cancel()
		if _, err := inspectorList(ctx, tunnel); err != nil {
			answer.Detail = firstLine(err.Error())
		} else {
			answer.Open = true
		}
		writeJSON(w, http.StatusOK, answer)
	})
}

// nodeOpenBudget is how long the inspector is waited for once signalled.
const nodeOpenBudget = 5 * time.Second

func (JVM) nodeOpen(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("confirm") != h.ConfirmToken(r) {
			writeProblem(w, http.StatusBadRequest, CodeNotConfirmed, "The inspector is opened only once it is confirmed.", nil)
			return
		}
		pid := 0
		if raw := r.URL.Query().Get("pid"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n <= 0 {
				writeProblem(w, http.StatusBadRequest, CodeBadInspectorQuery, quote(raw)+" is not a process id.", nil)
				return
			}
			pid = n
		}
		n, ok := resolveNode(w, r, h)
		if !ok {
			return
		}
		if n.flags.noSigusr1 {
			writeProblem(w, http.StatusUnprocessableEntity, CodeSigusr1Disabled,
				"The process starts with --disable-sigusr1, so its inspector cannot be opened.", nil)
			return
		}
		var res extension.ExecResult
		var err error
		for _, launcher := range nodeLaunchers {
			res, err = h.Exec(r, n.cluster, extension.ExecQuery{Namespace: n.namespace, Pod: n.name, Container: n.container,
				Command: nodeOpenArgv(launcher, pid)})
			// env present with no node on its PATH exits 127 rather than
			// failing to start: the next launcher is tried as well.
			if err == nil && res.ExitCode == 127 {
				err = extension.ErrNoSuchCommand
			}
			if !errors.Is(err, extension.ErrNoSuchCommand) {
				break
			}
		}
		if errors.Is(err, extension.ErrNoSuchCommand) {
			writeProblem(w, http.StatusUnprocessableEntity, CodeNoNode, "There is no node program in this container to open its inspector with.", nil)
			return
		}
		if err != nil {
			writeNodeExecFailure(w, n.cluster, err)
			return
		}
		var said struct {
			State string `json:"state"`
			Pid   int    `json:"pid"`
			Pids  []int  `json:"pids"`
		}
		if res.ExitCode != 0 || json.Unmarshal([]byte(strings.TrimSpace(string(res.Stdout))), &said) != nil {
			writeProblem(w, http.StatusBadGateway, CodeInspectorFailed, "The inspector could not be opened.",
				errors.New(firstLine(strings.TrimSpace(string(res.Stderr)+" "+string(res.Stdout)))))
			return
		}
		// A chosen pid the script did not find has exited: said in one line,
		// with the processes left to choose from when there are any.
		if said.State == "choose" && len(said.Pids) == 0 {
			message := "No Node.js process was found in this container."
			if pid != 0 {
				message = "Process " + strconv.Itoa(pid) + " is no longer running, and no other Node.js process was found in this container."
			}
			writeProblem(w, http.StatusUnprocessableEntity, CodeNoNodeProcess, message, nil)
			return
		}
		if said.State == "choose" {
			message := "Choose the Node.js process whose inspector to open."
			if pid != 0 {
				message = "Process " + strconv.Itoa(pid) + " is no longer running: choose another."
			}
			writeJSON(w, http.StatusConflict, map[string]any{"code": CodeChooseProcess, "pids": said.Pids,
				"message": message})
			return
		}
		tunnel, ok := tunnelTo(w, r, h, n.cluster, n.namespace, n.name, n.flags.port)
		if !ok {
			return
		}
		defer tunnel.Stop()
		deadline := time.Now().Add(nodeOpenBudget)
		var last error
		for {
			ctx, cancel := context.WithTimeout(r.Context(), time.Second)
			_, last = inspectorList(ctx, tunnel)
			cancel()
			if last == nil || time.Now().After(deadline) || r.Context().Err() != nil {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if last != nil {
			writeProblem(w, http.StatusUnprocessableEntity, CodeInspectorRefused,
				"Process "+strconv.Itoa(said.Pid)+" was signalled and its inspector did not answer on port "+
					strconv.Itoa(n.flags.port)+".", last)
			return
		}
		writeJSON(w, http.StatusOK, nodeAnswer{Port: n.flags.port, Source: n.flags.source, Started: n.flags.started,
			Open: true, Pid: said.Pid})
	})
}

// --- the DevTools protocol -------------------------------------------------------

// cdp is one DevTools session over a WebSocket.
type cdp struct {
	conn *websocket.Conn
	next int
}

// cdpMessage is a command's answer or an event.
type cdpMessage struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// cdpReadLimit bounds one message: a CPU profile or a report. A variable
// for the tests, as are the limits below.
var cdpReadLimit int64 = 256 << 20

// inspectorListLimit bounds the inspector's /json/list.
var inspectorListLimit int64 = 1 << 20

// dialInspector opens a session with the first Node.js target the inspector
// lists, through tunnel.
func dialInspector(ctx context.Context, tunnel extension.Tunnel) (*cdp, error) {
	targets, err := inspectorList(ctx, tunnel)
	if err != nil {
		return nil, err
	}
	target := targets[0]
	for _, t := range targets {
		if t.Type == "node" {
			target = t
			break
		}
	}
	u, err := url.Parse(target.URL)
	if err != nil || u.Scheme != "ws" {
		return nil, fmt.Errorf("the inspector's target %q is not a WebSocket address", target.URL)
	}
	u.Host = tunnel.Addr
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(cdpReadLimit)
	return &cdp{conn: conn}, nil
}

func (c *cdp) close() { _ = c.conn.Close() }

// call sends method and waits for its answer, handing every event that
// arrives meanwhile to onEvent.
func (c *cdp) call(ctx context.Context, method string, params any, onEvent func(cdpMessage) error) (json.RawMessage, error) {
	c.next++
	id := c.next
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetWriteDeadline(deadline)
	}
	// The read ends when ctx does, deadline or cancel. Through safego:
	// context.AfterFunc runs this on a goroutine of its own.
	stop := context.AfterFunc(ctx, func() {
		_ = safego.Call("node inspector: ending a read as its request ends", func() error {
			return c.conn.SetReadDeadline(time.Now())
		})
	})
	defer stop()
	if err := c.conn.WriteJSON(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		var m cdpMessage
		if err := c.conn.ReadJSON(&m); err != nil {
			return nil, err
		}
		if m.ID == id {
			if m.Error != nil {
				return nil, fmt.Errorf("%s: %s", method, m.Error.Message)
			}
			return m.Result, nil
		}
		if m.Method != "" && onEvent != nil {
			if err := onEvent(m); err != nil {
				return nil, err
			}
		}
	}
}

// The reads, by the route's last segment.
var nodeReads = map[string]bool{"heapsnapshot": true, "profile": true, "report": true}

// nodeProfileSeconds are the CPU profile's durations.
var nodeProfileSeconds = []int{10, 30, 60}

var (
	// nodeSnapshotLimit is the most bytes a heap snapshot download carries.
	nodeSnapshotLimit int64 = 1 << 30
	// nodeSnapshotBudget bounds a heap snapshot.
	nodeSnapshotBudget = 5 * time.Minute
)

// nodeReportExpression is the one expression evaluated in the process.
const nodeReportExpression = "JSON.stringify(process.report.getReport())"

func (JVM) nodeRead(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		what := r.PathValue("read")
		if !nodeReads[what] {
			writeProblem(w, http.StatusBadRequest, CodeBadInspectorQuery, quote(what)+" is not a read this page makes.", nil)
			return
		}
		seconds := 0
		switch what {
		case "heapsnapshot":
			if r.URL.Query().Get("confirm") != h.ConfirmToken(r) {
				writeProblem(w, http.StatusBadRequest, CodeNotConfirmed, "A heap snapshot is taken only once it is confirmed.", nil)
				return
			}
		case "profile":
			n, err := strconv.Atoi(r.URL.Query().Get("seconds"))
			if err != nil || !contains(nodeProfileSeconds, n) {
				writeProblem(w, http.StatusBadRequest, CodeBadInspectorQuery, fmt.Sprintf("seconds is one of %v.", nodeProfileSeconds), nil)
				return
			}
			seconds = n
		}
		n, ok := resolveNode(w, r, h)
		if !ok {
			return
		}
		tunnel, ok := tunnelTo(w, r, h, n.cluster, n.namespace, n.name, n.flags.port)
		if !ok {
			return
		}
		defer tunnel.Stop()
		budget := pprofProbeBudget + time.Duration(seconds)*time.Second
		if what == "heapsnapshot" {
			budget = nodeSnapshotBudget
		}
		ctx, cancel := context.WithTimeout(r.Context(), budget)
		defer cancel()
		session, err := dialInspector(ctx, tunnel)
		if err != nil {
			writeProblem(w, http.StatusUnprocessableEntity, CodeInspectorClosed,
				"The inspector does not answer on port "+strconv.Itoa(n.flags.port)+": open it first.", err)
			return
		}
		defer session.close()
		filename := n.name + "-" + n.container + "-" + time.Now().UTC().Format("20060102-150405")
		switch what {
		case "heapsnapshot":
			nodeSnapshot(ctx, w, session, filename+".heapsnapshot")
		case "profile":
			nodeProfile(ctx, w, session, seconds, filename+".cpuprofile")
		case "report":
			nodeReport(ctx, w, session)
		}
	})
}

func attachment(w http.ResponseWriter, filename string) {
	hd := w.Header()
	hd.Set("Content-Type", "application/octet-stream")
	hd.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Content-Type-Options", "nosniff")
}

// nodeSnapshot streams the snapshot's chunks as they arrive. A failure
// before the first byte is a problem; past it the response is ended broken,
// so the browser fails the download rather than save a cut snapshot that
// DevTools cannot load. An empty chunk writes nothing, so it commits no 200,
// and a snapshot that ends with no byte at all is a problem too.
func nodeSnapshot(ctx context.Context, w http.ResponseWriter, session *cdp, filename string) {
	written := int64(0)
	_, err := session.call(ctx, "HeapProfiler.takeHeapSnapshot", map[string]any{"reportProgress": false},
		func(m cdpMessage) error {
			if m.Method != "HeapProfiler.addHeapSnapshotChunk" {
				return nil
			}
			var p struct {
				Chunk string `json:"chunk"`
			}
			if err := json.Unmarshal(m.Params, &p); err != nil {
				return err
			}
			if p.Chunk == "" {
				return nil
			}
			if written+int64(len(p.Chunk)) > nodeSnapshotLimit {
				return fmt.Errorf("the heap snapshot is larger than %d bytes", nodeSnapshotLimit)
			}
			if written == 0 {
				attachment(w, filename)
			}
			n, err := io.WriteString(w, p.Chunk)
			written += int64(n)
			return err
		})
	switch {
	case err == nil && written > 0:
	case err == nil:
		writeProblem(w, http.StatusBadGateway, CodeInspectorFailed, "The heap snapshot came back empty.", nil)
	case written == 0:
		writeProblem(w, http.StatusBadGateway, CodeInspectorFailed, "The heap snapshot could not be taken.", err)
	default:
		panic(http.ErrAbortHandler)
	}
}

func nodeProfile(ctx context.Context, w http.ResponseWriter, session *cdp, seconds int, filename string) {
	for _, method := range []string{"Profiler.enable", "Profiler.start"} {
		if _, err := session.call(ctx, method, map[string]any{}, nil); err != nil {
			writeProblem(w, http.StatusBadGateway, CodeInspectorFailed, "The CPU profile could not be started.", err)
			return
		}
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Duration(seconds) * time.Second):
	}
	result, err := session.call(ctx, "Profiler.stop", map[string]any{}, nil)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, CodeInspectorFailed, "The CPU profile could not be read.", err)
		return
	}
	var stopped struct {
		Profile json.RawMessage `json:"profile"`
	}
	if err := json.Unmarshal(result, &stopped); err != nil || len(stopped.Profile) == 0 {
		writeProblem(w, http.StatusBadGateway, CodeInspectorFailed, "The CPU profile came back empty.", err)
		return
	}
	attachment(w, filename)
	_, _ = w.Write(stopped.Profile)
}

// nodeReportAnswer is the diagnostic report, its environment masked.
type nodeReportAnswer struct {
	Report map[string]any `json:"report"`
	// Masked is how many environment values and command-line values were
	// hidden.
	Masked int `json:"masked"`
}

func nodeReport(ctx context.Context, w http.ResponseWriter, session *cdp) {
	result, err := session.call(ctx, "Runtime.evaluate", map[string]any{"expression": nodeReportExpression, "returnByValue": true}, nil)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, CodeInspectorFailed, "The diagnostic report could not be read.", err)
		return
	}
	var evaluated struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &evaluated); err != nil || len(evaluated.Exception) > 0 {
		writeProblem(w, http.StatusBadGateway, CodeInspectorFailed, "The process could not write its diagnostic report.", err)
		return
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(evaluated.Result.Value), &report); err != nil {
		writeProblem(w, http.StatusBadGateway, CodeInspectorFailed, "The diagnostic report is not JSON.", err)
		return
	}
	masked := 0
	if env, ok := report["environmentVariables"].(map[string]any); ok {
		for name := range env {
			if secretName(name) {
				env[name] = maskedValue
				masked++
			}
		}
	}
	// The command line's secret-looking options, by the JVM tab's rule.
	if header, ok := report["header"].(map[string]any); ok {
		if line, ok := header["commandLine"].([]any); ok {
			tokens := make([]string, 0, len(line))
			for _, t := range line {
				s, _ := t.(string)
				tokens = append(tokens, s)
			}
			out := make([]any, 0, len(line))
			for _, a := range maskArgs(tokens) {
				if a.Secret != "" {
					out = append(out, a.Text+maskedValue)
					masked++
					continue
				}
				out = append(out, a.Text)
			}
			header["commandLine"] = out
		}
	}
	writeJSON(w, http.StatusOK, nodeReportAnswer{Report: report, Masked: masked})
}

// maskedValue stands for a hidden value in the report.
const maskedValue = "••••••"

// writeNodeExecFailure answers an exec of the inspector's opening script
// that gave no result, in the inspector's words rather than jcmd's.
func writeNodeExecFailure(w http.ResponseWriter, cluster string, err error) {
	switch {
	case errors.Is(err, extension.ErrUnknownCluster):
		writeProblem(w, http.StatusNotFound, CodeUnknownCluster, extension.UnknownClusterText(cluster), err)
	case errors.Is(err, context.Canceled):
	case errors.Is(err, extension.ErrReadOnly):
		writeReadOnly(w, cluster, err)
	case errors.Is(err, extension.ErrDenied):
		writeProblem(w, http.StatusForbidden, CodeExecForbidden,
			"The cluster says you may not run commands in this pod (create pods/exec), so the inspector was not opened.", err)
	case errors.Is(err, extension.ErrUnreviewable):
		writeProblem(w, http.StatusServiceUnavailable, CodeExecUnreviewed,
			"The console could not ask the cluster whether you may run commands in this pod, so the inspector was not opened. Try again.", err)
	case errors.Is(err, extension.ErrExecBusy):
		writeProblem(w, http.StatusTooManyRequests, CodeExecBusy,
			"Too many commands run by extensions are already running; try again in a moment.", err)
	case errors.Is(err, extension.ErrExecTimedOut):
		writeProblem(w, http.StatusGatewayTimeout, CodeExecTimedOut,
			"Opening the inspector did not finish within its deadline and was stopped.", err)
	default:
		writeProblem(w, http.StatusBadGateway, CodeExecFailed, "The inspector's opening script could not be run in the container.", err)
	}
}
