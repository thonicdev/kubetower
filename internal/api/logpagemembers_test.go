package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The same shape as `listmembers_test.go`, for the same reason, on the members
// of a log page.
//
// **Every optional member is satisfied by being absent**, so the response
// conformance test cannot see a member that is declared and never sent. `stop`
// and `note` are both optional — they have to be, a page with more to come
// carries neither — which puts them in exactly the class that becomes dead code
// when a member is declared, emitted by nothing, and branched on by a client
// whose generated types say it is required.
//
// So this asserts both directions against the YAML read at run time: the contract
// declares nothing this response does not carry, and the response carries nothing
// the contract does not declare.
//
// # What it does not check
//
// It drives the real handler over `fakeDock`, so it proves the contract type can
// carry every declared member and that the handler emits what it is given. It
// cannot prove `internal/web` fills them from `logs.Streamer.Earlier` — that
// package imports this one. `TestThePageBackwardsIsFilledFromTheStreamerThatPagesIt`
// in `internal/web` is the other half, and neither is sufficient alone.
func TestTheLogPageCarriesEveryMemberTheContractDeclares(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/api/logs?cluster=prod&kind=pods&namespace=default&name=api-1&before=200&page=500", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/logs?before=200 answered %d: %s", recorder.Code, recorder.Body.String())
	}
	// The stream is `text/event-stream` and this is not. A page backwards that
	// came back as a stream would satisfy every assertion below over an empty
	// map, and report success.
	if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("a page backwards answered %q; with `before` this operation is a "+
			"bounded JSON read and not a stream", got)
	}

	var envelope struct {
		State string         `json:"state"`
		Data  map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("the answer is not an envelope: %v", err)
	}
	if envelope.State != string(StateOK) || len(envelope.Data) == 0 {
		t.Fatalf("state=%q with a %d-member page; this test measures a page that has "+
			"something in it", envelope.State, len(envelope.Data))
	}

	// `lines` is an array **and never null**, which is a claim about the value
	// rather than about the member set — so neither the loop below nor the schema
	// validator next door sees it. A nil Go slice marshals to `null`, a client
	// reads `.length` on it in the middle of a handler whose whole job is to say
	// what happened, and the TypeError that follows is attributed to the log
	// rather than to the envelope.
	if _, ok := envelope.Data["lines"].([]any); !ok {
		t.Errorf("lines is %#v, want an array; `[]` and `null` are not the same answer",
			envelope.Data["lines"])
	}

	declared := schemaMembersInContract(t, "LogPage")
	carried := membersOf(envelope.Data)
	for _, member := range sorted(declared) {
		if !carried[member] {
			t.Errorf("the contract declares LogPage.%s and the response does not carry it; "+
				"either fill it from the producer that holds it, or record why it has no source",
				member)
		}
	}
	for _, member := range sorted(carried) {
		if !declared[member] {
			t.Errorf("the response carries LogPage.%s and the contract does not declare it; "+
				"this is how `Cluster.group` survived a contract that had removed it", member)
		}
	}
}

// The JSON arm of `streamLogs`, put through the schema the contract declares
// for it.
//
// `TestRealResponsesMatchTheSchemasInTheContract` exercises one shape per
// operation, and this operation has two: `text/event-stream` without
// `before` and `application/json` with it. The suite's run order is keyed by
// operationId, so the second shape has nowhere to sit there; `responseSchema`
// must pick the media type the response used rather than whichever the
// contract's map yields first. This is the arm that run leaves unexercised.
func TestThePageBackwardsMatchesTheSchemaTheContractDeclares(t *testing.T) {
	spec := loadSpec(t)
	check := &validator{root: spec}

	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/api/logs?cluster=prod&kind=pods&namespace=default&name=api-1&before=200", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("the page answered %d: %s", recorder.Code, recorder.Body.String())
	}

	// Taken out of the operation rather than named here, so that moving the
	// schema in the YAML fails this test instead of being agreed with.
	declared := jsonArmOf(t, spec, "/api/logs", "200")
	for _, problem := range validateJSON(check, declared, recorder.Body.Bytes(), "body") {
		t.Errorf("the page backwards does not match its own schema: %s", problem)
	}
}

// jsonArmOf is the `application/json` schema one operation declares for one
// status.
func jsonArmOf(t *testing.T, spec map[string]any, path, status string) schema {
	t.Helper()
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatal("the contract declares no paths")
	}
	item, ok := paths[path].(map[string]any)
	if !ok {
		t.Fatalf("the contract declares no path %q", path)
	}
	get, ok := item["get"].(map[string]any)
	if !ok {
		t.Fatalf("%s declares no get", path)
	}
	responses, ok := get["responses"].(map[string]any)
	if !ok {
		t.Fatalf("GET %s declares no responses", path)
	}
	answer, ok := responses[status].(map[string]any)
	if !ok {
		t.Fatalf("GET %s declares no %s", path, status)
	}
	content, ok := answer["content"].(map[string]any)
	if !ok {
		t.Fatalf("GET %s %s declares no content", path, status)
	}
	media, ok := content["application/json"].(map[string]any)
	if !ok {
		t.Fatalf("GET %s %s declares no application/json arm; a page backwards is a "+
			"bounded read and cannot be described by the stream's schema", path, status)
	}
	found, ok := asSchema(media["schema"])
	if !ok {
		t.Fatalf("GET %s %s application/json declares no schema", path, status)
	}
	return found
}

// The stop reason is a member a client branches on, so it has to be one of the
// three the contract enumerates and not whatever `internal/logs` happens to
// spell.
//
// Read out of the YAML rather than restated here: a test that listed the three
// names it had just copied from types.go would agree with the constants by
// construction.
func TestTheThreeStopReasonsAreTheOnesTheContractEnumerates(t *testing.T) {
	spec := loadSpec(t)
	components := spec["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	schema, ok := schemas["LogPage"].(map[string]any)
	if !ok {
		t.Fatal("the contract declares no LogPage schema")
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("the LogPage schema declares no properties")
	}
	stop, ok := properties["stop"].(map[string]any)
	if !ok {
		t.Fatal("the LogPage schema declares no stop")
	}
	values, ok := stop["enum"].([]any)
	if !ok || len(values) == 0 {
		t.Fatal("LogPage.stop declares no enum; a client cannot branch on an open string")
	}

	declared := map[string]bool{}
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("a stop reason is not a string: %T", value)
		}
		declared[text] = true
	}
	for _, reason := range []LogStop{LogStopBeginning, LogStopCeiling, LogStopDense} {
		if !declared[string(reason)] {
			t.Errorf("this package can produce stop=%q and the contract does not enumerate it", reason)
		}
		delete(declared, string(reason))
	}
	for reason := range declared {
		t.Errorf("the contract enumerates stop=%q and nothing in this package produces it", reason)
	}
}

// A page backwards is refused in words, because a client that got the arithmetic
// wrong will get it wrong again and a 400 with no body says nothing about which
// of the two numbers it was.
func TestABadPageBackwardsSaysWhichNumberWasWrong(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	for _, subject := range []struct {
		query string
		says  string
	}{
		{"before=0", "before must be"},
		{"before=nonsense", "before must be"},
		{"before=200&page=0", "page must be"},
		{"before=200&page=nonsense", "page must be"},
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
			"/api/logs?cluster=prod&kind=pods&namespace=default&name=api-1&"+subject.query, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s answered %d, want 400: %s", subject.query, recorder.Code, recorder.Body.String())
			continue
		}
		var problem Problem
		if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
			t.Errorf("%s: the refusal is not a problem object: %v", subject.query, err)
			continue
		}
		if !strings.Contains(problem.Detail, subject.says) {
			t.Errorf("%s was refused with %q, which does not say %q", subject.query, problem.Detail, subject.says)
		}
	}
}
