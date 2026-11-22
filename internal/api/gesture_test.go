package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// The confirmation gesture.
//
// `gestureSatisfied` is the only thing between a plan and the act it authorizes,
// and its final statement must be a refusal. A kind the switch does not
// recognize must not be a *satisfied* kind — otherwise the way past every
// check in the function is to be asked for a gesture it has never heard of.
// The kind is not the user's to choose (the plan carries it, which is the
// point of carrying it), so an unrecognized one means the server asked for
// something it cannot check, and the only safe answer is no.

// gestureActs is a recording double, and it records the thing that matters:
// whether the act half was reached at all.
//
// Asserting on the status code alone would pass for a handler that ran the
// action and then answered 400, which is precisely the failure this file is
// about. fakeActs is embedded so only the two halves under test are written
// here and the rest of the interface stays in one place.
type gestureActs struct {
	fakeActs
	confirmation *Confirmation
	ran          int
	// confirmed is what reached the backend. For a `number` gesture that string
	// is the act itself, so "the handler checked it" and "the handler passed it
	// on" are two different properties and both are asserted.
	confirmed string
}

func (g *gestureActs) PreviewAction(_ context.Context, _ string, req ActionRequest) (PlanFacts, error) {
	return PlanFacts{
		Verb: req.Verb, Targets: []PlanTarget{req.Target}, Count: 1,
		Confirmation: g.confirmation,
	}, nil
}

func (g *gestureActs) RunAction(_ context.Context, _ string, req ActionRequest, confirmed string) (ActOutcome, error) {
	g.ran++
	g.confirmed = confirmed
	return ActOutcome{Succeeded: 1, Results: []ActResult{{Target: req.Target, OK: true}}}, nil
}

// runGesture previews, then spends the token with what the operator typed, and
// answers with the act's status and how many times the act half was reached.
func runGesture(t *testing.T, confirmation *Confirmation, confirmed string) (int, int) {
	t.Helper()
	status, ran, _ := runGestureAgainst(t, confirmation, confirmed)
	return status, ran
}

// runGestureAgainst also answers what reached the backend, which is a different
// question from whether the act ran: a `number` gesture's typed value **is** the
// act, and a handler that checked it and then dropped it on the way through
// would satisfy every assertion in this file but one.
func runGestureAgainst(t *testing.T, confirmation *Confirmation, confirmed string) (int, int, string) {
	t.Helper()
	acts := &gestureActs{confirmation: confirmation}
	mux := http.NewServeMux()
	New(Deps{Clusters: fakeClusters{}, Acts: acts,
		AllowOrigin: func(*http.Request, string) bool { return true }}).Mount(mux)

	post := func(path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("X-KubeTower-Request", "1")
		mux.ServeHTTP(recorder, request)
		return recorder
	}

	preview := post("/api/c/prod/actions/preview",
		`{"verb":"scale","target":{"kind":"deployments","namespace":"prod","name":"api"}}`)
	if preview.Code != http.StatusOK {
		t.Fatalf("the preview answered %d: %s", preview.Code, preview.Body.String())
	}
	var plan struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if acts.ran != 0 {
		t.Fatalf("the preview half reached the act half %d time(s)", acts.ran)
	}

	act := post("/api/c/prod/actions",
		`{"token":`+strconv.Quote(plan.Token)+`,"confirmed":`+strconv.Quote(confirmed)+`}`)
	return act.Code, acts.ran, acts.confirmed
}

// TestAnUnrecognisedGestureIsRefusedAndNothingIsRun pins the refusal.
//
// It goes through the handler rather than calling gestureSatisfied directly,
// because the claim is not "the function returns false" — it is "the act never
// happened". A guard that returns false into a caller which acts anyway is not a
// guard, and only the recorder can tell the two apart.
func TestAnUnrecognisedGestureIsRefusedAndNothingIsRun(t *testing.T) {
	for _, kind := range []string{"sideways", "", "Acknowledge", "confirm", "number "} {
		t.Run(strconv.Quote(kind), func(t *testing.T) {
			status, ran := runGesture(t, &Confirmation{Kind: kind}, "whatever")
			if status != http.StatusBadRequest {
				t.Errorf("a %q gesture answered %d, expected 400", kind, status)
			}
			if ran != 0 {
				t.Fatalf("a %q gesture reached the act half %d time(s) — it was refused "+
					"after the cluster had already been written to", kind, ran)
			}
		})
	}
}

// The three kinds that are recognized still behave, or the refusal above is a
// blanket refusal wearing a guard's clothes.
func TestTheRecognisedGesturesStillPass(t *testing.T) {
	cases := []struct {
		what         string
		confirmation *Confirmation
		confirmed    string
		status       int
		ran          int
	}{
		{"acknowledge takes anything", &Confirmation{Kind: "acknowledge"}, "", http.StatusOK, 1},
		{"a matching count", &Confirmation{Kind: "count", Expected: "3"}, "3", http.StatusOK, 1},
		{"a count that does not match", &Confirmation{Kind: "count", Expected: "3"}, "4", http.StatusBadRequest, 0},
		{"a matching name", &Confirmation{Kind: "name", Expected: "api"}, "api", http.StatusOK, 1},
		{"a name that does not match", &Confirmation{Kind: "name", Expected: "api"}, "apu", http.StatusBadRequest, 0},
		// No confirmation at all is a plan that asks for no gesture, which is a
		// different thing from one asking for a gesture nobody can check.
		{"no confirmation asked for", nil, "", http.StatusOK, 1},
	}
	for _, one := range cases {
		t.Run(one.what, func(t *testing.T) {
			status, ran := runGesture(t, one.confirmation, one.confirmed)
			if status != one.status {
				t.Errorf("answered %d, expected %d", status, one.status)
			}
			if ran != one.ran {
				t.Errorf("the act half ran %d time(s), expected %d", ran, one.ran)
			}
		})
	}
}

// TestTheNumberGestureIsCheckedAsANumber covers the fourth kind the contract
// declares.
//
// `number` is the gesture a scale needs: what is typed is the value the act
// uses, not a token proving intent. So an empty box is a refusal rather than a
// default — a replica count nobody stated is not a replica count, which is the
// same distinction `actions.Request.Replicas` is a pointer for.
func TestTheNumberGestureIsCheckedAsANumber(t *testing.T) {
	zero, three := int64(0), int64(3)
	cases := []struct {
		what         string
		confirmation *Confirmation
		confirmed    string
		status       int
		ran          int
	}{
		{"nothing typed is not zero", &Confirmation{Kind: "number"}, "", http.StatusBadRequest, 0},
		{"blank is not zero either", &Confirmation{Kind: "number"}, "   ", http.StatusBadRequest, 0},
		{"a word is not a number", &Confirmation{Kind: "number"}, "none", http.StatusBadRequest, 0},
		{"a fraction is not a whole number", &Confirmation{Kind: "number"}, "1.5", http.StatusBadRequest, 0},
		{"a stated zero is accepted", &Confirmation{Kind: "number", Minimum: &zero}, "0", http.StatusOK, 1},
		{"a stated number is accepted", &Confirmation{Kind: "number"}, "3", http.StatusOK, 1},
		{"padding is tolerated", &Confirmation{Kind: "number"}, " 3 ", http.StatusOK, 1},
		{"below the floor", &Confirmation{Kind: "number", Minimum: &zero}, "-1", http.StatusBadRequest, 0},
		{"above the ceiling", &Confirmation{Kind: "number", Maximum: &three}, "4", http.StatusBadRequest, 0},
		{"on the ceiling", &Confirmation{Kind: "number", Maximum: &three}, "3", http.StatusOK, 1},
	}
	for _, one := range cases {
		t.Run(one.what, func(t *testing.T) {
			status, ran := runGesture(t, one.confirmation, one.confirmed)
			if status != one.status {
				t.Errorf("answered %d, expected %d", status, one.status)
			}
			if ran != one.ran {
				t.Errorf("the act half ran %d time(s), expected %d", ran, one.ran)
			}
		})
	}
}

// The replica count reaches the act half, and it is the one the operator typed.
//
// # Why this is its own test
//
// Every other gesture in this contract is a **proof**: the operator types a name
// or a count, this package compares it to the plan, and the act half never needs
// the string again. `number` is the exception the contract states in one line -
// *what the operator typed is the value the act will use, not a token proving
// they meant it* - and Scale has nowhere else for that value to come from: it is
// typed into the dialog, after the plan was minted, so it is not in the
// `ActionRequest` the plan was built from.
//
// The act half must be reached with it. `internal/actions`
// refuses a scale whose `Replicas` is nil - deliberately, because a plain int
// there would read a request that never mentioned replicas as a request to scale
// to nothing - so a handler that dropped the typed value would make the whole
// verb unreachable rather than wrong.
func TestTheReplicaCountReachesTheActHalf(t *testing.T) {
	status, ran, confirmed := runGestureAgainst(t,
		&Confirmation{Kind: "number", Minimum: intPointer64(0)}, "12")

	if status != http.StatusOK {
		t.Fatalf("the act answered %d", status)
	}
	if ran != 1 {
		t.Fatalf("the act half ran %d time(s)", ran)
	}
	if confirmed != "12" {
		t.Errorf("the backend was handed %q; a scale confirmed as 12 that patches anything else "+
			"is a confirmation showing one thing and sending another", confirmed)
	}
}

// And a `number` below its own minimum is refused before anything runs. The
// bound is checked here as well as wherever the act re-checks it: this one gives
// the operator the sentence, the other is the guarantee.
func TestANumberOutsideItsBoundsIsRefused(t *testing.T) {
	status, ran := runGesture(t, &Confirmation{Kind: "number", Minimum: intPointer64(0)}, "-1")
	if status != http.StatusBadRequest || ran != 0 {
		t.Fatalf("status=%d ran=%d; a negative replica count reached the cluster", status, ran)
	}
}

func intPointer64(v int64) *int64 { return &v }
