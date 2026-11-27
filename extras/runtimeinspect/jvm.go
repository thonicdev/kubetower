// Package runtimeinspect is the Runtime Inspector, published until 0.2.0 as
// the JVM Inspector (key `jvm`, kept as its legacy key): which runtime each
// container of a cluster's pods runs - Java, Go, Node.js or Python, read from
// the spec (runtimes.go) - and, for Java, how close each one's maximum heap is
// to its container's memory limit, behind its own per-cluster switch.
//
// # What is here
//
// The page and the probe read the pod spec alone, and run nothing in a
// container:
//
//   - detect.go says why a container looks like Java: its image, its command
//     line, the environment variables the JVM or its launcher reads. Each hint
//     carries its reason, because a guess has to say what it rests on;
//   - heap.go reads the heap options the JVM itself would read
//     (JAVA_TOOL_OPTIONS, then JDK_JAVA_OPTIONS, then the command line, the
//     last one winning), estimates the maximum heap from them and the memory
//     limit, and grades the OOMKill risk;
//   - routes.go answers `GET /` (the probable Java pods of the cluster or of a
//     namespace, with each container's working set from metrics-server) and
//     `GET /pod/{namespace}/{name}` (one pod, every container);
//   - column.go answers `GET /column?kind=` (the "Java" cell of the console's
//     Pods list and of the workloads that own pods, from one pod list);
//   - suggest.go is the banner's evidence (extension.Suggester).
//
// The JVM tab on a pod asks the JVM itself, on a click and never on opening:
//
//   - inspect.go answers `POST /pod/{namespace}/{name}/{container}/inspect`
//     (version, uptime, GC, key flags, command line, system properties, and
//     the memory bar against the working set, request and limit) and
//     `…/inspect/reveal` (one masked value, read again);
//   - jcmd.go holds the only commands it may send, a fixed map, and is the one
//     caller of Host.Exec; parse.go reads their answers; mask.go hides the
//     values whose name looks like a secret.
//
// # Who sees what
//
// Every read is a pod list through Host.List, or the working sets through
// Host.ContainerUsage, reviewed for the person behind the request.
// **Host.Client is never called**, and nothing here writes. jcmd runs through
// Host.Exec, which reviews `create pods/exec` on the pod for the person and
// records each argv.
//
// # What one answer may hold
//
// Nothing is cached between requests. A read asks for at most MaxPodsRead
// pods, an inventory answers with at most MaxJavaPods of them, the worst
// first, and says so when it cuts; the metrics read beside the list is
// always waited for, so no goroutine outlives its request.
//
// # What a spec cannot show
//
// An image whose entrypoint starts java with nothing in the spec (a Jib image,
// most in-house images, a buildpack's launcher) has no hint here, and options
// set in the image's own environment are invisible to the API server. Both
// are said on the page: an absent pod is "not seen from its spec", never "not
// Java".
package runtimeinspect

import (
	"net/http"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
)

// Key is `runtime`: the page's URL segment, the KT_EXTRAS name and the key the
// per-cluster switch is stored under, so it is not free to change.
const Key = "runtime"

// legacyKey is the key the page was published under as the JVM Inspector:
// a stored switch, a KT_EXTRAS line and a bookmark that say `jvm` keep working.
const legacyKey = "jvm"

// Label is the menu entry.
const Label = "Runtime Inspector"

var podsGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

// JVM is the extension `cmd/kubetower` registers. It holds nothing between
// requests.
type JVM struct{}

// New is the constructor `cmd/kubetower` calls.
func New() JVM { return JVM{} }

func (JVM) Key() string   { return Key }
func (JVM) Label() string { return Label }

// Summary is the catalog entry.
func (JVM) Summary() string {
	return "Which runtime each pod runs - Java, Go, Node.js or Python - read from its spec, and for Java how " +
		"close each one's maximum heap is to its memory limit, before the container is OOMKilled."
}

// Needs is drawn beside the switch.
func (JVM) Needs() string {
	return "pods running Java, Go, Node.js or Python; each is recognized from the image, the command line, the " +
		"environment or the " + AnnotationRuntime + " annotation in the spec"
}

// Legacy is the JVM Inspector's key, which this page replaced.
func (JVM) Legacy() []string { return []string{legacyKey} }

// Routes are four reads from the spec and from metrics, all GETs, which run
// nothing in a container; every route under a container is a POST, so the
// console's request header and origin check hold it and a link from another
// site reaches none of them (#82): the probe that asks whether jcmd is there
// (tools.go), the JVM tab's four, which run jcmd through Host.Exec on the
// person's click (inspect.go, threads.go, histogram.go), and the heap dump,
// whose POST answers the file itself (heapdump.go); Go's pprof through a
// tunnel to the pod (pprof.go): one probe that asks whether it answers, and
// the reads; and Node.js's inspector through the same tunnel (node.go): one
// probe, the confirmed open with a fixed script through Host.Exec, and the
// reads through it. Nothing here changes a cluster; opening the inspector
// changes the process, until it restarts.
func (x JVM) Routes(h extension.Host) []extension.Route {
	return []extension.Route{
		{Method: http.MethodGet, Pattern: "/{$}", Handler: x.inventory(h)},
		{Method: http.MethodGet, Pattern: "/column", Handler: x.column(h)},
		{Method: http.MethodGet, Pattern: "/pod/{namespace}/{name}", Handler: x.pod(h)},
		{Method: http.MethodGet, Pattern: "/pod/{namespace}/{name}/history", Handler: x.history(h)},
		{Method: http.MethodPost, Pattern: "/pod/{namespace}/{name}/{container}/tools", Handler: x.tools(h)},
		{Method: http.MethodPost, Pattern: "/pod/{namespace}/{name}/{container}/inspect", Handler: x.inspect(h)},
		{Method: http.MethodPost, Pattern: "/pod/{namespace}/{name}/{container}/inspect/reveal", Handler: x.reveal(h)},
		{Method: http.MethodPost, Pattern: "/pod/{namespace}/{name}/{container}/threads", Handler: x.threads(h)},
		{Method: http.MethodPost, Pattern: "/pod/{namespace}/{name}/{container}/histogram", Handler: x.histogram(h)},
		{Method: http.MethodPost, Pattern: "/pod/{namespace}/{name}/{container}/heapdump", Handler: x.heapDump(h)},
		{Method: http.MethodPost, Pattern: "/pod/{namespace}/{name}/{container}/recording", Handler: x.flightRecording(h)},
		{Method: http.MethodPost, Pattern: "/pod/{namespace}/{name}/{container}/pprof", Handler: x.pprofState(h)},
		{Method: http.MethodPost, Pattern: "/pod/{namespace}/{name}/{container}/pprof/goroutines", Handler: x.pprofGoroutines(h)},
		{Method: http.MethodPost, Pattern: "/pod/{namespace}/{name}/{container}/pprof/{profile}", Handler: x.pprofDownload(h)},
		{Method: http.MethodPost, Pattern: "/pod/{namespace}/{name}/{container}/node", Handler: x.nodeState(h)},
		{Method: http.MethodPost, Pattern: "/pod/{namespace}/{name}/{container}/node/inspector", Handler: x.nodeOpen(h)},
		{Method: http.MethodPost, Pattern: "/pod/{namespace}/{name}/{container}/node/{read}", Handler: x.nodeRead(h)},
		{Method: http.MethodPost, Pattern: "/pod/{namespace}/{name}/{container}/python", Handler: x.pythonProcesses(h)},
		{Method: http.MethodPost, Pattern: "/pod/{namespace}/{name}/{container}/python/metrics", Handler: x.pythonMetrics(h)},
	}
}
