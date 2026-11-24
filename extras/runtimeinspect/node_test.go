package runtimeinspect

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
}

func newInspector(t *testing.T, open bool) *inspector {
	t.Helper()
	in := &inspector{open: open}
	upgrader := websocket.Upgrader{}
	in.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in.mu.Lock()
		open := in.open
		in.mu.Unlock()
		switch {
		case r.URL.Path == "/json/list" && open:
			_ = json.NewEncoder(w).Encode([]inspectorTarget{{Type: "node", URL: "ws://127.0.0.1:9229/0f2c-uuid"}})
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
					for _, chunk := range []string{`{"snapshot":`, `{"node_count":2}}`} {
						_ = conn.WriteJSON(map[string]any{"method": "HeapProfiler.addHeapSnapshotChunk", "params": map[string]any{"chunk": chunk}})
					}
					_ = conn.WriteJSON(map[string]any{"id": m.ID, "result": map[string]any{}})
				case "Profiler.stop":
					_ = conn.WriteJSON(map[string]any{"id": m.ID, "result": map[string]any{"profile": map[string]any{"nodes": []any{}, "startTime": 1}}})
				case "Runtime.evaluate":
					report, _ := json.Marshal(map[string]any{
						"header":               map[string]any{"nodejsVersion": "v22.11.0"},
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
	if report.Masked != 2 || env["DB_PASSWORD"] != maskedValue || env["API_TOKEN"] != maskedValue || env["NODE_ENV"] != "production" {
		t.Errorf("report env = %v, masked %d", env, report.Masked)
	}
	if strings.Contains(rec.Body.String(), "hunter2") {
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
