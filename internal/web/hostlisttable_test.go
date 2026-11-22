package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// tableServer plays an API server that answers a list as its table when asked
// with `as=Table`, and records every request it is sent.
//
//   - pods: three, billing's first, honoring a limit with a continue token;
//   - vulnerabilityreports.aquasecurity.github.io: two reports, always over two
//     pages, the second reached with a continue token;
//   - widgets.example.io: answers a WidgetList whatever is asked, as an API
//     server that ignores the Accept header would.
type tableServer struct {
	mu       sync.Mutex
	requests []*http.Request
}

func (s *tableServer) seen() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*http.Request(nil), s.requests...)
}

func podRow(name, namespace string, restarts int) map[string]any {
	return map[string]any{
		"cells": []any{name, "1/1", "Running", restarts, "5m"},
		"object": map[string]any{"kind": "PartialObjectMetadata", "apiVersion": "meta.k8s.io/v1",
			"metadata": map[string]any{"name": name, "namespace": namespace,
				"creationTimestamp": "2026-11-01T10:00:00Z"}},
	}
}

func reportRow(name string, critical int, score any) map[string]any {
	return map[string]any{
		"cells": []any{name, "ghcr.io/x/api", "Trivy", critical, score, true, nil, map[string]any{"a": 1}},
		"object": map[string]any{"kind": "PartialObjectMetadata", "apiVersion": "meta.k8s.io/v1",
			"metadata": map[string]any{
				"name": name, "namespace": "shop", "creationTimestamp": "2026-11-02T08:30:00Z",
				"labels":      map[string]any{"trivy-operator.container.name": "api"},
				"annotations": map[string]any{"trivy-operator.aquasecurity.github.io/report-ttl": "24h"},
				"ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSet",
					"name": "api-7d9", "uid": "u-1", "controller": true}},
			}},
	}
}

func (s *tableServer) host(t *testing.T) extension.Host {
	t.Helper()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	table := func(columns []string, rows []any, next string) map[string]any {
		defs := make([]any, len(columns))
		for i, c := range columns {
			defs[i] = map[string]any{"name": c, "type": "string", "priority": i % 2}
		}
		return map[string]any{"kind": "Table", "apiVersion": "meta.k8s.io/v1",
			"metadata":          map[string]any{"resourceVersion": "9", "continue": next},
			"columnDefinitions": defs, "rows": rows}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Clone(r.Context()))
		s.mu.Unlock()
		query := r.URL.Query()
		switch r.URL.Path {
		case "/api/v1/pods", "/api/v1/namespaces/shop/pods":
			rows := []any{podRow("bill-0", "billing", 0), podRow("api-0", "shop", 2), podRow("api-1", "shop", 0)}
			if r.URL.Path != "/api/v1/pods" {
				rows = rows[1:]
			}
			next := ""
			if n, err := strconv.Atoi(query.Get("limit")); err == nil && n > 0 && n < len(rows) && query.Get("continue") == "" {
				rows, next = rows[:n], "more"
			} else if query.Get("continue") != "" {
				rows = rows[1:]
			}
			write(w, table([]string{"Name", "Ready", "Status", "Restarts", "Age"}, rows, next))
		case "/apis/aquasecurity.github.io/v1alpha1/vulnerabilityreports":
			columns := []string{"Name", "Repository", "Scanner", "Critical", "Score", "Fixable", "Note", "Extra"}
			if query.Get("continue") == "" {
				write(w, table(columns, []any{reportRow("replicaset-api-7d9-api", 3, 7.5)}, "page-2"))
				return
			}
			write(w, table(columns, []any{reportRow("replicaset-web-1-web", 0, 0)}, ""))
		case "/apis/example.io/v1/widgets":
			write(w, map[string]any{"apiVersion": "example.io/v1", "kind": "WidgetList",
				"metadata": map[string]any{"resourceVersion": "9"},
				"items":    []any{map[string]any{"metadata": map[string]any{"name": "w", "namespace": "shop"}}}})
		default:
			http.Error(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte(`apiVersion: v1
kind: Config
current-context: prod
clusters:
- name: prod
  cluster: {server: `+server.URL+`}
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
	srv, err := New(Options{Clusters: manager, InCluster: true,
		Access: access.New(access.Options{Reviews: reviews.For, InCluster: true})})
	if err != nil {
		t.Fatal(err)
	}
	return srv.Host()
}

var (
	reportsTable = extension.ListQuery{Columns: true, GVR: schema.GroupVersionResource{
		Group: "aquasecurity.github.io", Version: "v1alpha1", Resource: "vulnerabilityreports"}}
	podsTable = extension.ListQuery{Columns: true, GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}}
)

// Every column, priority 1 included, in the API server's order; every cell with
// its type; every row with its metadata; both pages, the second reached with
// the first's continue token and without the watch cache's version.
func TestHostListColumnsReadsTheTableWithItsMetadataOverEveryPage(t *testing.T) {
	s := &tableServer{}
	page, err := s.host(t).List(asPerson(person("erin@example.com", "oidc:readers")), "prod", reportsTable)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !page.Complete || page.Note != "" || len(page.Items) != 0 {
		t.Errorf("complete %v, note %q, %d items: want a complete page with no objects", page.Complete, page.Note, len(page.Items))
	}
	wantColumns := []string{"Name", "Repository", "Scanner", "Critical", "Score", "Fixable", "Note", "Extra"}
	if !reflect.DeepEqual(page.Columns, wantColumns) {
		t.Errorf("columns %q, want %q", page.Columns, wantColumns)
	}
	if len(page.Rows) != 2 {
		t.Fatalf("%d rows, want the two of both pages", len(page.Rows))
	}
	first := page.Rows[0]
	wantCells := []any{"replicaset-api-7d9-api", "ghcr.io/x/api", "Trivy", int64(3), 7.5, true, nil, `{"a":1}`}
	if !reflect.DeepEqual(first.Cells, wantCells) {
		t.Errorf("cells %#v, want %#v", first.Cells, wantCells)
	}
	if got := page.Rows[1].Cells[3:5]; !reflect.DeepEqual(got, []any{int64(0), int64(0)}) {
		t.Errorf("second row's counts %#v, want two int64 zeros", got)
	}
	if first.Namespace != "shop" || first.Name != "replicaset-api-7d9-api" {
		t.Errorf("row %s/%s, want shop/replicaset-api-7d9-api", first.Namespace, first.Name)
	}
	if first.Labels["trivy-operator.container.name"] != "api" ||
		first.Annotations["trivy-operator.aquasecurity.github.io/report-ttl"] != "24h" {
		t.Errorf("labels %v, annotations %v: the object's metadata is missing", first.Labels, first.Annotations)
	}
	if len(first.Owners) != 1 || first.Owners[0].Kind != "ReplicaSet" || first.Owners[0].Name != "api-7d9" ||
		first.Owners[0].Controller == nil || !*first.Owners[0].Controller {
		t.Errorf("owners %+v, want the controlling ReplicaSet api-7d9", first.Owners)
	}
	if want := time.Date(2026, 11, 2, 8, 30, 0, 0, time.UTC); !first.Created.Equal(want) {
		t.Errorf("created %v, want %v", first.Created, want)
	}

	requests := s.seen()
	if len(requests) != 2 {
		t.Fatalf("%d requests, want two pages", len(requests))
	}
	for i, r := range requests {
		if accept := r.Header.Get("Accept"); accept != asTable {
			t.Errorf("request %d asked Accept %q, want %q", i, accept, asTable)
		}
		if got := r.URL.Query().Get("includeObject"); got != "Metadata" {
			t.Errorf("request %d asked includeObject=%q, want Metadata", i, got)
		}
	}
	if q := requests[0].URL.Query(); q.Get("resourceVersion") != "0" || q.Get("continue") != "" {
		t.Errorf("first page asked %v, want the watch cache's version and no token", q)
	}
	if q := requests[1].URL.Query(); q.Get("continue") != "page-2" || q.Get("resourceVersion") != "" {
		t.Errorf("second page asked %v, want the first page's token and no version", q)
	}
}

// A limit is one request for that many rows, not from the watch cache, and the
// page says it was cut short.
func TestHostListColumnsWithALimitAsksForThatManyRows(t *testing.T) {
	s := &tableServer{}
	q := podsTable
	q.Limit = 1
	page, err := s.host(t).List(asPerson(person("erin@example.com", "oidc:readers")), "prod", q)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0].Name != "bill-0" || page.Complete || !strings.Contains(page.Note, "first 1") {
		t.Errorf("rows %+v, complete %v, note %q: want bill-0 alone, said to be cut short", page.Rows, page.Complete, page.Note)
	}
	requests := s.seen()
	if len(requests) != 1 {
		t.Fatalf("%d requests, want one", len(requests))
	}
	if q := requests[0].URL.Query(); q.Get("limit") != "1" || q.Get("resourceVersion") != "" {
		t.Errorf("asked %v, want limit 1 and no watch-cache version", q)
	}
}

// A person who may list pods only in shop gets shop's rows, told the list is
// narrowed; with a limit, the rows are cut after narrowing, so billing's pod
// does not take the place of shop's.
func TestHostListColumnsIsNarrowedToTheNamespacesThePersonMayList(t *testing.T) {
	s := &tableServer{}
	h := s.host(t)
	page, err := h.List(asPerson(shopViewer()), "prod", podsTable)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Rows) != 2 || page.Rows[0].Namespace != "shop" || page.Rows[1].Namespace != "shop" {
		t.Errorf("rows %+v, want shop's two pods", page.Rows)
	}
	if page.Complete || !strings.Contains(page.Note, "namespaces where you may") {
		t.Errorf("complete %v, note %q: a narrowed list must say so", page.Complete, page.Note)
	}
	if len(page.Columns) != 5 {
		t.Errorf("columns %q, want the five of the table", page.Columns)
	}

	q := podsTable
	q.Limit = 1
	page, err = h.List(asPerson(shopViewer()), "prod", q)
	if err != nil {
		t.Fatalf("List with a limit: %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0].Namespace != "shop" || page.Complete {
		t.Errorf("rows %+v, complete %v: want one of shop's pods, not complete", page.Rows, page.Complete)
	}
}

// A person refused the list in the namespace asked is refused, classifiably,
// and the cluster is not read.
func TestHostListColumnsRefusalIsErrDenied(t *testing.T) {
	s := &tableServer{}
	q := podsTable
	q.Namespace = "shop"
	page, err := s.host(t).List(asPerson(nobody()), "prod", q)
	if !errors.Is(err, extension.ErrDenied) {
		t.Fatalf("err %v, page %+v: want ErrDenied", err, page)
	}
	if n := len(s.seen()); n != 0 {
		t.Errorf("%d requests reached the cluster for a refused list", n)
	}
}

// An API server that answers objects to a table request is an error, never
// rows made from the objects, nor an empty page.
func TestHostListColumnsRefusesAnAnswerThatIsNotATable(t *testing.T) {
	s := &tableServer{}
	q := extension.ListQuery{Columns: true, GVR: schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}}
	page, err := s.host(t).List(asPerson(person("erin@example.com", "oidc:readers")), "prod", q)
	if err == nil {
		t.Fatalf("no error, page %+v: an answer that is not a table was read as one", page)
	}
	if !strings.Contains(err.Error(), "WidgetList") || errors.Is(err, extension.ErrDenied) {
		t.Errorf("err %v: want a failed read naming what was answered", err)
	}
}

// Secrets are refused before anything is asked of the cluster, table or not.
func TestHostListColumnsRefusesSecretsBeforeAnyRequest(t *testing.T) {
	s := &tableServer{}
	q := extension.ListQuery{Columns: true, GVR: schema.GroupVersionResource{Version: "v1", Resource: "secrets"}}
	_, err := s.host(t).List(asPerson(person("erin@example.com", "oidc:readers")), "prod", q)
	if !errors.Is(err, extension.ErrSecretsNotListed) {
		t.Fatalf("err %v, want ErrSecretsNotListed", err)
	}
	if n := len(s.seen()); n != 0 {
		t.Errorf("%d requests reached the cluster for a list of Secrets", n)
	}
}

func TestHostListColumnsOfAnUnknownClusterIsErrUnknownCluster(t *testing.T) {
	s := &tableServer{}
	_, err := s.host(t).List(asPerson(person("erin@example.com", "oidc:readers")), "staging", reportsTable)
	if !errors.Is(err, extension.ErrUnknownCluster) {
		t.Fatalf("err %v, want ErrUnknownCluster", err)
	}
}

func TestTableCellKeepsEachTypeTheContractNames(t *testing.T) {
	for _, c := range []struct {
		in   any
		want any
	}{
		{nil, nil},
		{"x", "x"},
		{true, true},
		{json.Number("42"), int64(42)},
		{json.Number("-1"), int64(-1)},
		{json.Number("0.25"), 0.25},
		{json.Number("1e3"), 1000.0},
		{[]any{json.Number("1"), "a"}, `[1,"a"]`},
	} {
		if got := tableCell(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("tableCell(%#v) = %#v, want %#v", c.in, got, c.want)
		}
	}
}
