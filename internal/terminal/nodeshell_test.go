package terminal

// The shell on a node, and the four things about it that are easy to break
// invisibly: that the pod it creates is the one `kubectl debug node/…` creates,
// that it is refused where every other mutating action is, that a pod which
// never ran is still removed, and that a wait says *why* rather than counting
// seconds.

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// Each of these pod fields is the difference between a shell on the node and a
// shell in a container that happens to be on it, and none of them is visible
// from the terminal once it opens — a pod without hostPID looks exactly like a
// pod with it until somebody runs `ps`.
func TestTheDebugPodCanActuallySeeTheNode(t *testing.T) {
	pod := nodeShellPod("ip-10-0-3-14.eu-west-1.compute.internal", "alpine:3.24")

	if pod.Spec.NodeName != "ip-10-0-3-14.eu-west-1.compute.internal" {
		t.Errorf("the pod is not pinned to the node (%q); the scheduler would put it anywhere",
			pod.Spec.NodeName)
	}
	if !pod.Spec.HostPID || !pod.Spec.HostIPC || !pod.Spec.HostNetwork {
		t.Errorf("the pod does not share the host's namespaces (pid %t, ipc %t, net %t), so it is "+
			"a shell in a container and not a shell on a machine",
			pod.Spec.HostPID, pod.Spec.HostIPC, pod.Spec.HostNetwork)
	}

	var mounted bool
	for _, volume := range pod.Spec.Volumes {
		if volume.HostPath != nil && volume.HostPath.Path == "/" {
			mounted = true
		}
	}
	if !mounted {
		t.Error("the node's root filesystem is not mounted, so `chroot /host` has nothing to chroot to")
	}
	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("the pod has %d containers", len(pod.Spec.Containers))
	}
	container := pod.Spec.Containers[0]
	if container.Image != "alpine:3.24" {
		t.Errorf("the pod runs %q rather than the configured image", container.Image)
	}
	if len(container.VolumeMounts) == 0 || container.VolumeMounts[0].MountPath != "/host" {
		t.Errorf("the host filesystem is not at /host: %+v", container.VolumeMounts)
	}

	// Every taint. The node worth opening a shell on is usually the cordoned,
	// NotReady, disk-pressured one — all of which are taints that would
	// otherwise refuse this pod, and a node shell that will not open on a broken
	// node is a node shell for nothing.
	if len(pod.Spec.Tolerations) == 0 || pod.Spec.Tolerations[0].Operator != corev1.TolerationOpExists {
		t.Errorf("the pod does not tolerate every taint, so it will not schedule onto a cordoned "+
			"or NotReady node: %+v", pod.Spec.Tolerations)
	}
}

// The pod is parked on a sleep rather than being the shell itself, so it stays
// Running whatever the image's entrypoint is and can be exec'd into more than
// once. busybox `sleep` refuses `infinity`, so the loop is checked by name.
func TestTheDebugPodIsParkedRatherThanBeingTheShell(t *testing.T) {
	pod := nodeShellPod("node-a", "alpine:3.24")
	command := strings.Join(pod.Spec.Containers[0].Command, " ")
	if !strings.Contains(command, "sleep") {
		t.Errorf("the container is not parked: %q", command)
	}
	if strings.Contains(command, "sleep infinity") {
		t.Errorf("`sleep infinity` is not busybox's sleep and the default image is Alpine: %q", command)
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy is %q; a debug shell a controller restarts outlives the reason for it",
			pod.Spec.RestartPolicy)
	}
}

// The backstop for the case this console cannot handle: KubeTower killed with a
// session open. Without a deadline on the pod itself, that leaves a shell
// running on a production node until somebody notices it.
func TestThePodStopsItselfIfKubeTowerDoesNot(t *testing.T) {
	pod := nodeShellPod("node-a", "alpine:3.24")
	if pod.Spec.ActiveDeadlineSeconds == nil {
		t.Fatal("the pod has no activeDeadlineSeconds, so a KubeTower that dies mid-session leaves " +
			"a pod on the node with nothing left to remove it")
	}
	if got := *pod.Spec.ActiveDeadlineSeconds; got != int64(nodeShellDeadline/time.Second) {
		t.Errorf("activeDeadlineSeconds is %d, want %d", got, int64(nodeShellDeadline/time.Second))
	}
	if pod.Labels["app.kubernetes.io/managed-by"] != "kubetower" {
		t.Errorf("the pod is not labeled as ours, so 'did this console leave anything behind' has "+
			"no one-line answer: %v", pod.Labels)
	}
}

// A node name can be sixty characters on EKS and a pod name has to stay a DNS
// name. The suffix is what makes two shells on one node two pods rather than a
// conflict.
func TestThePodNameSurvivesALongNodeName(t *testing.T) {
	long := strings.Repeat("ip-10-0-3-14.eu-west-1.compute.internal", 4)
	first := nodeShellPodName(long)
	second := nodeShellPodName(long)

	if first == second {
		t.Error("two shells on one node produced the same pod name, so the second is a conflict")
	}
	for _, name := range []string{first, second, nodeShellPodName("a"), nodeShellPodName("---")} {
		if len(name) > 253 {
			t.Errorf("%q is %d characters, past what a pod name may be", name, len(name))
		}
		if !validName(name) {
			t.Errorf("%q is not a Kubernetes name, so the API server refuses the pod", name)
		}
		if !strings.HasPrefix(name, "kubetower-") {
			t.Errorf("%q does not say whose it is in `kubectl get pods`", name)
		}
	}
}

// --- the wait ------------------------------------------------------------------

// stubPods answers Get from a script, so the wait can be driven through its
// states without a cluster.
type stubPods struct {
	pods []*corev1.Pod
	at   int
	err  error
}

func (s *stubPods) Get(context.Context, string, metav1.GetOptions) (*corev1.Pod, error) {
	if s.err != nil {
		return nil, s.err
	}
	pod := s.pods[s.at]
	if s.at < len(s.pods)-1 {
		s.at++
	}
	return pod, nil
}

func pending(reason, message string) *corev1.Pod {
	return &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodPending,
		ContainerStatuses: []corev1.ContainerStatus{{
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
				Reason: reason, Message: message,
			}},
		}},
	}}
}

// A pod that becomes ready is waited for and nothing is said about it beyond
// what happened.
func TestTheWaitReturnsOnceThePodRuns(t *testing.T) {
	pods := &stubPods{pods: []*corev1.Pod{
		pending("ContainerCreating", ""),
		{Status: corev1.PodStatus{Phase: corev1.PodRunning}},
	}}
	if err := waitForNodeShell(context.Background(), func(string) {}, pods, "p"); err != nil {
		t.Fatalf("waiting for a pod that runs: %v", err)
	}
}

// **The reason, not the seconds.** A pod that cannot pull its image sits in
// Pending with ErrImagePull on it, and a wait that only counts turns "you chose
// an image that does not exist" into "the node shell is broken" — which is the
// difference between a setting somebody fixes and a feature somebody stops
// using. The image is the operator's own choice here, so this is the failure
// this feature will actually produce.
func TestTheWaitSaysWhyRatherThanHowLong(t *testing.T) {
	pods := &stubPods{pods: []*corev1.Pod{
		pending("ErrImagePull", "failed to pull image \"alpine:3.99\": not found"),
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := waitForNodeShell(ctx, func(string) {}, pods, "p")
	if err == nil {
		t.Fatal("a pod that never runs was waited for successfully")
	}
	if !strings.Contains(err.Error(), "ErrImagePull") {
		t.Errorf("the failure does not name what the cluster said: %v", err)
	}
}

// A pod that *ends* is a failure here, however it ended: the container is parked
// on a sleep, so it finishing means the image had no /bin/sh in it. Reported as
// what it is rather than waited out for two minutes.
func TestAPodThatEndsIsNotWaitedOutForTwoMinutes(t *testing.T) {
	for _, phase := range []corev1.PodPhase{corev1.PodFailed, corev1.PodSucceeded} {
		pods := &stubPods{pods: []*corev1.Pod{{Status: corev1.PodStatus{
			Phase: phase, Reason: "StartError",
		}}}}
		err := waitForNodeShell(context.Background(), func(string) {}, pods, "p")
		if err == nil {
			t.Fatalf("a %s pod was taken as a working shell", phase)
		}
		if !strings.Contains(err.Error(), "StartError") {
			t.Errorf("%s: the failure does not carry the reason: %v", phase, err)
		}
	}
}

// The scheduler's own explanation, for the other half of the failures — a node
// whose taints this pod somehow does not tolerate, or one that is full.
func TestTheWaitReadsTheSchedulersRefusalToo(t *testing.T) {
	pod := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodPending,
		Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
			Reason: "Unschedulable", Message: "0/9 nodes are available: 9 Insufficient memory.",
		}},
	}}
	if got := nodeShellReason(pod); !strings.Contains(got, "Insufficient memory") {
		t.Errorf("nodeShellReason lost the scheduler's sentence: %q", got)
	}

	// The ordinary path is not news. A notice per poll saying "ContainerCreating"
	// is a pane of scrolling noise in front of a shell that is about to open.
	if got := nodeShellReason(pending("ContainerCreating", "")); got != "" {
		t.Errorf("the ordinary path was announced as a problem: %q", got)
	}
	if got := nodeShellReason(&corev1.Pod{}); got != "" {
		t.Errorf("a pod with nothing to say produced %q", got)
	}
}

// An error reading the pod ends the wait rather than being retried until the
// deadline: a 403 does not become a 200 in two minutes, and two minutes of
// silence in front of one is worse than the sentence.
func TestTheWaitGivesUpOnAnErrorRatherThanRetryingIt(t *testing.T) {
	pods := &stubPods{err: errors.New("pods is forbidden")}
	start := time.Now()
	err := waitForNodeShell(context.Background(), func(string) {}, pods, "p")
	if err == nil {
		t.Fatal("an unreadable pod was waited for successfully")
	}
	if time.Since(start) > time.Second {
		t.Errorf("it retried a permission error for %s", time.Since(start))
	}
}

// The read-only refusal on the node shell must not be deletable with the whole
// suite green. The pod "is refused where every other mutating action is", and
// the modal drawing no button is only the courtesy, which
// internal/web/nodeshell.go's own header distinguishes from the boundary.
//
// Unlike the pod exec, this refusal happens *after* the upgrade and in-band, for
// the reason serveNodeShell gives: a handshake turned away reaches the dock as
// nothing at all. So the test has to complete a real handshake and read the
// message, which is also what makes it a test of the thing rather than of a
// status code.
func TestTheNodeShellIsRefusedOnAReadOnlyCluster(t *testing.T) {
	// The loopback rewrite must not fire: this address is deliberately dead and
	// has to stay dead, so that a guard which stopped refusing would hang here
	// rather than reach a cluster. See internal/cluster/loopback.go.
	t.Setenv("KT_IN_CONTAINER", "0")
	kubeconfig := execKubeconfig(t, "prod-eks", "lab")

	term, err := New(managerFor(t, kubeconfig), Options{
		Kubeconfig: cluster.Kubeconfig{kubeconfig},
		ReadOnly:   func(name string) bool { return name == "prod-eks" },
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(term.NodeShellHandler())
	t.Cleanup(srv.Close)
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	client, _, err := websocket.DefaultDialer.Dial(url+"/kt-api/nodeshell?cluster=prod-eks&node=ip-10-0-3-14", nil)
	if err != nil {
		t.Fatalf("the handshake failed, so the refusal could not be read: %v", err)
	}
	defer client.Close()

	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("nothing was said before the socket closed: %v", err)
	}
	said := string(data)

	// Named, or the operator has no way to know why a button stopped working.
	for _, want := range []string{"prod-eks", "KT_READONLY_CLUSTERS"} {
		if !strings.Contains(said, want) {
			t.Errorf("the refusal does not mention %q: %q", want, said)
		}
	}
	// And it must be a refusal, not a report of progress. "Creating" is what the
	// allowed path says first, so its presence here would mean the guard was
	// passed and the pod was on its way.
	if strings.Contains(said, "Creating") {
		t.Errorf("the node shell started creating a pod on a read-only cluster: %q", said)
	}
}

// The other half, and the half that fails silently: a cluster nobody marked must
// not be refused. A guard that refuses everything passes the test above.
func TestTheNodeShellIsNotRefusedOnAnUnmarkedCluster(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")
	kubeconfig := execKubeconfig(t, "prod-eks", "lab")

	term, err := New(managerFor(t, kubeconfig), Options{
		Kubeconfig: cluster.Kubeconfig{kubeconfig},
		ReadOnly:   func(name string) bool { return name == "prod-eks" },
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(term.NodeShellHandler())
	t.Cleanup(srv.Close)
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	client, _, err := websocket.DefaultDialer.Dial(url+"/kt-api/nodeshell?cluster=lab&node=ip-10-0-3-14", nil)
	if err != nil {
		t.Fatalf("the handshake failed: %v", err)
	}
	defer client.Close()

	_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, data, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("nothing was said before the socket closed: %v", err)
	}
	said := string(data)

	if strings.Contains(said, "KT_READONLY_CLUSTERS") {
		t.Errorf("an unmarked cluster was refused by the read-only guard: %q", said)
	}
}

// A Create whose request arrived and whose response did not. Returning "" would
// tell serveNodeShell there is nothing to clean up, and a
// hostPID/hostIPC/hostNetwork pod with the node's root filesystem at /host would
// sit on the node for its full four-hour activeDeadlineSeconds.
//
// The classification is the whole point, so it is what is asserted. The default
// matters most: an error kind nobody anticipated must be treated as "something
// may exist", because the cost of being wrong that way is one delete of a name
// that is probably not there.
func TestOnlyAnErrorThatProvesNothingWasCreatedSkipsTheCleanup(t *testing.T) {
	gvr := schema.GroupResource{Resource: "pods"}

	mustClean := []struct {
		name string
		err  error
	}{
		{"a timeout", apierrors.NewTimeoutError("too slow", 1)},
		{"an unavailable apiserver", apierrors.NewServiceUnavailable("no endpoints")},
		{"an internal error", apierrors.NewInternalError(errors.New("etcd said no"))},
		{"a server timeout", apierrors.NewServerTimeout(gvr, "create", 1)},
		{"too many requests", apierrors.NewTooManyRequestsError("slow down")},
		{"a dropped connection", errors.New("unexpected EOF")},
		{"a canceled context", context.Canceled},
	}
	for _, c := range mustClean {
		if nothingWasCreated(c.err) {
			t.Errorf("%s was treated as having created nothing, so the pod would be left on the node: %v",
				c.name, c.err)
		}
	}

	mustNotClean := []struct {
		name string
		err  error
	}{
		// The one that would delete somebody else's shell.
		{"already exists", apierrors.NewAlreadyExists(gvr, "kubetower-node-shell-x-abc123")},
		{"forbidden", apierrors.NewForbidden(gvr, "x", errors.New("no"))},
		{"unauthorised", apierrors.NewUnauthorized("no token")},
		{"invalid", apierrors.NewInvalid(schema.GroupKind{Kind: "Pod"}, "x", nil)},
		{"bad request", apierrors.NewBadRequest("malformed")},
	}
	for _, c := range mustNotClean {
		if !nothingWasCreated(c.err) {
			t.Errorf("%s can only mean nothing was created, so cleaning up would delete an object "+
				"this call did not make: %v", c.name, c.err)
		}
	}
}

// The sweep's whole safety property, and the reason it is a separate
// function: a pod that might be somebody's live shell must never be deleted.
//
// Two KubeTowers against one cluster is a supported configuration —
// `go run ./cmd/kubetower` beside the container — so "our label is on it"
// does not mean "we own it". Getting this backwards deletes a root shell on a
// production node out from under the person typing in it, which is a worse bug
// than the leak the sweep exists to clean up.
func TestTheSweepNeverDeletesAPodThatCouldStillBeInUse(t *testing.T) {
	leftovers := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "deadline-exceeded"},
			Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "DeadlineExceeded"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "completed"},
			Status: corev1.PodStatus{Phase: corev1.PodSucceeded}},
		{ObjectMeta: metav1.ObjectMeta{Name: "somebody-is-in-this-one"},
			Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		{ObjectMeta: metav1.ObjectMeta{Name: "still-pulling"},
			Status: corev1.PodStatus{Phase: corev1.PodPending}},
		// A phase this build has not heard of. The default arm must be the safe
		// one: unknown is not dead.
		{ObjectMeta: metav1.ObjectMeta{Name: "from-a-later-kubernetes"},
			Status: corev1.PodStatus{Phase: corev1.PodPhase("Hibernating")}},
	}

	residue, live := partitionLeftovers(leftovers)

	deleted := map[string]bool{}
	for _, pod := range residue {
		deleted[pod.name] = true
	}
	for _, mustSurvive := range []string{"somebody-is-in-this-one", "still-pulling", "from-a-later-kubernetes"} {
		if deleted[mustSurvive] {
			t.Errorf("the sweep would delete %q, which Kubernetes has not stopped — "+
				"that may be another KubeTower's live shell on a node", mustSurvive)
		}
	}

	// And it does clean what it is for, or it is an expensive no-op.
	for _, mustGo := range []string{"deadline-exceeded", "completed"} {
		if !deleted[mustGo] {
			t.Errorf("%q was left behind; it is residue and cannot be anybody's session", mustGo)
		}
	}
	if len(live) != 3 {
		t.Errorf("%d pods were reported rather than 3; every one not deleted must be named "+
			"to the operator, or the sweep hides what it decided not to touch", len(live))
	}

	// The stated reason travels with the decision, because a log line saying
	// "removed" without saying why is the one nobody can audit afterwards.
	for _, pod := range residue {
		if pod.why == "" {
			t.Errorf("%q was deleted with no reason recorded", pod.name)
		}
	}
	if residue[0].why != "Failed, DeadlineExceeded" {
		t.Errorf("the reason is %q, want the phase and the reason together", residue[0].why)
	}
}

// An empty cluster is the ordinary case and must not produce noise.
func TestTheSweepSaysNothingWhenThereIsNothingToSay(t *testing.T) {
	residue, live := partitionLeftovers(nil)
	if len(residue) != 0 || len(live) != 0 {
		t.Errorf("a cluster with no leftovers produced %d deletions and %d reports",
			len(residue), len(live))
	}
}

// fakeCreator is a pods.Create that answers however the test says.
type fakeCreator struct {
	err     error
	created *corev1.Pod
	calls   int
}

func (f *fakeCreator) Create(_ context.Context, pod *corev1.Pod, _ metav1.CreateOptions) (*corev1.Pod, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	f.created = pod
	return pod, nil
}

// The call site, not only the predicate. Asserting nothingWasCreated over twelve
// error kinds leaves the suite green with the entire `if nothingWasCreated(err)`
// branch deleted from the call site, because nothing drives the line that joins
// the predicate to the behavior. That is why createNodeShell is a function
// rather than five lines inside a longer one.
//
// So this asserts what serveNodeShell actually reads: the *name*, because a name
// is what makes the deferred removeNodeShell run.
func TestALostCreateResponseStillYieldsANameToCleanUp(t *testing.T) {
	spec := nodeShellPod("ip-10-0-3-14", "alpine:3.24")

	// A response that never arrived. The pod may well exist on the node.
	lost := &fakeCreator{err: apierrors.NewTimeoutError("etcd took too long", 1)}
	name, err := createNodeShell(t.Context(), lost, spec)
	if err == nil {
		t.Fatal("the failure was swallowed")
	}
	if name != spec.Name {
		t.Errorf("name is %q, want %q — serveNodeShell defers removeNodeShell only for a "+
			"non-empty name, so an empty one leaves a hostPID pod with the node's root "+
			"filesystem at /host sitting there for four hours", name, spec.Name)
	}

	// And the one error that means the name is somebody else's. Cleaning up here
	// would delete another session's shell.
	taken := &fakeCreator{err: apierrors.NewAlreadyExists(
		schema.GroupResource{Resource: "pods"}, spec.Name)}
	name, err = createNodeShell(t.Context(), taken, spec)
	if err == nil {
		t.Fatal("AlreadyExists was swallowed")
	}
	if name != "" {
		t.Errorf("name is %q; this call created nothing, and deleting that pod would take "+
			"another session's shell with it", name)
	}

	// The ordinary path still answers with what the server actually stored, which
	// is not always what was asked for.
	ok := &fakeCreator{}
	name, err = createNodeShell(t.Context(), ok, spec)
	if err != nil {
		t.Fatalf("a successful create failed: %v", err)
	}
	if name != spec.Name {
		t.Errorf("name is %q, want %q", name, spec.Name)
	}
	if ok.calls != 1 {
		t.Errorf("Create was called %d times", ok.calls)
	}
}

// nodeShellLists counts the times the sweep asked the cluster for node-shell
// pods, which is what "the sweep ran" means from outside.
func nodeShellLists(clientset *fake.Clientset) int {
	n := 0
	for _, action := range clientset.Actions() {
		if action.Matches("list", "pods") && action.GetNamespace() == nodeShellNamespace {
			n++
		}
	}
	return n
}

func sweeper() *Terminal {
	return &Terminal{logf: func(string, ...any) {}}
}

// A sweep that could not reach the cluster has not looked, so it must not count
// as the once-per-process sweep. A lapsed SSO session at the first shell would
// otherwise leave that cluster's leftovers unlooked-for until the next restart.
func TestASweepThatCouldNotGetAClientIsTriedAgain(t *testing.T) {
	clientset := fake.NewClientset()
	clients := &fakeManager{failFirst: 1, client: &cluster.Client{Context: "prod", Clients: clientset}}
	term := sweeper()

	term.sweepNodeShells(context.Background(), clients, "prod")
	term.sweepNodeShells(context.Background(), clients, "prod")

	if got := nodeShellLists(clientset); got != 1 {
		t.Fatalf("the second sweep listed node-shell pods %d times, want 1: "+
			"a failed first attempt was remembered as done", got)
	}

	// And once it has looked, it is done.
	term.sweepNodeShells(context.Background(), clients, "prod")
	if got := nodeShellLists(clientset); got != 1 {
		t.Errorf("a sweep that listed ran again: %d lists, want 1", got)
	}
}

// The same for a client that exists but whose List fails, a token refused or a
// cluster briefly unreachable.
func TestASweepWhoseListFailedIsTriedAgain(t *testing.T) {
	clientset := fake.NewClientset()
	failed := false
	clientset.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		if failed {
			return false, nil, nil
		}
		failed = true
		return true, nil, errors.New("Unauthorized")
	})
	clients := &fakeManager{client: &cluster.Client{Context: "prod", Clients: clientset}}
	term := sweeper()

	term.sweepNodeShells(context.Background(), clients, "prod")
	term.sweepNodeShells(context.Background(), clients, "prod")

	if got := nodeShellLists(clientset); got != 2 {
		t.Fatalf("node-shell pods were listed %d times over two sweeps, want 2: "+
			"a failed List was remembered as done", got)
	}

	term.sweepNodeShells(context.Background(), clients, "prod")
	if got := nodeShellLists(clientset); got != 2 {
		t.Errorf("a sweep that listed ran again: %d lists, want 2", got)
	}
}
