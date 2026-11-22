package web

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/edit"
	"sigs.k8s.io/yaml"
)

type apiActs struct{ s *Server }

// writeError turns a refusal from internal/edit into the sentinel the contract
// maps onto a status. A Kubernetes 403 keeps its message and is never a 500.
func writeError(err error) error {
	if err == nil {
		return nil
	}
	// The access guard holding back a request no review covered is a console defect:
	// a 500, never a 403 or a 400.
	if refusal, ok := edit.AsRefusal(err); (ok && refusal.Kind == edit.RefusedUnsent) || errors.Is(err, access.ErrNotReviewed) {
		reason := firstLine(err.Error())
		if ok {
			reason = refusal.Reason
		}
		return fmt.Errorf("the console held back its own request, which is a defect in the console and not "+
			"the cluster's answer: %s", reason)
	}
	if refusal, ok := edit.AsRefusal(err); ok {
		switch refusal.Kind {
		case edit.RefusedReadOnly:
			return fmt.Errorf("%w: %s", api.ErrReadOnly, refusal.Reason)
		case edit.RefusedConflict:
			return fmt.Errorf("%w: %s", api.ErrConflict, refusal.Reason)
		case edit.RefusedByAPI, edit.RefusedByAccess:
			return fmt.Errorf("%w: %s", api.ErrRefused, refusal.Reason)
		case edit.RefusedUnreviewable:
			return fmt.Errorf("%w: %s", api.ErrUnreviewable, refusal.Reason)
		case edit.RefusedUnrecorded:
			return fmt.Errorf("%w: %s", api.ErrUnrecorded, refusal.Reason)
		case edit.RefusedNotSent:
			return fmt.Errorf("%w: %s", api.ErrNotSent, refusal.Reason)
		case edit.RefusedUnanswered:
			return fmt.Errorf("%w: %s", api.ErrUnanswered, refusal.Reason)
		case edit.RefusedNotInProfile:
			// The reason opens with the sentinel's own sentence: said once.
			return fmt.Errorf("%w%s", api.ErrRBACNotInProfile,
				strings.TrimPrefix(refusal.Reason, access.ErrRBACNotInProfile.Error()))
		}
		return fmt.Errorf("%w: %s", api.ErrInvalid, refusal.Reason)
	}
	return fmt.Errorf("%w: %s", api.ErrInvalid, firstLine(err.Error()))
}

func (a apiActs) PreviewAction(ctx context.Context, name string, req api.ActionRequest) (api.PlanFacts, error) {
	if a.s.act == nil {
		return api.PlanFacts{}, fmt.Errorf("%w: this build has no write path", api.ErrNotInProfile)
	}
	verb := actions.Verb(req.Verb)
	if !actions.Known(verb) {
		return api.PlanFacts{}, fmt.Errorf("%w: %q is not a verb this console performs", api.ErrInvalid, req.Verb)
	}
	if err := a.s.refuseOwnNamespace(verb, req.Target.Kind, req.Target.Name); err != nil {
		return api.PlanFacts{}, err
	}
	if req.IncludeUnowned && verb != actions.Drain {
		return api.PlanFacts{}, fmt.Errorf("%w: includeUnowned is a drain's, and %q is not a drain",
			api.ErrInvalid, req.Verb)
	}
	target := api.PlanTarget{Kind: req.Target.Kind, Namespace: req.Target.Namespace, Name: req.Target.Name}
	request := actions.Request{
		Target:         edit.Target{Cluster: name, Kind: req.Target.Kind, Namespace: req.Target.Namespace, Name: req.Target.Name},
		Verb:           verb,
		Stage:          req.Stage,
		IncludeUnowned: req.IncludeUnowned,
	}
	if verb == actions.Drain && req.Stage == actions.StageEvict {
		return a.evictionFacts(ctx, req, request)
	}
	recap, err := a.s.act.Preview(ctx, request)
	if err != nil {
		return api.PlanFacts{}, writeError(err)
	}
	spec, hasSpec := specFor(verb, req.Target.Kind)
	facts := api.PlanFacts{
		Verb:        req.Verb,
		Targets:     []api.PlanTarget{target},
		Count:       1,
		Destructive: destructiveFor(verb, req.Target.Kind),
		Confirmation: &api.Confirmation{
			Kind:     gestureFor(verb, req.Target.Kind),
			Expected: actions.SingleConfirm(verb, req.Target.Kind, req.Target.Name),
		},
	}
	if hasSpec {
		// The verb's own words from `specFor`, which knows a restart on a Pod is a
		// delete; the dialog folds them under "Details" and leads with the line below.
		facts.Explanation = spec.Prompt
		facts.Warning = spec.Warning
		facts.Button = buttonFor(spec, req.Target.Name)
	}
	if verb == actions.Scale {
		// Scale asks for a value: the gesture is `number`, where what is typed is the
		// act. `minimum: 0` and no maximum: scaling to nothing is deliberate
		// (`actions.Request.Replicas` is a pointer to tell zero from unstated), and a
		// maximum would invent a quota the cluster enforces itself.
		facts.Confirmation = &api.Confirmation{
			Kind:    "number",
			Value:   replicasNow(recap),
			Minimum: int64Of(0),
			Label:   scaleLabel(recap),
		}
	}
	// One line about this object and its own warnings (actionline.go); the line
	// becomes the plan's sentence in `internal/api`. `irreversible` is the line's,
	// not the spec's: a delete of a pod its ReplicaSet replaces is not irreversible,
	// a delete of a bare pod is.
	if hasSpec {
		line := lineFor(verb, req.Target.Kind, req.Target.Name, recap)
		facts.Headline = line.Text
		facts.Warnings = append(facts.Warnings, line.Warnings...)
		if line.Irreversible {
			facts.Irreversible = boolOf(true)
		}
	}
	facts.Recap = recapOf(recap)
	// The version the recap was read at is the one the act carries: stated on the
	// plan, compared by `internal/api` before the act and handed to RunAction as a
	// precondition. It is the pin (`actions.PinOf`), not the resourceVersion, which
	// moves with every status write and would refuse a confirmation for a heartbeat.
	facts.Precondition = recap.Pin.Value
	facts.PreconditionOn = recap.Pin.On
	// And the UID, which the pin cannot stand for: a generation restarts at 1 on a
	// recreate.
	facts.PreconditionUID = recap.Pin.UID
	if verb == actions.Delete {
		// A single delete is a simple confirmation: the line and a button, nothing
		// under it. `forceable` draws "Force delete" and is what `internal/api` checks
		// an act's `force` against.
		facts.Explanation = ""
		facts.Warning = ""
		facts.Warnings = nil
		facts.Irreversible = nil
		facts.Recap = nil
		facts.Forceable = true
	}
	if verb == actions.Drain && req.IncludeUnowned {
		// Said on the first stage too, where the choice was made.
		facts.Warnings = append(facts.Warnings, "This drain also evicts the pods nothing owns, as "+
			"kubectl drain --force does. Nothing creates them again: each is listed by name in the next "+
			"stage, with its warning, before anything is evicted.")
	}
	return facts, nil
}

// evictionFacts is the second stage of a drain as a plan: the pods to evict by
// name with their warnings, the ones left alone as warnings, and the node's name
// to type again.
//
// The second stage needs `Stage == StageEvict`, which no client request can say,
// so the first stage's outcome hands back the request and `internal/api` mints a
// plan through this function, the same one it re-derives the facts with before
// evicting: the list on screen and the list compared at the click are one
// reading, and a changed set gets the usual 409. The gesture is the node's name,
// as the runner insists at both stages. The facts say `evict`, so the sentence
// reads "Evict 3 pods"; the request still says drain, which the runner routes
// on. No precondition: the pods are the targets.
func (a apiActs) evictionFacts(ctx context.Context, req api.ActionRequest, request actions.Request) (api.PlanFacts, error) {
	plan, err := a.s.act.DrainPlan(ctx, request)
	if err != nil {
		return api.PlanFacts{}, writeError(err)
	}
	facts := api.PlanFacts{
		Verb:    string(actions.Evict),
		Targets: make([]api.PlanTarget, 0, len(plan.Evict)),
		Count:   len(plan.Evict),
		// A phrase for the set this stage resolved, from the same `plural` call as the
		// gesture's label. Not `spec.Button`, written for one object.
		Button: "Evict " + plural(len(plan.Evict), "pod", "pods"),
		// Each is a pod eviction, so red as a single Evict is.
		Destructive: destructiveFor(actions.Evict, "pods"),
		Confirmation: &api.Confirmation{
			Kind:     "name",
			Expected: actions.SingleConfirm(actions.Drain, req.Target.Kind, req.Target.Name),
			Label: fmt.Sprintf("Type %s again to evict %s from it", req.Target.Name,
				plural(len(plan.Evict), "pod", "pods")),
		},
		Recap: &api.PlanRecap{Note: drainEvictNote(ctx, a.s.act, request)},
	}
	for _, pod := range plan.Evict {
		facts.Targets = append(facts.Targets, api.PlanTarget{
			Kind: "pods", Namespace: pod.Namespace, Name: pod.Name, Note: pod.Warning,
		})
	}
	for _, pod := range plan.Skip {
		facts.Warnings = append(facts.Warnings,
			fmt.Sprintf("%s/%s is left alone: %s", pod.Namespace, pod.Name, pod.Skip))
	}
	if plan.Note != "" {
		facts.Warnings = append(facts.Warnings, plan.Note)
	}
	if !plan.Cordoned {
		facts.Warnings = append(facts.Warnings, req.Target.Name+" is not cordoned now — something uncordoned "+
			"it since the first stage. Evicting cordons it again first, so nothing is scheduled back onto it.")
	}
	// The pods nothing owns are left alone, as kubectl drain without --force, and
	// listed among the warnings; evicting them is offered as a plan of its own.
	if !req.IncludeUnowned {
		facts.Alternative = includeUnownedOffer(req, plan.SparedUnowned())
	}
	return facts, nil
}

// includeUnownedOffer is the same drain asked to include the pods nothing owns,
// where a drain left some; nil where it left none. One producer for the
// eviction stage's plan and the cordon's outcome.
func includeUnownedOffer(req api.ActionRequest, spared []string) *api.PlanAlternative {
	if len(spared) == 0 {
		return nil
	}
	return &api.PlanAlternative{
		Label: "Plan again, evicting the " + plural(len(spared), "pod", "pods") + " nothing owns as well",
		Request: api.AlternativeRequest{
			Verb: req.Verb, Target: req.Target, IncludeUnowned: true,
		},
	}
}

// drainEvictNote is the recap sentence for the eviction stage, worded by the
// package that owns it. A node that cannot be read already refused DrainPlan, so
// an error here is a blank note.
func drainEvictNote(ctx context.Context, act *actions.Runner, request actions.Request) string {
	recap, err := act.Preview(ctx, request)
	if err != nil {
		return ""
	}
	return recap.Note
}

// recapOf carries the change to the wire as fields, not a flattened string,
// because the dialog draws two flags: `unchanged` (already at that value, so
// nothing changes) and `absent` (not set, `before` carrying the default in
// effect).
func recapOf(recap actions.Recap) *api.PlanRecap {
	if !recap.Stated() && recap.Note == "" {
		return nil
	}
	return &api.PlanRecap{
		Field:  recap.Field,
		Before: recap.Before,
		After:  recap.After,
		// Both flags travel, false included, so a client never re-compares the strings.
		Absent:    recap.Absent,
		Unchanged: recap.Unchanged,
		Note:      recap.Note,
	}
}

// gestureFor names how a person is asked to agree, as `internal/actions` decided;
// a front end left to choose would choose the easier one.
func gestureFor(verb actions.Verb, kind string) string {
	switch {
	case verb == actions.Scale:
		// The one gesture whose answer is a value; the caller fills its bounds and
		// current reading.
		return "number"
	case !actions.TypedOne(verb, kind):
		// A single delete among them: a button.
		return "acknowledge"
	case actions.ByCount(verb, kind):
		return "count"
	}
	return "name"
}

// replicasNow is the count in effect, off the recap the preview already read, or
// nil where it is not a number: opening on 0 for a workload running twelve would
// be a confident wrong answer.
func replicasNow(recap actions.Recap) *int64 {
	if !recap.Stated() {
		return nil
	}
	value, err := strconv.ParseInt(strings.TrimSpace(recap.Before), 10, 64)
	if err != nil {
		return nil
	}
	return &value
}

// scaleLabel is the sentence above the box, so "Type 12" reads as a check rather
// than an obstacle.
func scaleLabel(recap actions.Recap) string {
	if now := replicasNow(recap); now != nil {
		return fmt.Sprintf("It is running %s. How many replicas?", plural(int(*now), "replica", "replicas"))
	}
	return "How many replicas?"
}

func int64Of(v int64) *int64 { return &v }

func (a apiActs) RunAction(ctx context.Context, name string, req api.ActionRequest, confirmed string) (api.ActOutcome, error) {
	if a.s.act == nil {
		return api.ActOutcome{}, fmt.Errorf("%w: this build has no write path", api.ErrNotInProfile)
	}
	verb := actions.Verb(req.Verb)
	if err := a.s.refuseOwnNamespace(verb, req.Target.Kind, req.Target.Name); err != nil {
		return api.ActOutcome{}, err
	}
	target := edit.Target{Cluster: name, Kind: req.Target.Kind, Namespace: req.Target.Namespace, Name: req.Target.Name}

	// The replica count is the gesture itself. `internal/api` has checked it against
	// the plan's bounds; it is parsed again here because it is about to be patched
	// into a running workload. nil is not zero: an unstated count is refused, a
	// deliberate zero patches zero.
	var replicas *int64
	if verb == actions.Scale {
		value, err := strconv.ParseInt(strings.TrimSpace(confirmed), 10, 64)
		if err != nil || value < 0 {
			return api.ActOutcome{}, fmt.Errorf(
				"%w: a scale is confirmed by typing the number of replicas, and %q is not one",
				api.ErrInvalid, confirmed)
		}
		replicas = &value
	}

	result, err := a.s.act.Run(ctx, actions.Request{
		Target:   target,
		Verb:     verb,
		Stage:    req.Stage,
		Replicas: replicas,
		// Asked of the one function that composes it, so the box and the server cannot
		// insist on different strings.
		Confirm: actions.SingleConfirm(verb, req.Target.Kind, req.Target.Name),
		// The pin the plan stated, filled by `internal/api`, never by a client. Empty
		// means none, as for a drain's second stage.
		Precondition:    req.Precondition,
		PreconditionUID: req.PreconditionUID,
		IncludeUnowned:  req.IncludeUnowned,
		// Let through by `internal/api` only on a `forceable` plan: a single delete.
		Force: req.Force,
	})
	if err != nil {
		return api.ActOutcome{}, writeError(err)
	}
	if verb == actions.Drain && result.Plan != nil {
		return drainOutcome(req, result), nil
	}
	return api.ActOutcome{
		Succeeded: 1,
		Results: []api.ActResult{{
			Target:  api.PlanTarget{Kind: req.Target.Kind, Namespace: req.Target.Namespace, Name: req.Target.Name},
			OK:      true,
			Message: result.Message,
		}},
		// The runner's sentence: a restart on a pod was a delete, a suspend left a run
		// in flight alone.
		Summary: result.Message,
	}, nil
}

// drainOutcome is the two stages of a drain on the wire.
//
// The first stage's outcome is the cordon on the node and, when there is
// something to evict, the request for the second stage, which `internal/api`
// turns into the plan the client opens next (`Result.Plan`). Where only pods
// nothing owns remain, the outcome offers the drain including them instead of
// minting an eviction stage of nothing; the offer is not part of any agreement
// (sameFacts ignores it), and pressing it previews a drain whose eviction stage
// names each pod.
//
// The second stage's outcome is per pod: accepted or refused, with what the API
// server said, so a PodDisruptionBudget's refusal shows which pod it protected.
// Unanswered is `timeout`, never sent is `not_probed_yet`; neither is a refusal.
func drainOutcome(req api.ActionRequest, result actions.Result) api.ActOutcome {
	plan := result.Plan
	if req.Stage != actions.StageEvict {
		out := api.ActOutcome{
			Succeeded: 1,
			Results: []api.ActResult{{
				Target:  api.PlanTarget{Kind: req.Target.Kind, Namespace: req.Target.Namespace, Name: req.Target.Name},
				OK:      true,
				Message: result.Message,
			}},
			Summary: result.Message,
		}
		if len(plan.Evict) > 0 {
			// The one place a stage-two request is made; the stage stays server-side.
			out.NextRequest = &api.ActionRequest{Verb: req.Verb, Target: req.Target, Stage: actions.StageEvict,
				IncludeUnowned: req.IncludeUnowned}
		} else if !req.IncludeUnowned {
			out.Alternative = includeUnownedOffer(req, plan.SparedUnowned())
		}
		return out
	}
	out := api.ActOutcome{
		Succeeded: plan.Requested, Failed: plan.Failed,
		Results: make([]api.ActResult, 0, len(plan.Evict)),
		Summary: result.Message,
	}
	for _, pod := range plan.Evict {
		entry := api.ActResult{
			Target:  api.PlanTarget{Kind: "pods", Namespace: pod.Namespace, Name: pod.Name},
			OK:      !pod.Failed,
			Message: pod.Outcome,
		}
		if pod.Failed {
			reason := api.Because(api.CodeVerbDenied, pod.Outcome)
			switch {
			case pod.Unanswered, pod.TimedOut:
				reason = api.Because(api.CodeTimeout, pod.Outcome).Retry(true)
			case pod.NotAttempted:
				reason = api.Because(api.CodeNotProbedYet, pod.Outcome).Retry(true)
			}
			entry.Reason = &reason
		}
		out.Results = append(out.Results, entry)
	}
	if plan.Note != "" {
		out.Summary = strings.TrimSpace(out.Summary + " " + plan.Note)
	}
	return out
}

func (a apiActs) PreviewBulk(ctx context.Context, name string, req api.BulkRequest) (api.PlanFacts, error) {
	verb := actions.Verb(req.Verb)
	if !actions.IsBulk(verb) {
		return api.PlanFacts{}, fmt.Errorf("%w: %q is not a verb that means anything over a set", api.ErrInvalid, req.Verb)
	}
	if err := a.s.refuseOwnNamespace(verb, req.Kind, targetNames(req.Targets)...); err != nil {
		return api.PlanFacts{}, err
	}
	targets := make([]api.PlanTarget, 0, len(req.Targets))
	for _, target := range req.Targets {
		targets = append(targets, api.PlanTarget{Kind: req.Kind, Namespace: target.Namespace, Name: target.Name})
	}
	// Every target is reviewed as the single door's preview reviews one, so a
	// row the person may not touch is marked refused here and not first at the
	// run. A set with no row they may act on is refused whole, as one object is.
	if a.s != nil && a.s.act != nil {
		reviews := a.s.act.ReviewBulk(ctx, actions.BulkRequest{
			Cluster: name, Kind: req.Kind, Verb: verb, Targets: actionTargets(req.Targets),
		})
		var first error
		allowed := 0
		for i, review := range reviews {
			if review.Err == nil {
				allowed++
				continue
			}
			if first == nil {
				first = review.Err
			}
			reason := review.Err.Error()
			if refusal, ok := edit.AsRefusal(review.Err); ok {
				reason = refusal.Reason
			}
			targets[i].Refused = true
			targets[i].Note = firstLine(reason)
		}
		if allowed == 0 && first != nil {
			return api.PlanFacts{}, writeError(first)
		}
	}
	facts := api.PlanFacts{
		Verb:        req.Verb,
		Targets:     targets,
		Count:       len(targets),
		Destructive: destructiveFor(verb, req.Kind),
		Confirmation: &api.Confirmation{
			// A selection is confirmed by its count, not a typed name.
			Kind:     gestureFor(verb, req.Kind),
			Expected: actions.BulkConfirm(verb, req.Kind, len(targets)),
		},
	}
	if spec, ok := specFor(verb, req.Kind); ok {
		// The bulk plan's own words. `specFor`'s sentences and `spec.Button` are written
		// for one object, so the button is composed for a set: `spec.Label` (as the
		// selection bar's control reads it), the count the server resolved, and the kind
		// as discovery spells it (`kindWords`). Without a Button the dialog would show
		// the wire slug. Destructiveness stays `destructiveFor`'s.
		facts.Button = fmt.Sprintf("%s %d %s", spec.Label, len(targets), a.kindWord(ctx, name, req.Kind, len(targets)))
	}
	if words, ok := a.s.kindWords(ctx, name, req.Kind); ok && len(targets) > 0 {
		// Named as the button names it, where `sentenceFor` only has the slug.
		facts.Headline = bulkLine(verb, req.Kind, targets, words)
	}
	return facts, nil
}

// bulkLine is a selection's sentence: `sentenceFor`'s, with the kind as the
// cluster spells it and, for namespaces deleted, what goes with them.
func bulkLine(verb actions.Verb, slug string, targets []api.PlanTarget, words kindWords) string {
	said := strings.ToUpper(string(verb[:1])) + string(verb[1:])
	var line string
	if len(targets) == 1 {
		where := targets[0].Name
		if targets[0].Namespace != "" {
			where = targets[0].Namespace + "/" + where
		}
		line = fmt.Sprintf("%s %s %s.", said, words.one, where)
	} else {
		line = fmt.Sprintf("%s %d %s. This cannot be undone.", said, len(targets), words.many)
	}
	if verb == actions.Delete && slug == "namespaces" {
		if len(targets) == 1 {
			return line + " Everything in it is deleted with it."
		}
		return line + " Everything in them is deleted with them."
	}
	return line
}

// kindWord is api.KindWord with the cluster's own spelling where discovery
// answers, and the slug's rule where it does not.
func (a apiActs) kindWord(ctx context.Context, cluster, slug string, count int) string {
	words, ok := a.s.kindWords(ctx, cluster, slug)
	if !ok {
		return api.KindWord(slug, count)
	}
	if count == 1 {
		return words.one
	}
	return words.many
}

func (a apiActs) RunBulk(ctx context.Context, name string, req api.BulkRequest) (api.ActOutcome, error) {
	if a.s.act == nil {
		return api.ActOutcome{}, fmt.Errorf("%w: this build has no write path", api.ErrNotInProfile)
	}
	verb := actions.Verb(req.Verb)
	if err := a.s.refuseOwnNamespace(verb, req.Kind, targetNames(req.Targets)...); err != nil {
		return api.ActOutcome{}, err
	}
	targets := make([]actions.BulkTarget, 0, len(req.Targets))
	for _, target := range req.Targets {
		targets = append(targets, actions.BulkTarget{Namespace: target.Namespace, Name: target.Name})
	}
	result, err := a.s.act.RunBulk(ctx, actions.BulkRequest{
		Cluster: name,
		Kind:    req.Kind,
		Verb:    verb,
		Targets: targets,
		Confirm: actions.BulkConfirm(verb, req.Kind, len(targets)),
	})
	if err != nil {
		return api.ActOutcome{}, writeError(err)
	}
	// Per target, always: a bulk act that half worked is the normal case.
	out := api.ActOutcome{Succeeded: result.Done, Failed: result.Failed,
		Results: make([]api.ActResult, 0, len(result.Outcomes))}
	for _, outcome := range result.Outcomes {
		entry := api.ActResult{
			Target:  api.PlanTarget{Kind: req.Kind, Namespace: outcome.Namespace, Name: outcome.Name},
			OK:      !outcome.Failed,
			Message: outcome.Message,
		}
		if outcome.Failed {
			reason := bulkReason(outcome)
			entry.Reason = &reason
		}
		out.Results = append(out.Results, entry)
	}
	return out, nil
}

// bulkReason is the code a failed row of a bulk act carries. A request that
// never left the console is not a refusal (the code of a drain's unsent
// eviction); one sent and not answered is `timeout`: not known, and not safe to
// rerun without a look.
func bulkReason(outcome actions.BulkOutcome) api.Reason {
	switch outcome.Refusal {
	case edit.RefusedNotSent:
		return api.Because(api.CodeNotProbedYet, outcome.Message).Retry(true)
	case edit.RefusedUnanswered:
		return api.Because(api.CodeTimeout, outcome.Message)
	case edit.RefusedNotInProfile:
		return api.Because(api.CodeRBACNotInProfile, outcome.Message)
	}
	return api.Because(api.CodeVerbDenied, outcome.Message)
}

func (a apiActs) PreviewData(ctx context.Context, name string, req api.DataWrite) (api.PlanFacts, error) {
	if a.s.writer == nil {
		return api.PlanFacts{}, fmt.Errorf("%w: this build has no write path", api.ErrNotInProfile)
	}
	if len(req.Entries) != 1 {
		// One value per write, as internal/edit.SetKey sends: that key and the
		// precondition are the call's whole blast radius.
		return api.PlanFacts{}, fmt.Errorf("%w: one key per write", api.ErrInvalid)
	}
	entry := req.Entries[0]
	preview, err := a.s.writer.PreviewKey(ctx, edit.KeyChange{
		Target:          edit.Target{Cluster: name, Kind: req.Kind, Namespace: req.Namespace, Name: req.Name},
		Key:             entry.Key,
		Value:           entry.Value,
		ResourceVersion: req.ResourceVersion,
	})
	if err != nil {
		return api.PlanFacts{}, writeError(err)
	}
	facts := api.PlanFacts{
		Verb:    "write",
		Targets: []api.PlanTarget{{Kind: req.Kind, Namespace: req.Namespace, Name: req.Name, Note: entry.Key}},
		Count:   1,
		// The key, the act's whole blast radius: the sentence names the object, the
		// button the key that changes.
		Button:       "Write " + entry.Key,
		Destructive:  true,
		Confirmation: &api.Confirmation{Kind: "acknowledge"},
	}
	if !preview.Secret {
		facts.Diff = diffText(preview.Diff)
	}
	if preview.Unchanged {
		facts.Warnings = []string{"this value is byte-for-byte what is already there"}
	}
	return facts, nil
}

func (a apiActs) ApplyData(ctx context.Context, name string, req api.DataWrite) (api.ActOutcome, error) {
	if a.s.writer == nil {
		return api.ActOutcome{}, fmt.Errorf("%w: this build has no write path", api.ErrNotInProfile)
	}
	entry := req.Entries[0]
	applied, err := a.s.writer.SetKey(ctx, edit.KeyChange{
		Target:          edit.Target{Cluster: name, Kind: req.Kind, Namespace: req.Namespace, Name: req.Name},
		Key:             entry.Key,
		Value:           entry.Value,
		ResourceVersion: req.ResourceVersion,
	})
	if err != nil {
		return api.ActOutcome{}, writeError(err)
	}
	return api.ActOutcome{Succeeded: 1, Results: []api.ActResult{{
		Target:  api.PlanTarget{Kind: req.Kind, Namespace: req.Namespace, Name: req.Name},
		OK:      true,
		Message: "written at " + applied.ResourceVersion,
	}}}, nil
}

// PreviewManifest reads the kind out of the submitted document after parsing,
// never from a route or a sibling field: checking one kind and writing another
// is a full bypass that looks right in both halves. It passes no kind and lets
// internal/edit resolve it from the document.
func (a apiActs) PreviewManifest(ctx context.Context, name string, req api.ManifestWrite) (api.PlanFacts, error) {
	if a.s.writer == nil {
		return api.PlanFacts{}, fmt.Errorf("%w: this build has no write path", api.ErrNotInProfile)
	}
	target, err := manifestTarget(name, req.Document)
	if err != nil {
		return api.PlanFacts{}, err
	}
	preview, err := a.s.writer.PreviewManifest(ctx, edit.ManifestChange{
		Target: target, YAML: req.Document, ResourceVersion: req.ResourceVersion,
	})
	if err != nil {
		return api.PlanFacts{}, writeError(err)
	}
	return api.PlanFacts{
		Verb: "apply",
		Targets: []api.PlanTarget{{
			Kind: target.Kind, Namespace: target.Namespace, Name: target.Name, Note: preview.Validated,
		}},
		Count: 1,
		// Named through `api.KindWord`, as the sentence above the button is, from the
		// document's own `kind` ("ConfigMap").
		Button:      "Apply this " + api.KindWord(target.Kind, 1),
		Destructive: true,
		Diff:        diffText(preview.Diff),
		// Computed by `internal/edit` while producing the patch: a client re-deriving
		// them from the diff would be guessing.
		Fields:       preview.Fields,
		Request:      preview.Patch,
		Precondition: preview.ResourceVersion,
		Confirmation: &api.Confirmation{Kind: "acknowledge"},
	}, nil
}

func (a apiActs) ApplyManifest(ctx context.Context, name string, req api.ManifestWrite) (api.ActOutcome, error) {
	if a.s.writer == nil {
		return api.ActOutcome{}, fmt.Errorf("%w: this build has no write path", api.ErrNotInProfile)
	}
	target, err := manifestTarget(name, req.Document)
	if err != nil {
		return api.ActOutcome{}, err
	}
	applied, err := a.s.writer.ApplyManifest(ctx, edit.ManifestChange{
		Target: target, YAML: req.Document, ResourceVersion: req.ResourceVersion,
	})
	if err != nil {
		return api.ActOutcome{}, writeError(err)
	}
	return api.ActOutcome{Succeeded: 1, Results: []api.ActResult{{
		Target:  api.PlanTarget{Kind: target.Kind, Namespace: target.Namespace, Name: target.Name},
		OK:      true,
		Message: "applied at " + applied.ResourceVersion,
	}}}, nil
}

func (a apiActs) PreviewNodeShell(ctx context.Context, name string, req api.NodeShellRequest) (api.PlanFacts, error) {
	if a.s.claude == nil {
		return api.PlanFacts{}, fmt.Errorf("%w: this build has no node shell", api.ErrNotInProfile)
	}
	return api.PlanFacts{
		Verb: "nodeshell",
		Targets: []api.PlanTarget{{
			Kind: "nodes", Name: req.Node,
			Note: "a debug pod running " + a.s.claude.NodeShellImage() + ", deleted when the socket closes",
		}},
		Count: 1,
		// The node, named: the one thing a person can get wrong here, carried on the plan
		// rather than read back from a query string (`Terminals.FramedNodeShell`).
		Button:       "Open a root shell on " + req.Node,
		Destructive:  true,
		Confirmation: &api.Confirmation{Kind: "acknowledge"},
		Warnings: []string{
			"this opens a root shell on the node with the host filesystem mounted",
		},
	}, nil
}

// manifestTarget reads the address out of the document, after parsing, from the
// document alone: ManifestWrite carries no kind, name or namespace, so what is
// parsed is what is authorized and what is sent.
func manifestTarget(cluster, document string) (edit.Target, error) {
	var head struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	if err := yaml.Unmarshal([]byte(document), &head); err != nil {
		return edit.Target{}, fmt.Errorf("%w: this document is not YAML: %s", api.ErrInvalid, firstLine(err.Error()))
	}
	if head.Kind == "" || head.Metadata.Name == "" {
		return edit.Target{}, fmt.Errorf("%w: this document carries no kind or no name, so there is nothing to address", api.ErrInvalid)
	}
	return edit.Target{
		Cluster:   cluster,
		Kind:      head.Kind,
		Namespace: head.Metadata.Namespace,
		Name:      head.Metadata.Name,
	}, nil
}

func diffText(lines []edit.DiffLine) string {
	if len(lines) == 0 {
		return ""
	}
	var out strings.Builder
	for _, line := range lines {
		out.WriteString(string(line.Mark))
		out.WriteString(line.Text)
		out.WriteByte('\n')
	}
	return out.String()
}

func actionTargets(in []api.PlanTarget) []actions.BulkTarget {
	out := make([]actions.BulkTarget, 0, len(in))
	for _, target := range in {
		out = append(out, actions.BulkTarget{Namespace: target.Namespace, Name: target.Name})
	}
	return out
}
