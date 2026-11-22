package terminal

// Which shell a session lands in, and the three things that differ per shell.
//
// The shell itself decides, not runtime.GOOS: Options.Shell is configurable,
// so Windows may be told to run bash. Three families answer three of this
// package's questions differently:
//
//	                  the prompt            a script          no init files
//	POSIX (sh/bash)   $PS1 in the env       -c <script>       --norc --noprofile
//	PowerShell        function prompt {}    -Command <s>      -NoProfile
//	cmd.exe           %PROMPT% in the env   /C <script>       /D (skips AutoRun)
//
// The prompt matters most: it is what says which cluster the commands will hit,
// and `PS1` means nothing to PowerShell.
//
// Everything else is shared (sessionEnv, the kubeconfig writer, the AWS home,
// the session limit, the WebSocket protocol); the families differ only here
// and in readonly.go's two shim templates, so a change to what a session gets is
// made once.

import (
	"path/filepath"
	"strings"
)

// shellFamily is which of the three a configured shell belongs to. POSIX is
// the zero value: the container's shell, and what a test's sessionSpec with no
// shell should mean.
type shellFamily int

const (
	familyPOSIX shellFamily = iota
	familyPowerShell
	familyCmd
)

// shellFamilyOf reads the family off argv[0]'s base name, lower-cased, since
// Windows spells executables however the caller typed them. Anything
// unrecognized is POSIX: wrong that way, the prompt does not render, but the
// session still starts.
func shellFamilyOf(shell []string) shellFamily {
	if len(shell) == 0 {
		return familyPOSIX
	}
	switch strings.ToLower(strings.TrimSuffix(filepath.Base(shell[0]), ".exe")) {
	case "pwsh", "powershell":
		return familyPowerShell
	case "cmd":
		return familyCmd
	}
	return familyPOSIX
}

// --- the interactive session's command line ----------------------------------

// sessionArgv is the command line for the shell the operator lands in. The
// prompt arrives by a different route per family:
//
//   - POSIX takes it from the environment (`PS1`, set by sessionEnv), so the
//     configured shell is used untouched;
//   - PowerShell's `prompt` is a function, so it must be defined by running
//     something, and `-NoExit -Command <definition>` both defines it and stays
//     interactive;
//   - cmd.exe reads `PROMPT` from the environment, like `PS1`.
func sessionArgv(shell []string, cluster string, readOnly bool) []string {
	if shellFamilyOf(shell) != familyPowerShell {
		return shell
	}
	return append(append([]string{}, shell...),
		"-NoExit", "-Command", powerShellPrompt(cluster, readOnly))
}

// scriptArgv hands a script to the configured shell and lets it exit. Its own
// name because claudeSessionArgv must stay open afterwards, while the fence
// probe (fence.go) must terminate.
func scriptArgv(shell []string, script string) []string {
	switch shellFamilyOf(shell) {
	case familyPowerShell:
		return append(append([]string{}, shell...), "-Command", script)
	case familyCmd:
		return append(append([]string{}, shell...), "/C", script)
	}
	return posixScriptArgv(shell, script)
}

// posixScriptArgv runs a script in a POSIX shell: the configured binary and
// flags, minus the interactive flag, plus `-c` and the script. Derived from
// Options.Shell rather than hard-coded.
func posixScriptArgv(shell []string, script string) []string {
	argv := []string{shell[0]}
	for _, flag := range shell[1:] {
		if flag == "-i" || flag == "--interactive" {
			continue
		}
		argv = append(argv, flag)
	}
	return append(argv, "-c", script)
}

// --- the prompt --------------------------------------------------------------

// powerShellPrompt is the definition of a `prompt` function that says which
// cluster this window is pointed at.
//
//   - The cluster name is a single-quoted literal in a variable, not
//     interpolated into the double-quoted string: a context name is somebody
//     else's data, and PowerShell expands `$` and backticks in double quotes.
//     psEscapeSingle (localterm.go) does the doubling.
//   - `$([char]27)`, not a literal ESC byte, since the string goes through
//     ComposeCommandLine.
//   - `$ExecutionContext.SessionState.Path.CurrentLocation` rather than
//     Get-Location, which would run a cmdlet on every redraw.
func powerShellPrompt(cluster string, readOnly bool) string {
	tag, colour := promptTag(cluster, readOnly)
	return "$global:KubeTowerCluster = '" + psEscapeSingle(tag) + "'; " +
		"function global:prompt { \"$([char]27)[" + colour +
		"m[$global:KubeTowerCluster]$([char]27)[0m $($ExecutionContext.SessionState.Path.CurrentLocation) $ \" }"
}

// buildCmdPrompt is the same in cmd.exe's prompt language, where `$E` is ESC,
// `$P` the working directory and `$G` a `>`. A `$` is stripped from the tag
// because cmd has no escape for it, and `$P` in a name would print the working
// directory.
//
// The least verified path here: resolveDefaultShell reaches cmd.exe only on a
// Windows with neither pwsh.exe nor powershell.exe, and none has been tried.
func buildCmdPrompt(cluster string, readOnly bool) string {
	tag, colour := promptTag(cluster, readOnly)
	return "$E[" + colour + "m[" + strings.ReplaceAll(tag, "$", "") + "]$E[0m $P$G "
}

// promptTag is what goes in the brackets and its color, shared by the three
// families: yellow when read-only, otherwise cyan, and the name shortened since
// an EKS context is a full ARN.
//
// Reduced to characters no prompt language reads as syntax: bash runs `$(…)`
// and backticks in PS1 at every prompt, so a context named `prod-$(touch x)`
// would run `touch`. Escaping would take three languages; the tag keeps letters,
// digits and `. _ @ : -` and replaces the rest with `_`. KT_CLUSTER and the
// kubeconfig carry the exact name.
func promptTag(cluster string, readOnly bool) (tag, colour string) {
	short := promptSafeName(shortCluster(cluster))
	if readOnly {
		return short + " ro", "33"
	}
	return short, "36"
}

// promptSafeName is the cluster's name with everything outside
// `[A-Za-z0-9._@:-]` replaced by `_`. `_` itself is kept because GKE contexts are
// `gke_<project>_<zone>_<name>`.
func promptSafeName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case strings.ContainsRune("._@:-", r):
			return r
		}
		return '_'
	}, name)
}
