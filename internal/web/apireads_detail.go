package web

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/detail"
	"github.com/thonicdev/kubetower/internal/forward"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func (r apiReads) GetResource(ctx context.Context, ref api.ObjectRef) (api.DetailPage, *api.Failure) {
	object, fail := r.object(ctx, ref)
	if fail != nil {
		return api.DetailPage{}, fail
	}
	// Asked here, not in the mapper: `detailPageOf` stays a pure function of what
	// was read.
	writable, note := r.s.writable(ref.Cluster)
	page := detailPageOf(ref, object)
	// The guardrail's answer, then the person's (access.go).
	page.Actions = r.s.gatedActions(ctx, ref, r.s.offeredActions(ref, object, writable, note))
	// Shell, under the server profile: asked for this pod, as the verbs are.
	page.Shell = r.s.shellFor(ctx, ref)
	// Whether this console forwards at all is the server's state, not the object's.
	page.Ports = r.s.portsFor(ref.Cluster, page.Ports)
	return page, nil
}

// portsFor is what the Ports panel is told about each port, given whether this
// console forwards at all.
//
// Nothing changes under the server profile: `capabilities.portForward` is false
// there and the page draws the `kubectl port-forward` command for each
// forwardable port, to run on the viewer's machine with their credentials, which
// also makes several replicas irrelevant. Elsewhere, no forwarder makes every
// port a disabled control, and a cluster marked read-only (which the forwarder
// refuses) makes every otherwise forwardable port a disabled control carrying
// the marking's sentence; a UDP port keeps its own reason.
func (s *Server) portsFor(cluster string, ports []api.ObjectPort) []api.ObjectPort {
	if s.inCluster {
		return ports
	}
	if why := s.forwardingRefusal(); why != "" {
		return withoutForwarding(ports, why)
	}
	if resolved, ok := s.clusters.Resolve(cluster); ok && s.readOnly != nil && s.readOnly(resolved) {
		// The forwarder's own sentence, so the drawn reason and a click's refusal
		// cannot drift apart.
		why := (&forward.ReadOnlyRefusal{Cluster: resolved}).Error()
		for i := range ports {
			if !ports[i].Forwardable {
				continue
			}
			ports[i].Forwardable = false
			ports[i].Reason = reasonPtr(api.Because(api.CodeVerbDenied, why).Retry(false))
		}
	}
	return ports
}

// withoutForwarding makes every port a disabled control carrying why, for a
// console outside the server profile that opens no forward, as `portsOf` does
// for a UDP port.
func withoutForwarding(ports []api.ObjectPort, why string) []api.ObjectPort {
	for i := range ports {
		ports[i].Forwardable = false
		ports[i].Reason = reasonPtr(api.Because(api.CodeNotSupportedHere, why))
	}
	return ports
}

// objectActions says which verbs this object offers and whether each is
// permitted. The set is the kind's (`verbsFor`, as for the bulk bar); the
// permission answered here is only the read-only guardrail. RBAC is not: the
// person's review is applied by the caller, and the cluster may still refuse.
// `permitted` is an offer; `internal/actions` is the control.
//
// Restart on a pod nothing will recreate is withheld, not disabled: the verb is
// not a thing this object has. `actions.RestartsAPod` decides, the same rule
// `internal/actions` refuses by server-side.
func objectActions(kind string, object detail.Object, writable bool, note string) []api.DetailAction {
	verbs := verbsFor(kind)
	if len(verbs) == 0 {
		return nil
	}
	terminating := isTerminatingNamespace(kind, object)
	deleting := isDeleting(object)

	out := make([]api.DetailAction, 0, len(verbs))
	for _, verb := range verbs {
		if verb == actions.Restart && isPodKind(kind) && !actions.RestartsAPod(controllerKindOf(&object)) {
			continue
		}
		// A namespace already Terminating offers no Delete: a repeat changes nothing,
		// and its conditions say what holds it.
		if verb == actions.Delete && terminating {
			continue
		}
		// One half of a toggle the object is already on is withheld: Suspend on a
		// suspended CronJob, Cordon on a cordoned node. The other half is offered.
		if settled(verb, object.Switches) {
			continue
		}
		spec, ok := specFor(verb, kind)
		if !ok {
			continue
		}
		entry := api.DetailAction{Verb: string(verb), Label: spec.Label, Permitted: writable}
		if spec.Danger {
			entry.Destructive = boolOf(true)
		}
		if spec.Irreversible {
			entry.Irreversible = boolOf(true)
		}
		if !writable {
			// The guardrail's own sentence, which names the setting that refuses.
			reason := api.Because(api.CodeVerbDenied, note).Retry(false)
			entry.Reason = &reason
		} else if deleting {
			// An object being deleted takes no verb: a Restart or a second Delete changes
			// nothing. Disabled rather than withheld, so the row of buttons keeps its shape.
			entry.Permitted = false
			reason := api.Because(api.CodeNotSupportedHere, deletingReason).Retry(false)
			entry.Reason = &reason
		}
		out = append(out, entry)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// settled reports a verb whose whole change is one two-state field that already
// holds the value the verb would write: Suspend on a CronJob whose
// `spec.suspend` is true, Uncordon on a node that is schedulable. The field and
// its target value are `actions.FieldFor`'s, the strings the patch writes, so
// the two cannot drift. A field the read did not carry settles nothing: both
// halves stay offered, and the confirmation still says when an act changes
// nothing.
func settled(verb actions.Verb, switches map[string]string) bool {
	field, after, ok := actions.FieldFor(verb)
	if !ok {
		return false
	}
	now, known := switches[field]
	return known && now == after
}

// unchangedVerbs is the kind's bulk verbs that would change nothing on one row,
// by `settled`, in `verbsFor`'s order; nil where every verb would act. The
// selection bar reads it to offer a toggle only where a ticked row needs it,
// and to send only those rows.
func unchangedVerbs(kind string, switches map[string]string) []string {
	if len(switches) == 0 {
		return nil
	}
	var out []string
	for _, verb := range verbsFor(kind) {
		if actions.IsBulk(verb) && settled(verb, switches) {
			out = append(out, string(verb))
		}
	}
	return out
}

// deletingReason is the sentence on a verb of an object being deleted.
const deletingReason = "this object is being deleted; it is gone once whatever holds it lets go"

// isDeleting reports an object carrying a deletion timestamp, from the
// "Deleting since" fact internal/detail drew from it.
func isDeleting(object detail.Object) bool {
	for _, fact := range object.Facts {
		if fact.Label == "Deleting since" {
			return true
		}
	}
	return false
}

// isTerminatingNamespace reports a Namespace whose status.phase is Terminating,
// from the Phase fact internal/detail drew from it.
func isTerminatingNamespace(kind string, object detail.Object) bool {
	if !matchKind(kind, "namespaces") {
		return false
	}
	for _, fact := range object.Facts {
		if fact.Label == "Phase" {
			return fact.Value == "Terminating"
		}
	}
	return false
}

// detailPageOf is the whole mapping from what internal/detail computed to what
// crosses the wire, a pure function of (ref, object) so a test can drive it with
// an object that carries everything and catch a member declared and never sent.
func detailPageOf(ref api.ObjectRef, object detail.Object) api.DetailPage {
	return api.DetailPage{
		Kind:      object.Kind,
		Namespace: object.Namespace,
		Name:      object.Name,
		Title:     object.Title,
		Tabs:      tabsFor(ref, object),
		// Being deleted: the client reads the object again until it is gone.
		Deleting: isDeleting(object),
		// The charts, as an address rather than a tab: drawn at the top of the details
		// panel.
		Metrics: metricsAddressFor(ref),
		// The keys, as an address for the same reason, drawn between the facts and
		// Related; absent on every kind without a data block.
		Data: dataAddressFor(ref, object),
		// What an Event says, when this object is one.
		Event: eventSubjectOf(object.Event),
		// Already fetched with the object: a mapping, not a round trip.
		Events:     eventsOf(object.Events),
		EventsNote: object.EventsNote,
		Sections:   sectionsOf(object),
		// The set a Service's selector is matched against.
		Labels: object.Labels,
		// Already without `last-applied-configuration` and, on a Secret, with every
		// value masked (internal/detail/annotations.go). The note travels beside the map
		// so a treated map says it was treated.
		Annotations: object.Annotations,
		// What the TLS material says: otherwise `kubectl get secret ... | base64 -d |
		// openssl x509 -text`.
		Certificates: certificateSetOf(object.Certificates),
		// What a Role grants, off the same fetch.
		Rules: ruleSetOf(object.Rules),
		// The addresses an Ingress actually serves.
		URLs:     urlsOf(object.URLs),
		URLsNote: object.URLsNote,
		// Everything a pod is made of, with its QoS answer.
		Containers: containerViews(object.Containers),
		EnvNote:    object.EnvNote,
		// What this object exposes, filled for a pod and a Service. `ref.Kind`, as every
		// other decision here reads the route's kind.
		Ports:     portsOf(object.Ports, ref.Kind),
		PortsNote: object.PortsNote,
		// The relations graph.
		Relations:     relationsOf(object.Relations),
		RelationsNote: object.RelationsNote,
		// The reverse direction: who uses this ConfigMap or mounts this Secret. The
		// referrers are rows of Relations; this is whether the walk ran, completed, and
		// what "nothing" may mean.
		UsedBy: usedByOf(object.Usage, api.Scope{
			Cluster: ref.Cluster, Resource: ref.Kind,
			Namespace: ref.Namespace, Name: ref.Name, Verb: "list",
		}),
		// `actions` is filled by the caller, GetResource.
	}
}

// usedByOf carries the reverse walk's answer to the wire. Its empty sentence is
// met on about half the ConfigMaps and Secrets a person opens, and reads as
// "safe to delete", which a walk cannot establish:
//
//   - a ConfigMap picked up by label is named in no object (Grafana's sidecar,
//     Prometheus rule discovery, Argo CD plugin discovery);
//   - a custom resource can hold the reference (cert-manager, ExternalSecrets),
//     and reading every custom resource would cost tens of megabytes;
//   - bare pods, finished Jobs and hand-made ReplicaSets are not counted, since
//     history makes almost everything look used.
//
// So it says "nothing found", never "unused", says what it could not see, and
// `reason.detail` lists where it looked. A walk that could not list one of its
// kinds has not established absence: with nothing found it is `forbidden` or
// `unreadable` (via `stateOf`); with something found it stays `ok`, and
// `relationsNote` names the refused list.
func usedByOf(use *detail.Usage, scope api.Scope) *api.UsedBy {
	if use == nil {
		return nil
	}
	if use.Found == 0 && use.Err != nil {
		failure := stateOf(use.Err, scope)
		failure.Reason.Message = "Who uses this " + strings.ToLower(use.Subject) +
			" could not be established: " + joinWords(use.Refused) + " could not be listed."
		failure.Reason.Detail = firstLine(use.Err.Error())
		return &api.UsedBy{State: failure.State, Reason: &failure.Reason}
	}
	if use.Found == 0 {
		reason := api.Because(api.CodeEmptyScope, nothingFoundSentence(use.Subject)).
			WithDetail(lookedIn(use, scope)).
			In(scope).
			// Every list answered: retrying would give the same answer.
			Retry(false)
		return &api.UsedBy{State: api.StateEmpty, Reason: &reason}
	}
	return &api.UsedBy{State: api.StateOK}
}

// nothingFoundSentence is the wording the whole entry turns on, in one place.
// Each walked kind has its own blind spot: a reason that cannot apply to the
// kind on screen makes the right conclusion read as a wrong one.
func nothingFoundSentence(subject string) string {
	// The pods no walk counts can mount a ConfigMap or a Secret and run as a
	// ServiceAccount; none of them can name a Role or a ClusterRole.
	uncounted := ", and a bare pod, a finished Job and a hand-made ReplicaSet were not counted"
	var blind string
	switch subject {
	case "Secret":
		blind = "a custom resource can hold the reference in a field no walk enumerates — " +
			"cert-manager, ExternalSecrets and Argo CD repository credentials all do"
	case "Role":
		blind = "only a RoleBinding grants a Role, and a binding can be created at any time " +
			"by a person, a GitOps tool or an operator"
		uncounted = ""
	case "ClusterRole":
		blind = "a ClusterRole can be folded into another by an aggregationRule label without being named, " +
			"and a binding can be created at any time"
		uncounted = ""
	case "ServiceAccount":
		blind = "its token can be used from outside the cluster, by a pipeline or a kubeconfig, " +
			"with nothing in the cluster naming it"
	default:
		blind = "a controller can pick a ConfigMap up by label without naming it in any object at all — " +
			"a Grafana dashboard and a Prometheus rule work exactly that way, and no walk can see them; " +
			"Kubernetes projects kube-root-ca.crt into every pod from no template at all"
	}
	return "Nothing that was looked at names this " + subject + ". " +
		"That is not the same as unused: " + blind + uncounted + ". " +
		"Read it as nothing found, not as safe to delete."
}

// lookedIn is the audit behind the sentence: what "nothing" was looked for in.
func lookedIn(use *detail.Usage, scope api.Scope) string {
	if len(use.Looked) == 0 {
		return ""
	}
	where := "the cluster"
	if scope.Namespace != "" {
		where = "namespace " + scope.Namespace
	}
	audit := "Listed in " + where + ": " + strings.Join(use.Looked, ", ") + "."
	if len(use.Narrowed) > 0 {
		// The access review narrowed these to the namespaces the person may list, so a
		// referrer elsewhere is not seen, and the audit says so.
		audit += " " + joinWords(use.Narrowed) + " " + pick(len(use.Narrowed), "was", "were") +
			" listed only in the namespaces you may list."
	}
	return audit
}

// joinWords joins as "a, b and c".
func joinWords(items []string) string {
	switch len(items) {
	case 0:
		return "nothing"
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// ruleSetOf carries a Role's rules to the wire with the state that tells the two
// negative answers apart: a Role that grants nothing is real, a Role whose rules
// could not be read is a fault, and on a permissions panel confusing them is the
// worst case. There is no `forbidden` arm: a caller who may not read the Role
// never reaches here (`apiReads.object` refuses first).
func ruleSetOf(set *detail.RuleSet) *api.RuleSet {
	if set == nil {
		return nil
	}
	if set.Unreadable != "" {
		return &api.RuleSet{
			State: api.StateUnreadable,
			Reason: &api.Reason{
				Code:    api.CodeUndecodable,
				Message: "This object's rules could not be read.",
				Detail:  set.Unreadable,
			},
			Note: set.Note,
		}
	}
	if len(set.Rules) == 0 {
		// `empty` with no reason: nothing failed, the Role grants nothing.
		return &api.RuleSet{State: api.StateEmpty, Note: set.Note, Selectors: set.Selectors}
	}
	rules := make([]api.PolicyRule, 0, len(set.Rules))
	for _, rule := range set.Rules {
		rules = append(rules, api.PolicyRule{
			APIGroups:       rule.APIGroups,
			Resources:       rule.Resources,
			Verbs:           rule.Verbs,
			ResourceNames:   rule.ResourceNames,
			NonResourceURLs: rule.NonResourceURLs,
			Wildcards:       rule.Wildcards,
			// Through the same grading as the rest of this surface.
			WildcardTone: toneOf(rule.WildcardClass),
		})
	}
	return &api.RuleSet{State: api.StateOK, Rules: rules, Note: set.Note, Selectors: set.Selectors}
}

func (r apiReads) object(ctx context.Context, ref api.ObjectRef) (detail.Object, *api.Failure) {
	scope := api.Scope{Cluster: ref.Cluster, Resource: ref.Kind, Namespace: ref.Namespace, Name: ref.Name, Verb: "get"}
	object, err := r.s.objects.Get(ctx, ref.Cluster, detail.Request{
		Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name,
	})
	if err != nil {
		if objectGone(err, ref.Name) {
			// The API server says this object is not there: gone, a different answer from a
			// kind nothing serves.
			return detail.Object{}, api.Fail(api.StateAbsent, api.CodeNoSuchObject, firstLine(err.Error())).Within(scope)
		}
		return detail.Object{}, stateOf(err, scope)
	}
	return object, nil
}

// objectGone reports the API server's 404 about this object by name - the
// Status it sends for a missing object names it in its details. A 404 that
// names nothing is a path the server does not serve, which stays no_such_kind.
func objectGone(err error, name string) bool {
	var status apierrors.APIStatus
	if name == "" || !errors.As(err, &status) || !apierrors.IsNotFound(err) {
		return false
	}
	details := status.Status().Details
	return details != nil && details.Name == name
}

// tabsFor carries the drawer's tabs as data, so a page that may not be installed
// degrades by being absent. The data block and the metrics are members, drawn in
// the details panel (dataAddressFor, metricsAddressFor), not tabs. `changes` and
// `manifest` are on every kind: each answers `absent` with a reason, which is
// true of any object.
func tabsFor(ref api.ObjectRef, object detail.Object) []api.Tab {
	base := "/api" + clusterPath(ref.Cluster)
	query := objectQuery(ref)
	tabs := make([]api.Tab, 0, 3)
	return append(tabs,
		// Events carries no endpoint, which makes it inline: the object's read already
		// fetched them. It is on every kind; `eventsNote` tells "none recently" from
		// unreadable.
		api.Tab{Key: "events", Label: "Events"},
		// Changes is every object's: the field managers' record.
		api.Tab{Key: "changes", Label: "Changes", Endpoint: base + "/changes" + query, Lazy: boolOf(true)},
		api.Tab{Key: "manifest", Label: "Manifest", Endpoint: base + "/manifest" + query, Lazy: boolOf(true)},
	)
}

// eventsOf carries what the cluster still remembers to the wire. A failure to
// read events is not a failure of the object: it lands in `EventsNote`, and an
// empty list and an unreadable one stay different answers. Instants are
// pointers, so a zero time is not sent as year one.
func eventsOf(events []detail.Event) []api.ObjectEvent {
	if len(events) == 0 {
		return nil
	}
	out := make([]api.ObjectEvent, 0, len(events))
	for _, event := range events {
		out = append(out, api.ObjectEvent{
			Type:   event.Type,
			Reason: event.Reason,
			// Never clipped: the list clips, this panel holds the whole sentence.
			Message: event.Message,
			From:    event.From,
			Count:   event.Count,
			First:   instantOf(event.First),
			Last:    instantOf(event.Last),
			About:   eventAboutOf(event.About),
		})
	}
	return out
}

// eventAboutOf names the owned object an event is about, and nil on the
// object's own events.
func eventAboutOf(about *detail.Owned) *api.EventAbout {
	if about == nil {
		return nil
	}
	return &api.EventAbout{Kind: about.Resource, Title: about.Kind, Name: about.Name}
}

// instantOf is nil for a zero time, and a pointer to it otherwise.
func instantOf(at time.Time) *time.Time {
	if at.IsZero() {
		return nil
	}
	return &at
}

// portsOf carries what an object exposes, answering `Forwardable` here:
// forwarding is TCP, so a UDP port gets a disabled control and a reason, and the
// browser never holds a copy of a rule the server enforces. A Service's port and
// a pod's share one row type, as the panel draws one table.
func portsOf(ports []detail.Port, kind string) []api.ObjectPort {
	if len(ports) == 0 {
		return nil
	}
	out := make([]api.ObjectPort, 0, len(ports))
	for _, port := range ports {
		one := api.ObjectPort{
			Name:        port.Name,
			Port:        port.Port,
			TargetPort:  port.TargetPort,
			NodePort:    port.NodePort,
			HostPort:    port.HostPort,
			Protocol:    port.Protocol,
			AppProtocol: port.AppProtocol,
			Container:   port.Container,
		}
		switch {
		case !isTCP(port.Protocol):
			one.Reason = reasonPtr(api.Because(api.CodeNotSupportedHere,
				"port-forwarding is TCP, and this port is "+port.Protocol))
		case !isPodKind(kind) && !isServiceKind(kind):
			// Any other kind: a forward is opened on a pod or a Service only.
			one.Reason = reasonPtr(api.Because(api.CodeNotSupportedHere,
				"a forward is opened on a pod or a service"))
		default:
			one.Forwardable = true
		}
		out = append(out, one)
	}
	return out
}

// isTCP treats an unstated protocol as TCP, the field's default.
func isTCP(protocol string) bool {
	return protocol == "" || strings.EqualFold(protocol, "TCP")
}

func isServiceKind(kind string) bool {
	return strings.EqualFold(strings.SplitN(kind, ".", 2)[0], "services")
}

// eventSubjectOf carries an Event's own sentence, nil for every other kind,
// through `EventDetail.Object()`, which already composes "Pod/api-7d9f".
func eventSubjectOf(event *detail.EventDetail) *api.EventSubject {
	if event == nil {
		return nil
	}
	return &api.EventSubject{
		Message:     event.Message,
		Reason:      event.Reason,
		Type:        event.Type,
		Action:      event.Action,
		Source:      event.Source,
		Node:        event.Node,
		Count:       event.Count,
		First:       instantOf(event.First),
		Last:        instantOf(event.Last),
		Object:      event.Object(),
		ObjectField: event.ObjectField,
	}
}

// measurement says a table's gauges are empty, on the kinds that have them, and
// why: the state and the sentence together, so a column of dashes leaves a
// thread to pull.
//
// A first read still in flight is `pending`, saying the columns fill in when it
// returns (it runs on its own twenty-second bound, so not "on the next
// refresh"); only a read that came back empty is `unmeasured`. It names no
// cause: the health page's metrics diagnosis does, and the client links to it.
// `usageRead` is asked, the same cached read the rows used, rather than reading
// emptiness off the cells.
func (r apiReads) measurement(ctx context.Context, q api.ListQuery, kind string) (state, note string) {
	if !hasLiveFigures(kind) {
		return "", ""
	}
	usage, pending := r.s.usageRead(ctx, q.Cluster, kind, firstNamespace(q.Namespace))
	switch {
	case len(usage) > 0:
		return "", ""
	case pending:
		return api.MeasurementPending, "Reading the measurements for this kind; the usage columns fill in when that read returns."
	}
	return api.MeasurementUnmeasured, "No measurements for this kind right now, so the usage columns are empty."
}

// firstNamespace is the one namespace a usage read is scoped to, or "" for all,
// by the fetch's own rule, so the cache is asked what the rows were filled from.
func firstNamespace(namespace string) string {
	selection := parseNamespaces(namespace)
	if len(selection) == 1 {
		return selection[0]
	}
	return ""
}

// dataAddressFor is where a ConfigMap's or a Secret's keys are fetched, empty on
// every other kind. A member rather than a tab, since the block is drawn inside
// the details panel. Its own address, because the client refreshes it after a
// write and the save's `resourceVersion` precondition rides on it, a ConfigMap's
// values are unbounded, and the two reads run in parallel so the facts paint
// while the values arrive.
//
// `object.Title` is the Kind ("Secret") that `detail.HasData` compares against;
// internal/detail fills `Kind` with the discovery plural ("secrets"). `ref.Kind`,
// the route's plural, goes on the wire.
func dataAddressFor(ref api.ObjectRef, object detail.Object) string {
	if !detail.HasData(object.Title) {
		return ""
	}
	return "/api" + clusterPath(ref.Cluster) + "/data" + objectQuery(ref)
}

// metricsAddressFor is where this object's charts are read from, or "" where it
// has none: a pod, a node or a claim. Any other kind would only ever answer `absent`. A
// member, so the charts sit above the fact table, and its own address, since two
// Prometheus range queries can take a second.
func metricsAddressFor(ref api.ObjectRef) string {
	if !isPodKind(ref.Kind) && !isNodeKind(ref.Kind) && !isVolumeClaimKind(ref.Kind) {
		return ""
	}
	return "/api" + clusterPath(ref.Cluster) + "/metrics" + objectQuery(ref)
}

// objectQuery names the object in a panel address's query string, each value
// escaped so a `&`, `#` or `%` cannot end or change it. Written out rather than
// `url.Values.Encode`, which sorts the keys, to keep the contract's order;
// `namespace` is kept when empty, so a cluster-scoped address says it has none.
func objectQuery(ref api.ObjectRef) string {
	return "?kind=" + url.QueryEscape(ref.Kind) +
		"&namespace=" + url.QueryEscape(ref.Namespace) +
		"&name=" + url.QueryEscape(ref.Name)
}

// relationsOf carries the relations graph to the wire. Linkable comes from
// detail.Relation.Linkable and is not recomputed; the mount cap and its "+N
// more" element travel as produced. No glyph: that is the drawing side's
// choice.
func relationsOf(relations []detail.Relation) []api.Relation {
	if len(relations) == 0 {
		return nil
	}
	out := make([]api.Relation, 0, len(relations))
	for _, relation := range relations {
		out = append(out, api.Relation{
			Role: relation.Role,
			Kind: relation.Kind,
			// Left empty by internal/detail for a cluster-scoped target; never the
			// subject's.
			Namespace: relation.Namespace,
			Name:      relation.Name,
			Label:     relation.Label,
			Detail:    relation.Detail,
			Mounts:    relation.Mounts,
			Selector:  relation.Selector,
			Linkable:  relation.Linkable(),
		})
	}
	return out
}

// certificateSetOf carries the parsed TLS material. A bundle's note sits beside
// the certificates that parsed, so a valid leaf is not drawn as a failure nor a
// partial chain as whole. PrivateKeys carries names, never values.
func certificateSetOf(set *detail.CertificateSet) *api.CertificateSet {
	if set == nil {
		return nil
	}
	out := &api.CertificateSet{Note: set.Note, PrivateKeys: set.PrivateKeys}
	for _, bundle := range set.Bundles {
		carried := api.CertificateBundle{Key: bundle.Key, Note: bundle.Note}
		for _, certificate := range bundle.Certificates {
			carried.Certificates = append(carried.Certificates, certificateOf(certificate))
		}
		out.Bundles = append(out.Bundles, carried)
	}
	return out
}

func certificateOf(certificate detail.Certificate) api.Certificate {
	out := api.Certificate{
		Position: certificate.Position,
		// Read off the certificate, not its position.
		Role:       certificate.Role,
		Subject:    certificate.Subject,
		Issuer:     certificate.Issuer,
		SelfSigned: certificate.SelfSigned,
		IsCA:       certificate.IsCA,
		SANs:       certificate.SANs,
		SANsNote:   certificate.SANsNote,
		Expiry:     certificate.Expiry,
		// Graded against the certificate's own lifetime (gradeExpiry): 30 days left is
		// fine on a 90-day certificate and late on a one-year one.
		ExpiryTone:         toneOf(certificate.ExpiryClass),
		Serial:             certificate.Serial,
		SignatureAlgorithm: certificate.SignatureAlgorithm,
		PublicKey:          certificate.PublicKey,
	}
	if !certificate.NotBefore.IsZero() {
		at := certificate.NotBefore
		out.NotBefore = &at
	}
	if !certificate.NotAfter.IsZero() {
		at := certificate.NotAfter
		out.NotAfter = &at
	}
	return out
}

// urlsOf carries the addresses an Ingress serves as internal/detail decided
// them, scheme included; `Note` says where one is not literally what the object
// writes. Nothing is composed from Host and Path here.
func urlsOf(urls []detail.URL) []api.ObjectURL {
	if len(urls) == 0 {
		return nil
	}
	out := make([]api.ObjectURL, 0, len(urls))
	for _, address := range urls {
		out = append(out, api.ObjectURL{
			Role:    address.Role,
			Address: address.Address,
			Host:    address.Host,
			Path:    address.Path,
			// The only reason Address says https, so a client need not re-read the string.
			TLS:     address.TLS,
			Service: address.Service,
			Note:    address.Note,
		})
	}
	return out
}
