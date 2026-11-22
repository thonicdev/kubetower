package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/resources"
)

// Which kinds hold an object the person may see, against
// an API server that answers lists from a table, and a gate answering from a
// function. Unknown is never none.

// fakeObjects is an API server holding objects per list path: a count, or a
// status it refuses with. A path it does not name holds nothing.
type fakeObjects struct {
	mu      sync.Mutex
	count   map[string]int
	status  map[string]int
	asked   []*http.Request
	answers int
	// hold, when set, sees each request before it is answered, and may keep it
	// waiting.
	hold func(*http.Request)
}

func (f *fakeObjects) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.hold != nil {
		f.hold(r)
	}
	f.mu.Lock()
	f.asked = append(f.asked, r.Clone(context.Background()))
	f.answers++
	n, status := f.count[r.URL.Path], f.status[r.URL.Path]
	f.mu.Unlock()
	if !strings.HasPrefix(r.URL.Path, "/api/v1/") && !strings.HasPrefix(r.URL.Path, "/apis/") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","code":%d,"message":"refused by the fake"}`, status)
		return
	}
	items := make([]string, 0, n)
	for i := 0; i < n && i < 1; i++ {
		items = append(items, fmt.Sprintf(`{"metadata":{"name":"o-%d"}}`, i))
	}
	_, _ = fmt.Fprintf(w, `{"kind":"PartialObjectMetadataList","apiVersion":"meta.k8s.io/v1","metadata":{},"items":[%s]}`,
		strings.Join(items, ","))
}

func (f *fakeObjects) requests() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.asked...)
}

func (f *fakeObjects) pathsAsked() []string {
	var out []string
	for _, r := range f.requests() {
		out = append(out, r.URL.Path)
	}
	return out
}

// occupancyServer is a Server over one context, "prod", whose API server is
// objects, with the gate answering through decide and the console's
// namespace hold holding names (failed where names is nil).
func occupancyServer(t *testing.T, inCluster bool, objects *fakeObjects, names []string,
	decide func(accesstest.Question) (bool, error)) (*Server, *cluster.Client, *accesstest.Reviews) {
	t.Helper()
	apiServer := httptest.NewServer(objects)
	t.Cleanup(apiServer.Close)
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
current-context: prod
clusters:
- name: remote
  cluster:
    server: `+apiServer.URL+`
contexts:
- name: prod
  context: {cluster: remote, user: none}
users:
- name: none
  user: {token: none}
`), 0o600); err != nil {
		t.Fatalf("writing the kubeconfig: %v", err)
	}
	manager, err := cluster.NewManager(path)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	reviews := &accesstest.Reviews{Answer: decide}
	s, err := New(Options{Clusters: manager, InCluster: inCluster,
		Access: access.New(access.Options{Reviews: reviews.For, InCluster: inCluster})})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now()
	s.namespaces.held["prod"] = namespaceHold{names: names, at: now, readAt: now, failed: names == nil}
	client, err := manager.Client("prod")
	if err != nil {
		t.Fatalf("the client: %v", err)
	}
	return s, client, reviews
}

var (
	podsKind    = occupancyKind{slug: "pods", question: resources.InNamespace("", "v1", "pods", "")}
	classesKind = occupancyKind{slug: "ingressclasses",
		question: resources.ClusterScoped("networking.k8s.io", "v1", "ingressclasses")}
	argoKind = occupancyKind{slug: "applications.argoproj.io",
		question: resources.InNamespace("argoproj.io", "v1alpha1", "applications", "")}
)

// asAlice is a person signed in through the identity provider, reviewed as
// herself under the server profile.
func asAlice() context.Context {
	return identity.With(context.Background(), identity.Who{Name: "alice", Method: identity.SSO, Groups: []string{"dev"}})
}

// consoleAllowed lets the console list everything, and decides a person's
// questions through person.
func consoleAllowed(person func(accesstest.Question) (bool, error)) func(accesstest.Question) (bool, error) {
	return func(q accesstest.Question) (bool, error) {
		if q.Self {
			return true, nil
		}
		return person(q)
	}
}

func stateOfKind(t *testing.T, got map[string]api.KindOccupancy, slug string) api.KindOccupancy {
	t.Helper()
	k, ok := got[slug]
	if !ok {
		t.Fatalf("%s is not in the answer %+v; a kind left out reads as unknown, but every kind asked about is answered", slug, got)
	}
	return k
}

// The desktop: the person is the console, and the three answers come from
// one list each.
func TestOccupancyOnTheDesktopIsTheConsolesOwnRead(t *testing.T) {
	objects := &fakeObjects{
		count:  map[string]int{"/api/v1/pods": 3},
		status: map[string]int{"/apis/argoproj.io/v1alpha1/applications": http.StatusInternalServerError},
	}
	s, client, _ := occupancyServer(t, false, objects, []string{"default"}, nil)
	got := s.occupancyOf(context.Background(), "prod", client, []occupancyKind{podsKind, classesKind, argoKind})

	if k := stateOfKind(t, got, "pods"); k.State != api.OccupancySome {
		t.Errorf("pods: %+v; the cluster holds three", k)
	}
	if k := stateOfKind(t, got, "ingressclasses"); k.State != api.OccupancyNone || k.Reason != "" {
		t.Errorf("ingressclasses: %+v; the cluster holds none, and none carries no reason", k)
	}
	// Unknown is never zero.
	if k := stateOfKind(t, got, "applications.argoproj.io"); k.State != api.OccupancyUnknown || k.Reason == "" {
		t.Errorf("applications: %+v; a list that failed is unknown, with its reason", k)
	}
	for _, r := range objects.requests() {
		q := r.URL.Query()
		if q.Get("limit") != "1" {
			t.Errorf("%s asked limit=%q; one row answers the question", r.URL.Path, q.Get("limit"))
		}
		// resourceVersion=0 is the watch cache, where some API servers ignore
		// the limit and send the whole list.
		if q.Has("resourceVersion") {
			t.Errorf("%s asked resourceVersion=%q; a one-row read is not served from the watch cache", r.URL.Path,
				q.Get("resourceVersion"))
		}
		if !strings.Contains(r.Header.Get("Accept"), "as=PartialObjectMetadataList") {
			t.Errorf("%s accepted %q; metadata only, so no Secret's values are ever sent", r.URL.Path, r.Header.Get("Accept"))
		}
	}
}

// What the console read is held: a second request, by anybody, lists nothing.
func TestOccupancyIsHeldForEveryPerson(t *testing.T) {
	objects := &fakeObjects{count: map[string]int{"/api/v1/pods": 1}}
	s, client, _ := occupancyServer(t, true, objects, []string{"default"}, consoleAllowed(allowIn("pods", "")))
	s.occupancyOf(asAlice(), "prod", client, []occupancyKind{podsKind, classesKind})
	before := len(objects.requests())
	bob := identity.With(context.Background(), identity.Who{Name: "bob", Method: identity.SSO})
	s.occupancyOf(bob, "prod", client, []occupancyKind{podsKind, classesKind})
	if after := len(objects.requests()); after != before {
		t.Errorf("the second request listed %d more times; the console's answer is held", after-before)
	}
}

// A list the console may not make is unknown, and nothing is sent for it.
func TestOccupancyOfAKindTheConsoleMayNotListIsUnknown(t *testing.T) {
	objects := &fakeObjects{count: map[string]int{"/apis/argoproj.io/v1alpha1/applications": 2}}
	s, client, _ := occupancyServer(t, true, objects, []string{"default"}, func(q accesstest.Question) (bool, error) {
		return q.Attributes.Resource != "applications", nil
	})
	got := s.occupancyOf(asAlice(), "prod", client, []occupancyKind{argoKind})
	if k := stateOfKind(t, got, argoKind.slug); k.State != api.OccupancyUnknown || !strings.Contains(k.Reason, "may not list") {
		t.Errorf("applications: %+v; refused to the console is unknown, saying so", k)
	}
	if asked := objects.pathsAsked(); len(asked) != 0 {
		t.Errorf("listed %v; a list the console was refused is not sent", asked)
	}
}

// The person's own review decides what they are told of the console's read.
func TestOccupancyIsTheConsolesReadReviewedForThePerson(t *testing.T) {
	objects := &fakeObjects{count: map[string]int{"/api/v1/pods": 1, "/apis/networking.k8s.io/v1/ingressclasses": 1}}
	for _, tc := range []struct {
		name   string
		person func(accesstest.Question) (bool, error)
		pods   api.Occupancy
		class  api.Occupancy
	}{
		{"allowed across the cluster", allowIn("pods", ""), api.OccupancySome, api.OccupancyNone},
		{"allowed both", func(accesstest.Question) (bool, error) { return true, nil },
			api.OccupancySome, api.OccupancySome},
		// Held by the cluster, and nothing of it theirs to see.
		{"a cluster-scoped kind denied", allowIn("nothing"), api.OccupancyNone, api.OccupancyNone},
		{"a review nobody could make", func(accesstest.Question) (bool, error) {
			return false, errors.New("connection refused")
		}, api.OccupancyUnknown, api.OccupancyUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No namespace: a denied namespaced kind would otherwise be
			// asked of them, which the next test is about.
			s, client, _ := occupancyServer(t, true, objects, []string{}, consoleAllowed(tc.person))
			got := s.occupancyOf(asAlice(), "prod", client, []occupancyKind{podsKind, classesKind})
			if k := stateOfKind(t, got, "ingressclasses"); k.State != tc.class {
				t.Errorf("ingressclasses: %+v; want %s", k, tc.class)
			}
			if tc.name == "a cluster-scoped kind denied" {
				// pods denied across the cluster with no namespace known:
				// not known where they may list it.
				if k := stateOfKind(t, got, "pods"); k.State != api.OccupancyUnknown {
					t.Errorf("pods: %+v; with no namespace to ask, unknown", k)
				}
				return
			}
			if k := stateOfKind(t, got, "pods"); k.State != tc.pods {
				t.Errorf("pods: %+v; want %s", k, tc.pods)
			}
		})
	}
}

// A person denied the list across the cluster is asked per namespace, and
// only a namespace they may list counts - an object in another one is not
// theirs to see, so the kind is none for them.
func TestANarrowedPersonIsAnsweredFromTheNamespacesTheyMayList(t *testing.T) {
	names := []string{"empty", "other", "shop"}
	for _, tc := range []struct {
		name    string
		allowed []string
		want    api.Occupancy
	}{
		{"one of theirs holds some", []string{"empty", "shop"}, api.OccupancySome},
		{"theirs hold none, another does", []string{"empty"}, api.OccupancyNone},
		{"they may list it nowhere", nil, api.OccupancyNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := &fakeObjects{count: map[string]int{
				"/api/v1/pods": 1, "/api/v1/namespaces/shop/pods": 1, "/api/v1/namespaces/other/pods": 4}}
			s, client, _ := occupancyServer(t, true, objects, names, consoleAllowed(allowIn("pods", tc.allowed...)))
			got := s.occupancyOf(asAlice(), "prod", client, []occupancyKind{podsKind})
			if k := stateOfKind(t, got, "pods"); k.State != tc.want {
				t.Errorf("pods: %+v; want %s", k, tc.want)
			}
			for _, path := range objects.pathsAsked() {
				if path == "/api/v1/namespaces/other/pods" {
					t.Error("other was listed; nothing is read in a namespace the person may not list")
				}
			}
		})
	}
}

// Every way of not knowing, on the narrowed path, is unknown.
func TestANarrowedPersonsAnswerThatCouldNotBeHadIsUnknown(t *testing.T) {
	inShop := consoleAllowed(allowIn("pods", "shop"))
	for _, tc := range []struct {
		name    string
		names   []string
		decide  func(accesstest.Question) (bool, error)
		refused bool
	}{
		{"the namespaces could not be read", nil, inShop, false},
		{"the list in their namespace failed", []string{"shop"}, inShop, true},
		{"a namespace's review could not be made", []string{"default", "shop"},
			consoleAllowed(func(q accesstest.Question) (bool, error) {
				if q.Attributes.Namespace == "default" {
					return false, errors.New("connection refused")
				}
				return false, nil
			}), false},
		{"more namespaces than it narrows to", namespaces(access.NarrowedNamespaces + 1), inShop, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := &fakeObjects{count: map[string]int{"/api/v1/pods": 1}}
			if tc.refused {
				objects.status = map[string]int{"/api/v1/namespaces/shop/pods": http.StatusInternalServerError}
			}
			s, client, _ := occupancyServer(t, true, objects, tc.names, tc.decide)
			got := s.occupancyOf(asAlice(), "prod", client, []occupancyKind{podsKind})
			if k := stateOfKind(t, got, "pods"); k.State != api.OccupancyUnknown || k.Reason == "" {
				t.Errorf("pods: %+v; not known is unknown, with a reason - never none", k)
			}
		})
	}
}

// The request's review budget: a walk it does not cover is not started, and
// the kind is unknown rather than none.
func TestANarrowedWalkPastTheBudgetIsUnknown(t *testing.T) {
	objects := &fakeObjects{count: map[string]int{"/api/v1/pods": 1}}
	s, client, reviews := occupancyServer(t, true, objects, namespaces(access.NarrowedNamespaces),
		consoleAllowed(allowIn("nothing")))
	budget := &occupancyBudget{reviews: access.NarrowedNamespaces - 1, probes: occupancyProbes}
	names := func() namespaceAnswer { return s.namespaceAnswer(context.Background(), "prod") }
	k := s.kindOccupancy(asAlice(), "prod", client, podsKind, budget, names)
	if k.State != api.OccupancyUnknown {
		t.Errorf("pods: %+v; a walk past the budget is unknown", k)
	}
	for _, q := range reviews.Asked() {
		if !q.Self && q.Attributes.Namespace != "" {
			t.Fatalf("asked %+v; a walk the budget does not cover is not started", q)
		}
	}
}

// The operation itself: every kind of the menu answered, by slug, and the
// custom resource kinds that could not be listed said in the note.
func TestNavOccupancyAnswersEveryBuiltInKindBySlug(t *testing.T) {
	objects := &fakeObjects{count: map[string]int{"/api/v1/pods": 1},
		status: map[string]int{"/apis/apiextensions.k8s.io/v1/customresourcedefinitions": http.StatusForbidden}}
	s, _, _ := occupancyServer(t, false, objects, []string{"default"}, nil)
	got, fail := apiClusters{s}.NavOccupancy(context.Background(), "prod")
	if fail != nil {
		t.Fatalf("the operation failed: %+v", fail.Reason)
	}
	for _, d := range resources.Kinds() {
		if _, ok := got.Kinds[d.Kind]; !ok {
			t.Errorf("%s, a kind of the menu, is not answered", d.Kind)
		}
	}
	if k := got.Kinds["pods"]; k.State != api.OccupancySome {
		t.Errorf("pods: %+v; the cluster holds one", k)
	}
	if k := got.Kinds["nodes"]; k.State != api.OccupancyNone {
		t.Errorf("nodes: %+v; the cluster holds none", k)
	}
	// The CRDs could not be listed: said, and none of them is answered none.
	if got.Note == "" {
		t.Error("the custom resource kinds could not be listed and the note says nothing")
	}
}

// The CRD list, the slow read, does not hold back the kinds discovery names:
// they are counted while it is still being read, and the answer still waits
// for both halves.
func TestNavOccupancyCountsTheServedKindsWhileTheCRDsAreRead(t *testing.T) {
	const definitions = "/apis/apiextensions.k8s.io/v1/customresourcedefinitions"
	release := make(chan struct{})
	podsCounted := make(chan struct{})
	var counted sync.Once
	objects := &fakeObjects{count: map[string]int{"/api/v1/pods": 1}}
	objects.hold = func(r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/pods":
			counted.Do(func() { close(podsCounted) })
		case r.URL.Path == definitions && r.URL.Query().Get("limit") != "1":
			// The catalog's list, not the CRDs kind's own one-row count.
			<-release
		}
	}
	s, _, _ := occupancyServer(t, false, objects, []string{"default"}, nil)

	answered := make(chan api.NavOccupancy, 1)
	go func() {
		got, _ := apiClusters{s}.NavOccupancy(context.Background(), "prod")
		answered <- got
	}()
	select {
	case <-podsCounted:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("pods were not counted while the CRD list was still being read")
	}
	select {
	case got := <-answered:
		close(release)
		t.Fatalf("answered %+v before the CRD list landed", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)

	got := <-answered
	if k := got.Kinds["pods"]; k.State != api.OccupancySome {
		t.Errorf("pods: %+v; the cluster holds one", k)
	}
	if got.Note != "" {
		t.Errorf("the CRDs were listed and the note says %q", got.Note)
	}
}

func TestNavOccupancyOfAClusterThatIsNotKnownIsAFailure(t *testing.T) {
	s, _, _ := occupancyServer(t, false, &fakeObjects{}, []string{"default"}, nil)
	if _, fail := (apiClusters{s}).NavOccupancy(context.Background(), "nowhere"); fail == nil {
		t.Error("a cluster nobody configured answered an occupancy")
	}
}

// An answer past its own TTL is dropped when another is filed, so a
// namespace that is gone is not held for the life of the process.
func TestOccupancyAnswersPastTheirTTLAreDropped(t *testing.T) {
	objects := &fakeObjects{
		count:  map[string]int{"/api/v1/namespaces/a/pods": 1},
		status: map[string]int{"/api/v1/namespaces/b/pods": http.StatusInternalServerError},
	}
	s, client, _ := occupancyServer(t, false, objects, []string{"default"}, nil)
	clock := time.Now()
	s.occupancy.now = func() time.Time { return clock }
	file := func(namespace string) occupancyAnswer {
		t.Helper()
		a := s.consoleOccupancy(context.Background(), client, podsKind, namespace)
		if a.passing {
			t.Fatalf("%s: %+v; a finished read is held", namespace, a)
		}
		return a
	}
	heldIn := func() map[string]bool {
		s.occupancy.mu.Lock()
		defer s.occupancy.mu.Unlock()
		out := map[string]bool{}
		for key := range s.occupancy.held {
			out[key.namespace] = true
		}
		return out
	}
	want := func(when string, namespaces ...string) {
		t.Helper()
		got := heldIn()
		if len(got) != len(namespaces) {
			t.Errorf("%s: held %v; want exactly %v", when, got, namespaces)
			return
		}
		for _, n := range namespaces {
			if !got[n] {
				t.Errorf("%s: held %v; want exactly %v", when, got, namespaces)
			}
		}
	}

	if a := file("a"); a.state != api.OccupancySome {
		t.Fatalf("a: %+v; the namespace holds one pod", a)
	}
	if a := file("b"); a.state != api.OccupancyUnknown {
		t.Fatalf("b: %+v; a list that failed is unknown", a)
	}
	want("both filed", "a", "b")

	clock = clock.Add(20 * time.Second)
	file("c")
	want("20 s on, the unknown is past its 15 s", "a", "c")

	clock = clock.Add(occupancyTTL + time.Second)
	file("d")
	want("past 3 min, every earlier answer is", "d")
}
