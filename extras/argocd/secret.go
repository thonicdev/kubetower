package argocd

import (
	"errors"
	"net/http"
	"strings"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/extras/argocd/drift"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A Secret is never read by this extra. Host.List refuses `v1/secrets`
// (extension.ErrSecretsNotListed): a list would hand over every value. So the
// resource route answers a Secret from the Applications alone - the one whose
// `status.resources` lists it, and that entry's sync word - and, when
// argocd-server may be asked, from Argo CD's own comparison, masked: the keys
// that differ, never a value.

// isSecret is core `v1/secrets`.
func isSecret(t target) bool {
	return t.gvr.Group == "" && t.gvr.Resource == "secrets"
}

// trackSecret is the resource route's answer for a Secret.
func trackSecret(r *http.Request, h extension.Host, cluster string, t target, namespace, name string) Tracked {
	out := Tracked{Kind: t.kind, Namespace: namespace, Name: name, Secret: true,
		Drift: Drift{Fields: []drift.Field{}, Masked: true}}
	// What the comparison is made with: the Secret's identity, no field of it.
	object := unstructured.Unstructured{Object: map[string]any{}}
	object.SetAPIVersion("v1")
	object.SetKind(t.kind)
	object.SetNamespace(namespace)
	object.SetName(name)

	application, resource := secretApplication(r, h, cluster, t, object, &out)
	if application == nil || resource == nil {
		if out.ApplicationCode == "" {
			out.Note = "No Argo CD Application lists this Secret."
		}
		return out
	}
	out.Managed = true
	out.Via = "application"
	out.Application, out.Resource = application, resource
	if application.Operation != nil && application.Operation.Revision != "" {
		out.SyncedRevision = application.Operation.Revision
	} else if len(application.History) > 0 {
		out.SyncedRevision = application.History[0].Revision
	}
	diff := argoDiff(r, h, cluster, application, t, object)
	diff.Masked = true
	diff.Fields = secretKeys(diff.Fields)
	out.ArgoDiff = &diff
	return out
}

// secretApplication finds, among the Applications the person may read, the one
// whose resources list the Secret. A failure is written on out.
func secretApplication(r *http.Request, h extension.Host, cluster string, t target, object unstructured.Unstructured, out *Tracked) (*ApplicationDetail, *Resource) {
	registry, err := h.Definitions(r, cluster)
	if err != nil {
		out.ApplicationCode, out.ApplicationNote = CodeCatalogueUnreadable, "The custom resource catalog could not be read."
		return nil, nil
	}
	crd, err := registry.Lookup(r.Context(), ApplicationKind)
	switch {
	case errors.Is(err, extension.ErrUnknownKind):
		return nil, nil
	case err != nil:
		out.ApplicationCode, out.ApplicationNote = CodeKindUnresolved, "Resolving "+ApplicationKind+" failed."
		return nil, nil
	}
	page, err := h.List(r, cluster, extension.ListQuery{GVR: crd.GVR()})
	switch {
	case errors.Is(err, extension.ErrDenied), apierrors.IsForbidden(err):
		out.ApplicationCode, out.ApplicationNote = CodeListForbidden, capital(whoPerson)+" may not read Argo CD's Applications."
		return nil, nil
	case errors.Is(err, extension.ErrUnreviewable):
		out.ApplicationCode, out.ApplicationNote = CodeListUnreviewed, "The console could not ask whether you may read Argo CD's Applications."
		return nil, nil
	case err != nil:
		out.ApplicationCode, out.ApplicationNote = CodeListFailed, "The Applications could not be read."
		return nil, nil
	}
	for i := range page.Items {
		detail := applicationDetail(page.Items[i])
		if res := listed(detail.Resources, t, object); res != nil {
			return &detail, res
		}
	}
	return nil, nil
}

// secretKeys turns a masked comparison into one field per key of the Secret
// that differs: `data.password`, `stringData.token`, never a value. Anything
// else that differs (labels, type) keeps its path, still without a value.
func secretKeys(fields []drift.Field) []drift.Field {
	out := make([]drift.Field, 0, len(fields))
	seen := map[string]bool{}
	for _, f := range fields {
		path := f.Path
		for _, top := range []string{"data.", "stringData."} {
			if rest, ok := strings.CutPrefix(path, top); ok {
				key, _, _ := strings.Cut(rest, ".")
				path = top + key
			}
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, drift.Field{Path: path, Change: f.Change})
	}
	return out
}
