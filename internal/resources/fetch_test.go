package resources

// What actually goes on the wire for a list, and what comes back off it.
//
// These drive a real client-go clientset against an httptest server rather than
// the fake clientset the rest of the suite uses, because the two things being
// pinned here — the query string a list is asked with, and what happens when the
// API server hands back a continue token — are invisible to the fake: it ignores
// both and answers from a tracker.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// listServer answers every list with the pages it was given, recording what was
// asked for.
type listServer struct {
	mu     sync.Mutex
	asked  []url.Values
	pages  []string // the continue token each response carries, "" to stop
	served int
}

func (s *listServer) start(t *testing.T) kubernetes.Interface {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.asked = append(s.asked, r.URL.Query())
		token := ""
		if s.served < len(s.pages) {
			token = s.pages[s.served]
		}
		name := fmt.Sprintf("item-%d", s.served)
		s.served++
		s.mu.Unlock()

		body, _ := json.Marshal(map[string]any{
			"metadata": map[string]any{"resourceVersion": "42", "continue": token},
			"items":    []any{map[string]any{"metadata": map[string]any{"name": name, "namespace": "kube-system"}}},
		})
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(server.Close)

	api, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatalf("building a client: %v", err)
	}
	return api
}

func (s *listServer) queries() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values(nil), s.asked...)
}

// allowed is a Reader whose access review lets everything through: what these
// tests pin is how a list is read, and access_test.go pins whether it is.
func allowed(api kubernetes.Interface) Reader {
	return Reader{Gate: accesstest.Allow(), Cluster: "test", Clients: api}
}

// Every list in this package is answered from the API server's watch cache
// rather than by a quorum read out of etcd. It is one parameter and it is the
// difference between a table that costs the cluster nothing to redraw every two
// seconds and one that makes etcd read the whole of a kind each time.
func TestEveryKindIsListedFromTheWatchCache(t *testing.T) {
	for _, descriptor := range Kinds() {
		t.Run(descriptor.Kind, func(t *testing.T) {
			server := &listServer{}
			api := server.start(t)

			if _, _, err := descriptor.list(context.Background(), allowed(api), "kube-system"); err != nil {
				t.Fatalf("listing: %v", err)
			}
			asked := server.queries()
			if len(asked) != 1 {
				t.Fatalf("%d requests for one list", len(asked))
			}
			if got := asked[0].Get("resourceVersion"); got != "0" {
				t.Errorf("resourceVersion is %q, so this list is a quorum read out of etcd", got)
			}
			if got := asked[0].Get("limit"); got != fmt.Sprint(PageSize) {
				t.Errorf("limit is %q, want %d", got, PageSize)
			}
		})
	}
}

// The limit is a chunk size, never a cap. An API server that honors it hands
// back a continue token, and a list that stopped at the first page would be a
// table missing rows it claims to be showing — and a filter box answering "no
// results" for a name that is in the cluster.
func TestAContinueTokenIsFollowedToTheEndOfTheList(t *testing.T) {
	server := &listServer{pages: []string{"page-2", "page-3", ""}}
	api := server.start(t)

	pods, _, err := fetchPods(context.Background(), allowed(api), "kube-system")
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) != 3 {
		t.Fatalf("%d pods came back, want the 3 the server had across its pages", len(pods))
	}

	asked := server.queries()
	if len(asked) != 3 {
		t.Fatalf("%d requests, want one per page", len(asked))
	}
	if asked[0].Get("continue") != "" {
		t.Error("the first request carried a continue token")
	}
	for i, page := range asked[1:] {
		if page.Get("continue") == "" {
			t.Errorf("request %d carried no continue token, so it re-read the first page", i+2)
		}
		// The API server refuses continue and resourceVersion together: the token
		// already pins the snapshot the rest of the pages are read from.
		if got := page.Get("resourceVersion"); got != "" {
			t.Errorf("request %d carried resourceVersion=%q alongside a continue token, which the API server rejects", i+2, got)
		}
	}
}

// The other API server. One serves the whole list from its watch cache and
// ignores the limit entirely; the difference must not be visible to a caller.
func TestAServerThatIgnoresTheLimitStillGivesACompleteList(t *testing.T) {
	server := &listServer{}
	api := server.start(t)

	pods, _, err := fetchPods(context.Background(), allowed(api), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) != 1 {
		t.Fatalf("%d pods, want the 1 the server sent", len(pods))
	}
	if len(server.queries()) != 1 {
		t.Fatalf("%d requests for a list with no continue token", len(server.queries()))
	}
}

// A cluster-scoped kind must not acquire a namespace on its way to the API
// server. Paging rewrites the options between pages, which is exactly where a
// field like that gets carried by accident.
func TestPagingDoesNotChangeWhatWasAskedFor(t *testing.T) {
	server := &listServer{pages: []string{"page-2", ""}}
	api := server.start(t)

	if _, _, err := fetchNodes(context.Background(), allowed(api), "kube-system"); err != nil {
		t.Fatal(err)
	}
	for i, page := range server.queries() {
		if strings.Contains(page.Encode(), "namespace") {
			t.Errorf("request %d named a namespace on a cluster-scoped kind: %s", i+1, page.Encode())
		}
	}
}
