package runtimeinspect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

// nodeHost is podHost that confirms with `confirm`, as the console does.
type nodeHost struct{ *podHost }

func (nodeHost) ConfirmToken(*http.Request) string { return "confirm" }

// inspector plays Node.js's inspector: `/json/list` while open, and a
// DevTools session answering the methods this extra sends.
type inspector struct {
	mu      sync.Mutex
	open    bool
	methods []string
	srv     *httptest.Server
	// target is the address /json/list gives; chunks the snapshot's; drop
	// ends the session after the first chunk; stall answers no snapshot.
	target string
	chunks []string
	drop   bool
	stall  bool
}

func newInspector(t *testing.T, open bool) *inspector {
	t.Helper()
	in := &inspector{open: open, target: "ws://127.0.0.1:9229/0f2c-uuid", chunks: []string{`{"snapshot":`, `{"node_count":2}}`}}
	upgrader := websocket.Upgrader{}
	in.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in.mu.Lock()
		open, target := in.open, in.target
		in.mu.Unlock()
		switch {
		case r.URL.Path == "/json/list" && open:
			_ = json.NewEncoder(w).Encode([]inspectorTarget{{Type: "node", URL: target}})
		case r.URL.Path == "/0f2c-uuid" && open:
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			for {
				var m struct {
					ID     int             `json:"id"`
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				}
				if conn.ReadJSON(&m) != nil {
					return
				}
				in.mu.Lock()
				in.methods = append(in.methods, m.Method)
				in.mu.Unlock()
				switch m.Method {
				case "HeapProfiler.takeHeapSnapshot":
					in.mu.Lock()
					stall, drop, chunks := in.stall, in.drop, in.chunks
					in.mu.Unlock()
					if stall {
						continue
					}
					for _, chunk := range chunks {
						_ = conn.WriteJSON(map[string]any{"method": "HeapProfiler.addHeapSnapshotChunk", "params": map[string]any{"chunk": chunk}})
						if drop {
							return
						}
					}
					_ = conn.WriteJSON(map[string]any{"id": m.ID, "result": map[string]any{}})
				case "Profiler.stop":
					_ = conn.WriteJSON(map[string]any{"id": m.ID, "result": map[string]any{"profile": map[string]any{"nodes": []any{}, "startTime": 1}}})
				case "Runtime.evaluate":
					// Only the one fixed expression, by value, is answered.
					var p struct {
						Expression    string `json:"expression"`
						ReturnByValue bool   `json:"returnByValue"`
					}
					if json.Unmarshal(m.Params, &p) != nil || p.Expression != "JSON.stringify(process.report.getReport())" || !p.ReturnByValue {
						_ = conn.WriteJSON(map[string]any{"id": m.ID, "error": map[string]any{"message": "unexpected expression"}})
						continue
					}
					report, _ := json.Marshal(map[string]any{
						"header": map[string]any{"nodejsVersion": "v22.11.0",
							"commandLine": []string{"node", "--db-password=hunter3", "server.js"}},
						"environmentVariables": map[string]any{"NODE_ENV": "production", "DB_PASSWORD": "hunter2", "API_TOKEN": "t"},
					})
					_ = conn.WriteJSON(map[string]any{"id": m.ID, "result": map[string]any{"result": map[string]any{"type": "string", "value": string(report)}}})
				default:
					_ = conn.WriteJSON(map[string]any{"id": m.ID, "result": map[string]any{}})
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(in.srv.Close)
	return in
}

// set changes the inspector's behaviour under its lock.
func (in *inspector) set(change func(*inspector)) {
	in.mu.Lock()
	change(in)
	in.mu.Unlock()
}

func (in *inspector) setOpen() {
	in.mu.Lock()
	in.open = true
	in.mu.Unlock()
}

func (in *inspector) asked() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]string{}, in.methods...)
}

func nodeContainer(env ...corev1.EnvVar) corev1.Container {
	return corev1.Container{Name: "app", Image: "node:22", Command: []string{"node", "server.js"}, Env: env}
}

func newNodeHost(t *testing.T, in *inspector, c corev1.Container) nodeHost {
	p := pod("shop", "web-0", c, corev1.Container{Name: "proxy", Image: "nginx:1.27"})
	return nodeHost{&podHost{fakeHost: newHost(t, p), addr: strings.TrimPrefix(in.srv.URL, "http://")}}
}

const nodeBase = "/x/kind/runtime/pod/shop/web-0/app/node"

// The inspector's port and flags come from the command line and NODE_OPTIONS.
func TestTheInspectorFlagsAreReadFromTheSpec(t *testing.T) {
	f := readNodeFlags(nodeContainer())
	if f.port != 9229 || f.source != "default" || f.started || f.noSigusr1 {
		t.Errorf("default = %+v", f)
	}
	f = readNodeFlags(corev1.Container{Command: []string{"node", "--inspect=0.0.0.0:9230", "app.js"}})
	if f.port != 9230 || f.source != "command" || !f.started {
		t.Errorf("command = %+v", f)
	}
	f = readNodeFlags(nodeContainer(corev1.EnvVar{Name: "NODE_OPTIONS", Value: "--max-old-space-size=512 --inspect-port=9300 --disable-sigusr1"}))
	if f.port != 9300 || f.source != "env" || f.started || !f.noSigusr1 {
		t.Errorf("env = %+v", f)
	}
}

// The state says whether the inspector answers, through the tunnel to its
// port, and runs nothing in the container.
func TestTheNodeStateSaysWhetherTheInspectorIsOpen(t *testing.T) {
	in := newInspector(t, false)
	h := newNodeHost(t, in, nodeContainer())
	got := decode[nodeAnswer](t, post(t, h, nodeBase, ""))
	if got.Open || got.Port != 9229 || !strings.Contains(got.Detail, "404") || len(h.ran) != 0 {
		t.Errorf("closed = %+v, ran %v", got, h.ran)
	}
	in.setOpen()
	got = decode[nodeAnswer](t, post(t, h, nodeBase, ""))
	if !got.Open || h.opened[len(h.opened)-1] != "kind/shop/web-0:9229" || h.stopped != 2 {
		t.Errorf("open = %+v, opened %v, stopped %d", got, h.opened, h.stopped)
	}
}

// Opening: confirmed, a fixed script run once through Host.Exec, then the
// inspector waited for. NODE_OPTIONS is unset for the helper first; a
// missing env falls back to node itself.
func TestOpeningTheInspectorRunsTheFixedScriptOnceConfirmed(t *testing.T) {
	in := newInspector(t, false)
	h := newNodeHost(t, in, nodeContainer())
	if rec := post(t, h, nodeBase+"/inspector", ""); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), CodeNotConfirmed) {
		t.Fatalf("unconfirmed: %d %s", rec.Code, rec.Body)
	}
	if len(h.ran) != 0 {
		t.Fatalf("an unconfirmed open ran %v", h.ran)
	}
	first := strings.Join(nodeOpenArgv(nodeLaunchers[0], 0), " ")
	second := strings.Join(nodeOpenArgv(nodeLaunchers[1], 0), " ")
	h.execs = map[string]execAnswer{
		first:  {err: extension.ErrNoSuchCommand},
		second: {res: extension.ExecResult{Stdout: []byte(`{"state":"signalled","pid":7}` + "\n")}},
	}
	in.setOpen()
	rec := post(t, h, nodeBase+"/inspector?confirm=confirm", "")
	got := decode[nodeAnswer](t, rec)
	if rec.Code != http.StatusOK || !got.Open || got.Pid != 7 {
		t.Fatalf("%d %+v", rec.Code, got)
	}
	if len(h.ran) != 2 || strings.Join(h.ran[0][:4], " ") != "env -u NODE_OPTIONS node" || h.ran[1][0] != "node" ||
		!strings.Contains(h.ran[1][2], "process._debugProcess(pick)") {
		t.Errorf("ran %q", h.ran)
	}
	for _, argv := range h.ran {
		if strings.Contains(strings.Join(argv, " "), "SIGUSR2") || strings.Contains(strings.Join(argv, " "), "kill") {
			t.Errorf("a signal other than the inspector's was sent: %q", argv)
		}
	}
}

// Several Node.js processes: the page chooses, and the chosen pid is the
// argv's last word; a pid that is not a number is refused before anything.
func TestOpeningTheInspectorAsksWhichProcess(t *testing.T) {
	in := newInspector(t, true)
	h := newNodeHost(t, in, nodeContainer())
	h.execs = map[string]execAnswer{
		strings.Join(nodeOpenArgv(nodeLaunchers[0], 0), " "):  {res: extension.ExecResult{Stdout: []byte(`{"state":"choose","pids":[7,12]}`)}},
		strings.Join(nodeOpenArgv(nodeLaunchers[0], 12), " "): {res: extension.ExecResult{Stdout: []byte(`{"state":"signalled","pid":12}`)}},
	}
	rec := post(t, h, nodeBase+"/inspector?confirm=confirm", "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"pids":[7,12]`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := post(t, h, nodeBase+"/inspector?confirm=confirm&pid=12", ""); rec.Code != http.StatusOK {
		t.Fatalf("chosen: %d %s", rec.Code, rec.Body)
	}
	before := len(h.ran)
	if rec := post(t, h, nodeBase+"/inspector?confirm=confirm&pid=12x", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad pid: %d", rec.Code)
	}
	if len(h.ran) != before {
		t.Error("a bad pid ran something")
	}
}

// A process started with --disable-sigusr1 is refused before anything runs;
// a signalled process whose inspector never answers is said.
func TestOpeningTheInspectorRefusals(t *testing.T) {
	in := newInspector(t, false)
	h := newNodeHost(t, in, nodeContainer(corev1.EnvVar{Name: "NODE_OPTIONS", Value: "--disable-sigusr1"}))
	if rec := post(t, h, nodeBase+"/inspector?confirm=confirm", ""); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), CodeSigusr1Disabled) || len(h.ran) != 0 {
		t.Errorf("disabled: %d %s, ran %v", rec.Code, rec.Body, h.ran)
	}
	if testing.Short() {
		return
	}
	h = newNodeHost(t, in, nodeContainer())
	h.execs = map[string]execAnswer{strings.Join(nodeOpenArgv(nodeLaunchers[0], 0), " "): {res: extension.ExecResult{Stdout: []byte(`{"state":"signalled","pid":7}`)}}}
	if rec := post(t, h, nodeBase+"/inspector?confirm=confirm", ""); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), CodeInspectorRefused) {
		t.Errorf("never answered: %d %s", rec.Code, rec.Body)
	}
}

// The three reads, each a fixed DevTools method through the tunnel.
func TestTheInspectorReads(t *testing.T) {
	in := newInspector(t, true)
	h := newNodeHost(t, in, nodeContainer())

	if rec := post(t, h, nodeBase+"/heapsnapshot", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("an unconfirmed snapshot: %d", rec.Code)
	}
	rec := post(t, h, nodeBase+"/heapsnapshot?confirm=confirm", "")
	if rec.Code != http.StatusOK || rec.Body.String() != `{"snapshot":{"node_count":2}}` ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), ".heapsnapshot") {
		t.Errorf("snapshot: %d %q", rec.Code, rec.Body)
	}

	rec = post(t, h, nodeBase+"/report", "")
	var report nodeReportAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("report: %d %s", rec.Code, rec.Body)
	}
	env := report.Report["environmentVariables"].(map[string]any)
	if report.Masked != 3 || env["DB_PASSWORD"] != maskedValue || env["API_TOKEN"] != maskedValue || env["NODE_ENV"] != "production" {
		t.Errorf("report env = %v, masked %d", env, report.Masked)
	}
	line := report.Report["header"].(map[string]any)["commandLine"].([]any)
	if len(line) != 3 || line[1] != "--db-password="+maskedValue || line[2] != "server.js" {
		t.Errorf("report command line = %v", line)
	}
	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "hunter3") {
		t.Error("a secret left in the report")
	}

	for _, path := range []string{nodeBase + "/profile?seconds=7", nodeBase + "/profile", nodeBase + "/evaluate", nodeBase + "/coverage"} {
		if rec := post(t, h, path, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s answered %d", path, rec.Code)
		}
	}
	for _, m := range in.asked() {
		switch m {
		case "HeapProfiler.takeHeapSnapshot", "Runtime.evaluate":
		default:
			t.Errorf("the inspector was sent %s", m)
		}
	}
}

// A closed inspector is said, and a container nothing says is Node.js is
// refused.
func TestTheInspectorReadsNeedItOpen(t *testing.T) {
	in := newInspector(t, false)
	h := newNodeHost(t, in, nodeContainer())
	if rec := post(t, h, nodeBase+"/report", ""); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), CodeInspectorClosed) {
		t.Errorf("closed: %d %s", rec.Code, rec.Body)
	}
	if rec := post(t, h, "/x/kind/runtime/pod/shop/web-0/proxy/node", ""); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), CodeNotNode) {
		t.Errorf("not node: %d %s", rec.Code, rec.Body)
	}
}

// aborted is whether serving ended the response broken
// (http.ErrAbortHandler), as a cut download must.
func aborted(t *testing.T, h extension.Host, path string) (rec *httptest.ResponseRecorder, broke bool) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			if r != http.ErrAbortHandler {
				panic(r)
			}
			broke = true
		}
	}()
	return post(t, h, path, ""), false
}

// A heap snapshot cut after its first chunk - the session dropped, the size
// limit or the budget reached - ends the response broken, never as a whole
// file; one that fails before any chunk is a problem.
func TestACutHeapSnapshotIsNotSavedAsWhole(t *testing.T) {
	const path = nodeBase + "/heapsnapshot?confirm=confirm"
	in := newInspector(t, true)
	h := newNodeHost(t, in, nodeContainer())

	in.set(func(in *inspector) { in.drop = true })
	if _, broke := aborted(t, h, path); !broke {
		t.Error("a snapshot whose session dropped was saved as whole")
	}

	in.set(func(in *inspector) { in.drop = false })
	setFor(t, &nodeSnapshotLimit, 15)
	if _, broke := aborted(t, h, path); !broke {
		t.Error("a snapshot past its limit was saved as whole")
	}
	setFor(t, &nodeSnapshotLimit, 5)
	if rec, broke := aborted(t, h, path); broke || rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), CodeInspectorFailed) {
		t.Errorf("a first chunk past the limit: broke %v, %d %s", broke, rec.Code, rec.Body)
	}

	setFor(t, &nodeSnapshotLimit, 1<<30)
	setFor(t, &nodeSnapshotBudget, 100*time.Millisecond)
	in.set(func(in *inspector) { in.stall = true })
	start := time.Now()
	rec, broke := aborted(t, h, path)
	if took := time.Since(start); broke || rec.Code != http.StatusBadGateway || took > 3*time.Second {
		t.Errorf("a stalled snapshot: broke %v, %d after %v", broke, rec.Code, took)
	}
}

// A message past the session's read limit and a /json/list past its own
// fail; a target that is not a WebSocket address is not dialled.
func TestTheInspectorSessionIsBounded(t *testing.T) {
	in := newInspector(t, true)
	h := newNodeHost(t, in, nodeContainer())

	setFor(t, &cdpReadLimit, 64)
	if rec := post(t, h, nodeBase+"/report", ""); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), CodeInspectorFailed) {
		t.Errorf("a report past the read limit: %d %s", rec.Code, rec.Body)
	}
	setFor(t, &cdpReadLimit, 256<<20)

	setFor(t, &inspectorListLimit, 8)
	if rec := post(t, h, nodeBase+"/report", ""); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), CodeInspectorClosed) {
		t.Errorf("a /json/list past its limit: %d %s", rec.Code, rec.Body)
	}
	setFor(t, &inspectorListLimit, 1<<20)

	in.set(func(in *inspector) { in.target = "http://127.0.0.1:9229/0f2c-uuid" })
	before := len(in.asked())
	if rec := post(t, h, nodeBase+"/report", ""); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), CodeInspectorClosed) {
		t.Errorf("an http target: %d %s", rec.Code, rec.Body)
	}
	for _, m := range in.asked()[before:] {
		t.Errorf("the inspector was sent %s through a refused session", m)
	}
}

// Opening finds no Node.js process: said in one line, not an empty choice.
// env without node on its PATH (exit 127) falls back to the next launcher; a
// pid of 0 is refused; a refused exec speaks of the inspector, not jcmd.
func TestOpeningTheInspectorEdges(t *testing.T) {
	in := newInspector(t, true)
	h := newNodeHost(t, in, nodeContainer())
	first := strings.Join(nodeOpenArgv(nodeLaunchers[0], 0), " ")
	second := strings.Join(nodeOpenArgv(nodeLaunchers[1], 0), " ")

	h.execs = map[string]execAnswer{first: {res: extension.ExecResult{Stdout: []byte(`{"state":"choose","pids":[]}`)}}}
	if rec := post(t, h, nodeBase+"/inspector?confirm=confirm", ""); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), CodeNoNodeProcess) {
		t.Errorf("no process: %d %s", rec.Code, rec.Body)
	}

	h.ran = nil
	h.execs = map[string]execAnswer{
		first:  {res: extension.ExecResult{ExitCode: 127, Stderr: []byte("env: 'node': No such file or directory")}},
		second: {res: extension.ExecResult{Stdout: []byte(`{"state":"signalled","pid":7}`)}},
	}
	if rec := post(t, h, nodeBase+"/inspector?confirm=confirm", ""); rec.Code != http.StatusOK || len(h.ran) != 2 {
		t.Errorf("env without node: %d %s, ran %d", rec.Code, rec.Body, len(h.ran))
	}

	before := len(h.ran)
	for _, pid := range []string{"0", "-3"} {
		if rec := post(t, h, nodeBase+"/inspector?confirm=confirm&pid="+pid, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("pid %s: %d", pid, rec.Code)
		}
	}
	if len(h.ran) != before {
		t.Error("a pid of 0 or less ran something")
	}

	h.execs = map[string]execAnswer{first: {err: extension.ErrDenied}}
	rec := post(t, h, nodeBase+"/inspector?confirm=confirm", "")
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "jcmd") || !strings.Contains(rec.Body.String(), "inspector") {
		t.Errorf("denied: %d %s", rec.Code, rec.Body)
	}
}

// procHarness stands a fake /proc in front of the opening script: the
// processes PROCS names (pid -> [comm, exe]), the script's own pid and its
// parent's as node, and process._debugProcess saying which pid it was handed.
const procHarness = `const fs=require('fs');const procs=JSON.parse(process.env.PROCS);` +
	`procs[process.pid]=['node','/usr/local/bin/node'];procs[process.ppid]=['node','/usr/local/bin/node'];` +
	`const rd=fs.readdirSync,rf=fs.readFileSync,rl=fs.readlinkSync;` +
	`const at=(f,i)=>{const m=/^\/proc\/([0-9]+)\//.exec(String(f));if(!m)return undefined;` +
	`const e=procs[m[1]];if(!e||e[i]===''){const x=new Error('ENOENT');x.code='ENOENT';throw x}return e[i]};` +
	`fs.readdirSync=(d,...a)=>d==='/proc'?Object.keys(procs).concat(['self','cpuinfo']):rd(d,...a);` +
	`fs.readFileSync=(f,...a)=>{const v=at(f,0);return v===undefined?rf(f,...a):v+'\n'};` +
	`fs.readlinkSync=(f,...a)=>{const v=at(f,1);return v===undefined?rl(f,...a):v};` +
	`process._debugProcess=p=>{process.stderr.write('debugged '+p)};`

// runOpenScript runs nodeOpenScript under node against procs, and answers
// what it printed and which pid it opened, "" for none.
func runOpenScript(t *testing.T, procs map[string][2]string, pid int) (string, string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		// GitHub's runners carry node; a CI without it would pass this
		// test without ever running the script, so it fails there instead.
		if underCI() {
			t.Fatal("no node on PATH to run the opening script, and under CI it must run")
		}
		t.Skip("no node on PATH to run the opening script")
	}
	harness := filepath.Join(t.TempDir(), "harness.js")
	if err := os.WriteFile(harness, []byte(procHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	table, _ := json.Marshal(procs)
	argv := nodeOpenArgv([]string{node, "-r", harness}, pid)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "PROCS="+string(table))
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("the script failed: %v\n%s", err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), strings.TrimPrefix(stderr.String(), "debugged ")
}

// The script finds Node.js by comm, or by exe when the title was changed,
// never itself nor its parent, and never a package manager that runs on node:
// with CMD ["npm","start"] the application is opened without a choice.
func TestTheOpeningScriptPicksTheApplication(t *testing.T) {
	for _, c := range []struct {
		name   string
		procs  map[string][2]string
		pid    int
		out    string
		opened string
	}{
		{"by comm", map[string][2]string{"1": {"node", "/usr/local/bin/node"}, "7": {"sh", "/bin/sh"}}, 0,
			`{"state":"signalled","pid":1}`, "1"},
		{"by exe, the title changed", map[string][2]string{"1": {"next-server (v1", "/usr/local/bin/node"}}, 0,
			`{"state":"signalled","pid":1}`, "1"},
		{"npm start beside its app", map[string][2]string{"1": {"npm start", "/usr/local/bin/node"},
			"18": {"node", "/usr/local/bin/node"}}, 0, `{"state":"signalled","pid":18}`, "18"},
		{"npx, pnpm and yarn too", map[string][2]string{"1": {"npx", "/usr/local/bin/node"},
			"2": {"pnpm", "/usr/local/bin/node"}, "3": {"yarn", "/usr/local/bin/node"},
			"9": {"server", "/usr/local/bin/node"}}, 0, `{"state":"signalled","pid":9}`, "9"},
		{"two apps", map[string][2]string{"1": {"node", "/usr/local/bin/node"}, "2": {"node", "/usr/local/bin/node"}}, 0,
			`{"state":"choose","pids":[1,2]}`, ""},
		{"the chosen one", map[string][2]string{"1": {"node", "/usr/local/bin/node"}, "2": {"node", "/usr/local/bin/node"}}, 2,
			`{"state":"signalled","pid":2}`, "2"},
		{"none", map[string][2]string{"1": {"java", "/opt/java/bin/java"}, "4": {"", ""}}, 0,
			`{"state":"choose","pids":[]}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, opened := runOpenScript(t, c.procs, c.pid)
			if out != c.out || opened != c.opened {
				t.Errorf("printed %s and opened %q, want %s and %q", out, opened, c.out, c.opened)
			}
		})
	}
}

// An empty chunk writes nothing, so it commits no 200: the snapshot after it
// is saved whole, and a snapshot of empty chunks only is a problem (#100).
func TestAnEmptyHeapSnapshotChunkCommitsNothing(t *testing.T) {
	const path = nodeBase + "/heapsnapshot?confirm=confirm"
	in := newInspector(t, true)
	h := newNodeHost(t, in, nodeContainer())

	in.set(func(in *inspector) { in.chunks = []string{"", `{"snapshot":`, "", `{"node_count":2}}`} })
	rec, broke := aborted(t, h, path)
	if broke || rec.Code != http.StatusOK || rec.Body.String() != `{"snapshot":{"node_count":2}}` ||
		!strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment;") {
		t.Errorf("empty chunks among others: broke %v, %d %q", broke, rec.Code, rec.Body)
	}

	in.set(func(in *inspector) { in.chunks = []string{"", ""} })
	rec, broke = aborted(t, h, path)
	if broke || rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), CodeInspectorFailed) ||
		rec.Header().Get("Content-Disposition") != "" {
		t.Errorf("only empty chunks: broke %v, %d %s", broke, rec.Code, rec.Body)
	}
}

// A chosen pid that has exited is said in one line: with the processes left
// to choose from, or as no process at all, never as an empty choice (#100).
func TestAChosenProcessThatExitedIsSaid(t *testing.T) {
	in := newInspector(t, true)
	h := newNodeHost(t, in, nodeContainer())
	chosen := strings.Join(nodeOpenArgv(nodeLaunchers[0], 12), " ")

	h.execs = map[string]execAnswer{chosen: {res: extension.ExecResult{Stdout: []byte(`{"state":"choose","pids":[]}`)}}}
	rec := post(t, h, nodeBase+"/inspector?confirm=confirm&pid=12", "")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), CodeNoNodeProcess) ||
		!strings.Contains(rec.Body.String(), "Process 12 is no longer running") {
		t.Errorf("gone, none left: %d %s", rec.Code, rec.Body)
	}

	h.execs = map[string]execAnswer{chosen: {res: extension.ExecResult{Stdout: []byte(`{"state":"choose","pids":[7]}`)}}}
	rec = post(t, h, nodeBase+"/inspector?confirm=confirm&pid=12", "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"pids":[7]`) ||
		!strings.Contains(rec.Body.String(), "Process 12 is no longer running: choose another.") {
		t.Errorf("gone, another left: %d %s", rec.Code, rec.Body)
	}
}

// underCI is whether the CI variable says this runs under CI: a true value,
// as GitHub and every other runner sets it. CI=false or CI=0 is a person
// saying it does not (#157).
func underCI() bool {
	on, err := strconv.ParseBool(os.Getenv("CI"))
	return err == nil && on
}

func TestOnlyATrueCIIsCI(t *testing.T) {
	for value, want := range map[string]bool{"true": true, "1": true, "TRUE": true, "false": false, "0": false, "": false, "yes": false} {
		t.Setenv("CI", value)
		if got := underCI(); got != want {
			t.Errorf("CI=%q: %v, want %v", value, got, want)
		}
	}
}

// The inspector's target must be a plain ws:// address (#157): the tunnel is
// a TCP stream to the pod, so neither wss:// nor any other scheme is dialled,
// and the refusal names the target rather than the dialer's own complaint.
func TestOnlyAWsTargetIsDialled(t *testing.T) {
	for _, target := range []string{"wss://127.0.0.1:9229/0f2c-uuid", "http://127.0.0.1:9229/0f2c-uuid"} {
		upgraded := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/json/list" {
				_ = json.NewEncoder(w).Encode([]map[string]string{{"type": "node", "webSocketDebuggerUrl": target}})
				return
			}
			upgraded = true
			http.Error(w, "not a debugger", http.StatusBadRequest)
		}))
		_, err := dialInspector(context.Background(), extension.Tunnel{Addr: strings.TrimPrefix(srv.URL, "http://")})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "is not a WebSocket address") {
			t.Errorf("%s: %v, want it refused as not a WebSocket address", target, err)
		}
		if upgraded {
			t.Errorf("%s was dialled", target)
		}
	}
}
