package web

import (
	"strings"
)

// isNodeKind reports whether this resource is a machine, charted from node-level
// series rather than by summing containers, which miss everything running
// outside one.
func isNodeKind(kind string) bool {
	return strings.EqualFold(kind, "nodes") || strings.EqualFold(kind, "node")
}

// volumeChart is the one icon-row key of a claim's panel, and the chart its two
// lines (used, capacity) share.
const volumeChart = "volume"

// isVolumeClaimKind reports whether this resource is a PersistentVolumeClaim,
// charted from the kubelet's volume counters.
func isVolumeClaimKind(kind string) bool {
	return strings.EqualFold(kind, "persistentvolumeclaims") || strings.EqualFold(kind, "persistentvolumeclaim")
}
