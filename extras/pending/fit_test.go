package pending

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestTolerationsFollowTheSchedulersRule(t *testing.T) {
	taint := corev1.Taint{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule}
	for _, tc := range []struct {
		name string
		tol  corev1.Toleration
		want bool
	}{
		{"equal, same value", corev1.Toleration{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "gpu"}, true},
		{"empty operator is Equal", corev1.Toleration{Key: "dedicated", Value: "gpu"}, true},
		{"equal, other value", corev1.Toleration{Key: "dedicated", Value: "db"}, false},
		{"exists on the key", corev1.Toleration{Key: "dedicated", Operator: corev1.TolerationOpExists}, true},
		{"empty key with exists tolerates all", corev1.Toleration{Operator: corev1.TolerationOpExists}, true},
		{"other effect", corev1.Toleration{Key: "dedicated", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute}, false},
		{"same effect", corev1.Toleration{Key: "dedicated", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}, true},
		{"other key", corev1.Toleration{Key: "zone", Operator: corev1.TolerationOpExists}, false},
	} {
		if got := tolerates([]corev1.Toleration{tc.tol}, taint); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	// PreferNoSchedule never refuses a node, and the cordon's taint is the
	// cordon column's.
	got := untolerated(nil, []corev1.Taint{
		{Key: "soft", Effect: corev1.TaintEffectPreferNoSchedule},
		{Key: unschedulableKey, Effect: corev1.TaintEffectNoSchedule},
		taint,
	})
	if len(got) != 1 || got[0] != "dedicated=gpu:NoSchedule" {
		t.Errorf("untolerated = %v", got)
	}
}

func TestNodeAffinityOperators(t *testing.T) {
	n := &Node{Name: "n1", Labels: map[string]string{"zone": "a", "gen": "5"}}
	expr := func(key string, op corev1.NodeSelectorOperator, values ...string) *corev1.NodeSelector {
		return &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: key, Operator: op, Values: values}}}}}
	}
	for _, tc := range []struct {
		sel  *corev1.NodeSelector
		want bool
	}{
		{expr("zone", corev1.NodeSelectorOpIn, "a", "b"), true},
		{expr("zone", corev1.NodeSelectorOpIn, "b"), false},
		{expr("zone", corev1.NodeSelectorOpNotIn, "b"), true},
		{expr("missing", corev1.NodeSelectorOpNotIn, "b"), true},
		{expr("zone", corev1.NodeSelectorOpExists), true},
		{expr("zone", corev1.NodeSelectorOpDoesNotExist), false},
		{expr("gen", corev1.NodeSelectorOpGt, "4"), true},
		{expr("gen", corev1.NodeSelectorOpLt, "4"), false},
		{expr("gen", corev1.NodeSelectorOpGt, "x"), false},
		{&corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"n1"}}}}}}, true},
		// Terms are ORed.
		{&corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"z"}}}},
			{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "gen", Operator: corev1.NodeSelectorOpIn, Values: []string{"5"}}}},
		}}, true},
		// No term matches nothing.
		{&corev1.NodeSelector{}, false},
	} {
		if got := nodeSelectorMatches(tc.sel, n); got != tc.want {
			t.Errorf("%+v: %v, want %v", tc.sel, got, tc.want)
		}
	}
	if ok, why := matchesNode(corev1.PodSpec{NodeSelector: map[string]string{"disktype": "ssd"}}, n); ok || why != "no label disktype=ssd" {
		t.Errorf("nodeSelector: %v %q", ok, why)
	}
}

func TestThePodRequestIsTheSchedulers(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	spec := corev1.PodSpec{
		InitContainers: []corev1.Container{
			container("2", "1Gi"), // a plain init: alone, with no sidecar yet
			func() corev1.Container { c := container("100m", "64Mi"); c.RestartPolicy = &always; return c }(), // a sidecar
			container("500m", "3Gi"), // an init after the sidecar: with it
		},
		Containers: []corev1.Container{container("300m", "256Mi"), container("200m", "256Mi")},
		Overhead:   quantities("cpu", "50m", "memory", "10Mi"),
	}
	got := PodRequest(spec)
	// cpu: containers 500m + sidecar 100m = 600m; inits peak 2000m: 2000m + 50m.
	if got["cpu"] != 2050 {
		t.Errorf("cpu = %dm, want 2050m", got["cpu"])
	}
	// memory: containers 512Mi + sidecar 64Mi = 576Mi; init after sidecar 3Gi+64Mi peaks.
	want := int64(3*1024+64+10) << 20
	if got["memory"] != want {
		t.Errorf("memory = %d, want %d", got["memory"], want)
	}
	// A limit stands in for an unset request.
	c := corev1.Container{Resources: corev1.ResourceRequirements{Limits: quantities("nvidia.com/gpu", "1")}}
	if got := PodRequest(corev1.PodSpec{Containers: []corev1.Container{c}}); got["nvidia.com/gpu"] != 1 {
		t.Errorf("gpu = %d", got["nvidia.com/gpu"])
	}
}

func TestTheFitOfOnePodOnOneNode(t *testing.T) {
	n := nodeOf(node("big", "4", "8Gi", map[string]string{"zone": "a"}, corev1.Taint{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule}))
	n.Requested = Amounts{"cpu": 3500, "memory": 2 << 30}
	n.Pods = 10
	spec := corev1.PodSpec{Containers: []corev1.Container{container("1", "1Gi")}}
	w := &Want{Spec: spec, Request: PodRequest(spec)}
	cols := w.Columns()
	if want := []string{PredCordon, PredTaints, PredAffinity, "resource:cpu", "resource:memory", PredPods}; !equal(cols, want) {
		t.Fatalf("columns = %v", cols)
	}
	f := w.FitOn(n, cols)
	if f.Failing != 2 {
		t.Errorf("failing = %d, want 2 (taint, cpu): %+v", f.Failing, f.Cells)
	}
	if d := f.Cells[3].Detail; d != "needs 1, 500m free of 4" {
		t.Errorf("cpu detail = %q", d)
	}
	if f.Deficit < 0.124 || f.Deficit > 0.126 {
		t.Errorf("deficit = %v, want 0.125", f.Deficit)
	}
	// Pod affinity is never computed: its cell is no pass and no fail.
	w.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{}}
	cols = w.Columns()
	f = w.FitOn(n, cols)
	last := f.Cells[len(f.Cells)-1]
	if last.Key != PredPodAffinity || last.OK != nil {
		t.Errorf("pod affinity cell = %+v", last)
	}
}

func TestAmountsAreWrittenForAPerson(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    int64
		want string
	}{
		{"cpu", 500, "500m"}, {"cpu", 2000, "2"}, {"cpu", 1500, "1.5"},
		{"memory", 64 << 30, "64Gi"}, {"memory", 1536 << 20, "1.5Gi"}, {"memory", 512, "512"},
		{"nvidia.com/gpu", 2, "2"},
	} {
		if got := Format(tc.name, tc.v); got != tc.want {
			t.Errorf("Format(%s, %d) = %q, want %q", tc.name, tc.v, got, tc.want)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
