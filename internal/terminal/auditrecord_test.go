package terminal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/identity"
)

// The two streams and their audit record: an exec the review
// refuses before its stream is recorded as refused; an exec or a node-shell
// pod whose record cannot be written is refused, says so, and never reads as
// opened or created.

type streamSink struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	broken bool
}

func (s *streamSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken {
		return 0, errors.New("write /dev/stdout: broken pipe")
	}
	return s.buf.Write(p)
}

func (s *streamSink) records(t *testing.T) []audit.Record {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []audit.Record
	scanner := bufio.NewScanner(bytes.NewReader(s.buf.Bytes()))
	for scanner.Scan() {
		var r audit.Record
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			t.Fatalf("not a record: %v\n%s", err, scanner.Text())
		}
		out = append(out, r)
	}
	return out
}

func execThrough(t *testing.T, gate *access.Gate, who identity.Who) (string, *recordingLog) {
	t.Helper()
	kubeconfig := execKubeconfig(t, "unreachable")
	term, err := New(managerFor(t, kubeconfig), Options{Kubeconfig: cluster.Kubeconfig{kubeconfig}, Access: gate})
	if err != nil {
		t.Fatal(err)
	}
	log := &recordingLog{}
	term.logf = log.logf
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		term.ExecHandler().ServeHTTP(w, r.WithContext(identity.With(r.Context(), who)))
	}))
	defer srv.Close()
	return dialAndRead(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/kt-api/exec?cluster=unreachable&namespace=shop&pod=api-1"), log
}

var erin = identity.Who{Name: "erin@example.com", Method: identity.SSO, Groups: []string{"oidc:dev"}}

func TestAnExecRefusedBeforeItsStreamIsRecordedAsRefused(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")
	sink := &streamSink{}
	reviews := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return false, nil }}
	gate := access.New(access.Options{Reviews: reviews.For, InCluster: true, Audit: audit.New(sink)})
	execThrough(t, gate, erin)

	got := sink.records(t)
	if len(got) != 2 || got[0].ID != got[1].ID || got[1].Outcome != audit.Refused {
		t.Fatalf("a refused exec was recorded as %+v, want an attempt and a refused outcome", got)
	}
	r := got[1]
	if r.Act != audit.Exec || r.Decision != "denied" || r.Who != "erin@example.com" || r.Name != "api-1" || r.Subresource != "exec" {
		t.Errorf("the record reads %+v", r)
	}
}

func TestAnExecWhoseRecordFailsNeverReadsAsOpened(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")
	gate := access.New(access.Options{Reviews: (&accesstest.Reviews{}).For, Audit: audit.New(&streamSink{broken: true})})
	got, log := execThrough(t, gate, erin)

	if !strings.Contains(got, "was not opened") || !strings.Contains(got, "could not write the audit record") {
		t.Errorf("the pane was not told the shell was refused for its record: %q", got)
	}
	if log.has("exec opened") || log.has("exec closed") {
		t.Errorf("a shell that never opened has an opened or closed line: %v", log.lines)
	}
	if !log.has("exec refused", "could not write the audit record", `who="erin@example.com"`) {
		t.Errorf("no attributed refusal line: %v", log.lines)
	}
}

func TestANodeShellPodWhoseRecordFailsIsNotCreated(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")
	fake := &nodeServer{}
	url := fake.start(t)
	kubeconfig := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\nkind: Config\ncurrent-context: lab\nclusters:\n- name: lab\n  cluster:\n    server: "+url+
		"\ncontexts:\n- name: lab\n  context:\n    cluster: lab\n    user: lab\nusers:\n- name: lab\n  user:\n    token: t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gate := access.New(access.Options{Reviews: (&accesstest.Reviews{}).For, Audit: audit.New(&streamSink{broken: true})})
	term, err := New(managerFor(t, kubeconfig), Options{Kubeconfig: cluster.Kubeconfig{kubeconfig}, Access: gate})
	if err != nil {
		t.Fatal(err)
	}
	log := &recordingLog{}
	term.logf = log.logf
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		term.NodeShellHandler().ServeHTTP(w, r.WithContext(identity.With(r.Context(), sharedAccount)))
	}))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/kt-api/nodeshell?cluster=lab&node=node-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
	conn.Close()

	if fake.wasCreated() {
		t.Error("the node-shell pod was created although its record could not be written")
	}
	if !log.has("node shell pod", "outcome=refused") {
		t.Errorf("no `node shell pod ... outcome=refused` line: %v", log.lines)
	}
}
