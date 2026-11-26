package terminal

// The debug session against a fake API server: what the PATCH carries, which
// reviews it asks and records, that a read-only cluster is refused with no call
// at all, each refusal code, the wait's three outcomes, and that the shell opens
// in the container that was added.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/identity"
)

// debugAPI is a fake API server holding one pod, shop/api-1, with one
// container, api. patch answers the PATCH on its ephemeralcontainers
// subresource (nil accepts it); state is what the kubelet reports about the
// added container on every read after it.
type debugAPI struct {
	patch func(w http.ResponseWriter)
	state corev1.ContainerState
	// noPod answers the pod's read with a 404.
	noPod bool

	mu       sync.Mutex
	requests []string
	body     []byte
	added    string
}

func (d *debugAPI) start(t *testing.T) string {
	t.Helper()
	write := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.requests = append(d.requests, r.Method+" "+r.URL.Path)
		d.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces/shop/pods/api-1":
			if d.noPod {
				write(w, http.StatusNotFound, statusOf(http.StatusNotFound, metav1.StatusReasonNotFound, `pods "api-1" not found`))
				return
			}
			write(w, http.StatusOK, d.pod())
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/namespaces/shop/pods/api-1/ephemeralcontainers":
			body, _ := io.ReadAll(r.Body)
			var patch struct {
				Spec struct {
					EphemeralContainers []corev1.EphemeralContainer `json:"ephemeralContainers"`
				} `json:"spec"`
			}
			_ = json.Unmarshal(body, &patch)
			d.mu.Lock()
			d.body = body
			if len(patch.Spec.EphemeralContainers) == 1 {
				d.added = patch.Spec.EphemeralContainers[0].Name
			}
			d.mu.Unlock()
			if d.patch != nil {
				d.patch(w)
				return
			}
			write(w, http.StatusOK, d.pod())
		default:
			http.Error(w, "not here", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (d *debugAPI) pod() *corev1.Pod {
	d.mu.Lock()
	defer d.mu.Unlock()
	pod := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "shop"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "shop/api:1"}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if d.added != "" {
		pod.Spec.EphemeralContainers = []corev1.EphemeralContainer{{
			EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: d.added, Image: "busybox:1.36"},
		}}
		pod.Status.EphemeralContainerStatuses = []corev1.ContainerStatus{{Name: d.added, State: d.state}}
	}
	return pod
}

func (d *debugAPI) seen() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.requests...)
}

func (d *debugAPI) patched() bool {
	for _, r := range d.seen() {
		if strings.HasPrefix(r, "PATCH ") {
			return true
		}
	}
	return false
}

func statusOf(code int, reason metav1.StatusReason, message string) *metav1.Status {
	return &metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status: metav1.StatusFailure, Code: int32(code), Reason: reason, Message: message}
}

func refuseWith(code int, reason metav1.StatusReason, message string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(statusOf(code, reason, message))
	}
}

var running = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}

// debugRig is a Terminal on the fake, its exec captured rather than sent.
type debugRig struct {
	term  *Terminal
	api   *debugAPI
	log   *recordingLog
	sink  *streamSink
	execs chan *url.URL
	srv   *httptest.Server
}

func newDebugRig(t *testing.T, api *debugAPI, gate *access.Gate, readOnly bool) *debugRig {
	t.Helper()
	t.Setenv("KT_IN_CONTAINER", "0")
	server := api.start(t)
	kubeconfig := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\nkind: Config\ncurrent-context: lab\nclusters:\n- name: lab\n  cluster:\n    server: "+server+
		"\ncontexts:\n- name: lab\n  context:\n    cluster: lab\n    user: lab\nusers:\n- name: lab\n  user:\n    token: t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if gate == nil {
		gate = accesstest.Allow()
	}
	term, err := New(managerFor(t, kubeconfig), Options{
		Kubeconfig: cluster.Kubeconfig{kubeconfig},
		Access:     gate,
		ReadOnly:   func(string) bool { return readOnly },
	})
	if err != nil {
		t.Fatal(err)
	}
	rig := &debugRig{term: term, api: api, log: &recordingLog{}, execs: make(chan *url.URL, 4)}
	term.logf = rig.log.logf
	term.debugWait, term.debugPoll = 2*time.Second, 10*time.Millisecond
	term.openExec = func(_ *rest.Config, target *url.URL) (remotecommand.Executor, error) {
		rig.execs <- target
		return exitingExecutor{}, nil
	}
	rig.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		term.FramedDebugHandler().ServeHTTP(w, r.WithContext(identity.With(r.Context(), erin)))
	}))
	t.Cleanup(rig.srv.Close)
	return rig
}

func (rig *debugRig) address(query string) string {
	return "ws" + strings.TrimPrefix(rig.srv.URL, "http") + "/?cluster=lab&" + query
}

// open dials the debug socket and reads every frame up to the close.
func (rig *debugRig) open(t *testing.T, query string) []serverFrame {
	t.Helper()
	return framesUntilClose(t, dialFramed(t, rig.address(query)))
}

const debugQuery = "namespace=shop&pod=api-1&image=busybox:1.36&target=api"

// refusal is the first `state` frame, or a failure naming what arrived.
func refusal(t *testing.T, frames []serverFrame) serverFrame {
	t.Helper()
	for _, f := range frames {
		if f.Type == "state" {
			if f.Reason == nil {
				t.Fatalf("a state frame without a reason: %+v", f)
			}
			return f
		}
	}
	t.Fatalf("no refusal among the frames: %+v", frames)
	return serverFrame{}
}

// The PATCH is the container `kubectl debug -it --target` adds: named by the
// console, the chosen image, the image's sh on stdin and a terminal so it stays
// up, the target named, pulled only if absent, and nothing else asked of it.
func TestTheDebugPatchAddsOneContainerThatStaysUp(t *testing.T) {
	api := &debugAPI{state: running}
	rig := newDebugRig(t, api, nil, false)
	frames := rig.open(t, debugQuery)
	if last := lastFrame(t, frames); last.Type != "exit" {
		t.Fatalf("the session did not open and end: %+v", frames)
	}

	api.mu.Lock()
	body := api.body
	api.mu.Unlock()
	var patch struct {
		Spec struct {
			EphemeralContainers []corev1.EphemeralContainer `json:"ephemeralContainers"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(body, &patch); err != nil {
		t.Fatalf("the PATCH body is not JSON: %v (%s)", err, body)
	}
	if len(patch.Spec.EphemeralContainers) != 1 {
		t.Fatalf("the PATCH adds %d containers: %s", len(patch.Spec.EphemeralContainers), body)
	}
	c := patch.Spec.EphemeralContainers[0]
	if !strings.HasPrefix(c.Name, "kt-debug-") || len(c.Name) != len("kt-debug-")+5 || !validName(c.Name) {
		t.Errorf("the container is named %q, want kt-debug- and five lowercase characters", c.Name)
	}
	if c.Image != "busybox:1.36" || c.TargetContainerName != "api" {
		t.Errorf("image %q target %q, want busybox:1.36 and api", c.Image, c.TargetContainerName)
	}
	if len(c.Command) != 1 || c.Command[0] != "sh" || !c.Stdin || !c.TTY {
		t.Errorf("the container runs %v stdin=%t tty=%t; without sh on stdin and a tty it ends at once",
			c.Command, c.Stdin, c.TTY)
	}
	if c.ImagePullPolicy != corev1.PullIfNotPresent || c.SecurityContext != nil {
		t.Errorf("pull %q securityContext %+v, want IfNotPresent and none", c.ImagePullPolicy, c.SecurityContext)
	}
	// A strategic-merge PATCH, which merges the list by name and keeps the pod's
	// earlier debug containers.
	if !strings.Contains(string(body), `"ephemeralContainers"`) {
		t.Errorf("the body does not name the list: %s", body)
	}
}

// The shell opens in the container that was added, with /bin/sh, through the
// exec path.
func TestTheDebugShellOpensInTheAddedContainer(t *testing.T) {
	api := &debugAPI{state: running}
	rig := newDebugRig(t, api, nil, false)
	rig.open(t, debugQuery)

	select {
	case target := <-rig.execs:
		api.mu.Lock()
		added := api.added
		api.mu.Unlock()
		q := target.Query()
		if !strings.HasSuffix(target.Path, "/namespaces/shop/pods/api-1/exec") {
			t.Errorf("the exec went to %s", target.Path)
		}
		if q.Get("container") != added || added == "" {
			t.Errorf("the exec opened in container %q, want the added %q", q.Get("container"), added)
		}
		if q.Get("command") != "/bin/sh" {
			t.Errorf("the exec runs %q, want /bin/sh", q["command"])
		}
	default:
		t.Fatal("no exec was opened")
	}
	if !rig.log.has("debug opened", `who="erin@example.com"`) {
		t.Errorf("no attributed opened line: %v", rig.log.lines)
	}
}

// A read-only cluster is refused before the upgrade, with its record, and the
// cluster is never called.
func TestADebugOnAReadOnlyClusterIsRefusedWithNoCall(t *testing.T) {
	sink := &streamSink{}
	gate := access.New(access.Options{Reviews: (&accesstest.Reviews{}).For, Audit: audit.New(sink)})
	api := &debugAPI{state: running}
	rig := newDebugRig(t, api, gate, true)

	resp, err := http.Get(rig.srv.URL + "/?cluster=lab&" + debugQuery)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(text), "KT_READONLY_CLUSTERS") {
		t.Errorf("answered %d %q, want 403 naming the setting", resp.StatusCode, text)
	}
	if got := api.seen(); len(got) != 0 {
		t.Errorf("the cluster was called on a read-only cluster: %v", got)
	}
	records := sink.records(t)
	if len(records) != 2 || records[1].Outcome != audit.Refused {
		t.Fatalf("records %+v, want one attempt and its refused outcome", records)
	}
	r := records[1]
	if r.Decision != string(access.ReadOnly) || r.Act != audit.Write || r.Verb != "patch" ||
		r.Subresource != "ephemeralcontainers" || r.Name != "api-1" || r.Image != "busybox:1.36" || r.Target != "api" {
		t.Errorf("the refusal is recorded as %+v", r)
	}
}

// Bad names and an image that is not a reference are refused before the
// upgrade, and nothing is called.
func TestADebugWithABadRequestIsRefusedBeforeTheUpgrade(t *testing.T) {
	api := &debugAPI{state: running}
	rig := newDebugRig(t, api, nil, false)
	for _, query := range []string{
		"namespace=shop&pod=API&image=busybox",
		"namespace=shop&pod=api-1",
		"namespace=shop&pod=api-1&image=" + url.QueryEscape("busybox; rm -rf /"),
		"namespace=shop&pod=api-1&image=busybox&target=" + url.QueryEscape("../x"),
	} {
		resp, err := http.Get(rig.srv.URL + "/?cluster=lab&" + query)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s answered %d, want 400", query, resp.StatusCode)
		}
	}
	if got := api.seen(); len(got) != 0 {
		t.Errorf("an invalid request reached the cluster: %v", got)
	}
}

// Both reviews are asked for the person, in order, and the first refused one
// is the framed refusal and its record: nothing is added.
func TestBothDebugReviewsAreAskedAndARefusalIsRecorded(t *testing.T) {
	patchAttrs := debugAttributes("shop", "api-1")
	execAttrs := execAttributes("shop", "api-1")
	for _, tc := range []struct {
		name    string
		refuse  access.Attributes
		act     string
		asked   int
		message string
	}{
		{"the change", patchAttrs, audit.Write, 1, "may not patch pods/ephemeralcontainers api-1"},
		{"the shell", execAttrs, audit.Exec, 2, "may not create pods/exec api-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &streamSink{}
			reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
				return q.Attributes != tc.refuse, nil
			}}
			gate := access.New(access.Options{Reviews: reviews.For, InCluster: true, Audit: audit.New(sink)})
			api := &debugAPI{state: running}
			rig := newDebugRig(t, api, gate, false)

			frame := refusal(t, rig.open(t, debugQuery))
			if frame.State != stateForbidden || frame.Reason.Code != codeVerbDenied ||
				!strings.Contains(frame.Reason.Message, tc.message) {
				t.Errorf("refused with %+v, want forbidden/verb_denied saying %q", frame, tc.message)
			}
			if api.patched() {
				t.Error("the container was added although a review refused")
			}
			asked := reviews.Asked()
			if len(asked) != tc.asked || asked[0].Attributes != patchAttrs || asked[0].User != erin.Name {
				t.Fatalf("asked %+v, want %d questions for erin starting with %+v", asked, tc.asked, patchAttrs)
			}
			if tc.asked == 2 && asked[1].Attributes != execAttrs {
				t.Errorf("the second question was %+v, want %+v", asked[1].Attributes, execAttrs)
			}
			records := sink.records(t)
			if len(records) != 2 || records[1].Outcome != audit.Refused || records[1].Act != tc.act ||
				records[1].Decision != "denied" || records[1].Who != erin.Name || records[1].Image != "busybox:1.36" {
				t.Errorf("records %+v, want the refused %s by erin", records, tc.act)
			}
		})
	}
}

// Allowed, the PATCH and the shell are each recorded as applied: the PATCH
// with the image, the target and the container's name.
func TestAnAllowedDebugRecordsThePatchAndTheShell(t *testing.T) {
	sink := &streamSink{}
	gate := access.New(access.Options{Reviews: (&accesstest.Reviews{}).For, InCluster: true, Audit: audit.New(sink)})
	api := &debugAPI{state: running}
	rig := newDebugRig(t, api, gate, false)
	rig.open(t, debugQuery)

	api.mu.Lock()
	added := api.added
	api.mu.Unlock()
	// The shell's outcome is written once runExec returns, which is after the
	// socket closed; wait for it rather than race it.
	var patch, shell *audit.Record
	var records []audit.Record
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		records = sink.records(t)
		outcomes := 0
		for _, r := range records {
			if r.Phase == audit.Outcome {
				outcomes++
			}
		}
		if outcomes >= 2 || time.Now().After(deadline) {
			break
		}
	}
	for i := range records {
		r := &records[i]
		if r.Phase != audit.Outcome {
			continue
		}
		switch {
		case r.Act == audit.Write && r.Subresource == "ephemeralcontainers":
			patch = r
		case r.Act == audit.Exec:
			shell = r
		}
	}
	if patch == nil || patch.Outcome != audit.Applied || patch.Verb != "patch" || patch.Image != "busybox:1.36" ||
		patch.Target != "api" || patch.Container != added || patch.Who != erin.Name {
		t.Errorf("the PATCH is recorded as %+v, want applied with image, target and container %q", patch, added)
	}
	if shell == nil || shell.Outcome != audit.Applied || shell.Container != added {
		t.Errorf("the shell is recorded as %+v, want applied in container %q", shell, added)
	}
}

// Each cause of a refused PATCH keeps its own code.
func TestEachDebugRefusalKeepsItsCause(t *testing.T) {
	for _, tc := range []struct {
		name  string
		api   *debugAPI
		state string
		code  string
	}{
		{"pod security", &debugAPI{patch: refuseWith(http.StatusForbidden, metav1.StatusReasonForbidden,
			`pods "api-1" is forbidden: violates PodSecurity "restricted:latest": allowPrivilegeEscalation != false`)},
			stateForbidden, codePodSecurity},
		{"admission webhook", &debugAPI{patch: refuseWith(http.StatusBadRequest, metav1.StatusReasonBadRequest,
			`admission webhook "validate.kyverno.svc-fail" denied the request: no debug containers here`)},
			stateForbidden, codeAdmissionDenied},
		{"rbac", &debugAPI{patch: refuseWith(http.StatusForbidden, metav1.StatusReasonForbidden,
			`pods "api-1" is forbidden: User "erin" cannot patch resource "pods/ephemeralcontainers"`)},
			stateForbidden, codeVerbDenied},
		{"no subresource", &debugAPI{patch: refuseWith(http.StatusNotFound, metav1.StatusReasonNotFound,
			"the server could not find the requested resource")},
			stateUnreadable, codeNotSupportedHere},
		{"anything else", &debugAPI{patch: refuseWith(http.StatusInternalServerError, metav1.StatusReasonInternalError,
			"etcd is having a bad day")},
			stateUnreadable, codeUnclassified},
		{"no such pod", &debugAPI{noPod: true}, stateAbsent, codeNoSuchObject},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newDebugRig(t, tc.api, nil, false)
			frame := refusal(t, rig.open(t, debugQuery))
			if frame.State != tc.state || frame.Reason.Code != tc.code {
				t.Errorf("refused as %s/%s (%q), want %s/%s", frame.State, frame.Reason.Code, frame.Reason.Message, tc.state, tc.code)
			}
			select {
			case <-rig.execs:
				t.Error("a shell was opened after a refused PATCH")
			default:
			}
		})
	}
}

// A target the pod does not declare is said as such, and nothing is added.
func TestADebugTargetThePodDoesNotHaveIsRefused(t *testing.T) {
	api := &debugAPI{state: running}
	rig := newDebugRig(t, api, nil, false)
	frame := refusal(t, rig.open(t, "namespace=shop&pod=api-1&image=busybox&target=sidecar"))
	if frame.State != stateAbsent || frame.Reason.Code != codeNoSuchObject || !strings.Contains(frame.Reason.Message, "sidecar") {
		t.Errorf("refused as %+v", frame)
	}
	if api.patched() {
		t.Error("the container was added for a target the pod does not have")
	}
}

// The wait: a pull that fails and a container that ended stop it at once with
// the kubelet's reason, and the bound stops one that never runs.
func TestTheDebugWaitSaysWhyAContainerWillNotRun(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state corev1.ContainerState
		code  string
		says  string
	}{
		{"pull failure", corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
			Reason: "ImagePullBackOff", Message: `Back-off pulling image "nope:1"`}},
			codeContainerNotStarted, "ImagePullBackOff"},
		{"ended", corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "StartError", Message: `exec: "sh": executable file not found in $PATH`}},
			codeContainerNotStarted, "StartError"},
		{"time limit", corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}},
			codeTimeout, "was not running after"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newDebugRig(t, &debugAPI{state: tc.state}, nil, false)
			rig.term.debugWait = 200 * time.Millisecond
			frame := refusal(t, rig.open(t, debugQuery))
			if frame.State != stateUnreadable || frame.Reason.Code != tc.code || !strings.Contains(frame.Reason.Message, tc.says) {
				t.Errorf("refused as %+v, want %s saying %q", frame, tc.code, tc.says)
			}
			select {
			case <-rig.execs:
				t.Error("a shell was opened in a container that is not running")
			default:
			}
		})
	}
}

// waitForDebug on its own: running ends the wait, and the context ends it too.
func TestWaitForDebugReturnsOnceRunningAndOnCancel(t *testing.T) {
	pod := func(state corev1.ContainerState) *corev1.Pod {
		return &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning,
			EphemeralContainerStatuses: []corev1.ContainerStatus{{Name: "kt-debug-abcde", State: state}}}}
	}
	reads := 0
	reader := podReaderFunc(func() (*corev1.Pod, error) {
		reads++
		if reads < 3 {
			return pod(corev1.ContainerState{}), nil
		}
		return pod(running), nil
	})
	if r := waitForDebug(context.Background(), func(string) {}, reader, "p", "kt-debug-abcde", time.Second, time.Millisecond); r != nil {
		t.Errorf("a container that came up was refused: %v", r)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	never := podReaderFunc(func() (*corev1.Pod, error) { return pod(corev1.ContainerState{}), nil })
	if r := waitForDebug(ctx, func(string) {}, never, "p", "kt-debug-abcde", time.Minute, time.Minute); r == nil {
		t.Error("a cancelled wait answered running")
	}
	failing := podReaderFunc(func() (*corev1.Pod, error) { return nil, errors.New("gone") })
	if r := waitForDebug(context.Background(), func(string) {}, failing, "p", "x", time.Minute, time.Minute); r == nil || r.code != codeUnclassified {
		t.Errorf("an unreadable pod answered %v", r)
	}
}

type podReaderFunc func() (*corev1.Pod, error)

func (f podReaderFunc) Get(context.Context, string, metav1.GetOptions) (*corev1.Pod, error) {
	return f()
}

func TestDebugContainerNamesAreKubernetesNamesAndDiffer(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		name := debugContainerName()
		if !validName(name) || !strings.HasPrefix(name, debugContainerPrefix) || len(name) != len(debugContainerPrefix)+5 {
			t.Fatalf("%q is not kt-debug- and five lowercase characters", name)
		}
		seen[name] = true
	}
	if len(seen) < 45 {
		t.Errorf("50 names gave %d distinct ones", len(seen))
	}
}

// --- no_shell ----------------------------------------------------------------

// A pod shell whose program is not in the image answers no_shell, a code a
// client branches on to offer a debug container; any other failure keeps its
// own sentence and is not dressed up as one.
func TestAShellNotInTheImageAnswersNoShell(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		noShell bool
	}{
		{"runc", errors.New(`OCI runtime exec failed: exec failed: unable to start container process: exec: "/bin/sh": stat /bin/sh: no such file or directory: unknown`), true},
		{"crun", errors.New(`executable file not found in $PATH: No such file or directory: OCI runtime attempted to invoke a command that was not found`), true},
		{"another failure", errors.New("error dialing backend: dial tcp 10.0.0.4:10250: connect: connection refused"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frames := framesUntilClose(t, dialFramed(t, framedExec(t, exitingExecutor{err: tc.err})))
			var got *serverFrame
			for i := range frames {
				if frames[i].Type == "state" {
					got = &frames[i]
					break
				}
			}
			switch {
			case tc.noShell && (got == nil || got.State != stateUnreadable || got.Reason.Code != codeNoShell ||
				!strings.Contains(got.Reason.Message, "/bin/sh")):
				t.Errorf("a shell not in the image answered %+v; frames %+v", got, frames)
			case !tc.noShell && got != nil && got.Reason != nil && got.Reason.Code == codeNoShell:
				t.Errorf("%v was classified as no_shell", tc.err)
			}
		})
	}
}

// A debug container whose own image has no /bin/sh answers no_shell too: the
// session's exec is the exec path's.
func TestADebugShellNotInTheImageAnswersNoShell(t *testing.T) {
	api := &debugAPI{state: running}
	rig := newDebugRig(t, api, nil, false)
	rig.term.openExec = func(*rest.Config, *url.URL) (remotecommand.Executor, error) {
		return exitingExecutor{err: fmt.Errorf(`exec: "/bin/sh": stat /bin/sh: no such file or directory`)}, nil
	}
	frame := refusal(t, rig.open(t, debugQuery))
	if frame.Reason.Code != codeNoShell {
		t.Errorf("refused as %+v, want no_shell", frame)
	}
}

// execRefusal is the not-opened half: a refusal before the stream keeps the
// same classification.
func TestExecRefusalClassifiesTheRuntimesSentence(t *testing.T) {
	if _, code, _ := execRefusal(errors.New(`exec: "bash": executable file not found in $PATH`)); code != codeNoShell {
		t.Errorf("a missing program before the stream answered %s", code)
	}
	if _, code, _ := execRefusal(errors.New("could not write the audit record")); code != codeUnclassified {
		t.Errorf("an audit failure answered %s", code)
	}
}
