package launch

import (
	"context"
	"errors"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/metrics"
)

// noClusters resolves nothing, so a discovery answers at once without a
// cluster.
type noClusters struct{}

func (noClusters) Resolve(string) (string, bool)          { return "", false }
func (noClusters) Client(string) (*cluster.Client, error) { return nil, errors.New("none") }

func TestTheServerProfileShowsItsSourceAndCannotSetOne(t *testing.T) {
	configured, _ := metrics.ParseTarget("monitoring/kps-prometheus:9090")
	m := metricsSources{
		measure:    metrics.New(metrics.Options{Clusters: noClusters{}, Configured: true, Prometheus: configured, Logf: func(string, ...any) {}}),
		configured: configured,
	}
	got := m.MetricsSource(context.Background(), "prod")
	if got.Editable || got.Source != "monitoring/kps-prometheus:9090" {
		t.Fatalf("%+v", got)
	}
	if _, err := m.SaveMetricsSource(context.Background(), "prod", "vm/vmsingle-main:8428"); !errors.Is(err, api.ErrNotInProfile) {
		t.Fatalf("err %v, want ErrNotInProfile", err)
	}
}

func TestTheDesktopSetsAndClearsASource(t *testing.T) {
	store := metrics.NewSourceStore(t.TempDir(), func(string, ...any) {})
	m := metricsSources{
		measure:  metrics.New(metrics.Options{Clusters: noClusters{}, Sources: sourceLookup(store), Logf: func(string, ...any) {}}),
		store:    store,
		editable: true,
	}
	ctx := context.Background()
	if got := m.MetricsSource(ctx, "prod"); got.Source != "" || !got.Editable {
		t.Fatalf("fresh: %+v", got)
	}
	got, err := m.SaveMetricsSource(ctx, "prod", "vm/vmselect-main:8481/select/0/prometheus")
	if err != nil || got.Source != "vm/vmselect-main:8481/select/0/prometheus" || got.Persisted == nil || !*got.Persisted {
		t.Fatalf("set: %+v, %v", got, err)
	}
	if _, err := m.SaveMetricsSource(ctx, "prod", "vm/vmselect-main"); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("a value that is not a target: %v", err)
	}
	if got := m.MetricsSource(ctx, "prod"); got.Source != "vm/vmselect-main:8481/select/0/prometheus" {
		t.Fatalf("a refused value moved the source: %+v", got)
	}
	if got, err := m.SaveMetricsSource(ctx, "prod", ""); err != nil || got.Source != "" {
		t.Fatalf("clear: %+v, %v", got, err)
	}
}

// A store whose writes are not registered (a state directory not kept) is
// not written through either.
func TestANotEditableDesktopCannotSetASource(t *testing.T) {
	store := metrics.NewSourceStore("", func(string, ...any) {})
	m := metricsSources{measure: metrics.New(metrics.Options{Clusters: noClusters{}, Logf: func(string, ...any) {}}), store: store}
	if _, err := m.SaveMetricsSource(context.Background(), "prod", "vm/vmsingle-main:8428"); !errors.Is(err, api.ErrNotInProfile) {
		t.Fatalf("err %v", err)
	}
	if store.Source("prod") != nil {
		t.Fatal("the store was written")
	}
}
