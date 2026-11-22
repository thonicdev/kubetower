package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A preference write that took effect and could not be written down has its own
// answer, and it is not the 400 a refusal is.
//
// `statefile.ErrNotSaved` - "in effect now, but it will not survive a restart" -
// must not reach the client as `400 invalid_request`, which says nothing
// happened. A client that believes it stops: `RailArrange.tsx`'s swap sends the
// first of its two writes, reads the 400 and never sends the second, leaving half
// a swap applied in memory. The answer is a 500 with its own code, `not_saved`:
// the console did fail at something - keeping the change - and a client branches
// on `code` to learn that the change itself is in effect.
func TestAWriteAppliedButNotSavedHasItsOwnCode(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   ProblemCode
		title  string
	}{
		{fmt.Errorf("%w: in effect now, but it will not survive a restart: writing hotbars.json", ErrNotSaved),
			http.StatusInternalServerError, ProblemNotSaved, "Applied, not saved"},
		// The refusal it must not be confused with keeps its answer.
		{fmt.Errorf("%w: there are 4 hotbars", ErrInvalid), http.StatusBadRequest, ProblemInvalidRequest, "Bad request"},
	} {
		rec := httptest.NewRecorder()
		fail(rec, tc.err)
		var problem Problem
		if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
			t.Fatalf("the body is not a problem: %s", rec.Body.String())
		}
		if rec.Code != tc.status || problem.Code != tc.code || problem.Title != tc.title {
			t.Errorf("%v answered %d %+v, want %d %s %q", tc.err, rec.Code, problem, tc.status, tc.code, tc.title)
		}
	}
}

// unsavedPrefs applies every write and keeps none of them.
type unsavedPrefs struct{ fakePrefs }

func (unsavedPrefs) SetRailMove(string, int) error {
	return fmt.Errorf("%w: in effect now, but it will not survive a restart", ErrNotSaved)
}

// extensionAnswer answers an extension write with whatever it is given.
type extensionAnswer struct {
	fakeClusters
	err error
}

func (c extensionAnswer) SetExtension(context.Context, string, string, bool) error { return c.err }

// Through the routes, so the shared tail of the preference writes and the
// extension write both reach the new answer, and an extension this build does
// not carry is the contract's 404 rather than a 400.
func TestThePreferenceRoutesAnswerNotSavedAndNotFound(t *testing.T) {
	send := func(t *testing.T, deps Deps, method, path, body string) (int, Problem) {
		t.Helper()
		deps.Sessions, deps.Profile = fakeSessions{}, fakeProfile{}
		deps.AllowOrigin = func(*http.Request, string) bool { return true }
		mux := http.NewServeMux()
		New(deps).Mount(mux)
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-KubeTower-Request", "1")
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		var problem Problem
		_ = json.Unmarshal(recorder.Body.Bytes(), &problem)
		return recorder.Code, problem
	}

	status, problem := send(t, Deps{Prefs: unsavedPrefs{}}, http.MethodPost,
		"/api/preferences/rail/move", `{"cluster":"prod","order":1}`)
	if status != http.StatusInternalServerError || problem.Code != ProblemNotSaved {
		t.Errorf("an unsaved rail move answered %d %s, want 500 %s", status, problem.Code, ProblemNotSaved)
	}

	status, problem = send(t,
		Deps{Clusters: extensionAnswer{err: fmt.Errorf("%w: in effect now, but it will not survive a restart", ErrNotSaved)}},
		http.MethodPut, "/api/c/"+testCluster+"/extensions/alfa", `{"enabled":true}`)
	if status != http.StatusInternalServerError || problem.Code != ProblemNotSaved {
		t.Errorf("an unsaved extension write answered %d %s, want 500 %s", status, problem.Code, ProblemNotSaved)
	}

	status, problem = send(t,
		Deps{Clusters: extensionAnswer{err: fmt.Errorf("%w: no such extra: \"zulu\"", ErrNotFound)}},
		http.MethodPut, "/api/c/"+testCluster+"/extensions/zulu", `{"enabled":true}`)
	if status != http.StatusNotFound || problem.Code != ProblemNotFound {
		t.Errorf("an unknown extension answered %d %s, want 404 %s", status, problem.Code, ProblemNotFound)
	}
}
