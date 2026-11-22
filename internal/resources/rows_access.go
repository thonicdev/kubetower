package resources

import (
	"slices"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
)

// --- access control ---------------------------------------------------------------

func serviceAccountRow(sa *corev1.ServiceAccount, now time.Time) []Cell {
	return []Cell{
		text(sa.Name),
		text(sa.Namespace),
		count(len(sa.Secrets)),
		text(JoinCapped(sortedNames(sa.Secrets), 3)),
		age(sa.CreationTimestamp, now),
	}
}

func clusterRoleRow(r *rbacv1.ClusterRole, now time.Time) []Cell {
	resources, wildcards := ruleSummary(r.Rules)
	return []Cell{
		text(r.Name),
		count(len(r.Rules)),
		text(resources),
		wildcards,
		age(r.CreationTimestamp, now),
	}
}

func roleRow(r *rbacv1.Role, now time.Time) []Cell {
	resources, wildcards := ruleSummary(r.Rules)
	return []Cell{
		text(r.Name),
		text(r.Namespace),
		count(len(r.Rules)),
		text(resources),
		wildcards,
		age(r.CreationTimestamp, now),
	}
}

// ruleSummary is what a role list is read for: what it touches, and whether
// any of it is a wildcard. `*` on both verbs and resources in one rule is
// cluster-admin under another name and is ranked; any other wildcard is only
// colored, as stock clusters ship dozens.
func ruleSummary(rules []rbacv1.PolicyRule) (string, Cell) {
	var names []string
	var anyGroup, anyResource, anyVerb, allOnAll, nonResource bool

	for _, rule := range rules {
		wildVerb := slices.Contains(rule.Verbs, rbacv1.VerbAll)
		wildResource := slices.Contains(rule.Resources, rbacv1.ResourceAll)
		anyGroup = anyGroup || slices.Contains(rule.APIGroups, rbacv1.APIGroupAll)
		anyVerb = anyVerb || wildVerb
		anyResource = anyResource || wildResource
		allOnAll = allOnAll || (wildVerb && wildResource)
		for _, resource := range rule.Resources {
			if !slices.Contains(names, resource) {
				names = append(names, resource)
			}
		}
		nonResource = nonResource || len(rule.NonResourceURLs) > 0
	}
	sort.Strings(names)
	if len(names) == 0 && nonResource {
		// A role granting only non-resource URLs still grants something.
		names = []string{"non-resource URLs"}
	}

	var flags []string
	if anyGroup {
		flags = append(flags, "api groups")
	}
	if anyResource {
		flags = append(flags, "resources")
	}
	if anyVerb {
		flags = append(flags, "verbs")
	}

	wildcards := Cell{Text: Missing}
	if len(flags) > 0 {
		wildcards = Cell{Text: strings.Join(flags, ", "), Class: ClassWarn}
		if allOnAll {
			wildcards.Class, wildcards.Rank = ClassBad, RankBad
		}
	}
	return JoinCapped(names, 4), wildcards
}

func clusterRoleBindingRow(b *rbacv1.ClusterRoleBinding, now time.Time) []Cell {
	return []Cell{
		text(b.Name),
		text(roleRef(b.RoleRef)),
		count(len(b.Subjects)),
		text(subjectsText(b.Subjects, 3)),
		age(b.CreationTimestamp, now),
	}
}

func roleBindingRow(b *rbacv1.RoleBinding, now time.Time) []Cell {
	return []Cell{
		text(b.Name),
		text(b.Namespace),
		text(roleRef(b.RoleRef)),
		count(len(b.Subjects)),
		text(subjectsText(b.Subjects, 3)),
		age(b.CreationTimestamp, now),
	}
}

// roleRef keeps the kind: a RoleBinding may point at a ClusterRole, and the
// name alone cannot tell them apart.
func roleRef(ref rbacv1.RoleRef) string {
	if ref.Kind == "" {
		return ref.Name
	}
	return ref.Kind + "/" + ref.Name
}

// subjectsText is who the binding grants to. Each subject keeps its kind (a
// Group and a User of one name are different grants), and a ServiceAccount its
// namespace.
func subjectsText(subjects []rbacv1.Subject, limit int) string {
	out := make([]string, 0, len(subjects))
	for _, s := range subjects {
		name := s.Name
		if s.Kind == rbacv1.ServiceAccountKind && s.Namespace != "" {
			name = s.Namespace + "/" + s.Name
		}
		out = append(out, subjectKind(s.Kind)+":"+name)
	}
	return JoinCapped(out, limit)
}

func subjectKind(kind string) string {
	switch kind {
	case rbacv1.ServiceAccountKind:
		return "sa"
	case rbacv1.UserKind:
		return "user"
	case rbacv1.GroupKind:
		return "group"
	}
	return strings.ToLower(kind)
}
