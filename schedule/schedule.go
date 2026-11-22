// Package schedule answers when a CronJob was due, and whether it fired.
//
// A resource list can say how long ago a CronJob last ran, not whether that is
// late: a six-hourly job that ran three hours ago and one whose schedule
// stopped firing read the same. Telling them apart means evaluating the
// expression.
//
// # The zone is not optional
//
// Every entry point takes an IANA zone name beside the expression, because
// Kubernetes evaluates a CronJob's schedule in its own `spec.timeZone` (the
// controller prepends `TZ=<zone>`; pinning the parsed schedule's Location is
// the same operation). Evaluated in the console's zone, `0 4 * * *` in
// Asia/Tokyo would read nine hours late every day.
//
// An empty zone is UTC. Kubernetes evaluates a CronJob that declares none in
// the controller manager's own zone, which is UTC on every managed cluster.
// Leaving it in the zone of the timestamp handed in, as robfig does, would put
// it in the console's zone instead: a desktop in Paris would count a daily
// `0 4 * * *` down to 04:00 Paris, and judge it missed for an hour a day.
//
// `time.LoadLocation` needs a zone database, which the Alpine image and Windows
// lack; `internal/launch` imports `time/tzdata` to embed it. Without it every
// zoned CronJob silently resolves to nothing.
//
// # It refuses rather than guesses
//
// An unparseable expression, an unknown zone or a schedule too sparse to locate
// produce no answer, and callers must render nothing: a wrong countdown or a
// false missed-run badge is worse than none.
package schedule

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// Parse parses a standard five-field cron expression — or one of the `@`
// shorthands — and resolves it in the IANA zone named tz.
//
// It uses robfig/cron/v3 because the Kubernetes CronJob controller does: a
// parser that disagreed on `@monthly`, stepped ranges or the
// day-of-month/day-of-week rule would predict wrongly what the cluster does.
//
// A tz argument takes precedence over any `CRON_TZ=` or `TZ=` prefix inside
// expr; Kubernetes rejects a CronJob that sets both.
func Parse(expr, tz string) (cron.Schedule, error) {
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return nil, fmt.Errorf("schedule: %q: %w", expr, err)
	}
	loc := time.UTC
	if tz != "" {
		loc, err = time.LoadLocation(tz)
		if err != nil {
			return nil, fmt.Errorf("schedule: unknown time zone %q: %w", tz, err)
		}
	}
	// Wall-clock schedules ("0 4 * * *") carry the zone they are read in;
	// interval schedules ("@every 1h") are zone-independent, and only
	// SpecSchedule has a Location. With no tz, a `CRON_TZ=` prefix the parser
	// already applied is kept: only robfig's own default, time.Local, becomes UTC.
	if spec, ok := sched.(*cron.SpecSchedule); ok && (tz != "" || spec.Location == time.Local) {
		spec.Location = loc
	}
	return sched, nil
}

// inZone re-stamps an instant with the schedule's own zone.
//
// robfig returns occurrences in the location of the timestamp it was given, so
// a Tokyo schedule asked from a UTC `now` yields the right instant shown in
// UTC; a person needs "04:00" in Tokyo, not 19:00. Parse has already loaded the
// zone, so a failure here leaves the instant untouched.
func inZone(t time.Time, tz string) time.Time {
	if tz == "" || t.IsZero() {
		return t
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return t
	}
	return t.In(loc)
}

// NextRun returns the first scheduled time strictly after `after`, expressed in
// the schedule's own zone.
func NextRun(expr, tz string, after time.Time) (time.Time, error) {
	s, err := Parse(expr, tz)
	if err != nil {
		return time.Time{}, err
	}
	next := inZone(s.Next(after), tz)
	if next.IsZero() {
		// robfig returns the zero time for a schedule that can never fire
		// again, such as "0 0 30 2 *"; Kubernetes admits it and never runs it.
		return time.Time{}, fmt.Errorf("schedule: %q has no next occurrence", expr)
	}
	return next, nil
}

// Next is NextRun with the refusal expressed as a zero time rather than an
// error, the shape a caller that renders it wants: no answer rather than a
// wrong one. A caller must test the zero, which otherwise renders as 1 January
// year 1. Callers wanting to say why there is no answer use NextRun.
func Next(expr, tz string, after time.Time) time.Time {
	next, err := NextRun(expr, tz, after)
	if err != nil {
		return time.Time{}
	}
	return next
}

// NextRuns returns the next n scheduled times after `after`.
func NextRuns(expr, tz string, after time.Time, n int) ([]time.Time, error) {
	s, err := Parse(expr, tz)
	if err != nil {
		return nil, err
	}
	out := make([]time.Time, 0, n)
	t := after
	for range n {
		t = s.Next(t)
		if t.IsZero() {
			break
		}
		out = append(out, inZone(t, tz))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("schedule: %q has no next occurrence", expr)
	}
	return out, nil
}

// maxLookback bounds how far PrevRun will search. A schedule whose last
// occurrence is over a year old fires on a date this year does not contain
// ("0 0 29 2 *" outside a leap year), and is not a late run.
const maxLookback = 400 * 24 * time.Hour

// maxProbes bounds one walk, so a per-minute schedule cannot turn a page render
// into hundreds of thousands of calls if the window estimate is wrong. A walk
// that reaches it has not reached the end of its window, so its last occurrence
// is not the latest: PrevRun carries on from there, in further walks, until
// maxSearchProbes, and past that answers an error rather than an earlier time.
const maxProbes = 2000

// maxSearchProbes bounds the probes of one PrevRun call, across every walk.
const maxSearchProbes = 16 * maxProbes

// PrevRun returns the most recent scheduled time at or before `now`, expressed
// in the schedule's own zone.
//
// robfig/cron only walks forwards, so this searches from a start in the past.
// A fixed window fails both ways: wide is costly for a per-minute schedule, and
// narrow (25 hours) misses weekly and monthly schedules on most days, which
// would silently switch missed-run detection off for them. So the window is
// derived from the schedule: twice the cadence of two future occurrences, then
// quadrupled until an occurrence is found, which also covers irregular
// schedules (`0 9 * * 1-5` across a weekend).
func PrevRun(expr, tz string, now time.Time) (time.Time, error) {
	s, err := Parse(expr, tz)
	if err != nil {
		return time.Time{}, err
	}

	window := 2 * estimateCadence(s, now)
	if window < time.Hour {
		window = time.Hour
	}

	// Clamped, and the loop runs at least once: a yearly schedule's estimated
	// cadence already exceeds the cap, and must still be searched.
	probes := 0
	for {
		if window > maxLookback {
			window = maxLookback
		}
		prev, ok, cut, n := walk(s, now.Add(-window), now)
		probes += n
		for cut {
			if probes >= maxSearchProbes {
				return time.Time{}, fmt.Errorf("schedule: %q has too many occurrences to search", expr)
			}
			var next time.Time
			var more bool
			next, more, cut, n = walk(s, prev, now)
			probes += n
			if more {
				prev = next
			}
		}
		if ok {
			return inZone(prev, tz), nil
		}
		if window >= maxLookback {
			break
		}
		window *= 4
	}
	return time.Time{}, fmt.Errorf("schedule: %q has no occurrence in the last %d days", expr, int(maxLookback.Hours()/24))
}

// estimateCadence measures the gap between the next two occurrences. It is an
// estimate, not a period: an irregular schedule has none, which is why the
// caller expands the window rather than trusting this.
func estimateCadence(s cron.Schedule, now time.Time) time.Duration {
	first := s.Next(now)
	if first.IsZero() {
		return maxLookback
	}
	second := s.Next(first)
	if second.IsZero() {
		return maxLookback
	}
	return second.Sub(first)
}

// walk returns the last occurrence in (from, to] it reached, whether there was
// one, whether it stopped on maxProbes before passing `to` (so that occurrence
// may not be the last in the window), and how many probes it made.
func walk(s cron.Schedule, from, to time.Time) (prev time.Time, found, cut bool, probes int) {
	t := from
	for range maxProbes {
		probes++
		next := s.Next(t)
		if next.IsZero() || next.After(to) {
			return prev, found, false, probes
		}
		prev, found = next, true
		t = next
	}
	return prev, found, true, probes
}

// MissedGracePeriod is how long after a due time a run may still appear before
// the schedule counts as missed.
//
// It absorbs the lag before the Job created by the controller is observable;
// the controller itself tolerates 100 seconds. Five minutes is past that and
// still flags a skipped hourly job within the hour.
const MissedGracePeriod = 5 * time.Minute

// LastRun is the minimum this package needs to know about the most recent run,
// whatever it was read from (live Jobs, Prometheus, a collector).
type LastRun struct {
	StartedAt time.Time
	// Active marks a run that has not finished. A run still in flight proves the
	// schedule fired, whenever it started.
	Active bool
}

// Verdict is what can be said about a CronJob's schedule health.
type Verdict string

const (
	// VerdictOK means the last due occurrence produced a run.
	VerdictOK Verdict = "ok"
	// VerdictMissed means it did not. This is the only value that is an alarm.
	VerdictMissed Verdict = "missed"
	// VerdictSuspended means nothing was due, because the CronJob is suspended.
	// A decision, not a fault.
	VerdictSuspended Verdict = "suspended"
	// VerdictUnknown means the question could not be answered: the expression or
	// the zone did not resolve, or the schedule is too sparse to locate a due
	// time. It must never be drawn as healthy (that hides broken schedules) nor
	// as missed (that alarms on a typo in a zone).
	VerdictUnknown Verdict = "unknown"
)

// Health judges one CronJob. `last` may be nil, meaning no run is known.
//
// Not a boolean: "not missed" is three answers — it ran, it was not due, or it
// could not be told — and the caller draws them differently.
//
// Health does not know when the CronJob was created; HealthSince does, and a
// caller that has the creation time uses it.
func Health(expr, tz string, suspended bool, last *LastRun, now time.Time) (Verdict, time.Time) {
	return HealthSince(expr, tz, suspended, last, time.Time{}, now)
}

// HealthSince is Health for a CronJob created at `created` (zero: unknown).
//
// A CronJob that has never run cannot have missed an occurrence due before it
// existed: a daily job created this afternoon has missed nothing until
// tomorrow's tick has passed.
func HealthSince(expr, tz string, suspended bool, last *LastRun, created, now time.Time) (Verdict, time.Time) {
	if suspended {
		return VerdictSuspended, time.Time{}
	}
	due, err := PrevRun(expr, tz, now)
	if err != nil {
		return VerdictUnknown, time.Time{}
	}
	if last == nil && !created.IsZero() && due.Before(created) {
		return VerdictOK, due
	}

	// Due within the grace period, so that occurrence cannot be judged yet:
	// step back to the one before rather than answer OK, or a dead per-minute
	// CronJob, always inside some grace period, would read healthy forever.
	//
	// Only when a run is known: with last == nil the earlier occurrence may
	// predate the CronJob, so a new CronJob would read as having missed a tick
	// from before it existed. Its verdict waits out the grace period instead.
	if now.Sub(due) <= MissedGracePeriod {
		if last == nil {
			return VerdictOK, due
		}
		earlier, err := PrevRun(expr, tz, due.Add(-time.Nanosecond))
		if err != nil {
			// Its first-ever occurrence is the one in flight; nothing earlier
			// could have been missed.
			return VerdictOK, due
		}
		due = earlier
	}

	if last == nil {
		return VerdictMissed, due
	}
	if last.Active || !last.StartedAt.Before(due) {
		return VerdictOK, due
	}
	return VerdictMissed, due
}
