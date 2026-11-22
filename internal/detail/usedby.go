// The reverse walk: who uses this ConfigMap or Secret, who binds this Role or
// ClusterRole, who runs as this ServiceAccount.
//
// Nothing on the object answers that, and the API has no field selector for
// it, so whole lists are read (from the watch cache, paged) and filtered here;
// `Limit` cannot cap a client-side filter. The cost is paid per drawer open.
//
// Workload templates are read rather than pods: fewer bytes, and the workload
// is what an operator changes. Finished Jobs are skipped, since history makes
// everything look used; ReplicaSets are not listed, a live one being its
// Deployment's and an old one history.
//
// A ConfigMap picked up by label (dashboard sidecars, rule discovery) is
// reachable by no walk, and "nothing found" is the common answer. Hence Usage
// carries a state of its own: found nothing, could not list, and capped are
// different answers.
package detail

import (
	"context"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/resources"
)

// maxUsedBy caps the referrers drawn. Separate from maxRelations because the
// walk knows the true total and can say exactly how many it left out.
const maxUsedBy = 25

// maxUsageWays caps how many ways one referrer's row says it uses the object.
const maxUsageWays = 4

// usageTarget is the object the walk is looking for referrers of.
type usageTarget struct {
	// resource is one of the five reverseWalked kinds.
	resource string
	// kind is the Kind as a person writes it ("ConfigMap"), as container.go's
	// extractors spell it.
	kind      string
	namespace string
	name      string
}

// targetKinds is the closed list of kinds the walk points backwards from, and
// what each is called: ConfigMaps, Secrets and the three of access control.
var targetKinds = map[string]string{
	resConfigMaps:      "ConfigMap",
	resSecrets:         "Secret",
	resRoles:           "Role",
	resClusterRoles:    "ClusterRole",
	resServiceAccounts: "ServiceAccount",
}

// reverseWalked reports whether opening this kind runs the walk.
func reverseWalked(resource string) bool {
	_, ok := targetKinds[resource]
	return ok
}

func targetOf(resource, namespace, name string) usageTarget {
	return usageTarget{resource: resource, kind: targetKinds[resource], namespace: namespace, name: name}
}

// Usage is what the reverse walk established, nil on every kind it does not
// run for. Its own member rather than a note, because "walked and found
// nothing" is neither "cut" nor "could not be read", and it is the answer an
// operator deletes an object on.
type Usage struct {
	// Subject is the target's Kind ("ConfigMap", "Role", …), for the sentence.
	Subject string
	// Found is how many referrers were drawn.
	Found int
	// Total is how many were found before the cap.
	Total int
	// Looked names every list that answered, in the panel's words: the
	// evidence behind "nothing uses this".
	Looked []string
	// Refused names every list that did not answer. A zero Found with anything
	// here is a failure, never an empty answer.
	Refused []string
	// Narrowed names every list that answered only for the namespaces the
	// person may list, so "nothing uses this" cannot be said either.
	Narrowed []string
	// Err is the first refusal, verbatim, for internal/web to classify like any
	// other cluster read.
	Err error
}

// usageSource is one list the walk makes.
type usageSource struct {
	gvr schema.GroupVersionResource
	// resource is the plural name usesTarget switches on, the same string
	// readRelations switches on.
	resource string
	// clusterScoped kinds are listed across the cluster.
	clusterScoped bool
	// canName is which target kinds this list can hold a reference to; it is
	// never listed for any other, since it could find nothing.
	canName []string
	// everyNamespaceFor names the targets this list is read across every
	// namespace for: a RoleBinding may bind a ClusterRole, or name a
	// ServiceAccount subject, from any namespace.
	everyNamespaceFor []string
	// label is how the panel names it.
	label string
}

func (s usageSource) names(target string) bool {
	for _, kind := range s.canName {
		if kind == target {
			return true
		}
	}
	return false
}

func (s usageSource) everyNamespace(target string) bool {
	for _, kind := range s.everyNamespaceFor {
		if kind == target {
			return true
		}
	}
	return s.clusterScoped
}

// What each list can name. usageSources is in the order lists are made and
// named: workloads first, as what an operator changes; cluster-scoped lists
// after, as the most likely to be refused to a namespace-scoped identity.
var (
	configOrSecret = []string{resConfigMaps, resSecrets}
	// A workload template names a ConfigMap, a Secret, and the ServiceAccount
	// it runs as.
	podSpecNames = []string{resConfigMaps, resSecrets, resServiceAccounts}
	secretOnly   = []string{resSecrets}
	// A binding names its role and its ServiceAccount subjects.
	bindingNames = []string{resRoles, resClusterRoles, resServiceAccounts}
)

var usageSources = []usageSource{
	{gvr: gvr("apps", "v1", "deployments"), resource: "deployments", canName: podSpecNames, label: "Deployments"},
	{gvr: gvr("apps", "v1", "statefulsets"), resource: "statefulsets", canName: podSpecNames, label: "StatefulSets"},
	{gvr: gvr("apps", "v1", "daemonsets"), resource: "daemonsets", canName: podSpecNames, label: "DaemonSets"},
	{gvr: gvr("batch", "v1", "cronjobs"), resource: "cronjobs", canName: podSpecNames, label: "CronJobs"},
	{gvr: gvr("batch", "v1", "jobs"), resource: "jobs", canName: podSpecNames, label: "Jobs that have not finished"},
	{gvr: gvr("", "v1", resServiceAccounts), resource: resServiceAccounts,
		canName: secretOnly, label: "ServiceAccounts"},
	{gvr: gvr("networking.k8s.io", "v1", resIngresses), resource: resIngresses,
		canName: secretOnly, label: "Ingresses"},
	{gvr: gvr("", "v1", resPVs), resource: resPVs,
		clusterScoped: true, canName: secretOnly, label: "PersistentVolumes"},
	{gvr: gvr("storage.k8s.io", "v1", resStorageClasses), resource: resStorageClasses,
		clusterScoped: true, canName: secretOnly, label: "StorageClasses"},
	// A Role is bound only from its own namespace; a ClusterRole or a
	// ServiceAccount can be named from any.
	{gvr: gvr("rbac.authorization.k8s.io", "v1", resRoleBindings), resource: resRoleBindings,
		canName: bindingNames, everyNamespaceFor: []string{resClusterRoles, resServiceAccounts},
		label: "RoleBindings"},
	{gvr: gvr("rbac.authorization.k8s.io", "v1", resClusterRoleBindings), resource: resClusterRoleBindings,
		clusterScoped: true, canName: []string{resClusterRoles, resServiceAccounts},
		label: "ClusterRoleBindings"},
}

func gvr(group, version, resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
}

// addUsedBy reads every list that can name the target and adds a row per
// referrer. It never fails the panel: a refused list is recorded and named,
// and the rows from the lists that answered are still drawn.
func (r *Reader) addUsedBy(
	ctx context.Context,
	client *cluster.Client,
	set *relationSet,
	raw *unstructured.Unstructured,
	resource string,
) *Usage {
	target := targetOf(resource, raw.GetNamespace(), raw.GetName())
	use := &Usage{Subject: target.kind}
	if client == nil || client.Dynamic == nil {
		// No client to ask with. Not an empty answer — nothing was asked.
		use.Refused = append(use.Refused, "this cluster")
		use.Err = fmt.Errorf("no client for %s", raw.GetNamespace())
		return use
	}

	var found []Relation
	for _, source := range usageSources {
		if !source.names(target.resource) {
			continue
		}
		namespace := target.namespace
		if source.everyNamespace(target.resource) {
			namespace = ""
		}
		items, visible, err := r.listAll(ctx, client, source.gvr, namespace)
		if err != nil {
			use.Refused = append(use.Refused, source.label)
			if use.Err == nil {
				use.Err = err
			}
			continue
		}
		use.Looked = append(use.Looked, source.label)
		if visible.Filtered() {
			use.Narrowed = append(use.Narrowed, source.label)
		}
		for i := range items {
			item := &items[i]
			if !liveWorkload(item, source.resource) {
				continue
			}
			if rel := usesTarget(item, source.resource, target); rel != nil {
				found = append(found, *rel)
			}
		}
	}

	// A stable order: by kind, then by name.
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].Kind != found[j].Kind {
			return found[i].Kind < found[j].Kind
		}
		return found[i].Name < found[j].Name
	})

	// The refusal is said once, and only where Usage's state cannot say it:
	// when something was found, the state is ok and this note is what says
	// the list may be short.
	if len(found) > 0 && len(use.Refused) > 0 {
		set.note(andList(use.Refused) + " could not be listed, so this list may be short.")
	}

	use.Total = len(found)
	if len(found) > maxUsedBy {
		set.note(fmt.Sprintf("%d more objects use this %s and are not shown.",
			len(found)-maxUsedBy, strings.ToLower(target.kind)))
		found = found[:maxUsedBy]
	}
	use.Found = len(found)
	for _, rel := range found {
		set.add(rel)
	}
	return use
}

// listAll reads one kind to the end, paged and out of the watch cache, as a
// list reviewed for the person behind ctx: the one namespace asked, or every
// namespace narrowed to those they may list.
func (r *Reader) listAll(
	ctx context.Context,
	client *cluster.Client,
	resource schema.GroupVersionResource,
	namespace string,
) ([]unstructured.Unstructured, access.Visible, error) {
	question := access.Attributes{
		Verb: "list", Group: resource.Group, Version: resource.Version, Resource: resource.Resource, Namespace: namespace,
	}
	return access.List(ctx, r.gate, client.Context, question,
		func(item *unstructured.Unstructured) string { return item.GetNamespace() },
		func(ctx context.Context) ([]unstructured.Unstructured, error) {
			var iface dynamic.ResourceInterface = client.Dynamic.Resource(resource)
			if namespace != "" {
				iface = client.Dynamic.Resource(resource).Namespace(namespace)
			}
			return resources.Paged(ctx, resources.Cached(),
				func(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
					return iface.List(ctx, opts)
				},
				func(l *unstructured.UnstructuredList) []unstructured.Unstructured { return l.Items })
		})
}

// liveWorkload answers whether this object is a current user or a record of a
// past one. Only Jobs are filtered, on the Complete or Failed condition the
// Job controller writes when done.
func liveWorkload(raw *unstructured.Unstructured, resource string) bool {
	if resource != "jobs" {
		return true
	}
	for _, item := range sliceOf(mapAt(raw.Object, "status")["conditions"]) {
		condition := asMap(item)
		if str(condition["status"]) != "True" {
			continue
		}
		switch str(condition["type"]) {
		case "Complete", "Failed":
			return false
		}
	}
	return true
}

// usesTarget is the matching half, and pure: one object in, at most one row
// out — one row per referrer, never per reference.
func usesTarget(raw *unstructured.Unstructured, resource string, target usageTarget) *Relation {
	ways := &usageWays{}

	switch resource {
	case resPods:
		fromPodSpecUse(ways, mapAt(raw.Object, "spec"), raw.GetNamespace(), raw.GetName(), target)

	case "deployments", "statefulsets", "daemonsets", "replicasets", "replicationcontrollers", "jobs":
		fromPodSpecUse(ways, mapAt(raw.Object, "spec", "template", "spec"), raw.GetNamespace(), "", target)

	case "cronjobs":
		fromPodSpecUse(ways,
			mapAt(raw.Object, "spec", "jobTemplate", "spec", "template", "spec"),
			raw.GetNamespace(), "", target)

	case resServiceAccounts, resIngresses, resPVs, resStorageClasses, resRoleBindings, resClusterRoleBindings:
		// No pod spec: the forward extraction, filtered.
		fromForwardUse(ways, raw, resource, target)

	default:
		return nil
	}

	if len(ways.how) == 0 {
		return nil
	}
	// A binding binds a role rather than using it.
	role := RoleUsedBy
	if resource == resRoleBindings || resource == resClusterRoleBindings {
		role = RoleBoundBy
	}
	return &Relation{
		Role:      role,
		Kind:      resource,
		Namespace: raw.GetNamespace(),
		Name:      raw.GetName(),
		Label:     raw.GetKind() + "/" + raw.GetName(),
		Detail:    ways.detail(),
		Mounts:    ways.mounts,
	}
}

// usageWays collects the distinct ways one referrer uses the object.
type usageWays struct {
	how    []string
	seen   map[string]bool
	mounts []string
}

func (w *usageWays) add(how string, mounts ...string) {
	if how == "" {
		return
	}
	if w.seen == nil {
		w.seen = map[string]bool{}
	}
	if !w.seen[how] {
		w.seen[how] = true
		w.how = append(w.how, how)
	}
	w.mounts = mergeMounts(w.mounts, mounts)
}

func (w *usageWays) detail() string {
	how := w.how
	if len(how) > maxUsageWays {
		extra := len(how) - maxUsageWays
		how = append(append([]string(nil), how[:maxUsageWays]...),
			fmt.Sprintf("+%d more", extra))
	}
	return strings.Join(how, "; ")
}

// fromPodSpecUse reads one pod spec or pod template spec (the same shape).
// Volumes and the like go through relations.go's fromPodSpec into a throwaway
// set, filtered, so every shape it knows is covered by one extractor. The env
// references are not relations, so they are read here through container.go's
// wholeOf and sourceOf.
func fromPodSpecUse(ways *usageWays, spec map[string]any, namespace, podName string, target usageTarget) {
	if spec == nil {
		return
	}

	mirror := newRelationSet()
	fromPodSpec(mirror, spec, podScope{namespace: namespace, podName: podName})
	for _, rel := range mirror.items {
		if rel.Kind != target.resource || rel.Namespace != target.namespace || rel.Name != target.name {
			continue
		}
		switch rel.Role {
		case RoleImagePullSecret:
			ways.add("pulls images with it", rel.Mounts...)
		case RoleServiceAccount:
			// The forward row has no detail, so the reverse one says it.
			ways.add("runs as it")
		default:
			ways.add(rel.Detail, rel.Mounts...)
		}
	}
	// A template naming no ServiceAccount runs as `default`: admission fills it
	// in on the Pods, never on the template. Without this, `default` reads as
	// used by nothing and invites deleting the account the workload runs as.
	// Reverse walk only: the workload's own panel keeps what the template says.
	if podName == "" && target.resource == resServiceAccounts &&
		target.namespace == namespace && target.name == "default" &&
		serviceAccountOf(spec) == "" {
		ways.add("runs as it, by default (no serviceAccountName)")
	}

	for _, key := range []string{"containers", "initContainers", "ephemeralContainers"} {
		for _, item := range sliceOf(spec[key]) {
			container := asMap(item)
			if container == nil {
				continue
			}
			name := str(container["name"])
			for _, whole := range wholeOf(container) {
				if whole.Kind != target.kind || whole.Name != target.name {
					continue
				}
				ways.add(importedBy(name, whole))
			}
			for _, entry := range sliceOf(container["env"]) {
				_, source := sourceOf(asMap(entry))
				if source == nil || source.Kind != target.kind || source.Name != target.name {
					continue
				}
				ways.add(readBy(name, source))
			}
		}
	}
}

// importedBy says what an `envFrom` import does: the container and the prefix.
// Whether the reference is optional is not said.
func importedBy(container string, whole Whole) string {
	text := "every key imported into " + container
	if whole.Prefix != "" {
		text += " as " + whole.Prefix + "*"
	}
	return text
}

func readBy(container string, source *EnvSource) string {
	text := "key " + source.Key + " read by " + container
	return text
}

// fromForwardUse keeps the rows readRelations extracts forward that point at
// the target, for the kinds with no pod spec (ServiceAccounts, Ingresses,
// PersistentVolumes, StorageClasses and the bindings).
func fromForwardUse(ways *usageWays, raw *unstructured.Unstructured, resource string, target usageTarget) {
	forward, _, _ := readRelations(raw, resource, func(string, string) (string, bool, bool) {
		// No mapper needed: only owner references resolve a kind at runtime.
		return "", false, false
	})
	for _, rel := range forward.items {
		if rel.Kind != target.resource || rel.Namespace != target.namespace || rel.Name != target.name {
			continue
		}
		ways.add(reverseDetail(raw, rel))
	}
}

// reverseDetail words a forward row for the panel of the object it points at.
// A binding's forward wording leans on its own panel ("the role above"), which
// the reversed row does not have, so it is reworded; every other source's
// detail reads the same both ways.
func reverseDetail(raw *unstructured.Unstructured, rel Relation) string {
	switch {
	case rel.Role == RoleGrants:
		return "grants its rules"
	case rel.Role == RoleSubject && rel.Kind == resServiceAccounts:
		ref := asMap(raw.Object["roleRef"])
		return "grants it " + str(ref["kind"]) + " " + str(ref["name"])
	}
	return rel.Detail
}

// andList is "a, b and c".
func andList(items []string) string {
	switch len(items) {
	case 0:
		return "nothing"
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
