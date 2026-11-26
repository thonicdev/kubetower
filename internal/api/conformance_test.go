package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Real handler responses, through the schemas in `api/openapi.yaml`.
//
// # Why this exists at all
//
// Nothing regenerates that file. It is hand-written,
// and the only thing standing between it and fiction is this. Without it the spec
// and the handlers drift in the first week and nobody notices until a generated
// client breaks against a server that has been wrong for a month.
//
// # Why it reads the YAML
//
// **A drift test that duplicates the spec tests that two copies agree.** It would
// pass a handler and a Go struct that are wrong together, which is exactly the
// case that matters. So the schemas come out of the file, at run time, through a
// generic evaluator that knows nothing about this product — see
// jsonschema_test.go. Editing `api/openapi.yaml` changes what this enforces on
// the next run, with no second edit anywhere.
//
// It is strictly stronger than a regeneration diff, which can only ever confirm
// that a generator ran: this can fail because the server is wrong.

const specPath = "../../api/openapi.yaml"

func loadSpec(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("the contract could not be read: %v", err)
	}
	// YAML to JSON first, so the evaluator walks the same value shapes
	// `encoding/json` produces for a response — float64 for every number, and
	// map[string]any for every object. Two different decoders would make
	// "matches the schema" mean two things.
	converted, err := yaml.YAMLToJSON(raw)
	if err != nil {
		t.Fatalf("the contract is not YAML: %v", err)
	}
	var spec map[string]any
	if err := json.Unmarshal(converted, &spec); err != nil {
		t.Fatalf("the contract is not an object: %v", err)
	}
	return spec
}

// operationsIn is every operationId in the file, with the address and method it
// is reachable at. The test's coverage assertion is built from this, so an
// operation added to the contract fails this package until it is exercised.
type operation struct {
	id        string
	method    string
	path      string
	responses map[string]any
	// request is the operation's `requestBody`, kept for the same reason
	// `responses` is: what the server is *sent* is half the contract.
	request map[string]any
}

func operationsIn(t *testing.T, spec map[string]any) []operation {
	t.Helper()
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatal("the contract declares no paths")
	}
	var out []operation
	for path, raw := range paths {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for method, rawOp := range item {
			op, ok := rawOp.(map[string]any)
			if !ok {
				continue
			}
			id, ok := op["operationId"].(string)
			if !ok {
				continue
			}
			responses, _ := op["responses"].(map[string]any)
			request, _ := op["requestBody"].(map[string]any)
			out = append(out, operation{
				id: id, method: strings.ToUpper(method), path: path,
				responses: responses, request: request,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// --- The exercises -----------------------------------------------------------

// exercise is one call this test makes, and what it expects back.
//
// `body` and `path` are functions of a small state map so that the act half of a
// confirm-then-act pair can spend the token its preview returned. That is not
// test plumbing for its own sake: the whole mechanism under test is that an act
// takes a token rather than a thing to do, and an exercise that posted a made-up
// token would validate the 409 instead of the outcome.
type exercise struct {
	status int
	path   func(state map[string]string) string
	body   func(state map[string]string) string
	// keep records something out of the response for a later exercise.
	keep func(state map[string]string, body []byte)
}

const testCluster = "prod"

func at(path string) func(map[string]string) string {
	return func(map[string]string) string { return path }
}

func sends(body string) func(map[string]string) string {
	return func(map[string]string) string { return body }
}

// keepToken pulls the plan token out of a ConfirmationPlan.
func keepToken(name string) func(map[string]string, []byte) {
	return func(state map[string]string, body []byte) {
		var plan struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal(body, &plan)
		state[name] = plan.Token
	}
}

func spends(name string) func(map[string]string) string {
	return func(state map[string]string) string {
		return `{"token":` + strconv.Quote(state[name]) + `}`
	}
}

// order is the sequence the exercises run in. It is explicit because four of
// them depend on a token an earlier one produced, and a map has no order.
var order = []string{
	"liveness", "readiness",
	"launchSignIn", "handoffLaunch",
	"login", "logout", "getSignInMethods", "startSingleSignOn", "finishSingleSignOn",
	"getSession", "getCapabilities",
	"listClusters", "probeCluster", "getNav", "getNavGroup", "getNavOccupancy", "listExtensions", "listExtensionSuggestions", "setExtensionEnabled",
	"setExtensionSetting",
	"getSettings", "saveSettings", "saveNodeShellSettings", "getHealth",
	"listNamespaces",
	"listResources", "getClusterScopedResource", "getNamespacedResource",
	"listHelmReleases", "getHelmRelease", "revealHelmValues", "revealHelmNotes",
	"getOverview", "getMetricsPanel", "getChangesPanel", "revealEnvValue",
	"getDataPanel", "getManifest",
	"previewData", "applyData",
	"previewManifest", "applyManifest",
	"getCreateTemplate", "previewCreate", "applyCreate",
	"previewAction", "runAction",
	"previewBulk", "runBulk",
	"previewNodeShell",
	"getPrereq", "previewPrereq", "installPrereq",
	"checkUpdates",
	"getUpdateInstall", "setUpdateInstall", "downloadUpdate", "installUpdate", "openUpdateFolder",
	"clearState",
	"getKindPreferences", "setKindPreferences",
	"getPreferences",
	"setRailBar", "setRailAssign", "setRailMove", "setRailPlace", "setRailHide",
	"setRailReveal", "setRailNameplate",
	"setColumnSize", "setColumnPlace", "setColumnShow", "setColumnDensity", "resetColumns",
	"setSeenVersion",
	"openDesktopTerminal", "listLogContainers", "streamLogs",
	"listForwards", "openForward", "closeForward", "openForwardWindow",
}

func exercises() map[string]exercise {
	object := "?kind=pods&namespace=default&name=api-1"
	return map[string]exercise{
		"liveness":  {status: 200, path: at("/healthz")},
		"readiness": {status: 200, path: at("/readyz")},

		"launchSignIn":       {status: 302, path: at("/auth/launch?code=c")},
		"handoffLaunch":      {status: 200, path: at("/auth/handoff?native=1")},
		"login":              {status: 204, path: at("/api/auth/login"), body: sends(`{"user":"u","password":"p"}`)},
		"logout":             {status: 204, path: at("/api/auth/logout"), body: sends(``)},
		"getSignInMethods":   {status: 200, path: at("/api/auth/methods")},
		"startSingleSignOn":  {status: 302, path: at("/api/auth/oidc/start?next=%2Fc%2Fprod%2Foverview")},
		"finishSingleSignOn": {status: 303, path: at("/api/auth/oidc/callback?code=c&state=s")},
		"getSession":         {status: 200, path: at("/api/session")},
		"getCapabilities":    {status: 200, path: at("/api/capabilities")},

		"listClusters":             {status: 200, path: at("/api/clusters")},
		"probeCluster":             {status: 200, path: at("/api/c/prod/probe")},
		"getNav":                   {status: 200, path: at("/api/c/prod/nav")},
		"getNavGroup":              {status: 200, path: at("/api/c/prod/nav/custom-resources")},
		"getNavOccupancy":          {status: 200, path: at("/api/c/prod/nav/occupancy")},
		"listExtensions":           {status: 200, path: at("/api/c/prod/extensions")},
		"listExtensionSuggestions": {status: 200, path: at("/api/c/prod/extensions/suggestions")},
		"setExtensionEnabled": {status: 204, path: at("/api/c/prod/extensions/sample-extra"),
			body: sends(`{"enabled":true}`)},
		"setExtensionSetting": {status: 204, path: at("/api/c/prod/extensions/sample-extra/settings/list-column"),
			body: sends(`{"value":false}`)},

		"getSettings": {status: 200, path: at("/api/settings")},
		"saveSettings": {status: 200, path: at("/api/settings"),
			body: sends(`{"archiveEnabled":true,"captureLogs":false,"retentionDays":7}`)},
		"saveNodeShellSettings": {status: 200, path: at("/api/settings/nodeshell"),
			body: sends(`{"enabled":true,"image":"busybox:1.36"}`)},
		"getHealth": {status: 200, path: at("/api/health")},

		"listNamespaces":           {status: 200, path: at("/api/c/prod/namespaces")},
		"listResources":            {status: 200, path: at("/api/c/prod/r/pods?namespace=default")},
		"getClusterScopedResource": {status: 200, path: at("/api/c/prod/r/nodes/node-1")},
		"getNamespacedResource":    {status: 200, path: at("/api/c/prod/r/pods/default/api-1")},
		"listHelmReleases":         {status: 200, path: at("/api/c/prod/helm")},
		"getHelmRelease":           {status: 200, path: at("/api/c/prod/helm/default/ingress")},
		"revealHelmValues":         {status: 200, path: at("/api/c/prod/helm/default/ingress/values?revision=41")},
		"revealHelmNotes":          {status: 200, path: at("/api/c/prod/helm/default/ingress/notes?revision=41")},

		"getOverview":     {status: 200, path: at("/api/c/prod/overview")},
		"getMetricsPanel": {status: 200, path: at("/api/c/prod/metrics" + object + "&range=6h")},
		"getChangesPanel": {status: 200, path: at("/api/c/prod/changes" + object)},
		"revealEnvValue":  {status: 200, path: at("/api/c/prod/env" + object + "&key=CONNECTION_URL")},
		"getDataPanel":    {status: 200, path: at("/api/c/prod/data" + object)},
		"getManifest":     {status: 200, path: at("/api/c/prod/manifest" + object)},

		"previewData": {status: 200, path: at("/api/c/prod/data/preview"),
			body: sends(`{"kind":"configmaps","namespace":"default","name":"app","resourceVersion":"42",` +
				`"entries":[{"key":"LEVEL","value":"debug"}]}`),
			keep: keepToken("data")},
		"applyData": {status: 200, path: at("/api/c/prod/data"), body: spends("data")},

		"previewManifest": {status: 200, path: at("/api/c/prod/manifest/preview"),
			body: sends(`{"document":"apiVersion: v1\nkind: ConfigMap\n","resourceVersion":"42"}`),
			keep: keepToken("manifest")},
		"applyManifest": {status: 200, path: at("/api/c/prod/manifest/apply"), body: spends("manifest")},

		"getCreateTemplate": {status: 200, path: at("/api/c/prod/create/template?kind=deployments&namespace=default")},
		"previewCreate": {status: 200, path: at("/api/c/prod/create/preview"),
			body: sends(`{"document":"apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n  namespace: default\n"}`),
			keep: keepToken("create")},
		"applyCreate": {status: 200, path: at("/api/c/prod/create/apply"), body: spends("create")},

		"previewAction": {status: 200, path: at("/api/c/prod/actions/preview"),
			body: sends(`{"verb":"delete","target":{"kind":"pods","namespace":"default","name":"api-1"}}`),
			keep: keepToken("action")},
		"runAction": {status: 200, path: at("/api/c/prod/actions"),
			body: func(state map[string]string) string {
				// The gesture the plan asked for. A delete on a pod is confirmed
				// by count, which is "1" for one object.
				return `{"token":` + strconv.Quote(state["action"]) + `,"confirmed":"1"}`
			}},

		"previewBulk": {status: 200, path: at("/api/c/prod/bulk/preview"),
			body: sends(`{"verb":"delete","kind":"pods","targets":[` +
				`{"kind":"pods","namespace":"default","name":"api-1"},` +
				`{"kind":"pods","namespace":"default","name":"api-2"}]}`),
			keep: keepToken("bulk")},
		"runBulk": {status: 200, path: at("/api/c/prod/bulk"),
			body: func(state map[string]string) string {
				return `{"token":` + strconv.Quote(state["bulk"]) + `,"confirmed":"2"}`
			}},

		"previewNodeShell": {status: 200, path: at("/api/c/prod/nodeshell/preview"),
			body: sends(`{"node":"node-1"}`)},

		"getPrereq": {status: 200, path: at("/api/c/prod/prereq/trivy-operator")},
		"previewPrereq": {status: 200, path: at("/api/c/prod/prereq/trivy-operator/preview"),
			keep: keepToken("prereq")},
		"installPrereq": {status: 200, path: at("/api/c/prod/prereq/trivy-operator/install"),
			body: func(state map[string]string) string {
				// The gesture the plan asks for: the Application's name.
				return `{"token":` + strconv.Quote(state["prereq"]) + `,"confirmed":"trivy-operator"}`
			}},

		"checkUpdates":     {status: 200, path: at("/api/settings/update-check")},
		"getUpdateInstall": {status: 200, path: at("/api/settings/update")},
		"setUpdateInstall": {status: 200, path: at("/api/settings/update"), body: sends(`{"enabled":false,"when":"now"}`)},
		"downloadUpdate":   {status: 200, path: at("/api/settings/update/download")},
		"installUpdate":    {status: 200, path: at("/api/settings/update/install")},
		"openUpdateFolder": {status: 200, path: at("/api/settings/update/open-folder")},

		"clearState": {status: 200, path: at("/api/settings/clear"),
			body: sends(`{"scope":"preferences","preview":true}`)},

		"getKindPreferences": {status: 200, path: at("/api/me/kinds")},
		"setKindPreferences": {status: 200, path: at("/api/me/kinds"),
			body: sends(`{"showUnused":true,"favourites":{"prod":["pods","certificates.cert-manager.io"]}}`)},

		"getPreferences": {status: 200, path: at("/api/preferences")},
		"setRailBar": {status: 204, path: at("/api/preferences/rail/bar"),
			body: sends(`{"bar":2}`)},
		"setRailAssign": {status: 204, path: at("/api/preferences/rail/assign"),
			body: sends(`{"cluster":"prod","bar":2}`)},
		"setRailMove": {status: 204, path: at("/api/preferences/rail/move"),
			body: sends(`{"cluster":"prod","order":0}`)},
		"setRailPlace": {status: 204, path: at("/api/preferences/rail/place"),
			body: sends(`{"cluster":"prod","target":"staging","edge":"after"}`)},
		"setRailHide": {status: 204, path: at("/api/preferences/rail/hide"),
			body: sends(`{"cluster":"prod"}`)},
		"setRailReveal": {status: 204, path: at("/api/preferences/rail/reveal"),
			body: sends(`{"cluster":"prod"}`)},
		"setRailNameplate": {status: 204, path: at("/api/preferences/rail/nameplate"),
			body: sends(`{"cluster":"prod","label":"Production","initials":"PR","colour":"amber"}`)},
		"setColumnSize": {status: 204, path: at("/api/preferences/columns/size"),
			body: sends(`{"kind":"pods","column":"Name","width":240}`)},
		"setColumnPlace": {status: 204, path: at("/api/preferences/columns/place"),
			body: sends(`{"kind":"pods","column":"Name","order":0}`)},
		"setColumnShow": {status: 204, path: at("/api/preferences/columns/show"),
			body: sends(`{"kind":"pods","column":"Node","visible":false}`)},
		"setColumnDensity": {status: 204, path: at("/api/preferences/columns/density"),
			body: sends(`{"density":"compact"}`)},
		"resetColumns": {status: 204, path: at("/api/preferences/columns/reset"),
			body: sends(`{"kind":"pods"}`)},
		"setSeenVersion": {status: 204, path: at("/api/preferences/seen-version"),
			body: sends(`{"version":"0.2.0"}`)},

		"openDesktopTerminal": {status: 201, path: at("/api/dock/terminals"),
			body: sends(`{"cluster":"prod","namespace":"default","kind":"pods","name":"api-1"}`)},
		"listLogContainers": {status: 200,
			path: at("/api/logs/containers?cluster=prod&kind=pods&namespace=default&name=api-1")},
		"streamLogs": {status: 200,
			path: at("/api/logs?cluster=prod&kind=pods&namespace=default&name=api-1")},
		"listForwards": {status: 200, path: at("/api/forwards")},
		"openForward": {status: 201, path: at("/api/forwards"),
			body: sends(`{"cluster":"prod","namespace":"default","pod":"api-1","remotePort":8080}`)},
		"closeForward": {status: 204, path: at("/api/forwards/fwd-1")},
		"openForwardWindow": {status: 204, path: at("/api/forwards/fwd-1/window"),
			body: sends(`{"tool":"hubble","path":"/?namespace=default","closeWithWindow":true}`)},
	}
}

// TestEveryOperationInTheContractIsExercised is the half that stops this test
// rotting by omission.
//
// A drift test that only validates what somebody remembered to call is a test
// that goes on passing while the contract grows an endpoint nobody serves. The
// list of operations comes out of the file; the list of exercises is here; a
// difference either way fails.
func TestEveryOperationInTheContractIsExercised(t *testing.T) {
	spec := loadSpec(t)
	declared := operationsIn(t, spec)
	planned := exercises()

	if len(declared) == 0 {
		t.Fatal("no operations were found in the contract")
	}
	for _, op := range declared {
		if _, ok := planned[op.id]; !ok {
			t.Errorf("%s (%s %s) is in the contract and is exercised by nothing",
				op.id, op.method, op.path)
		}
	}
	known := map[string]bool{}
	for _, op := range declared {
		known[op.id] = true
	}
	for id := range planned {
		if !known[id] {
			t.Errorf("%s is exercised here and is in no longer in the contract", id)
		}
	}
	if len(order) != len(planned) {
		t.Errorf("the run order names %d operations and %d are planned", len(order), len(planned))
	}
	t.Logf("the contract declares %d operations", len(declared))
}

// TestRealResponsesMatchTheSchemasInTheContract is the test itself.
func TestRealResponsesMatchTheSchemasInTheContract(t *testing.T) {
	spec := loadSpec(t)
	check := &validator{root: spec}

	walkContract(t, spec, func(op operation, plan exercise, response *http.Response, payload []byte) {
		id := op.id
		if response.StatusCode != plan.status {
			t.Errorf("%s answered %d, expected %d: %s", id, response.StatusCode, plan.status,
				strings.TrimSpace(string(payload)))
			return
		}

		schemaFor, mediaType, found := responseSchema(op, plan.status,
			response.Header.Get("Content-Type"))
		if !found {
			// A 204 with no content declared. The contract says nothing comes
			// back, so anything coming back is the failure.
			if len(payload) != 0 {
				t.Errorf("%s answered %d with a body the contract does not declare: %s",
					id, plan.status, payload)
			}
			return
		}
		for _, problem := range validateBody(check, schemaFor, mediaType, payload) {
			t.Errorf("%s: %s", id, problem)
		}
	})
}

// walkContract runs every exercise, in the run order, against a router serving
// every operation the contract declares, and hands each answer to visit.
//
// It is the conformance test's own walk, shared so that a property of
// **every** answer - `Cache-Control: no-store` on everything under /api/ - is
// checked over exactly the operations the contract declares, and gains the next
// one the day it is added, rather than over a list somebody keeps beside it.
//
// An exercise's keep runs only on the status it planned for: a later exercise
// spending a token out of an answer that went wrong would test the 409, not
// the outcome.
func walkContract(t *testing.T, spec map[string]any,
	visit func(op operation, plan exercise, response *http.Response, payload []byte)) {
	t.Helper()
	byID := map[string]operation{}
	for _, op := range operationsIn(t, spec) {
		byID[op.id] = op
	}

	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		// Both ways in, so both the password route and the single sign-on pair
		// are registered: an operation the router does not serve cannot be
		// checked against the contract.
		SignIn:   fakeSignIn{password: true, sso: true, launch: true, handoffs: true},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, KindPrefs: fakeKindPrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Prereqs: fakePrereqs{}, Dock: fakeDock{}, Probe: fakeProbe{}, Updates: fakeUpdates{}, Installer: fakeInstaller{}, ToolWindows: fakeToolWindows{},
		// Every channel on offer: this exercises every operation the contract
		// declares, and two of them - the node-shell preview and its setting -
		// exist only where the node-shell channel does. A build offering no
		// channels is real, and is the one that must NOT serve them.
		Channels:    allChannels(),
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	planned := exercises()
	state := map[string]string{}

	for _, id := range order {
		plan, ok := planned[id]
		if !ok {
			t.Fatalf("%s is in the run order and is not planned", id)
		}
		op, ok := byID[id]
		if !ok {
			t.Fatalf("%s is in the run order and is not in the contract", id)
		}

		var body strings.Reader
		if plan.body != nil {
			body = *strings.NewReader(plan.body(state))
		}
		request := httptest.NewRequest(op.method, plan.path(state), &body)
		if plan.body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		// Every mutation carries the marker, **including the ones with no body**
		// — DELETE /api/forwards/{id} is one case that needs it. A request
		// without it is refused at the door, which is its own test.
		if op.method != http.MethodGet {
			request.Header.Set("X-KubeTower-Request", "1")
		}
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		response := recorder.Result()
		payload := recorder.Body.Bytes()

		if response.StatusCode == plan.status && plan.keep != nil {
			plan.keep(state, payload)
		}
		visit(op, plan, response, payload)
	}
}

// responseSchema finds the schema the contract declares for one status, **for
// the media type the response actually used**.
//
// The last clause matters because `streamLogs` has two media types on the same
// status: with `before` it answers `application/json` and without it
// `text/event-stream`. Ranging over the content map and taking whichever came
// first is correct while every operation has exactly one media type and a coin
// toss once one has two — a test that passes
// on some runs is worse than one that fails on all of them, because the failure
// is attributed to whatever else changed that week.
//
// A response whose Content-Type the contract does not declare is not silently
// validated against the other one: `found` is false, and the caller then fails on
// a body the contract does not describe. That is the honest answer and it is the
// one that catches a handler serving the wrong shape.
func responseSchema(op operation, status int, contentType string) (schema, string, bool) {
	raw, ok := op.responses[strconv.Itoa(status)].(map[string]any)
	if !ok {
		return nil, "", false
	}
	content, ok := raw["content"].(map[string]any)
	if !ok {
		return nil, "", false
	}
	// The essence only. A charset parameter is legal and says nothing about the
	// shape.
	essence := strings.TrimSpace(strings.Split(contentType, ";")[0])
	if essence != "" {
		if media, ok := content[essence].(map[string]any); ok {
			if found, ok := asSchema(media["schema"]); ok {
				return found, essence, true
			}
		}
		if len(content) > 0 {
			return nil, "", false
		}
	}
	for mediaType, rawMedia := range content {
		media, ok := rawMedia.(map[string]any)
		if !ok {
			continue
		}
		if found, ok := asSchema(media["schema"]); ok {
			return found, mediaType, true
		}
	}
	return nil, "", false
}

// validateBody puts one response through the schema, decoding it the way its
// media type says.
func validateBody(check *validator, s schema, mediaType string, payload []byte) []string {
	if mediaType == "text/event-stream" {
		// Each event is one JSON object in a `data:` field, and the schema types
		// **an event** rather than the stream. Validating the whole body would be
		// validating a concatenation against an object schema, which is the shape
		// of test that passes because it never ran.
		var problems []string
		events := 0
		for _, line := range strings.Split(string(payload), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			events++
			problems = append(problems,
				validateJSON(check, s, []byte(strings.TrimPrefix(line, "data: ")),
					fmt.Sprintf("event %d", events))...)
		}
		if events == 0 {
			return []string{"the stream carried no events at all"}
		}
		return problems
	}
	return validateJSON(check, s, payload, "body")
}

func validateJSON(check *validator, s schema, payload []byte, path string) []string {
	var document any
	if err := json.Unmarshal(payload, &document); err != nil {
		return []string{path + " is not JSON: " + err.Error()}
	}
	return check.validate(document, s, path)
}

// TestTheEnvelopeArmsAreRefusedByTheContractItself proves the evaluator has
// teeth, using the exact cases `api/errors.md` records as verified.
//
// Without this, a validator that silently ignored `if`/`then` would pass every
// test above while accepting the one shape this whole contract exists to refuse.
// The negative cases are the only ones that can tell the two apart.
func TestTheEnvelopeArmsAreRefusedByTheContractItself(t *testing.T) {
	spec := loadSpec(t)
	check := &validator{root: spec}

	cases := []struct {
		schema string
		body   string
		valid  bool
		why    string
	}{
		{"ClusterListEnvelope", `{"state":"ok","items":[]}`, false,
			"zero elements read successfully is empty, and allowing both spellings puts the collapse straight back in"},
		{"ClusterListEnvelope", `{"state":"empty","items":[]}`, true,
			"read, and there is nothing here"},
		{"ClusterListEnvelope", `{"state":"unreachable","items":[]}`, false,
			"a failed arm carries a reason and no items member at all"},
		{"ClusterListEnvelope",
			`{"state":"unreachable","reason":{"code":"cluster_unreachable","message":"no"}}`, true,
			"the failed arm, spelled correctly"},
		{"NavEnvelope", `{"state":"empty"}`, false,
			"a value is never empty; a thing that is not there is absent"},
		{"NavEnvelope", `{"state":"absent","reason":{"code":"not_installed","message":"no"}}`, true,
			"the absent arm of a value envelope"},
	}

	for _, one := range cases {
		resolved, err := check.resolve("#/components/schemas/" + one.schema)
		if err != nil {
			t.Fatalf("%s: %v", one.schema, err)
		}
		problems := validateJSON(check, resolved, []byte(one.body), one.schema)
		switch {
		case one.valid && len(problems) > 0:
			t.Errorf("%s %s should be valid (%s): %v", one.schema, one.body, one.why, problems)
		case !one.valid && len(problems) == 0:
			t.Errorf("%s %s should be refused: %s", one.schema, one.body, one.why)
		}
	}
}

// TestEveryAnswerListNamespacesCanGiveIsInTheContract puts the arms the fixture
// does not take through the schema.
//
// The conformance run above exercises one call, so it validates one arm — the
// kept reading, which is the interesting one and is not the one that breaks. The
// endpoint owes four answers and a collapse between any two of them is the defect
// this contract exists to remove, so each is built here the way a handler builds
// it and put through `NamespaceListEnvelope` itself.
//
// Built through namespaceList rather than written out as JSON: a literal would
// test that somebody can spell a valid envelope, and what is worth testing is
// that the mapping from what a backend established produces one.
func TestEveryAnswerListNamespacesCanGiveIsInTheContract(t *testing.T) {
	spec := loadSpec(t)
	check := &validator{root: spec}
	envelope, err := check.resolve("#/components/schemas/NamespaceListEnvelope")
	if err != nil {
		t.Fatal(err)
	}

	read := NamespaceReading{Names: []string{"default", "kube-system"}, ReadAt: testMoment}
	kept := NamespaceReading{Names: []string{"default"}, ReadAt: testMoment, Stale: true,
		Why: Unreachable("the API server did not answer in time")}

	cases := []struct {
		what  string
		got   NamespaceList
		state State
		code  ReasonCode
	}{
		{"a cluster that answered", namespaceList(read, nil), StateOK, ""},
		{"a cluster with nothing in it", namespaceList(NamespaceReading{ReadAt: testMoment}, nil),
			StateEmpty, ""},
		{"a cluster nobody has asked yet",
			namespaceList(NamespaceReading{}, NotProbed("this cluster has not been probed")),
			StatePending, CodeNotProbedYet},
		{"a cluster that could not be read",
			namespaceList(NamespaceReading{}, Unreadable("this account may not list namespaces")),
			StateUnreadable, CodeReadDenied},
		{"a cluster the API server refused",
			namespaceList(NamespaceReading{}, Forbidden("list is denied on namespaces")),
			StateForbidden, CodeVerbDenied},
		{"a reading kept from before it stopped answering", namespaceList(kept, nil),
			StateOK, CodeStaleReading},
		// A kept reading with nothing in it is not a kept reading. It is the
		// failure it is stale because of, and answering `ok` with `items: []`
		// would be the collapse wearing the one state that means "there is
		// something to show".
		{"a kept reading with no names",
			namespaceList(NamespaceReading{Stale: true, ReadAt: testMoment,
				Why: Forbidden("list is denied on namespaces")}, nil),
			StateForbidden, CodeVerbDenied},
	}

	for _, one := range cases {
		if got := one.got.State(); got != one.state {
			t.Errorf("%s answered %q, expected %q", one.what, got, one.state)
		}
		switch reason := one.got.Reason(); {
		case one.code == "" && reason != nil:
			t.Errorf("%s carries reason %q and should carry none", one.what, reason.Code)
		case one.code != "" && reason == nil:
			t.Errorf("%s carries no reason and should carry %q", one.what, one.code)
		case one.code != "" && reason.Code != one.code:
			t.Errorf("%s carries %q, expected %q", one.what, reason.Code, one.code)
		}
		payload, err := json.Marshal(one.got)
		if err != nil {
			t.Fatalf("%s could not be encoded: %v", one.what, err)
		}
		for _, problem := range validateJSON(check, envelope, payload, "body") {
			t.Errorf("%s: %s — %s", one.what, problem, payload)
		}
	}
}

// TestTheSchemaEvaluatorUnderstandsEveryKeywordTheContractUses is the guard on
// the guard.
//
// A validator that quietly skips a keyword is green about nothing, like a test
// that greps for words no longer in any Go string. So the keywords are counted out of the file
// rather than assumed, and a keyword in neither set fails here rather than
// silently weakening every assertion above.
func TestTheSchemaEvaluatorUnderstandsEveryKeywordTheContractUses(t *testing.T) {
	spec := loadSpec(t)
	components, _ := spec["components"].(map[string]any)
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatal("the contract declares no schemas")
	}
	// The **values**, not the map: its keys are schema names, and walking them
	// would report every schema in the contract as an unknown keyword.
	used := map[string]bool{}
	for _, declared := range schemas {
		keywordsIn(declared, used)
	}

	// A schema's own property names are in this set too — `properties` maps a
	// name to a schema, and the walk cannot tell a keyword from a member name.
	// Collecting the member names separately is what keeps the census honest.
	members := map[string]bool{}
	collectPropertyNames(schemas, members)

	var unknown []string
	for keyword := range used {
		if supportedKeywords[keyword] || annotationKeywords[keyword] || members[keyword] {
			continue
		}
		unknown = append(unknown, keyword)
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		t.Errorf("the contract uses %v, which this evaluator would ignore — "+
			"every assertion in this file is weaker than it looks until they are handled.\n"+
			"supported: %v", unknown, sortedKeys(supportedKeywords))
	}
}

func collectPropertyNames(node any, out map[string]bool) {
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "properties" {
				if properties, ok := child.(map[string]any); ok {
					for name := range properties {
						out[name] = true
					}
				}
			}
			collectPropertyNames(child, out)
		}
	case []any:
		for _, child := range value {
			collectPropertyNames(child, out)
		}
	}
}

// TestAnythingUnderTheAPIPrefixThatMatchesNoOperationIsJSON holds the one rule
// the contract states about its own miss case.
//
// HTML here would leave a client unable to tell a missing endpoint from a
// logged-out session — and net/http's own 405 is plain text, which is the same
// problem wearing a status nobody expects.
func TestAnythingUnderTheAPIPrefixThatMatchesNoOperationIsJSON(t *testing.T) {
	spec := loadSpec(t)
	check := &validator{root: spec}
	problem, err := check.resolve("#/components/schemas/Problem")
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	New(Deps{}).Mount(mux)

	for _, address := range []string{"/api/nothing-here", "/api/clusters/deeper", "/api/"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(method, address, nil))
			if recorder.Code != http.StatusNotFound {
				t.Errorf("%s %s answered %d, expected 404", method, address, recorder.Code)
				continue
			}
			if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
				t.Errorf("%s %s answered %s, expected application/problem+json", method, address, got)
			}
			for _, complaint := range validateJSON(check, problem, recorder.Body.Bytes(), "body") {
				t.Errorf("%s %s: %s", method, address, complaint)
			}
		}
	}
}

// TestAMutationWithoutTheRequestMarkerIsRefused holds the other half of the
// cross-site defense. The session is a cookie, so a cross-site form post would
// otherwise carry it.
func TestAMutationWithoutTheRequestMarkerIsRefused(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{Prefs: fakePrefs{}}).Mount(mux)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/preferences/rail/bar",
		strings.NewReader(`{"visible":true}`))
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("a mutation with no marker answered %d, expected 403", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "origin_rejected") {
		t.Errorf("the refusal does not say why: %s", recorder.Body.String())
	}
}

// TestACrossOriginMutationIsRefusedEvenWithTheMarker holds the half people
// forget: a custom header cannot be set cross-origin without a preflight, but
// the Origin check is what makes that true rather than assumed.
func TestACrossOriginMutationIsRefusedEvenWithTheMarker(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{Prefs: fakePrefs{}}).Mount(mux)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/preferences/rail/bar",
		strings.NewReader(`{"visible":true}`))
	request.Header.Set("X-KubeTower-Request", "1")
	request.Header.Set("Origin", "https://evil.example")
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("a cross-origin mutation answered %d, expected 403", recorder.Code)
	}
}

// TestAPlanTokenIsSingleUse holds the property the whole confirm-then-act shape
// rests on: a plan that has been acted on no longer describes anything.
func TestAPlanTokenIsSingleUse(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{Clusters: fakeClusters{}, Acts: fakeActs{},
		AllowOrigin: func(*http.Request, string) bool { return true }}).Mount(mux)

	post := func(path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("X-KubeTower-Request", "1")
		mux.ServeHTTP(recorder, request)
		return recorder
	}

	preview := post("/api/c/prod/actions/preview",
		`{"verb":"delete","target":{"kind":"pods","namespace":"default","name":"api-1"}}`)
	if preview.Code != http.StatusOK {
		t.Fatalf("the preview answered %d: %s", preview.Code, preview.Body.String())
	}
	var plan struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}

	first := post("/api/c/prod/actions", `{"token":`+strconv.Quote(plan.Token)+`,"confirmed":"1"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("the act answered %d: %s", first.Code, first.Body.String())
	}
	second := post("/api/c/prod/actions", `{"token":`+strconv.Quote(plan.Token)+`,"confirmed":"1"}`)
	if second.Code != http.StatusConflict {
		t.Errorf("spending the same token twice answered %d, expected 409", second.Code)
	}
}

// TestAnEnvelopeCannotBeBuiltInAForbiddenShape is the Go half of the arm rules.
//
// The schema refuses `{"state":"ok","items":[]}` on the way out; this refuses it
// on the way in, which is stronger: a handler cannot produce the shape at all,
// so the schema check above is a confirmation rather than the only guard.
func TestAnEnvelopeCannotBeBuiltInAForbiddenShape(t *testing.T) {
	if got := Items([]Cluster{}).State(); got != StateEmpty {
		t.Errorf("an empty slice built %q, expected empty — that is the collapse", got)
	}
	if got := Items([]Cluster{{Name: "prod"}}).State(); got != StateOK {
		t.Errorf("a filled slice built %q, expected ok", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("ListFailed accepted `ok` as a failure state")
		}
	}()
	ListFailed[Cluster](StateOK, Because(CodeTimeout, "no"))
}

// --- The other half of the contract ------------------------------------------

// TestEveryRequestBodyMatchesTheContract validates what the client is told to
// send.
//
// # The gap this closes
//
// `TestRealResponsesMatchTheSchemasInTheContract` reads the server's answers.
// Twelve mutating routes answer 204 with no body at all, so for those the whole
// of the contract that is checked on the response side is the status code. The
// write schemas could say anything: a field the route does not take, or a shape
// that differs from what it reads, and a suite that validates one direction says
// nothing about the other.
//
// # Why it sends the requests rather than only reading the bodies
//
// Four exercises spend a token an earlier one returned, so the bodies are a
// function of live state. Validating them against an empty state would be
// validating a made-up token, which is the shape of test that passes because it
// never ran. So this runs the same sequence through the same server, and
// validates each body on the way out.
func TestEveryRequestBodyMatchesTheContract(t *testing.T) {
	spec := loadSpec(t)
	check := &validator{root: spec}
	byID := map[string]operation{}
	for _, op := range operationsIn(t, spec) {
		byID[op.id] = op
	}

	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		// Both ways in, so both the password route and the single sign-on pair
		// are registered: an operation the router does not serve cannot be
		// checked against the contract.
		SignIn:   fakeSignIn{password: true, sso: true, launch: true, handoffs: true},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, KindPrefs: fakeKindPrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Prereqs: fakePrereqs{}, Dock: fakeDock{}, Probe: fakeProbe{}, Updates: fakeUpdates{}, Installer: fakeInstaller{}, ToolWindows: fakeToolWindows{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	planned := exercises()
	state := map[string]string{}
	validated := 0

	for _, id := range order {
		plan := planned[id]
		op := byID[id]

		sent := ""
		if plan.body != nil {
			sent = plan.body(state)
		}
		schemaFor, mediaType, declared := requestSchema(op)

		switch {
		case declared && strings.TrimSpace(sent) == "":
			// A declared body nothing sends is a schema no client is held to.
			if required, _ := op.request["required"].(bool); required {
				t.Errorf("%s declares a required %s body and this exercise sends none",
					id, mediaType)
			}
		case !declared && strings.TrimSpace(sent) != "":
			t.Errorf("%s sends a body and the contract declares none for it: %s", id, sent)
		case declared:
			validated++
			for _, problem := range validateJSON(check, schemaFor, []byte(sent), "body") {
				t.Errorf("%s: %s", id, problem)
			}
		}

		// Still run it, so the tokens the later exercises spend exist.
		var body strings.Reader
		if plan.body != nil {
			body = *strings.NewReader(sent)
		}
		request := httptest.NewRequest(op.method, plan.path(state), &body)
		if plan.body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		if op.method != http.MethodGet {
			request.Header.Set("X-KubeTower-Request", "1")
		}
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if plan.keep != nil {
			plan.keep(state, recorder.Body.Bytes())
		}
	}

	// The coverage half, and it is the part that stops this rotting by omission.
	// An operation that declares a body and is exercised without one is a write
	// schema nothing holds to the contract.
	withBody := 0
	for _, op := range byID {
		if _, _, declared := requestSchema(op); declared {
			withBody++
		}
	}
	if validated != withBody {
		t.Errorf("the contract declares %d request bodies and %d were validated",
			withBody, validated)
	}
	t.Logf("%d request bodies validated against the contract", validated)
}

// requestSchema finds the schema the contract declares for what is sent.
//
// The mirror of responseSchema, and deliberately written out rather than shared
// with it: a response is addressed by status and a request is not, so a common
// helper would take a status that means nothing on one side.
func requestSchema(op operation) (schema, string, bool) {
	content, ok := op.request["content"].(map[string]any)
	if !ok {
		return nil, "", false
	}
	for mediaType, rawMedia := range content {
		media, ok := rawMedia.(map[string]any)
		if !ok {
			continue
		}
		if found, ok := asSchema(media["schema"]); ok {
			return found, mediaType, true
		}
	}
	return nil, "", false
}
