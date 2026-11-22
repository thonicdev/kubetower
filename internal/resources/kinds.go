package resources

import "time"

// How often a list is worth re-reading, in three bands chosen by how fast the
// object changes. The interval multiplies every read: two seconds on Secrets
// would carry every secret's data through this process thirty times a minute.
//
//   - live: a phase, a restart count, a node's readiness; watched as it moves.
//   - reconciling: what a controller writes after a deploy, scale, bind or
//     finish; a rollout still visibly moves.
//   - declared: what a human last applied, minutes to days apart.
//
// Nothing is longer than a minute: a console table quietly out of date is worse
// than a slow one.
const (
	liveRefresh        = DefaultRefresh
	reconcilingRefresh = 5 * time.Second
	declaredRefresh    = 60 * time.Second
)

// The table. Adding a kind is a row here, a row function in its family's
// rows_*.go and a list call in fetch.go — no route, no page. The order is the
// navigation's: what runs, what exposes it, what configures it, what stores its
// data, and what the cluster is made of.
var descriptors = []Descriptor{
	{
		Kind:       "pods",
		Group:      "Workloads",
		Title:      "Pods",
		Namespaced: true,
		Columns: []Column{
			// Containers, Controlled by and QoS sit where Lens puts them.
			// Controlled by is not filterable: one value per owner is too many.
			col("Name"), col("Namespace"), numCol("Ready"), filterCol("Status"),
			numCol("Restarts"), numCol("CPU"), numCol("Memory"),
			numCol("Containers"), sideCol("Controlled by"), filterCol("Node"), filterCol("QoS"),
			numCol("Age"),
		},
		refresh: liveRefresh,
		list:    rendersLive(fetchPods, podRow),
	},
	{
		// No "Controlled by" here, nor on stateful sets, daemon sets or cron
		// jobs: these are applied by an operator and nearly never owned. Pods,
		// replica sets and jobs, nearly always owned, have it.
		Kind:       "deployments",
		Group:      "Workloads",
		Title:      "Deployments",
		Namespaced: true,
		Columns: []Column{
			col("Name"), col("Namespace"), numCol("Ready"), numCol("Up-to-date"),
			numCol("Available"), col("Image"), numCol("Age"),
		},
		refresh: reconcilingRefresh,
		list:    renders(fetchDeployments, deploymentRow),
	},
	{
		Kind:       "statefulsets",
		Group:      "Workloads",
		Title:      "Stateful sets",
		Namespaced: true,
		Columns: []Column{
			col("Name"), col("Namespace"), numCol("Ready"), numCol("Up-to-date"),
			col("Image"), numCol("Age"),
		},
		refresh: reconcilingRefresh,
		list:    renders(fetchStatefulSets, statefulSetRow),
	},
	{
		Kind:       "daemonsets",
		Group:      "Workloads",
		Title:      "Daemon sets",
		Namespaced: true,
		Columns: []Column{
			col("Name"), col("Namespace"), numCol("Ready"), numCol("Up-to-date"),
			numCol("Available"), col("Image"), numCol("Age"),
		},
		refresh: reconcilingRefresh,
		list:    renders(fetchDaemonSets, daemonSetRow),
	},
	{
		Kind:       "replicasets",
		Group:      "Workloads",
		Title:      "Replica sets",
		Namespaced: true,
		Columns: []Column{
			col("Name"), col("Namespace"), numCol("Ready"), numCol("Current"),
			col("Image"), sideCol("Controlled by"), numCol("Age"),
		},
		refresh: reconcilingRefresh,
		list:    renders(fetchReplicaSets, replicaSetRow),
	},
	{
		Kind:       "jobs",
		Group:      "Workloads",
		Title:      "Jobs",
		Namespaced: true,
		Columns: []Column{
			col("Name"), col("Namespace"), filterCol("Status"), numCol("Completions"),
			numCol("Duration"), sideCol("Controlled by"), numCol("Age"),
		},
		refresh: reconcilingRefresh,
		list:    renders(fetchJobs, jobRow),
	},
	{
		Kind:       "cronjobs",
		Group:      "Workloads",
		Title:      "Cron jobs",
		Namespaced: true,
		Columns: []Column{
			// Last run and Next run as a pair: one is only readable beside the
			// other. Last result says how that run ended.
			col("Name"), col("Namespace"), col("Schedule"), filterCol("State"),
			numCol("Active"), numCol("Last run"), filterCol("Last result"), numCol("Next run"), numCol("Age"),
		},
		refresh: reconcilingRefresh,
		list:    renders(fetchCronJobs, cronJobRow),
	},
	{
		// Last in Workloads: a legacy kind, rarely looked for.
		Kind:       "replicationcontrollers",
		Group:      "Workloads",
		Title:      "Replication controllers",
		Namespaced: true,
		Columns: []Column{
			col("Name"), col("Namespace"), numCol("Ready"), numCol("Current"),
			col("Image"), numCol("Age"),
		},
		refresh: reconcilingRefresh,
		list:    renders(fetchReplicationControllers, replicationControllerRow),
	},
	{
		Kind:       "services",
		Group:      "Network",
		Title:      "Services",
		Namespaced: true,
		Columns: []Column{
			// Cluster IP and External are addresses to paste elsewhere. See
			// Column.Copyable.
			col("Name"), col("Namespace"), filterCol("Type"), addrCol("Cluster IP"),
			addrCol("External"), col("Ports"), numCol("Age"),
		},
		// Reconciling, not declared: a cloud controller writes External after
		// the Service is applied.
		refresh: reconcilingRefresh,
		list:    renders(fetchServices, serviceRow),
	},
	{
		Kind:       "ingresses",
		Group:      "Network",
		Title:      "Ingresses",
		Namespaced: true,
		Columns: []Column{
			// Hosts and Address both copy; only Hosts links (see ingressHosts and
			// ingressAddress). Address is secondary: it is looked up when writing a
			// DNS record, not read down the table.
			col("Name"), col("Namespace"), filterCol("Class"), addrCol("Hosts"),
			secondary(addrCol("Address")), numCol("Age"),
		},
		// As for services: the controller writes Address after apply.
		refresh: reconcilingRefresh,
		list:    renders(fetchIngresses, ingressRow),
	},
	{
		Kind:       "endpoints",
		Group:      "Network",
		Title:      "Endpoints",
		Namespaced: true,
		Columns: []Column{
			col("Name"), col("Namespace"), col("Addresses"), numCol("Ready"),
			numCol("Not ready"), numCol("Age"),
		},
		refresh: reconcilingRefresh,
		list:    renders(fetchEndpoints, endpointsRow),
	},
	{
		// Beside endpoints, not instead: Endpoints answers "is anything behind
		// this Service"; slices are what the cluster keeps, with the Service,
		// the address family and the split the projection loses. See
		// fetchEndpoints.
		Kind:       "endpointslices",
		Group:      "Network",
		Title:      "Endpoint slices",
		Namespaced: true,
		Columns: []Column{
			col("Name"), col("Namespace"), filterCol("Service"), filterCol("Address type"),
			numCol("Ready"), col("Ports"), numCol("Age"),
		},
		refresh: reconcilingRefresh,
		list:    renders(fetchEndpointSlices, endpointSliceRow),
	},
	{
		Kind:       "ingressclasses",
		Group:      "Network",
		Title:      "Ingress classes",
		Namespaced: false,
		Columns: []Column{
			col("Name"), col("Controller"), col("Default"), numCol("Age"),
		},
		refresh: declaredRefresh,
		list:    renders(fetchIngressClasses, ingressClassRow),
	},
	{
		Kind:       "networkpolicies",
		Group:      "Network",
		Title:      "Network policies",
		Namespaced: true,
		Columns: []Column{
			col("Name"), col("Namespace"), col("Selects"),
			numCol("Ingress"), numCol("Egress"), numCol("Age"),
		},
		// Declared: a policy has no status, so only an apply changes a row.
		refresh: declaredRefresh,
		list:    renders(fetchNetworkPolicies, networkPolicyRow),
	},
	{
		Kind:       "configmaps",
		Group:      "Configuration",
		Title:      "Config maps",
		Namespaced: true,
		Columns: []Column{
			col("Name"), col("Namespace"), numCol("Keys"), col("Names"), numCol("Age"),
		},
		// Every value is read to count keys, and a ConfigMap changes on deploy.
		refresh: declaredRefresh,
		list:    renders(fetchConfigMaps, configMapRow),
	},
	{
		// Columns chosen so that no cell can hold a secret value; see secretRow.
		Kind:       "secrets",
		Group:      "Configuration",
		Title:      "Secrets",
		Namespaced: true,
		Columns: []Column{
			col("Name"), col("Namespace"), filterCol("Type"), numCol("Keys"),
			col("Key names"), numCol("Age"),
		},
		// This list carries every secret's plaintext through this process to
		// render the Keys column; a Secret's rate of change deserves no more.
		refresh: declaredRefresh,
		list:    renders(fetchSecrets, secretRow),
	},
	{
		Kind:       "resourcequotas",
		Group:      "Configuration",
		Title:      "Resource quotas",
		Namespaced: true,
		Columns: []Column{
			// "Highest" is what the row sorts and colors on: at 100% every
			// further create in that namespace fails.
			col("Name"), col("Namespace"), numCol("Resources"), col("Used of hard"),
			numCol("Highest"), numCol("Age"),
		},
		// Used-of-hard moves with every create in the namespace.
		refresh: reconcilingRefresh,
		list:    renders(fetchResourceQuotas, resourceQuotaRow),
	},
	{
		Kind:       "limitranges",
		Group:      "Configuration",
		Title:      "Limit ranges",
		Namespaced: true,
		Columns: []Column{
			col("Name"), col("Namespace"), col("Types"), numCol("Limits"),
			col("Constraints"), numCol("Age"),
		},
		refresh: declaredRefresh,
		list:    renders(fetchLimitRanges, limitRangeRow),
	},
	{
		Kind:       "horizontalpodautoscalers",
		Group:      "Configuration",
		Title:      "Horizontal pod autoscalers",
		Namespaced: true,
		Columns: []Column{
			// Replicas, the bounds, and the metric with its current value: what
			// answers "why is this not scaling".
			col("Name"), col("Namespace"), col("Reference"), numCol("Replicas"),
			numCol("Min"), numCol("Max"), col("Metrics"), filterCol("Status"), numCol("Age"),
		},
		refresh: reconcilingRefresh,
		list:    renders(fetchHPAs, hpaRow),
	},
	{
		Kind:       "poddisruptionbudgets",
		Group:      "Configuration",
		Title:      "Pod disruption budgets",
		Namespaced: true,
		Columns: []Column{
			col("Name"), col("Namespace"), col("Budget"), col("Selects"),
			numCol("Healthy"), numCol("Allowed"), numCol("Age"),
		},
		refresh: reconcilingRefresh,
		list:    renders(fetchPDBs, pdbRow),
	},
	{
		Kind:       "priorityclasses",
		Group:      "Configuration",
		Title:      "Priority classes",
		Namespaced: false,
		Columns: []Column{
			col("Name"), numCol("Value"), col("Global default"), filterCol("Preemption"),
			col("Description"), numCol("Age"),
		},
		refresh: declaredRefresh,
		list:    renders(fetchPriorityClasses, priorityClassRow),
	},
	{
		Kind:       "runtimeclasses",
		Group:      "Configuration",
		Title:      "Runtime classes",
		Namespaced: false,
		Columns: []Column{
			col("Name"), filterCol("Handler"), col("Overhead"), col("Node selector"),
			numCol("Age"),
		},
		refresh: declaredRefresh,
		list:    renders(fetchRuntimeClasses, runtimeClassRow),
	},
	{
		Kind:       "leases",
		Group:      "Configuration",
		Title:      "Leases",
		Namespaced: true,
		Columns: []Column{
			// A lease no longer renewed is a controller that has stopped, and
			// nothing else in the API says so.
			col("Name"), col("Namespace"), col("Holder"), numCol("Renewed"),
			numCol("Duration"), numCol("Age"),
		},
		// Watching a lease lapse needs seconds: the default duration is fifteen.
		refresh: reconcilingRefresh,
		list:    renders(fetchLeases, leaseRow),
	},
	{
		Kind:       "mutatingwebhookconfigurations",
		Group:      "Configuration",
		Title:      "Mutating webhooks",
		Namespaced: false,
		Columns: []Column{
			col("Name"), numCol("Webhooks"), filterCol("Failure policy"), col("Backend"),
			numCol("Age"),
		},
		refresh: declaredRefresh,
		list:    renders(fetchMutatingWebhooks, mutatingWebhookRow),
	},
	{
		Kind:       "validatingwebhookconfigurations",
		Group:      "Configuration",
		Title:      "Validating webhooks",
		Namespaced: false,
		Columns: []Column{
			col("Name"), numCol("Webhooks"), filterCol("Failure policy"), col("Backend"),
			numCol("Age"),
		},
		refresh: declaredRefresh,
		list:    renders(fetchValidatingWebhooks, validatingWebhookRow),
	},
	{
		// The group is Access Control, not Security: it holds who may do what,
		// with no scanning or findings.
		Kind:       "serviceaccounts",
		Group:      "Access Control",
		Title:      "Service accounts",
		Namespaced: true,
		Columns: []Column{
			col("Name"), col("Namespace"), numCol("Secrets"), col("Names"), numCol("Age"),
		},
		refresh: declaredRefresh,
		list:    renders(fetchServiceAccounts, serviceAccountRow),
	},
	{
		Kind:       "clusterroles",
		Group:      "Access Control",
		Title:      "Cluster roles",
		Namespaced: false,
		Columns: []Column{
			// A rule count and a wildcard flag; see ruleSummary for why only `*`
			// on both verbs and resources is ranked.
			col("Name"), numCol("Rules"), col("Resources"), col("Wildcards"),
			numCol("Age"),
		},
		refresh: declaredRefresh,
		list:    renders(fetchClusterRoles, clusterRoleRow),
	},
	{
		Kind:       "roles",
		Group:      "Access Control",
		Title:      "Roles",
		Namespaced: true,
		Columns: []Column{
			col("Name"), col("Namespace"), numCol("Rules"), col("Resources"),
			col("Wildcards"), numCol("Age"),
		},
		refresh: declaredRefresh,
		list:    renders(fetchRoles, roleRow),
	},
	{
		Kind:       "clusterrolebindings",
		Group:      "Access Control",
		Title:      "Cluster role bindings",
		Namespaced: false,
		Columns: []Column{
			// The role and the subjects; the subject list is capped and says how
			// many it left out.
			col("Name"), filterCol("Role"), numCol("Subjects"), col("Bound to"), numCol("Age"),
		},
		refresh: declaredRefresh,
		list:    renders(fetchClusterRoleBindings, clusterRoleBindingRow),
	},
	{
		Kind:       "rolebindings",
		Group:      "Access Control",
		Title:      "Role bindings",
		Namespaced: true,
		Columns: []Column{
			col("Name"), col("Namespace"), filterCol("Role"), numCol("Subjects"),
			col("Bound to"), numCol("Age"),
		},
		refresh: declaredRefresh,
		list:    renders(fetchRoleBindings, roleBindingRow),
	},
	{
		Kind:       "persistentvolumeclaims",
		Group:      "Storage",
		Title:      "Persistent volume claims",
		Namespaced: true,
		Columns: []Column{
			// Used sits after Capacity: the two are one reading. Gauged because
			// the kubelet's answer arrives after the first paint (Column.Gauged).
			col("Name"), col("Namespace"), filterCol("Status"), numCol("Capacity"),
			gaugeCol("Used"), filterCol("Access"), col("Volume"),
			filterCol("Storage class"), numCol("Age"),
		},
		refresh: reconcilingRefresh,
		// rendersLive: Used comes from a kubelet, not the claim; the other
		// columns draw without `nodes/proxy`.
		list: rendersLive(fetchPVCs, pvcRow),
	},
	{
		Kind:       "persistentvolumes",
		Group:      "Storage",
		Title:      "Persistent volumes",
		Namespaced: false,
		Columns: []Column{
			// The claim's reading: a PV and its PVC are one filesystem, keyed
			// through `spec.claimRef` (see volumeUsed).
			col("Name"), filterCol("Status"), numCol("Capacity"), gaugeCol("Used"),
			filterCol("Access"), filterCol("Reclaim"), col("Claim"),
			filterCol("Storage class"), numCol("Age"),
		},
		// Status and Claim change when a claim binds or is deleted.
		refresh: reconcilingRefresh,
		list:    rendersLive(fetchPVs, pvRow),
	},
	{
		Kind:       "storageclasses",
		Group:      "Storage",
		Title:      "Storage classes",
		Namespaced: false,
		Columns: []Column{
			// Default matters: two defaults, or none, is a cluster problem shown
			// only here.
			col("Name"), filterCol("Provisioner"), filterCol("Reclaim"), filterCol("Binding mode"),
			col("Default"), numCol("Age"),
		},
		refresh: declaredRefresh,
		list:    renders(fetchStorageClasses, storageClassRow),
	},
	{
		Kind:       "nodes",
		Group:      "",
		Title:      "Nodes",
		Namespaced: false,
		Columns: []Column{
			// CPU, Memory and Disk are read together as how full a machine is;
			// Disk, often unmeasured, goes last.
			col("Name"), filterCol("Status"), numCol("Taints"), gaugeCol("CPU"),
			gaugeCol("Memory"), gaugeCol("Disk"), filterCol("Version"), addrCol("Internal IP"),
			filterCol("Instance type"), numCol("Age"),
		},
		refresh: liveRefresh,
		list:    rendersLive(fetchNodes, nodeRow),
	},
	{
		Kind:       "namespaces",
		Group:      "",
		Title:      "Namespaces",
		Namespaced: false,
		Columns:    []Column{col("Name"), filterCol("Status"), numCol("Age")},
		// A namespace stuck Terminating is often what this list is watched for.
		refresh: reconcilingRefresh,
		list:    renders(fetchNamespaces, namespaceRow),
	},
	{
		Kind:       "events",
		Group:      "",
		Title:      "Events",
		Namespaced: true,
		Columns: []Column{
			col("Namespace"), filterCol("Type"), filterCol("Reason"), col("Object"),
			col("Message"), numCol("Count"), numCol("Age"),
		},
		// Not live: events record what already happened, internal/archive
		// keeps them, and this is the API server's noisiest list.
		refresh:     reconcilingRefresh,
		list:        renders(fetchEvents, eventRow),
		recentFirst: true,
	},
}

// aliases: the singular, and the abbreviation kubectl accepts, since people
// type kinds the way kubectl taught them.
var aliases = map[string]string{
	"po": "pods", "pod": "pods",
	"deploy": "deployments", "deployment": "deployments",
	"sts": "statefulsets", "statefulset": "statefulsets",
	"ds": "daemonsets", "daemonset": "daemonsets",
	"rs": "replicasets", "replicaset": "replicasets",
	"job": "jobs",
	"cj":  "cronjobs", "cronjob": "cronjobs",
	"svc": "services", "service": "services",
	"ing": "ingresses", "ingress": "ingresses",
	"ep": "endpoints", "endpoint": "endpoints",
	"cm": "configmaps", "configmap": "configmaps",
	"secret": "secrets",
	"sa":     "serviceaccounts", "serviceaccount": "serviceaccounts",
	"pvc": "persistentvolumeclaims", "persistentvolumeclaim": "persistentvolumeclaims",
	"pv": "persistentvolumes", "persistentvolume": "persistentvolumes",
	"no": "nodes", "node": "nodes",
	"ns": "namespaces", "namespace": "namespaces",
	"ev": "events", "event": "events",
	"rc": "replicationcontrollers", "replicationcontroller": "replicationcontrollers",
	"eps": "endpointslices", "endpointslice": "endpointslices",
	"ingressclass": "ingressclasses",
	"netpol":       "networkpolicies", "networkpolicy": "networkpolicies",
	"quota": "resourcequotas", "resourcequota": "resourcequotas",
	"limits": "limitranges", "limitrange": "limitranges",
	"hpa": "horizontalpodautoscalers", "horizontalpodautoscaler": "horizontalpodautoscalers",
	"pdb": "poddisruptionbudgets", "poddisruptionbudget": "poddisruptionbudgets",
	"pc": "priorityclasses", "priorityclass": "priorityclasses",
	"runtimeclass": "runtimeclasses",
	"lease":        "leases",
	"mwc":          "mutatingwebhookconfigurations", "mutatingwebhookconfiguration": "mutatingwebhookconfigurations",
	"vwc": "validatingwebhookconfigurations", "validatingwebhookconfiguration": "validatingwebhookconfigurations",
	"sc": "storageclasses", "storageclass": "storageclasses",
	"clusterrole": "clusterroles", "role": "roles",
	"clusterrolebinding": "clusterrolebindings", "rolebinding": "rolebindings",
}
