package detail

import (
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/thonicdev/kubetower/internal/resources"
)

// What a run is asked about: when it started, when it stopped, and how long it
// took. Created does not answer it: a CronJob's Job is created when scheduled.

// jobFacts is a Job's Started, Finished and Duration.
//
// Duration is the list's cell (resources.JobDuration), so the two agree; the
// list rounds to one unit, so the exact figure follows in brackets where it
// says more.
func jobFacts(job *batchv1.Job, now time.Time) []Fact {
	if job.Status.StartTime == nil {
		// Suspended before it ran, or not yet picked up: said, not left out.
		return []Fact{{Label: "Started", Value: "not yet"}}
	}
	start := job.Status.StartTime.Time
	facts := []Fact{{Label: "Started", Value: stamp(start, now)}}

	end, stopped := resources.JobStopped(job)
	switch {
	case !stopped:
		facts = append(facts, Fact{Label: "Finished", Value: "running"})
		end = now
	case end.IsZero():
		facts = append(facts, Fact{Label: "Finished", Value: "stopped, and the Job records no time it stopped"})
	default:
		facts = append(facts, Fact{Label: "Finished", Value: stamp(end, now)})
	}

	duration := resources.JobDuration(job, now).Text
	if !end.IsZero() {
		if exact := exactDuration(end.Sub(start)); exact != duration {
			duration += " (" + exact + ")"
		}
	}
	return append(facts, Fact{Label: "Duration", Value: duration})
}

// podRunFacts is how long the run of a pod a Job owns lasted: from the pod's
// start to the last of its containers' exits.
//
// Only for a Job's pod: a Deployment's pod is meant to keep running, and
// "ran for 41d" would read as something that ended.
func podRunFacts(pod *corev1.Pod, now time.Time) []Fact {
	if !ownedBy(pod.OwnerReferences, "Job") {
		return nil
	}
	if pod.Status.StartTime == nil {
		// Not started: Phase already says Pending.
		return nil
	}
	start := pod.Status.StartTime.Time

	if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
		return []Fact{{Label: "Ran", Value: fmt.Sprintf("running for %s, since %s",
			exactDuration(now.Sub(start)), start.Format(time.RFC3339))}}
	}
	end := lastExit(pod.Status.ContainerStatuses)
	if end.IsZero() {
		return []Fact{{Label: "Ran", Value: "ended, and the kubelet records no time its containers finished"}}
	}
	return []Fact{{Label: "Ran", Value: fmt.Sprintf("%s, from %s to %s (%s ago)",
		exactDuration(end.Sub(start)), start.Format(time.RFC3339), end.Format(time.RFC3339),
		shortDuration(now.Sub(end)))}}
}

// lastExit is the latest finishedAt among the containers that have terminated.
// Init containers finish before the others start, so they are never the last.
func lastExit(statuses []corev1.ContainerStatus) time.Time {
	var last time.Time
	for _, s := range statuses {
		if t := s.State.Terminated; t != nil && t.FinishedAt.Time.After(last) {
			last = t.FinishedAt.Time
		}
	}
	return last
}

func ownedBy(owners []metav1.OwnerReference, kind string) bool {
	for _, owner := range owners {
		if owner.Kind == kind {
			return true
		}
	}
	return false
}

// jobOf and podOf convert the object to its typed shape once. Nil when it does
// not convert, and those readings then add nothing rather than half a panel.
func jobOf(raw *unstructured.Unstructured) *batchv1.Job {
	var job batchv1.Job
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Object, &job); err != nil {
		return nil
	}
	return &job
}

func podOf(raw *unstructured.Unstructured) *corev1.Pod {
	var pod corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Object, &pod); err != nil {
		return nil
	}
	return &pod
}

// stamp is a moment as the panel prints Created: the time, and how long ago.
func stamp(t, now time.Time) string {
	return fmt.Sprintf("%s (%s ago)", t.Format(time.RFC3339), shortDuration(now.Sub(t)))
}

// exactDuration prints every unit down to the second: 1h40m12s, 3d2h, 27s.
// time.Duration's own String prints a day as 24h0m0s.
func exactDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d <= 0 {
		return "0s"
	}
	parts := []struct {
		n    time.Duration
		unit string
	}{
		{d / (24 * time.Hour), "d"},
		{d % (24 * time.Hour) / time.Hour, "h"},
		{d % time.Hour / time.Minute, "m"},
		{d % time.Minute / time.Second, "s"},
	}
	var b strings.Builder
	for _, p := range parts {
		if p.n > 0 {
			fmt.Fprintf(&b, "%d%s", p.n, p.unit)
		}
	}
	return b.String()
}

// What a failed Job is asked next: how many pods ran and failed, how many it
// may lose, and what it says it is. `conditionsOf` keeps the conditions that
// are not True, but a Job complains the other way round (`Failed: True,
// BackoffLimitExceeded`), so the Job gets its own facts.

// jobStatusFacts is a Job's counts and its retry budget, as `kubectl get -o
// yaml` spells them: `spec.completions`, `spec.parallelism`, `spec.backoffLimit`
// and `status.{active,ready,succeeded,failed,terminating}`.
func jobStatusFacts(job *batchv1.Job) []Fact {
	spec, status := job.Spec, job.Status

	// Succeeded over wanted, as the list draws it. With no `completions` the
	// Job is a work queue completed by any one success; "1/1" would invent a
	// target.
	completions := fmt.Sprintf("%d succeeded (no completions target: a work queue)", status.Succeeded)
	if spec.Completions != nil {
		completions = fmt.Sprintf("%d/%d", status.Succeeded, *spec.Completions)
	}
	facts := []Fact{{Label: "Completions", Value: completions}}
	if spec.Parallelism != nil {
		facts = append(facts, Fact{Label: "Parallelism", Value: fmt.Sprint(*spec.Parallelism)})
	}

	// Every count, zeros included: "0 failed" is an answer.
	pods := fmt.Sprintf("%d active", status.Active)
	if status.Ready != nil {
		pods += fmt.Sprintf(" (%d ready)", *status.Ready)
	}
	pods += fmt.Sprintf(", %d succeeded, %d failed", status.Succeeded, status.Failed)
	if status.Terminating != nil && *status.Terminating > 0 {
		pods += fmt.Sprintf(", %d terminating", *status.Terminating)
	}
	class := resources.ClassNone
	if status.Failed > 0 {
		class = resources.ClassWarn
	}
	facts = append(facts, Fact{Label: "Pods", Value: pods, Class: class})

	// The Job is marked failed once failed pods exceed `backoffLimit`, so the
	// count beside the limit says how much is spent. The API server defaults it
	// to 6. Not drawn with `backoffLimitPerIndex`: the budget is then per index
	// and `backoffLimit` defaults to MaxInt32, a number the Job does not act on.
	if spec.BackoffLimit != nil && spec.BackoffLimitPerIndex == nil {
		limit := *spec.BackoffLimit
		value := fmt.Sprintf("%d retries; %d failed pod(s) so far", limit, status.Failed)
		class := resources.ClassNone
		switch {
		case status.Failed > limit:
			value += " — exceeded"
			class = resources.ClassBad
		case status.Failed > 0:
			class = resources.ClassWarn
		}
		facts = append(facts, Fact{Label: "Backoff limit", Value: value, Class: class})
	}
	if spec.BackoffLimitPerIndex != nil {
		value := fmt.Sprintf("%d retries per index", *spec.BackoffLimitPerIndex)
		if status.FailedIndexes != nil && *status.FailedIndexes != "" {
			value += "; failed indexes " + *status.FailedIndexes
		}
		facts = append(facts, Fact{Label: "Backoff limit per index", Value: value})
	}
	return facts
}

// jobConditionFacts is every condition a Job carries, in the order it carries
// them, colored by what the condition means rather than by its status.
//
// All of them: a Job has a handful, each answering "what happened to this
// run". A False one is drawn plain, a state the Job left
// (`Suspended: False, JobResumed`).
func jobConditionFacts(job *batchv1.Job, now time.Time) []Fact {
	out := make([]Fact, 0, len(job.Status.Conditions))
	for _, c := range job.Status.Conditions {
		if c.Type == "" {
			continue
		}
		value := conditionText(condition{
			kind: string(c.Type), status: string(c.Status), reason: c.Reason, message: c.Message,
			since: c.LastTransitionTime.Time,
		}, now)
		class := resources.ClassNone
		if c.Status == corev1.ConditionTrue {
			switch c.Type {
			case batchv1.JobFailed, batchv1.JobFailureTarget:
				class = resources.ClassBad
			case batchv1.JobComplete, batchv1.JobSuccessCriteriaMet:
				class = resources.ClassOK
			case batchv1.JobSuspended:
				class = resources.ClassWarn
			}
		}
		out = append(out, Fact{
			Label:    string(c.Type),
			Value:    value,
			Class:    class,
			Sentence: true,
		})
	}
	return out
}
