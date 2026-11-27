// Package changes answers "what changed here, and who changed it".
//
// Kubernetes records the answer in `managedFields`: each field carries the
// manager that last wrote it, and when. Only the object and the ReplicaSets a
// Deployment left behind are read, so nothing here is beyond what a viewer of
// the resource can already see. It reads through client-go, not kubectl, which
// hides managedFields without `--show-managed-fields`.
package changes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/kindref"
)

// Change is one entry, as the object itself remembers it.
type Change struct {
	// At is when this manager last wrote. Kubernetes records it per manager and
	// operation, never per field.
	At time.Time `json:"at"`
	// Manager is the controller or user that wrote it: "kubectl-client-side-apply",
	// "kube-controller-manager", "helm", "argocd-controller".
	Manager string `json:"manager"`
	// Operation is Apply or Update, with the subresource when there is one.
	Operation string `json:"operation"`
	// Fields are the dotted paths this manager owns, trimmed to what a person
	// can read.
	Fields []string `json:"fields"`
	// Source names the resource this entry came from, so a pod can show its
	// Deployment's history beside its own without the two being confused.
	Source string `json:"source"`
	// Total is how many paths there were before trimming.
	Total int `json:"total"`
}

// Human reports whether a person did this rather than a controller.
// A controller's write is noise on every object; a person's is usually the
// answer.
func (c Change) Human() bool {
	m := strings.ToLower(c.Manager)
	for _, sign := range []string{"kubectl", "helm", "argocd", "flux", "terraform", "kustomize", "oc-"} {
		if strings.Contains(m, sign) {
			return true
		}
	}
	return false
}

// StatusOnly reports whether this entry only touched the status subresource,
// which controllers rewrite constantly and which is never a configuration
// change.
func (c Change) StatusOnly() bool {
	return strings.Contains(c.Operation, "(status)")
}

// Rollout is one revision, read from the ReplicaSets a Deployment leaves behind,
// with the images and timestamps `kubectl rollout history` leaves out.
type Rollout struct {
	Revision string    `json:"revision"`
	At       time.Time `json:"at"`
	Name     string    `json:"name"`
	Replicas int64     `json:"replicas"`
	Image    string    `json:"image,omitempty"`
	Current  bool      `json:"current"`
	Cause    string    `json:"cause,omitempty"`
}

// Report is everything the panel shows.
type Report struct {
	Cluster    string    `json:"cluster"`
	Kind       string    `json:"kind"`
	Namespace  string    `json:"namespace"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"createdAt"`
	Generation int64     `json:"generation"`
	Changes    []Change  `json:"changes"`
	Rollouts   []Rollout `json:"rollouts,omitempty"`
	// Note explains an empty report: "nothing changed" and "this object does
	// not record changes" look identical and mean opposite things.
	Note string `json:"note,omitempty"`

	// Revisioned says this kind keeps a rollout history at all, which
	// `Rollouts == nil` alone cannot tell from a Deployment never rolled out.
	Revisioned bool `json:"revisioned,omitempty"`

	// RolloutsErr is the rollout read's own failure, so a Forbidden on
	// `replicasets` does not render as "no rollouts". It is the error rather
	// than a sentence, so the edge classifies it as every other panel's read.
	RolloutsErr error `json:"-"`
}

// Request identifies one object.
type Request struct {
	Kind      string
	Namespace string
	Name      string
}

// maxFields caps how many paths one entry reports: a controller owns dozens,
// which would hide the entry that matters.
const maxFields = 12

// Reader reads change reports. It holds a REST mapper per cluster in a
// `kindref.Cache`, the same mechanism as `internal/detail`, so a CRD that opens
// in the drawer is not refused by the Changes panel inside it.
type Reader struct {
	clusters *cluster.Manager
	// gate reviews both reads for the person behind the request's context:
	// the object by name, and its ReplicaSets. Nil refuses every read.
	gate *access.Gate

	// dynamicOf and newMapper are fields so tests can use fakes.
	//
	// dynamicOf returns the cluster's held client and builds nothing: a client
	// built from client.Config per Read misses client-go's transport cache
	// (cluster.Manager sets Dial) and costs a new TLS connection per drawer.
	dynamicOf func(*cluster.Client) (dynamic.Interface, error)
	newMapper func(context.Context, *cluster.Client) (meta.RESTMapper, error)

	mappers *kindref.Cache
}

// NewReader returns a Reader over clusters, reviewing reads through gate.
func NewReader(clusters *cluster.Manager, gate *access.Gate) *Reader {
	return &Reader{
		clusters:  clusters,
		gate:      gate,
		dynamicOf: heldDynamic,
		newMapper: defaultMapper,
		mappers:   kindref.NewCache(),
	}
}

// heldDynamic is the unstructured client cluster.Manager built once for this
// cluster, from the same config and http.Client as the typed clientset.
func heldDynamic(client *cluster.Client) (dynamic.Interface, error) {
	if client.Dynamic == nil {
		return nil, fmt.Errorf("cluster %q holds no dynamic client", client.Context)
	}
	return client.Dynamic, nil
}

// Read builds the report for one object.
func (r *Reader) Read(ctx context.Context, contextName string, req Request) (Report, error) {
	name, ok := r.clusters.Resolve(contextName)
	if !ok {
		return Report{}, fmt.Errorf("unknown cluster %q", contextName)
	}
	client, err := r.clusters.Client(name)
	if err != nil {
		return Report{}, err
	}

	// The mapper resolves what a URL carries and reports the scope, so a
	// namespace given for a cluster-scoped kind is dropped rather than sent.
	_, mapping, err := r.mappers.Mapping(ctx, client.MapperKey(), req.Kind, func(ctx context.Context) (meta.RESTMapper, error) {
		return r.newMapper(ctx, client)
	})
	if err != nil {
		var discovery *kindref.DiscoveryError
		if errors.As(err, &discovery) {
			return Report{}, fmt.Errorf("discovering %q: %w", name, discovery.Err)
		}
		return Report{}, err
	}

	dyn, err := r.dynamicOf(client)
	if err != nil {
		return Report{}, err
	}

	namespaced := mapping.Scope.Name() == meta.RESTScopeNameNamespace
	var iface dynamic.ResourceInterface = dyn.Resource(mapping.Resource)
	if namespaced {
		if req.Namespace == "" {
			return Report{}, fmt.Errorf("%s is namespaced; a namespace is required", mapping.Resource.Resource)
		}
		iface = dyn.Resource(mapping.Resource).Namespace(req.Namespace)
	}

	// By name, as the drawer's own read is: the history is the object's.
	namespace := ""
	if namespaced {
		namespace = req.Namespace
	}
	question := access.Attributes{
		Verb: "get", Group: mapping.Resource.Group, Version: mapping.Resource.Version,
		Resource: mapping.Resource.Resource, Namespace: namespace, Name: req.Name,
	}
	var object *unstructured.Unstructured
	_, err = r.gate.Read(ctx, name, question, func(ctx context.Context) error {
		var err error
		object, err = iface.Get(ctx, req.Name, metav1.GetOptions{})
		return err
	})
	if err != nil {
		return Report{}, err
	}

	report := Report{
		Cluster:    name,
		Kind:       mapping.Resource.Resource,
		Namespace:  object.GetNamespace(),
		Name:       object.GetName(),
		CreatedAt:  object.GetCreationTimestamp().Time,
		Generation: object.GetGeneration(),
		Changes:    changesFrom(object, ""),
	}

	if strings.EqualFold(mapping.Resource.Resource, "deployments") {
		// A rollout history that cannot be read does not fail the panel: the
		// managedFields half still shows, and the error is kept.
		report.Revisioned = true
		rollouts, err := r.rollouts(ctx, name, dyn, object)
		if err != nil {
			report.RolloutsErr = err
		} else {
			report.Rollouts = rollouts
		}
	}

	report.Note = noteFor(report)
	return report, nil
}

// noteFor explains an empty report (see Report.Note).
func noteFor(r Report) string {
	if len(r.Changes) > 0 {
		return ""
	}
	return "This object carries no managedFields. Objects created before " +
		"server-side apply, or by a controller that does not set them, record no history."
}

// changesFrom turns an object's managedFields into entries, newest first.
func changesFrom(object *unstructured.Unstructured, source string) []Change {
	entries := object.GetManagedFields()
	changes := make([]Change, 0, len(entries))

	for _, entry := range entries {
		paths, total := flattenFieldsV1(entry.FieldsV1)

		change := Change{
			Manager:   entry.Manager,
			Operation: string(entry.Operation),
			Fields:    paths,
			Total:     total,
			Source:    source,
		}
		if entry.Time != nil {
			change.At = entry.Time.Time
		}
		// Naming the subresource keeps a status rewrite from reading as a
		// configuration change.
		if entry.Subresource != "" {
			change.Operation += " (" + entry.Subresource + ")"
		}
		changes = append(changes, change)
	}

	sort.SliceStable(changes, func(i, j int) bool { return changes[i].At.After(changes[j].At) })
	return changes
}

// flattenFieldsV1 turns Kubernetes' nested `f:` tree into dotted paths.
// It returns the trimmed paths and the total, so the caller can say "and 34
// more" rather than show a fraction as if it were all.
func flattenFieldsV1(raw *metav1.FieldsV1) ([]string, int) {
	if raw == nil || len(raw.Raw) == 0 {
		return nil, 0
	}
	var tree map[string]any
	if err := json.Unmarshal(raw.Raw, &tree); err != nil {
		return nil, 0
	}

	var paths []string
	var walk func(node map[string]any, prefix string)
	walk = func(node map[string]any, prefix string) {
		for key, value := range node {
			// "." marks the node itself; "k:", "v:" and "i:" prefix list
			// entries. None reads as a name, so they are walked through.
			if key == "." {
				continue
			}
			if strings.HasPrefix(key, "k:") || strings.HasPrefix(key, "v:") || strings.HasPrefix(key, "i:") {
				if child, ok := value.(map[string]any); ok {
					walk(child, prefix)
				}
				continue
			}

			path := strings.TrimPrefix(key, "f:")
			if prefix != "" {
				path = prefix + "." + path
			}

			child, ok := value.(map[string]any)
			if !ok || len(child) == 0 {
				paths = append(paths, path)
				continue
			}
			// A node whose only child is "." is a leaf owned whole.
			if _, only := child["."]; only && len(child) == 1 {
				paths = append(paths, path)
				continue
			}
			walk(child, path)
		}
	}
	walk(tree, "")

	sort.Strings(paths)

	// Deduplicate: the entries of one list collapse onto the same path (ten
	// containers give "spec.template.spec.containers.image" ten times), which
	// would spend the maxFields budget on one thing.
	unique := paths[:0]
	for i, path := range paths {
		if i == 0 || path != paths[i-1] {
			unique = append(unique, path)
		}
	}
	paths = unique

	total := len(paths)
	if total > maxFields {
		paths = paths[:maxFields]
	}
	return paths, total
}

// rollouts reads the revision history a Deployment leaves in its ReplicaSets.
func (r *Reader) rollouts(ctx context.Context, cluster string, dyn dynamic.Interface, deployment *unstructured.Unstructured) ([]Rollout, error) {
	selector, found, err := unstructured.NestedStringMap(deployment.Object, "spec", "selector", "matchLabels")
	if err != nil || !found || len(selector) == 0 {
		return nil, fmt.Errorf("the deployment has no matchLabels selector")
	}
	pairs := make([]string, 0, len(selector))
	for key, value := range selector {
		pairs = append(pairs, key+"="+value)
	}
	sort.Strings(pairs) // deterministic, so a cached answer matches a fresh one

	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
	// Reviewed as the list it is: a person who may not list ReplicaSets still
	// gets the managedFields half.
	question := access.Attributes{Verb: "list", Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource,
		Namespace: deployment.GetNamespace()}
	var list *unstructured.UnstructuredList
	_, err = r.gate.Read(ctx, cluster, question, func(ctx context.Context) error {
		var err error
		list, err = dyn.Resource(gvr).Namespace(deployment.GetNamespace()).
			List(ctx, metav1.ListOptions{LabelSelector: strings.Join(pairs, ",")})
		return err
	})
	if err != nil {
		return nil, err
	}

	current := deployment.GetAnnotations()["deployment.kubernetes.io/revision"]

	out := make([]Rollout, 0, len(list.Items))
	for i := range list.Items {
		item := list.Items[i]
		// A ReplicaSet matching the selector but owned by something else is not
		// this Deployment's history.
		if !ownedBy(&item, deployment.GetName()) {
			continue
		}
		annotations := item.GetAnnotations()
		revision := annotations["deployment.kubernetes.io/revision"]

		replicas, _, _ := unstructured.NestedInt64(item.Object, "spec", "replicas")

		out = append(out, Rollout{
			Revision: revision,
			At:       item.GetCreationTimestamp().Time,
			Name:     item.GetName(),
			Replicas: replicas,
			Image:    imagesOf(&item),
			Current:  revision != "" && revision == current,
			Cause:    annotations["kubernetes.io/change-cause"],
		})
	}

	// Newest revision first, by creation time where the revision annotation is
	// missing.
	sort.SliceStable(out, func(i, j int) bool {
		a, aok := strconv.Atoi(out[i].Revision)
		b, bok := strconv.Atoi(out[j].Revision)
		if aok == nil && bok == nil && a != b {
			return a > b
		}
		return out[i].At.After(out[j].At)
	})
	return out, nil
}

func ownedBy(object *unstructured.Unstructured, name string) bool {
	for _, owner := range object.GetOwnerReferences() {
		if owner.Kind == "Deployment" && owner.Name == name {
			return true
		}
	}
	return false
}

// imagesOf reports the container images of a pod template, which is what
// distinguishes one revision from the next in almost every real rollout.
func imagesOf(object *unstructured.Unstructured) string {
	containers, found, err := unstructured.NestedSlice(object.Object, "spec", "template", "spec", "containers")
	if err != nil || !found {
		return ""
	}
	images := make([]string, 0, len(containers))
	for _, raw := range containers {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if image, ok := c["image"].(string); ok && image != "" {
			images = append(images, image)
		}
	}
	return strings.Join(images, ", ")
}

// defaultMapper builds a cluster's mapper; tests replace it.
func defaultMapper(ctx context.Context, client *cluster.Client) (meta.RESTMapper, error) {
	return kindref.DiscoveryMapper(ctx, client.Clients.Discovery())
}
