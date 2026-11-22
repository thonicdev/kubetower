package resources

import (
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
)

// A role is cluster-admin under another name only when one rule holds `*` on
// both its resources and its verbs. Wildcards spread over two rules — every
// resource read-only, every verb on pods — grant nothing close, and are only
// colored.
func TestRuleSummaryRanksBadOnlyWhenOneRuleIsWildOnResourcesAndVerbs(t *testing.T) {
	for _, c := range []struct {
		name      string
		rules     []rbacv1.PolicyRule
		wantText  string
		wantClass string
		wantRank  int
	}{
		{
			name: "wildcards split over two rules",
			rules: []rbacv1.PolicyRule{
				{APIGroups: []string{""}, Resources: []string{"*"}, Verbs: []string{"get"}},
				{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"*"}},
			},
			wantText:  "resources, verbs",
			wantClass: ClassWarn,
			wantRank:  0,
		},
		{
			name: "one rule wild on both",
			rules: []rbacv1.PolicyRule{
				{APIGroups: []string{""}, Resources: []string{"*"}, Verbs: []string{"*"}},
			},
			wantText:  "resources, verbs",
			wantClass: ClassBad,
			wantRank:  RankBad,
		},
		{
			name: "no wildcard",
			rules: []rbacv1.PolicyRule{
				{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list"}},
			},
			wantText:  Missing,
			wantClass: ClassNone,
			wantRank:  0,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, got := ruleSummary(c.rules)
			if got.Text != c.wantText || got.Class != c.wantClass || got.Rank != c.wantRank {
				t.Errorf("ruleSummary = {Text: %q, Class: %q, Rank: %d}, want {Text: %q, Class: %q, Rank: %d}",
					got.Text, got.Class, got.Rank, c.wantText, c.wantClass, c.wantRank)
			}
		})
	}
}
