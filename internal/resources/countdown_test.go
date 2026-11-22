package resources

// The Next run column on the CronJobs list.
//
// Two things are being pinned and only one of them is arithmetic. The first is
// that the zone is honored, because `spec.timeZone` is a pointer and forgetting
// it does not crash — it reports a CronJob in Asia/Tokyo nine hours out, every
// day, plausibly. The second is that a schedule which will not resolve gets **no
// countdown at all**: a wrong countdown is worse than an absent one, and a
// column that quietly invents a number for a typo is a column that stops being
// read.

import (
	"context"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	// The IANA database, for the same reason internal/launch imports it: Windows
	// has no /usr/share/zoneinfo and the Alpine runtime does not install one, so
	// without this every zoned CronJob resolves to nothing — silently, and only
	// on the CronJobs that set a zone.
	_ "time/tzdata"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

func cronJob(name, schedule string, zone *string, suspend bool) *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "batch"},
		Spec: batchv1.CronJobSpec{
			Schedule: schedule,
			TimeZone: zone,
			Suspend:  &suspend,
		},
	}
}

// cronTable lists at a fixed clock, which is the only way a countdown is
// testable at all. build() takes the clock; List does not, so the rows go
// through it directly.
func cronTable(t *testing.T, jobs ...*batchv1.CronJob) Table {
	t.Helper()
	objects := make([]runtime.Object, 0, len(jobs))
	for _, j := range jobs {
		objects = append(objects, j)
	}
	table, err := List(context.Background(), accesstest.Allow(), client(objects...), Query{Kind: "cronjobs"}, nil)
	if err != nil {
		t.Fatalf("listing cronjobs: %v", err)
	}
	return table
}

// The countdown against a clock we hold, which means calling the row function
// rather than the listing: List reads time.Now, and a test that did too would
// assert nothing.
func countdown(t *testing.T, job *batchv1.CronJob, now time.Time) Cell {
	t.Helper()
	cells := cronJobRow(job, now)
	return cells[columnIndex(t, "cronjobs", "Next run")]
}

func columnIndex(t *testing.T, kind, title string) int {
	t.Helper()
	for i, c := range ColumnsOf(kind) {
		if c.Title == title {
			return i
		}
	}
	t.Fatalf("%s has no %q column", kind, title)
	return -1
}

func TestTheCountdownIsResolvedInTheCronJobsOwnTimeZone(t *testing.T) {
	// 12:00 UTC is 21:00 in Tokyo, so `0 4 * * *` is 16 hours away read in UTC
	// and 7 hours away read where the CronJob says to read it.
	tokyo := "Asia/Tokyo"

	utc := countdown(t, cronJob("nightly", "0 4 * * *", nil, false), clock)
	if utc.Text != "in 16h" {
		t.Errorf("with no zone the countdown is %q, want \"in 16h\"", utc.Text)
	}
	// The same instant, held by a console whose zone is New York: a CronJob
	// with no zone is still UTC's, not the console's.
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	local := countdown(t, cronJob("nightly", "0 4 * * *", nil, false), clock.In(newYork))
	if local.Text != "in 16h" {
		t.Errorf("with no zone, asked from New York, the countdown is %q, want \"in 16h\": "+
			"the console's own zone leaked into a CronJob that declares none", local.Text)
	}

	zoned := countdown(t, cronJob("nightly", "0 4 * * *", &tokyo, false), clock)
	if zoned.Text != "in 7h" {
		t.Errorf("with spec.timeZone %q the countdown is %q, want \"in 7h\". Reading the "+
			"expression in the console's zone reports a Tokyo CronJob nine hours out, every "+
			"day, and the number looks entirely reasonable", tokyo, zoned.Text)
	}
	// The hint names the zone, because "in 7h" against `0 4 * * *` reads as a
	// contradiction until the row says which 04:00 it means.
	if !strings.Contains(zoned.Hint, tokyo) {
		t.Errorf("hint = %q, want it to name %q", zoned.Hint, tokyo)
	}
}

// The absence, which is the whole design. Each of these is a real CronJob a
// cluster will accept at admission.
func TestAScheduleThatWillNotResolveGetsNoCountdown(t *testing.T) {
	bad := "Not/AZone"
	for name, job := range map[string]*batchv1.CronJob{
		"an expression that does not parse": cronJob("typo", "0 4 * *", nil, false),
		"a zone that is not an IANA name":   cronJob("zoned", "0 4 * * *", &bad, false),
		// Kubernetes accepts this at admission and the CronJob simply never
		// runs. robfig answers the zero time for it.
		"a date the calendar does not have": cronJob("never", "0 0 30 2 *", nil, false),
		"an empty schedule":                 cronJob("blank", "", nil, false),
	} {
		t.Run(name, func(t *testing.T) {
			cell := countdown(t, job, clock)
			if cell.Text != Missing {
				t.Errorf("Next run = %q, want an em dash. A countdown that cannot be "+
					"computed must be absent, not guessed: this is the rule the whole "+
					"schedule package is built on, and a plausible wrong number is worse "+
					"than a blank because nobody checks it", cell.Text)
			}
			if cell.Value != 0 {
				t.Errorf("Value = %v on a cell that says nothing; sorting would place it "+
					"among the resolvable ones", cell.Value)
			}
			if cell.Gauge != nil {
				t.Errorf("an unresolvable schedule drew a gauge")
			}
		})
	}
}

// Numeric column, so it compares by Value and not by the string — "in 9m" sorts
// above "in 10m" alphabetically, and that is precisely backwards.
func TestTheCountdownSortsByTimeAndNotByItsText(t *testing.T) {
	// Nine minutes away and ten. Chosen so the two readings sort the *wrong* way
	// round as strings — "in 9m" is above "in 10m" in any alphabet — which is
	// what makes this a test of Value rather than a coincidence.
	soon := countdown(t, cronJob("soon", "9,19,29,39,49,59 * * * *", nil, false), clock)
	later := countdown(t, cronJob("later", "*/10 * * * *", nil, false), clock)

	if soon.Text != "in 9m" || later.Text != "in 10m" {
		t.Fatalf("readings are %q and %q, want \"in 9m\" and \"in 10m\" — the premise of "+
			"this test is that the two strings sort backwards", soon.Text, later.Text)
	}
	if !(soon.Text > later.Text) {
		t.Fatalf("%q does not sort above %q, so this test no longer demonstrates anything",
			soon.Text, later.Text)
	}
	if !(soon.Value > 0 && soon.Value < later.Value) {
		t.Fatalf("Values are %v and %v; the column is Numeric, so it compares by Value, "+
			"and the nearer run has to be the smaller number", soon.Value, later.Value)
	}
}

// No next run while suspended. A countdown with the suspension only in its
// hint would be read on its own: "in 16h" on a CronJob that will not run in
// 16h is the column stating something false. The time it would run at, were it
// resumed, stays in the
// hint, because that is what somebody about to resume it wants to know.
func TestASuspendedCronJobHasNoNextRun(t *testing.T) {
	cell := countdown(t, cronJob("paused", "0 4 * * *", nil, true), clock)
	if cell.Text != Missing {
		t.Errorf("Next run = %q on a suspended CronJob, want an em dash: nothing will run", cell.Text)
	}
	if cell.Value != 0 {
		t.Errorf("Value = %v on a cell that says nothing; sorting would place it among the "+
			"CronJobs that will run", cell.Value)
	}
	if !strings.Contains(cell.Hint, "spec.suspend") || !strings.Contains(cell.Hint, "in 16h") {
		t.Errorf("hint = %q, want it to say why there is no next run and when it would run "+
			"if resumed", cell.Hint)
	}
}

// The Last result column: how the latest run ended, from the CronJob's own
// status, because "3h ago" in Last run reads the same whether every run fails or
// every run works.
func TestTheLastResultIsReadFromTheCronJobsStatus(t *testing.T) {
	at := func(d time.Duration) *metav1.Time { m := metav1.NewTime(clock.Add(d)); return &m }
	for name, c := range map[string]struct {
		scheduled, succeeded *metav1.Time
		active               int
		text, class          string
	}{
		"never scheduled":               {text: Missing},
		"the latest run succeeded":      {scheduled: at(-time.Hour), succeeded: at(-55 * time.Minute), text: "Succeeded", class: ClassOK},
		"the latest run is still going": {scheduled: at(-time.Minute), succeeded: at(-55 * time.Minute), active: 1, text: "Running"},
		// Overlapping runs: an older run succeeded after the newest was
		// scheduled, and the newest is still going. Not "Succeeded".
		"an older run succeeded while the latest still runs": {scheduled: at(-10 * time.Minute), succeeded: at(-5 * time.Minute), active: 1, text: "Running"},
		"the latest run ended and did not succeed":           {scheduled: at(-time.Hour), succeeded: at(-25 * time.Hour), text: "Failed", class: ClassBad},
		"no run ever succeeded":                              {scheduled: at(-time.Hour), text: "Failed", class: ClassBad},
	} {
		t.Run(name, func(t *testing.T) {
			job := cronJob("nightly", "0 4 * * *", nil, false)
			job.Status.LastScheduleTime = c.scheduled
			job.Status.LastSuccessfulTime = c.succeeded
			for range c.active {
				job.Status.Active = append(job.Status.Active, corev1.ObjectReference{Name: "nightly-1"})
			}
			cell := cronJobRow(job, clock)[columnIndex(t, "cronjobs", "Last result")]
			if cell.Text != c.text || cell.Class != c.class {
				t.Errorf("Last result = %q (%q), want %q (%q)", cell.Text, cell.Class, c.text, c.class)
			}
			if cell.Text == "Failed" && !strings.Contains(cell.Hint, "deleted") {
				t.Errorf("hint = %q: the status cannot tell a failed run from a deleted Job, "+
					"and the hint has to say both", cell.Hint)
			}
		})
	}
}

// spec.timeZone is a pointer, and the nil check is the whole of CronJobSchedule.
func TestCronJobScheduleReadsTheZoneWithoutDereferencingNothing(t *testing.T) {
	zone := "Europe/Paris"
	for name, c := range map[string]struct {
		job        *batchv1.CronJob
		expr, want string
	}{
		"no zone declared":  {cronJob("a", "@hourly", nil, false), "@hourly", ""},
		"a zone declared":   {cronJob("b", "@daily", &zone, false), "@daily", zone},
		"no CronJob at all": {nil, "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			expr, got := CronJobSchedule(c.job)
			if expr != c.expr || got != c.want {
				t.Errorf("CronJobSchedule = (%q, %q), want (%q, %q)", expr, got, c.expr, c.want)
			}
		})
	}
}

// The column exists on the kind and the row fills it. A descriptor whose columns
// and cells drift renders perfectly with every value one column to the left.
func TestTheCronJobsListCarriesTheCountdownColumn(t *testing.T) {
	table := cronTable(t, cronJob("nightly", "*/5 * * * *", nil, false))
	cell := cellOf(t, table, "nightly", "Next run")
	if cell.Text == Missing {
		t.Fatalf("Next run is an em dash for `*/5 * * * *`, which resolves in any zone")
	}
	if !strings.HasPrefix(cell.Text, "in ") {
		t.Errorf("Next run = %q, want a countdown reading \"in …\"", cell.Text)
	}
}
