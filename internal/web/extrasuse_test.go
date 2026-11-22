package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/extras"
	"github.com/thonicdev/kubetower/internal/identity"
)

// The server profile's per-cluster switch: the
// chart's values (KT_EXTRAS) say which extras may be turned on, the `use` permission
// on an extra says who may turn it on or off and configure it, the choice is
// kept in the state volume and read back after a restart, and every write
// leaves an audit record.

// auditRecords is every audit record written to sink so far.
func auditRecords(t *testing.T, sink *lockedBuffer) []audit.Record {
	t.Helper()
	var out []audit.Record
	for _, line := range strings.Split(strings.TrimSpace(sink.String()), "\n") {
		if line == "" {
			continue
		}
		var r audit.Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("an audit line is not JSON: %v\n%s", err, line)
		}
		out = append(out, r)
	}
	return out
}

// alice (shellconsole_test.go) holds `use` on the extras a test grants; frank
// (access_test.go) holds it on none.

// useServer is a server-profile console on the cluster `prod`, with KT_EXTRAS
// set to values, its state in dir, and a gate that allows `use` on the keys in
// granted to alice alone, and every other question.
func useServer(t *testing.T, values, dir string, granted ...string) (*Server, *lockedBuffer, *accesstest.Reviews) {
	t.Helper()
	t.Setenv(extras.EnvExtras, values)
	s := serverFor(t, true)
	sink := &lockedBuffer{}
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		if q.Attributes.Group != UseGroup {
			return true, nil
		}
		if q.Attributes.Verb != "use" || q.User != alice.Name {
			return false, nil
		}
		for _, key := range granted {
			if q.Attributes.Resource == key {
				return true, nil
			}
		}
		return false, nil
	}}
	s.gate = access.New(access.Options{Reviews: reviews.For, InCluster: true, Audit: audit.New(sink)})
	s.extras = extras.New(dir, []string{"prod"}, nil, extras.InCluster())
	return s, sink, reviews
}

func listed(t *testing.T, s *Server, who identity.Who) map[string]api.Extension {
	t.Helper()
	rows, failure := apiClusters{s}.Extensions(as(who), "prod")
	if failure != nil {
		t.Fatalf("Extensions: %+v", failure)
	}
	out := map[string]api.Extension{}
	for _, row := range rows {
		out[row.Key] = row
	}
	return out
}

// **One replica, an extra the values name**: off on a cluster nobody chose,
// writable for a person holding `use` on it, turned on by them, read back after
// a restart, and recorded - who, which extra, which cluster, what changed.
func TestUnderTheServerProfileAPersonHoldingUseTurnsAnExtraOnAndItIsKept(t *testing.T) {
	dir := t.TempDir()
	s, sink, _ := useServer(t, fixtureKey+","+fixtureOtherKey, dir, fixtureKey, fixtureOtherKey)

	rows := listed(t, s, alice)
	alfa := rows[fixtureKey]
	if alfa.Enabled || alfa.Pinned {
		t.Fatalf("an extra the values name is enabled=%v pinned=%v on a cluster nobody chose", alfa.Enabled, alfa.Pinned)
	}
	if !alfa.Writable || alfa.Locked != "" || alfa.Reason != "" {
		t.Fatalf("alice holds use on %s and may not turn it on: %+v", fixtureKey, alfa)
	}

	if err := (apiClusters{s}).SetExtension(as(alice), "prod", fixtureKey, true); err != nil {
		t.Fatalf("SetExtension: %v", err)
	}
	if !listed(t, s, alice)[fixtureKey].Enabled {
		t.Fatal("turned on, the listing says off")
	}
	// The restart: a new store over the same state directory.
	again, _, _ := useServer(t, fixtureKey+","+fixtureOtherKey, dir, fixtureKey)
	if !listed(t, again, alice)[fixtureKey].Enabled {
		t.Error("the choice did not survive a restart")
	}

	var found bool
	for _, r := range auditRecords(t, sink) {
		if r.Act != audit.Extension || r.Phase != audit.Outcome {
			continue
		}
		found = true
		if r.Who != alice.Name || r.Cluster != "prod" || r.Resource != fixtureKey || r.Group != UseGroup ||
			r.Verb != "use" || r.Change != "enabled=true, was false" || r.Outcome != audit.Applied {
			t.Errorf("the audit record does not say who turned which extra on where: %+v", r)
		}
	}
	if !found {
		t.Errorf("no audit record of the write: %+v", auditRecords(t, sink))
	}

	// Whoever may turn it on may configure it.
	if err := (apiClusters{s}).SetExtensionSetting(as(alice), "prod", fixtureOtherKey, fixtureSetting, false); err != nil {
		t.Errorf("alice holds use on %s and may not change its setting: %v", fixtureOtherKey, err)
	}
}

// **A person without `use`** is offered no switch, and both writes are
// refused as the contract's 403 - and the store is not touched.
func TestUnderTheServerProfileAPersonWithoutUseCannotToggleOrConfigure(t *testing.T) {
	s, sink, _ := useServer(t, fixtureKey+","+fixtureOtherKey, t.TempDir(), fixtureKey, fixtureOtherKey)

	rows := listed(t, s, frank)
	for _, key := range []string{fixtureKey, fixtureOtherKey} {
		row := rows[key]
		if row.Writable || row.Locked != api.ExtensionNoUse || !strings.Contains(row.Reason, "use") {
			t.Errorf("frank holds no use on %s and the listing says writable=%v locked=%q reason=%q",
				key, row.Writable, row.Locked, row.Reason)
		}
	}

	err := (apiClusters{s}).SetExtension(as(frank), "prod", fixtureKey, true)
	if !errors.Is(err, api.ErrRefused) {
		t.Errorf("frank turning %s on: %v, want ErrRefused (403)", fixtureKey, err)
	}
	if s.extras.EnabledOn("prod", fixtureKey) {
		t.Error("a refused write reached the store")
	}
	err = (apiClusters{s}).SetExtensionSetting(as(frank), "prod", fixtureOtherKey, fixtureSetting, false)
	if !errors.Is(err, api.ErrRefused) {
		t.Errorf("frank changing a setting of %s: %v, want ErrRefused (403)", fixtureOtherKey, err)
	}
	if !s.extras.Setting("prod", fixtureOtherKey, fixtureSetting) {
		t.Error("a refused setting write reached the store")
	}
	refused := 0
	for _, r := range auditRecords(t, sink) {
		if r.Act == audit.Extension && r.Phase == audit.Outcome && r.Outcome == audit.Refused && r.Who == frank.Name {
			refused++
		}
	}
	if refused != 2 {
		t.Errorf("%d refused writes recorded, want 2: %+v", refused, auditRecords(t, sink))
	}

	// Through the route, so the 403 is what a person sees.
	deps := api.Deps{Clusters: apiClusters{s}, InCluster: true}
	body := strings.NewReader(`{"enabled": true}`)
	request := httptest.NewRequest(http.MethodPut, "/api/c/prod/extensions/"+fixtureKey, body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-KubeTower-Request", "1")
	request = request.WithContext(identity.With(request.Context(), frank))
	rec := httptest.NewRecorder()
	api.New(deps).ServeHTTP(rec, request)
	if rec.Code != http.StatusForbidden {
		t.Errorf("PUT as frank: %d %s, want 403", rec.Code, rec.Body.String())
	}
}

// **The shared password account's switch names the ServiceAccount's grant.**
// Under the server profile the shared account is reviewed as the console
// itself (a self review); a denial there is the ServiceAccount's missing
// grant, which an administrator gives, and Settings > Extra says so in the
// route's words (consoleMayNotUse) - never "You may not".
func TestUnderTheServerProfileTheSharedAccountsSwitchNamesTheServiceAccount(t *testing.T) {
	s, _, reviews := useServer(t, fixtureKey, t.TempDir())
	reviews.Answer = func(q accesstest.Question) (bool, error) {
		return q.Attributes.Group != UseGroup, nil
	}
	admin := identity.Who{Name: "admin", Method: identity.Password}

	row := listed(t, s, admin)[fixtureKey]
	if row.Writable || row.Locked != api.ExtensionNoUse {
		t.Fatalf("the shared account's refused use left the switch writable=%v locked=%q", row.Writable, row.Locked)
	}
	if want := consoleMayNotUse(row.Label, fixtureKey); row.Reason != want {
		t.Errorf("the shared account's switch says %q, want the route's sentence %q", row.Reason, want)
	}
	if !strings.Contains(row.Reason, "ServiceAccount") || !strings.Contains(row.Reason, "use on "+UseGroup+"/"+fixtureKey) {
		t.Errorf("the shared account's switch does not name the ServiceAccount's grant: %q", row.Reason)
	}
	if strings.Contains(row.Reason, "You may not") {
		t.Errorf("the shared account's switch blames the person: %q", row.Reason)
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

	// A person's own denial keeps the person's sentence.
	if reason := listed(t, s, frank)[fixtureKey].Reason; !strings.Contains(reason, "You may not turn") ||
		strings.Contains(reason, "ServiceAccount") {
		t.Errorf("frank's own refusal says %q, want the person's sentence", reason)
	}
}

// **An extra the values do not name** is listed with its button disabled and
// the reason, and its writes are refused - for a person who holds `use` on it
// too.
func TestUnderTheServerProfileAnExtraTheValuesDoNotNameIsListedAndRefused(t *testing.T) {
	s, _, _ := useServer(t, fixtureKey, t.TempDir(), fixtureKey, fixtureOtherKey)

	row := listed(t, s, alice)[fixtureOtherKey]
	if row.Writable || row.Enabled || row.Locked != api.ExtensionNotInValues || row.Reason != extras.NotInValues {
		t.Errorf("an extra the values do not name: %+v", row)
	}
	if !strings.Contains(strings.ToLower(row.Reason), "not enabled in this installation's chart values") {
		t.Errorf("the reason is %q", row.Reason)
	}
	err := (apiClusters{s}).SetExtension(as(alice), "prod", fixtureOtherKey, true)
	if !errors.Is(err, api.ErrNotInProfile) {
		t.Errorf("turning on an extra the values do not name: %v, want ErrNotInProfile (403)", err)
	}
	err = (apiClusters{s}).SetExtensionSetting(as(alice), "prod", fixtureOtherKey, fixtureSetting, false)
	if !errors.Is(err, api.ErrNotInProfile) {
		t.Errorf("configuring an extra the values do not name: %v, want ErrNotInProfile (403)", err)
	}
	if s.extras.EnabledOn("prod", fixtureOtherKey) {
		t.Error("an extra the values do not name is on")
	}
}

// **Several replicas**: every row is a view saying why, and the writes are
// not registered (internal/api's router: TestTheExtensionWritesFollowTheProfile).
// The same with no state volume under the server profile.
func TestWithSeveralReplicasOrNoStateVolumeEveryRowSaysWhyItCannotChange(t *testing.T) {
	for name, tc := range map[string]struct {
		set           func(*Server)
		says, notSays string
	}{
		"several replicas": {func(s *Server) { s.replicated = true }, "several replicas", "state volume (KT_STATE"},
		// One replica with no state volume is named as what it is, never as
		// several replicas.
		"no state volume": {func(s *Server) { s.setup.StateEphemeral = true }, "KT_STATE_PERSISTENT=false", "several replicas"},
	} {
		t.Run(name, func(t *testing.T) {
			s, _, reviews := useServer(t, fixtureKey, t.TempDir(), fixtureKey)
			tc.set(s)
			for key, row := range listed(t, s, alice) {
				if row.Writable || row.Locked != api.ExtensionNoSwitch || !strings.Contains(row.Reason, tc.says) ||
					strings.Contains(row.Reason, tc.notSays) {
					t.Errorf("%s: writable=%v locked=%q reason=%q", key, row.Writable, row.Locked, row.Reason)
				}
			}
			if n := len(reviews.Asked()); n != 0 {
				t.Errorf("%d reviews asked for a switch that is not there", n)
			}
			// The refusal at the extra's own address says the same.
			rec := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/x/prod/"+fixtureKey+"/", nil)
			request.SetPathValue("cluster", "prod")
			if !s.refuseExtraOff(rec, request, fixtureKey) {
				t.Fatal("refuseExtraOff let an extra through that is off")
			}
			if body := rec.Body.String(); !strings.Contains(body, tc.says) || strings.Contains(body, tc.notSays) ||
				strings.Contains(body, "PUT ") {
				t.Errorf("the refusal says %q", firstLine(body))
			}
		})
	}
}

// **A review that could not be made refuses** (under the server profile
// nothing is relaxed): the SubjectAccessReview answers an error, the listing
// offers no button for that extra, and both writes answer 503 without
// touching the store.
func TestUnderTheServerProfileAReviewThatFailsRefusesTheSwitch(t *testing.T) {
	s, _, reviews := useServer(t, fixtureKey+","+fixtureOtherKey, t.TempDir(), fixtureKey, fixtureOtherKey)
	reviews.Answer = func(q accesstest.Question) (bool, error) {
		if q.Attributes.Group == UseGroup {
			return false, errors.New("the authorizer is unreachable")
		}
		return true, nil
	}

	row := listed(t, s, alice)[fixtureKey]
	if row.Writable || row.Locked != api.ExtensionNoUse || !strings.Contains(row.Reason, "could not be reviewed") {
		t.Errorf("an unreviewable use: writable=%v locked=%q reason=%q", row.Writable, row.Locked, row.Reason)
	}

	err := (apiClusters{s}).SetExtension(as(alice), "prod", fixtureKey, true)
	if !errors.Is(err, api.ErrUnreviewable) {
		t.Errorf("turning on with no review: %v, want ErrUnreviewable (503)", err)
	}
	err = (apiClusters{s}).SetExtensionSetting(as(alice), "prod", fixtureOtherKey, fixtureSetting, false)
	if !errors.Is(err, api.ErrUnreviewable) {
		t.Errorf("configuring with no review: %v, want ErrUnreviewable (503)", err)
	}
	if s.extras.EnabledOn("prod", fixtureKey) || !s.extras.Setting("prod", fixtureOtherKey, fixtureSetting) {
		t.Error("a write nobody could review reached the store")
	}

	request := httptest.NewRequest(http.MethodPut, "/api/c/prod/extensions/"+fixtureKey, strings.NewReader(`{"enabled": true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-KubeTower-Request", "1")
	request = request.WithContext(identity.With(request.Context(), alice))
	rec := httptest.NewRecorder()
	api.New(api.Deps{Clusters: apiClusters{s}, InCluster: true}).ServeHTTP(rec, request)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("PUT with no review: %d %s, want 503", rec.Code, rec.Body.String())
	}
}

// **The banner suggests only what the person could turn on**: under
// the server profile an extra the values name and the person holds `use` on;
// never one the values do not name, never to a person without `use`.
func TestUnderTheServerProfileTheBannerSuggestsOnlyWhatThePersonCouldTurnOn(t *testing.T) {
	for name, tc := range map[string]struct {
		values  string
		who     identity.Who
		granted []string
		want    bool
	}{
		"named, holding use":     {values: fixtureSuggestingKey, who: alice, granted: []string{fixtureSuggestingKey}, want: true},
		"named, without use":     {values: fixtureSuggestingKey, who: frank, granted: []string{fixtureSuggestingKey}},
		"not named, holding use": {values: fixtureKey, who: alice, granted: []string{fixtureSuggestingKey}},
	} {
		t.Run(name, func(t *testing.T) {
			s, _, _ := useServer(t, tc.values, t.TempDir(), tc.granted...)
			suggested.Lock()
			suggested.evidence, suggested.err, suggested.asked = "1 thing", nil, nil
			suggested.Unlock()

			r := httptest.NewRequest(http.MethodGet, "/api/c/prod/extensions/suggestions", nil)
			r = r.WithContext(identity.With(r.Context(), tc.who))
			items, failure := apiClusters{s}.ExtensionSuggestions(r, "prod")
			if failure != nil {
				t.Fatalf("the suggestions failed: %+v", failure)
			}
			if got := len(items) == 1 && items[0].Key == fixtureSuggestingKey; got != tc.want {
				t.Errorf("suggested %+v, want suggested=%v", items, tc.want)
			}
		})
	}
}

// On the desktop nothing is reviewed: the switch is the one operator's, as it
// always was, and every extension nothing pins is writable.
func TestOnTheDesktopTheSwitchIsNotReviewed(t *testing.T) {
	t.Setenv(extras.EnvExtras, "")
	s := serverFor(t, false)
	reviews := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return false, nil }}
	s.gate = reviews.Gate()
	s.extras = extras.New(t.TempDir(), []string{"prod"}, nil)

	for key, row := range listed(t, s, frank) {
		if !row.Writable {
			t.Errorf("%s is not writable on the desktop: %+v", key, row)
		}
	}
	if err := (apiClusters{s}).SetExtension(as(frank), "prod", fixtureKey, true); err != nil {
		t.Errorf("SetExtension on the desktop: %v", err)
	}
	if n := len(reviews.Asked()); n != 0 {
		t.Errorf("the desktop asked %d reviews about its own switch", n)
	}
}

// **Two people switching one extra at once**:
// each record names the value its own write replaced. alice turns the extra on
// and her review is held; frank turns it off meanwhile. Read outside the
// writes' critical section, both read `was false` and frank's record says
// "enabled=false, was false" when his write replaced true.
//
// alice's review waits for frank's to begin, or for a bound: with the fix
// frank cannot begin until alice's write is done, so the bound is what lets
// her through. frank's waits for alice's write, so the order is always hers,
// then his.
func TestTwoConcurrentSwitchesOfOneExtraEachRecordWhatTheirWriteReplaced(t *testing.T) {
	for name, tc := range map[string]struct {
		write  func(s *Server, who identity.Who, value bool) error
		change func(value, was bool) string
		start  bool
	}{
		"switch": {
			write: func(s *Server, who identity.Who, value bool) error {
				return (apiClusters{s}).SetExtension(as(who), "prod", fixtureKey, value)
			},
			change: func(value, was bool) string { return fmt.Sprintf("enabled=%t, was %t", value, was) },
		},
		"setting": {
			write: func(s *Server, who identity.Who, value bool) error {
				return (apiClusters{s}).SetExtensionSetting(as(who), "prod", fixtureOtherKey, fixtureSetting, value)
			},
			change: func(value, was bool) string {
				return fmt.Sprintf("setting %s=%t, was %t", fixtureSetting, value, was)
			},
			// The setting's declared default is true: alice turns it off, frank on.
			start: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, sink, reviews := useServer(t, fixtureKey+","+fixtureOtherKey, t.TempDir(), fixtureKey, fixtureOtherKey)
			aliceReviewing, frankReviewing, aliceDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var frankOnce sync.Once
			reviews.Answer = func(q accesstest.Question) (bool, error) {
				if q.Attributes.Group != UseGroup {
					return true, nil
				}
				switch q.User {
				case alice.Name:
					close(aliceReviewing)
					select {
					case <-frankReviewing:
					case <-time.After(300 * time.Millisecond):
					}
				case frank.Name:
					frankOnce.Do(func() { close(frankReviewing) })
					<-aliceDone
				}
				return true, nil
			}

			errs := make(chan error, 2)
			go func() {
				defer close(aliceDone)
				errs <- tc.write(s, alice, !tc.start)
			}()
			<-aliceReviewing
			go func() { errs <- tc.write(s, frank, tc.start) }()
			for range 2 {
				if err := <-errs; err != nil {
					t.Fatalf("a write failed: %v", err)
				}
			}

			want := map[string]string{
				alice.Name: tc.change(!tc.start, tc.start),
				frank.Name: tc.change(tc.start, !tc.start),
			}
			seen := 0
			for _, r := range auditRecords(t, sink) {
				if r.Act != audit.Extension {
					continue
				}
				seen++
				if r.Change != want[r.Who] {
					t.Errorf("%s's %s record says %q, want %q", r.Who, r.Phase, r.Change, want[r.Who])
				}
			}
			if seen != 4 {
				t.Errorf("%d extension records, want an attempt and an outcome each: %+v", seen, auditRecords(t, sink))
			}
		})
	}
}
