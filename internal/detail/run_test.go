package detail

import (
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/resources"
)

var runClock = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

// local is a fixture's timestamp as the panel prints it: metav1.Time decodes
// into the machine's zone, as Created always has, so the expectation follows it.
func local(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		panic(err)
	}
	return t.Local().Format(time.RFC3339)
}

const completedJobDoc = `
apiVersion: batch/v1
kind: Job
metadata: {name: nightly-29350920, namespace: ops}
status:
  startTime: "2026-10-19T09:00:00Z"
  completionTime: "2026-10-19T10:40:12Z"
  succeeded: 1
  conditions:
    - {type: Complete, status: "True", lastTransitionTime: "2026-10-19T10:40:12Z"}
`

// completionTime is set on success only: a failure is dated by its condition.
const failedJobDoc = `
apiVersion: batch/v1
kind: Job
metadata: {name: nightly-29346600, namespace: ops}
status:
  startTime: "2026-10-16T12:00:00Z"
  failed: 4
  conditions:
    - {type: Failed, status: "True", lastTransitionTime: "2026-10-16T12:02:00Z"}
`

const suspendedJobDoc = `
apiVersion: batch/v1
kind: Job
metadata: {name: backfill, namespace: ops}
spec: {suspend: true}
status:
  startTime: "2026-10-19T11:00:00Z"
  conditions:
    - {type: Suspended, status: "True", lastTransitionTime: "2026-10-19T11:30:00Z"}
`

const runningJobDoc = `
apiVersion: batch/v1
kind: Job
metadata: {name: nightly-29352360, namespace: ops}
status:
  startTime: "2026-10-19T11:59:33Z"
  active: 1
`

const unstartedJobDoc = `
apiVersion: batch/v1
kind: Job
metadata: {name: held, namespace: ops}
spec: {suspend: true}
status: {}
`

// The drawer's Duration and the list's Duration column are one reading. Each
// case builds the list's cell from the same object and asserts the drawer
// leads with it; the exact figure may follow, in brackets, and nothing else.
func TestAJobsDurationInTheDrawerIsTheListsCell(t *testing.T) {
	for _, c := range []struct {
		name, doc, want, finished string
	}{
		{"completed", completedJobDoc, "1h (1h40m12s)", local("2026-10-19T10:40:12Z") + " (1h19m ago)"},
		{"failed", failedJobDoc, "2m", local("2026-10-16T12:02:00Z") + " (2d23h ago)"},
		{"suspended", suspendedJobDoc, "30m", local("2026-10-19T11:30:00Z") + " (30m ago)"},
		{"running", runningJobDoc, "27s", "running"},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw := nodeFrom(t, c.doc)
			var job batchv1.Job
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Object, &job); err != nil {
				t.Fatal(err)
			}
			cell := resources.JobDuration(&job, runClock).Text

			facts := jobFacts(jobOf(raw), runClock)
			got, ok := factValue(facts, "Duration")
			if !ok {
				t.Fatalf("no Duration row in %v", facts)
			}
			if got != c.want {
				t.Errorf("Duration = %q, want %q", got, c.want)
			}
			if got != cell && !strings.HasPrefix(got, cell+" (") {
				t.Errorf("the drawer says %q and the list says %q", got, cell)
			}
			if finished, _ := factValue(facts, "Finished"); finished != c.finished {
				t.Errorf("Finished = %q, want %q", finished, c.finished)
			}
		})
	}
}

func TestAJobSaysWhenItStarted(t *testing.T) {
	started, _ := factValue(jobFacts(jobOf(nodeFrom(t, completedJobDoc)), runClock), "Started")
	if want := local("2026-10-19T09:00:00Z") + " (3h0m ago)"; started != want {
		t.Errorf("Started = %q, want %q", started, want)
	}
}

// Suspended before it ever ran: no start, so nothing to measure, and the row
// says so rather than going missing.
func TestAJobThatNeverStartedSaysNotYetAndHasNoDuration(t *testing.T) {
	facts := jobFacts(jobOf(nodeFrom(t, unstartedJobDoc)), runClock)
	if started, _ := factValue(facts, "Started"); started != "not yet" {
		t.Errorf("Started = %q, want \"not yet\"", started)
	}
	if _, ok := factValue(facts, "Duration"); ok {
		t.Errorf("a Job that never started has a Duration row: %v", facts)
	}
}

// A CronJob's run: the pod started at 02:00:04 and its last container exited at
// 02:00:31. The init container's earlier exit is not the run's end.
const finishedJobPodDoc = `
apiVersion: v1
kind: Pod
metadata:
  name: nightly-29350920-x7k2p
  namespace: ops
  ownerReferences:
    - {apiVersion: batch/v1, kind: Job, name: nightly-29350920, uid: "1", controller: true}
status:
  phase: Succeeded
  startTime: "2026-10-19T02:00:04Z"
  initContainerStatuses:
    - name: fetch
      state: {terminated: {exitCode: 0, finishedAt: "2026-10-19T02:00:09Z"}}
  containerStatuses:
    - name: main
      state: {terminated: {exitCode: 0, finishedAt: "2026-10-19T02:00:31Z"}}
    - name: sidecar
      state: {terminated: {exitCode: 0, finishedAt: "2026-10-19T02:00:30Z"}}
`

const runningJobPodDoc = `
apiVersion: v1
kind: Pod
metadata:
  name: nightly-29352360-q9w4z
  namespace: ops
  ownerReferences:
    - {apiVersion: batch/v1, kind: Job, name: nightly-29352360, uid: "2", controller: true}
status:
  phase: Running
  startTime: "2026-10-19T11:57:00Z"
`

const deploymentPodDoc = `
apiVersion: v1
kind: Pod
metadata:
  name: web-7d9f8-abcde
  namespace: ops
  ownerReferences:
    - {apiVersion: apps/v1, kind: ReplicaSet, name: web-7d9f8, uid: "3", controller: true}
status:
  phase: Running
  startTime: "2026-09-08T12:00:00Z"
`

func TestAJobsPodSaysHowLongItsRunLasted(t *testing.T) {
	ran, ok := factValue(podRunFacts(podOf(nodeFrom(t, finishedJobPodDoc)), runClock), "Ran")
	if !ok {
		t.Fatal("a finished Job's pod has no Ran row")
	}
	if want := "27s, from " + local("2026-10-19T02:00:04Z") + " to " + local("2026-10-19T02:00:31Z") + " (9h59m ago)"; ran != want {
		t.Errorf("Ran = %q, want %q", ran, want)
	}
}

func TestARunningJobsPodSaysHowLongItHasBeenRunning(t *testing.T) {
	ran, _ := factValue(podRunFacts(podOf(nodeFrom(t, runningJobPodDoc)), runClock), "Ran")
	if want := "running for 3m, since " + local("2026-10-19T11:57:00Z"); ran != want {
		t.Errorf("Ran = %q, want %q", ran, want)
	}
}

// A Deployment's pod is meant to keep running: "ran for 41d" would read as
// something that ended.
func TestAPodNoJobOwnsHasNoRanRow(t *testing.T) {
	if facts := podRunFacts(podOf(nodeFrom(t, deploymentPodDoc)), runClock); len(facts) != 0 {
		t.Errorf("a ReplicaSet's pod got %v", facts)
	}
}

// The wiring: the generic panel carries the run's rows for a Job and a Job's
// pod, and for nothing else.
func TestTheGenericPanelCarriesTheRunsRows(t *testing.T) {
	job := factsOf(nodeFrom(t, completedJobDoc), schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"})
	for _, label := range []string{"Started", "Finished", "Duration"} {
		if _, ok := factValue(job, label); !ok {
			t.Errorf("a Job's panel has no %s row", label)
		}
	}
	pod := factsOf(nodeFrom(t, finishedJobPodDoc), schema.GroupVersionKind{Version: "v1", Kind: "Pod"})
	if _, ok := factValue(pod, "Ran"); !ok {
		t.Error("a Job's pod's panel has no Ran row")
	}
	configMap := factsOf(nodeFrom(t, configMapDoc), schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"})
	for _, label := range []string{"Started", "Finished", "Duration", "Ran"} {
		if _, ok := factValue(configMap, label); ok {
			t.Errorf("a ConfigMap's panel has a %s row", label)
		}
	}
}

func TestExactDurationPrintsEveryUnitDownToTheSecond(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                "0s",
		27 * time.Second: "27s",
		time.Hour + 40*time.Minute + 12*time.Second: "1h40m12s",
		74 * time.Hour:          "3d2h",
		1500 * time.Millisecond: "2s",
	} {
		if got := exactDuration(d); got != want {
			t.Errorf("exactDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
