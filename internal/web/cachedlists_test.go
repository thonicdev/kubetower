package web

// What the two kubelet-backed readings put on the wire, and what happens when
// the API server hands back a continue token.
//
// These drive a real client-go clientset against an httptest server rather than
// the fake clientset the rest of this package uses, because both properties
// being pinned here are invisible to the fake: it ignores `resourceVersion` and
// `limit` alike and answers everything from a tracker.
//
// The two are one change and they have to be tested as one. Asking with
// `resourceVersion: "0"` is what keeps these lists off etcd — without it the API
// server answers by doing a quorum read, and both of these run behind pages that
// poll. But `resources.Cached` carries a `Limit` with it, so adding it alone
// converts an unbounded-but-correct list into a silently truncated one on any
// cluster with more than five hundred objects in scope. The options assertions
// below fail if the options are missing; the truncation assertions fail on code
// that adds `Cached` without `Paged`, which is the worse failure.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/resources"
)

// pagingServer answers list calls the way an API server that honors `limit`
// does: a chunk at a time, with a continue token naming where the next one
// starts, and the whole list in one response when no limit was asked for.
//
// That last clause is what makes this a fair fixture rather than a rigged one.
// A server that paged regardless of the limit would make today's bare
// `ListOptions{}` look broken when it is merely expensive, and the point of
// these tests is that it is the *limit* that introduces the risk.
type pagingServer struct {
	mu    sync.Mutex
	asked map[string][]url.Values

	nodes  []any
	pods   []any
	claims []any

	// summary is what every kubelet answers with, or "" for a node proxy that
	// refuses. The node's own filesystem only; the claims tests do not need one.
	summary string
}

func (s *pagingServer) record(resource string, q url.Values) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.asked == nil {
		s.asked = map[string][]url.Values{}
	}
	s.asked[resource] = append(s.asked[resource], q)
}

func (s *pagingServer) queries(resource string) []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values(nil), s.asked[resource]...)
}

func (s *pagingServer) start(t *testing.T) kubernetes.Interface {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if strings.HasSuffix(path, "/proxy/stats/summary") {
			s.record("proxy", r.URL.Query())
			if s.summary == "" {
				http.Error(w, "no kubelet here", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, s.summary)
			return
		}

		var items []any
		var resource string
		switch {
		case strings.HasSuffix(path, "/nodes"):
			items, resource = s.nodes, "nodes"
		case strings.HasSuffix(path, "/pods"):
			items, resource = s.pods, "pods"
		case strings.HasSuffix(path, "/persistentvolumeclaims"):
			items, resource = s.claims, "persistentvolumeclaims"
		default:
			http.Error(w, "unexpected path "+path, http.StatusNotFound)
			return
		}
		s.record(resource, r.URL.Query())

		from, _ := strconv.Atoi(r.URL.Query().Get("continue"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		to := len(items)
		if limit > 0 && from+limit < to {
			to = from + limit
		}
		token := ""
		if to < len(items) {
			token = strconv.Itoa(to)
		}

		body, _ := json.Marshal(map[string]any{
			"metadata": map[string]any{"resourceVersion": "42", "continue": token},
			"items":    items[from:to],
		})
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(server.Close)

	// QPS below zero disables client-go's own rate limiter outright. These tests
	// make five hundred requests against a local server and a throttled client
	// would turn that into minutes.
	api, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, QPS: -1})
	if err != nil {
		t.Fatalf("building a client: %v", err)
	}
	return api
}

// --- fixtures -----------------------------------------------------------------

func nodeItem(name string, ready bool) any {
	status := "False"
	if ready {
		status = "True"
	}
	return map[string]any{
		"metadata": map[string]any{"name": name},
		"status": map[string]any{
			"conditions": []any{map[string]any{"type": "Ready", "status": status}},
		},
	}
}

func claimItem(namespace, name string) any {
	return map[string]any{
		"metadata": map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"resources": map[string]any{"requests": map[string]any{"storage": "1Gi"}},
		},
	}
}

func podItem(namespace, name, node, claim string) any {
	volumes := []any{}
	if claim != "" {
		volumes = append(volumes, map[string]any{
			"name":                  "data",
			"persistentVolumeClaim": map[string]any{"claimName": claim},
		})
	}
	return map[string]any{
		"metadata": map[string]any{"name": name, "namespace": namespace},
		"spec":     map[string]any{"nodeName": node, "volumes": volumes},
		"status":   map[string]any{"phase": "Running"},
	}
}

const oneNodeFilesystem = `{"node":{"fs":{"usedBytes":40,"capacityBytes":100}}}`

// assertWatchCache is the options half, and it is the same assertion for every
// list: `resourceVersion=0` or the API server does a quorum read out of etcd,
// and `limit` at the chunk size the rest of the console uses.
func assertWatchCache(t *testing.T, what string, asked []url.Values) {
	t.Helper()
	if len(asked) == 0 {
		t.Fatalf("%s was never listed", what)
	}
	if got := asked[0].Get("resourceVersion"); got != "0" {
		t.Errorf("%s: resourceVersion is %q, so this list is a quorum read out of etcd", what, got)
	}
	if got := asked[0].Get("limit"); got != fmt.Sprint(resources.PageSize) {
		t.Errorf("%s: limit is %q, want %d", what, got, resources.PageSize)
	}
}

// --- the options -------------------------------------------------------------

func TestNodeDiskIsListedFromTheWatchCache(t *testing.T) {
	server := &pagingServer{nodes: []any{nodeItem("node-a", true)}, summary: oneNodeFilesystem}
	api := server.start(t)

	if got := readNodeDisk(context.Background(), api); len(got) != 1 {
		t.Fatalf("%d nodes with a disk figure, want 1", len(got))
	}
	assertWatchCache(t, "nodes", server.queries("nodes"))
}

func TestVolumeUsageIsListedFromTheWatchCache(t *testing.T) {
	server := &pagingServer{
		claims: []any{claimItem("apps", "claim-a")},
		pods:   []any{podItem("apps", "pod-a", "node-a", "claim-a")},
	}
	api := server.start(t)

	if got := readVolumeUsage(context.Background(), api, "apps"); len(got) != 1 {
		t.Fatalf("%d claims came back, want 1", len(got))
	}
	assertWatchCache(t, "persistentvolumeclaims", server.queries("persistentvolumeclaims"))
	assertWatchCache(t, "pods", server.queries("pods"))
}

// --- the truncation ----------------------------------------------------------

// The node past the chunk size is the last one, and it is the only other Ready
// node in the list — so a list that stopped at five hundred comes back with one
// disk figure where the cluster has two, and the operator sees an em dash on a
// node whose kubelet answered perfectly well.
func TestNodeDiskIsNotTruncatedAtTheChunkSize(t *testing.T) {
	const total = resources.PageSize + 1
	nodes := make([]any, 0, total)
	for i := 0; i < total; i++ {
		ready := i == 0 || i == total-1
		nodes = append(nodes, nodeItem(fmt.Sprintf("node-%04d", i), ready))
	}
	server := &pagingServer{nodes: nodes, summary: oneNodeFilesystem}
	api := server.start(t)

	got := readNodeDisk(context.Background(), api)
	if len(got) != 2 {
		t.Fatalf("%d nodes have a disk figure, want 2 — the %dth node is past the "+
			"chunk size, so this list stopped at %d", len(got), total, resources.PageSize)
	}
	if _, ok := got[fmt.Sprintf("node-%04d", total-1)]; !ok {
		t.Errorf("the last node of %d is missing, so the continue token was not followed", total)
	}
	if pages := len(server.queries("nodes")); pages != 2 {
		t.Errorf("%d list requests, want 2 — one per page", pages)
	}
}

// Every claim in scope gets an entry, measured or not: joinVolumeUsage is
// explicit that a list page has nowhere else to put "why is there no figure".
// So the count of entries is the count of claims, and a short list here is a
// storage table missing rows it claims to be showing.
func TestTheClaimsListIsNotTruncatedAtTheChunkSize(t *testing.T) {
	const total = resources.PageSize + 1
	claims := make([]any, 0, total)
	for i := 0; i < total; i++ {
		claims = append(claims, claimItem("apps", fmt.Sprintf("claim-%04d", i)))
	}
	server := &pagingServer{claims: claims}
	api := server.start(t)

	got := readVolumeUsage(context.Background(), api, "apps")
	if len(got) != total {
		t.Fatalf("%d claims came back, want %d — the list stopped at the chunk size", len(got), total)
	}
	if pages := len(server.queries("persistentvolumeclaims")); pages != 2 {
		t.Errorf("%d list requests, want 2 — one per page", pages)
	}
}

// The pod list is the half that fails quietly rather than visibly: a truncated
// claims list loses rows, but a truncated *pod* list keeps every row and tells
// the operator that nothing has the claim mounted — a sentence that is wrong
// rather than absent, about a claim a pod is using right now.
func TestThePodListIsNotTruncatedAtTheChunkSize(t *testing.T) {
	const total = resources.PageSize + 1
	pods := make([]any, 0, total)
	for i := 0; i < total-1; i++ {
		pods = append(pods, podItem("apps", fmt.Sprintf("pod-%04d", i), "node-a", ""))
	}
	pods = append(pods, podItem("apps", "pod-mounting-it", "node-a", "claim-a"))

	server := &pagingServer{claims: []any{claimItem("apps", "claim-a")}, pods: pods}
	api := server.start(t)

	got := readVolumeUsage(context.Background(), api, "apps")
	volume, ok := got["apps/claim-a"]
	if !ok {
		t.Fatalf("the claim is not in the answer at all: %v", got)
	}
	if strings.Contains(volume.Note, "No running pod") {
		t.Errorf("the claim reads as unmounted though pod %d of %d mounts it, so "+
			"the pod list stopped at the chunk size: %q", total, total, volume.Note)
	}
	if pages := len(server.queries("pods")); pages != 2 {
		t.Errorf("%d list requests, want 2 — one per page", pages)
	}
}
