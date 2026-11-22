package kubecron

import (
	"testing"
	"time"

	_ "time/tzdata"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/schedule"
)

// A CronJob with no spec.timeZone is judged in UTC, as the cluster runs it,
// even when the console's clock is in another zone, and a CronJob never run is
// not blamed for a tick that came before it existed.
func TestJudgeReadsAZonelessCronJobInUTCAndFromItsCreation(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC).In(newYork) // 06:00 in New York

	ran := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "ops",
			CreationTimestamp: metav1.NewTime(now.AddDate(0, -1, 0))},
		Spec:   batchv1.CronJobSpec{Schedule: "0 4 * * *"},
		Status: batchv1.CronJobStatus{LastScheduleTime: &metav1.Time{Time: time.Date(2026, 8, 21, 4, 0, 0, 0, time.UTC)}},
	}
	got := judge(ran, nil, now)
	if got.Verdict != schedule.VerdictOK {
		t.Errorf("verdict %q for a job that fired at 04:00 UTC, judged from New York; want %q", got.Verdict, schedule.VerdictOK)
	}
	if want := time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC); !got.NextAt.Equal(want) {
		t.Errorf("next run %s, want %s", got.NextAt.UTC(), want)
	}

	fresh := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "fresh", Namespace: "ops",
			CreationTimestamp: metav1.NewTime(time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC))},
		Spec: batchv1.CronJobSpec{Schedule: "0 4 * * *"},
	}
	if v := judge(fresh, nil, now).Verdict; v != schedule.VerdictOK {
		t.Errorf("verdict %q for a never-run CronJob created after its last tick, want %q", v, schedule.VerdictOK)
	}
}
