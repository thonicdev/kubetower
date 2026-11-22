package edit

// Creating an object that does not exist yet, through the same door as every
// other write.
//
// live() checks the `guarded` stamp because every patch fetches its subject
// first; a create fetches nothing, so it checks here. Location's fields are
// exported, so a hand-assembled one would otherwise reach a cluster without
// the read-only guardrail. Creating a Job from a CronJob and the one-click
// operator install both go through Create.
//
// Refusals a bare iface.Create would not make:
//
//   - an unguarded Location;
//   - a namespace that disagrees with the kind's scope, in both directions:
//     a create is the one write whose namespace is chosen rather than read
//     off an existing object;
//   - a body whose identity disagrees with its address: Create sends the
//     body's name, so an object built with one name and addressed with
//     another would be created under the name nobody checked.

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/thonicdev/kubetower/internal/access"
)

// Create sends one new object, either as a dry run or for real.
//
// where must come from Locate, the one place KT_READONLY_CLUSTERS is
// consulted; any other Location is refused before the request leaves.
//
// The dry run is unambiguous for a create: the API server says whether the
// object would exist. So quota, admission and a malformed spec are a
// sentence before anything exists; callers dry-run first, then commit.
//
// Reviewed first, dry run included: `create` on this resource in this
// namespace for the person behind ctx, with no name, as the API server
// authorizes a create. createInterface has already refused a body whose kind
// is not the resource's, so the kind reviewed is the kind written.
func Create(ctx context.Context, where Location, target Target, object *unstructured.Unstructured, dryRun bool) (*unstructured.Unstructured, access.Decision, error) {
	if _, err := createInterface(where, target, object); err != nil {
		return nil, "", err
	}
	options := metav1.CreateOptions{}
	if dryRun {
		options.DryRun = []string{metav1.DryRunAll}
	}
	var created *unstructured.Unstructured
	decision, err := perform(ctx, where, target, "create", "", dryRun, func(ctx context.Context) error {
		iface, err := createInterface(where, target, object)
		if err != nil {
			return err
		}
		created, err = iface.Create(ctx, object, options)
		if err != nil {
			err = onTheWire(ctx, createsObject, err)
			if dryRun {
				if incomplete := checkIncomplete(err); incomplete != nil {
					return incomplete
				}
			}
			return apiRefusal(err)
		}
		return nil
	})
	if err != nil {
		return nil, decision, err
	}
	return created, decision, nil
}

// createInterface is every check there is, and the endpoint if they all hold.
//
// One block, testable without a cluster, so the two halves of a
// validate-then-commit pair cannot ask different questions.
func createInterface(where Location, target Target, object *unstructured.Unstructured) (dynamic.ResourceInterface, error) {
	// A Location that did not come from locate() has not been through the
	// read-only guardrail. Repeated from live(), which a create never reaches.
	if !where.guarded {
		return nil, refuse(RefusedInput,
			"this create was built on a location that never went through the read-only "+
				"guardrail; call Locate rather than assembling a Location")
	}
	if object == nil {
		return nil, refuse(RefusedInput, "there is no object to create")
	}
	if target.Name == "" {
		return nil, refuse(RefusedInput, "no object name was given")
	}

	// A create sends the body's own name, so a disagreement would create
	// something under a name no guard and no confirmation saw.
	if name := object.GetName(); name != target.Name {
		return nil, refuse(RefusedInput,
			"this object is called %q and the request is addressed to %q; a create sends the "+
				"body's own name, so the two have to agree", name, target.Name)
	}
	if namespace := object.GetNamespace(); namespace != target.Namespace {
		return nil, refuse(RefusedInput,
			"this object declares namespace %q and the request is addressed to %q; the two have to agree",
			namespace, target.Namespace)
	}
	if object.GetAPIVersion() == "" || object.GetKind() == "" {
		return nil, refuse(RefusedInput, "this object carries no apiVersion or no kind, so nothing can be created from it")
	}
	// The body's kind is the endpoint's, or nothing is sent: otherwise the
	// request would be reviewed as one kind and decoded as another.
	if err := sameKind(where, object.GetAPIVersion(), object.GetKind()); err != nil {
		return nil, err
	}

	// The scope, refused in both directions.
	if !where.Namespaced {
		if target.Namespace != "" {
			return nil, refuse(RefusedInput,
				"%s is cluster-scoped on this cluster and a namespace (%q) was given for it",
				where.Resource.Resource, target.Namespace)
		}
		return where.Dynamic.Resource(where.Resource), nil
	}
	if target.Namespace == "" {
		return nil, refuse(RefusedInput, "%s is namespaced and no namespace was given", where.Resource.Resource)
	}
	return where.Dynamic.Resource(where.Resource).Namespace(target.Namespace), nil
}

// APIVersionOf is the `apiVersion` string for the version of a kind this
// cluster serves, taken from the Location the mapper produced. Hardcoding
// it (`argoproj.io/v1alpha1`) would make the object uncreatable the release
// its project moves on, with a 404 naming a kind the console just listed.
func APIVersionOf(where Location) string {
	if where.Resource.Group == "" {
		return where.Resource.Version
	}
	return fmt.Sprintf("%s/%s", where.Resource.Group, where.Resource.Version)
}

// sameKind refuses a body whose `kind` or API group is not the resource it is
// about to be sent to. Empty is accepted on a patch (a merge patch that
// leaves `kind` out names no other); a create has already refused empty.
// The version may differ, since the API server converts between served
// versions; the group may not, because a group is a different API.
func sameKind(where Location, apiVersion, kind string) error {
	if kind != "" && !strings.EqualFold(kind, where.Kind) {
		return refuse(RefusedInput,
			"this document is a %s and the request is addressed to %s; the kind reviewed and written is "+
				"the one the request goes to, so the two have to agree", kind, where.Resource.Resource)
	}
	if apiVersion == "" {
		return nil
	}
	group := ""
	if i := strings.LastIndex(apiVersion, "/"); i >= 0 {
		group = apiVersion[:i]
	}
	if group != where.Resource.Group {
		return refuse(RefusedInput,
			"this document's apiVersion %q is not in the API group of %s (%q); the kind reviewed and "+
				"written is the one the request goes to, so the two have to agree",
			apiVersion, where.Resource.Resource, where.Resource.Group)
	}
	return nil
}
