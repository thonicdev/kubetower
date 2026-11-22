package overview

import (
	"context"
	"fmt"
	"time"
)

// The kubelet's volume counters, which describe a claim's filesystem as the node
// that mounts it sees it. Only a claim a running pod mounts has them.
const (
	metricVolumeUsed     = "kubelet_volume_stats_used_bytes"
	metricVolumeCapacity = "kubelet_volume_stats_capacity_bytes"
)

// VolumeHistory is one claim's used and capacity bytes over a window.
//
// Unlike PodHistory it keeps no reasons: the claim's panel draws nothing at all
// where there is nothing to draw, so a missing Prometheus, a counter this cluster
// does not scrape, an empty window and a failed read all come back as lines with
// no points. Err is the first failure, for a caller that wants to log it; a line
// that has points is never discarded because the other failed.
type VolumeHistory struct {
	Used     History
	Capacity History
	Window   Window
	Err      error
}

// Drawable reports whether any line has a sample to draw.
func (v VolumeHistory) Drawable() bool { return len(v.Used.Points) > 0 || len(v.Capacity.Points) > 0 }

// volumeSelector names one claim. A claim's namespace is its own, and the same
// name in two namespaces is two volumes.
func volumeSelector(namespace, claim string) string {
	return fmt.Sprintf(`namespace=%s,persistentvolumeclaim=%s`, promQuote(namespace), promQuote(claim))
}

// volumeQuery is one counter for one claim. dedup first, as every query here,
// so a kubelet scraped twice does not read double; then max, because a claim
// that moved between nodes leaves the old node's series behind until it ages out
// and the newest figure is the larger one only by accident, while the capacity
// is the same on both.
func volumeQuery(metric, namespace, claim string) string {
	return fmt.Sprintf(`max(%s)`, dedup(fmt.Sprintf(`%s{%s}`, metric, volumeSelector(namespace, claim))))
}

// ReadVolumeHistory fetches the claim's two lines. A nil metrics is a cluster
// with no Prometheus: nothing is asked and the lines are empty.
func ReadVolumeHistory(ctx context.Context, metrics Metrics, cluster, namespace, claim string, window Window) VolumeHistory {
	out := VolumeHistory{
		Window:   window,
		Used:     History{Title: "Used", Unit: UnitBytes, Step: window.Step},
		Capacity: History{Title: "Capacity", Unit: UnitBytes, Step: window.Step},
	}
	if metrics == nil {
		return out
	}
	end := time.Now()
	start := end.Add(-window.Range)

	var usedErr, capacityErr error
	gather(
		guard(&usedErr, "the volume used query", func() {
			out.Used.Points, usedErr = rangeQueryStep(ctx, metrics, cluster, volumeQuery(metricVolumeUsed, namespace, claim), start, end, window.Step)
		}),
		guard(&capacityErr, "the volume capacity query", func() {
			out.Capacity.Points, capacityErr = rangeQueryStep(ctx, metrics, cluster, volumeQuery(metricVolumeCapacity, namespace, claim), start, end, window.Step)
		}),
	)
	if usedErr != nil {
		out.Used.Points, out.Err = nil, usedErr
	}
	if capacityErr != nil {
		out.Capacity.Points = nil
		if out.Err == nil {
			out.Err = capacityErr
		}
	}
	return out
}
