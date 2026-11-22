package extension

import (
	"fmt"
	"time"
)

// The types of Host.ContainerUsage and Host.UsageWindow: each container's CPU and
// memory now, and over a window. Both exist for right-sizing,
// which sizes a container's requests from what it uses.

// ContainerKey is how ContainerUsage and UsageWindow key a container:
// `namespace/pod/container`, **always**, whatever namespace was asked for.
//
// A StatefulSet names its pods `web-0` in staging and in production alike, so a
// key without the namespace hands one workload the other's figures, silently and
// in the direction that produces a suggestion from somebody else's load. A key
// whose shape changed with the question would be the same bug waiting for the
// first caller holding both shapes.
func ContainerKey(namespace, pod, container string) string {
	return namespace + "/" + pod + "/" + container
}

// Resources is one container's CPU and memory at one instant.
type Resources struct {
	// CPUMillicores is thousandths of a core, the unit a request is written in.
	CPUMillicores int64
	MemoryBytes   int64
}

// LiveUsage is what each container is using now, as metrics-server measured it.
type LiveUsage struct {
	// Containers is keyed ContainerKey. **An empty map means metrics-server
	// answered and nothing in scope was running**; it never means the read
	// failed - that is the error return, a *MetricsUnavailable when there is no
	// metrics-server to ask. A container absent from a non-empty map is one
	// metrics-server did not measure (just started, or already gone), not one
	// using nothing: a measured zero is in the map, as a zero.
	Containers map[string]Resources
	// Complete is false when the containers were narrowed to the namespaces the
	// person behind the request may list pods in, and Note says so in a sentence
	// the page can print. Page's rule: a narrowed answer drawn as complete is the
	// failure an empty one is, one step along.
	Complete bool
	Note     string
}

// MetricsReason is why no per-container usage could be read at all.
type MetricsReason string

const (
	// MetricsAbsent: the cluster answered, and has no metrics-server. A fact
	// about the cluster, for its operator to install one.
	MetricsAbsent MetricsReason = "absent"
	// MetricsRefused: the API server answered and refused the read, 401 or 403.
	MetricsRefused MetricsReason = "refused"
	// MetricsUnreachable: the API server did not answer at all - the network,
	// TLS, or the credential plugin the context authenticates with.
	MetricsUnreachable MetricsReason = "unreachable"
)

// MetricsUnavailable is ContainerUsage's error when there are no figures at all,
// and says which of the three reasons it is. Ask with errors.As.
//
// Three and not one because "no metrics" from a cluster the console failed to
// authenticate against reads like a fact about the cluster, and is a fact about
// the console.
type MetricsUnavailable struct {
	Reason MetricsReason
	// Detail is one line on what happened, for the page to print.
	Detail string
}

func (e *MetricsUnavailable) Error() string {
	return fmt.Sprintf("no per-container usage (%s): %s", e.Reason, e.Detail)
}

// What UsageWindow reads, so a page names the window it was given rather than one
// it remembers. Fixed in the core and not a parameter, deliberately: Prometheus
// has no RBAC, and a window an extension could shape is a query it could aim.
const (
	// WindowSpan is how far back a window reaches: seven days.
	WindowSpan = 7 * 24 * time.Hour
	// WindowStep is the resolution: one sample every fifteen minutes. A CPU
	// sample is the five-minute rate at that instant; a memory sample is the
	// largest working set of the fifteen minutes it closes, so no peak falls
	// between two samples.
	WindowStep = 15 * time.Minute
	// WindowBatch is how many containers one pair of the window's range
	// queries asks for. A scope holding more is read in batches and the
	// answers assembled, so no scope is too large for a window.
	WindowBatch = 500
)

// WindowState is which of four answers a usage window is.
//
// Four and not two, because each of the first three is a different sentence for
// a different person, and drawing any of them as an empty window would tell the
// reader there is no load where the truth is that nobody could look.
type WindowState string

const (
	// WindowNoPrometheus: this installation has no Prometheus configured, or the
	// cluster was asked and runs none.
	WindowNoPrometheus WindowState = "no-prometheus"
	// WindowUnreadable: a Prometheus is there and could not be asked, or
	// refused the query.
	WindowUnreadable WindowState = "unreadable"
	// WindowEmpty: Prometheus answered, with nothing in scope.
	WindowEmpty WindowState = "empty"
	// WindowRead: a window, in Containers.
	WindowRead WindowState = "read"
)

// Sample is one point of a window: T in Unix seconds, V in the series' unit.
type Sample struct {
	T float64
	V float64
}

// ContainerWindow is one container's window, oldest sample first.
//
// Either half may be empty while the other is not: memory is a gauge, there on
// the first scrape, and a five-minute rate() is not, so a pod three minutes old
// has a memory window and no CPU one. A gap - a NaN or an infinity in
// Prometheus's answer - is left out, never turned into a zero.
type ContainerWindow struct {
	// CPU is in millicores.
	CPU []Sample
	// Memory is the working set, in bytes.
	Memory []Sample
}

// Window is a usage window per container, out of the cluster's Prometheus.
type Window struct {
	State WindowState
	// Detail is a sentence for every state: why there is no window, or, for
	// WindowRead, the window and step it covers ("Prometheus, 7d at 15m") and
	// which half failed if one did. What the absence costs - no P95, no burst
	// ratio, no trend - is the page's to add; the core does not know what the
	// window was for.
	Detail string
	// Containers is keyed ContainerKey, set only for WindowRead. Whether that
	// covers the page - a partial window or a full one - is the page's to say:
	// it depends on how many containers it shows. A container absent from here
	// is one Prometheus said nothing about, not one that used nothing.
	Containers map[string]ContainerWindow
	// CPUFailed and MemoryFailed are set, for WindowRead only, when that half of
	// the window failed and the other was read: the reason, in one line, the
	// address dropped before the reason when it does not fit (textutil.ReasonN).
	CPUFailed    string
	MemoryFailed string
	// Partial is set, for WindowRead only, when some of the batches the window
	// was read in could not be read: how many, why, and that their containers
	// have no window, or half of one. The rest of Containers is whole. Detail
	// carries the same sentence.
	Partial string
	// Complete and Note are Page's: false, with the sentence, when the window
	// was narrowed to the namespaces the person may list pods in.
	Complete bool
	Note     string
	// ReadAt is when Prometheus answered the read this window comes from. A
	// window the core kept from an earlier read carries that read's time, so
	// a page can say how old what it draws is. Zero when unknown or when
	// nothing was asked.
	ReadAt time.Time
}
