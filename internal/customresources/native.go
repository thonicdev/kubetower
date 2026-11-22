package customresources

// The project's kinds that the API server serves itself and the console does
// not type by hand. No CRD describes them, so their printer columns are chosen
// here, each for a reason given beside it, and rendered like a CRD's
// (render.go).
//
// A row names the versions its field paths were checked against. At another
// version, or for a served project kind not in this table, the entry is still
// offered, with Name, Namespace and Age alone rather than columns of dashes.

import "time"

// How often a native project kind's list re-reads itself: kinds.go's own
// bands (a test holds the figures together).
const (
	reconcilingRefresh = 5 * time.Second
	declaredRefresh    = 60 * time.Second
)

// nativeKind is one row: a group and resource, the versions its columns were
// checked against, and the columns.
type nativeKind struct {
	group, resource string
	versions        []string
	refresh         time.Duration
	columns         []PrinterColumn
}

func column(name, path string) PrinterColumn {
	return PrinterColumn{Name: name, Type: "string", JSONPath: path}
}

func numberColumn(name, path string) PrinterColumn {
	return PrinterColumn{Name: name, Type: "integer", JSONPath: path}
}

func dateColumn(name, path string) PrinterColumn {
	return PrinterColumn{Name: name, Type: "date", JSONPath: path}
}

// natives is the table, in the order the menu lists them within a section.
var natives = []nativeKind{
	// --- Workloads -------------------------------------------------------------
	{
		// What the template would run, which is all a PodTemplate is.
		group: "", resource: "podtemplates", versions: []string{"v1"},
		refresh: declaredRefresh,
		columns: []PrinterColumn{
			column("Containers", ".template.spec.containers[*].name"),
			column("Images", ".template.spec.containers[*].image"),
		},
	},
	{
		// Read for which DaemonSet or StatefulSet and which revision; written
		// on every rollout, so it moves with the workloads.
		group: "apps", resource: "controllerrevisions", versions: []string{"v1"},
		refresh: reconcilingRefresh,
		columns: []PrinterColumn{
			column("Controlled by", ".metadata.ownerReferences[?(@.controller==true)].name"),
			column("Controller kind", ".metadata.ownerReferences[?(@.controller==true)].kind"),
			numberColumn("Revision", ".revision"),
		},
	},

	// --- Network ---------------------------------------------------------------
	{
		// A ServiceCIDR that is not Ready is a range no Service can be given
		// an address in.
		group: "networking.k8s.io", resource: "servicecidrs", versions: []string{"v1", "v1beta1"},
		refresh: declaredRefresh,
		columns: []PrinterColumn{
			column("CIDRs", ".spec.cidrs[*]"),
			column("Ready", `.status.conditions[?(@.type=="Ready")].status`),
		},
	},
	{
		// The name is the address; the question is what holds it, in three
		// columns because a JSONPath cannot join them.
		group: "networking.k8s.io", resource: "ipaddresses", versions: []string{"v1", "v1beta1"},
		refresh: reconcilingRefresh,
		columns: []PrinterColumn{
			column("Held by", ".spec.parentRef.resource"),
			column("Parent namespace", ".spec.parentRef.namespace"),
			column("Parent", ".spec.parentRef.name"),
		},
	},

	// --- Configuration ---------------------------------------------------------
	{
		// What happens when it cannot decide, and what it is parameterized
		// by. The CEL expressions cannot fit a cell.
		group: "admissionregistration.k8s.io", resource: "validatingadmissionpolicies", versions: []string{"v1"},
		refresh: declaredRefresh,
		columns: []PrinterColumn{
			column("Failure policy", ".spec.failurePolicy"),
			column("Param kind", ".spec.paramKind.kind"),
		},
	},
	{
		// The policy it applies, with which parameters, and what a violation
		// does (Deny, Warn or Audit).
		group: "admissionregistration.k8s.io", resource: "validatingadmissionpolicybindings", versions: []string{"v1"},
		refresh: declaredRefresh,
		columns: []PrinterColumn{
			column("Policy", ".spec.policyName"),
			column("Param", ".spec.paramRef.name"),
			column("Actions", ".spec.validationActions[*]"),
		},
	},
	{
		// Plus whether it runs again after a later mutation, the one setting
		// that changes what a mutating chain does.
		group: "admissionregistration.k8s.io", resource: "mutatingadmissionpolicies", versions: []string{"v1", "v1beta1"},
		refresh: declaredRefresh,
		columns: []PrinterColumn{
			column("Failure policy", ".spec.failurePolicy"),
			column("Param kind", ".spec.paramKind.kind"),
			column("Reinvocation", ".spec.reinvocationPolicy"),
		},
	},
	{
		group: "admissionregistration.k8s.io", resource: "mutatingadmissionpolicybindings", versions: []string{"v1", "v1beta1"},
		refresh: declaredRefresh,
		columns: []PrinterColumn{
			column("Policy", ".spec.policyName"),
			column("Param", ".spec.paramRef.name"),
		},
	},
	{
		// Which aggregated APIs this API server forwards to and whether they
		// answer: an unavailable APIService is how "metrics-server is down"
		// looks, and the reason says why. A local one has no service.
		group: "apiregistration.k8s.io", resource: "apiservices", versions: []string{"v1"},
		refresh: reconcilingRefresh,
		columns: []PrinterColumn{
			column("Service namespace", ".spec.service.namespace"),
			column("Service", ".spec.service.name"),
			column("Available", `.status.conditions[?(@.type=="Available")].status`),
			column("Reason", `.status.conditions[?(@.type=="Available")].reason`),
		},
	},
	{
		// Which group, scope and versions a CRD declares. Each object carries
		// its whole schema, so the list is re-read at the slowest band.
		group: "apiextensions.k8s.io", resource: "customresourcedefinitions", versions: []string{"v1"},
		refresh: declaredRefresh,
		columns: []PrinterColumn{
			column("Group", ".spec.group"),
			column("Scope", ".spec.scope"),
			column("Versions", ".spec.versions[*].name"),
		},
	},
	{
		// The two facts behind "why was this throttled".
		group: "flowcontrol.apiserver.k8s.io", resource: "flowschemas", versions: []string{"v1"},
		refresh: declaredRefresh,
		columns: []PrinterColumn{
			column("Priority level", ".spec.priorityLevelConfiguration.name"),
			numberColumn("Precedence", ".spec.matchingPrecedence"),
			column("Distinguisher", ".spec.distinguisherMethod.type"),
		},
	},
	{
		// Its share of concurrency and its queues; exempt levels have
		// neither, and say so by their type.
		group: "flowcontrol.apiserver.k8s.io", resource: "prioritylevelconfigurations", versions: []string{"v1"},
		refresh: declaredRefresh,
		columns: []PrinterColumn{
			column("Type", ".spec.type"),
			numberColumn("Shares", ".spec.limited.nominalConcurrencyShares"),
			numberColumn("Queues", ".spec.limited.limitResponse.queuing.queues"),
		},
	},
	{
		// A claim is read for what it was given and who holds it.
		group: "resource.k8s.io", resource: "resourceclaims", versions: []string{"v1", "v1beta2"},
		refresh: reconcilingRefresh,
		columns: []PrinterColumn{
			column("Devices", ".status.allocation.devices.results[*].device"),
			column("Reserved for", ".status.reservedFor[*].name"),
		},
	},
	{
		// What a pod made from it asks for, by request name.
		group: "resource.k8s.io", resource: "resourceclaimtemplates", versions: []string{"v1", "v1beta2"},
		refresh: declaredRefresh,
		columns: []PrinterColumn{
			column("Requests", ".spec.spec.devices.requests[*].name"),
		},
	},
	{
		// No column beyond the name: a class is CEL selectors, and the name
		// is what a claim refers to.
		group: "resource.k8s.io", resource: "deviceclasses", versions: []string{"v1", "v1beta2"},
		refresh: declaredRefresh,
	},
	{
		// What a driver publishes per node; rewritten whenever its inventory
		// changes.
		group: "resource.k8s.io", resource: "resourceslices", versions: []string{"v1", "v1beta2"},
		refresh: reconcilingRefresh,
		columns: []PrinterColumn{
			column("Node", ".spec.nodeName"),
			column("Driver", ".spec.driver"),
			column("Pool", ".spec.pool.name"),
			column("Devices", ".spec.devices[*].name"),
		},
	},
	{
		// A taint is its key and what it does, on whose devices.
		group: "resource.k8s.io", resource: "devicetaintrules", versions: []string{"v1"},
		refresh: declaredRefresh,
		columns: []PrinterColumn{
			column("Driver", ".spec.deviceSelector.driver"),
			column("Key", ".spec.taint.key"),
			column("Effect", ".spec.taint.effect"),
		},
	},
	{
		// Who holds which lease's candidacy, at which version, and when it
		// last renewed.
		group: "coordination.k8s.io", resource: "leasecandidates", versions: []string{"v1beta1", "v1alpha2"},
		refresh: reconcilingRefresh,
		columns: []PrinterColumn{
			column("Lease", ".spec.leaseName"),
			column("Binary version", ".spec.binaryVersion"),
			dateColumn("Renewed", ".spec.renewTime"),
		},
	},
	{
		// Which resource is being rewritten, and whether that finished.
		group: "storagemigration.k8s.io", resource: "storageversionmigrations", versions: []string{"v1", "v1beta1"},
		refresh: reconcilingRefresh,
		columns: []PrinterColumn{
			column("Group", ".spec.resource.group"),
			column("Resource", ".spec.resource.resource"),
			column("Succeeded", `.status.conditions[?(@.type=="Succeeded")].status`),
		},
	},

	// --- Access Control --------------------------------------------------------
	{
		// The three things `kubectl certificate approve` is decided on.
		group: "certificates.k8s.io", resource: "certificatesigningrequests", versions: []string{"v1"},
		refresh: reconcilingRefresh,
		columns: []PrinterColumn{
			column("Signer", ".spec.signerName"),
			column("Requestor", ".spec.username"),
			column("Condition", ".status.conditions[*].type"),
		},
	},
	{
		// A bundle is published for one signer; the certificates are the rest.
		group: "certificates.k8s.io", resource: "clustertrustbundles", versions: []string{"v1", "v1beta1"},
		refresh: declaredRefresh,
		columns: []PrinterColumn{
			column("Signer", ".spec.signerName"),
		},
	},
	{
		// Which pod on which node asked which signer.
		group: "certificates.k8s.io", resource: "podcertificaterequests", versions: []string{"v1", "v1beta1"},
		refresh: reconcilingRefresh,
		columns: []PrinterColumn{
			column("Signer", ".spec.signerName"),
			column("Pod", ".spec.podName"),
			column("Node", ".spec.nodeName"),
		},
	},

	// --- Storage ---------------------------------------------------------------
	{
		// A pod stuck in ContainerCreating on a volume is read here.
		group: "storage.k8s.io", resource: "volumeattachments", versions: []string{"v1"},
		refresh: reconcilingRefresh,
		columns: []PrinterColumn{
			column("Attacher", ".spec.attacher"),
			column("Volume", ".spec.source.persistentVolumeName"),
			column("Node", ".spec.nodeName"),
			column("Attached", ".status.attached"),
		},
	},
	{
		// The switches that change what the kubelet does for this driver, and
		// the lifecycle it supports.
		group: "storage.k8s.io", resource: "csidrivers", versions: []string{"v1"},
		refresh: declaredRefresh,
		columns: []PrinterColumn{
			column("Attach required", ".spec.attachRequired"),
			column("Pod info on mount", ".spec.podInfoOnMount"),
			column("Storage capacity", ".spec.storageCapacity"),
			column("Modes", ".spec.volumeLifecycleModes[*]"),
		},
	},
	{
		// One per node, named after it: which drivers that node has registered.
		group: "storage.k8s.io", resource: "csinodes", versions: []string{"v1"},
		refresh: declaredRefresh,
		columns: []PrinterColumn{
			column("Drivers", ".spec.drivers[*].name"),
		},
	},
	{
		// What a driver says is left: the figures the scheduler places a
		// volume on.
		group: "storage.k8s.io", resource: "csistoragecapacities", versions: []string{"v1"},
		refresh: reconcilingRefresh,
		columns: []PrinterColumn{
			column("Storage class", ".storageClassName"),
			column("Capacity", ".capacity"),
			column("Maximum volume size", ".maximumVolumeSize"),
		},
	},
	{
		// Read like StorageClasses: which driver.
		group: "storage.k8s.io", resource: "volumeattributesclasses", versions: []string{"v1", "v1beta1"},
		refresh: declaredRefresh,
		columns: []PrinterColumn{
			column("Driver", ".driverName"),
		},
	},
}

// nativeAt is the table's row for a project kind, and its index (the menu
// order).
func nativeAt(group, resource string) (nativeKind, int, bool) {
	for i, n := range natives {
		if n.group == group && n.resource == resource {
			return n, i, true
		}
	}
	return nativeKind{}, 0, false
}
