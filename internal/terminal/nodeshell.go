package terminal

// A shell on a node, over the same socket protocol as the other terminals.
// The image is a setting (internal/nodeshell).
//
// A Node has no `exec` subresource: a node is a machine, not a process. The
// gesture that exists, `kubectl debug node/<name> -it --image=<image>`, creates
// a pod pinned to the node, sharing the host's PID, network and IPC namespaces,
// with the node's root filesystem at /host; `chroot /host` in it is a prompt on
// the machine. So this is a mutating action:
//
//   - refused outright on a cluster in KT_READONLY_CLUSTERS, as `exec` is;
//   - behind the console's confirmation, like delete and drain (the framed
//     route spends a plan token; see FramedNodeShell);
//   - the pod is deleted when the socket closes, which `kubectl debug` does
//     not do (removeNodeShell).
//
// The delete runs on a background context: the request's is already canceled
// by then, and canceling the cleanup with its own cause would leave pods on
// nodes. Two more removals stand behind it, since a killed process is not
// exotic:
//
//   - `activeDeadlineSeconds`: the kubelet stops the pod after
//     nodeShellDeadline whatever KubeTower does, which also bounds a session
//     (stated in the confirmation);
//   - the label `app.kubernetes.io/managed-by=kubetower`, so `kubectl get pods
//     -A -l app.kubernetes.io/managed-by=kubetower` finds anything left behind.
//
// There is no sweep at start-up, which would delete pods on every reachable
// cluster, contacting every context (lapsed EKS sessions included). The sweep
// runs on the first node shell opened on a cluster, once per cluster per
// process, and deletes only pods Kubernetes has already stopped; see
// sweepNodeShells.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/textutil"
)

// nodeShellNamespace is where the debug pod is created: `default`, as
// `kubectl debug node/…` uses when the kubeconfig names none. Not configurable:
// where a one-session pod is filed asks no question worth a setting. A namespace
// that refuses the pod (a restricted PodSecurity label) says so in the API
// server's words, in the pane.
const nodeShellNamespace = "default"

// nodeShellDeadline is how long the pod may live, enforced by the kubelet
// rather than by this process: longer than any session, short enough that a
// KubeTower killed mid-session does not leave a shell on a node's root
// filesystem overnight.
const nodeShellDeadline = 4 * time.Hour

// nodeShellStartTimeout bounds the wait for the pod to run. The wait is
// usually an image pull on a node that has never seen the image, so less would
// make a cold pull look broken. Progress is reported in the pane meanwhile.
const nodeShellStartTimeout = 2 * time.Minute

// nodeShellPoll is how often the pod's phase is read while waiting: polling
// one object for at most two minutes needs no watch.
const nodeShellPoll = 500 * time.Millisecond

// nodeShellLabels mark every pod this opens: `app.kubernetes.io/managed-by`,
// the label people already grep for, and one naming the feature, since
// "managed-by kubetower" may later be true of something else.
var nodeShellLabels = map[string]string{
	"app.kubernetes.io/managed-by": "kubetower",
	"kubetower.io/node-shell":      "true",
}

// nodeShellPark is what the container runs: the pod is parked and the shell
// exec-ed into it, rather than being the container's process as with `kubectl
// debug`. The pod then stays Running whatever the image's entrypoint, a dropped
// socket leaves a pod that can still be deleted, and the shell gets the same
// sh-to-bash fallback as a pod exec.
//
// A loop of `sleep 3600` rather than `sleep infinity`, which busybox (the
// default image's) refuses.
var nodeShellPark = []string{"/bin/sh", "-c", "while :; do sleep 3600; done"}

// nodeShellCommand is what the operator lands on. The test comes before the
// exec: when exec fails a non-interactive POSIX shell exits, so `exec bash ||
// exec sh` would close the terminal on any image without bash.
var nodeShellCommand = []string{"/bin/sh", "-c",
	"command -v bash >/dev/null 2>&1 && exec bash || exec /bin/sh"}

// NodeShellImage is the image a node shell runs, for the settings page.
func (t *Terminal) NodeShellImage() string { return t.nodeShell.Image() }

// SetNodeShellImage records a new one. The error is advisory: a valid value is
// in effect whether or not it could be persisted, and the page must be able to
// say so.
func (t *Terminal) SetNodeShellImage(image string) error {
	_, err := t.nodeShell.Set(image)
	return err
}

// NodeShellHandler serves a shell on a node over the terminal's own WebSocket
// protocol, so the dock mounts the component it already has.
func (t *Terminal) NodeShellHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.serveNodeShell(w, r, wireRaw, pathOrQuery(r, "node"))
	})
}

// FramedNodeShell serves the same node shell over api/websocket.md's frame
// vocabulary, on a node chosen by the caller, not read here.
//
// On the framed route the act carries a plan token; internal/api spends it and
// re-derives the node from the facts the operator agreed to, so editing a query
// string cannot move a confirmed shell to another node.
//
// The node must still be a Kubernetes name: it reaches a pod spec, and a name
// refused here is one sentence rather than an upgrade failure.
func (t *Terminal) FramedNodeShell(w http.ResponseWriter, r *http.Request, node string) {
	t.serveNodeShell(w, r, wireFramed, node)
}

func (t *Terminal) serveNodeShell(w http.ResponseWriter, r *http.Request, speaks wire, node string) {
	name, ok := t.clusters.Resolve(clusterParam(r))
	if !ok {
		http.Error(w, "unknown cluster", http.StatusNotFound)
		return
	}

	if !validName(node) {
		http.Error(w, "node must be a Kubernetes name", http.StatusBadRequest)
		return
	}

	conn, err := t.upgrader.Upgrade(w, r, nil)
	if err != nil {
		t.logf("node shell upgrade failed: %v", err)
		return
	}
	stream := t.openStream(conn, speaks)
	defer stream.close()

	// Refused after the upgrade, unlike serveExec: a rejected handshake gives the
	// browser no status and no body. The drawer does not offer a node shell on a
	// read-only cluster, so only a hand-made socket reaches this, and it gets the
	// sentence.
	if t.opts.ReadOnly != nil && t.opts.ReadOnly(name) {
		stream.refuse(stateForbidden, codeVerbDenied,
			name+" is marked read-only in KT_READONLY_CLUSTERS. A shell on a node "+
				"creates a pod on it, with the machine's own filesystem in it, so it is refused there.")
		t.logf("node shell refused on read-only cluster %q (node %s)", name, node)
		return
	}

	// The same ceiling and count as the shell and the pod exec (limit.go).
	release, err := t.hold(r.Context(), stream)
	if err != nil {
		stream.refuse(stateUnreadable, codeNotSupportedHere, err.Error())
		t.logf("node shell refused on %q %s: %v", name, node, err)
		return
	}
	defer release()

	// What a previous KubeTower left on this cluster, before adding to it; see
	// sweepNodeShells.
	t.sweepNodeShells(r.Context(), t.clusters, name)

	image := t.nodeShell.Image()
	t.logf("node shell opening on %q node %s (image %s)", name, node, image)

	pod, err := t.startNodeShell(r.Context(), stream, name, node, image)
	if pod != "" {
		// Deferred before the error is examined: a pod created that then failed to
		// become ready is exactly the one that must not be left behind.
		defer t.removeNodeShell(name, pod)
	}
	if err != nil {
		stream.refuse(stateUnreachable, codeUnclassified,
			"the node shell could not be started: "+textutil.FirstLine(err.Error()))
		t.logf("node shell failed on %q %s: %v", name, node, err)
		return
	}

	err = t.runExec(r.Context(), stream, execTarget{
		cluster:   name,
		namespace: nodeShellNamespace,
		pod:       pod,
		container: nodeShellContainer,
		command:   nodeShellCommand,
	}, nil)
	// An ordinary end is said by streamExec, before the socket closes.
	if err != nil {
		stream.refuse(stateUnreachable, codeUnclassified, fmt.Sprintf("node shell ended: %v", err))
	}
	t.logf("node shell closed on %q %s", name, node)
}

// nodeShellContainer is the one container in the pod.
const nodeShellContainer = "shell"

// startNodeShell creates the pod and waits for it to run, narrating the wait.
// It returns the pod's name whenever one was created, including on failure, so
// the caller can remove it.
func (t *Terminal) startNodeShell(ctx context.Context, s *stream, clusterName, node, image string) (string, error) {
	client, err := t.clusters.Client(clusterName)
	if err != nil {
		return "", err
	}
	pods := client.Clients.CoreV1().Pods(nodeShellNamespace)

	// The node is read first: a typo is then one sentence rather than a pod that
	// schedules nowhere and times out, and the scheduler does not validate a
	// `nodeName`.
	if _, err := client.Clients.CoreV1().Nodes().Get(ctx, node, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("there is no node called %s on this cluster", node)
		}
		return "", err
	}

	spec := nodeShellPod(node, image)
	s.notice("Creating " + spec.Name + " on " + node + " from " + image + "…")

	// Reviewed as the request it is: `create pods` in the node-shell namespace,
	// for the person behind ctx; the shell is reviewed again as `create pods/exec`
	// when runExec opens it. Only the desktop offers a node shell today, so the
	// review is the kubeconfig's own; it is here so that holds for any profile.
	var name string
	answer, err := t.opts.Access.Perform(ctx, clusterName,
		access.Attributes{Verb: "create", Version: "v1", Resource: "pods", Namespace: nodeShellNamespace},
		func(ctx context.Context) error {
			var err error
			name, err = createNodeShell(ctx, pods, spec)
			return err
		})
	// The record says which decision the pod was created on (on the desktop
	// possibly `unreviewable`, let through by access.Gate.LetsThrough) and whether
	// it was.
	outcome := "created"
	if err != nil {
		outcome = "refused"
		// Refused by the review, or its audit record could not be written: either way
		// nothing was sent.
		if _, refused := err.(*access.Refused); !refused && !errors.Is(err, access.ErrUnrecorded) {
			outcome = "failed"
		}
	}
	t.logf("%s", identity.Line(ctx, "node shell pod on %q node %s: decision=%s outcome=%s",
		clusterName, node, answer.Decision, outcome))
	if err != nil {
		return name, err
	}
	created := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}}

	if err := waitForNodeShell(ctx, s.notice, pods, created.Name); err != nil {
		return created.Name, err
	}
	s.notice("Running. The node's filesystem is at /host — `chroot /host` gives you its own root. " +
		"Closing this tab deletes the pod.")
	return created.Name, nil
}

// sweepNodeShells clears what a previous KubeTower left on one cluster.
//
// A node-shell pod is left behind by a kill between create and delete, a lost
// Create response (see nothingWasCreated), or a handler parked by a vanished
// browser. Each is a hostPID/hostIPC/hostNetwork pod with the node's root
// filesystem at /host that nothing knows about.
//
// Only what cannot be in use is deleted. Two KubeTowers against one cluster
// (`go run ./cmd/kubetower` beside the container) is supported, and there is no
// way to ask a pod whether someone has it open, so deleting every labeled pod
// could remove another instance's live root shell. A pod is removed only once
// Kubernetes has stopped it: Succeeded, or Failed (activeDeadlineSeconds leaves
// a Failed pod object, reason DeadlineExceeded). A pod still Running is
// reported with the command to remove it, and left to a person.
//
// Lazy and once per cluster per process: a start-up sweep would contact every
// context, lapsed SSO sessions included. Once means once it has looked: an
// attempt that could not get a client or list the pods is forgotten, and the
// next shell on that cluster tries again. A sweep that listed stays done even
// if one removal failed; that pod is reported. Run before the new pod exists,
// so it never sees its own.
//
// clients is t.clusters outside tests, an interface so a test can make the
// first attempt fail.
func (t *Terminal) sweepNodeShells(ctx context.Context, clients clusterClients, clusterName string) {
	if _, done := t.swept.LoadOrStore(clusterName, true); done {
		return
	}

	client, err := clients.Client(clusterName)
	if err != nil {
		t.swept.Delete(clusterName)
		return
	}
	// Its own deadline: housekeeping must never be why a shell is slow to open.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	pods := client.Clients.CoreV1().Pods(nodeShellNamespace)
	list, err := pods.List(ctx, metav1.ListOptions{
		LabelSelector: "kubetower.io/node-shell=true",
		Limit:         100,
	})
	if err != nil {
		// Not worth a word to the operator, who asked for a shell; the pods are
		// bounded by activeDeadlineSeconds anyway.
		t.logf("could not look for leftover node shells on %q: %v", clusterName, err)
		t.swept.Delete(clusterName)
		return
	}

	residue, live := partitionLeftovers(list.Items)

	for _, pod := range residue {
		// Housekeeping, not a person's write: the console removing a pod it created,
		// which the access guard lets through for this pod only.
		removal := access.Housekeeping(ctx, clusterName, nodeShellRemoval(pod.name))
		if err := pods.Delete(removal, pod.name, metav1.DeleteOptions{}); err != nil &&
			!apierrors.IsNotFound(err) {
			t.logf("CANNOT DELETE leftover node shell %s/%s on %q: %v",
				nodeShellNamespace, pod.name, clusterName, err)
			continue
		}
		t.logf("removed leftover node shell %s/%s on %q (%s)",
			nodeShellNamespace, pod.name, clusterName, pod.why)
	}
	for _, pod := range live {
		t.logf("node shell %s/%s on %q is %s and was not started by this process - "+
			"left alone in case somebody is in it (kubectl -n %s delete pod %s)",
			nodeShellNamespace, pod.name, clusterName, pod.why,
			nodeShellNamespace, pod.name)
	}
}

// leftover is one pod the sweep found, with the phrase saying why it was
// treated as it was, carried so the decision and its stated reason agree.
type leftover struct {
	name string
	why  string
}

// partitionLeftovers splits what the sweep found into certain residue and what
// might be somebody's live shell. Separate because it is the whole safety
// property: wrong one way deletes a live root shell, wrong the other leaves pods
// activeDeadlineSeconds removes anyway.
//
// Only the two terminal phases are residue. Running, Pending and any phase a
// later Kubernetes adds are reported: an unknown phase is not assumed dead.
func partitionLeftovers(items []corev1.Pod) (residue, live []leftover) {
	for _, pod := range items {
		why := string(pod.Status.Phase)
		if pod.Status.Reason != "" {
			why += ", " + pod.Status.Reason
		}
		switch pod.Status.Phase {
		case corev1.PodSucceeded, corev1.PodFailed:
			residue = append(residue, leftover{name: pod.Name, why: why})
		default:
			live = append(live, leftover{name: pod.Name, why: why})
		}
	}
	return residue, live
}

// podCreator is the slice of the typed client createNodeShell uses, declared
// so it can be driven against a fake.
type podCreator interface {
	Create(ctx context.Context, pod *corev1.Pod, opts metav1.CreateOptions) (*corev1.Pod, error)
}

// createNodeShell creates the pod and answers with the name to clean up, even
// on failure: a Create whose request arrived and whose response was lost (a
// timeout, a reset, a 503 during an API server roll) has created the pod, and
// "" would tell serveNodeShell there is nothing to remove, leaving a privileged
// pod on the node until its deadline.
//
// Safe because nodeShellPodName ends in random bytes, so the name belongs to
// this call; AlreadyExists, the one error saying it is someone else's, is
// excluded by nothingWasCreated. A function of its own so a test reaches the
// branch, not only the predicate.
func createNodeShell(ctx context.Context, pods podCreator, spec *corev1.Pod) (string, error) {
	created, err := pods.Create(ctx, spec, metav1.CreateOptions{})
	if err != nil {
		if nothingWasCreated(err) {
			return "", err
		}
		return spec.Name, err
	}
	return created.Name, nil
}

// nothingWasCreated reports whether a Create error proves no object exists.
// The question is "could it have worked", not "did it fail": a rejected request
// (bad shape, no permission, name taken) created nothing, and cleaning up after
// it would delete an object this call did not make. Anything else, timeouts,
// 5xx and dropped connections included, may have succeeded. The default is
// "something may exist", which costs at most one needless delete.
func nothingWasCreated(err error) bool {
	switch {
	case apierrors.IsAlreadyExists(err),
		apierrors.IsInvalid(err),
		apierrors.IsBadRequest(err),
		apierrors.IsForbidden(err),
		apierrors.IsUnauthorized(err),
		apierrors.IsMethodNotSupported(err),
		apierrors.IsNotFound(err):
		return true
	}
	return false
}

// podReader is the slice of the typed client waitForNodeShell uses, declared
// so it can be tested against a fake.
type podReader interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.Pod, error)
}

// waitForNodeShell blocks until the pod runs, or says why it will not. It
// reports the reason, not the seconds: a pod that cannot pull its image sits in
// Pending with `ErrImagePull`, which dots would turn into "the node shell is
// broken". It takes `say` rather than the stream so a test needs no WebSocket.
func waitForNodeShell(ctx context.Context, say func(string), pods podReader, name string) error {
	deadline := time.Now().Add(nodeShellStartTimeout)
	said := ""

	for {
		pod, err := pods.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		switch pod.Status.Phase {
		case corev1.PodRunning:
			return nil
		case corev1.PodFailed, corev1.PodSucceeded:
			// Succeeded is a failure here: the container is parked on a sleep, so it
			// finishing means the image had no /bin/sh.
			return fmt.Errorf("the pod ended in %s before a shell could be opened%s",
				strings.ToLower(string(pod.Status.Phase)), detailSuffix(nodeShellReason(pod)))
		}

		if reason := nodeShellReason(pod); reason != "" && reason != said {
			said = reason
			say(reason)
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("the pod was still %s after %s%s",
				strings.ToLower(string(pod.Status.Phase)), nodeShellStartTimeout, detailSuffix(said))
		}

		select {
		case <-ctx.Done():
			// The last thing the cluster said goes out with the cancellation;
			// "context deadline exceeded" alone says nothing useful.
			return fmt.Errorf("%w%s", ctx.Err(), detailSuffix(said))
		case <-time.After(nodeShellPoll):
		}
	}
}

// detailSuffix appends a reason to a sentence, or nothing, so a missing reason
// does not print "()".
func detailSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return " — " + reason
}

// nodeShellReason is what the cluster says about a pod that is not running
// yet: the container's waiting reason first (`ErrImagePull`,
// `ImagePullBackOff`, `CreateContainerError`, the failures a chosen image
// produces), then the pod conditions, where the scheduler explains a taint.
func nodeShellReason(pod *corev1.Pod) string {
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Waiting == nil {
			continue
		}
		waiting := status.State.Waiting
		// ContainerCreating and PodInitializing are the ordinary path, not news.
		if waiting.Reason == "ContainerCreating" || waiting.Reason == "PodInitializing" {
			continue
		}
		if waiting.Message != "" {
			return waiting.Reason + ": " + textutil.FirstLine(waiting.Message)
		}
		return waiting.Reason
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse {
			if condition.Message != "" {
				return condition.Reason + ": " + textutil.FirstLine(condition.Message)
			}
			return condition.Reason
		}
	}
	if pod.Status.Reason != "" {
		return pod.Status.Reason
	}
	return ""
}

// nodeShellRemoval is the request the console makes to remove a node-shell pod
// it created: `delete pods` on that pod, in the node-shell namespace.
//
// Not reviewed, and named as such (access.Housekeeping): a privileged pod left
// on a node because nobody was there to be asked is what the removal prevents.
// The stamp covers that one pod only, and choke_test.go lists both callers.
func nodeShellRemoval(pod string) access.Attributes {
	return access.Attributes{Verb: "delete", Version: "v1", Resource: "pods", Namespace: nodeShellNamespace, Name: pod}
}

// removeNodeShell deletes the pod the session was using, on a background
// context: the request's is canceled the moment the browser goes, which is
// when this is needed. A failure is logged loudly, naming the pod so it can be
// removed by hand.
func (t *Terminal) removeNodeShell(clusterName, pod string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := t.clusters.Client(clusterName)
	if err != nil {
		t.logf("CANNOT DELETE node shell pod %s/%s on %q: %v - remove it by hand",
			nodeShellNamespace, pod, clusterName, err)
		return
	}
	// Nobody is behind this request (the browser has gone), so it is
	// housekeeping, stamped for this one pod.
	ctx = access.Housekeeping(ctx, clusterName, nodeShellRemoval(pod))
	err = client.Clients.CoreV1().Pods(nodeShellNamespace).Delete(ctx, pod, metav1.DeleteOptions{})
	switch {
	case err == nil:
		t.logf("node shell pod %s/%s deleted on %q", nodeShellNamespace, pod, clusterName)
	case apierrors.IsNotFound(err):
		// Already gone: nothing is left on the node.
	default:
		t.logf("CANNOT DELETE node shell pod %s/%s on %q: %v - remove it by hand "+
			"(kubectl -n %s delete pod %s)",
			nodeShellNamespace, pod, clusterName, err, nodeShellNamespace, pod)
	}
}

// nodeShellPod is the manifest: `kubectl debug node/<name>`'s pod, written
// out. Pinned to the node by name, sharing the host's PID, network and IPC
// namespaces, tolerating every taint, and mounting the host's root at /host.
//
// Not privileged, as `kubectl debug node/…` is not in its default profile:
// `chroot /host` needs CAP_SYS_CHROOT, which every container has, and
// `privileged: true` would add reconfiguring the node's kernel, which a console
// button should not hand out by default.
func nodeShellPod(node, image string) *corev1.Pod {
	deadline := int64(nodeShellDeadline / time.Second)
	grace := int64(5)
	root := corev1.HostPathDirectory

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeShellPodName(node),
			Namespace: nodeShellNamespace,
			Labels:    nodeShellLabels,
			Annotations: map[string]string{
				// Which node, visible in `kubectl get pod -o yaml` even when the name is
				// truncated.
				"kubetower.io/node": node,
				"kubetower.io/why": "A shell opened from the KubeTower console. It is deleted when " +
					"that terminal closes; activeDeadlineSeconds stops it if KubeTower does not.",
			},
		},
		Spec: corev1.PodSpec{
			NodeName:    node,
			HostPID:     true,
			HostIPC:     true,
			HostNetwork: true,
			// Never: a restarted debug shell outlives its reason.
			RestartPolicy:                 corev1.RestartPolicyNever,
			ActiveDeadlineSeconds:         &deadline,
			TerminationGracePeriodSeconds: &grace,
			// Every taint: the node worth a shell is usually cordoned, NotReady or under
			// disk pressure.
			Tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
			Volumes: []corev1.Volume{{
				Name: "host-root",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{Path: "/", Type: &root},
				},
			}},
			Containers: []corev1.Container{{
				Name:    nodeShellContainer,
				Image:   image,
				Command: nodeShellPark,
				// IfNotPresent: a node with the image opens a shell in a second, and pulling
				// on every click would be slow on a slow registry.
				ImagePullPolicy:          corev1.PullIfNotPresent,
				TerminationMessagePolicy: corev1.TerminationMessageReadFile,
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "host-root",
					MountPath: "/host",
				}},
			}},
		},
	}
}

// nodeShellPodName is a name that says what it is and cannot collide. The node
// name is in it for whoever reads `kubectl get pods`, truncated because an EKS
// node name can be long; the random tail makes two shells on one node two pods.
func nodeShellPodName(node string) string {
	const prefix = "kubetower-node-shell-"
	const room = 40

	short := node
	if len(short) > room {
		short = short[:room]
	}
	// A trailing separator left by the truncation would make an invalid name.
	short = strings.Trim(short, "-.")
	if short == "" {
		short = "node"
	}

	buf := make([]byte, 3)
	if _, err := rand.Read(buf); err != nil {
		// Time is a poorer suffix than randomness and a better one than a collision.
		return prefix + short + "-" + fmt.Sprintf("%06x", time.Now().UnixNano()&0xffffff)
	}
	return prefix + short + "-" + hex.EncodeToString(buf)
}
