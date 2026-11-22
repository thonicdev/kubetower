package cluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The failure this prevents.
//
// A container mounting `~/.kube` and naming `config` in KUBECONFIG crash-loops
// when that directory holds its kubeconfigs under other names -- every path
// missing, `Load` refuses, `internal/launch` turns it into a Fatalf, compose
// restarts it. Renaming `config` to `prod.yaml` reproduces it.

func kubeconfigWith(t *testing.T, dir, name string, contexts ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("apiVersion: v1\nkind: Config\nclusters:\n")
	for _, c := range contexts {
		b.WriteString("- name: " + c + "\n  cluster:\n    server: https://" + c + ".example\n")
	}
	b.WriteString("contexts:\n")
	for _, c := range contexts {
		b.WriteString("- name: " + c + "\n  context:\n    cluster: " + c + "\n    user: " + c + "\n")
	}
	b.WriteString("users:\n")
	for _, c := range contexts {
		b.WriteString("- name: " + c + "\n  user:\n    token: redacted\n")
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// TestAKubeDirectoryWithNoFileCalledConfigStillLoads is that case.
func TestAKubeDirectoryWithNoFileCalledConfigStillLoads(t *testing.T) {
	dir := t.TempDir()
	kubeconfigWith(t, dir, "prod.yaml", "prod")
	kubeconfigWith(t, dir, "staging.yaml", "staging")

	named := Kubeconfig{filepath.Join(dir, "config")} // does not exist

	// Without discovery this is the crash: every path missing.
	if _, err := named.Load(); err == nil {
		t.Fatal("a list whose only file is absent loaded; the premise of this test is gone")
	}

	extended, scans := named.Discover()
	raw, err := extended.Load()
	if err != nil {
		t.Fatalf("discovery did not rescue the load: %v\nscans: %v", err, sentences(scans))
	}
	for _, want := range []string{"prod", "staging"} {
		if _, ok := raw.Contexts[want]; !ok {
			t.Errorf("context %q is missing after discovery; got %v", want, keysOf(raw.Contexts))
		}
	}
	// And the named file is still first, so an explicit KUBECONFIG keeps
	// precedence over anything found by looking.
	if extended[0] != named[0] {
		t.Errorf("the named file lost its place: %v", extended)
	}
}

// TestTheNamedFileWinsADuplicateName. First-wins is client-go's rule and the
// whole reason discovery appends rather than prepends: a file the operator
// named must not be overridden by one that merely sits beside it.
func TestTheNamedFileWinsADuplicateName(t *testing.T) {
	dir := t.TempDir()
	named := kubeconfigWith(t, dir, "config", "prod")
	kubeconfigWith(t, dir, "other.yaml", "prod")

	extended, _ := Kubeconfig{named}.Discover()
	raw, err := extended.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := raw.Contexts["prod"].Cluster; got != "prod" {
		t.Errorf("cluster for prod = %q", got)
	}
	// The survey is what makes the shadowing visible rather than merely
	// effective; discovery must not have broken it.
	if s := extended.Survey(); len(s.Shadowed) == 0 {
		t.Error("two files declare prod and the survey reports no shadowing")
	}
}

// TestDiscoveryIsOrderedTheSameWayTwice.
//
// First-wins makes the order the answer. An order that varied between restarts
// would put a different cluster behind one name from one start to the next with
// nothing on screen changing -- the difference between a mistake and a haunting.
func TestDiscoveryIsOrderedTheSameWayTwice(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"zulu.yaml", "alpha.yaml", "mike.yaml", "bravo.yaml"} {
		kubeconfigWith(t, dir, n, strings.TrimSuffix(n, ".yaml"))
	}
	named := Kubeconfig{filepath.Join(dir, "config")}

	first, _ := named.Discover()
	for range 5 {
		again, _ := named.Discover()
		if strings.Join(first, "|") != strings.Join(again, "|") {
			t.Fatalf("two scans of one directory disagreed:\n  %v\n  %v", first, again)
		}
	}
	// Sorted, and not merely stable: ReadDir's order is not contractual.
	if !sortedAscending(first[1:]) {
		t.Errorf("the discovered files are not in sorted order: %v", first[1:])
	}
}

// TestWhatIsNotAKubeconfigIsPassedOver, and the two halves fail in opposite
// directions: junk must not be added, and the reason must be sayable.
func TestWhatIsNotAKubeconfigIsPassedOver(t *testing.T) {
	dir := t.TempDir()
	good := kubeconfigWith(t, dir, "good.yaml", "prod")

	if err := os.MkdirAll(filepath.Join(dir, "cache", "discovery"), 0o700); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	write("config.lock", "")
	write("notes.txt", "this is not yaml: [unclosed")
	write("empty.yaml", "")
	write(".hidden", "apiVersion: v1\nkind: Config\n")
	// Valid YAML, not a kubeconfig: parses to a config with no contexts.
	write("compose.yaml", "services:\n  web:\n    image: nginx\n")

	extended, scans := Kubeconfig{filepath.Join(dir, "config")}.Discover()

	if len(extended) != 2 || extended[1] != good {
		t.Errorf("discovery added something it should not have: %v", extended)
	}
	joined := strings.Join(sentences(scans), " | ")
	if !strings.Contains(joined, "good.yaml") {
		t.Errorf("the file that was added is not named in the log line: %s", joined)
	}
	// `cache/`, the lock and the dotfile are skipped quietly: a line burying the
	// one useful name under kubectl's discovery cache is a line nobody reads.
	for _, quiet := range []string{"cache", "config.lock", ".hidden"} {
		if strings.Contains(joined, quiet) {
			t.Errorf("%q is reported when it should be passed over quietly: %s", quiet, joined)
		}
	}
	// But a file that *looks* like a candidate and is not gets a reason, because
	// "why is my cluster not there" must not need a bisect.
	if !strings.Contains(joined, "compose.yaml") {
		t.Errorf("a plausible file was dropped with no reason given: %s", joined)
	}
}

// TestDiscoveryNeverFails. It exists to prevent a refusal, so it must not
// become one: an unreadable directory is reported and the named list returned.
func TestDiscoveryNeverFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-dir", "config")
	extended, scans := Kubeconfig{missing}.Discover()

	if len(extended) != 1 || extended[0] != missing {
		t.Errorf("the list changed for an unreadable directory: %v", extended)
	}
	joined := strings.Join(sentences(scans), " | ")
	if !strings.Contains(joined, "could not be listed") {
		t.Errorf("an unreadable directory is not reported: %s", joined)
	}
}

// TestAnOversizedFileIsNotParsed. The scan runs at start-up before anything is
// served; a database dump in ~/.kube must cost a Stat, not a parse.
func TestAnOversizedFileIsNotParsed(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.yaml")
	if err := os.WriteFile(big, make([]byte, maxKubeconfigBytes+1), 0o600); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	_, scans := Kubeconfig{filepath.Join(dir, "config")}.Discover()
	joined := strings.Join(sentences(scans), " | ")
	if !strings.Contains(joined, "big.yaml") || !strings.Contains(joined, "MiB") {
		t.Errorf("an oversized file is not reported as such: %s", joined)
	}
}

func sentences(scans []Scan) []string {
	var out []string
	for _, s := range scans {
		if line := s.Sentence(); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func sortedAscending(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] > s[i] {
			return false
		}
	}
	return true
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestAnEmptyMountSaysSoRatherThanNamingAFileNobodyHas.
//
// Discovery rescues a directory whose kubeconfigs are called something else; a
// directory with **no** kubeconfig in it cannot be rescued, and that is the
// commoner mistake by far -- Docker Desktop *creates* a bind mount's source when
// it is missing, so a recipient who skips "copy your ~/.kube here" gets an empty
// directory, `Config not found` naming a file they never had, and a restart
// loop with not one line naming the directory.
func TestAnEmptyMountSaysSoRatherThanNamingAFileNobodyHas(t *testing.T) {
	dir := t.TempDir() // exists, empty -- exactly what Docker creates
	named := Kubeconfig{filepath.Join(dir, "config")}

	_, scans := named.Discover()
	why := WhyNothing(named, scans)

	if why == "" {
		t.Fatal("an empty kubeconfig directory produced no explanation at all, which is the " +
			"state that cost four restarts and a support exchange")
	}
	if !strings.Contains(why, dir) {
		t.Errorf("the explanation does not name the directory: %s", why)
	}
	if !strings.Contains(why, "empty") {
		t.Errorf("an empty directory is not described as empty: %s", why)
	}
}

// TestADirectoryOfTheWrongFilesIsDescribedDifferently. An empty mount and a
// directory full of the wrong thing are two different mistakes with two
// different fixes, and a single sentence for both sends the operator looking in
// the wrong place.
func TestADirectoryOfTheWrongFilesIsDescribedDifferently(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"notes.txt", "compose.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("services: {}\n"), 0o600); err != nil {
			t.Fatalf("seeding: %v", err)
		}
	}
	named := Kubeconfig{filepath.Join(dir, "config")}

	_, scans := named.Discover()
	why := WhyNothing(named, scans)

	if !strings.Contains(why, "none of them is a kubeconfig") {
		t.Errorf("a directory of the wrong files is not described as such: %s", why)
	}
	if strings.Contains(why, "empty") {
		t.Errorf("a directory with two files in it is called empty: %s", why)
	}
}

// TestNothingIsExplainedWhenThereIsNothingWrong. The other direction, and the
// one a heavy-handed fix breaks: a working start must not carry a paragraph
// about kubeconfigs it read perfectly well.
func TestNothingIsExplainedWhenThereIsNothingWrong(t *testing.T) {
	dir := t.TempDir()
	named := Kubeconfig{kubeconfigWith(t, dir, "config", "prod")}

	_, scans := named.Discover()
	if why := WhyNothing(named, scans); why != "" {
		t.Errorf("a directory holding exactly the named kubeconfig produced a complaint: %s", why)
	}
}
