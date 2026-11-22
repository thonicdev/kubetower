package detail

// A Role's rules, read the way the drawer reads them.
//
// Through `Reader.Get` rather than by calling `rulesOf`, for the reason
// annotations_test.go gives one file along: what is guarded is not a
// function's logic but a panel being filled, and the only honest way to ask
// what the panel receives is to read an object the way it does.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/resources"
)

const rbacGroup = "rbac.authorization.k8s.io"

// rbacReaderFor is `readerFor` with the two RBAC kinds registered instead of the
// three core ones. A separate builder rather than a widened shared one: the
// shared harness is `internal/detail`'s oldest and is depended on by five files,
// and a kind added there is a kind every one of them can now accidentally read.
func rbacReaderFor(t *testing.T, objects ...runtime.Object) *Reader {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config")
	body := `apiVersion: v1
kind: Config
clusters:
- name: nowhere
  cluster:
    server: https://127.0.0.1:1
contexts:
- name: prod
  context: {cluster: nowhere, user: nobody}
users:
- name: nobody
  user: {token: none}
current-context: prod
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing a kubeconfig: %v", err)
	}
	clusters, err := cluster.NewManager(path)
	if err != nil {
		t.Fatalf("loading the kubeconfig: %v", err)
	}

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			{Group: rbacGroup, Version: "v1", Resource: "roles"}:        "RoleList",
			{Group: rbacGroup, Version: "v1", Resource: "clusterroles"}: "ClusterRoleList",
		},
		objects...,
	)

	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Group: rbacGroup, Version: "v1"}})
	mapper.Add(schema.GroupVersionKind{Group: rbacGroup, Version: "v1", Kind: "Role"}, meta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: rbacGroup, Version: "v1", Kind: "ClusterRole"}, meta.RESTScopeRoot)

	reader := NewReader(clusters, accesstest.Allow())
	reader.newMapper = func(context.Context, *cluster.Client) (meta.RESTMapper, error) { return mapper, nil }
	reader.dynamicOf = func(*cluster.Client) (dynamic.Interface, error) { return dyn, nil }
	return reader
}

func roleObject(namespace, name string, rules ...any) *unstructured.Unstructured {
	object := map[string]any{
		"apiVersion": rbacGroup + "/v1", "kind": "Role",
		"metadata": map[string]any{"name": name, "namespace": namespace, "resourceVersion": "1"},
	}
	if rules != nil {
		object["rules"] = rules
	}
	return &unstructured.Unstructured{Object: object}
}

func clusterRoleObject(name string, rules ...any) *unstructured.Unstructured {
	object := map[string]any{
		"apiVersion": rbacGroup + "/v1", "kind": "ClusterRole",
		"metadata": map[string]any{"name": name, "resourceVersion": "1"},
	}
	if rules != nil {
		object["rules"] = rules
	}
	return &unstructured.Unstructured{Object: object}
}

func rule(groups, resourceNames, verbs []any) map[string]any {
	return map[string]any{"apiGroups": groups, "resources": resourceNames, "verbs": verbs}
}

// The whole of the point: every rule of a Role, on the page that opens by
// default, without the Manifest tab.
func TestARolesRulesReachTheDrawerWithoutTheManifestTab(t *testing.T) {
	reader := rbacReaderFor(t, roleObject("shop", "reader",
		rule([]any{""}, []any{"pods", "configmaps"}, []any{"get", "list", "watch"}),
		rule([]any{"apps"}, []any{"deployments"}, []any{"get"}),
	))

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "roles", Namespace: "shop", Name: "reader",
	})
	if err != nil {
		t.Fatalf("reading the role: %v", err)
	}
	if object.Rules == nil {
		t.Fatal("the role carries no rules block; the panel has nothing to draw")
	}
	if len(object.Rules.Rules) != 2 {
		t.Fatalf("%d rules arrived, want 2: %+v", len(object.Rules.Rules), object.Rules.Rules)
	}

	first := object.Rules.Rules[0]
	if strings.Join(first.Resources, ",") != "pods,configmaps" {
		t.Errorf("the resources did not arrive in the order they are stored: %v", first.Resources)
	}
	if strings.Join(first.Verbs, ",") != "get,list,watch" {
		t.Errorf("the verbs did not arrive: %v", first.Verbs)
	}
	// The core group is the empty string, and it must survive as a one-entry list
	// rather than be tidied into nothing: `apiGroups: [""]` and no apiGroups at
	// all are different rules, and only the first grants anything on core kinds.
	if len(first.APIGroups) != 1 || first.APIGroups[0] != "" {
		t.Errorf("the core group did not arrive as the empty string: %#v", first.APIGroups)
	}
	if object.Rules.Unreadable != "" {
		t.Errorf("a readable role says it is not: %q", object.Rules.Unreadable)
	}
	// Nothing was graded, so nothing is said. A note that is always set is a note
	// nobody reads.
	if object.Rules.Note != "" {
		t.Errorf("a note on a role with no wildcard: %q", object.Rules.Note)
	}
}

// `resourceNames` and `nonResourceURLs` are the two members a naive table drops,
// and the second is the one `ruleSummary:1893` already handles for the list — a
// rule granting only `/healthz` has no resources at all, and an empty resources
// cell reads as a rule that grants nothing.
func TestARuleKeepsItsResourceNamesAndItsNonResourceURLs(t *testing.T) {
	reader := rbacReaderFor(t, roleObject("shop", "narrow",
		map[string]any{
			"apiGroups":     []any{""},
			"resources":     []any{"secrets"},
			"resourceNames": []any{"db-credentials"},
			"verbs":         []any{"get"},
		},
		map[string]any{
			"nonResourceURLs": []any{"/healthz", "/metrics"},
			"verbs":           []any{"get"},
		},
	))

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "roles", Namespace: "shop", Name: "narrow",
	})
	if err != nil {
		t.Fatalf("reading the role: %v", err)
	}

	named := object.Rules.Rules[0]
	if strings.Join(named.ResourceNames, ",") != "db-credentials" {
		t.Errorf("resourceNames did not arrive: %v", named.ResourceNames)
	}

	urls := object.Rules.Rules[1]
	if strings.Join(urls.NonResourceURLs, ",") != "/healthz,/metrics" {
		t.Errorf("nonResourceURLs did not arrive: %v", urls.NonResourceURLs)
	}
	// And it carries no resources rather than an empty list, which is what lets
	// the panel draw it as the other kind of rule it is.
	if urls.Resources != nil {
		t.Errorf("a non-resource rule grew a resources list: %v", urls.Resources)
	}
	if named.NonResourceURLs != nil {
		t.Errorf("an ordinary rule grew a nonResourceURLs list: %v", named.NonResourceURLs)
	}
}

// The grade is the list's, not a second one. `ruleSummary` colors any wildcard
// and ranks worst when `*` falls on **both** verbs and resources — cluster-admin
// under another name — and the panel says the same thing about the same rule in
// the same three words.
func TestTheRulesPanelGradesAWildcardTheWayTheListDoes(t *testing.T) {
	reader := rbacReaderFor(t, clusterRoleObject("admin",
		rule([]any{"*"}, []any{"*"}, []any{"*"}),
		rule([]any{"*"}, []any{"pods"}, []any{"get"}),
		rule([]any{""}, []any{"pods"}, []any{"get"}),
	))

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "clusterroles", Name: "admin",
	})
	if err != nil {
		t.Fatalf("reading the clusterrole: %v", err)
	}
	rules := object.Rules.Rules

	if rules[0].Wildcards != "api groups, resources, verbs" || rules[0].WildcardClass != resources.ClassBad {
		t.Errorf("* on everything graded %q/%q, want the list's worst class",
			rules[0].Wildcards, rules[0].WildcardClass)
	}
	// One wildcard, and on api groups alone: colored, never ranked worst. A
	// stock cluster ships dozens of these and a panel that shouted about each
	// would be one nobody reads twice.
	if rules[1].Wildcards != "api groups" || rules[1].WildcardClass != resources.ClassWarn {
		t.Errorf("* on api groups alone graded %q/%q, want the list's warn class",
			rules[1].Wildcards, rules[1].WildcardClass)
	}
	if rules[2].Wildcards != "" || rules[2].WildcardClass != "" {
		t.Errorf("a rule with no wildcard was graded %q/%q", rules[2].Wildcards, rules[2].WildcardClass)
	}

	// The literal is drawn and no sentence explains that it is not expanded.
	if object.Rules.Note != "" {
		t.Errorf("a wildcard produced a note: %q", object.Rules.Note)
	}
}

// The first of the two negative answers. A Role whose `rules` array is empty is
// a real and consequential object — it grants nothing — and it is not a panel
// that failed to draw.
func TestARoleThatGrantsNothingSaysSoRatherThanDrawingNoPanel(t *testing.T) {
	reader := rbacReaderFor(t, roleObject("shop", "empty"))

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "roles", Namespace: "shop", Name: "empty",
	})
	if err != nil {
		t.Fatalf("reading the role: %v", err)
	}
	if object.Rules == nil {
		t.Fatal("a Role with no rules carries no rules block at all; empty and absent now render the same")
	}
	if len(object.Rules.Rules) != 0 {
		t.Errorf("rules arrived from nowhere: %+v", object.Rules.Rules)
	}
	if object.Rules.Unreadable != "" {
		t.Errorf("an empty role is reported as a fault: %q", object.Rules.Unreadable)
	}
}

// The second. A `rules` field that is present and is not a list of rules is an
// object the API server would not have produced, and drawing it as a Role that
// grants nothing is the worst available answer on a permissions surface.
func TestARoleWhoseRulesAreNotRulesIsAFaultAndNotAnEmptyOne(t *testing.T) {
	broken := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": rbacGroup + "/v1", "kind": "Role",
		"metadata": map[string]any{"name": "broken", "namespace": "shop", "resourceVersion": "1"},
		"rules":    []any{"get pods"},
	}}
	reader := rbacReaderFor(t, broken)

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "roles", Namespace: "shop", Name: "broken",
	})
	if err != nil {
		t.Fatalf("reading the role: %v", err)
	}
	if object.Rules == nil {
		t.Fatal("no rules block at all")
	}
	if object.Rules.Unreadable == "" {
		t.Fatalf("a rules array that is not rules reads as a role that grants nothing: %+v", object.Rules)
	}
	if len(object.Rules.Rules) != 0 {
		t.Errorf("a fault carries rules as well: %+v", object.Rules.Rules)
	}
}

// An aggregated ClusterRole's rules are the controller's: it writes them from
// the roles its label selector matches, so they are accurate at the read and
// move when the cluster's roles do. Drawn without the sentence, the table reads
// as a fixed grant.
func TestAnAggregatedClusterRoleSaysItsRulesAreWrittenForIt(t *testing.T) {
	aggregated := clusterRoleObject("view", rule([]any{""}, []any{"pods"}, []any{"get"}))
	aggregated.Object["aggregationRule"] = map[string]any{
		"clusterRoleSelectors": []any{
			map[string]any{"matchLabels": map[string]any{"rbac.example.com/aggregate-to-view": "true"}},
		},
	}
	reader := rbacReaderFor(t, aggregated)

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "clusterroles", Name: "view",
	})
	if err != nil {
		t.Fatalf("reading the clusterrole: %v", err)
	}
	if !strings.Contains(object.Rules.Note, "aggregated") {
		t.Errorf("nothing says the rules are the controller's: %q", object.Rules.Note)
	}
}

// An aggregated ClusterRole the controller has not filled yet is *also* empty,
// and the sentence has to reach that case too — otherwise the one object where
// "grants nothing" is most likely to be temporary is the one that says least.
func TestAnEmptyAggregatedClusterRoleStillSaysWhereItsRulesComeFrom(t *testing.T) {
	aggregated := clusterRoleObject("fresh")
	aggregated.Object["aggregationRule"] = map[string]any{"clusterRoleSelectors": []any{}}
	reader := rbacReaderFor(t, aggregated)

	object, err := reader.Get(context.Background(), "prod", Request{
		Kind: "clusterroles", Name: "fresh",
	})
	if err != nil {
		t.Fatalf("reading the clusterrole: %v", err)
	}
	if len(object.Rules.Rules) != 0 {
		t.Fatalf("rules arrived: %+v", object.Rules.Rules)
	}
	if !strings.Contains(object.Rules.Note, "aggregated") {
		t.Errorf("an empty aggregated role says nothing about where its rules come from: %q", object.Rules.Note)
	}
}

// The selectors an aggregated ClusterRole names are carried, one list of
// requirements per selector, so the panel can draw what the role aggregates.
func TestAnAggregatedClusterRoleCarriesItsSelectors(t *testing.T) {
	aggregated := clusterRoleObject("admin", rule([]any{""}, []any{"pods"}, []any{"get"}))
	aggregated.Object["aggregationRule"] = map[string]any{
		"clusterRoleSelectors": []any{
			map[string]any{"matchLabels": map[string]any{"b": "2", "a": "1"}},
			map[string]any{"matchExpressions": []any{
				map[string]any{"key": "tier", "operator": "In", "values": []any{"x", "y"}},
				map[string]any{"key": "hide", "operator": "DoesNotExist"},
			}},
		},
	}
	reader := rbacReaderFor(t, aggregated)

	object, err := reader.Get(context.Background(), "prod", Request{Kind: "clusterroles", Name: "admin"})
	if err != nil {
		t.Fatalf("reading the clusterrole: %v", err)
	}
	got := strings.Join([]string{
		strings.Join(object.Rules.Selectors[0], "|"),
		strings.Join(object.Rules.Selectors[1], "|"),
	}, ";")
	if want := "a=1|b=2;tier in (x,y)|!hide"; got != want {
		t.Errorf("selectors = %s, want %s", got, want)
	}
}
