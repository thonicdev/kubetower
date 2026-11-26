package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/identity"
)

// What a person reads goes through the access review,
// driven through the handlers the router calls against an API server of this
// test's own. The console's credentials read everything; each person is shown
// what their own RoleBindings say - which is what `kubectl auth can-i --as`
// would answer for them.

// readServer is an API server holding pods in three namespaces and two Secrets
// side by side in one. It records every path it is asked, so a test can say
// what was never read.
type readServer struct {
	mu    sync.Mutex
	paths []string
}

func (s *readServer) asked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...)
}

var readPods = map[string][]string{
	"shop":        {"api-1", "api-2"},
	"billing":     {"ledger-0"},
	"kube-system": {"coredns-1"},
}

// phaseOf gives kube-system's pod a Status no other namespace holds, so a
// test can tell the values a selection offers from the whole cluster's.
func phaseOf(namespace string) string {
	if namespace == "kube-system" {
		return "Failed"
	}
	return "Running"
}

func (s *readServer) start(t *testing.T) string {
	t.Helper()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	pod := func(ns, name string) map[string]any {
		return map[string]any{"apiVersion": "v1", "kind": "Pod",
			"metadata": map[string]any{"name": name, "namespace": ns, "resourceVersion": "5"},
			"spec":     map[string]any{"containers": []any{map[string]any{"name": "app", "image": "x"}}},
			"status":   map[string]any{"phase": phaseOf(ns)}}
	}
	secret := func(name string) map[string]any {
		return map[string]any{"apiVersion": "v1", "kind": "Secret",
			"metadata": map[string]any{"name": name, "namespace": "shop", "resourceVersion": "3"},
			"data":     map[string]any{"password": "aHVudGVyMg=="}}
	}
	list := func(kind string, items []any) map[string]any {
		return map[string]any{"apiVersion": "v1", "kind": kind, "metadata": map[string]any{"resourceVersion": "9"}, "items": items}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		switch path := r.URL.Path; {
		case path == "/api":
			write(w, map[string]any{"kind": "APIVersions", "versions": []string{"v1"}})
		case path == "/apis":
			write(w, map[string]any{"kind": "APIGroupList", "apiVersion": "v1", "groups": []any{}})
		case path == "/api/v1":
			write(w, map[string]any{"kind": "APIResourceList", "groupVersion": "v1", "resources": []any{
				map[string]any{"name": "pods", "singularName": "pod", "namespaced": true, "kind": "Pod",
					"verbs": []string{"get", "list", "watch", "delete"}},
				map[string]any{"name": "secrets", "singularName": "secret", "namespaced": true, "kind": "Secret",
					"verbs": []string{"get", "list", "watch"}},
				map[string]any{"name": "namespaces", "singularName": "namespace", "namespaced": false, "kind": "Namespace",
					"verbs": []string{"get", "list"}},
			}})
		case path == "/api/v1/pods":
			var items []any
			for _, ns := range []string{"billing", "kube-system", "shop"} {
				for _, name := range readPods[ns] {
					items = append(items, pod(ns, name))
				}
			}
			write(w, list("PodList", items))
		case strings.HasPrefix(path, "/api/v1/namespaces/") && strings.HasSuffix(path, "/pods"):
			ns := strings.Split(path, "/")[4]
			items := []any{}
			for _, name := range readPods[ns] {
				items = append(items, pod(ns, name))
			}
			write(w, list("PodList", items))
		case path == "/api/v1/namespaces/shop/secrets/db-prod":
			write(w, secret("db-prod"))
		case path == "/api/v1/namespaces/shop/secrets/db-staging":
			write(w, secret("db-staging"))
		case path == "/api/v1/namespaces":
			write(w, list("NamespaceList", []any{
				map[string]any{"metadata": map[string]any{"name": "billing"}},
				map[string]any{"metadata": map[string]any{"name": "kube-system"}},
				map[string]any{"metadata": map[string]any{"name": "shop"}},
			}))
		case strings.HasSuffix(path, "/events"):
			write(w, list("EventList", []any{}))
		default:
			http.Error(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// roleBindings is the cluster's RBAC as the API server would evaluate it:
//
//   - `oidc:shop-viewers` (alice): a RoleBinding in `shop` granting `get` and
//     `list` on pods and events, and `get` on the one Secret `db-prod`
//     (resourceNames);
//   - `oidc:platform` (carol): a ClusterRoleBinding granting `get` and `list`
//     on pods, events and namespaces everywhere;
//   - bob: bound to nothing;
//   - the console's own ServiceAccount (a self review): everything it reads,
//     which is the threat model's "close to cluster-admin"
//     (docs/SERVER-MODE-THREAT-MODEL.md).
func roleBindings(q accesstest.Question) (bool, error) {
	if q.Self {
		return true, nil
	}
	a := q.Attributes
	read := a.Verb == "get" || a.Verb == "list"
	for _, g := range q.Groups {
		switch g {
		case "oidc:shop-viewers":
			if a.Namespace == "shop" && read && (a.Resource == "pods" || a.Resource == "events") {
				return true, nil
			}
			if a.Namespace == "shop" && a.Verb == "get" && a.Resource == "secrets" && a.Name == "db-prod" {
				return true, nil
			}
		case "oidc:readers":
			if read {
				return true, nil
			}
		case "oidc:shop-claims":
			if a.Namespace == "shop" && a.Verb == "list" && a.Resource == "persistentvolumeclaims" {
				return true, nil
			}
		case "oidc:shop-secrets":
			if a.Namespace == "shop" && a.Verb == "list" && a.Resource == "secrets" {
				return true, nil
			}
		case "oidc:platform":
			if read && (a.Resource == "pods" || a.Resource == "events" || a.Resource == "namespaces") {
				return true, nil
			}
		}
	}
	return false, nil
}

func person(name, group string) context.Context {
	return identity.With(context.Background(), identity.Who{Name: name, Method: identity.SSO, Groups: []string{group}})
}

func readsServer(t *testing.T) (*Server, *readServer) {
	t.Helper()
	fake := &readServer{}
	url := fake.start(t)
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte(`apiVersion: v1
kind: Config
current-context: prod
clusters:
- name: prod
  cluster: {server: `+url+`}
contexts:
- name: prod
  context: {cluster: prod, user: sa}
users:
- name: sa
  user: {token: sa-token}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	reviews := &accesstest.Reviews{Answer: roleBindings}
	s, err := New(Options{Clusters: manager, InCluster: true,
		Access: access.New(access.Options{Reviews: reviews.For, InCluster: true})})
	if err != nil {
		t.Fatal(err)
	}
	return s, fake
}

func podRows(page api.ListPage) []string {
	var out []string
	for _, row := range page.Rows {
		out = append(out, row.Namespace+"/"+row.Name)
	}
	sort.Strings(out)
	return out
}

// **The acceptance criterion, in both directions.** One list of every
// namespace, three people: alice sees the pods of the one namespace her
// RoleBinding names and is told the list is narrowed, and which namespaces
// are hers; bob, who may list pods in no namespace, is refused, which is not
// "there are none" (TestAPersonAllowedNowhereIsRefusedTheList); carol sees
// every pod and is told nothing, because her list is the cluster's whole
// answer.
func TestAPodListOfEveryNamespaceShowsEachPersonWhatTheirBindingsSay(t *testing.T) {
	s, _ := readsServer(t)
	for _, tc := range []struct {
		who      string
		ctx      context.Context
		rows     []string
		narrowed bool
		allowed  []string
	}{
		{"alice", person("alice@example.com", "oidc:shop-viewers"), []string{"shop/api-1", "shop/api-2"}, true, []string{"shop"}},
		{"carol", person("carol@example.com", "oidc:platform"), []string{"billing/ledger-0", "kube-system/coredns-1", "shop/api-1", "shop/api-2"}, false, nil},
	} {
		t.Run(tc.who, func(t *testing.T) {
			page, fail := apiReads{s}.ListResources(tc.ctx, api.ListQuery{Cluster: "prod", Kind: "pods"})
			if fail != nil {
				t.Fatalf("the list failed: %+v", fail)
			}
			if strings.Join(page.AllowedNamespaces, ",") != strings.Join(tc.allowed, ",") {
				t.Errorf("allowedNamespaces = %v, want %v", page.AllowedNamespaces, tc.allowed)
			}
			if got := podRows(page); strings.Join(got, ",") != strings.Join(tc.rows, ",") {
				t.Errorf("rows = %v, want %v", got, tc.rows)
			}
			if (page.AccessNote != "") != tc.narrowed {
				t.Errorf("accessNote = %q, want narrowed=%v", page.AccessNote, tc.narrowed)
			}
			if tc.narrowed && (strings.Contains(page.AccessNote, "billing") || strings.Contains(page.AccessNote, "kube-system")) {
				t.Errorf("the note names a namespace the person may not list: %q", page.AccessNote)
			}
		})
	}
}

// A narrowing that leaves no namespace is a refusal, said as one: bob may
// list pods in none of the cluster's namespaces, and is told so rather than
// shown a narrowed list of nothing, which reads as namespaces of his that
// hold no pod.
func TestAPersonAllowedNowhereIsRefusedTheList(t *testing.T) {
	s, _ := readsServer(t)
	page, fail := apiReads{s}.ListResources(person("bob@example.com", "oidc:nobody"), api.ListQuery{Cluster: "prod", Kind: "pods"})
	if fail == nil {
		t.Fatalf("a person who may list pods nowhere was shown a list of %d rows, note %q", len(page.Rows), page.AccessNote)
	}
	if fail.State != api.StateForbidden {
		t.Errorf("state = %q, want forbidden", fail.State)
	}
	if !strings.Contains(fail.Reason.Message, "may not list pods in any namespace") {
		t.Errorf("message = %q, want it to say pods may be listed in no namespace", fail.Reason.Message)
	}
}

// A namespace the person may not list is refused, and the API server is never
// asked for its pods: the review is made before the read, not after it.
func TestAPodListOfANamespaceThePersonMayNotListIsForbiddenAndUnread(t *testing.T) {
	s, fake := readsServer(t)
	_, fail := apiReads{s}.ListResources(person("alice@example.com", "oidc:shop-viewers"),
		api.ListQuery{Cluster: "prod", Kind: "pods", Namespace: "billing"})
	if fail == nil || fail.State != api.StateForbidden {
		t.Fatalf("answered %+v, want forbidden", fail)
	}
	for _, path := range fake.asked() {
		if strings.Contains(path, "/namespaces/billing/pods") {
			t.Errorf("the pods of billing were read for a person who may not list them: %s", path)
		}
	}
	// And the namespace she may list is answered whole, with no note.
	page, fail := apiReads{s}.ListResources(person("alice@example.com", "oidc:shop-viewers"),
		api.ListQuery{Cluster: "prod", Kind: "pods", Namespace: "shop"})
	if fail != nil || len(page.Rows) != 2 || page.AccessNote != "" {
		t.Fatalf("shop answered %+v (%d rows, note %q), want both pods and no note", fail, len(page.Rows), page.AccessNote)
	}
}

// A selection of several namespaces is one list of every namespace filtered
// here, so its note names the selected namespaces the person may not list -
// she typed them - and is absent when she may list all of them.
func TestASelectionNamesTheSelectedNamespacesThePersonMayNotList(t *testing.T) {
	s, _ := readsServer(t)
	alice := person("alice@example.com", "oidc:shop-viewers")
	page, fail := apiReads{s}.ListResources(alice, api.ListQuery{Cluster: "prod", Kind: "pods", Namespace: "shop,billing"})
	if fail != nil {
		t.Fatalf("the list failed: %+v", fail)
	}
	if got := podRows(page); strings.Join(got, ",") != "shop/api-1,shop/api-2" {
		t.Errorf("rows = %v, want the two pods of shop", got)
	}
	if !strings.Contains(page.AccessNote, "billing") || strings.Contains(page.AccessNote, "shop") {
		t.Errorf("accessNote = %q, want it to name billing and only billing", page.AccessNote)
	}
	carol := person("carol@example.com", "oidc:platform")
	page, fail = apiReads{s}.ListResources(carol, api.ListQuery{Cluster: "prod", Kind: "pods", Namespace: "shop,billing"})
	if fail != nil || page.AccessNote != "" || len(page.Rows) != 3 {
		t.Errorf("carol's selection answered %+v (%d rows, note %q), want three rows and no note", fail, len(page.Rows), page.AccessNote)
	}
}

// **A Role granting `get` on one named Secret**: the drawer opens that Secret
// and refuses its neighbor, and the neighbor is never read.
func TestADrawerOpensTheNamedSecretAndRefusesItsNeighbour(t *testing.T) {
	s, fake := readsServer(t)
	alice := person("alice@example.com", "oidc:shop-viewers")
	if _, fail := (apiReads{s}).GetResource(alice, api.ObjectRef{Cluster: "prod", Kind: "secrets", Namespace: "shop", Name: "db-prod"}); fail != nil {
		t.Fatalf("the named Secret was refused: %+v", fail)
	}
	_, fail := apiReads{s}.GetResource(alice, api.ObjectRef{Cluster: "prod", Kind: "secrets", Namespace: "shop", Name: "db-staging"})
	if fail == nil || fail.State != api.StateForbidden {
		t.Fatalf("its neighbor answered %+v, want forbidden", fail)
	}
	for _, path := range fake.asked() {
		if strings.HasSuffix(path, "/secrets/db-staging") {
			t.Errorf("the neighbor was read although its review refused it: %s", path)
		}
	}
	// The data panel is the same read, reviewed the same way.
	if _, fail := (apiReads{s}).DataPanel(alice, api.ObjectRef{Cluster: "prod", Kind: "secrets", Namespace: "shop", Name: "db-staging"}); fail == nil || fail.State != api.StateForbidden {
		t.Errorf("the neighbor's data panel answered %+v, want forbidden", fail)
	}
}

// The namespace dropdown is cluster-scoped: a person who may not list
// namespaces is refused it - and is not told whether a namespace they typed
// exists, which is exactly the question `list namespaces` guards.
func TestTheNamespaceDropdownIsTheirOwnListNamespaces(t *testing.T) {
	s, _ := readsServer(t)
	alice := person("alice@example.com", "oidc:shop-viewers")
	if _, fail := (apiReads{s}).Namespaces(alice, "prod"); fail == nil || fail.State != api.StateForbidden {
		t.Errorf("alice, who may not list namespaces, was answered %+v, want forbidden", fail)
	}
	if note := s.namespaceUnknown(alice, "prod", []string{"no-such-namespace"}); note != "" {
		t.Errorf("a person who may not list namespaces was told %q about a name she typed", note)
	}
	carol := person("carol@example.com", "oidc:platform")
	reading, fail := (apiReads{s}).Namespaces(carol, "prod")
	if fail != nil || len(reading.Names) != 3 {
		t.Errorf("carol, who may list namespaces, was answered %+v %+v", reading, fail)
	}
	if note := s.namespaceUnknown(carol, "prod", []string{"no-such-namespace"}); note == "" {
		t.Error("carol may list namespaces and was not told a typed one does not exist")
	}
}

// Through the router as well: the list's envelope carries the note to the
// wire, and a refused list is the forbidden envelope.
func TestTheRouterCarriesTheAccessNote(t *testing.T) {
	s, _ := readsServer(t)
	mux := http.NewServeMux()
	s.mountAPI(mux)
	get := func(ctx context.Context, path string) string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx))
		return rec.Body.String()
	}
	body := get(person("alice@example.com", "oidc:shop-viewers"), "/api/c/prod/r/pods")
	if !strings.Contains(body, `"accessNote"`) || strings.Contains(body, "ledger-0") {
		t.Errorf("alice's list on the wire: %s", body)
	}
	body = get(person("alice@example.com", "oidc:shop-viewers"), "/api/c/prod/r/pods?namespace=billing")
	if !strings.Contains(body, `"forbidden"`) || strings.Contains(body, "ledger-0") {
		t.Errorf("alice's refused list on the wire: %s", body)
	}
}

// A read refused by the review reaches the contract as what it is: a denial is
// forbidden, a review that could not be made is its own code and retryable -
// never "the cluster is unreachable", which would send the person to the
// network.
func TestARefusedReadIsClassifiedByTheReviewsOwnAnswer(t *testing.T) {
	question := access.Attributes{Verb: "list", Version: "v1", Resource: "pods", Namespace: "billing"}
	denied := stateOf(&access.Refused{Cluster: "prod", Attributes: question,
		Answer: access.Answer{Decision: access.Denied, Subject: access.Person}}, api.Scope{Cluster: "prod"})
	if denied.State != api.StateForbidden {
		t.Errorf("a denial is %s, want forbidden", denied.State)
	}
	unknown := stateOf(&access.Refused{Cluster: "prod", Attributes: question,
		Answer: access.Answer{Decision: access.Unreviewable, Subject: access.Person}}, api.Scope{Cluster: "prod"})
	if unknown.State != api.StateUnreadable || unknown.Reason.Code != api.CodeAccessUnreviewable {
		t.Errorf("an unreviewable read is %s/%s, want unreadable/%s", unknown.State, unknown.Reason.Code, api.CodeAccessUnreviewable)
	}
}
