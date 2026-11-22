package terminal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/identity"
)

// The desktop's one exception, on the two streams: a review of the
// kubeconfig's own identity that could not be made lets the pod exec and the
// node shell through - the API server checks that same identity - and the
// lines say `decision=unreviewable`. Under the server profile the same answer
// refuses. The node shell's pod is the more privileged of the two, so both
// have to agree.

var unreviewable = func(accesstest.Question) (bool, error) {
	return false, errors.New("the authorization API is not served")
}

type recordingLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *recordingLog) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *recordingLog) has(parts ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		all := true
		for _, part := range parts {
			all = all && strings.Contains(line, part)
		}
		if all {
			return true
		}
	}
	return false
}

func gateFor(inCluster bool) *access.Gate {
	return access.New(access.Options{Reviews: (&accesstest.Reviews{Answer: unreviewable}).For, InCluster: inCluster})
}

var sharedAccount = identity.Who{Name: "admin", Method: identity.Password}

func TestADesktopExecGoesThroughAnUnreviewableReviewAndTheServerProfileRefuses(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")
	for _, tc := range []struct {
		name      string
		inCluster bool
		opens     bool
	}{
		{"desktop", false, true},
		{"server profile", true, false},
	} {
		kubeconfig := execKubeconfig(t, "unreachable")
		term, err := New(managerFor(t, kubeconfig), Options{Kubeconfig: cluster.Kubeconfig{kubeconfig}, Access: gateFor(tc.inCluster)})
		if err != nil {
			t.Fatal(err)
		}
		log := &recordingLog{}
		term.logf = log.logf
		guarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			term.ExecHandler().ServeHTTP(w, r.WithContext(identity.With(r.Context(), sharedAccount)))
		})
		srv := httptest.NewServer(guarded)
		got := dialAndRead(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/kt-api/exec?cluster=unreachable&namespace=shop&pod=api-1")
		srv.Close()
		if tc.opens {
			if strings.Contains(got, "could not ask") {
				t.Errorf("%s: the exec was refused for want of a review: %q", tc.name, got)
			}
			deadline := time.Now().Add(5 * time.Second)
			for !log.has("exec opened", "decision=unreviewable") && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if !log.has("exec opened", "decision=unreviewable") {
				t.Errorf("%s: no `exec opened ... decision=unreviewable` line: %v", tc.name, log.lines)
			}
		} else {
			if !strings.Contains(got, "could not ask") {
				t.Errorf("%s: the exec was not refused: %q", tc.name, got)
			}
			if log.has("exec opened") {
				t.Errorf("%s: the exec opened: %v", tc.name, log.lines)
			}
		}
	}
}

// nodeServer is an API server with one node, which answers a pod create and
// says the pod is running, and records whether a pod was created.
type nodeServer struct {
	mu      sync.Mutex
	created bool
}

func (n *nodeServer) start(t *testing.T) string {
	t.Helper()
	write := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/nodes/"):
			write(w, 200, map[string]any{"apiVersion": "v1", "kind": "Node", "metadata": map[string]any{"name": "node-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces/default/pods":
			write(w, 200, map[string]any{"apiVersion": "v1", "kind": "PodList", "metadata": map[string]any{}, "items": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/namespaces/default/pods":
			n.mu.Lock()
			n.created = true
			n.mu.Unlock()
			body, _ := io.ReadAll(r.Body)
			// JSON or protobuf, whichever client-go chose, through the scheme.
			decoded, _, err := scheme.Codecs.UniversalDeserializer().Decode(body, nil, nil)
			pod, ok := decoded.(*corev1.Pod)
			if err != nil || !ok {
				http.Error(w, "not a pod", http.StatusBadRequest)
				return
			}
			pod.APIVersion, pod.Kind = "v1", "Pod"
			pod.Status.Phase = corev1.PodRunning
			write(w, 201, pod)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/namespaces/default/pods/"):
			name := strings.TrimPrefix(r.URL.Path, "/api/v1/namespaces/default/pods/")
			write(w, 200, map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": name, "namespace": "default"},
				"status": map[string]any{"phase": "Running"}})
		case r.Method == http.MethodDelete:
			write(w, 200, map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Success"})
		default:
			http.Error(w, "not here", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (n *nodeServer) wasCreated() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.created
}

func TestADesktopNodeShellGoesThroughAnUnreviewableReviewAndTheServerProfileRefuses(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")
	for _, tc := range []struct {
		name      string
		inCluster bool
		creates   bool
	}{
		{"desktop", false, true},
		{"server profile", true, false},
	} {
		fake := &nodeServer{}
		url := fake.start(t)
		kubeconfig := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\nkind: Config\ncurrent-context: lab\nclusters:\n- name: lab\n  cluster:\n    server: "+url+
			"\ncontexts:\n- name: lab\n  context:\n    cluster: lab\n    user: lab\nusers:\n- name: lab\n  user:\n    token: t\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		term, err := New(managerFor(t, kubeconfig), Options{Kubeconfig: cluster.Kubeconfig{kubeconfig}, Access: gateFor(tc.inCluster)})
		if err != nil {
			t.Fatal(err)
		}
		log := &recordingLog{}
		term.logf = log.logf
		guarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			term.NodeShellHandler().ServeHTTP(w, r.WithContext(identity.With(r.Context(), sharedAccount)))
		})
		srv := httptest.NewServer(guarded)
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/kt-api/nodeshell?cluster=lab&node=node-a", nil)
		if err != nil {
			t.Fatalf("%s: the handshake failed: %v", tc.name, err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
		conn.Close()
		srv.Close()

		if fake.wasCreated() != tc.creates {
			t.Errorf("%s: pod created=%t, want %t", tc.name, fake.wasCreated(), tc.creates)
		}
		want := "outcome=created"
		if !tc.creates {
			want = "outcome=refused"
		}
		if !log.has("node shell pod", "decision=unreviewable", want) {
			t.Errorf("%s: no `node shell pod ... decision=unreviewable %s` line: %v", tc.name, want, log.lines)
		}
	}
}
