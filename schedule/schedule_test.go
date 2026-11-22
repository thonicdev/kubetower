package schedule

import (
	"testing"
	"time"

	// The zone database, for the same reason internal/launch imports it: the test
	// host may be Windows, which has no /usr/share/zoneinfo, and a zone test
	// that silently skips is worse than no zone test.
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v — is time/tzdata imported?", name, err)
	}
	return loc
}

// --- Parse -----------------------------------------------------------------

func TestParseAcceptsWhatKubernetesAccepts(t *testing.T) {
	// Kubernetes validates spec.schedule with this same parser, so anything it
	// admits must resolve here. @-shorthands included: they are common in charts.
	for _, expr := range []string{
		"* * * * *",
		"0 4 * * *",
		"*/15 * * * *",
		"0 9 * * 1-5",
		"0 0 1 * *",
		"0 0 * * 0",
		"@hourly", "@daily", "@weekly", "@monthly", "@yearly",
		"@every 1h30m",
	} {
		if _, err := Parse(expr, ""); err != nil {
			t.Errorf("Parse(%q): %v", expr, err)
		}
	}
}

func TestParseRejectsRatherThanGuesses(t *testing.T) {
	for _, tc := range []struct{ expr, tz, why string }{
		{"not a schedule", "", "free text"},
		{"* * * *", "", "four fields"},
		{"99 * * * *", "", "minute out of range"},
		{"0 4 * * *", "Mars/Olympus_Mons", "unknown zone"},
	} {
		if _, err := Parse(tc.expr, tc.tz); err == nil {
			t.Errorf("Parse(%q, %q) accepted %s; it must refuse rather than guess", tc.expr, tc.tz, tc.why)
		}
	}
}

// TestParsePinsTheZone is the whole reason the zone is a parameter. Evaluated
// in UTC, a Tokyo CronJob's 04:00 is nine hours away from where it belongs.
func TestParsePinsTheZone(t *testing.T) {
	tokyo := mustLoad(t, "Asia/Tokyo")
	base := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)

	got, err := NextRun("0 4 * * *", "Asia/Tokyo", base)
	if err != nil {
		t.Fatal(err)
	}
	// Midnight UTC is already 09:00 in Tokyo, so today's 04:00 has gone and the
	// next one is tomorrow's — 2026-08-21 19:00 UTC.
	want := time.Date(2026, 8, 22, 4, 0, 0, 0, tokyo)
	if !got.Equal(want) {
		t.Errorf("next run = %s, want %s (04:00 Tokyo, not 04:00 UTC)", got, want)
	}
	// 19 hours after midnight UTC — the assertion that catches a zone silently
	// ignored, which would put the answer 4 hours out instead.
	if d := got.Sub(base); d != 19*time.Hour {
		t.Errorf("gap from midnight UTC = %s, want 19h", d)
	}
	// And it comes back wearing Tokyo, not the zone of the `now` it was asked
	// from: the page prints this, and "04:00" is only true in one of them.
	if got.Location().String() != "Asia/Tokyo" {
		t.Errorf("next run is in %s; a caller printing it would show the wrong wall clock", got.Location())
	}
	if h := got.Hour(); h != 4 {
		t.Errorf("next run reads %02d:00 to a person, want 04:00", h)
	}
}

// TestParseLeavesIntervalSchedulesAlone: "@every 1h" measures elapsed time. It
// has no wall clock to pin, and asserting a Location onto it would be a type
// error waiting to happen.
func TestParseLeavesIntervalSchedulesAlone(t *testing.T) {
	base := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	got, err := NextRun("@every 90m", "Asia/Tokyo", base)
	if err != nil {
		t.Fatal(err)
	}
	if d := got.Sub(base); d != 90*time.Minute {
		t.Errorf("gap = %s, want 90m regardless of zone", d)
	}
}

// TestParseAcrossDST: the 2 a.m. hour does not exist on the spring-forward day
// in Paris, so a 02:30 daily job must not silently resolve to something
// plausible-looking on that date. Whatever robfig does here, Kubernetes does
// too — which is the only property that matters, and the reason this test
// asserts agreement with the parser rather than a hand-computed time.
func TestParseAcrossDST(t *testing.T) {
	paris := mustLoad(t, "Europe/Paris")
	// 2026-03-29 is the spring-forward Sunday in the EU: 02:00 → 03:00.
	before := time.Date(2026, 3, 28, 12, 0, 0, 0, paris)

	runs, err := NextRuns("30 2 * * *", "Europe/Paris", before, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range runs {
		if r.Location().String() != "Europe/Paris" {
			t.Errorf("run %d is in %s, not Europe/Paris", i, r.Location())
		}
		if i > 0 && !r.After(runs[i-1]) {
			t.Errorf("runs are not strictly increasing across the DST boundary: %v", runs)
		}
	}
}

func TestNextRunRefusesAScheduleThatCanNeverFire(t *testing.T) {
	// The 30th of February. Kubernetes admits it; it simply never runs. An
	// absent countdown is the honest rendering.
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	if got, err := NextRun("0 0 30 2 *", "", now); err == nil {
		t.Errorf("NextRun returned %s for a date that does not exist; want an error", got)
	}
}

// --- PrevRun ---------------------------------------------------------------

func TestPrevRun(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 34, 0, 0, time.UTC)
	tests := []struct {
		name, expr string
		want       time.Time
	}{
		{"per minute", "* * * * *", time.Date(2026, 8, 21, 12, 34, 0, 0, time.UTC)},
		{"quarter hour", "*/15 * * * *", time.Date(2026, 8, 21, 12, 30, 0, 0, time.UTC)},
		{"hourly", "0 * * * *", time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)},
		{"daily at 04:00", "0 4 * * *", time.Date(2026, 8, 21, 4, 0, 0, 0, time.UTC)},
		// 2026-08-21 is a Friday, so the previous weekday-09:00 is the same day.
		{"weekdays at 09:00", "0 9 * * 1-5", time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)},
		// The sparse cases. A fixed 25-hour lookback answers "cannot say" to
		// every one of these, which silently disables missed detection for the
		// nightly backup and the monthly invoice run.
		{"weekly on Sunday", "0 0 * * 0", time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)},
		{"monthly on the 1st", "0 0 1 * *", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
		{"quarterly", "0 0 1 1,4,7,10 *", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)},
		{"yearly", "@yearly", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PrevRun(tc.expr, "", now)
			if err != nil {
				t.Fatalf("PrevRun(%q): %v", tc.expr, err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("PrevRun(%q) = %s, want %s", tc.expr, got, tc.want)
			}
		})
	}
}

// TestPrevRunIsAtOrBeforeNow: the contract is "≤ now". A previous run in the
// future would make every comparison against it nonsense.
func TestPrevRunIsAtOrBeforeNow(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 34, 56, 0, time.UTC)
	for _, expr := range []string{"* * * * *", "*/5 * * * *", "0 4 * * *", "0 0 1 * *"} {
		got, err := PrevRun(expr, "", now)
		if err != nil {
			t.Fatalf("PrevRun(%q): %v", expr, err)
		}
		if got.After(now) {
			t.Errorf("PrevRun(%q) = %s, which is after now (%s)", expr, got, now)
		}
	}
}

func TestPrevRunHonoursTheZone(t *testing.T) {
	// 08:00 UTC on the 21st is 17:00 in Tokyo, so Tokyo's 04:00 has passed
	// today while UTC's has too — but they are five hours apart, and a console
	// reading the wrong one reports a nine-hour-old run as fresh.
	now := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	tokyo, err := PrevRun("0 4 * * *", "Asia/Tokyo", now)
	if err != nil {
		t.Fatal(err)
	}
	utc, err := PrevRun("0 4 * * *", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if tokyo.Equal(utc) {
		t.Fatal("the zone made no difference; it is being ignored")
	}
	if !tokyo.Before(utc) {
		t.Errorf("Tokyo 04:00 (%s) should precede UTC 04:00 (%s) at this instant", tokyo, utc)
	}
}

func TestPrevRunGivesUpRatherThanGuessing(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	// 29 February in a non-leap year: no occurrence within the lookback, and
	// "overdue" is the wrong word for it.
	if got, err := PrevRun("0 0 29 2 *", "", now); err == nil {
		t.Errorf("PrevRun returned %s; want an error so the caller renders nothing", got)
	}
}

// --- Health ----------------------------------------------------------------

func TestHealth(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	ranAt := func(d time.Duration) *LastRun { return &LastRun{StartedAt: now.Add(d)} }

	tests := []struct {
		name      string
		expr      string
		suspended bool
		last      *LastRun
		want      Verdict
	}{
		{
			name: "hourly job that ran on schedule",
			expr: "0 * * * *", last: ranAt(-59 * time.Minute), want: VerdictOK,
		},
		{
			name: "hourly job that did not run",
			expr: "0 * * * *", last: ranAt(-25 * time.Hour), want: VerdictMissed,
		},
		{
			name: "suspended is a decision, not a fault",
			expr: "0 * * * *", suspended: true, last: nil, want: VerdictSuspended,
		},
		{
			// The run is old, but so is the last due time — a monthly job on the
			// 21st is not late. This is the case a 25-hour lookback gets wrong,
			// by answering "unknown" and hiding a genuinely dead monthly job.
			name: "monthly job that ran when it was due",
			expr: "0 0 1 * *", last: ranAt(-20 * 24 * time.Hour), want: VerdictOK,
		},
		{
			name: "monthly job that stopped firing",
			expr: "0 0 1 * *", last: ranAt(-70 * 24 * time.Hour), want: VerdictMissed,
		},
		{
			// A run still in flight proves the schedule fired, however long ago
			// it started — a six-hour backup is not a missed run.
			name: "long run still in flight",
			expr: "0 * * * *", last: &LastRun{StartedAt: now.Add(-6 * time.Hour), Active: true}, want: VerdictOK,
		},
		{
			name: "unparseable schedule says so",
			expr: "not a schedule", last: nil, want: VerdictUnknown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := Health(tc.expr, "", tc.suspended, tc.last, now)
			if got != tc.want {
				t.Errorf("Health = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHealthOnACronJobThatHasNeverRun. Two answers, and the grace period is
// what separates them.
//
// The "ok" half is not a technicality: with no run to compare against there is
// no evidence in either direction, and the occurrence before this one may well
// predate the CronJob itself. A CronJob created three minutes ago has missed
// nothing, and reporting it as missed on its first render is how an operator
// learns to ignore the badge on the day they deploy.
func TestHealthOnACronJobThatHasNeverRun(t *testing.T) {
	due := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	// Two minutes after the tick: could be brand new, could be dead. Withhold.
	if got, _ := Health("0 * * * *", "", false, nil, due.Add(2*time.Minute)); got != VerdictOK {
		t.Errorf("Health = %q inside the grace period, want %q — a CronJob created moments ago has missed nothing", got, VerdictOK)
	}

	// Past the grace period the same state is a genuine miss.
	past := due.Add(MissedGracePeriod + time.Minute)
	if got, _ := Health("0 * * * *", "", false, nil, past); got != VerdictMissed {
		t.Errorf("Health = %q past the grace period with no run ever recorded, want %q", got, VerdictMissed)
	}
}

// TestHealthGracePeriod: a job is not missed in the seconds after it was due.
// Without this, every per-minute CronJob in the cluster is "missed" for part of
// every minute, and the badge becomes noise nobody reads.
func TestHealthGracePeriod(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 30, 0, time.UTC) // 30s past the minute
	got, _ := Health("* * * * *", "", false, &LastRun{StartedAt: now.Add(-90 * time.Second)}, now)
	if got != VerdictOK {
		t.Errorf("Health = %q %v after the due time; want %q — the grace period is not applying",
			got, MissedGracePeriod, VerdictOK)
	}
}

// TestHealthUnknownZoneIsUnknownNotMissed is one rule in one assertion: a
// typo in spec.timeZone must not page somebody about a job that runs perfectly.
func TestHealthUnknownZoneIsUnknownNotMissed(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	got, _ := Health("0 * * * *", "Mars/Olympus_Mons", false, nil, now)
	if got != VerdictUnknown {
		t.Errorf("Health with an unknown zone = %q, want %q", got, VerdictUnknown)
	}
}

// TestHealthReturnsTheDueTime: the UI says "due 3h ago", so the verdict alone
// is not enough — and the due time must be absent exactly when the verdict is
// one that has none.
func TestHealthReturnsTheDueTime(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	if _, due := Health("0 * * * *", "", false, nil, now); due.IsZero() {
		t.Error("a missed verdict carries no due time, so the page cannot say how late it is")
	}
	if _, due := Health("0 * * * *", "", true, nil, now); !due.IsZero() {
		t.Error("a suspended CronJob was given a due time; nothing was due")
	}
	if _, due := Health("nonsense", "", false, nil, now); !due.IsZero() {
		t.Error("an unresolvable schedule was given a due time")
	}
}

// TestPrevRunPastTheProbeBound: a weekday per-minute schedule asked on Saturday
// night is found only in a window holding more occurrences than one walk
// probes. The last occurrence reached by that walk is Friday afternoon; the
// latest is Friday 23:59.
func TestPrevRunPastTheProbeBound(t *testing.T) {
	now := time.Date(2026, 11, 7, 23, 0, 0, 0, time.UTC) // a Saturday
	got, err := PrevRun("* * * * 1-5", "UTC", now)
	if err != nil {
		t.Fatalf("PrevRun: %v", err)
	}
	if want := time.Date(2026, 11, 6, 23, 59, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("PrevRun = %s, want %s", got, want)
	}
}

// --- cost ------------------------------------------------------------------

// TestPrevRunStaysCheapOnDenseSchedules guards the reason the window is derived
// rather than fixed. A page draws this once per CronJob, and a cluster can hold
// hundreds: a naive 25-hour walk costs 1 500 Next() calls for a per-minute
// schedule, so 200 CronJobs would cost 300 000 per render.
func TestPrevRunStaysCheapOnDenseSchedules(t *testing.T) {
	s, err := Parse("* * * * *", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 21, 12, 34, 0, 0, time.UTC)
	counter := &countingSchedule{inner: s}

	window := 2 * estimateCadence(counter, now)
	if window < time.Hour {
		window = time.Hour
	}
	counter.n = 0
	if _, ok, _, _ := walk(counter, now.Add(-window), now); !ok {
		t.Fatal("no occurrence found for a per-minute schedule")
	}
	if counter.n > 70 {
		t.Errorf("walking a per-minute schedule took %d Next() calls; the window is too wide", counter.n)
	}
}

type countingSchedule struct {
	inner cron.Schedule
	n     int
}

func (c *countingSchedule) Next(t time.Time) time.Time {
	c.n++
	return c.inner.Next(t)
}

// TestHealthOnTheHourStillJudgesADeadJob is the reason Health steps back an
// occurrence at all. Asked exactly on the hour, the most recent due time is
// zero seconds old and cannot be judged — but a job that last ran a day ago has
// missed twenty-three occurrences since, and answering "ok" because of the
// timing of the question is how a dead schedule stays invisible.
func TestHealthOnTheHourStillJudgesADeadJob(t *testing.T) {
	onTheHour := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	last := &LastRun{StartedAt: onTheHour.Add(-25 * time.Hour)}

	if got, _ := Health("0 * * * *", "", false, last, onTheHour); got != VerdictMissed {
		t.Errorf("Health = %q for a job silent for 25h, asked on the hour; want %q", got, VerdictMissed)
	}
	// And the same job, healthy, must still read healthy at that instant.
	fresh := &LastRun{StartedAt: onTheHour.Add(-1 * time.Minute)}
	if got, _ := Health("0 * * * *", "", false, fresh, onTheHour); got != VerdictOK {
		t.Errorf("Health = %q for a job that ran a minute ago; want %q", got, VerdictOK)
	}
}

// TestAZonelessScheduleIsUTCWhateverTheZoneOfNow: a CronJob with no
// spec.timeZone runs in the controller manager's zone, UTC on managed clusters.
// The console's own zone must not leak in through the `now` it hands over: on a
// desktop in New York, a daily 04:00 read in local time is 04:00 New York,
// eight hours from the cluster's 04:00 UTC.
func TestAZonelessScheduleIsUTCWhateverTheZoneOfNow(t *testing.T) {
	newYork := mustLoad(t, "America/New_York")
	// 10:00 UTC on 2026-08-21 is 06:00 in New York.
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC).In(newYork)

	next, err := NextRun("0 4 * * *", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Errorf("next run = %s, want %s: 04:00 UTC, not 04:00 in the zone of now", next.UTC(), want)
	}

	prev, err := PrevRun("0 4 * * *", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 8, 21, 4, 0, 0, 0, time.UTC); !prev.Equal(want) {
		t.Errorf("previous run = %s, want %s", prev.UTC(), want)
	}

	// It fired at 04:00 UTC today. Read in New York time, the due tick would be
	// 04:00 New York (08:00 UTC), after the run, and the job would read missed.
	last := &LastRun{StartedAt: time.Date(2026, 8, 21, 4, 0, 5, 0, time.UTC)}
	if got, _ := Health("0 4 * * *", "", false, last, now); got != VerdictOK {
		t.Errorf("Health = %q for a job that fired at 04:00 UTC, asked from New York; want %q", got, VerdictOK)
	}
}

// TestACronTZPrefixIsKeptWhenNoZoneIsDeclared: older clusters admitted a
// `CRON_TZ=` prefix in spec.schedule. With no spec.timeZone, that prefix is the
// zone, and defaulting to UTC must not override it.
func TestACronTZPrefixIsKeptWhenNoZoneIsDeclared(t *testing.T) {
	tokyo := mustLoad(t, "Asia/Tokyo")
	base := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	got, err := NextRun("CRON_TZ=Asia/Tokyo 0 4 * * *", "", base)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 8, 22, 4, 0, 0, 0, tokyo); !got.Equal(want) {
		t.Errorf("next run = %s, want %s (04:00 Tokyo from the prefix)", got, want)
	}
}

// TestHealthSinceDoesNotBlameACronJobForBeforeItExisted: a daily job created
// this afternoon, never run, has missed nothing: its last due tick predates it.
func TestHealthSinceDoesNotBlameACronJobForBeforeItExisted(t *testing.T) {
	now := time.Date(2026, 8, 21, 15, 0, 0, 0, time.UTC)
	createdAfterTick := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	if got, _ := HealthSince("0 4 * * *", "", false, nil, createdAfterTick, now); got != VerdictOK {
		t.Errorf("HealthSince = %q for a never-run CronJob created after its last tick, want %q", got, VerdictOK)
	}
	// Created before the tick and still never run: that tick was missed.
	createdBeforeTick := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	if got, _ := HealthSince("0 4 * * *", "", false, nil, createdBeforeTick, now); got != VerdictMissed {
		t.Errorf("HealthSince = %q for a never-run CronJob that existed at its tick, want %q", got, VerdictMissed)
	}
	// An unknown creation time keeps Health's answer.
	if got, _ := HealthSince("0 4 * * *", "", false, nil, time.Time{}, now); got != VerdictMissed {
		t.Errorf("HealthSince with no creation time = %q, want %q (Health's answer)", got, VerdictMissed)
	}
}
