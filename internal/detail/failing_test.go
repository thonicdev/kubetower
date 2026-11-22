package detail

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/resources"
)

// A failing object says why on its own page: a Job's
// conditions, counts and backoffLimit; a pod's Phase pill not green while it is
// not Ready; a controller's events with those of what it owns.

func factNamed(facts []Fact, label string) (Fact, bool) {
	for _, f := range facts {
		if f.Label == label {
			return f, true
		}
	}
	return Fact{}, false
}

var (
	jobGVK        = schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
	podGVK        = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	deploymentGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
)

// A Job that ran out of retries, as `kubectl get job -o yaml` shows it on
// docker-desktop after `command: [sh, -c, exit 1]` with `backoffLimit: 3`.
const failedJob = `
apiVersion: batch/v1
kind: Job
metadata:
  name: migrate
  namespace: shop
  creationTimestamp: "2026-10-23T09:00:00Z"
spec:
  backoffLimit: 3
  completions: 1
  parallelism: 1
status:
  active: 0
  ready: 0
  failed: 4
  terminating: 0
  startTime: "2026-10-23T09:00:00Z"
  conditions:
  - type: FailureTarget
    status: "True"
    reason: BackoffLimitExceeded
    message: Job has reached the specified backoff limit
    lastTransitionTime: "2026-10-23T09:02:10Z"
  - type: Failed
    status: "True"
    reason: BackoffLimitExceeded
    message: Job has reached the specified backoff limit
    lastTransitionTime: "2026-10-23T09:02:10Z"
`

func TestAFailedJobSaysWhyItsCountsAndItsBackoffLimit(t *testing.T) {
	facts := factsOf(object(t, failedJob), jobGVK)

	failed, ok := factNamed(facts, "Failed")
	if !ok {
		t.Fatalf("no Failed condition on the panel - `Failed: True` is the sentence a failed "+
			"run exists to say, and the generic rule kept only conditions that are not True: %+v", facts)
	}
	if failed.Class != resources.ClassBad || !strings.Contains(failed.Value, "BackoffLimitExceeded") ||
		!strings.Contains(failed.Value, "specified backoff limit") {
		t.Errorf("Failed = %+v, want it bad, with its reason and message", failed)
	}
	if !failed.Sentence {
		t.Error("a condition message is prose and must be allowed to wrap")
	}
	if _, ok := factNamed(facts, "FailureTarget"); !ok {
		t.Error("FailureTarget is dropped; every condition the Job carries is drawn")
	}

	if got, _ := factNamed(facts, "Completions"); got.Value != "0/1" {
		t.Errorf("Completions = %q, want 0/1", got.Value)
	}
	if got, _ := factNamed(facts, "Parallelism"); got.Value != "1" {
		t.Errorf("Parallelism = %q, want 1", got.Value)
	}
	pods, _ := factNamed(facts, "Pods")
	if pods.Value != "0 active (0 ready), 0 succeeded, 4 failed" {
		t.Errorf("Pods = %q, want every count kubectl shows, zeros included", pods.Value)
	}
	backoff, ok := factNamed(facts, "Backoff limit")
	if !ok {
		t.Fatal("no Backoff limit on the panel")
	}
	if !strings.Contains(backoff.Value, "3 retries") || !strings.Contains(backoff.Value, "4 failed") ||
		!strings.Contains(backoff.Value, "exceeded") || backoff.Class != resources.ClassBad {
		t.Errorf("Backoff limit = %+v, want the limit, the failures that spent it, and that it is exceeded", backoff)
	}
}

func TestACompleteJobIsGreenAndAResumedOneIsPlain(t *testing.T) {
	job := object(t, `
apiVersion: batch/v1
kind: Job
metadata: {name: report, namespace: shop}
spec: {backoffLimit: 6, completions: 3, parallelism: 2}
status:
  succeeded: 3
  ready: 0
  startTime: "2026-10-23T09:00:00Z"
  completionTime: "2026-10-23T09:01:00Z"
  conditions:
  - {type: Suspended, status: "False", reason: JobResumed}
  - {type: SuccessCriteriaMet, status: "True", reason: CompletionsReached}
  - {type: Complete, status: "True", reason: CompletionsReached}
`)
	facts := factsOf(job, jobGVK)
	if got, _ := factNamed(facts, "Complete"); got.Class != resources.ClassOK {
		t.Errorf("Complete = %+v, want ok", got)
	}
	suspended, ok := factNamed(facts, "Suspended")
	if !ok || suspended.Class != resources.ClassNone {
		t.Errorf("Suspended False = %+v, want it drawn plain: a state the Job left, not a complaint", suspended)
	}
	if got, _ := factNamed(facts, "Completions"); got.Value != "3/3" {
		t.Errorf("Completions = %q, want 3/3", got.Value)
	}
	if got, _ := factNamed(facts, "Backoff limit"); got.Class != resources.ClassNone {
		t.Errorf("Backoff limit = %+v on a Job that never failed, want no tone", got)
	}
}

// The pod phase pill. Every case is a real `status` a kubelet writes.
func TestThePodPhasePillIsNotGreenWhileThePodIsNotReady(t *testing.T) {
	for name, c := range map[string]struct {
		doc  string
		want string
	}{
		"running and ready": {want: resources.ClassOK, doc: `
status:
  phase: Running
  conditions: [{type: Ready, status: "True"}]
  containerStatuses: [{name: api, ready: true, state: {running: {}}}]`},
		// Restarted after an OOM kill and not ready yet: phase Running, and
		// the container card beside it says OOMKilled.
		"running after an OOM kill, not ready": {want: resources.ClassWarn, doc: `
status:
  phase: Running
  conditions: [{type: Ready, status: "False", reason: ContainersNotReady}]
  containerStatuses:
  - name: api
    ready: false
    restartCount: 3
    state: {running: {}}
    lastState: {terminated: {reason: OOMKilled, exitCode: 137}}`},
		"running and crash-looping": {want: resources.ClassBad, doc: `
status:
  phase: Running
  conditions: [{type: Ready, status: "False"}]
  containerStatuses:
  - name: api
    ready: false
    state: {waiting: {reason: CrashLoopBackOff}}`},
		"running with a container just OOMKilled": {want: resources.ClassBad, doc: `
status:
  phase: Running
  conditions: [{type: Ready, status: "False"}]
  containerStatuses:
  - name: api
    ready: false
    state: {terminated: {reason: OOMKilled, exitCode: 137}}`},
		// A finished pod is never Ready, and that is not a fault.
		"succeeded": {want: resources.ClassOK, doc: `
status:
  phase: Succeeded
  conditions: [{type: Ready, status: "False", reason: PodCompleted}]
  containerStatuses:
  - name: job
    ready: false
    state: {terminated: {reason: Completed, exitCode: 0}}`},
	} {
		t.Run(name, func(t *testing.T) {
			pod := object(t, "apiVersion: v1\nkind: Pod\nmetadata: {name: api-0, namespace: shop}\n"+c.doc)
			phase, ok := factNamed(factsOf(pod, podGVK), "Phase")
			if !ok {
				t.Fatal("no Phase fact")
			}
			if phase.Class != c.want {
				t.Errorf("Phase %q is %q, want %q", phase.Value, phase.Class, c.want)
			}
		})
	}
}

// The fixtures of a controller's family, on the fake API server.
var familyAt = metav1.NewTime(time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC))

func controlledByRef(apiVersion, kind, name string, uid types.UID) []metav1.OwnerReference {
	yes := true
	return []metav1.OwnerReference{{APIVersion: apiVersion, Kind: kind, Name: name, UID: uid, Controller: &yes}}
}

func eventAbout(name, kind, about string, uid types.UID, reason string, minute int) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: "shop"},
		InvolvedObject: corev1.ObjectReference{Kind: kind, Name: about, Namespace: "shop", UID: uid},
		Reason:         reason, Type: "Warning", Message: reason,
		LastTimestamp: metav1.NewTime(familyAt.Add(time.Duration(minute) * time.Minute)),
	}
}

const apiDeployment = `
apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: shop, uid: dep-1}
spec:
  selector: {matchLabels: {app: api}}
`

// lists counts the list calls the fake answered, by resource.
func lists(api *fake.Clientset) map[string]int {
	out := map[string]int{}
	for _, action := range api.Actions() {
		if action.GetVerb() == "list" {
			out[action.GetResource().Resource]++
		}
	}
	return out
}

// A Deployment's events panel carries its ReplicaSets' and its pods' events,
// each naming the object it is about - and not those of a pod with the same
// labels that another controller owns. And it costs a fixed number of calls.
func TestADeploymentReadsTheEventsOfItsReplicaSetsAndPods(t *testing.T) {
	labels := map[string]string{"app": "api"}
	api := fake.NewClientset(
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "api-7d9f", Namespace: "shop", UID: "rs-1",
			Labels: labels, OwnerReferences: controlledByRef("apps/v1", "Deployment", "api", "dep-1"), CreationTimestamp: familyAt}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-7d9f-x2k", Namespace: "shop", UID: "pod-1",
			Labels: labels, OwnerReferences: controlledByRef("apps/v1", "ReplicaSet", "api-7d9f", "rs-1"), CreationTimestamp: familyAt}},
		// Same labels, another controller: its events are not this Deployment's.
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "impostor", Namespace: "shop", UID: "pod-2",
			Labels: labels, OwnerReferences: controlledByRef("apps/v1", "ReplicaSet", "other", "rs-2"), CreationTimestamp: familyAt}},
		eventAbout("e1", "ReplicaSet", "api-7d9f", "rs-1", "FailedCreate", 1),
		eventAbout("e2", "Pod", "api-7d9f-x2k", "pod-1", "BackOff", 3),
		eventAbout("e3", "Pod", "impostor", "pod-2", "Unhealthy", 4),
	)
	r := &Reader{gate: accesstest.Allow()}
	client := &cluster.Client{Context: "test", Clients: api}

	events, note := r.events(context.Background(), client, object(t, apiDeployment), deploymentGVK)
	if note != "" {
		t.Errorf("note = %q on a family whose every read succeeded", note)
	}
	reasons := map[string]*Owned{}
	for _, e := range events {
		reasons[e.Reason] = e.About
	}
	if _, ok := reasons["Unhealthy"]; ok {
		t.Error("the panel carries the events of a pod another controller owns: the selector " +
			"narrows the read, and only the owner reference may decide")
	}
	rs, ok := reasons["FailedCreate"]
	if !ok || rs == nil || rs.Kind != "ReplicaSet" || rs.Resource != "replicasets" || rs.Name != "api-7d9f" {
		t.Errorf("the ReplicaSet's FailedCreate is missing or unattributed: %+v", rs)
	}
	pod, ok := reasons["BackOff"]
	if !ok || pod == nil || pod.Kind != "Pod" || pod.Resource != "pods" || pod.Name != "api-7d9f-x2k" {
		t.Errorf("the pod's BackOff is missing or unattributed: %+v", pod)
	}
	if len(events) == 2 && !events[0].Last.After(events[1].Last) {
		t.Error("the merged list is not newest first")
	}

	// The cost, which runs on every drawer open: the object's own events, one
	// namespace events read for the whole family, one ReplicaSet list, one pod
	// list - not one events list per owned object.
	if got := lists(api); got["events"] != 2 || got["replicasets"] != 1 || got["pods"] != 1 {
		t.Errorf("list calls = %v, want events 2, replicasets 1, pods 1", got)
	}
}

// A Deployment keeps ten old ReplicaSets. None of them is dropped: the events
// read costs the same for ten UIDs as for one, so there is no cap to admit.
func TestEveryReplicaSetOfADeploymentIsRead(t *testing.T) {
	objects := []runtime.Object{}
	for i := range 10 {
		name := fmt.Sprintf("api-%d", i)
		uid := types.UID("rs-" + fmt.Sprint(i))
		objects = append(objects,
			&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: uid,
				Labels:            map[string]string{"app": "api"},
				OwnerReferences:   controlledByRef("apps/v1", "Deployment", "api", "dep-1"),
				CreationTimestamp: metav1.NewTime(familyAt.Add(time.Duration(i) * time.Hour))}},
			eventAbout("e"+fmt.Sprint(i), "ReplicaSet", name, uid, "SuccessfulCreate", i))
	}
	api := fake.NewClientset(objects...)
	r := &Reader{gate: accesstest.Allow()}
	events, note := r.events(context.Background(), &cluster.Client{Context: "test", Clients: api},
		object(t, apiDeployment), deploymentGVK)
	if len(events) != 10 {
		t.Errorf("%d events for ten ReplicaSets, want 10: an owned object was dropped", len(events))
	}
	if note != "" {
		t.Errorf("note = %q, want none: nothing was left out", note)
	}
	if got := lists(api); got["events"] != 2 {
		t.Errorf("%d events lists for ten ReplicaSets, want 2", got["events"])
	}
}

// A namespace with more events than one read takes is said, and what was read
// is still newest first.
func TestANamespaceWithMoreEventsThanOneReadTakesIsSaid(t *testing.T) {
	api := fake.NewClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-0", Namespace: "shop", UID: "pod-1",
			Labels: map[string]string{"app": "api"}, OwnerReferences: controlledByRef("apps/v1", "StatefulSet", "api", "sts-1")}},
	)
	// Every page of the namespace's events says there is another.
	api.PrependReactor("list", "events", func(action k8stesting.Action) (bool, runtime.Object, error) {
		list := action.(k8stesting.ListActionImpl)
		if list.GetListOptions().FieldSelector != "" {
			return true, &corev1.EventList{}, nil
		}
		return true, &corev1.EventList{
			ListMeta: metav1.ListMeta{Continue: "more"},
			Items: []corev1.Event{
				*eventAbout("old", "Pod", "api-0", "pod-1", "Pulled", 1),
				*eventAbout("new", "Pod", "api-0", "pod-1", "BackOff", 9),
			},
		}, nil
	})
	statefulSet := object(t, `
apiVersion: apps/v1
kind: StatefulSet
metadata: {name: api, namespace: shop, uid: sts-1}
spec:
  selector: {matchLabels: {app: api}}
`)
	r := &Reader{gate: accesstest.Allow()}
	events, note := r.events(context.Background(), &cluster.Client{Context: "test", Clients: api},
		statefulSet, schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"})
	if !strings.Contains(note, "more than 2000 events") {
		t.Errorf("note = %q, want it to say the namespace's events were cut", note)
	}
	if len(events) == 0 || events[0].Reason != "BackOff" {
		t.Errorf("events = %+v, want the newest first", events)
	}
	if got := lists(api); got["events"] != 1+eventsPages {
		t.Errorf("%d events lists, want the object's own and %d pages", got["events"], eventsPages)
	}
}

// A CronJob's Jobs' pods are found in one list, through the label each Job's
// selector pins to its UID.
func TestACronJobReadsItsJobsPodsInOneList(t *testing.T) {
	job := func(name string, uid types.UID) *batchv1.Job {
		return &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: uid,
				OwnerReferences: controlledByRef("batch/v1", "CronJob", "nightly", "cj-1")},
			Spec: batchv1.JobSpec{Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"batch.kubernetes.io/controller-uid": string(uid)}}},
		}
	}
	pod := func(name string, job string, uid types.UID) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: types.UID("p-" + name),
			Labels:          map[string]string{"batch.kubernetes.io/controller-uid": string(uid)},
			OwnerReferences: controlledByRef("batch/v1", "Job", job, uid)}}
	}
	api := fake.NewClientset(
		job("nightly-1", "job-1"), job("nightly-2", "job-2"),
		pod("nightly-1-a", "nightly-1", "job-1"), pod("nightly-2-a", "nightly-2", "job-2"),
		eventAbout("e1", "Pod", "nightly-1-a", "p-nightly-1-a", "BackOff", 1),
		eventAbout("e2", "Job", "nightly-2", "job-2", "BackoffLimitExceeded", 2),
	)
	cronJob := object(t, `
apiVersion: batch/v1
kind: CronJob
metadata: {name: nightly, namespace: shop, uid: cj-1}
spec: {schedule: "0 4 * * *"}
`)
	r := &Reader{gate: accesstest.Allow()}
	events, _ := r.events(context.Background(), &cluster.Client{Context: "test", Clients: api},
		cronJob, schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJob"})
	if len(events) != 2 {
		t.Errorf("%d events, want the pod's BackOff and the Job's BackoffLimitExceeded: %+v", len(events), events)
	}
	if got := lists(api); got["pods"] != 1 {
		t.Errorf("%d pod lists for two Jobs, want 1", got["pods"])
	}
	selector := jobPodSelector([]metav1.Object{job("a", "job-1"), job("b", "job-2")}, uidSet("job-1", "job-2")).String()
	if selector != "batch.kubernetes.io/controller-uid in (job-1,job-2)" {
		t.Errorf("selector = %q, want one set-based requirement", selector)
	}
}

// One empty state for the whole family - and when what it owns could not be
// read, the sentence does not claim the family is quiet.
func TestAQuietDeploymentHasOneEmptyStateAndARefusalIsSaid(t *testing.T) {
	deployment := object(t, apiDeployment)
	client := &cluster.Client{Context: "test", Clients: fake.NewClientset()}

	quiet := &Reader{gate: accesstest.Allow()}
	events, note := quiet.events(context.Background(), client, deployment, deploymentGVK)
	if len(events) != 0 {
		t.Fatalf("%d events on an empty cluster", len(events))
	}
	if note != "" {
		t.Errorf("note = %q, want none: a quiet object is the panel's own empty state", note)
	}

	refused := &Reader{gate: (&accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		return q.Attributes.Resource != "replicasets", nil
	}}).Gate()}
	_, note = refused.events(context.Background(), client, deployment, deploymentGVK)
	if !strings.Contains(note, "could not be read") {
		t.Errorf("note = %q, want it to say the owned objects' events could not be read", note)
	}
	if strings.Contains(note, "quiet") || strings.Contains(note, "or the ReplicaSets and pods it owns.") {
		t.Errorf("note = %q: what it owns was not read, so the family is not known to be quiet", note)
	}
	if !strings.Contains(note, "no events of its own") {
		t.Errorf("note = %q, want it to say what is known: the Deployment's own events are none", note)
	}
}

// With a per-index budget the Job-wide backoffLimit is the API server's
// MaxInt32 default, and drawing it would be a number the Job does not act on.
func TestAPerIndexJobDrawsNoJobWideBackoffLimit(t *testing.T) {
	facts := factsOf(object(t, `
apiVersion: batch/v1
kind: Job
metadata: {name: shards, namespace: shop}
spec: {backoffLimit: 2147483647, backoffLimitPerIndex: 1, completionMode: Indexed, completions: 4}
status: {failed: 2, failedIndexes: "1,3", startTime: "2026-10-23T09:00:00Z"}
`), jobGVK)
	if got, ok := factNamed(facts, "Backoff limit"); ok {
		t.Errorf("Backoff limit = %+v on a per-index Job", got)
	}
	got, ok := factNamed(facts, "Backoff limit per index")
	if !ok || !strings.Contains(got.Value, "1 retries per index") || !strings.Contains(got.Value, "1,3") {
		t.Errorf("Backoff limit per index = %+v", got)
	}
}

// A pod owns nothing: its events panel is its own only.
func TestAPodsEventsAreItsOwn(t *testing.T) {
	if ownedWords(podGVK) != "" {
		t.Error("a pod is read as a controller")
	}
}
