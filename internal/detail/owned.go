package detail

import (
	"context"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// A controller's Events include those of the objects it owns. A Deployment that
// cannot roll out says little about itself: the reason is on its ReplicaSet
// (`FailedCreate`) or its pods (`FailedScheduling`, `BackOff`). The same holds
// for StatefulSets, DaemonSets, Jobs and, one level further, CronJobs. Each row
// names the object it is about.
//
// "Owns" is the controller owner reference, matched by UID: a selector can
// overlap another controller's pods and a name can be a previous incarnation's.
// The selector only narrows the list read.
//
// This runs on every drawer open, so the calls are fixed: at most one list of
// intermediates (ReplicaSets, Jobs), one of pods, and one of the namespace's
// events, filtered here by UID.

// Owned is one object a controller owns, as an event row names it.
type Owned struct {
	// Kind is the singular kind as Kubernetes spells it: "Pod".
	Kind string
	// Resource is the plural an address carries: "pods".
	Resource string
	Name     string
	uid      types.UID
}

const (
	// ownedListLimit bounds the list of intermediates and the list of pods.
	ownedListLimit = 500
	// eventsPage and eventsPages bound the namespace's events read: four pages
	// of 500. A namespace holding more is said, not hidden.
	eventsPage  = 500
	eventsPages = 4
	// maxPanelEvents bounds the merged list the panel draws.
	maxPanelEvents = 200
)

// ownedWords names what a kind owns, for the empty state and the notes; empty
// for a kind whose events are its own alone.
func ownedWords(gvk schema.GroupVersionKind) string {
	switch {
	case gvk.Group == "apps" && gvk.Kind == "Deployment":
		return "ReplicaSets and pods"
	case gvk.Group == "batch" && gvk.Kind == "CronJob":
		return "Jobs and pods"
	case gvk.Group == "apps" && (gvk.Kind == "ReplicaSet" || gvk.Kind == "StatefulSet" || gvk.Kind == "DaemonSet"),
		gvk.Group == "batch" && gvk.Kind == "Job",
		gvk.Group == "" && gvk.Kind == "ReplicationController":
		return "pods"
	}
	return ""
}

// ownedAnswer is what the walk down from a controller found.
type ownedAnswer struct {
	events []Event
	// note says what was cut: a list past its bound, a namespace with more
	// events than one read takes. The rows shown are still right.
	note string
	// failed says what could not be read at all. Set, the family is not known
	// to be quiet, and the empty state must not say it is.
	failed string
}

// ownedEvents is the events of what raw owns, each row carrying the object it
// is about. Nothing at all for a kind that owns nothing.
func (r *Reader) ownedEvents(ctx context.Context, client *cluster.Client, raw *unstructured.Unstructured, gvk schema.GroupVersionKind) ownedAnswer {
	what := ownedWords(gvk)
	if what == "" || raw.GetNamespace() == "" || client == nil || client.Clients == nil {
		return ownedAnswer{}
	}
	owned, note, err := r.ownedOf(ctx, client, raw, gvk)
	if err != nil {
		return ownedAnswer{note: note, failed: "The events of the " + what + " it owns could not be read: " + firstLine(err.Error()) + "."}
	}
	if len(owned) == 0 {
		return ownedAnswer{note: note}
	}
	events, cut, err := r.eventsOfAll(ctx, client, raw.GetNamespace(), owned)
	if err != nil {
		return ownedAnswer{note: note, failed: "The events of the " + what + " it owns could not be read: " + firstLine(err.Error()) + "."}
	}
	return ownedAnswer{events: events, note: joinNotes(note, cut)}
}

// eventsOfAll is one reviewed read of the namespace's events, kept where they
// are about one of owned, newest first. Paged, and bounded at eventsPages: a
// namespace past that is said, because the API answers a list in name order,
// not time order, so a cut list can miss any row.
func (r *Reader) eventsOfAll(ctx context.Context, client *cluster.Client, namespace string, owned []Owned) ([]Event, string, error) {
	about := make(map[types.UID]*Owned, len(owned))
	for i := range owned {
		about[owned[i].uid] = &owned[i]
	}
	question := access.Attributes{Verb: "list", Version: "v1", Resource: "events", Namespace: namespace}
	var out []Event
	cut := false
	_, err := r.gate.Read(ctx, client.Context, question, func(ctx context.Context) error {
		next := ""
		for page := 0; page < eventsPages; page++ {
			list, err := client.Clients.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{Limit: eventsPage, Continue: next})
			if err != nil {
				return err
			}
			for i := range list.Items {
				if o, ok := about[list.Items[i].InvolvedObject.UID]; ok {
					e := eventFrom(&list.Items[i])
					e.About = o
					out = append(out, e)
				}
			}
			next = list.Continue
			if next == "" {
				return nil
			}
		}
		cut = true
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	if cut {
		return out, fmt.Sprintf("The namespace holds more than %d events; only those among the first %d read are searched for the objects it owns.",
			eventsPage*eventsPages, eventsPage*eventsPages), nil
	}
	return out, "", nil
}

// ownedOf walks down from raw to what it controls: the intermediates, then the
// pods, each newest first. All are kept (the events read costs the same for any
// number of UIDs); the note says where a list was cut. err is a failed read or
// a selector that does not parse.
func (r *Reader) ownedOf(ctx context.Context, client *cluster.Client, raw *unstructured.Unstructured, gvk schema.GroupVersionKind) ([]Owned, string, error) {
	ns := raw.GetNamespace()
	selector, err := selectorOf(raw, gvk)
	if err != nil {
		return nil, "", fmt.Errorf("its selector does not parse: %w", err)
	}

	switch {
	case gvk.Group == "apps" && gvk.Kind == "Deployment":
		sets, note, err := r.listOwned(ctx, client, ns, "apps", "replicasets", selector,
			func(ctx context.Context, opts metav1.ListOptions) ([]metav1.Object, string, error) {
				list, err := client.Clients.AppsV1().ReplicaSets(ns).List(ctx, opts)
				if err != nil {
					return nil, "", err
				}
				return objectsOf(list.Items, func(s *appsv1.ReplicaSet) metav1.Object { return s }), list.Continue, nil
			})
		if err != nil {
			return nil, "", err
		}
		intermediates := controlledBy(sets, uidSet(raw.GetUID()), "ReplicaSet", "replicasets")
		pods, podNote, err := r.ownedPods(ctx, client, ns, selector, uidsOf(intermediates))
		if err != nil {
			return intermediates, note, err
		}
		return append(intermediates, pods...), joinNotes(note, podNote), nil

	case gvk.Group == "batch" && gvk.Kind == "CronJob":
		// A CronJob has no selector: its Jobs are found by their owner alone.
		jobs, note, err := r.listOwned(ctx, client, ns, "batch", "jobs", labels.Everything(),
			func(ctx context.Context, opts metav1.ListOptions) ([]metav1.Object, string, error) {
				list, err := client.Clients.BatchV1().Jobs(ns).List(ctx, opts)
				if err != nil {
					return nil, "", err
				}
				return objectsOf(list.Items, func(j *batchv1.Job) metav1.Object { return j }), list.Continue, nil
			})
		if err != nil {
			return nil, "", err
		}
		owned := controlledBy(jobs, uidSet(raw.GetUID()), "Job", "jobs")
		if len(owned) == 0 {
			return nil, note, nil
		}
		pods, podNote, err := r.ownedPods(ctx, client, ns, jobPodSelector(jobs, uidsOf(owned)), uidsOf(owned))
		if err != nil {
			return owned, note, err
		}
		return append(owned, pods...), joinNotes(note, podNote), nil
	}

	pods, note, err := r.ownedPods(ctx, client, ns, selector, uidSet(raw.GetUID()))
	return pods, note, err
}

// jobPodSelector is one selector for the pods of every Job in owned: the label
// each Job's selector pins to its UID (`batch.kubernetes.io/controller-uid`, or
// `controller-uid` on older clusters), as one `in` requirement. A Job pinning no
// such label falls back to everything, and the owner check decides.
func jobPodSelector(jobs []metav1.Object, owned map[types.UID]bool) labels.Selector {
	byKey := map[string][]string{}
	for _, job := range jobs {
		j, ok := job.(*batchv1.Job)
		if !ok || !owned[j.UID] {
			continue
		}
		key := ""
		if j.Spec.Selector != nil {
			for k, v := range j.Spec.Selector.MatchLabels {
				if v == string(j.UID) {
					key = k
					break
				}
			}
		}
		if key == "" {
			return labels.Everything()
		}
		byKey[key] = append(byKey[key], string(j.UID))
	}
	if len(byKey) != 1 {
		// None, or Jobs with both label spellings: everything is still one call.
		return labels.Everything()
	}
	for key, uids := range byKey {
		sort.Strings(uids)
		requirement, err := labels.NewRequirement(key, selection.In, uids)
		if err != nil {
			return labels.Everything()
		}
		return labels.NewSelector().Add(*requirement)
	}
	return labels.Everything()
}

// ownedPods lists the pods selector matches and keeps those one of owners
// controls.
func (r *Reader) ownedPods(ctx context.Context, client *cluster.Client, ns string, selector labels.Selector, owners map[types.UID]bool) ([]Owned, string, error) {
	if len(owners) == 0 {
		return nil, "", nil
	}
	pods, note, err := r.listOwned(ctx, client, ns, "", "pods", selector,
		func(ctx context.Context, opts metav1.ListOptions) ([]metav1.Object, string, error) {
			list, err := client.Clients.CoreV1().Pods(ns).List(ctx, opts)
			if err != nil {
				return nil, "", err
			}
			return objectsOf(list.Items, func(p *corev1.Pod) metav1.Object { return p }), list.Continue, nil
		})
	if err != nil {
		return nil, "", err
	}
	return controlledBy(pods, owners, "Pod", "pods"), note, nil
}

// listOwned is one reviewed list read, narrowed by selector and bounded. A list
// that was cut says so.
func (r *Reader) listOwned(ctx context.Context, client *cluster.Client, ns, group, resource string, selector labels.Selector,
	read func(context.Context, metav1.ListOptions) ([]metav1.Object, string, error)) ([]metav1.Object, string, error) {
	question := access.Attributes{Verb: "list", Group: group, Version: "v1", Resource: resource, Namespace: ns}
	var items []metav1.Object
	var more string
	_, err := r.gate.Read(ctx, client.Context, question, func(ctx context.Context) error {
		var err error
		items, more, err = read(ctx, metav1.ListOptions{LabelSelector: selector.String(), Limit: ownedListLimit})
		return err
	})
	if err != nil {
		return nil, "", err
	}
	note := ""
	if more != "" {
		note = fmt.Sprintf("More than %d %s match; the owned ones among the first %d are read.",
			ownedListLimit, resource, ownedListLimit)
	}
	return items, note, nil
}

// selectorOf is the label selector a controller finds its children by. A
// ReplicationController's is a plain map; everyone else's is a LabelSelector.
// A kind with none (a CronJob) gets everything, and the owner check decides.
func selectorOf(raw *unstructured.Unstructured, gvk schema.GroupVersionKind) (labels.Selector, error) {
	value, found, err := unstructured.NestedFieldNoCopy(raw.Object, "spec", "selector")
	if err != nil || !found || value == nil {
		return labels.Everything(), nil
	}
	body, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("spec.selector is a %T", value)
	}
	if gvk.Group == "" && gvk.Kind == "ReplicationController" {
		set := labels.Set{}
		for k, v := range body {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("spec.selector.%s is a %T", k, v)
			}
			set[k] = s
		}
		return labels.SelectorFromSet(set), nil
	}
	var selector metav1.LabelSelector
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(body, &selector); err != nil {
		return nil, err
	}
	return metav1.LabelSelectorAsSelector(&selector)
}

// controlledBy keeps the objects whose controller is one of owners, newest
// first.
func controlledBy(items []metav1.Object, owners map[types.UID]bool, kind, resource string) []Owned {
	kept := make([]metav1.Object, 0, len(items))
	for _, item := range items {
		if ref := metav1.GetControllerOfNoCopy(item); ref != nil && owners[ref.UID] {
			kept = append(kept, item)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		a, b := kept[i].GetCreationTimestamp(), kept[j].GetCreationTimestamp()
		if !a.Equal(&b) {
			return b.Before(&a)
		}
		return kept[i].GetName() < kept[j].GetName()
	})
	out := make([]Owned, 0, len(kept))
	for _, item := range kept {
		out = append(out, Owned{Kind: kind, Resource: resource, Name: item.GetName(), uid: item.GetUID()})
	}
	return out
}

func objectsOf[T any](items []T, as func(*T) metav1.Object) []metav1.Object {
	out := make([]metav1.Object, 0, len(items))
	for i := range items {
		out = append(out, as(&items[i]))
	}
	return out
}

func uidSet(uids ...types.UID) map[types.UID]bool {
	out := make(map[types.UID]bool, len(uids))
	for _, uid := range uids {
		out[uid] = true
	}
	return out
}

func uidsOf(owned []Owned) map[types.UID]bool {
	out := make(map[types.UID]bool, len(owned))
	for _, o := range owned {
		out[o.uid] = true
	}
	return out
}

// joinNotes puts sentences side by side, skipping the empty ones.
func joinNotes(notes ...string) string {
	kept := notes[:0:0]
	for _, n := range notes {
		if n != "" {
			kept = append(kept, n)
		}
	}
	return strings.Join(kept, " ")
}
