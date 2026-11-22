package detail

// The reverse walk, in two halves that are tested separately for the reason the
// walk exists at all: the **matching** is pure and answers from a fixture, and
// the **listing** is a live read whose three negative answers must stay three.
//
// Every fixture here is shaped on what `kubernetes-admin@kubernetes` actually
// serves, and the counts quoted in usedby.go's header come from that cluster.

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"

	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

func uses(t *testing.T, resource, doc string, target usageTarget) *Relation {
	t.Helper()
	return usesTarget(object(t, doc), resource, target)
}

func theConfigMap(namespace, name string) usageTarget {
	return targetOf(resConfigMaps, namespace, name)
}

func theSecret(namespace, name string) usageTarget {
	return targetOf(resSecrets, namespace, name)
}

// --- The matching half: pure, one object in, at most one row out -------------

const deploymentMountingIt = `
apiVersion: apps/v1
kind: Deployment
metadata: { name: api, namespace: shop }
spec:
  template:
    spec:
      containers:
        - name: api
          volumeMounts:
            - { name: conf, mountPath: /etc/api }
      volumes:
        - name: conf
          configMap: { name: api-config }
`

func TestADeploymentMountingTheConfigMapIsFound(t *testing.T) {
	got := uses(t, "deployments", deploymentMountingIt, theConfigMap("shop", "api-config"))
	if got == nil {
		t.Fatal("a Deployment mounting this ConfigMap as a volume is not listed as using it")
	}
	if got.Role != RoleUsedBy {
		t.Errorf("role = %q, want %q", got.Role, RoleUsedBy)
	}
	if got.Label != "Deployment/api" || got.Kind != "deployments" || got.Namespace != "shop" {
		t.Errorf("row addresses %s %s/%s, want deployments shop/api", got.Kind, got.Namespace, got.Label)
	}
	// The mount path is the whole point of reusing Relation.Mounts: it is what
	// answers "which secret is at /etc/creds" from the other side.
	if strings.Join(got.Mounts, ",") != "/etc/api" {
		t.Errorf("mounts = %v, want [/etc/api]", got.Mounts)
	}
}

// A name that matches in another namespace matches nothing. There is no such
// thing as a cross-namespace configMap volume reference, so a walk that found
// one would be inventing it.
func TestANearMissInAnotherNamespaceIsNotAUse(t *testing.T) {
	if got := uses(t, "deployments", deploymentMountingIt, theConfigMap("other", "api-config")); got != nil {
		t.Fatalf("matched across namespaces: %+v", got)
	}
	if got := uses(t, "deployments", deploymentMountingIt, theConfigMap("shop", "api-conf")); got != nil {
		t.Fatalf("matched a different name: %+v", got)
	}
}

const statefulSetImportingIt = `
apiVersion: apps/v1
kind: StatefulSet
metadata: { name: db, namespace: shop }
spec:
  template:
    spec:
      containers:
        - name: db
          envFrom:
            - configMapRef: { name: api-config }
              prefix: DB_
`

// `envFrom` is the case a filter over readRelations alone gets wrong: the
// import feeds the Containers panel and is not a relation, so a
// ConfigMap used only this way would read as unused.
func TestAWholeEnvFromImportIsAUse(t *testing.T) {
	got := uses(t, "statefulsets", statefulSetImportingIt, theConfigMap("shop", "api-config"))
	if got == nil {
		t.Fatal("a StatefulSet importing this ConfigMap with envFrom is not listed as using it")
	}
	if !strings.Contains(got.Detail, "every key imported into db") {
		t.Errorf("detail = %q, want it to say the whole import", got.Detail)
	}
	if !strings.Contains(got.Detail, "DB_") {
		t.Errorf("detail = %q, want the prefix, which is what the variables are actually called", got.Detail)
	}
}

const daemonSetReadingOneKey = `
apiVersion: apps/v1
kind: DaemonSet
metadata: { name: agent, namespace: shop }
spec:
  template:
    spec:
      containers:
        - name: agent
          env:
            - name: LEVEL
              valueFrom:
                configMapKeyRef: { name: api-config, key: log.level, optional: true }
`

func TestOneVariableReadThroughConfigMapKeyRefIsAUse(t *testing.T) {
	got := uses(t, "daemonsets", daemonSetReadingOneKey, theConfigMap("shop", "api-config"))
	if got == nil {
		t.Fatal("a DaemonSet reading one key out of this ConfigMap is not listed as using it")
	}
	if !strings.Contains(got.Detail, "log.level") {
		t.Errorf("detail = %q, want the key, which is what distinguishes this from mounting the lot", got.Detail)
	}
	// An optional reference is not annotated: no word about whether it may be there.
	if strings.Contains(got.Detail, "absent") || strings.Contains(got.Detail, "optional") {
		t.Errorf("detail = %q, want no word about the reference being optional", got.Detail)
	}
}

const podWithAPullSecret = `
apiVersion: v1
kind: Pod
metadata: { name: api-7d9f, namespace: shop }
spec:
  imagePullSecrets:
    - name: ghcr-login
`

func TestAnImagePullSecretIsAUse(t *testing.T) {
	got := uses(t, resPods, podWithAPullSecret, theSecret("shop", "ghcr-login"))
	if got == nil {
		t.Fatal("a pod pulling with this Secret is not listed as using it")
	}
	if !strings.Contains(got.Detail, "pulls images") {
		t.Errorf("detail = %q, want it to say the kind of use", got.Detail)
	}
}

const ingressWithTLS = `
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata: { name: shop, namespace: shop }
spec:
  tls:
    - hosts: [shop.example.com]
      secretName: shop-tls
`

func TestAnIngressCertificateIsAUse(t *testing.T) {
	got := uses(t, resIngresses, ingressWithTLS, theSecret("shop", "shop-tls"))
	if got == nil {
		t.Fatal("an Ingress serving this Secret as its certificate is not listed as using it")
	}
	if !strings.Contains(got.Detail, "shop.example.com") {
		t.Errorf("detail = %q, want the host the certificate is for", got.Detail)
	}
}

const serviceAccountListingASecret = `
apiVersion: v1
kind: ServiceAccount
metadata: { name: builder, namespace: shop }
secrets:
  - name: builder-token
imagePullSecrets:
  - name: ghcr-login
`

// The other half of the ServiceAccount indirection: a pod names its
// ServiceAccount, and the ServiceAccount must name its Secrets, or the chain
// stops one hop short of the answer.
func TestAServiceAccountListingTheSecretIsAUse(t *testing.T) {
	got := uses(t, resServiceAccounts, serviceAccountListingASecret, theSecret("shop", "builder-token"))
	if got == nil {
		t.Fatal("a ServiceAccount listing this Secret is not listed as using it")
	}
	pull := uses(t, resServiceAccounts, serviceAccountListingASecret, theSecret("shop", "ghcr-login"))
	if pull == nil {
		t.Fatal("a ServiceAccount's imagePullSecrets is not listed as a use")
	}
}

const deploymentNamingNoServiceAccount = `
apiVersion: apps/v1
kind: Deployment
metadata: { name: web, namespace: x }
spec:
  template:
    spec:
      containers:
        - name: web
`

const deploymentNamingDefault = `
apiVersion: apps/v1
kind: Deployment
metadata: { name: web, namespace: x }
spec:
  template:
    spec:
      serviceAccountName: default
      containers:
        - name: web
`

// A template that names no ServiceAccount runs as `default`; admission writes
// it on the Pods, never on the template the walk reads. Missing it makes the
// account the workload runs as read as safe to delete.
func TestATemplateNamingNoServiceAccountUsesDefault(t *testing.T) {
	use, relations, _ := walkRBAC(t, resServiceAccounts, "x", "default", seeded(t, deploymentNamingNoServiceAccount))
	if use == nil || use.Found != 1 {
		t.Fatalf("Usage = %+v, want the Deployment found once", use)
	}
	got := find(t, relations, RoleUsedBy, "Deployment/web")
	if got.Detail != "runs as it, by default (no serviceAccountName)" {
		t.Errorf("detail = %q, want it to say the account is the default one", got.Detail)
	}

	if other := uses(t, "deployments", deploymentNamingNoServiceAccount,
		targetOf(resServiceAccounts, "x", "other")); other != nil {
		t.Errorf("x/other is not what the Deployment runs as, matched: %+v", other)
	}
	if elsewhere := uses(t, "deployments", deploymentNamingNoServiceAccount,
		targetOf(resServiceAccounts, "y", "default")); elsewhere != nil {
		t.Errorf("y/default is another namespace's, matched: %+v", elsewhere)
	}

	// The workload's own panel keeps what the template says.
	forward, _ := relationsOf(t, "deployments", deploymentNamingNoServiceAccount)
	if sa := findRole(forward, RoleServiceAccount); len(sa) != 0 {
		t.Errorf("forward relations gained a ServiceAccount the template does not name: %+v", sa)
	}
}

func TestATemplateNamingDefaultRunsAsIt(t *testing.T) {
	got := uses(t, "deployments", deploymentNamingDefault, targetOf(resServiceAccounts, "x", "default"))
	if got == nil {
		t.Fatal("a Deployment naming default is not listed as using it")
	}
	if got.Detail != "runs as it" {
		t.Errorf("detail = %q, want %q", got.Detail, "runs as it")
	}
}

const bindingOfViewToAServiceAccount = `
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: { name: app-view, namespace: ns }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: view }
subjects:
  - { kind: ServiceAccount, name: app, namespace: ns }
`

// Reversed onto the ServiceAccount's panel, a binding's row says which role it
// grants: that panel has no role above it for the forward wording to lean on.
func TestABindingOnItsServiceAccountNamesTheRole(t *testing.T) {
	got := uses(t, resRoleBindings, bindingOfViewToAServiceAccount,
		targetOf(resServiceAccounts, "ns", "app"))
	if got == nil {
		t.Fatal("a RoleBinding naming this ServiceAccount is not listed as binding it")
	}
	if got.Detail != "grants it ClusterRole view" {
		t.Errorf("detail = %q, want %q", got.Detail, "grants it ClusterRole view")
	}
	if strings.Contains(got.Detail, "above") {
		t.Errorf("detail %q points at a role above that the panel does not show", got.Detail)
	}
}

// Reversed onto the role's panel, the same binding grants that role's rules.
func TestABindingOnItsRoleGrantsItsRules(t *testing.T) {
	got := uses(t, resRoleBindings, bindingOfViewToAServiceAccount,
		targetOf(resClusterRoles, "", "view"))
	if got == nil {
		t.Fatal("a RoleBinding of this ClusterRole is not listed as binding it")
	}
	if got.Detail != "grants its rules" {
		t.Errorf("detail = %q, want %q", got.Detail, "grants its rules")
	}
}

const volumeWithAnInTreeSecret = `
apiVersion: apps/v1
kind: Deployment
metadata: { name: files, namespace: shop }
spec:
  template:
    spec:
      volumes:
        - name: share
          cephfs:
            monitors: ["10.0.0.1:6789"]
            secretRef: { name: ceph-key }
`

// A volume source of an in-tree driver: fromVolume's default arm names the
// volume, and its secretRef is found by looking inside it.
func TestAnInTreeVolumeSecretIsAUse(t *testing.T) {
	got := uses(t, "deployments", volumeWithAnInTreeSecret, theSecret("shop", "ceph-key"))
	if got == nil {
		t.Fatal("a cephfs volume's secretRef is not listed as a use")
	}
}

const pvWithCSISecrets = `
apiVersion: v1
kind: PersistentVolume
metadata: { name: pv-0001 }
spec:
  csi:
    driver: rbd.csi.ceph.com
    volumeHandle: "0001"
    nodeStageSecretRef: { name: csi-rbd-node, namespace: ceph }
`

// The other one. A PersistentVolume is cluster-scoped and names a Secret with an
// explicit namespace — the only cross-namespace reference the built-in API
// permits, and the reason the walk is not purely namespaced.
func TestAPersistentVolumeSecretIsAUse(t *testing.T) {
	got := uses(t, resPVs, pvWithCSISecrets, theSecret("ceph", "csi-rbd-node"))
	if got == nil {
		t.Fatal("a PersistentVolume's nodeStageSecretRef is not listed as a use")
	}
	if !strings.Contains(got.Detail, "nodeStageSecretRef") {
		t.Errorf("detail = %q, want the field, because a CSI volume has five of them", got.Detail)
	}
	if got.Namespace != "" {
		t.Errorf("namespace = %q on a cluster-scoped referrer, want none", got.Namespace)
	}
}

const storageClassWithASecret = `
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata: { name: ceph-rbd }
provisioner: rbd.csi.ceph.com
parameters:
  csi.storage.k8s.io/provisioner-secret-name: csi-rbd-provisioner
  csi.storage.k8s.io/provisioner-secret-namespace: ceph
  csi.storage.k8s.io/node-stage-secret-name: ${pvc.name}-node
  csi.storage.k8s.io/node-stage-secret-namespace: ${pvc.namespace}
`

func TestAStorageClassParameterSecretIsAUse(t *testing.T) {
	got := uses(t, resStorageClasses, storageClassWithASecret, theSecret("ceph", "csi-rbd-provisioner"))
	if got == nil {
		t.Fatal("a StorageClass naming this Secret in its parameters is not listed as using it")
	}
	// The templated pair names no one object, so it must match no one object.
	if other := uses(t, resStorageClasses, storageClassWithASecret, theSecret("ceph", "${pvc.name}-node")); other != nil {
		t.Errorf("a templated parameter matched a literal name: %+v", other)
	}
}

// One row per referrer, whatever the number of references. A DaemonSet that
// mounts a ConfigMap, imports it and reads two keys out of it is one object.
const oneReferrerManyWays = `
apiVersion: apps/v1
kind: DaemonSet
metadata: { name: agent, namespace: shop }
spec:
  template:
    spec:
      containers:
        - name: agent
          envFrom:
            - configMapRef: { name: api-config }
          env:
            - { name: A, valueFrom: { configMapKeyRef: { name: api-config, key: a } } }
            - { name: B, valueFrom: { configMapKeyRef: { name: api-config, key: b } } }
          volumeMounts:
            - { name: conf, mountPath: /etc/agent }
      volumes:
        - name: conf
          configMap: { name: api-config }
`

func TestOneReferrerIsOneRowHoweverManyReferences(t *testing.T) {
	got := uses(t, "daemonsets", oneReferrerManyWays, theConfigMap("shop", "api-config"))
	if got == nil {
		t.Fatal("not found at all")
	}
	for _, want := range []string{"volume conf", "every key imported", "key a", "key b"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail = %q, want it to mention %q", got.Detail, want)
		}
	}
}

// --- The walking half: three answers that must stay three --------------------

var listKinds = map[schema.GroupVersionResource]string{
	gvr("apps", "v1", "deployments"):               "DeploymentList",
	gvr("apps", "v1", "statefulsets"):              "StatefulSetList",
	gvr("apps", "v1", "daemonsets"):                "DaemonSetList",
	gvr("batch", "v1", "cronjobs"):                 "CronJobList",
	gvr("batch", "v1", "jobs"):                     "JobList",
	gvr("", "v1", resServiceAccounts):              "ServiceAccountList",
	gvr("networking.k8s.io", "v1", resIngresses):   "IngressList",
	gvr("", "v1", resPVs):                          "PersistentVolumeList",
	gvr("storage.k8s.io", "v1", resStorageClasses): "StorageClassList",
}

func walk(t *testing.T, resource, namespace, name string, seed ...runtime.Object) (*Usage, []Relation, string) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, seed...)
	return walkWith(t, dyn, resource, namespace, name)
}

func walkWith(t *testing.T, dyn *dynamicfake.FakeDynamicClient, resource, namespace, name string) (*Usage, []Relation, string) {
	t.Helper()
	set := newRelationSet()
	subject := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
	}}
	reader := &Reader{gate: accesstest.Allow()}
	use := reader.addUsedBy(context.Background(), &cluster.Client{Dynamic: dyn}, set, subject, resource)
	relations, note := set.finish()
	return use, relations, note
}

func seeded(t *testing.T, doc string) *unstructured.Unstructured {
	t.Helper()
	return object(t, doc)
}

func TestTheWalkFindsTheWorkloadThatMountsIt(t *testing.T) {
	use, relations, _ := walk(t, resConfigMaps, "shop", "api-config", seeded(t, deploymentMountingIt))
	if use == nil || use.Found != 1 {
		t.Fatalf("Usage = %+v, want one referrer found", use)
	}
	find(t, relations, RoleUsedBy, "Deployment/api")
}

// The majority answer, and the one that must never be a refusal.
func TestAConfigMapNothingNamesIsEmptyAndNotRefused(t *testing.T) {
	use, relations, _ := walk(t, resConfigMaps, "shop", "unused-config", seeded(t, deploymentMountingIt))
	if use == nil {
		t.Fatal("no Usage at all: the panel cannot tell 'walked and found nothing' from 'never walked'")
	}
	if use.Found != 0 {
		t.Fatalf("Found = %d, want 0", use.Found)
	}
	if len(use.Refused) != 0 {
		t.Fatalf("Refused = %v on a walk where every list answered", use.Refused)
	}
	if len(use.Looked) == 0 {
		t.Fatal("Looked is empty, so the panel cannot say what 'nothing' was looked for in")
	}
	for _, rel := range relations {
		if rel.Role == RoleUsedBy {
			t.Fatalf("drew a referrer for an object nothing names: %+v", rel)
		}
	}
}

// A refused list is not an empty answer. This is the whole reason Usage carries
// Refused beside Found rather than a count alone.
func TestARefusedListIsNotAnEmptyAnswer(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	dyn.PrependReactor("list", "deployments",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New(`deployments.apps is forbidden: User "reader" cannot list resource "deployments"`)
		})

	use, _, note := walkWith(t, dyn, resConfigMaps, "shop", "api-config")
	if use == nil {
		t.Fatal("no Usage")
	}
	if len(use.Refused) == 0 || use.Err == nil {
		t.Fatalf("Usage = %+v, want the refusal kept so it can be classified", use)
	}
	// And the note stays empty, because nothing was found: the state is the
	// answer, and a note repeating it is a second copy of one sentence. On the
	// measured restricted identity all five namespaced lists refuse at once, and
	// a note per list would draw the same 403 five times.
	if note != "" {
		t.Errorf("relationsNote = %q, want nothing: the refusal is the state's to say", note)
	}
}

// The other half of that rule: a walk that was refused a list **and still found
// something** is `ok`, and this one line is then the only thing on screen saying
// the list is short.
func TestAPartialWalkSaysSoOnceInTheNote(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds,
		seeded(t, deploymentMountingIt))
	dyn.PrependReactor("list", "daemonsets",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("daemonsets.apps is forbidden")
		})
	dyn.PrependReactor("list", "statefulsets",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("statefulsets.apps is forbidden")
		})

	use, relations, note := walkWith(t, dyn, resConfigMaps, "shop", "api-config")
	find(t, relations, RoleUsedBy, "Deployment/api")
	if use.Found != 1 {
		t.Fatalf("Found = %d, want the one the lists that answered did find", use.Found)
	}
	if note != "StatefulSets and DaemonSets could not be listed, so this list may be short." {
		t.Errorf("relationsNote = %q", note)
	}
}

// 51 of the 52 Jobs on the measured cluster are complete, and one Secret there
// has 22 workload referrers of which 16 are finished Jobs. Counting history
// makes almost everything look used.
const completedJob = `
apiVersion: batch/v1
kind: Job
metadata: { name: migrate-once, namespace: shop }
status:
  conditions:
    - { type: Complete, status: "True" }
spec:
  template:
    spec:
      containers:
        - name: migrate
          envFrom:
            - secretRef: { name: ghcr-login }
`

const runningJob = `
apiVersion: batch/v1
kind: Job
metadata: { name: migrate-now, namespace: shop }
status:
  active: 1
spec:
  template:
    spec:
      containers:
        - name: migrate
          envFrom:
            - secretRef: { name: ghcr-login }
`

func TestACompletedJobIsNotAUser(t *testing.T) {
	use, relations, _ := walk(t, resSecrets, "shop", "ghcr-login",
		seeded(t, completedJob), seeded(t, runningJob))
	if use == nil {
		t.Fatal("no Usage")
	}
	find(t, relations, RoleUsedBy, "Job/migrate-now")
	absent(t, relations, RoleUsedBy, "Job/migrate-once")
	if use.Found != 1 {
		t.Errorf("Found = %d, want 1 — a finished Job is a record of a past use", use.Found)
	}
}

// A ConfigMap cannot be named by anything cluster-scoped, by a ServiceAccount or
// by an Ingress, so those four lists are not made for one. Two round trips that
// cannot find anything are two round trips paid on every drawer open.
func TestAConfigMapDoesNotPayForTheSecretOnlyLists(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	if _, _, _ = walkWith(t, dyn, resConfigMaps, "shop", "api-config"); true {
		for _, action := range dyn.Actions() {
			switch action.GetResource().Resource {
			case resPVs, resStorageClasses, resServiceAccounts, resIngresses:
				t.Errorf("listed %s for a ConfigMap, which nothing of that kind can name",
					action.GetResource().Resource)
			}
		}
	}
}

func TestASecretIsLookedForInTheTwoClusterScopedKinds(t *testing.T) {
	use, relations, _ := walk(t, resSecrets, "ceph", "csi-rbd-node", seeded(t, pvWithCSISecrets))
	if use == nil || use.Found != 1 {
		t.Fatalf("Usage = %+v, want the PersistentVolume found", use)
	}
	find(t, relations, RoleUsedBy, "PersistentVolume/pv-0001")
}

// The list is read to its end rather than capped at a page, because a
// client-side filter over a limited list answers about the first page and says
// nothing about the rest.
func TestTheWalkAsksTheWatchCache(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	_, _, _ = walkWith(t, dyn, resConfigMaps, "shop", "api-config")
	for _, action := range dyn.Actions() {
		list, ok := action.(k8stesting.ListActionImpl)
		if !ok {
			continue
		}
		_ = list
	}
	if len(dyn.Actions()) == 0 {
		t.Fatal("no list was made at all")
	}
}

var _ = metav1.ListOptions{}
