package detail

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Whether a pod's containers are healthy: the probes as declared (from the
// container spec) and the current state (from `status.containerStatuses` or
// `status.initContainerStatuses`, matched by name). Like container.go this only
// parses the pod in hand.
//
// Three answers must not share one blank cell: the container declares no such
// probe (a finding: a Service may send traffic to a container nothing asks
// about), something is declared and could not be read, and the kubelet has
// reported nothing (which is not "not ready"). Each has its own value.

// ProbeState is whether a probe is declared, as a kind rather than as a nil.
// Three values rather than a `*Probe`, because nil would fold "absent" and
// "unreadable" into one.
type ProbeState string

const (
	// ProbeDeclared: the container declares this probe, and Handler names it.
	ProbeDeclared ProbeState = "declared"
	// ProbeAbsent: the container declares no such probe. The panel says so; it
	// is a finding, not missing data.
	ProbeAbsent ProbeState = "absent"
	// ProbeUnreadable: something is declared and this console cannot say what
	// (a field of the wrong shape, or an unknown handler). Problem says which;
	// never used for a container with no probe.
	ProbeUnreadable ProbeState = "unreadable"
)

// Probe is one of a container's three probes, as declared.
// The startup probe is kept beside the other two: while it runs the kubelet
// holds liveness and readiness off, so it explains a container that never
// gets as far as failing liveness. Nothing here judges whether a probe is good.
type Probe struct {
	// Kind is "liveness", "readiness" or "startup".
	Kind  string
	State ProbeState
	// Handler is the sort of check ("httpGet", "exec", "tcpSocket", "grpc") and
	// Target what it points at; two fields so a caller never parses a sentence.
	Handler string
	Target  string
	// The four timings, as the object carries them. Not defaulted here: the API
	// server fills them on write, and inventing defaults would show a number the
	// object does not say. They travel together: PeriodSeconds and
	// FailureThreshold together are how long a container has to recover, and
	// InitialDelaySeconds often explains a liveness kill during start-up.
	InitialDelaySeconds int
	PeriodSeconds       int
	TimeoutSeconds      int
	FailureThreshold    int
	// Problem says why this console cannot name what is declared; it keeps
	// ProbeUnreadable from reading as ProbeAbsent.
	Problem string
}

// ContainerRunState is what the kubelet last said a container was doing.
//
// The first three are Kubernetes' own `state` union; the fourth is this
// package's, which is why this is not a `Running bool`.
type ContainerRunState string

const (
	ContainerRunning    ContainerRunState = "running"
	ContainerWaiting    ContainerRunState = "waiting"
	ContainerTerminated ContainerRunState = "terminated"
	// ContainerUnreported: nothing was reported about this container (a new
	// pod, one never scheduled, or an unreadable status block). It is not
	// "not ready"; Ready means nothing under this value.
	ContainerUnreported ContainerRunState = "unreported"
)

// ContainerStatus is how one container is doing.
type ContainerStatus struct {
	State ContainerRunState
	// Ready is meaningful only where State is not ContainerUnreported. For a
	// container behind a Service it is whether traffic is being sent to it.
	Ready bool
	// Reason is the kubelet's own word: CrashLoopBackOff, ImagePullBackOff,
	// OOMKilled, Completed. Empty on a container that is simply running.
	Reason string
	// Message is the kubelet's sentence beside Reason, where it wrote one.
	Message string
	// Restarts is meaningful only where State is not ContainerUnreported.
	Restarts int
	// ExitCode is what the last run exited with, where State is
	// ContainerTerminated. Kept beside Reason: OOMKilled and 137 are one event.
	ExitCode int
	// Problem says why there is no status on this row. Present only where State
	// is ContainerUnreported.
	Problem string
	// Last is how the previous run ended (`lastState.terminated`), or nil. An
	// OOM-killed container is running again, and this is the only place the
	// reason it restarted survives.
	Last *Termination
}

// Termination is one finished run of a container, as the kubelet keeps it in
// `lastState.terminated`.
// It is the one previous run only: the kubelet keeps exactly one until the
// next restart replaces it, and an evicted or recreated pod has none. Its
// absence does not mean "never".
type Termination struct {
	ExitCode int
	// Signal is the signal that ended it, where the runtime reported one; most
	// leave it at zero and encode it in the exit code (128+n).
	Signal int
	// Reason is the kubelet's word: OOMKilled, Error, Completed.
	Reason  string
	Message string
	// StartedAt and FinishedAt bound the run; zero where the kubelet did not
	// write them.
	StartedAt  time.Time
	FinishedAt time.Time
}

// probeKinds is the three, in drawing order, with the field each is read from.
// Fixed, so a container that declares none still produces three entries.
var probeKinds = []struct{ kind, field string }{
	{"liveness", "livenessProbe"},
	{"readiness", "readinessProbe"},
	{"startup", "startupProbe"},
}

// probesOf reads all three probes of one container spec, always all three.
func probesOf(spec map[string]any) []Probe {
	out := make([]Probe, 0, len(probeKinds))
	for _, want := range probeKinds {
		out = append(out, probeOf(spec, want.kind, want.field))
	}
	return out
}

func probeOf(spec map[string]any, kind, field string) Probe {
	probe := Probe{Kind: kind, State: ProbeAbsent}

	block, found, err := unstructured.NestedMap(spec, field)
	if err != nil {
		// Present and not an object: declared, but unreadable.
		probe.State = ProbeUnreadable
		probe.Problem = "the pod carries a " + kind + " probe in a shape this console cannot read"
		return probe
	}
	if !found {
		return probe
	}

	probe.InitialDelaySeconds = intOf(block, "initialDelaySeconds")
	probe.PeriodSeconds = intOf(block, "periodSeconds")
	probe.TimeoutSeconds = intOf(block, "timeoutSeconds")
	probe.FailureThreshold = intOf(block, "failureThreshold")

	handler, target, known := handlerOf(block)
	if !known {
		// Declared (the timings above are kept) with an unknown handler: said
		// as unreadable, since blank would read as "there is none".
		probe.State = ProbeUnreadable
		probe.Problem = "this " + kind + " probe uses a handler this console does not recognize"
		return probe
	}
	probe.State = ProbeDeclared
	probe.Handler = handler
	probe.Target = target
	return probe
}

// handlerOf names the check and renders what it points at.
//
// Rendered here so the client does not carry a second copy of this switch.
func handlerOf(block map[string]any) (handler, target string, known bool) {
	if get, found, err := unstructured.NestedMap(block, "httpGet"); found && err == nil {
		path, _, _ := unstructured.NestedString(get, "path")
		if path == "" {
			path = "/"
		}
		scheme, _, _ := unstructured.NestedString(get, "scheme")
		address := path + ":" + portOf(get)
		// Only where it is not the default, as with PullPolicy.
		if strings.EqualFold(scheme, "HTTPS") {
			address = "https " + address
		}
		return "httpGet", address, true
	}
	if socket, found, err := unstructured.NestedMap(block, "tcpSocket"); found && err == nil {
		return "tcpSocket", ":" + portOf(socket), true
	}
	if exec, found, err := unstructured.NestedMap(block, "exec"); found && err == nil {
		return "exec", commandOf(exec), true
	}
	if grpc, found, err := unstructured.NestedMap(block, "grpc"); found && err == nil {
		service, _, _ := unstructured.NestedString(grpc, "service")
		if service != "" {
			return "grpc", service + " :" + portOf(grpc), true
		}
		return "grpc", ":" + portOf(grpc), true
	}
	return "", "", false
}

// portOf reads a port that may be a number or a named port.
//
// Both are legal in a probe: `8080` is a port, `http` names one declared in
// `ports`. A named port rendered empty would make a working probe look broken.
func portOf(block map[string]any) string {
	value, found, err := unstructured.NestedFieldNoCopy(block, "port")
	if !found || err != nil {
		return ""
	}
	switch port := value.(type) {
	case string:
		return port
	case int64:
		return strconv.FormatInt(port, 10)
	case float64:
		// A number decoded by plain encoding/json.
		return strconv.FormatInt(int64(port), 10)
	default:
		return fmt.Sprint(port)
	}
}

func commandOf(exec map[string]any) string {
	items, found, err := unstructured.NestedStringSlice(exec, "command")
	if !found || err != nil || len(items) == 0 {
		return ""
	}
	return strings.Join(items, " ")
}

// intOf reads a whole number, and answers 0 where there is none.
//
// Both `int64` and `float64`: the dynamic client decodes an integral JSON
// number to int64, while plain `encoding/json` (fixtures, `sigs.k8s.io/yaml`)
// leaves a float64, which `NestedInt64` reports as not found.
func intOf(block map[string]any, field string) int {
	value, found, err := unstructured.NestedFieldNoCopy(block, field)
	if !found || err != nil {
		return 0
	}
	switch number := value.(type) {
	case int64:
		return int(number)
	case float64:
		return int(number)
	case int32:
		return int(number)
	case int:
		return number
	default:
		return 0
	}
}

// statusesOf indexes a pod's reported container statuses by name.
//
// Both lists into one map, by name: neither list is in spec order, and a
// restartable sidecar is filed under `initContainerStatuses`. Names are unique
// across both lists (the API server validates that). A pod with no readable
// status returns nil, and every container reads ContainerUnreported.
func statusesOf(raw *unstructured.Unstructured) map[string]map[string]any {
	return statusesIn(raw, "initContainerStatuses", "containerStatuses")
}

// statusesIn indexes the named status lists by container name, nil when none
// carries anything.
func statusesIn(raw *unstructured.Unstructured, fields ...string) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, field := range fields {
		items, found, err := unstructured.NestedSlice(raw.Object, "status", field)
		if err != nil || !found {
			continue
		}
		for _, item := range items {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name, _, _ := unstructured.NestedString(entry, "name")
			if name == "" {
				continue
			}
			out[name] = entry
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// statusOf reads one container's reported status, or says that there is none.
//
// Never nil: no status is itself an answer, and nil would look like a mapper
// that dropped the member.
func statusOf(reported map[string]any) *ContainerStatus {
	if reported == nil {
		return &ContainerStatus{
			State: ContainerUnreported,
			// Ordinary: every new pod looks like this until scheduled, and so
			// does one that never will be.
			Problem: "the kubelet has reported nothing about this container yet",
		}
	}

	status := &ContainerStatus{}
	status.Ready, _, _ = unstructured.NestedBool(reported, "ready")
	status.Restarts = intOf(reported, "restartCount")
	status.Last = lastTermination(reported)

	for _, shape := range []struct {
		field string
		state ContainerRunState
	}{
		{"running", ContainerRunning},
		{"waiting", ContainerWaiting},
		{"terminated", ContainerTerminated},
	} {
		block, found, err := unstructured.NestedMap(reported, "state", shape.field)
		if !found || err != nil {
			continue
		}
		status.State = shape.state
		status.Reason, _, _ = unstructured.NestedString(block, "reason")
		status.Message, _, _ = unstructured.NestedString(block, "message")
		if shape.state == ContainerTerminated {
			status.ExitCode = intOf(block, "exitCode")
		}
		return status
	}

	// An entry with no recognized state is closer to "nothing reported"
	// than to any of the three; `running` would be wrong and look right.
	return &ContainerStatus{
		State:   ContainerUnreported,
		Problem: "the kubelet reported this container without a state this console recognizes",
	}
}

// lastTermination reads `lastState.terminated`, or nil where the kubelet keeps
// no previous run. Read beside `state`, never instead of it: this is why it
// restarted.
func lastTermination(reported map[string]any) *Termination {
	block, found, err := unstructured.NestedMap(reported, "lastState", "terminated")
	if !found || err != nil {
		return nil
	}
	out := &Termination{
		ExitCode: intOf(block, "exitCode"),
		Signal:   intOf(block, "signal"),
	}
	out.Reason, _, _ = unstructured.NestedString(block, "reason")
	out.Message, _, _ = unstructured.NestedString(block, "message")
	out.StartedAt = timeOf(block, "startedAt")
	out.FinishedAt = timeOf(block, "finishedAt")
	return out
}

// timeOf reads an RFC 3339 timestamp, or the zero time where there is none or
// it does not parse; a run with no recorded end is drawn without one.
func timeOf(block map[string]any, field string) time.Time {
	text, _, _ := unstructured.NestedString(block, field)
	at, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}
	}
	return at
}
