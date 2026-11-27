package runtimeinspect

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

// readOnlyHost is a console whose cluster is marked read-only: Exec,
// ExecStream and ForwardPod refuse as internal/web's host does there
// (extension.ErrReadOnly, before anything is reviewed or sent), and each
// refusal is kept, as the console records it and nothing else.
type readOnlyHost struct {
	*podHost
	refused []string
}

func (*readOnlyHost) ConfirmToken(*http.Request) string { return "confirm" }

func (h *readOnlyHost) refuse(act string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refused = append(h.refused, act)
	return fmt.Errorf("%w: kind is marked read-only in KT_READONLY_CLUSTERS; %s is refused there", extension.ErrReadOnly, act)
}

func (h *readOnlyHost) Exec(*http.Request, string, extension.ExecQuery) (extension.ExecResult, error) {
	return extension.ExecResult{}, h.refuse("running a command in a pod")
}

func (h *readOnlyHost) ExecStream(*http.Request, string, extension.ExecQuery, io.Writer, int64) (extension.ExecStreamResult, error) {
	return extension.ExecStreamResult{}, h.refuse("running a command in a pod")
}

func (h *readOnlyHost) ForwardPod(*http.Request, string, string, string, int) (extension.Tunnel, error) {
	return extension.Tunnel{}, h.refuse("opening a tunnel to a pod")
}

// Every route that runs a command in a container or opens a tunnel to it -
// the jcmd tools, the JVM inspection and dumps, pprof, the Node inspector and
// its snapshot, the Python processes and metrics - answers a read-only cluster with read_only in one line, opens
// no tunnel, reaches no pprof, and leaves a refusal as the only trace.
func TestAReadOnlyClusterRefusesEveryDiagnostic(t *testing.T) {
	srv, asked := pprofServer(t, true)
	p := pod("shop", "mix-0",
		corev1.Container{Name: "jvm", Image: "eclipse-temurin:21-jdk"},
		corev1.Container{Name: "go", Image: "registry.example.com/api:1", Env: envNamed("GOMEMLIMIT")},
		corev1.Container{Name: "web", Image: "node:22", Command: []string{"node", "server.js"}},
		corev1.Container{Name: "py", Image: "python:3.12-slim", Command: []string{"python", "app.py"}})
	h := &readOnlyHost{podHost: newPodHost(t, srv, p)}
	n := 0
	for _, r := range New().Routes(h) {
		if !strings.Contains(r.Pattern, "{container}") {
			continue
		}
		n++
		container := "jvm"
		switch {
		case strings.Contains(r.Pattern, "/pprof"):
			container = "go"
		case strings.Contains(r.Pattern, "/node"):
			container = "web"
		case strings.Contains(r.Pattern, "/python"):
			container = "py"
		}
		fill := strings.NewReplacer("{namespace}", "shop", "{name}", "mix-0", "{container}", container,
			"{profile}", "heap", "{read}", "heapsnapshot")
		path := "/x/kind/runtime" + fill.Replace(r.Pattern) + "?confirm=confirm&pid=1&seconds=30&tool=flags&key=k"
		t.Run(r.Pattern, func(t *testing.T) {
			before := len(h.refused)
			body := `{"confirmed":true}`
			if strings.HasSuffix(r.Pattern, "/reveal") {
				body = `{"pid":1,"kind":"option","name":"app.name"}`
			}
			rec := post(t, h, path, body)
			var problem struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &problem)
			if rec.Code != http.StatusForbidden || problem.Code != CodeReadOnly {
				t.Fatalf("POST %s answered %d %s, want 403 %s", path, rec.Code, rec.Body.String(), CodeReadOnly)
			}
			if problem.Message != "kind is read-only: no command, tunnel or dump." {
				t.Errorf("the refusal reads %q", problem.Message)
			}
			if got := len(h.refused) - before; got != 1 {
				t.Errorf("POST %s asked the console %d times, want once: one refusal, then nothing", path, got)
			}
		})
	}
	if n != 15 {
		t.Errorf("%d routes under a container, want 15", n)
	}
	if len(h.opened) != 0 || len(h.ran) != 0 || len(*asked) != 0 {
		t.Errorf("a read-only cluster opened %v, ran %v, asked pprof %v", h.opened, h.ran, *asked)
	}
}
