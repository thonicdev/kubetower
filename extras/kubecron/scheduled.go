package kubecron

import (
	"fmt"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/thonicdev/kubetower/schedule"
	"github.com/thonicdev/kubetower/textutil"
)

// What one cluster's CronJobs are doing, judged. CronJobs and Jobs are read
// through `extension.Host.List`, reviewed for the person, so a person is judged
// only the CronJobs they may list, and the collector's history is joined onto
// those alone.
//
// The answers come from, in order:
//
//  1. The CronJob objects: schedule, zone, suspension, lastScheduleTime and
//     status.active, enough on their own to say whether a schedule is late.
//  2. The Jobs Kubernetes still retains: outcomes and durations.
//  3. A KubeCron collector, when deployed: outcomes, exit codes, samples and
//     logs for runs Kubernetes forgot (collector.go).
//
// The first must give a complete, correct page alone, which is why WithHistory
// is a separate step. Nothing here writes (suspend, resume and trigger are the
// console's, behind its review), and a verdict it cannot establish stays
// unknown.

// Availability is whether part of the answer could be produced, and why not.
// A cluster that could not be asked must never render as one with nothing
// wrong: "no late jobs" is also what a healthy cluster looks like.
type Availability struct {
	OK     bool
	Detail string
}

// Run is one retained Job, as much of it as the cluster still holds.
type Run struct {
	Name       string
	StartedAt  time.Time
	FinishedAt time.Time
	// Status is the Job's reading in the console's Jobs list's words (jobStatus),
	// and Class its severity, so this page and that list say the same thing.
	Status string
	Class  string
}

// Duration is how long the run took, or has been running. Zero when it never
// started.
func (r Run) Duration(now time.Time) time.Duration {
	if r.StartedAt.IsZero() {
		return 0
	}
	end := r.FinishedAt
	if end.IsZero() {
		end = now
	}
	return end.Sub(r.StartedAt)
}

// CronJob is one scheduled job and everything this page knows about it.
type CronJob struct {
	Name      string
	Namespace string
	Schedule  string
	// TimeZone is spec.timeZone, empty when the CronJob declares none.
	TimeZone  string
	Suspended bool

	// Verdict is the schedule-health answer. VerdictUnknown is a real value and
	// is drawn as its own state, never folded into on time.
	Verdict schedule.Verdict
	// Unresolved says why Verdict is unknown, in a sentence. Empty otherwise.
	Unresolved string
	// DueAt is the occurrence Verdict was judged against; zero when none.
	DueAt time.Time
	// NextAt is the next occurrence; zero when it cannot be computed, which is
	// how a bad schedule renders: nothing, rather than a wrong countdown.
	NextAt time.Time
	// LastScheduleTime is when the controller last created a Job. It survives
	// the Job being garbage-collected, which is why the verdict rests on it.
	LastScheduleTime time.Time
	// LastSuccessfulTime is status.lastSuccessfulTime. Its distance from
	// LastScheduleTime is the useful reading: a job that fires hourly and last
	// succeeded yesterday is failing, not idle.
	LastSuccessfulTime time.Time
	// ActiveCount is how many runs are in flight, from status.active.
	ActiveCount int
	// Runs are the retained Jobs, newest first. Empty is normal.
	Runs []Run
	// History is what the collector knows over its window. Nil when none
	// answered, which is a different thing from an Outcome with no failures.
	History *Outcome
}

// Late is how far past its due time this CronJob is. Zero unless missed.
func (c CronJob) Late(now time.Time) time.Duration {
	if c.Verdict != schedule.VerdictMissed || c.DueAt.IsZero() {
		return 0
	}
	return now.Sub(c.DueAt)
}

// Report is one cluster's scheduled-jobs answer, with what it was built from.
type Report struct {
	// CronJobs, worst first: missed, unknown, in flight, the rest, suspended.
	CronJobs []CronJob
	// Schedules says whether CronJobs could be listed at all. When it is not OK
	// there is nothing on the page, and it says so rather than drawing an empty
	// table that reads as "no CronJobs here".
	Schedules Availability
	// Complete is false when the list stopped before its end; Note says why.
	Complete bool
	Note     string
	// Retained says whether the retained Jobs could be read. Verdicts do not
	// need them, so this failing costs the recent-runs line and not health.
	Retained Availability
	// History says whether the collector answered for outcomes.
	History Availability

	Total, Missed, Unknown, Suspended, Active, Namespaces int
	// Failing is how many CronJobs have a failed run in the collector's window.
	// Zero until it answered, which is why History carries the difference.
	Failing int

	// CollectedAt stamps the read: a page that does not say how old it is will
	// be read as live.
	CollectedAt time.Time
}

// listed is what the caller read through Host.List: the objects, and whether
// the read reached the end. A function's input rather than a Host, so the
// judgment is testable without a cluster.
type listed struct {
	Items    []unstructured.Unstructured
	Complete bool
	Note     string
}

// judgeAll turns the CronJobs and the Jobs the person may read into a report.
// jobsErr is the Jobs read's failure, which costs outcomes and not verdicts.
func judgeAll(cronJobs listed, jobs *listed, jobsErr error, now time.Time) Report {
	report := Report{
		CollectedAt: now,
		Complete:    cronJobs.Complete,
		Note:        cronJobs.Note,
		Schedules:   Availability{OK: true, Detail: fmt.Sprintf("%d CronJob(s)", len(cronJobs.Items))},
	}

	runs := map[string][]Run{}
	switch {
	case jobsErr != nil:
		report.Retained = Availability{Detail: fmt.Sprintf("the Jobs behind these schedules could not be read (%s), so their recent runs are missing — the schedule verdicts are unaffected", shortError(jobsErr))}
	case jobs != nil:
		runs = indexRuns(jobs.Items)
		report.Retained = Availability{OK: true, Detail: fmt.Sprintf(
			"%d retained Job(s) — Kubernetes keeps only the last few per CronJob (successfulJobsHistoryLimit, 3 by default), so an empty history is normal",
			len(jobs.Items))}
	}

	namespaces := map[string]struct{}{}
	for i := range cronJobs.Items {
		var c batchv1.CronJob
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(cronJobs.Items[i].Object, &c); err != nil {
			continue
		}
		cj := judge(&c, runs, now)
		report.CronJobs = append(report.CronJobs, cj)
		namespaces[cj.Namespace] = struct{}{}

		report.Total++
		switch cj.Verdict {
		case schedule.VerdictMissed:
			report.Missed++
		case schedule.VerdictUnknown:
			report.Unknown++
		case schedule.VerdictSuspended:
			report.Suspended++
		}
		report.Active += cj.ActiveCount
	}
	report.Namespaces = len(namespaces)
	sortCronJobs(report.CronJobs)
	return report
}

// judge turns one CronJob object into the page's reading of it.
func judge(c *batchv1.CronJob, runs map[string][]Run, now time.Time) CronJob {
	out := CronJob{
		Name:        c.Name,
		Namespace:   c.Namespace,
		Schedule:    c.Spec.Schedule,
		Suspended:   c.Spec.Suspend != nil && *c.Spec.Suspend,
		ActiveCount: len(c.Status.Active),
		Runs:        runs[c.Namespace+"/"+c.Name],
	}
	if c.Spec.TimeZone != nil {
		out.TimeZone = *c.Spec.TimeZone
	}
	if c.Status.LastScheduleTime != nil {
		out.LastScheduleTime = c.Status.LastScheduleTime.Time
	}
	if c.Status.LastSuccessfulTime != nil {
		out.LastSuccessfulTime = c.Status.LastSuccessfulTime.Time
	}

	// status.lastScheduleTime is the signal, not the retained Jobs: judging on
	// the Jobs alone would report every per-minute CronJob as missed the moment
	// its history rolled.
	var last *schedule.LastRun
	if !out.LastScheduleTime.IsZero() {
		last = &schedule.LastRun{StartedAt: out.LastScheduleTime, Active: out.ActiveCount > 0}
	}
	out.Verdict, out.DueAt = schedule.HealthSince(out.Schedule, out.TimeZone, out.Suspended, last, c.CreationTimestamp.Time, now)
	if out.Verdict == schedule.VerdictUnknown {
		out.Unresolved = unresolvedReason(out.Schedule, out.TimeZone, now)
	}
	// Computed even for a suspended CronJob: "it would have run at 04:00" is what
	// somebody about to resume it wants to know.
	out.NextAt = schedule.Next(out.Schedule, out.TimeZone, now)
	return out
}

// unresolvedReason says why a schedule could not be judged, naming the zone or
// the expression so the sentence points at the CronJob and not at this page.
func unresolvedReason(expr, tz string, now time.Time) string {
	if _, err := schedule.Parse(expr, tz); err != nil {
		if tz != "" {
			if _, zerr := time.LoadLocation(tz); zerr != nil {
				return fmt.Sprintf("spec.timeZone is %q, which is not an IANA zone name", tz)
			}
		}
		return fmt.Sprintf("spec.schedule is %q, which does not parse as a cron expression", expr)
	}
	if _, err := schedule.PrevRun(expr, tz, now); err != nil {
		return fmt.Sprintf("spec.schedule is %q, which has no occurrence recent enough to judge — a date like 29 February can be years apart", expr)
	}
	return "the schedule could not be evaluated"
}

// indexRuns groups Jobs by the CronJob that owns them, newest first. A Job with
// no CronJob owner is a manual run and is not evidence about a schedule.
func indexRuns(items []unstructured.Unstructured) map[string][]Run {
	out := map[string][]Run{}
	for i := range items {
		var j batchv1.Job
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(items[i].Object, &j); err != nil {
			continue
		}
		owner, ok := cronJobOwner(&j)
		if !ok {
			continue
		}
		status, class := jobStatus(&j)
		run := Run{Name: j.Name, Status: status, Class: class}
		if j.Status.StartTime != nil {
			run.StartedAt = j.Status.StartTime.Time
		}
		if end, stopped := jobStopped(&j); stopped {
			run.FinishedAt = end
		}
		key := j.Namespace + "/" + owner
		out[key] = append(out[key], run)
	}
	for key := range out {
		list := out[key]
		sort.SliceStable(list, func(a, b int) bool { return list[a].StartedAt.After(list[b].StartedAt) })
	}
	return out
}

func cronJobOwner(j *batchv1.Job) (string, bool) {
	for _, ref := range j.OwnerReferences {
		if ref.Kind == "CronJob" {
			return ref.Name, true
		}
	}
	return "", false
}

// jobStatus is the console's Jobs-list reading of a Job (`resources.JobStatus`
// in the core), restated because an extra cannot import `internal/`. The two
// must say the same word for the same Job; the cases are the core's, in its
// order, and the classes are the console's pill classes.
func jobStatus(j *batchv1.Job) (string, string) {
	for _, c := range j.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return "Complete", "ok"
		case batchv1.JobFailed:
			return "Failed", "bad"
		}
	}
	if j.Spec.Suspend != nil && *j.Spec.Suspend {
		return "Suspended", "warn"
	}
	if j.Status.Failed > 0 {
		return fmt.Sprintf("Retrying (%d failed)", j.Status.Failed), "warn"
	}
	if j.Status.Active > 0 {
		return "Running", ""
	}
	return "Pending", "warn"
}

// jobStopped is when a Job stopped, as the core's `resources.JobStopped` reads
// it: completionTime is set on success only, so a failure is dated by its
// Failed (or FailureTarget) condition.
func jobStopped(j *batchv1.Job) (time.Time, bool) {
	if j.Status.CompletionTime != nil {
		return j.Status.CompletionTime.Time, true
	}
	for _, want := range []batchv1.JobConditionType{batchv1.JobFailed, batchv1.JobFailureTarget} {
		for _, c := range j.Status.Conditions {
			if c.Type == want && c.Status == corev1.ConditionTrue {
				return c.LastTransitionTime.Time, true
			}
		}
	}
	return time.Time{}, false
}

// sortCronJobs puts what needs attention first: missed before unknown (a fact
// before a question), then a CronJob whose runs fail, then in flight, the rest,
// and suspended last. Failing comes from the collector, so WithHistory sorts
// again after the join.
func sortCronJobs(list []CronJob) {
	weight := func(c CronJob) int {
		switch {
		case c.Verdict == schedule.VerdictMissed:
			return 0
		case c.Verdict == schedule.VerdictUnknown:
			return 1
		case c.History != nil && c.History.Failed > 0:
			return 2
		case c.ActiveCount > 0:
			return 3
		case c.Verdict == schedule.VerdictSuspended:
			return 5
		default:
			return 4
		}
	}
	sort.SliceStable(list, func(a, b int) bool {
		wa, wb := weight(list[a]), weight(list[b])
		if wa != wb {
			return wa < wb
		}
		if !list[a].DueAt.Equal(list[b].DueAt) {
			return list[a].DueAt.Before(list[b].DueAt)
		}
		if list[a].Namespace != list[b].Namespace {
			return list[a].Namespace < list[b].Namespace
		}
		return list[a].Name < list[b].Name
	})
}

// shortError trims an error to something that fits a sentence: an RBAC refusal
// arrives as three lines of URL.
func shortError(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if i := strings.Index(text, ":"); i > 0 && i < 40 {
		if rest := strings.TrimSpace(text[i+1:]); len(rest) > 0 && len(text) > 120 {
			text = strings.TrimSpace(text[:i]) + ": " + strings.TrimSpace(textutil.FirstLine(rest))
		}
	}
	return textutil.Clip(text, 160)
}
