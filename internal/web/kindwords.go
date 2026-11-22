package web

import (
	"context"
	"strings"
)

// kindWords is how a sentence names a kind, as discovery spells it: `ConfigMap`
// for one, `ConfigMaps` for several, `Endpoints` for both. `api.KindWord` only
// has the lower-case slug to go on; the detail reader's mapper knows the Kind
// and the plural without a request.
type kindWords struct{ one, many string }

// kindWords resolves the slug through the detail reader's mapper; false where it
// does not, and the caller falls back to the slug's rule.
func (s *Server) kindWords(ctx context.Context, cluster, slug string) (kindWords, bool) {
	if s == nil || s.objects == nil {
		return kindWords{}, false
	}
	where, err := (editResolver{objects: s.objects}).Locate(ctx, cluster, slug)
	if err != nil || where.Kind == "" {
		return kindWords{}, false
	}
	return wordsFor(where.Kind, where.Resource.Resource), true
}

// wordsFor spells the plural with the Kind's capitals (`networkpolicies` under
// `NetworkPolicy` reads `NetworkPolicies`). A plural that does not start with the
// Kind's letters, as a custom resource may have, is used as discovery has it.
func wordsFor(kind, plural string) kindWords {
	if plural == "" {
		return kindWords{one: kind, many: kind}
	}
	shared := 0
	for shared < len(kind) && shared < len(plural) && strings.EqualFold(kind[shared:shared+1], plural[shared:shared+1]) {
		shared++
	}
	// All of the Kind but its last letter, which `y` to `ies` changes.
	if shared < len(kind)-1 {
		return kindWords{one: kind, many: plural}
	}
	return kindWords{one: kind, many: kind[:shared] + plural[shared:]}
}
