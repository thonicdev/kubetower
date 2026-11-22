package terminal

import (
	"fmt"
	"strings"
)

// mutatingVerbs are the verbs refused on a cluster declared read-only.
//
// This list and helmOnlyVerbs generate both the shell override and the table
// test, so the two cannot drift; the easy-to-miss verbs are also asserted as
// literal command lines in readonly_test.go, since a table cannot see a word in
// neither list.
//
// A guardrail, not a security boundary: the session's credentials can still
// write, and the binary can be called by its absolute path. The point is to
// prevent the unfortunate gesture on a sensitive cluster.
var mutatingVerbs = []string{
	"apply", "create", "delete", "edit", "patch", "replace",
	"scale", "autoscale", "rollout", "drain", "cordon", "uncordon",
	"taint", "annotate", "label", "set", "exec", "attach", "cp",
	"debug", "evict", "install", "upgrade", "uninstall", "rollback",
	"expose", "run", "kill",
	// Writes that are easy to miss:
	//
	//	"proxy"       `kubectl proxy &` opens a door to the API server on
	//	              localhost, authenticated as this session and asking nothing
	//	              of whoever walks through, so the next `curl -X DELETE` is a
	//	              write no word in this list would see.
	//	"reconcile"   `kubectl auth reconcile` writes RBAC; the Claude allowlist in
	//	              claude.go is narrowed to `auth can-i` for the same reason.
	//	"approve"     `kubectl certificate approve` issues a client certificate,
	//	"deny"        a credential for the cluster. Matched on the second word,
	//	              not on "certificate", which `kubectl get certificate` needs.
	"proxy", "reconcile", "approve", "deny",
	// And:
	//
	//	"port-forward"  opens a TCP path into a pod as this session, for
	//	                whatever is sent through it: `proxy`'s reasoning. The
	//	                console's Forward button is refused on a marked cluster too
	//	                (internal/forward, refuseOnReadOnlyClusters). The Prometheus
	//	                panel's tunnels are not this verb and are not refused.
	"port-forward",
}

// helmOnlyVerbs are writes in helm whose word is an ordinary noun elsewhere,
// so only helm's shim carries them. This narrows the "examine every argument"
// rule by binary, not by position (a position is what lets "kubectl --context
// prod delete ns X" through).
//
// `helm test` runs the chart's test hooks, creating pods. `test` is a common
// namespace name, and refusing `kubectl get pods -n test` would teach the
// operator the marking is noise; the residue is `helm list -n test`, refused.
//
// `del` and `un` because a denylist must know a tool's aliases: `helm
// uninstall` answers to `uninstall`, `del`, `delete` and `un`. Verified against
// the shipped binary: unlisted, `helm del <missing release>` reaches helm and
// answers `Error: uninstall: Release not loaded`. They are here and not in
// mutatingVerbs for `test`'s reason: plausible namespace names.
//
// install, upgrade and rollback have no aliases (checked). `list` and
// `history` have aliases (`ls`, `hist`) and are reads.
//
// A denylist over somebody else's CLI must be built from that CLI, not from its
// documentation.
var helmOnlyVerbs = []string{"test", "del", "un"}

// shimPluginScanPOSIX refuses a kubectl plugin under the fence. `kubectl foo
// bar` runs `kubectl-foo-bar` or `kubectl-foo` from PATH when `foo` is not a
// kubectl command, and a plugin's verbs are its own, so no verb list can cover
// them; an invocation resolving to a plugin is refused whatever it is called.
// `krew` is itself a plugin, so installing more is refused too.
//
// What kubectl resolves, measured (kubectl v1.35.9, with `kubectl-foo`,
// `kubectl-foo-bar` and `kubectl-baz_qux` on PATH):
//
//	kubectl foo x                -> kubectl-foo x
//	kubectl foo bar -n x         -> kubectl-foo-bar -n x
//	kubectl baz-qux              -> kubectl-baz_qux
//	kubectl --context x foo      -> Error: flags cannot be placed before plugin name
//	kubectl foo --context x bar  -> kubectl-foo --context x bar
//
// A plugin is named by the leading words up to the first flag, each `-` inside
// a word spelt `_`, joined by `-`. That is kubectl's own rule and the one place
// this file looks at position. The shim checks PATH for every prefix of those
// words.
//
// A built-in command with a `kubectl-get` beside it on PATH is refused too,
// though kubectl would run its own: a refusal naming the file can be acted on,
// and the alternative is a list of built-ins that changes every release.
//
// Not covered: `kubectl-foo` called by its own name, a different binary, as an
// absolute path is; and helm's plugins, which helm finds in its own directory.
//
// The assistant's override runs the same scan whatever KT_CLAUDE_READONLY says,
// with its own refusal (nosecrets.go): a plugin can read a Secret. Each sentence
// names the setting that would lift it, and none lifts the assistant's.
const shimPluginScanPOSIX = shimPluginScanHeadPOSIX + shimPluginRefuseReadOnlyPOSIX + shimPluginScanTailPOSIX

const shimPluginScanHeadPOSIX = `plugin=kubectl
for arg in "$@"; do
  case "$arg" in -*) break ;; esac
  plugin="$plugin-$(printf '%s' "$arg" | tr - _)"
  if command -v "$plugin" >/dev/null 2>&1; then
`

const shimPluginRefuseReadOnlyPOSIX = `    echo "kubetower: '$arg' runs the kubectl plugin $plugin, and a plugin (krew's or any other) can do anything: refused, this session is read-only." >&2
    echo "kubetower: ${KT_READONLY_WHY:-remove this cluster from KT_READONLY_CLUSTERS to allow it.}" >&2
`

const shimPluginScanTailPOSIX = `    exit 1
  fi
done
`

// pluginScanFor is the plugin half of an override, for the one binary that
// has plugins on PATH.
func pluginScanFor(binary string) string {
	if binary == "kubectl" {
		return shimPluginScanPOSIX
	}
	return ""
}

// verbsFor is the denylist one binary's override is generated from.
func verbsFor(binary string) []string {
	if binary == "helm" {
		return append(append([]string{}, mutatingVerbs...), helmOnlyVerbs...)
	}
	return mutatingVerbs
}

// shimHeadPOSIX opens every POSIX override. The write scan after it examines
// every argument, not a verb at a position: "the first non-flag argument" stops
// on "prod" in "kubectl --context prod delete ns X". A resource named like a
// verb may be refused, the right side of the trade: a needless refusal can be
// worked around, a deletion cannot be undone.
//
// The second line of a refusal comes from the session (KT_READONLY_WHY),
// because a cluster in KT_READONLY_CLUSTERS and a Claude session, restricted
// everywhere by default, are lifted by different settings.
//
// Three pieces rather than one script, since the assistant's override
// (nosecrets.go) needs the same head and tail around a second scan, sometimes
// without the first. readOnlyShimTemplate below is still one constant string,
// which fence.go matches on.
const shimHeadPOSIX = `#!/bin/sh
# Override placed by KubeTower: this session is restricted.
real="%s"
`

const shimWriteScanPOSIX = `for arg in "$@"; do
  case "$arg" in
    %s)
      echo "kubetower: '$arg' is refused, this session is read-only." >&2
      echo "kubetower: ${KT_READONLY_WHY:-remove this cluster from KT_READONLY_CLUSTERS to allow it.}" >&2
      exit 1 ;;
  esac
done
`

// shimSecretScanPOSIX is the read scan. Its sentence says the session does
// not read Secrets, not that it cannot. A `--raw` path is matched once decoded
// (kt_unescape, rawPathUnescapePOSIX in nosecrets.go), since client-go decodes
// it before the request is sent; one that does not decode is refused.
const shimSecretScanPOSIX = `kt_prev=
for arg in "$@"; do
  kt_check=$arg
  case "$kt_prev" in --raw) kt_raw=yes ;; *) kt_raw= ;; esac
  case "$arg" in --raw=*) kt_raw=yes ;; esac
  kt_prev=$arg
  if [ -n "$kt_raw" ]; then
    if ! kt_unescape "$arg"; then
      echo "kubetower: '$arg' is an API path that does not decode, and this session does not read Secrets." >&2
      echo "kubetower: ${KT_NO_SECRETS_WHY:-KubeTower withholds Secrets from an assistant session.}" >&2
      exit 1
    fi
    kt_check=$kt_dec
  fi
  case "$kt_check" in
    %s)
      echo "kubetower: '$arg' names a Secret, and this session does not read Secrets." >&2
      echo "kubetower: ${KT_NO_SECRETS_WHY:-KubeTower withholds Secrets from an assistant session.}" >&2
      exit 1 ;;
  esac
done
`

const shimTailPOSIX = `exec "$real" "$@"
`

// readOnlyShimTemplate is the override for a binary with no plugins on PATH
// (helm), and the part of kubectl's that fence.go's refusal sentence is in;
// buildReadOnlyShim adds the plugin scan for kubectl.
const readOnlyShimTemplate = shimHeadPOSIX + shimWriteScanPOSIX + shimTailPOSIX

// buildReadOnlyShim renders the override script for one binary. The binary is
// named because the denylists differ (helmOnlyVerbs); an unknown name gets the
// shared list, the safe default.
//
// The plugin scan is appended rather than formatted in: it carries a `%s` of
// its own for the shell's printf.
func buildReadOnlyShim(binary, realPath string) string {
	return fmt.Sprintf(shimHeadPOSIX, realPath) +
		fmt.Sprintf(shimWriteScanPOSIX, strings.Join(verbsFor(binary), "|")) +
		pluginScanFor(binary) + shimTailPOSIX
}

// --- the same rule for Windows -----------------------------------------------

// shimHeadCmd opens the override in cmd.exe's language.
//
// Windows resolves executables through PATHEXT, so the POSIX script in an
// extensionless `kubectl` is not executable there and the session would resolve
// straight to the real binary. The compiler and the text-level tests stay
// silent about that, which is why this guard is tested through the real shell.
//
// `.cmd` rather than `.ps1`: `.CMD` is in the default PATHEXT and `.PS1` is
// not, so a `.cmd` is found by PowerShell, cmd.exe and exec.LookPath, while a
// `.ps1` is found only by PowerShell and refused under AllSigned. A shim cannot
// pass `-ExecutionPolicy Bypass` to somebody else's shell.
//
// Batch traps:
//
//   - `for %%A in (%*)` cannot walk the arguments: it splits on commas and
//     semicolons, strips quotes and expands wildcards (the `*` in
//     `-o 'jsonpath={.items[*]}'` would become file names). A `shift` loop over
//     `%1` does none of that, and leaves `%*` intact for the real binary.
//   - `if /I`, not `if`: Windows is not case-sensitive and a denylist missing
//     `Delete` is a hole. This refuses more than the POSIX shim; the residue is
//     `kubectl Delete`, which kubectl would reject anyway.
//   - `%1`, not `%~1`, in the loop's end test. `%~1` strips quotes, so an
//     explicitly empty argument (`kubectl --context "" delete ns X`) would match
//     the end test and skip `delete`. With `%1`, absent gives `""==""` and empty
//     gives `""""==""`. The comparison on the next line uses `%~1`, where the
//     quotes must come off.
//
// Not covered: an argument containing a bare `"` makes `"%1"` malformed (and
// `"%~1"` too), a property of batch quoting; kubectl has no verb reachable that
// way, and the shim errors rather than passing it through.
//
// A `.cmd` that checks and then calls the real executable with `%*` is the
// shape of every npm-installed CLI on Windows.
//
// Literal batch with `@VERBS@` and `@REAL@` placeholders rather than a
// fmt.Sprintf format: batch is `%`-dense, and strings.NewReplacer does not care.
const shimHeadCmd = `@echo off
rem Override placed by KubeTower: this session is restricted.
rem Generated from verbsFor() and secretPatternsFor() in internal/terminal. Do not edit.
setlocal EnableExtensions DisableDelayedExpansion
set "ktlead=1"
set "ktplugin=kubectl"
set "ktraw="
:kubetower_scan
if "%1"=="" goto kubetower_run
`

const shimWriteScanCmd = `for %%V in (@VERBS@) do if /I "%~1"=="%%V" goto kubetower_refuse
`

// shimPluginScanCmd is shimPluginScanPOSIX in cmd.exe's language: while the
// words are still leading (no flag yet), the plugin name grows by one word with
// its `-` spelt `_`, and each PATHEXT extension is looked for on PATH with
// `%%~$PATH:`, cmd's own search, which spawns nothing. An empty argument ends
// the leading words, as a flag does.
const shimPluginScanCmd = `set "ktword=%~1"
if not defined ktword set "ktword=-"
if defined ktlead if "%ktword:~0,1%"=="-" set "ktlead="
if defined ktlead call set "ktplugin=%%ktplugin%%-%%ktword:-=_%%"
if defined ktlead for %%E in (%PATHEXT%) do for %%P in ("%ktplugin%%%E") do if not "%%~$PATH:P"=="" goto kubetower_refuse_plugin
`

const shimRefusePluginCmd = `:kubetower_refuse_plugin
>&2 echo kubetower: '%~1' runs the kubectl plugin %ktplugin%, and a plugin (krew's or any other) can do anything: refused, this session is read-only.
if defined KT_READONLY_WHY (>&2 echo kubetower: %KT_READONLY_WHY%) else (>&2 echo kubetower: remove this cluster from KT_READONLY_CLUSTERS to allow it.)
endlocal
exit /b 1
`

// pluginScanCmdFor is the Windows half of pluginScanFor: the scan inside the
// loop and its refusal block after it, both empty for a binary with no plugins
// on PATH.
func pluginScanCmdFor(binary string) (scan, refuse string) {
	if binary == "kubectl" {
		return shimPluginScanCmd, shimRefusePluginCmd
	}
	return "", ""
}

const shimScanEndCmd = `shift
goto kubetower_scan
`

const shimRefuseWriteCmd = `:kubetower_refuse
>&2 echo kubetower: '%~1' is refused, this session is read-only.
if defined KT_READONLY_WHY (>&2 echo kubetower: %KT_READONLY_WHY%) else (>&2 echo kubetower: remove this cluster from KT_READONLY_CLUSTERS to allow it.)
endlocal
exit /b 1
`

const shimRunCmd = `:kubetower_run
endlocal & "@REAL@" %*
exit /b %ERRORLEVEL%
`

// readOnlyShimCmdTemplate is the override for a binary with no plugins on PATH
// (helm); kubectl's adds pluginScanCmdFor's two pieces. Kept whole because the
// comments and the tests name the shape by it.
const readOnlyShimCmdTemplate = shimHeadCmd + shimWriteScanCmd + shimScanEndCmd +
	shimRefuseWriteCmd + shimRunCmd

// buildReadOnlyShimCmd renders the Windows override for one binary. Not behind
// `//go:build windows`: this is the half with syntax in it, and it must be
// testable on any host. Only the choice between the two templates is
// platform-gated (readonly_shimfile_windows.go).
func buildReadOnlyShimCmd(binary, realPath string) string {
	scan, refuse := pluginScanCmdFor(binary)
	return fillShimCmd(shimHeadCmd+shimWriteScanCmd+scan+shimScanEndCmd+shimRefuseWriteCmd+refuse+shimRunCmd,
		strings.Join(verbsFor(binary), " "), realPath)
}

// fillShimCmd puts the verb list and the real binary into a batch template.
func fillShimCmd(template, verbs, realPath string) string {
	return strings.NewReplacer("@VERBS@", verbs, "@REAL@", realPath).Replace(template)
}
