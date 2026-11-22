package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The kinds column's choices, per person: a body the
// contract refuses never reaches the store, and a store that keeps nothing is
// a refusal naming the profile, not a 500.

// keptKindPrefs records what it was handed.
type keptKindPrefs struct{ got *KindPreferences }

func (k keptKindPrefs) KindPreferences(context.Context) (KindPreferences, error) {
	return KindPreferences{}, nil
}

func (k keptKindPrefs) SetKindPreferences(_ context.Context, p KindPreferences) (KindPreferences, error) {
	*k.got = p
	return p, nil
}

// notKept is the store of a console that keeps none.
type notKept struct{}

func (notKept) KindPreferences(context.Context) (KindPreferences, error) {
	return KindPreferences{}, ErrNotInProfile
}

func (notKept) SetKindPreferences(context.Context, KindPreferences) (KindPreferences, error) {
	return KindPreferences{}, ErrNotInProfile
}

func sendKindPrefs(t *testing.T, store KindPreferenceStore, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	New(Deps{Sessions: fakeSessions{}, Profile: fakeProfile{}, KindPrefs: store,
		AllowOrigin: func(*http.Request, string) bool { return true }}).Mount(mux)
	request := httptest.NewRequest(method, "/api/me/kinds", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-KubeTower-Request", "1")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

func TestAKindPreferencesWriteTheContractRefusesNeverReachesTheStore(t *testing.T) {
	long := strings.Repeat("a", MaxKindSlug+1)
	for name, body := range map[string]string{
		"showUnused left out":      `{"favourites":{}}`,
		"an empty slug":            `{"showUnused":true,"favourites":{"prod":[""]}}`,
		"a slug past its length":   `{"showUnused":true,"favourites":{"prod":["` + long + `"]}}`,
		"an empty cluster":         `{"showUnused":true,"favourites":{"":["pods"]}}`,
		"a member nobody declared": `{"showUnused":true,"favourites":{},"columns":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			var got KindPreferences
			rec := sendKindPrefs(t, keptKindPrefs{&got}, http.MethodPut, body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("answered %d %s; want 400", rec.Code, rec.Body.String())
			}
			if got.Favourites != nil {
				t.Errorf("the store was handed %+v", got)
			}
		})
	}
}

func TestAKindPreferencesWriteIsKeptAndAnsweredWhole(t *testing.T) {
	var got KindPreferences
	rec := sendKindPrefs(t, keptKindPrefs{&got}, http.MethodPut, `{"showUnused":true,"favourites":{"prod":["pods"],"dev":null}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d %s", rec.Code, rec.Body.String())
	}
	if !got.ShowUnused || len(got.Favourites["prod"]) != 1 || got.Favourites["dev"] == nil {
		t.Errorf("the store was handed %+v; a null list is an empty one", got)
	}
	var answered KindPreferences
	if err := json.Unmarshal(rec.Body.Bytes(), &answered); err != nil || !answered.ShowUnused {
		t.Errorf("answered %s (%v)", rec.Body.String(), err)
	}
}

func TestAConsoleThatKeepsNoKindPreferencesSaysItIsNotThisProfiles(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		rec := sendKindPrefs(t, notKept{}, method, `{"showUnused":false,"favourites":{}}`)
		var problem Problem
		_ = json.Unmarshal(rec.Body.Bytes(), &problem)
		if rec.Code != http.StatusForbidden || problem.Code != ProblemNotInProfile {
			t.Errorf("%s answered %d %+v; want 403 %s", method, rec.Code, problem, ProblemNotInProfile)
		}
	}
}
