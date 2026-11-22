package kubeadjust

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/extension"
)

// At the route: the revisions are read through Host.List,
// and a list refused is a caveat on the page, never a refusal of it.

// revisionRS is a ReplicaSet of Deployment owner at a revision, its template
// holding one container "app" at a memory limit.
func revisionRS(namespace, name, owner string, revision int, memLimit string) *appsv1.ReplicaSet {
	yes := true
	return &appsv1.ReplicaSet{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "ReplicaSet"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace,
			Annotations: map[string]string{"deployment.kubernetes.io/revision": strconv.Itoa(revision)},
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: "apps/v1", Kind: "Deployment", Name: owner, UID: "d", Controller: &yes}}},
		Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{app("", "100Mi", "", memLimit)}}}},
	}
}

// quietWindow is six hours at 2m, flat, for each key: CPU at 5m, memory at mem.
func quietWindow(mem float64, keys ...string) extension.Window {
	flat := func(v float64) []extension.Sample {
		out := make([]extension.Sample, 180)
		for i := range out {
			out[i] = extension.Sample{T: float64(1_700_000_000 + 120*i), V: v}
		}
		return out
	}
	w := extension.Window{State: extension.WindowRead, Detail: "Prometheus, 6h at 2m", Complete: true,
		Containers: map[string]extension.ContainerWindow{}}
	for _, k := range keys {
		w.Containers[k] = extension.ContainerWindow{CPU: flat(5), Memory: flat(mem)}
	}
	return w
}

func memoryRows(a answer) []row {
	var out []row
	for _, c := range a.Cards {
		for _, f := range c.Findings {
			if strings.HasPrefix(f.Resource, "Memory") {
				out = append(out, f)
			}
		}
	}
	return out
}

// The issue's case after the rollout: two ReplicaSets, the newest raising the
// limit from 2Gi to 3Gi, its pods quiet at 32Mi. No reduction, and the card
// names the raise.
func TestTheRouteRemembersARaiseFromTheReplicaSets(t *testing.T) {
	h := &fakeHost{t: t}
	h.add("nodes", node("worker-1", "8", "16Gi"))
	h.add("namespaces", namespaceObj("shop"))
	h.add("replicasets", revisionRS("shop", "api-old", "api", 1, "2Gi"), revisionRS("shop", "api-new", "api", 2, "3Gi"))
	h.add("pods", runningPod("shop", "api-new-a", "api-new", "worker-1", app("10m", "100Mi", "", "3Gi")))
	h.usage = live(map[string][2]int64{"shop/api-new-a/app": {5, 32 << 20}})
	h.window = quietWindow(32<<20, "shop/api-new-a/app")

	a := page(t, h, url.Values{"ns": {"shop"}})
	for _, f := range memoryRows(a) {
		if f.Action == "Reduce limit" || f.Action == "Set limit" || f.Action == "Reduce request + limit" {
			t.Errorf("a reduction of the raised limit: %+v", f)
		}
		if !strings.Contains(strings.Join(f.Warnings, " | "), "raised from 2.00 GiB to 3.00 GiB") {
			t.Errorf("memory row %q does not say why: %v", f.Action, f.Warnings)
		}
	}
	if len(a.Cards) != 1 || len(a.Cards[0].Containers) != 1 {
		t.Fatalf("cards = %+v", a.Cards)
	}
	got := a.Cards[0].Containers[0].MemoryRaised
	if got == nil || got.From != "2.00 GiB" || got.To != "3.00 GiB" || got.Revision != 2 {
		t.Errorf("memoryRaised = %+v, want 2.00 GiB → 3.00 GiB at revision 2", got)
	}

	// The same cluster with only the new ReplicaSet kept: the reduction comes
	// back, so the test above measures the revisions and nothing else - and it
	// says it could not check.
	h.objects["replicasets"] = h.objects["replicasets"][1:]
	a = page(t, h, url.Values{"ns": {"shop"}})
	red := findRow(memoryRows(a), "Reduce limit")
	if red == nil {
		t.Fatalf("with revision 2 alone kept, no reduction: %+v", memoryRows(a))
	}
	if !strings.Contains(strings.Join(red.Warnings, " | "), "could not be checked") {
		t.Errorf("the reduction does not say the history was unchecked: %v", red.Warnings)
	}
	if a.Cards[0].Containers[0].MemoryRaised != nil {
		t.Errorf("a raise drawn with no revision holding it")
	}
}

// A StatefulSet whose controllerrevisions the person may not list: the page
// answers, the reduction stands, and it says why it could not be checked.
func TestARefusedRevisionListIsACaveatNotARefusal(t *testing.T) {
	h := &fakeHost{t: t}
	h.add("nodes", node("worker-1", "8", "16Gi"))
	h.add("namespaces", namespaceObj("data"))
	pod := runningPod("data", "db-0", "", "worker-1", app("10m", "100Mi", "", "3Gi"))
	yes := true
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "db", UID: "s", Controller: &yes}}
	h.add("pods", pod)
	h.usage = live(map[string][2]int64{"data/db-0/app": {5, 32 << 20}})
	h.window = quietWindow(32<<20, "data/db-0/app")
	h.listErr = map[string]error{"controllerrevisions": errors.New("controllerrevisions.apps is forbidden")}

	red := findRow(memoryRows(page(t, h, url.Values{"ns": {"data"}})), "Reduce limit")
	if red == nil {
		t.Fatal("no reduction: a refused list refused the advice")
	}
	joined := strings.Join(red.Warnings, " | ")
	if !strings.Contains(joined, "could not be checked") || !strings.Contains(joined, "forbidden") {
		t.Errorf("warnings = %v, want the unchecked caveat naming the refusal", red.Warnings)
	}
}

func findRow(rows []row, action string) *row {
	for i := range rows {
		if rows[i].Action == action {
			return &rows[i]
		}
	}
	return nil
}
