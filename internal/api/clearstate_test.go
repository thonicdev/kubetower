package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// clearingSettings is `fakeSettings` with a gesture worth checking and a record
// of whether the act half ran.
type clearingSettings struct {
	fakeSettings
	cleared int
}

func (c *clearingSettings) PreviewClear(scope string) (PlanFacts, error) {
	if scope != "archive" {
		return c.fakeSettings.PreviewClear(scope)
	}
	return PlanFacts{
		Verb:         "empty",
		Targets:      []PlanTarget{{Kind: "archive", Name: "this console's archive"}},
		Count:        1,
		Destructive:  true,
		Irreversible: boolPtr(true),
		Confirmation: &Confirmation{
			Kind: "count", Expected: "7",
			Label: "Type 7 - the number of days this archive holds - to empty it.",
		},
	}, nil
}

func (c *clearingSettings) Clear(scope string) (ActOutcome, error) {
	c.cleared++
	return c.fakeSettings.Clear(scope)
}

// The gesture on `clearState` is a lock and not a dialog.
//
// Every act in this contract runs `gestureSatisfied` before touching anything,
// and `clearState` must too: its plan carries a `Confirmation` that has to be
// compared with what the client sent.
//
// It is easy to miss because every scope but one asks for `acknowledge`, which
// is satisfied by anything at all - including a body with no `confirmed` in it.
// The archive scope asks the user to type how many days they are about to
// delete, and there an unchecked gesture is a destructive act with a dialog in
// front of it and no lock behind it: any client that skipped the box, or got
// the number wrong, would be obeyed.
func TestClearingRefusesAPlanWhoseGestureWasNotMade(t *testing.T) {
	settings := &clearingSettings{}
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: settings, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	post := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/settings/clear", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-KubeTower-Request", "1")
		mux.ServeHTTP(recorder, request)
		return recorder
	}

	// The plan, which changes nothing and carries the gesture.
	planned := post(`{"scope":"archive","preview":true}`)
	if planned.Code != http.StatusOK {
		t.Fatalf("the preview answered %d: %s", planned.Code, planned.Body.String())
	}
	var plan struct {
		Token string    `json:"token"`
		Facts PlanFacts `json:"facts"`
	}
	if err := json.Unmarshal(planned.Body.Bytes(), &plan); err != nil {
		t.Fatalf("the plan is not a plan: %v", err)
	}
	if plan.Token == "" || plan.Facts.Confirmation == nil || plan.Facts.Confirmation.Expected != "7" {
		t.Fatalf("the plan does not carry a checkable gesture: %+v", plan)
	}
	if settings.cleared != 0 {
		t.Fatal("the preview cleared something")
	}

	// The wrong number, which is the ordinary way this goes wrong: a second tab,
	// another day, an archive that grew between the dialog opening and the
	// button.
	wrong := post(`{"scope":"archive","preview":false,"token":"` + plan.Token + `","confirmed":"6"}`)
	if wrong.Code != http.StatusBadRequest {
		t.Fatalf("a wrong gesture answered %d: %s", wrong.Code, wrong.Body.String())
	}
	if settings.cleared != 0 {
		t.Fatalf("the archive was emptied %d times by a refused gesture", settings.cleared)
	}

	// **And the token is not spent by the refusal.** A refused gesture agreed to
	// nothing: the gesture is checked before the take, and the right number with
	// the same token does the act. An archive that grew in the meantime is still
	// caught: the act re-derives the plan and answers 409 with a fresh one when it
	// moved.
	right := post(`{"scope":"archive","preview":false,"token":"` + plan.Token + `","confirmed":"7"}`)
	if right.Code != http.StatusOK {
		t.Fatalf("the agreed gesture, after a refused one, answered %d: %s", right.Code, right.Body.String())
	}
	if settings.cleared != 1 {
		t.Fatalf("the archive was emptied %d times", settings.cleared)
	}

	// Single-use still: the act spent it.
	replayed := post(`{"scope":"archive","preview":false,"token":"` + plan.Token + `","confirmed":"7"}`)
	if replayed.Code != http.StatusConflict {
		t.Fatalf("a spent token answered %d: %s", replayed.Code, replayed.Body.String())
	}
	if settings.cleared != 1 {
		t.Fatalf("the archive was emptied %d times by one plan", settings.cleared)
	}
}
