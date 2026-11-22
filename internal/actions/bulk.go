package actions

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/thonicdev/kubetower/internal/edit"
	"github.com/thonicdev/kubetower/textutil"
)

// One verb applied to a set of objects the operator ticked.
//
//   - A selection is a list of identities, never a query: RunBulk reaches
//     exactly the namespace/name pairs it is handed and never re-runs a
//     filter, which would act on an object nobody had seen.
//   - Every object goes through Runner.Run (Locate and checkConfirm), one at a
//     time. There is no second write path: KT_READONLY_CLUSTERS is enforced by
//     that Locate and by nothing in this file.
//   - Partial failure is normal (three deletes succeed, a PodDisruptionBudget
//     refuses the fourth), so RunBulk returns a Result per object and no
//     error: "done" over a partial failure misleads, and an error over three
//     successes throws away what happened.

// BulkConcurrency is how many objects are acted on at once.
//
// A deliberate ceiling: an unbounded fan-out over sixty pods is a stampede
// on one API server. Four keeps fifty objects to a few seconds without
// fifty requests in flight.
const BulkConcurrency = 4

// BulkTarget is one object in a selection, exactly as ticked. Kind and
// cluster are on the request: a selection is always within one table.
type BulkTarget struct {
	Namespace string
	Name      string
}

// BulkRequest is one verb over a captured set.
type BulkRequest struct {
	Cluster string
	Kind    string
	Verb    Verb
	// Targets is the set, in report order. Empty is refused rather than
	// read as "everything".
	Targets []BulkTarget
	// Confirm is BulkConfirm's answer: the number of objects typed out for
	// the irreversible verbs, the token for the rest.
	Confirm string
}

// BulkOutcome is what happened to one object, in the words the panel prints.
type BulkOutcome struct {
	Namespace string
	Name      string
	// Message is the sentence: what was done, or why it was not.
	Message string
	Failed  bool
	// Refusal is the refusal's kind, zero for a success, so a caller can
	// tell "refused by a budget" from "no longer exists".
	Refusal edit.RefusalKind
}

// BulkResult is the whole run, with no error beside it (see the file
// comment). A run that could not start at all is RunBulk's error instead.
type BulkResult struct {
	Verb     Verb
	Outcomes []BulkOutcome
	Done     int
	Failed   int
	// Summary is the one line above the table: "3 deleted, 1 refused."
	Summary string
}

// BulkVerbs is the verbs that mean something over a set.
//
// Drain, Trigger and Scale are absent: a drain is already a multi-stage plan
// over one node's pods, Trigger creates objects rather than acting on the
// selection, and one Scale count over a mixed selection is a value nobody
// chose for any of them.
func BulkVerbs() []Verb {
	return []Verb{Delete, Restart, Evict, Cordon, Uncordon, Suspend, Resume}
}

// IsBulk reports whether this verb may be applied to a set.
func IsBulk(verb Verb) bool {
	for _, candidate := range BulkVerbs() {
		if candidate == verb {
			return true
		}
	}
	return false
}

// BulkConfirm is what a bulk run has to carry.
//
// A single delete is confirmed by typing the object's name, against the
// wrong-row mistake. Typing twelve names is an obstacle course answered by
// pasting, so a set types the number of objects: a fact read off the screen
// that fails when the selection is not the size the operator thinks, and
// that muscle memory cannot produce. Everything else keeps the token.
func BulkConfirm(verb Verb, kind string, count int) string {
	if Typed(verb, kind) {
		return strconv.Itoa(count)
	}
	return ConfirmToken
}

// checkBulkConfirm is the server-side half, which a hand-made POST meets.
func checkBulkConfirm(req BulkRequest) error {
	want := BulkConfirm(req.Verb, req.Kind, len(req.Targets))
	if strings.TrimSpace(req.Confirm) == want {
		return nil
	}
	if Typed(req.Verb, req.Kind) {
		return edit.Refuse(edit.RefusedInput,
			"this is not undoable, so it is confirmed by size: type %s — the number of objects "+
				"selected — to %s them", want, string(req.Verb))
	}
	return edit.Refuse(edit.RefusedInput,
		"this action was not confirmed. Open it from the selection bar, read what it will do, "+
			"and confirm there")
}

// RunBulk applies one verb to a captured set of objects.
//
// Resolve and apply the guardrail before asking for a confirmation, as Run
// does, so a read-only cluster never answers with "type 12 to confirm".
//
// An error means nothing was attempted; a BulkResult means every target was
// attempted.
func (r *Runner) RunBulk(ctx context.Context, req BulkRequest) (BulkResult, error) {
	if r.writer == nil {
		return BulkResult{}, edit.Refuse(edit.RefusedInput,
			"this build has no write path, so no action can be performed")
	}
	if !IsBulk(req.Verb) {
		return BulkResult{}, edit.Refuse(edit.RefusedInput,
			"%q is not an action this console performs over a selection", string(req.Verb))
	}
	if len(req.Targets) == 0 {
		return BulkResult{}, edit.Refuse(edit.RefusedInput,
			"nothing was selected. An empty selection is not a shorthand for everything")
	}
	for _, target := range req.Targets {
		if strings.TrimSpace(target.Name) == "" {
			return BulkResult{}, edit.Refuse(edit.RefusedInput,
				"one of the selected objects has no name")
		}
	}

	// One Locate settles KT_READONLY_CLUSTERS for the whole selection (same
	// kind and cluster) in one sentence. Run still locates every object;
	// this only refuses early and legibly.
	if _, err := r.writer.Locate(ctx, edit.Target{
		Cluster: req.Cluster, Kind: req.Kind,
		Namespace: req.Targets[0].Namespace, Name: req.Targets[0].Name,
	}); err != nil {
		return BulkResult{}, err
	}
	if err := checkBulkConfirm(req); err != nil {
		return BulkResult{}, err
	}

	outcomes := make([]BulkOutcome, len(req.Targets))
	var (
		wait   sync.WaitGroup
		slots  = make(chan struct{}, BulkConcurrency)
		cancel = ctx.Done()
	)
	for i, target := range req.Targets {
		select {
		case <-cancel:
			// The browser left or the deadline passed. What was sent was sent;
			// the rest is reported as not attempted.
			outcomes[i] = BulkOutcome{
				Namespace: target.Namespace, Name: target.Name, Failed: true,
				Message: "not attempted: the request was canceled before this object's turn",
				Refusal: edit.RefusedInput,
			}
			continue
		case slots <- struct{}{}:
		}

		wait.Add(1)
		go func(at int, target BulkTarget) {
			defer wait.Done()
			defer func() { <-slots }()
			outcomes[at] = r.oneConfined(ctx, req, target)
		}(i, target)
	}
	wait.Wait()

	result := BulkResult{Verb: req.Verb, Outcomes: outcomes}
	for _, outcome := range outcomes {
		if outcome.Failed {
			result.Failed++
			continue
		}
		result.Done++
	}
	result.Summary = bulkSummary(req.Verb, result.Done, result.Failed)
	return result, nil
}

// one is a single object's turn: Runner.Run and nothing else, whose
// checkConfirm runs on every object. It is given the object's own
// confirmation, never the set's size: conflating them would make the bulk
// path a way to act on one object without typing anything.
func (r *Runner) one(ctx context.Context, req BulkRequest, target BulkTarget) BulkOutcome {
	outcome := BulkOutcome{Namespace: target.Namespace, Name: target.Name}
	object := edit.Target{
		Cluster: req.Cluster, Kind: req.Kind,
		Namespace: target.Namespace, Name: target.Name,
	}
	// Reviewed before Run reads anything: Run reads the object with the
	// console's rights first (a missing object reads "no longer exists"), so
	// without this a person could act on guessed names and read an existence
	// oracle off the table. Asked first, they get one refusal per row.
	if err := r.reviewFirst(ctx, req.Verb, object); err != nil {
		outcome.Failed = true
		outcome.Message = refusalReason(err)
		outcome.Refusal = refusalKind(err)
		return outcome
	}
	result, err := r.Run(ctx, Request{
		Target:  object,
		Verb:    req.Verb,
		Confirm: perObjectConfirm(req.Verb, req.Kind, target.Name),
	})
	if err != nil {
		outcome.Failed = true
		outcome.Message = refusalReason(err)
		outcome.Refusal = refusalKind(err)
		return outcome
	}
	outcome.Message = result.Message
	return outcome
}

// reviewFirst asks the verb's questions (Needs) for one object before anything
// of it is read, and answers a refusal in this package's vocabulary.
func (r *Runner) reviewFirst(ctx context.Context, verb Verb, target edit.Target) error {
	if r.writer == nil {
		return edit.Refuse(edit.RefusedInput, "this build has no write path, so no action can be performed")
	}
	where, err := r.writer.Locate(ctx, target)
	if err != nil {
		return err
	}
	for _, question := range Needs(verb, where, target) {
		if err := edit.CheckRecorded(ctx, where, question, false); err != nil {
			return previewRefused(err)
		}
	}
	return nil
}

// oneConfined is `one` with a panic kept inside the row it happened on.
//
// net/http recovers a panic on its own handler goroutine, but the bulk
// fan-out runs on goroutines this file starts, where a panic ends the
// process: some deletes sent, the outcome table lost, every dock terminal
// and log stream killed. A Resolver returning a Location with a nil Dynamic
// is enough to cause one.
//
// The recovered row is a failed row: a zero BulkOutcome would count as done.
// Its sentence does not claim nothing happened, since a panic can land on
// either side of the request.
func (r *Runner) oneConfined(ctx context.Context, req BulkRequest, target BulkTarget) (outcome BulkOutcome) {
	defer func() {
		crash := recover()
		if crash == nil {
			return
		}
		// The stack goes to the log: the table is read by an operator, not by
		// whoever fixes this, and an unrecorded panic stops being reported.
		log.Printf("actions: bulk %s on %s panicked and was confined to that row: %v\n%s",
			req.Verb, targetLabel(req.Kind, target), crash, debug.Stack())

		outcome = BulkOutcome{
			Namespace: target.Namespace,
			Name:      target.Name,
			Failed:    true,
			Message: fmt.Sprintf("KubeTower itself failed on this object and the run carried on "+
				"without it: %s. Whether the %s was sent is not known here — the failure can "+
				"land on either side of the request — so look at this object before retrying. "+
				"The stack trace is in KubeTower's log.",
				textutil.FirstLineN(fmt.Sprint(crash), refusalLine), req.Verb),
			Refusal: refusedCrash,
		}
	}()
	return r.one(ctx, req, target)
}

// refusedCrash is the kind a crashed row carries, deliberately none of
// edit's: a panic is not a refusal, and every existing kind would state a
// cause this row does not have. internal/web names an unknown kind
// "unclassified". Negative, so a kind added to edit's iota cannot collide.
const refusedCrash = edit.RefusalKind(-1)

// targetLabel is one object for the log line, namespaced or not.
func targetLabel(kind string, target BulkTarget) string {
	if target.Namespace == "" {
		return kind + "/" + target.Name
	}
	return kind + " " + target.Namespace + "/" + target.Name
}

// perObjectConfirm is what Run wants for one object: SingleConfirm. One
// spelling only (pods are confirmed by count, and passing the name would
// refuse every row), and not BulkConfirm, which is the selection's size.
func perObjectConfirm(verb Verb, kind, name string) string {
	return SingleConfirm(verb, kind, name)
}

// refusalReason is the sentence an outcome carries when a write did not
// happen: this package's own refusal where there is one, and the API
// server's first line where there is not.
//
// One function, which the drain uses too.
func refusalReason(err error) string {
	if refusal, ok := edit.AsRefusal(err); ok {
		return refusal.Reason
	}
	return textutil.FirstLineN(err.Error(), refusalLine)
}

func refusalKind(err error) edit.RefusalKind {
	if refusal, ok := edit.AsRefusal(err); ok {
		return refusal.Kind
	}
	return edit.RefusedInput
}

// bulkSummary is the one line above the table.
//
// Both numbers whenever both are non-zero, never "done" over a partial
// failure.
func bulkSummary(verb Verb, done, failed int) string {
	switch {
	case failed == 0:
		return fmt.Sprintf("%s: %s.", upperFirst(pastTense(verb)), objects(done))
	case done == 0:
		return fmt.Sprintf("Nothing was %s: all %s were refused.", pastTense(verb), objects(failed))
	default:
		return fmt.Sprintf("Partly done: %s %s, %d refused. What was refused is listed below, "+
			"object by object.", objects(done), pastTense(verb), failed)
	}
}

// upperFirst rather than the deprecated strings.Title, which would also
// capitalize every word.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func objects(n int) string {
	if n == 1 {
		return "1 object"
	}
	return fmt.Sprintf("%d objects", n)
}

func pastTense(verb Verb) string {
	switch verb {
	case Delete:
		return "deleted"
	case Restart:
		return "restarted"
	case Evict:
		return "evicted"
	case Cordon:
		return "cordoned"
	case Uncordon:
		return "uncordoned"
	case Suspend:
		return "suspended"
	case Resume:
		return "resumed"
	}
	return string(verb) + "ed"
}

// SortTargets puts a selection into the order it will be reported: namespace,
// then name, rather than the browser's row order.
func SortTargets(targets []BulkTarget) {
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].Namespace != targets[j].Namespace {
			return targets[i].Namespace < targets[j].Namespace
		}
		return targets[i].Name < targets[j].Name
	})
}
