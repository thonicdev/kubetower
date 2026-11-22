package overview

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// The trouble panel says since when.
//
// *What* is a sentence about the present (`2 of 3 replicas ready`), and how
// long it has been so is the first thing a reader needs to tell a Deployment
// mid-rollout from one that has been broken since Tuesday. The neighboring
// panel dates its rows too.
//
// The source is the object's own record of the transition, per kind, and it is
// the record and never the collection instant: a row whose kind has no such
// record is left undated, because "since now" is the one reading that is
// always false.

func unavailableDeployment(name string, ready, want int32, since time.Time) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(want)},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas: ready,
			Conditions: []appsv1.DeploymentCondition{{
				Type:               appsv1.DeploymentAvailable,
				Status:             corev1.ConditionFalse,
				Reason:             "MinimumReplicasUnavailable",
				LastTransitionTime: metav1.NewTime(since),
			}},
		},
	}
}

func troubleNamed(t *testing.T, rows []Trouble, kind, name string) Trouble {
	t.Helper()
	for _, row := range rows {
		if row.Kind == kind && row.Name == name {
			return row
		}
	}
	t.Fatalf("no %s %q in %+v", kind, name, rows)
	return Trouble{}
}

// A Deployment made unavailable is dated by the condition that says so.
func TestAnUnavailableDeploymentIsDatedByItsAvailableCondition(t *testing.T) {
	since := clock.Add(-3 * 24 * time.Hour)
	rows := deploymentTrouble([]appsv1.Deployment{*unavailableDeployment("api", 0, 3, since)}, clock)

	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if !rows[0].Since.Equal(since) {
		t.Errorf("Since = %v, want the Available condition's lastTransitionTime %v", rows[0].Since, since)
	}
	if rows[0].Age != "3d" {
		t.Errorf("Age = %q, want %q — the server's wording, aged from the collection instant", rows[0].Age, "3d")
	}
}

// A Deployment past its progress deadline is dated by that, when Available has
// nothing to say: a rollout that has been stuck for an hour on a Deployment that
// is still serving from its old pods is `Available=True`, and the deadline is the
// only record of how long the new ones have failed to come up.
func TestAStuckRolloutIsDatedByItsProgressDeadline(t *testing.T) {
	since := clock.Add(-47 * time.Minute)
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(3)},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas: 2,
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(clock.Add(-30 * 24 * time.Hour))},
				{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse,
					Reason: "ProgressDeadlineExceeded", LastTransitionTime: metav1.NewTime(since)},
			},
		},
	}
	rows := deploymentTrouble([]appsv1.Deployment{*d}, clock)
	if len(rows) != 1 || !rows[0].Since.Equal(since) || rows[0].Age != "47m" {
		t.Errorf("rows = %+v, want one row dated %v as 47m", rows, since)
	}
}

// A Deployment short of a replica but still Available — an ordinary rolling
// update inside its maxUnavailable — has no record of leaving its intended
// state, because it has not left it. It is listed, and it is not dated: dating
// it from the Available condition's *True* transition would say the rollout has
// been failing since the day the Deployment was created.
func TestADeploymentMidRolloutIsListedAndNotDated(t *testing.T) {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(3)},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas: 2,
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(clock.Add(-30 * 24 * time.Hour))},
				{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue,
					Reason: "ReplicaSetUpdated", LastTransitionTime: metav1.NewTime(clock.Add(-time.Minute))},
			},
		},
	}
	rows := deploymentTrouble([]appsv1.Deployment{*d}, clock)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want the shortfall listed", len(rows))
	}
	if !rows[0].Since.IsZero() || rows[0].Age != "" {
		t.Errorf("Since = %v, Age = %q; a Deployment that is still Available has no date to give",
			rows[0].Since, rows[0].Age)
	}
}

// A StatefulSet's shortfall has no timestamp of its own — the
// observedGeneration/updatedReplicas gap is a pair of integers — so it is dated
// from its newest non-ready pod's startTime, and by the newest so that the
// panel never claims the trouble is older than the most recent thing it can
// point at. A pod of some other owner that happens to share the namespace is
// not evidence about this StatefulSet.
func TestAStatefulSetIsDatedByItsNewestNonReadyPod(t *testing.T) {
	owned := func(name string, ready bool, started time.Time) corev1.Pod {
		p := pod(name, "worker-1", corev1.PodRunning, corev1.Container{Name: "db"})
		p.OwnerReferences = []metav1.OwnerReference{{
			Kind: "StatefulSet", Name: "postgres", Controller: func() *bool { b := true; return &b }(),
		}}
		p.Status.StartTime = &metav1.Time{Time: started}
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "db", Ready: ready}}
		return *p
	}
	stranger := pod("redis-0", "worker-1", corev1.PodRunning, corev1.Container{Name: "c"})
	stranger.Status.StartTime = &metav1.Time{Time: clock.Add(-time.Minute)}
	stranger.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "c", Ready: false}}

	newest := clock.Add(-2 * time.Hour)
	pods := []corev1.Pod{
		owned("postgres-0", true, clock.Add(-40*24*time.Hour)),
		owned("postgres-1", false, clock.Add(-5*24*time.Hour)),
		owned("postgres-2", false, newest),
		*stranger,
	}
	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "postgres", Namespace: "default"},
		Spec:       appsv1.StatefulSetSpec{Replicas: i32(3)},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1},
	}
	rows := statefulSetTrouble([]appsv1.StatefulSet{sts}, pods, clock)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if !rows[0].Since.Equal(newest) || rows[0].Age != "2h" {
		t.Errorf("Since = %v (%q), want the newest non-ready pod's startTime %v as 2h",
			rows[0].Since, rows[0].Age, newest)
	}
}

// A failed Job is dated by its failure, not by its start.
//
// `status.startTime` would date a Job that ran for two minutes and failed two
// hours ago as a day-old trouble, because it started yesterday. The failure is the
// Failed condition's transition, or FailureTarget's while the controller is
// still terminating the pods; a Job whose failure carries no time is undated,
// not dated from its start.
func TestAFailedJobIsDatedByItsFailure(t *testing.T) {
	started := clock.Add(-26 * time.Hour)
	failed := clock.Add(-2 * time.Hour)
	job := func(name string, conditions ...batchv1.JobCondition) batchv1.Job {
		return batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Status: batchv1.JobStatus{
				Failed:     3,
				StartTime:  &metav1.Time{Time: started},
				Conditions: conditions,
			},
		}
	}
	rows := jobTrouble([]batchv1.Job{
		job("backup", batchv1.JobCondition{
			Type: batchv1.JobFailed, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(failed),
		}),
		job("undated", batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}),
	}, clock)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	if !rows[0].Since.Equal(failed) || rows[0].Age != "2h" {
		t.Errorf("Since = %v (%q), want the Failed condition's transition %v as 2h, not the "+
			"start %v", rows[0].Since, rows[0].Age, failed, started)
	}
	if !rows[1].Since.IsZero() || rows[1].Age != "" {
		t.Errorf("a failure with no transition time is dated %v (%q); it must be undated, not "+
			"dated from the Job's start", rows[1].Since, rows[1].Age)
	}
}

// Through the whole page: a kind with no source is listed undated, and rows of
// one tone come oldest first.
//
// The Pod is the kind with no source here — the entry names none for it — and
// its row must arrive with the zero time and an empty age, which the contract
// omits and the client draws as an em dash. **Never as the collection instant**:
// a pod that has been crash-looping since Tuesday drawn as "0s" is the same lie
// the restarting panel's `lastTermination` refuses to tell.
func TestTheTroublePanelDatesWhatItCanAndSortsTheOldestFirst(t *testing.T) {
	crashing := pod("crasher", "worker-1", corev1.PodRunning, corev1.Container{Name: "c"})
	crashing.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "c",
		Ready: false,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
	}}

	// Against the real clock, not the fixed one: Workloads collects at
	// time.Now(), and the point of the last assertion is that every age on the
	// page is measured from that one instant.
	now := time.Now()
	out, err := Workloads(context.Background(), accesstest.Allow(), client(
		crashing,
		unavailableDeployment("recent", 0, 3, now.Add(-10*time.Minute)),
		unavailableDeployment("tuesday", 0, 3, now.Add(-4*24*time.Hour)),
	), "", nil)
	if err != nil {
		t.Fatalf("Workloads: %v", err)
	}

	podRow := troubleNamed(t, out.Trouble, "Pod", "crasher")
	if !podRow.Since.IsZero() || podRow.Age != "" {
		t.Errorf("a Pod row is dated %v (%q); the entry names no source for this kind, "+
			"and an undated row must not be drawn as now", podRow.Since, podRow.Age)
	}

	// All three rows are bad. Within one tone the dated rows come oldest first
	// and the undated one after them: an undated row is not "zero seconds old".
	var order []string
	for _, row := range out.Trouble {
		order = append(order, row.Name)
	}
	want := []string{"tuesday", "recent", "crasher"}
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Fatalf("order = %v, want %v — the oldest trouble at the top", order, want)
		}
	}
	if got := troubleNamed(t, out.Trouble, "Deployment", "tuesday").Age; got != "4d" {
		t.Errorf("age = %q, want 4d measured from the page's own collection instant", got)
	}
}
