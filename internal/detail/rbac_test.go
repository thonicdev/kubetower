package detail

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// The five kinds under Access Control draw their relations. The
// forward half is pure - a binding's roleRef and subjects, read off the
// manifest - and the reverse half is usedby.go's walk with a wider table, so
// the tests are the same two shapes as usedby_test.go's: fixtures against
// readRelations, and a fake dynamic client against the walk.

const roleBindingToARole = `
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: { name: api-reads-config, namespace: shop }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: Role, name: config-reader }
subjects:
  - { kind: ServiceAccount, name: api, namespace: shop }
  - { kind: User, name: alice, apiGroup: rbac.authorization.k8s.io }
  - { kind: Group, name: "system:masters", apiGroup: rbac.authorization.k8s.io }
`

const clusterRoleBindingAcrossNamespaces = `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: { name: metrics-readers }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: metrics-reader }
subjects:
  - { kind: ServiceAccount, name: prometheus, namespace: observability }
`

// A RoleBinding that binds a ClusterRole - the shape the API permits and
// the one that makes a ClusterRole's referrers cluster-wide.
const roleBindingToAClusterRole = `
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: { name: shop-metrics, namespace: shop }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: metrics-reader }
subjects:
  - { kind: ServiceAccount, name: scraper, namespace: shop }
`

const deploymentRunningAsAPI = `
apiVersion: apps/v1
kind: Deployment
metadata: { name: api, namespace: shop }
spec:
  template:
    spec:
      serviceAccountName: api
      containers:
        - name: api
`

func relationsOf(t *testing.T, resource, doc string) ([]Relation, string) {
	t.Helper()
	set, _, _ := readRelations(object(t, doc), resource, func(string, string) (string, bool, bool) {
		return "", false, false
	})
	return set.finish()
}

func findRole(relations []Relation, role string) []Relation {
	var out []Relation
	for _, rel := range relations {
		if rel.Role == role {
			out = append(out, rel)
		}
	}
	return out
}

func TestARoleBindingDrawsWhatItGrantsAndToWhom(t *testing.T) {
	relations, note := relationsOf(t, resRoleBindings, roleBindingToARole)

	grants := findRole(relations, RoleGrants)
	if len(grants) != 1 {
		t.Fatalf("Grants rows = %d, want 1: %+v", len(grants), relations)
	}
	if grants[0].Kind != resRoles || grants[0].Namespace != "shop" || grants[0].Name != "config-reader" {
		t.Errorf("the Role is drawn as %+v, want roles shop/config-reader", grants[0])
	}

	subjects := findRole(relations, RoleSubject)
	if len(subjects) != 3 {
		t.Fatalf("Subject rows = %d, want 3 (the ServiceAccount, the user and the group): %+v",
			len(subjects), subjects)
	}
	var account *Relation
	for i := range subjects {
		if subjects[i].Linkable() {
			account = &subjects[i]
		}
	}
	if account == nil || account.Kind != resServiceAccounts ||
		account.Namespace != "shop" || account.Name != "api" {
		t.Errorf("the linkable subject is %+v, want serviceaccounts shop/api", account)
	}

	// The two identities that are not objects are listed, inert, with no
	// sentence about them.
	if note != "" {
		t.Errorf("a user and a group produced a note: %q", note)
	}
	labels := map[string]bool{}
	for _, subject := range subjects {
		labels[subject.Label] = subject.Linkable()
	}
	for _, want := range []string{"user alice", "group system:masters"} {
		linkable, present := labels[want]
		if !present || linkable {
			t.Errorf("%q: present=%v linkable=%v, want an inert entry", want, present, linkable)
		}
	}
}

func TestAClusterRoleBindingNamesAClusterRoleWithNoNamespace(t *testing.T) {
	relations, note := relationsOf(t, resClusterRoleBindings, clusterRoleBindingAcrossNamespaces)

	grants := findRole(relations, RoleGrants)
	if len(grants) != 1 || grants[0].Kind != resClusterRoles || grants[0].Namespace != "" || grants[0].Name != "metrics-reader" {
		t.Fatalf("the ClusterRole is drawn as %+v, want clusterroles /metrics-reader with no namespace", grants)
	}
	subjects := findRole(relations, RoleSubject)
	if len(subjects) != 1 || subjects[0].Namespace != "observability" || subjects[0].Name != "prometheus" {
		t.Fatalf("the subject is drawn as %+v, want serviceaccounts observability/prometheus - the subject's own namespace", subjects)
	}
	if note != "" {
		t.Errorf("a binding with only ServiceAccount subjects has a note: %q", note)
	}
}

func TestARoleBindingMayBindAClusterRole(t *testing.T) {
	relations, _ := relationsOf(t, resRoleBindings, roleBindingToAClusterRole)
	grants := findRole(relations, RoleGrants)
	if len(grants) != 1 || grants[0].Kind != resClusterRoles || grants[0].Namespace != "" {
		t.Fatalf("a RoleBinding's ClusterRole is drawn as %+v, want clusterroles with no namespace", grants)
	}
}

// --- The reverse half --------------------------------------------------------

var rbacListKinds = func() map[schema.GroupVersionResource]string {
	kinds := map[schema.GroupVersionResource]string{}
	for k, v := range listKinds {
		kinds[k] = v
	}
	kinds[gvr("rbac.authorization.k8s.io", "v1", resRoleBindings)] = "RoleBindingList"
	kinds[gvr("rbac.authorization.k8s.io", "v1", resClusterRoleBindings)] = "ClusterRoleBindingList"
	return kinds
}()

func walkRBAC(t *testing.T, resource, namespace, name string, seed ...runtime.Object) (*Usage, []Relation, string) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), rbacListKinds, seed...)
	return walkWith(t, dyn, resource, namespace, name)
}

func TestARoleIsBoundByTheBindingsOfItsNamespaceThatNameIt(t *testing.T) {
	use, relations, _ := walkRBAC(t, resRoles, "shop", "config-reader",
		seeded(t, roleBindingToARole),
		seeded(t, roleBindingToAClusterRole), // names a ClusterRole, not this Role
	)
	bound := findRole(relations, RoleBoundBy)
	if len(bound) != 1 {
		t.Fatalf("Bound by rows = %d, want 1: %+v", len(bound), relations)
	}
	if bound[0].Kind != resRoleBindings || bound[0].Name != "api-reads-config" {
		t.Errorf("bound by %+v, want rolebindings shop/api-reads-config", bound[0])
	}
	if use.Subject != "Role" {
		t.Errorf("Subject = %q, want Role", use.Subject)
	}
	// A Role is bound from its own namespace and nowhere else, so the walk
	// asked the RoleBindings and not the ClusterRoleBindings.
	if strings.Contains(strings.Join(use.Looked, ","), "ClusterRoleBindings") {
		t.Errorf("a Role's walk listed ClusterRoleBindings, which cannot bind one: %v", use.Looked)
	}
	for _, label := range use.Looked {
		if label == "Deployments" {
			t.Errorf("a Role's walk listed workloads, which cannot name one: %v", use.Looked)
		}
	}
}

func TestAClusterRoleIsBoundFromEveryNamespaceAndByClusterRoleBindings(t *testing.T) {
	_, relations, _ := walkRBAC(t, resClusterRoles, "", "metrics-reader",
		seeded(t, clusterRoleBindingAcrossNamespaces),
		seeded(t, roleBindingToAClusterRole),
		seeded(t, roleBindingToARole), // a Role, not this
	)
	bound := findRole(relations, RoleBoundBy)
	if len(bound) != 2 {
		t.Fatalf("Bound by rows = %d, want 2 (one ClusterRoleBinding, one RoleBinding in shop): %+v", len(bound), bound)
	}
	kinds := map[string]string{}
	for _, rel := range bound {
		kinds[rel.Kind] = rel.Namespace + "/" + rel.Name
	}
	if kinds[resClusterRoleBindings] != "/metrics-readers" || kinds[resRoleBindings] != "shop/shop-metrics" {
		t.Errorf("bound by %v", kinds)
	}
}

func TestAServiceAccountIsBoundByBindingsAndRunByWorkloads(t *testing.T) {
	_, relations, _ := walkRBAC(t, resServiceAccounts, "shop", "api",
		seeded(t, roleBindingToARole),
		seeded(t, deploymentRunningAsAPI),
		seeded(t, clusterRoleBindingAcrossNamespaces), // binds another account
	)
	bound := findRole(relations, RoleBoundBy)
	if len(bound) != 1 || bound[0].Name != "api-reads-config" {
		t.Fatalf("Bound by = %+v, want the one RoleBinding naming shop/api", bound)
	}
	used := findRole(relations, RoleUsedBy)
	if len(used) != 1 || used[0].Kind != "deployments" || used[0].Name != "api" {
		t.Fatalf("Used by = %+v, want the Deployment running as it", used)
	}
	if !strings.Contains(used[0].Detail, "runs as it") {
		t.Errorf("the workload row does not say how: %q", used[0].Detail)
	}
}

func TestAnAccountBoundFromAnotherNamespaceIsFound(t *testing.T) {
	// A RoleBinding in shop may name a ServiceAccount of observability as its
	// subject, which is why a ServiceAccount's RoleBindings are read across the
	// cluster and not in its own namespace.
	_, relations, _ := walkRBAC(t, resServiceAccounts, "observability", "prometheus",
		seeded(t, clusterRoleBindingAcrossNamespaces),
		seeded(t, `
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: { name: let-prometheus-read, namespace: shop }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: Role, name: config-reader }
subjects:
  - { kind: ServiceAccount, name: prometheus, namespace: observability }
`),
	)
	bound := findRole(relations, RoleBoundBy)
	if len(bound) != 2 {
		t.Fatalf("Bound by rows = %d, want 2 (a ClusterRoleBinding and a RoleBinding in shop): %+v", len(bound), bound)
	}
}

func TestAConfigMapStillPaysForNoRBACList(t *testing.T) {
	// The widened table must not send a ConfigMap's walk to lists that cannot
	// name it: the same guard TestAConfigMapDoesNotPayForTheSecretOnlyLists
	// makes, for the two access-control lists.
	use, _, _ := walk(t, resConfigMaps, "shop", "api-config")
	for _, label := range use.Looked {
		if label == "RoleBindings" || label == "ClusterRoleBindings" {
			t.Errorf("a ConfigMap's walk listed %s, which cannot name one", label)
		}
	}
}
