package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/extras"
	"github.com/thonicdev/kubetower/internal/registry"
)

// **A disabled extra must be both unlinked and unroutable.**
//
// Hiding the menu entry is a courtesy; refusing the route is the control — see
// the argument at the top of extras.go.
//
// # Why it checks the wrapper and not the handlers
//
// The pages live in another module; the console mounts their routes itself
// through `mountExtensions`, and the gate is the wrapper it mounts them in. An
// extension **cannot** forget the gate, because it is not the thing doing the
// checking - so the property is structural, and what is left to check is that
// the wrapper really is applied to every route an extension declares, whatever
// shape it is.

// A pattern that does not begin with a slash is refused rather than mounted
// somewhere surprising.
func TestARouteWithoutALeadingSlashIsNotMounted(t *testing.T) {
	for _, pattern := range []string{"panel", "", "GET /panel", " /panel"} {
		if _, err := registry.Pattern(fixtureKey, extension.Route{Pattern: pattern}); err == nil {
			t.Errorf("%q was accepted as a route pattern; an extension that can write its own "+
				"prefix can mount itself over the console", pattern)
		}
	}
	if got, err := registry.Pattern(fixtureKey, extension.Route{Method: "GET", Pattern: "/panel"}); err != nil ||
		got != "GET /x/{cluster}/"+fixtureKey+"/panel" {
		t.Errorf("a valid route built %q, %v", got, err)
	}
}

// A guard that answered 404 for an extra that is on would be worse than no guard
// at all, so the sentence it produces is pinned here too.
func TestTheRefusalNamesTheExtraAndWhereToTurnItOn(t *testing.T) {
	t.Setenv(extras.EnvExtras, "")
	// A bare Server: the assertion is about the sentence, and a cluster manager
	// would be a fixture it does not depend on. An extra that is off everywhere
	// is refused before any cluster is considered, which is the order that sends
	// the operator to the control that would actually work.
	server := &Server{extras: extras.New("", nil, nil)}

	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/x/mi-k3s/"+fixtureKey+"/", nil)
	if !server.refuseExtraOff(rec, request, fixtureKey) {
		t.Fatal("refuseExtraOff let a disabled extra through")
	}
	body := rec.Body.String()

	if !strings.Contains(body, fixtureLabel) {
		t.Errorf("the refusal does not name %q, so the reader cannot match it to a switch: %q",
			fixtureLabel, firstLine(body))
	}
	// The switches that exist, spelled so they can be typed: the Extra tab,
	// on every profile, the write behind it, and the pin.
	for _, want := range []string{
		"Settings > Extra",
		"PUT /api/c/<cluster>/extensions/" + fixtureKey,
		extras.EnvExtras + "=" + fixtureKey,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not say %q, so it does not say how to turn it on: %q",
				want, firstLine(body))
		}
	}
	if strings.Contains(body, "Tick") {
		t.Errorf("the refusal names a tick that does not exist: %q", firstLine(body))
	}
}

// On one cluster and not on this one: the refusal names this cluster's own
// switch, with the context name percent-encoded as the API takes it, and not
// a Settings tab.
func TestARefusalOnOneClusterNamesThatClustersSwitch(t *testing.T) {
	t.Setenv(extras.EnvExtras, "")
	const here = "arn:aws:eks:eu-west-1:1/prod one"
	server := navFixture(t, here)
	server.extras = extras.New(t.TempDir(), []string{here, "staging"}, nil)
	if err := server.extras.SetOn("staging", fixtureKey, true); err != nil {
		t.Fatalf("turning the fixture on elsewhere: %v", err)
	}

	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/x/any/"+fixtureKey+"/", nil)
	request.SetPathValue("cluster", here)
	if !server.refuseExtraOff(rec, request, fixtureKey) {
		t.Fatal("refuseExtraOff let an extra through on a cluster it is off on")
	}
	body := rec.Body.String()

	for _, want := range []string{
		"for this cluster",
		"PUT /api/c/arn:aws:eks:eu-west-1:1%2Fprod%20one/extensions/" + fixtureKey,
		extras.EnvExtras + "=" + fixtureKey,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not say %q: %q", want, firstLine(body))
		}
	}
	if strings.Contains(body, "Tick") {
		t.Errorf("the refusal names a tick that does not exist: %q", firstLine(body))
	}
}

// A replicated console registers no per-cluster write, so its refusal must not
// name one; and KT_EXTRAS pins nothing in a cluster, so the refusal
// says what it takes instead: one replica. Both refusals end with the same
// sentence; both are checked.
func TestAReplicatedConsolesRefusalNamesNoWriteItDoesNotRegister(t *testing.T) {
	t.Setenv(extras.EnvExtras, "")
	const here = "prod"

	for name, onElsewhere := range map[string]bool{"off everywhere": false, "on elsewhere": true} {
		server := navFixture(t, here)
		server.replicated = true
		server.extras = extras.New(t.TempDir(), []string{here, "staging"}, nil)
		if onElsewhere {
			if err := server.extras.SetOn("staging", fixtureKey, true); err != nil {
				t.Fatalf("turning the fixture on elsewhere: %v", err)
			}
		}

		rec := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/x/any/"+fixtureKey+"/", nil)
		request.SetPathValue("cluster", here)
		if !server.refuseExtraOff(rec, request, fixtureKey) {
			t.Fatalf("%s: refuseExtraOff let an extra through that is off here", name)
		}
		body := rec.Body.String()

		if strings.Contains(body, "PUT ") || strings.Contains(body, "/extensions/") {
			t.Errorf("%s: a replicated console names a write it does not register: %q", name, firstLine(body))
		}
		if !strings.Contains(body, "one replica") {
			t.Errorf("%s: the refusal does not say one replica is what it takes: %q", name, firstLine(body))
		}
	}
}

// And when the deployment pinned it off, the refusal has to send the operator to
// what decided rather than to a control that cannot override it.
//
// This is the case that costs real time when it is wrong: an operator sent to
// Settings ticks a box, watches it refuse, and concludes the console is broken.
func TestARefusalForAPinnedExtraNamesTheVariableAndNotThePage(t *testing.T) {
	t.Setenv(extras.EnvExtras, fixtureKey+"=off")
	server := &Server{extras: extras.New("", nil, nil)}

	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/x/mi-k3s/"+fixtureKey+"/", nil)
	if !server.refuseExtraOff(rec, request, fixtureKey) {
		t.Fatal("refuseExtraOff let a pinned-off extra through")
	}
	body := rec.Body.String()

	if !strings.Contains(body, extras.EnvExtras) {
		t.Errorf("the refusal does not name %s: %q", extras.EnvExtras, firstLine(body))
	}
	// Where it is decided, and that the console cannot undo it. Not a compose
	// file, which does not ship, and not Settings, which has no such control.
	if !strings.Contains(body, "deployment's environment") || !strings.Contains(body, "cannot change") {
		t.Errorf("the refusal does not say the deployment decided and the console cannot undo it: %q",
			firstLine(body))
	}
	for _, stale := range []string{"docker-compose", "Settings"} {
		if strings.Contains(body, stale) {
			t.Errorf("the refusal names %q, which is not where this is decided: %q", stale, firstLine(body))
		}
	}
}

// Off everywhere, asked on a cluster the console knows: the PUT names that
// cluster rather than a placeholder, percent-encoded as the API takes it.
func TestARefusalOffEverywhereNamesTheClusterItWasAskedOn(t *testing.T) {
	t.Setenv(extras.EnvExtras, "")
	const here = "arn:aws:eks:eu-west-1:1/prod one"
	server := navFixture(t, here)
	server.extras = extras.New(t.TempDir(), []string{here}, nil)

	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/x/any/"+fixtureKey+"/", nil)
	request.SetPathValue("cluster", here)
	if !server.refuseExtraOff(rec, request, fixtureKey) {
		t.Fatal("refuseExtraOff let an extra through that is on nowhere")
	}
	body := rec.Body.String()

	if want := "PUT /api/c/arn:aws:eks:eu-west-1:1%2Fprod%20one/extensions/" + fixtureKey; !strings.Contains(body, want) {
		t.Errorf("the refusal does not say %q: %q", want, firstLine(body))
	}
	if strings.Contains(body, "<cluster>") || strings.Contains(body, "{cluster}") {
		t.Errorf("the refusal prints a placeholder where it knew the cluster: %q", firstLine(body))
	}
}

// A key nothing registered is refused, and the refusal names the key rather than
// inventing a label for a page this console has never heard of.
func TestAnUnregisteredKeyIsRefusedByTheKeyItCarried(t *testing.T) {
	t.Setenv(extras.EnvExtras, "")
	server := &Server{extras: extras.New("", nil, nil)}

	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/x/mi-k3s/nosuchpage/", nil)
	if !server.refuseExtraOff(rec, request, "nosuchpage") {
		t.Fatal("refuseExtraOff let an unregistered key through")
	}
	if !strings.Contains(rec.Body.String(), "nosuchpage") {
		t.Errorf("the refusal does not name what was asked for: %q", firstLine(rec.Body.String()))
	}
}

// **Every refusal ends in extras.HowToTurnOn's words, and the pinned one names
// the place in extras.WhereSet's.** The start-up line ends in the same helper
// (internal/launch), so the two cannot name different switches; a
// spelling of the sentence inline could drift from the others to a switch
// that does not exist. The tests above check
// what the sentence contains; this one checks where it comes from, so an inline
// copy here - even a correct one today - fails.
func TestEveryRefusalEndsInTheSharedSentence(t *testing.T) {
	const here = "arn:aws:eks:eu-west-1:1/prod one"
	for _, tc := range []struct {
		inCluster, replicated, ephemeral bool
		profile                          extras.Profile
	}{
		{false, false, false, extras.Desktop},
		{true, false, false, extras.Server},
		{true, true, false, extras.Replicated},
		{true, false, true, extras.Unkept},
	} {
		replicated, profile := tc.replicated, tc.profile
		t.Run("", func(t *testing.T) {
			t.Setenv(extras.EnvExtras, "")

			// Off everywhere, with no cluster to name: the placeholder.
			bare := &Server{extras: extras.New("", nil, nil), replicated: replicated, inCluster: tc.inCluster}
			bare.setup.StateEphemeral = tc.ephemeral
			rec := httptest.NewRecorder()
			if !bare.refuseExtraOff(rec, httptest.NewRequest(http.MethodGet, "/x/mi-k3s/"+fixtureKey+"/", nil), fixtureKey) {
				t.Fatal("refuseExtraOff let a disabled extra through")
			}
			if want := extras.HowToTurnOn("<cluster>", fixtureKey, profile); !strings.Contains(rec.Body.String(), want) {
				t.Errorf("replicated=%v, off everywhere: the refusal does not end in HowToTurnOn's sentence\n  %q\nit says\n  %q",
					replicated, want, firstLine(rec.Body.String()))
			}

			// On elsewhere and not here: this cluster, as the API spells it.
			server := navFixture(t, here)
			server.replicated = replicated
			server.inCluster = tc.inCluster
			server.setup.StateEphemeral = tc.ephemeral
			server.extras = extras.New(t.TempDir(), []string{here, "staging"}, nil)
			if err := server.extras.SetOn("staging", fixtureKey, true); err != nil {
				t.Fatalf("turning the fixture on elsewhere: %v", err)
			}
			rec = httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/x/any/"+fixtureKey+"/", nil)
			request.SetPathValue("cluster", here)
			if !server.refuseExtraOff(rec, request, fixtureKey) {
				t.Fatal("refuseExtraOff let an extra through on a cluster it is off on")
			}
			if want := extras.HowToTurnOn(url.PathEscape(here), fixtureKey, profile); !strings.Contains(rec.Body.String(), want) {
				t.Errorf("replicated=%v, on elsewhere: the refusal does not end in HowToTurnOn's sentence\n  %q\nit says\n  %q",
					replicated, want, firstLine(rec.Body.String()))
			}
		})
	}

	// Pinned off: the variable, then the place in WhereSet's words.
	t.Setenv(extras.EnvExtras, fixtureKey+"=off")
	pinned := &Server{extras: extras.New("", nil, nil)}
	rec := httptest.NewRecorder()
	if !pinned.refuseExtraOff(rec, httptest.NewRequest(http.MethodGet, "/x/mi-k3s/"+fixtureKey+"/", nil), fixtureKey) {
		t.Fatal("refuseExtraOff let a pinned-off extra through")
	}
	if want := "turned off by " + extras.EnvExtras + " " + extras.WhereSet; !strings.Contains(rec.Body.String(), want) {
		t.Errorf("the pinned-off refusal does not say %q: %q", want, firstLine(rec.Body.String()))
	}
}

// The server profile registers the per-cluster switch, behind the `use`
// permission and the chart's values: its refusal
// names the write, who may make it, and the values - never KT_EXTRAS as a pin,
// which it is not there.
func TestTheServerProfilesRefusalNamesTheReviewedSwitch(t *testing.T) {
	t.Setenv(extras.EnvExtras, "")
	const here = "prod"
	server := navFixture(t, here)
	server.inCluster = true
	server.extras = extras.New(t.TempDir(), []string{here}, nil)

	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/x/any/"+fixtureKey+"/", nil)
	request.SetPathValue("cluster", here)
	if !server.refuseExtraOff(rec, request, fixtureKey) {
		t.Fatal("refuseExtraOff let an extra through that is off")
	}
	body := rec.Body.String()
	for _, want := range []string{"PUT /api/c/" + here + "/extensions/" + fixtureKey, "use on " + UseGroup + "/" + fixtureKey, "chart's values"} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not say %q: %q", want, body)
		}
	}
	if strings.Contains(body, extras.EnvExtras+"="+fixtureKey) {
		t.Errorf("the refusal offers %s=%s as a pin, which it is not under the server profile: %q", extras.EnvExtras, fixtureKey, body)
	}
}

// TestAnExtensionRouteIsHeldToTheMutationGuardAndTheOriginCheck: an
// extension route gets the session guard and the per-cluster gate and also the
// two checks every /api/ mutation and socket gets, or a cross-site form post -
// which the Lax session cookie accompanies on a top-level POST - would reach the
// extension's handler. The method-less form is the one the contract gives an
// extension for a socket, and it answers a POST too.
func TestAnExtensionRouteIsHeldToTheMutationGuardAndTheOriginCheck(t *testing.T) {
	t.Setenv(extras.EnvExtras, "")
	const here = "prod"
	server := navFixture(t, here)
	server.extras = extras.New(t.TempDir(), []string{here}, nil)
	if err := server.extras.SetOn(here, fixtureKey, true); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	server.mountExtensions(mux)

	serve := func(method, path, origin string, marked bool) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, nil)
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		if marked {
			request.Header.Set("X-KubeTower-Request", "1")
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, request)
		return rec
	}
	socket := "/x/" + here + "/" + fixtureKey + "/socket"
	panel := "/x/" + here + "/" + fixtureKey + "/panel"

	for _, refused := range []struct {
		name, method, path, origin string
		marked                     bool
	}{
		{"a cross-site POST, marked", http.MethodPost, socket, "https://evil.example", true},
		{"a same-site POST without the marker", http.MethodPost, socket, "", false},
		{"a cross-site socket handshake", http.MethodGet, socket, "https://evil.example", false},
		{"a cross-site GET", http.MethodGet, panel, "https://evil.example", false},
	} {
		rec := serve(refused.method, refused.path, refused.origin, refused.marked)
		if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "reached") {
			t.Errorf("%s reached the extension's handler: %d %q", refused.name, rec.Code, firstLine(rec.Body.String()))
		}
	}
	// The controls: the console's own front end gets through.
	if rec := serve(http.MethodPost, socket, "http://example.com", true); !strings.Contains(rec.Body.String(), "reached the socket") {
		t.Errorf("a same-origin, marked POST was refused: %d %q", rec.Code, firstLine(rec.Body.String()))
	}
	if rec := serve(http.MethodGet, panel, "", false); !strings.Contains(rec.Body.String(), "reached the panel") {
		t.Errorf("a plain GET was refused: %d %q", rec.Code, firstLine(rec.Body.String()))
	}
}

// The extensions listing says which rows the deployment decided, on the
// desktop and in a container: pinned on, pinned off and not named, each carried
// as it is, and a pinned row is not writable and says why. (Under the server
// profile KT_EXTRAS pins nothing: extrasuse_test.go.)
func TestTheExtensionsListingSaysWhichTheDeploymentPinned(t *testing.T) {
	const here = "prod"
	for name, tc := range map[string]struct {
		env                  string
		wantOn, wantPinned   bool
		otherOn, otherPinned bool
	}{
		"named":         {env: fixtureKey, wantOn: true, wantPinned: true},
		"named off":     {env: fixtureKey + "=off", wantPinned: true},
		"named neither": {env: ""},
		"the other one": {env: fixtureOtherKey, otherOn: true, otherPinned: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(extras.EnvExtras, tc.env)
			server := navFixture(t, here)
			server.extras = extras.New(t.TempDir(), []string{here}, nil)

			items, failure := apiClusters{s: server}.Extensions(t.Context(), here)
			if failure != nil {
				t.Fatalf("the listing failed: %+v", failure)
			}
			got := map[string][2]bool{}
			for _, item := range items {
				got[item.Key] = [2]bool{item.Enabled, item.Pinned}
				if item.Writable == item.Pinned || (item.Pinned && (item.Locked != api.ExtensionPinned || item.Reason == "")) {
					t.Errorf("%s: pinned=%v writable=%v locked=%q reason=%q", item.Key, item.Pinned, item.Writable, item.Locked, item.Reason)
				}
			}
			if want := [2]bool{tc.wantOn, tc.wantPinned}; got[fixtureKey] != want {
				t.Errorf("%s is enabled/pinned %v, want %v", fixtureKey, got[fixtureKey], want)
			}
			if want := [2]bool{tc.otherOn, tc.otherPinned}; got[fixtureOtherKey] != want {
				t.Errorf("%s is enabled/pinned %v, want %v", fixtureOtherKey, got[fixtureOtherKey], want)
			}
		})
	}
}

// The banner's question: an extension that is off here and
// that nothing pins is asked, with the person's own request, and its evidence
// is the suggestion. One that is on, or pinned, is never asked; one whose
// answer failed, or that found nothing, is left out, and the list itself does
// not fail.
func TestOnlyAnExtensionThatCouldBeTurnedOnHereIsAskedForASuggestion(t *testing.T) {
	const here = "prod"
	for name, tc := range map[string]struct {
		env       string
		turnOn    bool
		evidence  string
		err       error
		wantAsked bool
		want      string
		absent    bool
	}{
		"off by default, found":        {evidence: "2 things", wantAsked: true, want: "2 things"},
		"off by default, nothing":      {wantAsked: true, want: "charlie absent", absent: true},
		"off by default, failed":       {err: errors.New("services is forbidden"), wantAsked: true},
		"turned on here":               {turnOn: true, evidence: "2 things"},
		"pinned off by the deployment": {env: fixtureSuggestingKey + "=off", evidence: "2 things"},
		"pinned on by the deployment":  {env: fixtureSuggestingKey + "=on", evidence: "2 things"},
		"pinned on, component absent":  {env: fixtureSuggestingKey + "=on"},
		"pinned off, component absent": {env: fixtureSuggestingKey + "=off"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(extras.EnvExtras, tc.env)
			server := navFixture(t, here)
			server.extras = extras.New(t.TempDir(), []string{here}, nil)
			if tc.turnOn {
				if err := server.extras.SetOn(here, fixtureSuggestingKey, true); err != nil {
					t.Fatal(err)
				}
			}
			suggested.Lock()
			suggested.evidence, suggested.err, suggested.asked = tc.evidence, tc.err, nil
			suggested.Unlock()

			r := httptest.NewRequest(http.MethodGet, "/api/c/"+here+"/extensions/suggestions", nil)
			items, failure := apiClusters{s: server}.ExtensionSuggestions(r, here)
			if failure != nil {
				t.Fatalf("the suggestions failed as a list: %+v", failure)
			}

			suggested.Lock()
			asked := suggested.asked
			suggested.Unlock()
			if (len(asked) > 0) != tc.wantAsked {
				t.Fatalf("asked %d times, want asked=%t", len(asked), tc.wantAsked)
			}
			if tc.wantAsked && asked[0] != r {
				t.Error("the extension was not handed the person's own request")
			}
			got := ""
			for _, item := range items {
				if item.Key != fixtureSuggestingKey {
					t.Errorf("%s was suggested, and only %s suggests", item.Key, fixtureSuggestingKey)
				}
				got = item.Evidence
				if item.Absent != tc.absent {
					t.Errorf("absent = %t, want %t", item.Absent, tc.absent)
				}
			}
			if got != tc.want {
				t.Errorf("suggested %q, want %q", got, tc.want)
			}
		})
	}
}

// **The listing's `writable` follows extras.ProfileOf**, for all eight
// combinations of the three facts the router is given: the per-cluster switch
// exists exactly where the profile is Desktop or Server, and extensionSwitch
// reads it there rather than spelling a copy of the router's rule; a listing
// that offered a switch the router does not register would draw a control
// whose every press is a 404. The wanted answer
// is spelled from the profile's values, not through Profile.Switchable, so a
// wrong Switchable fails here too. And the refusal's sentence names the same
// profile, so the two halves of this file cannot disagree either.
func TestTheExtensionSwitchIsTheProfiles(t *testing.T) {
	for _, inCluster := range []bool{false, true} {
		for _, replicated := range []bool{false, true} {
			for _, ephemeral := range []bool{false, true} {
				profile := extras.ProfileOf(inCluster, replicated, ephemeral)
				want := profile == extras.Desktop || profile == extras.Server
				s := &Server{extras: extras.New("", nil, nil), inCluster: inCluster, replicated: replicated}
				s.setup.StateEphemeral = ephemeral
				if got := s.extensionSwitch(); got != want {
					t.Errorf("inCluster=%t replicated=%t ephemeral=%t: extensionSwitch() = %t, "+
						"want %t (profile %d)", inCluster, replicated, ephemeral, got, want, profile)
				}
				if got, want := s.howToTurnOn("<cluster>", ""), extras.HowToTurnOn("<cluster>", "", profile); got != want {
					t.Errorf("inCluster=%t replicated=%t ephemeral=%t: the refusal says\n  %q\nwant\n  %q",
						inCluster, replicated, ephemeral, got, want)
				}
			}
		}
	}
}
