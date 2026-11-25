package runtimeinspect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// The JVM tab's two POSTs: inspect one container's JVM, and reveal one value
// the inspection masked. Both run jcmd through Host.Exec, so both are POSTs:
// each exec is recorded as the person's, and a cross-site link can make a
// browser send a GET with the session cookie. Nothing runs until the person
// clicks: the tab's probe reads the pod's spec through `GET /pod/…`.

// The codes the inspection answers a failure with, beside those of routes.go.
const (
	CodeUnknownContainer = "unknown_container"
	CodeNotRunning       = "not_running"
	// The image has no jcmd: a JRE or a distroless image.
	CodeNoJcmd = "no_jcmd"
	// jcmd could not attach: -XX:+DisableAttachMechanism, or another user.
	CodeAttachRefused = "attach_refused"
	// jcmd lists no JVM, and pid 1 is not one it can reach.
	CodeNoJVM = "no_jvm"
	// CodeAttachDisabled is a JVM that says it does not support the attach
	// mechanism (-XX:+DisableAttachMechanism): it runs, and cannot be asked.
	CodeAttachDisabled = "attach_disabled"
	// The exec was refused for the person: 403, or 503 when it could not be
	// asked.
	CodeExecForbidden = "exec_forbidden"
	// CodeReadOnly: the cluster is marked read-only (KT_READONLY_CLUSTERS),
	// so nothing is run in its pods nor tunnelled to them (extension.ErrReadOnly).
	CodeReadOnly       = "read_only"
	CodeExecUnreviewed = "exec_unreviewed"
	CodeExecBusy       = "exec_busy"
	CodeExecTimedOut   = "exec_timed_out"
	CodeExecFailed     = "exec_failed"
	CodeJcmdFailed     = "jcmd_failed"
	CodeNoSuchValue    = "no_such_value"
)

// The states of one command of the inspection.
const (
	stateOK          = "ok"
	stateUnavailable = "unavailable" // the JDK does not have the command
	stateNotEnabled  = "not-enabled" // native memory tracking is off
	stateFailed      = "failed"
	// stateTruncated: the output reached the exec's limit and was cut, so it
	// is not read at all - a cut list read as whole would be a wrong answer.
	stateTruncated = "truncated"
	// stateSkipped: not run, the inspection's budget being spent or the
	// request gone.
	stateSkipped = "skipped"
)

// section is one diagnostic command and how it went.
type section struct {
	Command string `json:"command"`
	State   string `json:"state"`
	// Sentence says what a state other than ok means here.
	Sentence string `json:"sentence,omitempty"`
	// Truncated is set when the output reached the exec's limit: what came
	// after it is not read.
	Truncated bool `json:"truncated,omitempty"`
}

// Figure is a size in bytes with its text.
type Figure struct {
	Bytes int64  `json:"bytes"`
	Text  string `json:"text"`
}

func figure(n int64) *Figure { return &Figure{Bytes: n, Text: textutil.Bytes(n)} }

// summary is the tab's first block.
type summary struct {
	VM      string `json:"vm"`
	Version string `json:"version"`
	// Major is the feature release, 0 when it could not be read.
	Major  int    `json:"major"`
	Uptime string `json:"uptime"`
	// GC is the collector VM.flags names, empty when none does.
	GC string `json:"gc"`
	// Flags are the flags that decide the JVM's memory, as VM.flags printed
	// them; one VM.flags did not print is at its default.
	Flags []keyFlag `json:"flags"`
}

// keyFlag is one flag of the summary, with its size where it is one.
type keyFlag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Text  string `json:"text,omitempty"`
}

// The flags the summary names, in this order, where VM.flags printed them.
var keyFlags = []string{"MaxHeapSize", "InitialHeapSize", "MaxRAMPercentage", "InitialRAMPercentage",
	"UseContainerSupport", "NativeMemoryTracking", "DisableAttachMechanism"}

// commandLineRows is VM.command_line, masked.
type commandLineRows struct {
	JVMArgs     []arg `json:"jvmArgs"`
	JavaCommand []arg `json:"javaCommand"`
}

// inspectAnswer is `POST /pod/{namespace}/{name}/{container}/inspect`.
type inspectAnswer struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	// State is `inspected`, or `choose` when jcmd lists several JVMs and no
	// `?pid=` chose one: JVMs is then the list, and nothing else was run.
	State string      `json:"state"`
	JVMs  []listedJVM `json:"jvms,omitempty"`
	PID   int         `json:"pid,omitempty"`
	// PIDFrom is how the JVM was found: `listed`, `chosen`, or `pid1` when
	// jcmd listed none (a JVM started with -XX:-UsePerfData is not listed).
	PIDFrom string `json:"pidFrom,omitempty"`
	// Inherited is set when the image has no env, so jcmd ran with the
	// container's JAVA_TOOL_OPTIONS and JDK_JAVA_OPTIONS.
	Inherited   bool             `json:"inherited,omitempty"`
	Sections    []section        `json:"sections,omitempty"`
	Summary     *summary         `json:"summary,omitempty"`
	Memory      *memoryBar       `json:"memory,omitempty"`
	AllFlags    []flag           `json:"allFlags,omitempty"`
	CommandLine *commandLineRows `json:"commandLine,omitempty"`
	Properties  []propertyRow    `json:"properties,omitempty"`
	// GC is each garbage collector's count and time, from PerfCounter.print;
	// nil when it was not read.
	GC    *gcWork  `json:"gc,omitempty"`
	Notes []string `json:"notes"`
}

// revealAnswer is `POST …/inspect/reveal`: one value, read again.
type revealAnswer struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// revealQuery is the body of a reveal: which JVM, and which value - a system
// property by its key, or an option of the command line by its name.
type revealQuery struct {
	PID  int    `json:"pid"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// target is the container a POST names, read from the pod's spec.
type target struct {
	cluster, namespace, name, container string
	spec                                corev1.Container
}

// resolve reads the pod the route names, through the list reviewed for the
// person, and finds the container in it. On a failure it has answered w.
func resolve(w http.ResponseWriter, r *http.Request, h extension.Host) (target, bool) {
	t := target{cluster: r.PathValue("cluster"), namespace: r.PathValue("namespace"), name: r.PathValue("name"),
		container: r.PathValue("container")}
	switch {
	case len(validation.IsDNS1123Label(t.namespace)) > 0:
		writeProblem(w, http.StatusBadRequest, CodeBadRequest, quote(t.namespace)+" is not a namespace name.", nil)
		return t, false
	case len(validation.IsDNS1123Subdomain(t.name)) > 0:
		writeProblem(w, http.StatusBadRequest, CodeBadRequest, quote(t.name)+" is not a pod name.", nil)
		return t, false
	case len(validation.IsDNS1123Label(t.container)) > 0:
		writeProblem(w, http.StatusBadRequest, CodeBadRequest, quote(t.container)+" is not a container name.", nil)
		return t, false
	}
	page, err := h.List(r, t.cluster, extension.ListQuery{GVR: podsGVR, Namespace: t.namespace,
		FieldSelector: "metadata.name=" + t.name})
	if err != nil {
		writeReadFailure(w, t.cluster, err)
		return t, false
	}
	for i := range page.Items {
		item := &page.Items[i]
		if item.GetName() != t.name || item.GetNamespace() != t.namespace {
			continue
		}
		var p corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &p); err != nil {
			writeProblem(w, http.StatusBadGateway, CodeUnreadablePod, "The cluster returned the pod, and its spec could not be read.", err)
			return t, false
		}
		for _, c := range p.Spec.Containers {
			if c.Name == t.container {
				t.spec = c
				if p.Status.Phase != corev1.PodRunning {
					writeProblem(w, http.StatusConflict, CodeNotRunning, "The pod is "+strings.ToLower(string(p.Status.Phase))+
						", not running: there is no JVM to ask.", nil)
					return t, false
				}
				return t, true
			}
		}
		writeProblem(w, http.StatusNotFound, CodeUnknownContainer,
			"The pod has no container "+quote(t.container)+": only its own containers are inspected, never an init container.", nil)
		return t, false
	}
	writeProblem(w, http.StatusNotFound, CodeUnknownPod,
		"There is no pod "+quote(t.name)+" in namespace "+quote(t.namespace)+": it may have been deleted or replaced.", nil)
	return t, false
}

func (t target) runner(h extension.Host, r *http.Request) *runner {
	return &runner{h: h, r: r, cluster: t.cluster, namespace: t.namespace, pod: t.name, container: t.container}
}

// The sentences of the failures a person can act on.
const (
	noJcmdText = "There is no jcmd in this image: a JRE or a distroless image carries none, so the JVM cannot be asked " +
		"from inside its container. Inspection through an ephemeral container comes later."
	attachRefusedText = "The JVM refused jcmd's attach: it runs with -XX:+DisableAttachMechanism, or jcmd runs as " +
		"another user than the JVM."
	attachDisabledText = "The JVM does not support the attach mechanism: it was started with -XX:+DisableAttachMechanism, " +
		"so jcmd cannot ask it anything."
	noJVMText = "jcmd lists no JVM in this container, and process 1 did not answer an attach: either no JVM runs " +
		"here, or it was started with -XX:-UsePerfData and refuses the attach (-XX:+DisableAttachMechanism, or another user)."
	execDeniedText = "The cluster says you may not run commands in this pod (create pods/exec), so jcmd was not run."
	inheritedText  = "This image has no env program, so jcmd ran with the container's JAVA_TOOL_OPTIONS and " +
		"JDK_JAVA_OPTIONS, and loaded whatever they name; its own messages about them are not part of the answer."
)

// writeExecFailure answers an exec that gave no result.
func writeExecFailure(w http.ResponseWriter, cluster string, err error) {
	switch {
	case errors.Is(err, extension.ErrUnknownCluster):
		writeProblem(w, http.StatusNotFound, CodeUnknownCluster, extension.UnknownClusterText(cluster), err)
	case errors.Is(err, context.Canceled):
	case errors.Is(err, extension.ErrReadOnly):
		writeReadOnly(w, cluster, err)
	case errors.Is(err, extension.ErrDenied):
		writeProblem(w, http.StatusForbidden, CodeExecForbidden, execDeniedText, err)
	case errors.Is(err, extension.ErrUnreviewable):
		writeProblem(w, http.StatusServiceUnavailable, CodeExecUnreviewed,
			"The console could not ask the cluster whether you may run commands in this pod, so it ran none. Try again.", err)
	case errors.Is(err, extension.ErrNoSuchCommand):
		writeProblem(w, http.StatusUnprocessableEntity, CodeNoJcmd, noJcmdText, nil)
	case errors.Is(err, extension.ErrExecBusy):
		writeProblem(w, http.StatusTooManyRequests, CodeExecBusy,
			"Too many commands run by extensions are already running; try again in a moment.", err)
	case errors.Is(err, extension.ErrExecTimedOut):
		writeProblem(w, http.StatusGatewayTimeout, CodeExecTimedOut,
			"jcmd did not answer within its deadline and was stopped.", err)
	default:
		writeProblem(w, http.StatusBadGateway, CodeExecFailed, "jcmd could not be run in the container.", err)
	}
}

// writeReadOnly answers an act the read-only marking refused, in one line.
func writeReadOnly(w http.ResponseWriter, cluster string, err error) {
	writeProblem(w, http.StatusForbidden, CodeReadOnly, cluster+" is read-only: no command, tunnel or dump.", err)
}

// execSentence is a command's failure inside an inspection that had begun.
func execSentence(err error) string {
	switch {
	case errors.Is(err, extension.ErrExecBusy):
		return "Not run: too many commands run by extensions were already running."
	case errors.Is(err, extension.ErrExecTimedOut):
		return "It did not answer within its deadline and was stopped."
	case errors.Is(err, extension.ErrReadOnly):
		return "Not run: the cluster is read-only."
	case errors.Is(err, extension.ErrDenied):
		return execDeniedText
	}
	return "It could not be run: " + textutil.FirstLineN(err.Error(), 300)
}

// pidParam is `?pid=`: 0 when absent.
func pidParam(r *http.Request) (int, error) {
	v := strings.TrimSpace(r.URL.Query().Get("pid"))
	if v == "" {
		return 0, nil
	}
	pid, err := strconv.Atoi(v)
	if err != nil || pid <= 0 || pid > 1<<22 {
		return 0, fmt.Errorf("%s is not a process id", quote(v))
	}
	return pid, nil
}

func (JVM) inspect(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chosen, err := pidParam(r)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, err.Error(), nil)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), inspectBudget)
		defer cancel()
		r = r.WithContext(ctx)
		t, ok := resolve(w, r, h)
		if !ok {
			return
		}
		run := t.runner(h, r)
		out := inspectAnswer{Cluster: t.cluster, Namespace: t.namespace, Pod: t.name, Container: t.container,
			State: "inspected", Notes: []string{}}

		pid, from, jvms, ok := pick(w, run, t.cluster, chosen)
		if !ok {
			return
		}
		if len(jvms) > 0 {
			out.State, out.JVMs, out.Inherited = "choose", jvms, run.plain
			writeJSON(w, http.StatusOK, out)
			return
		}
		out.PID, out.PIDFrom = pid, from

		results := map[string]extension.ExecResult{}
		for i, name := range inspection {
			if i > 0 && ctx.Err() != nil {
				// The request went, or the budget is spent: nothing more is
				// sent, and what was not run says so.
				out.Sections = append(out.Sections, section{Command: commandOf(name), State: stateSkipped,
					Sentence: "Not run: the inspection stopped at its " + inspectBudget.String() + " budget."})
				continue
			}
			res, err := run.dcmd(pid, name)
			if i == 0 && !answered(w, run, t.cluster, pid, from, name, res, err) {
				return
			}
			s := section{Command: commandOf(name)}
			switch {
			case err != nil:
				s.State, s.Sentence = stateFailed, execSentence(err)
			case unknownCommand(res):
				s.State, s.Sentence = stateUnavailable, "Not available on this JDK: it came with JDK 11."
			case name == dcmdNMT && nmtOff(res):
				s.State, s.Sentence = stateNotEnabled, nmtOffText
			case res.ExitCode != 0 && name == dcmdPerf:
				// Never what it printed: its output holds the command line.
				s.State, s.Sentence = stateFailed, "It failed (exit "+strconv.Itoa(res.ExitCode)+
					"): the JVM may run with -XX:-UsePerfData, so the garbage collectors' counts are not read."
			case res.ExitCode != 0:
				s.State, s.Sentence = stateFailed, "It failed (exit "+strconv.Itoa(res.ExitCode)+"): "+said(res)
			case res.Truncated:
				s.State, s.Truncated = stateTruncated, true
				s.Sentence = "Its output reached the exec's limit of " + textutil.Bytes(extension.ExecOutputLimit) +
					" and was cut, so it is not read: a cut answer would read as a whole one."
			default:
				s.State = stateOK
				results[name] = res
			}
			out.Sections = append(out.Sections, s)
		}
		out.Inherited = run.plain
		if run.plain {
			out.Notes = append(out.Notes, inheritedText)
		}
		fill(&out, results)
		out.Memory = breakdown(r, h, t, results, &out)
		writeJSON(w, http.StatusOK, out)
	})
}

// pick is the JVM a POST asks: the one chosen with `?pid=`, the one jcmd
// lists, or process 1 when it lists none. With several listed and none
// chosen, jvms is the list and nothing else is to be run. On a failure it has
// answered w, and ok is false.
func pick(w http.ResponseWriter, run *runner, cluster string, chosen int) (pid int, from string, jvms []listedJVM, ok bool) {
	if chosen != 0 {
		return chosen, "chosen", nil, true
	}
	res, err := run.list()
	switch {
	case err != nil:
		writeExecFailure(w, cluster, err)
		return 0, "", nil, false
	case !run.plain && missingJcmd(res):
		writeProblem(w, http.StatusUnprocessableEntity, CodeNoJcmd, noJcmdText, nil)
		return 0, "", nil, false
	case res.ExitCode != 0:
		writeProblem(w, http.StatusBadGateway, CodeJcmdFailed, "jcmd -l failed (exit "+strconv.Itoa(res.ExitCode)+
			"), so which JVM runs here is unknown.", errors.New(said(res)))
		return 0, "", nil, false
	}
	listed := parseList(res.Stdout)
	switch len(listed) {
	case 0:
		return 1, "pid1", nil, true
	case 1:
		return listed[0].PID, "listed", nil, true
	}
	return 0, "", listed, true
}

// answered decides, from the first command sent to pid, whether there is a
// JVM to ask; when there is not, it has answered w and is false.
func answered(w http.ResponseWriter, run *runner, cluster string, pid int, from, name string,
	res extension.ExecResult, err error) bool {
	switch {
	case err != nil:
		writeExecFailure(w, cluster, err)
	case !run.plain && missingJcmd(res):
		writeProblem(w, http.StatusUnprocessableEntity, CodeNoJcmd, noJcmdText, nil)
	case attachDisabled(res):
		writeProblem(w, http.StatusUnprocessableEntity, CodeAttachDisabled, attachDisabledText, errors.New(said(res)))
	case attachRefused(res) && from == "pid1":
		writeProblem(w, http.StatusUnprocessableEntity, CodeNoJVM, noJVMText, errors.New(said(res)))
	case attachRefused(res):
		writeProblem(w, http.StatusUnprocessableEntity, CodeAttachRefused, attachRefusedText, errors.New(said(res)))
	case res.ExitCode != 0:
		writeProblem(w, http.StatusBadGateway, CodeJcmdFailed, "jcmd "+strconv.Itoa(pid)+" "+commandOf(name)+" failed (exit "+
			strconv.Itoa(res.ExitCode)+").", errors.New(said(res)))
	default:
		return true
	}
	return false
}

// commandOf is a diagnostic command as the answer names it: `VM.metaspace basic`.
func commandOf(name string) string {
	d, ok := diagnostics[name]
	if !ok {
		return name
	}
	return strings.Join(append([]string{d.name}, d.args...), " ")
}

// nmtOffText is the sentence for a JVM without native memory tracking:
// "not enabled", never zero.
const nmtOffText = "Native memory tracking is not enabled in this JVM, so its memory outside the heap, metaspace and " +
	"code cache is not broken down. It can only be turned on at start-up: -XX:NativeMemoryTracking=summary, " +
	"for instance in JAVA_TOOL_OPTIONS, at a cost of 5 to 10 % of the JVM's performance according to Oracle."

// fill fills the summary, the flags, the command line and the properties from
// the commands that answered.
func fill(out *inspectAnswer, results map[string]extension.ExecResult) {
	s := summary{Flags: []keyFlag{}}
	if res, ok := results[dcmdVersion]; ok {
		v := parseVersion(body(res.Stdout))
		s.VM, s.Version, s.Major = v.VM, v.JDK, v.Major
	}
	if res, ok := results[dcmdUptime]; ok {
		if secs, ok := parseUptime(body(res.Stdout)); ok {
			s.Uptime = uptimeText(secs)
		}
	}
	if res, ok := results[dcmdFlags]; ok {
		flags := parseFlags(body(res.Stdout))
		out.AllFlags = flags
		if len(flags) > maxFlags {
			out.AllFlags = flags[:maxFlags]
			out.Notes = append(out.Notes, fmt.Sprintf("VM.flags printed %d flags; the first %d are listed.", len(flags), maxFlags))
		}
		s.GC = collector(flags)
		for _, name := range keyFlags {
			v, ok := flagValue(flags, name)
			if !ok {
				continue
			}
			row := keyFlag{Name: name, Value: v}
			if strings.HasSuffix(name, "HeapSize") {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					row.Text = textutil.Bytes(n)
				}
			}
			s.Flags = append(s.Flags, row)
		}
	}
	out.Summary = &s
	if res, ok := results[dcmdPerf]; ok {
		if gc, ok := parseGC(body(res.Stdout)); ok {
			out.GC = &gc
		} else {
			out.Notes = append(out.Notes, "PerfCounter.print answered without the garbage collectors' counters, so their counts are not shown.")
		}
	}
	if res, ok := results[dcmdCommandLine]; ok {
		cl := parseCommandLine(body(res.Stdout))
		out.CommandLine = &commandLineRows{JVMArgs: maskArgs(cl.JVMArgs), JavaCommand: maskArgs(cl.JavaCommand)}
	}
	if res, ok := results[dcmdProperties]; ok {
		out.Properties = []propertyRow{}
		props := parseProperties(body(res.Stdout))
		for _, p := range props {
			if len(out.Properties) == maxProperties {
				out.Notes = append(out.Notes, fmt.Sprintf("The JVM has %d system properties; the first %d are listed.",
					len(props), maxProperties))
				break
			}
			out.Properties = append(out.Properties, maskProperty(p))
		}
	}
}

// uptimeText is a duration a person reads: `3 d 4 h`, `2 h 5 min`, `49 s`.
func uptimeText(secs float64) string {
	d := int64(secs)
	switch {
	case d >= 86400:
		return fmt.Sprintf("%d d %d h", d/86400, d%86400/3600)
	case d >= 3600:
		return fmt.Sprintf("%d h %d min", d/3600, d%3600/60)
	case d >= 60:
		return fmt.Sprintf("%d min %d s", d/60, d%60)
	}
	return fmt.Sprintf("%d s", d)
}

func (JVM) reveal(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q revealQuery
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&q); err != nil {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, "The request is not a reveal: {pid, kind, name}.", err)
			return
		}
		q.Name = strings.TrimSpace(q.Name)
		if q.PID <= 0 || q.Name == "" || len(q.Name) > 256 || (q.Kind != "property" && q.Kind != "option") {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest,
				"A reveal names a JVM's process id, a kind (property or option) and a name.", nil)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), revealBudget)
		defer cancel()
		r = r.WithContext(ctx)
		t, ok := resolve(w, r, h)
		if !ok {
			return
		}
		name := dcmdProperties
		if q.Kind == "option" {
			name = dcmdCommandLine
		}
		run := t.runner(h, r)
		res, err := run.dcmd(q.PID, name)
		switch {
		case err != nil:
			writeExecFailure(w, t.cluster, err)
			return
		case !run.plain && missingJcmd(res):
			writeProblem(w, http.StatusUnprocessableEntity, CodeNoJcmd, noJcmdText, nil)
			return
		case attachDisabled(res):
			writeProblem(w, http.StatusUnprocessableEntity, CodeAttachDisabled, attachDisabledText, errors.New(said(res)))
			return
		case attachRefused(res):
			writeProblem(w, http.StatusUnprocessableEntity, CodeAttachRefused, attachRefusedText, errors.New(said(res)))
			return
		case res.ExitCode != 0:
			writeProblem(w, http.StatusBadGateway, CodeJcmdFailed, "jcmd failed (exit "+strconv.Itoa(res.ExitCode)+").",
				errors.New(said(res)))
			return
		case res.Truncated:
			writeProblem(w, http.StatusBadGateway, CodeJcmdFailed, "jcmd's output was cut at the exec's limit, so the value is not read.", nil)
			return
		}
		value, found := "", false
		if q.Kind == "property" {
			for _, p := range parseProperties(body(res.Stdout)) {
				if p.Key == q.Name {
					value, found = p.Value, true
					break
				}
			}
		} else {
			cl := parseCommandLine(body(res.Stdout))
			value, found = argValue(cl.JVMArgs, q.Name)
			if !found {
				value, found = argValue(cl.JavaCommand, q.Name)
			}
		}
		if !found {
			writeProblem(w, http.StatusNotFound, CodeNoSuchValue,
				"The JVM no longer has "+quote(q.Name)+": it may have been restarted.", nil)
			return
		}
		writeJSON(w, http.StatusOK, revealAnswer{Kind: q.Kind, Name: q.Name, Value: value})
	})
}

// --- the memory bar ------------------------------------------------------------

// segment is one part of the memory bar: Start and Width are percentages of
// the track, computed here so the page draws them and never rescales.
type segment struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Bytes int64   `json:"bytes"`
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	Width float64 `json:"width"`
	// Used is the heap's used part, drawn inside its committed segment.
	Used *usedPart `json:"used,omitempty"`
	// Unattributed marks "outside the JVM or not tracked": the working set
	// minus everything the JVM says it committed. It is not attributed to
	// anything.
	Unattributed bool `json:"unattributed,omitempty"`
}

type usedPart struct {
	Bytes int64   `json:"bytes"`
	Text  string  `json:"text"`
	Width float64 `json:"width"`
}

// marker is a line across the bar: the request, the limit, the working set,
// the maximum heap.
type marker struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Bytes int64   `json:"bytes"`
	Text  string  `json:"text"`
	At    float64 `json:"at"`
}

// memoryBar is the memory bar and the figures under it.
type memoryBar struct {
	// Source is `nmt` when native memory tracking gave the breakdown, `jvm`
	// when it is the heap, metaspace and code cache alone.
	Source   string    `json:"source"`
	Track    Figure    `json:"track"`
	Segments []segment `json:"segments"`
	Markers  []marker  `json:"markers"`
	// Committed is what the segments add up to before the unattributed part.
	Committed *Figure `json:"committed"`
	// WorkingSet is null when metrics-server gave no figure, and
	// WorkingSetNote says why; no unattributed part is drawn then.
	WorkingSet     *Figure `json:"workingSet"`
	WorkingSetNote string  `json:"workingSetNote,omitempty"`
	// Heap and the rest are the JVM's own figures, null where it said nothing.
	HeapUsed           *Figure `json:"heapUsed"`
	HeapCommitted      *Figure `json:"heapCommitted"`
	HeapMax            *Figure `json:"heapMax"`
	MetaspaceUsed      *Figure `json:"metaspaceUsed"`
	MetaspaceCommitted *Figure `json:"metaspaceCommitted"`
	CodeCacheUsed      *Figure `json:"codeCacheUsed"`
	CodeCacheSize      *Figure `json:"codeCacheSize"`
	// HeapGauge is the heap's used and committed parts as percentages of its
	// maximum, for the ring the tab draws; null without a heap or a maximum.
	HeapGauge *gauge `json:"heapGauge"`
	// NMT is every category native memory tracking reported, when it is on.
	NMT   []nmtRow `json:"nmt,omitempty"`
	Notes []string `json:"notes"`
}

// gauge is a part and a whole, in percentages of the whole, rounded to a
// tenth: the page draws them as given.
type gauge struct {
	UsedPct      float64 `json:"usedPct"`
	CommittedPct float64 `json:"committedPct"`
}

type nmtRow struct {
	Name      string `json:"name"`
	Reserved  Figure `json:"reserved"`
	Committed Figure `json:"committed"`
}

// The NMT categories the bar draws on their own, by the label it gives them;
// every other category, one this list does not know included, goes into
// "Other JVM".
var nmtGroups = []struct {
	key, label string
	names      []string
}{
	{"heap", "Java heap", []string{"Java Heap"}},
	{"metaspace", "Class metadata", []string{"Class", "Metaspace"}},
	{"code", "Code", []string{"Code"}},
	{"threads", "Threads", []string{"Thread", "Thread Stack"}},
	{"gc", "GC", []string{"GC", "GCCardSet"}},
	{"cds", "Shared class space", []string{"Shared class space"}},
}

// breakdown computes the bar from what the commands answered, the pod's
// request and limit, and the working set metrics-server measured.
func breakdown(r *http.Request, h extension.Host, t target, results map[string]extension.ExecResult, out *inspectAnswer) *memoryBar {
	m := &memoryBar{Segments: []segment{}, Markers: []marker{}, Notes: []string{}}
	var heap heapInfo
	if res, ok := results[dcmdHeap]; ok {
		heap = parseHeapInfo(body(res.Stdout))
		if heap.Recognised {
			m.HeapUsed, m.HeapCommitted = figure(heap.Used), figure(heap.Committed)
		} else {
			m.Notes = append(m.Notes, "GC.heap_info answered with a heap layout this page does not recognize, so the "+
				"heap's used and committed figures are not shown.")
		}
		if heap.Metaspace {
			m.MetaspaceUsed, m.MetaspaceCommitted = figure(heap.MetaspaceUsed), figure(heap.MetaspaceCommitted)
		}
	}
	if !heap.Metaspace {
		if res, ok := results[dcmdMetaspace]; ok {
			if b := parseMetaspaceBasic(body(res.Stdout)); b.Recognised {
				heap.Metaspace, heap.MetaspaceUsed, heap.MetaspaceCommitted = true, b.Used, b.Committed
				m.MetaspaceUsed, m.MetaspaceCommitted = figure(b.Used), figure(b.Committed)
			}
		}
	}
	if out.AllFlags != nil {
		if v, ok := flagValue(out.AllFlags, "MaxHeapSize"); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				m.HeapMax = figure(n)
			}
		}
	}
	if heap.Recognised && m.HeapMax != nil {
		of := func(v int64) float64 { return math.Round(min(100, float64(v)/float64(m.HeapMax.Bytes)*100)*10) / 10 }
		m.HeapGauge = &gauge{UsedPct: of(heap.Used), CommittedPct: of(heap.Committed)}
	}
	var code codeCache
	if res, ok := results[dcmdCodeCache]; ok {
		if code = parseCodeCache(body(res.Stdout)); code.Recognised {
			m.CodeCacheUsed, m.CodeCacheSize = figure(code.Used), figure(code.Size)
		}
	}
	var native nmt
	if res, ok := results[dcmdNMT]; ok {
		native = parseNMT(body(res.Stdout))
	}

	// The parts, end to end.
	type part struct {
		key, label string
		bytes      int64
		used       *int64
	}
	var parts []part
	var committed int64
	if native.Recognised {
		m.Source = "nmt"
		grouped := make([]int64, len(nmtGroups))
		var other int64
		for _, c := range native.Categories {
			if len(m.NMT) < maxNMTRows {
				m.NMT = append(m.NMT, nmtRow{Name: c.Name, Reserved: *figure(c.Reserved), Committed: *figure(c.Committed)})
			}
			placed := false
			for i, g := range nmtGroups {
				for _, n := range g.names {
					if n == c.Name {
						grouped[i] += c.Committed
						placed = true
					}
				}
			}
			if !placed {
				other += c.Committed
			}
		}
		for i, g := range nmtGroups {
			p := part{key: g.key, label: g.label, bytes: grouped[i]}
			if g.key == "heap" && heap.Recognised {
				used := heap.Used
				p.used = &used
			}
			parts = append(parts, p)
		}
		parts = append(parts, part{key: "other", label: "Other JVM", bytes: other})
		committed = native.TotalCommitted
	} else {
		m.Source = "jvm"
		if heap.Recognised {
			used := heap.Used
			parts = append(parts, part{key: "heap", label: "Java heap", bytes: heap.Committed, used: &used})
			committed += heap.Committed
		}
		if heap.Metaspace {
			parts = append(parts, part{key: "metaspace", label: "Metaspace", bytes: heap.MetaspaceCommitted})
			committed += heap.MetaspaceCommitted
		}
		if code.Recognised {
			parts = append(parts, part{key: "code", label: "Code cache (used)", bytes: code.Used})
			committed += code.Used
		}
		m.Notes = append(m.Notes, "Without native memory tracking, the JVM's threads, GC structures, compiler and "+
			"symbols are not counted here: they are part of what is outside.")
	}
	if len(parts) > 0 {
		m.Committed = figure(committed)
	}

	// The working set, from metrics-server.
	ws, wsNote := workingSet(r, h, t)
	if ws != nil {
		m.WorkingSet = figure(*ws)
		m.Notes = append(m.Notes, "The working set is metrics-server's last sample, up to a minute old; each jcmd "+
			"this inspection ran is a small JVM in the same container, and may be counted in it.")
		if len(parts) > 0 {
			if outside := *ws - committed; outside > 0 {
				parts = append(parts, part{key: "outside", label: "Outside the JVM or not tracked", bytes: outside})
			} else {
				m.Notes = append(m.Notes, "The JVM has committed more than the container's working set: memory it "+
					"committed and has not touched yet is not resident, so nothing is left unattributed.")
			}
		}
	} else {
		m.WorkingSetNote = wsNote
	}

	// The track: the largest of everything drawn, the limit in the usual case.
	var request, limit int64
	if q, ok := t.spec.Resources.Requests[corev1.ResourceMemory]; ok {
		request = q.Value()
	}
	if q, ok := t.spec.Resources.Limits[corev1.ResourceMemory]; ok {
		limit = q.Value()
	}
	track := max(limit, request)
	var sum int64
	for _, p := range parts {
		sum += p.bytes
	}
	track = max(track, sum)
	if ws != nil {
		track = max(track, *ws)
	}
	if m.HeapMax != nil {
		track = max(track, m.HeapMax.Bytes)
	}
	if track <= 0 {
		m.Notes = append(m.Notes, "Nothing the JVM or the cluster said gives the bar a scale.")
		return m
	}
	m.Track = *figure(track)
	if limit == 0 {
		m.Notes = append(m.Notes, "The container has no memory limit: the bar is scaled to the largest figure shown.")
	}
	pct := func(v int64) float64 { return math.Round(float64(v)/float64(track)*1000) / 10 }
	var at int64
	for _, p := range parts {
		s := segment{Key: p.key, Label: p.label, Bytes: p.bytes, Text: textutil.Bytes(p.bytes), Start: pct(at),
			Width: pct(p.bytes), Unattributed: p.key == "outside"}
		if p.used != nil {
			s.Used = &usedPart{Bytes: *p.used, Text: textutil.Bytes(*p.used), Width: pct(min(*p.used, p.bytes))}
		}
		m.Segments = append(m.Segments, s)
		at += p.bytes
	}
	mark := func(key, label string, v int64) {
		m.Markers = append(m.Markers, marker{Key: key, Label: label, Bytes: v, Text: textutil.Bytes(v), At: pct(v)})
	}
	if request > 0 {
		mark("request", "Request", request)
	}
	if limit > 0 {
		mark("limit", "Limit", limit)
	}
	if ws != nil {
		mark("workingSet", "Working set", *ws)
	}
	if m.HeapMax != nil {
		mark("heapMax", "Max heap", m.HeapMax.Bytes)
	}
	return m
}

// workingSet is the container's working set as metrics-server measured it,
// or nil and why not.
func workingSet(r *http.Request, h extension.Host, t target) (*int64, string) {
	usage, err := h.ContainerUsage(r, t.cluster, t.namespace)
	if err != nil {
		var unavailable *extension.MetricsUnavailable
		switch {
		case errors.As(err, &unavailable) && unavailable.Reason == extension.MetricsAbsent:
			return nil, "No working set from metrics-server (" + textutil.FirstLineN(unavailable.Detail, 200) + "), so " +
				"nothing is drawn outside the JVM."
		case errors.Is(err, extension.ErrDenied):
			return nil, "You may not read this namespace's metrics, so the container's working set is not shown."
		}
		return nil, "The container's working set could not be read from metrics-server: " + textutil.FirstLineN(err.Error(), 200)
	}
	v, ok := usage.Containers[extension.ContainerKey(t.namespace, t.name, t.container)]
	if !ok {
		return nil, "metrics-server has not measured this container yet, so its working set is not known."
	}
	ws := v.MemoryBytes
	return &ws, ""
}
