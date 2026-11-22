package rightsizing

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// A kill for memory is remembered across the rollout that
// fixed it. See rollout.go for the evidence and the rule.

// quietMemory is a six-hour window at 2m, flat at v MiB, for pod-1/app.
func quietMemory(v float64) []ContainerHistory {
	return memWindow("app", steadyThenPeak(180, v, v, 0)...)
}

// matured is c in one pod two hours old, the warm-up refusal cleared.
func matured(c Container) []Pod {
	pod := testPod("pod-1", c)
	pod.Age, pod.AgeKnown = 2*time.Hour, true
	return []Pod{pod}
}

func memoryReductions(list []Suggestion) []Suggestion {
	return filter(list, func(s Suggestion) bool {
		return strings.HasPrefix(s.Resource, "Memory") &&
			(s.Action == "Reduce limit" || s.Action == "Reduce request + limit" || s.Action == "Set limit")
	})
}

// revs builds kept revisions of one container "app", number → memory limit.
func revs(pairs ...int64) []revision {
	var out []revision
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, revision{number: pairs[i], limits: map[string]int64{"app": pairs[i+1]}, readable: true})
	}
	return out
}

// The example: request 100Mi, limit 2Gi,
// OOMKilled, 32Mi used, with a window → "Increase limit → 3Gi". Applied, the
// new pods carry no lastState, are older than ten minutes and run quiet. With
// the revisions read, no memory limit reduction follows, and the card says why;
// without them, the old defect is back - which is what this test proves it
// measures.
func TestAnOOMKillIsRememberedAcrossTheRolloutThatFixedIt(t *testing.T) {
	killed := ctr("app", opts{memReq: 100 * mibTest, memLim: 2048 * mibTest, memUse: 32 * mibTest, cpuUse: 5, oomKilled: true, restarts: 1})
	before := Compute([]Workload{workloadOf("api", matured(killed), "", "")}, quietMemory(32), nil)
	inc, ok := find(before, byResourceAction("Memory", "Increase limit"))
	if !ok || inc.SuggestedRaw != 3072*mibTest {
		t.Fatalf("before the rollout: %v, want Increase limit → 3Gi", actions(before))
	}

	// The rollout: every pod replaced, no lastState, the same quiet usage.
	after := ctr("app", opts{memReq: 100 * mibTest, memLim: 2048 * mibTest, memUse: 32 * mibTest, cpuUse: 5})
	apply(&after, inc)
	if after.MemLimit != 3072*mibTest {
		t.Fatalf("applying %+v left the limit at %d", inc, after.MemLimit)
	}
	w := workloadOf("api", matured(after), "", "")

	t.Run("without the revisions the defect reproduces", func(t *testing.T) {
		got := Compute([]Workload{w}, quietMemory(32), nil)
		red, ok := find(got, byResourceAction("Memory", "Reduce limit"))
		if !ok || red.SuggestedRaw >= 2048*mibTest {
			t.Fatalf("got %v: the test no longer reaches the case it guards", actions(got))
		}
	})

	t.Run("with the revisions no reduction is suggested, and the card says why", func(t *testing.T) {
		w := w
		w.Rollout = rolloutOf(revs(1, 2048*mibTest, 2, 3072*mibTest))
		got := Compute([]Workload{w}, quietMemory(32), nil)
		if red := memoryReductions(got); len(red) > 0 {
			t.Fatalf("a reduction of the raised limit: %+v", red)
		}
		for _, s := range filter(got, func(s Suggestion) bool { return s.Resource == "Memory" }) {
			if !hasWarning(s, "raised from 2.00 GiB to 3.00 GiB at revision 2") {
				t.Errorf("memory row %q does not say the limit was raised: %v", s.Action, s.Warnings)
			}
		}
		readings := Readings([]Workload{w}, quietMemory(32))
		raise := readings[0].Containers[0].MemoryRaise
		if raise == nil || raise.From != 2048*mibTest || raise.To != 3072*mibTest || raise.Revision != 2 {
			t.Errorf("the card's raise = %+v, want 2Gi → 3Gi at revision 2", raise)
		}
	})
}

// A Guaranteed pod moves its memory request and limit together, and that pair
// lowers the limit a raise set: refused too.
func TestAGuaranteedPairDoesNotLowerARaisedMemoryLimit(t *testing.T) {
	c := ctr("app", opts{cpuReq: 100, cpuLim: 100, memReq: 3072 * mibTest, memLim: 3072 * mibTest, memUse: 32 * mibTest, cpuUse: 50})
	w := workloadOf("db", matured(c), "", "Guaranteed")
	if got := Compute([]Workload{w}, quietMemory(32), nil); len(memoryReductions(got)) == 0 {
		t.Fatalf("got %v: without a raise the pair should be offered, or this test guards nothing", actions(got))
	}
	w.Rollout = rolloutOf(revs(3, 2048*mibTest, 4, 3072*mibTest))
	if red := memoryReductions(Compute([]Workload{w}, quietMemory(32), nil)); len(red) > 0 {
		t.Errorf("a Guaranteed pair lowering the raised limit: %+v", red)
	}
}

// A raise that removed the limit: "Set limit" now would be the reduction.
func TestALimitRemovedAfterAKillIsNotSetAgain(t *testing.T) {
	c := ctr("app", opts{memReq: 100 * mibTest, memUse: 32 * mibTest, cpuUse: 5})
	w := workloadOf("api", matured(c), "", "")
	if _, ok := find(Compute([]Workload{w}, quietMemory(32), nil), byResourceAction("Memory — no limit", "Set limit")); !ok {
		t.Fatal("no Set limit without a raise: this test guards nothing")
	}
	w.Rollout = rolloutOf(revs(1, 2048*mibTest, 2, 0))
	if red := memoryReductions(Compute([]Workload{w}, quietMemory(32), nil)); len(red) > 0 {
		t.Errorf("a limit set again after it was removed: %+v", red)
	}
}

// When the history cannot be read, nothing is refused and the reduction says
// the check was not made - never a silent pass.
func TestAnUnreadableHistoryRefusesNothingAndSaysSo(t *testing.T) {
	c := ctr("app", opts{memReq: 100 * mibTest, memLim: 3072 * mibTest, memUse: 32 * mibTest, cpuUse: 5})
	for _, r := range []*Rollout{
		{Unknown: "its revisions could not be listed: forbidden"},
		rolloutOf(revs(5, 3072*mibTest)),                  // only a revision after the first
		rolloutOf(revs(1, 2048*mibTest, 2, 4096*mibTest)), // the pods are not the newest revision
		ReadRollouts(nil, errors.New("forbidden"), nil, nil).For("Deployment", "default", "api"),
	} {
		w := workloadOf("api", matured(c), "", "")
		w.Rollout = r
		red, ok := find(Compute([]Workload{w}, quietMemory(32), nil), byResourceAction("Memory", "Reduce limit"))
		if !ok {
			t.Errorf("%+v: the reduction was refused on no evidence", r)
			continue
		}
		if !hasWarning(red, "could not be checked") {
			t.Errorf("%+v: the reduction does not say the history was not checked: %v", r, red.Warnings)
		}
	}
}

func TestTheNewestChangeOfAMemoryLimitIsRead(t *testing.T) {
	const g = 1024 * mibTest
	for _, c := range []struct {
		name      string
		revs      []revision
		raise     bool
		unchecked bool
	}{
		{"a raise", revs(1, 2*g, 2, 3*g), true, false},
		{"a raise kept across a later image-only rollout", revs(1, 2*g, 2, 3*g, 3, 3*g, 4, 3*g), true, false},
		{"a later reduction ends it", revs(1, 2*g, 2, 3*g, 3, 5*g/2), false, false},
		{"order is by revision number, not by list order", revs(2, 3*g, 1, 2*g), true, false},
		{"revision 1 alone is a whole history with no change", revs(1, 3*g), false, false},
		{"only revisions after the first, no change among them", revs(4, 3*g, 5, 3*g), false, true},
		{"a change among later revisions is still read", revs(4, 2*g, 5, 3*g), true, false},
		{"a limit set where there was none is not a raise", revs(1, 0, 2, 3*g), false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := rolloutOf(c.revs)
			lim := r.Containers["app"].Newest // the pods run the newest revision
			if _, got := r.raise("app", lim); got != c.raise {
				t.Errorf("raise = %v, want %v (%+v)", got, c.raise, r.Containers["app"])
			}
			if got := r.unknown("app", lim) != ""; got != c.unchecked {
				t.Errorf("unchecked = %q, want %v", r.unknown("app", lim), c.unchecked)
			}
		})
	}

	t.Run("a container added later has a whole history", func(t *testing.T) {
		r := rolloutOf([]revision{
			{number: 3, limits: map[string]int64{"app": 3 * g}, readable: true},
			{number: 4, limits: map[string]int64{"app": 3 * g, "sidecar": g}, readable: true},
		})
		if r.unknown("sidecar", g) != "" {
			t.Errorf("sidecar = %q, want a known history", r.unknown("sidecar", g))
		}
	})
	t.Run("an unreadable revision stops the walk", func(t *testing.T) {
		r := rolloutOf([]revision{{number: 1}, {number: 2, limits: map[string]int64{"app": 3 * g}, readable: true}})
		if !strings.Contains(r.unknown("app", 3*g), "revision 1 could not be read") {
			t.Errorf("unknown = %q", r.unknown("app", 3*g))
		}
	})
	t.Run("no revision at all is not an empty history", func(t *testing.T) {
		if rolloutOf(nil).unknown("app", 3*g) == "" {
			t.Error("no revision read as a known history")
		}
	})
}

// template is a pod template with one container "app" at a memory limit.
func template(limit string) corev1.PodTemplateSpec {
	c := corev1.Container{Name: "app"}
	if limit != "" {
		c.Resources.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(limit)}
	}
	return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{c}}}
}

func owned(kind, name string) []metav1.OwnerReference {
	yes := true
	return []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: kind, Name: name, Controller: &yes}}
}

// controllerRevision is what the StatefulSet and DaemonSet controllers write:
// the template as a replace patch under data.spec.template.
func controllerRevision(t *testing.T, kind, owner string, number int64, limit string) appsv1.ControllerRevision {
	t.Helper()
	tpl, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ptr(template(limit)))
	if err != nil {
		t.Fatal(err)
	}
	tpl["$patch"] = "replace"
	raw, err := json.Marshal(map[string]any{"spec": map[string]any{"template": tpl}})
	if err != nil {
		t.Fatal(err)
	}
	return appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{Name: owner + "-" + strconv.FormatInt(number, 10), Namespace: "default",
			OwnerReferences: owned(kind, owner)},
		Revision: number, Data: runtime.RawExtension{Raw: raw},
	}
}

func ptr[T any](v T) *T { return &v }

func TestRevisionsAreReadFromReplicaSetsAndControllerRevisions(t *testing.T) {
	rsOf := func(name string, number int, limit string) appsv1.ReplicaSet {
		return appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", OwnerReferences: owned("Deployment", "api"),
				Annotations: map[string]string{revisionAnnotation: strconv.Itoa(number)}},
			Spec: appsv1.ReplicaSetSpec{Template: template(limit)},
		}
	}
	revisions := []appsv1.ControllerRevision{
		controllerRevision(t, "StatefulSet", "db", 1, "2Gi"),
		controllerRevision(t, "StatefulSet", "db", 2, "3Gi"),
		controllerRevision(t, "DaemonSet", "agent", 7, "3Gi"),
	}
	// The same revision through the unstructured list the extra reads: the
	// converter must keep data intact.
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&revisions[1])
	if err != nil {
		t.Fatal(err)
	}
	var back appsv1.ControllerRevision
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u, &back); err != nil {
		t.Fatal(err)
	}
	revisions[1] = back

	all := ReadRollouts([]appsv1.ReplicaSet{rsOf("api-a", 1, "2Gi"), rsOf("api-b", 2, "3Gi")}, nil, revisions, nil)
	const lim = 3072 * mibTest
	for _, c := range []struct {
		kind, name string
		raise      bool
		unknown    bool
	}{
		{"Deployment", "api", true, false},
		{"StatefulSet", "db", true, false},
		{"DaemonSet", "agent", false, true}, // revision 7 alone
		{"Deployment", "other", false, true},
	} {
		r := all.For(c.kind, "default", c.name)
		if _, got := r.raise("app", lim); got != c.raise {
			t.Errorf("%s/%s raise = %v, want %v (%+v)", c.kind, c.name, got, c.raise, r)
		}
		if got := r.unknown("app", lim) != ""; got != c.unknown {
			t.Errorf("%s/%s unknown = %q, want %v", c.kind, c.name, r.unknown("app", lim), c.unknown)
		}
	}

	refused := ReadRollouts(nil, nil, nil, errors.New("controllerrevisions is forbidden"))
	if got := refused.For("StatefulSet", "default", "db").unknown("app", lim); !strings.Contains(got, "forbidden") {
		t.Errorf("a refused list reads %q, want its error", got)
	}
	if got := refused.For("CronJob", "default", "nightly").unknown("app", lim); got == "" {
		t.Error("a CronJob, which keeps no revision, reads as checked")
	}
	if refused.For("Pod", "default", "bare") != nil {
		t.Error("a bare pod has a Rollout, and set resources never rolls one out")
	}
}
