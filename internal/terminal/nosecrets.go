package terminal

import (
	"fmt"
	"strings"
)

// Secrets are not read from an assistant session.
//
// claude.go refuses a session addressed to a Secret; this covers a session
// opened on a Pod that then types `kubectl get secret -o yaml`.
//
// # A guardrail, not a security boundary
//
// As readonly.go says of the mutating-verb overrides: the session's credentials
// can still read Secrets, and nothing stops calling the binary by its absolute
// path, `curl` against the API server with the kubeconfig's token, or a program
// of its own. This makes the well-behaved case unable to reach a Secret by
// accident and the determined case deliberate. Only a credential that cannot
// read Secrets, RBAC at the API server, cannot be worked around; that is a
// cluster-side change.
//
// # The denylist is built from the binary, not from memory
//
// There are more ways to read a Secret than to write one (helmOnlyVerbs shows
// how aliases get missed), so the set below comes from asking kubectl:
//
//	$ kubectl api-resources --api-group=''
//	NAME        SHORTNAMES   APIVERSION   NAMESPACED   KIND
//	secrets                  v1           true         Secret
//
// `secrets` has no short name: `kubectl get se` and `kubectl get sec` both
// answer `error: the server doesn't have a resource type "se"` on a live API
// server. The spellings it accepts, each tried against that server:
//
//	secret        accepted        se     rejected
//	secrets       accepted        sec    rejected
//	Secret        accepted
//	SECRET        accepted
//	Secrets       accepted
//	secret.       accepted
//	secrets.      accepted
//	secret.v1.    accepted
//	secrets.v1.   accepted
//
// kubectl matches a resource name case-insensitively and POSIX `case` does
// not, so every letter is a bracketed pair.
//
// # What is covered
//
// Asserted as literal command lines in nosecrets_test.go:
//
//   - `get secret`, `get secrets`, and either with a name after it
//   - `secret/<name>` and `secrets/<name>`, the slash form
//   - `secret.v1.`, `secrets.v1.` and any group-qualified spelling
//   - a comma list naming a Secret in any position (`secrets,configmaps`,
//     `pods,secret`, `configmap/a,secret/b,pod/c`, `pods,secrets.v1.`)
//   - any capitalization of all of the above
//   - `describe`, and every output flag (`-o yaml`, `-o json`, `-o jsonpath`,
//     `-o go-template`, `--template`), since the resource word is a separate
//     argument and is what is matched
//   - `-A` and `--all-namespaces`, for the same reason
//   - `--raw` against an API path containing `/secrets`, as `--raw <path>` or
//     `--raw=<path>`, matched once percent-decoded (`%73ecrets`, `x%2Fsecrets`,
//     double encoding); a `--raw` path that does not decode is refused
//     (rawPathUnescapePOSIX). On Windows every `--raw` path holding a `%` is
//     refused instead (shimSecretScanCmd): cmd.exe cannot decode one without
//     delayed expansion, which the scan keeps off
//   - `helm get manifest|values|all|hooks`, which print a release's rendered
//     Secrets and the values that produced them, and `helm get notes` and
//     `helm status`, which print the notes charts write generated passwords in
//   - any kubectl plugin (`kubectl view-secret x`, a two-word plugin, `kubectl
//     krew install view-secret`), whether or not the session may write, since a
//     plugin can read a Secret by a door no pattern names (see
//     shimPluginRefuseSecretPOSIX). Only through `kubectl`:
//     `kubectl-view_secret x` by its own name is another binary (readonly.go,
//     "Not covered")
//
// `kubectl exec` into a pod to `cat /var/run/secrets/...` is covered only in a
// read-only session, where `exec` is a mutating verb. With
// KT_CLAUDE_READONLY=false the assistant keeps `exec`, `attach`, `cp`, `debug`,
// `run`, `port-forward` and `proxy`, and can read what is mounted in a pod.
//
// # What is not covered
//
//   - `-f <manifest>`: `kubectl get -f secret.yaml` names the Secret inside a
//     file this shim does not open.
//   - A Secret reached by an API path with no `/secrets` in it once decoded:
//     none in core Kubernetes, but a proxy or aggregated API server could
//     present one.
//   - Anything that is not kubectl or helm: `curl`, `wget`, a script, the token
//     in the session's kubeconfig.
//   - The absolute path: `/usr/bin/kubectl get secret` reaches the real binary.
//   - The start-up fence measures `ro-bin` only (fence.go). `assistant-bin` and
//     `assistant-ro-bin` are written by the same function, under the same root,
//     with the same names and modes: strong evidence, not a measurement.
//   - A ServiceAccount's token minted into a Secret is covered as a Secret;
//     `kubectl create token` is refused as a mutation, not as a read.

// secretReadPatterns are the argument shapes that name a Secret, as POSIX
// `case` patterns, every letter a bracketed pair (kubectl is case-insensitive,
// `case` is not).
var secretReadPatterns = []string{
	// `secret` and `secrets`, bare.
	`[Ss][Ee][Cc][Rr][Ee][Tt]`,
	`[Ss][Ee][Cc][Rr][Ee][Tt][Ss]`,
	// `secret/<name>`, one object in one argument.
	`[Ss][Ee][Cc][Rr][Ee][Tt]/*`,
	`[Ss][Ee][Cc][Rr][Ee][Tt][Ss]/*`,
	// `secrets.v1.` and every other group-qualified spelling. `secret.*`, not
	// `secret*`, which would also take `secretstore`, another custom resource.
	`[Ss][Ee][Cc][Rr][Ee][Tt].*`,
	`[Ss][Ee][Cc][Rr][Ee][Tt][Ss].*`,
	// A comma list (`secrets,configmaps`, `pods,secret/api-tls`): kubectl reads
	// several resource types from one argument, and the patterns above anchor at
	// the argument's start or behind a `/`. The first element is those shapes plus a
	// comma; a later one is a comma plus them, at the end or followed by a comma,
	// `/` or `.`. Never `,secret*`: `pods,secretstores` is another resource.
	`[Ss][Ee][Cc][Rr][Ee][Tt],*`,
	`[Ss][Ee][Cc][Rr][Ee][Tt][Ss],*`,
	`*,[Ss][Ee][Cc][Rr][Ee][Tt]`,
	`*,[Ss][Ee][Cc][Rr][Ee][Tt][Ss]`,
	`*,[Ss][Ee][Cc][Rr][Ee][Tt],*`,
	`*,[Ss][Ee][Cc][Rr][Ee][Tt][Ss],*`,
	`*,[Ss][Ee][Cc][Rr][Ee][Tt]/*`,
	`*,[Ss][Ee][Cc][Rr][Ee][Tt][Ss]/*`,
	`*,[Ss][Ee][Cc][Rr][Ee][Tt].*`,
	`*,[Ss][Ee][Cc][Rr][Ee][Tt][Ss].*`,
	// `--raw /api/v1/namespaces/x/secrets`, and `--raw=<path>` as one argument.
	// `?*` is one character then anything, covering `/secrets/<name>` and
	// `/secrets?limit=1`.
	`*/[Ss][Ee][Cc][Rr][Ee][Tt][Ss]`,
	`*/[Ss][Ee][Cc][Rr][Ee][Tt][Ss]?*`,
}

// helmSecretSubcommands are helm's own reads that print a release's Secrets:
// `manifest` and `all` render the release's objects, Secrets with their data;
// `values` and `hooks` are the same material by another door. Helm keeps each
// release in a Secret too.
//
// Helm-only, as with helmOnlyVerbs: these are ordinary words elsewhere, and
// refusing `kubectl get all` would teach the operator to ignore markings.
//
// `notes` and `status` print the release's NOTES.txt, where charts write the
// passwords they generated; the console shows notes only behind a recorded
// reveal (apireads_helm.go). `helm status` is refused outright, not "unless
// --hide-notes": `-o json|yaml` carries the notes as `info.notes` whatever
// that flag does.
//
// `helm show values|all <chart>` read a chart's defaults and hold no cluster
// secret; they are refused anyway, the accepted side of the trade: a needless
// refusal can be worked round, a leaked credential cannot be recalled. So is
// `helm status`, for the same reason.
var helmSecretSubcommands = []string{"manifest", "values", "all", "hooks", "notes", "status"}

// rawPathUnescapePOSIX defines kt_unescape, the shell's percent-decoding of a
// `--raw` path. client-go parses the path as a URL and sends the decoded
// path's escaping (rest/request.go), so `/api/v1/namespaces/x/%73ecrets` asks
// for Secrets while naming none as typed. kt_unescape sets kt_dec to the path
// decoded, and fails on a `%` not followed by two hex digits, as
// url.PathUnescape does.
//
// It decodes until no `%` is left, so `%2573ecrets` is matched as `secrets`
// too: a decoding further along (a proxy, an aggregated API server) must not
// turn a path the scan let through into one it would have refused. Each pass
// shortens the path, so the loop ends.
//
// Pure shell, no process per argument: a `case` table, generated by
// unescapeTablePOSIX. Only the bytes the Secret patterns can depend on decode
// to themselves (letters, `/`, `,`, `.`, and `%` for the next pass); every
// other byte becomes `_`, which no pattern needs.
//
// The path is not cleaned (path.Clean): cleaning only removes segments, so a
// path that names `secrets` once cleaned already holds `/secrets`, which the
// patterns match anywhere; the cost is refusing `.../secrets/..`, the side a
// guardrail may err on.
func rawPathUnescapePOSIX() string {
	return `kt_unescape() {
  kt_in=$1
  while :; do
    case "$kt_in" in *%*) ;; *) kt_dec=$kt_in; return 0 ;; esac
    kt_out=
    while [ -n "$kt_in" ]; do
      case "$kt_in" in
        %[0-9A-Fa-f][0-9A-Fa-f]*)
          kt_hex=${kt_in#%}
          kt_hex=${kt_hex%"${kt_hex#??}"}
          kt_in=${kt_in#%??}
          case "$kt_hex" in
` + unescapeTablePOSIX() + `            *) kt_out="${kt_out}_" ;;
          esac ;;
        %*) return 1 ;;
        *)
          kt_rest=${kt_in#?}
          kt_out=$kt_out${kt_in%"$kt_rest"}
          kt_in=$kt_rest ;;
      esac
    done
    kt_in=$kt_out
  done
}
`
}

// unescapeTablePOSIX is kt_unescape's `case` arms: one per byte that decodes
// to itself, its two hex digits matched in either case (`2f` and `2F`).
func unescapeTablePOSIX() string {
	var b strings.Builder
	keep := "%,./"
	for c := 'A'; c <= 'Z'; c++ {
		keep += string(c)
	}
	for c := 'a'; c <= 'z'; c++ {
		keep += string(c)
	}
	for _, c := range keep {
		b.WriteString("            ")
		for _, h := range fmt.Sprintf("%02X", c) {
			if h >= 'A' && h <= 'F' {
				fmt.Fprintf(&b, "[%c%c]", h, h+'a'-'A')
			} else {
				b.WriteRune(h)
			}
		}
		fmt.Fprintf(&b, ") kt_out=\"${kt_out}%c\" ;;\n", c)
	}
	return b.String()
}

// secretScanFor is the assistant's read scan for one binary: kt_unescape and
// the scan that calls it.
func secretScanFor(binary string) string {
	return rawPathUnescapePOSIX() + fmt.Sprintf(shimSecretScanPOSIX, strings.Join(secretPatternsFor(binary), "|"))
}

// secretPatternsFor is the read denylist one binary's override is generated
// from, shaped like verbsFor for writes.
func secretPatternsFor(binary string) []string {
	if binary == "helm" {
		return append(append([]string{}, secretReadPatterns...), helmSecretSubcommands...)
	}
	return secretReadPatterns
}

// --- the overrides an assistant session gets --------------------------------

// shimSecretScanCmd is the Windows half of the read scan, in cmd.exe's
// language. It cannot be a word list like the write scan: `for ... if /I`
// compares whole strings and cannot match `secret/api-tls`, `secrets.v1.` or an
// API path. Three mechanisms instead, none spawning a process (a `findstr` per
// argument would be ten processes per kubectl line):
//
//   - `%ktarg:/secrets=%`, cmd's case-insensitive substring replacement: if
//     removing `/secrets` changes the argument, it contained it (the `--raw`
//     case);
//   - `for /f "delims=/."`, which keeps the argument up to its first `/` or
//     `.`, so `secret/api-tls` and `secrets.v1.` reduce to the compared word;
//   - `if /I`, for kubectl's case-insensitivity.
//
// cmd.exe splits `%1` on commas as well as spaces, so an unquoted comma list
// arrives as separate arguments. A quoted one does not: `,` is also a `for /f`
// delimiter, reducing the first element, and `%ktarg:,secret=%` catches a later
// one. That is coarser than POSIX (a quoted `pods,secretstores` is refused
// here), the side a guardrail may err on.
//
// Delayed expansion stays off, hence `call set` rather than `!ktarg!`: with it
// on, a `!` in an argument is eaten, and `-l 'tier!=db'` would reach the API
// server as a different selector.
//
// **A `--raw` path holding a `%` is refused, whatever it decodes to.** client-go
// percent-decodes the path before sending it, so `%73ecrets` asks for Secrets
// while naming none as typed. The POSIX scan decodes it (rawPathUnescapePOSIX);
// cmd.exe cannot, since `%` is its own expansion character and cannot be a
// substring to replace without delayed expansion. `for /f "delims=%"` finds one
// instead: the argument, wrapped in `x` so its first token is never empty,
// differs from that token exactly when it holds a `%`. ktraw carries `--raw`
// to the next argument, the path of `--raw <path>`. The cost: an encoded path
// that asks for no Secret (`%70ods`) is refused too, the side a guardrail may
// err on.
const shimSecretScanCmd = `@HELMWORDS@set "ktarg=%~1"
set "ktpct="
for /f "tokens=1 delims=%%" %%P in ("x%ktarg%x") do if not "%%P"=="x%ktarg%x" set "ktpct=1"
if defined ktpct if defined ktraw goto kubetower_refuse_encoded
if defined ktpct if /I "%ktarg:~0,6%"=="--raw=" goto kubetower_refuse_encoded
set "ktraw="
if /I "%ktarg%"=="--raw" set "ktraw=1"
call set "ktstrip=%%ktarg:/secrets=%%"
if /I not "%ktstrip%"=="%ktarg%" goto kubetower_refuse_secret
call set "ktstrip=%%ktarg:,secret=%%"
if /I not "%ktstrip%"=="%ktarg%" goto kubetower_refuse_secret
for /f "tokens=1 delims=/.," %%H in ("%ktarg%") do if /I "%%H"=="secret" goto kubetower_refuse_secret
for /f "tokens=1 delims=/.," %%H in ("%ktarg%") do if /I "%%H"=="secrets" goto kubetower_refuse_secret
`

const shimHelmSecretWordsCmd = `for %%V in (@HELMSECRETS@) do if /I "%~1"=="%%V" goto kubetower_refuse_secret
`

const shimRefuseSecretCmd = `:kubetower_refuse_encoded
>&2 echo kubetower: '%~1' is a percent-encoded API path, and this session does not read one.
endlocal
exit /b 1
:kubetower_refuse_secret
>&2 echo kubetower: '%~1' names a Secret, and this session does not read Secrets.
if defined KT_NO_SECRETS_WHY (>&2 echo kubetower: %KT_NO_SECRETS_WHY%) else (>&2 echo kubetower: KubeTower withholds Secrets from an assistant session.)
endlocal
exit /b 1
`

// shimPluginRefuseSecretPOSIX refuses a kubectl plugin in an assistant
// session. `kubectl view-secret api-tls` (krew's) names no Secret in a shape the
// read scan knows, and the plugin calls the API server itself. A plugin is
// somebody else's program and can do anything, so neither an allowlist nor a
// denylist of plugins works: every invocation resolving to a plugin is refused,
// with the same scan (shimPluginScanHeadPOSIX), whether or not the session may
// write. `krew` is a plugin too, so `kubectl krew install` is refused before
// anything is installed.
//
// The refusal gives the no-Secrets reason even in a read-only session, since
// the read-only sentence names a setting that would not lift this.
//
// The human terminal is untouched: a writable cluster's shell has no override,
// and a read-only one's (`ro-bin`) keeps its own plugin refusal.
const shimPluginRefuseSecretPOSIX = `    echo "kubetower: '$arg' runs the kubectl plugin $plugin, and a plugin (krew's view-secret or any other) can read a Secret: refused, this session does not read Secrets." >&2
    echo "kubetower: ${KT_NO_SECRETS_WHY:-KubeTower withholds Secrets from an assistant session.}" >&2
`

// shimRefusePluginSecretCmd is shimPluginRefuseSecretPOSIX in cmd.exe's
// language, under the label `shimPluginScanCmd` jumps to.
const shimRefusePluginSecretCmd = `:kubetower_refuse_plugin
>&2 echo kubetower: '%~1' runs the kubectl plugin %ktplugin%, and a plugin (krew's view-secret or any other) can read a Secret: refused, this session does not read Secrets.
if defined KT_NO_SECRETS_WHY (>&2 echo kubetower: %KT_NO_SECRETS_WHY%) else (>&2 echo kubetower: KubeTower withholds Secrets from an assistant session.)
endlocal
exit /b 1
`

// assistantPluginScanFor is the assistant's plugin scan, for the one binary
// that has plugins on PATH: pluginScanFor's scan with the no-Secrets refusal.
func assistantPluginScanFor(binary string) string {
	if binary == "kubectl" {
		return shimPluginScanHeadPOSIX + shimPluginRefuseSecretPOSIX + shimPluginScanTailPOSIX
	}
	return ""
}

// assistantPluginScanCmdFor is the Windows half of assistantPluginScanFor.
func assistantPluginScanCmdFor(binary string) (scan, refuse string) {
	if binary == "kubectl" {
		return shimPluginScanCmd, shimRefusePluginSecretCmd
	}
	return "", ""
}

// buildAssistantShim renders the POSIX override an assistant session resolves.
// denyWrites adds the read-only guardrail; a parameter because a session the
// operator allowed to write (KT_CLAUDE_READONLY=false) must be able to. The
// Secret and plugin scans are always on.
func buildAssistantShim(binary, realPath string, denyWrites bool) string {
	script := fmt.Sprintf(shimHeadPOSIX, realPath)
	if denyWrites {
		script += fmt.Sprintf(shimWriteScanPOSIX, strings.Join(verbsFor(binary), "|"))
	}
	script += assistantPluginScanFor(binary)
	script += secretScanFor(binary)
	return script + shimTailPOSIX
}

// buildAssistantShimCmd is the same override for cmd.exe: the loop's scans,
// the shift, then one refusal block per scan. A shim that does not deny writes
// has no `:kubetower_refuse` label and nothing jumps to one; every kubectl
// override has `:kubetower_refuse_plugin`, with the no-Secrets sentence.
func buildAssistantShimCmd(binary, realPath string, denyWrites bool) string {
	script := shimHeadCmd
	if denyWrites {
		script += fillShimCmd(shimWriteScanCmd, strings.Join(verbsFor(binary), " "), realPath)
	}
	pluginScan, pluginRefuse := assistantPluginScanCmdFor(binary)
	script += pluginScan

	helm := ""
	if binary == "helm" {
		helm = strings.NewReplacer("@HELMSECRETS@", strings.Join(helmSecretSubcommands, " ")).
			Replace(shimHelmSecretWordsCmd)
	}
	script += strings.NewReplacer("@HELMWORDS@", helm).Replace(shimSecretScanCmd)

	script += shimScanEndCmd
	if denyWrites {
		script += shimRefuseWriteCmd
	}
	script += pluginRefuse + shimRefuseSecretCmd
	return fillShimCmd(script+shimRunCmd, "", realPath)
}
