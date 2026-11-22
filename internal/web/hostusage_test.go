package web

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/kubeletstats"
)

// kubeletsNamingTwoNamespaces is two kubelets' answers naming pods in "shop"
// and "billing" - whoever's they are, as a kubelet names them.
func kubeletsNamingTwoNamespaces() map[string]kubeletstats.Summary {
	return map[string]kubeletstats.Summary{
		"node-1": {Pods: []kubeletstats.Pod{{Name: "web", Namespace: "shop"}, {Name: "ledger", Namespace: "billing"}}},
		"node-2": {Pods: []kubeletstats.Pod{{Name: "cart", Namespace: "shop"}}},
	}
}

// usageAs answers usageFor for frank, signed in under the server profile, with
// the gate deciding through decide.
func usageAs(t *testing.T, decide func(accesstest.Question) (bool, error)) extension.Usage {
	t.Helper()
	gate := access.New(access.Options{Reviews: (&accesstest.Reviews{Answer: decide}).For, InCluster: true})
	ctx := identity.With(context.Background(), frank)
	got, err := usageFor(ctx, gate, "prod", kubeletsNamingTwoNamespaces(), full(2, 2))
	if err != nil {
		t.Fatalf("usageFor: %v", err)
	}
	return got
}

func podNames(u extension.Usage) []string {
	var names []string
	for _, node := range u.Nodes {
		for _, p := range node.Pods {
			names = append(names, p.Namespace+"/"+p.Name)
		}
	}
	sort.Strings(names)
	return names
}

// A node's pods narrowed to the person's
// namespaces must not come back looking like every pod on the node. The three
// answers a person can get are told apart on the Usage itself.
func TestHostUsageSaysWhetherANodesPodsWereNarrowedForThePerson(t *testing.T) {
	t.Run("complete", func(t *testing.T) {
		got := usageAs(t, func(accesstest.Question) (bool, error) { return true, nil })
		if !got.Complete || got.Note != "" {
			t.Errorf("Complete=%v Note=%q; a person who may list pods everywhere was told the answer is partial",
				got.Complete, got.Note)
		}
		if names := podNames(got); len(names) != 3 {
			t.Errorf("pods %v; want all three the kubelets named", names)
		}
	})

	t.Run("narrowed", func(t *testing.T) {
		got := usageAs(t, func(q accesstest.Question) (bool, error) { return q.Attributes.Namespace == "shop", nil })
		if got.Complete {
			t.Error("Complete is true on pods narrowed to the namespaces the person may list")
		}
		if !strings.Contains(got.Note, "namespaces where you may") {
			t.Errorf("Note %q does not say the pods are those of the person's namespaces", got.Note)
		}
		if strings.Contains(got.Note, "could not be made") {
			t.Errorf("Note %q blames a review that was answered", got.Note)
		}
		if names := podNames(got); strings.Join(names, ",") != "shop/cart,shop/web" {
			t.Errorf("pods %v; want only shop's", names)
		}
	})

	t.Run("unreviewable", func(t *testing.T) {
		// The whole list is denied and no namespace's review can be made: the
		// pods are narrowed to nothing, which must not read as idle nodes.
		got := usageAs(t, func(q accesstest.Question) (bool, error) {
			if q.Attributes.Namespace == "" {
				return false, nil
			}
			return false, errors.New("the authorizer did not answer")
		})
		if got.Complete {
			t.Error("Complete is true on pods withheld because their review could not be made")
		}
		if !strings.Contains(got.Note, "could not be made") {
			t.Errorf("Note %q does not say a review could not be made", got.Note)
		}
		if names := podNames(got); len(names) != 0 {
			t.Errorf("pods %v shown on a review nobody answered", names)
		}
		if len(got.Nodes) != 2 || got.Asked != 2 || got.Answered != 2 {
			t.Errorf("nodes %d, %d of %d answered; narrowing the pods must not drop the nodes",
				len(got.Nodes), got.Answered, got.Asked)
		}
	})
}
