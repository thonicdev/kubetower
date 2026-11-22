// Package detail fetches one object of any kind, with what surrounds it.
//
// Any kind, through the dynamic client and a REST mapper, so a CRD the console
// has never heard of gets a panel too. This package covers what the object is
// and its events; changes (internal/changes) and logs (internal/logs) are
// owned elsewhere.
package detail

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/kindref"
	"github.com/thonicdev/kubetower/internal/resources"
	"github.com/thonicdev/kubetower/textutil"
)

// Request identifies one object. Namespace is empty for a cluster-scoped kind,
// and supplying one anyway is not an error — the scope decides, not the caller.
type Request struct {
	Kind      string
	Namespace string
	Name      string
}

// Object is one resource, ready to be rendered.
type Object struct {
	Cluster    string
	Kind       string // the plural resource name, as the API serves it
	Title      string // the Kind, as a person writes it: "StatefulSet"
	Name       string
	Namespace  string
	Namespaced bool

	// ResourceVersion is the precondition every write in internal/edit carries:
	// a save built on this panel fails if the object moved underneath it.
	ResourceVersion string

	Facts  []Fact
	Labels map[string]string
	// Annotations are the object's own, already treated (see annotations.go),
	// never raw: an annotation is free text of unbounded length and content.
	Annotations map[string]string
	YAML        string

	// Controller is the owner reference marked `controller: true`, nil when
	// nothing controls this object. The "Controlled by" Fact is for reading;
	// this is for a caller to decide on (the KubeCron tab only on a Job a
	// CronJob created) without parsing the Fact back.
	Controller *OwnerRef

	// Switches is the two-state fields a verb flips, by the dotted path the
	// verb writes (`actions.FieldFor`), as the value in effect: "true" or
	// "false", an absent field read as the API server reads it. Filled on a
	// CronJob (`spec.suspend`) and a Node (`spec.unschedulable`), nil on every
	// other kind, so the caller can withhold Suspend from a CronJob already
	// suspended without parsing a Fact back.
	Switches map[string]string

	// Containers is what a pod is made of, init containers first. Nil for every
	// other kind. See container.go.
	Containers []Container
	// EnvNote says which of the containers' `envFrom` imports were not read,
	// and why: expanding them costs round trips, so they are capped, and the
	// cap is admitted. Empty when everything was read. See envfrom.go.
	EnvNote string

	// Data is the key/value block of a ConfigMap or a Secret, nil otherwise.
	// A Secret's values are not in here: they are fetched one key at a time
	// (data.go), so no template bug can put a credential on screen.
	Data *DataSet
	// Certificates is what the TLS material in a Secret or ConfigMap says:
	// expiry, SANs, issuer, the chain. Nil for objects carrying none. Kept
	// apart from Data, which holds no value at all; this holds parsed fields
	// of public material and never touches `tls.key` (certificate.go).
	Certificates *CertificateSet
	// Event is what an Event says, nil for every other kind. Distinct from
	// Events, which are the events *about* this object.
	Event  *EventDetail
	Events []Event
	// EventsNote explains an empty event list: Kubernetes drops events after an
	// hour by default, so "no events" means "nothing recently".
	EventsNote string

	// Relations are the other objects this one points at, most important first.
	// See relations.go.
	Relations []Relation
	// RelationsNote says what the capped relation list left out, and why.
	RelationsNote string
	// Usage is what the reverse walk found (usedby.go), nil on every kind it
	// does not run for. Apart from Relations because it says whether the walk ran
	// and completed: "nothing found" is the answer an operator deletes on, so it
	// must render neither as a refusal nor as silence. See usedby.go.
	Usage *Usage

	// Ports are what this object exposes, for pods and services only — the
	// two things a port-forward can reach. A Deployment's template ports have
	// no pod behind them.
	Ports     []Port
	PortsNote string

	// URLs are addresses a person can open, from Ingresses only: a scheme for a
	// LoadBalancer Service would be a guess.
	URLs     []URL
	URLsNote string

	// Rules is what a Role or a ClusterRole grants, nil on every other kind,
	// read off the object already fetched. See rules.go.
	Rules *RuleSet

	// ServiceType and ClusterIP are filled for a Service only. They sit beside
	// the ports because what a port means depends on them: a nodePort on a
	// ClusterIP Service is nothing, and "None" means headless.
	ServiceType string
	ClusterIP   string
}

// OwnerRef is the controller that created an object. Kind is the singular API
// kind as the reference spells it ("CronJob"), not a URL's plural; resolving
// it is internal/kindref's job.
type OwnerRef struct {
	Kind string
	Name string
}

// Relation is another object this one points at, and why.
//
// Namespace obeys the *target's* scope, not this object's: a cluster-scoped
// target gets none, because a namespace handed to a cluster-scoped kind comes
// back "not found" for a resource that is right there.
type Relation struct {
	// Role says why the two are related; see the Role* constants.
	Role string
	// Kind is the target's plural resource name, as a URL carries it, or ""
	// when nothing can be opened (an emptyDir is still listed).
	Kind string
	// Namespace is the target's namespace, empty for a cluster-scoped target.
	Namespace string
	// Name is the target's name, empty when the relation is a selector rather
	// than one object.
	Name string
	// Label is what to write on screen.
	Label string
	// Detail is the qualifier: "optional", "Running on node-3", … Often empty.
	Detail string
	// Mounts are the paths this object is mounted at, capped with a final
	// "+N more" element rather than a silent cut.
	Mounts []string
	// Selector is a label selector when the relation is "everything matching
	// this" rather than one named object; Kind is set alongside it so the UI
	// can open a filtered list.
	Selector string
}

// Linkable reports whether this relation addresses one object that can be
// opened. False for an emptyDir, for a selector, and for a kind this cluster
// does not serve.
func (r Relation) Linkable() bool { return r.Kind != "" && r.Name != "" }

// Port is one thing this object listens on. Port is the number a port-forward
// targets: the container port on a pod, the service port on a Service.
type Port struct {
	Name string // "" when the port is unnamed
	Port int32
	// TargetPort is filled for a Service only: a number or the *name* of a
	// container port.
	TargetPort string
	// NodePort is filled for a Service only, 0 when there is none.
	NodePort int32
	// HostPort is filled for a pod only, 0 when there is none.
	HostPort int32
	// Protocol is "TCP" unless the object says otherwise. Port-forward only
	// works on TCP, so this decides whether the button is offered.
	Protocol string
	// AppProtocol is a Service port's `appProtocol` ("" for a pod, which has no
	// such field): the authoritative answer to whether a forward's link says
	// https (internal/forward/scheme.go).
	AppProtocol string
	// Container is filled for a pod only: which container declares the port.
	Container string
}

// URL is an address a person can open.
type URL struct {
	Role string // "Ingress"
	// Address is what to open, scheme included; tlsCovers decides the scheme.
	Address string
	Host    string
	// Path is the path exactly as the object writes it, pattern and all.
	// Address may carry only its literal beginning; Note says when it does.
	Path string
	// TLS reports that this host is covered by the Ingress's own `spec.tls`,
	// the only reason Address ever says https.
	TLS bool
	// Service is the backend this address reaches, "" when the rule points at
	// something that is not a Service.
	Service string
	// Note is why Address may not be literally what the object says.
	Note string
}

// Fact is one labeled line. Class asks for it to be drawn as a status badge;
// the view model carries the intent and the stylesheet carries the appearance.
type Fact struct {
	Label string
	Value string
	Class string
	Pill  bool
	// Sentence says this value is prose the cluster wrote (a condition
	// message), so its cell may wrap; identifiers stay on one line.
	Sentence bool
	// Copy is what the value's copy button puts on the clipboard; empty means
	// no button.
	Copy string
}

// Event is one thing the cluster reported about this object.
type Event struct {
	Type    string
	Reason  string
	Message string
	From    string
	Count   int32
	First   time.Time
	Last    time.Time
	// About is the owned object the event is about (owned.go); nil for the
	// object's own events.
	About *Owned
}

// Warning reports whether this is the kind of event worth coloring.
func (e Event) Warning() bool { return strings.EqualFold(e.Type, "Warning") }

// Reader fetches objects, caching one REST mapper per cluster: building one
// walks every API group the cluster serves. The cache is internal/kindref's,
// shared with internal/changes so both resolve a new CRD the same way.
type Reader struct {
	clusters *cluster.Manager
	// gate reviews every read for the person behind the request's context. A
	// nil gate refuses every read (fail closed).
	gate *access.Gate

	// dynamicOf hands Locate the cluster's held unstructured client; a field
	// so tests can fake it. Never dynamic.NewForConfig per call: the config
	// sets Dial, so client-go's transport cache misses and each call would
	// cost a new transport, TLS handshake and idle connection.
	dynamicOf func(*cluster.Client) (dynamic.Interface, error)
	newMapper func(context.Context, *cluster.Client) (meta.RESTMapper, error)

	mappers *kindref.Cache
}

// NewReader returns a Reader over clusters whose reads are reviewed by gate.
func NewReader(clusters *cluster.Manager, gate *access.Gate) *Reader {
	return &Reader{
		clusters:  clusters,
		gate:      gate,
		dynamicOf: heldDynamic,
		newMapper: defaultMapper,
		mappers:   kindref.NewCache(),
	}
}

// heldDynamic is the cluster's own unstructured client, built beside the typed
// clientset on the same http.Client, so it shares the bounded dial, the rate
// limiter, the credential and any transport wrapper.
func heldDynamic(client *cluster.Client) (dynamic.Interface, error) {
	if client.Dynamic == nil {
		return nil, fmt.Errorf("cluster %q holds no dynamic client", client.Context)
	}
	return client.Dynamic, nil
}

// controllerOf reads the owner marked as the controller. A nil `Controller`
// pointer is not "yes": hand-written owner lists often mark none.
func controllerOf(raw *unstructured.Unstructured) *OwnerRef {
	for _, owner := range raw.GetOwnerReferences() {
		if owner.Controller != nil && *owner.Controller {
			return &OwnerRef{Kind: owner.Kind, Name: owner.Name}
		}
	}
	return nil
}

// switchesOf reads the two-state fields a verb flips, for the kinds that have
// one: `spec.suspend` on a batch CronJob, `spec.unschedulable` on a core Node.
// The paths are the ones `actions.FieldFor` names; a test in internal/web holds
// the two together. A field of the wrong type leaves the map nil: a state the
// console could not read offers both verbs, as before.
func switchesOf(raw *unstructured.Unstructured, gvk schema.GroupVersionKind) map[string]string {
	var path []string
	switch {
	case gvk.Group == "batch" && gvk.Kind == "CronJob":
		path = []string{"spec", "suspend"}
	case gvk.Group == "" && gvk.Kind == "Node":
		path = []string{"spec", "unschedulable"}
	default:
		return nil
	}
	on, _, err := unstructured.NestedBool(raw.Object, path...)
	if err != nil {
		return nil
	}
	return map[string]string{strings.Join(path, "."): strconv.FormatBool(on)}
}

// Get fetches the object and everything this package can say about it. A
// failure to read its events is not a failure of the call: the object is the
// answer, the events are context.
func (r *Reader) Get(ctx context.Context, contextName string, req Request) (Object, error) {
	where, err := r.Locate(ctx, contextName, req.Kind)
	if err != nil {
		return Object{}, err
	}

	iface, err := where.Interface(req.Namespace)
	if err != nil {
		return Object{}, err
	}

	// Reviewed with the name, so RBAC's `resourceNames` on `get` holds.
	var raw *unstructured.Unstructured
	_, err = r.gate.Read(ctx, where.Cluster, where.Attributes("get", req.Namespace, req.Name), func(ctx context.Context) error {
		var err error
		raw, err = iface.Get(ctx, req.Name, metav1.GetOptions{})
		return err
	})
	if err != nil {
		return Object{}, err
	}

	gvk := where.GroupVersionKind
	object := Object{
		Cluster:         where.Cluster,
		Kind:            where.Resource.Resource,
		Title:           gvk.Kind,
		Name:            raw.GetName(),
		Namespace:       raw.GetNamespace(),
		Namespaced:      where.Namespaced,
		ResourceVersion: raw.GetResourceVersion(),
		Labels:          raw.GetLabels(),
		Facts:           factsOf(raw, gvk),
		Controller:      controllerOf(raw),
		Switches:        switchesOf(raw, gvk),
	}
	object.Annotations = annotationsOf(raw, gvk)
	object.YAML = manifestOf(raw)
	object.Data = dataOf(raw, gvk.Kind)
	if object.Data != nil {
		object.Facts = append(object.Facts, object.Data.Facts()...)
	}
	// Certificates and rules are parsed off the same fetch, with no round trip.
	object.Certificates = certificatesOf(raw, gvk.Kind, time.Now())
	object.Event = eventDetailOf(raw, gvk.Kind)
	object.Rules = rulesOf(raw, gvk)
	object.Containers = containersOf(raw, gvk.Kind)
	// The one pass here that reads other objects: each `envFrom` source, for
	// key names only, never a value (envfrom.go).
	if len(object.Containers) > 0 {
		object.EnvNote = expandEnvFrom(ctx, object.Containers,
			r.envLookupFor(where.Cluster, raw.GetNamespace()))
	}

	// The relational pass; see relations.go.
	r.enrich(ctx, where.Client, &object, raw, resolverFor(where.mapper))

	object.Events, object.EventsNote = r.events(ctx, where.Client, raw, gvk)
	return object, nil
}

// Location is where a kind lives on one cluster, with the client to reach it.
// internal/edit uses it too, so there is one REST mapper per cluster, not two.
type Location struct {
	// Cluster is the real context name, as the kubeconfig spells it.
	Cluster          string
	Resource         schema.GroupVersionResource
	GroupVersionKind schema.GroupVersionKind
	Namespaced       bool
	Client           *cluster.Client
	Dynamic          dynamic.Interface

	// mapper lets Get hand relations.go a resolver without resolving again.
	mapper meta.RESTMapper
}

// Attributes is the access review's question for verb on this kind, in
// namespace and on name - the namespace dropped for a cluster-scoped kind, as
// Interface drops it, so the question asked is the request made. One
// Namespace is the exception, reviewed in itself as the API server authorizes
// it (access.RequestNamespace).
func (l Location) Attributes(verb, namespace, name string) access.Attributes {
	return access.Attributes{
		Verb: verb, Group: l.Resource.Group, Version: l.Resource.Version, Resource: l.Resource.Resource,
		Namespace: access.RequestNamespace(l.Resource.Resource, l.Namespaced, namespace, name), Name: name,
	}
}

// Interface narrows a location to one namespace, or refuses. The kind's scope
// decides, not the URL: a namespace is ignored for a cluster-scoped kind.
func (l Location) Interface(namespace string) (dynamic.ResourceInterface, error) {
	if !l.Namespaced {
		return l.Dynamic.Resource(l.Resource), nil
	}
	if namespace == "" {
		return nil, fmt.Errorf("%s is namespaced and no namespace was given", l.Resource.Resource)
	}
	return l.Dynamic.Resource(l.Resource).Namespace(namespace), nil
}

// Locate resolves a cluster name and a kind to the endpoint that serves it.
// An unknown kind can cost a full discovery walk, hence the context.
func (r *Reader) Locate(ctx context.Context, contextName, kind string) (Location, error) {
	name, ok := r.clusters.Resolve(contextName)
	if !ok {
		return Location{}, fmt.Errorf("unknown cluster %q", contextName)
	}
	client, err := r.clusters.Client(name)
	if err != nil {
		return Location{}, err
	}

	// kindref splits a custom resource's `plural.group`, and retries once with
	// a fresh mapper when a kind does not resolve (a newly installed CRD).
	mapper, mapping, err := r.mappers.Mapping(ctx, client.Context, kind, func(ctx context.Context) (meta.RESTMapper, error) {
		return r.newMapper(ctx, client)
	})
	if err != nil {
		var discovery *kindref.DiscoveryError
		if errors.As(err, &discovery) {
			// A cluster that could not be asked, not a kind it does not serve.
			return Location{}, fmt.Errorf("discovering %q: %w", name, discovery.Err)
		}
		return Location{}, err
	}
	gvk := mapping.GroupVersionKind

	dyn, err := r.dynamicOf(client)
	if err != nil {
		return Location{}, err
	}

	return Location{
		Cluster:          name,
		Resource:         mapping.Resource,
		GroupVersionKind: gvk,
		Namespaced:       mapping.Scope.Name() == meta.RESTScopeNameNamespace,
		Client:           client,
		Dynamic:          dyn,
		mapper:           mapper,
	}, nil
}

// manifestOf renders the object as YAML, trimmed (see Trimmed). managedFields
// is often longer than the rest of the object; the Changes panel reads it.
func manifestOf(raw *unstructured.Unstructured) string {
	out, err := yaml.Marshal(Trimmed(raw).Object)
	if err != nil {
		return "# the manifest could not be rendered: " + err.Error()
	}
	document := string(out)

	// Says the mask is a mask. A YAML comment, so it never reaches the cluster
	// when the editor sends the document back.
	if strings.Contains(document, SecretValueMask) {
		document = "# The values below are masked. Editing anything else here is safe: a masked\n" +
			"# value is compared against the same mask, so it cannot be written back over the\n" +
			"# real one. To change a value, type the new base64 over its mask.\n" +
			document
	}
	return document
}

// Trimmed is the object as the drawer shows it: a copy without managedFields,
// the last-applied annotation, and a Secret's values. internal/edit diffs the
// edited document against this same form, so whatever is hidden is equal on
// both sides and cannot enter the patch.
func Trimmed(raw *unstructured.Unstructured) *unstructured.Unstructured {
	clone := raw.DeepCopy()
	clone.SetManagedFields(nil)

	// The last-applied annotation is the whole object again, on one line.
	if annotations := clone.GetAnnotations(); annotations != nil {
		delete(annotations, LastAppliedAnnotation)
		if len(annotations) == 0 {
			clone.SetAnnotations(nil)
		} else {
			clone.SetAnnotations(annotations)
		}
	}

	maskSecretValues(clone)
	return clone
}

// SecretValueMask stands in for a Secret's value anywhere a whole object is
// rendered. It is not valid base64, so it cannot collide with a real `data`
// value, and "the mask was left alone" is an exact string comparison.
const SecretValueMask = "<masked>"

// maskSecretValues replaces a Secret's values in place.
//
// Called from Trimmed, not the renderer: Trimmed is both what the drawer shows
// and the base of the edit diff, so both sides carry the mask and `data`
// enters a patch only where a new value was typed over it. Masking only on
// screen would make saving an unrelated label write `<masked>` into the Secret.
func maskSecretValues(object *unstructured.Unstructured) {
	gvk := object.GroupVersionKind()
	if gvk.Group != "" || gvk.Kind != "Secret" {
		return
	}

	// `stringData` is write-only and never returned; masked anyway in case.
	for _, field := range []string{"data", "stringData"} {
		values, found, err := unstructured.NestedStringMap(object.Object, field)
		if err != nil || !found {
			continue
		}
		for key := range values {
			values[key] = SecretValueMask
		}
		if err := unstructured.SetNestedStringMap(object.Object, values, field); err != nil {
			// Fail closed: the object still holds the real values, so drop
			// the field.
			unstructured.RemoveNestedField(object.Object, field)
		}
	}
}

// factsOf builds the summary. Generic first, so any CRD gets a useful panel.
func factsOf(raw *unstructured.Unstructured, gvk schema.GroupVersionKind) []Fact {
	facts := []Fact{
		{Label: "Kind", Value: gvk.Kind},
		{Label: "API version", Value: raw.GetAPIVersion()},
		{Label: "Name", Value: raw.GetName()},
	}
	if ns := raw.GetNamespace(); ns != "" {
		facts = append(facts, Fact{Label: "Namespace", Value: ns})
	}
	if created := raw.GetCreationTimestamp().Time; !created.IsZero() {
		facts = append(facts, Fact{Label: "Created", Value: stamp(created, time.Now())})
	}

	// A deleting object is stuck on a finalizer; say which.
	if deleted := raw.GetDeletionTimestamp(); deleted != nil {
		facts = append(facts, Fact{
			Label: "Deleting since",
			Value: fmt.Sprintf("%s — held by %s",
				deleted.Format(time.RFC3339), joinOr(finalizersOf(raw, gvk), "no finalizer")),
			Class: "bad", Pill: true,
		})
	}

	// The typed shapes, converted once for every reading below that wants one.
	var pod *corev1.Pod
	var job *batchv1.Job
	switch {
	case gvk.Group == "" && gvk.Kind == "Pod":
		pod = podOf(raw)
	case gvk.Group == "batch" && gvk.Kind == "Job":
		job = jobOf(raw)
	}

	if phase, found, _ := unstructured.NestedString(raw.Object, "status", "phase"); found && phase != "" {
		class := phaseClass(phase)
		if pod != nil {
			class = podPhaseClass(pod)
		}
		facts = append(facts, Fact{Label: "Phase", Value: phase, Class: class, Pill: true})
	}
	if node, found, _ := unstructured.NestedString(raw.Object, "spec", "nodeName"); found && node != "" {
		facts = append(facts, Fact{Label: "Node", Value: node})
	}
	if pod != nil {
		facts = append(facts, podAddressFacts(pod)...)
	}
	if generation := raw.GetGeneration(); generation > 0 {
		facts = append(facts, Fact{Label: "Generation", Value: fmt.Sprint(generation)})
	}

	// Ownership answers "why does this exist"; controllerOf is the structured
	// half.
	owners := raw.GetOwnerReferences()
	if len(owners) > 0 {
		names := make([]string, 0, len(owners))
		for _, owner := range owners {
			names = append(names, owner.Kind+"/"+owner.Name)
		}
		facts = append(facts, Fact{Label: "Controlled by", Value: strings.Join(names, ", ")})
	}

	// Taints lead a Node's facts: a tainted node reports no failing condition.
	if gvk.Kind == "Node" {
		facts = append(facts, nodeFacts(raw)...)
	}

	// A Job, and a pod a Job owns, say when the run started and how long it
	// took.
	switch {
	case job != nil:
		facts = append(facts, jobFacts(job, time.Now())...)
		facts = append(facts, jobStatusFacts(job)...)
		// On a Job `Failed: True` is the complaint: see jobConditionFacts.
		return append(facts, jobConditionFacts(job, time.Now())...)
	case pod != nil:
		facts = append(facts, podRunFacts(pod, time.Now())...)
	}

	if rows, ok := workloadFacts(raw, gvk); ok {
		facts = append(facts, rows...)
		return append(facts, workloadConditionFacts(raw, time.Now())...)
	}

	if gvk.Group == "" && gvk.Kind == "Namespace" {
		return append(facts, namespaceConditionFacts(raw, time.Now())...)
	}

	return append(facts, conditionsOf(raw, time.Now())...)
}

// finalizersOf is what holds an object that is being deleted: its
// `metadata.finalizers`, plus a Namespace's `spec.finalizers`, where the
// namespace controller's `kubernetes` finalizer lives.
func finalizersOf(raw *unstructured.Unstructured, gvk schema.GroupVersionKind) []string {
	held := append([]string{}, raw.GetFinalizers()...)
	if gvk.Group == "" && gvk.Kind == "Namespace" {
		if spec, found, err := unstructured.NestedStringSlice(raw.Object, "spec", "finalizers"); err == nil && found {
			for _, one := range spec {
				held = append(held, one+" (spec)")
			}
		}
	}
	return held
}

// condition is one entry of `status.conditions`, read loosely: a CRD's
// conditions are not typed, and a missing field is drawn as missing.
type condition struct {
	kind, status, reason, message string
	// since is lastTransitionTime; zero when absent or unreadable.
	since time.Time
}

// conditionsIn reads `status.conditions`, skipping an entry with no type.
func conditionsIn(raw *unstructured.Unstructured) []condition {
	items, found, err := unstructured.NestedSlice(raw.Object, "status", "conditions")
	if err != nil || !found {
		return nil
	}
	out := make([]condition, 0, len(items))
	for _, item := range items {
		c, ok := item.(map[string]any)
		if !ok {
			continue
		}
		one := condition{}
		one.kind, _ = c["type"].(string)
		one.status, _ = c["status"].(string)
		one.reason, _ = c["reason"].(string)
		one.message, _ = c["message"].(string)
		if one.kind == "" {
			continue
		}
		if at, ok := c["lastTransitionTime"].(string); ok {
			// Local, as metav1.Time unmarshals, to match the other stamps.
			if parsed, err := time.Parse(time.RFC3339, at); err == nil {
				one.since = parsed.Local()
			}
		}
		out = append(out, one)
	}
	return out
}

// conditionText is status, reason and message, then when it last changed. The
// message is clipped before the time is added, so the date always shows.
func conditionText(c condition, now time.Time) string {
	value := c.status
	if c.reason != "" {
		value += " — " + c.reason
	}
	if c.message != "" {
		value += ": " + c.message
	}
	value = textutil.Clip(value, maxConditionMessage)
	if !c.since.IsZero() {
		value += " · since " + stamp(c.since, now)
	}
	return value
}

// namespaceConditionFacts draws every condition of a Namespace: they are
// written during deletion and say what it waits for. Each names a problem, so
// `True` is the complaint: a `…Failure` is bad, content or finalizers
// remaining is a warning (the normal wait), and a `False` one is drawn plain
// because it says what is already done.
func namespaceConditionFacts(raw *unstructured.Unstructured, now time.Time) []Fact {
	conditions := conditionsIn(raw)
	out := make([]Fact, 0, len(conditions))
	for _, c := range conditions {
		class := resources.ClassNone
		if c.status == "True" {
			class = resources.ClassWarn
			if strings.HasSuffix(c.kind, "Failure") {
				class = resources.ClassBad
			}
		}
		out = append(out, Fact{Label: c.kind, Value: conditionText(c, now), Class: class, Sentence: true})
	}
	return out
}

// conditionsOf reports only the conditions not in their happy state.
func conditionsOf(raw *unstructured.Unstructured, now time.Time) []Fact {
	var out []Fact
	for _, c := range conditionsIn(raw) {
		if !conditionFailing(c) {
			continue
		}
		out = append(out, Fact{
			Label: c.kind,
			Value: conditionText(c, now),
			Class: "warn", Pill: false, Sentence: true,
		})
	}
	return out
}

// conditionFailing says whether a condition is not in its happy state: "True"
// is healthy, except for a type that names a problem.
func conditionFailing(c condition) bool {
	if namesAProblem(c.kind) {
		return c.status == "True"
	}
	return c.status != "True"
}

// namesAProblem reports a condition type that names what is wrong, so `True`
// is the complaint, on any kind. A closed list of built-in types, not a suffix
// rule: one wide enough to catch every controller's spelling could invert a
// custom resource's healthy `True`. `…Pressure` is the one suffix kept, since
// it names nothing healthy.
func namesAProblem(kind string) bool {
	switch kind {
	case "MemoryPressure", "DiskPressure", "PIDPressure", "NetworkUnavailable", "ReplicaFailure":
		return true
	}
	return strings.HasSuffix(kind, "Pressure")
}

// Caps on free text in a drawer, in runes (through textutil, so a multibyte
// character is never split).
const (
	maxEventMessage     = 400
	maxConditionMessage = 300
)

// events reads what the cluster reported about this object.
func (r *Reader) events(ctx context.Context, client *cluster.Client, raw *unstructured.Unstructured, gvk schema.GroupVersionKind) ([]Event, string) {
	selector := fields.AndSelectors(
		fields.OneTermEqualSelector("involvedObject.name", raw.GetName()),
		fields.OneTermEqualSelector("involvedObject.kind", gvk.Kind),
	)
	// A cluster-scoped object's events live in the default namespace; an empty
	// namespace covers both, reviewed across every namespace and narrowed to
	// those the person may list.
	question := access.Attributes{Verb: "list", Version: "v1", Resource: "events", Namespace: raw.GetNamespace()}
	items, visible, err := access.List(ctx, r.gate, client.Context, question,
		func(e *corev1.Event) string { return e.Namespace },
		func(ctx context.Context) ([]corev1.Event, error) {
			list, err := client.Clients.CoreV1().Events(raw.GetNamespace()).
				List(ctx, metav1.ListOptions{FieldSelector: selector.String(), Limit: 100})
			if err != nil {
				return nil, err
			}
			return list.Items, nil
		})
	if err != nil {
		return nil, "Events could not be read: " + firstLine(err.Error())
	}

	out := make([]Event, 0, len(items))
	for i := range items {
		// Re-checked: the panel also carries owned objects' events, so a
		// stray row would be drawn as this object's own.
		if about := items[i].InvolvedObject; about.Name != raw.GetName() || about.Kind != gvk.Kind {
			continue
		}
		out = append(out, eventFrom(&items[i]))
	}

	// On a controller, the events of what it owns too (owned.go).
	family := r.ownedEvents(ctx, client, raw, gvk)
	out = append(out, family.events...)
	cut := family.note

	sort.SliceStable(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	if len(out) > maxPanelEvents {
		out = out[:maxPanelEvents]
		cut = joinNotes(cut, fmt.Sprintf("The newest %d events are shown.", maxPanelEvents))
	}

	if visible.Filtered() {
		// Said, so "no events" is not read as a quiet object.
		note := "Only the events in namespaces you may list are shown."
		if len(out) == 0 {
			note = "No events in the namespaces you may list; events in the others are not shown to you."
		}
		return out, note
	}
	if len(out) == 0 {
		// Empty is ordinary, unless what a controller owns could not be read:
		// then the family is not known to be quiet.
		if ownedWords(gvk) != "" && family.failed != "" {
			return nil, joinNotes("This "+gvk.Kind+" has no events of its own.", family.failed, cut)
		}
		return nil, cut
	}
	return out, joinNotes(family.failed, cut)
}

// eventFrom is one row of the drawer's Events panel, testable without a
// cluster.
func eventFrom(item *corev1.Event) Event {
	e := Event{
		Type:    item.Type,
		Reason:  item.Reason,
		Message: textutil.Clip(item.Message, maxEventMessage),
		From:    item.Source.Component,
		Count:   resources.EventCount(item),
		// Through resources, which also reads `series.lastObservedTime` and
		// copes with an event carrying no legacy stamp.
		First: resources.EventFirstTime(item),
		Last:  resources.EventTime(item),
	}
	if e.From == "" {
		e.From = item.ReportingController
	}
	return e
}

// phaseClass colors a raw `status.phase`. Pod phases go to
// resources.GradePodStatus, so a pod is graded the same in the drawer as in
// the lists; the arms here are the non-pod phases (PersistentVolume,
// Namespace). Container reasons such as CrashLoopBackOff never appear in a
// phase, so they have no arm.
func phaseClass(phase string) string {
	switch phase {
	case "Bound", "Available", "Active":
		return resources.ClassOK
	case "Released", "Terminating":
		return resources.ClassWarn
	case "Lost":
		return resources.ClassBad
	}
	switch phase {
	case "Running", "Pending", "Succeeded", "Failed", "Unknown":
		class, _ := resources.GradePodStatus(phase)
		return class
	}
	// A CRD's own word for a state: no color rather than a guess.
	return resources.ClassNone
}

// podPhaseClass is the Phase pill's tone on a Pod: the phase's grade, made
// worse - never better - by the list's Status grade (resources.PodStatus) and,
// on a Running pod, a Ready condition that is not True. A pod stays Running
// through a crash loop or a failing probe, so the phase alone would paint it
// green. The ranks from resources.GradePodStatus decide which is worse.
func podPhaseClass(pod *corev1.Pod) string {
	class, rank := resources.GradePodStatus(string(pod.Status.Phase))
	worse := func(c string, r int) {
		if r < rank {
			class, rank = c, r
		}
	}
	// Terminating has its own fact (Deleting since), so it does not tint this.
	if pod.DeletionTimestamp == nil {
		worse(resources.GradePodStatus(resources.PodStatus(pod)))
	}
	if pod.Status.Phase == corev1.PodRunning {
		for _, c := range pod.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status != corev1.ConditionTrue {
				worse(resources.ClassWarn, resources.RankWarn)
			}
		}
	}
	return class
}

func joinOr(values []string, fallback string) string {
	if len(values) == 0 {
		return fallback
	}
	return strings.Join(values, ", ")
}

// firstLine cuts an error at its first "\n" or "\r", trims it and caps it at
// detailLine, so a long refusal never reaches a panel whole.
func firstLine(s string) string { return textutil.FirstLineN(s, detailLine) }

// detailLine is the cap for a sentence that lands in a page rather than a PTY.
const detailLine = 200

// shortDuration is the compact form a table uses: 4d2h, 3h12m, 45s.
func shortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// defaultMapper is the injection point the tests replace; kindref builds it.
func defaultMapper(ctx context.Context, client *cluster.Client) (meta.RESTMapper, error) {
	return kindref.DiscoveryMapper(ctx, client.Clients.Discovery())
}
