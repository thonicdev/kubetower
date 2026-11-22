package extension

import (
	"context"
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// Clients is one cluster's two clients.
//
// A struct rather than two return values, so adding a client is not a breaking
// change to every call site. Both are `k8s.io` interfaces, nameable from
// another module, unlike the console's internal cluster client.
type Clients struct {
	Typed   kubernetes.Interface
	Dynamic dynamic.Interface
}

// The three answers a custom-resource lookup can give that are not success.
//
// They are kept apart because two of them mean the cluster has something the
// console could not show: drawn as one empty page, they tell the operator the
// cluster has nothing.
var (
	// ErrUnknownKind: no CRD in this cluster answers to that name.
	ErrUnknownKind = errors.New("unknown custom resource kind")
	// ErrNotServed: the CRD exists and every one of its versions is
	// `served: false`.
	ErrNotServed = errors.New("custom resource kind served by no version")
	// ErrAmbiguousKind: two API groups define the same plural, so the name does
	// not identify one kind. Answered by naming both rather than by choosing.
	ErrAmbiguousKind = errors.New("ambiguous custom resource kind")
)

// ErrUnknownCluster is the answer a Host method gives, wrapped, when the cluster
// it was handed is not a context in the kubeconfig this console reads.
//
// The per-cluster gate in front of an extension's routes refuses a name it
// cannot resolve before the route runs, as the console's API does: 404,
// application/problem+json, code not_found, UnknownClusterText as its detail.
// A route still meets this error when the kubeconfig changes between the gate
// and the read. Ask with errors.Is, answer 404 with UnknownClusterText, and keep 502 for a cluster
// that exists and could not be read: the two have different people fixing
// them. A known cluster that cannot be reached never answers with this.
var ErrUnknownCluster = errors.New("unknown cluster")

// UnknownClusterText is the console's sentence for a cluster name that is not
// in its kubeconfig, for a page to answer with instead of writing its own. It
// names both likely causes - a link to a removed or renamed context, and a
// kubeconfig that changed while the page was open - since they want different
// next steps.
//
// The name is quoted with %q and is otherwise as it arrived, from a URL: a page
// writing this into HTML escapes it like any other text, and one answering with
// http.Error (text/plain) need not.
func UnknownClusterText(cluster string) string {
	return fmt.Sprintf("There is no cluster called %q in the kubeconfig this KubeTower reads. "+
		"A link or bookmark may name a context that has since been removed or renamed, or the "+
		"kubeconfig changed while this page was open: go back to the console to see the clusters "+
		"it serves now.", cluster)
}

// CRD is one custom resource kind, reduced to what addressing it needs.
type CRD struct {
	Group    string
	Version  string
	Plural   string
	Singular string
	Kind     string
	// Namespaced is the CRD's own scope. False means cluster-scoped, and a page
	// that lists it in a namespace gets nothing rather than an error.
	Namespaced bool
}

// GVR is what a dynamic client is addressed with.
func (c CRD) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: c.Group, Version: c.Version, Resource: c.Plural}
}

// Slug is the kind's address in a URL: the plural, qualified by its group so
// two vendors defining the same plural do not collide.
func (c CRD) Slug() string {
	if c.Group == "" {
		return c.Plural
	}
	return c.Plural + "." + c.Group
}

// CRDRegistry is the cluster's custom-resource catalog, already cached.
//
// An interface so an extension cannot reach the console's per-cluster cache and
// dynamic client behind it, and so a test can hand a page a catalog with no
// cluster.
type CRDRegistry interface {
	// All is every kind this cluster serves, in no particular order.
	All(ctx context.Context) ([]CRD, error)
	// Lookup resolves a kind by plural, singular, slug or short name. It answers
	// with one of ErrUnknownKind, ErrNotServed or ErrAmbiguousKind, wrapped with
	// the name tried: ask with errors.Is.
	Lookup(ctx context.Context, kind string) (CRD, error)
}

// ListQuery is one list call.
//
// Addressed by GVR rather than by the console's kind slugs, which are a UI
// choice and may change.
type ListQuery struct {
	GVR schema.GroupVersionResource
	// Namespace, or empty for every namespace. A cluster-scoped kind ignores it.
	Namespace string
	// LabelSelector and FieldSelector are the API server's own, passed through
	// unchanged.
	LabelSelector string
	FieldSelector string
	// Limit, when above zero, is the most rows the page holds: a question
	// such as "is there any?" is answered with one request for that many
	// rows instead of the whole list. A page cut short by it is not
	// Complete, and its Note says so. For a person who may list the kind
	// only in some namespaces, the rows are kept after narrowing, so a
	// limit never hides a row they may see behind one they may not.
	// Zero reads the whole list.
	Limit int64
	// Columns, when true, reads the list as the API server's table: the
	// kind's printer columns and each object's metadata, never its body
	// (`as=Table`, `includeObject=Metadata`). The answer is in Page.Columns
	// and Page.Rows, and Page.Items stays empty. The review, the narrowing,
	// Limit and the paging are the same as for a list of objects.
	//
	// For a kind whose objects are large and whose printer columns say
	// enough - a trivy-operator report carries every finding, while its
	// columns carry the counts - or when only the metadata is wanted: owner
	// references, labels, annotations, creation time.
	Columns bool
	// Each, when set, reads the list page by page (EachPageSize objects a
	// request, following the continue token) and calls Each on every object
	// in turn, dropping each page once it is walked: what is kept is
	// Each's to choose, so a list of large objects costs one page at a time,
	// not the whole list. Page.Items stays empty. The review, the narrowing
	// to the namespaces the person may list (Complete false, with a Note) and
	// Limit are those of a list of objects; under narrowing, only the
	// namespaces the person may list are read. Each is called from one
	// goroutine, never concurrently. An error from Each stops the read and
	// is returned as it is. Each and Columns together are refused.
	Each func(unstructured.Unstructured) error
}

// EachPageSize is how many objects one request of a ListQuery.Each read
// asks for.
const EachPageSize = 250

// Page is the answer to a List.
//
// An empty Items means the cluster answered and had nothing to show; a failed
// call is the error return. The two must stay distinguishable: a failed read
// drawn as an empty list tells the reader something false about the cluster.
type Page struct {
	// Items is the objects read, when the query did not ask for Columns.
	Items []unstructured.Unstructured
	// Columns names the table's columns in the API server's order, every
	// one of them, priority 1 included (kubectl shows those only with
	// `-o wide`). Set only when the query asked for Columns.
	Columns []string
	// Rows is one row per object, when the query asked for Columns. Empty
	// means the cluster answered and had nothing to show, as for Items.
	Rows []Row
	// Complete is false when the read stopped before the end — a page cap, a
	// timeout, a partial answer from an aggregated API server. An incomplete
	// list must not be drawn as a complete one.
	Complete bool
	// Note is why Complete is false, in a sentence the page can print. Empty
	// when Complete is true.
	Note string
}

// Row is one object of a list read as the API server's table: its metadata,
// and its printer columns' cells.
type Row struct {
	Namespace string
	Name      string
	// Labels and Annotations are the object's own; nil when it has none.
	Labels      map[string]string
	Annotations map[string]string
	// Owners is the object's ownerReferences.
	Owners  []metav1.OwnerReference
	Created time.Time
	// Cells is aligned on Page.Columns. Each is a string, an int64, a
	// float64, a bool, or nil when the API server sent none. A number
	// written without a fraction or an exponent is an int64, any other a
	// float64, whatever type its column declares. A cell the API server sent
	// as an object or an array is its JSON text, a string.
	Cells []any
}

// Usage is what the kubelets said, and how many of them said it.
type Usage struct {
	// Nodes is keyed by node name. A node that failed is absent from the map,
	// so it cannot be confused with a node that answered and had nothing.
	Nodes map[string]NodeUsage
	// Asked and Answered are always set: "3 of 40 nodes answered" and "40 of
	// 40" draw the same empty panel on the missing ones, and only the page's
	// sentence tells them apart.
	Asked    int
	Answered int
	// FirstErr is the first failure, kept rather than counted: the reason is
	// what can be acted on, and identical failures add nothing.
	FirstErr error
	// Complete is false when a node's pods were narrowed for the person behind
	// the request: a kubelet names every pod it runs, so only the pods of
	// namespaces the person may list pods in are kept - none of a namespace
	// whose review could not be made. A narrowed answer must not be drawn as
	// the whole one.
	//
	// It is about the pods only. Which nodes answered is Asked and Answered,
	// and a node's own filesystem is never narrowed.
	Complete bool
	// Note is why Complete is false, in a sentence the page can print. Empty
	// when Complete is true. Built from the question, never from the pods, so
	// it does not tell the person how many pods they were not shown.
	Note string
}

// NodeUsage is one kubelet's answer, narrowed to what a page reads.
type NodeUsage struct {
	// NodeFS is the node's own root filesystem — the partition image layers,
	// writable layers and logs are written to, and the one the kubelet evicts
	// pods to protect.
	NodeFS FS
	// NodeFSMeasured says the kubelet reported that block at all. A kubelet that
	// answered without one is not a node with an empty disk, and a bare zeroed
	// FS cannot tell the two apart.
	NodeFSMeasured bool
	Pods           []PodUsage
}

// PodUsage is one pod's entry in a node's summary.
type PodUsage struct {
	Name       string
	Namespace  string
	Containers []ContainerUsage
	Volumes    []VolumeUsage
}

// ContainerUsage is one container's disk, from its own kubelet.
type ContainerUsage struct {
	Name string
	// EphemeralBytes is the writable layer plus the logs, which together are
	// what the `ephemeral-storage` limit is enforced against. Either alone
	// under-reports, and a container could then be evicted for a limit it
	// appeared to be under.
	EphemeralBytes int64
}

// VolumeUsage is one mounted volume, named as the pod spec names it.
type VolumeUsage struct {
	Name string
	FS   FS
	// PVCRef is the claim this volume is, as the kubelet names it —
	// "namespace/name" — or empty for an emptyDir, a configMap, a projected
	// token or a hostPath. Read from the kubelet rather than joined from the pod
	// spec, so one claim's figures cannot land under another claim's row.
	PVCRef string
}

// FS is a filesystem as the kubelet measured it.
type FS struct {
	UsedBytes int64
	// CapacityBytes is what statfs said about the mount point, which is not
	// always the size of the thing that was mounted. See Trustworthy.
	CapacityBytes int64
}
