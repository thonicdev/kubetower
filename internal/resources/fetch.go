package resources

// The list calls, one per kind. Repetitive because client-go's typed clients
// are; confined here so the descriptor table stays readable. A cluster-scoped
// kind ignores the namespace argument (List blanks it anyway).
//
// Every one goes through Listed, the access review's list door: what the
// console reads with its own credentials reaches the person only as far as
// their own `list` allows. internal/access's structural test guards this.

import (
	"context"
	"encoding/json"
	"fmt"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	nodev1 "k8s.io/api/node/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/apiraw"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// PageSize is how many objects one response may carry, when the API server
// honors a limit at all; 500 is what `kubectl get` asks for. A chunk size, not
// a cap: Paged follows the continue token to the end, or refuses past MaxRows,
// and never stops short in silence (the filter would then miss rows the
// cluster holds).
const PageSize = 500

// MaxRows is the most rows Paged gathers for one list. Past it the list is
// refused with TooLarge rather than truncated, for PageSize's reason.
//
// The ceiling bounds memory: under the server profile one unbounded list would
// weigh on every person's console. A list read only to be narrowed stops
// earlier, at access.NarrowedRows. It cannot bound an API server that ignores
// the limit and answers the whole list in one response.
const MaxRows = 50000

// TooLarge is a list Paged stopped reading at MaxRows. Its own type, so
// internal/web answers it as a ceiling (not retryable until a namespace is
// chosen) rather than as a failed read the page would re-poll.
type TooLarge struct{}

func (*TooLarge) Error() string {
	return fmt.Sprintf("this list holds more than %d rows, more than the console reads at once; "+
		"choose a namespace", MaxRows)
}

// Cached is what every list here asks with: `ResourceVersion: "0"`, served
// from the API server's watch cache instead of a quorum read from etcd. It may
// be a moment behind, less than the refresh interval. A function, not a var,
// so no caller can alter it for everyone.
func Cached() metav1.ListOptions {
	return metav1.ListOptions{ResourceVersion: "0", Limit: PageSize}
}

// Paged reads a list call to its end, following the continue token.
//
// Whether the API server ignores `Limit` or honors it with a token, the
// result is the complete list, or TooLarge past MaxRows.
//
// The resource version is dropped after the first page: the API server
// refuses it with `continue`, and the token already pins the snapshot.
func Paged[T any, L metav1.ListInterface](
	ctx context.Context,
	base metav1.ListOptions,
	list func(context.Context, metav1.ListOptions) (L, error),
	items func(L) []T,
) ([]T, error) {
	opts := base
	var out []T
	// A list read only to be narrowed stops at access.NarrowedRows: past it
	// the person is asked for a namespace anyway.
	narrowing := access.Narrowing(ctx)
	for {
		page, err := list(ctx, opts)
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = items(page)
		} else {
			out = append(out, items(page)...)
		}
		if narrowing && len(out) > access.NarrowedRows {
			return nil, access.TooLargeToNarrow()
		}
		if len(out) > MaxRows {
			return nil, &TooLarge{}
		}
		opts.Continue = page.GetContinue()
		if opts.Continue == "" {
			return out, nil
		}
		opts.ResourceVersion = ""
	}
}

// Reader is what a list is read through: the cluster's clients, and the gate
// that reviews the person behind the request's context before what was read
// reaches them (internal/access). Cluster is the resolved context name the
// gate files its answers under.
type Reader struct {
	Gate    *access.Gate
	Cluster string
	Clients kubernetes.Interface
	// Selector is the label selector the API server narrows every list to,
	// already parsed and written back in its canonical form (ParseSelector).
	// Empty selects everything.
	Selector string
}

// options is what every list of this Reader asks with: Cached, narrowed to
// the Reader's label selector by the API server.
func (r Reader) options() metav1.ListOptions {
	opts := Cached()
	opts.LabelSelector = r.Selector
	return opts
}

// ReaderFor is the Reader of one cluster's client.
func ReaderFor(gate *access.Gate, client *cluster.Client) Reader {
	return Reader{Gate: gate, Cluster: client.Context, Clients: client.Clients}
}

// Question is what a list is reviewed with, and whether its kind has
// namespaces to narrow a denied list to.
type Question struct {
	Attrs      access.Attributes
	Namespaced bool
}

// InNamespace is the question of a namespaced kind's list in ns - every
// namespace when ns is empty.
func InNamespace(group, version, resource, ns string) Question {
	return Question{Namespaced: true, Attrs: access.Attributes{
		Verb: "list", Group: group, Version: version, Resource: resource, Namespace: ns}}
}

// ClusterScoped is the question of a cluster-scoped kind's list.
func ClusterScoped(group, version, resource string) Question {
	return Question{Attrs: access.Attributes{Verb: "list", Group: group, Version: version, Resource: resource}}
}

// Listed is Paged behind the access review: the list is read only once the
// person behind ctx may have it, and a list of every namespace they may not
// make whole is narrowed to the namespaces they may (access.List). The
// Visible it answers is what lets a page tell the second from an empty list.
func Listed[T any, PT pointerTo[T], L metav1.ListInterface](
	ctx context.Context, r Reader, q Question, base metav1.ListOptions,
	list func(context.Context, metav1.ListOptions) (L, error),
	items func(L) []T,
) ([]T, access.Visible, error) {
	var where func(*T) string
	if q.Namespaced {
		where = func(item *T) string { return PT(item).GetNamespace() }
	}
	return access.List(ctx, r.Gate, r.Cluster, q.Attrs, where, func(ctx context.Context) ([]T, error) {
		return Paged(ctx, base, list, items)
	})
}

func fetchPods(ctx context.Context, r Reader, ns string) ([]corev1.Pod, access.Visible, error) {
	return Listed(ctx, r, InNamespace("", "v1", "pods", ns), r.options(), r.Clients.CoreV1().Pods(ns).List,
		func(l *corev1.PodList) []corev1.Pod { return l.Items })
}

func fetchDeployments(ctx context.Context, r Reader, ns string) ([]appsv1.Deployment, access.Visible, error) {
	return Listed(ctx, r, InNamespace("apps", "v1", "deployments", ns), r.options(), r.Clients.AppsV1().Deployments(ns).List,
		func(l *appsv1.DeploymentList) []appsv1.Deployment { return l.Items })
}

func fetchStatefulSets(ctx context.Context, r Reader, ns string) ([]appsv1.StatefulSet, access.Visible, error) {
	return Listed(ctx, r, InNamespace("apps", "v1", "statefulsets", ns), r.options(), r.Clients.AppsV1().StatefulSets(ns).List,
		func(l *appsv1.StatefulSetList) []appsv1.StatefulSet { return l.Items })
}

func fetchDaemonSets(ctx context.Context, r Reader, ns string) ([]appsv1.DaemonSet, access.Visible, error) {
	return Listed(ctx, r, InNamespace("apps", "v1", "daemonsets", ns), r.options(), r.Clients.AppsV1().DaemonSets(ns).List,
		func(l *appsv1.DaemonSetList) []appsv1.DaemonSet { return l.Items })
}

func fetchReplicaSets(ctx context.Context, r Reader, ns string) ([]appsv1.ReplicaSet, access.Visible, error) {
	return Listed(ctx, r, InNamespace("apps", "v1", "replicasets", ns), r.options(), r.Clients.AppsV1().ReplicaSets(ns).List,
		func(l *appsv1.ReplicaSetList) []appsv1.ReplicaSet { return l.Items })
}

func fetchJobs(ctx context.Context, r Reader, ns string) ([]batchv1.Job, access.Visible, error) {
	return Listed(ctx, r, InNamespace("batch", "v1", "jobs", ns), r.options(), r.Clients.BatchV1().Jobs(ns).List,
		func(l *batchv1.JobList) []batchv1.Job { return l.Items })
}

func fetchCronJobs(ctx context.Context, r Reader, ns string) ([]batchv1.CronJob, access.Visible, error) {
	return Listed(ctx, r, InNamespace("batch", "v1", "cronjobs", ns), r.options(), r.Clients.BatchV1().CronJobs(ns).List,
		func(l *batchv1.CronJobList) []batchv1.CronJob { return l.Items })
}

func fetchServices(ctx context.Context, r Reader, ns string) ([]corev1.Service, access.Visible, error) {
	return Listed(ctx, r, InNamespace("", "v1", "services", ns), r.options(), r.Clients.CoreV1().Services(ns).List,
		func(l *corev1.ServiceList) []corev1.Service { return l.Items })
}

func fetchIngresses(ctx context.Context, r Reader, ns string) ([]networkingv1.Ingress, access.Visible, error) {
	return Listed(ctx, r, InNamespace("networking.k8s.io", "v1", "ingresses", ns), r.options(), r.Clients.NetworkingV1().Ingresses(ns).List,
		func(l *networkingv1.IngressList) []networkingv1.Ingress { return l.Items })
}

func fetchConfigMaps(ctx context.Context, r Reader, ns string) ([]corev1.ConfigMap, access.Visible, error) {
	return Listed(ctx, r, InNamespace("", "v1", "configmaps", ns), r.options(), r.Clients.CoreV1().ConfigMaps(ns).List,
		func(l *corev1.ConfigMapList) []corev1.ConfigMap { return l.Items })
}

// fetchSecrets is the heaviest list here: the API has no projection, so
// reading key names means reading the values too. Naming a namespace is what
// makes it cheap.
//
// For a person who may not list Secrets in every namespace it goes through
// access.ListIn rather than Listed: namespaces holding Secrets come from a
// metadata-only list (secretNamespaces), each is reviewed, and Secrets are read
// only where the person may list them, so no Secret they may not see is read.
func fetchSecrets(ctx context.Context, r Reader, ns string) ([]corev1.Secret, access.Visible, error) {
	q := InNamespace("", "v1", "secrets", ns)
	return access.ListIn(ctx, r.Gate, r.Cluster, q.Attrs,
		func(ctx context.Context) ([]string, error) { return secretNamespaces(ctx, r.Clients, r.options()) },
		func(ctx context.Context, ns string) ([]corev1.Secret, error) {
			return Paged(ctx, r.options(), r.Clients.CoreV1().Secrets(ns).List,
				func(l *corev1.SecretList) []corev1.Secret { return l.Items })
		})
}

// metadataOnly is the Accept header that has the API server answer a list as
// PartialObjectMetadataList, metadata without bodies, as client-go's metadata
// client asks.
const metadataOnly = "application/json;as=PartialObjectMetadataList;g=meta.k8s.io;v=v1"

// secretNamespaces is where the cluster's Secrets are, read without their
// values: one metadata-only list across every namespace, paged and bounded as
// every list is.
//
// An answer that is not metadata is refused, not decoded: the API server
// ignored the Accept header and sent the Secrets themselves.
func secretNamespaces(ctx context.Context, clients kubernetes.Interface, base metav1.ListOptions) ([]string, error) {
	list := func(ctx context.Context, opts metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
		raw, err := apiraw.Do(ctx, clients.CoreV1().RESTClient().Get().Resource("secrets").
			VersionedParams(&opts, scheme.ParameterCodec).SetHeader("Accept", metadataOnly))
		if err != nil {
			return nil, err
		}
		var page metav1.PartialObjectMetadataList
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("the metadata-only list of Secrets did not parse: %w", err)
		}
		if page.Kind != "PartialObjectMetadataList" {
			return nil, fmt.Errorf("the API server answered a metadata-only list of Secrets with %q, "+
				"not their metadata; choose a namespace", page.Kind)
		}
		return &page, nil
	}
	items, err := Paged(ctx, base, list,
		func(l *metav1.PartialObjectMetadataList) []metav1.PartialObjectMetadata { return l.Items })
	if err != nil {
		return nil, err
	}
	namespaces := make([]string, 0, len(items))
	for i := range items {
		namespaces = append(namespaces, items[i].Namespace)
	}
	return namespaces, nil
}

func fetchPVCs(ctx context.Context, r Reader, ns string) ([]corev1.PersistentVolumeClaim, access.Visible, error) {
	return Listed(ctx, r, InNamespace("", "v1", "persistentvolumeclaims", ns), r.options(), r.Clients.CoreV1().PersistentVolumeClaims(ns).List,
		func(l *corev1.PersistentVolumeClaimList) []corev1.PersistentVolumeClaim { return l.Items })
}

func fetchPVs(ctx context.Context, r Reader, _ string) ([]corev1.PersistentVolume, access.Visible, error) {
	return Listed(ctx, r, ClusterScoped("", "v1", "persistentvolumes"), r.options(), r.Clients.CoreV1().PersistentVolumes().List,
		func(l *corev1.PersistentVolumeList) []corev1.PersistentVolume { return l.Items })
}

func fetchNodes(ctx context.Context, r Reader, _ string) ([]corev1.Node, access.Visible, error) {
	return Listed(ctx, r, ClusterScoped("", "v1", "nodes"), r.options(), r.Clients.CoreV1().Nodes().List,
		func(l *corev1.NodeList) []corev1.Node { return l.Items })
}

func fetchNamespaces(ctx context.Context, r Reader, _ string) ([]corev1.Namespace, access.Visible, error) {
	return Listed(ctx, r, ClusterScoped("", "v1", "namespaces"), r.options(), r.Clients.CoreV1().Namespaces().List,
		func(l *corev1.NamespaceList) []corev1.Namespace { return l.Items })
}

func fetchEvents(ctx context.Context, r Reader, ns string) ([]corev1.Event, access.Visible, error) {
	return Listed(ctx, r, InNamespace("", "v1", "events", ns), r.options(), r.Clients.CoreV1().Events(ns).List,
		func(l *corev1.EventList) []corev1.Event { return l.Items })
}

func fetchServiceAccounts(ctx context.Context, r Reader, ns string) ([]corev1.ServiceAccount, access.Visible, error) {
	return Listed(ctx, r, InNamespace("", "v1", "serviceaccounts", ns), r.options(), r.Clients.CoreV1().ServiceAccounts(ns).List,
		func(l *corev1.ServiceAccountList) []corev1.ServiceAccount { return l.Items })
}

// fetchEndpoints: Endpoints answers "does this Service have anything behind
// it" in one object per Service. EndpointSlices are listed beside it: they are
// what the cluster keeps (Endpoints is a projection capped at a thousand
// addresses), with the address family and the split.
func fetchEndpoints(ctx context.Context, r Reader, ns string) ([]corev1.Endpoints, access.Visible, error) {
	return Listed(ctx, r, InNamespace("", "v1", "endpoints", ns), r.options(), r.Clients.CoreV1().Endpoints(ns).List,
		func(l *corev1.EndpointsList) []corev1.Endpoints { return l.Items })
}

func fetchEndpointSlices(ctx context.Context, r Reader, ns string) ([]discoveryv1.EndpointSlice, access.Visible, error) {
	return Listed(ctx, r, InNamespace("discovery.k8s.io", "v1", "endpointslices", ns), r.options(), r.Clients.DiscoveryV1().EndpointSlices(ns).List,
		func(l *discoveryv1.EndpointSliceList) []discoveryv1.EndpointSlice { return l.Items })
}

func fetchReplicationControllers(ctx context.Context, r Reader, ns string) ([]corev1.ReplicationController, access.Visible, error) {
	return Listed(ctx, r, InNamespace("", "v1", "replicationcontrollers", ns), r.options(), r.Clients.CoreV1().ReplicationControllers(ns).List,
		func(l *corev1.ReplicationControllerList) []corev1.ReplicationController { return l.Items })
}

func fetchResourceQuotas(ctx context.Context, r Reader, ns string) ([]corev1.ResourceQuota, access.Visible, error) {
	return Listed(ctx, r, InNamespace("", "v1", "resourcequotas", ns), r.options(), r.Clients.CoreV1().ResourceQuotas(ns).List,
		func(l *corev1.ResourceQuotaList) []corev1.ResourceQuota { return l.Items })
}

func fetchLimitRanges(ctx context.Context, r Reader, ns string) ([]corev1.LimitRange, access.Visible, error) {
	return Listed(ctx, r, InNamespace("", "v1", "limitranges", ns), r.options(), r.Clients.CoreV1().LimitRanges(ns).List,
		func(l *corev1.LimitRangeList) []corev1.LimitRange { return l.Items })
}

// fetchHPAs reads autoscaling/v2: v1 carries only a CPU utilization target
// and silently drops every other metric.
func fetchHPAs(ctx context.Context, r Reader, ns string) ([]autoscalingv2.HorizontalPodAutoscaler, access.Visible, error) {
	return Listed(ctx, r, InNamespace("autoscaling", "v2", "horizontalpodautoscalers", ns), r.options(), r.Clients.AutoscalingV2().HorizontalPodAutoscalers(ns).List,
		func(l *autoscalingv2.HorizontalPodAutoscalerList) []autoscalingv2.HorizontalPodAutoscaler {
			return l.Items
		})
}

func fetchPDBs(ctx context.Context, r Reader, ns string) ([]policyv1.PodDisruptionBudget, access.Visible, error) {
	return Listed(ctx, r, InNamespace("policy", "v1", "poddisruptionbudgets", ns), r.options(), r.Clients.PolicyV1().PodDisruptionBudgets(ns).List,
		func(l *policyv1.PodDisruptionBudgetList) []policyv1.PodDisruptionBudget { return l.Items })
}

func fetchPriorityClasses(ctx context.Context, r Reader, _ string) ([]schedulingv1.PriorityClass, access.Visible, error) {
	return Listed(ctx, r, ClusterScoped("scheduling.k8s.io", "v1", "priorityclasses"), r.options(), r.Clients.SchedulingV1().PriorityClasses().List,
		func(l *schedulingv1.PriorityClassList) []schedulingv1.PriorityClass { return l.Items })
}

func fetchRuntimeClasses(ctx context.Context, r Reader, _ string) ([]nodev1.RuntimeClass, access.Visible, error) {
	return Listed(ctx, r, ClusterScoped("node.k8s.io", "v1", "runtimeclasses"), r.options(), r.Clients.NodeV1().RuntimeClasses().List,
		func(l *nodev1.RuntimeClassList) []nodev1.RuntimeClass { return l.Items })
}

// fetchLeases: a lease no longer renewed is a controller that has stopped,
// and nothing else in the API says so.
func fetchLeases(ctx context.Context, r Reader, ns string) ([]coordinationv1.Lease, access.Visible, error) {
	return Listed(ctx, r, InNamespace("coordination.k8s.io", "v1", "leases", ns), r.options(), r.Clients.CoordinationV1().Leases(ns).List,
		func(l *coordinationv1.LeaseList) []coordinationv1.Lease { return l.Items })
}

func fetchMutatingWebhooks(ctx context.Context, r Reader, _ string) ([]admissionv1.MutatingWebhookConfiguration, access.Visible, error) {
	return Listed(ctx, r, ClusterScoped("admissionregistration.k8s.io", "v1", "mutatingwebhookconfigurations"), r.options(), r.Clients.AdmissionregistrationV1().MutatingWebhookConfigurations().List,
		func(l *admissionv1.MutatingWebhookConfigurationList) []admissionv1.MutatingWebhookConfiguration {
			return l.Items
		})
}

func fetchValidatingWebhooks(ctx context.Context, r Reader, _ string) ([]admissionv1.ValidatingWebhookConfiguration, access.Visible, error) {
	return Listed(ctx, r, ClusterScoped("admissionregistration.k8s.io", "v1", "validatingwebhookconfigurations"), r.options(), r.Clients.AdmissionregistrationV1().ValidatingWebhookConfigurations().List,
		func(l *admissionv1.ValidatingWebhookConfigurationList) []admissionv1.ValidatingWebhookConfiguration {
			return l.Items
		})
}

func fetchIngressClasses(ctx context.Context, r Reader, _ string) ([]networkingv1.IngressClass, access.Visible, error) {
	return Listed(ctx, r, ClusterScoped("networking.k8s.io", "v1", "ingressclasses"), r.options(), r.Clients.NetworkingV1().IngressClasses().List,
		func(l *networkingv1.IngressClassList) []networkingv1.IngressClass { return l.Items })
}

func fetchNetworkPolicies(ctx context.Context, r Reader, ns string) ([]networkingv1.NetworkPolicy, access.Visible, error) {
	return Listed(ctx, r, InNamespace("networking.k8s.io", "v1", "networkpolicies", ns), r.options(), r.Clients.NetworkingV1().NetworkPolicies(ns).List,
		func(l *networkingv1.NetworkPolicyList) []networkingv1.NetworkPolicy { return l.Items })
}

func fetchStorageClasses(ctx context.Context, r Reader, _ string) ([]storagev1.StorageClass, access.Visible, error) {
	return Listed(ctx, r, ClusterScoped("storage.k8s.io", "v1", "storageclasses"), r.options(), r.Clients.StorageV1().StorageClasses().List,
		func(l *storagev1.StorageClassList) []storagev1.StorageClass { return l.Items })
}

func fetchClusterRoles(ctx context.Context, r Reader, _ string) ([]rbacv1.ClusterRole, access.Visible, error) {
	return Listed(ctx, r, ClusterScoped("rbac.authorization.k8s.io", "v1", "clusterroles"), r.options(), r.Clients.RbacV1().ClusterRoles().List,
		func(l *rbacv1.ClusterRoleList) []rbacv1.ClusterRole { return l.Items })
}

func fetchRoles(ctx context.Context, r Reader, ns string) ([]rbacv1.Role, access.Visible, error) {
	return Listed(ctx, r, InNamespace("rbac.authorization.k8s.io", "v1", "roles", ns), r.options(), r.Clients.RbacV1().Roles(ns).List,
		func(l *rbacv1.RoleList) []rbacv1.Role { return l.Items })
}

func fetchClusterRoleBindings(ctx context.Context, r Reader, _ string) ([]rbacv1.ClusterRoleBinding, access.Visible, error) {
	return Listed(ctx, r, ClusterScoped("rbac.authorization.k8s.io", "v1", "clusterrolebindings"), r.options(), r.Clients.RbacV1().ClusterRoleBindings().List,
		func(l *rbacv1.ClusterRoleBindingList) []rbacv1.ClusterRoleBinding { return l.Items })
}

func fetchRoleBindings(ctx context.Context, r Reader, ns string) ([]rbacv1.RoleBinding, access.Visible, error) {
	return Listed(ctx, r, InNamespace("rbac.authorization.k8s.io", "v1", "rolebindings", ns), r.options(), r.Clients.RbacV1().RoleBindings(ns).List,
		func(l *rbacv1.RoleBindingList) []rbacv1.RoleBinding { return l.Items })
}
