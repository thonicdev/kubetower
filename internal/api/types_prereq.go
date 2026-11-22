package api

import "context"

// Installing an operator an extra page reads from, through the console's own
// write door (`internal/prereq`). The page that needs it links here; it never
// writes itself.

// Prereqs is the backend of the three prerequisite operations. A preview and
// an install come in a pair, as Acts's do, and the handler re-derives the
// facts through PreviewPrereq before the install, as it does for every act.
type Prereqs interface {
	// Prereq is what a page reads before it offers the install: whether Argo
	// CD is there to install through, where, whether the Application exists,
	// and whether this person may create it. Every cluster read in it is
	// reviewed for the person.
	Prereq(ctx context.Context, cluster, key string) (Prereq, *Failure)
	// PreviewPrereq plans the install: the Application exactly as it will be
	// sent, validated by a dry run of the create, reviewed for the person.
	PreviewPrereq(ctx context.Context, cluster, key string) (PlanFacts, error)
	// InstallPrereq creates the Application in argoNamespace - the namespace
	// of the plan the person confirmed, never one a client supplies.
	InstallPrereq(ctx context.Context, cluster, key, argoNamespace string) (ActOutcome, error)
}

// The states of Argo CD on a cluster, as a prerequisite reads them.
const (
	// ArgoFound is one application-controller, in one namespace.
	ArgoFound = "found"
	// ArgoAbsent is no Application kind served, or none of its controllers
	// running, on a read that covered the whole cluster.
	ArgoAbsent = "absent"
	// ArgoAmbiguous is controllers in several namespaces: which one would
	// reconcile the Application is not this console's to guess.
	ArgoAmbiguous = "ambiguous"
	// ArgoUnknown is a read that could not answer: refused, narrowed to
	// namespaces that hold none, or failed. Never drawn as absent.
	ArgoUnknown = "unknown"
)

// The states of the Application an install would create.
const (
	ApplicationAbsent  = "absent"
	ApplicationPresent = "present"
	ApplicationUnknown = "unknown"
)

// Prereq is one prerequisite on one cluster.
type Prereq struct {
	Key   string      `json:"key"`
	Label string      `json:"label"`
	Chart PrereqChart `json:"chart"`
	Argo  PrereqArgo  `json:"argo"`
	// Application is the object an install creates. Absent where Argo CD was
	// not found, since there is no namespace to look in.
	Application *PrereqApplication `json:"application,omitempty"`
	// Installable is whether a preview would be worth asking for now. The
	// preview and the install check everything again.
	Installable bool `json:"installable"`
	// Reason says why not, where Installable is false; where it is true, it
	// may carry what the desktop could not review (the cluster decides).
	Reason *Reason `json:"reason,omitempty"`
	// Helm is the chart as a helm command installs it, where Argo CD is not
	// there to install through. Structured, for the client to compose the
	// command line from.
	Helm *PrereqHelm `json:"helm,omitempty"`
}

// PrereqChart is what an install brings: the chart, pinned exactly.
type PrereqChart struct {
	Repo       string `json:"repo"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	AppVersion string `json:"appVersion,omitempty"`
	// Namespace is where the chart's release goes.
	Namespace string `json:"namespace"`
}

// PrereqArgo is Argo CD as this person can see it on this cluster.
type PrereqArgo struct {
	State string `json:"state"`
	// Namespace is the application-controller's, where State is found.
	Namespace string `json:"namespace,omitempty"`
	// Namespaces are every namespace a controller was found in, where State is
	// ambiguous.
	Namespaces []string `json:"namespaces,omitempty"`
	// Note is what the read said, where it says more than the state.
	Note string `json:"note,omitempty"`
}

// PrereqApplication is the Argo CD Application an install creates.
type PrereqApplication struct {
	State     string `json:"state"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Note      string `json:"note,omitempty"`
}

// PrereqHelm is the chart as `helm upgrade --install` takes it.
type PrereqHelm struct {
	Release   string `json:"release"`
	Repo      string `json:"repo"`
	Chart     string `json:"chart"`
	Version   string `json:"version"`
	Namespace string `json:"namespace"`
}
