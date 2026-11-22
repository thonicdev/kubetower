// Package security is the Security Center: what trivy-operator found in the
// images and the configuration of a cluster's workloads, read from the
// reports it writes as custom resources, behind its own per-cluster switch.
//
// # Reading without the weight
//
// A VulnerabilityReport carries every finding with its description, about a
// kibibyte per CVE: a cluster of a few thousand reports holds hundreds of
// mebibytes of them. So the overview, the column of the lists and the brief
// never read a report's body. They read the reports **as the API server's
// table** (ListQuery.Columns): the printer columns trivy-operator declares
// (repository, tag and the counts by severity) and each report's metadata,
// a few hundred bytes a row. Pods, ReplicaSets and Jobs are read the same way,
// for their owner references and labels alone.
//
// A report's body is read only on one workload's detail, by label selector in
// its namespace, summarized here - a title clipped, never the description,
// never a secret's matched text - and dropped as soon as it is summarized.
// The SBOM reports are read the same way, for their component counts, and
// their bodies only on one workload's `/sbom`, capped at MaxComponents. The
// signatures are the admission's annotation on the pods, already in their
// table, and the image verifications Kyverno 1.20 and later write in the
// policy reports instead, walked through Each and kept for those results
// alone: nothing here asks a registry or a transparency log.
// The one other body read is `/config`: the config audit reports of the
// workloads on one page of the table (at most MaxConfigRows), because a
// report's summary counts a failed check once per place it failed and the
// table shows each check ID once, as the detail does.
//
// # Who sees what
//
// Every read goes through Host.Definitions and Host.List, reviewed for the
// person behind the request; a person who may list reports only in some
// namespaces gets those, and the answer says it is partial. **Host.Client is
// never called**, every route is a GET, and nothing here writes. Nothing is
// cached between requests: an extra does not know who is asking, so a cache
// would hand one person what another may read.
//
// # What one request may cost
//
// At most four reads run at once and every one is waited for: no goroutine
// outlives its request. The answers are bounded (MaxWorkloads, MaxNamespaces,
// MaxImages, MaxCVEs), and a bound that cut something is said.
package security

import (
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
)

// Key is `security`: the page's URL segment, the KT_EXTRAS name and the key
// the per-cluster switch is stored under, so it is not free to change.
const Key = "security"

// Label is the menu entry.
const Label = "Security Center"

// group is trivy-operator's API group.
const group = "aquasecurity.github.io"

// The three report kinds this extra reads, as the source names the page
// draws, and their catalog slugs.
const (
	KindVulnerability = "vulnerabilityreports"
	KindConfigAudit   = "configauditreports"
	KindExposedSecret = "exposedsecretreports"
)

// reportKinds is the three, in the order the sources are drawn.
var reportKinds = []string{KindVulnerability, KindConfigAudit, KindExposedSecret}

// The bounds of one answer. A bound that cut something is said: capped and
// total on the workloads, cvesTotal on a detail.
const (
	// MaxWorkloads is the most workloads an overview lists, the worst first.
	MaxWorkloads = 5000
	// MaxNamespaces is the most namespaces in the overview's bars.
	MaxNamespaces = 100
	// MaxImages is the most images in the overview's top.
	MaxImages = 50
	// MaxCVEs is the most CVEs one workload's detail lists, the worst first.
	MaxCVEs = 2000
	// MaxRead is the most rows one read of one kind asks for. A read cut by
	// it is not complete, and its source says so.
	MaxRead = 20000
	// MaxTitle is the most characters of a finding's title sent: the title
	// is a label, and the description is never sent at all.
	MaxTitle = 160
	// parallel is how many reads one request runs at once.
	parallel = 4
)

// The trivy-operator labels a report carries about what it scanned
// (pkg/trivyoperator/constants.go at v0.36.1).
const (
	labelKind      = "trivy-operator.resource.kind"
	labelName      = "trivy-operator.resource.name"
	labelNamespace = "trivy-operator.resource.namespace"
	labelContainer = "trivy-operator.container.name"
	labelNameHash  = "trivy-operator.resource.name-hash"
)

var (
	podsGVR        = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	servicesGVR    = schema.GroupVersionResource{Version: "v1", Resource: "services"}
	replicaSetsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
	jobsGVR        = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	ingressesGVR   = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}
)

// Security is the extension `cmd/kubetower` registers. It holds nothing
// between requests but its clock.
type Security struct {
	now func() time.Time
}

// New is the constructor `cmd/kubetower` calls.
func New() Security { return Security{now: time.Now} }

func (Security) Key() string   { return Key }
func (Security) Label() string { return Label }

// Summary is the catalog entry.
func (Security) Summary() string {
	return "What trivy-operator found in the cluster's images and workloads: vulnerabilities by severity, " +
		"failed configuration checks and exposed secrets, the worst first, and which of them can be reached from outside; " +
		"and which Kyverno or Gatekeeper policies fail, and on what. Read-only."
}

// Needs is drawn beside the switch.
func (Security) Needs() string {
	return "trivy-operator, Kyverno or Gatekeeper; the right to list their reports (aquasecurity.github.io, " +
		"wgpolicyk8s.io, openreports.io, constraints.gatekeeper.sh) and Kyverno's policies (kyverno.io), " +
		"which Kubernetes' view role may not grant"
}

// Legacy is empty: the key has never been anything else.
func (Security) Legacy() []string { return nil }

// Routes are reads only, every one a GET: nothing here changes a cluster.
func (x Security) Routes(h extension.Host) []extension.Route {
	return []extension.Route{
		{Method: http.MethodGet, Pattern: "/{$}", Handler: x.overview(h)},
		{Method: http.MethodGet, Pattern: "/workload/{namespace}/{kind}/{name}", Handler: x.detail(h)},
		{Method: http.MethodGet, Pattern: "/workload/{namespace}/{kind}/{name}/sbom", Handler: x.sbom(h)},
		{Method: http.MethodGet, Pattern: "/brief/{namespace}/{name}", Handler: x.brief(h)},
		{Method: http.MethodGet, Pattern: "/cells", Handler: x.cells(h)},
		{Method: http.MethodGet, Pattern: "/policies", Handler: x.policies(h)},
		{Method: http.MethodGet, Pattern: "/policies/{engine}/{name}", Handler: x.policyDetail(h)},
		{Method: http.MethodGet, Pattern: "/cluster", Handler: x.findings(h, clusterKinds)},
		{Method: http.MethodGet, Pattern: "/rbac", Handler: x.findings(h, rbacKinds)},
		{Method: http.MethodGet, Pattern: "/compliance", Handler: x.compliance(h)},
		{Method: http.MethodGet, Pattern: "/config", Handler: x.config(h)},
	}
}

func (x Security) clock() time.Time {
	if x.now == nil {
		return time.Now()
	}
	return x.now()
}
