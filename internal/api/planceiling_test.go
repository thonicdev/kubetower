package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/sharedstate"
)

// The plan store's ceiling: a plan holds the request it was minted
// from for two minutes, in the store the sessions live in, and nothing bounded
// how many there were.

// clock is a time a test moves by hand.
type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

func person(name string) context.Context {
	return identity.With(context.Background(), identity.Who{Name: name, Method: identity.SSO})
}

func aPlan() *plan {
	return &plan{kind: planAction, cluster: "prod", facts: PlanFacts{Verb: "delete", Count: 1}}
}

// TestThePlanStoreRefusesPastItsCeiling: past the count, the store refuses with
// the contract's limit and stores nothing; a plan spent, or lapsed, gives its
// place back.
func TestThePlanStoreRefusesPastItsCeiling(t *testing.T) {
	c := &clock{at: time.Unix(1_800_000_000, 0)}
	kv := sharedstate.NewMemory(c.now)
	store := newPlanStore(c.now, kv)
	store.limits.plans = 3

	var tokens []string
	for i := 0; i < 3; i++ {
		token, err := store.issue(person("ada"), aPlan())
		if err != nil {
			t.Fatalf("plan %d under the ceiling was refused: %v", i+1, err)
		}
		tokens = append(tokens, token)
	}
	_, err := store.issue(person("ada"), aPlan())
	if !errors.Is(err, ErrLimitReached) || !strings.Contains(err.Error(), "its limit") {
		t.Fatalf("the plan past the ceiling answered %v, want the limit with a sentence", err)
	}
	if n := kv.Len(planPrefix); n != 3 {
		t.Errorf("the store holds %d plans after a refusal, want the 3 it accepted", n)
	}

	// Spent: its place comes back.
	if _, ok, err := store.take(context.Background(), tokens[0], planAction); err != nil || !ok {
		t.Fatalf("a held plan could not be spent: %v %v", ok, err)
	}
	if _, err := store.issue(person("ada"), aPlan()); err != nil {
		t.Errorf("a plan after one was spent was refused: %v", err)
	}

	// Lapsed: every place comes back, without anybody spending them.
	c.at = c.at.Add(planLife)
	for i := 0; i < 3; i++ {
		if _, err := store.issue(person("ada"), aPlan()); err != nil {
			t.Fatalf("plan %d after the others lapsed was refused: %v", i+1, err)
		}
	}
}

// The bytes are a ceiling too: a few large manifests weigh more than many
// small plans, and they are what fills the store.
func TestThePlanStoreRefusesPastItsBytes(t *testing.T) {
	c := &clock{at: time.Unix(1_800_000_000, 0)}
	store := newPlanStore(c.now, sharedstate.NewMemory(c.now))
	store.limits.bytes = 3 << 20

	large := func() *plan {
		return &plan{kind: planManifest, cluster: "prod", manifest: &ManifestWrite{Document: strings.Repeat("a", 1<<20)}}
	}
	for i := 0; i < 2; i++ {
		if _, err := store.issue(person("ada"), large()); err != nil {
			t.Fatalf("manifest %d under the ceiling was refused: %v", i+1, err)
		}
	}
	_, err := store.issue(person("ada"), large())
	if !errors.Is(err, ErrLimitReached) || !strings.Contains(err.Error(), "MiB") {
		t.Fatalf("a manifest past the bytes answered %v, want the limit naming the size", err)
	}
	// A small plan still fits beside them.
	if _, err := store.issue(person("ada"), aPlan()); err != nil {
		t.Errorf("a small plan beside the large ones was refused: %v", err)
	}
}

// One person at their share, another can still preview: the share is what
// keeps one browser in a loop from refusing everybody else. The shared password
// account has no share - its name is everybody who holds the password - and
// counts against the ceiling alone.
func TestOnePersonsShareOfThePlanStoreLeavesTheRest(t *testing.T) {
	c := &clock{at: time.Unix(1_800_000_000, 0)}
	store := newPlanStore(c.now, sharedstate.NewMemory(c.now)).shared()
	store.limits.perPersonPlans = 2

	for i := 0; i < 2; i++ {
		if _, err := store.issue(person("ada"), aPlan()); err != nil {
			t.Fatalf("ada's plan %d was refused: %v", i+1, err)
		}
	}
	_, err := store.issue(person("ada"), aPlan())
	if !errors.Is(err, ErrLimitReached) || !strings.Contains(err.Error(), "one person's share") {
		t.Fatalf("ada past her share answered %v, want her share named", err)
	}
	if _, err := store.issue(person("bob"), aPlan()); err != nil {
		t.Errorf("bob was refused while only ada held her share: %v", err)
	}

	team := identity.With(context.Background(), identity.Who{Name: "admin", Method: identity.Password})
	for i := 0; i < 4; i++ {
		if _, err := store.issue(team, aPlan()); err != nil {
			t.Fatalf("the shared password account's plan %d was refused by a share it does not have: %v", i+1, err)
		}
	}
}

// The desktop has no share: one person, and a share would only lower the
// ceiling they already have.
func TestTheDesktopHasNoShareOfThePlanStore(t *testing.T) {
	desktop := New(Deps{Clusters: fakeClusters{}, Acts: fakeActs{}})
	server := New(Deps{Clusters: fakeClusters{}, Acts: fakeActs{}, InCluster: true})
	if desktop.plans.limits.perPersonPlans != 0 {
		t.Errorf("the desktop has a share of %d plans", desktop.plans.limits.perPersonPlans)
	}
	if server.plans.limits.perPersonPlans == 0 || server.plans.limits.perPersonPlans >= server.plans.limits.plans {
		t.Errorf("the server profile's share is %d of %d plans", server.plans.limits.perPersonPlans,
			server.plans.limits.plans)
	}
}

// Through the router: a preview past the ceiling answers the contract's 409
// `limit_reached` with the sentence, never a 500 and never a 503 naming a store
// that answered; and the act, which spends a plan, still works.
func TestAPreviewPastTheCeilingIsLimitReached(t *testing.T) {
	a := New(Deps{Clusters: fakeClusters{}, Acts: fakeActs{}, AllowOrigin: func(*http.Request, string) bool { return true }})
	a.plans.limits.plans = 1
	mux := http.NewServeMux()
	a.Mount(mux)

	token := previewOn(t, mux)
	got := postTo(mux, "/api/c/prod/actions/preview",
		`{"verb":"delete","target":{"kind":"pods","namespace":"default","name":"api-1"}}`)
	var problem Problem
	_ = json.Unmarshal(got.Body.Bytes(), &problem)
	if got.Code != http.StatusConflict || problem.Code != ProblemLimitReached || !strings.Contains(problem.Detail, "1 confirmation is already waiting") {
		t.Fatalf("a preview past the ceiling answered %d %q %q, want 409 %q with the sentence", got.Code,
			problem.Code, problem.Detail, ProblemLimitReached)
	}
	if act := postTo(mux, "/api/c/prod/actions", `{"token":`+strconv.Quote(token)+`,"confirmed":"1"}`); act.Code != http.StatusOK {
		t.Fatalf("the plan held under the ceiling could not be confirmed: %d %s", act.Code, act.Body.String())
	}
	previewOn(t, mux) // its place came back
}

// Every preview can answer it, so every preview says so in the contract.
func TestEveryPreviewDeclaresTheLimitInTheContract(t *testing.T) {
	want := map[string]bool{"previewData": false, "previewManifest": false, "previewAction": false,
		"previewBulk": false, "previewNodeShell": false}
	paths, _ := loadSpec(t)["paths"].(map[string]any)
	for _, item := range paths {
		methods, _ := item.(map[string]any)
		for _, raw := range methods {
			op, _ := raw.(map[string]any)
			id, _ := op["operationId"].(string)
			if _, ok := want[id]; !ok {
				continue
			}
			responses, _ := op["responses"].(map[string]any)
			conflict, _ := responses["409"].(map[string]any)
			want[id] = conflict["$ref"] == "#/components/responses/LimitReached"
		}
	}
	for id, declared := range want {
		if !declared {
			t.Errorf("%s does not declare 409 LimitReached, which the plan store's ceiling answers", id)
		}
	}
}
