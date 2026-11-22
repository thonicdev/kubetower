package detail

// The object's annotations, as the Details tab may show them. Unlike labels,
// annotations are free text of any length and content, so two treatments apply:
//
//   - `kubectl.kubernetes.io/last-applied-configuration` never leaves this
//     process: it is the whole object again, `data` included, so on a Secret
//     every value in clear. It is deleted here and not by relying on `Trimmed`,
//     which runs only on the manifest path (`manifestOf`, `internal/edit`).
//   - On a Secret every remaining value is masked, since an annotation can hold
//     a credential as easily as a note. Keys are kept, so a withheld value does
//     not look like an absent key.
//
// The cost is accepted: harmless values such as `meta.helm.sh/release-name` read
// `<masked>` on a Secret. Over-masking wastes a click; under-masking cannot be
// undone.

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// LastAppliedAnnotation is what `kubectl apply` writes to remember what it last
// sent. One constant for the manifest path, which deletes it, and the details
// path, which withholds it.
const LastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// annotationsOf is the object's annotations as the Details tab may draw them.
func annotationsOf(raw *unstructured.Unstructured, gvk schema.GroupVersionKind) map[string]string {
	stored := raw.GetAnnotations()
	if len(stored) == 0 {
		return nil
	}

	// The same test as `maskSecretValues`: a CRD whose Kind is "Secret" in
	// another group is not a core Secret.
	secret := gvk.Group == "" && gvk.Kind == "Secret"

	out := make(map[string]string, len(stored))
	for key, value := range stored {
		if key == LastAppliedAnnotation {
			continue
		}
		if secret && value != "" {
			// `<masked>` over an empty string would claim something was withheld.
			out[key] = SecretValueMask
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		out = nil
	}

	return out
}
