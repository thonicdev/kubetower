package terminal

// A shell in a debug container: an ephemeral container added to a running
// pod, then exec-ed into. It is `kubectl debug -it <pod> --image=<image>
// --target=<container>`, for the pod whose own image has no shell (distroless,
// scratch) or none of the tools a diagnosis needs.
//
// It is a write before it is a shell. The container is added through the
// pod's `ephemeralcontainers` subresource, which cannot be undone: an ephemeral
// container is never removed from a pod, only stopped, and the pod carries it
// in its spec until the pod itself goes. So:
//
//   - refused on a cluster in KT_READONLY_CLUSTERS before the upgrade, with
//     its audit record, and nothing sent to the cluster;
//   - reviewed twice before anything is spent: `patch pods/ephemeralcontainers`
//     (the change) and `create pods/exec` (the shell opened in it). Either
//     refused, nothing is added: a container nobody may open a shell in is a
//     container added for nothing, and it cannot be taken back;
//   - the PATCH goes through Access.Perform, which records it with the image,
//     the target and the container's name;
//   - the shell is the exec path's own (runExec), reviewed and recorded as
//     every pod shell is. `pods/attach` is never asked: the container runs
//     `sh` on a terminal of its own so that it stays up, and the person's
//     shell is a second `sh` beside it, which an exec gives without a second
//     grant to review.
//
// The refusals keep their cause apart, because each has a different remedy and
// a different person to ask: RBAC (verb_denied), the namespace's Pod Security
// level (pod_security), an admission webhook's policy (admission_denied), a
// cluster that serves no such subresource (not_supported_here).

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/nodeshell"
	"github.com/thonicdev/kubetower/safego"
	"github.com/thonicdev/kubetower/textutil"
)

// debugStartTimeout bounds the wait for the debug container to run. As for
// the node shell, the wait is usually an image pull on a node that has never
// seen the image, and less would make a cold pull look broken.
const debugStartTimeout = 2 * time.Minute

// debugStartPoll is how often the pod is read while waiting: one object, for
// at most two minutes, needs no watch.
const debugStartPoll = 500 * time.Millisecond

// debugContainerPrefix starts every debug container's name, so `kubectl get
// pod -o yaml` says which ones the console added.
const debugContainerPrefix = "kt-debug-"

// debugContainerCommand is what the debug container itself runs: the image's
// `sh`, on stdin and a terminal of its own, so it waits on its input and stays
// up for the shell exec-ed beside it. busybox, alpine and netshoot all carry
// one; an image without it ends at once, which the wait reports.
var debugContainerCommand = []string{"sh"}

// FramedDebugHandler serves a shell in a new debug container over
// api/websocket.md's frame vocabulary.
//
// Parameters: cluster, namespace, pod, image, and optionally target, the
// container whose process namespace the debug container joins.
//
// Framed only, with no raw twin: the SPA reaches every pod shell through
// `/api/c/{cluster}/ws/...` on both profiles, and a raw route nobody calls is
// a door to keep shut for nothing.
func (t *Terminal) FramedDebugHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.serveDebug(w, r, wireFramed)
	})
}

// debugAttributes is the review the change needs: `patch` on the pod's
// `ephemeralcontainers` subresource, on that pod. The request is a PATCH on
// that path, so the transport guard (access.Guard) reads the same question
// off it.
func debugAttributes(namespace, pod string) access.Attributes {
	return access.Attributes{Verb: "patch", Version: "v1", Resource: "pods", Subresource: "ephemeralcontainers", Namespace: namespace, Name: pod}
}

// debugTarget is one debug session's request, validated.
type debugTarget struct {
	cluster   string
	namespace string
	pod       string
	image     string
	// target is the container whose processes the debug container sees; empty
	// for none.
	target string
}

// debugRefusal is a debug session that did not open, in the framed vocabulary:
// the state, the branchable code and the sentence.
type debugRefusal struct {
	state   string
	code    string
	message string
}

func (d *debugRefusal) Error() string { return d.message }

func (t *Terminal) serveDebug(w http.ResponseWriter, r *http.Request, speaks wire) {
	name, ok := t.clusters.Resolve(clusterParam(r))
	if !ok {
		http.Error(w, "unknown cluster", http.StatusNotFound)
		return
	}

	q := r.URL.Query()
	target := debugTarget{
		cluster:   name,
		namespace: pathOrQuery(r, "namespace"),
		pod:       pathOrQuery(r, "pod"),
		image:     q.Get("image"),
		target:    q.Get("target"),
	}
	// Every record of this session carries the image and the target: the
	// read-only refusal below, the reviews' refusals, and the PATCH's.
	ctx := access.WithNote(r.Context(), access.Note{Image: target.image, Target: target.target})

	// The read-only guardrail, before anything else and before the upgrade, as
	// for `exec`: the change cannot be undone, and the shell it leads to is one
	// the fence refuses already. Recorded, unlike exec's, because this is a write
	// refused, and a refused write is what an audit reader looks for. The record
	// is the only trace (nothing is reviewed or sent); one that cannot be written
	// changes nothing, the act being refused already, and the gate logs it.
	if t.opts.ReadOnly != nil && t.opts.ReadOnly(name) {
		attrs := debugAttributes(target.namespace, target.pod)
		pending, _ := t.opts.Access.Begin(ctx, t.opts.Access.Record(ctx, audit.Write, name, attrs, access.ReadOnly))
		t.opts.Access.End(ctx, pending, audit.Refused, "")
		t.logf("%s", identity.Line(ctx, "debug refused on read-only cluster %q %s/%s", name, target.namespace, target.pod))
		http.Error(w, name+" is marked read-only in KT_READONLY_CLUSTERS; adding a debug container is refused there",
			http.StatusForbidden)
		return
	}

	if why := target.invalid(); why != "" {
		http.Error(w, why, http.StatusBadRequest)
		return
	}

	conn, err := t.upgrader.Upgrade(w, r, nil)
	if err != nil {
		t.logf("debug upgrade failed: %v", err)
		return
	}
	stream := t.openStream(conn, speaks)
	defer stream.close()

	// Both questions, for this person, before anything is spent: the change and
	// the shell. After the upgrade, since a refused handshake reaches the
	// browser with no reason. Asked in that order and refused at the first no,
	// with that question's record; Perform and runExec ask both again inside
	// their doors, from the held answers.
	//
	// As for the exec (serveExec): on the desktop an answer that could not be
	// established lets it through, since the API server checks the same
	// identity; a denial refuses on every profile.
	for _, step := range []struct {
		act   string
		attrs access.Attributes
	}{
		{audit.Write, debugAttributes(target.namespace, target.pod)},
		{audit.Exec, execAttributes(target.namespace, target.pod)},
	} {
		answer := t.opts.Access.Review(ctx, name, step.attrs)
		if answer.Allowed() || t.opts.Access.LetsThrough(answer) {
			continue
		}
		refused := &access.Refused{Cluster: name, Attributes: step.attrs, Answer: answer}
		state := stateForbidden
		if answer.Decision != access.Denied {
			state = stateUnreadable
		}
		pending, _ := t.opts.Access.Begin(ctx, t.opts.Access.Record(ctx, step.act, name, step.attrs, answer.Decision))
		t.opts.Access.End(ctx, pending, audit.Refused, "")
		t.logf("%s", identity.Line(ctx, "debug refused on %q %s/%s: decision=%s on %s",
			name, target.namespace, target.pod, answer.Decision, step.attrs))
		stream.refuse(state, codeVerbDenied, refused.Error())
		return
	}

	// The same ceiling and count as every other shell (limit.go).
	release, err := t.hold(ctx, stream)
	if err != nil {
		stream.refuse(stateUnreadable, codeNotSupportedHere, err.Error())
		t.logf("%s", identity.Line(ctx, "debug refused on %q %s/%s: %v", name, target.namespace, target.pod, err))
		return
	}
	defer release()

	// Re-checked while open, on the shell's question, as serveExec does.
	execAttrs := execAttributes(target.namespace, target.pod)
	safego.Go("debug: re-checking an open shell", func() error {
		t.watch(ctx, stream, name, execAttrs)
		return nil
	}, func(err error) {
		if err != nil {
			stream.farewell(stateUnreadable, codeUnclassified,
				"this shell was closed: the console's re-check of it stopped on an internal error")
		}
	})

	container, refusal := t.startDebug(ctx, stream, target)
	if refusal != nil {
		t.logf("%s", identity.Line(ctx, "debug refused on %q %s/%s: %s: %s",
			name, target.namespace, target.pod, refusal.code, refusal.message))
		stream.refuse(refusal.state, refusal.code, refusal.message)
		return
	}

	// The shell's records name the container too, so the exec and the PATCH
	// before it read as one session. A context of its own: the re-check above
	// holds ctx.
	shellCtx := access.WithNote(ctx, access.Note{Image: target.image, Target: target.target, Container: container})
	opened := false
	open := func() {
		opened = true
		t.logf("%s", identity.Line(shellCtx, "debug opened on %q %s/%s container %s", name, target.namespace, target.pod, container))
	}
	err = t.runExec(shellCtx, stream, execTarget{
		cluster:   name,
		namespace: target.namespace,
		pod:       target.pod,
		container: container,
		command:   defaultExecCommand,
	}, open)
	if !opened {
		t.logf("%s", identity.Line(shellCtx, "debug shell refused on %q %s/%s: %v", name, target.namespace, target.pod, err))
		stream.refuse(execRefusal(err))
		return
	}
	// An ordinary end, and a shell not in the image, are said by streamExec
	// before the socket closes; this covers the rest.
	if err != nil {
		stream.refuse(stateUnreachable, codeUnclassified, fmt.Sprintf("exec ended: %v", err))
	}
	t.logf("%s", identity.Line(shellCtx, "debug closed on %q %s/%s container %s", name, target.namespace, target.pod, container))
}

// invalid says what is wrong with the request, or "" when nothing is. Refused
// here as one sentence, rather than by the API server as an invalid patch.
func (d debugTarget) invalid() string {
	if !validName(d.namespace) || !validName(d.pod) {
		return "namespace and pod must be Kubernetes names"
	}
	if d.target != "" && !validName(d.target) {
		return "target must be a container's name"
	}
	if strings.TrimSpace(d.image) == "" {
		return "image is required: the debug container runs it"
	}
	// The node shell's own check: permissive, refusing only what is certainly not
	// an image reference. The kubelet is the authority on whether it exists.
	if d.image != strings.TrimSpace(d.image) {
		return "image must be an image reference, with no surrounding spaces"
	}
	if err := nodeshell.Validate(d.image); err != nil {
		return "image: " + err.Error()
	}
	return ""
}

// startDebug adds the debug container and waits for it to run, narrating the
// wait. It answers the container's name, or why there is none.
func (t *Terminal) startDebug(ctx context.Context, s *stream, d debugTarget) (string, *debugRefusal) {
	// The browser leaving ends the wait: ctx is the request's, which the
	// WebSocket hijack never cancels (stream.go), so without this a closed tab
	// would keep reading the pod for the rest of the two minutes. serveDebug
	// closes the stream on every path, so this ends with the session.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	safego.Go("debug: ending the wait when the browser leaves", func() error {
		select {
		case <-s.closed():
			cancel()
		case <-ctx.Done():
		}
		return nil
	}, nil)

	client, err := t.clusters.Client(d.cluster)
	if err != nil {
		return "", &debugRefusal{stateUnreachable, codeUnclassified, "the debug container was not added: " + textutil.FirstLine(err.Error())}
	}
	pods := client.Clients.CoreV1().Pods(d.namespace)

	// The pod is read first, so that a pod that is not there is said as such: the
	// PATCH's 404 cannot tell a missing pod from a cluster that serves no
	// `ephemeralcontainers` subresource, and only the second is not_supported_here.
	// The target is checked against it too, which the API server would refuse as
	// an invalid patch in words that name a field path.
	current, err := pods.Get(ctx, d.pod, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return "", &debugRefusal{stateAbsent, codeNoSuchObject,
			fmt.Sprintf("there is no pod %s in namespace %s", d.pod, d.namespace)}
	case err != nil:
		return "", &debugRefusal{stateUnreadable, codeUnclassified,
			"the debug container was not added: the pod could not be read: " + textutil.FirstLine(err.Error())}
	}
	if d.target != "" && !hasContainer(current, d.target) {
		return "", &debugRefusal{stateAbsent, codeNoSuchObject,
			fmt.Sprintf("pod %s has no container named %s to target", d.pod, d.target)}
	}

	container := debugContainerName()
	body, err := debugPatch(container, d.image, d.target)
	if err != nil {
		return "", &debugRefusal{stateUnreadable, codeUnclassified, "the debug container was not added: " + err.Error()}
	}
	s.notice("Adding " + container + " to " + d.pod + " from " + d.image + "…")

	noted := access.WithNote(ctx, access.Note{Image: d.image, Target: d.target, Container: container})
	answer, err := t.opts.Access.Perform(noted, d.cluster, debugAttributes(d.namespace, d.pod),
		func(ctx context.Context) error {
			// The typed client's REST interface, on the subresource by name: the
			// request is `PATCH .../pods/<pod>/ephemeralcontainers`, which the chart's
			// derive-needs.py reads as `patch pods/ephemeralcontainers`.
			return client.Clients.CoreV1().RESTClient().Patch(types.StrategicMergePatchType).
				Namespace(d.namespace).
				Resource("pods").
				Name(d.pod).
				SubResource("ephemeralcontainers").
				Body(body).
				Do(ctx).
				Error()
		})
	t.logf("%s", identity.Line(ctx, "debug container %s on %q %s/%s (image %s, target %q): decision=%s err=%v",
		container, d.cluster, d.namespace, d.pod, d.image, d.target, answer.Decision, err))
	if err != nil {
		return "", patchRefusal(err)
	}

	wait, poll := t.debugWait, t.debugPoll
	if wait <= 0 {
		wait = debugStartTimeout
	}
	if poll <= 0 {
		poll = debugStartPoll
	}
	if refusal := waitForDebug(ctx, s.notice, pods, d.pod, container, wait, poll); refusal != nil {
		return "", refusal
	}
	return container, nil
}

// hasContainer is whether the pod declares a container by that name, init
// containers included.
func hasContainer(pod *corev1.Pod, name string) bool {
	for _, c := range pod.Spec.Containers {
		if c.Name == name {
			return true
		}
	}
	for _, c := range pod.Spec.InitContainers {
		if c.Name == name {
			return true
		}
	}
	return false
}

// debugContainerName is `kt-debug-` and five random lowercase letters or
// digits: short enough to read in a container list, random enough that two
// sessions on one pod are two containers (a clash is refused by the API server,
// never merged into the other).
func debugContainerName() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		// Time is a poorer suffix than randomness and a better one than a clash.
		n := time.Now().UnixNano()
		for i := range buf {
			buf[i] = byte(n >> (8 * i))
		}
	}
	for i, b := range buf {
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	return debugContainerPrefix + string(buf)
}

// debugPatch is the strategic-merge patch adding one ephemeral container. The
// list is merged by name, so the pod's existing ephemeral containers stay.
//
// No securityContext is set: the container gets the namespace's defaults, and
// a namespace that forbids them says so (pod_security). IfNotPresent, as for
// the node shell: a node that has the image opens in a second.
func debugPatch(name, image, target string) ([]byte, error) {
	container := corev1.EphemeralContainer{
		EphemeralContainerCommon: corev1.EphemeralContainerCommon{
			Name:                     name,
			Image:                    image,
			Command:                  debugContainerCommand,
			Stdin:                    true,
			TTY:                      true,
			ImagePullPolicy:          corev1.PullIfNotPresent,
			TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		},
		TargetContainerName: target,
	}
	return json.Marshal(map[string]any{
		"spec": map[string]any{"ephemeralContainers": []corev1.EphemeralContainer{container}},
	})
}

// patchRefusal classifies a PATCH that did not add the container. The API
// server's 403 covers three different refusals, told apart by its own words
// because nothing else carries them: Pod Security admission writes "violates
// PodSecurity", a validating webhook "admission webhook ... denied the
// request", and RBAC neither.
func patchRefusal(err error) *debugRefusal {
	message := "the debug container was not added: " + textutil.FirstLine(err.Error())
	var refused *access.Refused
	switch {
	case errors.As(err, &refused):
		// The console's own review, inside Perform: the held answer changed since
		// the session asked.
		state := stateForbidden
		if refused.Answer.Decision != access.Denied {
			state = stateUnreadable
		}
		return &debugRefusal{state, codeVerbDenied, message}
	case errors.Is(err, access.ErrUnrecorded):
		return &debugRefusal{stateUnreadable, codeUnclassified, message}
	case apierrors.IsForbidden(err) && strings.Contains(err.Error(), "violates PodSecurity"):
		return &debugRefusal{stateForbidden, codePodSecurity, message}
	case strings.Contains(err.Error(), "admission webhook") && strings.Contains(err.Error(), "denied the request"):
		return &debugRefusal{stateForbidden, codeAdmissionDenied, message}
	case apierrors.IsNotFound(err), apierrors.IsMethodNotSupported(err):
		// The pod was read a moment ago, so a 404 here is the subresource: a
		// cluster older than ephemeral containers, or one that turned them off.
		return &debugRefusal{stateUnreadable, codeNotSupportedHere,
			"this cluster does not serve ephemeral containers, so no debug container can be added: " +
				textutil.FirstLine(err.Error())}
	case apierrors.IsForbidden(err):
		return &debugRefusal{stateForbidden, codeVerbDenied, message}
	}
	return &debugRefusal{stateUnreadable, codeUnclassified, message}
}

// waitForDebug blocks until the debug container runs, or says why it will
// not. A pull that fails, or a container that ended, will not start on its own,
// and the wait stops at once with the kubelet's reason rather than at the
// limit. It takes `say` rather than the stream so a test needs no socket.
func waitForDebug(ctx context.Context, say func(string), pods podReader, pod, container string, limit, poll time.Duration) *debugRefusal {
	deadline := time.Now().Add(limit)
	said := ""
	for {
		current, err := pods.Get(ctx, pod, metav1.GetOptions{})
		if err != nil {
			return &debugRefusal{stateUnreadable, codeUnclassified,
				"the debug container was added and could not be watched starting: " + textutil.FirstLine(err.Error())}
		}
		status := ephemeralStatus(current, container)
		switch {
		case status != nil && status.State.Running != nil:
			return nil
		case status != nil && status.State.Terminated != nil:
			ended := status.State.Terminated
			return &debugRefusal{stateUnreadable, codeContainerNotStarted,
				"the debug container ended before a shell could be opened" + detailSuffix(waitingReason(ended.Reason, ended.Message))}
		case status != nil && status.State.Waiting != nil && pullFailed(status.State.Waiting.Reason):
			waiting := status.State.Waiting
			return &debugRefusal{stateUnreadable, codeContainerNotStarted,
				"the debug container's image could not be pulled" + detailSuffix(waitingReason(waiting.Reason, waiting.Message))}
		case current.Status.Phase == corev1.PodSucceeded || current.Status.Phase == corev1.PodFailed:
			// A pod that has ended starts nothing new.
			return &debugRefusal{stateUnreadable, codeContainerNotStarted,
				fmt.Sprintf("the pod has ended (%s), so the debug container will not start", strings.ToLower(string(current.Status.Phase)))}
		}

		if status != nil && status.State.Waiting != nil {
			reason := waitingReason(status.State.Waiting.Reason, status.State.Waiting.Message)
			if reason != "" && reason != said && status.State.Waiting.Reason != "ContainerCreating" {
				said = reason
				say(reason)
			}
		}

		if time.Now().After(deadline) {
			return &debugRefusal{stateUnreadable, codeTimeout,
				fmt.Sprintf("the debug container was not running after %s", limit) + detailSuffix(said)}
		}
		select {
		case <-ctx.Done():
			return &debugRefusal{stateUnreadable, codeUnclassified, ctx.Err().Error() + detailSuffix(said)}
		case <-time.After(poll):
		}
	}
}

// ephemeralStatus is the kubelet's report on one ephemeral container, or nil
// while it has reported nothing.
func ephemeralStatus(pod *corev1.Pod, container string) *corev1.ContainerStatus {
	for i := range pod.Status.EphemeralContainerStatuses {
		if pod.Status.EphemeralContainerStatuses[i].Name == container {
			return &pod.Status.EphemeralContainerStatuses[i]
		}
	}
	return nil
}

// pullFailed is whether a waiting reason is an image that will not arrive on
// its own: the kubelet keeps retrying a back-off, and a person waiting two
// minutes for it learns nothing the first answer did not say.
func pullFailed(reason string) bool {
	switch reason {
	case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "ErrImageNeverPull":
		return true
	}
	return false
}

// waitingReason is a reason and its first message line, or the reason alone.
func waitingReason(reason, message string) string {
	if message != "" {
		if reason == "" {
			return textutil.FirstLine(message)
		}
		return reason + ": " + textutil.FirstLine(message)
	}
	return reason
}
