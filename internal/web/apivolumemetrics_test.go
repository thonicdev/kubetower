package web

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
)

// A claim's panel draws a storage chart where the kubelet's volume counters
// answer, and is `absent` - drawn as nothing by the client - in every other
// case: no Prometheus, no series, a failed read.

type failingProm struct{ promFake }

func (*failingProm) QueryRange(context.Context, string, string, time.Time, time.Time, time.Duration) ([]byte, error) {
	return nil, errors.New("prometheus is down")
}

func claimQuery() api.MetricsQuery {
	return api.MetricsQuery{
		ObjectRef: api.ObjectRef{Cluster: "prod", Kind: "persistentvolumeclaims", Namespace: "shop", Name: "data"},
		Range:     "6h",
	}
}

func TestAClaimsPanelDrawsUsedAndCapacity(t *testing.T) {
	prom := &promFake{}
	reads := apiReads{&Server{series: prom, gate: accesstest.Allow()}}
	panel, fail := reads.Metrics(context.Background(), claimQuery())
	if fail != nil {
		t.Fatalf("the panel failed: %+v", fail)
	}
	got := seriesByKey(panel)
	for _, key := range []string{"volume_used", "volume_capacity"} {
		one, ok := got[key]
		if !ok || one.Chart != "volume" || one.Unit != "bytes" || len(one.Data.Values) == 0 {
			t.Errorf("series %q = %+v (keys %v)", key, one, seriesKeysOf(panel))
		}
	}
	if len(panel.Metrics) != 1 || panel.Metrics[0].Key != "volume" || !panel.Metrics[0].Available {
		t.Errorf("icon row = %+v", panel.Metrics)
	}
	asked := strings.Join(prom.ranges, "\n")
	for _, want := range []string{"kubelet_volume_stats_used_bytes", "kubelet_volume_stats_capacity_bytes", `namespace="shop"`, `persistentvolumeclaim="data"`} {
		if !strings.Contains(asked, want) {
			t.Errorf("no query asked for %q: %s", want, asked)
		}
	}
	for _, w := range panel.Windows {
		if w.Key == "6h" && !w.Selected {
			t.Errorf("the asked span is not selected: %+v", panel.Windows)
		}
	}
}

func TestAClaimsPanelIsAbsentWhereThereIsNothingToDraw(t *testing.T) {
	for name, reads := range map[string]apiReads{
		"no prometheus": {&Server{gate: accesstest.Allow()}},
		"no series":     {&Server{series: &promFake{emptyFor: "kubelet_volume_stats"}, gate: accesstest.Allow()}},
		"failed read":   {&Server{series: &failingProm{}, gate: accesstest.Allow()}},
	} {
		t.Run(name, func(t *testing.T) {
			panel, fail := reads.Metrics(context.Background(), claimQuery())
			if fail == nil {
				t.Fatalf("want a refusal, got panel %+v", panel)
			}
			if fail.State != api.StateAbsent {
				t.Errorf("want absent, got %+v", fail)
			}
			if len(panel.Series) != 0 {
				t.Errorf("series were sent: %+v", panel.Series)
			}
		})
	}
}

func TestAClaimsPanelIsReviewedAsGetOnTheClaim(t *testing.T) {
	reviews := &accesstest.Reviews{Answer: func(accesstest.Question) (bool, error) { return false, nil }}
	prom := &promFake{}
	reads := apiReads{&Server{series: prom, gate: reviews.Gate()}}
	_, fail := reads.Metrics(context.Background(), claimQuery())
	if fail == nil {
		t.Fatal("a refused review answered a panel")
	}
	// The refusal names the person's own question, so the tab draws a
	// refusal rather than no metrics (#156).
	if fail.State != api.StateForbidden || fail.Reason.Scope == nil ||
		fail.Reason.Scope.Verb != "get" || fail.Reason.Scope.Resource != "persistentvolumeclaims" {
		t.Errorf("refusal %+v scope %+v", fail, fail.Reason.Scope)
	}
	if len(prom.ranges) != 0 {
		t.Errorf("Prometheus was asked %d times for a person who may not read the claim", len(prom.ranges))
	}
	asked := reviews.Asked()
	if len(asked) == 0 {
		t.Fatal("no review was made")
	}
	a := asked[len(asked)-1].Attributes
	if a.Verb != "get" || a.Resource != "persistentvolumeclaims" || a.Namespace != "shop" || a.Name != "data" {
		t.Errorf("reviewed %+v", a)
	}
}

func TestMetricsAddressIncludesClaims(t *testing.T) {
	if got := metricsAddressFor(api.ObjectRef{Cluster: "prod", Kind: "persistentvolumeclaims", Namespace: "shop", Name: "data"}); got == "" {
		t.Error("a claim has no metrics address")
	}
	if got := metricsAddressFor(api.ObjectRef{Cluster: "prod", Kind: "configmaps", Namespace: "shop", Name: "x"}); got != "" {
		t.Errorf("a configmap has one: %s", got)
	}
}
