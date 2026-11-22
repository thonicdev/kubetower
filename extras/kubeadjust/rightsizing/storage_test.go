package rightsizing

import (
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The disk half of this package, and the thing worth saying first: **every test
// below is about telling an unmeasured disk from an empty one.** The engine
// skips a volume whose usage is zero, so a cluster that refuses `nodes/proxy`
// produces exactly the same suggestions as a cluster where every disk is fine.
// Only the Availability distinguishes them, and only these tests keep it honest.
//
// The tests of the read itself - the kubelet asked through the node proxy, a
// refused `nodes/proxy` said rather than drawn as clean disks, a partial answer
// saying how partial - are the extra's, through `Host.Usage`.

// gibs is a size in GiB. Named apart from the package's own `gib` constant,
// which is one GiB rather than a function of n.
func gibs(n int64) int64 { return n * 1024 * 1024 * 1024 }

// --- the guard that stops a shared export being reported as a claim ----------

// A PVC on NFS or CephFS makes the kubelet measure the whole share, and the
// number it returns is not about this claim at all. It is also stable, which is
// what makes it dangerous: a 5 GiB claim on a 40 TiB pool reports a comfortable
// 30 % for ever and nobody discovers it was somebody else's disk.
func TestAKubeletMeasuringASharedExportIsNotBelieved(t *testing.T) {
	for _, tc := range []struct {
		name              string
		declared, fsSize  int64
		want              bool
		whatItWouldReport string
	}{
		{"a block device of exactly the claim's size", gibs(5), gibs(5), true, ""},
		{"a filesystem, which costs a little of the device", gibs(5), gibs(5) - gibs(5)/40, true, ""},
		{"an NFS share far larger than the claim", gibs(5), gibs(40000), false,
			"the pool's usage drawn under this pod's name"},
		{"a share slightly too large to be this claim", gibs(5), gibs(6), false,
			"a bar whose denominator is not the claim an operator would grow"},
		{"the claim's size unknown", 0, gibs(5), false, "usage over a capacity of zero"},
		{"the kubelet reported nothing", gibs(5), 0, false, "a claim drawn as empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := trustworthy(tc.declared, tc.fsSize); got != tc.want {
				t.Errorf("trustworthy(%d, %d) = %v, want %v — %s",
					tc.declared, tc.fsSize, got, tc.want, tc.whatItWouldReport)
			}
		})
	}
}

// And the consequence on the page: a claim the kubelet could not honestly
// measure keeps its capacity and loses its usage, rather than borrowing the
// share's.
func TestAnUntrustedClaimKeepsItsCapacityAndLosesItsUsage(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "db-0", Namespace: "data"},
		Spec: corev1.PodSpec{Volumes: []corev1.Volume{{
			Name: "data",
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.
				PersistentVolumeClaimVolumeSource{ClaimName: "data-db-0"}},
		}}},
	}
	st := PodStorage{Volumes: map[string]VolumeStat{
		"data": {UsedBytes: gibs(12000), CapacityBytes: gibs(40000)},
	}}
	claims := map[string]int64{"data/data-db-0": gibs(5)}

	got := volumesOf(pod, st, claims)
	if len(got) != 1 {
		t.Fatalf("got %d volumes, want 1", len(got))
	}
	if got[0].Capacity != gibs(5) {
		t.Errorf("capacity = %d, want the claim's own %d", got[0].Capacity, gibs(5))
	}
	if got[0].Usage != 0 {
		t.Errorf("usage = %d: the kubelet was measuring a 40 TiB export, and reporting "+
			"12 TiB used against a 5 GiB claim draws a bar about somebody else's disk", got[0].Usage)
	}
}

// --- what is judged, and what is deliberately not ----------------------------

// A configMap, a Secret and a projected token are not sized by whoever wrote the
// workload. A bar for a 4 KiB configMap mount teaches an operator to stop
// reading the section, which costs them the PVC three rows down.
func TestOnlyClaimsAndEmptyDirsAreJudged(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "web"},
		Spec: corev1.PodSpec{Volumes: []corev1.Volume{
			{Name: "config", VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{}}},
			{Name: "creds", VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{}}},
			{Name: "token", VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{}}},
			{Name: "host", VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: "/var/run"}}},
			{Name: "cache", VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			{Name: "data", VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "c"}}},
		}},
	}
	st := PodStorage{Volumes: map[string]VolumeStat{
		"cache": {UsedBytes: gibs(1), CapacityBytes: gibs(50)},
		"data":  {UsedBytes: gibs(4), CapacityBytes: gibs(5)},
	}}

	var names []string
	for _, v := range volumesOf(pod, st, map[string]int64{"web/c": gibs(5)}) {
		names = append(names, v.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "cache,data" {
		t.Errorf("judged %v, want only the emptyDir and the claim", names)
	}
}

// An emptyDir is the node's own filesystem, so there is no second disk for the
// kubelet to be measuring by mistake and its figures are taken as they come.
// Its ceiling, though, is its sizeLimit and not what the node happens to have —
// a 50 GiB node disk is not a promise this pod may use 50 GiB.
func TestAnEmptyDirIsScaledToItsSizeLimitAndNotToTheNode(t *testing.T) {
	limit := resource.MustParse("2Gi")
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-0", Namespace: "jobs"},
		Spec: corev1.PodSpec{Volumes: []corev1.Volume{{
			Name:         "scratch",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &limit}},
		}}},
	}
	st := PodStorage{Volumes: map[string]VolumeStat{
		"scratch": {UsedBytes: gibs(1), CapacityBytes: gibs(500)},
	}}

	got := volumesOf(pod, st, nil)[0]
	if !got.HasSizeLimit {
		t.Error("the sizeLimit was not seen, so this emptyDir will be reported as unbounded")
	}
	if got.Capacity != limit.Value() {
		t.Errorf("capacity = %d, want the sizeLimit %d: scaling to the node's 500 GiB draws "+
			"a pod at 0.2%% of a ceiling it is not allowed to reach", got.Capacity, limit.Value())
	}
}

// --- an unmeasured pod is not an empty one -----------------------------------

// A pod on a node whose kubelet did not answer keeps zeroes, and zero is what
// the engine reads as silence. The alternative — inserting an empty entry so
// every pod has one — would draw a Volumes section saying this pod has no
// volumes, on a pod with three.
func TestAPodOnANodeThatDidNotAnswerGetsNoDiskFiguresAtAll(t *testing.T) {
	pods := []corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "web"},
		Spec: corev1.PodSpec{
			NodeName: "node-b",
			Volumes: []corev1.Volume{{Name: "cache", VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
		},
	}}
	workloads := []Workload{{
		Kind: "Deployment", Name: "api", Namespace: "web",
		Pods: []Pod{{Name: "api-1", Containers: []Container{{Name: "app"}}}},
	}}

	// node-a answered; the pod is on node-b.
	storage := map[string]PodStorage{
		"web/other-1": {Ephemeral: map[string]int64{"app": gibs(1)}},
	}
	attachStorage(workloads, pods, storage, nil)

	if got := workloads[0].Pods[0].Containers[0].EphemeralUsage; got != 0 {
		t.Errorf("ephemeral usage = %d, want 0 for a pod nothing measured", got)
	}
	if got := workloads[0].Pods[0].Volumes; got != nil {
		t.Errorf("volumes = %v, want none. An empty Volumes list on a pod with a "+
			"volume reads as a pod without one, which is the opposite of the truth", got)
	}
}

// The same distinction one level up, on the bar the page draws. A running
// container always has some log and some writable layer, so a kubelet that saw
// one never reports zero for it — zero therefore means nobody looked.
func TestAnEphemeralBarIsUnmeasuredRatherThanEmpty(t *testing.T) {
	pods := []Pod{{Name: "p-1", Containers: []Container{{
		Name: "app", EphemeralLimit: gibs(1),
	}}}}
	agg := aggregateContainers(pods)[0]

	got := ephemeralReading(agg, pods)
	if got.Measured {
		t.Error("an ephemeral bar with no kubelet behind it says it was measured, so a " +
			"cluster that refuses nodes/proxy draws every container as using no disk")
	}
	if got.Status != StatusNone {
		t.Errorf("status = %q, want %q — any other color is a claim about a disk "+
			"nobody read", got.Status, StatusNone)
	}
}

// And the reverse, because it is the case the first one is most likely to eat:
// a container the kubelet *did* report on is measured, and its bar is scaled to
// the limit in its template.
func TestAMeasuredEphemeralBarIsJudgedAgainstItsLimit(t *testing.T) {
	pods := []Pod{{Name: "p-1", Containers: []Container{{
		Name: "app", EphemeralLimit: gibs(1), EphemeralUsage: gibs(1) * 95 / 100,
	}}}}
	agg := aggregateContainers(pods)[0]

	got := ephemeralReading(agg, pods)
	if !got.Measured {
		t.Fatal("a container the kubelet reported on is drawn as unmeasured")
	}
	if got.Status != StatusDanger {
		t.Errorf("status = %q at 95%% of the limit, want %q", got.Status, StatusDanger)
	}
}

// The worst replica and not the mean. Disk is a level, not a rate: one replica
// that has filled its writable layer is one about to be evicted, and averaging
// it against four healthy peers reports the workload at 30%.
func TestEphemeralTakesTheWorstReplicaAndNotTheAverage(t *testing.T) {
	pods := []Pod{
		{Name: "p-1", Containers: []Container{{Name: "app", EphemeralLimit: gibs(4), EphemeralUsage: gibs(1)}}},
		{Name: "p-2", Containers: []Container{{Name: "app", EphemeralLimit: gibs(4), EphemeralUsage: gibs(1)}}},
		{Name: "p-3", Containers: []Container{{Name: "app", EphemeralLimit: gibs(4), EphemeralUsage: gibs(1)}}},
		{Name: "p-4", Containers: []Container{{Name: "app", EphemeralLimit: gibs(4), EphemeralUsage: gibs(4)}}},
	}
	agg := aggregateContainers(pods)[0]

	got := ephemeralReading(agg, pods)
	if got.Peak != gibs(4) {
		t.Errorf("peak = %d, want the worst replica's %d", got.Peak, gibs(4))
	}
	if got.Status != StatusDanger {
		t.Errorf("status = %q, want %q: the mean here is 1.75 GiB of 4 GiB, which is a "+
			"comfortable green over a replica whose disk is full", got.Status, StatusDanger)
	}
}

// A volume with no ceiling is a warning once it holds a gigabyte. An emptyDir
// with no sizeLimit can fill the node's disk and evict every pod on it - but it
// was a warning at any size, which colored every scratch directory holding a
// lock file, so the bar now draws the same line as the finding.
func TestAnUnboundedEmptyDirIsAWarningOnceItHoldsAGigabyte(t *testing.T) {
	for _, tc := range []struct {
		usage int64
		want  Status
	}{{4096, StatusHealthy}, {gibs(2), StatusWarning}} {
		pods := []Pod{{Name: "p-1", Volumes: []Volume{{
			Name: "scratch", Type: "emptyDir", Usage: tc.usage, Capacity: gibs(500),
		}}}}
		got := volumeReadings(pods)
		if len(got) != 1 {
			t.Fatalf("got %d readings, want 1", len(got))
		}
		if got[0].Status != tc.want {
			t.Errorf("usage %d: status = %q, want %q", tc.usage, got[0].Status, tc.want)
		}
	}
}

// --- what is asked, and of whom ---------------------------------------------

// Only the nodes actually hosting these pods. On a fourteen-node cluster
// narrowed to one namespace that is two or three, and the other eleven would be
// eleven proxied requests for pods nobody is looking at.
func TestOnlyTheNodesHostingThesePodsAreAsked(t *testing.T) {
	pods := []corev1.Pod{
		{Spec: corev1.PodSpec{NodeName: "node-b"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		{Spec: corev1.PodSpec{NodeName: "node-a"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		{Spec: corev1.PodSpec{NodeName: "node-b"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		// Not running: nothing of its disk is worth reading, and it may not be
		// placed anywhere yet.
		{Spec: corev1.PodSpec{NodeName: "node-c"}, Status: corev1.PodStatus{Phase: corev1.PodPending}},
		// Unscheduled.
		{Status: corev1.PodStatus{Phase: corev1.PodRunning}},
	}
	got := nodesHosting(pods)
	if strings.Join(got, ",") != "node-a,node-b" {
		t.Errorf("asked %v, want [node-a node-b] — sorted, so two reads of one cluster "+
			"ask in the same order", got)
	}
}
