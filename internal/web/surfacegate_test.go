package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/edit"
)

// The three surfaces the access review guards on the way *out*, driven through
// the handlers the router calls - GetResource, ListResources and RevealEnv -
// against an API server of this test's own, with a gate that denies. The
// helpers in gateverbs_test.go prove the mapping; these prove the handlers use
// it. Replacing any of the three calls with the ungated function it wraps fails
// here.

// surfaceServer is a small API server: core/v1 discovery for pods and secrets,
// one pod, one secret, and empty lists for everything else a page asks for. It
// records every path it is asked, so a test can say what was never read.
type surfaceServer struct {
	mu    sync.Mutex
	paths []string
}

func (s *surfaceServer) asked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...)
}

func (s *surfaceServer) start(t *testing.T) string {
	t.Helper()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
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
					"verbs": []string{"get", "list", "watch", "delete", "patch"}},
				map[string]any{"name": "secrets", "singularName": "secret", "namespaced": true, "kind": "Secret",
					"verbs": []string{"get", "list", "watch", "patch"}},
				map[string]any{"name": "namespaces", "singularName": "namespace", "namespaced": false, "kind": "Namespace",
					"verbs": []string{"get", "list"}},
			}})
		case path == "/api/v1/namespaces/shop/pods/api-1":
			write(w, map[string]any{"apiVersion": "v1", "kind": "Pod",
				"metadata": map[string]any{"name": "api-1", "namespace": "shop", "uid": "u1", "resourceVersion": "7",
					"ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSet",
						"name": "api", "uid": "rs", "controller": true}}},
				"spec":   map[string]any{"containers": []any{map[string]any{"name": "api", "image": "x"}}},
				"status": map[string]any{"phase": "Running"}})
		case path == "/api/v1/namespaces/shop/services/api":
			write(w, map[string]any{"apiVersion": "v1", "kind": "Service",
				"metadata": map[string]any{"name": "api", "namespace": "shop", "resourceVersion": "4"},
				"spec":     map[string]any{"ports": []any{map[string]any{"name": "https", "port": 443}}}})
		case path == "/api/v1/namespaces/shop/secrets/db":
			write(w, map[string]any{"apiVersion": "v1", "kind": "Secret",
				"metadata": map[string]any{"name": "db", "namespace": "shop", "resourceVersion": "3"},
				"data":     map[string]any{"password": "aHVudGVyMg=="}})
		case strings.HasSuffix(path, "/pods") || strings.HasSuffix(path, "/events") ||
			strings.HasSuffix(path, "/namespaces") || strings.HasSuffix(path, "/secrets"):
			kind := "List"
			if strings.HasSuffix(path, "/pods") {
				kind = "PodList"
			}
			items := []any{}
			if strings.HasSuffix(path, "/pods") {
				items = append(items, map[string]any{"apiVersion": "v1", "kind": "Pod",
					"metadata": map[string]any{"name": "api-1", "namespace": "shop"},
					"status":   map[string]any{"phase": "Running"}})
			}
			write(w, map[string]any{"apiVersion": "v1", "kind": kind, "metadata": map[string]any{"resourceVersion": "9"}, "items": items})
		default:
			http.Error(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// denyingServer is a Server over surfaceServer whose gate answers no to every
// question about the signed-in person that is not a read - and to reading a
// Secret. The pages themselves must load, since reads are reviewed too
// so what these tests pin is the verbs a page offers and
// the reveal, not whether the page can be read at all.
func denyingServer(t *testing.T) (*Server, *surfaceServer, *accesstest.Reviews) {
	t.Helper()
	manager, fake := surfaceManager(t)
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		read := q.Attributes.Verb == "get" || q.Attributes.Verb == "list" || q.Attributes.Verb == "watch"
		return read && q.Attributes.Resource != "secrets", nil
	}}
	s, err := New(Options{Clusters: manager, InCluster: true,
		Access: access.New(access.Options{Reviews: reviews.For, InCluster: true})})
	if err != nil {
		t.Fatal(err)
	}
	return s, fake, reviews
}

// surfaceManager is a cluster manager over a fresh surfaceServer, as cluster
// `prod`.
func surfaceManager(t *testing.T) (*cluster.Manager, *surfaceServer) {
	t.Helper()
	fake := &surfaceServer{}
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
	return manager, fake
}

func TestAPodPageForAPersonWhoMayNotWriteOffersNoVerb(t *testing.T) {
	s, _, reviews := denyingServer(t)
	page, fail := apiReads{s}.GetResource(grace(), api.ObjectRef{Cluster: "prod", Kind: "pods", Namespace: "shop", Name: "api-1"})
	if fail != nil {
		t.Fatalf("the pod page failed: %+v", fail)
	}
	if len(page.Actions) != 0 {
		t.Errorf("a person the cluster denies every verb was offered %v", verbNames(page.Actions))
	}
	if len(reviews.Asked()) == 0 {
		t.Error("the page offered its verbs without asking the review")
	}
}

func TestAPodListForAPersonWhoMayNotWriteOffersNoBulkVerb(t *testing.T) {
	s, _, reviews := denyingServer(t)
	page, fail := apiReads{s}.ListResources(grace(), api.ListQuery{Cluster: "prod", Kind: "pods", Namespace: "shop"})
	if fail != nil {
		t.Fatalf("the pod list failed: %+v", fail)
	}
	if page.Bulk != nil {
		t.Errorf("a person the cluster denies every verb was offered a selection bar: %+v", page.Bulk.Verbs)
	}
	if len(reviews.Asked()) == 0 {
		t.Error("the list offered its bulk verbs without asking the review")
	}
}

// The most serious of the three: the console can read every Secret, and this is
// the line that stops a signed-in person reading one with its rights. Refused,
// the Secret is never fetched at all.
func TestARevealThePersonMayNotMakeIsForbiddenAndReadsNothing(t *testing.T) {
	s, fake, _ := denyingServer(t)
	_, fail := apiReads{s}.RevealEnv(grace(), api.EnvQuery{
		ObjectRef: api.ObjectRef{Cluster: "prod", Kind: "secrets", Namespace: "shop", Name: "db"}, Key: "password",
	})
	if fail == nil || fail.State != api.StateForbidden {
		t.Fatalf("a denied reveal answered %+v, want forbidden", fail)
	}
	for _, path := range fake.asked() {
		if strings.Contains(path, "/secrets/db") {
			t.Errorf("the Secret was read although the reveal was denied: %s", path)
		}
	}
	// Through the router too: the contract's answer to a forbidden reveal.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/c/prod/env?kind=secrets&namespace=shop&name=db&key=password", nil)
	req = req.WithContext(grace())
	mux := http.NewServeMux()
	s.mountAPI(mux)
	mux.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "aHVudGVyMg") {
		t.Errorf("the value reached the response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"forbidden"`) {
		t.Errorf("the route answered %d %s, want the forbidden envelope", rec.Code, rec.Body.String())
	}
}

// The act endpoints' mapping of the review's two refusals: a denial is a 403
// the person reads as RBAC, a review that could not be made is the 503 that
// sends them to the cluster's reachability instead.
func TestTheReviewsTwoRefusalsReachTheContractAsDifferentAnswers(t *testing.T) {
	denied := writeError(edit.Refuse(edit.RefusedByAccess, "the cluster says you may not delete pods"))
	if !errors.Is(denied, api.ErrRefused) || errors.Is(denied, api.ErrUnreviewable) {
		t.Errorf("a denial maps to %v", denied)
	}
	unknown := writeError(edit.Refuse(edit.RefusedUnreviewable, "the console could not ask"))
	if !errors.Is(unknown, api.ErrUnreviewable) || errors.Is(unknown, api.ErrRefused) {
		t.Errorf("an unreviewable write maps to %v", unknown)
	}
}
