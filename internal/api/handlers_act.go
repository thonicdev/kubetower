package api

import (
	"net/http"
	"strings"
)

// The confirm-then-act pairs, one shape for all of them.
//
// The preview is a dry run that writes nothing; it is a POST only because its
// subject is a body, so it is not cacheable and must never be retried as the
// act. The act takes only a token, and the server re-derives the facts from the
// request that token was issued for: the one mechanism binding a confirmation
// to what happens.

func (a *API) actsOrMissing(w http.ResponseWriter) bool {
	if a.deps.Acts == nil {
		missing(w, "a writer")
		return false
	}
	return true
}

// --- Data -------------------------------------------------------------------

func (a *API) previewData(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	var body DataWrite
	if !decode(w, r, maxBody, &body) {
		return
	}
	if body.Kind == "" || body.Name == "" || body.ResourceVersion == "" {
		badRequest(w, "kind, name and resourceVersion are required")
		return
	}
	if !a.actsOrMissing(w) {
		return
	}
	facts, err := a.deps.Acts.PreviewData(r.Context(), name, body)
	if err != nil {
		fail(w, err)
		return
	}
	a.answerPlan(w, r, &plan{kind: planData, cluster: name, facts: facts, data: &body})
}

// applyData carries the object's resourceVersion as a precondition, so an
// object changed since the preview gets a 409 with the fresh state rather than
// a silent overwrite.
func (a *API) applyData(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	var body ApplyRequest
	if !decode(w, r, maxBody, &body) {
		return
	}
	agreed, ok := a.spend(w, r, body.Token, planData)
	if !ok || !a.sameCluster(w, agreed, name) {
		return
	}
	if !a.actsOrMissing(w) {
		return
	}
	fresh, err := a.deps.Acts.PreviewData(r.Context(), agreed.cluster, *agreed.data)
	if err != nil {
		fail(w, err)
		return
	}
	if !sameFacts(agreed.facts, fresh) {
		a.reissue(w, r, &plan{kind: planData, cluster: agreed.cluster, facts: fresh, data: agreed.data},
			"the object changed since the preview")
		return
	}
	outcome, err := a.deps.Acts.ApplyData(r.Context(), agreed.cluster, *agreed.data)
	if err != nil {
		fail(w, err)
		return
	}
	writeOK(w, outcome)
}

// --- Manifest ---------------------------------------------------------------

// previewManifest accepts any kind. The kind checked is the one read out of the
// submitted document after parsing, never one a route or field claims:
// checking the route's kind and writing the body's is a full bypass, so this
// endpoint takes no kind at all.
func (a *API) previewManifest(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	var body ManifestWrite
	if !decode(w, r, maxManifestBody, &body) {
		return
	}
	if body.Document == "" {
		badRequest(w, "document is required")
		return
	}
	if len(body.Document) > 4000000 {
		badRequest(w, "document is larger than the contract allows")
		return
	}
	if !a.actsOrMissing(w) {
		return
	}
	facts, err := a.deps.Acts.PreviewManifest(r.Context(), name, body)
	if err != nil {
		fail(w, err)
		return
	}
	a.answerPlan(w, r, &plan{kind: planManifest, cluster: name, facts: facts, manifest: &body})
}

func (a *API) applyManifest(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	var body ApplyRequest
	if !decode(w, r, maxBody, &body) {
		return
	}
	agreed, ok := a.spend(w, r, body.Token, planManifest)
	if !ok || !a.sameCluster(w, agreed, name) {
		return
	}
	if !a.actsOrMissing(w) {
		return
	}
	fresh, err := a.deps.Acts.PreviewManifest(r.Context(), agreed.cluster, *agreed.manifest)
	if err != nil {
		fail(w, err)
		return
	}
	if !sameFacts(agreed.facts, fresh) {
		a.reissue(w, r, &plan{kind: planManifest, cluster: agreed.cluster, facts: fresh, manifest: agreed.manifest},
			"the object changed since the preview")
		return
	}
	outcome, err := a.deps.Acts.ApplyManifest(r.Context(), agreed.cluster, *agreed.manifest)
	if err != nil {
		fail(w, err)
		return
	}
	writeOK(w, outcome)
}

// --- Create -----------------------------------------------------------------

// previewCreate is previewManifest for an object that does not exist yet: a
// dry run of a create, never a patch. It takes no kind for the same reason,
// and refuses a resourceVersion. The backend reviews it for the person
// whatever ListPage.create said, so a forged request is refused there.
func (a *API) previewCreate(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	var body ManifestWrite
	if !decode(w, r, maxManifestBody, &body) {
		return
	}
	if body.Document == "" {
		badRequest(w, "document is required")
		return
	}
	if len(body.Document) > 4000000 {
		badRequest(w, "document is larger than the contract allows")
		return
	}
	if body.ResourceVersion != "" {
		badRequest(w, "a new object has no resourceVersion; this creates objects and never replaces one")
		return
	}
	if !a.actsOrMissing(w) {
		return
	}
	facts, err := a.deps.Acts.PreviewCreate(r.Context(), name, body)
	if err != nil {
		fail(w, err)
		return
	}
	a.answerPlan(w, r, &plan{kind: planCreate, cluster: name, facts: facts, manifest: &body})
}

// applyCreate re-runs the create's dry run from the plan's document and
// compares facts, as applyManifest does, so an object created by someone else
// in between is refused before the act.
func (a *API) applyCreate(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	var body ApplyRequest
	if !decode(w, r, maxBody, &body) {
		return
	}
	agreed, ok := a.spend(w, r, body.Token, planCreate)
	if !ok || !a.sameCluster(w, agreed, name) {
		return
	}
	if !a.actsOrMissing(w) {
		return
	}
	fresh, err := a.deps.Acts.PreviewCreate(r.Context(), agreed.cluster, *agreed.manifest)
	if err != nil {
		fail(w, err)
		return
	}
	if !sameFacts(agreed.facts, fresh) {
		a.reissue(w, r, &plan{kind: planCreate, cluster: agreed.cluster, facts: fresh, manifest: agreed.manifest},
			"what would be created changed since the preview")
		return
	}
	outcome, err := a.deps.Acts.ApplyCreate(r.Context(), agreed.cluster, *agreed.manifest)
	if err != nil {
		fail(w, err)
		return
	}
	writeOK(w, outcome)
}

// --- One object -------------------------------------------------------------

func (a *API) previewAction(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	var body ActionRequest
	if !decode(w, r, maxBody, &body) {
		return
	}
	if body.Verb == "" || body.Target.Kind == "" || body.Target.Name == "" {
		badRequest(w, "verb and a target with a kind and a name are required")
		return
	}
	if !a.actsOrMissing(w) {
		return
	}
	facts, err := a.deps.Acts.PreviewAction(r.Context(), name, body)
	if err != nil {
		fail(w, err)
		return
	}
	a.answerPlan(w, r, &plan{kind: planAction, cluster: name, facts: facts, action: &body})
}

func (a *API) runAction(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	var body ActRequest
	if !decode(w, r, maxBody, &body) {
		return
	}
	agreed, ok := a.spendConfirmed(w, r, body.Token, planAction, body.Confirmed, body.Force)
	if !ok || !a.sameCluster(w, agreed, name) {
		return
	}
	if !a.actsOrMissing(w) {
		return
	}
	fresh, err := a.deps.Acts.PreviewAction(r.Context(), agreed.cluster, *agreed.action)
	if err != nil {
		fail(w, err)
		return
	}
	if !sameFacts(agreed.facts, fresh) {
		a.reissue(w, r, &plan{kind: planAction, cluster: agreed.cluster, facts: fresh, action: agreed.action},
			whyChanged(agreed.facts, fresh, "the object is no longer what the plan described"))
		return
	}
	// The pin comes from the agreed facts, never the body: a client never
	// supplies it. The backend checks it again just before the write; this
	// comparison only offers the fresh plan, that one is the guarantee.
	request := *agreed.action
	request.Precondition = agreed.facts.Precondition
	request.PreconditionUID = agreed.facts.PreconditionUID
	// The "Force delete" box, let through by spendConfirmed only on a plan
	// that said `forceable`.
	request.Force = body.Force
	outcome, err := a.deps.Acts.RunAction(r.Context(), agreed.cluster, request, body.Confirmed)
	if err != nil {
		fail(w, err)
		return
	}
	a.mintNextStage(r, agreed.cluster, &outcome)
	writeOK(w, outcome)
}

// mintNextStage turns the request a staged verb hands back (a drain's
// evictions after its cordon) into the plan the client opens next.
//
// It goes through the preview, the same call runAction re-derives with, so the
// pod list on screen and the one compared at the click come from the same
// reading. Only this path stores a stage-two request; a client's preview cannot
// name one. A next stage that cannot be planned does not fail the act, which
// did happen: the outcome says so and the operator runs the verb again.
func (a *API) mintNextStage(r *http.Request, cluster string, outcome *ActOutcome) {
	next := outcome.NextRequest
	outcome.NextRequest = nil
	if next == nil {
		return
	}
	facts, err := a.deps.Acts.PreviewAction(r.Context(), cluster, *next)
	if err == nil {
		var made ConfirmationPlan
		made, err = a.planFrom(r.Context(), &plan{kind: planAction, cluster: cluster, facts: facts, action: next})
		if err == nil {
			outcome.NextPlan = &made
			return
		}
	}
	outcome.Summary = strings.TrimSpace(outcome.Summary + " The next stage could not be planned: " +
		err.Error() + ". Run the verb again to retry it.")
}

// --- A selection ------------------------------------------------------------

// previewBulk names every target it resolved; `facts.count` is what the
// operator agrees to, confirmed by count as `facts.confirmation` says.
func (a *API) previewBulk(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	var body BulkRequest
	if !decode(w, r, maxBody, &body) {
		return
	}
	if body.Verb == "" || body.Kind == "" {
		badRequest(w, "verb and kind are required")
		return
	}
	if len(body.Targets) == 0 || len(body.Targets) > 5000 {
		badRequest(w, "between one and five thousand targets")
		return
	}
	if !a.actsOrMissing(w) {
		return
	}
	facts, err := a.deps.Acts.PreviewBulk(r.Context(), name, body)
	if err != nil {
		fail(w, err)
		return
	}
	a.answerPlan(w, r, &plan{kind: planBulk, cluster: name, facts: facts, bulk: &body})
}

// runBulk re-resolves the selection from the token and refuses with 409 if the
// set is no longer the one that was shown.
func (a *API) runBulk(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	var body ActRequest
	if !decode(w, r, maxBody, &body) {
		return
	}
	agreed, ok := a.spendConfirmed(w, r, body.Token, planBulk, body.Confirmed, body.Force)
	if !ok || !a.sameCluster(w, agreed, name) {
		return
	}
	if !a.actsOrMissing(w) {
		return
	}
	fresh, err := a.deps.Acts.PreviewBulk(r.Context(), agreed.cluster, *agreed.bulk)
	if err != nil {
		fail(w, err)
		return
	}
	if !sameFacts(agreed.facts, fresh) {
		a.reissue(w, r, &plan{kind: planBulk, cluster: agreed.cluster, facts: fresh, bulk: agreed.bulk},
			"the selection resolved to a different set")
		return
	}
	outcome, err := a.deps.Acts.RunBulk(r.Context(), agreed.cluster, *agreed.bulk)
	if err != nil {
		fail(w, err)
		return
	}
	writeOK(w, outcome)
}

// --- The node shell ---------------------------------------------------------

// previewNodeShell is the only plan whose act is not an operation here: the
// shell is a WebSocket upgrade (`api/websocket.md`) that carries this token,
// so confirmation and act stay bound across two protocols, and an upgrade
// without a live token is refused before a socket opens.
func (a *API) previewNodeShell(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	var body NodeShellRequest
	if !decode(w, r, maxBody, &body) {
		return
	}
	if body.Node == "" || len(body.Node) > 253 {
		badRequest(w, "node is required")
		return
	}
	if !a.actsOrMissing(w) {
		return
	}
	facts, err := a.deps.Acts.PreviewNodeShell(r.Context(), name, body)
	if err != nil {
		fail(w, err)
		return
	}
	a.answerPlan(w, r, &plan{kind: planNodeShell, cluster: name, facts: facts})
}

// --- Shared -----------------------------------------------------------------

func (a *API) answerPlan(w http.ResponseWriter, r *http.Request, p *plan) {
	out, err := a.planFrom(r.Context(), p)
	if err != nil {
		planStoreFailure(w, err)
		return
	}
	writeOK(w, out)
}

// spend takes the token. An unknown token names no request to re-derive a
// plan from, so the 409 tells the front end to preview again.
func (a *API) spend(w http.ResponseWriter, r *http.Request, token string, kind planKind) (*plan, bool) {
	if token == "" {
		badRequest(w, "token is required; preview first")
		return nil, false
	}
	agreed, ok, err := a.plans.take(r.Context(), token, kind)
	if err != nil {
		planStoreFailure(w, err)
		return nil, false
	}
	if !ok {
		planStale(w, ProblemPlanExpired, "this token is unknown, already spent or expired", nil)
		return nil, false
	}
	return agreed, true
}

// spendConfirmed is spend for a plan that asks for a gesture, checking the
// gesture before the token is taken: a mismatch agreed to nothing, so the plan
// stays and the person corrects it in the same dialog. The re-derivation that
// follows still compares the plan with the cluster, and take stays the only
// thing making a plan single-use.
//
// `force` is checked here for the same reason: only a `forceable` plan (a
// single delete) may act with a grace period of zero.
func (a *API) spendConfirmed(w http.ResponseWriter, r *http.Request, token string, kind planKind, confirmed string, force bool) (*plan, bool) {
	if token == "" {
		badRequest(w, "token is required; preview first")
		return nil, false
	}
	held, ok, err := a.plans.peek(r.Context(), token, kind)
	if err != nil {
		planStoreFailure(w, err)
		return nil, false
	}
	if !ok {
		planStale(w, ProblemPlanExpired, "this token is unknown, already spent or expired", nil)
		return nil, false
	}
	if okGesture, why := gestureSatisfied(held.facts, confirmed); !okGesture {
		badRequest(w, why)
		return nil, false
	}
	if force && !held.facts.Forceable {
		badRequest(w, "force deletes with a grace period of zero, and this plan is not a single delete")
		return nil, false
	}
	return a.spend(w, r, token, kind)
}

// sameCluster refuses a token issued against one cluster and spent against
// another. The act re-derives from the plan's own cluster, so this changes
// nothing that happens; it stops a confirmation read about one cluster being
// reported back at another's address.
func (a *API) sameCluster(w http.ResponseWriter, p *plan, cluster string) bool {
	if p.cluster == cluster {
		return true
	}
	planStale(w, ProblemPlanChanged, "this plan was made against "+p.cluster, nil)
	return false
}
