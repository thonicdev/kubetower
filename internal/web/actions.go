package web

import (
	"strings"

	"github.com/thonicdev/kubetower/internal/actions"
)

// The verbs, as the drawer offers them. Everything here decides which controls
// to draw; the refusal is server-side, in `internal/actions` through
// `edit.Writer.Locate`, so a hand-made request against a read-only cluster is
// refused whatever was drawn. No verb is one click: each opens a confirmation
// that reads the object first where the change is one field (actions.Preview),
// so the dialog says what the field is now.

// actionSpec is one verb as the panel presents it.
type actionSpec struct {
	Verb  actions.Verb
	Label string
	Icon  string
	// Short is the tooltip and the accessible name's tail: "Restart this pod".
	Short string
	// Prompt is what the verb does in general, in full present-tense sentences. The
	// confirmation does not lead with it (it is true of every object); it leads with
	// `lineFor`'s line about this object and folds this under "Details".
	Prompt string
	// Warning is the verb's general caveat, where the consequence is not what the
	// name suggests; folded under "Details" with Prompt. A warning true of this
	// object is the line's (`planLine.Warnings`).
	Warning string
	// Danger colors the control, per (verb, kind): Restart on a Pod is dangerous,
	// on a Deployment it is not.
	Danger bool
	// Irreversible says the act cannot be undone from here: the object is gone. It
	// colors a verb as irreversible in menus, which have not read the object;
	// Danger keeps coloring the drawer's chips, the selection bar and the
	// confirmation. The plan's own `irreversible` is narrower and per object
	// (`lineFor`).
	Irreversible bool
	// Replicas asks for a number.
	Replicas bool
	// Button is the wording on the control that commits, with `{name}` where the
	// object's name goes ("Delete api-0"); see buttonFor.
	Button string
}

// specFor returns the presentation of one verb on one kind, or false. Kind is
// the URL's plural; matching is on a normalized singular, so "pods", "Pod" and
// "pod" behave the same.
func specFor(verb actions.Verb, kind string) (actionSpec, bool) {
	pod := isPodKind(kind)
	switch verb {
	case actions.Delete:
		return actionSpec{
			Verb: verb, Label: "Delete", Icon: "trash", Short: "Delete this object",
			Prompt: "The object is removed from the cluster and its dependents are collected behind it. " +
				"This cannot be undone from here: if nothing else holds this manifest, it is gone.",
			// "Delete", not "Delete api-0": the title names the object.
			Danger: true, Irreversible: true, Button: "Delete",
		}, true

	case actions.Restart:
		if pod {
			// Per kind because a Pod is not restarted: it is deleted, and something else may
			// make another.
			return actionSpec{
				Verb: verb, Label: "Restart", Icon: "refresh", Short: "Delete this pod so it is recreated",
				Prompt: "Restarting a pod means deleting it. A controller that owns this pod creates a " +
					"replacement immediately; the new pod has a new name and its logs start again from nothing.",
				Warning: "If nothing owns this pod, deleting it is all that happens — nothing will create another.",
				Danger:  true, Button: "Delete {name}",
			}, true
		}
		return actionSpec{
			Verb: verb, Label: "Restart", Icon: "refresh", Short: "Roll the pods of this workload",
			Prompt: "A rollout restart stamps " + actions.RestartedAtAnnotation + " on the pod template with the " +
				"current time, which is what `kubectl rollout restart` does. The controller then replaces its " +
				"pods gradually, honoring maxUnavailable, and the rollout can be watched and undone like any other.",
			Button: "Restart {name}",
		}, true

	case actions.Evict:
		return actionSpec{
			Verb: verb, Label: "Evict", Icon: "evict", Short: "Evict this pod, respecting its disruption budget",
			Prompt: "An eviction is not a delete. It asks the API server to remove the pod, and the API server " +
				"refuses if a PodDisruptionBudget would be violated — the pod then stays and you are told so. " +
				"Accepted, it is a graceful deletion: the pod's own termination grace period still runs.",
			// Marked although a PodDisruptionBudget may refuse it: an accepted eviction
			// removes the pod.
			Danger: true, Button: "Evict {name}",
		}, true

	case actions.Cordon:
		return actionSpec{
			Verb: verb, Label: "Cordon", Icon: "cordon", Short: "Stop scheduling new pods onto this node",
			Prompt: "spec.unschedulable is set to true. Nothing new is scheduled onto the node; everything " +
				"already running on it stays exactly where it is. Uncordon undoes this.",
			Button: "Cordon {name}",
		}, true

	case actions.Uncordon:
		return actionSpec{
			Verb: verb, Label: "Uncordon", Icon: "uncordon", Short: "Let pods be scheduled onto this node again",
			Prompt: "spec.unschedulable is set to false, so the scheduler may use this node again.",
			Button: "Uncordon {name}",
		}, true

	case actions.Drain:
		return actionSpec{
			Verb: verb, Label: "Drain", Icon: "drain", Short: "Cordon this node and evict what is on it",
			Prompt: "This first step cordons the node and then lists what is running on it. Nothing is " +
				"evicted yet — you will see every pod, and what would be left alone and why, and confirm " +
				"a second time before anything moves.",
			Warning: "Draining a node moves production workloads, and emptyDir contents are lost. Pods " +
				"that nothing owns are left on the node, as kubectl drain leaves them without --force: " +
				"nothing would recreate them. The drain names them and offers to include them.",
			Danger: true, Button: "Cordon {name} and list its pods",
		}, true

	case actions.Suspend:
		return actionSpec{
			Verb: verb, Label: "Suspend", Icon: "pause", Short: "Stop this CronJob starting new runs",
			Prompt: "spec.suspend is set to true, so no new run starts on the schedule. A run already in " +
				"flight is not touched — suspending is not pausing.",
			Button: "Suspend {name}",
		}, true

	case actions.Resume:
		return actionSpec{
			Verb: verb, Label: "Resume", Icon: "play", Short: "Let this CronJob run on its schedule again",
			Prompt: "spec.suspend is set to false. The schedule applies again from now; runs missed while " +
				"it was suspended are not made up.",
			Button: "Resume {name}",
		}, true

	case actions.Trigger:
		return actionSpec{
			Verb: verb, Label: "Run now", Icon: "trigger", Short: "Create a Job from this CronJob's template",
			Prompt: "A Job is created from spec.jobTemplate, which is what `kubectl create job --from` does. " +
				"It runs immediately, whether or not the CronJob is suspended.",
			Warning: "The Job is not owned by the CronJob: concurrencyPolicy does not apply to it, so it can " +
				"overlap a scheduled run, and the history limits will not clean it up.",
			Button: "Run {name} now",
		}, true

	case actions.Scale:
		return actionSpec{
			Verb: verb, Label: "Scale", Icon: "scale", Short: "Set the replica count",
			Prompt: "spec.replicas is set to the number below. The change is validated against the cluster " +
				"before it is committed, so a quota that refuses it is a sentence rather than a half-scaled workload.",
			Replicas: true, Button: "Scale {name}",
		}, true
	}
	return actionSpec{}, false
}

// destructiveFor is the one answer to whether a verb is destructive on a kind,
// read by everything that draws a red control so the drawer and the plans
// cannot disagree. It is per (verb, kind), from specFor. A verb with no spec is
// not destructive; plans with no verb spec (a data write, an apply, a node
// shell) set `Destructive: true` themselves.
func destructiveFor(verb actions.Verb, kind string) bool {
	spec, ok := specFor(verb, kind)
	return ok && spec.Danger
}

// verbsFor is which verbs a kind offers, in row order: the everyday ones first,
// the destructive ones last, Evict before Delete (an eviction a
// PodDisruptionBudget refuses leaves the pod where it is). The order is the
// server's because Danger is a boolean and cannot express that gradation; the
// client keeps a stable non-destructive-first sort on top. Every surface (drawer,
// page, row menu, selection bar) reads this table.
//
// Every kind offers Delete, a Node and a Namespace included, as `kubectl delete`
// does; RBAC and the API server still refuse with their reason. Withholding is
// per object, elsewhere: the console's own namespace under `--incluster`
// (ownnamespace.go) and a namespace already Terminating (objectActions).
func verbsFor(kind string) []actions.Verb {
	switch {
	case isPodKind(kind):
		return []actions.Verb{actions.Restart, actions.Evict, actions.Delete}
	case isNodeKind(kind):
		return []actions.Verb{actions.Cordon, actions.Uncordon, actions.Drain, actions.Delete}
	case matchKind(kind, "cronjobs"):
		return []actions.Verb{actions.Trigger, actions.Suspend, actions.Resume, actions.Delete}
	case matchKind(kind, "deployments"), matchKind(kind, "statefulsets"):
		return []actions.Verb{actions.Restart, actions.Scale, actions.Delete}
	case matchKind(kind, "daemonsets"):
		return []actions.Verb{actions.Restart, actions.Delete}
	case matchKind(kind, "replicasets"), matchKind(kind, "replicationcontrollers"):
		return []actions.Verb{actions.Scale, actions.Delete}
	}
	return []actions.Verb{actions.Delete}
}

// matchKind compares a URL's kind with a plural resource name, tolerating the
// singular and any casing: a link may have been written by hand.
func matchKind(kind, plural string) bool {
	kind = strings.ToLower(strings.TrimSpace(kind))
	return kind == plural || kind == strings.TrimSuffix(plural, "s")
}
