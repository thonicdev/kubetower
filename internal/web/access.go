package web

import (
	"context"

	"k8s.io/client-go/kubernetes"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/edit"
)

// Asking the cluster whether the person behind a request may do what a page
// offers. This is the courtesy; the write itself is reviewed again through the
// same gate (internal/access), which is the control.
//
// Shell, under the server profile, is asked per pod: `create pods/exec` on that
// pod, the question the socket is reviewed with, so both share one held answer.
// A profile-wide question would answer no to a person granted only some
// namespaces. `Capabilities.podExec` only says this build can open a pod shell;
// the desktop asks nothing more, and the exec is reviewed when it opens.
//
// The person, and the console too: under the server profile the request is the
// ServiceAccount's, so a question the person was not refused is asked again as
// the console (access.AsConsole), and where only the console may not, the
// control is drawn disabled naming the console's missing grant (consoleReason).
// The shared password account's refusal is the console's own (selfReason), also
// drawn disabled. On the desktop the person's review is the console's, asked
// once, and a refusal is withheld.
//
// Each verb a page offers is asked as the request it will send (actions.Needs):
// allowed is offered; denied is withheld; not established is drawn disabled,
// neither hidden (a no nobody gave) nor offered as working (a yes nobody gave).
// A verb the read-only guardrail refuses keeps that sentence and is not asked.
//
// One gate serves the page and the write: answers are held for access.TTL,
// failures for access.FailedTTL. The TTL is a revocation delay for both.

// NewGate is the access gate over a cluster manager, reviewing through each
// cluster's own clientset. The process builds one and shares it with the Server,
// the terminal and the forward manager, so every review shares one set of held
// answers; a Server handed none builds its own. records is the audit log every
// act is written to; nil keeps none, which an in-cluster gate refuses every act
// for.
func NewGate(clusters *cluster.Manager, inCluster bool, logf func(string, ...any), records *audit.Log) *access.Gate {
	return access.New(access.Options{
		Reviews: access.Clientset(func(name string) (kubernetes.Interface, error) {
			client, err := clusters.Client(name)
			if err != nil {
				return nil, err
			}
			return client.Clients, nil
		}),
		InCluster: inCluster,
		Logf:      logf,
		Audit:     records,
	})
}

// execAttributes is Shell's question about one pod, by name: what
// internal/terminal reviews the socket with, so both are one held answer.
func execAttributes(namespace, pod string) access.Attributes {
	return access.Attributes{Verb: "create", Version: "v1", Resource: "pods", Subresource: "exec", Namespace: namespace, Name: pod}
}

// mayExec is whether this build can open a shell in a pod at all: the exec
// channel is registered and there is a cluster. It asks the cluster nothing;
// under the server profile each pod's page answers for that pod (shellFor).
func (s *Server) mayExec() bool {
	return (apiChannels{s}).Offers(api.ChannelExec) && len(s.clusters.Contexts()) > 0
}

// shellFor is whether a pod's page draws Shell, under the server profile; nil
// elsewhere, where Capabilities.PodExec alone decides.
func (s *Server) shellFor(ctx context.Context, ref api.ObjectRef) *api.ShellOffer {
	if !s.inCluster || !isPodKind(ref.Kind) || ref.Namespace == "" || !s.mayExec() {
		return nil
	}
	return shellOffer(reviewBoth(ctx, s.gate, ref.Cluster, []access.Attributes{execAttributes(ref.Namespace, ref.Name)})[0])
}

// debugAttributes is Debug's question about the change, by name: what
// internal/terminal reviews and records the PATCH adding the container with.
func debugAttributes(namespace, pod string) access.Attributes {
	return access.Attributes{Verb: "patch", Version: "v1", Resource: "pods", Subresource: "ephemeralcontainers", Namespace: namespace, Name: pod}
}

// mayDebug is mayExec for the debug channel.
func (s *Server) mayDebug() bool {
	return (apiChannels{s}).Offers(api.ChannelDebug) && len(s.clusters.Contexts()) > 0
}

// debugFor is whether a pod's page offers Debug: nil on every other kind and
// where the channel is not served.
//
// Unlike Shell it is answered on both profiles, because a read-only cluster
// refuses it with a sentence (writable's), asked of nobody: adding a container
// is a change, which the guardrail refuses at the socket too. Shell is refused
// there only at its socket, `exec` being a mutating verb of the shell rather
// than a write, so its page draws it.
//
// On the desktop it is offered, as Capabilities.PodExec offers Shell: the
// kubeconfig's own identity goes, and the socket reviews it when it opens.
// Under the server profile both questions the socket asks are asked here, for
// the person and the console, and the worse answer decides as Shell's does:
// withheld from a person refused either, disabled with its sentence where
// unknown or where the console's ServiceAccount may not.
func (s *Server) debugFor(ctx context.Context, ref api.ObjectRef, writable bool, note string) *api.ShellOffer {
	if !isPodKind(ref.Kind) || ref.Namespace == "" || !s.mayDebug() {
		return nil
	}
	if !writable {
		reason := api.Because(api.CodeVerbDenied, note).Retry(false)
		return &api.ShellOffer{Reason: &reason}
	}
	if !s.inCluster {
		return &api.ShellOffer{Permitted: true}
	}
	verdicts := reviewBoth(ctx, s.gate, ref.Cluster, []access.Attributes{
		debugAttributes(ref.Namespace, ref.Name), execAttributes(ref.Namespace, ref.Name),
	})
	return shellOffer(worse(verdicts[0], verdicts[1]))
}

// shellOffer is one exec verdict as the page is told it: withheld from a person
// who may not; disabled with its sentence where unknown or where the console's
// ServiceAccount may not; offered where both may.
func shellOffer(v verdict) *api.ShellOffer {
	switch {
	case v.decision == access.Allowed:
		return &api.ShellOffer{Permitted: true}
	case v.decision == access.Denied && !v.console:
		return nil
	case v.decision == access.Denied:
		reason := api.Because(api.CodeVerbDenied, v.reason).Retry(false)
		return &api.ShellOffer{Reason: &reason}
	default:
		// Not established: only asked under the server profile, where the socket refuses
		// such an answer too.
		reason := unreviewableReason(v.reason)
		return &api.ShellOffer{Reason: &reason}
	}
}

// editableFor is whether the object's manifest and data are offered for editing
// to the person asking: editable, and the sentence for a disabled control. The
// read-only guardrail answers first; then `patch` on this object, the write
// door's question, is asked as a verb is and answered as Shell is (editOffer).
func (s *Server) editableFor(ctx context.Context, ref api.ObjectRef) (bool, string) {
	if writable, note := s.writable(ref.Cluster); !writable {
		return false, note
	}
	where, err := editResolver{objects: s.objects}.Locate(ctx, ref.Cluster, ref.Kind)
	if err != nil {
		return editOffer(verdict{decision: access.Unreviewable, reason: firstLine(err.Error())})
	}
	target := edit.Target{Cluster: ref.Cluster, Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
	return editOffer(editVerdictAt(ctx, s.gate, where, target))
}

// editVerdictAt is the edit's verdict once the kind is resolved, a function of
// the gate and the Location so a test can hand it both.
func editVerdictAt(ctx context.Context, gate *access.Gate, where edit.Location, target edit.Target) verdict {
	return reviewBoth(ctx, gate, where.Cluster, []access.Attributes{edit.Attributes(where, target, "patch", "")})[0]
}

// editOffer is one edit verdict as the panels carry it: allowed or let through
// on the desktop is editable; refused by the console, or unknown under the server
// profile, is not editable with a sentence; refused to the person is not
// editable with no sentence, which the panels draw as no edit control at all.
func editOffer(v verdict) (bool, string) {
	switch {
	case v.decision == access.Allowed:
		return true, ""
	case v.decision == access.Denied && !v.console:
		return false, ""
	case v.decision == access.Denied:
		return false, v.reason
	case v.through:
		return true, ""
	default:
		return false, unreviewableReason(v.reason).Message
	}
}

// verdict is what a page is told about one verb.
type verdict struct {
	decision access.Decision
	reason   string
	// through is an answer that could not be established and that the write door
	// lets through anyway: the desktop's own credentials (access.Gate.LetsThrough).
	through bool
	// everywhere is a denial of a question asked about every namespace at once: the
	// person may still be allowed in a row's namespace.
	everywhere bool
	// console is a denial of the console's own ServiceAccount (consoleReason, or the
	// shared account's selfReason): drawn disabled, since an administrator can grant
	// it.
	console bool
	// self marks a console denial that is the shared account's own review: that
	// account has no answer of its own to contradict, so its bar may also say
	// "choose a namespace". A single sign-on person refused by the console was
	// allowed, and that hint would be wrong for them.
	self bool
}

// reviewBoth answers each question for the person and, where the person was not
// refused, for the console too: one verdict per question, in order. The console
// is asked only where the person's answer was the person's (access.Person). A
// console answer that is not a denial changes nothing: a timed-out self review
// is not a refusal.
func reviewBoth(ctx context.Context, gate *access.Gate, cluster string, questions []access.Attributes) []verdict {
	out := make([]verdict, len(questions))
	var again []access.Attributes
	var at []int
	for i, answer := range gate.ReviewAll(ctx, cluster, questions) {
		switch answer.Decision {
		case access.Allowed:
			out[i] = verdict{decision: access.Allowed}
		case access.Denied:
			out[i] = verdict{decision: access.Denied, reason: answer.Reason}
			if gate.RefusesTheConsole(answer) {
				// The console's own grant: disabled with what is missing, never withheld.
				out[i] = verdict{decision: access.Denied, console: true, self: true, reason: selfReason(questions[i])}
			}
			continue
		case access.NotInProfile:
			// Drawn disabled with the profile's sentence, as the console's own
			// missing grant is: no RoleBinding of the person's would change it.
			out[i] = verdict{decision: access.Denied, console: true, reason: rbacNotInProfileReason}
			continue
		default:
			out[i] = verdict{decision: access.Unreviewable, reason: answer.Reason, through: gate.LetsThrough(answer)}
		}
		if answer.Subject == access.Person {
			again = append(again, questions[i])
			at = append(at, i)
		}
	}
	if len(again) == 0 {
		return out
	}
	for j, answer := range gate.ReviewAll(access.AsConsole(ctx), cluster, again) {
		if answer.Decision == access.Denied {
			out[at[j]] = verdict{decision: access.Denied, console: true, reason: consoleReason(again[j])}
		}
	}
	return out
}

// rbacNotInProfileReason is the sentence on a control that would change RBAC
// under the server profile (access.NotInProfile).
var rbacNotInProfileReason = access.ErrRBACNotInProfile.Error() +
	": this console does not create, change or delete roles, cluster roles or their bindings there"

// consoleReason is the sentence on a control the person may use and the
// console's ServiceAccount may not: what is missing, and the chart switch that
// grants it.
func consoleReason(attrs access.Attributes) string {
	return "you may, but this console may not: its ServiceAccount is not granted " + attrs.Verb + " on " +
		grantedWhat(attrs) + grantedWhere(attrs) +
		", so the cluster would refuse the request. An administrator grants it to the console (with the KubeTower chart, `" +
		chartGrant(attrs) + "`)"
}

// selfReason is consoleReason for the shared account under the server profile,
// whose self review is the console's ServiceAccount: only what is missing and
// who can grant it. A kind the chart's role does not list needs a role of its
// own, and the sentence says so.
func selfReason(attrs access.Attributes) string {
	return "this console's ServiceAccount, which the shared account acts with, is not granted " +
		attrs.Verb + " on " + grantedWhat(attrs) + grantedWhere(attrs) +
		", so the cluster would refuse the request. An administrator grants it to the console: the KubeTower chart's `" +
		chartGrant(attrs) + "` covers the kinds its role lists, and any other kind needs a Role or ClusterRole of its own"
}

// chartGrant is the chart switch that grants a question: `rbac.exec` for a shell
// or a forward, and for the debug container a shell is opened in, `rbac.write`
// for everything this console changes.
func chartGrant(attrs access.Attributes) string {
	if attrs.Subresource == "exec" || attrs.Subresource == "portforward" || attrs.Subresource == "ephemeralcontainers" {
		return "rbac.exec"
	}
	return "rbac.write"
}

func grantedWhat(attrs access.Attributes) string {
	what := attrs.Resource
	if attrs.Subresource != "" {
		what += "/" + attrs.Subresource
	}
	if attrs.Group != "" {
		what += "." + attrs.Group
	}
	return what
}

func grantedWhere(attrs access.Attributes) string {
	if attrs.Namespace == "" {
		return ""
	}
	return " in " + attrs.Namespace
}

// verbVerdicts asks every question actions.Needs gives for each verb and answers
// the worst: one denial withholds, one unreviewable leaves it unknown. A page's
// questions go together (Gate.ReviewAll), so several verbs are one wait.
func (s *Server) verbVerdicts(ctx context.Context, cluster, kind string, target edit.Target, verbs []actions.Verb) map[actions.Verb]verdict {
	out := make(map[actions.Verb]verdict, len(verbs))
	if len(verbs) == 0 {
		return out
	}
	where, err := editResolver{objects: s.objects}.Locate(ctx, cluster, kind)
	if err != nil {
		for _, verb := range verbs {
			out[verb] = verdict{decision: access.Unreviewable, reason: firstLine(err.Error())}
		}
		return out
	}
	return verdictsAt(ctx, s.gate, where, target, verbs)
}

// verdictsAt is verbVerdicts once the kind is resolved, so a test can hand it
// the gate and the Location.
func verdictsAt(ctx context.Context, gate *access.Gate, where edit.Location, target edit.Target, verbs []actions.Verb) map[actions.Verb]verdict {
	out := make(map[actions.Verb]verdict, len(verbs))
	var questions []access.Attributes
	var owner []actions.Verb
	for _, verb := range verbs {
		out[verb] = verdict{decision: access.Allowed}
		for _, q := range actions.Needs(verb, where, target) {
			questions = append(questions, q)
			owner = append(owner, verb)
		}
	}
	// Asked on the resolved context name, as the write is; the worst answer per
	// verb.
	for i, v := range reviewBoth(ctx, gate, where.Cluster, questions) {
		out[owner[i]] = worse(out[owner[i]], v)
	}
	return out
}

// throughReason is the sentence on a control the console could not ask about and
// still offers: on the desktop the kubeconfig's own identity goes, and the
// cluster decides.
func throughReason(why string) api.Reason {
	message := "the console could not ask the cluster whether you may do this; it is offered, and the cluster decides when you act"
	if why != "" {
		message += ": " + why
	}
	return api.Because(api.CodeAccessUnreviewable, message).Retry(true)
}

// unreviewableReason is the sentence on a control the console could not ask
// about.
func unreviewableReason(why string) api.Reason {
	message := "the console could not ask the cluster whether you may do this, so it is not offered as working"
	if why != "" {
		message += ": " + why
	}
	return api.Because(api.CodeAccessUnreviewable, message).Retry(true)
}

// gateActions applies the verdicts to what objectActions offered: denied is
// withheld, unknown disabled with its sentence, allowed left as it was. A verb
// the guardrail already disabled keeps the guardrail's sentence.
func gateActions(offered []api.DetailAction, verdicts map[actions.Verb]verdict) []api.DetailAction {
	out := make([]api.DetailAction, 0, len(offered))
	for _, action := range offered {
		if !action.Permitted {
			out = append(out, action)
			continue
		}
		switch v := verdicts[actions.Verb(action.Verb)]; v.decision {
		case access.Allowed:
			out = append(out, action)
		case access.Denied:
			if !v.console {
				continue
			}
			// The console's ServiceAccount may not: disabled, naming the missing grant.
			action.Permitted = false
			reason := api.Because(api.CodeVerbDenied, v.reason).Retry(false)
			action.Reason = &reason
			out = append(out, action)
		default:
			// Offered on the desktop, where the write goes through; disabled under the
			// server profile, where it is refused.
			if v.through {
				reason := throughReason(v.reason)
				action.Reason = &reason
				out = append(out, action)
				continue
			}
			action.Permitted = false
			reason := unreviewableReason(v.reason)
			action.Reason = &reason
			out = append(out, action)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// gateBulk is gateActions for the selection bar's verbs.
func gateBulk(offered *api.BulkAffordance, verdicts map[actions.Verb]verdict) *api.BulkAffordance {
	if offered == nil {
		return nil
	}
	out := &api.BulkAffordance{Scope: offered.Scope}
	for _, verb := range offered.Verbs {
		if !verb.Permitted {
			out.Verbs = append(out.Verbs, verb)
			continue
		}
		switch v := verdicts[actions.Verb(verb.Verb)]; v.decision {
		case access.Allowed:
			out.Verbs = append(out.Verbs, verb)
		case access.Denied:
			var message string
			switch {
			case v.everywhere && v.self:
				// The shared account's own refusal: both sentences, the more actionable first.
				// Never for a single sign-on person the console refuses, who may.
				message = everywhereReason + "; " + v.reason
			case v.console:
				message = v.reason
			case v.everywhere:
				message = everywhereReason
			default:
				continue
			}
			verb.Permitted = false
			reason := api.Because(api.CodeVerbDenied, message).Retry(false)
			verb.Reason = &reason
			out.Verbs = append(out.Verbs, verb)
		default:
			if v.through {
				reason := throughReason(v.reason)
				verb.Reason = &reason
				out.Verbs = append(out.Verbs, verb)
				continue
			}
			verb.Permitted = false
			reason := unreviewableReason(v.reason)
			verb.Reason = &reason
			out.Verbs = append(out.Verbs, verb)
		}
	}
	if len(out.Verbs) == 0 {
		return nil
	}
	return out
}

// everywhereReason is the sentence on a verb the person may not perform in
// every namespace, on a list of all of them.
const everywhereReason = "you may not do this in every namespace; choose a namespace to see whether you may there"

// gatedActions is the object page's verbs, answered for the person asking.
func (s *Server) gatedActions(ctx context.Context, ref api.ObjectRef, offered []api.DetailAction) []api.DetailAction {
	var verbs []actions.Verb
	for _, action := range offered {
		if action.Permitted {
			verbs = append(verbs, actions.Verb(action.Verb))
		}
	}
	if len(verbs) == 0 {
		return offered
	}
	target := edit.Target{Cluster: ref.Cluster, Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
	return gateActions(offered, s.verbVerdicts(ctx, ref.Cluster, ref.Kind, target, verbs))
}

// gatedBulk is the selection bar's verbs, answered for the person asking in every
// namespace the list shows: a selection can hold rows from each, so a verb is
// offered only where it would work on all. A list of every namespace asks once,
// cluster-wide.
func (s *Server) gatedBulk(ctx context.Context, cluster, kind string, namespaced bool, namespaces []string, offered *api.BulkAffordance) *api.BulkAffordance {
	if offered == nil {
		return nil
	}
	var verbs []actions.Verb
	for _, verb := range offered.Verbs {
		if verb.Permitted {
			verbs = append(verbs, actions.Verb(verb.Verb))
		}
	}
	if len(verbs) == 0 {
		return offered
	}
	verdicts := acrossNamespaces(bulkScopes(namespaced, namespaces), func(namespace string) map[actions.Verb]verdict {
		return s.verbVerdicts(ctx, cluster, kind, edit.Target{Cluster: cluster, Kind: kind, Namespace: namespace}, verbs)
	})
	return gateBulk(offered, acrossEveryNamespace(namespaced, namespaces, verdicts))
}

// acrossEveryNamespace marks the denials on a namespaced list of every
// namespace, asked cluster-wide: a person granted some namespaces is denied that
// question but allowed on some rows, so the verb is disabled with a sentence
// rather than withheld.
func acrossEveryNamespace(namespaced bool, namespaces []string, verdicts map[actions.Verb]verdict) map[actions.Verb]verdict {
	if !namespaced || len(namespaces) > 0 {
		return verdicts
	}
	for verb, v := range verdicts {
		if v.decision == access.Denied {
			v.everywhere = true
			verdicts[verb] = v
		}
	}
	return verdicts
}

// bulkScopes are the namespaces a selection's verbs are asked about: each one
// the list shows, or all of them at once ("") for a cluster-scoped kind or a
// list of every namespace.
func bulkScopes(namespaced bool, namespaces []string) []string {
	if namespaced && len(namespaces) > 0 {
		return namespaces
	}
	return []string{""}
}

// acrossNamespaces is the worst verdict per verb over every scope: a verb is
// offered only where it would work on all the rows a selection can hold.
func acrossNamespaces(scopes []string, ask func(namespace string) map[actions.Verb]verdict) map[actions.Verb]verdict {
	combined := map[actions.Verb]verdict{}
	for _, namespace := range scopes {
		for verb, v := range ask(namespace) {
			combined[verb] = worse(combined[verb], v)
		}
	}
	return combined
}

// worse is the answer that stops a verb sooner: the person's denial over the
// console's, a denial over an unknown, an unknown over an allowance. The zero
// verdict, "not asked yet", loses to everything.
func worse(a, b verdict) verdict {
	rank := func(v verdict) int {
		switch {
		case v.decision == access.Denied && !v.console:
			return 4
		case v.decision == access.Denied:
			return 3
		case v.decision == access.Unreviewable:
			return 2
		case v.decision == access.Allowed:
			return 1
		}
		return 0
	}
	if rank(b) > rank(a) {
		return b
	}
	// Two unknowns: offered only if both would be let through.
	if a.decision == access.Unreviewable && b.decision == access.Unreviewable {
		a.through = a.through && b.through
	}
	return a
}

// mayReveal reviews a reveal before anything is read: `get` on the object, by
// name. A nil failure means allowed; located is false when the kind could not be
// resolved, so nothing was reviewed and there is no act to record.
func (s *Server) mayReveal(ctx context.Context, q api.EnvQuery, scope api.Scope) (where edit.Location, located bool, decision access.Decision, fail *api.Failure) {
	where, err := editResolver{objects: s.objects}.Locate(ctx, q.Cluster, q.Kind)
	if err != nil {
		return edit.Location{}, false, access.Unreviewable, stateOf(err, scope)
	}
	decision, fail = revealVerdict(ctx, s.gate, where, q, scope)
	return where, true, decision, fail
}

// revealAttributes is the question a reveal is reviewed, and recorded, as.
func revealAttributes(where edit.Location, q api.EnvQuery) access.Attributes {
	return edit.Attributes(where, edit.Target{Namespace: q.Namespace, Name: q.Name}, "get", "")
}

// revealVerdict is mayReveal once the kind is resolved.
func revealVerdict(ctx context.Context, gate *access.Gate, where edit.Location, q api.EnvQuery, scope api.Scope) (access.Decision, *api.Failure) {
	attrs := revealAttributes(where, q)
	answer := gate.Review(ctx, where.Cluster, attrs)
	if answer.Allowed() {
		return access.Allowed, nil
	}
	refused := (&access.Refused{Cluster: where.Cluster, Attributes: attrs, Answer: answer}).Error()
	if answer.Decision == access.Denied {
		return access.Denied, api.Forbidden(refused).Within(scope)
	}
	return access.Unreviewable, api.Fail(api.StateUnreadable, api.CodeAccessUnreviewable, refused).Within(scope).Retryable()
}
