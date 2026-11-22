package api

import (
	"net/http"
	"regexp"
)

// The prerequisite installs: an operator an extra page reads from (Trivy
// Operator for the Security Center), installed by creating one Argo CD
// Application through the console's own write door.
//
// The extra page links to these and never writes: an extension has no write
// door on its contract, so an install is the console's act, reviewed and
// recorded as every other is.
//
// The same preview-then-act shape as the create (handlers_act.go): the
// preview is a dry run that writes nothing and mints a token; the install
// takes only the token and the typed gesture, re-derives the facts from the
// plan's own key, and acts only on the plan the person read.
//
// **Registered on every profile**, as the create, the manifest and the verbs
// are, and not withheld under --incluster as the console's own preference and
// settings writes are. Those write the console's state, which is everybody's
// on a shared console; this writes to the cluster, with the person's own
// rights: `create` on the Application is reviewed for the signed-in person at
// the write door (edit.Create), on the desktop and under the server profile
// alike, the act is recorded on stdout by the same door as one structured
// audit record, and a cluster in KT_READONLY_CLUSTERS refuses before anything is
// asked. Nothing an extra or a client supplies reaches the cluster: the key
// picks a row of internal/prereq's table, and the Argo CD namespace comes from
// the console's own reviewed read, carried in the plan.

// prereqKeyPattern is a key into the table: lower-case, as a URL carries it.
var prereqKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func prereqKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.PathValue("key")
	if !prereqKeyPattern.MatchString(key) {
		badRequest(w, "a prerequisite key is lower-case letters, digits and '-'")
		return "", false
	}
	return key, true
}

// getPrereq reads; it is not a mutation and carries no marker. What it says
// decides what the page draws, never what happens: the preview and the install
// check everything again.
func (a *API) getPrereq(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	key, ok := prereqKey(w, r)
	if !ok {
		return
	}
	if a.deps.Prereqs == nil {
		writeOK(w, ValueFailed[Prereq](StateAbsent, absentReads("a prerequisite installer").Reason))
		return
	}
	facts, f := a.deps.Prereqs.Prereq(r.Context(), name, key)
	writeOK(w, valueOf(facts, f))
}

// previewPrereq takes no body: the key is the whole request, and everything
// that reaches the cluster is the console's.
func (a *API) previewPrereq(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	key, ok := prereqKey(w, r)
	if !ok {
		return
	}
	if a.deps.Prereqs == nil {
		missing(w, "a prerequisite installer")
		return
	}
	facts, err := a.deps.Prereqs.PreviewPrereq(r.Context(), name, key)
	if err != nil {
		fail(w, err)
		return
	}
	a.answerPlan(w, r, &plan{kind: planPrereq, cluster: name, facts: facts, prereq: key})
}

// installPrereq checks the gesture before it spends the token (a mistyped
// name agreed to nothing), then re-derives the plan and compares it with the
// one confirmed: the target, and the document itself, so the Application sent
// is the one the person read - a cluster that started serving another version
// of the kind in between gets a fresh plan, not an object nobody saw.
func (a *API) installPrereq(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	key, ok := prereqKey(w, r)
	if !ok {
		return
	}
	var body ActRequest
	if !decode(w, r, maxBody, &body) {
		return
	}
	agreed, ok := a.spendConfirmed(w, r, body.Token, planPrereq, body.Confirmed, body.Force)
	if !ok || !a.sameCluster(w, agreed, name) {
		return
	}
	if agreed.prereq != key {
		planStale(w, ProblemPlanChanged, "this plan installs "+agreed.prereq, nil)
		return
	}
	if a.deps.Prereqs == nil {
		missing(w, "a prerequisite installer")
		return
	}
	fresh, err := a.deps.Prereqs.PreviewPrereq(r.Context(), agreed.cluster, agreed.prereq)
	if err != nil {
		fail(w, err)
		return
	}
	if !sameFacts(agreed.facts, fresh) || agreed.facts.Request != fresh.Request || len(agreed.facts.Targets) != 1 {
		a.reissue(w, r, &plan{kind: planPrereq, cluster: agreed.cluster, facts: fresh, prereq: agreed.prereq},
			"what would be installed changed since the preview")
		return
	}
	outcome, err := a.deps.Prereqs.InstallPrereq(r.Context(), agreed.cluster, agreed.prereq,
		agreed.facts.Targets[0].Namespace)
	if err != nil {
		fail(w, err)
		return
	}
	writeOK(w, outcome)
}
