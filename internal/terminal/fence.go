package terminal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The read-only guardrail, measured rather than believed.
//
// writeReadOnlyShims puts an override named `kubectl` in a directory (a
// `#!/bin/sh` script, or a `.cmd` on Windows), sessionEnv and withShims prepend
// it to a session's PATH, and the logs, the drawer and the Settings Infos tab
// then say every Claude session is fenced. Nothing else asks the session which
// `kubectl` it actually resolves.
//
// writeReadOnlyShims continues when kubectl is not found at start-up, so a
// process that installed no override would still promise a fence.
// internal/binpath fixes the common cause (a macOS GUI launch's PATH), but a
// kubectl under asdf, mise or ~/bin is still found by a login shell and not by
// this process, and no list of directories ends that. A guardrail believed and
// absent is worse than one absent and known. So this runs the shell a session
// gets, with a read-only session's PATH, makes the override refuse something,
// and reports what came back on the page and in the pane.
//
// A login profile cannot shadow the override, measured in an Alpine container
// with a decoy kubectl prepended in `/etc/profile.d`, `~/.bash_profile` and
// `~/.bashrc`:
//
//	dock shell tab      bash --norc --noprofile -i        -> /ro-bin/kubectl
//	Claude launcher     bash --norc --noprofile -c        -> /ro-bin/kubectl
//	CONTROL: login      bash -l -c                        -> /brew/bin/kubectl
//	CONTROL: rc         bash -i                           -> /brew/bin/kubectl
//	no override written, read-only session                -> /usr/local/bin/kubectl
//
// because no session is a login shell. But that rests on two flags in the
// exported Options.Shell that nothing asserts; a probe also catches a login
// shell configured later, a missing override, PATHEXT on Windows, a withShims
// that appends, and an override on a noexec mount.
//
// This does not make the guardrail a boundary (readonly.go says why); it makes
// the console's claim match its behavior.

// Fence is the read-only guardrail as measured at start-up.
//
// Three states: a fence that could not be probed is not one known to be absent,
// and calling it missing would send the operator to fix a shim that is fine.
// Measured is false where no session can open at all (a Windows without
// ConPTY, older than 10 1809) and on the cmd.exe shell family, for which no
// checked probe exists. See measureReadOnlyFence.
type Fence struct {
	// Measured is whether the probe ran to a conclusion.
	Measured bool
	// Intact is whether every binary the guardrail covers is behind it.
	Intact bool
	// Detail is one operator-facing sentence, set in all three states: what was
	// refused, what was not, or why nothing could be asked.
	Detail string
}

// State is the word the Settings Infos tab renders, kept here so the three
// states have one spelling.
func (f Fence) State() string {
	switch {
	case !f.Measured:
		return "unmeasured"
	case f.Intact:
		return "intact"
	default:
		return "absent"
	}
}

// ReadOnlyFence reports what the guardrail actually is, for the page that
// tells the operator about it. Two strings rather than the struct, so
// internal/web can declare what it needs without importing this package.
func (t *Terminal) ReadOnlyFence() (state, detail string) {
	return t.fence.State(), t.fence.Detail
}

// fenceBinaries is what a session's guardrail covers: the names
// writeReadOnlyShims writes, deliberately not a shared list, or the probe would
// agree with the writer by construction instead of asking the session.
var fenceBinaries = []string{"kubectl", "helm"}

// fenceRefusal is the part of the override's refusal the probe looks for. A
// substring of readOnlyShimTemplate, held to it by
// TestTheFenceLooksForWhatTheShimPrints: a marker that drifted would report
// every intact fence as a hole.
const fenceRefusal = "is refused, this session is read-only"

// fenceProbeArgs is the command line the probe runs, per binary:
// `kubectl delete --help`.
//
// `delete` is denied, so an override in front refuses and exits non-zero with
// its sentence: the guarantee itself, executed. `command -v` alone would miss an
// override whose interpreter is missing or whose content is a half-finished
// write. (A noexec mount is caught by both: PATH search skips the entry and
// `command -v` names the real binary.)
//
// `--help` makes it safe: without the override, the real kubectl prints help
// for `delete`, opening no connection and reading no kubeconfig. Verified in a
// container against Alpine's kubectl and helm: with the override, exit 1 and the
// refusal; without, exit 0 and help text.
func fenceProbeArgs(binary string) []string {
	if binary == "helm" {
		// `uninstall` rather than `delete`: both are on helm's list (helmOnlyVerbs),
		// and `uninstall` is the documented name, so it survives an alias being
		// dropped.
		return []string{"helm", "uninstall", "--help"}
	}
	return []string{binary, "delete", "--help"}
}

// measureFence asks a session's own shell which kubectl and helm it resolves,
// and makes each of them refuse. run is injected so the classification is
// tested without kubectl, helm or a POSIX shell on the test machine; the real
// runner is fenceRun.
func measureFence(shell []string, roBinDir, path string, run func(argv, env []string) (string, error)) Fence {
	if roBinDir == "" {
		// writeReadOnlyShims has not run. New() fails on that, so only a zero-valued
		// Terminal in a test gets here, and it must not report a fence it never had.
		return Fence{Detail: "the override directory was never created, so nothing could be probed."}
	}

	var holes []string
	answered := 0
	for _, name := range fenceBinaries {
		out, err := run(fenceProbeArgs(name), fenceProbeEnv(roBinDir, path))
		if err != nil && out == "" {
			// The shell itself would not start. Unmeasured, not absent.
			return Fence{Detail: fmt.Sprintf(
				"the guardrail could not be probed: %v. It may or may not be in front of kubectl and helm.", err)}
		}
		if strings.Contains(out, fenceRefusal) || fenceResolved(out) != "" {
			answered++
		}
		if hole := classifyFenceProbe(name, roBinDir, out); hole != "" {
			holes = append(holes, hole)
		}
	}

	// Nothing resolved is not a fence that held. One missing binary is nothing to
	// fence (an image without helm), but a probe that resolved neither may simply
	// not see them, as on Windows without PATHEXT (fenceProbeMachineVars), while a
	// session's shell does. Reported intact, it would be a guardrail believed and
	// absent.
	if answered == 0 && len(holes) == 0 {
		return Fence{Detail: "the probe resolved neither kubectl nor helm in a read-only session's shell, " +
			"so nothing refused anything: either neither is installed, or the probe could not see them. " +
			"Whether a session's kubectl is behind the override was not established."}
	}

	if len(holes) > 0 {
		return Fence{
			Measured: true,
			Detail:   strings.Join(holes, " "),
		}
	}
	return Fence{
		Measured: true,
		Intact:   true,
		Detail: fmt.Sprintf(
			"measured at start-up: a read-only session's kubectl and helm are the overrides in %s, and each refused a mutating verb when asked.",
			roBinDir),
	}
}

// classifyFenceProbe reads one probe's output and returns the sentence for a
// hole, or "" when there is none.
//
// A binary that does not resolve is not a hole: a session without helm runs no
// helm, and reporting it would flag every image without helm. A hole is a
// binary that resolves and then does not refuse. (Neither resolving is handled
// by measureFence as unmeasured.)
func classifyFenceProbe(name, roBinDir, out string) string {
	if strings.Contains(out, fenceRefusal) {
		return ""
	}

	resolved := fenceResolved(out)
	if resolved == "" {
		// Nothing to fence, and not reported: "helm is not fenced" on an image without
		// helm sends the operator looking for a defect.
		return ""
	}

	// shimFileName, not name: the override is `kubectl.cmd` on Windows, where an
	// extensionless file is not executable, and stat-ing the POSIX spelling would
	// report every correct Windows override as missing.
	want := filepath.Join(roBinDir, shimFileName(name))
	if _, err := os.Stat(want); errors.Is(err, os.ErrNotExist) {
		return fmt.Sprintf(
			"%s is NOT behind the read-only overrides: none was installed (it was not on this process's PATH at start-up), "+
				"and a session resolves %s instead.", name, resolved)
	}
	return fmt.Sprintf(
		"%s is NOT behind the read-only overrides: a session resolves %s rather than %s, "+
			"or the override did not refuse when it ran.", name, resolved, want)
}

// fenceResolvedPrefix marks the line the probe prints `command -v` on, since
// the real binary's help text shares the stream and a shell may print a
// job-control line first.
const fenceResolvedPrefix = "kubetower-fence-resolved="

func fenceResolved(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, fenceResolvedPrefix); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// fenceProbeEnv is the whole environment the probe runs in, built from
// nothing: a second guard on its safety. Besides `--help`, a KUBECONFIG naming
// a missing file leaves even a wrapper script no cluster to reach, and nothing
// inherited means no AWS credentials or service account either.
//
// PATH is exactly a read-only session's: the override directory prepended, as
// sessionEnv and withShims do. Any other PATH would measure something no
// session runs.
func fenceProbeEnv(roBinDir, path string) []string {
	return append([]string{
		"PATH=" + withShims(roBinDir, path),
		"KUBECONFIG=" + filepath.Join(roBinDir, "kubetower-fence-probe-no-such-kubeconfig"),
		"HOME=" + roBinDir,
		"TERM=dumb",
	}, machineEnv()...)
}

// fenceProbeMachineVars are the variables that describe the machine rather
// than the operator, passed through into the probe's environment. None is a
// credential or a path to one.
//
// Without them Windows breaks the probe: with PATHEXT absent, PowerShell does
// not consider `.cmd` at all, so both the override and the real binary are
// invisible, and answers
//
//	kubetower-fence-resolved=
//	The term 'kubectl' is not recognized as a name of a cmdlet, function,
//	script file, or executable program.
//
// That is an empty answer passing for a result, since "nothing resolved" is
// "nothing to fence". Linux and macOS define none of these names, so the
// container's probe, the one with a verified result, is unaffected.
var fenceProbeMachineVars = []string{
	// The variable Windows resolution of a `.cmd` depends on.
	"PATHEXT",
	// Running a `.cmd` means running cmd.exe, found through ComSpec, which needs
	// SystemRoot to load.
	"ComSpec",
	"SystemRoot",
	"SystemDrive",
	"windir",
	// PowerShell's start-up writes module analysis and transcript state to a temp
	// directory, and without one logs warnings into the probe's output.
	"TEMP",
	"TMP",
}

// machineEnv is those of fenceProbeMachineVars this machine sets. Only those:
// `PATHEXT=` would be an explicit empty list, worse than none.
func machineEnv() []string {
	var out []string
	for _, name := range fenceProbeMachineVars {
		if value, ok := os.LookupEnv(name); ok {
			out = append(out, name+"="+value)
		}
	}
	return out
}

// fenceProbeTimeout bounds one probe: paid once at start-up, and short enough
// that a wrapper script waiting on a prompt cannot hold start-up open.
const fenceProbeTimeout = 15 * time.Second

// fenceRun is the real execution: the session's own shell, one probe.
//
// The shell is the configured one without its interactive flag, claudeArgv's
// shape; the shell tab differs only by `-i`. Not a bare `sh -c`: the question is
// whether this console's shell resolves the override.
// TestTheSessionShellReadsNoInitFile covers the `-i` form against a profile
// that prepends.
func fenceRun(shell []string) func(argv, env []string) (string, error) {
	return func(argv, env []string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), fenceProbeTimeout)
		defer cancel()

		script := fenceScript(shellFamilyOf(shell), argv)

		// scriptArgv, not claudeSessionArgv: this must terminate, and the PowerShell
		// session argv carries `-NoExit`.
		full := scriptArgv(shell, script)
		cmd := exec.CommandContext(ctx, full[0], full[1:]...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		// A non-zero exit is the expected answer when the override refuses; the error
		// matters only with no output at all, which is a shell that would not start.
		if len(out) == 0 && err != nil {
			return "", err
		}
		return string(out), nil
	}
}

// fenceScript is the probe, in the session shell's own language: print where
// the shell resolves this binary, marked by fenceResolvedPrefix, then run a
// word the override refuses (see fenceProbeArgs for why that is safe).
//
// In the shell's own language because PATHEXT, an execution policy and a
// profile re-pointing $env:PATH can each make PowerShell resolve differently
// from `sh`.
//
// The cmd.exe family is refused upstream, in measureReadOnlyFence: a `where`
// and `for /f` batch would look right and report the wrong thing, and an
// unchecked measurement is worse than "unmeasured".
func fenceScript(family shellFamily, argv []string) string {
	if family == familyPowerShell {
		// Get-Command rather than where.exe: it is PowerShell's own resolution.
		// Filtered to an application because an alias or function named `kubectl`
		// (which a profile can define) has no Source, and would read as "nothing to
		// fence" instead of a hole.
		//
		// `2>&1` because the override writes its refusal to stderr, and Out-String
		// because PowerShell passes error records down the pipeline as objects.
		return "$ErrorActionPreference = 'Continue'\n" +
			"$kt = Get-Command -Name '" + psEscapeSingle(argv[0]) +
			"' -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1\n" +
			"Write-Output ('" + fenceResolvedPrefix + "' + $kt.Source)\n" +
			"& " + psArgList(argv) + " 2>&1 | Out-String\n"
	}
	return fmt.Sprintf("printf '%s%%s\\n' \"$(command -v %s 2>/dev/null)\"\n%s 2>&1\n",
		fenceResolvedPrefix, argv[0], strings.Join(argv, " "))
}

// psArgList renders a command line as PowerShell single-quoted literals, one
// per word: `& 'kubectl delete --help'` would look for an executable with
// spaces in its name. The values are this package's constants, so the quoting
// is belt and braces.
func psArgList(argv []string) string {
	quoted := make([]string, 0, len(argv))
	for _, a := range argv {
		quoted = append(quoted, psQuote(a))
	}
	return strings.Join(quoted, " ")
}

// fenceNotice is what a session marked read-only is told when the guardrail is
// not there: in the pane, where the person opening the tab sees it.
// stream.notice survives where the Claude banner does not, as Claude Code's
// alternate screen takes the banner with it.
func fenceNotice(f Fence) string {
	switch f.State() {
	case "absent":
		return "KubeTower: this session is marked read-only, but the guardrail is NOT in place. " + f.Detail +
			" Commands that change the cluster will run."
	case "unmeasured":
		return "KubeTower: this session is marked read-only, and KubeTower could not establish that the guardrail is in place. " + f.Detail
	}
	return ""
}
