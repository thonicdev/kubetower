package launch

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A variable the process reads must be able to reach the process.
//
// **`environment:` in both compose files is an explicit list**, not a
// pass-through, so a `KT_` variable absent from it never arrives however
// carefully the operator writes it into `.env`, and nothing says so. For
// example:
//
//   - `KT_DIAG`, documented in `.env.example`: unforwarded, the launch report
//     could not be switched off in a container.
//   - `KT_AWS_EXEC`: `.env.example` promises *"a way back that does not need a
//     new build is worth one variable."* Unforwarded, the container has no way
//     back.
//   - `KT_IN_CONTAINER`, whose override an operator is told to check.
//
// `docker-compose.yml`'s own comment names the opposite failure — *"a variable
// passed to a process that does not read it is a configuration knob that appears
// to work"* — and this is the mirror image, which is worse: the process does
// read it, and nothing anywhere tells the operator it never arrived.
//
// The test is a **gate rather than a guess**. A new variable must be either
// forwarded or listed below with a reason; it cannot be neither by accident.

// notForwarded is every `KT_` name that deliberately does not belong in a
// compose `environment:` block, with why.
//
// A list, and lists rot — which is why the failure message asks for a decision
// rather than assuming one. Adding a row here is cheap and is meant to be:
// what must not happen is a variable falling through both.
var notForwarded = map[string]string{
	// Written *into* a dock session's environment by internal/terminal, never
	// read from this process's own. Forwarding them would let the operator's
	// `.env` claim a session is read-only when it is not.
	"KT_CLUSTER":  "internal/terminal writes it into a session, it is not read from the environment",
	"KT_READONLY": "same: written into a session's environment, and forwarding it would let .env claim a fence that is not there",

	// Opt-in integration tests. They run from a shell with a real cluster, never
	// from a container.
	"KT_INTEGRATION_CLUSTER":    "opt-in integration tests, run from a shell against a real cluster",
	"KT_INTEGRATION_KUBECONFIG": "same",

	// Host-side only. `.env.example` says it in as many words -- *"the process
	// itself never reads KT_PORT"* -- and it appears in the tree at exactly one
	// place: the launch report's list of names whose *values* are safe to print.
	// That is a name in a list, not a variable being read, and it is the one
	// systematic false positive this scan has.
	"KT_PORT": "moves the host side of the published mapping; the process never reads it, and its one occurrence is a name in the launch report's allowlist",

	// The update check's off switch. A console running in a container is
	// upgraded by its image tag and never checks at all -- `update.New` settles
	// that on `cluster.InContainer()` before the switch is consulted -- so
	// forwarding it would put a knob in the compose file that cannot change
	// anything. That is the failure the root compose file's own comment names:
	// *"a variable passed to a process that does not read it is a configuration
	// knob that appears to work"*.
	"KT_UPDATE_CHECK": "the containerized build never checks for a release whatever this says, so forwarding it would be a knob that cannot change anything",

	// Single sign-on is the server profile's only, and compose.dev.yaml
	// runs the desktop profile: the process refuses to start with an issuer and
	// no --incluster, so forwarding these would be a knob whose only effect is a
	// console that will not start. They are documented in .env.example and set
	// through the chart.
	"KT_OIDC_ISSUER":          ssoIsTheServerProfiles,
	"KT_OIDC_DISCOVERY_URL":   ssoIsTheServerProfiles,
	"KT_OIDC_CLIENT_ID":       ssoIsTheServerProfiles,
	"KT_OIDC_CLIENT_SECRET":   ssoIsTheServerProfiles,
	"KT_OIDC_REDIRECT_URL":    ssoIsTheServerProfiles,
	"KT_OIDC_SCOPES":          ssoIsTheServerProfiles,
	"KT_OIDC_USERNAME_CLAIM":  ssoIsTheServerProfiles,
	"KT_OIDC_USERNAME_PREFIX": ssoIsTheServerProfiles,
	"KT_OIDC_GROUPS_CLAIM":    ssoIsTheServerProfiles,
	"KT_OIDC_GROUPS_PREFIX":   ssoIsTheServerProfiles,
	"KT_OIDC_SESSION_MAX":     ssoIsTheServerProfiles,
	"KT_AUTH_LOCAL_ACCOUNT":   ssoIsTheServerProfiles,

	// The ingress's addresses, whose X-Forwarded-For a password attempt is
	// counted by. compose.dev.yaml publishes the port straight to the host, so
	// there is no proxy in front of it to name.
	"KT_TRUSTED_PROXIES": behindTheIngress,

	// The server profile's Prometheus. The desktop
	// refuses to start with it set, and compose.dev.yaml runs the desktop
	// profile, so forwarding it would be a knob whose only effect is a console
	// that will not start - the SSO variables' case exactly. The chart sets it.
	"KT_PROMETHEUS_SERVICE": prometheusIsTheServerProfiles,
}

// behindTheIngress is the reason for a variable only a deployment behind an
// ingress sets: an operator sets it, so it is documented though not forwarded.
const behindTheIngress = "compose.dev.yaml has no proxy in front of the console; the chart's ingress is what it names"

const ssoIsTheServerProfiles = "single sign-on is refused without --incluster, and compose.dev.yaml runs the desktop profile"

const prometheusIsTheServerProfiles = "a configured Prometheus is refused without --incluster, and compose.dev.yaml runs the desktop profile"

// TestWhatAnOperatorSetsIsDocumentedThoughNotForwarded keeps what notForwarded
// skips: its rows are exempt from the .env.example check too, and the single
// sign-on and ingress rows are variables an operator sets - KT_TRUSTED_PROXIES
// could be removed from .env.example with the suite green.
func TestWhatAnOperatorSetsIsDocumentedThoughNotForwarded(t *testing.T) {
	raw, err := os.ReadFile("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	for v, why := range notForwarded {
		if (why == ssoIsTheServerProfiles || why == behindTheIngress || why == prometheusIsTheServerProfiles) &&
			!strings.Contains(string(raw), v) {
			t.Errorf(".env.example does not mention %s, which an operator sets", v)
		}
	}
}

// notInTheHandover is forwarded by the root compose and deliberately absent from
// `docker/docker-compose.yml`, which is a different deployment rather than an
// oversight.
//
// Separate from `notForwarded` so that the guard is not lost on the file where
// the variable *does* belong. One map covering both would have to skip the
// variable everywhere, which is how a gate quietly stops covering the case it
// was written for.
var notInTheHandover = map[string]string{
	// The hand-over compose says it at its own line: KT_HOST_AWS_CACHE points at
	// a read-only re-mount of the *host's* AWS assume-role cache so the health
	// page can name a corrupt entry, and that needs HOST_AWS_DIR to be a real
	// path -- which is exactly what is optional in that deployment. The
	// protection it accompanies is unaffected and still on.
	"KT_HOST_AWS_CACHE": "the hand-over deployment has no host AWS cache to point at; docker/docker-compose.yml says so where it would have been",
}

// TestEveryVariableTheCodeReadsCanReachTheProcess.
func TestEveryVariableTheCodeReadsCanReachTheProcess(t *testing.T) {
	read := variablesReadByTheCode(t)
	if len(read) < 10 {
		t.Fatalf("only %d KT_ variables found in the tree, so the scan is broken rather than "+
			"the wiring", len(read))
	}

	// `compose.dev.yaml`, and there is exactly one.
	//
	// No compose file ships; `compose.dev.yaml` is for development only.
	//
	// What this guards: *a variable the code reads and nothing sets is not a
	// compile error, it is read as its zero value.* A silent skip on the one gate
	// that catches that would be an empty answer rendered as a passing one.
	//
	// **No skip arm.** The file is in the repository, so a tree without it is a
	// tree somebody has broken, and that is a failure. A `docker build` does not
	// need a skip either: the builder stage is `COPY . .` with no
	// `.dockerignore`, so the compose file is in the context.
	const compose = "../../compose.dev.yaml"
	if _, err := os.Stat(compose); err != nil {
		t.Fatalf("%s is not in this tree: %v. "+
			"It is tracked, so this is not the container build - it is the file having been "+
			"moved or deleted, and with it the only gate that catches a KT_ variable the "+
			"code reads and nothing sets.", compose, err)
	}
	files := []string{compose}

	for _, name := range files {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		compose := string(raw)

		for _, v := range read {
			if _, deliberate := notForwarded[v]; deliberate {
				continue
			}
			if _, deliberate := notInTheHandover[v]; deliberate &&
				strings.Contains(name, "docker/") {
				continue
			}
			// **The key and not the name.** Every one of these variables is
			// also named in a comment beside it, so asking whether the file
			// *contains* the string would stay green with the key deleted. An
			// assertion that a documented variable is documented is not an
			// assertion that it is wired.
			if regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(v) + `:`).MatchString(compose) {
				continue
			}
			t.Errorf("%s reads %s and %s never passes it, so setting it in .env does nothing "+
				"and nothing says so. Either add `%s: ${%s:-}` to that file's environment "+
				"block, or add %s to notForwarded in this test with the reason it does not "+
				"belong there", "the code", v, name, v, v, v)
		}
	}
}

// TestEveryVariableTheCodeReadsIsInTheExample is the other half of the same
// contract: *"Every one the code reads is listed in `.env.example`, and nothing
// that is not read is."*
//
// Both halves, because they fail in opposite directions: an undocumented
// variable is a feature nobody finds, and a documented one nothing reads is a
// knob that appears to work.
func TestEveryVariableTheCodeReadsIsInTheExample(t *testing.T) {
	raw, err := os.ReadFile("../../.env.example")
	if err != nil {
		t.Fatalf("reading .env.example: %v", err)
	}
	example := string(raw)

	for _, v := range variablesReadByTheCode(t) {
		if _, deliberate := notForwarded[v]; deliberate {
			// Still worth documenting where it is a thing an operator sets;
			// not required for the session-injected ones.
			continue
		}
		if !strings.Contains(example, v) {
			t.Errorf(".env.example does not mention %s, which the code reads", v)
		}
	}
}

// variablesReadByTheCode collects every `KT_*` string literal in non-test Go
// under `internal/`, `cmd/` and `extras/` - the extras are packages of this
// module and read a variable of their own (`KT_ARGOCD_TOKEN_SECRET`).
//
// String literals rather than a `go/ast` walk of `os.Getenv` calls: several are
// read through a constant (`statefile.EnvDir`, `cluster.EnvAWSExec`) or through
// a helper that takes the name as an argument, so following the call graph would
// miss exactly the indirections worth catching. A literal that is not a variable
// name would be a false positive, and the `KT_` prefix makes that unlikely
// enough to be worth the simplicity.
func variablesReadByTheCode(t *testing.T) []string {
	t.Helper()

	pattern := regexp.MustCompile(`"(KT_[A-Z0-9_]+)"`)
	found := map[string]bool{}

	for _, root := range []string{"../../internal", "../../cmd", "../../extras"} {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range pattern.FindAllStringSubmatch(string(raw), -1) {
				found[m[1]] = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}

	out := make([]string, 0, len(found))
	for v := range found {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
