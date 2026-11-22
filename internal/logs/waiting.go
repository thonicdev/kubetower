package logs

// A follow opened on a container that has not started yet.
//
// People open a pod's log right after creating it. For a few seconds the
// container is `ContainerCreating`, or `PodInitializing` behind an init
// container, and the API server refuses the read with a 400 (`container "app"
// in pod "web" is waiting to start: ContainerCreating`). Reported as a final
// end, nothing would ask again once the container ran.
//
// So a follow waits on the stream it already has: it says it is waiting and
// why, looks at the pod again at a bounded interval, and opens the log as soon
// as the kubelet would serve it. The wait ends with a sentence on the stream's
// deadline, the pod going away, or a reason the container will not get past on
// its own.
//
// A fixed read does not wait: "not yet" is its answer, and holding it open
// would make it a follow that holds no slot.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/thonicdev/kubetower/textutil"
)

// startFirst and startEvery bound how often a waiting follow looks at its pod:
// a quarter of a second after the refusal, doubling to two seconds and staying
// there. Quick at first because most containers are a second or two from
// running; capped because a container held behind a slow init container can be
// waited on for the whole deadline, across up to sixteen follows.
//
// A pod GET rather than another log read: the API server answers it from its
// cache, where a log read goes on to the kubelet.
const (
	startFirst = 250 * time.Millisecond
	startEvery = 2 * time.Second
)

// waitingNote is how a waiting notice begins. The reason follows it, as the
// kubelet names it: "waiting for the container to start: ContainerCreating".
const waitingNote = "waiting for the container to start: "

// schedulingNote is the notice for a pod no node has taken yet, which comes
// before any container reason: there is no kubelet to have one.
const schedulingNote = "waiting for the pod to be scheduled"

// wontStart is the waiting reasons a container does not get past on its own.
// The kubelet keeps retrying behind most of them, but what fixes them is a
// person (a tag pushed, a Secret created), so the wait ends on the reason
// instead of holding a slot until the deadline; reopening the panel carries on.
//
// `CrashLoopBackOff` reaches this list only for a container that never ran:
// one that ran has logs, and startOf opens them before looking at why it waits,
// as the kubelet does.
var wontStart = map[string]bool{
	"ErrImagePull":               true,
	"ImagePullBackOff":           true,
	"ErrImageNeverPull":          true,
	"InvalidImageName":           true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
	"RunContainerError":          true,
	"CrashLoopBackOff":           true,
}

// errSaid is a follow that ended while waiting for its container and has
// already said why in an event. The caller has nothing to add: the stream has
// started, so no status can be sent.
var errSaid = errors.New("the log stream ended while waiting for its container, and said why")

// start opens req's log and, for a follow whose container is waiting to
// start, waits for it, saying so through emit. It returns one of:
//
//   - a stream, opened at once or once the container ran;
//   - errSaid: the wait ended and emit has carried the sentence;
//   - any other error, found before anything was emitted, exactly as Open
//     returns it, so a caller may still answer it as a status.
//
// emit is only called on the waiting path, which a fixed read never takes.
func (s *Streamer) start(ctx context.Context, req Request, emit func(Event) error) (io.ReadCloser, Request, error) {
	stream, opened, err := s.Open(ctx, req)
	if err == nil || !logOptions(req).Follow {
		return stream, opened, err
	}
	reason, waiting := waitingToStart(err)
	if !waiting || ctx.Err() != nil {
		return nil, opened, err
	}
	s.logf("log stream for %s/%s is waiting for container %q to start: %s",
		opened.Namespace, opened.Pod, opened.Container, reason)
	return s.awaitStart(ctx, opened, emit)
}

// awaitStart is the wait itself. req is already addressed: a resolved cluster
// and a named container, as Open handed back with its refusal.
//
// Each round reads the pod and decides from the container's status, as the
// kubelet decides whether it has a log to serve. Where the pod cannot be read
// (a role granting `pods/log` without `get pods`), the round asks the log
// itself, the same question at a higher price.
//
// The notice is emitted only when it changes.
func (s *Streamer) awaitStart(ctx context.Context, req Request, emit func(Event) error) (io.ReadCloser, Request, error) {
	pause := min(startFirst, s.startCap())
	// notice is what the stream says while it waits, from the pod's status or,
	// failing that, from the kubelet's refusal; said is what it last said.
	var notice, said string
	for {
		pod, err := s.pod(ctx, req.Cluster, req.Namespace, req.Pod)
		if deadlineCame(ctx, err) {
			return nil, req, s.stopWaiting(ctx, req, emit)
		}
		probe := false
		switch {
		case apierrors.IsNotFound(err):
			s.logf("log stream for %s/%s ended: the pod was deleted before %q started",
				req.Namespace, req.Pod, req.Container)
			return nil, req, endWith(emit, Event{Kind: EventNotStarted,
				Text: fmt.Sprintf("the pod was deleted before container %q started", req.Container)})
		case err != nil:
			probe = true
		default:
			now := startOf(pod, req.Container)
			if now.never != "" {
				s.logf("log stream for %s/%s ended: %s", req.Namespace, req.Pod, now.never)
				return nil, req, endWith(emit, Event{Kind: EventNotStarted, Text: now.never})
			}
			probe = now.ready
			if !now.ready {
				notice = now.notice
			}
		}

		if probe {
			stream, opened, err := s.Open(ctx, req)
			if err == nil {
				return stream, opened, nil
			}
			if deadlineCame(ctx, err) {
				return nil, req, s.stopWaiting(ctx, req, emit)
			}
			still, waiting := waitingToStart(err)
			if !waiting {
				// Refused for another reason (the pod gone between two reads, a review that
				// now says no): said as a failure, since the stream has started.
				return nil, req, endWith(emit, Event{Kind: EventError,
					Text: textutil.FirstLineN(err.Error(), errorLine)})
			}
			// The status said running and the kubelet does not agree yet, or the pod could
			// not be read: show the kubelet's reason.
			if wontStart[still] {
				return nil, req, endWith(emit, Event{Kind: EventNotStarted, Text: neverSentence(req.Container, still, "")})
			}
			notice = waitingNote + still
		}

		if notice != said {
			if err := emit(Event{Kind: EventWaiting, Text: notice}); err != nil {
				return nil, req, errSaid // the reader went away
			}
			said = notice
		}

		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, req, s.stopWaiting(ctx, req, emit)
		case <-timer.C:
		}
		pause = min(pause*2, s.startCap())
	}
}

// startCap is startEvery, shortened only by tests.
func (s *Streamer) startCap() time.Duration {
	if s.startEvery > 0 {
		return s.startEvery
	}
	return startEvery
}

// stopWaiting says why a wait ended on its context, in drive's three cases:
// ended from inside (watch.go), the deadline, or the reader gone.
//
// The deadline is the stream's own, not a second clock: a follow waiting on a
// stuck pod still holds a slot.
func (s *Streamer) stopWaiting(ctx context.Context, req Request, emit func(Event) error) error {
	if cut := whyCut(ctx); cut != nil {
		return endWith(emit, *cut)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		s.logf("log stream for %s/%s reached the %s ceiling waiting for %q to start",
			req.Namespace, req.Pod, s.deadline, req.Container)
		return endWith(emit, Event{Kind: EventEnd, Text: fmt.Sprintf(
			"stopped waiting after %s — container %q never started; reopen the panel to wait again",
			s.deadline, req.Container)})
	}
	// The client went away. Nobody to tell.
	return errSaid
}

// deadlineCame says whether err is the context ending rather than the cluster
// answering, and if so waits for the context to say so itself.
//
// ctx.Err() alone misses one case: after the deadline passes and before the
// context's timer fires, client-go's rate limiter refuses up front with "rate:
// Wait(n=1) would exceed context deadline" while ctx.Err() is still nil. Read as
// the cluster's answer, our own ceiling would reach the pane as an error.
//
// The limiter's sentence counts only within deadlineSlack of this context's
// deadline: client-go also throttles a retry against its own per-request
// timeout with the same sentence, while this deadline may be minutes away.
func deadlineCame(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	deadline, ok := ctx.Deadline()
	if err == nil || !ok {
		return false
	}
	left := time.Until(deadline)
	if left > 0 && !(left <= deadlineSlack && strings.Contains(err.Error(), "would exceed context deadline")) {
		return false
	}
	<-ctx.Done()
	return true
}

// deadlineSlack is how close to this context's deadline the limiter's refusal
// is read as that deadline: the most deadlineCame will wait for the timer.
const deadlineSlack = time.Second

// endWith emits the one event a wait ends on, and answers errSaid whether or
// not the reader was there to receive it.
func endWith(emit func(Event) error, event Event) error {
	_ = emit(event)
	return errSaid
}

// waitingToStart reads the kubelet's refusal of a container that has not
// started, and answers the reason it gave.
//
// Matched on the sentence because nothing else carries it: the 400 alone also
// covers a container name that does not exist, which no waiting fixes. It comes
// in three shapes:
//
//	container "app" in pod "web" is waiting to start: ContainerCreating
//	container "app" in pod "web" is waiting to start: image can't be pulled
//	container "app" in pod "web" is waiting to start - no logs yet
//
// The first is the status's reason; the other two are the kubelet's own words,
// shown as they are.
func waitingToStart(err error) (string, bool) {
	const marker = "is waiting to start"
	message := err.Error()
	at := strings.Index(message, marker)
	if at < 0 {
		return "", false
	}
	rest := strings.TrimLeft(message[at+len(marker):], " :-")
	rest = textutil.FirstLineN(strings.TrimSpace(rest), 120)
	if rest == "" {
		rest = "no reason given yet"
	}
	return rest, true
}

// startState is what a pod's status says about one container's log.
type startState struct {
	// ready is a log the kubelet will serve now.
	ready bool
	// notice is the sentence to wait under, while it is worth waiting.
	notice string
	// never is the sentence to end on when it is not.
	never string
}

// startOf answers whether container's log can be opened yet, from the pod's
// status.
//
// The order is the kubelet's own (validateContainerLogStatus): a container
// running or terminated has a log, and so does one waiting to restart after a
// run that ended. Only then is the waiting reason read, which is how
// `CrashLoopBackOff` means "never ran" by the time wontStart sees it.
func startOf(pod *corev1.Pod, container string) startState {
	status, found := statusOf(pod, container)
	if found {
		switch state := status.State; {
		case state.Running != nil, state.Terminated != nil, status.LastTerminationState.Terminated != nil:
			return startState{ready: true}
		case state.Waiting != nil && wontStart[state.Waiting.Reason]:
			return startState{never: neverSentence(container, state.Waiting.Reason, state.Waiting.Message)}
		}
	}

	// A finished pod does not start a container it never started: an init
	// container that failed under `restartPolicy: Never` leaves the main container
	// `PodInitializing` for ever, and only the phase says so.
	switch pod.Status.Phase {
	case corev1.PodSucceeded, corev1.PodFailed:
		return startState{never: fmt.Sprintf("the pod has ended (%s) and container %q never started",
			pod.Status.Phase, container)}
	}

	switch {
	case unscheduled(pod):
		return startState{notice: schedulingNotice(pod)}
	case found && status.State.Waiting != nil && status.State.Waiting.Reason != "":
		return startState{notice: waitingNote + status.State.Waiting.Reason}
	case pod.Spec.NodeName == "":
		return startState{notice: waitingNote + "the pod is not on a node yet"}
	default:
		return startState{notice: waitingNote + "no status reported yet"}
	}
}

// unscheduled is a pod no node has taken yet: Pending, with no node named.
//
// Its log is not refused like a waiting container's: with no kubelet to ask,
// the API server answers with an empty body, and a follow would end at once on
// "end of log". carry looks for this when a follow ends before its first line.
//
// An `Unschedulable` pod is waited on like any other, since a node added or an
// autoscaler can still schedule it; only the deadline ends that wait.
func unscheduled(pod *corev1.Pod) bool {
	return pod.Spec.NodeName == "" && pod.Status.Phase == corev1.PodPending
}

// schedulingNotice is the notice for an unscheduled pod, with the scheduler's
// reason and message when it has given up for now ("Unschedulable: 0/3 nodes
// are available: 3 Insufficient cpu."), usually the answer to why the pane
// waits.
func schedulingNotice(pod *corev1.Pod) string {
	for _, c := range pod.Status.Conditions {
		if c.Type != corev1.PodScheduled || c.Status != corev1.ConditionFalse {
			continue
		}
		why := c.Reason
		if message := textutil.FirstLineN(strings.TrimSpace(c.Message), 160); message != "" {
			if why != "" {
				why += ": "
			}
			why += message
		}
		if why != "" {
			return schedulingNote + ": " + why
		}
	}
	return schedulingNote
}

// neverSentence ends a wait on a reason the container does not get past on
// its own, with the kubelet's detail where it gave one (for an image, the name
// it could not pull).
func neverSentence(container, reason, detail string) string {
	if detail = textutil.FirstLineN(strings.TrimSpace(detail), 160); detail != "" {
		detail = " (" + detail + ")"
	}
	return fmt.Sprintf("container %q is not starting: %s%s — reopen the panel once that is fixed",
		container, reason, detail)
}

// statusOf finds one container's status wherever the pod keeps it: a regular
// container, an init container, or an ephemeral one - all three have a log.
func statusOf(pod *corev1.Pod, container string) (corev1.ContainerStatus, bool) {
	for _, list := range [][]corev1.ContainerStatus{
		pod.Status.ContainerStatuses,
		pod.Status.InitContainerStatuses,
		pod.Status.EphemeralContainerStatuses,
	} {
		for _, status := range list {
			if status.Name == container {
				return status, true
			}
		}
	}
	return corev1.ContainerStatus{}, false
}

// carry drives an opened stream to its end and owns it: it closes the stream,
// and any stream it opens after it.
//
// For a follow that ended before its first line, it first asks whether the pod
// is still waiting for a node: an unscheduled pod's log is an empty answer, not
// a refusal, so it cannot be told from a finished container's empty log without
// reading the pod. If so, the follow waits on the same stream, in the same slot
// and under the same deadline.
//
// Asked here rather than before the first Open, so the pod read is paid only by
// the rare stream that ends empty; the framed route names its container up
// front, so there is no earlier pod read to reuse.
//
// A pod that cannot be read here keeps the end it had.
func (s *Streamer) carry(ctx context.Context, stream io.ReadCloser, req Request, sink io.Writer, emit func(Event) error) error {
	follow := logOptions(req).Follow
	for {
		var (
			lines int
			held  *Event
		)
		err := s.drive(ctx, stream, req, sink, func(event Event) error {
			switch {
			case event.Kind == EventLine:
				lines++
			case follow && lines == 0 && event.Kind == EventEnd && event.Text == endOfLog:
				held = &event
				return nil
			}
			return emit(event)
		})
		_ = stream.Close()
		if err != nil || held == nil {
			return err
		}

		pod, err := s.pod(ctx, req.Cluster, req.Namespace, req.Pod)
		if err != nil && !apierrors.IsNotFound(err) || err == nil && !unscheduled(pod) {
			return emit(*held)
		}
		// Unscheduled, or deleted since its log was read: the wait says which.
		s.logf("log stream for %s/%s is waiting for the pod to be scheduled", req.Namespace, req.Pod)
		next, opened, err := s.awaitStart(ctx, req, emit)
		if err != nil {
			return nil // said
		}
		stream, req = next, opened
	}
}
