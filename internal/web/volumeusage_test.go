package web

// The join behind the Used column, tested where it is a function of its
// arguments — which is why it is one.
//
// Every case here is a *refusal*. Dividing used by declared is one line and
// would survive any rewrite; what would not survive is that four different
// silences are told apart, and that the loudest of them — a kubelet measuring a
// shared NFS export — produces a number that is stable, plausible and about
// somebody else's disk.
//
// That guard is the common path rather than an edge. Five claims of five
// different declared sizes — 4Gi, 5Gi, two 8Gi and a 10Gi — can all come back
// from their kubelets as
// **2.92 GiB used of 9.76 GiB**: an `nfs-nas-nvme` export, reported five times
// under five names, at a stable and completely believable 30 %.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/kubeletstats"
)

const (
	gib = int64(1) << 30
	tib = int64(1) << 40
)

// summaryWith is one kubelet's answer holding one claim.
func summaryWith(claim string, used, capacity int64) kubeletstats.Summary {
	return kubeletstats.Summary{Pods: []kubeletstats.Pod{{
		Volumes: []kubeletstats.Volume{{
			Name:   "data",
			PVCRef: claim,
			FS:     kubeletstats.FS{UsedBytes: used, CapacityBytes: capacity},
		}},
	}}}
}

func full(answered, asked int) kubeletstats.Coverage {
	return kubeletstats.Coverage{Asked: asked, Answered: answered}
}

// The reading is believed, and the denominator is the claim's own size.
func TestAClaimOnItsOwnFilesystemIsMeasured(t *testing.T) {
	out := joinVolumeUsage(
		map[string]claimFacts{"store/db-data": {declared: 8 * gib, volume: "pv-1"}},
		map[string][]string{"store/db-data": {"node-a"}},
		map[string]kubeletstats.Summary{"node-a": summaryWith("store/db-data", 6*gib, 8*gib)},
		full(1, 1),
	)

	got := out["store/db-data"]
	if !got.Measured {
		t.Fatalf("not measured: %+v", got)
	}
	if got.UsedBytes != 6*gib {
		t.Errorf("used = %d, want %d", got.UsedBytes, 6*gib)
	}
	if got.CapacityBytes != 8*gib {
		t.Errorf("capacity = %d, want the claim's declared %d — the kubelet's own "+
			"capacity is the filesystem it found under the mount point, and the "+
			"claim's is the number an operator would grow", got.CapacityBytes, 8*gib)
	}
	if !strings.Contains(got.Note, "node-a") {
		t.Errorf("note = %q, want it to name the kubelet the figure came from", got.Note)
	}
}

// The guard the whole reading depends on, at the shape that produced it: a small
// claim on a large share. Believing it would draw a bar at 30% for a claim whose
// fullness is unknown, and it would be *stable*, so nobody would ever find out.
func TestASharedExportIsRefusedRatherThanDrawn(t *testing.T) {
	out := joinVolumeUsage(
		map[string]claimFacts{"kubecron/kubecron-data": {declared: 500 << 20}},
		map[string][]string{"kubecron/kubecron-data": {"node-a"}},
		// 12 TiB used of a 40 TiB NAS, under a 500 MiB claim: an
		// `nfs-nas-nvme` storage class is exactly this.
		map[string]kubeletstats.Summary{
			"node-a": summaryWith("kubecron/kubecron-data", 12*tib, 40*tib),
		},
		full(1, 1),
	)

	got := out["kubecron/kubecron-data"]
	if got.Measured {
		t.Fatalf("a 40 TiB filesystem was believed to be a 500 MiB claim: %+v — that "+
			"draws a stable, plausible bar about somebody else's disk", got)
	}
	if got.UsedBytes != 0 || got.CapacityBytes != 0 {
		t.Errorf("figures survived the refusal (%+v); a caller reading them without "+
			"checking Measured would draw the share's fullness", got)
	}
	for _, want := range []string{"40.0 TiB", "500 MiB", "NFS", "node-a"} {
		if !strings.Contains(got.Note, want) {
			t.Errorf("note = %q, want it to mention %q — an operator has to be able "+
				"to see that the two sizes disagree", got.Note, want)
		}
	}
}

// A claim nothing has open is not a claim that could not be read. There is
// nothing to read, and no permission would change it — so the sentence must not
// send anybody after an RBAC grant.
func TestAClaimNoPodMountsSaysSoRatherThanBlamingThePermission(t *testing.T) {
	out := joinVolumeUsage(
		map[string]claimFacts{"store/cold": {declared: 8 * gib}},
		nil, nil, full(0, 0),
	)

	got := out["store/cold"]
	if got.Measured {
		t.Fatal("measured, from nothing")
	}
	if !strings.Contains(got.Note, "No running pod") {
		t.Errorf("note = %q, want it to say no running pod has it mounted", got.Note)
	}
	if strings.Contains(got.Note, "nodes/proxy") {
		t.Errorf("note = %q, which blames a permission for a claim that simply is "+
			"not mounted anywhere", got.Note)
	}
}

// A partial answer is reported as partial. "3 of 40 kubelets answered" and
// "40 of 40" produce the same em dash on the rows that lost, and only the
// sentence tells them apart — which on a list page has to travel with the rows,
// because a table has no Availability banner to put it in.
func TestAPartialKubeletAnswerSaysHowPartial(t *testing.T) {
	out := joinVolumeUsage(
		map[string]claimFacts{
			"store/here":  {declared: 8 * gib},
			"store/there": {declared: 8 * gib},
		},
		map[string][]string{
			"store/here":  {"node-a"},
			"store/there": {"node-z"},
		},
		map[string]kubeletstats.Summary{"node-a": summaryWith("store/here", 1*gib, 8*gib)},
		kubeletstats.Coverage{
			Asked: 40, Answered: 3,
			// Deliberately does not name node-z: if the test used
			// `nodes "node-z" not found`, the mutation that stops the
			// sentence naming the node at all would stay green — the name would be
			// reaching the assertion through the *error text*, so the assertion
			// would be testing that the error is quoted and not that the claim's own
			// node is named. Two facts, two sources.
			FirstErr: errors.New("forbidden: User cannot get resource nodes/proxy"),
		},
	)

	if !out["store/here"].Measured {
		t.Fatal("the claim on the node that answered lost its reading too")
	}

	lost := out["store/there"]
	if lost.Measured {
		t.Fatal("a claim on a node nobody heard from was measured")
	}
	for _, want := range []string{"3 of 40", "node-z", "10250", "forbidden"} {
		if !strings.Contains(lost.Note, want) {
			t.Errorf("note = %q, want it to mention %q", lost.Note, want)
		}
	}
}

// The other silence: a kubelet that answered and said nothing about this volume.
// That is a pod between scheduling and mounting, not a permission problem, and
// the two must not share a sentence.
func TestAKubeletThatAnsweredAndDidNotMentionTheVolumeSaysThatInstead(t *testing.T) {
	out := joinVolumeUsage(
		map[string]claimFacts{"store/db-data": {declared: 8 * gib}},
		map[string][]string{"store/db-data": {"node-a"}},
		map[string]kubeletstats.Summary{"node-a": {Pods: []kubeletstats.Pod{{}}}},
		full(1, 1),
	)

	got := out["store/db-data"]
	if got.Measured {
		t.Fatal("measured, from a kubelet that reported no volumes")
	}
	if !strings.Contains(got.Note, "node-a") || !strings.Contains(got.Note, "mounting") {
		t.Errorf("note = %q, want it to name the node and say the mount has not "+
			"finished", got.Note)
	}
	if strings.Contains(got.Note, "nodes/proxy") {
		t.Errorf("note = %q, which reports a permission problem on a node that "+
			"answered", got.Note)
	}
}

// An RWX claim is mounted on several nodes at once, and a claim whose first node
// is unreachable is not a claim nobody can measure. Which node wins has to be
// stable, or an RWX claim flips between three slightly different readings every
// ten seconds.
func TestAClaimMountedOnSeveralNodesTakesTheFirstThatAnswered(t *testing.T) {
	out := joinVolumeUsage(
		map[string]claimFacts{"media/shared": {declared: 100 * gib}},
		map[string][]string{"media/shared": {"node-a", "node-b", "node-c"}},
		map[string]kubeletstats.Summary{
			"node-b": summaryWith("media/shared", 40*gib, 100*gib),
			"node-c": summaryWith("media/shared", 41*gib, 100*gib),
		},
		full(2, 3),
	)

	got := out["media/shared"]
	if !got.Measured || got.UsedBytes != 40*gib {
		t.Fatalf("got %+v, want node-b's 40 GiB — the nodes are sorted so the same "+
			"one wins on every render", got)
	}
}

// A capacity of zero is not a filesystem: it is the one value that would make a
// bar divide by nothing.
func TestAVolumeWithNoCapacityIsNotAMeasurement(t *testing.T) {
	out := joinVolumeUsage(
		map[string]claimFacts{"store/db-data": {declared: 8 * gib}},
		map[string][]string{"store/db-data": {"node-a"}},
		map[string]kubeletstats.Summary{"node-a": summaryWith("store/db-data", 6*gib, 0)},
		full(1, 1),
	)
	if out["store/db-data"].Measured {
		t.Fatalf("measured against a capacity of zero: %+v", out["store/db-data"])
	}
}

// Every claim in scope gets an entry, which is the opposite of the rule the rest
// of this console follows — and it is the rule that lets a list page say
// anything at all about an absence.
func TestEveryClaimGetsASentence(t *testing.T) {
	claims := map[string]claimFacts{
		"a/one": {declared: gib}, "b/two": {declared: gib}, "c/three": {declared: gib},
	}
	out := joinVolumeUsage(claims, nil, nil, full(0, 0))
	if len(out) != len(claims) {
		t.Fatalf("%d entries for %d claims", len(out), len(claims))
	}
	for key, volume := range out {
		if volume.Note == "" {
			t.Errorf("%s has no note, so its row draws an em dash with nothing "+
				"explaining it", key)
		}
	}
}

// --- which nodes are asked ---------------------------------------------------

func mountingPod(namespace, name, node, claim string, phase corev1.PodPhase) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PodSpec{
			NodeName: node,
			Volumes: []corev1.Volume{{
				Name: "data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim},
				},
			}},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

// The fan-out is bounded by nodes hosting a *running* pod with a claim mounted,
// which is what keeps a namespace-scoped table at one or two proxied requests
// instead of one per node in the cluster. A Pending pod's node has mounted
// nothing yet and a Succeeded one's has unmounted it; asking either spends one
// of five worker slots on a figure that cannot arrive.
func TestOnlyTheNodesThatCouldAnswerAreAsked(t *testing.T) {
	pods := []corev1.Pod{
		mountingPod("store", "db-0", "node-a", "db-data", corev1.PodRunning),
		mountingPod("store", "db-1", "node-a", "db-data", corev1.PodRunning),
		mountingPod("store", "job-1", "node-b", "db-data", corev1.PodSucceeded),
		mountingPod("store", "new-0", "node-c", "cache", corev1.PodPending),
		// Placed nowhere yet.
		mountingPod("store", "new-1", "", "cache", corev1.PodRunning),
	}

	mounts := claimMounts(pods, "")
	if got := mounts["store/db-data"]; len(got) != 1 || got[0] != "node-a" {
		t.Errorf("db-data is on %v, want [node-a] once — two pods of one StatefulSet "+
			"on one node is one kubelet to ask, not two", got)
	}
	if got := mounts["store/cache"]; len(got) != 0 {
		t.Errorf("cache is on %v, want nothing: neither pod has mounted it", got)
	}
	if got := nodesToAsk(mounts); len(got) != 1 || got[0] != "node-a" {
		t.Errorf("asking %v, want [node-a] — every one of these is a request the API "+
			"server proxies to a kubelet", got)
	}
}

// A PersistentVolumeClaim reference in a pod spec is namespace-local. Joining
// across namespaces would put one team's fullness under another team's
// identically named claim — and `db-data` is not an unusual name.
func TestAPodOnlySpeaksForAClaimInItsOwnNamespace(t *testing.T) {
	pods := []corev1.Pod{
		mountingPod("store", "db-0", "node-a", "db-data", corev1.PodRunning),
		mountingPod("other", "db-0", "node-b", "db-data", corev1.PodRunning),
	}

	all := claimMounts(pods, "")
	if got := all["store/db-data"]; len(got) != 1 || got[0] != "node-a" {
		t.Errorf("store/db-data is on %v, want [node-a] only", got)
	}
	if got := all["other/db-data"]; len(got) != 1 || got[0] != "node-b" {
		t.Errorf("other/db-data is on %v, want [node-b] only", got)
	}

	// And when one namespace was asked about, the other's pods are not evidence
	// about anything on the page.
	narrowed := claimMounts(pods, "store")
	if _, ok := narrowed["other/db-data"]; ok {
		t.Errorf("a pod outside the namespace in scope reached the join: %v", narrowed)
	}
	if got := narrowed["store/db-data"]; len(got) != 1 {
		t.Errorf("store/db-data is on %v, want [node-a]", got)
	}
}

// --- how often the claims are listed -----------------------------------------

// withKubelet is the fake clientset with a core REST client that reaches a
// kubelet's stats summary through the node proxy, which the fake's own does not.
type withKubelet struct {
	*fake.Clientset
	rest rest.Interface
}

func (c withKubelet) CoreV1() typedcorev1.CoreV1Interface {
	return kubeletCoreV1{CoreV1Interface: c.Clientset.CoreV1(), rest: c.rest}
}

type kubeletCoreV1 struct {
	typedcorev1.CoreV1Interface
	rest rest.Interface
}

func (c kubeletCoreV1) RESTClient() rest.Interface { return c.rest }

// The PersistentVolumes page needs the claims twice, for `spec.volumeName` and
// for the reading, and must list them once: the list is every claim in the
// cluster, paged, on every render, and nothing caches it between the two.
func TestThePersistentVolumesPageListsTheClaimsOnce(t *testing.T) {
	kubelet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/nodes/node-a/proxy/stats/summary" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"pods":[{"podRef":{"name":"db-0","namespace":"store"},"volume":[`+
			`{"name":"data","pvcRef":{"name":"db-data","namespace":"store"},"usedBytes":%d,"capacityBytes":%d}]}]}`,
			6*gib, 8*gib)
	}))
	t.Cleanup(kubelet.Close)
	proxy, err := rest.RESTClientFor(&rest.Config{
		Host:    kubelet.URL,
		APIPath: "/api",
		ContentConfig: rest.ContentConfig{
			GroupVersion:         &corev1.SchemeGroupVersion,
			NegotiatedSerializer: scheme.Codecs.WithoutConversion(),
		},
	})
	if err != nil {
		t.Fatalf("a REST client for the fake kubelet: %v", err)
	}

	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "db-data", Namespace: "store"},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pv-1"},
		Status: corev1.PersistentVolumeClaimStatus{
			Phase:    corev1.ClaimBound,
			Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("8Gi")},
		},
	}
	pod := mountingPod("store", "db-0", "node-a", "db-data", corev1.PodRunning)
	clientset := fake.NewSimpleClientset(claim, &pod)

	got := volumeNameUsageFrom(context.Background(), withKubelet{Clientset: clientset, rest: proxy})
	volume := got["pv-1"].Volume
	if volume == nil || !volume.Measured || volume.UsedBytes != 6*gib {
		t.Fatalf("the claim's volume is not measured at 6 GiB: %+v", got)
	}

	lists := 0
	for _, action := range clientset.Actions() {
		if action.GetVerb() == "list" && action.GetResource().Resource == "persistentvolumeclaims" {
			lists++
		}
	}
	if lists != 1 {
		t.Errorf("%d lists of persistentvolumeclaims for one render, want 1", lists)
	}
}
