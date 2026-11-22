package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// --- a pod that is not running yet ---------------------------------------------

// startingPod is an apiserver serving one pod whose container goes through the
// states in script, one per look at the pod, and stays on the last. A log read
// sees the state the last look saw, or the first before any look.
//
// The log endpoint answers as the real one does from the same status: a
// container waiting to start is refused with a 400 carrying the kubelet's
// sentence, and a running one is served its lines. A fake clientset cannot
// stand in here - its GetLogs answers "fake logs" whatever the pod says, so the
// refusal this whole feature turns on could not be produced - which is why it is
// a stand-in apiserver, as everywhere else in this package.
type startingPod struct {
	t      *testing.T
	server *httptest.Server
	lines  []string

	mu     sync.Mutex
	script []corev1.ContainerState
	looks  int
	reads  int
	// gone answers the pod as deleted from this look on, counted from 1; zero
	// is never.
	gone int
	// noNode is how many looks see the pod before any node has taken it:
	// Pending, no node, the scheduler's condition false, and a log read
	// answered with the API server's empty 204. script starts after them.
	noNode int
}

// stage is what the look counted from 0 sees: no node yet, or the container's
// state.
func (p *startingPod) stage(look int) (bool, corev1.ContainerState) {
	look = max(look, 0) // a log read before any look sees the first
	if look < p.noNode {
		return true, corev1.ContainerState{}
	}
	return false, p.at(look - p.noNode)
}

func newStartingPod(t *testing.T, lines []string, script ...corev1.ContainerState) *startingPod {
	t.Helper()
	p := &startingPod{t: t, lines: lines, script: script}
	p.server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.server.Close)
	return p
}

func waitingFor(reason, message string) corev1.ContainerState {
	return corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: message}}
}

func runningNow() corev1.ContainerState {
	return corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
}

// at is the container's state at the look counted from 0.
func (p *startingPod) at(look int) corev1.ContainerState {
	return p.script[min(max(look, 0), len(p.script)-1)]
}

func (p *startingPod) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/pods/p/log"):
		p.reads++
		noNode, state := p.stage(p.looks - 1)
		deleted := p.gone > 0 && p.looks >= p.gone
		p.mu.Unlock()
		switch {
		case deleted:
			answerStatus(w, http.StatusNotFound, metav1.StatusReasonNotFound, `pods "p" not found`)
		case noNode:
			// No kubelet to ask: the API server answers with nothing at all.
			w.WriteHeader(http.StatusNoContent)
		case state.Waiting != nil:
			answerStatus(w, http.StatusBadRequest, metav1.StatusReasonBadRequest,
				fmt.Sprintf(`container "app" in pod "p" is waiting to start: %s`, state.Waiting.Reason))
		default:
			for _, line := range p.lines {
				_, _ = fmt.Fprintln(w, line)
			}
		}
	case strings.HasSuffix(r.URL.Path, "/pods/p"):
		p.looks++
		noNode, state := p.stage(p.looks - 1)
		deleted := p.gone > 0 && p.looks >= p.gone
		p.mu.Unlock()
		if deleted {
			answerStatus(w, http.StatusNotFound, metav1.StatusReasonNotFound, `pods "p" not found`)
			return
		}
		pod := corev1.Pod{
			TypeMeta:   metav1.TypeMeta{Kind: "Pod", APIVersion: "v1"},
			ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "n"},
			Spec: corev1.PodSpec{NodeName: "node-1",
				Containers: []corev1.Container{{Name: "app"}}},
			Status: corev1.PodStatus{Phase: corev1.PodPending,
				ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: state}}},
		}
		if noNode {
			pod.Spec.NodeName = ""
			pod.Status.ContainerStatuses = nil
			pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled,
				Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
				Message: "0/3 nodes are available: 3 Insufficient cpu."}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pod)
	default:
		p.mu.Unlock()
		http.Error(w, "not this test's endpoint: "+r.URL.Path, http.StatusNotFound)
	}
}

// answerStatus refuses as the API server does: a Status object, which
// client-go turns back into a typed error.
func answerStatus(w http.ResponseWriter, code int, reason metav1.StatusReason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusFailure, Message: message, Reason: reason, Code: int32(code),
	})
}

func (p *startingPod) counts() (looks, reads int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.looks, p.reads
}

// streamerOn is a Streamer over the pod, looking at it every few milliseconds
// rather than every two seconds: the interval is the bound, and the subject is
// what happens between looks.
func streamerOn(t *testing.T, p *startingPod) *Streamer {
	t.Helper()
	s := New(newFakeClients(t, p.server.URL, 5*time.Second), accesstest.Allow(), nil, nil)
	s.startEvery = 5 * time.Millisecond
	return s
}

// follow runs the framed reader to its end, bounded so a wait that never ends
// fails the test rather than hanging it, and answers every event it emitted.
// during runs on each event, while the follow is still open.
func follow(t *testing.T, s *Streamer, during func(Event)) ([]Event, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got []Event
	err := s.Follow(ctx,
		Request{Cluster: "docker-desktop", Namespace: "n", Pod: "p", Container: "app", Follow: true},
		func(e Event) error {
			got = append(got, e)
			if during != nil {
				during(e)
			}
			return nil
		})
	return got, err
}

func kinds(events []Event) string {
	names := map[EventKind]string{EventLine: "line", EventEnd: "end", EventTruncated: "truncated",
		EventError: "error", EventRefused: "refused", EventWaiting: "waiting", EventNotStarted: "not-started"}
	var out []string
	for _, e := range events {
		out = append(out, names[e.Kind])
	}
	return strings.Join(out, " ")
}

// --- the wait ------------------------------------------------------------------

// A pod created and its log opened straight away. The read is refused while the
// container is created, and the pane must not sit on that refusal for good over
// a container that is printing by then. The same
// stream says it is waiting, says so again when the reason changes, and carries
// the container's lines once it runs - holding the one follow slot it took
// throughout.
func TestAFollowWaitsForItsContainerAndThenReadsItOnTheSameStream(t *testing.T) {
	p := newStartingPod(t, []string{"starting", "listening on :8080"},
		waitingFor("PodInitializing", ""),
		waitingFor("ContainerCreating", ""),
		waitingFor("ContainerCreating", ""),
		runningNow())
	s := streamerOn(t, p)

	heldWhileWaiting := -1
	got, err := follow(t, s, func(e Event) {
		if e.Kind == EventWaiting && heldWhileWaiting < 0 {
			heldWhileWaiting = s.live.open()
		}
	})
	if err != nil {
		t.Fatalf("the follow answered an error: %v", err)
	}

	if want := "waiting waiting line line end"; kinds(got) != want {
		t.Fatalf("events %q, want %q: %+v", kinds(got), want, got)
	}
	if got[0].Text != "waiting for the container to start: PodInitializing" ||
		got[1].Text != "waiting for the container to start: ContainerCreating" {
		t.Errorf("the notices are %q and %q, want each to name the status's reason, once per reason",
			got[0].Text, got[1].Text)
	}
	if got[2].Text != "starting" || got[3].Text != "listening on :8080" {
		t.Errorf("the lines are %q and %q", got[2].Text, got[3].Text)
	}
	if got[4].Text != "end of log" {
		t.Errorf("the stream ended on %q, want the ordinary end of a log that ran out", got[4].Text)
	}

	// The slot taken when the follow opened is the one it waited in: one follow,
	// one slot, released at the end.
	if heldWhileWaiting != 1 {
		t.Errorf("%d follow slot(s) held while waiting, want the 1 this follow took", heldWhileWaiting)
	}
	if n := s.live.open(); n != 0 {
		t.Errorf("%d follow(s) still open after the stream ended, want 0", n)
	}

	// Bounded: one look per interval, not a loop on the API server. Four states
	// were scripted; the follow needs four looks to reach the running one, and a
	// busy loop would have made hundreds in the same time.
	if looks, _ := p.counts(); looks > 10 {
		t.Errorf("the pod was read %d times to see four states; the wait is not paced", looks)
	}
}

// A container that is not going to start on its own ends the wait with the
// reason and what to do, rather than holding a slot for thirty minutes under a
// notice.
func TestAContainerThatWillNotStartEndsTheWaitWithASentence(t *testing.T) {
	p := newStartingPod(t, nil,
		waitingFor("ContainerCreating", ""),
		waitingFor("ImagePullBackOff", `Back-off pulling image "registry.local/api:nope"`))
	s := streamerOn(t, p)

	got, err := follow(t, s, nil)
	if err != nil {
		t.Fatalf("the follow answered an error: %v", err)
	}
	if want := "waiting not-started"; kinds(got) != want {
		t.Fatalf("events %q, want %q: %+v", kinds(got), want, got)
	}
	last := got[len(got)-1].Text
	for _, want := range []string{"ImagePullBackOff", "registry.local/api:nope", "reopen the panel"} {
		if !strings.Contains(last, want) {
			t.Errorf("the wait ended on %q, which does not carry %q", last, want)
		}
	}
	if n := s.live.open(); n != 0 {
		t.Errorf("%d follow(s) still open, want 0", n)
	}
}

// A crash loop is not a reason to stop when the container has run: its last run
// has a log, and that is what the kubelet serves. Only a container that never
// ran is not going to.
func TestACrashLoopWithAPreviousRunIsReadNotWaitedOn(t *testing.T) {
	looping := corev1.ContainerStatus{Name: "app",
		State:                waitingFor("CrashLoopBackOff", "back-off 40s restarting failed container"),
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}}
	pod := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{looping}}}
	if got := startOf(pod, "app"); !got.ready {
		t.Errorf("a crash loop with a previous run answered %+v, want its log opened", got)
	}

	looping.LastTerminationState = corev1.ContainerState{}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{looping}
	if got := startOf(pod, "app"); got.never == "" || !strings.Contains(got.never, "CrashLoopBackOff") {
		t.Errorf("a crash loop that never ran answered %+v, want the wait ended naming it", got)
	}
}

// What the status says, read the way the kubelet reads it.
func TestStartOfReadsTheStatusTheWayTheKubeletDoes(t *testing.T) {
	status := func(state corev1.ContainerState) *corev1.Pod {
		return &corev1.Pod{Spec: corev1.PodSpec{NodeName: "node-1"},
			Status: corev1.PodStatus{Phase: corev1.PodPending,
				InitContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: state}}}}
	}
	for _, c := range []struct {
		what   string
		pod    *corev1.Pod
		ready  bool
		notice string
		never  bool
	}{
		{"running", status(runningNow()), true, "", false},
		{"terminated", status(corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}), true, "", false},
		{"being created", status(waitingFor("ContainerCreating", "")), false,
			"waiting for the container to start: ContainerCreating", false},
		{"behind an init container", status(waitingFor("PodInitializing", "")), false,
			"waiting for the container to start: PodInitializing", false},
		{"a missing ConfigMap", status(waitingFor("CreateContainerConfigError", `configmap "x" not found`)),
			false, "", true},
		{"no node yet, no phase reported", &corev1.Pod{}, false,
			"waiting for the container to start: the pod is not on a node yet", false},
		{"not scheduled yet", &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending}}, false,
			"waiting for the pod to be scheduled", false},
		{"unschedulable for now, which is still worth waiting on",
			&corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending,
				Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
					Reason: "Unschedulable", Message: "0/3 nodes are available: 3 Insufficient cpu."}}}},
			false, "waiting for the pod to be scheduled: Unschedulable: 0/3 nodes are available: 3 Insufficient cpu.",
			false},
		{"a pod that failed before it started the container",
			&corev1.Pod{Spec: corev1.PodSpec{NodeName: "node-1"}, Status: corev1.PodStatus{Phase: corev1.PodFailed,
				ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: waitingFor("PodInitializing", "")}}}},
			false, "", true},
	} {
		got := startOf(c.pod, "app")
		if got.ready != c.ready || got.notice != c.notice || (got.never != "") != c.never {
			t.Errorf("%s: answered %+v, want ready %v, notice %q, an end %v",
				c.what, got, c.ready, c.notice, c.never)
		}
	}
}

// The pod going away while the follow waits is an end, said.
func TestAPodDeletedWhileItsFollowWaitsEndsTheWait(t *testing.T) {
	p := newStartingPod(t, nil, waitingFor("ContainerCreating", ""))
	p.gone = 3
	s := streamerOn(t, p)

	got, err := follow(t, s, nil)
	if err != nil {
		t.Fatalf("the follow answered an error: %v", err)
	}
	if want := "waiting not-started"; kinds(got) != want {
		t.Fatalf("events %q, want %q: %+v", kinds(got), want, got)
	}
	if text := got[1].Text; !strings.Contains(text, "deleted") {
		t.Errorf("the wait ended on %q, want it to say the pod was deleted", text)
	}
}

// A container that stays in `ContainerCreating` is bounded by the stream's own
// deadline, and reaching it while waiting says that the container never
// started rather than that the log was cut.
func TestAWaitThatOutlivesTheDeadlineSaysTheContainerNeverStarted(t *testing.T) {
	p := newStartingPod(t, nil, waitingFor("ContainerCreating", ""))
	s := streamerOn(t, p)
	s.deadline = 80 * time.Millisecond

	got, err := follow(t, s, nil)
	if err != nil {
		t.Fatalf("the follow answered an error: %v", err)
	}
	if want := "waiting end"; kinds(got) != want {
		t.Fatalf("events %q, want %q: %+v", kinds(got), want, got)
	}
	if text := got[1].Text; !strings.Contains(text, "never started") || !strings.Contains(text, "80ms") {
		t.Errorf("the wait ended on %q, want the deadline and that the container never started", text)
	}
	if n := s.live.open(); n != 0 {
		t.Errorf("%d follow(s) still open, want 0", n)
	}
}

// A fixed read does not wait: "not yet" is its answer, given at once, exactly as
// before - one log read, no look at the pod, nothing emitted, the refusal
// returned for the caller to answer as a status.
func TestAFixedReadOfAWaitingContainerAnswersAtOnce(t *testing.T) {
	p := newStartingPod(t, nil, waitingFor("ContainerCreating", ""))
	s := streamerOn(t, p)

	var got []Event
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.Follow(ctx,
		Request{Cluster: "docker-desktop", Namespace: "n", Pod: "p", Container: "app", Follow: false},
		func(e Event) error { got = append(got, e); return nil })

	if err == nil || !strings.Contains(err.Error(), "is waiting to start: ContainerCreating") {
		t.Fatalf("the fixed read answered %v, want the API server's refusal", err)
	}
	if len(got) != 0 {
		t.Errorf("the fixed read emitted %q, want nothing", kinds(got))
	}
	if looks, reads := p.counts(); looks != 0 || reads != 1 {
		t.Errorf("the fixed read looked at the pod %d time(s) and read the log %d time(s), want 0 and 1",
			looks, reads)
	}

	// And through the SSE handler, as an ordinary status, as it always was.
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/kt-api/logs?cluster=docker-desktop&namespace=n&pod=p&container=app&follow=0", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("the handler answered a fixed read of a waiting container with %d, want 400", rec.Code)
	}
}

// The SSE handler waits the same way: one 200, a `waiting` frame, then the
// lines.
func TestTheHandlerWaitsOnTheSameResponse(t *testing.T) {
	p := newStartingPod(t, []string{"ready"}, waitingFor("ContainerCreating", ""), runningNow())
	s := streamerOn(t, p)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/kt-api/logs?cluster=docker-desktop&namespace=n&pod=p&container=app", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	want := "event: waiting\ndata: waiting for the container to start: ContainerCreating\n\n" +
		"data: ready\n\n" +
		"event: end\ndata: end of log\n\n"
	if body != want {
		t.Errorf("the response is\n%q\nwant\n%q", body, want)
	}
}

// --- a pod no node has taken yet -----------------------------------------------

// The same order of work, a second earlier: the pod is created and its log
// opened before the scheduler has placed it. There is no kubelet to refuse the
// read, so the API server answers it with nothing, and the follow must not end
// on "end of log" at once. The empty end is looked behind: the
// pod has no node, so the stream waits for the scheduler, then for the
// container, and carries its lines - one stream, one slot.
func TestAFollowOpenedBeforeThePodIsScheduledWaitsAndThenReadsIt(t *testing.T) {
	p := newStartingPod(t, []string{"hello"}, waitingFor("ContainerCreating", ""), runningNow())
	p.noNode = 2
	s := streamerOn(t, p)

	heldWhileWaiting := -1
	got, err := follow(t, s, func(e Event) {
		if e.Kind == EventWaiting && heldWhileWaiting < 0 {
			heldWhileWaiting = s.live.open()
		}
	})
	if err != nil {
		t.Fatalf("the follow answered an error: %v", err)
	}
	if want := "waiting waiting line end"; kinds(got) != want {
		t.Fatalf("events %q, want %q: %+v", kinds(got), want, got)
	}
	want := "waiting for the pod to be scheduled: Unschedulable: 0/3 nodes are available: 3 Insufficient cpu."
	if got[0].Text != want {
		t.Errorf("the first notice is %q, want %q", got[0].Text, want)
	}
	if want := "waiting for the container to start: ContainerCreating"; got[1].Text != want {
		t.Errorf("the second notice is %q, want %q", got[1].Text, want)
	}
	if got[2].Text != "hello" || got[3].Text != "end of log" {
		t.Errorf("the stream carried %q then %q", got[2].Text, got[3].Text)
	}
	if heldWhileWaiting != 1 {
		t.Errorf("%d follow slot(s) held while waiting, want the 1 this follow took", heldWhileWaiting)
	}
	if n := s.live.open(); n != 0 {
		t.Errorf("%d follow(s) still open after the stream ended, want 0", n)
	}
}

// An unschedulable pod is not given up on - a node can be added - so only the
// stream's own deadline ends that wait.
func TestAnUnschedulablePodIsWaitedOnUntilTheDeadline(t *testing.T) {
	p := newStartingPod(t, nil, runningNow())
	p.noNode = 1 << 30
	s := streamerOn(t, p)
	s.deadline = 80 * time.Millisecond

	got, err := follow(t, s, nil)
	if err != nil {
		t.Fatalf("the follow answered an error: %v", err)
	}
	if want := "waiting end"; kinds(got) != want {
		t.Fatalf("events %q, want %q: %+v", kinds(got), want, got)
	}
	if !strings.HasPrefix(got[0].Text, "waiting for the pod to be scheduled") ||
		!strings.Contains(got[1].Text, "stopped waiting after 80ms") {
		t.Errorf("the wait said %q and ended on %q", got[0].Text, got[1].Text)
	}
}

// A fixed read of a pod no node has taken answers the empty log, ended, after
// one read and no look at the pod.
func TestAFixedReadOfAnUnscheduledPodIsUnchanged(t *testing.T) {
	p := newStartingPod(t, nil, runningNow())
	p.noNode = 1 << 30
	s := streamerOn(t, p)

	var got []Event
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Follow(ctx,
		Request{Cluster: "docker-desktop", Namespace: "n", Pod: "p", Container: "app", Follow: false},
		func(e Event) error { got = append(got, e); return nil }); err != nil {
		t.Fatalf("the fixed read answered %v", err)
	}
	if kinds(got) != "end" || got[0].Text != "end of log" {
		t.Errorf("the fixed read emitted %q (%+v), want the end of an empty log", kinds(got), got)
	}
	if looks, reads := p.counts(); looks != 0 || reads != 1 {
		t.Errorf("the fixed read looked at the pod %d time(s) and read the log %d time(s), want 0 and 1",
			looks, reads)
	}
}

// A follow on a container that finished having printed nothing still ends as
// it did: the pod is read once, has a node, and the end is said.
func TestAFollowOnAnEmptyFinishedLogStillEnds(t *testing.T) {
	p := newStartingPod(t, nil, corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}})
	s := streamerOn(t, p)

	got, err := follow(t, s, nil)
	if err != nil {
		t.Fatalf("the follow answered an error: %v", err)
	}
	if kinds(got) != "end" || got[0].Text != "end of log" {
		t.Errorf("the follow emitted %q (%+v), want the end of an empty log", kinds(got), got)
	}
	if looks, _ := p.counts(); looks != 1 {
		t.Errorf("the pod was read %d time(s) behind an empty end, want 1", looks)
	}
}
