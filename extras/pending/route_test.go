package pending

import (
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// cluster is two nodes - one tainted for GPUs, one nearly full - pods placed
// on them, and pending pods of each kind measured on kind.
func cluster(t *testing.T) *fakeHost {
	h := newHost(t)
	h.add("nodes",
		node("gpu-1", "8", "32Gi", map[string]string{"zone": "a"}, corev1.Taint{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule}),
		node("small-1", "2", "4Gi", map[string]string{"zone": "b"}),
	)
	h.add("pods",
		placed("web", "web-0", "small-1", container("1500m", "1Gi")),
		placed("web", "web-1", "gpu-1", container("100m", "128Mi")),
		// A finished pod frees what it asked for: the selector leaves it out.
		func() *corev1.Pod {
			p := placed("batch", "done", "small-1", container("1", "1Gi"))
			p.Status.Phase = corev1.PodSucceeded
			return p
		}(),
		unschedulable("shop", "api-7f", msgMixed, 10*time.Minute, container("1", "512Mi")),
		unschedulable("shop", "big", msgMemory, time.Hour, container("100m", "64Gi")),
		unschedulable("ml", "trainer", msgGPU, 30*time.Minute, corev1.Container{Name: "t", Resources: corev1.ResourceRequirements{Limits: quantities("nvidia.com/gpu", "1")}}),
		func() *corev1.Pod {
			p := unschedulable("shop", "picky", msgAnti, 5*time.Minute, container("100m", "64Mi"))
			p.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{}}
			return p
		}(),
		func() *corev1.Pod {
			p := unschedulable("shop", "pull", "", 3*time.Minute, container("100m", "64Mi"))
			p.Spec.NodeName = "small-1"
			p.Status.Conditions = nil
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "Back-off pulling image"}}}}
			return p
		}(),
	)
	h.add("events",
		event("shop", "ReplicaSet", "over-quota-77", "FailedCreate", `Error creating: pods "over-quota-77-a" is forbidden: exceeded quota: small, requested: requests.cpu=3, used: requests.cpu=2, limited: requests.cpu=2`, testNow.Add(-2*time.Minute), 1),
		event("shop", "ReplicaSet", "over-quota-77", "FailedCreate", `(combined from similar events): Error creating: pods "over-quota-77-b" is forbidden: exceeded quota: small, requested: requests.cpu=3, used: requests.cpu=2, limited: requests.cpu=2`, testNow.Add(-time.Minute), 4),
		event("shop", "Pod", "big", "FailedScheduling", msgMemory, testNow.Add(-time.Hour), 1),
		event("shop", "Pod", "big", "FailedScheduling", msgMemory, testNow.Add(-30*time.Minute), 12),
	)
	h.add("resourcequotas", &corev1.ResourceQuota{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ResourceQuota"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "small"},
		Status:     corev1.ResourceQuotaStatus{Hard: quantities("requests.cpu", "2"), Used: quantities("requests.cpu", "2")},
	})
	return h
}

func TestThePageFilesEveryPendingPodAndFindsTheClosestNode(t *testing.T) {
	h := cluster(t)
	rec := serve(t, h, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	a := decode[PageAnswer](t, rec)
	if a.Total != 5 || len(a.Rows) != 5 {
		t.Fatalf("total %d rows %d", a.Total, len(a.Rows))
	}
	want := map[Category]int{CatTaints: 1, CatResources: 2, CatPodAffinity: 1, CatStarting: 1}
	for c, n := range want {
		if a.Counts[c] != n {
			t.Errorf("counts[%s] = %d, want %d (%v)", c, a.Counts[c], n, a.Counts)
		}
	}
	if a.Rows[0].Name != "big" {
		t.Errorf("longest waiting first: %s", a.Rows[0].Name)
	}
	rows := map[string]Row{}
	for _, r := range a.Rows {
		rows[r.Name] = r
	}
	// api-7f asks for 1 CPU: small-1 has 400m free (the pod pulling its image
	// there counts), gpu-1 is tainted for GPUs: a node short of room is
	// closer than one that keeps the pod out by a rule.
	if c := rows["api-7f"].Closest; c == nil || c.Node != "small-1" || c.Failing != 1 {
		t.Errorf("api-7f closest = %+v", c)
	}
	if rows["pull"].Stage != StageStarting || !strings.Contains(rows["pull"].Message, "ImagePullBackOff") || rows["pull"].Closest != nil {
		t.Errorf("pull = %+v", rows["pull"])
	}
	// The bars count the running pods and never the finished one.
	for _, b := range a.Bars {
		if b.Name == "small-1" && (b.CPU.Requested != 1600 || b.Pods.Requested != 2) {
			t.Errorf("small-1 bar = %+v", b)
		}
	}
	if len(a.Failures) != 1 {
		t.Fatalf("failures = %+v", a.Failures)
	}
	f := a.Failures[0]
	if f.Category != "quota" || f.Count != 5 || f.Quota == nil || f.Quota.Name != "small" || *f.Quota.Hard != 2000 || *f.Quota.Used != 2000 || *f.Quota.Room {
		t.Errorf("failure = %+v quota %+v", f, f.Quota)
	}
	if strings.HasPrefix(f.Message, "(combined") || strings.HasPrefix(f.Message, "Error creating") {
		t.Errorf("message kept its prefix: %q", f.Message)
	}
	if a.Pods.State != "read" || !a.Pods.Complete || a.Nodes.State != "read" || a.Placed.State != "read" {
		t.Errorf("sections: %+v %+v %+v", a.Pods, a.Nodes, a.Placed)
	}
}

func TestNothingPendingIsAnAnswerNotAFailure(t *testing.T) {
	h := newHost(t)
	h.add("nodes", node("n", "2", "4Gi", nil))
	rec := serve(t, h, "/")
	a := decode[PageAnswer](t, rec)
	if rec.Code != http.StatusOK || a.Total != 0 || a.Rows == nil || a.Failures == nil || !a.Pods.Complete {
		t.Errorf("%d %+v", rec.Code, a)
	}
}

func TestAPersonWhoMayNotListPodsIsToldSo(t *testing.T) {
	h := cluster(t)
	h.denied["pods"] = true
	rec := serve(t, h, "/")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"list_forbidden"`) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	h.denied["pods"], h.unreviewable["pods"] = false, true
	if rec := serve(t, h, "/"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unreviewable: %d", rec.Code)
	}
}

func TestNodesRefusedLeaveTheSchedulersReasons(t *testing.T) {
	h := cluster(t)
	h.denied["nodes"] = true
	a := decode[PageAnswer](t, serve(t, h, "/"))
	if a.Nodes.State != "denied" || a.Placed.State != "denied" || a.Total != 5 || len(a.Bars) != 0 {
		t.Fatalf("nodes %+v total %d bars %d", a.Nodes, a.Total, len(a.Bars))
	}
	for _, r := range a.Rows {
		if r.Closest != nil {
			t.Errorf("%s has a closest node with no nodes read", r.Name)
		}
	}
	v := decode[PodView](t, serve(t, h, "/pod/shop/api-7f"))
	if len(v.Fits) != 0 || len(v.WouldTake) != 1 || !strings.Contains(v.WouldTake[0], "could not be read for you") {
		t.Errorf("view: %+v", v.WouldTake)
	}
}

func TestANarrowedPersonIsToldTheAnswerIsPartial(t *testing.T) {
	h := cluster(t)
	h.allowed = []string{"shop"}
	a := decode[PageAnswer](t, serve(t, h, "/"))
	if a.Pods.Complete || a.Pods.Note == "" || a.Total != 4 {
		t.Errorf("pods %+v total %d", a.Pods, a.Total)
	}
	// The pods on the nodes were narrowed too: free capacity is overstated.
	if a.Placed.Complete || !strings.Contains(a.Placed.Note, "may be more than there is") {
		t.Errorf("placed %+v", a.Placed)
	}
}

func TestAnUnknownClusterIsNotAFailedRead(t *testing.T) {
	h := cluster(t)
	h.unknown = true
	for _, path := range []string{"/", "/pod/shop/big", "/brief/shop/big", "/cells?kind=pods"} {
		if rec := serve(t, h, path); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), CodeUnknownCluster) {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
}

func TestThePodViewDrawsTheMatrixAndSaysWhatItWouldTake(t *testing.T) {
	h := cluster(t)
	v := decode[PodView](t, serve(t, h, "/pod/shop/api-7f"))
	if !v.Pending || v.Row == nil || v.NodesTotal != 2 || len(v.Fits) != 2 {
		t.Fatalf("%+v", v)
	}
	if v.Fits[0].Node != "small-1" {
		t.Errorf("closest first: %s", v.Fits[0].Node)
	}
	if len(v.Columns) != 6 || v.Columns[3].Label != "CPU" {
		t.Errorf("columns %+v", v.Columns)
	}
	joined := strings.Join(v.WouldTake, "\n")
	if !strings.Contains(joined, "On small-1, the closest node: CPU needs 1, 400m free of 2") {
		t.Errorf("would take:\n%s", joined)
	}
	if len(v.Bars) != 2 || v.Bars[0].Name != "small-1" {
		t.Errorf("bars %+v", v.Bars)
	}

	big := decode[PodView](t, serve(t, h, "/pod/shop/big"))
	if !strings.Contains(strings.Join(big.WouldTake, "\n"), "No node has enough Memory free") {
		t.Errorf("big: %v", big.WouldTake)
	}
	if len(big.Events) != 2 || !big.Events[0].First.Before(big.Events[1].First) || big.Events[1].Count != 12 || big.Events[1].Source != "default-scheduler" {
		t.Errorf("events %+v", big.Events)
	}

	gpu := decode[PodView](t, serve(t, h, "/pod/ml/trainer"))
	if !strings.Contains(strings.Join(gpu.WouldTake, "\n"), "No node offers nvidia.com/gpu") {
		t.Errorf("gpu: %v", gpu.WouldTake)
	}

	picky := decode[PodView](t, serve(t, h, "/pod/shop/picky"))
	last := picky.Columns[len(picky.Columns)-1]
	// The pod declares an empty anti-affinity and no placed pod excludes it:
	// the column is computed and every node passes, while the scheduler's
	// count stays beside it for the person to compare.
	if last.Key != PredPodAffinity || !last.Computed || last.Scheduler != 1 {
		t.Errorf("pod affinity column %+v", last)
	}
	if !strings.Contains(strings.Join(picky.WouldTake, "\n"), "Every check computed here passes on small-1") {
		t.Errorf("picky: %v", picky.WouldTake)
	}

	pull := decode[PodView](t, serve(t, h, "/pod/shop/pull"))
	if len(pull.Fits) != 0 || pull.Nodes != nil || pull.Placed != nil || !strings.Contains(pull.WouldTake[0], "cannot be pulled on small-1") {
		t.Errorf("pull: %+v", pull)
	}
}

func TestAPodThatIsNotPendingIsNotDrawnAsOne(t *testing.T) {
	h := cluster(t)
	v := decode[PodView](t, serve(t, h, "/pod/web/web-0"))
	if v.Pending || v.Row != nil || v.Phase != "Running" {
		t.Errorf("%+v", v)
	}
	if rec := serve(t, h, "/pod/web/gone"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), CodeNoSuchPod) {
		t.Errorf("gone: %d %s", rec.Code, rec.Body)
	}
	if b := decode[Brief](t, serve(t, h, "/brief/web/web-0")); b.Pending {
		t.Errorf("brief of a running pod: %+v", b)
	}
	if b := decode[Brief](t, serve(t, h, "/brief/web/gone")); b.Pending {
		t.Errorf("brief of a gone pod: %+v", b)
	}
	b := decode[Brief](t, serve(t, h, "/brief/shop/big"))
	if !b.Pending || b.Row == nil || b.Row.Main != CatResources {
		t.Errorf("brief of big: %+v", b)
	}
	// The brief reads the one pod and nothing else.
	h.queries = nil
	serve(t, h, "/brief/shop/big")
	if len(h.queries) != 1 || h.queries[0].FieldSelector != "metadata.name=big" {
		t.Errorf("brief read %+v", h.queries)
	}
}

func TestBadNamesAreRefusedBeforeAnythingIsRead(t *testing.T) {
	h := cluster(t)
	for _, path := range []string{"/?ns=Not_A_Namespace", "/pod/BAD/x", "/pod/ok/bad..name", "/brief/BAD/x"} {
		if rec := serve(t, h, path); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
	if len(h.queries) != 0 {
		t.Errorf("read %+v", h.queries)
	}
}

func TestTheColumnAnswersEveryPendingPodInOneRead(t *testing.T) {
	h := cluster(t)
	cells := decode[map[string]CellAnswer](t, serve(t, h, "/cells?kind=pods"))
	if len(cells) != 5 {
		t.Fatalf("%v", cells)
	}
	c := cells["shop/big"]
	if c.Text != "Resources" || c.Tone != "bad" || c.Href != "/c/kind/x/pending/shop/big" || !strings.Contains(c.Hint, "Insufficient memory") {
		t.Errorf("big cell %+v", c)
	}
	if cells["shop/pull"].Tone != "warn" {
		t.Errorf("pull cell %+v", cells["shop/pull"])
	}
	if len(h.queries) != 1 {
		t.Errorf("reads: %d", len(h.queries))
	}
	if other := decode[map[string]CellAnswer](t, serve(t, h, "/cells?kind=services")); len(other) != 0 {
		t.Errorf("services: %v", other)
	}
}

func TestTheSuggestionCountsPodsWaitingForANode(t *testing.T) {
	h := cluster(t)
	p := Pending{now: func() time.Time { return testNow }}
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	got, err := p.Suggest(req, h, "kind")
	if err != nil || got != "4 pods have been waiting for a node for more than two minutes. Pending says why, and what it would take to start them." {
		t.Errorf("%q %v", got, err)
	}
	empty := newHost(t)
	if got, err := p.Suggest(req, empty, "kind"); got != "" || err != nil {
		t.Errorf("empty: %q %v", got, err)
	}
	h.denied["pods"] = true
	if _, err := p.Suggest(req, h, "kind"); err == nil {
		t.Error("a refused read must be an error, never no suggestion")
	}
}
