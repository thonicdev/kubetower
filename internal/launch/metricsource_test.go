package launch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

// A context name with a slash reaches the API in two spellings: `a/b`, and the
// DNS-friendly `a--b` that saved links carry. Both are one cluster, and the
// source is kept under the name discovery looks it up by.
func TestASourceIsKeptUnderTheResolvedClusterName(t *testing.T) {
	kubeconfig := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(kubeconfig, []byte(`apiVersion: v1
kind: Config
current-context: team/prod
clusters:
  - name: one
    cluster: { server: https://127.0.0.1:1 }
users:
  - name: one
    user: { token: not-a-token }
contexts:
  - name: team/prod
    context: { cluster: one, user: one }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	store := metrics.NewSourceStore(t.TempDir(), func(string, ...any) {})
	m := metricsSources{
		measure:  metrics.New(metrics.Options{Clusters: noClusters{}, Sources: sourceLookup(store), Logf: func(string, ...any) {}}),
		store:    store,
		editable: true,
		resolve:  clusters.Resolve,
	}
	ctx := context.Background()

	if _, err := m.SaveMetricsSource(ctx, "team--prod", "vm/vmsingle-main:8428"); err != nil {
		t.Fatal(err)
	}
	// Discovery asks the store under the resolved name.
	if got := store.Source("team/prod"); got == nil || got.Address() != "vm/vmsingle-main:8428" {
		t.Fatalf("under team/prod the store holds %+v", got)
	}
	if store.Source("team--prod") != nil {
		t.Error("the source was also kept under the DNS-friendly spelling")
	}
	for _, name := range []string{"team/prod", "team--prod"} {
		if got := m.MetricsSource(ctx, name); got.Source != "vm/vmsingle-main:8428" {
			t.Errorf("GET %s: source %q", name, got.Source)
		}
	}

	if _, err := m.SaveMetricsSource(ctx, "team/prod", ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"team/prod", "team--prod"} {
		if got := m.MetricsSource(ctx, name); got.Source != "" {
			t.Errorf("GET %s after clearing: source %q", name, got.Source)
		}
	}
}

// dnsClusters resolves the DNS-friendly form `a--b` to the context `a/b`.
type dnsClusters struct{}

func (dnsClusters) Resolve(name string) (string, bool) {
	if name == "a--b" {
		return "a/b", true
	}
	return name, true
}

// The console's wiring resolves names (#156): without it, a source saved
// through `a--b` is stored where discovery never looks (#135).
func TestTheWiredMetricsSourcesResolveNames(t *testing.T) {
	m := newMetricsSources(nil, nil, nil, false, dnsClusters{})
	if got := m.resolved("a--b"); got != "a/b" {
		t.Errorf("resolved %q", got)
	}
}
