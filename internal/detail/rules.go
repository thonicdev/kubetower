package detail

// A Role's rules, read off the object already in hand, so the object page shows
// the permissions the Role exists to grant without the YAML. `rules` is part of
// the object `Reader.Get` fetched: no extra cluster read, binding walk or
// `SelfSubjectRulesReview`.
//
// Wildcards are marked, never expanded: a bare `*` cell reads as a resource
// named `*`, and expanding it against discovery or resolving an
// `aggregationRule` would be doing the API server's job and disagree at the
// edges. The vocabulary is `ruleSummary`'s (internal/resources/rows_access.go),
// re-derived per rule because that function is unexported and grades a whole
// Role; `TestTheRulesPanelGradesAWildcardTheWayTheListDoes` keeps them aligned.

import (
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/resources"
)

// PolicyRule is one row of the panel: one entry of a Role's `rules` array,
// already graded.
type PolicyRule struct {
	// The rule's own four lists, in reading order; nil where the rule carries
	// none (or no string entries).
	APIGroups     []string
	Resources     []string
	Verbs         []string
	ResourceNames []string
	// NonResourceURLs is the other kind of rule (`/healthz`, `/metrics`): it
	// carries no resources, and must not read as a rule that grants nothing.
	NonResourceURLs []string

	// Wildcards is where a `*` fell, in `ruleSummary`'s words ("api groups",
	// "resources", "verbs"), joined; empty where none did.
	Wildcards string
	// WildcardClass is "", "warn" or "bad": colored on any `*`, worst when it
	// falls on both verbs and resources, which is cluster-admin by another name.
	WildcardClass string
}

// RuleSet is a Role's or a ClusterRole's whole rules block, and nil on every
// other kind.
//
// Empty and unreadable are kept apart here: someone opens this to conclude
// "that is all this Role can do", so silently showing fewer rules than exist
// is worse than refusing.
type RuleSet struct {
	Rules []PolicyRule
	// Unreadable is why the rules array could not be read, and empty when it
	// was. A malformed `rules` must not be drawn as a Role that grants nothing.
	Unreadable string
	// Note is what the panel must say about what it drew: an aggregated
	// ClusterRole's rules are written by a controller and move with the roles
	// it selects.
	Note string
	// Selectors are an aggregated ClusterRole's `clusterRoleSelectors`, one
	// entry per selector, each the list of its requirements ("k=v",
	// "k in (a,b)", "k", "!k"); nil on any other object.
	Selectors [][]string
}

// hasRules reports whether this kind has a rules block: Role and ClusterRole in
// `rbac.authorization.k8s.io` only. A CRD whose Kind is "Role" is not one (the
// same test `maskSecretValues` makes).
func hasRules(gvk schema.GroupVersionKind) bool {
	if gvk.Group != "rbac.authorization.k8s.io" {
		return false
	}
	return gvk.Kind == "Role" || gvk.Kind == "ClusterRole"
}

func rulesOf(raw *unstructured.Unstructured, gvk schema.GroupVersionKind) *RuleSet {
	if !hasRules(gvk) {
		return nil
	}

	items, found, err := unstructured.NestedSlice(raw.Object, "rules")
	if err != nil {
		return &RuleSet{Unreadable: "this object's rules field is not a list of rules: " + err.Error()}
	}
	// Found and empty, or absent, is a Role that grants nothing: a real
	// answer, not a failed panel.
	if !found || len(items) == 0 {
		return &RuleSet{Note: aggregationNote(raw, gvk), Selectors: selectorsOf(raw, gvk)}
	}

	out := make([]PolicyRule, 0, len(items))
	for at, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			return &RuleSet{Unreadable: fmt.Sprintf("rule %d of this object is not a rule", at+1)}
		}
		rule := PolicyRule{
			APIGroups:       stringsAt(entry, "apiGroups"),
			Resources:       stringsAt(entry, "resources"),
			Verbs:           stringsAt(entry, "verbs"),
			ResourceNames:   stringsAt(entry, "resourceNames"),
			NonResourceURLs: stringsAt(entry, "nonResourceURLs"),
		}
		rule.Wildcards, rule.WildcardClass = gradeWildcards(rule)
		out = append(out, rule)
	}

	return &RuleSet{Rules: out, Note: noteFor(out, raw, gvk), Selectors: selectorsOf(raw, gvk)}
}

// gradeWildcards is `ruleSummary`'s rule applied to one rule instead of a whole
// Role, with the same words and classes, so the panel and the grid agree.
func gradeWildcards(rule PolicyRule) (string, string) {
	const all = "*"
	var flags []string
	if slices.Contains(rule.APIGroups, all) {
		flags = append(flags, "api groups")
	}
	anyResource := slices.Contains(rule.Resources, all)
	if anyResource {
		flags = append(flags, "resources")
	}
	anyVerb := slices.Contains(rule.Verbs, all)
	if anyVerb {
		flags = append(flags, "verbs")
	}
	if len(flags) == 0 {
		return "", ""
	}
	// Both: cluster-admin by another name, ranked first by the list.
	if anyResource && anyVerb {
		return strings.Join(flags, ", "), resources.ClassBad
	}
	return strings.Join(flags, ", "), resources.ClassWarn
}

// noteFor is what the panel says about the rendered rules; today only the
// aggregation note.
func noteFor(_ []PolicyRule, raw *unstructured.Unstructured, gvk schema.GroupVersionKind) string {
	return aggregationNote(raw, gvk)
}

// aggregationNote is set for an aggregated ClusterRole: its `rules` are written by
// the controller from the roles its selector matches, accurate at read time and
// moving when those roles do; a bare table would read as a fixed grant.
func aggregationNote(raw *unstructured.Unstructured, gvk schema.GroupVersionKind) string {
	if gvk.Kind != "ClusterRole" {
		return ""
	}
	if _, found, err := unstructured.NestedMap(raw.Object, "aggregationRule"); err != nil || !found {
		return ""
	}
	return "This ClusterRole is aggregated: these rules are written by the controller from the roles " +
		"its label selector matches, and they change when those roles do."
}

// selectorsOf reads an aggregated ClusterRole's `aggregationRule.clusterRoleSelectors`
// off the object in hand, leniently: a malformed entry is skipped, since the
// rules panel is still true without it. Requirements are sorted for a stable
// reading order.
func selectorsOf(raw *unstructured.Unstructured, gvk schema.GroupVersionKind) [][]string {
	if gvk.Kind != "ClusterRole" {
		return nil
	}
	items, found, err := unstructured.NestedSlice(raw.Object, "aggregationRule", "clusterRoleSelectors")
	if err != nil || !found {
		return nil
	}
	var out [][]string
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		var reqs []string
		if labels, ok := entry["matchLabels"].(map[string]any); ok {
			for key, value := range labels {
				text, _ := value.(string)
				reqs = append(reqs, key+"="+text)
			}
			slices.Sort(reqs)
		}
		if exprs, ok := entry["matchExpressions"].([]any); ok {
			for _, expr := range exprs {
				e, ok := expr.(map[string]any)
				if !ok {
					continue
				}
				key, _ := e["key"].(string)
				op, _ := e["operator"].(string)
				values := strings.Join(stringsAt(e, "values"), ",")
				switch op {
				case "In":
					reqs = append(reqs, key+" in ("+values+")")
				case "NotIn":
					reqs = append(reqs, key+" notin ("+values+")")
				case "Exists":
					reqs = append(reqs, key)
				case "DoesNotExist":
					reqs = append(reqs, "!"+key)
				}
			}
		}
		out = append(out, reqs)
	}
	return out
}

// stringsAt reads one of a rule's string lists leniently: a non-string value is
// dropped rather than failing the panel (the API server keeps these well formed).
// An absent or empty list is nil.
func stringsAt(entry map[string]any, key string) []string {
	items, ok := entry[key].([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
