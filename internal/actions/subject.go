package actions

import (
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// What the confirmation's one line says about this object, read off it.
//
// A confirmation of sentences true of every pod gets clicked without being
// read. What tells this object from the next is a few facts the preview has
// already read: who owns it, whether Argo CD or Helm holds its manifest, a
// CronJob's suspension and runs in flight. This file reads them;
// internal/web words them.
//
// Read from the object the preview fetched and nothing else (an owner is
// named by its ownerReference, a CronJob's runs are its `status.active`),
// so the line costs no request and nobody is tempted to optimize it away.

// Subject is the facts about one object that make its confirmation specific.
// The zero value knows nothing beyond the name.
type Subject struct {
	// Kind is the object's own Kind as the API server spells it, read off
	// the object rather than derived from the URL's plural.
	Kind string
	// Controller is the ownerReference marked controller, or the zero Owner:
	// a pod can carry several ownerReferences, and at most one makes another.
	Controller Owner
	// Manager is a tool that holds this object's manifest outside the cluster
	// and can put it back - Argo CD or Helm - or the zero Manager.
	Manager Manager
	// Suspended is a CronJob's `spec.suspend`.
	Suspended bool
	// Active is the names of a CronJob's runs in flight (`status.active`),
	// sorted so the line is stable.
	Active []string
}

// Owner is an ownerReference, by kind and name.
type Owner struct {
	Kind string
	Name string
}

// Known reports whether there is an owner at all.
func (o Owner) Known() bool { return o.Kind != "" }

// Manager is what holds an object's manifest outside the cluster: Argo CD's
// application or Helm's release, by name.
type Manager struct {
	// By is the tool as a person names it: "Argo CD application", "Helm
	// release".
	By   string
	Name string
}

// Known reports whether a manager was found.
func (m Manager) Known() bool { return m.By != "" }

// The marks a manager leaves on what it applies.
//
// Argo CD's annotation is its tracking id, `<app>:<group>/<kind>:<ns>/<name>`.
// Helm 3 annotates every object of a release with its name, the mark
// `helm upgrade` itself reads.
//
// `app.kubernetes.io/instance` alone is not taken as either: Helm charts and
// many operators set it by convention, so it proves nothing. Taking it would
// remove "Cannot be undone." on a promise nothing keeps, which costs the
// object; a needless warning costs a moment of reading.
const (
	argoTrackingAnnotation = "argocd.argoproj.io/tracking-id"
	helmReleaseAnnotation  = "meta.helm.sh/release-name"
)

// SubjectOf reads the facts a confirmation's line is composed from.
func SubjectOf(object *unstructured.Unstructured) Subject {
	if object == nil {
		return Subject{}
	}
	subject := Subject{Kind: object.GetKind()}
	for _, owner := range object.GetOwnerReferences() {
		if owner.Controller != nil && *owner.Controller {
			subject.Controller = Owner{Kind: owner.Kind, Name: owner.Name}
			break
		}
	}
	subject.Manager = managerOf(object.GetAnnotations())
	if isCronJob(object.GetKind()) {
		subject.Suspended, _, _ = unstructured.NestedBool(object.Object, "spec", "suspend")
		subject.Active = activeRuns(object)
	}
	return subject
}

// managerOf is the Argo CD application or the Helm release that applied this
// object. Argo CD first: an application rendering a Helm chart leaves both
// marks, and Argo CD is what would put the object back; Helm's release is a
// record nobody upgrades.
func managerOf(annotations map[string]string) Manager {
	if id := strings.TrimSpace(annotations[argoTrackingAnnotation]); id != "" {
		app, _, _ := strings.Cut(id, ":")
		if app != "" {
			return Manager{By: "Argo CD application", Name: app}
		}
	}
	if release := strings.TrimSpace(annotations[helmReleaseAnnotation]); release != "" {
		return Manager{By: "Helm release", Name: release}
	}
	return Manager{}
}

// activeRuns is a CronJob's `status.active`: the Jobs it started that have not
// finished. A reference with no name is skipped.
func activeRuns(object *unstructured.Unstructured) []string {
	refs, found, err := unstructured.NestedSlice(object.Object, "status", "active")
	if err != nil || !found {
		return nil
	}
	var names []string
	for _, raw := range refs {
		ref, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := ref["name"].(string); name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// Remaker is the controller that makes another of this object when it goes,
// or the zero Owner when none would.
//
// A controller that keeps a count (a ReplicaSet, StatefulSet, DaemonSet,
// ReplicationController, a Deployment over its ReplicaSets) makes another.
// A CronJob does not: the Job deleted is gone with its pods and logs, so it
// keeps "Cannot be undone.".
//
// A Job over its pod counts: it starts another while it runs (at the cost
// of a retry, which internal/web says). Any other controller is taken at its
// word: an ownerReference marked controller is the cluster's claim that
// something reconciles the object.
//
// A Manager is not a remaker: Argo CD and Helm restore from the manifest on
// the next sync or upgrade, a different promise worded apart.
func (s Subject) Remaker() Owner {
	if s.Controller.Kind == "CronJob" {
		return Owner{}
	}
	return s.Controller
}
