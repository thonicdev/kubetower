package extension

import "time"

// ClaimRef names one PersistentVolumeClaim.
type ClaimRef struct {
	Namespace string
	Name      string
}

// ClaimFill is one claim's filesystem, as its kubelet last reported it to the
// cluster's metrics backend (kubelet_volume_stats_used_bytes and
// kubelet_volume_stats_capacity_bytes).
type ClaimFill struct {
	ClaimRef
	UsedBytes     int64
	CapacityBytes int64
}

// The engines that answer the Prometheus API (#131).
const (
	EnginePrometheus      = "prometheus"
	EngineVictoriaMetrics = "victoriametrics"
)

// VolumeFill is each claim's fill level from the cluster's Prometheus (#91).
type VolumeFill struct {
	// State is no-prometheus, unreadable, empty or read, never an error: what
	// the backend answered is a value a page switches on.
	State WindowState
	// Detail is a short reason for every state, and for a read what was asked.
	Detail string
	// Engine is what answered, EnginePrometheus or EngineVictoriaMetrics, when
	// it is known; empty otherwise.
	Engine string
	// Claims are the claims asked for that the backend had both figures of,
	// sorted by namespace then name. A claim it holds no series for is absent,
	// never a zero.
	Claims []ClaimFill
	// Complete is false when the person may not list claims in every
	// namespace asked, and Note then says so: those claims were not asked.
	Complete bool
	Note     string
	ReadAt   time.Time
}
