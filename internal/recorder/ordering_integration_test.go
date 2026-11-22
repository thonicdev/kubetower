package recorder

// What the API server actually orders a list by, asked of a real one.
//
// newestEvents turns entirely on this and it cannot be established from a fake: the
// client-go testing framework drops `Limit` and `Continue` on the floor, so a
// unit test can prove that newestEvents ranks what it is given and nothing
// about what a cluster hands it. The Kubernetes API offers no sort parameter,
// no ordering guarantee in its own documentation, and no time-based field
// selector on events — so the shape of the fix depends on a fact that has to be
// measured.
//
// Off by default, read-only, and one list call:
//
//	KT_INTEGRATION_CLUSTER=docker-desktop go test ./internal/recorder/ -run Ordering -v
//
// Measured against docker-desktop holding 21 events: a list
// with `Limit: 5` came back in `<namespace>/<name>` ascending order — two from
// `default`, two from `krow-system`, one from `kube-system` — while their times
// ran 23:31:59, 23:33:19, 23:32:46, 23:32:35, 23:31:33: not sorted, and the
// oldest of the five arriving last. The continue token says why in as many
// words; base64-decoded it is
//
//	{"v":"meta.k8s.io/v1","rv":1957839,"start":"kube-system/coredns-589f44dc88-h7q7k.18ccd3c713907b80<NUL>"}
//
// A cursor whose position is a *key*. There is nowhere in that for a time to be
// taken into account, which is the whole finding: `Limit: maxEvents` asks for
// the alphabetically first N, and on a cluster with more events than the cap it
// returns every event in `argocd` and none from `prod`.

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/resources"
)

func TestOrderingOfAListedEvents(t *testing.T) {
	name := os.Getenv("KT_INTEGRATION_CLUSTER")
	if name == "" {
		t.Skip("set KT_INTEGRATION_CLUSTER to a kubeconfig context to run this")
	}

	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("no home directory and no KUBECONFIG: %v", err)
		}
		kubeconfig = filepath.Join(home, ".kube", "config")
	}
	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatalf("reading %s: %v", kubeconfig, err)
	}
	resolved, ok := clusters.Resolve(name)
	if !ok {
		t.Fatalf("%q is not a context in %s", name, kubeconfig)
	}
	client, err := clusters.Client(resolved)
	if err != nil {
		t.Fatalf("connecting to %s: %v", resolved, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	list, err := client.Clients.CoreV1().Events("").List(ctx, metav1.ListOptions{Limit: 5})
	if err != nil {
		t.Fatalf("listing events on %s: %v", resolved, err)
	}
	if len(list.Items) < 2 {
		t.Skipf("%s has %d events, which says nothing about ordering", resolved, len(list.Items))
	}

	keys := make([]string, 0, len(list.Items))
	for i := range list.Items {
		e := &list.Items[i]
		keys = append(keys, e.Namespace+"/"+e.Name)
		t.Logf("%-60s %s", e.Namespace+"/"+e.Name, resources.EventTime(e).Format(time.RFC3339))
	}
	if !sort.StringsAreSorted(keys) {
		t.Errorf("a limited list did not come back in key order: %v\n"+
			"That is the assumption newestEvents is built on — if it no longer holds, re-read the "+
			"comment on maxEvents rather than the code.", keys)
	}
	t.Logf("continue token: %q (empty means this server served the whole list from its watch cache)",
		list.Continue)
}
