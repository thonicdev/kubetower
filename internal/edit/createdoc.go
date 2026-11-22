package edit

// A new object from a document the person wrote: the list's "+".
//
// Preview then apply, each doing all the work from scratch. Not the manifest
// path with a flag: that path patches a live object it fetches, and a create
// has nothing to fetch. Create (create.go) refuses an unguarded Location, a
// body addressed elsewhere and a wrong scope, and reviews `create` first.
//
// Two refusals of its own:
//
//   - a resourceVersion in the document: it was copied from an existing
//     object, which the person meant to edit;
//   - an object that already exists: said as the mistake it is (a create
//     never replaces anything), not as a refused change that reads like a
//     permission problem.

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/detail"
)

// CreateChange is a new object as the person wrote it. Target is read out of
// the document by the caller, never from a route or a sibling field; Create
// refuses a body that disagrees with it.
type CreateChange struct {
	Target
	YAML string
}

// CreatePreview is what the person confirms.
type CreatePreview struct {
	Target
	// Title is the kind as a person writes it: "Deployment".
	Title string
	// Diff is the document that would be sent, every line an addition,
	// trimmed as the drawer trims and with a Secret's values masked. Not the
	// dry run's answer, which carries fields the server fills in.
	Diff  []DiffLine
	Added int
	// Request is the body that leaves the process, as JSON, masked on a
	// Secret.
	Request string
	// Fields are the paths the document sets.
	Fields []string
	// Validated is what the dry run said.
	Validated string
}

// Created is a create that happened.
type Created struct {
	Target
	ResourceVersion string
	// Resource and Namespaced are where the object lives, for the caller to
	// name the page it is now on.
	Resource   schema.GroupVersionResource
	Namespaced bool
}

// PreviewCreate validates the new object with a dry run, and creates nothing.
func (w *Writer) PreviewCreate(ctx context.Context, change CreateChange) (CreatePreview, error) {
	ready, err := w.prepareCreate(ctx, change)
	if err != nil {
		return CreatePreview{}, err
	}
	// The document as it leaves, not the dry run's answer: the server fills
	// in a uid, timestamps and a resourceVersion it will set again on the
	// real create, which would read as part of what the person is creating.
	request, err := normalise(detail.Trimmed(ready.object).Object)
	if err != nil {
		return CreatePreview{}, err
	}
	after, err := renderYAML(request)
	if err != nil {
		return CreatePreview{}, err
	}
	lines, stat := diffLines("", after)
	return CreatePreview{
		Target:  change.Target,
		Title:   ready.where.Kind,
		Diff:    lines,
		Added:   stat.Added,
		Request: pretty(request),
		Fields:  paths(ready.object.Object),
		Validated: fmt.Sprintf("The API server was asked to create this %s as a dry run and accepted it. "+
			"Nothing exists until it is confirmed.", ready.where.Kind),
	}, nil
}

// ApplyCreate creates the object, redoing all of PreviewCreate's work, dry
// run included, so an object created in between is refused here.
func (w *Writer) ApplyCreate(ctx context.Context, change CreateChange) (Created, error) {
	ready, err := w.prepareCreate(ctx, change)
	if err != nil {
		return Created{}, err
	}
	created, decision, err := Create(ctx, ready.where, change.Target, ready.object.DeepCopy(), false)
	if err != nil {
		return Created{}, notOverwritten(err, ready.where, change.Target)
	}
	w.Logf(ctx, "write: cluster=%q kind=%s namespace=%s name=%s action=create decision=%s outcome=applied",
		ready.where.Cluster, ready.where.Resource.Resource, change.Namespace, change.Name, decision)
	return Created{
		Target:          change.Target,
		ResourceVersion: created.GetResourceVersion(),
		Resource:        ready.where.Resource,
		Namespaced:      ready.where.Namespaced,
	}, nil
}

type preparedCreate struct {
	where  Location
	object *unstructured.Unstructured
}

// prepareCreate runs every check in the order that refuses soonest: the
// read-only guardrail before parsing, the document's own refusals before
// any review, and the dry run (reviewed inside Create) last.
func (w *Writer) prepareCreate(ctx context.Context, change CreateChange) (*preparedCreate, error) {
	where, err := w.locate(ctx, change.Target)
	if err != nil {
		return nil, err
	}
	document, err := parseDocument(change.YAML)
	if err != nil {
		return nil, err
	}
	if metadata, ok := document["metadata"].(map[string]any); ok {
		if version, present := metadata["resourceVersion"]; present && version != nil && version != "" {
			return nil, refuse(RefusedInput,
				"this document carries a resourceVersion, which only an object that already exists has; "+
					"a create makes a new object - remove it, or edit the existing object from its page")
		}
	}
	object := &unstructured.Unstructured{Object: document}
	if _, _, err := Create(ctx, where, change.Target, object.DeepCopy(), true); err != nil {
		return nil, notOverwritten(err, where, change.Target)
	}
	return &preparedCreate{where: where, object: object}, nil
}

// notOverwritten says AlreadyExists as what it is. The API server answers it
// only after `create` was reviewed for the person, so someone who may not
// create learns nothing about what exists.
//
// Only when it is the cluster's answer to this request (RefusedByAPI): a
// create that timed out and was retried meets AlreadyExists from its own
// first attempt, and stays "may have happened".
func notOverwritten(err error, where Location, target Target) error {
	if refusal, ok := AsRefusal(err); !ok || refusal.Kind != RefusedByAPI || !apierrors.IsAlreadyExists(err) {
		return err
	}
	in := ""
	if target.Namespace != "" {
		in = " in " + target.Namespace
	}
	return refuseFrom(err, RefusedInput,
		"a %s called %q already exists%s; this creates a new object and never replaces one - "+
			"choose another name, or edit the existing one from its page", where.Kind, target.Name, in)
}
