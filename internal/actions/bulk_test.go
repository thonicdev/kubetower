package actions

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/edit"
	"github.com/thonicdev/kubetower/internal/identity"
)

// One verb over a set, and every one of these runs against a fake clientset.
// The rule at the top of actions_test.go applies here with more force, not less:
// a mistake in this file is a mistake N times over.

func targets(pairs ...string) []BulkTarget {
	out := make([]BulkTarget, 0, len(pairs))
	for _, pair := range pairs {
		namespace, name, _ := strings.Cut(pair, "/")
		out = append(out, BulkTarget{Namespace: namespace, Name: name})
	}
	return out
}

// The guardrail, over a set. Same claim as the single-object test and the same
// standard of proof: not "an error came back" but "nothing left the process".
//
// Deliberately well-formed — the count typed correctly, every object real — so
// the only reason it can fail is KT_READONLY_CLUSTERS.
func TestABulkRunOnAReadOnlyClusterSendsNothing(t *testing.T) {
	for _, verb := range BulkVerbs() {
		t.Run(string(verb), func(t *testing.T) {
			h := newHarness(t, func(string) bool { return true },
				pod("prod", "api-1"), pod("prod", "api-2"), node("node-a"))

			targets := targets("prod/api-1", "prod/api-2")
			_, err := h.runner.RunBulk(context.Background(), BulkRequest{
				Cluster: "prod", Kind: "pods", Verb: verb,
				Targets: targets, Confirm: BulkConfirm(verb, "pods", len(targets)),
			})
			if err == nil {
				t.Fatal("a read-only cluster accepted a bulk run")
			}
			refusal, ok := edit.AsRefusal(err)
			if !ok || refusal.Kind != edit.RefusedReadOnly {
				t.Fatalf("refused as %v, want the read-only guardrail", err)
			}
			h.noWrites()
		})
	}
}

// The confirmation is not a decoration on the form. A run whose typed size does
// not match the selection posted is refused by this package, whatever the page
// did or did not render — and refused before anything is sent.
func TestABulkDeleteWithoutTheTypedCountSendsNothing(t *testing.T) {
	for _, confirm := range []string{"", "confirm", "api-1", "1", "3"} {
		t.Run("confirm="+confirm, func(t *testing.T) {
			h := newHarness(t, func(string) bool { return false },
				pod("prod", "api-1"), pod("prod", "api-2"))

			_, err := h.runner.RunBulk(context.Background(), BulkRequest{
				Cluster: "prod", Kind: "pods", Verb: Delete,
				Targets: targets("prod/api-1", "prod/api-2"),
				Confirm: confirm,
			})
			if err == nil {
				t.Fatal("a bulk delete went through without the count typed")
			}
			h.noWrites()
		})
	}
}

// And the correctly confirmed one, so the two tests above are known to fail for
// the reason they claim rather than because the whole path is broken.
func TestAConfirmedBulkDeleteReachesEveryObjectAndNoOther(t *testing.T) {
	h := newHarness(t, func(string) bool { return false },
		pod("prod", "api-1"), pod("prod", "api-2"), pod("prod", "api-3"))

	selection := targets("prod/api-1", "prod/api-3")
	result, err := h.runner.RunBulk(context.Background(), BulkRequest{
		Cluster: "prod", Kind: "pods", Verb: Delete,
		Targets: selection, Confirm: BulkConfirm(Delete, "pods", len(selection)),
	})
	if err != nil {
		t.Fatalf("a confirmed bulk delete was refused: %v", err)
	}
	if result.Done != 2 || result.Failed != 0 {
		t.Fatalf("result = %d done, %d failed: %+v", result.Done, result.Failed, result.Outcomes)
	}

	var deleted []string
	for _, write := range h.writes() {
		if write.Verb != "delete" {
			continue
		}
		deleted = append(deleted, write.Name)
	}
	if len(deleted) != 2 {
		t.Fatalf("sent %d deletes, want 2: %v", len(deleted), deleted)
	}
	for _, name := range deleted {
		if name == "api-2" {
			t.Fatal("api-2 was deleted and was never selected. A selection is the set of " +
				"identities that were ticked, and nothing else may be reached from it")
		}
	}
}

// **The property this whole feature stands on.**
//
// A selection is captured when the operator ticks it. It is *not* a query that
// is answered again when the button is pressed — if it were, an object created
// between the tick and the click would be deleted without ever having been on
// screen, and its deletion would be reported as though it had been chosen.
//
// Written as: an object appears in the cluster after the selection is taken, and
// the run must not touch it. And it is not enough to check what was deleted —
// this also asserts the run never *listed* anything, because a re-run filter
// would have to start with a list and an implementation that added one would
// pass the first half of this test and fail it the first time the timing worked
// out.
func TestASelectionIsNotRerunWhenTheButtonIsPressed(t *testing.T) {
	h := newHarness(t, func(string) bool { return false },
		pod("prod", "api-1"), pod("prod", "api-2"))

	// The operator ticked these two, and only these two.
	selection := targets("prod/api-1", "prod/api-2")

	// A third pod arrives — the replacement a controller made, a new deploy, a
	// job that started. It matches every filter the first two did.
	if err := h.dyn.Tracker().Add(pod("prod", "api-3")); err != nil {
		t.Fatalf("seeding the late arrival: %v", err)
	}

	if _, err := h.runner.RunBulk(context.Background(), BulkRequest{
		Cluster: "prod", Kind: "pods", Verb: Delete,
		Targets: selection, Confirm: BulkConfirm(Delete, "pods", len(selection)),
	}); err != nil {
		t.Fatalf("the run was refused: %v", err)
	}

	for _, write := range h.writes() {
		if write.Name == "api-3" {
			t.Fatal("a pod that was created after the selection was taken was acted on. " +
				"The selection must be the identities that were ticked, never a query " +
				"answered again at execution time")
		}
	}
	for _, action := range h.dyn.Actions() {
		if action.GetVerb() == "list" || action.GetVerb() == "watch" {
			t.Fatalf("the run issued a %s. A bulk run that lists is a bulk run that can "+
				"act on something nobody selected", action.GetVerb())
		}
	}
}

// Partial failure is the normal case, and the report has to be per object.
//
// Two pods exist, a third does not. One "done" over that is a lie about the
// third and one error over it throws away the two that worked.
func TestPartialFailureIsReportedObjectByObject(t *testing.T) {
	h := newHarness(t, func(string) bool { return false },
		pod("prod", "api-1"), pod("prod", "api-2"))

	selection := targets("prod/api-1", "prod/ghost", "prod/api-2")
	result, err := h.runner.RunBulk(context.Background(), BulkRequest{
		Cluster: "prod", Kind: "pods", Verb: Delete,
		Targets: selection, Confirm: BulkConfirm(Delete, "pods", len(selection)),
	})
	if err != nil {
		t.Fatalf("a partial failure came back as an error, losing the two that worked: %v", err)
	}
	if result.Done != 2 || result.Failed != 1 {
		t.Fatalf("result = %d done, %d failed, want 2 and 1: %+v",
			result.Done, result.Failed, result.Outcomes)
	}
	if len(result.Outcomes) != len(selection) {
		t.Fatalf("%d outcomes for %d objects — every object has to be accounted for",
			len(result.Outcomes), len(selection))
	}
	for i, outcome := range result.Outcomes {
		// Position preserved, so an outcome cannot be read against the wrong
		// object. The one that fails is the middle one deliberately.
		if outcome.Name != selection[i].Name {
			t.Fatalf("outcome %d is about %q, want %q", i, outcome.Name, selection[i].Name)
		}
		if outcome.Message == "" {
			t.Errorf("%s has no sentence on it; a blank cell says nothing happened", outcome.Name)
		}
	}
	if !result.Outcomes[1].Failed {
		t.Error("the object that does not exist was reported as deleted")
	}
	if strings.Contains(strings.ToLower(result.Summary), "partly") == false {
		t.Errorf("the summary over a partial failure is %q — it must not read as a clean "+
			"success", result.Summary)
	}
}

// countingResolver counts how many objects are being worked on at once.
//
// It stands where it does for a reason worth writing down: the fake dynamic
// client cannot be used to measure this. `testing.Fake.Invokes` holds one lock
// across the whole reaction chain, so every request through it is serialized
// whatever the caller does — a reactor that timed itself would report a ceiling
// of one for an unbounded fan-out and the test would pass by accident.
//
// `Locate` is ordinary Go, runs per object inside Run, and is outside that lock.
// It is therefore the one place the parallelism is actually visible.
type countingResolver struct {
	inner   fakeResolver
	mu      sync.Mutex
	live    int
	highest int
}

func (c *countingResolver) Locate(ctx context.Context, cluster, kind string) (edit.Location, error) {
	c.mu.Lock()
	c.live++
	if c.live > c.highest {
		c.highest = c.live
	}
	c.mu.Unlock()
	// Long enough that a run with no ceiling piles every object up here at once,
	// short enough that the test is a fraction of a second.
	time.Sleep(15 * time.Millisecond)
	c.mu.Lock()
	c.live--
	c.mu.Unlock()
	return c.inner.Locate(ctx, cluster, kind)
}

func (c *countingResolver) worst() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.highest
}

// The fan-out is bounded.
//
// Fourteen concurrent `aws eks get-token` contending on one credential cache is
// a stampede; a bulk delete over sixty pods fanning out unboundedly is
// the same stampede aimed at one API server, and at one credential cache again
// on an EKS context.
func TestABulkRunNeverHasMoreThanItsShareInFlight(t *testing.T) {
	var (
		selection []BulkTarget
		seeds     []runtime.Object
	)
	for i := 0; i < 24; i++ {
		name := "api-" + string(rune('a'+i))
		selection = append(selection, BulkTarget{Namespace: "prod", Name: name})
		seeds = append(seeds, pod("prod", name))
	}

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{podGVR: "PodList"}, seeds...)
	resolver := &countingResolver{inner: fakeResolver{dyn: dyn, cluster: "prod"}}
	runner := New(Options{
		Writer: edit.New(edit.Options{Resolve: resolver, ReadOnly: func(string) bool { return false }, Access: accesstest.Allow()}),
		Now:    func() time.Time { return stamp },
	})

	result, err := runner.RunBulk(context.Background(), BulkRequest{
		Cluster: "prod", Kind: "pods", Verb: Delete,
		Targets: selection, Confirm: BulkConfirm(Delete, "pods", len(selection)),
	})
	if err != nil {
		t.Fatalf("the run was refused: %v", err)
	}
	if result.Done != len(selection) {
		t.Fatalf("%d of %d objects were acted on: %+v", result.Done, len(selection), result.Outcomes)
	}

	// The ceiling itself, as a number written here rather than as the constant
	// being checked. `worst <= BulkConcurrency` alone is a tautology waiting to
	// happen: raise the constant to 64 and the assertion raises with it, so the
	// test would pass over exactly the change it exists to stop. Eight is the
	// most a console should ever aim at one API server, and at one credential
	// cache behind it.
	const ceiling = 8
	if BulkConcurrency > ceiling || BulkConcurrency < 2 {
		t.Fatalf("BulkConcurrency is %d. Above %d is a stampede aimed at one API server — "+
			"fourteen concurrent `aws eks get-token` contending on one credential cache "+
			"is one — and below 2 is not concurrent at all",
			BulkConcurrency, ceiling)
	}
	worst := resolver.worst()
	if worst > BulkConcurrency {
		t.Fatalf("%d objects were in flight at once and the semaphore allows %d, so it is "+
			"not being honored", worst, BulkConcurrency)
	}
	if worst < 2 {
		t.Fatalf("only %d object was ever in flight — this test is not exercising the "+
			"ceiling it claims to check, so it would pass over a serial implementation "+
			"and over an unbounded one alike", worst)
	}
}

// The two confirmations are different questions, and the difference is what
// stops the bulk path becoming a way to delete one object without typing
// anything: the set is confirmed by its size, one object on its own terms.
func TestTheSetIsConfirmedBySizeAndTheObjectOnItsOwnTerms(t *testing.T) {
	if got := BulkConfirm(Delete, "pods", 12); got != "12" {
		t.Errorf("BulkConfirm(delete, pods, 12) = %q, want the count typed out", got)
	}
	if got := BulkConfirm(Evict, "pods", 12); got != ConfirmToken {
		t.Errorf("BulkConfirm(evict, pods, 12) = %q, want the ordinary token — evict is reversible "+
			"in the sense that matters: the API server refuses it rather than obeying it", got)
	}
	// The object's own confirmation is SingleConfirm's answer: the count for a
	// pod and the name for everything else typed. What
	// this asserts is the property that matters either way: it is **not** the
	// size of the set. A twelve-object run whose per-object confirmation was
	// "12" would be Run's check answering a question about the selection, and
	// Run would then accept "12" as a confirmation for any single object.
	// One delete is a button, so a delete's
	// per-object confirmation is the token - still not the set's size.
	if got := perObjectConfirm(Delete, "pods", "api-1"); got != ConfirmToken {
		t.Errorf("perObjectConfirm(delete, pods, api-1) = %q, want %q — one object, and never "+
			"the set's size", got, ConfirmToken)
	}
	if got := perObjectConfirm(Delete, "deployments", "api"); got != ConfirmToken {
		t.Errorf("perObjectConfirm(delete, deployments, api) = %q, want %q. "+
			"If this ever returns the set's size, Run's own check stops being a check", got, ConfirmToken)
	}
	// A pod's restart is a delete, so its per-object confirmation is a delete's.
	// A controller's restart keeps the token.
	if got := perObjectConfirm(Restart, "pods", "api-1"); got != "1" {
		t.Errorf("perObjectConfirm(restart, pods, api-1) = %q, want %q: restarting a pod "+
			"deletes it, so it is confirmed like a delete", got, "1")
	}
	if got := perObjectConfirm(Restart, "deployments", "api"); got != ConfirmToken {
		t.Errorf("perObjectConfirm(restart, deployments, api) = %q, want the token — a rollout "+
			"restart is an annotation write", got)
	}
}

// Drain, trigger and scale are not offered over a set, and each absence is a
// decision rather than an oversight. A bulk drain is a fan-out of fan-outs;
// trigger creates objects rather than acting on the ones selected; scale needs
// one number, and one number over a mixed selection is a value nobody chose.
func TestTheVerbsThatMakeNoSenseOverASetAreRefused(t *testing.T) {
	for _, verb := range []Verb{Drain, Trigger, Scale, Verb("nonsense")} {
		h := newHarness(t, func(string) bool { return false }, node("node-a"), pod("prod", "api-1"))
		_, err := h.runner.RunBulk(context.Background(), BulkRequest{
			Cluster: "prod", Kind: "nodes", Verb: verb,
			Targets: targets("/node-a"), Confirm: BulkConfirm(verb, "pods", 1),
		})
		if err == nil {
			t.Errorf("%s was accepted over a selection", verb)
		}
		h.noWrites()
	}
}

// An empty selection is refused rather than treated as everything. The two must
// never be the same answer: a cache that reports "no pods" for a cluster
// running 429 of them is that shape.
func TestAnEmptySelectionIsNotAShorthandForEverything(t *testing.T) {
	h := newHarness(t, func(string) bool { return false }, pod("prod", "api-1"))
	_, err := h.runner.RunBulk(context.Background(), BulkRequest{
		Cluster: "prod", Kind: "pods", Verb: Delete,
		Targets: nil, Confirm: BulkConfirm(Delete, "pods", 0),
	})
	if err == nil {
		t.Fatal("an empty selection was accepted")
	}
	h.noWrites()
}

// Restarting pods is deleting pods, and it is confirmed like deleting pods.
//
// A `Typed` that looked only at the verb would ask for a typed name for Delete
// on a Pod and take a click for Restart on the same Pod — while `restart()`
// routes both to `deleteObject`. In bulk that is the difference between typing
// "200" to remove two hundred pods and pressing one button to remove the same
// two hundred, with the kinder word on it.
func TestRestartingPodsIsConfirmedLikeDeletingThem(t *testing.T) {
	if !Typed(Restart, "pods") {
		t.Error("restarting a pod is a delete — restart() calls deleteObject and says so — " +
			"and it is being confirmed by a click while deleting the same pod is confirmed " +
			"by name")
	}
	if got := BulkConfirm(Restart, "pods", 200); got != "200" {
		t.Errorf("BulkConfirm(restart, pods, 200) = %q. Two hundred pods are about to be "+
			"deleted; the confirmation must be the one deleting them would need", got)
	}

	// And a rollout restart is not a delete, so it must not grow a typing step.
	// A Deployment's pods come back by construction; making an operator type
	// "12" to roll a deployment is the cost of getting this wrong the other way.
	for _, kind := range []string{"deployments", "statefulsets", "daemonsets"} {
		if Typed(Restart, kind) {
			t.Errorf("restarting %s asks for typing; a rollout restart is an annotation write "+
				"and the pods come back on their own", kind)
		}
		if got := BulkConfirm(Restart, kind, 12); got != ConfirmToken {
			t.Errorf("BulkConfirm(restart, %s, 12) = %q, want the ordinary token", kind, got)
		}
	}

	// An unknown kind is treated as the dangerous one. Restart is the only verb
	// whose destructiveness depends on its target, and the safe reading of "I do
	// not know what this is" is the careful one.
	if !Typed(Restart, "") {
		t.Error("an unknown kind takes the click-through path; the careful reading of not " +
			"knowing what something is must be the careful one")
	}
}

// And the server enforces it, not just the page that draws the box.
func TestABulkRestartOfPodsWithoutTheTypedCountSendsNothing(t *testing.T) {
	req := BulkRequest{
		Cluster: "prod", Kind: "pods", Verb: Restart,
		Targets: []BulkTarget{{Namespace: "shop", Name: "web-0"}, {Namespace: "shop", Name: "web-1"}},
		Confirm: ConfirmToken,
	}
	if err := checkBulkConfirm(req); err == nil {
		t.Fatal("a bulk restart of two pods was accepted on the generic token. That is two " +
			"deletions behind one click")
	}

	req.Confirm = "2"
	if err := checkBulkConfirm(req); err != nil {
		t.Errorf("the typed count was refused: %v", err)
	}
}

// --- a crash on one row ------------------------------------------------------

// crashingResolver hands back a Location with a nil Dynamic and no error at all.
//
// This is not a contrived double: `edit.prepare` does `where.Dynamic.Resource(...)` with no nil check, so
// any Resolver that answers "yes, that kind exists" without a client behind it
// panics there — and on a bulk run that panic is on a goroutine this package
// started, which `net/http`'s recover cannot see.
type crashingResolver struct{}

func (crashingResolver) Locate(_ context.Context, cluster, _ string) (edit.Location, error) {
	return edit.Location{Cluster: cluster, Resource: podGVR, Kind: "Pod", Namespaced: true}, nil
}

// crashHarness is a Runner whose write path panics, with the log captured rather
// than sprayed over the test output — and captured so the stack can be asserted
// on, because a panic that is confined and never recorded is a defect that stops
// being reported.
func crashHarness(t *testing.T, resolve edit.Resolver) (*Runner, *bytes.Buffer) {
	t.Helper()
	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })

	writer := edit.New(edit.Options{
		Resolve:  resolve,
		Access:   accesstest.Allow(),
		ReadOnly: func(string) bool { return false },
		Logf:     func(string, ...any) {},
	})
	return New(Options{Writer: writer, Now: func() time.Time { return stamp }}), &logged
}

// **A panic inside a bulk goroutine ends the row, not the process.**
//
// A missing recover is not an error this test could catch by inspecting a
// return value: an unrecovered panic on a goroutine `net/http` did not spawn
// ends the process, so the test would not fail — the whole `go test` binary
// would die with `panic: runtime error: invalid memory address` and every other
// test in the package would go with it. That the run *returns at all* is half
// the assertion.
func TestAPanicOnOneObjectDoesNotTakeTheRunDown(t *testing.T) {
	runner, logged := crashHarness(t, crashingResolver{})

	selection := targets("prod/api-1", "prod/api-2", "prod/api-3")
	result, err := runner.RunBulk(context.Background(), BulkRequest{
		Cluster: "prod", Kind: "pods", Verb: Delete,
		Targets: selection, Confirm: BulkConfirm(Delete, "pods", len(selection)),
	})
	if err != nil {
		t.Fatalf("the run came back as an error rather than as outcomes: %v", err)
	}

	// The half that matters more than the crash. A recover returning the zero
	// BulkOutcome leaves Failed false, and RunBulk counts everything that is not
	// failed as done: the operator would read "3 objects deleted" over three rows
	// nothing was ever sent for.
	if result.Done != 0 || result.Failed != len(selection) {
		t.Fatalf("result = %d done, %d failed, want 0 and %d. A row that crashed and was "+
			"counted as done is worse than the crash: it is a delete reported as made",
			result.Done, result.Failed, len(selection))
	}
	if len(result.Outcomes) != len(selection) {
		t.Fatalf("%d outcomes for %d objects — a crashed row must not vanish",
			len(result.Outcomes), len(selection))
	}
	for i, outcome := range result.Outcomes {
		if outcome.Name != selection[i].Name || outcome.Namespace != selection[i].Namespace {
			t.Fatalf("outcome %d is about %s/%s, want %s/%s", i,
				outcome.Namespace, outcome.Name, selection[i].Namespace, selection[i].Name)
		}
		if !outcome.Failed {
			t.Errorf("%s crashed and is reported as done", outcome.Name)
		}
		if outcome.Message == "" {
			t.Errorf("%s has no sentence on it; a blank cell reads as nothing happened", outcome.Name)
		}
		// The sentence must not claim the object is untouched. A panic can land on
		// either side of the request, so what this row honestly knows is that
		// KubeTower failed — not that nothing was sent.
		for _, forbidden := range []string{"nothing was sent", "no request was made", "was not sent"} {
			if strings.Contains(strings.ToLower(outcome.Message), forbidden) {
				t.Errorf("%s claims %q about a crash that could have happened after the "+
					"request left: %s", outcome.Name, forbidden, outcome.Message)
			}
		}
		if outcome.Refusal == edit.RefusedInput {
			t.Errorf("%s is reported as an input refusal. The input was fine and the console "+
				"was not; a kind nobody set is what internal/web calls unclassified", outcome.Name)
		}
	}
	if got := strings.ToLower(result.Summary); !strings.Contains(got, "nothing was") {
		t.Errorf("the summary over three crashed rows is %q — it must not read as a success", result.Summary)
	}

	// Recorded, not swallowed.
	if text := logged.String(); !strings.Contains(text, "panicked") ||
		!strings.Contains(text, "oneConfined") {
		t.Errorf("the crash was confined but not logged with its stack:\n%s", text)
	}
}

// panicOnName is a reactor that crashes on exactly one object.
//
// The test above proves the process survives; this one proves the confinement is
// per *row* — the two objects beside the bad one are still acted on, and the run
// reports 2 done and 1 failed rather than giving up at the first crash.
func TestACrashOnOneRowLeavesTheRestOfTheSelectionActedOn(t *testing.T) {
	h := newHarness(t, func(string) bool { return false },
		pod("prod", "api-1"), pod("prod", "boom"), pod("prod", "api-2"))

	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })

	h.dyn.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if remove, ok := action.(k8stesting.DeleteActionImpl); ok && remove.Name == "boom" {
			panic("a client-go path that panics rather than returning an error")
		}
		return false, nil, nil
	})

	selection := targets("prod/api-1", "prod/boom", "prod/api-2")
	result, err := h.runner.RunBulk(context.Background(), BulkRequest{
		Cluster: "prod", Kind: "pods", Verb: Delete,
		Targets: selection, Confirm: BulkConfirm(Delete, "pods", len(selection)),
	})
	if err != nil {
		t.Fatalf("one crashed row turned the whole run into an error: %v", err)
	}
	if result.Done != 2 || result.Failed != 1 {
		t.Fatalf("result = %d done, %d failed, want 2 and 1: %+v",
			result.Done, result.Failed, result.Outcomes)
	}
	if !result.Outcomes[1].Failed {
		t.Error("the row that crashed was reported as deleted")
	}
	if result.Outcomes[0].Failed || result.Outcomes[2].Failed {
		t.Errorf("a crash on one object was charged to its neighbors: %+v", result.Outcomes)
	}
	if !strings.Contains(strings.ToLower(result.Summary), "partly") {
		t.Errorf("the summary is %q — one crashed row out of three is a partial failure",
			result.Summary)
	}
	if !strings.Contains(logged.String(), "prod/boom") {
		t.Errorf("the log line does not name the object that crashed:\n%s", logged.String())
	}
}

// TestABulkActByADeniedPersonLearnsNothingAboutTheObjects: reading each object
// with the console's rights before the write's review would let a person with
// no rights in a namespace bulk-delete guessed names and tell the ones that
// exist ("no longer exists") from the ones that do not, and a bulk pod restart
// would name the owner kind. Each row gets the same access refusal, and
// nothing is read.
func TestABulkActByADeniedPersonLearnsNothingAboutTheObjects(t *testing.T) {
	h := newHarness(t, func(string) bool { return false },
		pod("payments", "db-0", ownedBy("Job", "migrate")), pod("payments", "api-0"))
	h.reviews.Answer = func(accesstest.Question) (bool, error) { return false, nil }

	for _, verb := range []Verb{Delete, Restart} {
		selection := targets("payments/db-0", "payments/ghost", "payments/api-0")
		result, err := h.runner.RunBulk(context.Background(), BulkRequest{
			Cluster: "prod", Kind: "pods", Verb: verb,
			Targets: selection, Confirm: BulkConfirm(verb, "pods", len(selection)),
		})
		if err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
		first := result.Outcomes[0].Message
		for _, outcome := range result.Outcomes {
			if !outcome.Failed || outcome.Refusal != edit.RefusedByAccess {
				t.Errorf("%s %s: refused as %v %q, want the access refusal", verb, outcome.Name, outcome.Refusal, outcome.Message)
			}
			if strings.Contains(outcome.Message, "no longer exists") || strings.Contains(outcome.Message, "Job") {
				t.Errorf("%s %s: the row tells a denied person about the object: %q", verb, outcome.Name, outcome.Message)
			}
			if strings.ReplaceAll(outcome.Message, outcome.Name, "X") != strings.ReplaceAll(first, result.Outcomes[0].Name, "X") {
				t.Errorf("%s: the rows can be told apart: %q and %q", verb, outcome.Message, first)
			}
		}
	}
	for _, action := range h.dyn.Actions() {
		if action.GetVerb() == "get" {
			t.Fatalf("an object was read before the person was reviewed: %s %s", action.GetVerb(), action.GetResource().Resource)
		}
	}
	h.noWrites()
}

// A bulk run over RBAC kinds under the server profile: every row is refused as
// the profile's, the kind the single door answers `rbac_not_in_profile` for,
// not as a refusal by the person's rights.
func TestABulkRowOnAnRBACKindUnderTheServerProfileIsTheProfilesRefusal(t *testing.T) {
	rolesGVR := schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{rolesGVR: "RoleList"})
	reviews := &accesstest.Reviews{}
	runner := New(Options{
		Writer: edit.New(edit.Options{
			Resolve:  rolesOnly{dyn: dyn, gvr: rolesGVR},
			ReadOnly: func(string) bool { return false },
			Access:   access.New(access.Options{Reviews: reviews.For, InCluster: true}),
		}),
		Now: func() time.Time { return stamp },
	})
	ctx := identity.With(context.Background(), identity.Who{Name: "bob@example.com", Method: identity.SSO})
	result, err := runner.RunBulk(ctx, BulkRequest{
		Cluster: "prod", Kind: "roles", Verb: Delete,
		Targets: targets("shop/editor", "shop/viewer"), Confirm: BulkConfirm(Delete, "roles", 2),
	})
	if err != nil {
		t.Fatalf("the run was refused as a whole: %v", err)
	}
	if result.Failed != 2 {
		t.Fatalf("outcomes %+v", result.Outcomes)
	}
	for _, outcome := range result.Outcomes {
		if outcome.Refusal != edit.RefusedNotInProfile {
			t.Errorf("%s was refused as kind %v, want RefusedNotInProfile", outcome.Name, outcome.Refusal)
		}
	}
}

type rolesOnly struct {
	dyn dynamic.Interface
	gvr schema.GroupVersionResource
}

func (r rolesOnly) Locate(_ context.Context, cluster, _ string) (edit.Location, error) {
	return edit.Location{Cluster: cluster, Resource: r.gvr, Kind: "Role", Namespaced: true, Dynamic: r.dyn}, nil
}
