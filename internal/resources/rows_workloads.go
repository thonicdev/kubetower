package resources

import (
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/schedule"
)

// --- workloads -----------------------------------------------------------------

func podRow(pod *corev1.Pod, now time.Time, usage *Usage) []Cell {
	status := PodStatus(pod)
	class, rank := GradePodStatus(status)

	restartCount := PodRestarts(pod)

	return []Cell{
		text(pod.Name),
		text(pod.Namespace),
		podReady(pod),
		graded(status, class, rank),
		restarts(restartCount),
		// Ungraded: high usage is a problem only against a limit this table
		// does not show.
		cpuCell(usage),
		memoryCell(usage),
		containersCell(pod),
		controlledBy(pod.OwnerReferences),
		text(pod.Spec.NodeName),
		// Ungraded: BestEffort is evicted first, but it is also a deliberate
		// choice for many DaemonSets.
		text(string(pod.Status.QOSClass)),
		age(pod.CreationTimestamp, now),
	}
}

// The shapes a container can be in. They differ in shape, not only color, so
// they read without telling red from green; the ready count is printed beside
// them too.
const (
	glyphReady    = "●" // running, and its readiness probe passes
	glyphDone     = "✓" // terminated on its own, exit 0
	glyphFailed   = "✕" // terminated non-zero, or waiting on something that failed
	glyphStarting = "○" // not started yet, or started and not ready yet
)

// containersCap is how many glyphs are drawn before the rest become "+n"; the
// counts beside them still state the whole.
const containersCap = 8

// containersCell is one glyph per container, in spec order, and the ready
// count. It says what Ready cannot: which container is down, and whether it
// failed or has not started.
//
// Class but no rank, as podReady: the Status cell decides where the row sorts.
func containersCell(pod *corev1.Pod) Cell {
	// The regular containers, preceded by native sidecars (their start order;
	// they run as long as the pod). Ordinary init containers have finished by
	// the time the pod is up and stay out. This must agree with the Ready
	// column, which counts sidecars the same way (IsNativeSidecar).
	run := make([]corev1.Container, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	for i := range pod.Spec.InitContainers {
		if IsNativeSidecar(&pod.Spec.InitContainers[i]) {
			run = append(run, pod.Spec.InitContainers[i])
		}
	}
	run = append(run, pod.Spec.Containers...)

	total := len(run)
	if total == 0 {
		// No containers: no glyphs describe it, and an em dash sorts last.
		return Cell{Text: Missing}
	}

	// One map for both status lists: container names are unique across init
	// and regular containers.
	reported := make(map[string]*corev1.ContainerStatus, len(pod.Status.ContainerStatuses)+len(pod.Status.InitContainerStatuses))
	for i := range pod.Status.InitContainerStatuses {
		status := &pod.Status.InitContainerStatuses[i]
		reported[status.Name] = status
	}
	for i := range pod.Status.ContainerStatuses {
		status := &pod.Status.ContainerStatuses[i]
		reported[status.Name] = status
	}

	// Spec order, not the kubelet's, so glyphs keep their place across
	// refreshes.
	var glyphs strings.Builder
	var ready, failed, starting int
	for i, c := range run {
		glyph := containerGlyph(reported[c.Name])
		switch glyph {
		case glyphReady:
			ready++
		case glyphFailed:
			failed++
		case glyphStarting:
			starting++
		}
		if i < containersCap {
			glyphs.WriteString(glyph)
		}
	}
	if total > containersCap {
		fmt.Fprintf(&glyphs, "+%d", total-containersCap)
	}

	cell := Cell{
		Text:  fmt.Sprintf("%s %d/%d", glyphs.String(), ready, total),
		Value: completeness(int32(ready), int32(total)),
	}
	switch {
	case failed > 0:
		cell.Class = ClassBad
	case starting > 0:
		cell.Class = ClassWarn
	default:
		// Every container is ready or finished cleanly, as in a completed Job pod.
		cell.Class = ClassOK
	}
	return cell
}

// containerGlyph grades one container with the same grader as the pod's
// Status cell, so a reason that makes Status red makes this glyph a cross.
func containerGlyph(status *corev1.ContainerStatus) string {
	switch {
	case status == nil:
		// Scheduled, and the kubelet has not reported on this container yet.
		return glyphStarting
	case status.State.Terminated != nil:
		if status.State.Terminated.ExitCode == 0 {
			return glyphDone
		}
		return glyphFailed
	case status.State.Waiting != nil:
		if class, _ := GradePodStatus(status.State.Waiting.Reason); class == ClassBad {
			return glyphFailed
		}
		return glyphStarting
	case status.Ready:
		return glyphReady
	}
	// Running, readiness probe not passed yet: not a failure.
	return glyphStarting
}

// controlledBy names what made this object: `ReplicaSet/web-5bfcd6f9b9`. An em
// dash is information: nothing will recreate the object.
//
// The controlling reference, not the first: of several owners, only the
// controller reconciles the object.
func controlledBy(refs []metav1.OwnerReference) Cell {
	chosen := -1
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			chosen = i
			break
		}
		if chosen < 0 {
			chosen = i
		}
	}
	if chosen < 0 {
		return text("")
	}
	owner := refs[chosen]
	if owner.Kind == "" {
		return text(owner.Name)
	}
	return text(owner.Kind + "/" + owner.Name)
}

// IsNativeSidecar reports whether an init container is a sidecar: an init
// container carrying `restartPolicy: Always`. Exported so every reading of a
// pod (overview's requests, PodStatus, PodReady) agrees.
//
// Such a container starts before the regular ones, the kubelet moves on once
// it reports `started`, and it runs for the pod's whole life: any reading that
// waits for it to terminate waits forever.
//
// A nil container is not a sidecar: a status the spec does not list must not
// panic.
func IsNativeSidecar(c *corev1.Container) bool {
	return c != nil && c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways
}

// nativeSidecars is the set of a pod's sidecar names, for looking a status up.
// By name rather than index, as kubectl does, so a status the spec no longer
// has cannot be matched to the wrong container.
func nativeSidecars(pod *corev1.Pod) map[string]bool {
	var out map[string]bool
	for i := range pod.Spec.InitContainers {
		if !IsNativeSidecar(&pod.Spec.InitContainers[i]) {
			continue
		}
		if out == nil {
			out = make(map[string]bool, len(pod.Spec.InitContainers))
		}
		out[pod.Spec.InitContainers[i].Name] = true
	}
	return out
}

// PodStatus is what a pod is actually doing, as kubectl prints it.
//
// The phase is what the pod is meant to be doing; a container's waiting reason
// is what it is doing (CrashLoopBackOff has phase Running). So a waiting
// reason wins over the phase, an unfinished init container wins over both, and
// a deletion in progress wins over everything.
//
// Exported so every page reads a pod the same way; how to color it is
// GradePodStatus's.
func PodStatus(pod *corev1.Pod) string {
	if pod.DeletionTimestamp != nil {
		return "Terminating"
	}

	// Init containers first: one stuck on a missing image leaves the pod at
	// Pending, which names nothing. The cases follow kubectl's printPod. A
	// native sidecar never terminates, so for it "finished" means started;
	// otherwise every meshed pod would read `Init:0/1` forever.
	sidecars := nativeSidecars(pod)
	var done int
	for _, cs := range pod.Status.InitContainerStatuses {
		if t := cs.State.Terminated; t != nil && t.ExitCode == 0 {
			done++
			continue
		}
		// Started, not Ready: the kubelet proceeds on `started`.
		if sidecars[cs.Name] && cs.Started != nil && *cs.Started {
			done++
			continue
		}
		if t := cs.State.Terminated; t != nil {
			if t.Reason != "" {
				return "Init:" + t.Reason
			}
			// No reason reported: name the signal or exit code, as kubectl does,
			// rather than counting a failed init container as unfinished.
			if t.Signal != 0 {
				return fmt.Sprintf("Init:Signal:%d", t.Signal)
			}
			return fmt.Sprintf("Init:ExitCode:%d", t.ExitCode)
		}
		// PodInitializing is every unstarted init container's normal state.
		if w := cs.State.Waiting; w != nil && w.Reason != "" && w.Reason != "PodInitializing" {
			return "Init:" + w.Reason
		}
	}
	if total := len(pod.Status.InitContainerStatuses); done < total {
		// How far through the init sequence it is.
		return fmt.Sprintf("Init:%d/%d", done, total)
	}

	for _, cs := range pod.Status.ContainerStatuses {
		// PodInitializing on a main container repeats the init containers.
		if w := cs.State.Waiting; w != nil && w.Reason != "" && w.Reason != "PodInitializing" {
			return w.Reason
		}
	}
	for _, cs := range pod.Status.ContainerStatuses {
		// A container that died (OOMKilled above all) before the pod is failed.
		if t := cs.State.Terminated; t != nil && t.Reason != "" && t.Reason != "Completed" {
			return t.Reason
		}
	}

	// Evicted and NodeLost live here; the phase only says "Failed".
	if pod.Status.Reason != "" {
		return pod.Status.Reason
	}
	return string(pod.Status.Phase)
}

// GradePodStatus turns what PodStatus said into a color and a sort rank.
// Separate from PodStatus because not every caller paints the same way.
func GradePodStatus(status string) (string, int) {
	switch status {
	case string(corev1.PodRunning), string(corev1.PodSucceeded), "Completed":
		return ClassOK, RankOK
	}
	switch {
	case status == string(corev1.PodFailed),
		strings.Contains(status, "BackOff"),
		strings.Contains(status, "Err"),
		// A container that died with no reason: Init:ExitCode:1,
		// Init:Signal:9.
		strings.Contains(status, "ExitCode"),
		strings.Contains(status, "Signal"),
		strings.Contains(status, "Invalid"),
		strings.Contains(status, "OOM"),
		strings.Contains(status, "Evicted"),
		strings.Contains(status, "Unknown"):
		return ClassBad, RankBad
	}
	// Pending, ContainerCreating, Terminating, Init:0/2: in flight, not an
	// alarm.
	return ClassWarn, RankWarn
}

// PodReady is how many of a pod's containers are ready, and how many there
// are. Native sidecars count in both halves, as kubectl counts them. Exported
// so every reader gets one count.
func PodReady(pod *corev1.Pod) (ready, total int32) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
	}
	total = int32(len(pod.Spec.Containers))

	for i := range pod.Spec.InitContainers {
		if IsNativeSidecar(&pod.Spec.InitContainers[i]) {
			total++
		}
	}
	sidecars := nativeSidecars(pod)
	for _, cs := range pod.Status.InitContainerStatuses {
		if sidecars[cs.Name] && cs.Ready {
			ready++
		}
	}
	return ready, total
}

// PodRestarts is every restart a pod has accumulated, native sidecars
// included: their statuses are under InitContainerStatuses, and kubectl adds
// exactly those and no other init restarts.
func PodRestarts(pod *corev1.Pod) int32 {
	var count int32
	for _, cs := range pod.Status.ContainerStatuses {
		count += cs.RestartCount
	}
	sidecars := nativeSidecars(pod)
	for _, cs := range pod.Status.InitContainerStatuses {
		if sidecars[cs.Name] {
			count += cs.RestartCount
		}
	}
	return count
}

func podReady(pod *corev1.Pod) Cell {
	ready, total := PodReady(pod)

	cell := Cell{Text: fmt.Sprintf("%d/%d", ready, total)}

	// A finished pod is not unready.
	if pod.Status.Phase == corev1.PodSucceeded {
		return cell
	}
	// Class but no rank: the status cell decides where the row sorts.
	if ready < total {
		cell.Class = ClassWarn
	} else {
		cell.Class = ClassOK
	}
	return cell
}

func deploymentRow(d *appsv1.Deployment, now time.Time) []Cell {
	return []Cell{
		text(d.Name),
		text(d.Namespace),
		ratio(d.Status.ReadyReplicas, replicasOf(d.Spec.Replicas)),
		count32(d.Status.UpdatedReplicas),
		count32(d.Status.AvailableReplicas),
		text(imagesOf(d.Spec.Template.Spec.Containers)),
		age(d.CreationTimestamp, now),
	}
}

func statefulSetRow(s *appsv1.StatefulSet, now time.Time) []Cell {
	return []Cell{
		text(s.Name),
		text(s.Namespace),
		ratio(s.Status.ReadyReplicas, replicasOf(s.Spec.Replicas)),
		count32(s.Status.UpdatedReplicas),
		text(imagesOf(s.Spec.Template.Spec.Containers)),
		age(s.CreationTimestamp, now),
	}
}

func daemonSetRow(d *appsv1.DaemonSet, now time.Time) []Cell {
	return []Cell{
		text(d.Name),
		text(d.Namespace),
		ratio(d.Status.NumberReady, d.Status.DesiredNumberScheduled),
		count32(d.Status.UpdatedNumberScheduled),
		count32(d.Status.NumberAvailable),
		text(imagesOf(d.Spec.Template.Spec.Containers)),
		age(d.CreationTimestamp, now),
	}
}

func replicaSetRow(r *appsv1.ReplicaSet, now time.Time) []Cell {
	// A ReplicaSet at zero is a past rollout's remains; ratio grades 0/0 healthy.
	return []Cell{
		text(r.Name),
		text(r.Namespace),
		ratio(r.Status.ReadyReplicas, replicasOf(r.Spec.Replicas)),
		count32(r.Status.Replicas),
		text(imagesOf(r.Spec.Template.Spec.Containers)),
		// Which Deployment this is a revision of; the hash name does not say.
		controlledBy(r.OwnerReferences),
		age(r.CreationTimestamp, now),
	}
}

// replicationControllerRow is replicaSetRow for the kind that predates it,
// listed because one still running is managed by no Deployment. It has no
// controller, so no "Controlled by".
//
// Spec.Template is a pointer on this kind alone, and may be nil on a stored
// object.
func replicationControllerRow(r *corev1.ReplicationController, now time.Time) []Cell {
	images := ""
	if r.Spec.Template != nil {
		images = imagesOf(r.Spec.Template.Spec.Containers)
	}
	return []Cell{
		text(r.Name),
		text(r.Namespace),
		ratio(r.Status.ReadyReplicas, replicasOf(r.Spec.Replicas)),
		count32(r.Status.Replicas),
		text(images),
		age(r.CreationTimestamp, now),
	}
}

func jobRow(j *batchv1.Job, now time.Time) []Cell {
	status, class, rank := JobStatus(j)
	return []Cell{
		text(j.Name),
		text(j.Namespace),
		graded(status, class, rank),
		Cell{
			Text:  fmt.Sprintf("%d/%d", j.Status.Succeeded, replicasOf(j.Spec.Completions)),
			Value: completeness(j.Status.Succeeded, replicasOf(j.Spec.Completions)),
		},
		JobDuration(j, now),
		// The CronJob that scheduled it, if any.
		controlledBy(j.OwnerReferences),
		age(j.CreationTimestamp, now),
	}
}

// JobStatus is what a Job is doing, as one reading, with its class and rank. A
// Job retrying within its backoff limit is neither complete nor failed, and a
// suspended one is not a failure. Exported so the scheduled-jobs page agrees
// with the Jobs list.
func JobStatus(j *batchv1.Job) (string, string, int) {
	for _, c := range j.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return "Complete", ClassOK, RankOK
		case batchv1.JobFailed:
			return "Failed", ClassBad, RankBad
		}
	}
	if j.Spec.Suspend != nil && *j.Spec.Suspend {
		return "Suspended", ClassWarn, RankWarn
	}
	if j.Status.Failed > 0 {
		// Retrying: the backoff limit has not been reached.
		return fmt.Sprintf("Retrying (%d failed)", j.Status.Failed), ClassWarn, RankWarn
	}
	if j.Status.Active > 0 {
		return "Running", ClassNone, RankOK
	}
	return "Pending", ClassWarn, RankWarn
}

// JobDuration is how long a Job ran, or has been running. A stopped Job is
// measured to when it stopped (JobStopped); one with no record of when prints
// an em dash rather than a figure growing to now. Exported so the Job's drawer
// (internal/detail) agrees with the list.
func JobDuration(j *batchv1.Job, now time.Time) Cell {
	if j.Status.StartTime == nil {
		return Cell{Text: Missing}
	}
	end, stopped := JobStopped(j)
	if !stopped {
		end = now
	}
	if end.IsZero() {
		return Cell{Text: Missing, Hint: "stopped, and the Job records no time it stopped"}
	}
	return elapsed(end.Sub(j.Status.StartTime.Time))
}

// JobStopped is when a Job stopped running, and whether it has.
//
// `status.completionTime` is set on success only; a failure is recorded by
// the `Failed` condition's transition, or `FailureTarget`'s, which the
// controller sets before it has finished terminating the pods. A suspended Job
// with no active pods has stopped at its `Suspended` transition; one still
// holding active pods has not.
//
// Stopped with a zero time means the Job records no time; callers print it as
// absent. Exported so the overview dates a failed Job the same way.
func JobStopped(j *batchv1.Job) (time.Time, bool) {
	if j.Status.CompletionTime != nil {
		return j.Status.CompletionTime.Time, true
	}
	var failed, failing, suspended *batchv1.JobCondition
	for i := range j.Status.Conditions {
		c := &j.Status.Conditions[i]
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobFailed:
			failed = c
		case batchv1.JobFailureTarget:
			failing = c
		case batchv1.JobSuspended:
			suspended = c
		}
	}
	switch {
	case failed != nil:
		return failed.LastTransitionTime.Time, true
	case failing != nil:
		return failing.LastTransitionTime.Time, true
	case j.Spec.Suspend != nil && *j.Spec.Suspend && j.Status.Active == 0:
		if suspended != nil {
			return suspended.LastTransitionTime.Time, true
		}
		return time.Time{}, true
	}
	return time.Time{}, false
}

func cronJobRow(c *batchv1.CronJob, now time.Time) []Cell {
	state := graded("Active", ClassNone, 0)
	if c.Spec.Suspend != nil && *c.Spec.Suspend {
		// Suspended is a decision, not a failure, but it explains a missed run,
		// so it sorts high.
		state = graded("Suspended", ClassWarn, RankWarn)
	}

	last := Cell{Text: Missing}
	if c.Status.LastScheduleTime != nil {
		last = elapsed(now.Sub(c.Status.LastScheduleTime.Time))
	}

	return []Cell{
		text(c.Name),
		text(c.Namespace),
		text(c.Spec.Schedule),
		state,
		count(len(c.Status.Active)),
		last,
		lastResult(c),
		nextRun(c, now),
		age(c.CreationTimestamp, now),
	}
}

// CronJobSchedule is the expression and the zone it must be evaluated in.
// `spec.timeZone` is a pointer; reading the expression in the console's own
// zone instead would shift every run. Exported so every caller agrees.
func CronJobSchedule(c *batchv1.CronJob) (expr, zone string) {
	if c == nil {
		return "", ""
	}
	if c.Spec.TimeZone != nil {
		zone = *c.Spec.TimeZone
	}
	return c.Spec.Schedule, zone
}

// nextRun is the countdown column: how long until this CronJob fires.
//
// It does not tick: it is printed to one unit, and the list re-reads every
// reconcilingRefresh, finer than that unit.
//
// A schedule that will not resolve (an unparsable expression, a non-IANA
// zone, an occurrence that does not exist) arrives as the zero time and
// renders as an em dash: a wrong countdown is worse than none.
func nextRun(c *batchv1.CronJob, now time.Time) Cell {
	expr, zone := CronJobSchedule(c)
	at := schedule.Next(expr, zone, now)
	if at.IsZero() {
		return Cell{Text: Missing}
	}

	until := at.Sub(now)
	if until < 0 {
		// schedule.Next answers after `now`, so this is the clock moving.
		until = 0
	}

	// The wall-clock time in the schedule's own zone, so the countdown can
	// be checked against the expression.
	when := at.Format("Mon 2 Jan 15:04")
	if zone != "" {
		when += " " + zone
	} else {
		when += " " + at.Format("MST")
	}
	if c.Spec.Suspend != nil && *c.Spec.Suspend {
		// No next run while suspended: a countdown would be false. The time it
		// would run if resumed stays in the hint.
		return Cell{Text: Missing, Hint: "spec.suspend is set, so nothing will run. Resumed, it would next run in " +
			ShortDuration(until) + ", at " + when}
	}
	return Cell{Text: "in " + ShortDuration(until), Value: until.Seconds(), Hint: when}
}

// lastResult is how the latest run ended, inferred from the CronJob's status
// (the Last run column says only when).
//
// The controller writes `lastScheduleTime` when it creates a run's Job and
// `lastSuccessfulTime` when it sees one complete. A run still `active` comes
// first: with overlapping runs an older run's success can postdate the newest
// schedule. With nothing active, the latest run succeeded if the success is no
// older than the schedule; otherwise it failed or its Job was deleted, which
// the status cannot tell apart without reading the Jobs, and the hint says so.
func lastResult(c *batchv1.CronJob) Cell {
	scheduled, succeeded := c.Status.LastScheduleTime, c.Status.LastSuccessfulTime
	stamp := func(t *metav1.Time) string { return t.UTC().Format("Mon 2 Jan 15:04 MST") }
	const inferred = " (inferred from the CronJob's status, not read from its Jobs)"
	switch {
	case scheduled == nil:
		return Cell{Text: Missing, Hint: "no run has been scheduled yet"}
	case len(c.Status.Active) > 0:
		cell := graded("Running", ClassNone, 0)
		cell.Hint = fmt.Sprintf("%d run(s) still going; the latest was scheduled at %s%s",
			len(c.Status.Active), stamp(scheduled), inferred)
		return cell
	case succeeded != nil && !succeeded.Before(scheduled):
		cell := graded("Succeeded", ClassOK, RankOK)
		cell.Hint = "the run scheduled at " + stamp(scheduled) + " succeeded at " + stamp(succeeded) + inferred
		return cell
	}
	cell := graded("Failed", ClassBad, RankBad)
	cell.Hint = "the run scheduled at " + stamp(scheduled) +
		" is not running and has not succeeded: it failed, or its Job was deleted before it finished" + inferred
	if succeeded != nil {
		cell.Hint += "; the last success was at " + stamp(succeeded)
	} else {
		cell.Hint += "; no run has succeeded"
	}
	return cell
}
