package api

import (
	"context"
	"net/http"
)

// MetricsSource is where a cluster's charts read their history from: the
// source set by hand, if any, and what discovery answered with it.
type MetricsSource struct {
	// Source is what is set, written `namespace/service:port[/path]`: on the
	// desktop the one chosen in Settings, under the server profile
	// KT_PROMETHEUS_SERVICE. Empty where discovery decides, or where the
	// server profile has none.
	Source string `json:"source"`
	// Editable says this console takes a source per cluster: the desktop, with
	// a state directory that is kept. False under the server profile, where the
	// deployment chooses, and the PUT is then not registered.
	Editable bool `json:"editable"`
	// Kind is the discovery's: prometheus, metrics-server, none, unreachable
	// or unauthorised.
	Kind string `json:"kind"`
	// Engine is what answers the Prometheus API, prometheus or
	// victoriametrics; set only where Kind is prometheus.
	Engine string `json:"engine,omitempty"`
	// Address is the Service queried, set only where Kind is prometheus.
	Address string `json:"address,omitempty"`
	// Transport is how it is reached, proxy or port-forward.
	Transport string `json:"transport,omitempty"`
	// Detail is one line saying what was found and how, or what failed.
	Detail string `json:"detail"`
	// Persisted and Note are a write's: a valid source is in effect whether or
	// not it could be written down. A pointer, so a read says nothing about a
	// write that did not happen.
	Persisted *bool  `json:"persisted,omitempty"`
	Note      string `json:"note,omitempty"`
}

// MetricsSourceWrite sets a cluster's source, or clears it with an empty one.
type MetricsSourceWrite struct {
	Source string `json:"source"`
}

// MetricsSources reads and sets the metrics source of one cluster.
type MetricsSources interface {
	// MetricsSource answers from the discovery's cache when it has one, and
	// probes the cluster otherwise.
	MetricsSource(ctx context.Context, cluster string) MetricsSource
	// SaveMetricsSource sets the source, or clears it, then probes the cluster
	// again. ErrInvalid for a value that is not a target, nothing changed.
	SaveMetricsSource(ctx context.Context, cluster, source string) (MetricsSource, error)
}

func (a *API) getMetricsSource(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	writeOK(w, a.deps.MetricsSources.MetricsSource(r.Context(), name))
}

func (a *API) saveMetricsSource(w http.ResponseWriter, r *http.Request) {
	name, ok := a.cluster(w, r)
	if !ok {
		return
	}
	var body MetricsSourceWrite
	if !decode(w, r, maxBody, &body) {
		return
	}
	out, err := a.deps.MetricsSources.SaveMetricsSource(r.Context(), name, body.Source)
	if err != nil {
		fail(w, err)
		return
	}
	consoleWrite(r, "the metrics source of %s was saved", name)
	writeOK(w, out)
}
