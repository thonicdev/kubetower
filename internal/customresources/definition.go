package customresources

// A CustomResourceDefinition is read out of the unstructured object by hand:
// the apiextensions/v1 types live in k8s.io/apiextensions-apiserver, which
// this binary does not link.

import (
	"strings"
	"unicode"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	// Named: this file's own `version` type is a spec.versions entry.
	kubeversion "k8s.io/apimachinery/pkg/version"
)

// definitionFrom reduces one CRD object to a Definition. It returns false for
// a CRD that cannot be addressed (no group, plural or kind): the cluster's
// problem, with nowhere to report it.
func definitionFrom(object *unstructured.Unstructured) (Definition, bool) {
	if object == nil {
		return Definition{}, false
	}
	spec, ok, err := unstructured.NestedMap(object.Object, "spec")
	if !ok || err != nil {
		return Definition{}, false
	}

	definition := Definition{
		Group:    nestedString(spec, "group"),
		Kind:     nestedString(spec, "names", "kind"),
		Plural:   nestedString(spec, "names", "plural"),
		Singular: nestedString(spec, "names", "singular"),
		// The scope alone decides this, never a URL or whether a namespace
		// was supplied.
		Namespaced: nestedString(spec, "scope") == "Namespaced",
		ShortNames: nestedStrings(spec, "names", "shortNames"),
	}
	if definition.Group == "" || definition.Plural == "" || definition.Kind == "" {
		return Definition{}, false
	}

	versions := versionsOf(spec)
	chosen, served := pickVersion(versions)
	definition.Served = served
	if chosen != nil {
		definition.Version = chosen.name
		definition.Columns = chosen.columns
	} else if len(versions) > 0 {
		// Unserved, but a version still lets an error name the kind fully.
		definition.Version = versions[0].name
	}
	return definition, true
}

// version is one entry of spec.versions. There is no `storage` field: the API
// server converts to whichever served version is asked for (see pickVersion).
type version struct {
	name    string
	served  bool
	columns []PrinterColumn
}

func versionsOf(spec map[string]any) []version {
	raw, ok, err := unstructured.NestedSlice(spec, "versions")
	if !ok || err != nil {
		return nil
	}
	out := make([]version, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := nestedString(m, "name")
		if name == "" {
			continue
		}
		out = append(out, version{
			name:    name,
			served:  nestedBool(m, "served"),
			columns: printerColumns(m),
		})
	}
	return out
}

// pickVersion chooses the version rendered: the served version discovery
// prefers, the highest in Kube-aware order.
//
// The drawer, the YAML view and edit resolve a kind through `internal/kindref`,
// whose mapper is built from discovery; this must give the same answer, or a
// row is listed at one version and opened at another. The storage version is
// not used: the API server converts to any served version, and a CRD mid
// migration would otherwise be listed at the old version.
//
// The rule is the API server's (apiextensions-apiserver's discovery
// controller sorts served versions with CompareKubeAwareVersionStrings and
// prefers the first), which client-go's RESTMapper and kubectl follow. To
// match it, `deprecated` plays no part, and the columns are the chosen
// version's even when it declares none: another version's JSONPaths describe
// another shape. Two names Kube-aware order cannot tell apart (`v1`, `v01`)
// keep the first one declared.
func pickVersion(versions []version) (*version, bool) {
	var best *version
	for i := range versions {
		if !versions[i].served {
			continue
		}
		if best == nil || kubeversion.CompareKubeAwareVersionStrings(versions[i].name, best.name) > 0 {
			best = &versions[i]
		}
	}
	return best, best != nil
}

func printerColumns(version map[string]any) []PrinterColumn {
	raw, ok, err := unstructured.NestedSlice(version, "additionalPrinterColumns")
	if !ok || err != nil {
		return nil
	}
	out := make([]PrinterColumn, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		column := PrinterColumn{
			Name:        nestedString(m, "name"),
			Type:        nestedString(m, "type"),
			JSONPath:    nestedString(m, "jsonPath"),
			Priority:    nestedInt32(m, "priority"),
			Description: nestedString(m, "description"),
		}
		if column.Name == "" || column.JSONPath == "" {
			continue
		}
		out = append(out, column)
	}
	return out
}

// --- reading unstructured without four returns per field -------------------

func nestedString(m map[string]any, fields ...string) string {
	s, ok, err := unstructured.NestedString(m, fields...)
	if !ok || err != nil {
		return ""
	}
	return s
}

func nestedBool(m map[string]any, fields ...string) bool {
	b, ok, err := unstructured.NestedBool(m, fields...)
	if !ok || err != nil {
		return false
	}
	return b
}

// nestedInt32 accepts every number type a priority can arrive as: int64 from
// JSON, int from a hand-built map.
func nestedInt32(m map[string]any, fields ...string) int32 {
	value, ok, err := unstructured.NestedFieldNoCopy(m, fields...)
	if !ok || err != nil {
		return 0
	}
	switch n := value.(type) {
	case int64:
		return int32(n)
	case int32:
		return n
	case int:
		return int32(n)
	case float64:
		return int32(n)
	default:
		return 0
	}
}

func nestedStrings(m map[string]any, fields ...string) []string {
	values, ok, err := unstructured.NestedStringSlice(m, fields...)
	if !ok || err != nil {
		return nil
	}
	return values
}

// capitalize upper-cases the first letter only: the plural is the CRD author's
// word, and invented camel case would be a heading nobody can search for.
func capitalise(s string) string {
	if s == "" {
		return ""
	}
	runes := []rune(s)
	runes[0] = unicode.ToUpper(runes[0])
	return strings.TrimSpace(string(runes))
}
