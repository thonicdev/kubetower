package launch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The launch report: everything about this machine a bug report needs, printed
// on purpose while this is a 0.x build.
//
// The hard launch failures are facts about the environment nothing else
// prints: a launcher's intent read from a flag that means something else on
// macOS, a credential plugin missing from launchd's PATH, an EKS token signed as
// the wrong identity because of an inherited AWS_PROFILE. Each is one Getenv
// away, so the report prints the answers before anybody asks. It costs some
// thirty lines per start and stops at 1.0 by itself, because the gate is the
// version.
//
// It never holds a value that could be a credential: a variable's value is
// printed only if its name is on a closed allowlist, and every other set
// variable is only named ("AWS_SECRET_ACCESS_KEY is set" reveals nothing). A
// second guard refuses a secret-looking name even if allowlisted; the two are
// tested apart.

// diagKey is the switch, read only in this file.
const diagKey = "KT_DIAG"

// verboseLaunch decides whether the report is printed.
//
// The gate is the version, so this expires on its own: a 0.x is run by the
// people building it and colleagues they hand a binary to. `dev` counts as 0.x,
// since an unstamped build is where somebody is debugging.
//
// KT_DIAG overrides it either way: `off` for an operator running a 0.x in
// earnest, `on` to ask somebody on a 1.x for the report without a special
// build.
func verboseLaunch(release, setting string) bool {
	switch strings.ToLower(strings.TrimSpace(setting)) {
	case "on", "1", "true", "yes":
		return true
	case "off", "0", "false", "no":
		return false
	}
	release = strings.TrimSpace(release)
	return release == devVersion || release == "" || strings.HasPrefix(release, "0.")
}

// printableEnv is the closed list of environment variables whose *value* is
// printed.
//
// Everything KubeTower reads that is a path, a port, a duration, a list of
// cluster names or a yes/no, plus the AWS variables that select a configuration
// rather than being one. KT_PASSWORD_HASH and KT_SESSION_SECRET are absent:
// each is a credential.
//
// AWS_SHARED_CREDENTIALS_FILE is absent too, though only a path: secretish
// matches `CREDENTIAL` and would suppress it anyway, so a row would read as
// printed and never print. The second guard must win, so the list agrees with
// it, and a test refuses a row that disagrees. The variable is still named.
//
// The proxy variables are absent: `HTTPS_PROXY=http://user:pass@host` is the
// documented form, so a proxy URL is often a credential. They are named.
var printableEnv = map[string]bool{
	"KUBECONFIG":                true,
	"HOME":                      true,
	"USERPROFILE":               true,
	"KT_LISTEN":                 true,
	"KT_PORT":                   true,
	"KT_STATE_DIR":              true,
	"KT_USER":                   true,
	"KT_EXTRAS":                 true,
	"KT_EXTRA_SETTINGS":         true,
	"KT_READONLY_CLUSTERS":      true,
	"KT_FORWARD_PORTS":          true,
	"KT_IN_CONTAINER":           true,
	"KT_SESSION_TTL":            true,
	"KT_SESSION_MAX":            true,
	"KT_LOG_FOLLOW_MAX":         true,
	"KT_ARCHIVE":                true,
	"KT_ARCHIVE_LOGS":           true,
	"KT_ARCHIVE_RETENTION_DAYS": true,
	"KT_AWS_EXEC":               true,
	"KT_NODE_SHELL_IMAGE":       true,
	"KT_CLAUDE_READONLY":        true,
	"KT_CLAUDE_PERMISSION_MODE": true,
	"KT_HOST_AWS_CACHE":         true,
	"KT_DIAG":                   true,
	"KT_UPDATE_CHECK":           true,
	"AWS_PROFILE":               true,
	"AWS_DEFAULT_PROFILE":       true,
	"AWS_REGION":                true,
	"AWS_DEFAULT_REGION":        true,
	"AWS_CONFIG_FILE":           true,
	"AWS_SDK_LOAD_CONFIG":       true,
	"AWS_EC2_METADATA_DISABLED": true,
}

// secretish is the second guard: a name that reads as a credential is never
// printed, allowlisted or not.
//
// The allowlist gets rows added in a hurry; a test adds one to prove this
// catches it.
func secretish(name string) bool {
	upper := strings.ToUpper(name)
	for _, needle := range []string{"SECRET", "TOKEN", "PASSWORD", "PASSWD", "CREDENTIAL",
		"_KEY", "KEY_", "APIKEY", "HASH", "SIGNING", "PRIVATE"} {
		if strings.Contains(upper, needle) {
			return true
		}
	}
	return false
}

// reportedEnv is which variables are worth mentioning at all.
//
// Everything KubeTower or AWS reads, and a few variables that steer the
// runtime. Not the whole environment: a Finder launch carries dozens about the
// window server, and a report nobody finishes is one nobody reads.
func reportedEnv(name string) bool {
	switch {
	case strings.HasPrefix(name, "KT_"), strings.HasPrefix(name, "AWS_"):
		return true
	}
	switch name {
	case "KUBECONFIG", "HOME", "USERPROFILE", "PATH", "TMPDIR", "TEMP",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "GODEBUG", "TERM", "SHELL":
		return true
	}
	return false
}

// envReport is the environment section: values for the safe, names for the rest.
func envReport(environ []string) []string {
	var printed, named []string
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !reportedEnv(name) {
			continue
		}
		if name == "PATH" {
			// PATH has its own line below.
			continue
		}
		// An empty value is not "set". A compose file writes
		// `KT_ARCHIVE: ${KT_ARCHIVE:-}` for every optional variable, and
		// listing those empties (KT_PASSWORD_HASH among them) would suggest a
		// password hash is configured. Every reader here treats empty as
		// absent, and so does the report.
		if value == "" {
			continue
		}
		if printableEnv[name] && !secretish(name) {
			printed = append(printed, name+"="+value)
			continue
		}
		// Named only: not the length, which narrows a password, and not a
		// prefix, which identifies an access key's account.
		named = append(named, name)
	}
	sort.Strings(printed)
	sort.Strings(named)

	var out []string
	if len(printed) > 0 {
		out = append(out, "env set:  "+strings.Join(printed, "  "))
	}
	if len(named) > 0 {
		out = append(out, "env also set, values not printed:  "+strings.Join(named, "  "))
	}
	if len(out) == 0 {
		out = append(out, "env:      nothing this process reads is set")
	}
	return out
}

// stateReport lists the state directory, where "why does it not remember" and
// "why can nobody log in" are answered.
//
// PASSWORD-ONCE.txt is called out in words: it is deleted at the first login,
// so its presence means nobody has ever logged in, a different problem from a
// forgotten password.
func stateReport(dir string) []string {
	out := []string{"state:    " + dir}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return append(out, "state:    cannot be listed ("+err.Error()+"), which is why nothing "+
			"is remembered between starts")
	}

	var rows []string
	passwordOnce := false
	credentials := false
	for _, entry := range entries {
		name := entry.Name()
		switch name {
		case "PASSWORD-ONCE.txt":
			passwordOnce = true
		case "credentials.json":
			credentials = true
		}
		info, err := entry.Info()
		if err != nil {
			rows = append(rows, name+" (unreadable)")
			continue
		}
		if entry.IsDir() {
			rows = append(rows, name+"/")
			continue
		}
		rows = append(rows, fmt.Sprintf("%s %o %dB", name, info.Mode().Perm(), info.Size()))
	}
	sort.Strings(rows)
	if len(rows) == 0 {
		out = append(out, "state:    empty, so this is a first run")
	} else {
		out = append(out, "state:    "+strings.Join(rows, "  "))
	}

	if passwordOnce {
		out = append(out, "state:    PASSWORD-ONCE.txt is still there, so nobody has ever logged "+
			"in with the drawn password. It holds that password in clear and is deleted at the "+
			"first login")
	}
	if !credentials {
		out = append(out, "state:    no credentials.json, so the next start writes one. On a desktop "+
			"listening on loopback it holds no password - the window opens signed in; in a container "+
			"or with KT_LISTEN beyond loopback a password is drawn, unless KT_PASSWORD_HASH sets one")
	}
	return out
}

// toolReport says which of the four external programs this machine can resolve.
//
// exec.LookPath, after internal/binpath has augmented PATH, so this is what the
// process will find rather than what a shell would: from the Dock, kubectl can
// be invisible to the process and present in a terminal.
func toolReport(look func(string) (string, error)) []string {
	var found []string
	for _, tool := range []string{"kubectl", "helm", "aws", "claude"} {
		path, err := look(tool)
		if err != nil {
			found = append(found, tool+": not found")
			continue
		}
		found = append(found, tool+": "+path)
	}
	return []string{"tools:    " + strings.Join(found, "  ")}
}

// launchReport composes the whole block.
//
// It takes its inputs rather than reading them, so a test drives a macOS bundle
// launch from any host. It reads the environment and the state directory itself,
// both of which a test can point somewhere harmless.
type launchFacts struct {
	Release   string
	Version   string // the full versionLine()
	Exe       string
	GOOS      string
	Args      []string
	Console   bool
	Bundled   bool
	WindowWhy string
	StateDir  string
	Environ   []string
	Look      func(string) (string, error)
}

func launchReport(f launchFacts) []string {
	look := f.Look
	if look == nil {
		look = exec.LookPath
	}

	out := []string{
		"---- launch report: verbose because this is a " + describeRelease(f.Release) +
			" build. " + diagKey + "=off silences it ----",
		"build:    " + f.Version,
	}

	exeNote := f.Exe
	if exeNote == "" {
		exeNote = "unknown"
	}
	if f.Bundled {
		exeNote += "  (inside a macOS application bundle, so this launch came from the Finder, " +
			"the Dock or Launchpad)"
	}
	out = append(out, "exe:      "+exeNote)

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "unknown (" + err.Error() + ")"
	}
	out = append(out,
		"launch:   "+f.GOOS+" argv "+argvNote(f.Args)+"; console attached: "+
			strconv.FormatBool(f.Console)+consoleCaveat(f.GOOS)+"; cwd "+cwd,
		"window:   "+f.WindowWhy,
		"PATH:     "+pathReport(os.Getenv("PATH")))
	out = append(out, toolReport(look)...)
	out = append(out, stateReport(f.StateDir)...)
	out = append(out, envReport(f.Environ)...)
	return append(out, "---- end of launch report ----")
}

// describeRelease is what to call this build in the report's own first line.
func describeRelease(release string) string {
	if release == devVersion || release == "" {
		return "development"
	}
	return release
}

// consoleCaveat is why that boolean is worth less than it looks off Windows.
//
// It is a real AttachConsole result on Windows and an assumed true elsewhere,
// and the report says which.
func consoleCaveat(goos string) string {
	if goos == "windows" {
		return " (measured)"
	}
	return " (assumed: only Windows measures this, so a Finder or menu launch says true too)"
}

// argvNote is the arguments, and only the ones this binary recognizes.
//
// An unrecognized argument is counted, not printed: a report echoing whatever
// it was given would leak the first flag that ever takes a secret.
func argvNote(args []string) string {
	known := map[string]bool{
		"--version": true, "-version": true, "version": true, "-v": true,
		"--notices": true, "-notices": true, "notices": true,
		"--app": true, "-app": true, "app": true,
		"--no-window": true, "-no-window": true, "no-window": true,
		"--reset-password": true, "-reset-password": true, "reset-password": true,
		"--unattended": true, "-unattended": true, "unattended": true,
		// The server profile. This list mirrors the switch in run.go and
		// drifts from it silently: a flag missed here is reported as
		// unrecognized.
		"--incluster": true, "-incluster": true, "incluster": true,
		"--install": true, "-install": true, "install": true,
		"--uninstall": true, "-uninstall": true, "uninstall": true,
		"--help": true, "-help": true, "-h": true, "help": true,
	}
	var shown []string
	others := 0
	for _, arg := range args {
		switch {
		case known[arg]:
			shown = append(shown, arg)
		case strings.HasPrefix(arg, "--bundle="):
			shown = append(shown, "--bundle=...")
		default:
			others++
		}
	}
	if others > 0 {
		shown = append(shown, "and "+strconv.Itoa(others)+" not recognized by this binary")
	}
	if len(shown) == 0 {
		return "(none)"
	}
	return strings.Join(shown, " ")
}

// pathReport is the search path, entry by entry.
//
// Named pathReport because install_linux.go, behind a linux build tag, already
// has a pathNote; building every platform in CI is what catches such a
// collision.
//
// Printed whole: a count answers none of the questions the list does.
func pathReport(path string) string {
	if path == "" {
		return "(empty, which is why nothing external resolves)"
	}
	entries := filepath.SplitList(path)
	return strconv.Itoa(len(entries)) + " entries: " + strings.Join(entries, "  ")
}
