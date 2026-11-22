// Package prereq installs the operators the extra pages read from, by writing
// one Argo CD Application and letting Argo do the installing.
//
// The console has no Argo sync button: that would be a second write path
// beside internal/edit's guarded door, and would need an Argo token. Neither
// objection holds here:
//
//   - No second write path: this creates an ordinary Kubernetes object
//     through edit.Writer.Locate and edit.Create, with the same read-only
//     cluster check, dry-run-then-commit and `write:` log line as the
//     manifest editor and the verbs.
//   - No Argo token: nothing here speaks to Argo CD's API. It writes an
//     `Application` with the kubeconfig's own credential, and Argo's
//     controller does the installing.
//
// It is not a general installer. A request carries a key into the table
// below and nothing else (no chart, repository, version, namespace or
// values): everything that reaches a cluster is written here in Go. Posting a
// manifest is the manifest editor's job, which makes the person read it.
//
// It does not install Argo CD itself: without Argo there is nothing to
// install through, and the Argo CD page gives the helm command instead.
package prereq

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/thonicdev/kubetower/internal/actions"
	"github.com/thonicdev/kubetower/internal/edit"
)

// ApplicationKind is the kind an install is written as, qualified with its API
// group.
//
// Always qualified and resolved through the mapper, never put in a GVR's
// Resource field, where every Argo CD CRD would answer "not a kind this
// cluster serves". The body's `apiVersion` comes from the mapper
// (edit.APIVersionOf), so no version constant here can go stale.
const ApplicationKind = "applications.argoproj.io"

// InClusterServer is the destination Argo CD calls the cluster it runs in.
//
// The literal and not a cluster name: Argo matches a destination against its
// registered clusters, every installation has this one under this name, and a
// cluster name would have to match a secret Argo wrote, which this process
// cannot read.
const InClusterServer = "https://kubernetes.default.svc"

// Prerequisite is one operator an extra page needs, and everything that reaches
// a cluster to install it.
//
// A table, not an interface and not loaded from anywhere: a caller may supply
// a key and nothing else.
type Prerequisite struct {
	// Key is what a URL carries. Stable: it can be bookmarked.
	Key string
	// Label is what to call it in a sentence: "Trivy Operator".
	Label string
	// Extra is the page that needs it, spelled out rather than imported so
	// this package has no opinion about which pages exist.
	Extra string

	// RepoURL, Chart and Version are the Helm chart, checked against the
	// repository's own index.
	RepoURL string
	Chart   string
	// AppVersion is the operator's own version the chart ships, read from the
	// same index entry as Version: what the page names beside the chart.
	AppVersion string
	// Version is pinned exactly. Unpinned (or a range) with automated sync,
	// Argo would replace a security scanner under the operator whenever a chart
	// is published, because of one button press. The exact version is shown
	// before anything is created and in the manifest; bumping it is a
	// deliberate edit to the Application. The cost: the pin goes stale here and
	// is checked at each release (see the CHECKED line beside each row).
	Version string
	// Namespace is where the chart's own release goes: the chart's documented
	// default.
	Namespace string

	// What the page says: what this will do, and what it costs.
	Does string
	Cost string
}

// The table. Each field is checked against the repository.
var prerequisites = []Prerequisite{
	{
		Key:   "trivy-operator",
		Label: "Trivy Operator",
		Extra: "security",

		// CHECKED 2026-11-13 against
		// https://aquasecurity.github.io/helm-charts/index.yaml (generated
		// 2026-11-05T13:22:19Z): the newest `trivy-operator` entry is version
		// 0.38.1, appVersion 0.36.1, created 2026-10-29, digest
		// 9c1de16f159bb9e1fca820b43cd7c5b1475fa869c2ce90541db0e747e8a4c674.
		// Before it: 0.37.0 (0.35.0), 0.36.0 (0.34.0).
		RepoURL:    "https://aquasecurity.github.io/helm-charts",
		Chart:      "trivy-operator",
		Version:    "0.38.1",
		AppVersion: "0.36.1",
		Namespace:  "trivy-system",

		Does: "Argo CD installs the trivy-operator chart into trivy-system and keeps it there. " +
			"Trivy then scans every workload's image and audits every pod spec, and writes what " +
			"it found into the custom resources this page reads.",
		Cost: "Trivy pulls a vulnerability database of a few hundred megabytes and scans every " +
			"image in the cluster, so the first reports appear minutes later and the node " +
			"running the operator does real work while they do.",
	},
}

// Lookup finds one prerequisite by key.
func Lookup(key string) (Prerequisite, bool) {
	for _, p := range prerequisites {
		if p.Key == strings.TrimSpace(key) {
			return p, true
		}
	}
	return Prerequisite{}, false
}

// For lists the prerequisites one extra page needs.
func For(extra string) []Prerequisite {
	var out []Prerequisite
	for _, p := range prerequisites {
		if p.Extra == extra {
			out = append(out, p)
		}
	}
	return out
}

// ChartRef is "trivy-operator 0.38.1", for a heading.
func (p Prerequisite) ChartRef() string { return p.Chart + " " + p.Version }

// Application is the object that will be created.
//
// Pure (no cluster, no clock, no I/O), so the manifest a test asserts on is
// the one that leaves the process. apiVersion is the version this cluster
// serves (see ApplicationKind).
//
//   - `project: default`: every Argo CD has it. Inventing a project would be
//     a second object and a policy that is not ours; a narrowed default
//     refuses this visibly, in Argo's words.
//   - `syncPolicy.automated`, not optional: without it the Application sits
//     OutOfSync until somebody presses Sync, and this console has no sync
//     button, so the install would do nothing.
//   - `prune: false` and `selfHeal: false`: deleting what the chart stops
//     shipping and undoing manual changes are not for a console button to
//     switch on in somebody's cluster; both are one edit away in Argo.
//   - `CreateNamespace=true` because trivy-system will not exist, and
//     `ServerSideApply=true` because Trivy's CRDs are large: client-side apply
//     writes them into `last-applied-configuration`, which the API server
//     refuses past 262 144 bytes (`metadata.annotations: Too long`).
//
// No `resources-finalizer.argocd.argoproj.io`: it would make deleting the
// Application delete everything it installed, CRDs and reports included,
// which is the operator's choice to make, not the console's.
func (p Prerequisite) Application(argoNamespace, apiVersion string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       "Application",
		"metadata": map[string]any{
			"name":      p.Key,
			"namespace": argoNamespace,
			// As on the node shell's pod: an object that appears in somebody's
			// cluster says what made it and why, because whoever finds it is not who
			// pressed the button.
			"annotations": map[string]any{
				"kubetower.io/why": "Created from KubeTower's Security Center to install " + p.Label +
					" on this cluster through Argo CD. Delete this Application to stop Argo " +
					"reconciling it; that alone does not uninstall the chart.",
			},
		},
		"spec": map[string]any{
			"project": "default",
			"source": map[string]any{
				"repoURL":        p.RepoURL,
				"chart":          p.Chart,
				"targetRevision": p.Version,
			},
			// `server` and not `name`: Argo refuses an Application carrying both.
			"destination": map[string]any{
				"server":    InClusterServer,
				"namespace": p.Namespace,
			},
			"syncPolicy": map[string]any{
				"automated": map[string]any{
					"prune":    false,
					"selfHeal": false,
				},
				"syncOptions": []any{
					"CreateNamespace=true",
					"ServerSideApply=true",
				},
			},
		},
	}}
}

// YAML is the Application as the operator reads it before pressing anything.
//
// The same object the create sends, marshalled, never a second copy: a
// confirmation showing a manifest other than the one sent is worse than
// none, because the reader believes it.
func (p Prerequisite) YAML(argoNamespace, apiVersion string) (string, error) {
	out, err := yaml.Marshal(p.Application(argoNamespace, apiVersion).Object)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// --- the install ---------------------------------------------------------------

// Installer writes one Application, through the one guarded door.
type Installer struct {
	writer *edit.Writer
}

// New builds an Installer. A nil Writer makes every call refuse rather than
// reach a cluster unguarded.
func New(writer *edit.Writer) *Installer {
	return &Installer{writer: writer}
}

// Request is one install.
//
// Prereq is a key and not a manifest: the only thing a caller chooses is
// which row of the table.
type Request struct {
	// Cluster is whatever spelling arrived from outside, resolved to a context
	// name before use.
	Cluster string
	// Prereq is a key into the table.
	Prereq string
	// ArgoNamespace is where the Application goes, which is where Argo CD is
	// installed, discovered by the caller. An empty one is refused rather than
	// defaulted to `argocd`: an Application no Argo controller watches is
	// reconciled by nobody and looks like an install in progress.
	ArgoNamespace string
	// Confirm is actions.ConfirmToken, the word every confirmed action uses.
	Confirm string
}

// Plan is what an install will do, and it reaches no cluster.
//
// The confirmation is drawn from the same code the commit runs: Install
// redoes all of Plan's work before sending anything. Nothing is carried
// between them, because a stale browser tab could carry it back.
type Plan struct {
	Prerequisite
	// Cluster is the resolved context name, which is the one every refusal names.
	Cluster string
	// ArgoNamespace is where the Application goes; Name is what it is called.
	ArgoNamespace string
	Name          string
	// APIVersion is the version of the Application kind this cluster serves.
	APIVersion string
	// YAML is the object, exactly as it will be sent.
	YAML string
}

// Done is a completed install.
type Done struct {
	Plan
	// Message is the sentence the panel shows. It does not say the operator is
	// installed: an Application exists and Argo will reconcile it, which takes
	// minutes and can fail; only the first is known here.
	Message string
}

// Plan resolves and validates, and sends nothing.
func (i *Installer) Plan(ctx context.Context, req Request) (Plan, error) {
	ready, err := i.prepare(ctx, req)
	if err != nil {
		return Plan{}, err
	}
	return ready.plan, nil
}

// Check is Plan with the API server's dry run of the create: what a preview
// shows. The create is reviewed for the person behind ctx (edit.Create), so a
// person who may not create Applications there is refused here, before any
// confirmation is asked for, and an Application of this name that exists
// already is said as such. Nothing is created.
func (i *Installer) Check(ctx context.Context, req Request) (Plan, error) {
	ready, err := i.prepare(ctx, req)
	if err != nil {
		return Plan{}, err
	}
	if _, _, err := edit.Create(ctx, ready.where, ready.target, ready.object.DeepCopy(), true); err != nil {
		return Plan{}, alreadyThere(err, ready)
	}
	return ready.plan, nil
}

// alreadyThere says AlreadyExists as what it is: an install that was made
// before, which this never replaces. Only when it is the API server's answer
// to this request, and so only after `create` was reviewed for the person.
func alreadyThere(err error, ready *prepared) error {
	refusal, ok := edit.AsRefusal(err)
	if !ok || refusal.Kind != edit.RefusedByAPI || !apierrors.IsAlreadyExists(err) {
		return err
	}
	return edit.RefuseFrom(err, edit.RefusedInput,
		"an Application called %q already exists in %s; this never replaces one - it is where to "+
			"watch the install", ready.target.Name, ready.target.Namespace)
}

// Install creates the Application, having done all of Plan's work again.
//
// Resolve and apply the read-only guardrail, then check the confirmation:
// the other order answers a request against a read-only cluster by asking
// the person to confirm, which invites them to.
func (i *Installer) Install(ctx context.Context, req Request) (Done, error) {
	ready, err := i.prepare(ctx, req)
	if err != nil {
		return Done{}, err
	}
	if req.Confirm != actions.ConfirmToken {
		return Done{}, edit.Refuse(edit.RefusedInput,
			"this install was not confirmed. Open it from the Security Center, read the "+
				"Application it will create, and confirm there")
	}

	// Dry run first, then commit: a refusing AppProject, an admission webhook
	// or an existing Application of this name is a sentence before anything
	// exists.
	if _, _, err := edit.Create(ctx, ready.where, ready.target, ready.object.DeepCopy(), true); err != nil {
		return Done{}, alreadyThere(err, ready)
	}
	created, decision, err := edit.Create(ctx, ready.where, ready.target, ready.object, false)
	if err != nil {
		return Done{}, alreadyThere(err, ready)
	}

	// The same `write:` line shape as internal/edit's, so one grep finds it.
	i.writer.Logf(ctx, "write: cluster=%q kind=%s namespace=%s name=%s action=install chart=%s version=%s decision=%s outcome=applied",
		ready.where.Cluster, ready.where.Resource.Resource, ready.target.Namespace,
		created.GetName(), ready.plan.Chart, ready.plan.Version, decision)

	return Done{
		Plan: ready.plan,
		Message: fmt.Sprintf("Created Argo CD Application %s in %s. Argo will reconcile it: it "+
			"installs %s %s into %s, which takes a few minutes and can fail. Nothing is installed "+
			"yet — the Application is where to watch that happen.",
			created.GetName(), ready.target.Namespace, ready.plan.Chart, ready.plan.Version,
			ready.plan.Namespace),
	}, nil
}

// prepared is everything both calls need, computed once and never carried
// between requests.
type prepared struct {
	where  edit.Location
	target edit.Target
	object *unstructured.Unstructured
	plan   Plan
}

// prepare does every check there is and stops at the first that fails.
func (i *Installer) prepare(ctx context.Context, req Request) (*prepared, error) {
	if i.writer == nil {
		return nil, edit.Refuse(edit.RefusedInput, "this build has no write path, so nothing can be installed")
	}
	prerequisite, ok := Lookup(req.Prereq)
	if !ok {
		return nil, edit.Refuse(edit.RefusedInput,
			"%q is not something this console installs", req.Prereq)
	}
	namespace := strings.TrimSpace(req.ArgoNamespace)
	if namespace == "" {
		return nil, edit.Refuse(edit.RefusedInput,
			"no Argo CD namespace was given, and this will not guess one: an Application in a "+
				"namespace no Argo controller watches is reconciled by nobody and looks exactly "+
				"like an install in progress")
	}

	target := edit.Target{
		Cluster:   req.Cluster,
		Kind:      ApplicationKind,
		Namespace: namespace,
		Name:      prerequisite.Key,
	}
	// Resolves the context name and refuses a read-only cluster, through the
	// same Locate every write uses.
	where, err := i.writer.Locate(ctx, target)
	if err != nil {
		return nil, err
	}
	if !where.Namespaced {
		return nil, edit.Refuse(edit.RefusedInput,
			"this cluster serves Applications as cluster-scoped, which this does not support")
	}

	apiVersion := edit.APIVersionOf(where)
	object := prerequisite.Application(namespace, apiVersion)
	document, err := prerequisite.YAML(namespace, apiVersion)
	if err != nil {
		return nil, edit.Refuse(edit.RefusedInput, "the Application could not be written out: %s", err)
	}

	return &prepared{
		where:  where,
		target: target,
		object: object,
		plan: Plan{
			Prerequisite:  prerequisite,
			Cluster:       where.Cluster,
			ArgoNamespace: namespace,
			Name:          prerequisite.Key,
			APIVersion:    apiVersion,
			YAML:          document,
		},
	}, nil
}
