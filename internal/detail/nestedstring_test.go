package detail

import "testing"

// nestedString reads what is there, and does not repair it.
//
// `internal/argocd` and `internal/security` have a *different* function,
// `stringAt`: it takes a single key and `strings.TrimSpace`s the result, so on
// `" HIGH "` it answers `"HIGH"` where this answers `" HIGH "`.
//
// Merging them looks obvious and is wrong, so the difference is asserted rather
// than removed. Trimming is right for a scanner's enum word; here the values are
// names, namespaces and classes that an object is then *resolved by*. A namespace
// with a trailing space is a namespace Kubernetes does not have, and silently
// repairing it would hide the object that has it behind a lookup for one that
// does not exist.
func TestNestedStringReadsAValueWithoutRepairingIt(t *testing.T) {
	obj := map[string]any{
		"metadata": map[string]any{"namespace": " kube-system "},
		"spec":     map[string]any{"storageClassName": "gp3", "nodeName": ""},
	}

	if got := nestedString(obj, "metadata", "namespace"); got != " kube-system " {
		t.Errorf("nestedString trimmed a namespace to %q. Trimming belongs to the packages "+
			"reading a scanner's severity word, not to the one that resolves objects by name: "+
			"a padded namespace is one this cluster does not have, and quietly fixing it looks "+
			"up a different object", got)
	}
	if got := nestedString(obj, "spec", "storageClassName"); got != "gp3" {
		t.Errorf("nestedString(spec.storageClassName) = %q, want %q", got, "gp3")
	}
	// A path, not a key — the other half of the difference. A single-key call
	// would have to answer here, and it answers nothing.
	if got := nestedString(obj, "storageClassName"); got != "" {
		t.Errorf("nestedString read %q from a top-level key that does not exist, so it is "+
			"behaving as the single-key stringAt the other two packages have", got)
	}
	if got := nestedString(obj, "spec", "nodeName"); got != "" {
		t.Errorf("nestedString(spec.nodeName) = %q, want empty", got)
	}
}
