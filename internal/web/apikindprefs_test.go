package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/kindprefs"
)

// The kinds column's choices, per person.

var (
	kindsAnna = identity.Who{Name: "anna", Method: identity.SSO}
	kindsBen  = identity.Who{Name: "ben", Method: identity.SSO}
)

func kindsAs(who identity.Who) context.Context {
	return identity.With(context.Background(), who)
}

// kindsThrough sends one request to the API's own route, as who.
func kindsThrough(t *testing.T, s *Server, who identity.Who, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "/api/me/kinds", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-KubeTower-Request", "1")
	request = request.WithContext(identity.With(request.Context(), who))
	rec := httptest.NewRecorder()
	api.New(api.Deps{KindPrefs: apiKindPrefs{s}}).ServeHTTP(rec, request)
	return rec
}

func TestEachPersonReadsTheirOwnKindPreferencesThroughTheRoute(t *testing.T) {
	s := &Server{kindPrefs: kindprefs.New(t.TempDir(), nil)}
	if rec := kindsThrough(t, s, kindsAnna, http.MethodPut, `{"showUnused":true,"favourites":{"prod":["pods"]}}`); rec.Code != http.StatusOK {
		t.Fatalf("anna's write: %d %s", rec.Code, rec.Body.String())
	}
	if rec := kindsThrough(t, s, kindsBen, http.MethodPut, `{"showUnused":false,"favourites":{"prod":["secrets"]}}`); rec.Code != http.StatusOK {
		t.Fatalf("ben's write: %d %s", rec.Code, rec.Body.String())
	}
	for _, tc := range []struct {
		who  identity.Who
		want string
	}{{kindsAnna, "true pods"}, {kindsBen, "false secrets"}} {
		who, want := tc.who, tc.want
		rec := kindsThrough(t, s, who, http.MethodGet, "")
		var got api.KindPreferences
		if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("%s's read: %d %s", who.Name, rec.Code, rec.Body.String())
		}
		if have := map[bool]string{true: "true", false: "false"}[got.ShowUnused] + " " + strings.Join(got.Favourites["prod"], ","); have != want {
			t.Errorf("%s reads %q, want %q", who.Name, have, want)
		}
	}
	if !(apiProfile{s}).Capabilities(nil).KindPreferences {
		t.Error("kindPreferences is false where they are kept")
	}
}

// Kept only where the extras' switch is: several replicas would each keep
// half, and a state directory that does not survive a restart would forget
// what the console said it kept. Refused as not this profile's, and the
// capability says so, so the browser keeps them.
func TestTheKindPreferencesAreNotKeptWhereTheyWouldNotLast(t *testing.T) {
	for name, s := range map[string]*Server{
		"no store":                 {},
		"several replicas":         {replicated: true},
		"in a cluster, no volume":  {inCluster: true, setup: SetupInfo{StateEphemeral: true}},
		"several in a cluster too": {inCluster: true, replicated: true},
	} {
		t.Run(name, func(t *testing.T) {
			if name != "no store" {
				s.kindPrefs = kindprefs.New(t.TempDir(), nil)
			}
			if (apiProfile{s}).Capabilities(nil).KindPreferences {
				t.Error("kindPreferences is true and nothing lasting keeps them")
			}
			store := apiKindPrefs{s}
			if _, err := store.KindPreferences(kindsAs(kindsAnna)); !errors.Is(err, api.ErrNotInProfile) {
				t.Errorf("the read answered %v", err)
			}
			if _, err := store.SetKindPreferences(kindsAs(kindsAnna), api.KindPreferences{}); !errors.Is(err, api.ErrNotInProfile) {
				t.Errorf("the write answered %v", err)
			}
			if rec := kindsThrough(t, s, kindsAnna, http.MethodGet, ""); rec.Code != http.StatusForbidden ||
				!strings.Contains(rec.Body.String(), string(api.ProblemNotInProfile)) {
				t.Errorf("the route answered %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestTheKindPreferencesAreKeptOnTheDesktopAndOnOneReplicaWithAVolume(t *testing.T) {
	for name, s := range map[string]*Server{
		"desktop":                   {},
		"one replica with a volume": {inCluster: true},
	} {
		t.Run(name, func(t *testing.T) {
			s.kindPrefs = kindprefs.New(t.TempDir(), nil)
			if !(apiProfile{s}).Capabilities(nil).KindPreferences {
				t.Error("kindPreferences is false")
			}
			if _, err := (apiKindPrefs{s}).SetKindPreferences(kindsAs(kindsAnna), api.KindPreferences{ShowUnused: true}); err != nil {
				t.Errorf("the write answered %v", err)
			}
		})
	}
}

func TestARequestCarryingNobodyIsRefused(t *testing.T) {
	s := &Server{kindPrefs: kindprefs.New(t.TempDir(), nil)}
	if _, err := (apiKindPrefs{s}).KindPreferences(context.Background()); !errors.Is(err, api.ErrRefused) {
		t.Errorf("the read answered %v, want ErrRefused", err)
	}
	if _, err := (apiKindPrefs{s}).SetKindPreferences(context.Background(), api.KindPreferences{}); !errors.Is(err, api.ErrRefused) {
		t.Errorf("the write answered %v, want ErrRefused", err)
	}
}

// A file that cannot be read is a 500, never an empty answer.
func TestAnUnreadableKindPreferencesFileIsNotAnEmptyAnswer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, kindprefs.FileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{kindPrefs: kindprefs.New(dir, nil)}
	if rec := kindsThrough(t, s, kindsAnna, http.MethodGet, ""); rec.Code != http.StatusInternalServerError {
		t.Errorf("the read answered %d %s, want 500", rec.Code, rec.Body.String())
	}
}
