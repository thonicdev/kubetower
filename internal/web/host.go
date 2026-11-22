package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/customresources"
	"github.com/thonicdev/kubetower/internal/kubeletstats"
	"github.com/thonicdev/kubetower/internal/metrics"
	"github.com/thonicdev/kubetower/internal/resources"
)

// host implements extension.Host.
//
// It lives here rather than in `app` for one reason that decides it: every one
// of its methods needs something this package already holds — the cluster
// manager, the CRD registry, the tunneller, the confirmation word — and `app` is
// public, so it could not name any of them in a signature even if it held them.
// The door is public; what stands behind it is not, and that is the whole shape
// of the boundary.
//
// A separate type and not `*Server` itself, deliberately: `*Server` has fifty
// methods and an extension that was handed one could reach every one of them by
// assertion. This has only the ones extension.Host declares.
//
// **One per extension** (hostFor): ext is the key of the extension it was
// handed to, so an act made through it - a Reveal - is recorded as that page's,
// not as an anonymous extension's.
type host struct {
	s   *Server
	ext string
}

// Host is the console's door with no extension behind it. Reveal refuses
// through it, having nobody to attribute the reveal to; mountExtensions hands
// each extension its own (hostFor).
func (s *Server) Host() extension.Host { return host{s: s} }

// hostFor is the door handed to the extension registered under key.
func (s *Server) hostFor(key string) extension.Host { return host{s: s, ext: key} }

// client resolves the cluster name the way every handler in this package does —
// through the manager, never by string comparison — and hands back the console's
// own client.
//
// The `*http.Request` is taken and unused here: the identity it carries is
// what List, Usage and Forward review, through the request's context.
//
// **What Client hands over answers discovery and nothing else**
// (access.DiscoveryOnly): an object read through it would be the console's,
// reviewed for nobody. Behind that, every client the cluster manager builds
// sends through the access guard (internal/access/transport.go), which refuses
// a change no Gate.Perform stamped - and an extra cannot reach Perform:
// extras/boundary_test.go refuses any import of this module from extras/
// beyond extension and the public leaves, internal/access included. The
// boundary module proves both from an extension's side.
//
// **A name the manager cannot resolve is extension.ErrUnknownCluster, wrapped**,
// and nothing else is: Client, Definitions, List and Usage all come through
// here (so do Reveal, ContainerUsage and UsageWindow, through client), so this
// is where an extension's "no such cluster" is decided for every door but
// Forward. host.Forward resolves the name itself, the same way, before it asks
// for a tunnel slot, and answers the same sentinel there; the wrap in
// terminal.Forwarder.Forward is the backstop, for a caller that reaches the
// forwarder another way or a server built without a cluster manager.
// A bare string here would make an extension answer 502 "could not be read" for
// a cluster that does not exist, its only other way to tell being to match the
// message from another module.
func (h host) client(_ *http.Request, name string) (*cluster.Client, error) {
	if h.s == nil || h.s.clusters == nil {
		return nil, errors.New("no cluster manager, so no cluster can be reached")
	}
	resolved, ok := h.s.clusters.Resolve(name)
	if !ok {
		return nil, fmt.Errorf("%w: %q is not a context in the kubeconfig", extension.ErrUnknownCluster, name)
	}
	return h.s.clusters.Client(resolved)
}

// refuseReadOnly answers the read-only guardrail for a command an extension
// would run inside a pod. `exec` is a mutating verb to the console's own pod
// shell (`internal/terminal/exec.go`), so a cluster named in
// KT_READONLY_CLUSTERS refuses it through every door, not only the shell's. The
// error answers errors.Is for extension.ErrDenied, so an extension that maps a
// refusal already answers it as one, and names the setting so the person can
// find its cause.
func (h host) refuseReadOnly(resolved string) error {
	if h.s != nil && h.s.readOnly != nil && h.s.readOnly(resolved) {
		return fmt.Errorf("%w: %s is marked read-only in KT_READONLY_CLUSTERS; running a command in a pod is refused there",
			extension.ErrDenied, resolved)
	}
	return nil
}

// Client hands an extension the cluster's clients **for discovery only**
// (cluster.Client.ForExtension): which kinds the cluster serves, and nothing
// about any object. A get, a list, a watch or a change through them is refused
// before it leaves the process, on both profiles, so what an extension reads of
// a cluster's objects goes through List and the other reviewed doors - and is
// reviewed for the person behind r - and never through the console's own
// credentials. The change guard (access.Guard) still stands behind them.
func (h host) Client(r *http.Request, name string) (extension.Clients, error) {
	client, err := h.client(r, name)
	if err != nil {
		return extension.Clients{}, err
	}
	typed, dynamic, err := client.ForExtension()
	if err != nil {
		return extension.Clients{}, err
	}
	return extension.Clients{Typed: typed, Dynamic: dynamic}, nil
}

// Definitions resolves the cluster before it looks for the catalog: a name
// the console does not know is ErrUnknownCluster (404) whatever the build
// carries, and only a known cluster is told there is no catalog to ask
// (502). In the other order a build without a catalog would answer "could not
// be read" for a cluster that does not exist.
func (h host) Definitions(r *http.Request, name string) (extension.CRDRegistry, error) {
	client, err := h.client(r, name)
	if err != nil {
		return nil, err
	}
	if h.s.custom == nil {
		return nil, errors.New("no custom-resource catalog in this build")
	}
	return crdRegistry{registry: h.s.custom, client: client}, nil
}

// crdRegistry is the console's catalog, narrowed to the two questions an
// extension may ask of it and translated into the public types.
type crdRegistry struct {
	registry *customresources.Registry
	client   *cluster.Client
}

func (c crdRegistry) All(ctx context.Context) ([]extension.CRD, error) {
	defs, err := c.registry.Definitions(ctx, c.client)
	if err != nil {
		return nil, err
	}
	out := make([]extension.CRD, 0, len(defs))
	for _, d := range defs {
		out = append(out, publicCRD(d))
	}
	return out, nil
}

func (c crdRegistry) Lookup(ctx context.Context, kind string) (extension.CRD, error) {
	def, err := c.registry.Definition(ctx, c.client, kind)
	if err != nil {
		return extension.CRD{}, translateCRDError(err)
	}
	return publicCRD(def), nil
}

func publicCRD(d customresources.Definition) extension.CRD {
	return extension.CRD{
		Group:      d.Group,
		Version:    d.Version,
		Plural:     d.Plural,
		Singular:   d.Singular,
		Kind:       d.Kind,
		Namespaced: d.Namespaced,
	}
}

// translateCRDError maps the console's three sentinels onto the contract's
// three, keeping the original message.
//
// **Three and not one**, which is the whole reason this function exists rather
// than the error being passed through: `customresources`'s sentinels are
// internal, so `errors.Is` against them is a question an extra cannot ask: it
// may not import them (extras/boundary_test.go).
// Collapsing the three here — returning the error bare, or wrapping all of them
// as "not found" — renders an unreadable cluster as an empty one at the one
// boundary a later release cannot change without breaking.
func translateCRDError(err error) error {
	switch {
	case errors.Is(err, customresources.ErrNotServed):
		return fmt.Errorf("%w: %s", extension.ErrNotServed, err)
	case errors.Is(err, customresources.ErrAmbiguousKind):
		return fmt.Errorf("%w: %s", extension.ErrAmbiguousKind, err)
	case errors.Is(err, customresources.ErrUnknownKind):
		return fmt.Errorf("%w: %s", extension.ErrUnknownKind, err)
	}
	return err
}

// List reads a list call to its end, from the API server's watch cache.
//
// `resources.Cached()` and `resources.Paged` are what every console list uses,
// and an extension gets the same two rather than its own: `Cached` is what keeps
// the read off etcd, and `Paged` is what makes two API servers that answer
// `Limit` differently produce the same complete list. The console holds no
// object cache of its own, so the review below is made on each answer.
//
// **Reviewed for the person behind the request**, as the console's own lists
// are: refused when they may not make it, and narrowed to the namespaces they
// may list when they may not list every one - answered as an incomplete Page
// whose Note says so, because a narrowed list drawn as complete is the failure
// Page.Complete exists to prevent. A cluster-scoped kind has nothing to narrow
// to, and a denial refuses it.
//
// A refusal is an *access.Refused, which answers errors.Is for
// extension.ErrDenied and extension.ErrUnreviewable (access.ErrDenied is the
// same value): that is how an extra tells it from a read that failed.
func (h host) List(r *http.Request, name string, q extension.ListQuery) (extension.Page, error) {
	page, err := h.list(r, name, q)
	// Under the server profile the read is the console's own, made after the
	// person's review allowed it: a Forbidden then is a grant the installation
	// lacks (extension.ErrConsoleNotGranted), not the person's refusal.
	if err != nil && h.s.inCluster && apierrors.IsForbidden(err) &&
		!errors.Is(err, extension.ErrDenied) && !errors.Is(err, extension.ErrUnreviewable) {
		err = extension.NotGranted(err)
	}
	return page, err
}

func (h host) list(r *http.Request, name string, q extension.ListQuery) (extension.Page, error) {
	// **Before the cluster is even resolved**: nothing about a Secret list is
	// read, reviewed or answered. Reveal is the door (hostreveal.go). A name
	// that is not one path segment first, or `secrets/` would be read as
	// Secrets once client-go cleaned the URL.
	if err := listSegments(q); err != nil {
		return extension.Page{}, err
	}
	if isSecrets(q.GVR.Group, q.GVR.Resource) {
		return extension.Page{}, extension.ErrSecretsNotListed
	}
	client, err := h.client(r, name)
	if err != nil {
		return extension.Page{}, err
	}
	base := resources.Cached()
	base.LabelSelector = q.LabelSelector
	base.FieldSelector = q.FieldSelector

	question := access.Attributes{Verb: "list", Group: q.GVR.Group, Version: q.GVR.Version, Resource: q.GVR.Resource,
		Namespace: q.Namespace}
	if q.Each != nil && q.Columns {
		return extension.Page{}, errors.New("a list read as a table cannot also be walked: ask for Columns or for Each")
	}
	if q.Columns {
		return h.listTable(r, client, q, base, question)
	}
	if q.Each != nil {
		return h.listEach(r, client, q, base, question)
	}
	namespaced, more := false, false
	items, seen, err := access.List(r.Context(), h.s.gate, client.Context, question,
		func(item *unstructured.Unstructured) string { return item.GetNamespace() },
		func(ctx context.Context) ([]unstructured.Unstructured, error) {
			api := client.Dynamic.Resource(q.GVR)
			var lister func(context.Context, metav1.ListOptions) (*unstructured.UnstructuredList, error)
			if q.Namespace == "" {
				lister = api.List
			} else {
				lister = api.Namespace(q.Namespace).List
			}
			if q.Limit > 0 && !access.Narrowing(ctx) {
				// One request for the rows asked for. Not from the watch
				// cache at version "0", which may ignore a limit and send
				// the whole list.
				opts := base
				opts.Limit, opts.ResourceVersion = q.Limit, ""
				page, err := lister(ctx, opts)
				if err != nil {
					return nil, err
				}
				more = page.GetContinue() != ""
				for i := range page.Items {
					namespaced = namespaced || page.Items[i].GetNamespace() != ""
				}
				return page.Items, nil
			}
			read, err := resources.Paged(ctx, base, lister,
				func(l *unstructured.UnstructuredList) []unstructured.Unstructured { return l.Items })
			for i := range read {
				namespaced = namespaced || read[i].GetNamespace() != ""
			}
			return read, err
		})
	if err != nil {
		// **An error and never an empty page.** A failed read that came back as
		// `{"items": []}` is the one failure this console's whole error
		// vocabulary exists to prevent, and it is cheapest to prevent here.
		return extension.Page{}, err
	}
	keep, page, err := listOutcome(r.Context(), client, q, question, seen, namespaced, more, len(items))
	if err != nil {
		return extension.Page{}, err
	}
	if keep == 0 && seen.Narrowed {
		if err := h.listedNowhere(r.Context(), name, client, seen); err != nil {
			return extension.Page{}, err
		}
	}
	page.Items = items[:keep]
	return page, nil
}

// listedNowhere is the refusal for a person who may list a kind across every
// namespace in none: the narrowed read drew no row, and the review of every
// namespace said no. Drawn as an empty list that would read as the cluster
// holding nothing (R5 §5), so it is answered as a refusal, as the console's
// own lists do. Nil where the namespaces cannot be established, or the person
// may list the kind somewhere: the narrowed, possibly empty page then stands.
func (h host) listedNowhere(ctx context.Context, name string, client *cluster.Client, seen access.Visible) error {
	held := h.s.namespaceAnswer(ctx, name)
	if held.state() != namespacesListed {
		return nil
	}
	_, _, err := h.s.gate.ListableIn(ctx, client.Context, seen.Asked, held.names)
	var refused *access.Refused
	if errors.As(err, &refused) {
		return err
	}
	return nil
}

// listOutcome is what List says about the rows it read, whether as objects or
// as a table: how many of the n rows to keep, and whether the page is
// Complete, with its Note. An error is a refusal or a read that failed.
func listOutcome(ctx context.Context, client *cluster.Client, q extension.ListQuery, question access.Attributes,
	seen access.Visible, namespaced, more bool, n int) (int, extension.Page, error) {
	if seen.Narrowed && !namespaced {
		// No row says where the kind lives, which is not the same as the kind
		// having no namespaces: a namespaced kind with no objects sends none.
		// Discovery says, and only here, so the common path asks the cluster
		// nothing more.
		scoped, err := namespacedKind(ctx, client, q.GVR)
		if err != nil {
			// An error and never a refusal or an empty page, for the reason above.
			return 0, extension.Page{}, err
		}
		if !scoped {
			// A cluster-scoped kind has no namespaces to narrow to, so the
			// denial of the whole list is the answer.
			return 0, extension.Page{}, &access.Refused{Cluster: client.Context, Attributes: question,
				Answer: access.Answer{Decision: access.Denied, Subject: access.Person}}
		}
	}
	keep := n
	if q.Limit > 0 && int64(n) > q.Limit {
		// A narrowed read is whole, so that a row the person may not see
		// never takes the place of one they may: cut after narrowing.
		keep, more = int(q.Limit), true
	}
	if seen.Filtered() {
		return keep, extension.Page{Complete: false,
			Note: "You may not list " + qualifiedResource(question) + " in every namespace, so this holds only the namespaces where you may."}, nil
	}
	if more {
		return keep, extension.Page{Complete: false, Note: limitNote(q.Limit, question)}, nil
	}
	return keep, extension.Page{Complete: true}, nil
}

// limitNote is the Note of a page Limit cut short.
func limitNote(limit int64, question access.Attributes) string {
	return fmt.Sprintf("Only the first %d of %s were read, as asked.", limit, qualifiedResource(question))
}

// namespacedKind is whether discovery lists gvr as namespaced, the kind's scope
// being the cluster's to say and not something to infer from the rows read. A
// kind discovery does not list is an error: guessing a scope for it would refuse
// or widen a person's list on a guess.
func namespacedKind(ctx context.Context, client *cluster.Client, gvr schema.GroupVersionResource) (bool, error) {
	if client.Clients == nil {
		return false, fmt.Errorf("no client for %q", client.Context)
	}
	list, err := discovery.ToDiscoveryInterfaceWithContext(client.Clients.Discovery()).
		ServerResourcesForGroupVersionWithContext(ctx, gvr.GroupVersion().String())
	if err != nil {
		return false, fmt.Errorf("discovering the scope of %s: %w", gvr.Resource, err)
	}
	for _, res := range list.APIResources {
		if res.Name == gvr.Resource {
			return res.Namespaced, nil
		}
	}
	return false, fmt.Errorf("discovering the scope of %s: %s does not list it", gvr.Resource, gvr.GroupVersion())
}

// Usage asks the named kubelets, bounded by the same worker cap the console
// uses, and hands back both counts.
//
// **A node that failed is absent from the map and counted in Asked**, which is
// the contract `kubeletstats.ReadNodes` already holds and the reason this method
// does not simply return the map: "3 of 40 answered" and "40 of 40" produce the
// same empty panel on the ones that did not.
//
// **Answered from the kubelet cache** when a node was read within usageTTL,
// the Nodes list's Disk column included.
//
// **Reviewed for the person behind the request** before any kubelet is asked:
// `list nodes`, because what comes back is about whole machines, and the pods
// on them are narrowed to the namespaces the person may list pods in - a
// kubelet names every pod it runs, whoever's it is. A narrowed answer says so:
// Usage.Complete false and a Note, as a narrowed List page does, because a
// node's pods drawn as all of them when they are only the person's is the
// failure Page.Complete exists to prevent.
func (h host) Usage(r *http.Request, name string, nodes []string) (extension.Usage, error) {
	client, err := h.client(r, name)
	if err != nil {
		return extension.Usage{}, err
	}
	if err := h.s.gate.Check(r.Context(), client.Context,
		access.Attributes{Verb: "list", Version: "v1", Resource: "nodes"}); err != nil {
		return extension.Usage{}, err
	}
	// Through the kubelet cache (kubeletcache.go): what is held is the
	// kubelet's raw answer, and usageFor below keeps it to this person's
	// namespaces on every call - the cache is never the filter.
	summaries, coverage := h.s.kubeletSummaries(r.Context(), client.Context, client.Clients, nodes)
	return usageFor(r.Context(), h.s.gate, client.Context, summaries, coverage)
}

// usageFor is Usage once the kubelets have answered: each node's pods kept to
// the person's namespaces, and the merged Visible of every node said on the
// answer rather than discarded. Split from the read so that the review can be
// tested without a kubelet.
func usageFor(ctx context.Context, gate *access.Gate, cluster string,
	summaries map[string]kubeletstats.Summary, coverage kubeletstats.Coverage) (extension.Usage, error) {
	out := extension.Usage{
		Nodes:    make(map[string]extension.NodeUsage, len(summaries)),
		Asked:    coverage.Asked,
		Answered: coverage.Answered,
		FirstErr: coverage.FirstErr,
	}
	pods := access.Attributes{Verb: "list", Version: "v1", Resource: "pods"}
	var seen access.Visible
	for node, summary := range summaries {
		usage := publicNodeUsage(summary)
		kept, visible, err := access.Keep(ctx, gate, cluster, pods, usage.Pods,
			func(p *extension.PodUsage) string { return p.Namespace })
		if err != nil {
			return extension.Usage{}, err
		}
		seen = seen.Merge(visible)
		usage.Pods = kept
		out.Nodes[node] = usage
	}
	out.Complete = !seen.Filtered()
	out.Note = usageAccessNote(seen)
	return out, nil
}

// usageAccessNote is what a Usage narrowed by the access review says about its
// pods, or "" for one that holds every pod the kubelets named. Like the list's,
// built from the question and never from the pods.
func usageAccessNote(seen access.Visible) string {
	switch {
	case seen.Narrowed && seen.Unreviewed:
		return "You may not list pods in every namespace, so each node's pods are those of the namespaces " +
			"where you may - and a namespace whose review could not be made is left out too."
	case seen.Narrowed:
		return "You may not list pods in every namespace, so each node's pods are those of the namespaces where you may."
	case seen.Unreviewed:
		return "A namespace whose access review could not be made is left out of each node's pods."
	}
	return ""
}

func publicNodeUsage(s kubeletstats.Summary) extension.NodeUsage {
	node := extension.NodeUsage{
		NodeFS:         extension.FS{UsedBytes: s.NodeFS.UsedBytes, CapacityBytes: s.NodeFS.CapacityBytes},
		NodeFSMeasured: s.NodeFSMeasured,
		Pods:           make([]extension.PodUsage, 0, len(s.Pods)),
	}
	for _, p := range s.Pods {
		pod := extension.PodUsage{Name: p.Name, Namespace: p.Namespace}
		for _, c := range p.Containers {
			pod.Containers = append(pod.Containers, extension.ContainerUsage{
				Name: c.Name, EphemeralBytes: c.EphemeralBytes,
			})
		}
		for _, v := range p.Volumes {
			pod.Volumes = append(pod.Volumes, extension.VolumeUsage{
				Name:   v.Name,
				FS:     extension.FS{UsedBytes: v.FS.UsedBytes, CapacityBytes: v.FS.CapacityBytes},
				PVCRef: v.PVCRef,
			})
		}
		node.Pods = append(node.Pods, pod)
	}
	return node
}

// Forward opens a tunnel onto a Service and hands back the address and the
// teardown.
//
// **At most maxExtensionTunnels open at once**, across every extension and
// every cluster (tunnelcap.go): web.New puts that ceiling in front of the
// tunneller it is handed, and past it this answers with a sentence naming the
// limit. **Under the server profile each person has a share of it**,
// perPersonExtensionTunnels, counted by the person ctx carries, and past their
// share they are answered with that number while everybody else can still open
// one. It is the only cap these tunnels have. It is not internal/forward's,
// which counts the forwards a browser opens through a forwarder of its own; the
// two do not share one. A tunnel's slot comes back when its stop is called or
// when it stops on its own.
//
// **The name is resolved before a slot is asked for**, so a cluster that is not
// in the kubeconfig is extension.ErrUnknownCluster, wrapped, as host.client
// answers it, whether or not the slots are full: "close a page and try again"
// is the wrong sentence for a cluster that does not exist, and an extension
// asks with errors.Is. A server built without a cluster manager (the tests')
// leaves the name to the tunneller.
//
// A nil tunneller is a supported state, and it answers with a sentence rather
// than a nil address and a nil error: a page that reaches a workload on a build
// with no forwarder must say so, not draw the workload as empty.
//
// **The tunnel is reviewed for whoever ctx carries**: the forwarder reads the
// Service and its pods for them and digs the tunnel inside Gate.Perform, as
// `create pods/portforward` on the pod it lands on. An extension that hands
// over its request's context is reviewed as the person; one that hands over a
// context with nobody in it is refused under the server profile.
//
// **And it names that pod**, as the forwarder knows it (terminal.Forward.Pod):
// a page that sends a credential down the tunnel reads it for the pod the
// tunnel reached, so a
// Service fronting pods that differ cannot receive one read for another. A
// tunneller that cannot name its pod answers PodKnown false, never "".
func (h host) Forward(ctx context.Context, name, namespace, service string, port int) (extension.Tunnel, error) {
	if h.s == nil || h.s.tunnels == nil {
		return extension.Tunnel{}, errors.New("this build has no port forwarder, so nothing inside a cluster can be reached")
	}
	if h.s.clusters != nil {
		if _, ok := h.s.clusters.Resolve(name); !ok {
			return extension.Tunnel{}, fmt.Errorf("%w: %q is not a context in the kubeconfig", extension.ErrUnknownCluster, name)
		}
	}
	tunnel, err := h.s.tunnels.Open(ctx, name, namespace, service, port)
	if err != nil {
		return extension.Tunnel{}, err
	}
	pod, known := metrics.PodOf(tunnel)
	if !known || pod == "" {
		// An empty name said to be known is still nobody's name.
		pod, known = "", false
	}
	return extension.Tunnel{Addr: tunnel.Addr(), Pod: pod, PodKnown: known, Stop: tunnel.Close}, nil
}

// ConfirmToken is the word the second step of a guarded write sends back.
//
// The request is taken and unused for `client`'s reason: a per-session token is
// the obvious next shape of this, and taking the request now is what keeps that
// from being a breaking change to every extension.
func (h host) ConfirmToken(*http.Request) string { return actions.ConfirmToken }

// Setting is one of this extension's text settings on cluster, as the extras
// store keeps it now: read on every call, so a change made
// on the Settings page applies to the next request. Looked up under h.ext and
// nothing else - an extension reads its own settings - and empty for the door
// with no extension behind it, a console with no store, or a key that names no
// text setting.
func (h host) Setting(_ *http.Request, cluster, key string) string {
	if h.s == nil || h.s.extras == nil || h.ext == "" {
		return ""
	}
	return h.s.extras.Text(cluster, h.ext, key)
}
