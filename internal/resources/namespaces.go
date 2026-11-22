package resources

import (
	"context"
	"sort"
)

// NamespaceNames is every namespace on a cluster, sorted, for a dropdown
// (without rendering the namespaces table). Reviewed like every list: a person
// who may not `list namespaces` is refused, not shown a narrowed dropdown.
func NamespaceNames(ctx context.Context, r Reader) ([]string, error) {
	list, _, err := fetchNamespaces(ctx, r, "")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list))
	for _, ns := range list {
		names = append(names, ns.Name)
	}
	sort.Strings(names)
	return names, nil
}
