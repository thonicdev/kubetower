package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/extras"
	"github.com/thonicdev/kubetower/internal/sharedstate"
)

// Several replicas behind one address: what this package decides about them.
//
// Two properties, and each has a control, because the failure they guard
// against is the quiet kind - a console that works one request in two:
//
//   - a plan minted by one replica is spent by the other, through the shared
//     store, and without one it is not;
//   - what only one pod can hold is not registered at all where the deployment
//     says there are several, and is registered where it does not.

// addressesSeveralReplicasRemove is every write that lands in one pod's state
// directory, plus the three forward operations, whose tunnel is a listener in
// one pod.
var addressesSeveralReplicasRemove = []struct {
	method, path string
}{
	{http.MethodPost, "/api/preferences/rail/bar"},
	{http.MethodPost, "/api/preferences/rail/assign"},
	{http.MethodPost, "/api/preferences/rail/move"},
	{http.MethodPost, "/api/preferences/rail/place"},
	{http.MethodPost, "/api/preferences/rail/hide"},
	{http.MethodPost, "/api/preferences/rail/reveal"},
	{http.MethodPost, "/api/preferences/rail/nameplate"},
	{http.MethodPost, "/api/preferences/columns/size"},
	{http.MethodPost, "/api/preferences/columns/place"},
	{http.MethodPost, "/api/preferences/columns/show"},
	{http.MethodPost, "/api/preferences/columns/density"},
	{http.MethodPost, "/api/preferences/columns/reset"},
	{http.MethodPost, "/api/preferences/seen-version"},
	{http.MethodPut, "/api/c/prod/extensions/argocd"},
	{http.MethodPut, "/api/c/prod/extensions/argocd/settings/list-column"},
	{http.MethodPut, "/api/settings"},
	{http.MethodPut, "/api/settings/nodeshell"},
	{http.MethodPost, "/api/settings/clear"},
	{http.MethodGet, "/api/forwards"},
	{http.MethodPost, "/api/forwards"},
	{http.MethodDelete, "/api/forwards/abc"},
}

// addressesSeveralReplicasKeep are the ones that must survive: the reads, and
// the writes to a cluster, which go through the shared plan store.
var addressesSeveralReplicasKeep = []struct {
	method, path string
}{
	{http.MethodGet, "/api/preferences"},
	{http.MethodGet, "/api/settings"},
	{http.MethodPost, "/api/c/prod/actions/preview"},
	{http.MethodPost, "/api/c/prod/actions"},
	{http.MethodPost, "/api/c/prod/manifest/apply"},
	{http.MethodGet, "/api/session"},
}

func TestSeveralReplicasRegisterNothingOnlyOnePodCanHold(t *testing.T) {
	a := New(Deps{Replicated: true, InCluster: true})
	for _, address := range addressesSeveralReplicasRemove {
		req := httptest.NewRequest(address.method, address.path, nil)
		if _, pattern := a.mux.Handler(req); pattern != "" {
			t.Errorf("%s %s is registered as %q on one of several replicas. It must not be: what it "+
				"writes lives in one pod, so it would be kept one request in two", address.method,
				address.path, pattern)
		}
	}
	for _, address := range addressesSeveralReplicasKeep {
		req := httptest.NewRequest(address.method, address.path, nil)
		if _, pattern := a.mux.Handler(req); pattern == "" {
			t.Errorf("%s %s is not registered on one of several replicas, and it should be", address.method,
				address.path)
		}
	}
}

// The control: one replica keeps every one of them. Without it, the cheapest way
// to make the test above pass is to remove them for everybody.
//
// Two other conditions remove addresses from the same list: the server
// profile registers no forward
// (Deps.InCluster), and the node-shell image write exists only beside the
// node-shell channel. So the control offers every channel, and in cluster it
// leaves the forwards to the profile - what is left is what Replicated alone
// decides. The same goes for the twelve preference writes, which the profile
// takes away on one replica too (profileroutes_test.go), and for the
// settings writes and the extra switch (consolewrites_test.go).
func TestOneReplicaKeepsEveryAddress(t *testing.T) {
	for _, inCluster := range []bool{false, true} {
		a := New(Deps{InCluster: inCluster, Channels: allChannels()})
		for _, address := range addressesSeveralReplicasRemove {
			if inCluster && (strings.HasPrefix(address.path, "/api/forwards") ||
				strings.HasPrefix(address.path, "/api/preferences/") ||
				strings.HasPrefix(address.path, "/api/settings") ||
				strings.Contains(address.path, "/extensions/")) {
				continue
			}
			req := httptest.NewRequest(address.method, address.path, nil)
			if _, pattern := a.mux.Handler(req); pattern == "" {
				t.Errorf("%s %s is not registered on a single replica (in cluster: %v), and it always was",
					address.method, address.path, inCluster)
			}
		}
	}
}

// Every condition that decides a settings write or a forward, in one table,
// because each is decided by more than one: the settings writes by several
// replicas, the node-shell write also by its channel, and the forwards by the
// server profile as well as by several replicas. Neither may put back what
// another took away.
func TestWhatDecidesTheSettingsWritesAndTheForwards(t *testing.T) {
	cases := []struct {
		name                             string
		deps                             Deps
		settings, nodeShell, clear, fwds bool
	}{
		{"desktop, one replica", Deps{Channels: allChannels()}, true, true, true, true},
		{"desktop, no node-shell channel", Deps{}, true, false, true, true},
		// Under the server profile the settings are the deployment's, on
		// one replica as on several.
		{"in cluster, one replica", Deps{InCluster: true, Channels: allChannels()}, false, false, false, false},
		{"several replicas", Deps{Replicated: true, Channels: allChannels()}, false, false, false, false},
		{"in cluster, several replicas", Deps{InCluster: true, Replicated: true, Channels: allChannels()},
			false, false, false, false},
	}
	registered := func(a *API, method, path string) bool {
		_, pattern := a.mux.Handler(httptest.NewRequest(method, path, nil))
		return pattern != ""
	}
	for _, c := range cases {
		a := New(c.deps)
		if got := registered(a, http.MethodPut, "/api/settings"); got != c.settings {
			t.Errorf("%s: PUT /api/settings registered = %v, want %v", c.name, got, c.settings)
		}
		if got := registered(a, http.MethodPut, "/api/settings/nodeshell"); got != c.nodeShell {
			t.Errorf("%s: PUT /api/settings/nodeshell registered = %v, want %v", c.name, got, c.nodeShell)
		}
		if got := registered(a, http.MethodPost, "/api/settings/clear"); got != c.clear {
			t.Errorf("%s: POST /api/settings/clear registered = %v, want %v", c.name, got, c.clear)
		}
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			if got := registered(a, method, "/api/forwards"); got != c.fwds {
				t.Errorf("%s: %s /api/forwards registered = %v, want %v", c.name, method, got, c.fwds)
			}
		}
		if got := registered(a, http.MethodDelete, "/api/forwards/abc"); got != c.fwds {
			t.Errorf("%s: DELETE /api/forwards/{id} registered = %v, want %v", c.name, got, c.fwds)
		}
	}
}

// replica is one API over a store, as one pod would be.
func replica(kv sharedstate.Store) *http.ServeMux {
	mux := http.NewServeMux()
	New(Deps{Clusters: fakeClusters{}, Acts: fakeActs{}, Shared: kv,
		AllowOrigin: func(*http.Request, string) bool { return true }}).Mount(mux)
	return mux
}

func postTo(mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("X-KubeTower-Request", "1")
	mux.ServeHTTP(recorder, request)
	return recorder
}

func previewOn(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	preview := postTo(mux, "/api/c/prod/actions/preview",
		`{"verb":"delete","target":{"kind":"pods","namespace":"default","name":"api-1"}}`)
	if preview.Code != http.StatusOK {
		t.Fatalf("the preview answered %d: %s", preview.Code, preview.Body.String())
	}
	var plan struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil || plan.Token == "" {
		t.Fatalf("the preview carried no token: %v %s", err, preview.Body.String())
	}
	return plan.Token
}

// TestAPlanMadeOnOneReplicaIsSpentOnTheOther is the confirmation half of
// several replicas: the preview and the click are two requests, and a Service
// sends each wherever it likes.
func TestAPlanMadeOnOneReplicaIsSpentOnTheOther(t *testing.T) {
	kv := sharedstate.NewMemory(time.Now)
	a, b := replica(kv), replica(kv)

	token := previewOn(t, a)
	act := `{"token":` + strconv.Quote(token) + `,"confirmed":"1"}`
	if got := postTo(b, "/api/c/prod/actions", act); got.Code != http.StatusOK {
		t.Fatalf("a plan made on one replica and confirmed on the other answered %d: %s", got.Code,
			got.Body.String())
	}
	// And single use across both, not within each.
	if again := postTo(a, "/api/c/prod/actions", act); again.Code != http.StatusConflict {
		t.Errorf("the same plan spent again, on the replica that made it, answered %d, expected 409", again.Code)
	}
}

// The control: two replicas each with their own memory refuse it.
func TestWithoutASharedStoreAPlanDoesNotCrossReplicas(t *testing.T) {
	a, b := replica(nil), replica(nil)
	token := previewOn(t, a)
	got := postTo(b, "/api/c/prod/actions", `{"token":`+strconv.Quote(token)+`,"confirmed":"1"}`)
	if got.Code != http.StatusConflict {
		t.Fatalf("two replicas with separate memories honored each other's plan (%d); the control is "+
			"not measuring what the test above claims", got.Code)
	}
}

// unanswering is a store that never answers.
type unanswering struct{}

func (unanswering) Put(context.Context, string, []byte, time.Time) error {
	return sharedstate.ErrUnreachable
}
func (unanswering) PutNew(context.Context, string, []byte, time.Time) (bool, error) {
	return false, sharedstate.ErrUnreachable
}
func (unanswering) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, sharedstate.ErrUnreachable
}
func (unanswering) Take(context.Context, string) ([]byte, bool, error) {
	return nil, false, sharedstate.ErrUnreachable
}
func (unanswering) Extend(context.Context, string, time.Time) (bool, error) {
	return false, sharedstate.ErrUnreachable
}
func (unanswering) Delete(context.Context, string) error { return sharedstate.ErrUnreachable }
func (unanswering) Describe() string                     { return "nothing" }

// A store that does not answer is said as such: a 503 naming it, never a 409
// telling the user to preview again - which would fail the same way.
func TestAPlanStoreThatDoesNotAnswerIsA503(t *testing.T) {
	mux := replica(unanswering{})
	preview := postTo(mux, "/api/c/prod/actions/preview",
		`{"verb":"delete","target":{"kind":"pods","namespace":"default","name":"api-1"}}`)
	act := postTo(mux, "/api/c/prod/actions", `{"token":"abc","confirmed":"1"}`)
	for name, got := range map[string]*httptest.ResponseRecorder{"preview": preview, "act": act} {
		var problem Problem
		_ = json.Unmarshal(got.Body.Bytes(), &problem)
		if got.Code != http.StatusServiceUnavailable || problem.Code != ProblemStoreUnreachable {
			t.Errorf("the %s answered %d %q with a store that does not answer, expected 503 %q", name,
				got.Code, problem.Code, ProblemStoreUnreachable)
		}
	}
}

// TestAPlanSurvivesTheStoreWhole fills every member of a plan, of every kind,
// sends it through the store's encoding and compares what comes back.
//
// **The trap it exists for**: ActionRequest's precondition and stage are
// `json:"-"`, because a client must never supply them - and they are exactly
// what the act re-derives against. A plan that lost them on the way through the
// store would act with no precondition at all, which is a write guarded by
// nothing, and every other test would still pass.
func TestAPlanSurvivesTheStoreWhole(t *testing.T) {
	kinds := []planKind{planAction, planBulk, planData, planManifest, planCreate, planNodeShell, planClear}
	for _, kind := range kinds {
		in := &plan{kind: kind, cluster: "prod", expiresAt: time.Unix(1_900_000_000, 0).UTC(), clear: "archive"}
		fill(reflect.ValueOf(&in.facts).Elem())
		in.action, in.bulk, in.data, in.manifest = &ActionRequest{}, &BulkRequest{}, &DataWrite{}, &ManifestWrite{}
		fill(reflect.ValueOf(in.action).Elem())
		// Force is the act's, not the plan's: runAction sets it on a copy of
		// the request after the plan is taken, so the
		// store never holds one and has nothing to keep.
		in.action.Force = false
		fill(reflect.ValueOf(in.bulk).Elem())
		fill(reflect.ValueOf(in.data).Elem())
		fill(reflect.ValueOf(in.manifest).Elem())
		if in.action.Precondition == "" || in.action.Stage == "" {
			t.Fatal("the filler did not reach the two server-held fields, so this test proves nothing")
		}

		// UTC, because that is what JSON gives back: an instant in the local zone
		// survives the store as the same instant with another location, which
		// DeepEqual reads as a difference on a machine whose zone is UTC and not
		// on one whose is not.
		kv := sharedstate.NewMemory(func() time.Time { return time.Unix(1_800_000_000, 0).UTC() })
		store := newPlanStore(func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }, kv)
		token, err := store.issue(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		out, ok, err := store.take(context.Background(), token, kind)
		if err != nil || !ok {
			t.Fatalf("%s: the plan did not come back (%v, %v)", kind, ok, err)
		}
		if !reflect.DeepEqual(in, out) {
			t.Errorf("%s: the plan changed on its way through the store:\n in: %+v\nout: %+v", kind, in, out)
		}
	}
}

// fill sets every settable member it can reach to something that is not its
// zero value, so a member the encoding drops is a difference DeepEqual sees.
func fill(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("x-" + v.Type().Name())
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Ptr:
		if v.Type().Elem().Kind() == reflect.Struct && v.Type().Elem() == reflect.TypeOf(time.Time{}) {
			t := time.Unix(1_700_000_000, 0).UTC()
			v.Set(reflect.ValueOf(&t))
			return
		}
		n := reflect.New(v.Type().Elem())
		fill(n.Elem())
		v.Set(n)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fill(s.Index(0))
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		key := reflect.New(v.Type().Key()).Elem()
		fill(key)
		val := reflect.New(v.Type().Elem()).Elem()
		fill(val)
		m.SetMapIndex(key, val)
		v.Set(m)
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			v.Set(reflect.ValueOf(time.Unix(1_700_000_000, 0).UTC()))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).CanSet() {
				fill(v.Field(i))
			}
		}
	}
}

// TestTheStoreFailureIsInTheContract is the store-failure problem code and its
// capability, read out of the contract rather than trusted: a code the guard
// sends that the enum does not list is a response the conformance test would
// refuse, and a capability the Go always sends and the spec never declared is
// one the front end cannot type.
func TestTheStoreFailureIsInTheContract(t *testing.T) {
	problem := schemaPropertiesInContract(t, "Problem")
	code, _ := problem["code"].(map[string]any)
	enum, _ := code["enum"].([]any)
	found := false
	for _, one := range enum {
		if one == string(ProblemStoreUnreachable) {
			found = true
		}
	}
	if !found {
		t.Errorf("Problem.code does not list %q, which the guard answers with", ProblemStoreUnreachable)
	}
	if _, ok := schemaPropertiesInContract(t, "Capabilities")["preferences"]; !ok {
		t.Error("Capabilities does not declare `preferences`, which every server now sends")
	}
	raw, _ := json.Marshal(Capabilities{})
	if !strings.Contains(string(raw), `"preferences":false`) {
		t.Errorf("a false preferences is omitted from the wire, and absent reads as the contract's default "+
			"true: %s", raw)
	}
	// And `replicated`: declared, and a false one still sent.
	if _, ok := schemaPropertiesInContract(t, "Capabilities")["replicated"]; !ok {
		t.Error("Capabilities does not declare `replicated`, which every server now sends")
	}
	if !strings.Contains(string(raw), `"replicated":false`) {
		t.Errorf("a false replicated is omitted from the wire: %s", raw)
	}
}

// The extension writes follow the profile: the
// switch and the settings write on the desktop and in a container, and under
// the server profile with one replica and its state volume, and never on one of several replicas, nor under
// the server profile with no state volume, where each choice would be one pod's
// or gone at the next restart. The suggestion offers the switch, so it is
// registered exactly where the switch is.
//
// **Registered exactly where extras.ProfileOf says Desktop or Server**, for all
// eight combinations of the three facts: the start-up line and internal/web's
// refusals name the switches of that profile, so a router that registered the
// write anywhere else would contradict them. The wanted answer is spelled from
// the profile's values here rather than through Profile.Switchable, so a wrong
// Switchable fails this test as well as a wrong condition in the router. Which
// profile each combination is, is extras.TestProfileOfNamesTheSwitchesOfEachStart's.
func TestTheExtensionWritesFollowTheProfile(t *testing.T) {
	registered := 0
	for _, inCluster := range []bool{false, true} {
		for _, replicated := range []bool{false, true} {
			for _, ephemeral := range []bool{false, true} {
				profile := extras.ProfileOf(inCluster, replicated, ephemeral)
				want := profile == extras.Desktop || profile == extras.Server
				if want {
					registered++
				}
				name := fmt.Sprintf("inCluster=%t replicated=%t ephemeral=%t (profile %d)",
					inCluster, replicated, ephemeral, profile)
				a := New(Deps{InCluster: inCluster, Replicated: replicated, StateEphemeral: ephemeral})
				for _, path := range []string{
					"/api/c/prod/extensions/suggestions",
					"/api/c/prod/extensions/sample-extra",
					"/api/c/prod/extensions/sample-extra/settings/list-column",
				} {
					method := http.MethodGet
					if !strings.HasSuffix(path, "/suggestions") {
						method = http.MethodPut
					}
					_, pattern := a.mux.Handler(httptest.NewRequest(method, path, nil))
					if (pattern != "") != want {
						t.Errorf("%s: %s %s registered as %q, want registered=%t", name, method, path, pattern, want)
					}
				}
			}
		}
	}
	// The control: the desktop with and without a state volume, and the server
	// profile with one replica and its volume. A ProfileOf that answered
	// Replicated everywhere would otherwise pass a router that registers nothing.
	if registered != 3 {
		t.Errorf("%d of the eight combinations register the switch, want 3", registered)
	}
}
