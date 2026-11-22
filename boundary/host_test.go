package boundary_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/thonicdev/kubetower/extension"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

// recordingHost implements extension.Host from outside the core module.
//
// **That it can be written at all is half of what this module proves.** The
// console's own host is `internal/web.host`, and a contract whose methods
// mentioned an internal type would be implementable only in there - which would
// make every extra untestable without a cluster, and would be discovered by
// whoever writes the first one rather than here.
//
// It records which doors were opened, so the test asserts that the consumer
// reached every door rather than that it compiled while reaching none.
type recordingHost struct {
	mu      sync.Mutex
	reached map[string]bool

	// What each door answers. Zero values are the interesting cases - an empty
	// catalog, an empty page, a node nothing could measure - because those are
	// the answers the contract's comments say must stay distinguishable from a
	// failure.
	lookupErr error
	page      extension.Page
	usage     extension.Usage
	live      extension.LiveUsage
	liveErr   error
	window    extension.Window
	revealErr error
	logsErr   error
}

func newRecordingHost() *recordingHost {
	return &recordingHost{
		reached:   map[string]bool{},
		lookupErr: fmt.Errorf("looking up %q: %w", "applications", extension.ErrUnknownKind),
		page:      extension.Page{Complete: true},
		usage:     extension.Usage{Nodes: map[string]extension.NodeUsage{}, Complete: true},
		live: extension.LiveUsage{Complete: true, Containers: map[string]extension.Resources{
			extension.ContainerKey("default", "web-0", "app"): {CPUMillicores: 0, MemoryBytes: 64 << 20},
		}},
		window: extension.Window{State: extension.WindowNoPrometheus,
			Detail: "this installation has no Prometheus configured", Complete: true},
		revealErr: fmt.Errorf("%w: the cluster says you may not get secrets a-secret", extension.ErrRevealDenied),
		logsErr:   fmt.Errorf("%w: the cluster says you may not get pods/log in namespace default", extension.ErrLogsDenied),
	}
}

func (h *recordingHost) note(door string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reached[door] = true
}

func (h *recordingHost) opened(door string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reached[door]
}

// Client hands back the two k8s.io fakes. They are the real interfaces, so a
// consumer holding them is holding what a console would give it.
func (h *recordingHost) Client(r *http.Request, cluster string) (extension.Clients, error) {
	h.note("Client")
	return extension.Clients{
		Typed:   fake.NewClientset(),
		Dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
	}, nil
}

func (h *recordingHost) Definitions(r *http.Request, cluster string) (extension.CRDRegistry, error) {
	h.note("Definitions")
	return emptyCatalogue{err: h.lookupErr}, nil
}

func (h *recordingHost) List(r *http.Request, cluster string, q extension.ListQuery) (extension.Page, error) {
	h.note("List")
	return h.page, nil
}

func (h *recordingHost) Reveal(r *http.Request, cluster, namespace, name, key string) (string, error) {
	h.note("Reveal")
	if h.revealErr != nil {
		return "", h.revealErr
	}
	return "a-value", nil
}

func (h *recordingHost) Exec(r *http.Request, cluster string, q extension.ExecQuery) (extension.ExecResult, error) {
	h.note("Exec")
	return extension.ExecResult{Stdout: []byte("ok\n"), ExitCode: 3}, nil
}

func (h *recordingHost) MayReadLogs(r *http.Request, cluster, namespace string) error {
	h.note("MayReadLogs")
	return h.logsErr
}

func (h *recordingHost) Usage(r *http.Request, cluster string, nodes []string) (extension.Usage, error) {
	h.note("Usage")
	return h.usage, nil
}

func (h *recordingHost) ContainerUsage(r *http.Request, cluster, namespace string) (extension.LiveUsage, error) {
	h.note("ContainerUsage")
	return h.live, h.liveErr
}

func (h *recordingHost) UsageWindow(r *http.Request, cluster, namespace string) (extension.Window, error) {
	h.note("UsageWindow")
	return h.window, nil
}

func (h *recordingHost) DNSWindow(r *http.Request, cluster string, span extension.DNSSpan) (extension.DNS, error) {
	h.note("DNSWindow")
	return extension.DNS{State: extension.WindowNoPrometheus, Detail: "no Prometheus"}, nil
}

func (h *recordingHost) Forward(ctx context.Context, cluster, namespace, service string, port int) (extension.Tunnel, error) {
	h.note("Forward")
	return extension.Tunnel{Addr: "127.0.0.1:0", Pod: "a-service-0", PodKnown: true,
		Stop: func() { h.note("Forward.stop") }}, nil
}

func (h *recordingHost) ForwardAsConsole(r *http.Request, cluster, namespace, service string, port int) (extension.Tunnel, error) {
	h.note("ForwardAsConsole")
	return extension.Tunnel{Addr: "127.0.0.1:1", Pod: "a-backend-0", PodKnown: true,
		Stop: func() { h.note("ForwardAsConsole.stop") }}, nil
}

func (h *recordingHost) ConfiguredSecret(r *http.Request, cluster, namespace, name, key string) (string, error) {
	h.note("ConfiguredSecret")
	return "a-token-value", nil
}

func (h *recordingHost) ConfiguredService(r *http.Request, cluster, namespace, name string) (corev1.Service, error) {
	h.note("ConfiguredService")
	return corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8443}}}}, nil
}

func (h *recordingHost) RefreshApplication(*http.Request, string, string, string) error {
	h.note("RefreshApplication")
	return nil
}

func (h *recordingHost) ConfirmToken(r *http.Request) string {
	h.note("ConfirmToken")
	return "a-word"
}

func (h *recordingHost) Setting(r *http.Request, cluster, key string) string {
	h.note("Setting")
	return "a-value"
}

// emptyCatalogue is a CRDRegistry with nothing in it, answering Lookup with
// whichever sentinel the test set.
//
// A cluster that serves no custom resources is an ordinary cluster, not a
// broken one - which is the distinction extension/types.go spends three
// sentinels on.
type emptyCatalogue struct{ err error }

func (c emptyCatalogue) All(ctx context.Context) ([]extension.CRD, error) { return nil, nil }

func (c emptyCatalogue) Lookup(ctx context.Context, kind string) (extension.CRD, error) {
	return extension.CRD{}, c.err
}
