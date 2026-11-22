package web

// The list's "+": the template it opens, the verdict that draws it, and the
// create its plan dialog confirms. The split is the security of it:
//
//   - The template reviews nothing: it is a document, and reading it is not
//     permission to create.
//   - ListPage.create is the courtesy: `create` on the kind in the namespace in
//     view, reviewed through reviewBoth. It decides how the button is drawn,
//     never what happens.
//   - PreviewCreate and ApplyCreate are the control: through edit.Create, which
//     reviews `create` for the person at the write door before the dry run and
//     again before the commit, whatever the list said.

import (
	"context"
	"fmt"
	"strings"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/edit"
	"github.com/thonicdev/kubetower/internal/resources"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// CreateTemplate resolves the list's slug (built-in first, then the cluster's
// own kinds, as resolveKind does) and writes the document from the kind the
// cluster serves. A slug naming no kind answers `no_such_kind`.
func (r apiReads) CreateTemplate(ctx context.Context, q api.CreateTemplateQuery) (api.CreateTemplate, *api.Failure) {
	scope := api.Scope{Cluster: q.Cluster, Resource: q.Kind, Namespace: q.Namespace}
	if _, err := r.s.resolveKind(ctx, q.Cluster, q.Kind); err != nil {
		return api.CreateTemplate{}, stateOf(err, scope)
	}
	where, err := editResolver{objects: r.s.objects}.Locate(ctx, q.Cluster, q.Kind)
	if err != nil {
		return api.CreateTemplate{}, stateOf(err, scope)
	}
	document, generic, err := edit.Template(where, q.Namespace)
	if err != nil {
		return api.CreateTemplate{}, stateOf(err, scope)
	}
	return api.CreateTemplate{Document: document, Kind: where.Kind, Generic: generic}, nil
}

// PreviewCreate reads the address out of the document alone, after parsing, as
// PreviewManifest does, and dry-runs a create of it.
func (a apiActs) PreviewCreate(ctx context.Context, name string, req api.ManifestWrite) (api.PlanFacts, error) {
	if a.s.writer == nil {
		return api.PlanFacts{}, fmt.Errorf("%w: this build has no write path", api.ErrNotInProfile)
	}
	if req.ResourceVersion != "" {
		return api.PlanFacts{}, fmt.Errorf("%w: a new object has no resourceVersion", api.ErrInvalid)
	}
	target, err := manifestTarget(name, req.Document)
	if err != nil {
		return api.PlanFacts{}, err
	}
	preview, err := a.s.writer.PreviewCreate(ctx, edit.CreateChange{Target: target, YAML: req.Document})
	if err != nil {
		return api.PlanFacts{}, writeError(err)
	}
	return api.PlanFacts{
		Verb: "create",
		Targets: []api.PlanTarget{{
			Kind: target.Kind, Namespace: target.Namespace, Name: target.Name, Note: preview.Validated,
		}},
		Count: 1,
		// The document's own kind, named as the manifest's "Apply this" names it.
		Button: "Create this " + api.KindWord(target.Kind, 1),
		// Nothing existing is changed: a create of an object that exists is refused.
		Destructive:  false,
		Diff:         diffText(preview.Diff),
		Fields:       preview.Fields,
		Request:      preview.Request,
		Confirmation: &api.Confirmation{Kind: "acknowledge"},
	}, nil
}

// ApplyCreate creates the object and names it in `next`, so the client goes to
// the object it made.
func (a apiActs) ApplyCreate(ctx context.Context, name string, req api.ManifestWrite) (api.ActOutcome, error) {
	if a.s.writer == nil {
		return api.ActOutcome{}, fmt.Errorf("%w: this build has no write path", api.ErrNotInProfile)
	}
	target, err := manifestTarget(name, req.Document)
	if err != nil {
		return api.ActOutcome{}, err
	}
	created, err := a.s.writer.ApplyCreate(ctx, edit.CreateChange{Target: target, YAML: req.Document})
	if err != nil {
		return api.ActOutcome{}, writeError(err)
	}
	namespace := ""
	if created.Namespaced {
		namespace = target.Namespace
	}
	return api.ActOutcome{
		Succeeded: 1,
		Results: []api.ActResult{{
			Target:  api.PlanTarget{Kind: target.Kind, Namespace: target.Namespace, Name: target.Name},
			OK:      true,
			Message: "created at " + created.ResourceVersion,
		}},
		Next: []api.Ref{{
			Text: target.Name,
			Href: detailURL(name, kindSlug(created.Resource), namespace, target.Name),
		}},
	}, nil
}

// kindSlug is how the console's URLs spell the resource a create went to: the
// plural alone for a built-in kind, `plural.group` otherwise (the custom
// resource's slug). Built-in first, as resolveKind is, so a CRD whose plural is
// `services` keeps its group.
func kindSlug(resource schema.GroupVersionResource) string {
	group := resource.Group
	project := group == "" || !strings.Contains(group, ".") || strings.HasSuffix(group, ".k8s.io")
	if _, known := resources.Lookup(resource.Resource); known && project {
		return resource.Resource
	}
	if group == "" {
		return resource.Resource
	}
	return resource.Resource + "." + group
}

// neverCreated are the lists with no "+" at all: kinds whose objects the cluster
// makes itself (a Node is a kubelet's registration, an Event a report, Endpoints
// the Service controller's). Keyed by slug, so a custom resource of the same
// plural keeps its button. This decides the button, not the write.
var neverCreated = map[string]bool{"nodes": true, "events": true, "endpoints": true}

// createAffordance is ListPage.create: whether this person may create an object
// of this kind in the namespace in view, or cluster-wide for a cluster-scoped
// kind. Where they may not it is present, permitted false, with a reason, so the
// button is drawn disabled; absent only on neverCreated's kinds. Whether a kind
// supports `create` is not read here (the mapper holds no verbs); the API
// server's dry run refuses one that does not.
func (s *Server) createAffordance(ctx context.Context, cluster, kind string, namespaced bool, namespaces []string) *api.CreateAffordance {
	if neverCreated[kind] {
		return nil
	}
	out := s.createAnswer(ctx, cluster, kind, namespaced, namespaces)
	// The button's name, whatever the verdict. The mapper is the detail reader's, so
	// this asks nothing new.
	if where, err := (editResolver{objects: s.objects}).Locate(ctx, cluster, kind); err == nil {
		out.Kind = where.Kind
	}
	return out
}

// createAnswer is createAffordance's verdict, before the kind is named.
func (s *Server) createAnswer(ctx context.Context, cluster, kind string, namespaced bool, namespaces []string) *api.CreateAffordance {
	if writable, note := s.writable(cluster); !writable {
		reason := api.Because(api.CodeVerbDenied, note).Retry(false)
		return &api.CreateAffordance{Reason: &reason}
	}
	if namespaced && len(namespaces) != 1 {
		reason := api.Because(api.CodeNotSupportedHere,
			"choose one namespace to create in: a new object goes to exactly one, and this list shows "+
				shownNamespaces(namespaces)).Retry(false)
		return &api.CreateAffordance{Reason: &reason}
	}
	where, err := editResolver{objects: s.objects}.Locate(ctx, cluster, kind)
	if err != nil {
		reason := unreviewableReason(firstLine(err.Error()))
		return &api.CreateAffordance{Reason: &reason}
	}
	namespace := ""
	if where.Namespaced && len(namespaces) == 1 {
		namespace = namespaces[0]
	}
	return createVerdict(ctx, s.gate, where, namespace)
}

func shownNamespaces(namespaces []string) string {
	if len(namespaces) == 0 {
		return "every namespace"
	}
	return fmt.Sprintf("%d namespaces", len(namespaces))
}

// createVerdict is createAffordance once the kind is resolved: the question
// edit.Create is reviewed with (`create`, no name), asked through reviewBoth, so
// the page and the write share one held answer.
func createVerdict(ctx context.Context, gate *access.Gate, where edit.Location, namespace string) *api.CreateAffordance {
	attrs := edit.Attributes(where, edit.Target{Namespace: namespace}, "create", "")
	v := reviewBoth(ctx, gate, where.Cluster, []access.Attributes{attrs})[0]
	out := &api.CreateAffordance{Namespace: namespace}
	switch {
	case v.decision == access.Allowed:
		out.Permitted = true
	case v.decision == access.Denied && v.console:
		// The console's ServiceAccount may not: name the missing grant.
		reason := api.Because(api.CodeVerbDenied, v.reason).Retry(false)
		out.Reason = &reason
	case v.decision == access.Denied:
		where := ""
		if namespace != "" {
			where = " in " + namespace
		}
		reason := api.Because(api.CodeVerbDenied,
			"you may not create "+grantedWhat(attrs)+where).Retry(false)
		out.Reason = &reason
	case v.through:
		// The desktop: the kubeconfig's own identity goes, and the cluster decides when
		// the person acts.
		out.Permitted = true
		reason := throughReason(v.reason)
		out.Reason = &reason
	default:
		reason := unreviewableReason(v.reason)
		out.Reason = &reason
	}
	return out
}
