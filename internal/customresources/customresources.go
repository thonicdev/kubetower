// Package customresources reaches the kinds the typed clientset cannot: the
// ones a CRD defines.
//
// A CRD's `additionalPrinterColumns` is its author saying which fields matter
// (what `kubectl get` prints), so the work here is discovery and obedience:
// find what the cluster serves, then render what the definition asks for into
// a resources.Table.
//
// Two rules hold throughout. A kind that is unknown, or served by no version,
// is an error naming the kind, never an empty table: a cache answering "no
// pods" for a cluster running hundreds is worse than an error. And the CRD's
// `spec.scope` decides whether a namespace exists at all, whatever the request
// says, so a name read out of a URL is never repeated back as a namespace.
package customresources

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/resources"
	"github.com/thonicdev/kubetower/safego"
)

// crdGVR is where the definitions live. Read through the dynamic client: the
// typed apiextensions clientset would link an API server into the console for
// a handful of fields.
var crdGVR = schema.GroupVersionResource{
	Group:    "apiextensions.k8s.io",
	Version:  "v1",
	Resource: "customresourcedefinitions",
}

// unstructuredItems is resources.Paged's accessor for a dynamic list.
//
// Every list here goes through resources.Cached (served from the watch cache,
// since a CRD list carries every schema and is the most expensive list to read
// from etcd) and resources.Paged (a large cluster serves over a thousand CRDs,
// and a truncated catalog would report a served kind as unknown).
func unstructuredItems(l *unstructured.UnstructuredList) []unstructured.Unstructured {
	return l.Items
}

// How long a cluster's CRD list is trusted, and when it is renewed.
//
// A CRD list can take most of a second and the navigation needs it before it
// can draw, so a held catalog is refreshed in the background once it reaches
// catalogueRenew of its life, while still valid. A stale catalog is never
// served: an expired entry is fetched synchronously and a failure is reported.
const (
	catalogueTTL = 2 * time.Minute
	// catalogueRenew is the age after which a background refresh starts: late
	// enough not to poll an idle cluster, early enough to land before expiry.
	catalogueRenew = 3 * catalogueTTL / 4
)

// Errors callers match on to choose a status code rather than answering 500 for
// everything.
var (
	// ErrUnknownKind is returned when no CRD in this cluster answers to that name.
	ErrUnknownKind = errors.New("unknown custom resource kind")
	// ErrNotServed is returned when the CRD exists but every version is
	// `served: false`: the kind is real, the cluster is not offering it.
	ErrNotServed = errors.New("custom resource kind served by no version")
	// ErrAmbiguousKind is returned when two groups define the same name; the
	// error names both so the caller can retry with the qualified form.
	ErrAmbiguousKind = errors.New("ambiguous custom resource kind")
)

// PrinterColumn is one entry of `additionalPrinterColumns`, as the CRD wrote
// it. The JSONPath is not compiled here: a Definition is copied freely and a
// compiled parser is neither cheap to copy nor safe to share.
type PrinterColumn struct {
	Name     string
	Type     string // string, integer, number, boolean, date — as the CRD declares
	JSONPath string
	// Priority > 0 is a wide column: shown under `-o wide`, left out of the
	// default view.
	Priority    int32
	Description string
}

// Wide reports whether this column belongs to `-o wide` rather than to the
// default table.
func (c PrinterColumn) Wide() bool { return c.Priority > 0 }

// Definition is one CRD, reduced to what a console needs, at one version.
type Definition struct {
	// Group is the API group, "cert-manager.io", and the navigation section
	// its kinds are filed under.
	Group string
	// Version is the one version rendered: the served version discovery
	// prefers (the drawer's), not the storage version. See pickVersion.
	Version  string
	Kind     string
	Plural   string
	Singular string
	// ShortNames are the CRD's abbreviations, accepted as lookup aliases.
	ShortNames []string
	// Namespaced is `spec.scope == "Namespaced"`: the authority on whether a
	// namespace may be spoken about at all.
	Namespaced bool
	// Served is false when no version is served. Such a definition stays in
	// the catalog so asking for it yields ErrNotServed, not "unknown".
	Served  bool
	Columns []PrinterColumn

	// Section is the menu heading a Kubernetes project kind is filed under
	// ("Network" for a Gateway); empty for any other kind, which is listed by
	// API group. Set in project.go.
	Section string
	// Refresh is how often this kind's list is worth re-reading, where known
	// (native project kinds); zero for a CRD.
	Refresh time.Duration
	// native marks a project kind the API server serves itself (native.go):
	// no CRD defines it, so it resolves through discovery alone.
	native bool
}

// Slug is the name a URL carries and the name Lookup answers to first:
// "certificates.cert-manager.io". Qualified, because two operators may both
// define a "Policy".
func (d Definition) Slug() string {
	if d.Group == "" {
		return d.Plural
	}
	return d.Plural + "." + d.Group
}

// Title is what the page is called: "Certificates", "DeployHooks".
//
// A Kubernetes project kind is titled as the console titles its own kinds,
// among which it is filed. Any other kind is its author's `names.kind`,
// pluralized (kindPlural), since the capitalized plural alone would read
// "Deployhooks".
func (d Definition) Title() string {
	if ProjectGroup(d.Group) {
		return kindTitle(d.Kind, d.Plural)
	}
	return kindPlural(d.Kind, d.Plural)
}

// kindPlural is a kind in the plural, with its author's capitals: "DeployHook"
// and "deployhooks" give "DeployHooks".
//
// The ending comes from the plural, not from an English rule; a final "y"
// turned into "ies" is the one spelling change followed. Otherwise (an
// irregular plural, or one that is not the kind) the plural is capitalized.
func kindPlural(kind, plural string) string {
	lowerKind, lowerPlural := strings.ToLower(kind), strings.ToLower(plural)
	switch {
	case kind == "" || len(lowerKind) != len(kind) || len(lowerPlural) != len(plural):
		// Lower-casing changed a byte length and slicing would cut a rune: not
		// a valid name, but not one to panic on.
		return capitalise(plural)
	case strings.HasPrefix(lowerPlural, lowerKind):
		// Capitalized too: a kind written all lower case is still a heading.
		return capitalise(kind + plural[len(kind):])
	case strings.HasSuffix(lowerKind, "y") && lowerPlural == strings.TrimSuffix(lowerKind, "y")+"ies":
		return capitalise(kind[:len(kind)-1] + "ies")
	}
	return capitalise(plural)
}

// APIVersion is "group/version", the form a manifest carries.
func (d Definition) APIVersion() string { return d.Group + "/" + d.Version }

// GVR is how the dynamic client is addressed.
func (d Definition) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: d.Group, Version: d.Version, Resource: d.Plural}
}

// Instance is one custom object, fetched for a detail view.
type Instance struct {
	Definition Definition
	Name       string
	// Namespace is empty for a cluster-scoped kind, whatever the request said.
	Namespace string
	Object    *unstructured.Unstructured
	// Manifest is the object as YAML, without managedFields. See manifest().
	Manifest string
}

// Registry answers what a cluster serves, and lists it. One per process: it
// holds a short-lived catalog per cluster, read through that cluster's own
// dynamic client (cluster.Client.Dynamic).
type Registry struct {
	ttl time.Duration
	// renewAfter is the age at which a valid catalog starts being refreshed
	// in the background.
	renewAfter time.Duration

	// newDiscovery is a seam for tests (discovery/fake); nil means the
	// client's own.
	newDiscovery func(*cluster.Client) (discovery.DiscoveryInterface, error)

	mu    sync.Mutex
	slots map[string]*slot
}

// NewRegistry returns an empty Registry with the default catalog lifetimes.
func NewRegistry() *Registry {
	return &Registry{ttl: catalogueTTL, renewAfter: catalogueRenew, slots: map[string]*slot{}}
}

// slot is one cluster's cached state, and the lock that keeps a page-load
// stampede from becoming a burst of identical CRD lists.
type slot struct {
	mu sync.Mutex

	// dyn is the cluster's own dynamic client, taken from the cluster.Client
	// whose Config is builtFrom. builtFrom is compared by identity:
	// cluster.Manager hands out a fresh Client, with a fresh *rest.Config,
	// exactly when the kubeconfig was reloaded, so a new pointer means the
	// held catalog may describe another cluster.
	dyn       dynamic.Interface
	builtFrom *rest.Config

	held      *catalogue
	fetchedAt time.Time
	// fetching is non-nil while a synchronous fetch runs and is closed when it
	// lands, so concurrent callers share one CRD list. A channel rather than
	// holding mu across the call, so a waiter can leave on its own ctx.Done():
	// the fetch is detached from every caller's context.
	fetching chan struct{}
	// refreshing keeps a stale catalog from starting one background fetch
	// per render.
	refreshing bool

	// What discovery said of the Kubernetes project's kinds (discovered.go),
	// under the same lock and with the same three guards: the config it was
	// read through, single-flight, and background renewal.
	projectHeld     *projectSnapshot
	projectAt       time.Time
	projectFor      *rest.Config
	projectFetching *projectFetch
	projectRenewing bool
}

// Definitions lists the served kinds of this cluster, sorted by group then
// title, the order the navigation shows. An unserved definition has nothing
// to list.
func (r *Registry) Definitions(ctx context.Context, client *cluster.Client) ([]Definition, error) {
	cat, err := r.catalogue(ctx, client)
	if err != nil {
		return nil, err
	}

	out := make([]Definition, 0, len(cat.all))
	for _, d := range cat.all {
		if d.Served {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		// By the menu label as it reads; the kind breaks a tie.
		ti, tj := out[i].Title(), out[j].Title()
		if ti != tj {
			return foldedLess(ti, tj)
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}

// Definition resolves one kind by slug, plural, singular, kind or short name.
// An unknown name and an unserved one are different errors, and both name what
// was asked for.
//
// The CRDs first. A kind none of them defines may still be a Kubernetes
// project kind the API server serves itself, resolved through discovery:
// alone for a kind native.go names, so it opens even where CRDs may not be
// listed. Any other project kind needs the catalog for its printer columns,
// except when the CRD list was refused: that is a fact about these
// credentials, and the menu offers such a kind from discovery anyway, so it
// opens with Name, Namespace and Age (the list itself is still reviewed).
func (r *Registry) Definition(ctx context.Context, client *cluster.Client, kind string) (Definition, error) {
	cat, catErr := r.catalogue(ctx, client)
	if catErr == nil {
		definition, err := cat.lookup(kind)
		if err == nil {
			if !definition.Served {
				return Definition{}, fmt.Errorf("%w: %s", ErrNotServed, definition.Slug())
			}
			return definition, nil
		}
		if !errors.Is(err, ErrUnknownKind) {
			return Definition{}, err
		}
		catErr = err
	}
	if !projectSlug(kind) {
		// Only a project kind's slug can be in discovery's answer, so asking
		// for a third-party `plural.group` would only add a wait, unbounded
		// while discovery is failing.
		return Definition{}, catErr
	}

	project, found, err := r.projectKind(ctx, client, kind)
	switch {
	case err != nil && errors.Is(catErr, ErrUnknownKind) && !unnamedCore(kind):
		// Not a CRD, and discovery could not be asked: that failure is the
		// answer, not "unknown".
		return Definition{}, err
	case err != nil || !found:
		return Definition{}, catErr
	case project.native || errors.Is(catErr, ErrUnknownKind) || refused(catErr):
		return project, nil
	default:
		// The CRDs could not be listed for a reason that may pass, and this
		// kind may be one: a table without its columns would draw a transient
		// failure as a permanent shape.
		return Definition{}, catErr
	}
}

// refused is an answer the API server gave about these credentials.
func refused(err error) bool {
	return apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err)
}

// projectSlug is whether a name is spelt as a project kind's slug: a plural
// and a Kubernetes project group, or a bare plural (the core group).
//
// Any bare plural, not only native.go's: the menu offers every listable core
// kind discovery reports, including ones a later Kubernetes adds. Discovery
// is held for the catalog's TTL, and a vendor's `plural.group` is never
// asked.
func projectSlug(kind string) bool {
	name := strings.ToLower(strings.TrimSpace(kind))
	if _, group, ok := strings.Cut(name, "."); ok {
		return ProjectGroup(group)
	}
	return name != ""
}

// unnamedCore is a bare name native.go does not name. When discovery fails,
// such a name stays "unknown" rather than the failure: it is far more often a
// typo or an alias than a real kind, and the menu already says discovery
// could not be read.
func unnamedCore(kind string) bool {
	name := strings.ToLower(strings.TrimSpace(kind))
	if strings.Contains(name, ".") {
		return false
	}
	_, _, named := nativeAt("", name)
	return !named
}

// List fetches every instance of one custom kind into the same Table shape
// every built-in kind produces.
//
// A namespace on a cluster-scoped kind is dropped rather than refused (the UI
// carries a namespace across a change of kind), and it must never reach the
// API call or the table.
//
// gate reviews the list for the person behind ctx: refused when they may not
// make it, narrowed to the namespaces they may list otherwise (Table.Access).
// within is a selection of namespaces read as the whole cluster, or nil; rows
// outside it are dropped before matching (resources.Matcher.Within). selector
// is a label selector, refused with resources.InvalidSelector when it does not
// parse.
func (r *Registry) List(ctx context.Context, gate *access.Gate, client *cluster.Client, kind, namespace string, within []string, selector, query string, filters []resources.Filter) (resources.Table, error) {
	selector, err := resources.ParseSelector(selector)
	if err != nil {
		return resources.Table{}, err
	}
	definition, err := r.Definition(ctx, client, kind)
	if err != nil {
		return resources.Table{}, err
	}
	dyn, err := r.dynamicFor(client)
	if err != nil {
		return resources.Table{}, err
	}

	if !definition.Namespaced {
		namespace = ""
	}

	gvr := definition.GVR()
	question := access.Attributes{Verb: "list", Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, Namespace: namespace}
	var where func(*unstructured.Unstructured) string
	if definition.Namespaced {
		where = func(item *unstructured.Unstructured) string { return item.GetNamespace() }
	}
	items, visible, err := access.List(ctx, gate, client.Context, question, where,
		func(ctx context.Context) ([]unstructured.Unstructured, error) {
			var iface dynamic.ResourceInterface = dyn.Resource(gvr)
			if definition.Namespaced && namespace != "" {
				iface = dyn.Resource(gvr).Namespace(namespace)
			}
			base := resources.Cached()
			base.LabelSelector = selector
			return resources.Paged(ctx, base, iface.List, unstructuredItems)
		})
	if err != nil {
		// Named: a Forbidden on one CRD of forty is ordinary under tight
		// RBAC, and the caller needs to know which kind failed.
		return resources.Table{}, fmt.Errorf("listing %s: %w", definition.Slug(), err)
	}

	table := definition.render(items, namespace, within, query, filters, time.Now())
	table.Access = visible
	return table, nil
}

// Get fetches one instance for a detail view, reviewed by name for the person
// behind ctx, so a Role granting one named object opens that object only.
func (r *Registry) Get(ctx context.Context, gate *access.Gate, client *cluster.Client, kind, namespace, name string) (Instance, error) {
	definition, err := r.Definition(ctx, client, kind)
	if err != nil {
		return Instance{}, err
	}
	if strings.TrimSpace(name) == "" {
		return Instance{}, fmt.Errorf("fetching a %s: no name given", definition.Kind)
	}
	dyn, err := r.dynamicFor(client)
	if err != nil {
		return Instance{}, err
	}

	var iface dynamic.ResourceInterface = dyn.Resource(definition.GVR())
	switch {
	case !definition.Namespaced:
		// A cluster-scoped Get with a namespace would ask a namespaced path and
		// answer 404. Blanked here and in the Instance, so nothing downstream
		// repeats it as fact.
		namespace = ""
	case namespace == "":
		return Instance{}, fmt.Errorf("fetching %s/%s: %s is namespaced and no namespace was given",
			definition.Kind, name, definition.Slug())
	default:
		iface = dyn.Resource(definition.GVR()).Namespace(namespace)
	}

	gvr := definition.GVR()
	question := access.Attributes{Verb: "get", Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource,
		Namespace: namespace, Name: name}
	var object *unstructured.Unstructured
	_, err = gate.Read(ctx, client.Context, question, func(ctx context.Context) error {
		var err error
		object, err = iface.Get(ctx, name, metav1.GetOptions{})
		return err
	})
	if err != nil {
		return Instance{}, fmt.Errorf("fetching %s/%s: %w", definition.Slug(), name, err)
	}

	rendered, err := manifest(object)
	if err != nil {
		return Instance{}, fmt.Errorf("rendering %s/%s: %w", definition.Slug(), name, err)
	}

	return Instance{
		Definition: definition,
		Name:       object.GetName(),
		Namespace:  namespace,
		Object:     object,
		Manifest:   rendered,
	}, nil
}

// Forget drops a cluster's cached catalog, for a caller that
// knows of a kubeconfig reload before the TTL does.
func (r *Registry) Forget(context string) {
	r.mu.Lock()
	delete(r.slots, context)
	r.mu.Unlock()
}

// --- the catalog ------------------------------------------------------------

// catalog is one cluster's CRDs, indexed. Built whole and never mutated, so
// a reader holding one across a refresh keeps a coherent view.
type catalogue struct {
	all    []Definition
	bySlug map[string]Definition
	// byAlias holds the unqualified names (plural, singular, kind, short
	// names) only where exactly one CRD claims them.
	byAlias map[string]Definition
	// ambiguous maps the other names to the slugs that collided.
	ambiguous map[string][]string
}

func (c *catalogue) lookup(kind string) (Definition, error) {
	name := strings.ToLower(strings.TrimSpace(kind))
	if name == "" {
		return Definition{}, fmt.Errorf("%w: %q", ErrUnknownKind, kind)
	}
	if d, ok := c.bySlug[name]; ok {
		return d, nil
	}
	if d, ok := c.byAlias[name]; ok {
		return d, nil
	}
	if slugs, ok := c.ambiguous[name]; ok {
		return Definition{}, fmt.Errorf("%w %q: %s both define it — ask for one by its full name",
			ErrAmbiguousKind, kind, strings.Join(slugs, " and "))
	}
	return Definition{}, fmt.Errorf("%w %q", ErrUnknownKind, kind)
}

// refreshInBackground fetches a fresh catalog without making anyone wait.
// The caller holds the slot's lock.
func (r *Registry) refreshInBackground(s *slot, client *cluster.Client) {
	if s.refreshing {
		return
	}
	s.refreshing = true

	safego.Go("custom resources: renewing the CRD catalog of "+client.Context, func() error {
		// Its own bounded context: the request that triggered it is long gone.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// The client under the lock, the API call without it, so readers never
		// wait on a refresh. fetchedAt is recorded to detect, on the way back,
		// that something newer landed meanwhile.
		s.mu.Lock()
		dyn, err := r.dynamicLocked(s, client)
		since := s.fetchedAt
		s.mu.Unlock()

		var fresh *catalogue
		if err == nil {
			// A panic decoding a CRD becomes this renewal's error, so
			// `refreshing` is still cleared.
			err = safego.Call("custom resources: the CRD catalog of "+client.Context, func() error {
				items, err := resources.Paged(ctx, resources.Cached(),
					dyn.Resource(crdGVR).List, unstructuredItems)
				if err == nil {
					fresh = buildCatalogue(items)
				}
				return err
			})
		}

		s.mu.Lock()
		defer s.mu.Unlock()
		s.refreshing = false
		if err != nil {
			// Harmless: a held catalog is served only while valid, so it
			// expires on schedule and the next caller fetches synchronously
			// and sees the real error.
			return nil
		}
		// This answer is older than anything the slot learned since the lock
		// was released, and storing it would stamp it as newer. Two cases:
		// a synchronous fetch landed meanwhile (the TTL may expire
		// mid-flight), or dynamicLocked took a new client and cleared the
		// catalog because the context now names another cluster (context
		// names such as `kubernetes-admin@kubernetes` are reused). Both move
		// fetchedAt, which dynamicLocked zeroes when it clears; if that stops
		// being true, this check must change with it.
		if !s.fetchedAt.Equal(since) {
			return nil
		}
		s.held, s.fetchedAt = fresh, time.Now()
		return nil
	}, nil)
}

// catalog returns the cluster's CRDs, fetching them when the cache is cold.
// It loops because a round can end with "somebody else is fetching": the
// waiter asks again, since that fetch may have failed and stored nothing.
func (r *Registry) catalogue(ctx context.Context, client *cluster.Client) (*catalogue, error) {
	if client == nil || client.Config == nil {
		return nil, errors.New("no client for this cluster")
	}
	s := r.slot(client.Context)

	for {
		cat, wait, err := r.catalogueRound(ctx, s, client)
		if wait == nil {
			return cat, err
		}

		// Waiting on a channel rather than a mutex: the fetch is detached from
		// every caller's context, so a canceled request must be able to leave
		// instead of parking a goroutine for up to 30 s.
		select {
		case <-ctx.Done():
			// We stopped looking; the cluster may well serve custom resources.
			// A caller must not render this as an absence.
			return nil, fmt.Errorf(
				"the custom resource definitions of %q were still being listed when the request was canceled: %w",
				client.Context, ctx.Err())
		case <-wait:
		}
	}
}

// catalogueRound is one turn of catalog's loop. A nil channel means the
// first two values are the answer; a non-nil one means "wait on this, then
// ask again".
func (r *Registry) catalogueRound(ctx context.Context, s *slot, client *cluster.Client) (*catalogue, <-chan struct{}, error) {
	s.mu.Lock()

	// Before the freshness check: dynamicLocked is where a new config
	// drops the held catalog, and a young catalog must not survive a
	// reload that repointed a reused context name at another cluster. The
	// same config pointer returns at once.
	dyn, err := r.dynamicLocked(s, client)
	if err != nil {
		s.mu.Unlock()
		return nil, nil, err
	}

	if s.held != nil && time.Since(s.fetchedAt) < r.ttl {
		// Still valid; renew it in the background near the end of its life.
		if time.Since(s.fetchedAt) >= r.renewAfter {
			r.refreshInBackground(s, client)
		}
		held := s.held
		s.mu.Unlock()
		return held, nil, nil
	}

	if fetching := s.fetching; fetching != nil {
		// Somebody is already asking; wait on their answer, on something a
		// canceled request can walk away from.
		s.mu.Unlock()
		return nil, fetching, nil
	}

	fetching := make(chan struct{})
	s.fetching = fetching
	s.mu.Unlock()

	// Detached from the caller's context, with its own bound: a page load is
	// aborted when the user clicks on, and a fetch canceled with it would
	// store nothing, so a fast-clicking user would never let a catalog land.
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	items, err := resources.Paged(fetchCtx, resources.Cached(),
		dyn.Resource(crdGVR).List, unstructuredItems)
	cancel()

	var cat *catalogue
	if err == nil {
		cat = buildCatalogue(items)
	}

	s.mu.Lock()
	// The same staleness check as refreshInBackground's, asked of the client
	// rather than fetchedAt: a first fetch and a rebuild both start from a
	// zero fetchedAt, so only `s.dyn` tells whether this list was made through
	// the slot's current client. If not, the context now names another
	// cluster and the answer must not be stored.
	superseded := s.dyn != dyn
	if err == nil && !superseded {
		s.held, s.fetchedAt = cat, time.Now()
	}
	s.fetching = nil
	s.mu.Unlock()
	// Waiters are released whatever happened; after a failure nothing was
	// stored, so they fetch for themselves.
	// Whoever waited is released whatever happened, including a failure: nobody
	// must be left waiting on a result that is never coming. They loop, find no
	// fetch in flight, and take their own, since a failed fetch stored nothing.
	close(fetching)

	if err != nil {
		// The stale catalog is not served: a cluster that stopped answering
		// must say so.
		return nil, nil, fmt.Errorf("listing the custom resource definitions of %q: %w", client.Context, err)
	}
	if superseded {
		// Describes the cluster the context pointed at before. `fetching` is
		// closed, so returning it sends the caller round again.
		return nil, fetching, nil
	}
	return cat, nil, nil
}

func buildCatalogue(items []unstructured.Unstructured) *catalogue {
	cat := &catalogue{
		bySlug:    map[string]Definition{},
		byAlias:   map[string]Definition{},
		ambiguous: map[string][]string{},
	}

	for i := range items {
		definition, ok := definitionFrom(&items[i])
		if !ok {
			// A CRD without a group, plural or kind cannot be addressed;
			// skipped, as there is nowhere to report it.
			continue
		}
		cat.all = append(cat.all, definition)
		cat.bySlug[strings.ToLower(definition.Slug())] = definition
	}

	// Aliases only where unique: "policies" can mean Kyverno's or
	// Gatekeeper's, and guessing shows the wrong table convincingly.
	claimed := map[string]int{}
	for _, d := range cat.all {
		for _, alias := range d.Aliases() {
			claimed[alias]++
		}
	}
	for _, d := range cat.all {
		for _, alias := range d.Aliases() {
			if _, taken := cat.bySlug[alias]; taken {
				continue
			}
			if claimed[alias] > 1 {
				cat.ambiguous[alias] = append(cat.ambiguous[alias], d.Slug())
				continue
			}
			cat.byAlias[alias] = d
		}
	}
	for alias := range cat.ambiguous {
		sort.Strings(cat.ambiguous[alias])
	}
	return cat
}

// Aliases are the unqualified names this definition may be asked for by
// (plural, kind, singular, short names), lower-cased and deduplicated, so
// `cert` finds Certificates as `kubectl get cert` does. Deduplicated because
// a singular and its kind are often the same word, and a definition must not
// collide with itself.
func (d Definition) Aliases() []string {
	candidates := []string{d.Plural, d.Kind, d.Singular}
	candidates = append(candidates, d.ShortNames...)

	seen := map[string]bool{}
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// --- clients ------------------------------------------------------------------

func (r *Registry) slot(context string) *slot {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.slots == nil {
		r.slots = map[string]*slot{}
	}
	if s, ok := r.slots[context]; ok {
		return s
	}
	s := &slot{}
	r.slots[context] = s
	return s
}

// dynamicFor is the locking entry point; dynamicLocked is for paths that
// already hold the slot.
func (r *Registry) dynamicFor(client *cluster.Client) (dynamic.Interface, error) {
	if client == nil || client.Config == nil || client.Dynamic == nil {
		return nil, errors.New("no client for this cluster")
	}
	s := r.slot(client.Context)
	s.mu.Lock()
	defer s.mu.Unlock()
	return r.dynamicLocked(s, client)
}

// dynamicLocked is the cluster's own dynamic client, never one built here: a
// second client from the same config would be a second transport and
// connection pool per cluster (the guard's dial makes client-go's transport
// cache pass it by), beside the one cluster.Manager already holds.
func (r *Registry) dynamicLocked(s *slot, client *cluster.Client) (dynamic.Interface, error) {
	if client.Dynamic == nil {
		return nil, fmt.Errorf("no dynamic client for %q", client.Context)
	}
	if s.dyn != nil && s.builtFrom == client.Config {
		return s.dyn, nil
	}
	s.dyn, s.builtFrom = client.Dynamic, client.Config
	// A new config means a reloaded kubeconfig: the held CRDs belong to the
	// cluster the old config pointed at.
	s.held, s.fetchedAt = nil, time.Time{}
	return s.dyn, nil
}
