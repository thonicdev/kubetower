package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The health page's half of the member check, and it is the same class of defect
// as the dashboard's.
//
// # An absent optional member conforms
//
// `HealthReport` requires only `checks`, and every member of `HealthCheck`,
// `HealthNotice` and the count entries is optional. So a report that is a bare
// list of rows validates against the contract even though `Health.tsx` reads
// five members that were never sent. The members `internal/api` must carry:
//
//	HealthReport  total probed answered pending refreshing headline tone
//	              counts notices
//	HealthCheck   remedy readOnly version metricsSource checkedAt tookMs
//	HealthNotice  the whole type
//
// The consequence is exact: `Health.tsx` draws three
// arms — *nothing was configured* / *still asking* / *counted and not answered
// for* — and without the counts the branch reached is the first, the hedge saying
// *"without the counts this page cannot tell which"*. **The hedge would be the only
// arm that could run**, on the page whose whole product is being believed.
//
// # Proving it fails
//
// Removing `Notices` from `fakeSettings.Health` and re-running:
//
//	healthmembers_test.go: the report carries no notices; there is nothing to
//	measure
//
// Adding `"cluster"` to a count entry, which the contract does not declare there:
//
//	healthmembers_test.go: a count pill carries "cluster" and the contract does
//	not declare it
//
// Both were run. The second only fails because the
// count-entry block below reads the inline item schema out of the file like
// everything else, rather than checking only that each declared name was present.
//
// # What it does not check
//
// This drives the real handler over `fakeSettings`. It proves the contract types
// can carry every declared member and that the handler emits what it is given;
// it cannot prove that `internal/web` fills them from the probe.
// `TestTheHealthPageIsFilledFromTheProbeThatHoldsIt` in `internal/web` is the
// other half.
func TestTheHealthReportCarriesEveryMemberTheContractDeclares(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/health answered %d: %s", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		State string         `json:"state"`
		Data  map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("the answer is not an envelope: %v", err)
	}
	if envelope.State != string(StateOK) || len(envelope.Data) == 0 {
		t.Fatalf("state=%q with a %d-member report; this test measures a report that has "+
			"something in it", envelope.State, len(envelope.Data))
	}

	checks, ok := envelope.Data["checks"].([]any)
	if !ok || len(checks) == 0 {
		t.Fatal("the report carries no checks; there is nothing to measure")
	}
	notices, ok := envelope.Data["notices"].([]any)
	if !ok || len(notices) == 0 {
		t.Fatal("the report carries no notices; there is nothing to measure")
	}
	counts, ok := envelope.Data["counts"].([]any)
	if !ok || len(counts) == 0 {
		t.Fatal("the report carries no counts; there is nothing to measure")
	}
	sso, ok := envelope.Data["sso"].(map[string]any)
	if !ok {
		t.Fatal("the report carries no sso; there is nothing to measure")
	}
	ssoSessions, ok := sso["sessions"].([]any)
	if !ok || len(ssoSessions) == 0 {
		t.Fatal("the report's sso carries no sessions; there is nothing to measure")
	}

	// `Remedy` hangs off both a check and a notice, and it is the member the page
	// is read for. Measured as its own subject so that a report carrying every
	// other member and no remedy fails here rather than looking complete.
	remedies := map[string]bool{}
	for _, group := range [][]any{checks, notices} {
		for _, item := range group {
			object, ok := item.(map[string]any)
			if !ok {
				t.Fatalf("an item is not an object: %T", item)
			}
			remedy, ok := object["remedy"].(map[string]any)
			if !ok {
				continue
			}
			for member := range remedy {
				remedies[member] = true
			}
		}
	}
	if len(remedies) == 0 {
		t.Error("nothing in the report carries a remedy; the judgment the page is read for " +
			"is the member most easily left out, because everything else still renders")
	}

	for _, subject := range []struct {
		schema  string
		carried map[string]bool
	}{
		{"HealthReport", membersOf(envelope.Data)},
		{"HealthCheck", unionOf(t, checks)},
		{"HealthNotice", unionOf(t, notices)},
		{"Remedy", remedies},
		{"HealthSSO", membersOf(sso)},
		{"HealthSSOSession", unionOf(t, ssoSessions)},
	} {
		declared := schemaMembersInContract(t, subject.schema)
		for _, member := range sorted(declared) {
			if why, allowed := healthMembersWithNoSource[subject.schema+"."+member]; allowed {
				t.Logf("%s.%s is declared and deliberately not carried: %s", subject.schema, member, why)
				continue
			}
			if !subject.carried[member] {
				t.Errorf("the contract declares %s.%s and the response does not carry it; "+
					"either fill it from the producer that holds it, or record here why it has no source",
					subject.schema, member)
			}
		}
		for _, member := range sorted(subject.carried) {
			if !declared[member] {
				t.Errorf("the response carries %s.%s and the contract does not declare it; "+
					"this is how `Cluster.group` survived a contract that had removed it",
					subject.schema, member)
			}
		}
	}

	// The count entry is an **inline** object rather than a named schema, so its
	// declaration is read at the path it sits at rather than by name.
	//
	// Both directions here too, and the reverse one is not theoretical: a block
	// that listed four names and checked only that each was present would pass
	// when `cluster` is added to the Go `HealthCount` and to the fixture — a
	// member the contract does not declare. That is `Cluster.group` again, in
	// the test written to prevent it.
	carried := unionOf(t, counts)
	declared := inlineMembersInContract(t, "HealthReport", "counts")
	for _, member := range sorted(declared) {
		if !carried[member] {
			t.Errorf("the contract declares a count pill's %q and the response does not "+
				"carry it; the pills and the rows have to read in the same direction, and a "+
				"client ordering them has only prose without the code", member)
		}
	}
	for _, member := range sorted(carried) {
		if !declared[member] {
			t.Errorf("a count pill carries %q and the contract does not declare it", member)
		}
	}
}

// schemaPropertiesInContract is `components.schemas.<name>.properties`, raw.
//
// `schemaMembersInContract` reduces the same thing to a set of names; this hands
// back the objects, so a reader can walk one level further into an inline shape.
func schemaPropertiesInContract(t *testing.T, name string) map[string]any {
	t.Helper()
	spec := loadSpec(t)
	components, ok := spec["components"].(map[string]any)
	if !ok {
		t.Fatal("the contract declares no components")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatal("the contract declares no schemas")
	}
	schema, ok := schemas[name].(map[string]any)
	if !ok {
		t.Fatalf("the contract declares no %s schema", name)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		t.Fatalf("the %s schema declares no properties", name)
	}
	return properties
}

// inlineMembersInContract reads the properties of an array member's inline item
// schema, following a path of member names:
//
//	inlineMembersInContract(t, "HealthReport", "counts")
//	    → components.schemas.HealthReport.properties.counts.items.properties
//	inlineMembersInContract(t, "Settings", "info", "kubeconfigShadowed")
//	    → …Settings.properties.info.properties.kubeconfigShadowed.items.properties
//
// A second reader rather than a generalization of `schemaMembersInContract`,
// because the two answer different questions. The path is a list of names and
// deliberately not an expression: three hops is the deepest anything here goes,
// and a query language for that is the tool outliving the need.
func inlineMembersInContract(t *testing.T, schema string, path ...string) map[string]bool {
	t.Helper()
	if len(path) == 0 {
		t.Fatal("inlineMembersInContract needs at least one member name")
	}
	properties := schemaPropertiesInContract(t, schema)

	// Every hop but the last is an object; the last is the array.
	for _, hop := range path[:len(path)-1] {
		object, ok := properties[hop].(map[string]any)
		if !ok {
			t.Fatalf("%s declares no %s", schema, hop)
		}
		nested, ok := object["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s.%s declares no properties", schema, hop)
		}
		properties = nested
	}

	last := path[len(path)-1]
	array, ok := properties[last].(map[string]any)
	if !ok {
		t.Fatalf("%s declares no %s", schema, last)
	}
	items, ok := array["items"].(map[string]any)
	if !ok {
		t.Fatalf("%s.%s declares no items schema", schema, last)
	}
	inline, ok := items["properties"].(map[string]any)
	if !ok || len(inline) == 0 {
		t.Fatalf("%s.%s's item schema declares no properties", schema, last)
	}
	out := make(map[string]bool, len(inline))
	for name := range inline {
		out[name] = true
	}
	return out
}

// healthMembersWithNoSource is the deliberate exceptions, each with the evidence.
//
// **Empty, and that is the finding rather than an omission**: every member of the
// four schemas is traced to a producer:
//
//	HealthCheck.readOnly       health.Cluster.ReadOnly
//	HealthCheck.version        health.Cluster.Version
//	HealthCheck.metricsSource  health.Cluster.Metrics
//	HealthCheck.checkedAt      health.Cluster.CheckedAt
//	HealthCheck.tookMs         health.Cluster.Took, parsed by tookMillis
//	HealthCheck.remedy         internal/web.whoFixes, through clusterRemedy
//	HealthReport.*             internal/health.Snapshot's Unprobed and Total, and
//	                           internal/web.healthSummary
//	HealthNotice.*             internal/web.bannerOf and cacheBannerOf
//	Remedy.*                   internal/web.whoFixes and health.Cluster.Actionable
//
// An entry belongs here only with the search that established the absence. A name
// added to get past a failure is this test switched off one member at a time.
var healthMembersWithNoSource = map[string]string{}
