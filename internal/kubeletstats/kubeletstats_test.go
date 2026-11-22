package kubeletstats

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func clientsFor(t *testing.T, srv *httptest.Server) kubernetes.Interface {
	t.Helper()
	clients, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatalf("building a client: %v", err)
	}
	return clients
}

// The address is a published Kubernetes API and is pinned rather than trusted:
// `nodes/proxy` is the whole reason this capability degrades, and a path typo
// would present identically to a cluster that refuses it.
func TestTheKubeletIsAskedThroughTheNodeProxy(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		_, _ = w.Write([]byte(`{"node":{"nodeName":"node-a","fs":{"usedBytes":40,"capacityBytes":100}},
			"pods":[{"podRef":{"name":"api-1","namespace":"web"},
			"containers":[{"name":"app","rootfs":{"usedBytes":1000},"logs":{"usedBytes":500}}],
			"volume":[{"name":"cache","usedBytes":2048,"capacityBytes":4096}]}]}`))
	}))
	defer srv.Close()

	summary, err := Read(context.Background(), clientsFor(t, srv), "node-a")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if want := "/api/v1/nodes/node-a/proxy/stats/summary"; len(asked) != 1 || asked[0] != want {
		t.Fatalf("asked %v, want exactly [%s]", asked, want)
	}
	if !summary.NodeFSMeasured || summary.NodeFS != (FS{UsedBytes: 40, CapacityBytes: 100}) {
		t.Errorf("node filesystem = %+v (measured %v), want 40 of 100 — this is the Nodes "+
			"list's Disk gauge and no other API answers it",
			summary.NodeFS, summary.NodeFSMeasured)
	}
	if len(summary.Pods) != 1 {
		t.Fatalf("pods = %+v, want one", summary.Pods)
	}
	pod := summary.Pods[0]
	if pod.Namespace != "web" || pod.Name != "api-1" {
		t.Errorf("pod = %s/%s, want web/api-1", pod.Namespace, pod.Name)
	}
	// Both halves. Either alone under-reports, and a container evicted for
	// exceeding a limit it appeared to be under is the worst thing this can say.
	if len(pod.Containers) != 1 || pod.Containers[0].EphemeralBytes != 1500 {
		t.Errorf("ephemeral = %+v, want 1500 — the writable layer *plus* the logs",
			pod.Containers)
	}
	if len(pod.Volumes) != 1 || pod.Volumes[0].FS != (FS{UsedBytes: 2048, CapacityBytes: 4096}) {
		t.Errorf("volume = %+v, want 2048 of 4096", pod.Volumes)
	}
}

// A kubelet that answered and said nothing about its own filesystem is not a
// node with an empty disk. The flag is the only thing that tells the two apart,
// because `FS{}` is what both look like.
func TestAKubeletThatDescribedNoFilesystemIsNotANodeWithAnEmptyDisk(t *testing.T) {
	for name, body := range map[string]string{
		"no node block":     `{"pods":[]}`,
		"no fs block":       `{"node":{"nodeName":"node-a"},"pods":[]}`,
		"a fs of no size":   `{"node":{"nodeName":"node-a","fs":{"usedBytes":0,"capacityBytes":0}},"pods":[]}`,
		"used but no whole": `{"node":{"nodeName":"node-a","fs":{"usedBytes":40}},"pods":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			summary, err := Read(context.Background(), clientsFor(t, srv), "node-a")
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if summary.NodeFSMeasured {
				t.Errorf("the node filesystem is reported as measured from %s. A gauge "+
					"drawn from it would sit empty, and an empty bar reads as a disk with "+
					"room rather than as a question nobody answered", body)
			}
		})
	}
}

// A node that failed must be *absent*, never present with zeroes: the caller
// draws whatever is in the map, and a zeroed entry is a measurement.
func TestANodeThatRefusedIsAbsentRatherThanEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "node-b") {
			http.Error(w, `{"kind":"Status","message":"nodes \"node-b\" is forbidden: `+
				`User cannot get resource \"nodes/proxy\""}`, http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"node":{"nodeName":"x","fs":{"usedBytes":1,"capacityBytes":2}},"pods":[]}`))
	}))
	defer srv.Close()

	got, coverage := ReadNodes(context.Background(), clientsFor(t, srv), []string{"node-a", "node-b"})
	if _, present := got["node-b"]; present {
		t.Error("the node that refused is in the map, so its caller will draw a disk " +
			"gauge from zeroes it never measured")
	}
	if _, present := got["node-a"]; !present {
		t.Error("the node that answered is missing, so one refusal took the whole cluster " +
			"with it")
	}
	if coverage.Answered != 1 || coverage.Asked != 2 {
		t.Errorf("coverage = %d of %d, want 1 of 2 — a caller cannot say how much of the "+
			"cluster it is describing without both numbers", coverage.Answered, coverage.Asked)
	}
	if coverage.FirstErr == nil || !strings.Contains(coverage.FirstErr.Error(), "nodes/proxy") {
		t.Errorf("the reason is %v, which does not name `nodes/proxy` — that is the one "+
			"word that tells an operator this is an RBAC grant and not a broken page",
			coverage.FirstErr)
	}
}

func TestNotOneKubeletAnsweringIsNoMapAtAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "kubelet unreachable", http.StatusBadGateway)
	}))
	defer srv.Close()

	got, coverage := ReadNodes(context.Background(), clientsFor(t, srv), []string{"node-a", "node-b"})
	if got != nil {
		t.Errorf("got %v, want nil — an empty map is an answer and this is the absence of "+
			"one", got)
	}
	if coverage.Answered != 0 || coverage.Asked != 2 {
		t.Errorf("coverage = %d of %d, want 0 of 2", coverage.Answered, coverage.Asked)
	}
}

// Sorted, so two reads of one cluster ask the same nodes — which is what makes
// a partial answer reproducible rather than a different five nodes each time
// somebody reloads.
//
// **What is asserted is the in-flight set, not the arrival order.** `ReadNodes`
// sorts and then hands the names to a pool of Workers goroutines, so the order
// they are *dispatched* in is sorted and the order they *arrive* in is a race
// between five goroutines. Asserting that the first request to reach the server
// is the alphabetically first node would pass most runs, which is the worst
// kind of wrong.
//
// The property that is both real and observable: the pool can only hold Workers
// requests at once, so given more nodes than that, the names in flight are
// exactly the alphabetically first Workers. Blocking every handler until that
// many have arrived is what makes it deterministic — no sleep, no ordering
// assumption.
func TestTheKubeletsAreAskedInAStableOrder(t *testing.T) {
	nodes := []string{"node-h", "node-c", "node-a", "node-g", "node-b", "node-f", "node-d", "node-e"}
	if len(nodes) <= Workers {
		t.Fatalf("this test needs more nodes than the %d workers to observe anything", Workers)
	}

	var (
		mu       sync.Mutex
		inFlight []string
		full     = make(chan struct{})
		release  = make(chan struct{})
		once     sync.Once
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight = append(inFlight, r.URL.Path)
		reached := len(inFlight) >= Workers
		mu.Unlock()
		if reached {
			once.Do(func() { close(full) })
		}
		// Hold every handler until the pool is provably saturated, so the set
		// recorded above is the first batch and not a mixture of two.
		<-release
		_, _ = w.Write([]byte(`{"pods":[]}`))
	}))
	defer srv.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		ReadNodes(context.Background(), clientsFor(t, srv), nodes)
	}()

	select {
	case <-full:
	case <-time.After(10 * time.Second):
		mu.Lock()
		got := append([]string(nil), inFlight...)
		mu.Unlock()
		t.Fatalf("only %d of %d workers ever had a request in flight: %v", len(got), Workers, got)
	}

	mu.Lock()
	batch := append([]string(nil), inFlight[:Workers]...)
	mu.Unlock()
	close(release)
	<-done

	// The alphabetically first Workers names, in whatever order they arrived.
	want := map[string]bool{}
	for _, n := range []string{"node-a", "node-b", "node-c", "node-d", "node-e"} {
		want[n] = true
	}
	for _, path := range batch {
		matched := ""
		for n := range want {
			if strings.Contains(path, n) {
				matched = n
				break
			}
		}
		if matched == "" {
			t.Errorf("%q is in the first batch of %d, but the pool is fed from a sorted list "+
				"so the first batch can only be the alphabetically first %d nodes. A partial "+
				"answer now covers a different set on every read", path, Workers, Workers)
			continue
		}
		delete(want, matched)
	}
}

func TestNoNodesIsNoRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a kubelet was asked about %q when no node was named", r.URL.Path)
	}))
	defer srv.Close()

	got, coverage := ReadNodes(context.Background(), clientsFor(t, srv), nil)
	if got != nil || coverage.Asked != 0 {
		t.Errorf("got %v with coverage %+v, want nothing", got, coverage)
	}
}

// The kubelet names the claim it mounted, and this is where that is pinned.
//
// It matters more than a field usually does because the alternative to reading
// it is a join: pod list, `spec.volumes`, volume name to claim name. That join
// is silent when it is backwards — it puts one claim's fullness under another
// claim's row — so the PersistentVolumeClaims list takes the kubelet's own
// answer, and a change to this path has to break a test rather than a page.
func TestTheKubeletSaysWhichClaimAVolumeIs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"node":{"nodeName":"node-a"},"pods":[
			{"podRef":{"name":"db-0","namespace":"store"},"volume":[
			  {"name":"data","pvcRef":{"name":"db-data","namespace":"store"},
			   "usedBytes":700,"capacityBytes":1000},
			  {"name":"scratch","usedBytes":10,"capacityBytes":100},
			  {"name":"half","pvcRef":{"name":"nameless"},"usedBytes":1,"capacityBytes":2},
			  {"name":"other","pvcRef":{"namespace":"store"},"usedBytes":1,"capacityBytes":2}
			]}]}`))
	}))
	defer srv.Close()

	summary, err := Read(context.Background(), clientsFor(t, srv), "node-a")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(summary.Pods) != 1 || len(summary.Pods[0].Volumes) != 4 {
		t.Fatalf("pods = %+v, want one pod with four volumes", summary.Pods)
	}
	volumes := summary.Pods[0].Volumes
	if volumes[0].PVCRef != "store/db-data" {
		t.Errorf("PVCRef = %q, want %q — namespace/name, which is the key the "+
			"claims list looks a row up by", volumes[0].PVCRef, "store/db-data")
	}
	if volumes[0].FS != (FS{UsedBytes: 700, CapacityBytes: 1000}) {
		t.Errorf("FS = %+v, want 700 of 1000 alongside the reference", volumes[0].FS)
	}
	// An emptyDir, a configMap and a projected token all arrive with no pvcRef,
	// and none of them is a claim in the default namespace.
	if volumes[1].PVCRef != "" {
		t.Errorf("PVCRef = %q on a volume that is not a claim, want empty", volumes[1].PVCRef)
	}
	// Half a reference is not a reference. `/name` and `namespace/` would both
	// key into the map, and the first would collide with every other half
	// reference on the node.
	for _, v := range volumes[2:] {
		if v.PVCRef != "" {
			t.Errorf("%s: PVCRef = %q from half a reference, want empty — a claim "+
				"reference missing either half is a document this build does not "+
				"understand, not a claim in the default namespace", v.Name, v.PVCRef)
		}
	}
}

// The same refusal, sent the way an API server sends it: a JSON Status, with
// `Content-Type: application/json`. DoRaw's error for that answer says
// "unknown" - client-go only quotes a body it takes for text - so the reason
// would lose "nodes/proxy", the word the test above says it must keep. Read
// goes through internal/apiraw, which decodes the Status.
func TestARefusalSentAsAStatusKeepsTheAPIServersWords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403,` +
			`"message":"nodes \"node-a\" is forbidden: User \"jane\" cannot get resource \"nodes/proxy\""}`))
	}))
	defer srv.Close()

	_, err := Read(context.Background(), clientsFor(t, srv), "node-a")
	if err == nil {
		t.Fatal("a 403 read as an answer")
	}
	if !strings.Contains(err.Error(), "nodes/proxy") || strings.Contains(err.Error(), "unknown") {
		t.Errorf("the reason is %q; want the API server's own sentence naming `nodes/proxy`, not client-go's \"unknown\"", err)
	}
}

// A summary past the cap is refused with a sentence, not held whole.
func TestASummaryPastTheCapIsRefusedWithASentence(t *testing.T) {
	defer func(was int64) { maxSummary = was }(maxSummary)
	maxSummary = 1024
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"node":{"nodeName":"node-a"},"pods":[],"pad":"` + strings.Repeat("x", 2048) + `"}`))
	}))
	defer srv.Close()

	_, err := Read(context.Background(), clientsFor(t, srv), "node-a")
	if err == nil || !strings.Contains(err.Error(), "larger than") || !strings.Contains(err.Error(), "node-a") {
		t.Errorf("err = %v; want the node named and the summary refused as too large", err)
	}
}

// A shortened failure still unwraps to what the API server or the transport
// said: a caller classifies a refusal or a timeout with errors.Is and
// apierrors, while the sentence stays the short one.
func TestAShortenedFailureKeepsItsCause(t *testing.T) {
	long := `nodes \"node-a\" is forbidden: User \"jane\" cannot get resource \"nodes/proxy\"` + strings.Repeat(" in the cluster scope", 12)
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403,` +
			`"message":"` + long + `"}`))
	}))
	defer forbidden.Close()

	_, err := Read(context.Background(), clientsFor(t, forbidden), "node-a")
	if !apierrors.IsForbidden(err) {
		t.Errorf("apierrors.IsForbidden(%q) = false; want the 403 reachable through the shortened error", err)
	}
	var status *apierrors.StatusError
	if !errors.As(err, &status) {
		t.Fatalf("errors.As(%q, *StatusError) = false", err)
	}
	if want := "node-a: " + ShortError(status); err.Error() != want {
		t.Errorf("the sentence is %q; want it unchanged, %q", err, want)
	}
	if len(err.Error()) >= len(status.Error()) {
		t.Errorf("the cause is %d characters and the sentence %d; want a long cause shortened", len(status.Error()), len(err.Error()))
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer slow.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = Read(ctx, clientsFor(t, slow), "node-a")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("errors.Is(%q, context.DeadlineExceeded) = false; want the timeout reachable", err)
	}

	garbled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"node":`))
	}))
	defer garbled.Close()
	_, err = Read(context.Background(), clientsFor(t, garbled), "node-a")
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) || !strings.HasPrefix(err.Error(), "node-a: the kubelet's summary did not parse: ") {
		t.Errorf("err = %q; want the parse error kept as its cause and the sentence unchanged", err)
	}
}
