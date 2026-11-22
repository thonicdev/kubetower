// Relations, ports and addresses: every name in an object that points at
// another object, drawn as a link.
//
// Generic first: owners, node and service account are read off any object, so
// an unknown CRD still gets its ownership chain; the per-kind switch only adds.
// The target's scope decides its namespace: a relation to a cluster-scoped
// kind carries none.
//
// Nothing here writes. Beyond the object given, it lists pods for a Service
// and quotas and limit ranges for a Namespace (namespace.go).
package detail

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// The roles, as the panel labels them.
const (
	RoleControlledBy     = "Controlled by"
	RoleOwnedBy          = "Owned by"
	RoleNode             = "Node"
	RoleServiceAccount   = "Service account"
	RoleSelects          = "Selects"
	RolePod              = "Pod"
	RoleService          = "Service"
	RoleBackend          = "Backend"
	RoleIngressClass     = "Ingress class"
	RoleTLSCertificate   = "TLS certificate"
	RoleConfig           = "Config"
	RoleSecret           = "Secret"
	RoleImagePullSecret  = "Image pull secret"
	RoleVolumeClaim      = "Volume claim"
	RolePersistentVolume = "Persistent volume"
	RoleStorageClass     = "Storage class"
	RolePriorityClass    = "Priority class"
	RoleDataSource       = "Created from"
	RoleVolume           = "Volume"
	// RoleUsedBy points the other way: the objects that name this one, found
	// by the reverse walk (usedby.go).
	RoleUsedBy = "Used by"
	// The access-control roles. Grants and Subject are read off a binding;
	// Bound by is reverse, the bindings that name a Role, a ClusterRole or a
	// ServiceAccount (usedby.go).
	RoleGrants  = "Grants"
	RoleSubject = "Subject"
	RoleBoundBy = "Bound by"
	// What bounds a namespace, read by a list in it (namespace.go).
	RoleResourceQuota = "Resource quota"
	RoleLimitRange    = "Limit range"
)

// roleOrder is the reading order of the panel: why this object exists (then
// who uses it), where it runs, who it is, what it reaches, what configures it,
// and storage last, since the cap takes from the end.
var roleOrder = []string{
	RoleControlledBy, RoleOwnedBy, RoleUsedBy, RoleBoundBy,
	RoleGrants, RoleSubject,
	RoleResourceQuota, RoleLimitRange,
	RoleNode, RoleServiceAccount, RolePriorityClass,
	RoleSelects, RolePod, RoleService, RoleBackend, RoleIngressClass, RoleTLSCertificate,
	RoleConfig, RoleSecret, RoleImagePullSecret,
	RoleVolumeClaim, RolePersistentVolume, RoleStorageClass, RoleDataSource,
	RoleVolume,
}

var roleRanks = func() map[string]int {
	m := make(map[string]int, len(roleOrder))
	for i, role := range roleOrder {
		m[role] = i
	}
	return m
}()

func roleRank(role string) int {
	if r, ok := roleRanks[role]; ok {
		return r
	}
	return len(roleOrder)
}

// The plural resource names of the kinds this file points at by hand, where
// the target type is known statically. The mapper is used only where the kind
// is unknown until runtime: an owner reference.
const (
	resPods            = "pods"
	resNodes           = "nodes"
	resServiceAccounts = "serviceaccounts"
	resConfigMaps      = "configmaps"
	resSecrets         = "secrets"
	resPVCs            = "persistentvolumeclaims"
	resPVs             = "persistentvolumes"
	resStorageClasses  = "storageclasses"
	resServices        = "services"
	resIngresses       = "ingresses"
	resIngressClasses  = "ingressclasses"
	resPriorityClasses = "priorityclasses"
	// Access control.
	resRoles               = "roles"
	resClusterRoles        = "clusterroles"
	resRoleBindings        = "rolebindings"
	resClusterRoleBindings = "clusterrolebindings"
	// A Namespace's page.
	resNamespaces     = "namespaces"
	resResourceQuotas = "resourcequotas"
	resLimitRanges    = "limitranges"
)

// The caps on a drawer panel. Everything cut is counted and said.
const (
	maxRelations    = 40
	maxSelectedPods = 20
	maxMounts       = 6
	maxPorts        = 40
	maxURLs         = 40
)

// kindResolver turns an API group and kind into the plural resource name a URL
// carries, and says whether that kind is namespaced. ok is false for a kind
// this cluster does not serve: the relation is then listed but not linked.
type kindResolver func(group, kind string) (resource string, namespaced bool, ok bool)

func resolverFor(mapper meta.RESTMapper) kindResolver {
	return func(group, kind string) (string, bool, bool) {
		if kind == "" {
			return "", false, false
		}
		mapping, err := mapper.RESTMapping(schema.GroupKind{Group: group, Kind: kind})
		if err != nil {
			return "", false, false
		}
		return mapping.Resource.Resource, mapping.Scope.Name() == meta.RESTScopeNameNamespace, true
	}
}

// relationSet collects relations, folds duplicates (a configMap mounted in
// three containers is one row) and remembers what it had to leave out.
type relationSet struct {
	items []Relation
	index map[string]int
	notes []string
}

func newRelationSet() *relationSet {
	return &relationSet{index: map[string]int{}}
}

func (s *relationSet) note(text string) {
	s.notes = append(s.notes, text)
}

func (s *relationSet) add(rel Relation) {
	if rel.Label == "" {
		rel.Label = rel.Name
	}
	if rel.Label == "" && rel.Selector == "" {
		return
	}
	key := strings.Join([]string{rel.Role, rel.Kind, rel.Namespace, rel.Name, rel.Label, rel.Selector}, "\x00")
	if at, ok := s.index[key]; ok {
		existing := &s.items[at]
		existing.Mounts = mergeMounts(existing.Mounts, rel.Mounts)
		if existing.Detail == "" {
			existing.Detail = rel.Detail
		}
		return
	}
	rel.Mounts = mergeMounts(nil, rel.Mounts)
	s.index[key] = len(s.items)
	s.items = append(s.items, rel)
}

func mergeMounts(into, from []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(into)+len(from))
	for _, group := range [][]string{into, from} {
		for _, path := range group {
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			out = append(out, path)
		}
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// finish orders the panel by role, then applies the cap, so the cut falls on
// the storage end and never on "Controlled by".
func (s *relationSet) finish() ([]Relation, string) {
	items := s.items
	sort.SliceStable(items, func(i, j int) bool { return roleRank(items[i].Role) < roleRank(items[j].Role) })

	notes := s.notes
	if len(items) > maxRelations {
		notes = append(notes, fmt.Sprintf("%d more related objects are not shown.", len(items)-maxRelations))
		items = items[:maxRelations]
	}
	for i := range items {
		items[i].Mounts = capMounts(items[i].Mounts)
	}
	if len(items) == 0 {
		items = nil
	}
	return items, strings.Join(notes, " ")
}

func capMounts(paths []string) []string {
	if len(paths) <= maxMounts {
		return paths
	}
	out := append([]string(nil), paths[:maxMounts]...)
	return append(out, fmt.Sprintf("+%d more", len(paths)-maxMounts))
}

// enrich fills Relations, Ports, URLs, Usage and the two Service fields. A
// failed extra list is a note on the panel, never a failure of it.
func (r *Reader) enrich(ctx context.Context, client *cluster.Client, object *Object, raw *unstructured.Unstructured, resolve kindResolver) {
	set, ports, urls := readRelations(raw, object.Kind, resolve)

	if object.Kind == resServices {
		object.ServiceType = nestedString(raw.Object, "spec", "type")
		object.ClusterIP = nestedString(raw.Object, "spec", "clusterIP")
		r.addSelectedPods(ctx, client, set, raw)
	}

	// The reverse walk, which costs list calls; see usedby.go.
	if reverseWalked(object.Kind) {
		object.Usage = r.addUsedBy(ctx, client, set, raw, object.Kind)
	}

	// A namespace names nothing that bounds it; see namespace.go.
	if object.Kind == resNamespaces && raw.GetAPIVersion() == "v1" {
		r.addNamespaceBounds(ctx, client, set, raw.GetName())
	}

	object.Relations, object.RelationsNote = set.finish()

	if len(ports) > maxPorts {
		object.PortsNote = fmt.Sprintf("%d more ports are not shown.", len(ports)-maxPorts)
		ports = ports[:maxPorts]
	}
	object.Ports = ports

	if len(urls) > maxURLs {
		object.URLsNote = fmt.Sprintf("%d more addresses are not shown.", len(urls)-maxURLs)
		urls = urls[:maxURLs]
	}
	object.URLs = urls
}

// readRelations is the whole extraction, with no network: the generic pass,
// then the switch. Pure, so it can be tested against a manifest.
func readRelations(raw *unstructured.Unstructured, resource string, resolve kindResolver) (*relationSet, []Port, []URL) {
	set := newRelationSet()
	var ports []Port
	var urls []URL

	genericRelations(set, raw, resolve)

	obj := raw.Object
	ns := raw.GetNamespace()

	switch resource {
	case resPods:
		spec := mapAt(obj, "spec")
		fromPodSpec(set, spec, podScope{namespace: ns, podName: raw.GetName()})
		ports = portsFromPodSpec(spec)

	case "deployments", "statefulsets", "daemonsets", "replicasets", "replicationcontrollers", "jobs":
		template := mapAt(obj, "spec", "template", "spec")
		claims := claimTemplateNames(obj)
		fromPodSpec(set, template, podScope{namespace: ns, perReplicaClaims: claims})
		workloadSelector(set, mapAt(obj, "spec", "selector"), ns)
		fromVolumeClaimTemplates(set, obj, raw.GetName(), mountPaths(template))

	case "cronjobs":
		fromPodSpec(set, mapAt(obj, "spec", "jobTemplate", "spec", "template", "spec"), podScope{namespace: ns})

	case resServices:
		serviceRelations(set, obj, ns)
		ports = portsFromService(obj)

	case resIngresses:
		urls = ingressRelations(set, raw, ns, resolve)

	case resPVCs:
		claimRelations(set, obj, resolve)

	case resPVs:
		volumeRelations(set, obj)

	case resServiceAccounts:
		serviceAccountRelations(set, obj, ns)

	case resStorageClasses:
		storageClassRelations(set, obj)

	case resRoleBindings:
		bindingRelations(set, obj, ns)

	case resClusterRoleBindings:
		bindingRelations(set, obj, "")
	}

	return set, ports, urls
}

// bindingRelations is a binding's two halves: what it grants and to whom.
// `roleRef` is a Role in the binding's own namespace or a ClusterRole. A
// ServiceAccount subject is in the namespace it names, else the binding's; a
// User or a Group has no object, so it is listed inert.
func bindingRelations(set *relationSet, obj map[string]any, ns string) {
	ref := asMap(obj["roleRef"])
	if name := str(ref["name"]); name != "" {
		kind, where := resRoles, ns
		if str(ref["kind"]) == "ClusterRole" {
			kind, where = resClusterRoles, ""
		}
		set.add(Relation{
			Role: RoleGrants, Kind: kind, Namespace: where, Name: name,
			Detail: "the rules this binding grants",
		})
	}

	for _, item := range sliceOf(obj["subjects"]) {
		subject := asMap(item)
		name := str(subject["name"])
		if name == "" {
			continue
		}
		switch str(subject["kind"]) {
		case "ServiceAccount":
			where := str(subject["namespace"])
			if where == "" {
				where = ns
			}
			set.add(Relation{
				Role: RoleSubject, Kind: resServiceAccounts, Namespace: where, Name: name,
				Detail: "granted the role above",
			})
		case "User", "Group":
			set.add(Relation{
				Role: RoleSubject, Label: strings.ToLower(str(subject["kind"])) + " " + name,
				Detail: "granted the role above",
			})
		}
	}
}

// genericRelations is everything readable from any object: owners, node and
// service account, for every kind, CRDs included.
func genericRelations(set *relationSet, raw *unstructured.Unstructured, resolve kindResolver) {
	obj := raw.Object
	ns := raw.GetNamespace()

	for _, owner := range raw.GetOwnerReferences() {
		role := RoleOwnedBy
		if owner.Controller != nil && *owner.Controller {
			role = RoleControlledBy
		}
		rel := Relation{Role: role, Name: owner.Name, Label: owner.Kind + "/" + owner.Name}
		group := ""
		if gv, err := schema.ParseGroupVersion(owner.APIVersion); err == nil {
			group = gv.Group
		}
		if res, namespaced, ok := resolve(group, owner.Kind); ok {
			rel.Kind = res
			// An owner is in this object's namespace or cluster-scoped; its
			// own scope decides.
			if namespaced {
				rel.Namespace = ns
			}
		} else {
			rel.Detail = "this cluster does not serve " + owner.APIVersion
		}
		set.add(rel)
	}

	// Cluster-scoped: no namespace, deliberately.
	if node := nestedString(obj, "spec", "nodeName"); node != "" {
		set.add(Relation{Role: RoleNode, Kind: resNodes, Name: node})
	}
	if sa := serviceAccountOf(mapAt(obj, "spec")); sa != "" && ns != "" {
		set.add(Relation{Role: RoleServiceAccount, Kind: resServiceAccounts, Namespace: ns, Name: sa})
	}
}

// serviceAccountOf reads both spellings: a hand-written object may carry only
// the deprecated `serviceAccount`.
func serviceAccountOf(spec map[string]any) string {
	if spec == nil {
		return ""
	}
	if name := str(spec["serviceAccountName"]); name != "" {
		return name
	}
	return str(spec["serviceAccount"])
}

// podScope is what a pod spec needs from outside itself to be read.
type podScope struct {
	namespace string
	// podName is the pod's own name, empty for a template. Needed only for a
	// generic ephemeral volume, whose claim the kubelet names after the pod.
	podName string
	// perReplicaClaims are the volume names a StatefulSet overwrites with the
	// claims it generates. See fromVolume.
	perReplicaClaims map[string]bool
}

// fromPodSpec reads everything a pod spec or pod template spec says about
// other objects: volumes with their mount paths, the service account, the
// image pull secrets and the priority class.
func fromPodSpec(set *relationSet, spec map[string]any, scope podScope) {
	if spec == nil {
		return
	}
	ns := scope.namespace

	if sa := serviceAccountOf(spec); sa != "" && ns != "" {
		set.add(Relation{Role: RoleServiceAccount, Kind: resServiceAccounts, Namespace: ns, Name: sa})
	}
	for _, item := range sliceOf(spec["imagePullSecrets"]) {
		if name := str(asMap(item)["name"]); name != "" {
			set.add(Relation{Role: RoleImagePullSecret, Kind: resSecrets, Namespace: ns, Name: name})
		}
	}
	if pc := str(spec["priorityClassName"]); pc != "" {
		// Cluster-scoped.
		set.add(Relation{Role: RolePriorityClass, Kind: resPriorityClasses, Name: pc})
	}

	mounts := mountPaths(spec)
	for _, item := range sliceOf(spec["volumes"]) {
		volume := asMap(item)
		if volume == nil {
			continue
		}
		name := str(volume["name"])
		fromVolume(set, volume, name, scope, mounts[name])
	}
}

// claimTemplateNames is the set of volume names a StatefulSet generates claims
// for; empty for every other kind.
func claimTemplateNames(obj map[string]any) map[string]bool {
	var out map[string]bool
	for _, item := range sliceOf(mapAt(obj, "spec")["volumeClaimTemplates"]) {
		name := str(mapAt(asMap(item), "metadata")["name"])
		if name == "" {
			continue
		}
		if out == nil {
			out = map[string]bool{}
		}
		out[name] = true
	}
	return out
}

// mountPaths maps a volume name to the paths it is mounted at, across every
// container (ordinary, init and ephemeral): the path lives on the container,
// not the volume.
func mountPaths(spec map[string]any) map[string][]string {
	out := map[string][]string{}
	for _, key := range []string{"containers", "initContainers", "ephemeralContainers"} {
		for _, item := range sliceOf(spec[key]) {
			container := asMap(item)
			if container == nil {
				continue
			}
			for _, m := range sliceOf(container["volumeMounts"]) {
				mount := asMap(m)
				name, path := str(mount["name"]), str(mount["mountPath"])
				if name == "" || path == "" {
					continue
				}
				out[name] = append(out[name], path)
			}
		}
	}
	return out
}

// fromVolume turns one volume into what the panel says about it. Every volume
// is listed, even one with no object behind it (emptyDir, hostPath).
func fromVolume(set *relationSet, volume map[string]any, name string, scope podScope, paths []string) {
	ns, podName := scope.namespace, scope.podName
	kind, body := volumeSourceOf(volume)
	switch kind {
	case "":
		return

	case "configMap":
		set.add(Relation{
			Role: RoleConfig, Kind: resConfigMaps, Namespace: ns, Name: str(body["name"]),
			Detail: volumeDetail(name, body), Mounts: paths,
		})

	case "secret":
		set.add(Relation{
			Role: RoleSecret, Kind: resSecrets, Namespace: ns, Name: str(body["secretName"]),
			Detail: volumeDetail(name, body), Mounts: paths,
		})

	case "persistentVolumeClaim":
		// A volume named like a StatefulSet's volumeClaimTemplate is replaced
		// by the per-replica claim, so its claimName (often a placeholder) is
		// never used; fromVolumeClaimTemplates adds the true one.
		if scope.perReplicaClaims[name] {
			return
		}
		set.add(Relation{
			Role: RoleVolumeClaim, Kind: resPVCs, Namespace: ns, Name: str(body["claimName"]),
			Detail: volumeDetail(name, body), Mounts: paths,
		})

	case "projected":
		// The volume itself (what is mounted), and each configMap and secret
		// nested in it (what an operator opens).
		set.add(Relation{
			Role: RoleVolume, Label: name + " (projected)",
			Detail: projectedDetail(body), Mounts: paths,
		})
		for _, item := range sliceOf(body["sources"]) {
			source := asMap(item)
			if cm := asMap(source["configMap"]); cm != nil {
				set.add(Relation{
					Role: RoleConfig, Kind: resConfigMaps, Namespace: ns, Name: str(cm["name"]),
					Detail: "projected into " + name, Mounts: paths,
				})
			}
			if secret := asMap(source["secret"]); secret != nil {
				set.add(Relation{
					Role: RoleSecret, Kind: resSecrets, Namespace: ns, Name: str(secret["name"]),
					Detail: "projected into " + name, Mounts: paths,
				})
			}
		}

	case "ephemeral":
		// The claim is named <pod>-<volume>; a template has no pod name yet.
		if podName != "" {
			set.add(Relation{
				Role: RoleVolumeClaim, Kind: resPVCs, Namespace: ns, Name: podName + "-" + name,
				Detail: "created for this pod", Mounts: paths,
			})
			return
		}
		set.add(Relation{Role: RoleVolume, Label: name + " (ephemeral)", Detail: "one claim per pod", Mounts: paths})

	case "emptyDir":
		detail := "emptyDir"
		if medium := str(body["medium"]); medium != "" {
			detail += " on " + medium
		}
		if limit := str(body["sizeLimit"]); limit != "" {
			detail += ", at most " + limit
		}
		set.add(Relation{Role: RoleVolume, Label: name + " (emptyDir)", Detail: detail, Mounts: paths})

	case "hostPath":
		detail := "the host's " + str(body["path"])
		if t := str(body["type"]); t != "" {
			detail += " (" + t + ")"
		}
		set.add(Relation{Role: RoleVolume, Label: name + " (hostPath)", Detail: detail, Mounts: paths})

	case "csi":
		detail := str(body["driver"])
		set.add(Relation{Role: RoleVolume, Label: name + " (csi)", Detail: detail, Mounts: paths})
		if ref := asMap(body["nodePublishSecretRef"]); ref != nil {
			set.add(Relation{
				Role: RoleSecret, Kind: resSecrets, Namespace: ns, Name: str(ref["name"]),
				Detail: "publishes " + name,
			})
		}

	default:
		// Any other source is named and listed, never dropped.
		set.add(Relation{Role: RoleVolume, Label: name + " (" + kind + ")", Mounts: paths})
		// And the Secret it keeps credentials in (several in-tree drivers carry
		// a `secretRef` or `secretName`), found by shape so an unlisted driver
		// is covered too.
		for _, ref := range secretRefsIn(body) {
			at := ns
			if ref.namespace != "" {
				at = ref.namespace
			}
			set.add(Relation{
				Role: RoleSecret, Kind: resSecrets, Namespace: at, Name: ref.name,
				Detail: kind + "." + ref.field + " of volume " + name,
			})
		}
	}
}

// secretRef is one `secretRef`-shaped field found by shape rather than by name.
type secretRef struct {
	// field is the key it was found under, so the panel can say which of a
	// volume's several credentials this is.
	field     string
	name      string
	namespace string
}

// secretRefsIn finds every Secret a volume source names, by the API's naming
// convention rather than a list of fields: a `…SecretRef` or `secretRef`
// object with a `name`, or the `secretName`/`secretNamespace` pair.
//
// The namespace is returned only where the field carries one: a pod volume's
// reference resolves in the pod's namespace, while a PersistentVolume's must
// name one.
func secretRefsIn(body map[string]any) []secretRef {
	var out []secretRef
	for key, value := range body {
		switch {
		case strings.HasSuffix(key, "SecretRef"):
			ref := asMap(value)
			if ref == nil {
				continue
			}
			if name := str(ref["name"]); name != "" {
				out = append(out, secretRef{field: key, name: name, namespace: str(ref["namespace"])})
			}
		case key == "secretRef":
			ref := asMap(value)
			if ref == nil {
				continue
			}
			if name := str(ref["name"]); name != "" {
				out = append(out, secretRef{field: key, name: name, namespace: str(ref["namespace"])})
			}
		case key == "secretName":
			if name := str(value); name != "" {
				out = append(out, secretRef{field: key, name: name, namespace: str(body["secretNamespace"])})
			}
		}
	}
	// A stable order across redraws.
	sort.Slice(out, func(i, j int) bool { return out[i].field < out[j].field })
	return out
}

// volumeSourceOf returns the one key of a volume that is not its name — the
// source type — and its body. The API validates that there is exactly one.
func volumeSourceOf(volume map[string]any) (string, map[string]any) {
	for key, value := range volume {
		if key == "name" {
			continue
		}
		body := asMap(value)
		if body == nil {
			body = map[string]any{}
		}
		return key, body
	}
	return "", nil
}

func volumeDetail(name string, body map[string]any) string {
	detail := "volume " + name
	if optional, ok := body["optional"].(bool); ok && optional {
		detail += ", optional"
	}
	if readOnly, ok := body["readOnly"].(bool); ok && readOnly {
		detail += ", read-only"
	}
	return detail
}

// projectedDetail names a projected volume's sources that are not objects
// (a service account token, the downward API): nothing to link to.
func projectedDetail(body map[string]any) string {
	var kinds []string
	seen := map[string]bool{}
	for _, item := range sliceOf(body["sources"]) {
		for key := range asMap(item) {
			switch key {
			case "configMap", "secret":
				continue // listed as their own relation
			case "serviceAccountToken":
				key = "a service account token"
			case "downwardAPI":
				key = "the downward API"
			case "clusterTrustBundle":
				key = "a cluster trust bundle"
			}
			if !seen[key] {
				seen[key] = true
				kinds = append(kinds, key)
			}
		}
	}
	if len(kinds) == 0 {
		return ""
	}
	sort.Strings(kinds)
	return "also carries " + strings.Join(kinds, ", ")
}

// fromVolumeClaimTemplates lists a StatefulSet's per-replica claims, named
// <template>-<set>-<ordinal>: one per replica, so named with the pattern and
// not linked.
func fromVolumeClaimTemplates(set *relationSet, obj map[string]any, name string, mounts map[string][]string) {
	for _, item := range sliceOf(mapAt(obj, "spec")["volumeClaimTemplates"]) {
		template := asMap(item)
		claim := str(mapAt(template, "metadata")["name"])
		if claim == "" {
			continue
		}
		set.add(Relation{
			Role:   RoleVolumeClaim,
			Label:  claim + " (one per replica)",
			Detail: fmt.Sprintf("claims named %s-%s-0, -1, …", claim, name),
			// The claim is mounted under its own name.
			Mounts: mounts[claim],
		})
		if class := nestedString(template, "spec", "storageClassName"); class != "" {
			// Cluster-scoped.
			set.add(Relation{Role: RoleStorageClass, Kind: resStorageClasses, Name: class})
		}
	}
}

// workloadSelector turns a workload's selector into a link to its pods, which
// carry the labels even behind a Deployment's ReplicaSet.
func workloadSelector(set *relationSet, selector map[string]any, ns string) {
	labels := mapAt(selector, "matchLabels")
	expressions := sliceOf(selector["matchExpressions"])
	if len(labels) == 0 {
		if len(expressions) > 0 {
			set.add(Relation{Role: RoleSelects, Label: "an expression selector", Detail: "not expressible as a simple label filter"})
		}
		return
	}
	text := selectorString(labels)
	detail := "the pods this controls"
	if len(expressions) > 0 {
		detail += ", narrowed further by an expression selector"
	}
	// Scoped to the workload's namespace, not the whole cluster.
	set.add(Relation{Role: RoleSelects, Kind: resPods, Namespace: ns, Selector: text, Label: text, Detail: detail})
}

func serviceRelations(set *relationSet, obj map[string]any, ns string) {
	selector := mapAt(obj, "spec", "selector")
	if len(selector) == 0 {
		// Said, because it answers "why does this Service have no pods".
		set.add(Relation{
			Role: RoleSelects, Label: "no selector",
			Detail: "the endpoints are written by hand or by another controller",
		})
		return
	}
	text := selectorString(selector)
	set.add(Relation{
		Role: RoleSelects, Kind: resPods, Namespace: ns, Selector: text, Label: text,
		Detail: "the pods this Service sends traffic to",
	})
}

// addSelectedPods lists the pods the Service selects now. A refusal is a note:
// someone who may read the Service but not list pods still gets the panel.
func (r *Reader) addSelectedPods(ctx context.Context, client *cluster.Client, set *relationSet, raw *unstructured.Unstructured) {
	selector := mapAt(raw.Object, "spec", "selector")
	if len(selector) == 0 {
		return
	}
	var list *corev1.PodList
	question := access.Attributes{Verb: "list", Version: "v1", Resource: "pods", Namespace: raw.GetNamespace()}
	_, err := r.gate.Read(ctx, client.Context, question, func(ctx context.Context) error {
		var err error
		list, err = client.Clients.CoreV1().Pods(raw.GetNamespace()).List(ctx, metav1.ListOptions{
			LabelSelector: selectorString(selector),
			// One more than the cap, to know there are more.
			Limit: maxSelectedPods + 1,
		})
		return err
	})
	if err != nil {
		set.note("The pods this Service selects could not be listed: " + firstLine(err.Error()) + ".")
		return
	}

	items := list.Items
	if len(items) > maxSelectedPods {
		items = items[:maxSelectedPods]
		set.note(fmt.Sprintf("The Service selects more than %d pods; the first %d are listed.", maxSelectedPods, maxSelectedPods))
	}
	for _, pod := range items {
		detail := string(pod.Status.Phase)
		if pod.Spec.NodeName != "" {
			detail += " on " + pod.Spec.NodeName
		}
		set.add(Relation{Role: RolePod, Kind: resPods, Namespace: pod.Namespace, Name: pod.Name, Detail: detail})
	}
}

// claimRelations: a PersistentVolumeClaim and the two cluster-scoped things it
// points at, plus whatever it was cloned or restored from.
func claimRelations(set *relationSet, obj map[string]any, resolve kindResolver) {
	if volume := nestedString(obj, "spec", "volumeName"); volume != "" {
		// Cluster-scoped: not the claim's namespace.
		set.add(Relation{Role: RolePersistentVolume, Kind: resPVs, Name: volume, Detail: "bound to this claim"})
	}
	if class := nestedString(obj, "spec", "storageClassName"); class != "" {
		set.add(Relation{Role: RoleStorageClass, Kind: resStorageClasses, Name: class})
	}
	source := mapAt(obj, "spec", "dataSourceRef")
	if source == nil {
		source = mapAt(obj, "spec", "dataSource")
	}
	if source == nil {
		return
	}
	rel := Relation{
		Role:  RoleDataSource,
		Name:  str(source["name"]),
		Label: str(source["kind"]) + "/" + str(source["name"]),
	}
	if res, namespaced, ok := resolve(str(source["apiGroup"]), str(source["kind"])); ok {
		rel.Kind = res
		if namespaced {
			// dataSourceRef may name another namespace; else the claim's own.
			rel.Namespace = str(source["namespace"])
			if rel.Namespace == "" {
				rel.Namespace = nestedString(obj, "metadata", "namespace")
			}
		}
	}
	set.add(rel)
}

// volumeRelations: a PersistentVolume, seen from the other end.
func volumeRelations(set *relationSet, obj map[string]any) {
	if ref := mapAt(obj, "spec", "claimRef"); ref != nil {
		set.add(Relation{
			Role: RoleVolumeClaim, Kind: resPVCs,
			Namespace: str(ref["namespace"]), Name: str(ref["name"]),
			Detail: "bound to this volume",
		})
	}
	if class := nestedString(obj, "spec", "storageClassName"); class != "" {
		set.add(Relation{Role: RoleStorageClass, Kind: resStorageClasses, Name: class})
	}

	// The credentials the driver mounts with (up to five on a CSI volume). The
	// PV is cluster-scoped, so a reference without a namespace names nothing
	// that can be opened and is skipped.
	for source, value := range mapAt(obj, "spec") {
		body := asMap(value)
		if body == nil {
			continue
		}
		for _, ref := range secretRefsIn(body) {
			if ref.namespace == "" {
				continue
			}
			set.add(Relation{
				Role: RoleSecret, Kind: resSecrets, Namespace: ref.namespace, Name: ref.name,
				Detail: source + "." + ref.field,
			})
		}
	}
}

// serviceAccountRelations: the Secrets a ServiceAccount lists, completing the
// chain from a pod through its ServiceAccount (token Secrets on older
// clusters, and hand-attached pull secrets).
//
// Always the ServiceAccount's own namespace: `secrets[]` may spell another,
// but only same-namespace references are honored.
func serviceAccountRelations(set *relationSet, obj map[string]any, ns string) {
	for _, item := range sliceOf(obj["secrets"]) {
		if name := str(asMap(item)["name"]); name != "" {
			set.add(Relation{
				Role: RoleSecret, Kind: resSecrets, Namespace: ns, Name: name,
				Detail: "listed in this service account",
			})
		}
	}
	for _, item := range sliceOf(obj["imagePullSecrets"]) {
		if name := str(asMap(item)["name"]); name != "" {
			set.add(Relation{
				Role: RoleImagePullSecret, Kind: resSecrets, Namespace: ns, Name: name,
				Detail: "for every pod with this service account",
			})
		}
	}
}

// storageClassRelations: the provisioner credentials a StorageClass names in
// its untyped `parameters`, as `…secret-name`/`…secret-namespace` pairs.
// Either value may be a template (`${pvc.namespace}`) resolved per claim; such
// a reference has no one object behind it, so it is listed and not linked.
func storageClassRelations(set *relationSet, obj map[string]any) {
	params := mapAt(obj, "parameters")
	for key, value := range params {
		prefix, ok := strings.CutSuffix(key, "secret-name")
		if !ok {
			continue
		}
		name := str(value)
		if name == "" {
			continue
		}
		ns := str(params[prefix+"secret-namespace"])
		phase := strings.Trim(strings.TrimPrefix(prefix, "csi.storage.k8s.io/"), "-")
		detail := "the " + strings.ReplaceAll(phase, "-", " ") + " secret"
		if phase == "" {
			detail = "the provisioner's secret"
		}
		if templated(name) || templated(ns) {
			set.add(Relation{
				Role: RoleSecret, Label: name, Detail: detail + ", named per claim",
			})
			continue
		}
		set.add(Relation{Role: RoleSecret, Kind: resSecrets, Namespace: ns, Name: name, Detail: detail})
	}
}

func templated(value string) bool { return strings.Contains(value, "${") }

// ingressRelations reads the routing table: which services the rules reach,
// which secret holds the certificate, and the addresses a person can open.
func ingressRelations(set *relationSet, raw *unstructured.Unstructured, ns string, resolve kindResolver) []URL {
	obj := raw.Object

	if class := nestedString(obj, "spec", "ingressClassName"); class != "" {
		// Cluster-scoped.
		set.add(Relation{Role: RoleIngressClass, Kind: resIngressClasses, Name: class})
	}

	hosts, everyHost := tlsCoverage(obj)
	for _, item := range sliceOf(mapAt(obj, "spec")["tls"]) {
		entry := asMap(item)
		secret := str(entry["secretName"])
		if secret == "" {
			continue
		}
		detail := "the default certificate for this Ingress"
		if names := stringsOf(entry["hosts"]); len(names) > 0 {
			detail = "certificate for " + strings.Join(names, ", ")
		}
		set.add(Relation{Role: RoleTLSCertificate, Kind: resSecrets, Namespace: ns, Name: secret, Detail: detail})
	}

	if backend := mapAt(obj, "spec", "defaultBackend"); backend != nil {
		addBackend(set, backend, ns, "everything no rule matches", resolve)
	}

	address := loadBalancerAddress(obj)
	var urls []URL
	seen := map[string]bool{}

	for _, item := range sliceOf(mapAt(obj, "spec")["rules"]) {
		rule := asMap(item)
		host := str(rule["host"])
		for _, p := range sliceOf(mapAt(rule, "http")["paths"]) {
			path := asMap(p)
			route := str(path["path"])
			if route == "" {
				route = "/"
			}

			service := ""
			if backend := mapAt(path, "backend"); backend != nil {
				where := host + route
				if host == "" {
					where = route + " on any host"
				}
				service = addBackend(set, backend, ns, where, resolve)
			}

			u, ok := ingressURL(host, route, address, hosts, everyHost, service)
			if !ok || seen[u.Address] {
				continue
			}
			seen[u.Address] = true
			urls = append(urls, u)
		}
	}
	return urls
}

// addBackend links what a rule points at — a Service, or a typed object
// through `backend.resource` — and returns the Service name when it is one.
func addBackend(set *relationSet, backend map[string]any, ns, where string, resolve kindResolver) string {
	if service := mapAt(backend, "service"); service != nil {
		name := str(service["name"])
		if name == "" {
			return ""
		}
		detail := "serves " + where
		if port := servicePortOf(service); port != "" {
			detail = "serves " + where + " on port " + port
		}
		set.add(Relation{Role: RoleService, Kind: resServices, Namespace: ns, Name: name, Detail: detail})
		return name
	}
	if resource := mapAt(backend, "resource"); resource != nil {
		rel := Relation{
			Role:   RoleBackend,
			Name:   str(resource["name"]),
			Label:  str(resource["kind"]) + "/" + str(resource["name"]),
			Detail: "serves " + where,
		}
		if res, namespaced, ok := resolve(str(resource["apiGroup"]), str(resource["kind"])); ok {
			rel.Kind = res
			if namespaced {
				rel.Namespace = ns
			}
		}
		set.add(rel)
	}
	return ""
}

func servicePortOf(service map[string]any) string {
	port := mapAt(service, "port")
	if port == nil {
		return ""
	}
	if name := str(port["name"]); name != "" {
		return name
	}
	if number := num(port["number"]); number != 0 {
		return strconv.FormatInt(number, 10)
	}
	return ""
}

// ingressURL assembles one address; its scheme comes from the Ingress's own
// `spec.tls` only (tlsCovers).
func ingressURL(host, route, address string, tlsHosts []string, everyHost bool, service string) (URL, bool) {
	u := URL{Role: "Ingress", Host: host, Path: route, Service: service}

	// A rule with no host answers on the address the controller published;
	// without one there is nothing to open.
	if u.Host == "" {
		if address == "" {
			return URL{}, false
		}
		u.Host = address
		u.Note = "the rule sets no host; this is the address the controller published."
	}

	u.TLS = tlsCovers(tlsHosts, everyHost, u.Host)
	scheme := "http"
	if u.TLS {
		scheme = "https"
	}

	literal, trimmed := literalPath(route)
	if trimmed {
		u.Note = strings.TrimSpace(u.Note + " The path is a pattern (" + route + "); this address stops at its literal part.")
	}
	if strings.HasPrefix(u.Host, "*.") {
		u.Note = strings.TrimSpace(u.Note + " The rule matches any host under " + u.Host + "; this is not a name that resolves.")
	}

	u.Address = scheme + "://" + u.Host + literal
	return u, true
}

// tlsCoverage reads `spec.tls`. An entry with no hosts is the default
// certificate and covers every rule.
func tlsCoverage(obj map[string]any) (hosts []string, everyHost bool) {
	for _, item := range sliceOf(mapAt(obj, "spec")["tls"]) {
		entry := asMap(item)
		names := stringsOf(entry["hosts"])
		if len(names) == 0 {
			everyHost = true
			continue
		}
		hosts = append(hosts, names...)
	}
	return hosts, everyHost
}

// tlsCovers decides https only when this Ingress carries a certificate for the
// host: an exact `spec.tls` entry, a wildcard entry by the certificate rule,
// or a default certificate. Redirect annotations are not consulted: they say
// nothing about a certificate, and a wrong https ends at a certificate error
// where a wrong http only costs a redirect.
func tlsCovers(hosts []string, everyHost bool, host string) bool {
	if everyHost {
		return true
	}
	for _, candidate := range hosts {
		if strings.EqualFold(candidate, host) {
			return true
		}
		if strings.HasPrefix(candidate, "*.") && wildcardCovers(candidate, host) {
			return true
		}
	}
	return false
}

// wildcardCovers applies the certificate rule rather than a glob: *.example.com
// covers foo.example.com, and neither example.com nor a.b.example.com.
func wildcardCovers(pattern, host string) bool {
	suffix := strings.ToLower(pattern[1:]) // ".example.com"
	lower := strings.ToLower(host)
	if !strings.HasSuffix(lower, suffix) {
		return false
	}
	label := lower[:len(lower)-len(suffix)]
	return label != "" && !strings.Contains(label, ".")
}

// literalPath cuts a path at its first regular-expression character: an
// `ImplementationSpecific` path may be a controller's regex, and only its
// literal beginning is an address that works.
func literalPath(path string) (string, bool) {
	if path == "" {
		return "/", false
	}
	cut := strings.IndexAny(path, `(){}[]*+?|\^$`)
	if cut < 0 {
		return path, false
	}
	literal := path[:cut]
	if literal == "" {
		literal = "/"
	}
	return literal, true
}

// loadBalancerAddress is where the controller says this Ingress answers.
func loadBalancerAddress(obj map[string]any) string {
	for _, item := range sliceOf(mapAt(obj, "status", "loadBalancer")["ingress"]) {
		entry := asMap(item)
		if host := str(entry["hostname"]); host != "" {
			return host
		}
		if ip := str(entry["ip"]); ip != "" {
			return ip
		}
	}
	return ""
}

func portsFromPodSpec(spec map[string]any) []Port {
	if spec == nil {
		return nil
	}
	var out []Port
	// Init containers too: a sidecar is one, and it may listen.
	for _, key := range []string{"containers", "initContainers"} {
		for _, item := range sliceOf(spec[key]) {
			container := asMap(item)
			if container == nil {
				continue
			}
			for _, p := range sliceOf(container["ports"]) {
				port := asMap(p)
				number := num(port["containerPort"])
				if number == 0 {
					continue
				}
				out = append(out, Port{
					Name:      str(port["name"]),
					Port:      int32(number),
					HostPort:  int32(num(port["hostPort"])),
					Protocol:  protocolOr(str(port["protocol"])),
					Container: str(container["name"]),
				})
			}
		}
	}
	return out
}

func portsFromService(obj map[string]any) []Port {
	var out []Port
	for _, item := range sliceOf(mapAt(obj, "spec")["ports"]) {
		port := asMap(item)
		number := num(port["port"])
		if number == 0 {
			continue
		}
		out = append(out, Port{
			Name:        str(port["name"]),
			Port:        int32(number),
			TargetPort:  portValue(port["targetPort"]),
			NodePort:    int32(num(port["nodePort"])),
			Protocol:    protocolOr(str(port["protocol"])),
			AppProtocol: str(port["appProtocol"]),
		})
	}
	return out
}

func protocolOr(protocol string) string {
	if protocol == "" {
		return "TCP"
	}
	return protocol
}

// portValue renders a targetPort, a number or the *name* of a container port.
func portValue(value any) string {
	if name, ok := value.(string); ok {
		return name
	}
	if number := num(value); number != 0 {
		return strconv.FormatInt(number, 10)
	}
	return ""
}

// selectorString renders a label set the way a selector is written, sorted so
// the same selector is the same string every time.
func selectorString(labels map[string]any) string {
	pairs := make([]string, 0, len(labels))
	for key, value := range labels {
		pairs = append(pairs, key+"="+str(value))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

// The unstructured accessors, without copying: nothing here is mutated.

func asMap(value any) map[string]any {
	m, _ := value.(map[string]any)
	return m
}

func sliceOf(value any) []any {
	s, _ := value.([]any)
	return s
}

func mapAt(obj map[string]any, path ...string) map[string]any {
	value, found, err := unstructured.NestedFieldNoCopy(obj, path...)
	if err != nil || !found {
		return nil
	}
	return asMap(value)
}

// nestedString reads a string down a path, untrimmed: names are resolved
// exactly as written, so a stray space is not silently repaired.
func nestedString(obj map[string]any, path ...string) string {
	value, found, err := unstructured.NestedFieldNoCopy(obj, path...)
	if err != nil || !found {
		return ""
	}
	return str(value)
}

func str(value any) string {
	s, _ := value.(string)
	return s
}

func stringsOf(value any) []string {
	items := sliceOf(value)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s := str(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// num reads a JSON number. Unstructured decoding yields int64, but an object
// built in a test or round-tripped through encoding/json yields float64.
func num(value any) int64 {
	switch n := value.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}
