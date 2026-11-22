package rightsizing

import (
	"fmt"
	"strings"
)

// Status is how a single resource bar should read: the same four words a
// Suggestion uses, plus one for "nothing measured this".
//
// StatusNone is not a shade of healthy: what nothing measured is not zero.
type Status string

const (
	StatusDanger   Status = "danger"
	StatusWarning  Status = "warning"
	StatusOverkill Status = "overkill"
	StatusHealthy  Status = "healthy"
	StatusNone     Status = "none"
)

// ResourceStatus colors one container's CPU or memory bar with the same
// functions the findings use (targets.go), so the bar and its row cannot
// disagree: the P95 against the limit (CPU never past a warning, it is a
// throttle), the P95 against the request both ways, and the request's own
// target for over-provisioned.
//
// It does not see refusals: a container whose reduction was withheld keeps the
// color its numbers have, and the card says why there is no row.
func ResourceStatus(r ResourceReading, isCPU bool) Status {
	if !r.Measured {
		return StatusNone
	}
	st := usage{hasHistory: r.Windowed, p95: float64(r.P95), max: float64(r.Peak), mean: float64(r.Mean)}
	if kind, ok := limitPressure(st.p95, r.Limit, isCPU); ok {
		return Status(kind)
	}
	if kind, ok := requestPressure(st.p95, r.Request, isCPU); ok {
		return Status(kind)
	}
	// A measured zero is not an absence: using nothing of a request is
	// over-provisioning, even with no number to suggest.
	if r.Request > 0 && (st.p95 == 0 || requestOversized(st, r.Request, isCPU)) {
		return StatusOverkill
	}
	if r.Mean == 0 && r.Peak == 0 {
		// Measured at zero with nothing to judge it against: none, not
		// healthy.
		return StatusNone
	}
	return StatusHealthy
}

// StorageStatus colors a volume bar.
//
// A volume with no limit is a warning once it holds a gigabyte, the findings'
// line (ephemeralWorthALimit): with no ceiling it can fill the node's disk and
// evict every pod on it. Below that it is healthy.
func StorageStatus(usage, capacity int64, hasUsage, hasLimit bool) Status {
	if !hasUsage || usage == 0 {
		return StatusNone
	}
	if !hasLimit {
		if usage >= ephemeralWorthALimit {
			return StatusWarning
		}
		return StatusHealthy
	}
	if capacity == 0 {
		return StatusNone
	}
	pct := float64(usage) / float64(capacity)
	switch {
	case pct >= 0.90:
		return StatusDanger
	case pct >= 0.75:
		return StatusWarning
	default:
		return StatusHealthy
	}
}

// kubectlTarget maps a workload kind onto what `kubectl set resources` can
// address, and refuses the ones it cannot.
//
// A CronJob is addressed: `set resources` reaches spec.jobTemplate's pod
// template. A Job is refused, its pod template being immutable, and a bare Pod
// has no template.
func kubectlTarget(kind, name string) (string, bool) {
	switch kind {
	case "Deployment":
		return "deployment/" + name, true
	case "StatefulSet":
		return "statefulset/" + name, true
	case "DaemonSet":
		return "daemonset/" + name, true
	case "CronJob":
		return "cronjob/" + name, true
	case "ReplicaSet":
		return "replicaset/" + name, true
	default:
		return "", false
	}
}

// ToKubectlCmd renders a suggestion as the command that would apply it, or
// reports that it cannot be expressed as one.
//
// There is no command for "Migrate to larger node", a PVC or an emptyDir, or a
// Job's or bare Pod's template (see kubectlTarget).
//
// It only renders text: the command restarts every pod of the workload, so it
// is the operator's to run, never the console's.
func ToKubectlCmd(s Suggestion) (string, bool) {
	if s.Action == "Migrate to larger node" {
		return "", false
	}
	// No value, no command: a suggestion withheld for insufficient data names
	// none, and `cpu=0m` is a patch, not advice.
	if s.SuggestedRaw <= 0 {
		return "", false
	}

	var resource string
	switch {
	case strings.HasPrefix(s.Resource, "CPU"):
		resource = "cpu"
	case strings.HasPrefix(s.Resource, "Memory"):
		resource = "memory"
	case strings.HasPrefix(s.Resource, "Ephemeral"):
		resource = "ephemeral-storage"
	default:
		return "", false
	}

	target, ok := kubectlTarget(s.WorkloadKind, s.Workload)
	if !ok {
		return "", false
	}

	isCPU := resource == "cpu"
	value := resource + "=" + fmtKubectl(s.SuggestedRaw, isCPU)

	// A Guaranteed pod must keep request equal to limit, so both flags are
	// written together — the same pairing the suggestion itself carries.
	flags := "--limits=" + value
	switch {
	case s.AppliesToBoth:
		flags = "--requests=" + value + " --limits=" + value
	case s.PairedRequestRaw > 0:
		// A limit that carries a request moved in the same command: a
		// reduction folded into a new or reduced limit set under the current
		// request (foldRequest), or an increase riding on an OOM row.
		flags = "--requests=" + resource + "=" + fmtKubectl(s.PairedRequestRaw, isCPU) + " --limits=" + value
	case s.PairedLimitRaw > 0:
		// A request raised past the limit carries the limit in the same
		// command, or the API server refuses the request.
		flags = "--requests=" + value + " --limits=" + resource + "=" + fmtKubectl(s.PairedLimitRaw, isCPU)
	case strings.Contains(strings.ToLower(s.Action), "request"):
		flags = "--requests=" + value
	}

	return fmt.Sprintf("kubectl set resources %s -c %s %s -n %s", target, s.Container, flags, s.Namespace), true
}
