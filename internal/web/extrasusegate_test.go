package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/extras"
	"github.com/thonicdev/kubetower/internal/identity"
)

// Under the server profile an extra that is on for a cluster is reached, and
// drawn in the menu, only by a person holding the verb `use` on
// `extras.kubetower.dev/<key>`: the route is the enforcement, the menu the
// courtesy, and both ask the same question.

// extraRoutes is the fixture extension's three shapes of route on `prod`: a
// page, a sub-path, and the method-less route a socket uses.
func extraRoutes(key string) map[string]string {
	base := "/x/prod/" + key
	return map[string]string{"the page": base + "/", "the panel": base + "/panel", "the socket": base + "/socket"}
}

// turnedOn is a server-profile console on `prod` with keys named in the values
// and turned on there, the gate granting `use` on granted to alice alone, and
// the extension routes mounted.
func turnedOn(t *testing.T, keys []string, granted ...string) (*Server, *http.ServeMux, *accesstest.Reviews) {
	t.Helper()
	s, _, reviews := useServer(t, strings.Join(keys, ","), t.TempDir(), granted...)
	for _, key := range keys {
		if err := s.extras.SetOn("prod", key, true); err != nil {
			t.Fatalf("turning %s on: %v", key, err)
		}
	}
	mux := http.NewServeMux()
	s.mountExtensions(mux)
	return s, mux, reviews
}

func reach(mux *http.ServeMux, who identity.Who, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request = request.WithContext(identity.With(request.Context(), who))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, request)
	return rec
}

// problemCode is the `code` of a problem body, or "" where it is not one.
func problemCode(rec *httptest.ResponseRecorder) string {
	var p api.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		return ""
	}
	return string(p.Code)
}

// menuHas reports whether the menu drawn for who on `prod` links to key's page.
func menuHas(t *testing.T, s *Server, who identity.Who, key string) bool {
	t.Helper()
	menu, failure := apiClusters{s}.Nav(as(who), "prod")
	if failure != nil {
		t.Fatalf("the menu failed: %+v", failure.Reason)
	}
	return countHref(menu, extraPageURL("prod", key)) > 0
}

func useQuestions(reviews *accesstest.Reviews) int {
	n := 0
	for _, q := range reviews.Asked() {
		if q.Attributes.Group == UseGroup {
			n++
		}
	}
	return n
}

// **Two identities, one extra**: alice holds `use` on it and reaches every
// route and sees it in the menu; frank holds none, every route answers 403
// rbac_denied naming the permission, and the menu leaves it out.
func TestUnderTheServerProfileOnlyAPersonHoldingUseReachesAnExtraAndSeesIt(t *testing.T) {
	s, mux, _ := turnedOn(t, []string{fixtureKey}, fixtureKey)

	for what, path := range extraRoutes(fixtureKey) {
		if rec := reach(mux, alice, path); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "reached "+what) {
			t.Errorf("alice holds use and %s answered %d %q", what, rec.Code, firstLine(rec.Body.String()))
		}
		rec := reach(mux, frank, path)
		if rec.Code != http.StatusForbidden || problemCode(rec) != string(api.ProblemRBACDenied) ||
			!strings.Contains(rec.Body.String(), "use on "+UseGroup+"/"+fixtureKey) || strings.Contains(rec.Body.String(), "reached") {
			t.Errorf("frank holds no use and %s answered %d %q", what, rec.Code, firstLine(rec.Body.String()))
		}
	}
	if !menuHas(t, s, alice, fixtureKey) {
		t.Error("the menu leaves out an extra alice may use")
	}
	if menuHas(t, s, frank, fixtureKey) {
		t.Error("the menu offers frank an extra he may not use")
	}
}

// **The grant is per extra**: alice holds `use` on one of two extras that are
// on, and reaches and sees that one only.
func TestUnderTheServerProfileUseIsReviewedPerExtra(t *testing.T) {
	s, mux, _ := turnedOn(t, []string{fixtureKey, fixtureOtherKey}, fixtureKey)

	if rec := reach(mux, alice, extraRoutes(fixtureKey)["the page"]); rec.Code != http.StatusOK {
		t.Errorf("alice's granted extra answered %d %q", rec.Code, firstLine(rec.Body.String()))
	}
	if rec := reach(mux, alice, extraRoutes(fixtureOtherKey)["the page"]); rec.Code != http.StatusForbidden {
		t.Errorf("alice's ungranted extra answered %d %q, want 403", rec.Code, firstLine(rec.Body.String()))
	}
	if !menuHas(t, s, alice, fixtureKey) || menuHas(t, s, alice, fixtureOtherKey) {
		t.Errorf("the menu draws %s=%v %s=%v, want only %s", fixtureKey, menuHas(t, s, alice, fixtureKey),
			fixtureOtherKey, menuHas(t, s, alice, fixtureOtherKey), fixtureKey)
	}
}

// **A rename takes the page from nobody**: alice holds `use` only on the key
// the extra was published under, as a ClusterRole written before the rename
// does. She reaches every route, sees it in the menu, may turn it off, and the
// write is recorded under the grant that allowed it. frank, holding neither, is
// refused and told the key to grant from now on.
func TestUnderTheServerProfileUseOnTheKeyAnExtraWasPublishedUnderStillGrantsIt(t *testing.T) {
	s, mux, _ := turnedOn(t, []string{fixtureKey}, fixtureLegacyKey)

	for what, path := range extraRoutes(fixtureKey) {
		if rec := reach(mux, alice, path); rec.Code != http.StatusOK {
			t.Errorf("alice holds use on %s and %s answered %d %q", fixtureLegacyKey, what, rec.Code, firstLine(rec.Body.String()))
		}
	}
	if !menuHas(t, s, alice, fixtureKey) {
		t.Error("the menu leaves out an extra alice may use under its old key")
	}
	rec := reach(mux, frank, extraRoutes(fixtureKey)["the page"])
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "use on "+UseGroup+"/"+fixtureKey) {
		t.Errorf("frank holds no use and the page answered %d %q, want 403 naming %s", rec.Code,
			firstLine(rec.Body.String()), fixtureKey)
	}
	if row := listed(t, s, alice)[fixtureKey]; !row.Writable || row.Locked != "" {
		t.Errorf("alice holds use on %s and may not turn %s off: %+v", fixtureLegacyKey, fixtureKey, row)
	}
	if err := (apiClusters{s}).SetExtension(as(alice), "prod", fixtureKey, false); err != nil {
		t.Errorf("alice holds use on %s and may not turn %s off: %v", fixtureLegacyKey, fixtureKey, err)
	}

}

// extraGroup is the Extra heading of the menu drawn for who on `prod`, and
// whether there is one.
func extraGroup(t *testing.T, s *Server, who identity.Who) (api.NavGroup, bool) {
	t.Helper()
	menu, failure := apiClusters{s}.Nav(as(who), "prod")
	if failure != nil {
		t.Fatalf("the menu failed: %+v", failure.Reason)
	}
	for _, group := range menu.Groups {
		if group.Key == navKey(extraNavLabel) {
			return group, true
		}
	}
	return api.NavGroup{}, false
}

// **A review that could not be made refuses**: 503 access_unreviewable at the
// route, and the extra is left out of the menu. Nothing is relaxed for it -
// and the menu says so: the Extra heading stays, with no link to the extra,
// and its note says the review could not be made. A refused person's menu has
// no heading at all, so the two are told apart.
func TestUnderTheServerProfileAnUnreviewableUseRefusesTheExtra(t *testing.T) {
	s, mux, reviews := turnedOn(t, []string{fixtureKey}, fixtureKey)
	reviews.Answer = func(q accesstest.Question) (bool, error) {
		if q.Attributes.Group == UseGroup {
			return false, errors.New("the authorizer is unreachable")
		}
		return true, nil
	}

	rec := reach(mux, alice, extraRoutes(fixtureKey)["the page"])
	if rec.Code != http.StatusServiceUnavailable || problemCode(rec) != string(api.ProblemAccessUnreviewable) ||
		strings.Contains(rec.Body.String(), "reached") {
		t.Errorf("an unreviewable use answered %d %q, want 503 access_unreviewable", rec.Code, firstLine(rec.Body.String()))
	}
	if menuHas(t, s, alice, fixtureKey) {
		t.Error("the menu draws an extra whose use could not be reviewed")
	}
	group, ok := extraGroup(t, s, alice)
	if !ok {
		t.Fatal("an unreviewable use drops the Extra heading, as a refusal does: nothing says the review failed")
	}
	if !strings.Contains(group.Note, "Whether you may use "+fixtureLabel+" on this cluster could not be reviewed") ||
		!strings.Contains(group.Note, "the authorizer is unreachable") {
		t.Errorf("the Extra heading's note is %q, want the review that could not be made and why", group.Note)
	}
	if len(group.Items) != 0 {
		t.Errorf("the Extra heading links %+v under a failed review", group.Items)
	}
}

// **A refusal leaves nothing to say**: frank holds no use, and his menu has no
// Extra heading and no note - the other half of the test above.
func TestUnderTheServerProfileADeniedUseLeavesNoExtraHeading(t *testing.T) {
	s, _, _ := turnedOn(t, []string{fixtureKey}, fixtureKey)
	if group, ok := extraGroup(t, s, frank); ok {
		t.Errorf("a refused person's menu has an Extra heading: %+v", group)
	}
}

// **Under the shared password the review is the ServiceAccount's**: the shared
// account is reviewed as the console itself (a self review), so a refusal
// names the ServiceAccount's grant and does not tell the person they may not.
func TestUnderTheServerProfileTheSharedAccountsRefusalNamesTheServiceAccount(t *testing.T) {
	_, mux, reviews := turnedOn(t, []string{fixtureKey}, fixtureKey)
	reviews.Answer = func(q accesstest.Question) (bool, error) {
		if q.Attributes.Group == UseGroup {
			return false, nil
		}
		return true, nil
	}
	admin := identity.Who{Name: "admin", Method: identity.Password}

	rec := reach(mux, admin, extraRoutes(fixtureKey)["the page"])
	body := rec.Body.String()
	if rec.Code != http.StatusForbidden || problemCode(rec) != string(api.ProblemRBACDenied) || strings.Contains(body, "reached") {
		t.Fatalf("the shared account's refused use answered %d %q, want 403 rbac_denied", rec.Code, firstLine(body))
	}
	if !strings.Contains(body, "ServiceAccount") || !strings.Contains(body, "use on "+UseGroup+"/"+fixtureKey) {
		t.Errorf("the shared account's refusal does not name the ServiceAccount's grant: %q", body)
	}
	if strings.Contains(body, "You may not use") {
		t.Errorf("the shared account's refusal blames the person: %q", body)
	}
	asked := false
	for _, q := range reviews.Asked() {
		if q.Attributes.Group == UseGroup {
			asked = true
			if !q.Self {
				t.Errorf("the shared account's use was reviewed as %q, want a self review", q.User)
			}
		}
	}
	if !asked {
		t.Error("the shared account's use was never reviewed")
	}
}

// **A cluster nobody can name never reaches the extra**: every route of an
// extra on `prod`, asked for under a cluster the kubeconfig does not hold,
// answers 404 with the unknown-cluster sentence - from the guard, not the
// extra's handler - for a person holding use and one who does not, on the
// server profile and on the desktop. It is the console API's answer to the same
// name: a problem object, code not_found, the sentence as its detail.
func TestAnExtraRouteUnderAnUnknownClusterNeverReachesTheHandler(t *testing.T) {
	_, onServer, _ := turnedOn(t, []string{fixtureKey}, fixtureKey)

	t.Setenv(extras.EnvExtras, "")
	desktop := serverFor(t, false)
	desktop.extras = extras.New(t.TempDir(), []string{"prod"}, nil)
	if err := desktop.extras.SetOn("prod", fixtureKey, true); err != nil {
		t.Fatal(err)
	}
	onDesktop := http.NewServeMux()
	desktop.mountExtensions(onDesktop)

	for profile, mux := range map[string]*http.ServeMux{"the server profile": onServer, "the desktop": onDesktop} {
		for what, path := range extraRoutes(fixtureKey) {
			path = strings.Replace(path, "/x/prod/", "/x/nosuch/", 1)
			for _, who := range []identity.Who{alice, frank} {
				rec := reach(mux, who, path)
				if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "reached") ||
					!strings.Contains(rec.Body.String(), `no cluster called \"nosuch\"`) {
					t.Errorf("on %s, %s under an unknown cluster answered %s %d %q, want the guard's 404",
						profile, what, who.Name, rec.Code, firstLine(rec.Body.String()))
				}
				assertUnknownClusterProblem(t, rec, "nosuch")
			}
		}
	}
}

// **An extra that is off is never reviewed for use**: it stays the 404 it is,
// for a person holding `use` or not, and the menu has nothing to review.
func TestUnderTheServerProfileAnExtraThatIsOffIsNotReviewedForUse(t *testing.T) {
	s, _, reviews := useServer(t, fixtureKey, t.TempDir(), fixtureKey)
	mux := http.NewServeMux()
	s.mountExtensions(mux)

	for _, who := range []identity.Who{alice, frank} {
		if rec := reach(mux, who, extraRoutes(fixtureKey)["the page"]); rec.Code != http.StatusNotFound {
			t.Errorf("an extra that is off answered %s %d %q, want 404", who.Name, rec.Code, firstLine(rec.Body.String()))
		}
		if menuHas(t, s, who, fixtureKey) {
			t.Errorf("the menu draws an extra that is off for %s", who.Name)
		}
	}
	if n := useQuestions(reviews); n != 0 {
		t.Errorf("%d use reviews asked about an extra that is off", n)
	}
}

// **On the desktop nothing is asked**: the person is the kubeconfig's
// identity, and an extra that is on is reached and drawn whatever a review
// would have said.
func TestOnTheDesktopAnExtraThatIsOnIsNotReviewedForUse(t *testing.T) {
	t.Setenv(extras.EnvExtras, "")
	s := serverFor(t, false)
	reviews := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return false, nil }}
	s.gate = reviews.Gate()
	s.extras = extras.New(t.TempDir(), []string{"prod"}, nil)
	if err := s.extras.SetOn("prod", fixtureKey, true); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.mountExtensions(mux)

	if rec := reach(mux, frank, extraRoutes(fixtureKey)["the page"]); rec.Code != http.StatusOK {
		t.Errorf("the desktop refused an extra that is on: %d %q", rec.Code, firstLine(rec.Body.String()))
	}
	menu, failure := apiClusters{s}.Nav(context.Background(), "prod")
	if failure != nil {
		t.Fatalf("the menu failed: %+v", failure.Reason)
	}
	if countHref(menu, extraPageURL("prod", fixtureKey)) == 0 {
		t.Error("the desktop's menu leaves out an extra that is on")
	}
	if n := useQuestions(reviews); n != 0 {
		t.Errorf("the desktop asked %d use reviews", n)
	}
}

// assertUnknownClusterProblem is the console API's answer for a cluster name
// the kubeconfig does not hold (api.UnknownCluster), checked field by field.
func assertUnknownClusterProblem(t *testing.T, rec *httptest.ResponseRecorder, name string) {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("unknown cluster %q answered Content-Type %q, want application/problem+json", name, got)
	}
	var p api.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("unknown cluster %q answered a body that is not a problem: %q", name, firstLine(rec.Body.String()))
	}
	if rec.Code != http.StatusNotFound || p.Status != http.StatusNotFound || p.Code != api.ProblemNotFound {
		t.Errorf("unknown cluster %q answered %d (status %d, code %q), want 404 %s",
			name, rec.Code, p.Status, p.Code, api.ProblemNotFound)
	}
	if want := extension.UnknownClusterText(name); p.Detail != want {
		t.Errorf("unknown cluster %q answered the detail %q, want %q", name, p.Detail, want)
	}
}

// **The use review refuses a cluster nobody can name in the same shape**:
// refuseExtraUnused is asked after refuseExtraOff, which answers the case
// first, and it answers it itself so the review is never skipped.
func TestTheUseReviewAnswersAnUnknownClusterAsTheAPIDoes(t *testing.T) {
	s, _, reviews := turnedOn(t, []string{fixtureKey}, fixtureKey)
	request := httptest.NewRequest(http.MethodGet, "/x/nosuch/"+fixtureKey+"/", nil)
	request.SetPathValue("cluster", "nosuch")
	request = request.WithContext(identity.With(request.Context(), alice))
	rec := httptest.NewRecorder()
	if !s.refuseExtraUnused(rec, request, fixtureKey) {
		t.Fatal("refuseExtraUnused let an unknown cluster through")
	}
	assertUnknownClusterProblem(t, rec, "nosuch")
	if n := useQuestions(reviews); n != 0 {
		t.Errorf("%d use reviews asked about a cluster nobody can name", n)
	}
}

// **An API address under the old key is redirected, not lost**: a 308, which
// keeps the method and the body, to the same address and query under the key.
func TestAnAddressUnderTheKeyAnExtraWasPublishedUnderIsRedirected(t *testing.T) {
	_, mux, _ := turnedOn(t, []string{fixtureKey}, fixtureKey)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		request := httptest.NewRequest(method, "/x/prod/"+fixtureLegacyKey+"/panel?ns=shop", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, request)
		if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != "/x/prod/"+fixtureKey+"/panel?ns=shop" {
			t.Errorf("%s under %s answered %d to %q, want 308 to /x/prod/%s/panel?ns=shop", method, fixtureLegacyKey,
				rec.Code, rec.Header().Get("Location"), fixtureKey)
		}
	}
	if rec := reach(mux, alice, "/x/prod/"+fixtureKey+"/panel"); rec.Code != http.StatusOK {
		t.Errorf("the address the redirect points to answered %d", rec.Code)
	}
}

// **An old key whose review could not be made grants nothing** (#157):
// alice's `use` on the new key is denied and the one on the key the extra was
// published under could not be reviewed. Whether she may use it is unknown,
// so the page answers 503, never 403 and never the page, the menu leaves it
// out, and her switch is refused as unreviewable.
func TestAnUnreviewableOldKeyAnswers503AndGrantsNothing(t *testing.T) {
	s, mux, reviews := turnedOn(t, []string{fixtureKey})
	reviews.Answer = func(q accesstest.Question) (bool, error) {
		if q.Attributes.Group != UseGroup {
			return true, nil
		}
		if q.Attributes.Resource == fixtureLegacyKey {
			return false, errors.New("the authorizer is unreachable")
		}
		return false, nil
	}

	rec := reach(mux, alice, extraRoutes(fixtureKey)["the page"])
	if rec.Code != http.StatusServiceUnavailable || problemCode(rec) != string(api.ProblemAccessUnreviewable) ||
		strings.Contains(rec.Body.String(), "reached") {
		t.Errorf("an unreviewable old key answered %d %q, want 503 access_unreviewable", rec.Code, firstLine(rec.Body.String()))
	}
	if menuHas(t, s, alice, fixtureKey) {
		t.Error("the menu draws an extra whose old key could not be reviewed")
	}
	if err := (apiClusters{s}).SetExtension(as(alice), "prod", fixtureKey, false); !errors.Is(err, api.ErrUnreviewable) {
		t.Errorf("turning it off: %v, want ErrUnreviewable (503)", err)
	}
}

// **A write is reviewed once** (#157): turning an extra off asks the `use`
// question once, and the old key's only when the new one is denied.
func TestAnExtensionSwitchIsReviewedOnce(t *testing.T) {
	for _, tc := range []struct {
		name    string
		granted string
		want    int
	}{
		{"granted on its key", fixtureKey, 1},
		{"granted on the key it was published under", fixtureLegacyKey, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, reviews := turnedOn(t, []string{fixtureKey}, tc.granted)
			// A clock past the TTL at every read: no answer is held, so a
			// second review would reach the authorizer and be counted.
			var clock atomic.Int64
			clock.Store(time.Now().UnixNano())
			s.gate = access.New(access.Options{Reviews: reviews.For, InCluster: true, Audit: audit.New(&lockedBuffer{}),
				Now: func() time.Time { return time.Unix(0, clock.Add(int64(2*access.TTL))) }})
			before := useQuestions(reviews)
			if err := (apiClusters{s}).SetExtension(as(alice), "prod", fixtureKey, false); err != nil {
				t.Fatal(err)
			}
			if got := useQuestions(reviews) - before; got != tc.want {
				t.Errorf("%d use questions for one switch, want %d", got, tc.want)
			}
		})
	}
}
