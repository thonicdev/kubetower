package terminal

import (
	"context"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/thonicdev/kubetower/internal/identity"
)

func service(ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "prometheus", Namespace: "monitoring"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "prometheus"},
			Ports:    ports,
		},
	}
}

func pod(name string, phase corev1.PodPhase, ready bool, ports ...corev1.ContainerPort) corev1.Pod {
	p := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "monitoring"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "prometheus", Ports: ports}}},
		Status:     corev1.PodStatus{Phase: phase},
	}
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}
	return p
}

// A named targetPort is the common shape in a Helm chart, and the name only
// means something to the pod: nothing in the service says which number it
// stands for.
func TestSelectPodResolvesANamedTargetPort(t *testing.T) {
	svc := service(corev1.ServicePort{Port: 9090, TargetPort: intstr.FromString("http-web")})
	pods := []corev1.Pod{pod("prom-0", corev1.PodRunning, true, corev1.ContainerPort{Name: "http-web", ContainerPort: 9091})}

	name, port, err := selectPod(svc, pods, 9090)
	if err != nil {
		t.Fatalf("selecting: %v", err)
	}
	if name != "prom-0" || port != 9091 {
		t.Errorf("selected %s:%d, expected prom-0:9091", name, port)
	}
}

func TestSelectPodUsesTheNumericTargetPort(t *testing.T) {
	svc := service(corev1.ServicePort{Port: 80, TargetPort: intstr.FromInt32(9090)})
	pods := []corev1.Pod{pod("prom-0", corev1.PodRunning, true)}

	_, port, err := selectPod(svc, pods, 80)
	if err != nil {
		t.Fatalf("selecting: %v", err)
	}
	if port != 9090 {
		t.Errorf("target port %d, expected 9090", port)
	}
}

// An unset targetPort means the service port itself. A great many charts rely
// on the default and never write it down.
func TestSelectPodDefaultsAnUnsetTargetPortToTheServicePort(t *testing.T) {
	svc := service(corev1.ServicePort{Port: 9090})
	pods := []corev1.Pod{pod("prom-0", corev1.PodRunning, true)}

	_, port, err := selectPod(svc, pods, 9090)
	if err != nil {
		t.Fatalf("selecting: %v", err)
	}
	if port != 9090 {
		t.Errorf("target port %d, expected 9090", port)
	}
}

// The single-port shortcut: asking for Prometheus should not require knowing
// which port its chart chose this year.
func TestSelectPodTakesTheOnlyPortWhenNoneIsAsked(t *testing.T) {
	svc := service(corev1.ServicePort{Port: 9090})
	pods := []corev1.Pod{pod("prom-0", corev1.PodRunning, true)}

	if _, port, err := selectPod(svc, pods, 0); err != nil || port != 9090 {
		t.Errorf("got %d, %v — expected 9090 and no error", port, err)
	}
}

func TestSelectPodRefusesToGuessBetweenSeveralPorts(t *testing.T) {
	svc := service(
		corev1.ServicePort{Name: "http", Port: 9090},
		corev1.ServicePort{Name: "grpc", Port: 10901},
	)
	pods := []corev1.Pod{pod("prom-0", corev1.PodRunning, true)}

	if _, _, err := selectPod(svc, pods, 0); err == nil {
		t.Fatal("a multi-port service was silently reduced to one port")
	}
}

func TestSelectPodRefusesAPortTheServiceDoesNotExpose(t *testing.T) {
	svc := service(corev1.ServicePort{Port: 9090})
	pods := []corev1.Pod{pod("prom-0", corev1.PodRunning, true)}

	if _, _, err := selectPod(svc, pods, 8080); err == nil {
		t.Fatal("a port the service does not expose was accepted")
	}
}

// Order in a pod list is not readiness order, and a tunnel to a pod that is not
// serving looks exactly like a broken Prometheus.
func TestSelectPodPrefersAReadyPodOverAnEarlierOne(t *testing.T) {
	svc := service(corev1.ServicePort{Port: 9090})
	pods := []corev1.Pod{
		pod("prom-0", corev1.PodRunning, false),
		pod("prom-1", corev1.PodRunning, true),
	}

	name, _, err := selectPod(svc, pods, 9090)
	if err != nil {
		t.Fatalf("selecting: %v", err)
	}
	if name != "prom-1" {
		t.Errorf("selected %s, expected the ready pod prom-1", name)
	}
}

// A Prometheus replaying its write-ahead log fails its readiness probe and is
// exactly the pod somebody wants to look at. Refusing it would hide the thing
// being investigated.
func TestSelectPodFallsBackToARunningButUnreadyPod(t *testing.T) {
	svc := service(corev1.ServicePort{Port: 9090})
	pods := []corev1.Pod{pod("prom-0", corev1.PodRunning, false)}

	name, _, err := selectPod(svc, pods, 9090)
	if err != nil {
		t.Fatalf("selecting: %v", err)
	}
	if name != "prom-0" {
		t.Errorf("selected %q, expected prom-0", name)
	}
}

func TestSelectPodSkipsPodsThatAreNotRunning(t *testing.T) {
	svc := service(corev1.ServicePort{Port: 9090})
	pods := []corev1.Pod{
		pod("prom-pending", corev1.PodPending, false),
		pod("prom-failed", corev1.PodFailed, false),
	}

	if _, _, err := selectPod(svc, pods, 9090); err == nil {
		t.Fatal("a tunnel was opened to a pod that is not running")
	}
}

func TestSelectPodSkipsATerminatingPod(t *testing.T) {
	svc := service(corev1.ServicePort{Port: 9090})
	going := pod("prom-0", corev1.PodRunning, true)
	now := metav1.Now()
	going.DeletionTimestamp = &now

	pods := []corev1.Pod{going, pod("prom-1", corev1.PodRunning, true)}
	name, _, err := selectPod(svc, pods, 9090)
	if err != nil {
		t.Fatalf("selecting: %v", err)
	}
	if name != "prom-1" {
		t.Errorf("selected the terminating pod %q", name)
	}
}

func TestSelectPodReportsAMissingContainerPortName(t *testing.T) {
	svc := service(corev1.ServicePort{Port: 9090, TargetPort: intstr.FromString("http-web")})
	pods := []corev1.Pod{pod("prom-0", corev1.PodRunning, true, corev1.ContainerPort{Name: "metrics", ContainerPort: 9091})}

	_, _, err := selectPod(svc, pods, 9090)
	if err == nil {
		t.Fatal("a target port name no container carries was accepted")
	}
	if !strings.Contains(err.Error(), "http-web") {
		t.Errorf("the error does not name the port it could not find: %v", err)
	}
}

// An ExternalName service, or one whose Endpoints are kept by hand, has no pods
// to forward to. Saying so beats listing the namespace and picking something.
func TestServiceSelectorRefusesAServiceWithoutOne(t *testing.T) {
	svc := service(corev1.ServicePort{Port: 9090})
	svc.Spec.Selector = nil

	if _, err := serviceSelector(svc); err == nil {
		t.Fatal("a service with no selector was accepted")
	}
}

func TestServiceSelectorBuildsALabelQuery(t *testing.T) {
	got, err := serviceSelector(service(corev1.ServicePort{Port: 9090}))
	if err != nil {
		t.Fatalf("building the selector: %v", err)
	}
	if got != "app=prometheus" {
		t.Errorf("selector %q, expected app=prometheus", got)
	}
}

// --- the drain ---------------------------------------------------------------

// The forwarder writes a line per connection, and once about 64 KB had gone
// into a pipe nobody read, every new connection would block inside the
// forwarder with no error anywhere: a tunnel that works for twenty minutes and
// then stops. Whatever this package passes must never block, at
// any volume.
func TestLogWriterNeverBlocksAndKeepsNoUnboundedBuffer(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	w := &logWriter{
		prefix: "port-forward prom-0",
		logf: func(format string, v ...any) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, format)
		},
	}

	// Far past the 64 KB a pipe buffer holds.
	chunk := []byte("Handling connection for 9090\n")
	for range 8000 {
		n, err := w.Write(chunk)
		if err != nil || n != len(chunk) {
			t.Fatalf("write returned %d, %v", n, err)
		}
	}

	mu.Lock()
	got := len(lines)
	mu.Unlock()
	if got != 8000 {
		t.Errorf("logged %d lines, expected 8000", got)
	}
	if len(w.rest) != 0 {
		t.Errorf("%d bytes left buffered after complete lines", len(w.rest))
	}
}

func TestLogWriterFlushesAStreamWithNoNewlineInIt(t *testing.T) {
	var count int
	w := &logWriter{prefix: "port-forward", logf: func(string, ...any) { count++ }}

	// A forwarder that never emits a newline must not grow this buffer until
	// the process runs out of memory.
	for range 100 {
		if _, err := w.Write([]byte(strings.Repeat("a", 100))); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if count == 0 {
		t.Error("nothing was ever flushed")
	}
	if len(w.rest) > 4096 {
		t.Errorf("%d bytes buffered, expected the buffer to be capped", len(w.rest))
	}
}

func TestLogWriterSplitsOnNewlinesAcrossWrites(t *testing.T) {
	var lines []string
	w := &logWriter{prefix: "pf", logf: func(format string, v ...any) {
		lines = append(lines, v[1].(string))
	}}

	_, _ = w.Write([]byte("Handling conn"))
	_, _ = w.Write([]byte("ection for 9090\nUnable to listen\n"))

	if len(lines) != 2 || lines[0] != "Handling connection for 9090" || lines[1] != "Unable to listen" {
		t.Errorf("lines reassembled as %q", lines)
	}
}

// --- the request -------------------------------------------------------------

func TestForwardRequestValidation(t *testing.T) {
	f := &Forwarder{}
	cases := []struct {
		name string
		req  ForwardRequest
	}{
		{"neither a pod nor a service", ForwardRequest{Namespace: "default"}},
		{"both a pod and a service", ForwardRequest{Namespace: "default", Pod: "p", Service: "s", RemotePort: 80}},
		{"a pod with no port", ForwardRequest{Namespace: "default", Pod: "p"}},
		{"a pod that is not a name", ForwardRequest{Namespace: "default", Pod: "../etc", RemotePort: 80}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A nil client is safe here: every one of these is refused before
			// anything reaches the cluster, which is the point.
			if _, _, err := f.resolveTarget(t.Context(), "test", nil, tc.req); err == nil {
				t.Fatal("the request was accepted")
			}
		})
	}
}

// TestAPortForwardLineNamesWhoAskedForIt: a tunnel is a TCP path into a pod
// held by this console's ServiceAccount. The line it leaves names the person
// on the request, and the console's own tunnels say they have nobody.
//
// **The helper, not the whole Forward**: opening a real tunnel needs a pod.
// internal/forward's TestADugTunnelCarriesWhoAskedForIt proves the request's
// identity reaches Forward's ctx, and Forward writes this helper's line.
func TestAPortForwardLineNamesWhoAskedForIt(t *testing.T) {
	ctx := identity.With(context.Background(), identity.Who{Name: "bob@example.com", Method: identity.SSO})
	got := forwardOpenedLine(ctx, "127.0.0.1:30001", "prod", "db-0", 5432, "prod", "0.0.0.0")
	if want := `port-forward 127.0.0.1:30001 -> prod/db-0:5432 on prod (listening on 0.0.0.0) ` +
		`who="bob@example.com" via=sso`; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if got := forwardOpenedLine(context.Background(), "127.0.0.1:1", "monitoring", "prom-0", 9090, "prod",
		"127.0.0.1"); !strings.HasSuffix(got, "who=- via=unattributed") {
		t.Errorf("the console's own tunnel names somebody: %q", got)
	}
}
