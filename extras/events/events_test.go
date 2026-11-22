package events

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/thonicdev/kubetower/extension"
)

const backOffMsg = "Back-off restarting failed container app in pod %s_demo-broken(10847df6-7c6e-4e5d-835c-11592cc352ff)"

func TestBothEventShapesAreRead(t *testing.T) {
	min := func(n int) time.Time { return testNow.Add(-time.Duration(n) * time.Minute) }
	cases := []struct {
		name                string
		doc                 map[string]any
		count               int
		first, last         time.Time
		wantReason, wantTyp string
	}{
		{"kubelet: count and both timestamps", oldEvent("a", "Pod", "p", "BackOff", "m", "Warning", 52165, min(50), min(2)), 52165, min(50), min(2), "BackOff", "Warning"},
		{"scheduler: null count reads 1, eventTime is both ends", newEvent("a", "Pod", "p", "Scheduled", "m", "Normal", min(5), 0), 1, min(5), min(5), "Scheduled", "Normal"},
		{"scheduler: series.count first, lastObservedTime", newEvent("a", "Pod", "p", "FailedScheduling", "m", "Warning", min(10), 7), 7, min(10), min(9), "FailedScheduling", "Warning"},
		{"count zero reads 1", func() map[string]any {
			d := oldEvent("a", "Pod", "p", "X", "m", "Normal", 0, min(3), min(3))
			return d
		}(), 1, min(3), min(3), "X", "Normal"},
		{"neither firstTimestamp nor eventTime: creationTimestamp", func() map[string]any {
			d := oldEvent("a", "Pod", "p", "X", "m", "Normal", 4, min(8), min(8))
			delete(d, "firstTimestamp")
			delete(d, "lastTimestamp")
			return d
		}(), 4, min(8), min(8), "X", "Normal"},
		{"series beats count", func() map[string]any {
			d := newEvent("a", "Pod", "p", "X", "m", "Normal", min(4), 9)
			d["count"] = int64(2)
			return d
		}(), 9, min(4), min(3), "X", "Normal"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := unstructured.Unstructured{Object: c.doc}
			ev := readEvent(&u)
			if ev.count != c.count || !ev.first.Equal(c.first) || !ev.last.Equal(c.last) || ev.reason != c.wantReason || ev.typ != c.wantTyp {
				t.Errorf("got count=%d first=%v last=%v reason=%s type=%s; want count=%d first=%v last=%v",
					ev.count, ev.first, ev.last, ev.reason, ev.typ, c.count, c.first, c.last)
			}
		})
	}
}

func TestNormalise(t *testing.T) {
	cases := []struct{ in, name, want string }{
		{fmt.Sprintf(backOffMsg, "crasher-7474496469-kzs5j"), "crasher-7474496469-kzs5j", "Back-off restarting failed container app in pod *_demo-broken(*)"},
		{"Failed to pull image \"ghcr.io/x/app@sha256:" + strings.Repeat("ab12", 16) + "\"", "node-x", "Failed to pull image \"ghcr.io/x/app@*\""},
		{"connect to 10.0.3.17:8080 refused after 30 tries", "node-x", "connect to * refused after * tries"},
		{"0/5 nodes are available: 2 Insufficient cpu, 3 node(s) had untolerated taint", "node-x", "*/* nodes are available: * Insufficient cpu, * node(s) had untolerated taint"},
		{"version 1.2.3 is not an address", "node-x", "version *.*.* is not an address"},
		{"no digits here", "", "no digits here"},
	}
	for _, c := range cases {
		if got := normalise(c.in, c.name); got != c.want {
			t.Errorf("normalize(%q):\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestOneCauseAcrossManyPods(t *testing.T) {
	h := newHost(t)
	for i := 0; i < 30; i++ {
		name := fmt.Sprintf("crasher-7474496469-%05d", i)
		uid := fmt.Sprintf("%08x-7c6e-4e5d-835c-11592cc352ff", i)
		e := oldEvent("demo", "Pod", name, "BackOff", fmt.Sprintf("Back-off restarting failed container app in pod %s_demo(%s)", name, uid),
			"Warning", 10+i, testNow.Add(-30*time.Minute), testNow.Add(-time.Minute))
		h.add("events", e)
	}
	// A different cause stays apart.
	h.add("events", oldEvent("demo", "Pod", "other", "Failed", "Error: ImagePullBackOff", "Warning", 3, testNow.Add(-5*time.Minute), testNow.Add(-time.Minute)))

	r := page(t, newExt(), h, "/")
	bo := causeOf(r, "BackOff")
	if bo == nil || len(r.Causes) != 2 {
		t.Fatalf("causes = %+v, want BackOff and Failed", r.Causes)
	}
	if bo.Objects != 30 || len(bo.Groups) != 30 {
		t.Errorf("BackOff objects %d groups %d, want 30 pods in one cause", bo.Objects, len(bo.Groups))
	}
	wantCount := 0
	for i := 0; i < 30; i++ {
		wantCount += 10 + i
	}
	if bo.Count != wantCount {
		t.Errorf("BackOff count %d, want %d", bo.Count, wantCount)
	}
	if !strings.HasPrefix(bo.Message, "Back-off restarting failed container app in pod *_demo(*)") {
		t.Errorf("normalized message %q", bo.Message)
	}
	if len(bo.Examples) != MaxExamples {
		t.Errorf("%d examples, want %d", len(bo.Examples), MaxExamples)
	}
	if r.Tiles.Warnings.Count != wantCount+3 || r.Tiles.ObjectsTouched.Count != 31 {
		t.Errorf("tiles %+v", r.Tiles)
	}
}

func TestEventsAreDeduplicatedPerObject(t *testing.T) {
	h := newHost(t)
	// Two containers (or an Event recreated after expiry): one object, one row.
	h.add("events",
		oldEvent("a", "Pod", "p", "BackOff", "Back-off restarting failed container x in pod p_a(1)", "Warning", 5, testNow.Add(-20*time.Minute), testNow.Add(-10*time.Minute)),
		oldEvent("a", "Pod", "p", "BackOff", "Back-off restarting failed container x in pod p_a(2)", "Warning", 7, testNow.Add(-15*time.Minute), testNow.Add(-2*time.Minute)),
	)
	c := causeOf(page(t, newExt(), h, "/"), "BackOff")
	if len(c.Groups) != 1 || c.Groups[0].Count != 12 || c.Objects != 1 {
		t.Fatalf("groups %+v objects %d, want one row of 12", c.Groups, c.Objects)
	}
	if !c.Groups[0].FirstSeen.Equal(testNow.Add(-20*time.Minute)) || !c.Groups[0].LastSeen.Equal(testNow.Add(-2*time.Minute)) {
		t.Errorf("first/last %v %v", c.Groups[0].FirstSeen, c.Groups[0].LastSeen)
	}
}

func TestTheWindowDropsWhatWasLastSeenBeforeIt(t *testing.T) {
	h := newHost(t)
	h.add("events",
		oldEvent("a", "Pod", "old", "Failed", "x", "Warning", 1, testNow.Add(-2*time.Hour), testNow.Add(-100*time.Minute)),
		oldEvent("a", "Pod", "new", "Failed", "x", "Warning", 1, testNow.Add(-10*time.Minute), testNow.Add(-10*time.Minute)))
	e := newExt()
	if c := causeOf(page(t, e, h, "/?window=1h"), "Failed"); c == nil || c.Objects != 1 {
		t.Errorf("1h: %+v", c)
	}
	if c := causeOf(page(t, e, h, "/?window=3h"), "Failed"); c == nil || c.Objects != 2 {
		t.Errorf("3h: %+v", c)
	}
	if c := causeOf(page(t, e, h, "/?window=15m"), "Failed"); c == nil || c.Objects != 1 {
		t.Errorf("15m: %+v", c)
	}
	if rec := serve(t, e, h, "/?window=2d", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad window answered %d", rec.Code)
	}
}

// rollout builds a Deployment web with two revisions and a pod on the new one.
func rolloutCluster(t *testing.T, rolloutAt, firstSeen time.Time) *fakeHost {
	h := newHost(t)
	h.add("replicasets",
		replicaSetDoc("shop", "web-old", "web", 6, rolloutAt.Add(-48*time.Hour), "app:2.13.0"),
		replicaSetDoc("shop", "web-new", "web", 7, rolloutAt, "app:2.14.0"))
	h.add("pods", podDoc("shop", "web-new-abcde", "web-new", "node-a", crashLooping))
	h.add("events", oldEvent("shop", "Pod", "web-new-abcde", "BackOff", "Back-off restarting failed container app in pod web-new-abcde_shop(u)",
		"Warning", 4, firstSeen, firstSeen.Add(time.Minute)))
	return h
}

func TestACauseIsRelatedToTheRolloutThatPrecedesIt(t *testing.T) {
	rolloutAt := testNow.Add(-20 * time.Minute)
	for _, c := range []struct {
		name      string
		firstSeen time.Time
		related   bool
	}{
		{"five minutes after", rolloutAt.Add(5 * time.Minute), true},
		{"just inside thirty minutes", rolloutAt.Add(30 * time.Minute), true},
		{"after thirty minutes", rolloutAt.Add(31 * time.Minute), false},
		{"before the change", rolloutAt.Add(-time.Minute), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := rolloutCluster(t, rolloutAt, c.firstSeen)
			e := newExt()
			e.now = func() time.Time { return rolloutAt.Add(40 * time.Minute) }
			r := page(t, e, h, "/")
			bo := causeOf(r, "BackOff")
			if bo == nil {
				t.Fatalf("no BackOff cause: %+v", r.Causes)
			}
			ch := changeOf(r, "rollout/shop/web/7")
			if ch == nil || ch.Revision != 7 || len(ch.ImagesBefore) != 1 || ch.ImagesBefore[0] != "app:2.13.0" || ch.ImagesAfter[0] != "app:2.14.0" {
				t.Fatalf("rollout change = %+v", ch)
			}
			if got := bo.Since == "rollout/shop/web/7"; got != c.related {
				t.Errorf("since = %q, related %v, want related %v", bo.Since, got, c.related)
			}
			if got := bo.Groups[0].Since != ""; got != c.related {
				t.Errorf("group since = %q", bo.Groups[0].Since)
			}
		})
	}
}

func TestTheMostRecentPrecedingChangeWins(t *testing.T) {
	rolloutAt := testNow.Add(-25 * time.Minute)
	h := rolloutCluster(t, rolloutAt, testNow.Add(-5*time.Minute))
	h.add("replicasets") // keep
	// A ConfigMap the new ReplicaSet mounts, edited 10 minutes ago.
	h.objects["replicasets"][1] = unstructured.Unstructured{Object: replicaSetDoc("shop", "web-new", "web", 7, rolloutAt, "app:2.14.0", "web-config")}
	h.add("configmaps", configMapDoc("shop", "web-config", "kubectl-client-side-apply", testNow.Add(-10*time.Minute)))
	r := page(t, newExt(), h, "/")
	if got := causeOf(r, "BackOff").Since; !strings.HasPrefix(got, "config/shop/web-config/") {
		t.Errorf("since = %q, want the ConfigMap edit (more recent than the rollout)", got)
	}
	cm := changeOf(r, causeOf(r, "BackOff").Since)
	if cm == nil || cm.Manager != "kubectl-client-side-apply" || !cm.Referenced {
		t.Errorf("config change = %+v", cm)
	}
}

func TestAConfigMapNoWorkloadReferencesExplainsNothing(t *testing.T) {
	rolloutAt := testNow.Add(-3 * time.Hour)
	h := rolloutCluster(t, rolloutAt, testNow.Add(-5*time.Minute))
	h.add("configmaps", configMapDoc("shop", "unrelated", "someone", testNow.Add(-10*time.Minute)))
	r := page(t, newExt(), h, "/")
	if got := causeOf(r, "BackOff").Since; got != "" {
		t.Errorf("since = %q, want none: the rollout is older than 30 minutes and the ConfigMap is not referenced", got)
	}
	var cm *Change
	for i := range r.Changes {
		if r.Changes[i].Kind == ChangeConfig {
			cm = &r.Changes[i]
		}
	}
	if cm == nil || cm.Referenced {
		t.Errorf("the unreferenced edit should be listed, marked unreferenced: %+v", cm)
	}
	// A ConfigMap's data is never part of the answer.
	if body := fmt.Sprintf("%+v", r); strings.Contains(body, "NEVER-READ-ME") {
		t.Error("a ConfigMap's data reached the answer")
	}
}

func TestACauseIsRelatedToTheNodeCarryingItsPods(t *testing.T) {
	h := newHost(t)
	h.add("nodes", nodeDoc("node-a", testNow.Add(-20*time.Minute)), nodeDoc("node-old", testNow.Add(-72*time.Hour)))
	h.add("pods", podDoc("shop", "solo", "", "node-a", nil), podDoc("shop", "elsewhere", "", "node-old", nil))
	h.add("events",
		oldEvent("shop", "Pod", "solo", "FailedMount", "mount failed", "Warning", 2, testNow.Add(-10*time.Minute), testNow.Add(-9*time.Minute)),
		oldEvent("shop", "Pod", "elsewhere", "Unhealthy", "probe failed", "Warning", 2, testNow.Add(-10*time.Minute), testNow.Add(-9*time.Minute)))
	r := page(t, newExt(), h, "/")
	if got := causeOf(r, "FailedMount").Since; !strings.HasPrefix(got, "node-added//node-a/") {
		t.Errorf("FailedMount since = %q, want the new node", got)
	}
	if got := causeOf(r, "Unhealthy").Since; got != "" {
		t.Errorf("Unhealthy since = %q: its node is old", got)
	}
}

func TestANodeNotReadyEventIsAChange(t *testing.T) {
	h := newHost(t)
	h.add("pods", podDoc("shop", "p", "", "node-a", nil))
	h.add("events",
		oldEvent("default", "Node", "node-a", "NodeNotReady", "Node node-a status is now: NodeNotReady", "Normal", 1, testNow.Add(-15*time.Minute), testNow.Add(-15*time.Minute)),
		oldEvent("shop", "Pod", "p", "Unhealthy", "probe failed", "Warning", 2, testNow.Add(-10*time.Minute), testNow.Add(-9*time.Minute)))
	for _, path := range []string{"/", "/?namespace=shop"} {
		r := page(t, newExt(), h, path)
		if got := causeOf(r, "Unhealthy").Since; !strings.HasPrefix(got, "node-notready//node-a/") {
			t.Errorf("%s: since = %q, want the NodeNotReady", path, got)
		}
	}
}

func TestARollbackIsDatedByTheEventThatScaledTheOldReplicaSetUp(t *testing.T) {
	h := newHost(t)
	// web-old (rev 5) was created long ago and took revision 8 when it was rolled back to;
	// web-bad (rev 7) is the newer ReplicaSet.
	h.add("replicasets",
		replicaSetDoc("shop", "web-old", "web", 8, testNow.Add(-72*time.Hour), "app:1"),
		replicaSetDoc("shop", "web-bad", "web", 7, testNow.Add(-2*time.Hour), "app:2"))
	h.add("events", oldEvent("shop", "Deployment", "web", "ScalingReplicaSet", "Scaled up replica set web-old from 0 to 3", "Normal", 1,
		testNow.Add(-12*time.Minute), testNow.Add(-12*time.Minute)))
	r := page(t, newExt(), h, "/")
	ch := changeOf(r, "rollback/shop/web/8")
	if ch == nil || !ch.Time.Equal(testNow.Add(-12*time.Minute)) || ch.ImagesBefore[0] != "app:2" || ch.ImagesAfter[0] != "app:1" {
		t.Fatalf("rollback change = %+v; changes %+v", ch, r.Changes)
	}
	for _, c := range r.Changes {
		if c.Kind == ChangeScaling {
			t.Errorf("a rollback's own scale-up read as a scaling: %+v", c)
		}
	}
}

func TestScalingIsTheCurrentReplicaSetOnceTheRolloutSettled(t *testing.T) {
	h := newHost(t)
	h.add("replicasets",
		replicaSetDoc("shop", "web-old", "web", 1, testNow.Add(-3*time.Hour), "app:1"),
		replicaSetDoc("shop", "web-cur", "web", 2, testNow.Add(-50*time.Minute), "app:2"))
	h.add("events",
		// the rollout's own step: the new one scaled up right after creation, the old one down
		oldEvent("shop", "Deployment", "web", "ScalingReplicaSet", "Scaled up replica set web-cur from 0 to 1", "Normal", 1, testNow.Add(-49*time.Minute), testNow.Add(-49*time.Minute)),
		oldEvent("shop", "Deployment", "web", "ScalingReplicaSet", "Scaled down replica set web-old from 3 to 2", "Normal", 1, testNow.Add(-48*time.Minute), testNow.Add(-48*time.Minute)),
		// a scaling: long after
		oldEvent("shop", "Deployment", "web", "ScalingReplicaSet", "Scaled up replica set web-cur from 3 to 6", "Normal", 1, testNow.Add(-5*time.Minute), testNow.Add(-5*time.Minute)),
		oldEvent("shop", "HorizontalPodAutoscaler", "web", "SuccessfulRescale", "New size: 8; reason: cpu above target", "Normal", 1, testNow.Add(-3*time.Minute), testNow.Add(-3*time.Minute)))
	r := page(t, newExt(), h, "/")
	var scalings []Change
	for _, c := range r.Changes {
		if c.Kind == ChangeScaling {
			scalings = append(scalings, c)
		}
	}
	if len(scalings) != 2 || !strings.Contains(scalings[1].Detail+scalings[0].Detail, "from 3 to 6") {
		t.Errorf("scalings = %+v, want the 3 to 6 step and the HPA rescale only", scalings)
	}
}

func TestTheTilesReadPodStatesNotEvents(t *testing.T) {
	h := newHost(t)
	h.add("pods",
		podDoc("a", "loop-1", "", "n", crashLooping), podDoc("a", "loop-2", "", "n", crashLooping),
		podDoc("a", "oom-1", "", "n", oomKilled(testNow.Add(-10*time.Minute))),
		podDoc("a", "oom-old", "", "n", oomKilled(testNow.Add(-5*time.Hour))),
		podDoc("a", "fine", "", "n", nil))
	h.add("events",
		oldEvent("a", "Pod", "x", "FailedScheduling", "0/3 nodes are available: 3 Insufficient cpu.", "Warning", 4, testNow.Add(-9*time.Minute), testNow.Add(-1*time.Minute)),
		oldEvent("a", "Pod", "y", "FailedScheduling", "0/3 nodes are available: 3 Insufficient cpu.", "Warning", 2, testNow.Add(-9*time.Minute), testNow.Add(-1*time.Minute)),
		oldEvent("a", "Pod", "z", "Pulled", "pulled", "Normal", 1, testNow, testNow))
	r := page(t, newExt(), h, "/")
	tl := r.Tiles
	if tl.CrashLoop.Count != 2 || tl.OOMKilled.Count != 1 || tl.FailedScheduling.Count != 6 || tl.Warnings.Count != 6 || tl.ObjectsTouched.Count != 2 {
		t.Errorf("tiles %+v", tl)
	}
	if len(r.Pods) != 3 {
		t.Errorf("unhealthy pods %+v, want 3", r.Pods)
	}
	if c := causeOf(r, "FailedScheduling"); c == nil || c.Objects != 2 || strings.Contains(c.Message, "3 Insufficient") {
		t.Errorf("FailedScheduling cause %+v: numbers must be normalized", c)
	}
}

func TestARefusedSecondaryReadIsMarkedNeverEmpty(t *testing.T) {
	for _, resource := range []string{"nodes", "configmaps", "replicasets", "pods"} {
		t.Run(resource, func(t *testing.T) {
			h := rolloutCluster(t, testNow.Add(-20*time.Minute), testNow.Add(-15*time.Minute))
			h.denied[resource] = true
			r := page(t, newExt(), h, "/")
			s := map[string]Section{"nodes": r.Sources.Nodes, "configmaps": r.Sources.ConfigMaps,
				"replicasets": r.Sources.ReplicaSets, "pods": r.Sources.Pods}
			for name, sec := range s {
				want := StateRead
				if name == resource {
					want = StateDenied
				}
				if sec.State != want {
					t.Errorf("source %s = %q, want %q", name, sec.State, want)
				}
			}
			if r.Sources.Events.State != StateRead || causeOf(r, "BackOff") == nil {
				t.Errorf("the Events must still be read: %+v", r.Sources.Events)
			}
			if resource == "pods" && (r.Tiles.OOMKilled.State != StateDenied || r.Tiles.CrashLoop.State != StateDenied) {
				t.Errorf("the pod tiles must say they were refused: %+v", r.Tiles)
			}
			if resource == "pods" && r.Tiles.CrashLoop.Count != 1 {
				t.Errorf("CrashLoop falls back to the BackOff pods: %+v", r.Tiles.CrashLoop)
			}
		})
	}
}

func TestARefusedEventsReadAnswersTheRefusal(t *testing.T) {
	h := newHost(t)
	h.denied["events"] = true
	rec := serve(t, newExt(), h, "/", "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("a refused Events read answered %d: %s", rec.Code, rec.Body.String())
	}
}

func TestANarrowedReadSaysSo(t *testing.T) {
	h := rolloutCluster(t, testNow.Add(-20*time.Minute), testNow.Add(-15*time.Minute))
	h.allowed = []string{"shop"}
	r := page(t, newExt(), h, "/")
	if r.Complete || r.Note == "" {
		t.Errorf("complete %v note %q: a narrowed answer must say so", r.Complete, r.Note)
	}
	r = page(t, newExt(), h, "/?namespace=shop")
	if !r.Complete {
		t.Errorf("a namespace the person may list is complete: %+v", r.Note)
	}
}

func TestTheCapsAreHeldAndSaid(t *testing.T) {
	h := newHost(t)
	for i := 0; i < MaxGroups+50; i++ {
		h.add("events", oldEvent("a", "Pod", fmt.Sprintf("p%d", i), "Failed", "same cause", "Warning", 1, testNow.Add(-time.Minute), testNow.Add(-time.Minute)))
	}
	for i := 0; i < MaxCauses+20; i++ {
		h.add("events", oldEvent("a", "Pod", "q", fmt.Sprintf("Reason%d", i), "x", "Warning", 1, testNow.Add(-time.Minute), testNow.Add(-time.Minute)))
	}
	r := page(t, newExt(), h, "/")
	if !r.Truncated {
		t.Error("truncated not said")
	}
	total := 0
	for _, c := range r.Causes {
		total += len(c.Groups)
	}
	if total > MaxGroups || len(r.Causes) > MaxCauses {
		t.Errorf("groups %d (max %d), causes %d (max %d)", total, MaxGroups, len(r.Causes), MaxCauses)
	}
	// What was cut still counts in the totals.
	if c := causeOf(r, "Failed"); c.Count != MaxGroups+50 || c.Objects != MaxGroups+50 {
		t.Errorf("the Failed cause counts %d events %d objects, want all %d", c.Count, c.Objects, MaxGroups+50)
	}
}

func TestOnlyWhatIsAggregatedIsKept(t *testing.T) {
	h := rolloutCluster(t, testNow.Add(-20*time.Minute), testNow.Add(-15*time.Minute))
	h.add("configmaps", configMapDoc("shop", "web-config", "m", testNow.Add(-1*time.Minute)))
	page(t, newExt(), h, "/")
	for _, q := range h.queries {
		if q.GVR.Resource != "nodes" && q.Each == nil && q.FieldSelector == "" {
			t.Errorf("a whole-list read of %s was made without Each", q.GVR.Resource)
		}
		if q.GVR.Resource == "nodes" && !q.Columns {
			t.Error("nodes must be read as a table")
		}
	}
}

func TestObjectRoute(t *testing.T) {
	rolloutAt := testNow.Add(-20 * time.Minute)
	h := rolloutCluster(t, rolloutAt, rolloutAt.Add(5*time.Minute))
	h.add("pods", podDoc("shop", "other-xyz", "", "node-z", crashLooping))
	h.add("events", oldEvent("shop", "Pod", "other-xyz", "Unhealthy", "probe failed", "Warning", 9, testNow.Add(-5*time.Minute), testNow.Add(-4*time.Minute)))
	e := newExt()
	for _, path := range []string{"/object/deployments/shop/web", "/object/replicasets/shop/web-new", "/object/pods/shop/web-new-abcde"} {
		rec := serve(t, e, h, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		r := decode[Response](t, rec)
		if r.Object == nil || r.Object.Namespace != "shop" {
			t.Errorf("%s: object %+v", path, r.Object)
		}
		if causeOf(r, "Unhealthy") != nil {
			t.Errorf("%s: another workload's cause leaked in", path)
		}
		if bo := causeOf(r, "BackOff"); bo == nil || bo.Since != "rollout/shop/web/7" {
			t.Errorf("%s: causes %+v", path, r.Causes)
		}
		if r.Tiles.Warnings.Count != 4 || r.Tiles.CrashLoop.Count != 1 {
			t.Errorf("%s: tiles %+v", path, r.Tiles)
		}
	}
	for path, want := range map[string]int{"/object/nodes/shop/x": 400, "/object/pods/Shop/x": 400, "/object/pods/shop/BAD_name": 400} {
		if rec := serve(t, e, h, path, ""); rec.Code != want {
			t.Errorf("%s answered %d, want %d", path, rec.Code, want)
		}
	}
}

func TestLineRoute(t *testing.T) {
	rolloutAt := testNow.Add(-20 * time.Minute)
	h := rolloutCluster(t, rolloutAt, rolloutAt.Add(5*time.Minute))
	e := newExt()

	rec := serve(t, e, h, "/line/deployments/shop/web", "")
	l := decode[Line](t, rec)
	if rec.Code != 200 || l.Warnings != 4 || l.LastChange == nil || l.LastChange.ID != "rollout/shop/web/7" ||
		l.LastChangeAgeSeconds == nil || *l.LastChangeAgeSeconds != 1200 {
		t.Errorf("deployment line = %+v", l)
	}

	// A pod reads its own Events by field selector, and not the namespace's.
	before := h.listsOf("events")
	rec = serve(t, e, h, "/line/pods/shop/web-new-abcde", "")
	l = decode[Line](t, rec)
	if rec.Code != 200 || l.Warnings != 4 || l.LastChange == nil || l.LastChange.Kind != ChangeRollout || l.LastChange.Name != "web" {
		t.Errorf("pod line = %+v (%s)", l, rec.Body.String())
	}
	var sel string
	for _, q := range h.queries[len(h.queries)-3:] {
		if q.GVR.Resource == "events" {
			sel = q.FieldSelector
		}
	}
	if h.listsOf("events") != before+1 || sel != "involvedObject.name=web-new-abcde" {
		t.Errorf("the pod line made %d event reads, selector %q", h.listsOf("events")-before, sel)
	}

	// No change and no warning is a line that says so, not an error.
	rec = serve(t, e, h, "/line/jobs/shop/none", "")
	l = decode[Line](t, rec)
	if rec.Code != 200 || l.Warnings != 0 || l.LastChange != nil || l.LastChangeAgeSeconds != nil {
		t.Errorf("empty line = %+v", l)
	}
}

func TestSuggest(t *testing.T) {
	e := newExt()
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	h := newHost(t)
	for i := 0; i < SuggestAt-1; i++ {
		h.add("events", oldEvent("a", "Pod", fmt.Sprintf("p%d", i), "Failed", "x", "Warning", 1, testNow.Add(-time.Minute), testNow.Add(-time.Minute)))
	}
	h.add("events", oldEvent("a", "Pod", "old", "Failed", "x", "Warning", 1, testNow.Add(-3*time.Hour), testNow.Add(-3*time.Hour)),
		oldEvent("a", "Pod", "normal", "Pulled", "x", "Normal", 1, testNow, testNow))
	if got, err := e.Suggest(r, h, "kind"); err != nil || got != "" {
		t.Errorf("19 warnings: %q, %v", got, err)
	}
	h.add("events", oldEvent("a", "Pod", "twentieth", "Failed", "x", "Warning", 1, testNow.Add(-time.Minute), testNow.Add(-time.Minute)))
	got, err := e.Suggest(r, h, "kind")
	if err != nil || !strings.HasPrefix(got, "20 Warning events in the last hour.") {
		t.Errorf("20 warnings: %q, %v", got, err)
	}
	var _ extension.Suggester = e
	h.denied["events"] = true
	if _, err := e.Suggest(r, h, "kind"); err == nil {
		t.Error("a refused read must be an error, not silence")
	}
}

func TestTheExtensionNeverCallsAnotherDoor(t *testing.T) {
	// fakeHost fails the test on any Host method but List; this walks every route.
	h := rolloutCluster(t, testNow.Add(-20*time.Minute), testNow.Add(-15*time.Minute))
	e := newExt()
	for _, p := range []string{"/", "/?namespace=shop&window=3h", "/object/deployments/shop/web", "/line/pods/shop/web-new-abcde", "/line/replicasets/shop/web-new"} {
		if rec := serve(t, e, h, p, ""); rec.Code != 200 {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	if e.Key() != "events" || e.Label() != "Event Intelligence" || e.Summary() == "" || e.Needs() == "" {
		t.Error("identity")
	}
}
