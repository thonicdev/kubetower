package logs

// The log stream against a real cluster.
//
// The tests beside this one run on a stand-in apiserver and prove the shape of
// the thing: which client is asked for, what the archive is handed, when the
// deadline fires. What they cannot prove lives in client-go —
// rest.HTTPClientFor copies rest.Config.Timeout into http.Client.Timeout, which
// covers body reads — and a stand-in apiserver with a 250 ms timeout
// demonstrates the mechanism without ever exercising the real 30 seconds. Only
// a real client built by
// cluster.Manager, with the real requestTimeout on it, does that.
//
// Off by default, because a suite that needs a cluster is a suite that fails on
// a laptop in a train, and because this one deliberately takes forty seconds:
//
//	KT_INTEGRATION_CLUSTER=docker-desktop go test ./internal/logs/ -run Integration -v
//
// Strictly read-only. It lists pods and reads logs. It creates nothing, changes
// nothing and deletes nothing, and it must stay that way: a guard test in this
// repository once deleted a live namespace. The archive it writes to is a
// t.TempDir, never the operator's own.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/archive"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/textutil"
)

func integrationClusters(t *testing.T) (*cluster.Manager, string) {
	t.Helper()

	name := os.Getenv("KT_INTEGRATION_CLUSTER")
	if name == "" {
		t.Skip("set KT_INTEGRATION_CLUSTER to a kubeconfig context to run this")
	}

	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("no home directory, so no kubeconfig")
		}
		kubeconfig = filepath.Join(home, ".kube", "config")
	}
	if _, err := os.Stat(kubeconfig); err != nil {
		t.Skipf("no kubeconfig at %s", kubeconfig)
	}

	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if !clusters.Known(name) {
		t.Skipf("no %q context in %s", name, kubeconfig)
	}
	return clusters, name
}

// aTalkingPod finds something to read. kube-system, running, and it does not
// matter which: the subject is the connection staying open, not what it says.
func aTalkingPod(t *testing.T, clusters *cluster.Manager, context string) (namespace, pod string) {
	t.Helper()
	client, err := clusters.Client(context)
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	ctx, cancel := gocontext(20 * time.Second)
	defer cancel()

	list, err := client.Clients.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{Limit: 50})
	if err != nil {
		t.Skipf("%s is not answering, which is not this test's subject: %v", context, err)
	}
	for _, p := range list.Items {
		if p.Status.Phase == "Running" && len(p.Spec.Containers) > 0 {
			return p.Namespace, p.Name
		}
	}
	t.Skip("no running pod in kube-system to read")
	return "", ""
}

func gocontext(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// End to end: a followed stream must still be alive well past a 30-second
// per-request timeout.
//
// Forty seconds on purpose. Thirty is where a client timeout would end it — the
// tail shows, and the panel goes dark half a minute later — so a test that
// stops at twenty-nine proves nothing at all.
//
// This also covers the archive in the same run: it is on, and the file it
// writes is read back from disk at the end. One stream, because that is how the
// two arrive together in production.
func TestIntegrationAFollowSurvivesPastThirtySecondsAndIsArchived(t *testing.T) {
	if testing.Short() {
		t.Skip("this one waits forty seconds by design")
	}
	clusters, contextName := integrationClusters(t)
	namespace, pod := aTalkingPod(t, clusters, contextName)
	t.Logf("following %s/%s on %s", namespace, pod, contextName)

	store := archive.Open(archive.Options{
		Root:     t.TempDir(),
		Defaults: archive.Settings{Enabled: true, Logs: true, RetentionDays: 1},
		Logf:     t.Logf,
	})
	defer func() { _ = store.Close() }()
	if !store.CapturesLogs() {
		t.Fatal("the test archive is not capturing logs")
	}

	streamer := New(clusters, accesstest.Allow(), t.Logf, store)

	// The browser going away, forty seconds in. Everything before that must be
	// the stream's own doing.
	const watch = 40 * time.Second
	reqCtx, closePanel := context.WithCancel(context.Background())
	defer closePanel()

	query := "cluster=" + contextName + "&namespace=" + namespace + "&pod=" + pod
	r := httptest.NewRequest(http.MethodGet, "/kt-api/logs?"+query, nil).WithContext(reqCtx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	started := time.Now()
	go func() {
		defer close(done)
		streamer.Handler().ServeHTTP(rec, r)
	}()

	select {
	case <-done:
		t.Fatalf("the stream ended by itself after %s — this is the bug, and %s is where the "+
			"per-request timeout would cut it. Body:\n%s",
			time.Since(started).Round(time.Second), 30*time.Second, tail(rec.Body.String()))
	case <-time.After(watch):
	}

	closePanel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the handler did not notice the browser leaving")
	}

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("the stream answered %d: %s", rec.Code, textutil.FirstLine(body))
	}
	if strings.Contains(body, "event: error") {
		t.Errorf("the stream reported an error while it was being watched:\n%s", tail(body))
	}
	if !strings.Contains(body, "data: ") {
		t.Fatalf("nothing was streamed in %s:\n%s", watch, body)
	}
	t.Logf("streamed %d bytes over %s with no error event", len(body), watch)

	// And the other half: it was written down. Read back through the archive's
	// own reader, which is what a history tab would use.
	lines, err := store.Log(archive.LogQuery{
		Cluster: contextName, Namespace: namespace, Pod: pod,
		Container: containerOfFirstFrame(t, streamer, contextName, namespace, pod),
	})
	if err != nil {
		t.Fatalf("reading the archive back: %v", err)
	}
	if len(lines) == 0 {
		t.Fatal("the archive is empty; the tee did not reach disk")
	}
	t.Logf("the archive holds %d lines, the first stamped %s: %s",
		len(lines), lines[0].At.Format(time.RFC3339), textutil.FirstLine(lines[0].Text))
}

// containerOfFirstFrame is the container the stream chose, worked out the same
// way the stream did. The archive files under the *resolved* container, so
// asking for the archive under an empty one would find nothing and look like a
// tee that never ran.
func containerOfFirstFrame(t *testing.T, s *Streamer, contextName, namespace, pod string) string {
	t.Helper()
	ctx, cancel := gocontext(20 * time.Second)
	defer cancel()
	name, err := s.DefaultContainer(ctx, contextName, namespace, pod)
	if err != nil {
		t.Fatalf("DefaultContainer: %v", err)
	}
	return name
}

func tail(s string) string {
	if len(s) > 600 {
		return "…" + s[len(s)-600:]
	}
	return s
}
