package runtimeinspect

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// What this extra runs in a container, and nothing else.
//
// Host.Exec runs whatever argv it is handed: the review answers whether the
// person may exec in the pod, which is a shell there, not whether a command
// is harmless. So the list of what may be sent is held here, and **it is
// structural**: argv is built by jcmdArgv from a fixed map, and the routes
// take no command from the client - only which pod, which container and,
// where there are several, which JVM.

// diagnostic is one jcmd diagnostic command this extra may send, with its
// fixed arguments.
type diagnostic struct {
	name string
	args []string
}

// The diagnostic commands, by the name the answer files them under. Each is
// Low or Medium in jcmd's own manual, except Thread.print (a short stop of the
// world) and GC.class_histogram (a full GC), each behind a button of its own.
// Every other command - a heap dump, loading an agent,
// changing a flag, starting a management agent - is absent, and jcmdArgv
// refuses it.
const (
	dcmdVersion     = "VM.version"
	dcmdUptime      = "VM.uptime"
	dcmdFlags       = "VM.flags"
	dcmdCommandLine = "VM.command_line"
	dcmdProperties  = "VM.system_properties"
	dcmdHeap        = "GC.heap_info"
	dcmdMetaspace   = "VM.metaspace"
	dcmdCodeCache   = "Compiler.codecache"
	dcmdNMT         = "VM.native_memory"
	dcmdPerf        = "PerfCounter.print"
	dcmdThreads     = "Thread.print"
	dcmdHistogram   = "GC.class_histogram"
)

var diagnostics = map[string]diagnostic{
	dcmdVersion:     {name: "VM.version"},
	dcmdUptime:      {name: "VM.uptime"},
	dcmdFlags:       {name: "VM.flags"},
	dcmdCommandLine: {name: "VM.command_line"},
	dcmdProperties:  {name: "VM.system_properties"},
	dcmdHeap:        {name: "GC.heap_info"},
	// `basic` needs no safepoint (JDK 11 and later; JDK 8 does not know it).
	dcmdMetaspace: {name: "VM.metaspace", args: []string{"basic"}},
	dcmdCodeCache: {name: "Compiler.codecache"},
	dcmdNMT:       {name: "VM.native_memory", args: []string{"summary"}},
	// The performance counters, read from the JVM's shared memory without a
	// safepoint. Their output holds the command line and the system
	// properties, so it is never returned: perf.go reads the GC counters out
	// of it and drops the rest.
	dcmdPerf: {name: "PerfCounter.print"},
	// The thread dump, with the locked ownable synchronizers: a short stop of
	// the world, in proportion to the number of threads. It comes with its own
	// button, its own route and its own sentence, and is not sent from the
	// inspection.
	dcmdThreads: {name: "Thread.print", args: []string{"-l"}},
	// The class histogram: **a full GC**, and the application pauses while it
	// runs, in proportion to the heap. Sent on a confirmed click of its own
	// button and from nowhere else. Never with `-all`, which skips the GC and
	// counts the objects nothing refers to: the argv has no variable part.
	dcmdHistogram: {name: "GC.class_histogram"},
}

// inspection is the order the inspection runs them in: the version first, so
// an attach that is refused is found on the first one.
var inspection = []string{
	dcmdVersion, dcmdUptime, dcmdFlags, dcmdCommandLine, dcmdProperties,
	dcmdHeap, dcmdMetaspace, dcmdCodeCache, dcmdNMT, dcmdPerf,
}

// jcmd runs with the container's environment, and is itself a small JVM: it
// reads JAVA_TOOL_OPTIONS and JDK_JAVA_OPTIONS as the application did, says so
// on stderr, warns when they ask for native memory tracking, and would load a
// -javaagent they name. env unsets both first; an image with no env runs jcmd
// as is, and the answer says the options were inherited.
var withoutOptions = []string{"env", "-u", "JAVA_TOOL_OPTIONS", "-u", "JDK_JAVA_OPTIONS"}

// jcmdArgv is the argv that sends diagnostic command name to pid. Anything
// that is not in diagnostics is refused: this is the only way an argv with a
// command is built.
func jcmdArgv(plain bool, pid int, name string) ([]string, error) {
	d, ok := diagnostics[name]
	if !ok {
		return nil, fmt.Errorf("%q is not a command the Runtime Inspector sends", name)
	}
	if pid <= 0 {
		return nil, fmt.Errorf("a JVM's process id is a positive number, not %d", pid)
	}
	argv := prefix(plain)
	argv = append(argv, strconv.Itoa(pid), d.name)
	return append(argv, d.args...), nil
}

// listArgv lists the JVMs jcmd can see.
func listArgv(plain bool) []string { return append(prefix(plain), "-l") }

func prefix(plain bool) []string {
	if plain {
		return []string{"jcmd"}
	}
	return append(append([]string{}, withoutOptions...), "jcmd")
}

// The deadlines, tighter than Host.Exec's default: listing reads files, an
// attach may wait ten seconds for a JVM that does not answer, and every
// command of the inspection answers in well under a second on a JVM that does.
// The whole inspection has a budget of its own, so nine slow commands cannot
// hold a request for minutes.
const (
	listTimeout   = 10 * time.Second
	dcmdTimeout   = 15 * time.Second
	inspectBudget = 60 * time.Second
	revealBudget  = 20 * time.Second
	// threadsTimeout is Thread.print's: the safepoint is short, but writing
	// thousands of stacks through the attach socket is not instant.
	threadsTimeout = 30 * time.Second
	threadsBudget  = 45 * time.Second
	// histogramTimeout is GC.class_histogram's: a full GC of a large heap
	// takes seconds, and the answer is up to 4 MiB of text.
	histogramTimeout = 45 * time.Second
	histogramBudget  = 60 * time.Second
	maxProperties    = 1000
	maxFlags         = 500
	maxNMTRows       = 64
	maxListedJVMs    = 32
)

// runner runs jcmd in one container for one request.
type runner struct {
	h         extension.Host
	r         *http.Request
	cluster   string
	namespace string
	pod       string
	container string
	// plain is set once env was found missing: every later command runs jcmd
	// without it, and inherits the container's Java options.
	plain bool
}

// run runs a jcmd argv. With env missing from the image, it runs the same jcmd
// once more without it.
func (x *runner) run(argv func(plain bool) ([]string, error), timeout time.Duration) (extension.ExecResult, error) {
	cmd, err := argv(x.plain)
	if err != nil {
		return extension.ExecResult{}, err
	}
	res, err := x.exec(cmd, timeout)
	if err != nil && !x.plain && errors.Is(err, extension.ErrNoSuchCommand) {
		x.plain = true
		return x.run(argv, timeout)
	}
	return res, err
}

// exec is the one call to Host.Exec. A heap dump's cat and rm (heapdump.go)
// come through it too, with the argv fileArgv builds, and no env.
func (x *runner) exec(cmd []string, timeout time.Duration) (extension.ExecResult, error) {
	return x.h.Exec(x.r, x.cluster, extension.ExecQuery{
		Namespace: x.namespace, Pod: x.pod, Container: x.container, Command: cmd, Timeout: timeout,
	})
}

func (x *runner) list() (extension.ExecResult, error) {
	return x.run(func(plain bool) ([]string, error) { return listArgv(plain), nil }, listTimeout)
}

func (x *runner) dcmd(pid int, name string) (extension.ExecResult, error) {
	return x.dcmdWithin(pid, name, dcmdTimeout)
}

func (x *runner) dcmdWithin(pid int, name string, timeout time.Duration) (extension.ExecResult, error) {
	return x.run(func(plain bool) ([]string, error) { return jcmdArgv(plain, pid, name) }, timeout)
}

// missingJcmd is env's answer when it could not start jcmd: the program is not
// in the image, which env reports with 127 (126 when it is there and cannot be
// run).
func missingJcmd(res extension.ExecResult) bool {
	return res.ExitCode == 127 || res.ExitCode == 126
}

// --- reading what jcmd wrote ---------------------------------------------------

// listedJVM is one line of `jcmd -l`: the process id and its main class. The
// arguments after the main class are dropped - they may carry secrets - and
// never leave this package.
type listedJVM struct {
	PID       int    `json:"pid"`
	MainClass string `json:"mainClass"`
}

// parseList is the JVMs `jcmd -l` lists, jcmd's own line left out.
func parseList(stdout []byte) []listedJVM {
	var out []listedJVM
	for _, line := range lines(stdout) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		main := ""
		if len(fields) > 1 {
			main = fields[1]
		}
		if strings.HasSuffix(main, "sun.tools.jcmd.JCmd") {
			continue
		}
		switch {
		case main == "", strings.HasPrefix(main, "--"):
			// `-- process information unavailable`, or a JVM started with no
			// main class jcmd could read.
			main = "unknown main class"
		case strings.HasSuffix(main, ".jar"):
			// A jar's path: its name alone.
			main = main[strings.LastIndexByte(main, '/')+1:]
		}
		out = append(out, listedJVM{PID: pid, MainClass: textutil.Clip(main, 120)})
		if len(out) == maxListedJVMs {
			break
		}
	}
	return out
}

// lines splits output into lines without their line ends.
func lines(b []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), extension.ExecOutputLimit)
	for sc.Scan() {
		out = append(out, strings.TrimRight(sc.Text(), "\r"))
	}
	return out
}

// body is what a diagnostic command answered: the lines after the `<pid>:`
// line jcmd starts with. A line before it - a warning the jcmd JVM printed on
// stdout - is not part of the answer. With no such line, every line is.
func body(stdout []byte) []string {
	all := lines(stdout)
	for i, line := range all {
		if pidLine(line) {
			return all[i+1:]
		}
	}
	return all
}

func pidLine(line string) bool {
	head, ok := strings.CutSuffix(strings.TrimSpace(line), ":")
	if !ok || head == "" {
		return false
	}
	_, err := strconv.Atoi(head)
	return err == nil
}

// What jcmd writes when it could not reach the JVM, on either stream.
var attachRefusals = []string{"AttachNotSupportedException", "Unable to open socket file", "Operation not permitted"}

// attachDisabled is the JVM's own answer to an attach it was told to refuse
// (-XX:+DisableAttachMechanism): unlike a refusal from the system, it proves a
// JVM runs there, even in process 1 when jcmd lists none.
func attachDisabled(res extension.ExecResult) bool {
	for _, s := range [][]byte{res.Stdout, res.Stderr} {
		if bytes.Contains(s, []byte("does not support the attach mechanism")) {
			return true
		}
	}
	return false
}

func attachRefused(res extension.ExecResult) bool {
	for _, s := range [][]byte{res.Stdout, res.Stderr} {
		for _, marker := range attachRefusals {
			if bytes.Contains(s, []byte(marker)) {
				return true
			}
		}
	}
	return false
}

// unknownCommand is JDK 8's answer to a command it does not have.
func unknownCommand(res extension.ExecResult) bool {
	return bytes.Contains(res.Stdout, []byte("Unknown diagnostic command"))
}

// nmtOff is the answer of a JVM started without native memory tracking.
func nmtOff(res extension.ExecResult) bool {
	return bytes.Contains(res.Stdout, []byte("Native memory tracking is not enabled"))
}

// said is the first line a failed command wrote, stderr first, for a failure
// sentence; empty when it wrote nothing. Never parsed.
func said(res extension.ExecResult) string {
	for _, s := range [][]byte{res.Stderr, res.Stdout} {
		for _, line := range lines(s) {
			line = strings.TrimSpace(line)
			if line == "" || pidLine(line) || strings.HasPrefix(line, "Picked up ") {
				continue
			}
			return textutil.Clip(line, 300)
		}
	}
	return ""
}
