package web

import (
	"context"

	"github.com/thonicdev/kubetower/internal/detail"
	"github.com/thonicdev/kubetower/internal/edit"
)

// writable reports whether this cluster may be changed, and says why not. The
// sentence names the setting, so a person who meets the refusal can find its
// cause without reading the source. `internal/edit` checks again: this only
// decides whether a write is offered.
func (s *Server) writable(name string) (bool, string) {
	resolved, ok := s.clusters.Resolve(name)
	if !ok {
		return false, "unknown cluster"
	}
	if s.readOnly != nil && s.readOnly(resolved) {
		return false, resolved + " is marked read-only in KT_READONLY_CLUSTERS; changes are refused there"
	}
	return true, ""
}

func (e editResolver) Locate(ctx context.Context, cluster, kind string) (edit.Location, error) {
	where, err := e.objects.Locate(ctx, cluster, kind)
	if err != nil {
		return edit.Location{}, err
	}
	return edit.Location{
		Cluster:    where.Cluster,
		Resource:   where.Resource,
		Kind:       where.GroupVersionKind.Kind,
		Namespaced: where.Namespaced,
		Dynamic:    where.Dynamic,
	}, nil
}

// editResolver adapts the detail reader to what `internal/edit` declares, so the
// write path uses the reader's REST mapper per cluster instead of building a
// second one that could disagree.
type editResolver struct{ objects *detail.Reader }
