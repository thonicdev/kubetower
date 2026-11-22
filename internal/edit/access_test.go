package edit

import (
	"context"
	"strings"
	"testing"
)

// A Location assembled by hand cannot reach a cluster.
//
// access.go exports live() as Live() so `internal/actions` can build delete,
// evict, cordon and drain on the same guardrail as a manifest save — a
// guardrail with two implementations is a guardrail with two chances to stop
// matching, and the read-only check must run on the *resolved* context name.
//
// But every field of Location is exported, because a Resolver outside this
// package fills them in; so any package could assemble a Location around a
// dynamic client and write to a cluster with the guardrail never having run.
// Nothing does. The point is that nothing *can*, and that this stays true after
// the next person moves the code.
func TestALocationAssembledByHandCannotReachACluster(t *testing.T) {
	h := newHarness(t, func(string) bool { return false })

	fabricated := Location{
		Cluster:    h.cluster,
		Resource:   configMapGVR,
		Kind:       "ConfigMap",
		Namespaced: true,
		Dynamic:    h.dyn,
		// guarded is deliberately absent. It cannot be set from outside this
		// package at all, which is the whole mechanism.
	}

	_, err := live(context.Background(), fabricated,
		Target{Cluster: h.cluster, Kind: "ConfigMap", Namespace: "default", Name: "coredns"})

	if err == nil {
		t.Fatal("a Location that never went through Locate reached a cluster. The read-only " +
			"guardrail lives in locate(); a caller that skips it writes to a cluster the " +
			"operator marked read-only, and nothing anywhere reports that it happened")
	}
	if refusal, ok := AsRefusal(err); !ok {
		t.Errorf("the refusal is not one of this package's, so the panel and the "+
			"X-KubeTower-Refusal header will disagree about what happened: %v", err)
	} else if !strings.Contains(refusal.Reason, "Locate") {
		t.Errorf("the refusal does not name the call that was skipped, which is the one thing "+
			"the person reading it needs: %q", refusal.Reason)
	}
}

// And the ordinary path still works, so the check above is a guard and not a
// wall. A Location from locate() carries the stamp.
func TestALocationFromLocateIsAccepted(t *testing.T) {
	h := newHarness(t, func(string) bool { return false })

	where, err := h.writer.locate(t.Context(), Target{Cluster: "docker-desktop", Kind: "ConfigMap", Name: "coredns"})
	if err != nil {
		t.Fatalf("locate refused an ordinary target: %v", err)
	}
	if !where.guarded {
		t.Fatal("locate returned a Location it had not stamped, so every write through this " +
			"package is about to be refused as though it had been assembled by hand")
	}
}
