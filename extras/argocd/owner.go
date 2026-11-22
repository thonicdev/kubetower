package argocd

import (
	"net/http"
	"strings"

	"github.com/thonicdev/kubetower/extension"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// A Pod is Argo CD's through its owner.
//
// Argo CD marks only what it applies (a Deployment, not its ReplicaSets or
// Pods), so an unmarked object is followed up its controller chain to the
// first marked ancestor. Every object on the way is read for the person
// through Host.List; one they may not read ends the walk as not managed.

// MaxOwnerHops bounds the walk. Pod to Deployment, or Pod to CronJob, is two
// hops; four leaves room for a custom controller in between.
const MaxOwnerHops = 4

// ObjectRef names an object as the console addresses it.
type ObjectRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// Path is the kind's segment in the console's address.
	Path string `json:"path,omitempty"`
}

// managedOwner walks up the controller chain of an object that carries no
// mark, and answers the first marked ancestor, its target and the chain from
// the object's owner to it. Found is false when the walk ends without one, or
// when it would take more than budget hops.
func managedOwner(r *http.Request, h extension.Host, cluster string, object unstructured.Unstructured, budget int) (unstructured.Unstructured, target, []ObjectRef, bool) {
	clients, err := h.Client(r, cluster)
	if err != nil || clients.Typed == nil {
		return unstructured.Unstructured{}, target{}, nil, false
	}
	var chain []ObjectRef
	pages := newPages(r, h, cluster)
	current := object
	for hop := 0; hop < budget; hop++ {
		ref, ok := controllerOf(current)
		if !ok {
			return unstructured.Unstructured{}, target{}, nil, false
		}
		gv, err := schema.ParseGroupVersion(ref.apiVersion)
		if err != nil {
			return unstructured.Unstructured{}, target{}, nil, false
		}
		list, err := clients.Typed.Discovery().ServerResourcesForGroupVersion(ref.apiVersion)
		if err != nil {
			return unstructured.Unstructured{}, target{}, nil, false
		}
		var t target
		for _, res := range list.APIResources {
			if res.Kind == ref.kind && !strings.Contains(res.Name, "/") {
				t = target{gvr: gv.WithResource(res.Name), kind: res.Kind, namespaced: res.Namespaced}
				break
			}
		}
		if t.kind == "" {
			return unstructured.Unstructured{}, target{}, nil, false
		}
		namespace := ""
		if t.namespaced {
			namespace = current.GetNamespace()
		}
		page, err := h.List(r, cluster, extension.ListQuery{GVR: t.gvr, Namespace: namespace, FieldSelector: "metadata.name=" + ref.name})
		if err != nil {
			return unstructured.Unstructured{}, target{}, nil, false
		}
		var owner *unstructured.Unstructured
		for i := range page.Items {
			if page.Items[i].GetName() == ref.name {
				owner = &page.Items[i]
			}
		}
		if owner == nil {
			return unstructured.Unstructured{}, target{}, nil, false
		}
		chain = append(chain, ObjectRef{Kind: t.kind, Namespace: namespace, Name: ref.name, Path: pages.page(gv.Group, t.gvr.Resource).path})
		// A mark copied from above - a ReplicaSet carries its Deployment's -
		// is not the owner's own: the walk goes on to the object it names.
		if m, marked := markOf(*owner); marked && !copiedMark(m, t, *owner) {
			return *owner, t, chain, true
		}
		current = *owner
	}
	return unstructured.Unstructured{}, target{}, nil, false
}

// throughOwner answers an object as its first marked ancestor's, when Argo CD
// manages that ancestor. An ancestor marked only by Helm's instance label,
// which no Application lists, is not the answer: its own owners are asked in
// turn, within what is left of the budget. Charts put
// `app.kubernetes.io/instance` on their Pod template, so stopping there would
// answer a Pod of an Argo CD Application as not managed.
func throughOwner(r *http.Request, h extension.Host, cluster string, t target, object unstructured.Unstructured, budget int) (Tracked, bool) {
	owner, ownerTarget, chain, found := managedOwner(r, h, cluster, object, budget)
	if !found {
		return Tracked{}, false
	}
	through := trackWithin(r, h, cluster, ownerTarget, owner, budget-len(chain))
	if !through.Managed {
		return Tracked{}, false
	}
	// The owner may itself have been answered through its own: the chain runs
	// on from where this walk stopped.
	if through.Of != nil {
		chain = append(chain, through.Through...)
	}
	through.Of = &ObjectRef{Kind: t.kind, Namespace: object.GetNamespace(), Name: object.GetName()}
	through.Through = chain
	return through, true
}

type ownerRef struct{ apiVersion, kind, name string }

// controllerOf is the object's controlling owner: the ownerReference with
// controller true, the one that made it.
func controllerOf(object unstructured.Unstructured) (ownerRef, bool) {
	for _, ref := range object.GetOwnerReferences() {
		if ref.Controller != nil && *ref.Controller {
			return ownerRef{apiVersion: ref.APIVersion, kind: ref.Kind, name: ref.Name}, true
		}
	}
	return ownerRef{}, false
}
