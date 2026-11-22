package resources

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	nodev1 "k8s.io/api/node/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// A fixed clock, so an age column is testable at all.
var clock = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

func client(objects ...runtime.Object) *cluster.Client {
	return &cluster.Client{Context: "test", Clients: fake.NewClientset(objects...)}
}

// listing is List for the tests that care about neither of the last two
// arguments: no order of their own, and no metrics backend. The ones that do
// care call List directly, so that what they are exercising is visible at the
// call site.
func listing(c *cluster.Client, kind, namespace, query string) (Table, error) {
	return List(context.Background(), accesstest.Allow(), c, Query{Kind: kind, Namespace: namespace, Filter: query}, nil)
}

func listingSorted(c *cluster.Client, kind, sortBy string, descending bool) (Table, error) {
	return List(context.Background(), accesstest.Allow(), c, Query{Kind: kind, Sort: sortBy, Descending: descending}, nil)
}

// columnOf finds a column by its title, so a test says which column it means
// rather than counting to it — a count that goes stale the moment a column is
// inserted, and goes stale quietly.
func columnOf(t *testing.T, table Table, title string) int {
	t.Helper()
	for i, c := range table.Columns {
		if c.Title == title {
			return i
		}
	}
	t.Fatalf("the %s table has no %q column", table.Kind, title)
	return -1
}

func cellOf(t *testing.T, table Table, name, title string) Cell {
	t.Helper()
	at := columnOf(t, table, title)
	for _, row := range table.Rows {
		if row.Name == name {
			return row.Cells[at]
		}
	}
	t.Fatalf("no row named %q in the %s table", name, table.Kind)
	return Cell{}
}

// --- the rule this package exists for -------------------------------------------

func TestPodShowsWaitingReasonRatherThanPhase(t *testing.T) {
	tests := []struct {
		name   string
		pod    *corev1.Pod
		status string
		class  string
		rank   int
	}{
		{
			name:   "a healthy pod keeps its phase",
			pod:    pod("web", corev1.PodRunning, container("app", waiting(""), true)),
			status: "Running",
			class:  ClassOK,
			rank:   RankOK,
		},
		{
			// The whole point: the phase of a crash-looping pod is Running.
			name:   "a crash-looping pod is not reported as Running",
			pod:    pod("api", corev1.PodRunning, container("app", waiting("CrashLoopBackOff"), false)),
			status: "CrashLoopBackOff",
			class:  ClassBad,
			rank:   RankBad,
		},
		{
			name:   "an unpullable image is named, not hidden behind Pending",
			pod:    pod("job", corev1.PodPending, container("app", waiting("ImagePullBackOff"), false)),
			status: "ImagePullBackOff",
			class:  ClassBad,
			rank:   RankBad,
		},
		{
			name:   "a container still being created says so",
			pod:    pod("new", corev1.PodPending, container("app", waiting("ContainerCreating"), false)),
			status: "ContainerCreating",
			class:  ClassWarn,
			rank:   RankWarn,
		},
		{
			name:   "a completed pod is healthy, not unready",
			pod:    pod("batch", corev1.PodSucceeded, container("app", terminated("Completed", 0), false)),
			status: "Succeeded",
			class:  ClassOK,
			rank:   RankOK,
		},
		{
			name:   "an OOMKilled container outranks the phase it left behind",
			pod:    pod("hungry", corev1.PodRunning, container("app", terminated("OOMKilled", 137), false)),
			status: "OOMKilled",
			class:  ClassBad,
			rank:   RankBad,
		},
		{
			name:   "a failing init container is named with its prefix",
			pod:    initPod("stuck", corev1.PodPending, container("init", waiting("ImagePullBackOff"), false)),
			status: "Init:ImagePullBackOff",
			class:  ClassBad,
			rank:   RankBad,
		},
		{
			name:   "a pod still running its init containers says how far it got",
			pod:    initPod("starting", corev1.PodPending, container("init", waiting("PodInitializing"), false)),
			status: "Init:0/1",
			class:  ClassWarn,
			rank:   RankWarn,
		},
		{
			name:   "an init container that finished does not hold the status",
			pod:    initPod("started", corev1.PodPending, container("init", terminated("Completed", 0), false)),
			status: "Pending",
			class:  ClassWarn,
			rank:   RankWarn,
		},
		{
			name:   "an eviction is named rather than reported as Failed",
			pod:    evicted("evicted"),
			status: "Evicted",
			class:  ClassBad,
			rank:   RankBad,
		},
		{
			// A native sidecar never terminates — that is what
			// restartPolicy: Always on an init container *means* — so waiting
			// for it would leave an Istio-injected pod at Init:0/1 for the
			// whole time it runs, painted amber on the one column that says what is
			// wrong. There are two of them in a mesh cluster per application
			// pod.
			name: "a started native sidecar does not hold the pod at Init forever",
			pod: sidecarPod("meshed", corev1.PodRunning,
				sidecar("istio-proxy", true, true),
				container("app", waiting(""), true)),
			status: "Running",
			class:  ClassOK,
			rank:   RankOK,
		},
		{
			// The other side of it: before the kubelet reports `started`, the
			// pod genuinely is still initializing and saying so is right.
			name: "a sidecar that has not started yet is still holding the pod up",
			pod: sidecarPod("starting", corev1.PodPending,
				sidecar("istio-proxy", false, false),
				container("app", waiting("PodInitializing"), false)),
			status: "Init:0/1",
			class:  ClassWarn,
			rank:   RankWarn,
		},
		{
			// A sidecar that cannot pull is not "the pod is starting": nothing
			// in this pod will ever run, and the fault has a name.
			name: "a sidecar that cannot pull its image is named like any other init container",
			pod: sidecarPod("unpullable", corev1.PodPending,
				container("istio-proxy", waiting("ImagePullBackOff"), false),
				container("app", waiting("PodInitializing"), false)),
			status: "Init:ImagePullBackOff",
			class:  ClassBad,
			rank:   RankBad,
		},
		{
			// And a started sidecar must not be counted as the whole init
			// sequence: the migration behind it has not run yet.
			name:   "a started sidecar in front of an unfinished init container still counts",
			pod:    meshedWithInit("migrating"),
			status: "Init:1/2",
			class:  ClassWarn,
			rank:   RankWarn,
		},
		{
			// kubectl names the exit code when the kubelet reported no reason;
			// without that it falls through to the counter and reports a
			// *failed* init container as one that has simply not finished yet.
			name:   "an init container that died with no reason names its exit code",
			pod:    initPod("broken", corev1.PodPending, container("init", terminated("", 1), false)),
			status: "Init:ExitCode:1",
			class:  ClassBad,
			rank:   RankBad,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := PodStatus(tc.pod); got != tc.status {
				t.Fatalf("status = %q, want %q", got, tc.status)
			}
			// The status cell is the fourth column; see the pods descriptor.
			cell := podRow(tc.pod, clock, nil)[3]
			if cell.Class != tc.class || cell.Rank != tc.rank {
				t.Fatalf("cell = {%q %d}, want {%q %d}", cell.Class, cell.Rank, tc.class, tc.rank)
			}
		})
	}
}

func TestTerminatingPodOutranksItsPhase(t *testing.T) {
	p := pod("going", corev1.PodRunning, container("app", waiting(""), true))
	deleted := metav1.NewTime(clock)
	p.DeletionTimestamp = &deleted

	if got := PodStatus(p); got != "Terminating" {
		t.Fatalf("status = %q, want Terminating", got)
	}
}

// --- the three columns Lens has and a kubectl does not ---------------------------

func TestContainersColumnSeparatesFailedFromNotStarted(t *testing.T) {
	// `1/3` is the same Ready cell for a sidecar still pulling its image and for
	// a sidecar in CrashLoopBackOff. This column is the one that says which.
	tests := []struct {
		name  string
		pod   *corev1.Pod
		text  string
		class string
	}{
		{
			name:  "all ready",
			pod:   podOf("web", corev1.PodRunning, container("app", waiting(""), true), container("side", waiting(""), true)),
			text:  "●● 2/2",
			class: ClassOK,
		},
		{
			name: "one crash-looping, one still creating",
			pod: podOf("api", corev1.PodRunning,
				container("app", waiting(""), true),
				container("proxy", waiting("ContainerCreating"), false),
				container("side", waiting("CrashLoopBackOff"), false)),
			text:  "●○✕ 1/3",
			class: ClassBad,
		},
		{
			name: "nothing failed, something starting",
			pod: podOf("new", corev1.PodPending,
				container("app", waiting("ContainerCreating"), false),
				container("side", waiting("ContainerCreating"), false)),
			text:  "○○ 0/2",
			class: ClassWarn,
		},
		{
			// A finished Job pod has no ready container at all and is not unwell.
			name:  "a completed container is not a failed one",
			pod:   podOf("backup", corev1.PodSucceeded, container("app", terminated("Completed", 0), false)),
			text:  "✓ 0/1",
			class: ClassOK,
		},
		{
			name:  "an OOMKilled container is a cross",
			pod:   podOf("hungry", corev1.PodRunning, container("app", terminated("OOMKilled", 137), false)),
			text:  "✕ 0/1",
			class: ClassBad,
		},
		{
			// Running, and the readiness probe has not answered yet. Not a fault.
			name:  "running and not ready yet",
			pod:   podOf("slow", corev1.PodRunning, container("app", waiting(""), false)),
			text:  "○ 0/1",
			class: ClassWarn,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			table, err := listing(client(tc.pod), "pods", "", "")
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			cell := cellOf(t, table, tc.pod.Name, "Containers")
			if cell.Text != tc.text {
				t.Fatalf("Containers = %q, want %q", cell.Text, tc.text)
			}
			if cell.Class != tc.class {
				t.Fatalf("Containers class = %q, want %q", cell.Class, tc.class)
			}
			// The whole point of the glyph run: it must not be the only thing
			// distinguishing the states, because a reader who cannot tell red
			// from green sees one gray pill either way.
			if !strings.Contains(cell.Text, "/") {
				t.Fatalf("Containers = %q, which carries no count — color would be the only signal", cell.Text)
			}
		})
	}
}

// Native sidecars in the three columns that count containers, driven through the listing
// the browser actually asks for rather than through podRow.
//
// A native sidecar runs for the whole life of the pod, so it is one of the
// containers: leaving it out reports an Istio-injected pod as `1/1` where a
// kubectl beside it says `2/2`, draws one square for a pod running two
// processes, and — the one that costs an incident — prints `0` restarts for a
// mesh proxy the kernel has killed eleven times, because a sidecar's restarts
// are filed under InitContainerStatuses.
func TestAMeshedPodCountsItsSidecarInEveryColumnThatCountsContainers(t *testing.T) {
	meshed := sidecarPod("meshed", corev1.PodRunning,
		sidecar("istio-proxy", true, true),
		container("app", waiting(""), true))
	meshed.Status.InitContainerStatuses[0].RestartCount = 11

	table, err := listing(client(meshed), "pods", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if got := cellOf(t, table, "meshed", "Ready").Text; got != "2/2" {
		t.Errorf("Ready = %q, want 2/2 — the sidecar is a container this pod runs, and kubectl counts it", got)
	}
	if got := cellOf(t, table, "meshed", "Restarts").Text; got != "11" {
		t.Errorf("Restarts = %q, want 11 — a sidecar's restarts are in InitContainerStatuses, "+
			"and a mesh proxy being OOMKilled hourly showed as %q", got, "0")
	}
	if got := cellOf(t, table, "meshed", "Containers").Text; got != "●● 2/2" {
		t.Errorf("Containers = %q, want %q — one square per container the pod is running", got, "●● 2/2")
	}

	// An ordinary init container is not one of them: it finished in the first
	// second and a permanent tick for it says nothing that changes.
	withInit := initPod("migrated", corev1.PodRunning, container("migrate", terminated("Completed", 0), false))
	withInit.Status.ContainerStatuses = []corev1.ContainerStatus{container("app", waiting(""), true)}
	table, err = listing(client(withInit), "pods", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := cellOf(t, table, "migrated", "Ready").Text; got != "1/1" {
		t.Errorf("Ready = %q, want 1/1 — an ordinary init container is not one of the pod's containers", got)
	}
}

func TestTheContainerGlyphsAreFourDistinctShapes(t *testing.T) {
	// If two states share a glyph the column says nothing that Ready did not,
	// and the color becomes the only difference again.
	seen := map[string]string{}
	for name, glyph := range map[string]string{
		"ready": glyphReady, "done": glyphDone, "failed": glyphFailed, "starting": glyphStarting,
	} {
		if other, clash := seen[glyph]; clash {
			t.Fatalf("%s and %s are both %q", name, other, glyph)
		}
		seen[glyph] = name
	}
}

func TestContainersRankNothing(t *testing.T) {
	// The Status cell decides where a row sorts. A pod ranked once for its
	// status and again for its containers would outrank a worse one.
	p := podOf("api", corev1.PodRunning, container("app", waiting("CrashLoopBackOff"), false))
	table, err := listing(client(p), "pods", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := cellOf(t, table, "api", "Containers").Rank; got != 0 {
		t.Fatalf("Containers rank = %d, want 0 — only Status ranks a pod", got)
	}
}

func TestALongContainerRunIsCappedButStillCounted(t *testing.T) {
	statuses := make([]corev1.ContainerStatus, 0, 11)
	for i := range 11 {
		statuses = append(statuses, container(fmt.Sprintf("c%d", i), waiting(""), true))
	}
	table, err := listing(client(podOf("wide", corev1.PodRunning, statuses...)), "pods", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := strings.Repeat(glyphReady, containersCap) + "+3 11/11"
	if got := cellOf(t, table, "wide", "Containers").Text; got != want {
		t.Fatalf("Containers = %q, want %q", got, want)
	}
}

func TestControlledByNamesTheControllerAndNotTheFirstOwner(t *testing.T) {
	// Several owners is ordinary — a chart marks its own, a cross-namespace
	// owner exists — and exactly one of them is what would recreate this pod.
	no, yes := false, true
	p := pod("web-7d9f-abcde", corev1.PodRunning, container("app", waiting(""), true))
	p.OwnerReferences = []metav1.OwnerReference{
		{Kind: "HelmRelease", Name: "shop", Controller: &no},
		{Kind: "ReplicaSet", Name: "web-7d9f", Controller: &yes},
	}

	table, err := listing(client(p), "pods", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := cellOf(t, table, p.Name, "Controlled by").Text; got != "ReplicaSet/web-7d9f" {
		t.Fatalf("Controlled by = %q, want ReplicaSet/web-7d9f", got)
	}
}

func TestAnUncontrolledPodSaysSoRatherThanGuessing(t *testing.T) {
	table, err := listing(client(pod("standalone", corev1.PodRunning, container("app", waiting(""), true))), "pods", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := cellOf(t, table, "standalone", "Controlled by").Text; got != Missing {
		t.Fatalf("Controlled by = %q, want %q — nothing will ever recreate this pod", got, Missing)
	}
}

func TestAnUncontrolledRowSortsLastInBothDirections(t *testing.T) {
	// The existing rule, applied to a column that is genuinely empty on real
	// clusters: an em dash is the absence of an owner, not an owner named "".
	yes := true
	owned := func(name, owner string) *corev1.Pod {
		p := pod(name, corev1.PodRunning, container("app", waiting(""), true))
		p.OwnerReferences = []metav1.OwnerReference{{Kind: "ReplicaSet", Name: owner, Controller: &yes}}
		return p
	}
	c := client(
		owned("a-pod", "alpha"),
		pod("b-pod", corev1.PodRunning, container("app", waiting(""), true)),
		owned("c-pod", "zulu"),
	)

	for _, descending := range []bool{false, true} {
		table, err := listingSorted(c, "pods", "Controlled by", descending)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		got := names(table)
		if got[len(got)-1] != "b-pod" {
			t.Fatalf("descending=%v: order = %v, want the unowned pod last", descending, got)
		}
	}
}

func TestQoSIsReportedAndSortsWorstFirst(t *testing.T) {
	withQoS := func(name string, class corev1.PodQOSClass) *corev1.Pod {
		p := pod(name, corev1.PodRunning, container("app", waiting(""), true))
		p.Status.QOSClass = class
		return p
	}
	c := client(
		withQoS("guaranteed", corev1.PodQOSGuaranteed),
		withQoS("besteffort", corev1.PodQOSBestEffort),
		withQoS("burstable", corev1.PodQOSBurstable),
	)

	table, err := listingSorted(c, "pods", "QoS", false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := cellOf(t, table, "besteffort", "QoS").Text; got != "BestEffort" {
		t.Fatalf("QoS = %q, want BestEffort", got)
	}
	// Alphabetical happens to be worst-first, which is the useful direction.
	if got := strings.Join(names(table), ","); got != "besteffort,burstable,guaranteed" {
		t.Fatalf("order = %q, want besteffort,burstable,guaranteed", got)
	}
}

func TestAReplicaSetNamesItsDeploymentAndAJobItsCronJob(t *testing.T) {
	yes := true
	created := metav1.NewTime(clock.Add(-time.Hour))
	one := int32(1)
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "web-7d9f", Namespace: "default", CreationTimestamp: created,
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "web", Controller: &yes}},
		},
		Spec: appsv1.ReplicaSetSpec{Replicas: &one},
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nightly-29001", Namespace: "default", CreationTimestamp: created,
			OwnerReferences: []metav1.OwnerReference{{Kind: "CronJob", Name: "nightly", Controller: &yes}},
		},
		Spec: batchv1.JobSpec{Completions: &one},
	}
	c := client(rs, job)

	for kind, want := range map[string]string{
		"replicasets": "Deployment/web",
		"jobs":        "CronJob/nightly",
	} {
		table, err := listing(c, kind, "", "")
		if err != nil {
			t.Fatalf("List %s: %v", kind, err)
		}
		if got := cellOf(t, table, table.Rows[0].Name, "Controlled by").Text; got != want {
			t.Fatalf("%s Controlled by = %q, want %q", kind, got, want)
		}
	}
}

// A Job's Duration is how long it ran, and a Job that has stopped has stopped
// running.
//
// `status.completionTime` is set on success only. Reading it alone, a Job that
// ran two minutes and failed three days ago would read "3d" and keep growing,
// and so would one somebody suspended: the column would measure the time since
// the Job started, which the Age column beside it already says.
func TestAJobThatStoppedStopsCountingItsDuration(t *testing.T) {
	yes := true
	started := clock.Add(-3 * 24 * time.Hour)
	at := func(d time.Duration) metav1.Time { return metav1.NewTime(started.Add(d)) }
	job := func(status batchv1.JobStatus, suspend *bool) *batchv1.Job {
		status.StartTime = &metav1.Time{Time: started}
		return &batchv1.Job{Spec: batchv1.JobSpec{Suspend: suspend}, Status: status}
	}

	for name, c := range map[string]struct {
		job  *batchv1.Job
		want string
	}{
		"failed": {job(batchv1.JobStatus{
			Failed: 3,
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, LastTransitionTime: at(90 * time.Second)},
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, LastTransitionTime: at(2 * time.Minute)},
			},
		}, nil), "2m"},
		// The controller has decided the Job failed and is still terminating its
		// pods: the decision is the end, not the cleanup.
		"failing": {job(batchv1.JobStatus{
			Failed: 3,
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, LastTransitionTime: at(5 * time.Minute)},
			},
		}, nil), "5m"},
		"suspended": {job(batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobSuspended, Status: corev1.ConditionTrue, LastTransitionTime: at(10 * time.Minute)},
			},
		}, &yes), "10m"},
		"succeeded": {job(batchv1.JobStatus{
			Succeeded:      1,
			CompletionTime: &metav1.Time{Time: started.Add(7 * time.Minute)},
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: at(7 * time.Minute)},
			},
		}, nil), "7m"},
		// Still running, so still counting: the case a completionTime-only reading
		// also gets right, and it must stay right.
		"running": {job(batchv1.JobStatus{Active: 1}, nil), "3d"},
		// Suspended while its pods are still going away: not stopped yet.
		"suspending": {job(batchv1.JobStatus{
			Active: 1,
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobSuspended, Status: corev1.ConditionTrue, LastTransitionTime: at(10 * time.Minute)},
			},
		}, &yes), "3d"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := JobDuration(c.job, clock).Text; got != c.want {
				t.Errorf("Duration = %q, want %q", got, c.want)
			}
		})
	}

	// Stopped, with no record of when. Better an em dash than a figure that
	// grows for as long as the Job is kept.
	undated := job(batchv1.JobStatus{
		Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}},
	}, nil)
	if got := JobDuration(undated, clock); got.Text != Missing {
		t.Errorf("a failed Job with no transition time reads %q; it stopped, so it must not "+
			"count to now", got.Text)
	}
}

// --- secrets ---------------------------------------------------------------------

func TestSecretRowShowsKeyNamesAndNeverValues(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "prod", CreationTimestamp: metav1.NewTime(clock)},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"postgres-password": []byte("hunter2"),
			"username":          []byte("admin"),
		},
	}

	cells := secretRow(secret, clock)

	joined := ""
	for _, cell := range cells {
		joined += cell.Text + "\x00"
	}

	// Both the value and its base64: `data` is encoded, not encrypted, and
	// printing the encoding is printing the password one command later.
	for _, forbidden := range []string{"hunter2", "aHVudGVyMg==", "admin", "YWRtaW4="} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("a secret value reached the table: %q in %q", forbidden, joined)
		}
	}
	for _, wanted := range []string{"postgres-password", "username", "Opaque"} {
		if !strings.Contains(joined, wanted) {
			t.Fatalf("%q is missing from the row: %q", wanted, joined)
		}
	}
}

func TestSecretListingCarriesNoValues(t *testing.T) {
	// The same guarantee through the public entry point, because that is what
	// the HTTP handler actually calls.
	table, err := listing(client(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tls", Namespace: "prod"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.key": []byte("-----BEGIN PRIVATE KEY-----")},
	}), "secrets", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(table.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(table.Rows))
	}
	for _, cell := range table.Rows[0].Cells {
		if strings.Contains(cell.Text, "PRIVATE KEY") {
			t.Fatalf("a key body reached the table: %q", cell.Text)
		}
	}
}

// --- ordering ---------------------------------------------------------------------

func TestUnhealthyRowsSortAboveHealthyOnes(t *testing.T) {
	table, err := listing(client(
		pod("aaa-healthy", corev1.PodRunning, container("app", waiting(""), true)),
		pod("zzz-broken", corev1.PodRunning, container("app", waiting("CrashLoopBackOff"), false)),
		pod("mmm-pending", corev1.PodPending, container("app", waiting("ContainerCreating"), false)),
		pod("bbb-broken", corev1.PodRunning, container("app", waiting("ImagePullBackOff"), false)),
	), "pods", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// Broken first and alphabetical within a rank, so a page that refreshes
	// every two seconds does not reshuffle under the pointer.
	want := []string{"bbb-broken", "zzz-broken", "mmm-pending", "aaa-healthy"}
	got := names(table)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestEventsSortNewestFirstWithinARank(t *testing.T) {
	old := metav1.NewTime(clock.Add(-2 * time.Hour))
	recent := metav1.NewTime(clock.Add(-1 * time.Minute))

	table, err := listing(client(
		event("a.old", "default", corev1.EventTypeNormal, old),
		event("z.recent", "default", corev1.EventTypeNormal, recent),
	), "events", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if got := names(table); got[0] != "z.recent" {
		t.Fatalf("order = %v, want the recent event first", got)
	}
}

func TestFilterMatchesNameOrNamespace(t *testing.T) {
	objects := []runtime.Object{
		inNamespace(pod("checkout", corev1.PodRunning, container("app", waiting(""), true)), "shop"),
		inNamespace(pod("basket", corev1.PodRunning, container("app", waiting(""), true)), "shop"),
		inNamespace(pod("ledger", corev1.PodRunning, container("app", waiting(""), true)), "finance"),
	}

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{name: "an empty query keeps everything", query: "", want: []string{"basket", "checkout", "ledger"}},
		{name: "a name substring", query: "eck", want: []string{"checkout"}},
		// The namespace is not matched, and this pair is the statement of
		// it. There is a namespace selector; a box
		// that also widened the search back out over namespaces would return
		// rows from `finance` to someone who had just chosen `shop`, and they
		// would have no reason to suspect the box.
		{name: "a namespace is not a name", query: "finance", want: nil},
		{name: "nor is it, whatever the case", query: "SHOP", want: nil},
		{name: "the case does not matter for a name", query: "CHECK", want: []string{"checkout"}},
		{name: "no match is an empty table, not an error", query: "nothing-like-this", want: nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			table, err := listing(client(objects...), "pods", "", tc.query)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if got := names(table); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("rows = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- sorting by a column --------------------------------------------------------

// The CPU column of the pods table; see the descriptor.
const cpuColumn = "CPU"

// busy is three pods whose CPU figures are the whole point: 900m must not sort
// above 1200m, which is what comparing the printed text gives, and the pod
// nobody measured must not sort as though it were using nothing.
func busy() (*cluster.Client, map[string]Usage) {
	return client(
		pod("aaa-quiet", corev1.PodRunning, container("app", waiting(""), true)),
		pod("bbb-broken", corev1.PodRunning, container("app", waiting("CrashLoopBackOff"), false)),
		pod("ccc-unmeasured", corev1.PodRunning, container("app", waiting(""), true)),
	), map[string]Usage{
		"default/aaa-quiet":  {CPUMillicores: 1200, MemoryBytes: 2 << 30},
		"default/bbb-broken": {CPUMillicores: 900, MemoryBytes: 900 << 20},
	}
}

func TestANumericColumnSortsByItsValueAndNotByItsText(t *testing.T) {
	c, usage := busy()

	table, err := List(context.Background(), accesstest.Allow(), c, Query{Kind: "pods", Sort: cpuColumn}, usage)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// 900 below 1200 — and the crash-looping pod at the top of the list only
	// because it is the quietest, because an explicit sort replaces the health
	// ranking rather than tie-breaking on it.
	want := []string{"bbb-broken", "aaa-quiet", "ccc-unmeasured"}
	if got := names(table); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if table.Sort != cpuColumn || table.Descending {
		t.Fatalf("the table reports {%q %v}, want {%q false}", table.Sort, table.Descending, cpuColumn)
	}
}

func TestReversingASortLeavesTheUnmeasuredRowLast(t *testing.T) {
	c, usage := busy()

	table, err := List(context.Background(), accesstest.Allow(), c, Query{Kind: "pods", Sort: cpuColumn, Descending: true}, usage)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// An em dash is the absence of a measurement, not the smallest one. It ends
	// the list whichever way the column points; sorting it as a zero is how "we
	// could not measure this" becomes "this uses nothing".
	want := []string{"aaa-quiet", "bbb-broken", "ccc-unmeasured"}
	if got := names(table); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestDroppingTheSortReturnsToTheDefaultOrder(t *testing.T) {
	c, usage := busy()

	// What the third click on a heading asks for. The default order has to stay
	// reachable: it is the one that puts what is broken at the top.
	table, err := List(context.Background(), accesstest.Allow(), c, Query{Kind: "pods"}, usage)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	want := []string{"bbb-broken", "aaa-quiet", "ccc-unmeasured"}
	if got := names(table); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want the unhealthy pod first and then alphabetical: %v", got, want)
	}
	if table.Sort != "" {
		t.Fatalf("table.Sort = %q, want empty so that no heading draws an arrow", table.Sort)
	}
}

func TestAnUnknownSortColumnIsTheDefaultOrderAndNotAnError(t *testing.T) {
	c, usage := busy()

	// A bookmark from another kind's page, or a hand-typed URL. It must produce
	// a table, and it must not claim an order it did not apply.
	table, err := List(context.Background(), accesstest.Allow(), c, Query{Kind: "pods", Sort: "Instance type"}, usage)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if table.Sort != "" {
		t.Fatalf("table.Sort = %q, want empty", table.Sort)
	}
	if got := names(table); got[0] != "bbb-broken" {
		t.Fatalf("order = %v, want the default one", got)
	}
}

func TestSortingByColumnTitleIsCaseInsensitive(t *testing.T) {
	c, usage := busy()

	table, err := List(context.Background(), accesstest.Allow(), c, Query{Kind: "pods", Sort: "cpu"}, usage)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Echoed back as the column declares it, so the heading it belongs to is
	// found by an exact comparison on the page.
	if table.Sort != cpuColumn {
		t.Fatalf("table.Sort = %q, want %q", table.Sort, cpuColumn)
	}
}

func TestMemorySortsByBytesAndNotByTheUnitItPrints(t *testing.T) {
	c, usage := busy()

	table, err := List(context.Background(), accesstest.Allow(), c, Query{Kind: "pods", Sort: "Memory"}, usage)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// "2 GiB" below "900 MiB" is what comparing the text gives.
	if got := names(table); got[0] != "bbb-broken" || got[1] != "aaa-quiet" {
		t.Fatalf("order = %v, want the 900 MiB pod above the 2 GiB one", got)
	}
	if got := cellOf(t, table, "aaa-quiet", "Memory"); got.Text != "2.0 GiB" {
		t.Fatalf("memory = %q, want it in the unit a limit is written in", got.Text)
	}
	if got := cellOf(t, table, "bbb-broken", "Memory"); got.Text != "900 MiB" {
		t.Fatalf("memory = %q, want 900 MiB", got.Text)
	}
}

func TestAgeSortsByTheDurationAndNotByItsPrintedUnit(t *testing.T) {
	young := pod("young", corev1.PodRunning, container("app", waiting(""), true))
	young.CreationTimestamp = metav1.NewTime(clock.Add(-30 * time.Second))
	old := pod("old", corev1.PodRunning, container("app", waiting(""), true))
	old.CreationTimestamp = metav1.NewTime(clock.Add(-90 * 24 * time.Hour))

	table, err := listingSorted(client(young, old), "pods", "Age", false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// "30s" above "90d" alphabetically, and three months old is not younger
	// than half a minute.
	if got := names(table); got[0] != "young" {
		t.Fatalf("order = %v, want the youngest pod first", got)
	}
}

// --- usage, and the difference between nothing and zero -------------------------

func TestWithoutAUsageSourceTheLiveColumnsAreEmDashesAndNotZeros(t *testing.T) {
	// The case that matters most, because it is every cluster with no
	// metrics-server, every expired session, and every caller that has no
	// metrics backend wired in at all. A zero here is a measurement nobody
	// took, and the reader has no way left to tell.
	table, err := listing(client(pod("web", corev1.PodRunning, container("app", waiting(""), true))), "pods", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, title := range []string{"CPU", "Memory"} {
		if got := cellOf(t, table, "web", title); got.Text != Missing {
			t.Fatalf("%s = %q, want %q", title, got.Text, Missing)
		}
	}
}

func TestAMeasuredZeroIsPrintedAsAZero(t *testing.T) {
	// The other half of the same rule: a pod that really is using nothing has
	// been measured, and must not be reported as unmeasurable.
	table, err := List(context.Background(), accesstest.Allow(),
		client(pod("idle", corev1.PodRunning, container("app", waiting(""), true))),
		Query{Kind: "pods"},
		map[string]Usage{"default/idle": {}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := cellOf(t, table, "idle", "CPU"); got.Text != "0m" {
		t.Fatalf("CPU = %q, want 0m", got.Text)
	}
	if got := cellOf(t, table, "idle", "Memory"); got.Text != "0 KiB" {
		t.Fatalf("memory = %q, want a measured nothing", got.Text)
	}
}

func TestUsageIsKeyedTheWayTheMetricsPackageKeysIt(t *testing.T) {
	// Two namespaces each running a `web` is the ordinary state of a cluster.
	// Keyed by name alone, one of them takes the other's figures — which does
	// not look like a bug, it looks like a busy pod.
	objects := []runtime.Object{
		inNamespace(pod("web", corev1.PodRunning, container("app", waiting(""), true)), "shop"),
		inNamespace(pod("web", corev1.PodRunning, container("app", waiting(""), true)), "finance"),
	}

	t.Run("across every namespace the key carries the namespace", func(t *testing.T) {
		table, err := List(context.Background(), accesstest.Allow(), client(objects...), Query{Kind: "pods"},
			map[string]Usage{"shop/web": {CPUMillicores: 250}})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, row := range table.Rows {
			want := Missing
			if row.Namespace == "shop" {
				want = "250m"
			}
			if got := row.Cells[columnOf(t, table, "CPU")]; got.Text != want {
				t.Fatalf("%s/%s: CPU = %q, want %q", row.Namespace, row.Name, got.Text, want)
			}
		}
	})

	t.Run("within one namespace the key is the name alone", func(t *testing.T) {
		// Which is what metrics.Key answers with when a namespace was asked
		// about, and therefore what arrives here.
		table, err := List(context.Background(), accesstest.Allow(), client(objects...),
			Query{Kind: "pods", Namespace: "finance"},
			map[string]Usage{"web": {CPUMillicores: 40}})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if got := cellOf(t, table, "web", "CPU"); got.Text != "40m" {
			t.Fatalf("CPU = %q, want 40m", got.Text)
		}
	})
}

func TestNodesCarryTheLiveColumnsToo(t *testing.T) {
	table, err := List(context.Background(), accesstest.Allow(), client(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "ip-10-0-1-5"},
	}), Query{Kind: "nodes"}, map[string]Usage{"ip-10-0-1-5": {CPUMillicores: 3400, MemoryBytes: 6 << 30}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := cellOf(t, table, "ip-10-0-1-5", "CPU"); got.Text != "3.4 cores" {
		t.Fatalf("CPU = %q, want 3.4 cores", got.Text)
	}
	// The sort key is untouched: the text is banded, the comparison is not.
	if got := cellOf(t, table, "ip-10-0-1-5", "CPU"); got.Value != 3400 {
		t.Fatalf("CPU sorts by %v, want 3400 — banding the text must not reorder the column", got.Value)
	}
	if got := cellOf(t, table, "ip-10-0-1-5", "Memory"); got.Text != "6.0 GiB" {
		t.Fatalf("memory = %q, want 6.0 GiB", got.Text)
	}
}

// --- the contract with the caller ----------------------------------------------

func TestUnknownKindIsAnErrorNamingTheKind(t *testing.T) {
	table, err := listing(client(), "widgets", "", "")
	if err == nil {
		t.Fatal("an unknown kind returned no error")
	}
	if !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("err = %v, want it to wrap ErrUnknownKind", err)
	}
	if !strings.Contains(err.Error(), "widgets") {
		t.Fatalf("err = %v, want it to name the kind", err)
	}
	if len(table.Columns) != 0 || len(table.Rows) != 0 {
		t.Fatal("an unknown kind returned a table as well as an error")
	}
}

func TestAFailedQueryIsNotAnEmptyTable(t *testing.T) {
	// A cache that answers "no pods" for a cluster running 429 of them is the
	// failure here. A caller must be able to tell the two apart without guessing.
	broken := fake.NewClientset()
	broken.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("dial tcp 10.0.0.1:443: connect: connection refused")
	})

	table, err := listing(&cluster.Client{Context: "test", Clients: broken}, "pods", "", "")
	if err == nil {
		t.Fatal("a refused connection was reported as an empty table")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v, want the cause to survive", err)
	}
	if len(table.Rows) != 0 {
		t.Fatal("a failed listing returned rows")
	}

	// And the other half of the contract: a cluster that genuinely has none.
	empty, err := listing(client(), "pods", "", "")
	if err != nil {
		t.Fatalf("an empty cluster returned an error: %v", err)
	}
	if len(empty.Rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(empty.Rows))
	}
	if len(empty.Columns) == 0 {
		t.Fatal("an empty table lost its columns, so the page has no headings")
	}
}

func TestListWithoutAClientFailsRatherThanPanicking(t *testing.T) {
	if _, err := listing(nil, "pods", "", ""); err == nil {
		t.Fatal("a nil client returned no error")
	}
}

// --- the descriptor table ---------------------------------------------------------

func TestEveryKindRendersOneCellPerColumn(t *testing.T) {
	// A row one cell short of its headings renders perfectly and shifts every
	// value one column left, which is the kind of bug nobody reports.
	c := client(oneOfEverything()...)

	for _, descriptor := range Kinds() {
		t.Run(descriptor.Kind, func(t *testing.T) {
			table, err := listing(c, descriptor.Kind, "", "")
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(table.Rows) == 0 {
				t.Fatal("the fixture has no object of this kind, so nothing was checked")
			}
			for _, row := range table.Rows {
				if len(row.Cells) != len(descriptor.Columns) {
					t.Fatalf("%s: %d cells for %d columns", row.Name, len(row.Cells), len(descriptor.Columns))
				}
				for i, cell := range row.Cells {
					if cell.Text == "" {
						t.Fatalf("%s: column %q is empty, which reads as a rendering fault",
							row.Name, descriptor.Columns[i].Title)
					}
				}
			}
		})
	}
}

func TestClusterScopedKindsIgnoreANamespace(t *testing.T) {
	// The UI carries a namespace across a change of kind. A node list is not
	// wrong for having been asked while kube-system was selected — and a node
	// name must never be read as a namespace: a node that exists would be
	// reported as not found.
	table, err := listing(client(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "ip-10-0-1-5"},
	}), "nodes", "kube-system", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(table.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(table.Rows))
	}
	if table.Namespace != "" {
		t.Fatalf("namespace = %q, want it dropped", table.Namespace)
	}
}

func TestNamespacedListingIsRestrictedToOneNamespace(t *testing.T) {
	table, err := listing(client(
		inNamespace(pod("here", corev1.PodRunning, container("app", waiting(""), true)), "shop"),
		inNamespace(pod("elsewhere", corev1.PodRunning, container("app", waiting(""), true)), "finance"),
	), "pods", "shop", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := names(table); strings.Join(got, ",") != "here" {
		t.Fatalf("rows = %v, want only the ones in shop", got)
	}
}

func TestLookupAcceptsTheNamesPeopleActuallyType(t *testing.T) {
	tests := []struct {
		typed string
		want  string
	}{
		{typed: "pods", want: "pods"},
		{typed: "pod", want: "pods"},
		{typed: "po", want: "pods"},
		{typed: "Deployments", want: "deployments"},
		{typed: "svc", want: "services"},
		{typed: " pvc ", want: "persistentvolumeclaims"},
		{typed: "ns", want: "namespaces"},
	}
	for _, tc := range tests {
		t.Run(tc.typed, func(t *testing.T) {
			d, ok := Lookup(tc.typed)
			if !ok {
				t.Fatalf("%q was not recognized", tc.typed)
			}
			if d.Kind != tc.want {
				t.Fatalf("kind = %q, want %q", d.Kind, tc.want)
			}
		})
	}

	if _, ok := Lookup("widgets"); ok {
		t.Fatal("an invented kind was recognized")
	}
}

func TestEveryAliasPointsAtARealKind(t *testing.T) {
	for alias, kind := range aliases {
		if _, ok := Lookup(kind); !ok {
			t.Fatalf("alias %q points at %q, which is not a descriptor", alias, kind)
		}
	}
}

// --- formatting ---------------------------------------------------------------------

func TestAgesAreOneUnitAndNoMore(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{name: "seconds", d: 12 * time.Second, want: "12s"},
		{name: "just under a minute", d: 59 * time.Second, want: "59s"},
		{name: "minutes", d: 4 * time.Minute, want: "4m"},
		{name: "minutes round down", d: 4*time.Minute + 59*time.Second, want: "4m"},
		{name: "hours", d: 3 * time.Hour, want: "3h"},
		{name: "days", d: 21 * 24 * time.Hour, want: "21d"},
		{name: "a clock disagreement is not a negative age", d: -5 * time.Second, want: "0s"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShortDuration(tc.d); got != tc.want {
				t.Fatalf("ShortDuration(%s) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}

func TestScaledToZeroIsNotAFault(t *testing.T) {
	// The old ReplicaSets of every past rollout sit at 0/0. Coloring them red
	// teaches the reader to ignore red.
	tests := []struct {
		name        string
		ready, want int32
		class       string
		rank        int
	}{
		{name: "scaled to zero", ready: 0, want: 0, class: ClassOK, rank: RankOK},
		{name: "fully ready", ready: 3, want: 3, class: ClassOK, rank: RankOK},
		{name: "partially ready", ready: 1, want: 3, class: ClassWarn, rank: RankWarn},
		{name: "nothing ready", ready: 0, want: 3, class: ClassBad, rank: RankBad},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cell := ratio(tc.ready, tc.want)
			if cell.Class != tc.class || cell.Rank != tc.rank {
				t.Fatalf("ratio(%d,%d) = {%q %d}, want {%q %d}",
					tc.ready, tc.want, cell.Class, cell.Rank, tc.class, tc.rank)
			}
		})
	}
}

func TestAMissingValueIsAnEmDashAndNotAnEmptyCell(t *testing.T) {
	if got := text("").Text; got != Missing {
		t.Fatalf("text(\"\") = %q, want %q", got, Missing)
	}
}

// --- the later kinds -----------------------------------------

// A ReplicationController is the one workload whose pod template is a pointer,
// and one stored without it is valid enough for the API server to keep. The
// fixtures all have one; a real object from 2016 need not.
func TestAReplicationControllerWithNoTemplateRendersRatherThanPanicking(t *testing.T) {
	table, err := listing(client(&corev1.ReplicationController{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "default"},
	}), "replicationcontrollers", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := cellOf(t, table, "legacy", "Image").Text; got != Missing {
		t.Fatalf("Image = %q, want an em dash", got)
	}
}

// Two default storage classes is a real cluster problem — MI-K3S has exactly
// that — and this column is the only place it is visible. The beta annotation
// counts because older charts still write it, and reading only the stable
// spelling reports a working default as none.
func TestEveryClassCarryingTheDefaultAnnotationIsMarked(t *testing.T) {
	class := func(name, annotation string) *storagev1.StorageClass {
		c := &storagev1.StorageClass{
			ObjectMeta:  metav1.ObjectMeta{Name: name},
			Provisioner: "ebs.csi.aws.com",
		}
		if annotation != "" {
			c.Annotations = map[string]string{annotation: "true"}
		}
		return c
	}
	table, err := listing(client(
		class("stable", "storageclass.kubernetes.io/is-default-class"),
		class("beta", "storageclass.beta.kubernetes.io/is-default-class"),
		class("plain", ""),
	), "storageclasses", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	for _, name := range []string{"stable", "beta"} {
		cell := cellOf(t, table, name, "Default")
		if cell.Text != "default" || cell.Class != ClassOK {
			t.Fatalf("%s: Default = %q/%q, want the class marked", name, cell.Text, cell.Class)
		}
	}
	if got := cellOf(t, table, "plain", "Default").Text; got != Missing {
		t.Fatalf("plain: Default = %q, want an em dash", got)
	}
}

// disruptionsAllowed at zero is what a drain blocks on, and the drain names the
// budget and nothing else. It is the reason this kind is listed, so it is the
// thing that reaches the top of the list.
func TestAPodDisruptionBudgetAllowingNothingSortsToTheTop(t *testing.T) {
	budget := func(name string, allowed int32) *policyv1.PodDisruptionBudget {
		return &policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Status: policyv1.PodDisruptionBudgetStatus{
				CurrentHealthy: 3, DesiredHealthy: 2, DisruptionsAllowed: allowed,
			},
		}
	}
	table, err := listing(client(budget("a-fine", 1), budget("z-blocking", 0)), "poddisruptionbudgets", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	blocking := cellOf(t, table, "z-blocking", "Allowed")
	if blocking.Class != ClassBad || blocking.Rank != RankBad {
		t.Fatalf("Allowed=0 graded %q/%d, want bad", blocking.Class, blocking.Rank)
	}
	// Healthy is fine on both, so only the blocked one may be ranked — the two
	// are different faults and must not be reported as one.
	if got := names(table); got[0] != "z-blocking" {
		t.Fatalf("order = %v, want the blocked budget first despite its name", got)
	}
}

// A webhook with no failurePolicy fails *closed*: Fail is the v1 default and the
// opposite of the v1beta1 one. Reading nil as Ignore would call a configuration
// harmless when it can stop every write to the cluster.
func TestAWebhookWithNoFailurePolicyIsReportedAsFailing(t *testing.T) {
	ignore := admissionv1.Ignore
	table, err := listing(client(
		&admissionv1.ValidatingWebhookConfiguration{
			ObjectMeta: metav1.ObjectMeta{Name: "unstated"},
			Webhooks:   []admissionv1.ValidatingWebhook{{Name: "a.example.com"}},
		},
		&admissionv1.ValidatingWebhookConfiguration{
			ObjectMeta: metav1.ObjectMeta{Name: "open"},
			Webhooks: []admissionv1.ValidatingWebhook{
				{Name: "b.example.com", FailurePolicy: &ignore},
				{Name: "c.example.com", FailurePolicy: &ignore},
			},
		},
	), "validatingwebhookconfigurations", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	closed := cellOf(t, table, "unstated", "Failure policy")
	if closed.Text != "Fail" {
		t.Fatalf("an unstated failure policy rendered as %q, want Fail", closed.Text)
	}
	if closed.Class != ClassWarn {
		t.Fatalf("Fail was not colored (%q)", closed.Class)
	}
	// Colored, never ranked: half the configurations on a stock cluster fail
	// closed, and ranking them would put the same dozen rows on top for ever.
	if closed.Rank != 0 {
		t.Fatalf("Fail claimed rank %d, want none", closed.Rank)
	}
	// Repeated policies collapse: "Ignore, Ignore" says nothing the first did not.
	if got := cellOf(t, table, "open", "Failure policy").Text; got != "Ignore" {
		t.Fatalf("two Ignore webhooks rendered as %q", got)
	}
}

// An empty pod selector is every pod in the namespace — the widest a policy can
// be. Rendered as a blank or an em dash it reads as the narrowest, which is the
// opposite of the truth about what that policy does.
func TestAnEmptySelectorIsTheWholeNamespaceAndNotNothing(t *testing.T) {
	table, err := listing(client(&networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "default-deny", Namespace: "shop"},
		Spec: networkingv1.NetworkPolicySpec{
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}), "networkpolicies", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := cellOf(t, table, "default-deny", "Selects").Text; got != "all pods" {
		t.Fatalf("Selects = %q, want it to say the whole namespace", got)
	}
}

// The direction columns are the point of this kind. A policy naming Egress and
// carrying no egress rule denies all egress, DNS included; a policy not naming
// it at all leaves egress alone. Rendering both the same is how a namespace goes
// dark ten minutes after somebody adds a policy that looked like ingress only.
func TestAGovernedDirectionWithNoRulesSaysDenyAll(t *testing.T) {
	table, err := listing(client(&networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "agents", Namespace: "jenkins"},
		Spec: networkingv1.NetworkPolicySpec{
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}), "networkpolicies", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := cellOf(t, table, "agents", "Ingress").Text; got != "deny all" {
		t.Fatalf("Ingress = %q, want deny all", got)
	}
	if got := cellOf(t, table, "agents", "Egress").Text; got != Missing {
		t.Fatalf("Egress = %q, want an em dash for a direction this policy does not govern", got)
	}
}

// `*` on verbs and resources together is cluster-admin under another name, and
// it is what an auditor opens this list for. A wildcard on one of the three
// alone is colored and not ranked: a stock cluster ships dozens.
func TestARoleGrantingEverythingOutranksOneThatGrantsOneWildcard(t *testing.T) {
	role := func(name string, groups, resources, verbs []string) *rbacv1.ClusterRole {
		return &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: groups, Resources: resources, Verbs: verbs},
			},
		}
	}
	table, err := listing(client(
		role("a-narrow", []string{""}, []string{"pods"}, []string{"get", "list"}),
		role("m-partly", []string{"*"}, []string{"applications"}, []string{"get"}),
		role("z-everything", []string{"*"}, []string{"*"}, []string{"*"}),
	), "clusterroles", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	everything := cellOf(t, table, "z-everything", "Wildcards")
	if everything.Class != ClassBad || everything.Rank != RankBad {
		t.Fatalf("`*` on verbs and resources graded %q/%d, want bad", everything.Class, everything.Rank)
	}
	partly := cellOf(t, table, "m-partly", "Wildcards")
	if partly.Class != ClassWarn || partly.Rank != 0 {
		t.Fatalf("one wildcard graded %q/%d, want colored and unranked", partly.Class, partly.Rank)
	}
	if got := cellOf(t, table, "a-narrow", "Wildcards").Text; got != Missing {
		t.Fatalf("a role with no wildcard says %q, want an em dash", got)
	}
	if got := names(table); got[0] != "z-everything" {
		t.Fatalf("order = %v, want the role granting everything first", got)
	}
}

// The em-dash rule, on a column that is genuinely empty on most rows: a role
// with no wildcard has no value in that column, and no value sorts last in both
// directions rather than sorting as the smallest string.
func TestARoleWithNoWildcardSortsLastInBothDirections(t *testing.T) {
	role := func(name string, verbs []string) *rbacv1.ClusterRole {
		return &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: verbs}},
		}
	}
	c := client(
		role("a-wild", []string{"*"}),
		role("b-narrow", []string{"get"}),
		role("c-wild", []string{"*"}),
	)
	for _, descending := range []bool{false, true} {
		table, err := listingSorted(c, "clusterroles", "Wildcards", descending)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		got := names(table)
		if got[len(got)-1] != "b-narrow" {
			t.Fatalf("descending=%v: order = %v, want the role with no wildcard last", descending, got)
		}
	}
}

// A binding is its role and its subjects and nothing else. The role keeps its
// kind because a RoleBinding may point at a ClusterRole, and a subject keeps its
// own kind because a Group called `system:masters` and a User called
// `system:masters` are not the same grant.
func TestABindingNamesTheKindOfItsRoleAndOfItsSubjects(t *testing.T) {
	table, err := listing(client(&rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "readers", Namespace: "shop"},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: "view"},
		Subjects: []rbacv1.Subject{
			{Kind: rbacv1.ServiceAccountKind, Namespace: "kube-system", Name: "default"},
			{Kind: rbacv1.GroupKind, Name: "system:masters"},
			{Kind: rbacv1.UserKind, Name: "alice"},
			{Kind: rbacv1.UserKind, Name: "bob"},
		},
	}), "rolebindings", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if got := cellOf(t, table, "readers", "Role").Text; got != "ClusterRole/view" {
		t.Fatalf("Role = %q, want the kind kept", got)
	}
	// Bounded, and it says how many it left out rather than trailing off.
	want := "sa:kube-system/default, group:system:masters, user:alice +1 more"
	if got := cellOf(t, table, "readers", "Bound to").Text; got != want {
		t.Fatalf("Bound to = %q, want %q", got, want)
	}
}

// A slice with no addresses is a Service with nothing behind it — the outage
// this kind is listed for. ratio calls nothing-wanted healthy, which is right
// for a Deployment scaled to zero and wrong here.
//
// The nil readiness condition is the other half: it means "not tracked", which
// consumers are told to read as ready, and reading it as not-ready reports every
// endpoint of a headless Service as broken.
func TestAnEndpointSliceGradesEmptinessAndTreatsAnUnsetConditionAsReady(t *testing.T) {
	no := false
	slice := func(name string, endpoints ...discoveryv1.Endpoint) *discoveryv1.EndpointSlice {
		return &discoveryv1.EndpointSlice{
			ObjectMeta:  metav1.ObjectMeta{Name: name, Namespace: "default"},
			AddressType: discoveryv1.AddressTypeIPv4,
			Endpoints:   endpoints,
		}
	}
	table, err := listing(client(
		slice("empty"),
		slice("untracked", discoveryv1.Endpoint{Addresses: []string{"10.0.1.7"}}),
		slice("half",
			discoveryv1.Endpoint{Addresses: []string{"10.0.1.8"}},
			discoveryv1.Endpoint{
				Addresses:  []string{"10.0.1.9"},
				Conditions: discoveryv1.EndpointConditions{Ready: &no},
			}),
	), "endpointslices", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	empty := cellOf(t, table, "empty", "Ready")
	if empty.Class != ClassBad || empty.Rank != RankBad {
		t.Fatalf("an empty slice graded %q/%d, want bad", empty.Class, empty.Rank)
	}
	if got := cellOf(t, table, "untracked", "Ready").Text; got != "1/1" {
		t.Fatalf("an unset readiness condition rendered as %q, want it counted ready", got)
	}
	if got := cellOf(t, table, "half", "Ready").Text; got != "1/2" {
		t.Fatalf("Ready = %q, want 1/2", got)
	}
}

// A lease is judged against its own stated duration and never against a fixed
// number: a node lease is 40 seconds and a leader lease 15, so any absolute
// threshold is blind to one or alarmed by the other. A lease stating no duration
// is not judged at all — there is nothing to judge it against.
func TestALeaseIsJudgedAgainstItsOwnStatedDuration(t *testing.T) {
	lease := func(name string, seconds *int32, renewedAgo time.Duration) *coordinationv1.Lease {
		holder := "controller-0"
		renewed := metav1.MicroTime{Time: time.Now().Add(-renewedAgo)}
		return &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kube-system"},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       &holder,
				LeaseDurationSeconds: seconds,
				RenewTime:            &renewed,
			},
		}
	}
	fifteen, forty := int32(15), int32(40)
	table, err := listing(client(
		lease("fresh", &fifteen, 2*time.Second),
		lease("late", &fifteen, 40*time.Second),
		lease("gone", &fifteen, 10*time.Minute),
		// Fresh by its own reckoning, and stale by the one beside it. A fixed
		// threshold would have to be wrong about one of these two.
		lease("slow-but-fine", &forty, 30*time.Second),
		lease("unstated", nil, 10*time.Minute),
	), "leases", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	for _, tc := range []struct{ name, class string }{
		{"fresh", ClassOK},
		{"late", ClassWarn},
		{"gone", ClassBad},
		{"slow-but-fine", ClassOK},
		{"unstated", ClassNone},
	} {
		if got := cellOf(t, table, tc.name, "Renewed").Class; got != tc.class {
			t.Errorf("%s: Renewed graded %q, want %q", tc.name, got, tc.class)
		}
	}
}

// A quota's used-against-hard is the row, and the fullest resource leads it —
// the three a capped cell shows must be the three the operator is about to be
// stopped by, not the three whose names sort first.
//
// The fraction comes from AsApproximateFloat64 and not Value(): `3500m` of `4`
// is 88% full, and Value() rounds 3500m up to 4 and calls it 100%, which would
// put a quota with room to spare at the head of this list.
func TestTheQuotaListLeadsWithTheFullestResourceMeasuredInMillicores(t *testing.T) {
	table, err := listing(client(&corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "team", Namespace: "shop"},
		Status: corev1.ResourceQuotaStatus{
			Hard: corev1.ResourceList{"requests.cpu": quantityOf("4"), "pods": quantityOf("20")},
			Used: corev1.ResourceList{"requests.cpu": quantityOf("3500m"), "pods": quantityOf("19")},
		},
	}), "resourcequotas", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	used := cellOf(t, table, "team", "Used of hard").Text
	if !strings.HasPrefix(used, "pods 19/20") {
		t.Fatalf("Used of hard = %q, want the 95%%-full resource first", used)
	}
	highest := cellOf(t, table, "team", "Highest")
	if highest.Text != "95%" {
		t.Fatalf("Highest = %q, want 95%%", highest.Text)
	}
	if highest.Class != ClassBad && highest.Class != ClassWarn {
		t.Fatalf("a quota at 95%% graded %q, want it flagged", highest.Class)
	}
}

// The question an HPA is opened with is never "does it exist", it is "why is it
// not scaling" — so the metric carries its current value beside its target, and
// a metric the autoscaler could not read says so rather than rendering blank.
func TestTheAutoscalerReportsItsMetricAndTheConditionThatStoppedIt(t *testing.T) {
	one, seventy, fortyThree := int32(1), int32(70), int32(43)
	hpa := func(name string, current []autoscalingv2.MetricStatus, conditions []autoscalingv2.HorizontalPodAutoscalerCondition) *autoscalingv2.HorizontalPodAutoscaler {
		return &autoscalingv2.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop"},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
				ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "web"},
				MinReplicas:    &one,
				MaxReplicas:    10,
				Metrics: []autoscalingv2.MetricSpec{{
					Type: autoscalingv2.ResourceMetricSourceType,
					Resource: &autoscalingv2.ResourceMetricSource{
						Name:   corev1.ResourceCPU,
						Target: autoscalingv2.MetricTarget{AverageUtilization: &seventy},
					},
				}},
			},
			Status: autoscalingv2.HorizontalPodAutoscalerStatus{
				CurrentReplicas: 2, DesiredReplicas: 2,
				CurrentMetrics: current, Conditions: conditions,
			},
		}
	}

	table, err := listing(client(
		hpa("working",
			[]autoscalingv2.MetricStatus{{
				Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricStatus{
					Name:    corev1.ResourceCPU,
					Current: autoscalingv2.MetricValueStatus{AverageUtilization: &fortyThree},
				},
			}},
			[]autoscalingv2.HorizontalPodAutoscalerCondition{
				{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionTrue},
			}),
		hpa("blind", nil, []autoscalingv2.HorizontalPodAutoscalerCondition{
			{
				Type:   autoscalingv2.ScalingActive,
				Status: corev1.ConditionFalse,
				Reason: "FailedGetResourceMetric",
			},
		}),
	), "horizontalpodautoscalers", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if got := cellOf(t, table, "working", "Metrics").Text; got != "cpu 43%/70%" {
		t.Fatalf("Metrics = %q, want cpu 43%%/70%%", got)
	}
	// The absence of the number is the report, so it is printed rather than left
	// blank — a blank cell here reads as "nothing to say".
	if got := cellOf(t, table, "blind", "Metrics").Text; got != "cpu ?/70%" {
		t.Fatalf("Metrics = %q, want the unread metric named", got)
	}
	// The reason and not a summary of it: `FailedGetResourceMetric` is the string
	// worth searching for, and "not scaling" would send the reader to describe.
	status := cellOf(t, table, "blind", "Status")
	if status.Text != "FailedGetResourceMetric" || status.Rank != RankBad {
		t.Fatalf("Status = %q/%d, want the condition reason, ranked bad", status.Text, status.Rank)
	}
}

// --- fixtures -----------------------------------------------------------------------

func quantityOf(s string) resource.Quantity { return resource.MustParse(s) }

func withOwner(m metav1.ObjectMeta, refs []metav1.OwnerReference) metav1.ObjectMeta {
	m.OwnerReferences = refs
	return m
}

func names(table Table) []string {
	var out []string
	for _, row := range table.Rows {
		out = append(out, row.Name)
	}
	return out
}

func pod(name string, phase corev1.PodPhase, status corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(clock.Add(-time.Hour)),
		},
		Spec: corev1.PodSpec{
			NodeName:   "ip-10-0-1-5",
			Containers: []corev1.Container{{Name: status.Name, Image: "nginx:1.27"}},
		},
		Status: corev1.PodStatus{Phase: phase, ContainerStatuses: []corev1.ContainerStatus{status}},
	}
}

// podOf is pod for the tests that need more than one container: the spec is
// built from the statuses, because the Containers column walks the spec and
// looks each status up by name.
func podOf(name string, phase corev1.PodPhase, statuses ...corev1.ContainerStatus) *corev1.Pod {
	p := pod(name, phase, corev1.ContainerStatus{Name: "app"})
	p.Spec.Containers = make([]corev1.Container, 0, len(statuses))
	for _, s := range statuses {
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: s.Name, Image: "nginx:1.27"})
	}
	p.Status.ContainerStatuses = statuses
	return p
}

func initPod(name string, phase corev1.PodPhase, status corev1.ContainerStatus) *corev1.Pod {
	p := pod(name, phase, corev1.ContainerStatus{
		Name:  "app",
		Ready: false,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}},
	})
	p.Spec.InitContainers = []corev1.Container{{Name: status.Name, Image: "busybox"}}
	p.Status.InitContainerStatuses = []corev1.ContainerStatus{status}
	return p
}

// sidecarPod is a pod with one native sidecar in front of one regular
// container: an init container carrying `restartPolicy: Always`, which is how
// a service mesh and every log shipper are injected.
func sidecarPod(name string, phase corev1.PodPhase, side, app corev1.ContainerStatus) *corev1.Pod {
	p := pod(name, phase, app)
	always := corev1.ContainerRestartPolicyAlways
	p.Spec.InitContainers = []corev1.Container{
		{Name: side.Name, Image: "istio/proxyv2:1.24", RestartPolicy: &always},
	}
	p.Status.InitContainerStatuses = []corev1.ContainerStatus{side}
	return p
}

// sidecar is the status a running native sidecar has: never Terminated.
func sidecar(name string, hasStarted, ready bool) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name:    name,
		Ready:   ready,
		Started: &hasStarted,
		State:   corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}
}

// meshedWithInit is the pod a mesh cluster actually runs: a sidecar that is up
// and an ordinary init container behind it that has not run yet.
func meshedWithInit(name string) *corev1.Pod {
	p := sidecarPod(name, corev1.PodPending,
		sidecar("istio-proxy", true, true),
		container("app", waiting("PodInitializing"), false))
	p.Spec.InitContainers = append(p.Spec.InitContainers, corev1.Container{Name: "migrate", Image: "busybox"})
	p.Status.InitContainerStatuses = append(p.Status.InitContainerStatuses,
		container("migrate", waiting("PodInitializing"), false))
	return p
}

func evicted(name string) *corev1.Pod {
	p := pod(name, corev1.PodFailed, corev1.ContainerStatus{Name: "app"})
	p.Status.ContainerStatuses = nil
	p.Status.Reason = "Evicted"
	return p
}

func container(name string, state corev1.ContainerState, ready bool) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, Ready: ready, State: state}
}

func waiting(reason string) corev1.ContainerState {
	if reason == "" {
		return corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	}
	return corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}
}

func terminated(reason string, code int32) corev1.ContainerState {
	return corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{Reason: reason, ExitCode: code},
	}
}

func inNamespace(p *corev1.Pod, namespace string) *corev1.Pod {
	p.Namespace = namespace
	return p
}

func event(name, namespace string, kind string, at metav1.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: namespace, CreationTimestamp: at},
		Type:           kind,
		Reason:         "Scheduled",
		Message:        "Successfully assigned default/web to ip-10-0-1-5",
		Count:          1,
		LastTimestamp:  at,
		FirstTimestamp: at,
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "web", Namespace: namespace},
	}
}

// oneOfEverything is one object of every kind in the descriptor table, filled
// enough that no column comes out empty. It is what makes the cells-per-column
// check cover every kind rather than the one somebody remembered.
func oneOfEverything() []runtime.Object {
	created := metav1.NewTime(clock.Add(-24 * time.Hour))
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: created}
	}
	clusterMeta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, CreationTimestamp: created}
	}
	one := int32(1)
	suspended := false
	class := "gp3"
	ready := true
	port := int32(8080)
	tcp := corev1.ProtocolTCP
	seventy := int32(70)
	fortyThree := int32(43)
	fifteen := int32(15)
	minAvailable := intstr.FromInt32(1)
	preempt := corev1.PreemptLowerPriority
	holder := "ip-10-0-1-5_8f2ab1c4"
	fail := admissionv1.Fail
	reclaim := corev1.PersistentVolumeReclaimDelete
	binding := storagev1.VolumeBindingWaitForFirstConsumer
	template := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.example.com/app:1.2.3"}}},
	}
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}}
	controller := true
	ownedBy := func(kind, name string) []metav1.OwnerReference {
		return []metav1.OwnerReference{{Kind: kind, Name: name, Controller: &controller}}
	}
	owned := func(p *corev1.Pod) *corev1.Pod {
		p.OwnerReferences = ownedBy("ReplicaSet", "web-7d9f")
		p.Status.QOSClass = corev1.PodQOSBurstable
		return p
	}

	return []runtime.Object{
		owned(pod("web", corev1.PodRunning, container("app", waiting(""), true))),
		&appsv1.Deployment{
			ObjectMeta: meta("web"),
			Spec:       appsv1.DeploymentSpec{Replicas: &one, Selector: selector, Template: template},
			Status:     appsv1.DeploymentStatus{ReadyReplicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1},
		},
		&appsv1.StatefulSet{
			ObjectMeta: meta("db"),
			Spec:       appsv1.StatefulSetSpec{Replicas: &one, Selector: selector, Template: template},
			Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1, UpdatedReplicas: 1},
		},
		&appsv1.DaemonSet{
			ObjectMeta: meta("agent"),
			Spec:       appsv1.DaemonSetSpec{Selector: selector, Template: template},
			Status: appsv1.DaemonSetStatus{
				DesiredNumberScheduled: 1, NumberReady: 1, UpdatedNumberScheduled: 1, NumberAvailable: 1,
			},
		},
		&appsv1.ReplicaSet{
			ObjectMeta: withOwner(meta("web-7d9f"), ownedBy("Deployment", "web")),
			Spec:       appsv1.ReplicaSetSpec{Replicas: &one, Selector: selector, Template: template},
			Status:     appsv1.ReplicaSetStatus{Replicas: 1, ReadyReplicas: 1},
		},
		&batchv1.Job{
			ObjectMeta: withOwner(meta("backup"), ownedBy("CronJob", "nightly")),
			Spec:       batchv1.JobSpec{Completions: &one, Suspend: &suspended, Template: template},
			Status: batchv1.JobStatus{
				Succeeded:      1,
				StartTime:      &created,
				CompletionTime: &metav1.Time{Time: created.Add(90 * time.Second)},
				Conditions:     []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
			},
		},
		&batchv1.CronJob{
			ObjectMeta: meta("nightly"),
			Spec:       batchv1.CronJobSpec{Schedule: "0 2 * * *", Suspend: &suspended},
			Status:     batchv1.CronJobStatus{LastScheduleTime: &created},
		},
		&corev1.Service{
			ObjectMeta: meta("web"),
			Spec: corev1.ServiceSpec{
				Type:      corev1.ServiceTypeClusterIP,
				ClusterIP: "10.96.0.10",
				Ports:     []corev1.ServicePort{{Port: 80, Protocol: corev1.ProtocolTCP}},
			},
		},
		&networkingv1.Ingress{
			ObjectMeta: meta("web"),
			Spec: networkingv1.IngressSpec{
				IngressClassName: &class,
				Rules:            []networkingv1.IngressRule{{Host: "app.example.com"}},
			},
			Status: networkingv1.IngressStatus{
				LoadBalancer: networkingv1.IngressLoadBalancerStatus{
					Ingress: []networkingv1.IngressLoadBalancerIngress{{Hostname: "elb.example.com"}},
				},
			},
		},
		&corev1.Endpoints{
			ObjectMeta: meta("web"),
			Subsets: []corev1.EndpointSubset{{
				Addresses: []corev1.EndpointAddress{{IP: "10.0.1.7"}},
				Ports:     []corev1.EndpointPort{{Port: 8080}},
			}},
		},
		&corev1.ConfigMap{ObjectMeta: meta("settings"), Data: map[string]string{"app.yaml": "debug: false"}},
		&corev1.Secret{
			ObjectMeta: meta("db"),
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{"password": []byte("hunter2")},
		},
		&corev1.ServiceAccount{
			ObjectMeta: meta("deployer"),
			Secrets:    []corev1.ObjectReference{{Name: "deployer-token"}},
		},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: meta("data"),
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				StorageClassName: &class,
				VolumeName:       "pvc-1234",
			},
			Status: corev1.PersistentVolumeClaimStatus{
				Phase:    corev1.ClaimBound,
				Capacity: corev1.ResourceList{corev1.ResourceStorage: quantityOf("8Gi")},
			},
		},
		&corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pvc-1234", CreationTimestamp: created},
			Spec: corev1.PersistentVolumeSpec{
				AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Capacity:                      corev1.ResourceList{corev1.ResourceStorage: quantityOf("8Gi")},
				PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
				StorageClassName:              "gp3",
				ClaimRef:                      &corev1.ObjectReference{Namespace: "default", Name: "data"},
			},
			Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
		},
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "ip-10-0-1-5",
				CreationTimestamp: created,
				Labels: map[string]string{
					"node-role.kubernetes.io/worker":   "",
					"node.kubernetes.io/instance-type": "m6i.large",
				},
			},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
				Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.1.5"}},
				NodeInfo:   corev1.NodeSystemInfo{KubeletVersion: "v1.31.4-eks"},
			},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "default", CreationTimestamp: created},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		event("web.17abc", "default", corev1.EventTypeWarning, created),
		&corev1.ReplicationController{
			ObjectMeta: meta("legacy"),
			Spec: corev1.ReplicationControllerSpec{
				Replicas: &one,
				Template: &template,
			},
			Status: corev1.ReplicationControllerStatus{Replicas: 1, ReadyReplicas: 1},
		},
		&discoveryv1.EndpointSlice{
			ObjectMeta:  withLabels(meta("web-abc12"), map[string]string{discoveryv1.LabelServiceName: "web"}),
			AddressType: discoveryv1.AddressTypeIPv4,
			Endpoints: []discoveryv1.Endpoint{{
				Addresses:  []string{"10.0.1.7"},
				Conditions: discoveryv1.EndpointConditions{Ready: &ready},
			}},
			Ports: []discoveryv1.EndpointPort{{Port: &port, Protocol: &tcp}},
		},
		&networkingv1.IngressClass{
			ObjectMeta: withAnnotations(clusterMeta("nginx"), map[string]string{
				"ingressclass.kubernetes.io/is-default-class": "true",
			}),
			Spec: networkingv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"},
		},
		&networkingv1.NetworkPolicy{
			ObjectMeta: meta("allow-web"),
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: *selector,
				PolicyTypes: []networkingv1.PolicyType{
					networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress,
				},
				Ingress: []networkingv1.NetworkPolicyIngressRule{{}},
			},
		},
		&corev1.ResourceQuota{
			ObjectMeta: meta("team"),
			Spec:       corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{"pods": quantityOf("20")}},
			Status: corev1.ResourceQuotaStatus{
				Hard: corev1.ResourceList{"pods": quantityOf("20")},
				Used: corev1.ResourceList{"pods": quantityOf("12")},
			},
		},
		&corev1.LimitRange{
			ObjectMeta: meta("defaults"),
			Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
				Type:           corev1.LimitTypeContainer,
				Max:            corev1.ResourceList{corev1.ResourceCPU: quantityOf("2")},
				DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: quantityOf("100m")},
			}}},
		},
		&autoscalingv2.HorizontalPodAutoscaler{
			ObjectMeta: meta("web"),
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
				ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "web"},
				MinReplicas:    &one,
				MaxReplicas:    10,
				Metrics: []autoscalingv2.MetricSpec{{
					Type: autoscalingv2.ResourceMetricSourceType,
					Resource: &autoscalingv2.ResourceMetricSource{
						Name: corev1.ResourceCPU,
						Target: autoscalingv2.MetricTarget{
							Type:               autoscalingv2.UtilizationMetricType,
							AverageUtilization: &seventy,
						},
					},
				}},
			},
			Status: autoscalingv2.HorizontalPodAutoscalerStatus{
				CurrentReplicas: 2,
				DesiredReplicas: 2,
				CurrentMetrics: []autoscalingv2.MetricStatus{{
					Type: autoscalingv2.ResourceMetricSourceType,
					Resource: &autoscalingv2.ResourceMetricStatus{
						Name:    corev1.ResourceCPU,
						Current: autoscalingv2.MetricValueStatus{AverageUtilization: &fortyThree},
					},
				}},
				Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
					{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionTrue},
				},
			},
		},
		&policyv1.PodDisruptionBudget{
			ObjectMeta: meta("web"),
			Spec: policyv1.PodDisruptionBudgetSpec{
				MinAvailable: &minAvailable,
				Selector:     selector,
			},
			Status: policyv1.PodDisruptionBudgetStatus{
				CurrentHealthy: 2, DesiredHealthy: 1, DisruptionsAllowed: 1,
			},
		},
		&schedulingv1.PriorityClass{
			ObjectMeta:       clusterMeta("high"),
			Value:            1000,
			GlobalDefault:    true,
			PreemptionPolicy: &preempt,
			Description:      "for the things that must run",
		},
		&nodev1.RuntimeClass{
			ObjectMeta: clusterMeta("gvisor"),
			Handler:    "runsc",
			Overhead: &nodev1.Overhead{
				PodFixed: corev1.ResourceList{corev1.ResourceMemory: quantityOf("120Mi")},
			},
			Scheduling: &nodev1.Scheduling{NodeSelector: map[string]string{"sandbox": "true"}},
		},
		&coordinationv1.Lease{
			ObjectMeta: meta("kube-scheduler"),
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       &holder,
				LeaseDurationSeconds: &fifteen,
				RenewTime:            &metav1.MicroTime{Time: clock.Add(-2 * time.Second)},
			},
		},
		&admissionv1.MutatingWebhookConfiguration{
			ObjectMeta: clusterMeta("cert-manager"),
			Webhooks: []admissionv1.MutatingWebhook{{
				Name:          "webhook.cert-manager.io",
				FailurePolicy: &fail,
				ClientConfig: admissionv1.WebhookClientConfig{
					Service: &admissionv1.ServiceReference{Namespace: "cert-manager", Name: "webhook"},
				},
			}},
		},
		&admissionv1.ValidatingWebhookConfiguration{
			ObjectMeta: clusterMeta("gatekeeper"),
			Webhooks: []admissionv1.ValidatingWebhook{{
				Name:          "validation.gatekeeper.sh",
				FailurePolicy: &fail,
				ClientConfig: admissionv1.WebhookClientConfig{
					Service: &admissionv1.ServiceReference{Namespace: "gatekeeper", Name: "webhook"},
				},
			}},
		},
		&storagev1.StorageClass{
			ObjectMeta: withAnnotations(clusterMeta("gp3"), map[string]string{
				"storageclass.kubernetes.io/is-default-class": "true",
			}),
			Provisioner:       "ebs.csi.aws.com",
			ReclaimPolicy:     &reclaim,
			VolumeBindingMode: &binding,
		},
		&rbacv1.ClusterRole{
			ObjectMeta: clusterMeta("cluster-admin"),
			Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}},
			},
		},
		&rbacv1.Role{
			ObjectMeta: meta("reader"),
			Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list"}},
			},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: clusterMeta("admins"),
			RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: "cluster-admin"},
			Subjects: []rbacv1.Subject{
				{Kind: rbacv1.GroupKind, Name: "system:masters"},
			},
		},
		&rbacv1.RoleBinding{
			ObjectMeta: meta("readers"),
			RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: "view"},
			Subjects: []rbacv1.Subject{
				{Kind: rbacv1.ServiceAccountKind, Namespace: "default", Name: "deployer"},
			},
		},
	}
}

func withLabels(m metav1.ObjectMeta, labels map[string]string) metav1.ObjectMeta {
	m.Labels = labels
	return m
}

func withAnnotations(m metav1.ObjectMeta, annotations map[string]string) metav1.ObjectMeta {
	m.Annotations = annotations
	return m
}

// OrderBy is how a table built somewhere other than List — a custom resource
// kind, whose columns are a CRD's own printer columns — gets the order a
// heading was clicked for.
//
// It is the same function List sorts with, and this test exists to keep it so.
// A second implementation living beside the list page is how a click on a
// heading comes to mean one thing on pods and another on certificates: the
// missing value sorted as a zero, the tie broken differently, the arrow drawn
// on a column that was never applied.
func TestOrderByGivesAForeignTableTheSameRulesAsAListedOne(t *testing.T) {
	build := func() Table {
		return Table{
			Kind:  "certificates.cert-manager.io",
			Title: "Certificates",
			Columns: []Column{
				{Title: "Name"}, {Title: "Ready"}, {Title: "Age", Numeric: true},
			},
			Rows: []Row{
				{Name: "alpha", Cells: []Cell{{Text: "alpha"}, {Text: "True"}, {Text: "3d", Value: 259200}}},
				{Name: "beta", Cells: []Cell{{Text: "beta"}, {Text: "False"}, {Text: Missing}}},
				{Name: "gamma", Cells: []Cell{{Text: "gamma"}, {Text: "True"}, {Text: "1h", Value: 3600}}},
			},
		}
	}

	order := func(table Table) string {
		got := make([]string, 0, len(table.Rows))
		for _, row := range table.Rows {
			got = append(got, row.Name)
		}
		return strings.Join(got, ",")
	}

	ascending := build()
	ascending.OrderBy("Age", false)
	if got, want := order(ascending), "gamma,alpha,beta"; got != want {
		t.Errorf("ascending = %s, want %s — and the unmeasured row last", got, want)
	}
	if ascending.Sort != "Age" || ascending.Descending {
		t.Errorf("the table reported %q/%v rather than the order it applied", ascending.Sort, ascending.Descending)
	}

	descending := build()
	descending.OrderBy("age", true)
	if got, want := order(descending), "alpha,gamma,beta"; got != want {
		// beta stays last: an em dash is the absence of a value, not the
		// smallest one, and it ends the list whichever way the column points.
		t.Errorf("descending = %s, want %s", got, want)
	}
	if descending.Sort != "Age" {
		t.Errorf("Sort = %q, want the column as it is declared, so the heading matches it", descending.Sort)
	}

	// A column this kind does not have: a stale bookmark from another page. It
	// must claim no order at all, or the page draws an arrow on nothing.
	unknown := build()
	unknown.OrderBy("Instance type", true)
	if unknown.Sort != "" || unknown.Descending {
		t.Errorf("an unknown column was reported as applied: %q/%v", unknown.Sort, unknown.Descending)
	}
	if got := order(unknown); got != "alpha,beta,gamma" {
		t.Errorf("an unknown column reordered the rows: %s", got)
	}
}

// The taints column.
//
// A role is stable, one word, and usually identical across a pool — a column of
// "worker" twelve times. A taint is why a node that reads Ready is running
// nothing, and nothing else on the row admits to it.
func TestTheTaintCellCountsAndExplainsItself(t *testing.T) {
	none := nodeTaints(&corev1.Node{})
	if none.Text != Missing {
		t.Errorf("a node with no taints reads %q; an em dash and not a zero, because a column "+
			"of zeroes reads as measurements and invites comparison", none.Text)
	}
	if none.Class != "" {
		t.Errorf("an untainted node is graded %q; it makes no claim", none.Class)
	}

	policy := nodeTaints(&corev1.Node{Spec: corev1.NodeSpec{Taints: []corev1.Taint{
		{Key: "node-role.kubernetes.io/control-plane", Effect: corev1.TaintEffectNoSchedule},
		{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
	}}})
	if policy.Text != "2" {
		t.Errorf("two taints read %q", policy.Text)
	}
	if policy.Value != 2 {
		t.Errorf("the column sorts by %v, not by the count", policy.Value)
	}
	if policy.Class == ClassBad {
		t.Error("a control-plane node is supposed to carry its own taint. Grading it bad puts a " +
			"warning on every control-plane node in the estate, and a warning that is always on " +
			"is one nobody reads")
	}
	if want := "node-role.kubernetes.io/control-plane:NoSchedule\ndedicated=gpu:NoSchedule"; policy.Hint != want {
		t.Errorf("the hover reads %q, want %q — the count says whether to look and the hover "+
			"says what it is", policy.Hint, want)
	}

	// The node controller's own namespace: unreachable, or out of memory, disk or
	// PIDs. A fault wearing the same clothes as a policy.
	fault := nodeTaints(&corev1.Node{Spec: corev1.NodeSpec{Taints: []corev1.Taint{
		{Key: "node.kubernetes.io/unreachable", Effect: corev1.TaintEffectNoExecute},
	}}})
	if fault.Class != ClassBad {
		t.Errorf("a taint the node controller added because the node cannot be reached is graded "+
			"%q. It is a symptom, and it is the one case this column exists to surface", fault.Class)
	}
	if fault.Rank != RankBad {
		t.Errorf("an unreachable node ranks %d, so it does not sort to the top of the table", fault.Rank)
	}
}

// A cordon is a decision, and the taint it brings is not a fault.
//
// The node lifecycle controller adds `node.kubernetes.io/unschedulable` to every
// cordoned node. It lives in the controller's namespace, beside unreachable and
// memory-pressure, so grading the namespace as a whole would paint a healthy,
// cordoned node's Taints cell red and sort the row among the NotReady nodes —
// while the Status cell beside it says amber "Ready,SchedulingDisabled".
func TestACordonedHealthyNodeRanksAsAWarningNotAFault(t *testing.T) {
	cordoned := &corev1.Node{
		Spec: corev1.NodeSpec{
			Unschedulable: true,
			Taints: []corev1.Taint{
				{Key: corev1.TaintNodeUnschedulable, Effect: corev1.TaintEffectNoSchedule},
			},
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}

	if cell := nodeTaints(cordoned); cell.Class == ClassBad {
		t.Errorf("the cordon's own taint is graded %q. Status already says the node is cordoned, "+
			"in amber; a red taint beside it says the node is broken when it is not", cell.Class)
	}
	if got := worstRank(nodeRow(cordoned, time.Now(), nil)); got != RankWarn {
		t.Errorf("a Ready, cordoned node ranks %d, want RankWarn (%d): it must not sort with "+
			"the NotReady nodes", got, RankWarn)
	}

	// The cordon does not soften anything else. A cordoned node the controller
	// also could not reach is still a fault.
	unreachable := cordoned.DeepCopy()
	unreachable.Spec.Taints = append(unreachable.Spec.Taints,
		corev1.Taint{Key: corev1.TaintNodeUnreachable, Effect: corev1.TaintEffectNoExecute})
	if got := nodeTaints(unreachable); got.Rank != RankBad {
		t.Errorf("a cordoned node that is also unreachable ranks %d in its taints; the cordon "+
			"must not hide the fault beside it", got.Rank)
	}
}

// An event that happened cannot have happened zero times.
//
// Every cluster since 1.19 writes events through `events.k8s.io/v1`. The core/v1
// view keeps `count` for compatibility and leaves it at zero, putting the number
// in `series.count` — so reading the bare field prints 0 beside warnings that
// have just fired, while the drawer beside it reads 1.
func TestAnEventNeverReportsThatItHappenedZeroTimes(t *testing.T) {
	// The shape EKS actually returns: count unset, series carrying the number.
	series := &corev1.Event{Series: &corev1.EventSeries{Count: 47}}
	if got := EventCount(series); got != 47 {
		t.Errorf("an event that happened 47 times reports %d. `count` is empty on every cluster "+
			"that writes through events.k8s.io/v1, which is every cluster since 1.19", got)
	}

	// The older shape still works.
	if got := EventCount(&corev1.Event{Count: 3}); got != 3 {
		t.Errorf("the core/v1 count is no longer read: %d", got)
	}

	// And the floor. An event exists because something happened.
	if got := EventCount(&corev1.Event{}); got != 1 {
		t.Errorf("an event with no count reports %d times. Zero times is a thing that did not "+
			"happen, printed against a warning the operator is reading because it did", got)
	}
}

// --- refresh intervals ----------------------------------------------------------

// A table re-read every two seconds whatever it shows would carry every
// secret's plaintext through this process thirty times a minute to render a
// column saying "4".
//
// The remedy is the multiplier and not the request — the interval is a
// property of the kind — so these are the tests of a *judgment*, and they are written the
// way the judgment was made: fast for what changes fast, slow for what a human
// last applied. A change here should be a change of mind about how volatile a
// kind is, which is why the expectations are spelled out kind by kind rather than
// derived from the bands. Derived from the bands, this test would agree with any
// retuning of them, including a retuning that put Secrets at two seconds.
func TestEachKindIsRereadAtTheRateItChanges(t *testing.T) {
	for _, c := range []struct {
		kind string
		want time.Duration
		why  string
	}{
		// Live: watched while it moves.
		{"pods", 2 * time.Second, "a pod's phase and restart count change between one blink and the next"},
		{"nodes", 2 * time.Second, "a node going NotReady is the thing this table is open for"},

		// Reconciling: a controller writes it after somebody acts.
		{"deployments", 5 * time.Second, "a rollout must visibly move"},
		{"statefulsets", 5 * time.Second, "a rollout must visibly move"},
		{"daemonsets", 5 * time.Second, "a rollout must visibly move"},
		{"replicasets", 5 * time.Second, "the counts move during a rollout"},
		{"jobs", 5 * time.Second, "completions arrive while the operator waits"},
		{"cronjobs", 5 * time.Second, "Active goes to 1 and back"},
		{"replicationcontrollers", 5 * time.Second, "the counts move during a scale"},
		{"services", 5 * time.Second, "External is written by a cloud controller, not by the manifest"},
		{"ingresses", 5 * time.Second, "Address is written by the controller"},
		{"endpoints", 5 * time.Second, "the ready count follows the pods"},
		{"endpointslices", 5 * time.Second, "the ready count follows the pods"},
		{"resourcequotas", 5 * time.Second, "Used moves with every create in the namespace"},
		{"horizontalpodautoscalers", 5 * time.Second, "the replica count moves during a scale event"},
		{"poddisruptionbudgets", 5 * time.Second, "Healthy and Allowed follow the pods"},
		{"leases", 5 * time.Second, "the default lease duration is fifteen seconds"},
		{"persistentvolumeclaims", 5 * time.Second, "Pending to Bound is what this table is watched for"},
		{"persistentvolumes", 5 * time.Second, "Status and Claim are written when a claim binds"},
		{"namespaces", 5 * time.Second, "a namespace stuck in Terminating is a real problem"},
		{"events", 5 * time.Second, "read during an incident, but a record of what already happened"},

		// Declared: what a human last applied.
		{"configmaps", 60 * time.Second, "2.84 MB a refresh to count keys"},
		{"secrets", 60 * time.Second, "3.09 MB of plaintext a refresh to count keys"},
		{"limitranges", 60 * time.Second, "a limit range has no status"},
		{"networkpolicies", 60 * time.Second, "a network policy has no status"},
		{"ingressclasses", 60 * time.Second, "installed with the controller and then left alone"},
		{"priorityclasses", 60 * time.Second, "installed once"},
		{"runtimeclasses", 60 * time.Second, "installed once"},
		{"mutatingwebhookconfigurations", 60 * time.Second, "written by a chart, not by a controller"},
		{"validatingwebhookconfigurations", 60 * time.Second, "written by a chart, not by a controller"},
		{"serviceaccounts", 60 * time.Second, "RBAC changes when somebody deploys"},
		{"roles", 60 * time.Second, "RBAC changes when somebody deploys"},
		{"clusterroles", 60 * time.Second, "RBAC changes when somebody deploys"},
		{"rolebindings", 60 * time.Second, "RBAC changes when somebody deploys"},
		{"clusterrolebindings", 60 * time.Second, "RBAC changes when somebody deploys"},
		{"storageclasses", 60 * time.Second, "installed with the CSI driver"},
	} {
		t.Run(c.kind, func(t *testing.T) {
			d, ok := Lookup(c.kind)
			if !ok {
				t.Fatalf("there is no %s descriptor", c.kind)
			}
			if got := d.Refresh(); got != c.want {
				t.Errorf("%s is re-read every %s, want every %s — %s",
					c.kind, got, c.want, c.why)
			}
		})
	}
}

// Every kind is named above, so a kind added tomorrow fails this rather than
// slipping in unjudged.
//
// Not pedantry: the whole value of a per-kind interval is that somebody thought
// about the kind, and an unlisted row would inherit two seconds silently — which
// is exactly the waste per-kind intervals exist to avoid.
func TestEveryKindsIntervalHasBeenThoughtAbout(t *testing.T) {
	named := map[string]bool{}
	for _, kind := range []string{
		"pods", "nodes",
		"deployments", "statefulsets", "daemonsets", "replicasets", "jobs", "cronjobs",
		"replicationcontrollers", "services", "ingresses", "endpoints", "endpointslices",
		"resourcequotas", "horizontalpodautoscalers", "poddisruptionbudgets", "leases",
		"persistentvolumeclaims", "persistentvolumes", "namespaces", "events",
		"configmaps", "secrets", "limitranges", "networkpolicies", "ingressclasses",
		"priorityclasses", "runtimeclasses", "mutatingwebhookconfigurations",
		"validatingwebhookconfigurations", "serviceaccounts", "roles", "clusterroles",
		"rolebindings", "clusterrolebindings", "storageclasses",
	} {
		named[kind] = true
	}
	for _, d := range Kinds() {
		if !named[d.Kind] {
			t.Errorf("%s has no case in TestEachKindIsRereadAtTheRateItChanges. Add one, "+
				"with the reason: an unlisted kind takes the two-second default, which is "+
				"the waste per-kind intervals exist to avoid", d.Kind)
		}
	}
}

// The default, stated on its own: a descriptor that declares no interval is
// re-read every two seconds, the safe answer for a kind nobody has thought
// about.
func TestAKindThatDeclaresNoIntervalGetsTwoSeconds(t *testing.T) {
	if DefaultRefresh != 2*time.Second {
		t.Fatalf("DefaultRefresh is %s. Two seconds is the live kinds' interval; "+
			"changing it changes every kind that declares nothing, which is the "+
			"opposite of what a default is for", DefaultRefresh)
	}
	// A zero value, which is what a new row with no opinion looks like.
	if got := (Descriptor{}).Refresh(); got != DefaultRefresh {
		t.Errorf("a descriptor declaring no interval is re-read every %s, want %s", got, DefaultRefresh)
	}
	// And a negative one, which is what a typo looks like. Not "never refresh": a
	// table that has silently stopped asking is the failure this mechanism must
	// not be able to produce.
	if got := (Descriptor{refresh: -time.Second}).Refresh(); got != DefaultRefresh {
		t.Errorf("a negative interval is re-read every %s, want the default %s", got, DefaultRefresh)
	}
}

// Every interval, whether or not a case above names it, has to be something a
// console can render and a person would accept.
//
// The ceiling is the point. A kind put in the wrong band is a judgment call; a
// kind given ten minutes is a table that is wrong for ten minutes with nothing on
// the page saying so. The whole-second rule is
// what lets internal/web format the interval as `every Ns` without losing part of
// it.
func TestNoKindIsLeftStaleOrAskedForFasterThanTheTableCanBeRead(t *testing.T) {
	for _, d := range Kinds() {
		got := d.Refresh()
		switch {
		case got < time.Second:
			t.Errorf("%s is re-read every %s, which is faster than a cluster-wide list "+
				"of anything comes back", d.Kind, got)
		case got > time.Minute:
			t.Errorf("%s is re-read every %s. A console showing a table more than a minute "+
				"out of date is not slow, it is wrong — and there is nothing on the page "+
				"saying so", d.Kind, got)
		case got%time.Second != 0:
			t.Errorf("%s is re-read every %s, which is not a whole number of seconds; "+
				"internal/web renders the interval as `every Ns` and would round it",
				d.Kind, got)
		}
	}
}

// Two properties: the intervals are not all the same, and Secrets and
// ConfigMaps are read less often than the kind the two seconds were chosen for.
//
// Written as a comparison rather than as two more numbers so that it keeps meaning
// something if the bands are ever retuned: whatever the numbers become, a Secret
// must not be re-read as often as a Pod.
func TestSecretsAndConfigMapsAreNotReadAtAPodsRate(t *testing.T) {
	pods, ok := Lookup("pods")
	if !ok {
		t.Fatal("there is no pods descriptor")
	}
	for _, kind := range []string{"secrets", "configmaps"} {
		d, ok := Lookup(kind)
		if !ok {
			t.Fatalf("there is no %s descriptor", kind)
		}
		if d.Refresh() <= pods.Refresh() {
			t.Errorf("%s is re-read every %s and pods every %s. A %s list must not "+
				"cross this process at a pod list's rate to render a key count; that is "+
				"the whole point of a per-kind interval",
				kind, d.Refresh(), pods.Refresh(), kind)
		}
	}

	// And the set is not uniform, which is the mutation this half catches: a
	// Refresh that always answered the default would satisfy every "at least two
	// seconds" assertion above.
	seen := map[time.Duration]bool{}
	for _, d := range Kinds() {
		seen[d.Refresh()] = true
	}
	if len(seen) < 3 {
		t.Errorf("the descriptors resolve to %d distinct intervals. There are three bands "+
			"in kinds.go; fewer than three means something is collapsing them", len(seen))
	}
}

// An alias must resolve to the same interval as the name it stands for.
//
// Lookup accepts `secret` and `cm`, and it is the only way a kind reaches a
// descriptor — a URL and this test both go through it. An alias that
// missed the interval would give one page two different refresh rates depending
// on how the operator spelled the kind in the address bar.
func TestAnAliasIsReadAtTheSameRateAsTheKindItNames(t *testing.T) {
	for alias, canonical := range aliases {
		short, ok := Lookup(alias)
		if !ok {
			t.Fatalf("Lookup(%q) found nothing", alias)
		}
		full, ok := Lookup(canonical)
		if !ok {
			t.Fatalf("Lookup(%q) found nothing", canonical)
		}
		if short.Refresh() != full.Refresh() {
			t.Errorf("%q is re-read every %s and %q every %s",
				alias, short.Refresh(), canonical, full.Refresh())
		}
	}
}
