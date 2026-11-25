package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The arms of the preference writes that the conformance run cannot reach.
//
// # Why this file exists at all
//
// `TestEveryRequestBodyMatchesTheContract` validates one body per operation,
// and its coverage half asserts exactly that — one exercise per operation, no
// more. That is the right shape for a drift test and it leaves a hole: a write
// schema can change in ways the existing exercise bodies
// still satisfy, so the suite stays green while the Go cannot serve what
// the contract declares.
//
//   - `RailMoveWrite` is a `oneOf` over `direction` and `order`. The
//     exercise sends `order`, so nothing would
//     notice that `direction` reached no handler at all.
//   - `ColumnPlaceWrite` has `target` and `edge` beside `order`, the same
//     way and with the same blind spot.
//   - `RailNameplateWrite` has `initials`, `colour` and `reset`. The
//     exercise sends the first two; `reset` is a third arm and a body
//     carrying only `cluster` is a fourth.
//
// # What each test here pins, and how
//
// Every case does two things, and the second is the one that makes it worth
// more than a handler test. It puts the body **through the contract's own
// schema** before sending it, so a case cannot pin a shape the contract refuses;
// and it records what reached the store, so a handler that accepts a body and
// quietly does nothing with half of it fails. The first alone would be a test of
// the fixture. The second alone is how a write that ignores half its body goes
// unnoticed.

// recordingPrefs is fakePrefs with a memory. Embedded rather than rewritten, so
// the read half and the writes nothing here exercises stay one implementation.
type recordingPrefs struct {
	fakePrefs
	calls []string
}

func (p *recordingPrefs) SetRailBar(bar int) error {
	p.note("SetRailBar", bar)
	return nil
}

func (p *recordingPrefs) SetRailAssign(cluster string, bar int) error {
	p.note("SetRailAssign", cluster, bar)
	return nil
}

func (p *recordingPrefs) SetRailMove(cluster string, order int) error {
	p.note("SetRailMove", cluster, order)
	return nil
}

func (p *recordingPrefs) SetRailStep(cluster string, delta int) error {
	p.note("SetRailStep", cluster, delta)
	return nil
}

func (p *recordingPrefs) SetRailPlace(cluster, target string, after bool) error {
	p.note("SetRailPlace", cluster, target, after)
	return nil
}

func (p *recordingPrefs) SetRailNameplate(body RailNameplateWrite) error {
	// Written out member by member, with a nil spelled differently from an empty
	// string, because that difference is the whole subject of the nameplate
	// tests below: absent leaves a part alone, empty returns it to the derived
	// form, and a recorder that rendered both as "" could not tell them apart.
	p.note("SetRailNameplate", body.Cluster,
		said(body.Label), said(body.Initials), said(body.Colour), saidBool(body.Reset))
	return nil
}

func (p *recordingPrefs) SetColumnOrder(kind, column string, order int) error {
	p.note("SetColumnOrder", kind, column, order)
	return nil
}

func (p *recordingPrefs) SetColumnPlace(kind, column, target string, after bool) error {
	p.note("SetColumnPlace", kind, column, target, after)
	return nil
}

func (p *recordingPrefs) SetColumnDensity(density string) error {
	p.note("SetColumnDensity", density)
	return nil
}

func (p *recordingPrefs) SetSeenVersion(version string) error {
	p.note("SetSeenVersion", version)
	return nil
}

func (p *recordingPrefs) ResetColumns(kind, scope string) error {
	p.note("ResetColumns", kind, scope)
	return nil
}

func (p *recordingPrefs) note(name string, args ...any) {
	parts := []string{name}
	for _, arg := range args {
		parts = append(parts, sprint(arg))
	}
	p.calls = append(p.calls, strings.Join(parts, " "))
}

func sprint(v any) string { return fmt.Sprintf("%v", v) }

// said renders an optional string so that "not said" and "said empty" are two
// different strings in the recording.
func said(v *string) string {
	if v == nil {
		return "<absent>"
	}
	return "=" + *v
}

func saidBool(v *bool) string {
	if v == nil {
		return "<absent>"
	}
	return "=" + sprint(*v)
}

// armCase is one body, the operation it belongs to, and what it should produce.
type armCase struct {
	what   string
	id     string
	path   string
	body   string
	status int
	// call is what the store should have been told. Empty means the store must
	// not have been touched at all — which is the assertion that separates a
	// refusal from a handler that refused and wrote anyway.
	call string
}

// runArms sends each body and checks both directions: the contract accepts it,
// and the handler turned it into the store call it should have.
func runArms(t *testing.T, cases []armCase) {
	t.Helper()

	spec := loadSpec(t)
	check := &validator{root: spec}
	byID := map[string]operation{}
	for _, op := range operationsIn(t, spec) {
		byID[op.id] = op
	}

	for _, one := range cases {
		t.Run(one.what, func(t *testing.T) {
			op, known := byID[one.id]
			if !known {
				t.Fatalf("%s is not an operation in the contract", one.id)
			}

			// The body first, against the contract. A case that pins a shape the
			// schema refuses would be a case teaching the handler to accept
			// something no generated client can send.
			schemaFor, _, declared := requestSchema(op)
			if !declared {
				t.Fatalf("%s declares no request body", one.id)
			}
			// Only the accepted cases are held to it. A refused body may be one
			// the contract refuses too — most of them here are — or one it
			// permits and this server will not, which is a judgment the handler
			// is entitled to make and is commented at the case that makes it.
			problems := validateJSON(check, schemaFor, []byte(one.body), "body")
			if one.status == http.StatusNoContent && len(problems) > 0 {
				t.Fatalf("this body is meant to be accepted and the contract refuses it: %s\n%v",
					one.body, problems)
			}

			prefs := &recordingPrefs{}
			mux := http.NewServeMux()
			New(Deps{
				Sessions: fakeSessions{}, Profile: fakeProfile{}, Prefs: prefs,
				AllowOrigin: func(*http.Request, string) bool { return true },
			}).Mount(mux)

			request := httptest.NewRequest(http.MethodPost, one.path, strings.NewReader(one.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-KubeTower-Request", "1")
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)

			if recorder.Code != one.status {
				t.Fatalf("%d, expected %d: %s", recorder.Code, one.status, recorder.Body.String())
			}
			got := strings.Join(prefs.calls, "; ")
			if got != one.call {
				t.Errorf("the store was told %q, expected %q", got, one.call)
			}
		})
	}
}

// TestTheDirectionArmOfSetRailMoveReachesTheStore pins `direction`, which is the
// arm the two arrows send.
//
// It pins the translation as well as the route: the store moves a tile by a
// delta, so `up` is -1 and `down` is +1, and a handler that had them the wrong
// way round would send every tile the opposite way with a 204 saying it worked.
func TestTheDirectionArmOfSetRailMoveReachesTheStore(t *testing.T) {
	runArms(t, []armCase{
		{what: "up is one place towards the start", id: "setRailMove",
			path:   "/api/preferences/rail/move",
			body:   `{"cluster":"prod","direction":"up"}`,
			status: http.StatusNoContent, call: "SetRailStep prod -1"},
		{what: "down is one place towards the end", id: "setRailMove",
			path:   "/api/preferences/rail/move",
			body:   `{"cluster":"prod","direction":"down"}`,
			status: http.StatusNoContent, call: "SetRailStep prod 1"},
		{what: "the absolute arm still works", id: "setRailMove",
			path:   "/api/preferences/rail/move",
			body:   `{"cluster":"prod","order":3}`,
			status: http.StatusNoContent, call: "SetRailMove prod 3"},
		// The two refusals the `oneOf` exists for. Both are bodies the contract
		// itself rejects, so a client generated from it cannot build them — which
		// is exactly why the handler has to, since a hand-written one can.
		{what: "both arms says nothing", id: "setRailMove",
			path:   "/api/preferences/rail/move",
			body:   `{"cluster":"prod","direction":"up","order":3}`,
			status: http.StatusBadRequest, call: ""},
		{what: "neither arm says nothing either", id: "setRailMove",
			path:   "/api/preferences/rail/move",
			body:   `{"cluster":"prod"}`,
			status: http.StatusBadRequest, call: ""},
		{what: "sideways is not a direction", id: "setRailMove",
			path:   "/api/preferences/rail/move",
			body:   `{"cluster":"prod","direction":"sideways"}`,
			status: http.StatusBadRequest, call: ""},
	})
}

// TestTheTargetArmOfSetColumnPlaceReachesTheStore pins `target` and `edge`,
// which the store's own operation takes and the API must reach.
func TestTheTargetArmOfSetColumnPlaceReachesTheStore(t *testing.T) {
	runArms(t, []armCase{
		{what: "a target with no edge lands before it", id: "setColumnPlace",
			path:   "/api/preferences/columns/place",
			body:   `{"kind":"pods","column":"Node","target":"Name"}`,
			status: http.StatusNoContent, call: "SetColumnPlace pods Node Name false"},
		{what: "edge after lands after it", id: "setColumnPlace",
			path:   "/api/preferences/columns/place",
			body:   `{"kind":"pods","column":"Node","target":"Name","edge":"after"}`,
			status: http.StatusNoContent, call: "SetColumnPlace pods Node Name true"},
		{what: "the absolute arm still works", id: "setColumnPlace",
			path:   "/api/preferences/columns/place",
			body:   `{"kind":"pods","column":"Node","order":0}`,
			status: http.StatusNoContent, call: "SetColumnOrder pods Node 0"},
		{what: "both arms says nothing", id: "setColumnPlace",
			path:   "/api/preferences/columns/place",
			body:   `{"kind":"pods","column":"Node","target":"Name","order":0}`,
			status: http.StatusBadRequest, call: ""},
		{what: "neither arm says nothing either", id: "setColumnPlace",
			path:   "/api/preferences/columns/place",
			body:   `{"kind":"pods","column":"Node"}`,
			status: http.StatusBadRequest, call: ""},
		// An edge with no target names the side of nothing. The contract permits
		// the combination — its `oneOf` constrains only which of target and order
		// is present — so this refusal is the handler's own, and it is a refusal
		// rather than a shrug because a member that changes nothing reads as a
		// promise about which way the column moved.
		{what: "an edge with no target is refused rather than dropped", id: "setColumnPlace",
			path:   "/api/preferences/columns/place",
			body:   `{"kind":"pods","column":"Node","order":0,"edge":"after"}`,
			status: http.StatusBadRequest, call: ""},
	})
}

// TestTheNameplateWriteCarriesAllThreeParts pins what a nameplate is: what the
// tile says, what it says when there is no room, and what color it is.
//
// The assertions that matter are the two about **absence**. A body naming only
// the color must leave the label alone, and a handler that filled the unsaid
// members with "" would blank the name of every cluster anybody recolored — a
// 204 each time, and nothing to read afterwards that would say what happened.
func TestTheNameplateWriteCarriesAllThreeParts(t *testing.T) {
	runArms(t, []armCase{
		{what: "all three at once", id: "setRailNameplate",
			path:   "/api/preferences/rail/nameplate",
			body:   `{"cluster":"prod","label":"Production","initials":"PR","colour":"amber"}`,
			status: http.StatusNoContent,
			call:   "SetRailNameplate prod =Production =PR =amber <absent>"},
		{what: "the color alone leaves the rest unsaid", id: "setRailNameplate",
			path:   "/api/preferences/rail/nameplate",
			body:   `{"cluster":"prod","colour":"teal"}`,
			status: http.StatusNoContent,
			call:   "SetRailNameplate prod <absent> <absent> =teal <absent>"},
		{what: "an empty member is said, and says derive it", id: "setRailNameplate",
			path:   "/api/preferences/rail/nameplate",
			body:   `{"cluster":"prod","label":""}`,
			status: http.StatusNoContent,
			call:   "SetRailNameplate prod = <absent> <absent> <absent>"},
		{what: "reset clears the lot in one gesture", id: "setRailNameplate",
			path:   "/api/preferences/rail/nameplate",
			body:   `{"cluster":"prod","reset":true}`,
			status: http.StatusNoContent,
			call:   "SetRailNameplate prod <absent> <absent> <absent> =true"},
		// The schema's maxLength is in characters, so the refusal has to be too.
		{what: "a label longer than the schema allows", id: "setRailNameplate",
			path:   "/api/preferences/rail/nameplate",
			body:   `{"cluster":"prod","label":"` + strings.Repeat("é", 129) + `"}`,
			status: http.StatusBadRequest, call: ""},
	})
}

// TestTheCorrectedRailAndDensityWritesReachTheStore is the other four, and it is
// deliberately not about arms: what is pinned is that each member reaches the
// store call behind it.
func TestTheCorrectedRailAndDensityWritesReachTheStore(t *testing.T) {
	runArms(t, []armCase{
		{what: "a bar is a number and not a boolean", id: "setRailBar",
			path: "/api/preferences/rail/bar", body: `{"bar":2}`,
			status: http.StatusNoContent, call: "SetRailBar 2"},
		{what: "hotbars are numbered from 1", id: "setRailBar",
			path: "/api/preferences/rail/bar", body: `{"bar":0}`,
			status: http.StatusBadRequest, call: ""},
		{what: "assign names a bar and not a slot", id: "setRailAssign",
			path: "/api/preferences/rail/assign", body: `{"cluster":"prod","bar":3}`,
			status: http.StatusNoContent, call: "SetRailAssign prod 3"},
		{what: "place names a neighbor and not a group", id: "setRailPlace",
			path:   "/api/preferences/rail/place",
			body:   `{"cluster":"prod","target":"staging"}`,
			status: http.StatusNoContent, call: "SetRailPlace prod staging false"},
		{what: "an edge of after is the other side", id: "setRailPlace",
			path:   "/api/preferences/rail/place",
			body:   `{"cluster":"prod","target":"staging","edge":"after"}`,
			status: http.StatusNoContent, call: "SetRailPlace prod staging true"},
		{what: "density is the console's and carries no kind", id: "setColumnDensity",
			path: "/api/preferences/columns/density", body: `{"density":"comfortable"}`,
			status: http.StatusNoContent, call: "SetColumnDensity comfortable"},
		{what: "the version seen is a release version", id: "setSeenVersion",
			path: "/api/preferences/seen-version", body: `{"version":"0.2.0"}`,
			status: http.StatusNoContent, call: "SetSeenVersion 0.2.0"},
		{what: "a version that is not a release is refused", id: "setSeenVersion",
			path: "/api/preferences/seen-version", body: `{"version":"dev"}`,
			status: http.StatusBadRequest},
	})
}

// TestTheScopeArmOfResetColumnsReachesTheStore pins the split of the reset.
//
// Widths, order and hidden columns are three arrangements made by three
// gestures, and one verb must not undo all three: a user who
// wanted the default widths back must not pay their column order for them
// without being told. `internal/columns.Scope` carries the whole argument, including why
// `widths` is the one that adds a way back rather than a shortcut to one.
//
// **Three things are pinned here and the third is the one worth the file.** That
// each scope reaches the store; that an absent scope is `all`, because that is
// what this write meant before the field existed and a client written against
// the old contract has to keep the behavior it asked for; and that a scope
// nobody serves is refused **without touching the store** — `call: ""`. The last
// is not pedantry: `all` is the most destructive of the four, so a scope that
// fell through to it would forget the two arrangements the caller was trying to
// keep.
func TestTheScopeArmOfResetColumnsReachesTheStore(t *testing.T) {
	runArms(t, []armCase{
		{what: "no scope is still the whole kind", id: "resetColumns",
			path: "/api/preferences/columns/reset", body: `{"kind":"pods"}`,
			status: http.StatusNoContent, call: "ResetColumns pods all"},
		{what: "all, said out loud", id: "resetColumns",
			path: "/api/preferences/columns/reset", body: `{"kind":"pods","scope":"all"}`,
			status: http.StatusNoContent, call: "ResetColumns pods all"},
		{what: "the widths alone", id: "resetColumns",
			path: "/api/preferences/columns/reset", body: `{"kind":"pods","scope":"widths"}`,
			status: http.StatusNoContent, call: "ResetColumns pods widths"},
		{what: "the order alone", id: "resetColumns",
			path: "/api/preferences/columns/reset", body: `{"kind":"pods","scope":"order"}`,
			status: http.StatusNoContent, call: "ResetColumns pods order"},
		{what: "the hidden columns alone", id: "resetColumns",
			path: "/api/preferences/columns/reset", body: `{"kind":"pods","scope":"hidden"}`,
			status: http.StatusNoContent, call: "ResetColumns pods hidden"},
		// Refused by the contract's enum too, which is the point: a generated
		// client cannot build these, and a hand-written one can.
		{what: "a scope nobody serves resets nothing", id: "resetColumns",
			path: "/api/preferences/columns/reset", body: `{"kind":"pods","scope":"everything"}`,
			status: http.StatusBadRequest, call: ""},
		{what: "an empty scope is not an absent one", id: "resetColumns",
			path: "/api/preferences/columns/reset", body: `{"kind":"pods","scope":""}`,
			status: http.StatusBadRequest, call: ""},
	})
}
