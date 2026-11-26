package launch

import (
	"context"
	"errors"
	"fmt"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/metrics"
	"github.com/thonicdev/kubetower/internal/statefile"
	"github.com/thonicdev/kubetower/textutil"
)

// metricsSources is api.MetricsSources: Settings › General's metrics source
// per cluster.
//
// On the desktop the source is the store's, set per cluster, and empty leaves
// discovery to decide. Under the server profile it is KT_PROMETHEUS_SERVICE for
// every cluster, read only: store is nil there.
type metricsSources struct {
	measure *metrics.Metrics
	store   *metrics.SourceStore
	// configured is KT_PROMETHEUS_SERVICE, the server profile's source; nil on
	// the desktop and where it is unset.
	configured *metrics.PromTarget
	// editable says the PUT is registered: a store, in a state directory that
	// is kept by this one process (api.Deps' `kept`).
	editable bool
	// resolve is the cluster manager's Resolve: the API takes a context's
	// DNS-friendly form (`a--b` for `a/b`) as well as its name, and the store
	// is keyed as discovery looks it up, by the resolved name. Nil keeps the
	// name as given.
	resolve func(string) (string, bool)
}

// resolved is the name the store and discovery know cluster by.
func (m metricsSources) resolved(cluster string) string {
	if m.resolve != nil {
		if name, ok := m.resolve(cluster); ok {
			return name
		}
	}
	return cluster
}

// openMetricsSources is the desktop's store, or nil under the server profile.
func openMetricsSources(inCluster bool) *metrics.SourceStore {
	if inCluster {
		return nil
	}
	store := metrics.NewSourceStore(statefile.Dir(), logf)
	if store.Path() != "" {
		logf("metrics sources remembered in %s", store.Path())
	}
	return store
}

// sourceLookup is metrics.Options.Sources for the store, nil without one.
func sourceLookup(store *metrics.SourceStore) func(string) *metrics.PromTarget {
	if store == nil {
		return nil
	}
	return store.Source
}

func (m metricsSources) source(cluster string) string {
	if m.store != nil {
		if target := m.store.Source(cluster); target != nil {
			return target.Address()
		}
		return ""
	}
	if m.configured != nil {
		return m.configured.Address()
	}
	return ""
}

func (m metricsSources) answer(cluster string, found *metrics.Discovery) api.MetricsSource {
	out := api.MetricsSource{Source: m.source(cluster), Editable: m.editable}
	if found == nil {
		out.Kind, out.Detail = metrics.KindUnreachable, "the cluster has not been asked yet"
		return out
	}
	out.Kind, out.Detail = found.Kind, found.Detail
	if found.Kind == metrics.KindPrometheus && found.Prometheus != nil {
		out.Engine, out.Address, out.Transport = found.Engine, found.Prometheus.Address(), found.Transport
	}
	return out
}

func (m metricsSources) MetricsSource(ctx context.Context, cluster string) api.MetricsSource {
	cluster = m.resolved(cluster)
	return m.answer(cluster, m.measure.Discover(ctx, cluster, false))
}

func (m metricsSources) SaveMetricsSource(ctx context.Context, cluster, source string) (api.MetricsSource, error) {
	if m.store == nil || !m.editable {
		return api.MetricsSource{}, fmt.Errorf("%w: the metrics source is the deployment's (%s)",
			api.ErrNotInProfile, metrics.EnvPrometheusService)
	}
	cluster = m.resolved(cluster)
	_, err := m.store.Set(cluster, source)
	if err != nil && !errors.Is(err, statefile.ErrNotSaved) {
		return api.MetricsSource{}, fmt.Errorf("%w: %s", api.ErrInvalid, textutil.FirstLine(err.Error()))
	}
	out := m.answer(cluster, m.measure.Discover(ctx, cluster, true))
	saved := err == nil
	out.Persisted = &saved
	if err != nil {
		out.Note = "In effect now, and it will not survive a restart: " + textutil.FirstLine(err.Error())
	}
	return out, nil
}
