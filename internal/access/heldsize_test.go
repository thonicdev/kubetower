package access

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/identity"
)

// A person with hundreds of groups costs the held answers what anybody else
// costs. Every held answer carries its key; with the group list in it whole,
// the bound of MaxHeld answers for one such person was over a hundred
// megabytes. The key is a digest now, so filling the map to its bound for a
// person with 300 groups stays within a few megabytes.
func TestThreeHundredGroupsDoNotMultiplyTheHeldAnswers(t *testing.T) {
	groups := make([]string, 300)
	for i := range groups {
		groups[i] = fmt.Sprintf("oidc:engineering-platform-team-%03d-%s", i, strings.Repeat("x", 16))
	}
	ctx := identity.With(context.Background(), identity.Who{Name: "big@example.com", Method: identity.SSO, Groups: groups})
	c := &counting{}
	g := New(Options{Reviews: func(string) (Reviews, error) { return c, nil }, InCluster: true})

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < MaxHeld; i++ {
		g.Review(ctx, "prod", Attributes{Verb: "get", Resource: "secrets", Namespace: "shop", Name: fmt.Sprintf("name-%05d", i)})
	}
	runtime.GC()
	runtime.ReadMemStats(&after)

	if n := len(g.held); n != MaxHeld {
		t.Fatalf("%d answers held, want the map filled to its bound of %d", n, MaxHeld)
	}
	const most = 8 << 20
	if grew := int64(after.HeapAlloc) - int64(before.HeapAlloc); grew > most {
		t.Errorf("%d answers for one person with %d groups hold %.1f MiB, more than %d MiB: the key "+
			"carries the group list", MaxHeld, len(groups), float64(grew)/(1<<20), most>>20)
	}
	runtime.KeepAlive(g)
}
