// Package boundary is one extension, written from outside the core module.
//
// It is not a page and it is not a product surface. It exists so that the
// sentence `extension/types.go` already makes about `Clients` —
//
//	"Both are k8s.io interfaces and therefore nameable from another module,
//	which is the property that makes this method possible at all: the console's
//	own cluster client is internal and could be returned but never named"
//
// — is a compiled fact rather than a correct piece of reasoning. Every method of
// `extension.Host` is reached from a handler here, so a type that stops being
// nameable from outside stops this module compiling, in the repository that made
// the change, on the pull request that made it.
//
// # It names no real key, deliberately
//
// The same reason `internal/web/fixtureextension_test.go` gives for its own
// `alfa` and `bravo`: `boundarycheck` is about the mechanism and names nothing
// that will ever be offered. The real keys belong to the extras in `extras/`,
// and a consumer borrowing one would be refused beside them as a
// duplicate the day both were registered in one process.
package boundary

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/thonicdev/kubetower/extension"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Key is the extension's key, exported so the tests in this module name it once
// rather than spelling the string twice.
const Key = "boundarycheck"

// Consumer implements extension.Extension from outside github.com/thonicdev/kubetower.
//
// A struct with no fields: everything it needs arrives through the Host that
// Routes is handed, which is the shape the contract is designed for.
type Consumer struct{}

// The five declarative halves of the contract. Each is one line because each is
// one line in a real extra too; what is worth proving is that they are reachable
// at all from this side.
func (Consumer) Key() string   { return Key }
func (Consumer) Label() string { return "Boundary Check" }
func (Consumer) Summary() string {
	return "Not a page. It proves the extension contract compiles from outside the core module."
}
func (Consumer) Needs() string    { return "nothing - it never reaches a cluster" }
func (Consumer) Legacy() []string { return nil }

// SettingKey is the one setting the consumer offers. Like Key, it names nothing
// a real extra offers.
const SettingKey = "boundary-setting"

// Settings makes the consumer an extension.Configurable, from outside the core
// module: the optional half of the contract, which a type that stopped being
// nameable from here would break in this module's build.
func (Consumer) Settings() []extension.Setting {
	return []extension.Setting{{
		Key: SettingKey, Label: "A boundary setting",
		Summary: "Proves a setting declared outside the core reaches the listing.", Default: true,
	}}
}

var _ extension.Configurable = Consumer{}

// Suggest makes the consumer an extension.Suggester, from outside the core
// module. It suggests nothing: the contract is held to compiling from here.
func (Consumer) Suggest(*http.Request, extension.Host, string) (string, error) { return "", nil }

var _ extension.Suggester = Consumer{}

// ConfiguredSecretName and ConfiguredServiceName are the one Secret and the one
// Service the consumer names, in the namespace default, as what it reads with
// the console's credentials.
const (
	ConfiguredSecretName  = "a-token"
	ConfiguredServiceName = "a-backend"
)

// Configured makes the consumer an extension.Configured, from outside the core
// module: the list the console holds ConfiguredSecret and ConfiguredService to.
func (Consumer) Configured(string, func(string) string) extension.Configuration {
	return extension.Configuration{
		Secrets:  []extension.ObjectName{{Namespace: "default", Name: ConfiguredSecretName}},
		Services: []extension.ObjectName{{Namespace: "default", Name: ConfiguredServiceName}},
	}
}

var _ extension.Configured = Consumer{}

// Routes reaches every door on the contract, one route per door.
//
// **Every method is called rather than merely referenced.** A reference is
// satisfied by a nil interface value and proves only that the name exists; a
// call requires every argument type and every result type to be nameable and
// constructible from here, which is the property an extra actually needs and the
// one a fixture inside `internal/` cannot establish.
func (c Consumer) Routes(h extension.Host) []extension.Route {
	return []extension.Route{
		{Method: http.MethodGet, Pattern: "/{$}", Handler: c.clients(h)},
		{Method: http.MethodGet, Pattern: "/definitions", Handler: c.definitions(h)},
		{Method: http.MethodGet, Pattern: "/list", Handler: c.list(h)},
		{Method: http.MethodGet, Pattern: "/reveal", Handler: c.reveal(h)},
		{Method: http.MethodPost, Pattern: "/exec", Handler: c.exec(h)},
		{Method: http.MethodGet, Pattern: "/logs", Handler: c.logs(h)},
		{Method: http.MethodGet, Pattern: "/usage", Handler: c.usage(h)},
		{Method: http.MethodGet, Pattern: "/containers", Handler: c.containers(h)},
		{Method: http.MethodGet, Pattern: "/window", Handler: c.window(h)},
		{Method: http.MethodGet, Pattern: "/dns", Handler: c.dns(h)},
		{Method: http.MethodGet, Pattern: "/volume-fill", Handler: c.volumeFill(h)},
		{Method: http.MethodGet, Pattern: "/forward", Handler: c.forward(h)},
		{Method: http.MethodGet, Pattern: "/setting", Handler: c.setting(h)},
		{Method: http.MethodGet, Pattern: "/console-forward", Handler: c.consoleForward(h)},
		{Method: http.MethodGet, Pattern: "/configured-secret", Handler: c.configuredSecret(h)},
		{Method: http.MethodGet, Pattern: "/configured-service", Handler: c.configuredService(h)},
		// No method at all, which is what a WebSocket upgrade needs - an upgrade
		// not being a verb. The fixture covers that shape at the router; it is
		// here because a Route built outside the core must be able to express it.
		{Pattern: "/socket", Handler: c.confirm(h)},
		{Method: http.MethodGet, Pattern: "/refresh-application", Handler: c.refreshApplication(h)},
	}
}

// clients proves Host.Client: both fields of extension.Clients are k8s.io
// interfaces, so an extension can hold them *and write their type down*.
//
// **And, asked with `?write=1`, the other half of the contract**: what Client
// hands over is for reading. The handler then tries a change through each
// client - a typed delete and a dynamic create - and says what each answered,
// so a test against the real console can assert that both were refused and
// that neither reached the cluster (run_test.go). On the client door's own
// route rather than a route of its own, because there is one route per door
// and this is still the Client door; a GET, because it is that route, and the
// write it attempts is the thing that must never happen.
func (c Consumer) clients(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clients, err := h.Client(r, cluster(r))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		// The two declarations are the assertion. If either type were internal to
		// the console, neither line could be written from this module.
		typed := clients.Typed
		dynamic := clients.Dynamic
		if r.URL.Query().Has("read") {
			// An object read through them: refused, since they answer
			// discovery alone. Discovery itself goes through.
			_, got := typed.CoreV1().ConfigMaps("default").Get(r.Context(), "a-configmap", metav1.GetOptions{})
			_, listed := dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).
				Namespace("default").List(r.Context(), metav1.ListOptions{})
			_, discovered := typed.Discovery().ServerResourcesForGroupVersion("v1")
			fmt.Fprintf(w, "typed=%s\ndynamic=%s\ndiscovery=%s", outcome(got), outcome(listed), outcome(discovered))
			return
		}
		if !r.URL.Query().Has("write") {
			fmt.Fprintf(w, "typed=%t dynamic=%t", typed != nil, dynamic != nil)
			return
		}
		deleted := typed.CoreV1().ConfigMaps("default").Delete(r.Context(), "a-configmap", metav1.DeleteOptions{})
		object := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "a-configmap"},
		}}
		_, created := dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).
			Namespace("default").Create(r.Context(), object, metav1.CreateOptions{})
		fmt.Fprintf(w, "typed=%s\ndynamic=%s", outcome(deleted), outcome(created))
	})
}

// outcome is what a write through Host.Client answered, in one line.
func outcome(err error) string {
	if err == nil {
		return "sent"
	}
	return "refused: " + err.Error()
}

// definitions proves Host.Definitions, the CRDRegistry behind it, and the three
// sentinels - which are the part an extension most needs and is most likely to
// be handed as one flat error.
func (c Consumer) definitions(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registry, err := h.Definitions(r, cluster(r))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		all, err := registry.All(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		found, err := registry.Lookup(r.Context(), "applications")
		if err != nil {
			fmt.Fprintf(w, "all=%d lookup=%s", len(all), classify(err))
			return
		}
		// CRD's own helpers, called rather than referenced: a page addresses a
		// dynamic client with GVR and builds a URL with Slug.
		fmt.Fprintf(w, "all=%d gvr=%s slug=%s", len(all), found.GVR().String(), found.Slug())
	})
}

// list proves Host.List, a ListQuery addressed by GVR, and Page's two-field
// answer - the distinction between "the cluster had nothing" and "the read
// stopped early" that Page's own comment says must stay askable.
//
// **Asked with `?resource=secrets`, the refusal**: List does not read Secrets,
// and says so with a sentinel this side can name.
func (c Consumer) list(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resource := "configmaps"
		if r.URL.Query().Get("resource") == "secrets" {
			resource = "secrets"
		}
		page, err := h.List(r, cluster(r), extension.ListQuery{
			GVR:       schema.GroupVersionResource{Version: "v1", Resource: resource},
			Namespace: r.URL.Query().Get("namespace"),
		})
		if errors.Is(err, extension.ErrSecretsNotListed) {
			fmt.Fprint(w, "list=secrets refused")
			return
		}
		if said := refusal(err); said != "" {
			fmt.Fprint(w, "list="+said)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if !page.Complete {
			fmt.Fprintf(w, "items=%d incomplete: %s", len(page.Items), page.Note)
			return
		}
		fmt.Fprintf(w, "items=%d complete", len(page.Items))
	})
}

// usage proves Host.Usage, and the absent-means-unmeasured rule with it: a node
// that failed is not in the map, and this handler says so rather than printing a
// zero. It reads Complete and Note too, Usage's half of Page's two fields: pods
// narrowed for the person say so.
func (c Consumer) usage(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const node = "node-that-does-not-exist"
		usage, err := h.Usage(r, cluster(r), []string{node})
		if said := refusal(err); said != "" {
			fmt.Fprint(w, "usage="+said)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		pods := "pods complete"
		if !usage.Complete {
			pods = "pods narrowed: " + usage.Note
		}
		if _, measured := usage.Nodes[node]; !measured {
			fmt.Fprintf(w, "nodes=%d %s: nothing could measure it; %s", len(usage.Nodes), node, pods)
			return
		}
		fmt.Fprintf(w, "nodes=%d %s: measured; %s", len(usage.Nodes), node, pods)
	})
}

// containers proves Host.ContainerUsage, and the two answers it must keep apart:
// no metrics-server is a *MetricsUnavailable an extension can name with
// errors.As from this side, and an empty map is a measurement.
func (c Consumer) containers(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		usage, err := h.ContainerUsage(r, cluster(r), r.URL.Query().Get("namespace"))
		var gone *extension.MetricsUnavailable
		switch {
		case errors.As(err, &gone):
			fmt.Fprintf(w, "unmeasured=%s", gone.Reason)
			return
		case err != nil:
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		key := extension.ContainerKey("default", "web-0", "app")
		if figures, ok := usage.Containers[key]; ok {
			fmt.Fprintf(w, "containers=%d %s cpu=%dm memory=%d complete=%t", len(usage.Containers), key,
				figures.CPUMillicores, figures.MemoryBytes, usage.Complete)
			return
		}
		fmt.Fprintf(w, "containers=%d complete=%t", len(usage.Containers), usage.Complete)
	})
}

// window proves Host.UsageWindow: the state is a typed value an extension
// switches on, and a read window's series are nameable from here.
func (c Consumer) window(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		window, err := h.UsageWindow(r, cluster(r), r.URL.Query().Get("namespace"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if window.State != extension.WindowRead {
			fmt.Fprintf(w, "state=%s: %s", window.State, window.Detail)
			return
		}
		var cpu, memory []extension.Sample
		for _, series := range window.Containers {
			cpu = append(cpu, series.CPU...)
			memory = append(memory, series.Memory...)
		}
		fmt.Fprintf(w, "state=%s containers=%d cpu=%d memory=%d span=%s step=%s", window.State,
			len(window.Containers), len(cpu), len(memory), extension.WindowSpan, extension.WindowStep)
	})
}

// dns proves Host.DNSWindow: the span is a typed enum an extension builds from
// the contract's own constants, and the state is a value it switches on.
func (c Consumer) dns(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := h.DNSWindow(r, cluster(r), extension.DNSSpan15m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if got.State != extension.WindowRead {
			fmt.Fprintf(w, "state=%s: %s", got.State, got.Detail)
			return
		}
		fmt.Fprintf(w, "state=%s pods=%d upstreams=%d", got.State, len(got.Pods), len(got.Upstreams))
	})
}

// volumeFill proves Host.VolumeFill: the claims are typed refs an extension
// builds, the state is a value it switches on, and each figure comes back
// under the claim it was asked for (#91).
func (c Consumer) volumeFill(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := h.VolumeFill(r, cluster(r), []extension.ClaimRef{{Namespace: "default", Name: "data-db-0"}})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if got.State != extension.WindowRead {
			fmt.Fprintf(w, "state=%s: %s", got.State, got.Detail)
			return
		}
		for _, f := range got.Claims {
			fmt.Fprintf(w, "%s/%s used=%d capacity=%d ", f.Namespace, f.Name, f.UsedBytes, f.CapacityBytes)
		}
		fmt.Fprintf(w, "engine=%s complete=%v", got.Engine, got.Complete)
	})
}

// forward proves Host.Forward, including the one signature on the contract that
// takes a context instead of a request - a tunnel outliving the request that
// opened it - the pod it reached, and the teardown it hands back.
func (c Consumer) forward(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tunnel, err := h.Forward(context.WithoutCancel(r.Context()), cluster(r), "default", "a-service", 6379)
		if said := refusal(err); said != "" {
			fmt.Fprint(w, "forward="+said)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer tunnel.Stop()
		if !tunnel.PodKnown {
			fmt.Fprintf(w, "addr=%s pod unknown", tunnel.Addr)
			return
		}
		fmt.Fprintf(w, "addr=%s pod=%s", tunnel.Addr, tunnel.Pod)
	})
}

// consoleForward proves Host.ForwardAsConsole: the request and not a context,
// since the tunnel is for that request and ends with it, and the teardown a
// page still calls when it is done.
func (c Consumer) consoleForward(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tunnel, err := h.ForwardAsConsole(r, cluster(r), "default", "a-backend", 8443)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer tunnel.Stop()
		fmt.Fprintf(w, "addr=%s known=%t", tunnel.Addr, tunnel.PodKnown)
	})
}

// configuredSecret proves Host.ConfiguredSecret and its answers, each named with
// errors.Is from this side. `?name=` asks for another Secret than the one
// Configured names. **The value is never written**, only its length.
func (c Consumer) configuredSecret(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := ConfiguredSecretName
		if asked := r.URL.Query().Get("name"); asked != "" {
			name = asked
		}
		value, err := h.ConfiguredSecret(r, cluster(r), "default", name, "token")
		switch {
		case err == nil:
			fmt.Fprintf(w, "configured=%d bytes", len(value))
		case errors.Is(err, extension.ErrNotConfigured):
			fmt.Fprint(w, "configured=not configured")
		case errors.Is(err, extension.ErrRevealDenied):
			fmt.Fprint(w, "configured=not permitted")
		case errors.Is(err, extension.ErrRevealUnreviewed):
			fmt.Fprint(w, "configured=not reviewed")
		case errors.Is(err, extension.ErrNoSuchSecret):
			fmt.Fprint(w, "configured=no such secret")
		case errors.Is(err, extension.ErrNoSuchKey):
			fmt.Fprint(w, "configured=no such key")
		case errors.Is(err, extension.ErrUnknownCluster):
			fmt.Fprint(w, "configured=no such cluster")
		default:
			http.Error(w, "configured=failed: "+err.Error(), http.StatusBadGateway)
		}
	})
}

// configuredService proves Host.ConfiguredService: a corev1.Service, nameable
// from this side, and its answers named with errors.Is.
func (c Consumer) configuredService(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := ConfiguredServiceName
		if asked := r.URL.Query().Get("name"); asked != "" {
			name = asked
		}
		svc, err := h.ConfiguredService(r, cluster(r), "default", name)
		switch {
		case err == nil:
			fmt.Fprintf(w, "service=%s ports=%d", svc.Name, len(svc.Spec.Ports))
		case errors.Is(err, extension.ErrNotConfigured):
			fmt.Fprint(w, "service=not configured")
		case errors.Is(err, extension.ErrDenied):
			fmt.Fprint(w, "service=not permitted")
		case errors.Is(err, extension.ErrUnreviewable):
			fmt.Fprint(w, "service=not reviewed")
		case errors.Is(err, extension.ErrNoSuchService):
			fmt.Fprint(w, "service=no such service")
		case errors.Is(err, extension.ErrUnknownCluster):
			fmt.Fprint(w, "service=no such cluster")
		default:
			http.Error(w, "service=failed: "+err.Error(), http.StatusBadGateway)
		}
	})
}

// exec proves Host.Exec, its result and its refusals, each named with
// errors.Is from this side. A POST: running a command in a pod is not a read.
func (c Consumer) exec(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, err := h.Exec(r, cluster(r), extension.ExecQuery{
			Namespace: "default", Pod: "web-0", Container: "app", Command: []string{"true"},
			Timeout: extension.ExecTimeout,
		})
		switch {
		case err == nil:
			fmt.Fprintf(w, "exit=%d stdout=%d bytes truncated=%t", out.ExitCode, len(out.Stdout), out.Truncated)
		case errors.Is(err, extension.ErrReadOnly):
			fmt.Fprint(w, "exec=read-only")
		case errors.Is(err, extension.ErrDenied):
			fmt.Fprint(w, "exec=not permitted")
		case errors.Is(err, extension.ErrUnreviewable):
			fmt.Fprint(w, "exec=not reviewed")
		case errors.Is(err, extension.ErrNoSuchCommand):
			fmt.Fprint(w, "exec=not in the image")
		case errors.Is(err, extension.ErrExecTimedOut):
			fmt.Fprint(w, "exec=timed out")
		case errors.Is(err, extension.ErrExecBusy):
			fmt.Fprint(w, "exec=busy")
		case errors.Is(err, extension.ErrUnknownCluster):
			fmt.Fprint(w, "exec=no such cluster")
		default:
			http.Error(w, "exec=failed: "+err.Error(), http.StatusBadGateway)
		}
	})
}

// reveal proves Host.Reveal and its refusals, each named with errors.Is from
// this side. **The value is never written**, only its length: a consumer that
// printed it would be the leak the door exists to prevent.
func (c Consumer) reveal(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value, err := h.Reveal(r, cluster(r), "default", "a-secret", "password")
		switch {
		case err == nil:
			fmt.Fprintf(w, "reveal=%d bytes", len(value))
		case errors.Is(err, extension.ErrRevealDenied):
			fmt.Fprint(w, "reveal=not permitted")
		case errors.Is(err, extension.ErrRevealUnreviewed):
			fmt.Fprint(w, "reveal=not reviewed")
		case errors.Is(err, extension.ErrNoSuchSecret):
			fmt.Fprint(w, "reveal=no such secret")
		case errors.Is(err, extension.ErrNoSuchKey):
			fmt.Fprint(w, "reveal=no such key")
		case errors.Is(err, extension.ErrUnknownCluster):
			fmt.Fprint(w, "reveal=no such cluster")
		default:
			http.Error(w, "reveal=failed: "+err.Error(), http.StatusBadGateway)
		}
	})
}

// logs proves Host.MayReadLogs: its two refusals are sentinels an extension
// classifies with errors.Is from here, apart from each other and from an
// unknown cluster.
func (c Consumer) logs(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := h.MayReadLogs(r, cluster(r), "default")
		switch {
		case err == nil:
			fmt.Fprint(w, "logs=permitted")
		case errors.Is(err, extension.ErrLogsDenied):
			fmt.Fprint(w, "logs=not permitted")
		case errors.Is(err, extension.ErrLogsUnreviewed):
			fmt.Fprint(w, "logs=not reviewed")
		case errors.Is(err, extension.ErrUnknownCluster):
			fmt.Fprint(w, "logs=no such cluster")
		default:
			http.Error(w, "logs=failed: "+err.Error(), http.StatusBadGateway)
		}
	})
}

// setting proves Host.Setting: a text setting's value, a plain string an
// extension reads on every request.
func (c Consumer) setting(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "setting=%q", h.Setting(r, cluster(r), "a-setting"))
	})
}

// confirm proves Host.ConfirmToken - the one door a guarded write needs, and the
// reason an extra never invents a confirmation word of its own.
func (c Consumer) confirm(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "token=%s", h.ConfirmToken(r))
	})
}

// classify turns a Lookup failure into the sentence its sentinel stands for.
//
// `errors.Is` and not a string compare, because the contract says the sentinels
// arrive wrapped with the name that was tried - and an extension that compares
// strings re-creates the flat-error failure the three sentinels exist to
// prevent.
func classify(err error) string {
	switch {
	case errors.Is(err, extension.ErrUnknownKind):
		return "no kind of that name"
	case errors.Is(err, extension.ErrNotServed):
		return "the kind exists and no version is served"
	case errors.Is(err, extension.ErrAmbiguousKind):
		return "two groups define that plural"
	default:
		return "the catalog could not be read: " + err.Error()
	}
}

// refusal is the documented test for a read refused for the person, made from
// outside the core: errors.Is against the contract's two sentinels, never the
// message and never the status code. "" for anything that is not a refusal -
// a success, or a read that failed.
func refusal(err error) string {
	switch {
	case errors.Is(err, extension.ErrDenied):
		return "denied"
	case errors.Is(err, extension.ErrUnreviewable):
		return "unreviewable"
	}
	return ""
}

// cluster is the console's own path wildcard, read the way an extra reads it.
// The console mounts every extension route under a key inside the cluster scope,
// so `cluster` is set for every request that reaches this package.
func cluster(r *http.Request) string { return r.PathValue("cluster") }

// refreshApplication reaches Host.RefreshApplication, the narrow write door, and
// says whether it answered; the answer is an error value, so the signature is
// what is proven from outside.
func (c Consumer) refreshApplication(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h.RefreshApplication(r, cluster(r), "argocd", "guestbook"); err != nil {
			fmt.Fprintf(w, "refresh=%v", err)
			return
		}
		fmt.Fprint(w, "refresh=done")
	})
}
