package launch

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// **One kubeconfig list, and every reader of it gets the same one.**
//
// There is no seam here: reaching these lines means starting a real server
// against a real kubeconfig, so what a test can do is read the assembly — the
// gesture `TestTheSettingsPageIsHandedTheSameLineTheFlagPrints` beside this
// makes, and for the same reason.
//
// What it is guarding is worth stating, because the compiler covers less of it
// than it looks. `Kubeconfig` is a `cluster.Kubeconfig` in both option structs,
// so handing one of them a *string* does not compile. Handing one of
// them a **different list** does: `cluster.Kubeconfig{kubeconfig[0]}`, or a
// second `cluster.KubeconfigFromEnv()` call, or the manager built from one file
// and the terminals from another. Each compiles, starts, serves, and produces a
// console whose rail lists a context its own dock terminal cannot open: the
// same correction has to reach every path.
func TestOneKubeconfigListReachesEveryReaderOfIt(t *testing.T) {
	raw, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatalf("reading the assembly: %v", err)
	}
	source := string(raw)

	// **The list is resolved once**, counted on the *call* and not on the
	// assignment.
	//
	// Counting `x := cluster.KubeconfigFromEnv()` would miss a second call bound
	// any other way, such as `_ = cluster.KubeconfigFromEnv()` — including
	// `Kubeconfig: cluster.KubeconfigFromEnv()` written straight into an option
	// struct, which is exactly the shape somebody reaches for when a reader is
	// added later. Two calls agree today and are free to stop: `KUBECONFIG` is
	// read twice, and between the two reads is anything that edits an environment.
	if calls := strings.Count(source, "cluster.KubeconfigFromEnv()"); calls != 1 {
		t.Fatalf("cluster.KubeconfigFromEnv() is called %d times in run.go, want exactly "+
			"one. Two calls are two lists, and the way that shows up is a rail listing a "+
			"context the dock beside it cannot open", calls)
	}

	// The variable it is resolved into, read out of the source rather than
	// written down here: renaming it is fine, using a second one is not.
	assigned := regexp.MustCompile(`(\w+)\s*:=\s*cluster\.KubeconfigFromEnv\(\)`)
	matches := assigned.FindAllStringSubmatch(source, -1)
	if len(matches) != 1 {
		t.Fatalf("the one call to cluster.KubeconfigFromEnv() is not assigned to a variable "+
			"the readers below can share (%d matches)", len(matches))
	}
	name := matches[0][1]

	// Whitespace-insensitive: gofmt aligns those struct literals and the
	// alignment moves whenever a longer field name is added beside them.
	for _, want := range []struct {
		what  string
		shape *regexp.Regexp
		why   string
	}{
		{
			what:  "cluster.NewManager",
			shape: regexp.MustCompile(`cluster\.NewManager\(` + name + `\.\.\.\)`),
			why: "the context list the rail draws, and the client behind every request, " +
				"come from the manager — spread and not passed as one string, which " +
				"NewManager would take as a single filename",
		},
		{
			what:  "terminal.Options.Kubeconfig",
			shape: regexp.MustCompile(`Kubeconfig:\s*` + name + `,`),
			why: "a dock terminal, a pod exec and a desktop Claude window each copy one " +
				"context out of this; a subset means a session opened on a context from " +
				"any other file has no context at all",
		},
		{
			what:  "health.Options.Kubeconfig",
			shape: regexp.MustCompile(`Kubeconfig:\s*` + name + `,`),
			why: "/kt-healthz reports on the files it is given, so a subset reports on a " +
				"fraction of what the console actually reads",
		},
	} {
		if !want.shape.MatchString(source) {
			t.Errorf("run.go does not hand %s the list as `%s`: %s",
				want.what, want.shape, want.why)
		}
	}

	// Both option structs, and not one of them twice. The regexp above cannot
	// tell those apart, so the count is the assertion.
	handed := regexp.MustCompile(`Kubeconfig:\s*`+name+`,`).FindAllString(source, -1)
	if len(handed) != 2 {
		t.Errorf("the list is handed to %d option structs, want 2 (the terminals and the "+
			"readiness probe). A reader left behind keeps whatever it defaults to, which is "+
			"`KUBECONFIG` read a second time by somebody else's rules", len(handed))
	}

	// And no single-path reading: a `KUBECONFIG` holding two paths would resolve
	// to one filename with a separator in it, and the process would refuse to
	// start.
	if strings.Contains(source, `envOr("KUBECONFIG"`) {
		t.Error(`run.go reads KUBECONFIG with envOr again, which takes the whole ` +
			`path-list as one filename — the defect this change exists to fix`)
	}
}

// The startup announcement, which is one of the two places a shadowed name can
// be said at all.
//
// **What this does not cover, stated rather than implied**, the way
// `listen_test.go` states the same gap: that `main` still calls
// `announceKubeconfig`. Reaching that line means building a real manager. The
// call is one line immediately after `cluster.NewManager`, and the "N contexts
// from …" line goes with it, so its absence is visible the first time anybody
// starts the process — and the test above pins the assignment it reads from.
func TestTheStartupLineNamesEveryFileAndWhatTheMergeHid(t *testing.T) {
	dir := t.TempDir()
	first := writeContextKubeconfig(t, filepath.Join(dir, "first"), "prod")
	second := writeContextKubeconfig(t, filepath.Join(dir, "second"), "prod")
	absent := filepath.Join(dir, "other-laptop")

	files := cluster.Kubeconfig{first, second, absent}

	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	announceKubeconfig(files, files.Survey(), 1)
	log.SetOutput(previous)
	said := buf.String()

	if !strings.Contains(said, "1 contexts from") {
		t.Errorf("the count and the files are no longer said:\n%s", said)
	}
	for _, path := range []string{first, second, absent} {
		if !strings.Contains(said, path) {
			t.Errorf("%s is not named anywhere in the startup log:\n%s", path, said)
		}
	}
	if !strings.Contains(said, "not there") {
		t.Errorf("a KUBECONFIG entry that is not there was passed over in silence, so \"my "+
			"cluster is missing\" and \"I typed that path wrong\" stay indistinguishable:\n%s",
			said)
	}
	// The shadow. Without this the second `prod` does not exist anywhere an
	// operator can see: not in the rail, not in a URL, not as an error.
	if !strings.Contains(said, `"prod"`) || !strings.Contains(said, "discarded") {
		t.Errorf("a context declared by two files was not reported at startup:\n%s", said)
	}
}

// A single kubeconfig must produce a plain one-file line.
//
// An operator with one file reads it at every start-up. A list rendering that
// hides warnings would be the opposite defect; a list rendering that adds noise
// to the ordinary case is how the warnings come to be skipped.
func TestOneKubeconfigStillReadsAsOneLine(t *testing.T) {
	path := writeContextKubeconfig(t, filepath.Join(t.TempDir(), "config"), "only")
	files := cluster.Kubeconfig{path}

	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	announceKubeconfig(files, files.Survey(), 1)
	log.SetOutput(previous)

	said := strings.TrimSpace(buf.String())
	if strings.Count(said, "\n") != 0 {
		t.Errorf("one kubeconfig produced more than one line:\n%s", said)
	}
	if !strings.HasSuffix(said, "1 contexts from "+path) {
		t.Errorf("the line is %q, and an operator reads this shape at every start-up", said)
	}
}

func writeContextKubeconfig(t *testing.T, path, name string) string {
	t.Helper()
	doc := "apiVersion: v1\nkind: Config\ncurrent-context: " + name + "\n" +
		"clusters:\n- name: " + name + "\n  cluster:\n    server: https://" + name +
		".example:6443\n    insecure-skip-tls-verify: true\n" +
		"contexts:\n- name: " + name + "\n  context:\n    cluster: " + name +
		"\n    user: " + name + "\n" +
		"users:\n- name: " + name + "\n  user:\n    token: TOKEN\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}
