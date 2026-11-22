package access_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// apiServer is a cluster that records every request that reached it and
// answers each with something client-go can decode. What it records is the
// whole point: "refused before it leaves the process" is asserted as "this
// list did not grow".
type apiServer struct {
	*httptest.Server
	mu   sync.Mutex
	seen []string
}

func newAPIServer(t *testing.T) *apiServer {
	t.Helper()
	s := &apiServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		s.mu.Lock()
		s.seen = append(s.seen, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "accessreviews"):
			_, _ = io.WriteString(w, `{"kind":"SubjectAccessReview","apiVersion":"authorization.k8s.io/v1","status":{"allowed":true}}`)
		case r.Method == http.MethodDelete:
			_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Success"}`)
		default:
			_, _ = io.WriteString(w, `{"kind":"ConfigMap","apiVersion":"v1","metadata":{"name":"settings","namespace":"shop"}}`)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *apiServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// guarded is a config for the server, guarded as the cluster manager guards
// every config it builds, for the cluster called prod.
func guarded(s *apiServer, path string) *rest.Config {
	cfg := &rest.Config{Host: s.URL + path}
	access.Guard(cfg, "prod", nil)
	return cfg
}

func clientsetFor(t *testing.T, cfg *rest.Config) kubernetes.Interface {
	t.Helper()
	clients, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return clients
}

var deleteSettings = access.Attributes{Verb: "delete", Version: "v1", Resource: "configmaps", Namespace: "shop", Name: "settings"}

// refusedUnsent asserts a request was refused by the guard - as an Unreviewed,
// never as a denial - and that nothing reached the cluster.
func refusedUnsent(t *testing.T, s *apiServer, what string, err error) *access.Unreviewed {
	t.Helper()
	var refusal *access.Unreviewed
	if !errors.Is(err, access.ErrNotReviewed) || !errors.As(err, &refusal) {
		t.Fatalf("%s: answered %v, want the guard's refusal", what, err)
	}
	if errors.Is(err, access.ErrDenied) {
		t.Errorf("%s: the guard's refusal reads as a denial, which sends its reader to the wrong person", what)
	}
	if seen := s.requests(); len(seen) != 0 {
		t.Errorf("%s: the cluster received %v; a refused request must not leave the process", what, seen)
	}
	return refusal
}

// A mutating request with no review behind it, through a client built on the
// guarded config, is refused before it is sent - for the typed client and the
// dynamic one alike, since an extension is handed both.
func TestAWriteNoReviewStampedIsRefusedBeforeItLeaves(t *testing.T) {
	s := newAPIServer(t)
	cfg := guarded(s, "")

	err := clientsetFor(t, cfg).CoreV1().ConfigMaps("shop").Delete(context.Background(), "settings", metav1.DeleteOptions{})
	refusal := refusedUnsent(t, s, "a typed delete", err)
	if refusal.Attributes != deleteSettings || refusal.Stamp != nil {
		t.Errorf("the refusal says %+v (stamp %v), want the request's own attributes and no stamp", refusal.Attributes, refusal.Stamp)
	}
	if msg := err.Error(); !strings.Contains(msg, "no access review was made for it") || !strings.Contains(msg, `"prod"`) {
		t.Errorf("the refusal reads %q; it should say no review was made, and where", msg)
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "new"},
	}}
	_, err = dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace("shop").
		Create(context.Background(), object, metav1.CreateOptions{})
	refusedUnsent(t, s, "a dynamic create", err)
}

// And the same request made inside Perform, whose question is that
// request, goes - and reaches the cluster once.
func TestAWriteInsidePerformWithItsOwnQuestionGoes(t *testing.T) {
	s := newAPIServer(t)
	clients := clientsetFor(t, guarded(s, ""))

	_, err := accesstest.Allow().Perform(context.Background(), "prod", deleteSettings, func(ctx context.Context) error {
		return clients.CoreV1().ConfigMaps("shop").Delete(ctx, "settings", metav1.DeleteOptions{})
	})
	if err != nil {
		t.Fatalf("a reviewed delete was refused: %v", err)
	}
	if seen := s.requests(); len(seen) != 1 || seen[0] != "DELETE /api/v1/namespaces/shop/configmaps/settings" {
		t.Errorf("the cluster received %v, want the one delete", seen)
	}
}

// And in each of the shapes a mismatch takes: a closure whose request is
// not the question it was reviewed with is refused, and the refusal names the
// question that was asked so the bug can be found from the line.
func TestAWriteInsidePerformThatIsNotItsQuestionIsRefused(t *testing.T) {
	other := func(change func(*access.Attributes)) access.Attributes {
		a := deleteSettings
		change(&a)
		return a
	}
	for _, tc := range []struct {
		name     string
		cluster  string
		reviewed access.Attributes
		outer    bool
	}{
		{"another object's name", "prod", other(func(a *access.Attributes) { a.Name = "secrets-of-another" }), false},
		{"another namespace", "prod", other(func(a *access.Attributes) { a.Namespace = "billing" }), false},
		{"another verb", "prod", other(func(a *access.Attributes) { a.Verb = "patch" }), false},
		{"another resource", "prod", other(func(a *access.Attributes) { a.Resource = "secrets" }), false},
		{"a subresource", "prod", other(func(a *access.Attributes) { a.Subresource = "status" }), false},
		{"another group", "prod", other(func(a *access.Attributes) { a.Group = "apps" }), false},
		{"another version", "prod", other(func(a *access.Attributes) { a.Version = "v2" }), false},
		{"another cluster", "staging", deleteSettings, false},
		// The right question, and the request sent on a context captured from
		// outside the closure rather than the one Perform handed it.
		{"the context from outside", "prod", deleteSettings, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newAPIServer(t)
			clients := clientsetFor(t, guarded(s, ""))
			outer := context.Background()
			_, err := accesstest.Allow().Perform(outer, tc.cluster, tc.reviewed, func(ctx context.Context) error {
				if tc.outer {
					ctx = outer
				}
				return clients.CoreV1().ConfigMaps("shop").Delete(ctx, "settings", metav1.DeleteOptions{})
			})
			refusal := refusedUnsent(t, s, tc.name, err)
			if tc.outer {
				return
			}
			if refusal.Stamp == nil || *refusal.Stamp != tc.reviewed || refusal.StampCluster != tc.cluster {
				t.Errorf("the refusal names the review %v on %q, want %v on %q", refusal.Stamp, refusal.StampCluster, tc.reviewed, tc.cluster)
			}
		})
	}
}

// A question about every name, or every namespace, is the wider one: an allowed
// answer to it covers the narrower request. The reverse - a review of one
// object covering a write to its whole kind - is refused above and here.
func TestAWiderQuestionCoversANarrowerRequest(t *testing.T) {
	s := newAPIServer(t)
	clients := clientsetFor(t, guarded(s, ""))
	gate := accesstest.Allow()

	everyNamespace := access.Attributes{Verb: "delete", Resource: "configmaps"}
	if _, err := gate.Perform(context.Background(), "prod", everyNamespace, func(ctx context.Context) error {
		return clients.CoreV1().ConfigMaps("shop").Delete(ctx, "settings", metav1.DeleteOptions{})
	}); err != nil {
		t.Errorf("a review of every configmap did not cover one: %v", err)
	}

	// The narrower question, the wider request: a delete of one object is not a
	// delete of the collection.
	_, err := gate.Perform(context.Background(), "prod", deleteSettings, func(ctx context.Context) error {
		return clients.CoreV1().ConfigMaps("shop").DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
	})
	if !errors.Is(err, access.ErrNotReviewed) {
		t.Errorf("a review of one configmap let the whole collection go: %v", err)
	}
	if seen := s.requests(); len(seen) != 1 {
		t.Errorf("the cluster received %v, want only the covered delete", seen)
	}
}

// Reads go by - the read doors are their question - and so do the reviews
// themselves, which are the one request that cannot be asked about first.
func TestReadsAndTheReviewsThemselvesGoUnstamped(t *testing.T) {
	s := newAPIServer(t)
	clients := clientsetFor(t, guarded(s, ""))

	if _, err := clients.CoreV1().ConfigMaps("shop").Get(context.Background(), "settings", metav1.GetOptions{}); err != nil {
		t.Errorf("a read was refused: %v", err)
	}
	if _, err := clients.AuthorizationV1().SubjectAccessReviews().Create(context.Background(),
		&authorizationv1.SubjectAccessReview{}, metav1.CreateOptions{}); err != nil {
		t.Errorf("a SubjectAccessReview was refused: %v", err)
	}
	if _, err := clients.AuthorizationV1().SelfSubjectAccessReviews().Create(context.Background(),
		&authorizationv1.SelfSubjectAccessReview{}, metav1.CreateOptions{}); err != nil {
		t.Errorf("a SelfSubjectAccessReview was refused: %v", err)
	}
	// Not every create in the authorization group is a review that stores
	// nothing and asks about nobody else's request: the local review is not in
	// the console's use, and stays refused.
	_, err := clients.AuthorizationV1().LocalSubjectAccessReviews("shop").Create(context.Background(),
		&authorizationv1.LocalSubjectAccessReview{}, metav1.CreateOptions{})
	if !errors.Is(err, access.ErrNotReviewed) {
		t.Errorf("a LocalSubjectAccessReview went unstamped: %v", err)
	}
	if seen := s.requests(); len(seen) != 3 {
		t.Errorf("the cluster received %v, want the read and the two reviews", seen)
	}
}

// Housekeeping is a stamp nobody was reviewed for, and it is still a stamp:
// it lets its own request go and no other.
func TestHousekeepingCoversItsOwnRequestOnly(t *testing.T) {
	s := newAPIServer(t)
	clients := clientsetFor(t, guarded(s, ""))
	removal := access.Attributes{Verb: "delete", Version: "v1", Resource: "pods", Namespace: "kubetower-shell", Name: "shell-1"}
	ctx := access.Housekeeping(context.Background(), "prod", removal)

	if err := clients.CoreV1().Pods("kubetower-shell").Delete(ctx, "shell-1", metav1.DeleteOptions{}); err != nil {
		t.Errorf("the housekeeping request it names was refused: %v", err)
	}
	err := clients.CoreV1().Pods("kubetower-shell").Delete(ctx, "somebody-elses", metav1.DeleteOptions{})
	if !errors.Is(err, access.ErrNotReviewed) {
		t.Errorf("a housekeeping stamp let another pod's delete go: %v", err)
	}
	if seen := s.requests(); len(seen) != 1 {
		t.Errorf("the cluster received %v, want the one clean-up", seen)
	}
}

// The shape of a shell or a tunnel: a WebSocket upgrade is a GET, and is still
// refused unstamped - and it goes inside Perform reviewed as `create` on the
// subresource. And Carry takes the stamp to a request built with no context,
// which is what client-go's port-forward dialers send.
func TestAShellOrATunnelIsAChangeWhateverItsMethod(t *testing.T) {
	s := newAPIServer(t)
	cfg := guarded(s, "")
	execURL := s.URL + "/api/v1/namespaces/shop/pods/api-1/exec?command=sh"
	execReview := access.Attributes{Verb: "create", Version: "v1", Resource: "pods", Subresource: "exec", Namespace: "shop", Name: "api-1"}

	send := func(ctx context.Context, cfg *rest.Config) error {
		httpClient, err := rest.HTTPClientFor(cfg)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, execURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := httpClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}

	// context.Background() is what client-go's port-forward dialers build
	// their request on.
	bare := context.Background()
	refusedUnsent(t, s, "an exec upgrade with nothing behind it", send(bare, cfg))

	gate := accesstest.Allow()
	if _, err := gate.Perform(context.Background(), "prod", execReview, func(ctx context.Context) error {
		return send(ctx, cfg)
	}); err != nil {
		t.Errorf("an exec reviewed as create pods/exec was refused: %v", err)
	}
	// Built inside the closure and carrying its stamp, the request with no
	// context of its own goes.
	if _, err := gate.Perform(context.Background(), "prod", execReview, func(ctx context.Context) error {
		carried := rest.CopyConfig(cfg)
		carried.Wrap(access.Carry(ctx))
		return send(bare, carried)
	}); err != nil {
		t.Errorf("a request carried from inside Perform was refused: %v", err)
	}
	// Carry moves a stamp; it cannot make one.
	carried := rest.CopyConfig(cfg)
	carried.Wrap(access.Carry(context.Background()))
	if err := send(bare, carried); !errors.Is(err, access.ErrNotReviewed) {
		t.Errorf("Carry from a context Perform never stamped let the request go: %v", err)
	}
	if seen := s.requests(); len(seen) != 2 {
		t.Errorf("the cluster received %v, want the two reviewed upgrades", seen)
	}
}

// A cluster reached under a path prefix - a proxy serving several clusters -
// is parsed from its own root, so the same review covers the same request.
func TestAClusterUnderAPathPrefixIsReadFromItsRoot(t *testing.T) {
	s := newAPIServer(t)
	clients := clientsetFor(t, guarded(s, "/k8s/clusters/c-1"))

	if _, err := accesstest.Allow().Perform(context.Background(), "prod", deleteSettings, func(ctx context.Context) error {
		return clients.CoreV1().ConfigMaps("shop").Delete(ctx, "settings", metav1.DeleteOptions{})
	}); err != nil {
		t.Errorf("a reviewed delete under a prefix was refused: %v", err)
	}
	err := clients.CoreV1().ConfigMaps("shop").Delete(context.Background(), "settings", metav1.DeleteOptions{})
	if !errors.Is(err, access.ErrNotReviewed) {
		t.Errorf("an unreviewed delete under a prefix went: %v", err)
	}
	if seen := s.requests(); len(seen) != 1 || seen[0] != "DELETE /k8s/clusters/c-1/api/v1/namespaces/shop/configmaps/settings" {
		t.Errorf("the cluster received %v, want the reviewed delete only", seen)
	}
}

// A create is sent to the collection, with no name in its path, and the
// review asks about none (internal/edit.attributesOf says why): the two meet.
func TestACreateIsReviewedWithoutAName(t *testing.T) {
	s := newAPIServer(t)
	clients := clientsetFor(t, guarded(s, ""))
	create := access.Attributes{Verb: "create", Version: "v1", Resource: "configmaps", Namespace: "shop"}
	if _, err := accesstest.Allow().Perform(context.Background(), "prod", create, func(ctx context.Context) error {
		_, err := clients.CoreV1().ConfigMaps("shop").Create(ctx,
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "settings"}}, metav1.CreateOptions{})
		return err
	}); err != nil {
		t.Errorf("a reviewed create was refused: %v", err)
	}
}

// The kubelet behind a node's proxy, through the typed client's REST client -
// what Host.Client hands an extension. Its exec is refused unstamped; the
// kubelet-stats read the console itself makes goes.
func TestAKubeletExecThroughTheNodeProxyIsRefusedAndItsStatsAreNot(t *testing.T) {
	s := newAPIServer(t)
	rc := clientsetFor(t, guarded(s, "")).CoreV1().RESTClient()

	err := rc.Get().AbsPath("/api/v1/nodes/worker-1/proxy/exec/shop/api-1/app").
		Param("command", "sh").Do(context.Background()).Error()
	refusedUnsent(t, s, "a kubelet exec through the node proxy", err)

	if _, err := rc.Get().Resource("nodes").Name("worker-1").SubResource("proxy").
		Suffix("stats", "summary").DoRaw(context.Background()); err != nil {
		t.Errorf("the kubelet-stats read was refused: %v", err)
	}
	if seen := s.requests(); len(seen) != 1 || seen[0] != "GET /api/v1/nodes/worker-1/proxy/stats/summary" {
		t.Errorf("the cluster received %v, want the stats read only", seen)
	}
}
