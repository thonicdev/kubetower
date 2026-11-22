package rightsizing

import (
	"fmt"
	"math"
	"strconv"
)

const mib = 1024 * 1024

// memorySteps is the ladder a memory suggestion is allowed to land on: powers
// of two plus the common thirds (192, 384, 768…), 64 MiB to 32 GiB.
//
// A suggestion then reads like something an operator would type ("1.5Gi", not
// "1487Mi"), at the cost of at most ~28 % headroom, on the safe side.
var memorySteps = func() []int64 {
	steps := []int64{
		64, 128, 192, 256, 384, 512, 768, 1024, 1536, 2048,
		3072, 4096, 6144, 8192, 12288, 16384, 24576, 32768,
	}
	out := make([]int64, len(steps))
	for i, s := range steps {
		out[i] = s * mib
	}
	return out
}()

// RoundResource rounds a computed value up to the nearest value worth typing:
// CPU to a multiple of 50m below one core and 250m above it, memory to the
// ladder above.
//
// Always up, never to nearest: a limit rounded down can cause an incident.
func RoundResource(raw float64, isCPU bool) int64 {
	if raw <= 0 {
		return 0
	}
	if isCPU {
		if raw <= 1000 {
			return int64(math.Ceil(raw/50)) * 50
		}
		return int64(math.Ceil(raw/250)) * 250
	}
	for _, step := range memorySteps {
		if float64(step) >= raw {
			return step
		}
	}
	// Past the ladder, keep climbing in blocks of its last rung rather than
	// inventing new rungs nobody has seen.
	last := memorySteps[len(memorySteps)-1]
	return int64(math.Ceil(raw/float64(last))) * last
}

// toFixed formats to a fixed number of decimals the way JavaScript's toFixed
// does (half away from zero, where strconv rounds half to even), so the page
// and this package agree.
func toFixed(v float64, prec int) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		// Unreachable through Compute (every denominator is positive), but
		// never print "+Inf" into a sentence.
		return "?"
	}
	shift := math.Pow(10, float64(prec))
	return strconv.FormatFloat(math.Round(v*shift)/shift, 'f', prec, 64)
}

// fmtRaw formats a value for reading. CPU becomes millicores below one core and
// cores above it; memory climbs KiB, MiB, GiB.
func fmtRaw(v float64, isCPU bool) string {
	if isCPU {
		if v >= 1000 {
			return toFixed(v/1000, 2) + " cores"
		}
		return fmt.Sprintf("%dm", int64(math.Round(v)))
	}
	if gib := v / (1024 * 1024 * 1024); gib >= 1 {
		return toFixed(gib, 2) + " GiB"
	}
	if m := v / mib; m >= 1 {
		return toFixed(m, 0) + " MiB"
	}
	return toFixed(v/1024, 0) + " KiB"
}

// fmtKubectl formats a value as a quantity kubectl accepts ("1536Mi", never
// "1.50 GiB").
//
// Memory that is not a whole MiB is written in bytes, exactly: rounding up
// could pass a node's allocatable, and rounding down could put a limit under a
// `1G` request, which the API server refuses.
func fmtKubectl(raw int64, isCPU bool) string {
	if isCPU {
		return fmt.Sprintf("%dm", raw)
	}
	if raw%mib != 0 {
		return fmt.Sprintf("%d", raw)
	}
	m := raw / mib
	if m%1024 == 0 {
		return fmt.Sprintf("%dGi", m/1024)
	}
	return fmt.Sprintf("%dMi", m)
}

// MaxNodeCapacity is the ceiling an increase may not exceed: the largest
// allocatable CPU and memory among the nodes a workload can be placed on, with
// the per-node values kept so a suggestion can say how many nodes would still
// take the pod.
//
// NotReady nodes are excluded, and so are cordoned and NoSchedule- or
// NoExecute-tainted ones while any other node is left, since an ordinary pod
// cannot land there. A cluster whose every node is tainted is measured on its
// Ready nodes.
func MaxNodeCapacity(nodes []Node) NodeCapacity {
	out := NodeCapacity{byName: map[string]Node{}}
	anySchedulable := false
	for _, n := range nodes {
		if n.Ready {
			out.byName[n.Name] = n
			anySchedulable = anySchedulable || n.Schedulable
		}
	}
	for _, n := range nodes {
		if !n.Ready || (anySchedulable && !n.Schedulable) {
			continue
		}
		out.ReadyCPU = append(out.ReadyCPU, n.AllocatableCPU)
		out.ReadyMemory = append(out.ReadyMemory, n.AllocatableMemory)
		if n.AllocatableCPU > out.MaxCPU {
			out.MaxCPU = n.AllocatableCPU
		}
		if n.AllocatableMemory > out.MaxMemory {
			out.MaxMemory = n.AllocatableMemory
		}
	}
	return out
}

// forWorkload is the ceiling for one workload. A DaemonSet's is the smallest
// of the nodes it runs on, since it has a pod on each; every other kind keeps
// the cluster's.
func (nc NodeCapacity) forWorkload(kind string, pods []Pod) NodeCapacity {
	if kind != "DaemonSet" {
		return nc
	}
	out := NodeCapacity{byName: nc.byName}
	seen := map[string]bool{}
	for _, p := range pods {
		n, ok := nc.byName[p.NodeName]
		if !ok || seen[p.NodeName] {
			continue
		}
		seen[p.NodeName] = true
		out.ReadyCPU = append(out.ReadyCPU, n.AllocatableCPU)
		out.ReadyMemory = append(out.ReadyMemory, n.AllocatableMemory)
		if out.MaxCPU == 0 || n.AllocatableCPU < out.MaxCPU {
			out.MaxCPU = n.AllocatableCPU
		}
		if out.MaxMemory == 0 || n.AllocatableMemory < out.MaxMemory {
			out.MaxMemory = n.AllocatableMemory
		}
	}
	if len(seen) == 0 {
		// Its nodes are not known (no node read, or pods on nodes that were
		// not): the cluster's ceiling, which is what every other kind gets.
		return nc
	}
	return out
}

// nodesFitting says how many nodes could take a pod of this size, and nothing
// when every one of them could.
func nodesFitting(value int64, nodeLimits []int64) string {
	if len(nodeLimits) <= 1 {
		return ""
	}
	fits := 0
	for _, c := range nodeLimits {
		if c >= value {
			fits++
		}
	}
	if fits == len(nodeLimits) {
		return ""
	}
	return fmt.Sprintf(" · fits on %d/%d nodes", fits, len(nodeLimits))
}

// sized is a rounded suggestion with everything the message needs to describe
// how it was arrived at.
type sized struct {
	suggested string
	raw       int64
	capped    bool
	fitSuffix string
}

// suggest rounds and formats, with no ceiling. Used where a ceiling makes no
// sense: a reduction cannot fail to fit, and storage is not bounded by a node.
func suggest(raw float64, isCPU bool) sized {
	rounded := RoundResource(raw, isCPU)
	return sized{suggested: fmtRaw(float64(rounded), isCPU), raw: rounded}
}

// suggestCapped rounds and formats an increase, capped at what the largest node
// can hold. When the cap is no bigger than what the container already has,
// there is no increase to suggest, hence the "Migrate to larger node" action.
func suggestCapped(raw float64, isCPU bool, nc NodeCapacity) sized {
	ceiling := nc.MaxMemory
	nodeValues := nc.ReadyMemory
	if isCPU {
		ceiling = nc.MaxCPU
		nodeValues = nc.ReadyCPU
	}
	rounded := RoundResource(raw, isCPU)
	if ceiling > 0 && rounded > ceiling {
		return sized{
			suggested: fmtRaw(float64(ceiling), isCPU),
			raw:       ceiling,
			capped:    true,
			fitSuffix: nodesFitting(ceiling, nodeValues),
		}
	}
	return sized{
		suggested: fmtRaw(float64(rounded), isCPU),
		raw:       rounded,
		fitSuffix: nodesFitting(rounded, nodeValues),
	}
}

// FormatCPU is how this package writes a CPU quantity for a human, exported
// so a page's bars use the same words as its suggestions.
func FormatCPU(v int64) string { return fmtRaw(float64(v), true) }

// FormatMemory is how this package writes a memory quantity for a human.
func FormatMemory(v int64) string { return fmtRaw(float64(v), false) }
