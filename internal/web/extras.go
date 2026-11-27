package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/extras"
	"github.com/thonicdev/kubetower/internal/statefile"
)

// This file is the console's half of `internal/extras`: the guard every one of
// the additive pages passes through, and the settings handler that turns
// one on or off.
//
// # Why the routes refuse and do not merely go unlinked
//
// Hiding a menu entry is a courtesy; refusing the route is the control. The two
// are not the same thing, and the difference is not theoretical here: these
// pages get bookmarked, `/c/{cluster}/security` is a URL people type, and
// a tab left open across a settings change keeps working against a page the
// installation has since turned off. An extra that is off must be off — a page
// that still answers is a page still making the API calls the installation asked
// this console to stop making.
//
// The same argument `internal/actions` makes about its confirmations, resolved
// the same way: the drawn state is the courtesy, the server-side check is the
// control, and either alone is a defect.

// refuseExtraOff answers a request for an extra this installation has turned
// off, and reports whether it did.
//
// 404 rather than 403, deliberately. 403 says "you may not", which sends the
// reader looking for a permission they lack; the truth is that this
// installation does not have that page at all, which is what a 404 says. The
// body then says how to have it — a refusal that only says no teaches nothing,
// which is the rule `actions.cannotRestart` is written to.
func (s *Server) refuseExtraOff(w http.ResponseWriter, r *http.Request, key string) bool {
	// **Does this build have the page at all**, asked first and without a
	// cluster: a page this KubeTower does not carry is not carried on any cluster,
	// including one whose name does not resolve. Asking the narrower question
	// first would answer "not ticked for this cluster" for a page that is not
	// here at all, and send the reader to a checkbox that does not exist.
	//
	// Every extra is registered by cmd/kubetower, so a key that is not known
	// here is a mounted route whose key the registry no longer holds (after
	// app.Reset) - practically unreachable. The sentence below speaks of a
	// "full build", which this repository does not produce; it is user-visible
	// text and is not reworded here.
	if !extras.Known(key) {
		http.Error(w, fmt.Sprintf("%s is not enabled on this KubeTower: this build of KubeTower "+
			"carries no extra pages — the full build does, and so does the container image.",
			extraLabel(key)), http.StatusNotFound)
		return true
	}
	if s.extras.Pinned(key) && !s.extras.Enabled(key) {
		// The deployment's environment decided, and it is named as such: where
		// the deployment sets it, not a file or a page that may not exist.
		http.Error(w, fmt.Sprintf("%s is not enabled on this KubeTower: it is turned off by %s "+
			"%s, which the console cannot change while it runs. "+
			"Change %s where the deployment sets it and restart KubeTower.",
			extraLabel(key), extras.EnvExtras, extras.WhereSet, extras.EnvExtras), http.StatusNotFound)
		return true
	}

	// **Ticked nowhere at all**, asked before the cluster and deliberately so.
	//
	// This is the one question left that does not need a cluster, and asking it
	// here is what keeps the guard working on a Server built as a literal — which
	// this package's tests do on purpose, and which has no cluster manager.
	// Without it the `s.clusters == nil` escape below would let every extra
	// through: a guard that answers "fine" when it cannot decide is worse than no
	// guard.
	//
	// It is also the more useful sentence for a person who has never used
	// the page: "you have not ticked this anywhere" says the page exists, where
	// "not on this cluster" implies they had it somewhere.
	if !s.extras.Enabled(key) {
		http.Error(w, fmt.Sprintf("%s is not turned on for any cluster yet. %s",
			extraLabel(key), s.howToTurnOn(s.apiClusterSegment(r), key)),
			http.StatusNotFound)
		return true
	}

	// Then the cluster. Resolved through the manager rather than compared as a
	// string: what arrives here may be the `--` spelling or a percent-encoded ARN, and the store is keyed on the canonical name because
	// that is what the Settings page writes.
	//
	// s.clusters is nil on a Server built as a literal. Checked rather than
	// assumed: an invariant true of `New` and false of every fixture only holds
	// where it is not exercised.
	if s.clusters == nil {
		return false
	}
	name, ok := s.clusters.Resolve(requestCluster(r))
	if !ok {
		// **Refused here, before the extra's handler**, with the sentence an
		// extra's own page and the console's API answer the case with. Left to
		// the handler, a cluster that does not resolve would reach an extra's
		// route unreviewed (refuseExtraUnused has no cluster to ask about), and
		// "every route of an extra is reviewed" would rest on each extra
		// resolving the cluster before reading anything. In the API's shape and
		// code, so a page reads one answer for a cluster that is not there.
		api.UnknownCluster(w, requestCluster(r))
		return true
	}
	if s.extras.EnabledOn(name, key) {
		return false
	}

	// On somewhere, and not here: the ordinary refusal, and it names the axis
	// so a person who remembers ticking the page knows why it is missing.
	http.Error(w, fmt.Sprintf("%s is not turned on for this cluster. It is on elsewhere, and "+
		"a switch reaches only the cluster it is made on. %s", extraLabel(key),
		s.howToTurnOn(api.ClusterSegment(name), key)), http.StatusNotFound)
	return true
}

// refuseExtraUnused answers a request for an extra that is on for this
// cluster from a person who may not use it, and reports whether it did. It is
// asked after refuseExtraOff, so an extra that is off is never reviewed and
// stays the 404 it is.
//
// **Under the server profile only.** "May use extra X" is the verb `use` on
// `extras.kubetower.dev/<key>`, reviewed by SubjectAccessReview for the person
// the request carries, through the gate like every other question - the same
// question the switch asks (extensionLocks, useOrRefuse). On the desktop and in
// a container the person is the kubeconfig's identity, the console's one
// user, and nothing is asked.
//
// 403 rather than 404 here, and deliberately the other way round from
// refuseExtraOff: the page is on for this cluster and the person is the one
// thing missing, so the answer names the permission to ask for. A review that
// could not be made refuses too, as the 503 it is: nobody said no, and nothing
// is relaxed for it.
func (s *Server) refuseExtraUnused(w http.ResponseWriter, r *http.Request, key string) bool {
	if !s.inCluster {
		return false
	}
	cluster := requestCluster(r)
	if s.clusters != nil {
		name, ok := s.clusters.Resolve(cluster)
		if !ok {
			// refuseExtraOff answers this first; refused here too, so the
			// review is never skipped for a cluster nobody can name.
			api.UnknownCluster(w, cluster)
			return true
		}
		cluster = name
	}
	answers, _ := s.reviewUse(r.Context(), cluster, []string{key})
	answer := answers[0]
	switch {
	case answer.Allowed():
		return false
	case answer.Decision == access.Unreviewable:
		api.WriteProblem(w, http.StatusServiceUnavailable, api.ProblemAccessUnreviewable,
			"Access could not be reviewed", fmt.Sprintf("Whether you may use %s on this cluster could not be reviewed: %s",
				extraLabel(key), firstLine(answer.Reason)))
	case s.gate.RefusesTheConsole(answer):
		// The shared password account is reviewed as the console's own
		// ServiceAccount (a self review), so the grant missing is the
		// ServiceAccount's, which an administrator gives - not the person's.
		api.WriteProblem(w, http.StatusForbidden, api.ProblemRBACDenied, "Refused", consoleMayNotUse(extraLabel(key), key))
	default:
		api.WriteProblem(w, http.StatusForbidden, api.ProblemRBACDenied, "Refused",
			fmt.Sprintf("You may not use %s on this cluster: it needs the verb use on %s/%s, granted by a ClusterRole.",
				extraLabel(key), UseGroup, extras.Canonical(key)))
	}
	return true
}

// consoleMayNotUse is the refusal of `use` on the extra key, labeled label,
// when the gate refused the console's own ServiceAccount (RefusesTheConsole):
// the shared password account is reviewed as the console itself (a self
// review), so the grant missing is the ServiceAccount's, which an
// administrator gives - not the person's. One sentence for the route
// (refuseExtraUnused) and the switch (extensionLocks), so the two cannot say
// it differently.
func consoleMayNotUse(label, key string) string {
	return fmt.Sprintf("This console's ServiceAccount, which the shared account acts with, may not use %s on "+
		"this cluster: it needs the verb use on %s/%s, granted to the ServiceAccount by a ClusterRole. "+
		"An administrator grants it to the console.", label, UseGroup, extras.Canonical(key))
}

// extrasInMenu is which of the extras on for cluster the menu draws for the
// person ctx carries: the same question refuseExtraUnused asks at the route,
// so the menu never offers a link to its own refusal. Under the server profile
// every extra that is on is reviewed for `use` in one ReviewAll, and one that
// is denied or could not be reviewed is left out; elsewhere every extra that is
// on is drawn.
//
// **Left out for two different reasons, and the second is said.** A denial is
// the person's answer and leaves nothing to tell. A review that could not be
// made is nobody's answer: drawn as a bare absence, a person holding `use`
// under a failed review would get the menu of a person refused it - an empty
// answer and a failed one rendered alike (R5 §5). The note is the Extra
// heading's sentence for it, so the heading stays, with no link, and says the
// review could not be made.
func (s *Server) extrasInMenu(ctx context.Context, cluster string) (func(string) bool, string) {
	on := map[string]bool{}
	var keys []string
	for _, e := range extras.Catalogue() {
		if !s.extras.EnabledOn(cluster, e.Key) {
			continue
		}
		on[e.Key] = true
		keys = append(keys, e.Key)
	}
	var unreviewed []string
	reason := ""
	if s.inCluster && len(keys) > 0 {
		answers, _ := s.reviewUse(ctx, cluster, keys)
		for i, answer := range answers {
			if answer.Allowed() {
				continue
			}
			on[keys[i]] = false
			if answer.Decision == access.Unreviewable {
				unreviewed = append(unreviewed, extraLabel(keys[i]))
				if reason == "" {
					reason = firstLine(answer.Reason)
				}
			}
		}
	}
	return func(key string) bool { return on[key] }, unreviewableNote(unreviewed, reason)
}

// unreviewableNote is the Extra heading's sentence for the extras whose `use`
// could not be reviewed, or "" where there are none. It names them, says they
// are left out because nobody could answer rather than because the answer was
// no, and carries the first reason the gate gave.
func unreviewableNote(labels []string, reason string) string {
	switch len(labels) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("Whether you may use %s on this cluster could not be reviewed, so it is left out "+
			"until the review can be made: %s", labels[0], reason)
	}
	return fmt.Sprintf("Whether you may use %s on this cluster could not be reviewed, so they are left out "+
		"until the review can be made: %s", strings.Join(labels, ", "), reason)
}

// howToTurnOn is the sentence a refusal ends with: the two switches that exist,
// spelled so they can be typed. The cluster is the context name
// percent-encoded, which is what the API's `{cluster}` takes (`api/openapi.yaml`,
// the Cluster parameter) and the escape the console's own pages use too
// (`clusterPath`).
//
// The sentence itself is extras.HowToTurnOn, which the start-up line also uses,
// so the two cannot name different switches. This server
// supplies the one thing the sentence cannot know: which profile it runs, and
// whether it is one of several replicas, which have no per-cluster write.
func (s *Server) howToTurnOn(clusterSegment, key string) string {
	return extras.HowToTurnOn(clusterSegment, key, s.extrasProfile())
}

// extrasProfile is which switches exist here, from the same three facts
// internal/api's router is given (Deps.InCluster, Deps.Replicated,
// Deps.StateEphemeral) and through the same function.
func (s *Server) extrasProfile() extras.Profile {
	return extras.ProfileOf(s.inCluster, s.replicated, s.setup.StateEphemeral)
}

// extensionSwitch is whether the per-cluster write is registered, answered here
// for the listing's `writable`: the profile's, as the router reads it.
func (s *Server) extensionSwitch() bool {
	return s.extrasProfile().Switchable()
}

// UseGroup is the API group of the extras' `use` verb: "may use extra X" is `use` on the
// resource `<key>` of `extras.kubetower.dev`, granted with an ordinary
// ClusterRole and reviewed by SubjectAccessReview at the one choke point.
const UseGroup = "extras.kubetower.dev"

// useAttributes is the `use` question about one extension.
func useAttributes(key string) access.Attributes {
	return access.Attributes{Verb: "use", Group: UseGroup, Resource: extras.Canonical(key)}
}

// reviewUse answers `use` about each of keys on cluster, in order, and the
// question that answered it. Each is asked about its key first, all in one
// ReviewAll; one refused is then asked about every key it was published under
// (extras.LegacyOf), and the first of those allowed answers it.
//
// **A rename must not take the page from anyone.** An operator's ClusterRole
// grants `use` on the resource it named then: `jvm` for the Runtime Inspector
// published as the JVM Inspector. Asked only about the new key, that person
// loses the menu entry, the routes and the switch on upgrade, with nothing
// said. A refusal still names the new key, the one to grant from now on.
func (s *Server) reviewUse(ctx context.Context, cluster string, keys []string) ([]access.Answer, []access.Attributes) {
	asked := make([]access.Attributes, len(keys))
	for i, key := range keys {
		asked[i] = useAttributes(key)
	}
	answers := s.gate.ReviewAll(ctx, cluster, asked)
	var again []access.Attributes
	var of []int
	for i, key := range keys {
		if answers[i].Allowed() || answers[i].Decision == access.Unreviewable {
			continue
		}
		for _, old := range extras.LegacyOf(extras.Canonical(key)) {
			again = append(again, access.Attributes{Verb: "use", Group: UseGroup, Resource: old})
			of = append(of, i)
		}
	}
	if len(again) == 0 {
		return answers, asked
	}
	for j, answer := range s.gate.ReviewAll(ctx, cluster, again) {
		i := of[j]
		switch {
		case answers[i].Allowed():
		case answer.Allowed():
			answers[i], asked[i] = answer, again[j]
		case answer.Decision == access.Unreviewable:
			// A grant under the old key may exist and could not be read:
			// neither a yes nor a no, so 503, never 403, and nothing
			// granted (#157).
			answers[i], asked[i] = answer, again[j]
		}
	}
	return answers, asked
}

// lock is why one extension's switch is not the person's to press, or none.
type lock struct{ locked, reason string }

// extensionLocks answers, for each row of the store's State on one cluster and
// in the same order, why the person asking may not turn it on or off and
// change its settings there - or nothing, where they may. The listing's `writable` and the suggestion banner both
// read it; the writes refuse on their own (useOrRefuse, the store).
//
// **`use` is reviewed under the server profile only**, all rows in one
// ReviewAll, held a minute by the gate like every answer. On the desktop and
// in a container the person is the kubeconfig's identity, the one user of
// the console, and the switch is not reviewed there: the store's own
// refusals - a pin - are all there is.
func (s *Server) extensionLocks(ctx context.Context, cluster string, states []extras.State) []lock {
	out := make([]lock, len(states))
	var asked []int
	var questions []string
	for i, state := range states {
		switch {
		case !s.extensionSwitch():
			out[i] = lock{api.ExtensionNoSwitch, s.noSwitchReason()}
		case state.Pinned:
			out[i] = lock{api.ExtensionPinned, state.Note}
		case !state.Allowed:
			out[i] = lock{api.ExtensionNotInValues, extras.NotInValues}
		case s.inCluster:
			asked = append(asked, i)
			questions = append(questions, state.Key)
		}
	}
	if len(questions) == 0 {
		return out
	}
	answers, _ := s.reviewUse(ctx, cluster, questions)
	for j, answer := range answers {
		if answer.Allowed() {
			continue
		}
		i := asked[j]
		var why string
		switch {
		case answer.Decision == access.Unreviewable:
			why = fmt.Sprintf("Whether you may turn %s on or off could not be reviewed: %s",
				states[i].Label, firstLine(answer.Reason))
		case s.gate.RefusesTheConsole(answer):
			// The shared password account, reviewed as the console itself: the
			// grant missing is the ServiceAccount's, said as the route says it.
			why = consoleMayNotUse(states[i].Label, states[i].Key)
		default:
			why = fmt.Sprintf("You may not turn %s on or off on this cluster: it needs the verb use on "+
				"%s/%s, granted by a ClusterRole.", states[i].Label, UseGroup, states[i].Key)
		}
		out[i] = lock{api.ExtensionNoUse, why}
	}
	return out
}

// noSwitchReason is the sentence for a console with no per-cluster write.
func (s *Server) noSwitchReason() string {
	if s.replicated {
		return "This console runs as several replicas, which have no per-cluster switch: " +
			"extras are turned on with one replica and its state volume."
	}
	return "This console keeps no state volume (KT_STATE_PERSISTENT=false), so a choice made " +
		"here would be lost at the next restart: extras are turned on with one replica and its state volume."
}

// useOrRefuse runs one write to the extras store about key on cluster -
// turning it on or off, or one of its settings - and, under the server
// profile, reviews it first as `use` for the person ctx carries and
// records it in the audit log (access.Gate.Administer): who, which
// extension, which cluster, and change. A denial never reaches the store.
//
// A key this build does not carry skips the review, so the store answers it
// as the 404 it is rather than as a refusal about nothing.
func (s *Server) useOrRefuse(ctx context.Context, cluster, key, change string, write func() error) error {
	if !s.inCluster || !extras.Known(extras.Canonical(key)) {
		return write()
	}
	// Administered as the question that allows it, so a grant on the name the
	// extra was published under writes as it reads.
	// Reviewed once: the answer reviewUse read is the one the write is
	// recorded under (#157).
	answers, granted := s.reviewUse(ctx, cluster, []string{key})
	_, err := s.gate.AdministerAnswered(ctx, cluster, granted[0], answers[0], change, func() (bool, error) {
		err := write()
		// A choice in effect that could not be written down was applied.
		return err == nil || errors.Is(err, statefile.ErrNotSaved), err
	})
	return err
}

// apiClusterSegment is the `{cluster}` of an API path for this request's
// cluster, or the placeholder `<cluster>` where there is none to name.
//
// The refusal for an extra that is off everywhere is asked before any cluster
// is resolved - see refuseExtraOff - so it can arrive here from a Server with no
// cluster manager, or from a request whose cluster does not resolve. A literal
// `{cluster}` there reads as a path to type; angle brackets read as "fill this
// in", and a real cluster, when there is one, needs no filling in at all.
func (s *Server) apiClusterSegment(r *http.Request) string {
	if s.clusters != nil {
		if name, ok := s.clusters.Resolve(requestCluster(r)); ok {
			return api.ClusterSegment(name)
		}
	}
	return "<cluster>"
}

// requestCluster is the cluster a request is about, from wherever this router
// puts it: a path value on a page, a query parameter on a fragment.
//
// Both spellings, because the eight extras routes are four pages and four
// fragments and the guard is one function. A guard that only knew the page form
// would let every fragment through, which is the half of the pair that refreshes
// itself every few seconds.
func requestCluster(r *http.Request) string {
	if name := r.PathValue("cluster"); name != "" {
		return name
	}
	return r.URL.Query().Get("cluster")
}

// extraLabel is what to call an extra in a sentence: its catalog label,
// falling back to the key.
//
// The fallback is not decoration: `refuseExtraOff` is reachable with whatever a
// URL carried. A key the catalog does not hold is one nothing registered, and
// this console does not know what it is called — inventing a label would be
// inventing the page. The key is what the URL carried, which is enough for "x
// is not enabled on this KubeTower" to read as a refusal rather than as a
// broken sentence.
func extraLabel(key string) string {
	for _, e := range extras.Catalogue() {
		if e.Key == key {
			return e.Label
		}
	}
	return key
}
