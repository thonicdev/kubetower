package customresources

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"

	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// --- fixtures -----------------------------------------------------------------

var (
	widgetGVR        = schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	clusterWidgetGVR = schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "clusterwidgets"}
	policyGVR        = schema.GroupVersionResource{Group: "kyverno.io", Version: "v1", Resource: "policies"}
	otherPolicyGVR   = schema.GroupVersionResource{Group: "gatekeeper.sh", Version: "v1", Resource: "policies"}
)

// widgetCRD is the shape most CRDs in the wild have: a handful of printer
// columns, one of them wide, one of them the Age column kubectl gives nobody
// for free.
const widgetCRD = `{
  "apiVersion": "apiextensions.k8s.io/v1",
  "kind": "CustomResourceDefinition",
  "metadata": {"name": "widgets.example.io"},
  "spec": {
    "group": "example.io",
    "scope": "Namespaced",
    "names": {"kind": "Widget", "listKind": "WidgetList", "plural": "widgets", "singular": "widget", "shortNames": ["wg"]},
    "versions": [
      {
        "name": "v1beta1",
        "served": true,
        "storage": false,
        "additionalPrinterColumns": [
          {"name": "Old", "type": "string", "jsonPath": ".spec.old"}
        ]
      },
      {
        "name": "v1",
        "served": true,
        "storage": true,
        "additionalPrinterColumns": [
          {"name": "Ready", "type": "string", "jsonPath": ".status.ready"},
          {"name": "Size", "type": "integer", "jsonPath": ".spec.size"},
          {"name": "Secret", "type": "string", "jsonPath": ".spec.credentials"},
          {"name": "Phase", "type": "string", "jsonPath": ".status.phase"},
          {"name": "Internals", "type": "string", "jsonPath": ".spec.internals", "priority": 1},
          {"name": "Age", "type": "date", "jsonPath": ".metadata.creationTimestamp"}
        ]
      }
    ]
  }
}`

const clusterWidgetCRD = `{
  "apiVersion": "apiextensions.k8s.io/v1",
  "kind": "CustomResourceDefinition",
  "metadata": {"name": "clusterwidgets.example.io"},
  "spec": {
    "group": "example.io",
    "scope": "Cluster",
    "names": {"kind": "ClusterWidget", "listKind": "ClusterWidgetList", "plural": "clusterwidgets", "singular": "clusterwidget"},
    "versions": [
      {
        "name": "v1",
        "served": true,
        "storage": true,
        "additionalPrinterColumns": [
          {"name": "Ready", "type": "string", "jsonPath": ".status.ready"}
        ]
      }
    ]
  }
}`

// retiredCRD is installed but serves nothing: the operator was upgraded and the
// old version turned off without the definition being removed.
const retiredCRD = `{
  "apiVersion": "apiextensions.k8s.io/v1",
  "kind": "CustomResourceDefinition",
  "metadata": {"name": "gadgets.example.io"},
  "spec": {
    "group": "example.io",
    "scope": "Namespaced",
    "names": {"kind": "Gadget", "listKind": "GadgetList", "plural": "gadgets", "singular": "gadget"},
    "versions": [{"name": "v1alpha1", "served": false, "storage": true}]
  }
}`

func policyCRD(group string) string {
	return `{
  "apiVersion": "apiextensions.k8s.io/v1",
  "kind": "CustomResourceDefinition",
  "metadata": {"name": "policies.` + group + `"},
  "spec": {
    "group": "` + group + `",
    "scope": "Namespaced",
    "names": {"kind": "Policy", "listKind": "PolicyList", "plural": "policies", "singular": "policy"},
    "versions": [{"name": "v1", "served": true, "storage": true}]
  }
}`
}

func object(t *testing.T, body string) *unstructured.Unstructured {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("the fixture is not JSON: %v", err)
	}
	return &unstructured.Unstructured{Object: m}
}

// widget builds one instance. created is written as the API server writes it.
func widget(t *testing.T, name, namespace, body string) *unstructured.Unstructured {
	t.Helper()
	o := object(t, body)
	o.SetAPIVersion("example.io/v1")
	o.SetKind("Widget")
	o.SetName(name)
	if namespace != "" {
		o.SetNamespace(namespace)
	}
	return o
}

func newRegistry(t *testing.T, objects ...runtime.Object) (*Registry, *cluster.Client, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	listKinds := map[schema.GroupVersionResource]string{
		crdGVR:           "CustomResourceDefinitionList",
		widgetGVR:        "WidgetList",
		clusterWidgetGVR: "ClusterWidgetList",
		policyGVR:        "PolicyList",
		otherPolicyGVR:   "PolicyList",
	}
	fake := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objects...)

	registry := NewRegistry()
	return registry, &cluster.Client{Context: "test", Config: &rest.Config{}, Dynamic: fake}, fake
}

// columnTitles reads the titles off a rendered table.
func columnTitles(t *testing.T, kind string, registry *Registry, client *cluster.Client) []string {
	t.Helper()
	table, err := registry.List(t.Context(), accesstest.Allow(), client, kind, "", nil, "", "", nil)
	if err != nil {
		t.Fatalf("listing %s: %v", kind, err)
	}
	out := make([]string, 0, len(table.Columns))
	for _, c := range table.Columns {
		out = append(out, c.Title)
	}
	return out
}

// --- printer columns ----------------------------------------------------------

func TestPrinterColumnsOfTheChosenVersionAreWhatTheTableShows(t *testing.T) {
	registry, client, _ := newRegistry(t,
		object(t, widgetCRD),
		widget(t, "one", "apps", `{"spec": {"size": 3}, "status": {"ready": "True", "phase": "Bound"}}`),
	)

	table, err := registry.List(t.Context(), accesstest.Allow(), client, "widgets.example.io", "", nil, "", "", nil)
	if err != nil {
		t.Fatalf("listing widgets: %v", err)
	}

	want := []string{"Name", "Namespace", "Ready", "Size", "Secret", "Phase", "Age"}
	got := make([]string, 0, len(table.Columns))
	for _, c := range table.Columns {
		got = append(got, c.Title)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the CRD's own columns are not what the table shows:\n got %v\nwant %v", got, want)
	}
	// "Old" belongs to v1beta1; rendering it would mean rendering columns of a
	// version the list did not read the objects at.
	for _, c := range got {
		if c == "Old" {
			t.Errorf("a column of a version the list did not read reached the table: %v", got)
		}
	}

	if len(table.Rows) != 1 {
		t.Fatalf("want one row, got %d", len(table.Rows))
	}
	row := table.Rows[0]
	if len(row.Cells) != len(table.Columns) {
		t.Fatalf("cells and columns drifted apart (%d cells, %d columns): every value is one column off and the table still renders",
			len(row.Cells), len(table.Columns))
	}
	if row.Cells[2].Text != "True" {
		t.Errorf("Ready came out %q, want the value the JSONPath resolves to", row.Cells[2].Text)
	}
	if row.Cells[3].Text != "3" {
		t.Errorf("an integer column came out %q, want \"3\" — a JSON number must not print as 3e+00", row.Cells[3].Text)
	}
}

func TestWideColumnsStayOutOfTheDefaultTable(t *testing.T) {
	registry, client, _ := newRegistry(t,
		object(t, widgetCRD),
		widget(t, "one", "apps", `{"spec": {"internals": "noisy"}}`),
	)

	for _, title := range columnTitles(t, "widgets", registry, client) {
		if title == "Internals" {
			t.Fatalf("a priority column reached the default view; it is what kubectl only prints under -o wide")
		}
	}
}

func TestAnAgeColumnDeclaredByTheCRDIsNotPrintedTwice(t *testing.T) {
	registry, client, _ := newRegistry(t, object(t, widgetCRD),
		widget(t, "one", "apps", `{}`))

	count := 0
	for _, title := range columnTitles(t, "widgets", registry, client) {
		if title == "Age" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the table shows %d Age columns: the CRD declares one on .metadata.creationTimestamp and this table always adds its own", count)
	}
}

func TestAnUnresolvedJSONPathIsAnEmDashAndNotAnEmptyCell(t *testing.T) {
	registry, client, _ := newRegistry(t,
		object(t, widgetCRD),
		// No status at all: the ordinary state of an object a controller has not
		// reached yet.
		widget(t, "one", "apps", `{"spec": {"size": 1}}`),
	)

	table, err := registry.List(t.Context(), accesstest.Allow(), client, "widgets", "", nil, "", "", nil)
	if err != nil {
		t.Fatalf("listing widgets: %v", err)
	}
	ready := table.Rows[0].Cells[2]
	if ready.Text != missing {
		t.Fatalf("an unresolved column came out %q, want %q — a blank cell reads as a rendering fault", ready.Text, missing)
	}
}

func TestAnUnparseableJSONPathDoesNotTakeDownTheTable(t *testing.T) {
	broken := strings.Replace(widgetCRD, `".status.ready"`, `".status[ready"`, 1)
	registry, client, _ := newRegistry(t,
		object(t, broken),
		widget(t, "one", "apps", `{"spec": {"size": 2}}`),
	)

	table, err := registry.List(t.Context(), accesstest.Allow(), client, "widgets", "", nil, "", "", nil)
	if err != nil {
		t.Fatalf("one bad expression out of six took down the whole table: %v", err)
	}
	if table.Rows[0].Cells[2].Text != missing {
		t.Errorf("a column whose expression does not parse came out %q, want %q", table.Rows[0].Cells[2].Text, missing)
	}
	if table.Rows[0].Cells[3].Text != "2" {
		t.Errorf("the columns beside the broken one stopped working: %q", table.Rows[0].Cells[3].Text)
	}
}

func TestANonScalarValueIsDescribedAndNotPrinted(t *testing.T) {
	registry, client, _ := newRegistry(t,
		object(t, widgetCRD),
		// A printer column pointing at a whole object. It happens, and what is
		// under it is whatever the CRD author put there — here, a token.
		widget(t, "one", "apps", `{"spec": {"credentials": {"user": "admin", "token": "s3cr3t-value"}}}`),
	)

	table, err := registry.List(t.Context(), accesstest.Allow(), client, "widgets", "", nil, "", "", nil)
	if err != nil {
		t.Fatalf("listing widgets: %v", err)
	}
	secret := table.Rows[0].Cells[4].Text
	if strings.Contains(secret, "s3cr3t") || strings.Contains(secret, "admin") {
		t.Fatalf("the contents of a non-scalar field reached the table: %q", secret)
	}
	if secret != "{object}" {
		t.Fatalf("a non-scalar value came out %q, want its shape", secret)
	}
}

func TestADateColumnRendersAsAnAge(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	definition := Definition{
		Group: "example.io", Version: "v1", Kind: "Widget", Plural: "widgets", Namespaced: true, Served: true,
		Columns: []PrinterColumn{{Name: "Expires", Type: "date", JSONPath: ".status.expires"}},
	}
	item := object(t, `{"metadata": {"name": "one", "namespace": "apps"}, "status": {"expires": "2026-08-14T12:00:00Z"}}`)

	table := definition.render([]unstructured.Unstructured{*item}, "", nil, "", nil, now)
	if got := table.Rows[0].Cells[2].Text; got != "3d" {
		t.Fatalf("a date column came out %q, want \"3d\": what an operator reads out of a timestamp is how long ago", got)
	}
}

func TestSeveralResultsAreJoinedAndCapped(t *testing.T) {
	now := time.Now()
	definition := Definition{
		Group: "example.io", Version: "v1", Kind: "Widget", Plural: "widgets", Namespaced: false, Served: true,
		Columns: []PrinterColumn{{Name: "Hosts", Type: "string", JSONPath: ".spec.hosts[*]"}},
	}
	item := object(t, `{"metadata": {"name": "one"}, "spec": {"hosts": ["a", "b", "c", "d", "e", "f"]}}`)

	table := definition.render([]unstructured.Unstructured{*item}, "", nil, "", nil, now)
	got := table.Rows[0].Cells[1].Text
	if !strings.HasPrefix(got, "a, b, c, d") || !strings.Contains(got, "+2 more") {
		t.Fatalf("a wildcard column came out %q, want the first few and a count of the rest", got)
	}
}

// --- scope --------------------------------------------------------------------

func TestAClusterScopedKindNeverAcceptsANamespace(t *testing.T) {
	registry, client, fake := newRegistry(t,
		object(t, clusterWidgetCRD),
		object(t, `{"apiVersion": "example.io/v1", "kind": "ClusterWidget", "metadata": {"name": "one"}, "status": {"ready": "True"}}`),
	)

	// The namespace the UI was carrying when the operator changed kind. It is not
	// an error, and it must not reach the API, the table or anything reading them.
	table, err := registry.List(t.Context(), accesstest.Allow(), client, "clusterwidgets", "kube-system", nil, "", "", nil)
	if err != nil {
		t.Fatalf("listing clusterwidgets: %v", err)
	}
	if table.Namespace != "" || table.Namespaced {
		t.Errorf("the table claims a namespace for a cluster-scoped kind: %+v", table)
	}
	for _, c := range table.Columns {
		if c.Title == "Namespace" {
			t.Errorf("a cluster-scoped table grew a Namespace column")
		}
	}
	if len(table.Rows) != 1 || table.Rows[0].Namespace != "" {
		t.Errorf("a cluster-scoped row carries a namespace: %+v", table.Rows)
	}

	for _, action := range fake.Actions() {
		if action.GetResource() != clusterWidgetGVR {
			continue
		}
		if ns := action.GetNamespace(); ns != "" {
			t.Fatalf("the request reached the API as namespace %q: that is how a node's name ends up read as a namespace", ns)
		}
	}
}

func TestANamespacedKindListsInTheNamespaceItWasGiven(t *testing.T) {
	registry, client, fake := newRegistry(t,
		object(t, widgetCRD),
		widget(t, "one", "apps", `{}`),
		widget(t, "two", "other", `{}`),
	)

	table, err := registry.List(t.Context(), accesstest.Allow(), client, "widgets", "apps", nil, "", "", nil)
	if err != nil {
		t.Fatalf("listing widgets: %v", err)
	}
	if len(table.Rows) != 1 || table.Rows[0].Name != "one" {
		t.Fatalf("the namespace was not applied: %+v", table.Rows)
	}

	var seen string
	for _, action := range fake.Actions() {
		if action.GetResource() == widgetGVR {
			seen = action.GetNamespace()
		}
	}
	if seen != "apps" {
		t.Fatalf("the list reached the API with namespace %q, want \"apps\"", seen)
	}
}

func TestGetOnANamespacedKindWithoutANamespaceIsRefused(t *testing.T) {
	registry, client, _ := newRegistry(t, object(t, widgetCRD), widget(t, "one", "apps", `{}`))

	_, err := registry.Get(t.Context(), accesstest.Allow(), client, "widgets", "", "one")
	if err == nil {
		t.Fatal("a namespaced Get without a namespace was accepted; it can only answer for the wrong object or for none")
	}
	if !strings.Contains(err.Error(), "widgets.example.io") {
		t.Errorf("the error does not name the kind: %v", err)
	}
}

func TestGetOnAClusterScopedKindReportsNoNamespace(t *testing.T) {
	registry, client, _ := newRegistry(t,
		object(t, clusterWidgetCRD),
		object(t, `{"apiVersion": "example.io/v1", "kind": "ClusterWidget", "metadata": {"name": "one"}}`),
	)

	instance, err := registry.Get(t.Context(), accesstest.Allow(), client, "clusterwidgets", "kube-system", "one")
	if err != nil {
		t.Fatalf("fetching a cluster-scoped instance: %v", err)
	}
	if instance.Namespace != "" {
		t.Fatalf("the instance reports namespace %q; a cluster-scoped object has none, and repeating the request back as fact states a namespace that does not exist", instance.Namespace)
	}
}

// --- unknown kinds ------------------------------------------------------------

func TestAnUnknownKindErrorsWithItsName(t *testing.T) {
	registry, client, _ := newRegistry(t, object(t, widgetCRD))

	_, err := registry.List(t.Context(), accesstest.Allow(), client, "sprockets", "", nil, "", "", nil)
	if err == nil {
		t.Fatal("an unknown kind produced a table; an empty table for an unknown kind is how a console answers \"no pods\" for a cluster running 429")
	}
	if !errors.Is(err, ErrUnknownKind) {
		t.Errorf("want ErrUnknownKind so the caller can answer 404, got %v", err)
	}
	if !strings.Contains(err.Error(), "sprockets") {
		t.Errorf("the error does not name what was asked for: %v", err)
	}
}

func TestAKindNoVersionServesErrorsWithItsName(t *testing.T) {
	registry, client, _ := newRegistry(t, object(t, retiredCRD))

	_, err := registry.List(t.Context(), accesstest.Allow(), client, "gadgets", "", nil, "", "", nil)
	if err == nil {
		t.Fatal("an unserved kind produced a table rather than saying the cluster is not offering it")
	}
	if !errors.Is(err, ErrNotServed) {
		t.Errorf("want ErrNotServed — the kind is real, the cluster simply does not serve it — got %v", err)
	}
	if !strings.Contains(err.Error(), "gadgets.example.io") {
		t.Errorf("the error does not name the kind: %v", err)
	}
}

func TestAnUnservedKindStaysOutOfTheNavigation(t *testing.T) {
	registry, client, _ := newRegistry(t, object(t, widgetCRD), object(t, retiredCRD))

	definitions, err := registry.Definitions(t.Context(), client)
	if err != nil {
		t.Fatalf("listing definitions: %v", err)
	}
	for _, d := range definitions {
		if d.Plural == "gadgets" {
			t.Fatal("an unserved kind is in the menu; every click on it is an error page")
		}
	}
	if len(definitions) != 1 || definitions[0].Kind != "Widget" {
		t.Fatalf("want the one served definition, got %+v", definitions)
	}
}

func TestTwoGroupsDefiningTheSamePluralAreNotGuessedBetween(t *testing.T) {
	registry, client, _ := newRegistry(t,
		object(t, policyCRD("kyverno.io")),
		object(t, policyCRD("gatekeeper.sh")),
	)

	_, err := registry.List(t.Context(), accesstest.Allow(), client, "policies", "default", nil, "", "", nil)
	if err == nil {
		t.Fatal("an ambiguous plural was resolved by guessing; the wrong table renders perfectly")
	}
	if !errors.Is(err, ErrAmbiguousKind) {
		t.Errorf("want ErrAmbiguousKind, got %v", err)
	}
	if !strings.Contains(err.Error(), "policies.kyverno.io") || !strings.Contains(err.Error(), "policies.gatekeeper.sh") {
		t.Errorf("the error does not name both candidates: %v", err)
	}

	// The qualified form still works, which is what makes the error actionable.
	if _, err := registry.List(t.Context(), accesstest.Allow(), client, "policies.kyverno.io", "default", nil, "", "", nil); err != nil {
		t.Fatalf("the qualified name was refused too: %v", err)
	}
}

func TestAKindIsFoundBySlugPluralSingularKindAndShortName(t *testing.T) {
	registry, client, _ := newRegistry(t, object(t, widgetCRD))

	for _, name := range []string{"widgets.example.io", "widgets", "widget", "Widget", "wg", "  WIDGETS  "} {
		if _, err := registry.Definition(t.Context(), client, name); err != nil {
			t.Errorf("%q was not resolved: %v", name, err)
		}
	}
}

// --- the catalog ------------------------------------------------------------

func TestTheCRDListIsCachedForTheTTL(t *testing.T) {
	registry, client, fake := newRegistry(t, object(t, widgetCRD), widget(t, "one", "apps", `{}`))

	for i := 0; i < 5; i++ {
		if _, err := registry.List(t.Context(), accesstest.Allow(), client, "widgets", "apps", nil, "", "", nil); err != nil {
			t.Fatalf("listing widgets: %v", err)
		}
	}

	lists := 0
	for _, action := range fake.Actions() {
		if action.GetResource() == crdGVR && action.GetVerb() == "list" {
			lists++
		}
	}
	if lists != 1 {
		t.Fatalf("the CRD list was fetched %d times for five pages; it is on the path of every page and that is what the TTL is for", lists)
	}
}

func TestAnExpiredCatalogueIsFetchedAgain(t *testing.T) {
	registry, client, fake := newRegistry(t, object(t, widgetCRD))
	registry.ttl = 0

	for i := 0; i < 2; i++ {
		if _, err := registry.Definitions(t.Context(), client); err != nil {
			t.Fatalf("listing definitions: %v", err)
		}
	}

	lists := 0
	for _, action := range fake.Actions() {
		if action.GetResource() == crdGVR && action.GetVerb() == "list" {
			lists++
		}
	}
	if lists != 2 {
		t.Fatalf("the catalog was fetched %d times with a zero TTL, want 2: a CRD installed a minute ago must appear", lists)
	}
}

func TestAClusterThatStopsAnsweringIsNotServedFromTheCache(t *testing.T) {
	registry, client, fake := newRegistry(t, object(t, widgetCRD))

	if _, err := registry.Definitions(t.Context(), client); err != nil {
		t.Fatalf("the first fetch failed: %v", err)
	}
	registry.ttl = 0
	fake.PrependReactor("list", "customresourcedefinitions",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("the server could not find the requested resource")
		})

	_, err := registry.Definitions(t.Context(), client)
	if err == nil {
		t.Fatal("a failed refresh was answered from the cache; that is precisely how a console reports \"no pods\" for a cluster running 429")
	}
	if !strings.Contains(err.Error(), "test") {
		t.Errorf("the error does not name the cluster: %v", err)
	}
}

func TestNoClientIsAnErrorAndNotAnEmptyCatalogue(t *testing.T) {
	registry := NewRegistry()

	if _, err := registry.Definitions(t.Context(), nil); err == nil {
		t.Fatal("a nil client produced a catalog")
	}
	if _, err := registry.Definitions(t.Context(), &cluster.Client{Context: "test"}); err == nil {
		t.Fatal("a client with no rest.Config produced a catalog")
	}
}

// --- the manifest -------------------------------------------------------------

func TestTheManifestDropsManagedFieldsAndKeepsTheObject(t *testing.T) {
	instance := widget(t, "one", "apps", `{"spec": {"size": 3, "credentials": {"token": "s3cr3t"}}}`)
	instance.Object["metadata"].(map[string]any)["managedFields"] = []any{
		map[string]any{"manager": "kubectl", "operation": "Apply"},
	}
	registry, client, _ := newRegistry(t, object(t, widgetCRD), instance)

	got, err := registry.Get(t.Context(), accesstest.Allow(), client, "widgets", "apps", "one")
	if err != nil {
		t.Fatalf("fetching the instance: %v", err)
	}
	if strings.Contains(got.Manifest, "managedFields") {
		t.Errorf("managedFields reached the detail view; it is a different panel and it dwarfs the object:\n%s", got.Manifest)
	}
	for _, want := range []string{"apiVersion: example.io/v1", "kind: Widget", "name: one", "size: 3"} {
		if !strings.Contains(got.Manifest, want) {
			t.Errorf("the manifest lost %q:\n%s", want, got.Manifest)
		}
	}
	// Nothing else is removed: the detail view is where an operator goes to read
	// the object, and blanking a field on a guess produces a manifest that is
	// quietly wrong.
	if !strings.Contains(got.Manifest, "s3cr3t") {
		t.Errorf("a field was redacted on a guess; only managedFields is dropped:\n%s", got.Manifest)
	}
}

func TestGetNamesTheKindWhenTheObjectIsNotThere(t *testing.T) {
	registry, client, _ := newRegistry(t, object(t, widgetCRD))

	_, err := registry.Get(t.Context(), accesstest.Allow(), client, "widgets", "apps", "absent")
	if err == nil {
		t.Fatal("a missing object was fetched successfully")
	}
	if !strings.Contains(err.Error(), "widgets.example.io") || !strings.Contains(err.Error(), "absent") {
		t.Errorf("the error names neither the kind nor the object: %v", err)
	}
}

// --- filtering and shape ------------------------------------------------------

// A CRD's filter is the built-in one, and the namespace is not in it.
//
// The same rule as `internal/resources`: a search for `api` inside one
// namespace must not return rows from another whose *namespace* contains
// `api`, because the operator has just narrowed with a control of their own.
// One box must not behave two ways depending on whether the kind is built in
// or a CRD.
func TestACustomResourceFilterDoesNotMatchOnTheNamespace(t *testing.T) {
	registry, client, _ := newRegistry(t,
		object(t, widgetCRD),
		widget(t, "alpha", "apps", `{}`),
		widget(t, "beta", "other", `{}`),
	)

	table, err := registry.List(t.Context(), accesstest.Allow(), client, "widgets", "", nil, "", "OTH", nil)
	if err != nil {
		t.Fatalf("listing widgets: %v", err)
	}
	if len(table.Rows) != 0 {
		t.Fatalf("`OTH` matched %d row(s) through the namespace column. The namespace "+
			"selector is what narrows by namespace; the box widening it back is "+
			"invisible to whoever just narrowed: %+v", len(table.Rows), table.Rows)
	}

	// And the name still matches, so the filter is not simply off for custom
	// resources.
	table, err = registry.List(t.Context(), accesstest.Allow(), client, "widgets", "", nil, "", "BET", nil)
	if err != nil {
		t.Fatalf("listing widgets: %v", err)
	}
	if len(table.Rows) != 1 || table.Rows[0].Name != "beta" {
		t.Fatalf("the name no longer matches: %+v", table.Rows)
	}
}

// The filter matches labels and every visible cell, not only the name.
// A CRD's rows are its own printer columns, so this is the case that proves the
// rule is about *cells* rather than about a list of known fields.
func TestACustomResourceIsFoundByWhatItsColumnsShowAndByItsLabels(t *testing.T) {
	alpha := widget(t, "alpha", "apps", `{"status": {"phase": "Degraded"}}`)
	alpha.SetLabels(map[string]string{"app": "payments"})
	beta := widget(t, "beta", "apps", `{"status": {"phase": "Healthy"}}`)
	beta.SetLabels(map[string]string{"app": "billing"})

	registry, client, _ := newRegistry(t, object(t, widgetCRD), alpha, beta)

	find := func(query string) []string {
		t.Helper()
		table, err := registry.List(t.Context(), accesstest.Allow(), client, "widgets", "", nil, "", query, nil)
		if err != nil {
			t.Fatalf("listing widgets for %q: %v", query, err)
		}
		names := make([]string, 0, len(table.Rows))
		for _, row := range table.Rows {
			names = append(names, row.Name)
		}
		return names
	}

	cases := []struct {
		query string
		want  string
		why   string
	}{
		{"degrad", "alpha", "a printer column's own value is on screen, so typing it must work"},
		{"payments", "alpha", "a label is what an operator selects on, and is drawn nowhere"},
		{"app=billing", "beta", "the pair as they would write it in a selector"},
		{"BETA", "beta", "the name, case-insensitively, as before"},
	}
	for _, c := range cases {
		got := find(c.query)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%q matched %v, want [%s] — %s", c.query, got, c.want, c.why)
		}
	}
}

func TestAnEmptyClusterGivesAnEmptyTableAndNotAnError(t *testing.T) {
	registry, client, _ := newRegistry(t, object(t, widgetCRD))

	table, err := registry.List(t.Context(), accesstest.Allow(), client, "widgets", "", nil, "", "", nil)
	if err != nil {
		t.Fatalf("a served kind with no instances errored: %v", err)
	}
	if len(table.Rows) != 0 {
		t.Fatalf("want no rows, got %d", len(table.Rows))
	}
	if len(table.Columns) == 0 {
		t.Fatal("an empty table still has to carry its columns, or the page renders without a header")
	}
}

func TestEveryRowHasOneCellPerColumn(t *testing.T) {
	registry, client, _ := newRegistry(t,
		object(t, widgetCRD),
		widget(t, "one", "apps", `{"status": {"ready": "True"}}`),
		widget(t, "two", "apps", `{}`),
		object(t, clusterWidgetCRD),
		object(t, `{"apiVersion": "example.io/v1", "kind": "ClusterWidget", "metadata": {"name": "three"}}`),
	)

	for _, kind := range []string{"widgets", "clusterwidgets"} {
		table, err := registry.List(t.Context(), accesstest.Allow(), client, kind, "", nil, "", "", nil)
		if err != nil {
			t.Fatalf("listing %s: %v", kind, err)
		}
		for _, row := range table.Rows {
			if len(row.Cells) != len(table.Columns) {
				t.Fatalf("%s/%s has %d cells for %d columns: every value shifts one column and the table still renders perfectly",
					kind, row.Name, len(row.Cells), len(table.Columns))
			}
			for i, cell := range row.Cells {
				if cell.Text == "" {
					t.Errorf("%s/%s left column %q blank; a missing value is an em dash", kind, row.Name, table.Columns[i].Title)
				}
			}
		}
	}
}

// --- version choice -----------------------------------------------------------

// The highest served version in Kube-aware order, which is discovery's preferred
// version and so the drawer's. Storage plays no part:
// version_test.go asks the drawer's own mapper the same question.
func TestTheHighestServedVersionIsPreferredAndAnUnservedOneIsNot(t *testing.T) {
	cases := []struct {
		name     string
		versions []version
		want     string
		served   bool
	}{
		{"declared lowest first", []version{{name: "v1beta1", served: true}, {name: "v1", served: true}}, "v1", true},
		{"declared highest first", []version{{name: "v1", served: true}, {name: "v1beta1", served: true}}, "v1", true},
		{"the highest is not served", []version{{name: "v1beta1", served: true}, {name: "v1"}}, "v1beta1", true},
		{"none served", []version{{name: "v1alpha1"}}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chosen, served := pickVersion(c.versions)
			if served != c.served {
				t.Fatalf("served = %v, want %v", served, c.served)
			}
			if !served {
				return
			}
			if chosen.name != c.want {
				t.Fatalf("chose %q, want %q: the highest served version is the one discovery prefers and the drawer opens", chosen.name, c.want)
			}
		})
	}
}

func TestAMalformedCRDIsSkippedRatherThanBreakingTheCatalogue(t *testing.T) {
	registry, client, _ := newRegistry(t,
		object(t, `{"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition", "metadata": {"name": "broken"}, "spec": {"scope": "Namespaced"}}`),
		object(t, widgetCRD),
	)

	definitions, err := registry.Definitions(t.Context(), client)
	if err != nil {
		t.Fatalf("one unaddressable CRD took down the catalog: %v", err)
	}
	if len(definitions) != 1 || definitions[0].Kind != "Widget" {
		t.Fatalf("want the one addressable definition, got %+v", definitions)
	}
}

// The reason the renewal exists at all: the navigation asks what a cluster
// serves before it can draw itself, so a catalog fetch lands on a page load.
// Measured at 740 ms on a real EKS cluster, arriving every time the window
// closed — fast, fast, freeze, fast. Renewing *before* the window closes means
// the synchronous path never finds an expired entry while anybody is clicking.
func TestAnAgeingCatalogueIsRenewedBehindTheCallerRatherThanInFrontOfIt(t *testing.T) {
	registry, client, fake := newRegistry(t, object(t, widgetCRD))

	if _, err := registry.Definitions(t.Context(), client); err != nil {
		t.Fatalf("the first fetch failed: %v", err)
	}

	// The list is held open until this test lets it finish, which is what makes
	// the assertion below a statement about blocking rather than about scheduling.
	//
	// Asserting `listed.Load() == 0` immediately after the call would measure
	// "the background goroutine has not been scheduled yet", which is not the
	// property — the race detector can schedule that goroutine first, and fail
	// the test with the renewal working perfectly.
	release := make(chan struct{})
	var listed atomic.Int32
	fake.PrependReactor("list", "customresourcedefinitions",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			listed.Add(1)
			<-release
			return false, nil, nil // fall through to the tracker
		})

	// Old enough to renew, not old enough to expire.
	registry.renewAfter = 0
	registry.ttl = time.Hour

	done, err := registry.Definitions(t.Context(), client)
	if err != nil {
		t.Fatalf("a valid catalog was not served: %v", err)
	}
	if done == nil {
		t.Fatal("nothing was returned")
	}
	// The point, provable here: the list is still in flight — nothing can
	// complete it but this test — and the caller has already been answered from
	// the held catalog. So it did not wait on the API.
	if len(done) == 0 {
		t.Error("the caller was answered with an empty catalog while a renewal was in flight")
	}

	// It did start one, behind the caller.
	waitFor(t, func() bool { return listed.Load() >= 1 }, "the background renewal never ran")

	// And it does not start one per render: a screen is a dozen renders a second.
	//
	// The renders happen **while that renewal is still in flight**, which is the
	// property. Releasing the list first would leave nothing in flight to
	// suppress anything: with renewAfter at zero every render landing after a
	// renewal finished would start the next, and the count would depend on the
	// runner's speed.
	//
	// **The count is read after the release, not during it**, and that is not
	// a detail. The fake client holds its own lock while a reaction runs, so while
	// the first list is blocked every other list call queues *before* reaching the
	// counter - a count taken now reads zero with or without the guard.
	// Released, any queued call runs and counts.
	// With the guard nothing was queued, so the answer is zero however long this
	// waits; the wait only has to be long enough for a broken guard to show.
	before := listed.Load()
	for range 20 {
		if _, err := registry.Definitions(t.Context(), client); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	time.Sleep(200 * time.Millisecond)
	if grew := listed.Load() - before; grew != 0 {
		t.Errorf("twenty renders while a renewal was in flight started %d more; the one in flight should suppress them all", grew)
	}
}

// waitFor polls a condition rather than sleeping a guessed interval, so the test
// is neither flaky on a slow machine nor slow on a fast one.
func waitFor(t *testing.T, ok func() bool, complaint string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(complaint)
}

// The background renewal lets go of the slot's lock for the length of a
// network round trip — deliberately, so readers are not made to wait on the
// refresh they were being spared — so it must ask, before writing what it
// found, whether anything newer has landed meanwhile.
//
// Driven here through the case that matters most: the kubeconfig is rebuilt while
// a renewal is in flight, so `dynamicLocked` clears the catalog on purpose
// ("the CRDs it saw belong to the cluster the old config pointed at") and the
// next read learns what this context now points at. A context name is reused —
// `minikube`, `default`, `kubernetes-admin@kubernetes` is the name every kubeadm
// cluster is born with — so the renewal landing afterwards puts one cluster's
// list of kinds back under another cluster's name, and stamps it fresh, which is
// the part a TTL cannot recover from.
//
// Two dynamic clients rather than one, because a blocked reactor holds the whole
// fake: k8stesting.Fake.Invokes takes its own lock around the reaction chain, so
// a second call on the same fake would deadlock instead of racing.
func TestASlowRenewalDoesNotOverwriteWhatTheClusterSaidAfterIt(t *testing.T) {
	before := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{crdGVR: "CustomResourceDefinitionList"},
		object(t, widgetCRD))
	after := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{crdGVR: "CustomResourceDefinitionList"},
		object(t, clusterWidgetCRD))

	registry := NewRegistry()
	old := &cluster.Client{Context: "test", Config: &rest.Config{Host: "before"}, Dynamic: before}

	if got := kindNames(t, registry, old); len(got) != 1 || got[0] != "widgets.example.io" {
		t.Fatalf("the first fetch saw %v, want the widgets of the cluster this context named", got)
	}

	// The renewal's list is held open, so it is provably still in flight below.
	release := make(chan struct{})
	var lists atomic.Int32
	before.PrependReactor("list", "customresourcedefinitions",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			lists.Add(1)
			<-release
			return false, nil, nil // then answer with the cluster it was pointed at
		})

	// Old enough to renew, not old enough to expire: the renewal starts behind
	// the caller and blocks.
	registry.renewAfter, registry.ttl = 0, time.Hour
	if _, err := registry.Definitions(t.Context(), old); err != nil {
		t.Fatalf("a valid catalog was not served: %v", err)
	}
	waitFor(t, func() bool { return lists.Load() >= 1 }, "the background renewal never ran")

	// And now the kubeconfig is rebuilt: same context name, a different cluster.
	fresh := &cluster.Client{Context: "test", Config: &rest.Config{Host: "after"}, Dynamic: after}
	registry.ttl = time.Nanosecond
	if got := kindNames(t, registry, fresh); len(got) != 1 || got[0] != "clusterwidgets.example.io" {
		t.Fatalf("after the reload the console serves %v, want the new cluster's kinds; the test is not"+
			" exercising the race", got)
	}

	// The renewal lands, carrying the cluster this context used to name.
	close(release)
	settleRefresh(t, registry, "test")

	registry.ttl = time.Hour
	got := kindNames(t, registry, fresh)
	if len(got) != 1 || got[0] != "clusterwidgets.example.io" {
		t.Errorf("the slow renewal put %v back: this context now names a different cluster, the catalog"+
			" was cleared for that reason, and it has been resurrected and stamped fresh", got)
	}
}

// kindNames is what the navigation would draw.
func kindNames(t *testing.T, registry *Registry, client *cluster.Client) []string {
	t.Helper()
	definitions, err := registry.Definitions(t.Context(), client)
	if err != nil {
		t.Fatalf("listing what the cluster serves: %v", err)
	}
	out := make([]string, 0, len(definitions))
	for _, d := range definitions {
		out = append(out, d.Slug())
	}
	return out
}

// settleRefresh waits for the background renewal to have finished writing — or
// to have decided not to. The flag it clears is the only way to tell a refresh
// that declined to store from one that has not landed yet.
func settleRefresh(t *testing.T, registry *Registry, context string) {
	t.Helper()
	s := registry.slot(context)
	waitFor(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.refreshing
	}, "the background renewal never finished")
}

// The same hazard as the test above, without the race: a kubeconfig that
// repoints a context must not be answered from the catalog built for the
// cluster it used to name — not even while that catalog is still young.
//
// The invalidation is in dynamicLocked, and is only reachable because it is
// asked before the freshness check, which would otherwise return before
// anything compared the config it was answering about.
func TestARepointedContextIsNotAnsweredFromThePreviousClustersCatalogue(t *testing.T) {
	before := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{crdGVR: "CustomResourceDefinitionList"},
		object(t, widgetCRD))
	after := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{crdGVR: "CustomResourceDefinitionList"},
		object(t, clusterWidgetCRD))

	registry := NewRegistry()
	// Young by any measure, and deliberately so: this is the state the check
	// could not see past.
	registry.ttl, registry.renewAfter = time.Hour, time.Hour

	old := &cluster.Client{Context: "test", Config: &rest.Config{Host: "before"}, Dynamic: before}
	if got := kindNames(t, registry, old); len(got) != 1 || got[0] != "widgets.example.io" {
		t.Fatalf("the first fetch saw %v, want the widgets of the cluster this context named", got)
	}

	fresh := &cluster.Client{Context: "test", Config: &rest.Config{Host: "after"}, Dynamic: after}
	got := kindNames(t, registry, fresh)
	if len(got) != 1 || got[0] != "clusterwidgets.example.io" {
		t.Errorf("this context now names a different cluster and the console still serves %v: the"+
			" navigation is offering kinds that belong to somebody else's cluster", got)
	}
}

// --- a canceled request must stop waiting -------------------------------------

// blockTheCRDList holds every CRD list open until the returned release function
// is called, and closes `listing` the first time one arrives.
//
// A blocked reactor holds the whole fake — k8stesting.Fake.Invokes takes its own
// lock around the reaction chain — so a test using this must be sure that no
// second call reaches the same fake while one is parked here. That is exactly
// the property under test below: the waiter never reaches the API at all.
func blockTheCRDList(fake *dynamicfake.FakeDynamicClient) (listing <-chan struct{}, listed *atomic.Int32, release func()) {
	arrived := make(chan struct{})
	gate := make(chan struct{})
	var count atomic.Int32
	var once sync.Once
	fake.PrependReactor("list", "customresourcedefinitions",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			count.Add(1)
			once.Do(func() { close(arrived) })
			<-gate
			return false, nil, nil // fall through to the tracker
		})
	return arrived, &count, sync.OnceFunc(func() { close(gate) })
}

// The CRD list is deliberately detached from every caller's context, with a
// 30 s bound of its own, so that an operator clicking quickly still lets a
// catalog land. A second request parked on a sync.Mutex held across that
// list could not leave when its own request went away, and would sit there for
// up to 30 s with nobody left to read the answer — one goroutine per queued
// render, on a page that refreshes every two seconds.
//
// Cancellation, not duration, is what is asserted: the waiter must return
// because its context was canceled, while the fetch it was waiting on is still
// held open by this test and can only be finished by this test.
func TestACancelledRequestStopsWaitingForTheCatalogue(t *testing.T) {
	registry, client, fake := newRegistry(t, object(t, widgetCRD))
	listing, listed, release := blockTheCRDList(fake)
	defer release()

	leader := make(chan error, 1)
	go func() {
		// Not t.Context(): this one is meant to outlive the canceled request.
		_, err := registry.Definitions(context.Background(), client)
		leader <- err
	}()
	<-listing // the first request is on the network and holds the single flight

	ctx, cancel := context.WithCancel(context.Background())
	waiter := make(chan error, 1)
	go func() {
		_, err := registry.Definitions(ctx, client)
		waiter <- err
	}()
	cancel()

	select {
	case err := <-waiter:
		if err == nil {
			t.Fatal("a canceled request was answered; it should have said it stopped looking")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the waiter left with %v, want something wrapping context.Canceled — a caller has "+
				"to be able to tell \"we stopped looking\" from \"this cluster serves no custom "+
				"resources\"", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the waiter did not leave when its request was canceled. Nothing but this test can " +
			"finish the list it is parked behind, and in production that wait is bounded only by the " +
			"fetch's own 30 s — one parked goroutine per queued render")
	}

	// And it never asked the cluster: the single flight is still a single flight.
	if n := listed.Load(); n != 1 {
		t.Errorf("the CRD list was called %d times, want 1 — a waiter that gives up must not leave a "+
			"second identical call behind it", n)
	}

	release()
	if err := <-leader; err != nil {
		t.Fatalf("the request that was not canceled failed: %v", err)
	}
}

// One CRD list per cluster however many renders arrive at once, with waiters
// that can leave.
func TestConcurrentCallersStillProduceOneCRDList(t *testing.T) {
	registry, client, fake := newRegistry(t, object(t, widgetCRD))
	listing, listed, release := blockTheCRDList(fake)

	const callers = 8
	done := make(chan error, callers)
	go func() {
		_, err := registry.Definitions(context.Background(), client)
		done <- err
	}()
	<-listing

	var entered atomic.Int32
	for range callers - 1 {
		go func() {
			entered.Add(1)
			_, err := registry.Definitions(context.Background(), client)
			done <- err
		}()
	}
	// All of them are in the call and none of them has been answered, so they are
	// parked on the single flight — which is the state this test is about.
	waitFor(t, func() bool { return entered.Load() == callers-1 }, "the other callers never started")
	if n := len(done); n != 0 {
		t.Fatalf("%d callers were answered while the only CRD list in flight is held open by this test", n)
	}
	release()

	for range callers {
		if err := <-done; err != nil {
			t.Fatalf("a concurrent caller failed: %v", err)
		}
	}
	if n := listed.Load(); n != 1 {
		t.Errorf("%d concurrent callers made %d CRD lists, want 1 — this is the stampede the single "+
			"flight exists for", callers, n)
	}
}

// The same hazard on the synchronous path: letting go of the slot's lock for
// the length of the round trip is what makes this fetch able to land *after*
// something newer. The case that matters is the one the background renewal
// already guards — the kubeconfig is rebuilt while the fetch is in flight, so
// dynamicLocked drops the catalog on purpose ("the CRDs it saw belong to the
// cluster the old config pointed at") — and a context name is reused, so what
// lands afterwards is one cluster's kinds under another cluster's name.
//
// Two dynamic clients rather than one, because a blocked reactor holds the whole
// fake.
//
// The second request below is started while the first is still held open.
// Releasing the fetch *before* asking again with the new config would pass with
// the guard deleted: nothing would have rebuilt the client while the list was
// in flight, so there would be nothing to supersede.
func TestASlowFetchDoesNotServeThePreviousClustersKinds(t *testing.T) {
	before := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{crdGVR: "CustomResourceDefinitionList"},
		object(t, widgetCRD))
	after := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{crdGVR: "CustomResourceDefinitionList"},
		object(t, clusterWidgetCRD))

	registry := NewRegistry()
	// Young by any measure: a catalog stamped now would be served for an hour.
	registry.ttl, registry.renewAfter = time.Hour, time.Hour

	listing, _, release := blockTheCRDList(before)
	defer release()

	old := &cluster.Client{Context: "test", Config: &rest.Config{Host: "before"}, Dynamic: before}
	first := make(chan error, 1)
	go func() {
		_, err := registry.Definitions(context.Background(), old)
		first <- err
	}()
	<-listing // on the network against the cluster this context used to name

	// The kubeconfig is reloaded and this context now names another cluster. This
	// request rebuilds the dynamic client and then parks on the fetch above,
	// which is still in flight against the old one.
	fresh := &cluster.Client{Context: "test", Config: &rest.Config{Host: "after"}, Dynamic: after}
	second := make(chan []string, 1)
	go func() {
		defs, err := registry.Definitions(context.Background(), fresh)
		if err != nil {
			second <- []string{"error: " + err.Error()}
			return
		}
		names := make([]string, 0, len(defs))
		for _, d := range defs {
			names = append(names, d.Slug())
		}
		second <- names
	}()
	waitFor(t, func() bool {
		s := registry.slot("test")
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.builtFrom == fresh.Config
	}, "the second request never rebuilt the dynamic client")

	release()
	if err := <-first; err != nil {
		t.Fatalf("the first request failed: %v", err)
	}

	if got := <-second; len(got) != 1 || got[0] != "clusterwidgets.example.io" {
		t.Errorf("this context now names a different cluster and the console serves %v: a list that "+
			"started before the reload was stamped fresh under the new cluster's name, which is the "+
			"part a TTL cannot recover from", got)
	}
}

// A selection of several namespaces narrows a custom resource's rows before
// they are matched, as it does a built-in kind's. No printer column is
// Filterable today, so
// nothing is offered yet; the day one is, the values come from these rows.
func TestASelectionOfNamespacesNarrowsACustomResourceBeforeItIsMatched(t *testing.T) {
	in := func(name, namespace string) *unstructured.Unstructured {
		return widget(t, name, namespace, `{"status": {"phase": "Healthy"}}`)
	}
	registry, client, _ := newRegistry(t, object(t, widgetCRD),
		in("alpha", "apps"), in("beta", "shop"), in("gamma", "kube-system"))

	table, err := registry.List(t.Context(), accesstest.Allow(), client, "widgets", "", []string{"apps", "shop"}, "", "", nil)
	if err != nil {
		t.Fatalf("listing widgets: %v", err)
	}
	names := make([]string, 0, len(table.Rows))
	for _, row := range table.Rows {
		names = append(names, row.Namespace+"/"+row.Name)
	}
	if strings.Join(names, ",") != "apps/alpha,shop/beta" {
		t.Errorf("a selection of apps and shop listed %v; kube-system/gamma is outside it", names)
	}

	whole, err := registry.List(t.Context(), accesstest.Allow(), client, "widgets", "", nil, "", "", nil)
	if err != nil {
		t.Fatalf("listing widgets: %v", err)
	}
	if len(whole.Rows) != 3 {
		t.Errorf("no selection listed %d widgets, want all 3", len(whole.Rows))
	}
}
