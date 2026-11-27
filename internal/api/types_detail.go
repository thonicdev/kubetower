package api

import (
	"time"
)

// --- Namespaces ---------------------------------------------------------------

// Namespace is one entry of the selector every list page carries.
type Namespace struct {
	Name string `json:"name"`
	// State is one namespace's own state, where the console knows one; usually
	// absent, since the list is read in one call.
	State State `json:"state,omitempty"`
}

// NamespaceReading is what a backend establishes about one cluster's namespaces,
// including a list kept from before the cluster stopped answering, which needs
// both the names and when they were read.
type NamespaceReading struct {
	Names []string
	// ReadAt is when this list was read, which makes a stale answer actionable.
	ReadAt time.Time
	// Stale marks a reading still served although the cluster has since stopped
	// answering. It becomes `ok` with `reason.code: stale_reading`, never a
	// failure.
	Stale bool
	// Why is the failure the stale reading stands in for; the qualifying reason
	// is worded from it. Ignored unless Stale.
	Why *Failure
}

// HelmReleaseReading is what a backend answers about the releases on one
// cluster: the rows, and AccessNote where the access review narrowed them to
// the namespaces the person may list Secrets in.
type HelmReleaseReading struct {
	Releases   []HelmRelease
	AccessNote string
	// Columns is the list's column arrangement, as a ListPage carries a
	// resource list's: the stored order, widths and visibility over the
	// declared columns. ColumnsCustomised says something is stored.
	Columns           []Column
	ColumnsCustomised bool
}

// HelmList is the release list envelope: List[HelmRelease], `accessNote` and
// the column arrangement beside it. The arm is the embedded List's, as
// NamespaceList's is.
type HelmList struct {
	List[HelmRelease]
	accessNote        string
	columns           []Column
	columnsCustomised bool
}

// NamespaceList is the namespace envelope: List[Namespace] and `readAt` beside
// it. The arm is decided by the embedded List only (List.arm).
type NamespaceList struct {
	List[Namespace]
	readAt *time.Time
}

// The two values of ListPage.MeasurementState. A table whose gauges are filled
// carries neither, and neither does a kind that has no gauges.
const (
	// MeasurementPending is the first read of the figures, started and not yet
	// back. Nothing has failed.
	MeasurementPending = "pending"
	// MeasurementUnmeasured is a read that was made and gave no figures. Why is
	// the health page's answer.
	MeasurementUnmeasured = "unmeasured"
)

// ListPage is the grid, generic over kind: one view model draws every kind, and
// only what fills the cells is per kind.
type ListPage struct {
	Kind       string          `json:"kind"`
	Label      string          `json:"label"`
	Namespaced bool            `json:"namespaced"`
	Columns    []Column        `json:"columns"`
	Rows       []Row           `json:"rows"`
	Bulk       *BulkAffordance `json:"bulk,omitempty"`
	// Create is whether the list's "+" may create an object of this kind,
	// reviewed for the person in the one namespace in view.
	Create *CreateAffordance `json:"create,omitempty"`
	// ReadOnlyNote says why writes are refused here, when they are.
	ReadOnlyNote string `json:"readOnlyNote,omitempty"`
	// Namespaces is what this list is narrowed to, echoed back. A list because
	// the control it feeds is a multi-selection.
	Namespaces []string `json:"namespaces,omitempty"`
	// UnknownNamespace names a selected namespace this cluster does not have, so
	// a typo does not read as an empty namespace.
	UnknownNamespace string `json:"unknownNamespace,omitempty"`
	// Search and Selector are the filters in effect, echoed back.
	Search   string `json:"search,omitempty"`
	Selector string `json:"selector,omitempty"`
	// Filters is the filter choices that took effect, each resolved to a column
	// key, so a client draws a chip for exactly what the server did.
	Filters []ListFilter `json:"filters,omitempty"`
	// Count is how many rows are drawn. Never omitted, including zero.
	Count int `json:"count"`
	// MeasurementNote is why this table's gauges are empty, on a kind that has
	// them, since a dash in a cell cannot say. It states no cause: the health
	// page names that, rather than a guess here.
	MeasurementNote string `json:"measurementNote,omitempty"`
	// MeasurementState is set exactly when MeasurementNote is:
	// MeasurementPending while the first read is on its way (it is never waited
	// for, so a first Nodes list has no figures), MeasurementUnmeasured once a
	// read gave none. They draw differently: a read in flight is not a failure.
	MeasurementState string `json:"measurementState,omitempty"`
	// AccessNote says the access review narrowed this list to the namespaces the
	// person may list (or withheld one it could not review), so an empty table
	// and a narrowed one differ. Built from the question, never the rows, so it
	// does not say how much was left out. Absent on a whole answer.
	AccessNote string `json:"accessNote,omitempty"`
	// AllowedNamespaces is, on a narrowed list, every namespace in which the
	// person may list this kind, each reviewed. Absent when the list was not
	// narrowed or the set could not be established, never meaning "none".
	AllowedNamespaces []string `json:"allowedNamespaces,omitempty"`
	// ColumnsCustomised says something is stored for this kind, which puts a
	// reset control on the panel.
	ColumnsCustomised bool `json:"columnsCustomised,omitempty"`
	// RefreshSeconds is how often this kind is worth re-reading, a property of
	// the kind: a Secrets list re-read every two seconds would push megabytes of
	// plaintext through this process.
	RefreshSeconds int `json:"refreshSeconds,omitempty"`
}

// --- Detail and drawer ------------------------------------------------------

// Tab is a drawer tab as data rather than as a branch, so a tab whose page is
// not installed degrades by being absent.
type Tab struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Endpoint string `json:"endpoint,omitempty"`
	Lazy     *bool  `json:"lazy,omitempty"`
}

// DetailField is one labeled fact. Its flags are intent and the client decides
// the appearance (as in `detail.Fact`). Prose matters most: the grid's cells do
// not wrap, so only this flag tells a client which cell may.
type DetailField struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Ref   *Ref   `json:"ref,omitempty"`
	// Prose says the value is a sentence the cluster wrote and may wrap. There
	// is no Masked here: masking lives on DataEntry.
	Prose bool `json:"prose,omitempty"`
	// Tone is how the value reads: normal, muted, ok, warn, bad.
	Tone string `json:"tone,omitempty"`
	// Pill asks for a status badge rather than colored text, so "Running" and
	// a node name do not look like the same class of thing.
	Pill bool `json:"pill,omitempty"`
	// Hint is what hovering says, for a value the reader would look up.
	Hint string `json:"hint,omitempty"`
	// Copy is what a copy control puts on the clipboard, where that is not the
	// drawn value.
	Copy string `json:"copy,omitempty"`
}

// ResourceAmount is one resource figure, already formatted.
type ResourceAmount struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// EnvSource is the ConfigMap or Secret one variable is read out of, structured
// because it is the address the reveal control asks for.
type EnvSource struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Key is the key in the source object, not the variable's name, which
	// carries any import prefix.
	Key string `json:"key"`
	// Optional decides what an absence means: without it the kubelet will not
	// start the container until the key exists; with it the variable is unset.
	Optional bool `json:"optional,omitempty"`
}

// EnvVar is one environment entry. A Secret's value is never here: it is asked
// for one key at a time through GET /api/c/{cluster}/env, the one door that
// reveals.
type EnvVar struct {
	Name string `json:"name"`
	// Value is the literal, where there is one, and never a resolved one.
	Value string `json:"value,omitempty"`
	// From describes a source that cannot be followed: "field spec.nodeName".
	From   string     `json:"from,omitempty"`
	Source *EnvSource `json:"source,omitempty"`
	// Secret marks a source that is a Secret, so the panel can say a reveal is
	// recorded before it is asked for.
	Secret bool `json:"secret,omitempty"`
	// Resolved is a ConfigMap value, read and drawn on open: a ConfigMap is not
	// a credential store.
	Resolved string `json:"resolved,omitempty"`
	// Held reports that Resolved was read; an empty Resolved may be a real empty
	// value.
	Held    bool `json:"held,omitempty"`
	Clipped bool `json:"clipped,omitempty"`
	// Problem is why there is no value, in words the operator can act on.
	Problem string `json:"problem,omitempty"`
	// Absence says what sort of absence Problem describes: unread, unset,
	// expected or unprintable, which are exclusive.
	Absence string `json:"absence,omitempty"`
}

// EnvImport is an `envFrom` naming a ConfigMap or a Secret. Keys and Problem
// tell an object with no keys from one that could not be read.
type EnvImport struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Prefix   string `json:"prefix,omitempty"`
	Optional bool   `json:"optional,omitempty"`
	Keys     int    `json:"keys,omitempty"`
	Problem  string `json:"problem,omitempty"`
	// Absence says what sort of answer Problem is: `unread`, or `expected` for
	// an optional import whose object does not exist; EnvVar's enumeration.
	Absence string `json:"absence,omitempty"`
}

// PodContainer is one container of a pod, init or otherwise. Container is the
// log picker's smaller type.
type PodContainer struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	// Init is always written, including when false: a stuck init container must
	// not read as a crashing app container.
	Init bool `json:"init"`
	// Ephemeral marks a container added to the running pod through its
	// `ephemeralcontainers` subresource (a debug container), listed after the
	// app containers. Target is the container whose processes it joined, when
	// one was named.
	Ephemeral bool   `json:"ephemeral,omitempty"`
	Target    string `json:"target,omitempty"`
	// PullPolicy is carried only where it is not the default for the tag.
	PullPolicy string `json:"pullPolicy,omitempty"`
	// Command and Args are the spec's `command` and `args`, one element per
	// argument and never joined. Both absent means the image's own ENTRYPOINT
	// and CMD run.
	Command  []string         `json:"command,omitempty"`
	Args     []string         `json:"args,omitempty"`
	Requests []ResourceAmount `json:"requests,omitempty"`
	Limits   []ResourceAmount `json:"limits,omitempty"`
	Env      []EnvVar         `json:"env,omitempty"`
	EnvFrom  []EnvImport      `json:"envFrom,omitempty"`
	// Imported is what the envFrom imports contributed, prefix applied, kept
	// apart from Env because it is less certainly known.
	Imported []EnvVar `json:"imported,omitempty"`

	// Probes is the three probes as declared, always all three, in the order
	// liveness, readiness, startup.
	Probes []ContainerProbe `json:"probes,omitempty"`
	// Status is what the kubelet last reported about this container, matched by
	// name. Never nil: an unreported container carries ContainerUnreported.
	Status *ContainerStatus `json:"status,omitempty"`
}

// ProbeState is whether a probe is declared. Three values rather than a nil
// probe, because a missing probe is a finding about the container and an
// unreadable one is a defect in the console.
type ProbeState string

const (
	// ProbeDeclared: the container declares this probe and Handler names it.
	ProbeDeclared ProbeState = "declared"
	// ProbeAbsent: the container declares no such probe, and the panel says so.
	ProbeAbsent ProbeState = "absent"
	// ProbeUnreadable: something is declared and this console cannot say what
	// (an unexpected shape or an unknown handler); Problem says which.
	ProbeUnreadable ProbeState = "unreadable"
)

// ContainerProbe is one of a container's three probes, as declared. The startup
// probe is its own entry: while it runs the kubelet holds the other two off.
type ContainerProbe struct {
	// Kind is "liveness", "readiness" or "startup".
	Kind  string     `json:"kind"`
	State ProbeState `json:"state"`
	// Handler is the sort of check: "httpGet", "exec", "tcpSocket" or "grpc".
	Handler string `json:"handler,omitempty"`
	// Target is what that check points at, rendered per handler:
	// "/healthz:8080", ":5432", the command of an exec.
	Target string `json:"target,omitempty"`
	// The four timings that decide behavior, in seconds or counts.
	// PeriodSeconds and FailureThreshold together are how long a container has
	// to recover.
	InitialDelaySeconds int `json:"initialDelaySeconds,omitempty"`
	PeriodSeconds       int `json:"periodSeconds,omitempty"`
	TimeoutSeconds      int `json:"timeoutSeconds,omitempty"`
	FailureThreshold    int `json:"failureThreshold,omitempty"`
	// Problem says why this console cannot name what is declared.
	Problem string `json:"problem,omitempty"`
}

// ContainerRunState is what the kubelet last said a container was doing: the
// three Kubernetes states, plus ContainerUnreported.
type ContainerRunState string

const (
	ContainerRunning    ContainerRunState = "running"
	ContainerWaiting    ContainerRunState = "waiting"
	ContainerTerminated ContainerRunState = "terminated"
	// ContainerUnreported: no status was reported for this container (a new or
	// unscheduled pod, or an unreadable status block). Not "not ready": Ready
	// says nothing here.
	ContainerUnreported ContainerRunState = "unreported"
)

// ContainerStatus is how one container is doing, from status.containerStatuses
// or status.initContainerStatuses, matched to the spec by name: neither list is
// in the spec's order, and a sidecar's status is filed under the init list.
type ContainerStatus struct {
	State ContainerRunState `json:"state"`
	// Ready is always written, including when false. Meaningful only where
	// State is not ContainerUnreported.
	Ready bool `json:"ready"`
	// Reason is the kubelet's own word (CrashLoopBackOff, OOMKilled…). Empty on
	// a container that is simply running.
	Reason string `json:"reason,omitempty"`
	// Message is the kubelet's sentence beside Reason, where it wrote one.
	Message string `json:"message,omitempty"`
	// Restarts is meaningful only where State is not ContainerUnreported.
	Restarts int `json:"restarts,omitempty"`
	// ExitCode is what the last run exited with, where State is
	// ContainerTerminated.
	ExitCode int `json:"exitCode,omitempty"`
	// Problem says why there is no status on this row. Present only where State is
	// ContainerUnreported.
	Problem string `json:"problem,omitempty"`
	// LastTermination is how the previous run ended (`lastState.terminated`).
	// Absent where the kubelet keeps none, which does not mean it never stopped.
	LastTermination *ContainerTermination `json:"lastTermination,omitempty"`
}

// ContainerTermination is one finished run of a container: the previous one,
// as the kubelet keeps it, however long ago.
type ContainerTermination struct {
	// ExitCode is written always, including 0: a clean exit is a fact here.
	ExitCode   int        `json:"exitCode"`
	Signal     int        `json:"signal,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	Message    string     `json:"message,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// Certificate is one x509 certificate as the panel needs it: public material
// only, and `tls.key` is never read.
type Certificate struct {
	// Position is 1-based within the bundle, so the panel can say which is
	// presented first.
	Position int `json:"position"`
	// Role is read off the certificate, not its position: a leaf is not a CA, a
	// root is a self-signed CA, an intermediate a CA that is not self-signed.
	Role       string   `json:"role"`
	Subject    string   `json:"subject"`
	Issuer     string   `json:"issuer"`
	SelfSigned bool     `json:"selfSigned,omitempty"`
	IsCA       bool     `json:"isCA,omitempty"`
	SANs       []string `json:"sans,omitempty"`
	// SANsNote says how many were left out.
	SANsNote  string     `json:"sansNote,omitempty"`
	NotBefore *time.Time `json:"notBefore,omitempty"`
	NotAfter  *time.Time `json:"notAfter,omitempty"`
	Expiry    string     `json:"expiry,omitempty"`
	// ExpiryTone grades the expiry against the certificate's own lifetime:
	// cert-manager renews at two thirds elapsed, so 30 days left is healthy on a
	// 90-day certificate and late on a one-year one.
	ExpiryTone         string `json:"expiryTone,omitempty"`
	Serial             string `json:"serial,omitempty"`
	SignatureAlgorithm string `json:"signatureAlgorithm,omitempty"`
	PublicKey          string `json:"publicKey,omitempty"`
}

// CertificateBundle is one data key's worth of PEM, often a leaf plus its
// intermediates.
type CertificateBundle struct {
	Key          string        `json:"key"`
	Certificates []Certificate `json:"certificates,omitempty"`
	// Note is why this bundle is empty, or what stopped the walk part-way,
	// beside the certificates that did parse, so a partial chain never reads as
	// whole.
	Note string `json:"note,omitempty"`
}

// CertificateSet is what the TLS material in a Secret or a ConfigMap says.
type CertificateSet struct {
	Bundles []CertificateBundle `json:"bundles,omitempty"`
	// PrivateKeys names the private-key entries this object carries, never
	// their values, which are not read.
	PrivateKeys []string `json:"privateKeys,omitempty"`
	Note        string   `json:"note,omitempty"`
}

// UsedBy is what the reverse walk established about who uses this ConfigMap or
// Secret, and nil on every other kind. The referrers are `Relation` rows under
// the role `Used by`; this is the answer.
//
// `empty` and a refusal must not render the same: an operator deletes an
// object on "nothing uses it". A walk that could not list one of its kinds and
// found nothing is `forbidden` or `unreadable`, never `empty`; one that found
// something stays `ok` and names the refused list in `relationsNote`. Even
// `empty` cannot prove absence: a ConfigMap picked up by label is named
// nowhere.
type UsedBy struct {
	State  State   `json:"state"`
	Reason *Reason `json:"reason,omitempty"`
}

// RuleSet is a Role's or a ClusterRole's whole rules block, with its own state.
// `empty` and `unreadable` are kept apart: a panel that silently shows fewer
// rules than exist is worse than one that refuses. `forbidden` arrives on the
// envelope, never here: reading the rules is reading the object.
type RuleSet struct {
	State  State        `json:"state"`
	Reason *Reason      `json:"reason,omitempty"`
	Rules  []PolicyRule `json:"rules,omitempty"`
	// Note is what the panel must admit about what it drew: a `*` is shown as
	// stored, not resolved, and an aggregated ClusterRole's rules move with the
	// roles it selects.
	Note string `json:"note,omitempty"`
	// Selectors are an aggregated ClusterRole's label selectors, one list of
	// requirements per selector. Absent on any other object.
	Selectors [][]string `json:"selectors,omitempty"`
}

// PolicyRule is one entry of the object's `rules` array, already graded. Every
// list is nil where the rule carries none, so a `nonResourceURLs`-only rule has
// no resources rather than an empty cell.
type PolicyRule struct {
	APIGroups       []string `json:"apiGroups,omitempty"`
	Resources       []string `json:"resources,omitempty"`
	Verbs           []string `json:"verbs,omitempty"`
	ResourceNames   []string `json:"resourceNames,omitempty"`
	NonResourceURLs []string `json:"nonResourceURLs,omitempty"`
	// Wildcards is where a `*` fell, in the Access Control grid's words, and
	// WildcardTone its grade: warn on any wildcard, bad when `*` falls on both
	// verbs and resources. Sent so the panel and the grid cannot disagree.
	Wildcards    string `json:"wildcards,omitempty"`
	WildcardTone string `json:"wildcardTone,omitempty"`
}

// EventSubject is an Event read as the sentence it is, rather than its object
// metadata.
type EventSubject struct {
	// Message is never clipped; the list clips it to 140 characters.
	Message string `json:"message"`
	Reason  string `json:"reason,omitempty"`
	Type    string `json:"type,omitempty"`
	Action  string `json:"action,omitempty"`
	Source  string `json:"source,omitempty"`
	Node    string `json:"node,omitempty"`
	Count   int32  `json:"count,omitempty"`
	// First and Last are both carried: only the pair tells a long repetition
	// from a burst.
	First *time.Time `json:"first,omitempty"`
	Last  *time.Time `json:"last,omitempty"`
	// Object is "Pod/api-7d9f", already composed.
	Object      string `json:"object,omitempty"`
	ObjectField string `json:"objectField,omitempty"`
}

// ObjectPort is one port an object exposes. `Forwardable` is the server's
// answer, never derived from `Protocol` by a client, so a button is never
// offered that the server would refuse.
type ObjectPort struct {
	Name string `json:"name,omitempty"`
	Port int32  `json:"port"`
	// TargetPort is a Service's pod-side port: a number or the name of a
	// container port.
	TargetPort string `json:"targetPort,omitempty"`
	NodePort   int32  `json:"nodePort,omitempty"`
	HostPort   int32  `json:"hostPort,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
	// AppProtocol exists on a Service port only and is the authoritative answer
	// to whether it speaks TLS, from which the server picks a forward's scheme.
	AppProtocol string  `json:"appProtocol,omitempty"`
	Container   string  `json:"container,omitempty"`
	Forwardable bool    `json:"forwardable,omitempty"`
	Reason      *Reason `json:"reason,omitempty"`
}

// ObjectURL is an address a person can open. Only Ingresses have them: a scheme
// for a LoadBalancer Service would be a guess.
type ObjectURL struct {
	Role string `json:"role,omitempty"`
	// Address is what to open, scheme included, and https only where the host is
	// covered by the Ingress's own spec.tls.
	Address string `json:"address"`
	Host    string `json:"host,omitempty"`
	Path    string `json:"path,omitempty"`
	TLS     bool   `json:"tls,omitempty"`
	Service string `json:"service,omitempty"`
	Note    string `json:"note,omitempty"`
}

// Relation is another object this one points at, and why (the graph is
// internal/detail/relations.go).
//
// Namespace follows the target's scope, not the subject's: a cluster-scoped
// target carries none. Linkable is the server's answer and a client must not
// re-derive it.
type Relation struct {
	Role      string   `json:"role"`
	Kind      string   `json:"kind,omitempty"`
	Namespace string   `json:"namespace,omitempty"`
	Name      string   `json:"name,omitempty"`
	Label     string   `json:"label"`
	Detail    string   `json:"detail,omitempty"`
	Mounts    []string `json:"mounts,omitempty"`
	Selector  string   `json:"selector,omitempty"`
	// Linkable false draws the relation inert, with no href, rather than as a
	// link that fails.
	Linkable bool `json:"linkable"`
}

// DetailSection is one block of the drawer, with its own state.
type DetailSection struct {
	Key    string        `json:"key"`
	Label  string        `json:"label"`
	State  State         `json:"state,omitempty"`
	Reason *Reason       `json:"reason,omitempty"`
	Fields []DetailField `json:"fields,omitempty"`
}

// DetailAction is a verb offered on this object, already answered permitted or
// not.
type DetailAction struct {
	Verb        string `json:"verb"`
	Label       string `json:"label"`
	Permitted   bool   `json:"permitted"`
	Destructive *bool  `json:"destructive,omitempty"`
	// Irreversible marks a verb the object does not survive. A list row's menu
	// colors by it; the drawer's chips, the selection bar and the confirmation
	// color by Destructive.
	Irreversible *bool   `json:"irreversible,omitempty"`
	Reason       *Reason `json:"reason,omitempty"`
}

// ShellOffer is whether a pod's page draws Shell, answered for this pod. Present
// only under the server profile; on the desktop `Capabilities.PodExec` decides.
//
// Permitted false always carries the Reason: the console could not ask, or the
// person may and its ServiceAccount may not. A person who may not gets no offer
// at all.
type ShellOffer struct {
	Permitted bool    `json:"permitted"`
	Reason    *Reason `json:"reason,omitempty"`
}

// DetailPage is one object in the shape the drawer draws.
type DetailPage struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Title     string `json:"title,omitempty"`
	Tabs      []Tab  `json:"tabs"`
	// Deleting says the object carries a deletion timestamp, held only by its
	// grace period or a finalizer. A client re-reads it until it is gone.
	Deleting bool `json:"deleting,omitempty"`
	// Event is set when the object is an Event, nil otherwise. Events below are
	// the events about this object.
	Event *EventSubject `json:"event,omitempty"`
	// Events are what the cluster still remembers happening to this object,
	// newest first, and EventsNote is why there are none or few. Fetched with
	// Sections, so their Tab carries no endpoint.
	Events     []ObjectEvent `json:"events,omitempty"`
	EventsNote string        `json:"eventsNote,omitempty"`
	// Metrics is where this object's charts are read from, "" on kinds with
	// none. Drawn in the details panel but its own address: the range queries
	// take a second or more and must not delay this payload.
	Metrics string `json:"metrics,omitempty"`
	// Data is where this object's key/value block is read from, "" on every
	// kind but ConfigMap and Secret. Drawn in the details panel but its own
	// address: the values are unbounded, it is what `invalidateActedObjects`
	// refreshes after a write, and it carries the write's `resourceVersion`.
	Data     string          `json:"data,omitempty"`
	Sections []DetailSection `json:"sections"`
	// Labels are the object's own, as a map, drawn as chips.
	Labels map[string]string `json:"labels,omitempty"`
	// Annotations are the object's own, already treated, never raw.
	// `kubectl.kubernetes.io/last-applied-configuration` is dropped: on a Secret
	// it holds every value in clear. On a Secret every other value is masked and
	// the keys are kept (internal/detail/annotations.go).
	Annotations map[string]string `json:"annotations,omitempty"`
	// Certificates is what the TLS material in this object says, nil where
	// there is none.
	Certificates *CertificateSet `json:"certificates,omitempty"`
	// Rules is what a Role or a ClusterRole grants, nil on every other kind.
	// It costs no cluster read.
	Rules *RuleSet `json:"rules,omitempty"`
	// URLs are addresses a person can open. Ingresses have them; nothing else
	// does.
	URLs     []ObjectURL `json:"urls,omitempty"`
	URLsNote string      `json:"urlsNote,omitempty"`
	// Ports are what a pod or a Service exposes, nil elsewhere; a TCP port's row
	// is where a forward is opened from.
	Ports     []ObjectPort `json:"ports,omitempty"`
	PortsNote string       `json:"portsNote,omitempty"`
	// Containers is what a pod is made of, init containers first. Nil for every
	// other kind.
	Containers []PodContainer `json:"containers,omitempty"`
	// EnvNote says which envFrom imports were not read, and why.
	EnvNote string `json:"envNote,omitempty"`
	// Relations are the other objects this one points at, most important first.
	Relations []Relation `json:"relations,omitempty"`
	// UsedBy is the reverse walk's answer, on a ConfigMap and a Secret only.
	UsedBy *UsedBy `json:"usedBy,omitempty"`
	// RelationsNote says what was left out of Relations and why.
	RelationsNote string         `json:"relationsNote,omitempty"`
	Actions       []DetailAction `json:"actions,omitempty"`
	// Shell is whether this pod's page draws Shell, under the server profile.
	Shell *ShellOffer `json:"shell,omitempty"`
	// Debug is whether this pod's page offers Debug, a shell in an ephemeral
	// container added to the pod: present on a pod's page on both profiles
	// wherever the debug channel is served, with its reason on a read-only
	// cluster. Under the server profile it is reviewed like Shell, on both
	// questions the socket asks (`patch pods/ephemeralcontainers` and `create
	// pods/exec`); a person refused either gets no offer at all.
	Debug *ShellOffer `json:"debug,omitempty"`
}
