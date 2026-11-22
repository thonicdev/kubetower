package resources

// listQuestions is the review each kind's whole list is asked with, across
// every namespace: the same question its fetch function hands to Listed,
// spelled again for callers that only ask whether a list would be refused. A
// test runs every descriptor's list and fails when the two disagree.
var listQuestions = map[string]Question{
	"pods":                            InNamespace("", "v1", "pods", ""),
	"deployments":                     InNamespace("apps", "v1", "deployments", ""),
	"statefulsets":                    InNamespace("apps", "v1", "statefulsets", ""),
	"daemonsets":                      InNamespace("apps", "v1", "daemonsets", ""),
	"replicasets":                     InNamespace("apps", "v1", "replicasets", ""),
	"jobs":                            InNamespace("batch", "v1", "jobs", ""),
	"cronjobs":                        InNamespace("batch", "v1", "cronjobs", ""),
	"replicationcontrollers":          InNamespace("", "v1", "replicationcontrollers", ""),
	"services":                        InNamespace("", "v1", "services", ""),
	"ingresses":                       InNamespace("networking.k8s.io", "v1", "ingresses", ""),
	"endpoints":                       InNamespace("", "v1", "endpoints", ""),
	"endpointslices":                  InNamespace("discovery.k8s.io", "v1", "endpointslices", ""),
	"ingressclasses":                  ClusterScoped("networking.k8s.io", "v1", "ingressclasses"),
	"networkpolicies":                 InNamespace("networking.k8s.io", "v1", "networkpolicies", ""),
	"configmaps":                      InNamespace("", "v1", "configmaps", ""),
	"secrets":                         InNamespace("", "v1", "secrets", ""),
	"resourcequotas":                  InNamespace("", "v1", "resourcequotas", ""),
	"limitranges":                     InNamespace("", "v1", "limitranges", ""),
	"horizontalpodautoscalers":        InNamespace("autoscaling", "v2", "horizontalpodautoscalers", ""),
	"poddisruptionbudgets":            InNamespace("policy", "v1", "poddisruptionbudgets", ""),
	"priorityclasses":                 ClusterScoped("scheduling.k8s.io", "v1", "priorityclasses"),
	"runtimeclasses":                  ClusterScoped("node.k8s.io", "v1", "runtimeclasses"),
	"leases":                          InNamespace("coordination.k8s.io", "v1", "leases", ""),
	"mutatingwebhookconfigurations":   ClusterScoped("admissionregistration.k8s.io", "v1", "mutatingwebhookconfigurations"),
	"validatingwebhookconfigurations": ClusterScoped("admissionregistration.k8s.io", "v1", "validatingwebhookconfigurations"),
	"serviceaccounts":                 InNamespace("", "v1", "serviceaccounts", ""),
	"clusterroles":                    ClusterScoped("rbac.authorization.k8s.io", "v1", "clusterroles"),
	"roles":                           InNamespace("rbac.authorization.k8s.io", "v1", "roles", ""),
	"clusterrolebindings":             ClusterScoped("rbac.authorization.k8s.io", "v1", "clusterrolebindings"),
	"rolebindings":                    InNamespace("rbac.authorization.k8s.io", "v1", "rolebindings", ""),
	"persistentvolumeclaims":          InNamespace("", "v1", "persistentvolumeclaims", ""),
	"persistentvolumes":               ClusterScoped("", "v1", "persistentvolumes"),
	"storageclasses":                  ClusterScoped("storage.k8s.io", "v1", "storageclasses"),
	"nodes":                           ClusterScoped("", "v1", "nodes"),
	"namespaces":                      ClusterScoped("", "v1", "namespaces"),
	"events":                          InNamespace("", "v1", "events", ""),
}

// ListQuestion is the review this kind's list across every namespace is asked
// with, and whether this package has one (every kind in Kinds does). The
// in-cluster menu uses it to leave out kinds the console may list nowhere.
func (d Descriptor) ListQuestion() (Question, bool) {
	q, ok := listQuestions[d.Kind]
	return q, ok
}
