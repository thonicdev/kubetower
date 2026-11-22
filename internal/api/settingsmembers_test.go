package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The settings payload: every member the contract declares.
//
// # What must be carried
//
//   - `SaveGeneral` and `Clear` must work, not return `ErrNotInProfile`;
//   - `Settings()` must fill `General` as well as `Info` and `NodeShell`, or
//     `general` travels as `{}`;
//   - `retentionChoices`, `archive` and the members of `info` each need a Go
//     field.
//
// Otherwise the General tab cannot be read *or* written, the retention dropdown
// has no list, and the Infos tab has a state path and one kubeconfig line.
//
// # The one that is not cosmetic
//
// `retentionChoices` is a list and not a range, and it **always contains the
// value it is editing**. A control that cannot represent its own current state
// does not merely look wrong: with retention at 21 no option would carry `selected`,
// the browser would submit the first, and a Save opened to tick an unrelated
// checkbox would destroy twenty days of archived events while saying "Saved." The
// fixture is deliberately at 21 for that reason.
//
// # Proving it fails
//
// Leaving `Archive` unset in `fakeSettings.Settings` and re-running the whole
// package:
//
//	--- FAIL: TestRealResponsesMatchTheSchemasInTheContract
//	    getSettings: body.archive.state: is , which is not in the enum
//	    saveSettings: body.archive.state: is , which is not in the enum
//
// That one is caught by the conformance test rather than by this: a
// **zero-value envelope** marshals as `{"state":""}`, which is not a state.
// Removing `RetentionChoices` from the fixture:
//
//	settingsmembers_test.go: the settings carry no retentionChoices; there is
//	nothing to measure
func TestTheSettingsPayloadCarriesEveryMemberTheContractDeclares(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	// A read and a write, unioned. `applied` is filled on the response to a write
	// and **absent on a plain read**, which is the contract's own rule, so
	// measuring only one of them would leave that member declared and unmeasured.
	read := settingsBody(t, mux, httptest.NewRequest(http.MethodGet, "/api/settings", nil))

	body, err := json.Marshal(SettingsWrite{RetentionDays: intPtr(14)})
	if err != nil {
		t.Fatalf("marshalling the write: %v", err)
	}
	write := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(body))
	write.Header.Set("Content-Type", "application/json")
	write.Header.Set("X-KubeTower-Request", "1")
	write.Header.Set("Origin", "http://localhost")
	written := settingsBody(t, mux, write)

	if _, ok := read["applied"]; ok {
		t.Error("a plain read carries `applied`; it is what the last write did, and a read " +
			"reporting one would be describing somebody else's write")
	}
	if _, ok := written["applied"]; !ok {
		t.Error("a write carries no `applied`; the one thing an operator needs to know about " +
			"a save that did not persist is that it is in effect anyway")
	}

	settings := membersOf(read)
	for member := range written {
		settings[member] = true
	}

	choices, ok := read["retentionChoices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatal("the settings carry no retentionChoices; there is nothing to measure — and " +
			"a dropdown with no list is a dropdown that submits its first option")
	}
	archive, ok := read["archive"].(map[string]any)
	if !ok {
		t.Fatal("the settings carry no archive envelope at all")
	}
	facts, ok := archive["data"].(map[string]any)
	if !ok {
		t.Fatalf("the archive envelope is on the %v arm and carries no data; there is nothing "+
			"to measure", archive["state"])
	}
	info, ok := read["info"].(map[string]any)
	if !ok || len(info) == 0 {
		t.Fatal("the settings carry no info")
	}
	shadowed, ok := info["kubeconfigShadowed"].([]any)
	if !ok || len(shadowed) == 0 {
		t.Fatal("info carries no kubeconfigShadowed; there is nothing to measure — and it is " +
			"the only explanation of a context that exists in a file and nowhere in this console")
	}

	for _, subject := range []struct {
		schema  string
		carried map[string]bool
	}{
		{"Settings", settings},
		{"SettingsWrite", membersOf(mapOf(t, read, "general"))},
		{"NodeShellSettings", membersOf(mapOf(t, read, "nodeShell"))},
	} {
		declared := schemaMembersInContract(t, subject.schema)
		for _, member := range sorted(declared) {
			if why, allowed := settingsMembersWithNoSource[subject.schema+"."+member]; allowed {
				t.Logf("%s.%s is declared and deliberately not carried: %s", subject.schema, member, why)
				continue
			}
			if !subject.carried[member] {
				t.Errorf("the contract declares %s.%s and the response does not carry it",
					subject.schema, member)
			}
		}
		for _, member := range sorted(subject.carried) {
			if !declared[member] {
				t.Errorf("the response carries %s.%s and the contract does not declare it",
					subject.schema, member)
			}
		}
	}

	// The four inline shapes, read at the paths they sit at.
	for _, subject := range []struct {
		what     string
		declared map[string]bool
		carried  map[string]bool
	}{
		{"a retention choice", inlineMembersInContract(t, "Settings", "retentionChoices"),
			unionOf(t, choices)},
		{"info", inlinePropertiesInContract(t, "Settings", "info"), membersOf(info)},
		{"a shadowed context",
			inlineMembersInContract(t, "Settings", "info", "kubeconfigShadowed"),
			unionOf(t, shadowed)},
		{"applied", inlinePropertiesInContract(t, "Settings", "applied"),
			membersOf(mapOf(t, written, "applied"))},
	} {
		for _, member := range sorted(subject.declared) {
			if !subject.carried[member] {
				t.Errorf("the contract declares %s's %q and the response does not carry it",
					subject.what, member)
			}
		}
		for _, member := range sorted(subject.carried) {
			if !subject.declared[member] {
				t.Errorf("%s carries %q and the contract does not declare it", subject.what, member)
			}
		}
	}

	// The update check, one hop below `info`.
	//
	// Measured rather than trusted, and the fixture is on the `newer` arm on
	// purpose: it is the only one that carries every member, so a fixture on the
	// comfortable arm would leave `latest`, `releaseUrl` and `checkedAt` declared
	// and unmeasured. **An optional member is satisfied by being absent**, which
	// is how members of `info` could reach the contract with no Go field behind
	// them at all.
	update, ok := info["update"].(map[string]any)
	if !ok || len(update) == 0 {
		t.Fatal("info carries no update; there is nothing to measure - and a page with no " +
			"way to tell `failed` from `current` is a page confirming a version nothing checked")
	}
	// A schema of its own, which Check now answers with.
	declaredUpdate := schemaMembersInContract(t, "SettingsUpdate")
	carriedUpdate := membersOf(update)
	for _, member := range sorted(declaredUpdate) {
		if !carriedUpdate[member] {
			t.Errorf("the contract declares the update check's %q and the response does not "+
				"carry it", member)
		}
	}
	for _, member := range sorted(carriedUpdate) {
		if !declaredUpdate[member] {
			t.Errorf("the update check carries %q and the contract does not declare it", member)
		}
	}

	// The archive facts are inside an `allOf`, so they are named here rather than
	// read out of the file: `components.schemas.Settings.properties.archive` has
	// no `properties` of its own, only the three branches it composes. Naming
	// them is weaker than reading them and it is said so rather than dressed up —
	// what it still catches is a member dropped from the response.
	for _, member := range []string{"path", "bytes", "days", "oldestAt", "writesRefused", "note"} {
		if _, ok := facts[member]; !ok {
			t.Errorf("the archive facts carry no %q", member)
		}
	}
}

func settingsBody(t *testing.T, mux *http.ServeMux, request *http.Request) map[string]any {
	t.Helper()
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s %s answered %d: %s", request.Method, request.URL.Path,
			recorder.Code, recorder.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatalf("the settings are not an object: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("the settings are empty; there is nothing to measure")
	}
	return out
}

func mapOf(t *testing.T, object map[string]any, key string) map[string]any {
	t.Helper()
	nested, ok := object[key].(map[string]any)
	if !ok {
		t.Fatalf("the response carries no %q object", key)
	}
	return nested
}

// nestedPropertiesInContract is inlinePropertiesInContract one hop deeper: the
// properties of an inline object inside another inline object.
//
// Its own function rather than a variadic `inlinePropertiesInContract`, for the
// reason that one gives for not being `inlineMembersInContract` with a flag —
// a path expression with an arity in it is read wrong the first time.
func nestedPropertiesInContract(t *testing.T, schema string, path ...string) map[string]bool {
	t.Helper()
	if len(path) < 2 {
		t.Fatal("nestedPropertiesInContract needs at least two member names")
	}
	properties := schemaPropertiesInContract(t, schema)
	for _, hop := range path {
		object, ok := properties[hop].(map[string]any)
		if !ok {
			t.Fatalf("%s declares no %s", schema, hop)
		}
		nested, ok := object["properties"].(map[string]any)
		if !ok || len(nested) == 0 {
			t.Fatalf("%s.%s declares no properties", schema, hop)
		}
		properties = nested
	}
	out := make(map[string]bool, len(properties))
	for name := range properties {
		out[name] = true
	}
	return out
}

// inlinePropertiesInContract is the properties of an inline **object** member,
// as opposed to `inlineMembersInContract`'s inline *array item*.
//
// Two functions because the two paths differ by one hop and a single one taking
// a flag would be a path expression with a boolean in it.
func inlinePropertiesInContract(t *testing.T, schema, member string) map[string]bool {
	t.Helper()
	properties := schemaPropertiesInContract(t, schema)
	object, ok := properties[member].(map[string]any)
	if !ok {
		t.Fatalf("%s declares no %s", schema, member)
	}
	inline, ok := object["properties"].(map[string]any)
	if !ok || len(inline) == 0 {
		t.Fatalf("%s.%s declares no properties", schema, member)
	}
	out := make(map[string]bool, len(inline))
	for name := range inline {
		out[name] = true
	}
	return out
}

// settingsMembersWithNoSource is the deliberate exceptions, each with the
// evidence — and the one entry here is a **finding to report** rather than a
// gap to fill quietly.
//
//	NodeShellSettings.namespace
//	    Where the debug pod is created. It is a fact and it exists:
//
//	    $ grep -n "nodeShellNamespace" internal/terminal/nodeshell.go
//
//	    It is **unexported**, so `internal/web` cannot read it, and it is **not
//	    settable**: `internal/nodeshell` has no namespace at all, and
//	    `SetNodeShellImage` is the only writer on that surface.
//
//	    So the contract declares, in a body with `additionalProperties: false`, a
//	    property a client may send and the server silently ignores. Either the
//	    member is read-only like `defaultImage` beside it, or the console grows
//	    a control for it. Exporting the constant to fill the read half is a
//	    one-line change to `internal/terminal`.
//
// An entry belongs here only with the search that established the absence.
var settingsMembersWithNoSource = map[string]string{
	"NodeShellSettings.namespace": "internal/terminal.nodeShellNamespace is unexported " +
		"and internal/nodeshell has no namespace to set; the contract declares a writable " +
		"member the server ignores, which is reported rather than closed",
}
