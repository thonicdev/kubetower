package cluster

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeKubeconfigAt is kubeconfigWithContexts with the path chosen by the
// caller, because every test in this file needs *two* files and that helper
// puts one in a fresh t.TempDir().
//
// The server is per context so that a merge can be checked by asking which
// address a name came out with, which is the only way to see which file won:
// the loser leaves nothing else behind.
func writeKubeconfigAt(t *testing.T, path string, servers map[string]string) string {
	t.Helper()

	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}

	var b strings.Builder
	b.WriteString("apiVersion: v1\nkind: Config\nclusters:\n")
	for _, name := range names {
		fmt.Fprintf(&b, "- name: %q\n  cluster:\n    server: %s\n    insecure-skip-tls-verify: true\n",
			name, servers[name])
	}
	b.WriteString("contexts:\n")
	for _, name := range names {
		fmt.Fprintf(&b, "- name: %q\n  context:\n    cluster: %q\n    user: %q\n", name, name, name)
	}
	b.WriteString("users:\n")
	for _, name := range names {
		fmt.Fprintf(&b, "- name: %q\n  user:\n    token: TEST-TOKEN-%s\n", name, name)
	}
	if len(names) > 0 {
		fmt.Fprintf(&b, "current-context: %q\n", names[0])
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("making the directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("writing the kubeconfig: %v", err)
	}
	return path
}

// KUBECONFIG is a list, not one filename.
//
// The property asserted is the *shape*, not the separator: writing `;` or `:`
// out here would pin the test to the platform it was written on, which is
// exactly the mistake `filepath.SplitList` exists to avoid.
func TestKUBECONFIGIsReadAsAListAndNotAsOneFilename(t *testing.T) {
	first := filepath.Join("a", "config")
	second := filepath.Join("b", "other.yaml")
	joined := Kubeconfig{first, second}.Env()

	got := SplitKubeconfig(joined)
	if len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("splitting %q gave %v, want the two files. A single element here is the "+
			"defect this type exists for: the whole of KUBECONFIG taken as one filename, "+
			"which fails NewManager and refuses to start", joined, got)
	}
}

// The shell's own leftovers, and a duplicate.
func TestTheListDropsWhatWouldNotBeAFile(t *testing.T) {
	sep := string(os.PathListSeparator)
	got := SplitKubeconfig(sep + "one" + sep + sep + " " + sep + "one" + sep + "two" + sep)
	want := Kubeconfig{"one", "two"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %v, want %v. An empty entry names the current directory to some "+
			"readers and a file listed twice would draw as two files that happen to agree "+
			"on the Setup page", got, want)
	}
}

// The two joinings are not interchangeable, and one of them is read by kubectl.
func TestTheListForAChildProcessUsesThePlatformSeparator(t *testing.T) {
	files := Kubeconfig{"one", "two"}
	if got := files.Env(); got != "one"+string(os.PathListSeparator)+"two" {
		t.Errorf("Env() is %q. A child process reads this as KUBECONFIG, so anything but "+
			"the platform's path-list separator names one file that does not exist", got)
	}
	if got := files.String(); !strings.Contains(got, ", ") {
		t.Errorf("String() is %q, which is what a person reads in a log line", got)
	}
	// One file must render as just that file: an operator with one
	// kubeconfig reads that startup line at every start.
	if got := (Kubeconfig{"only"}).String(); got != "only" {
		t.Errorf("one file renders as %q rather than as itself", got)
	}
}

// **The merge is client-go's, and the rule that surprises people is that the
// first file wins.**
//
// Asserted through the *server address* rather than through presence, because
// presence is true either way: both files declare `prod`, and what is at stake
// is which cluster the console means by it. Getting this backwards is somebody
// running a command against the wrong API server with a name that matches.
func TestTheFirstFileWinsOnANameTwoFilesDeclare(t *testing.T) {
	dir := t.TempDir()
	first := writeKubeconfigAt(t, filepath.Join(dir, "first"), map[string]string{
		"prod": "https://first.example:6443",
	})
	second := writeKubeconfigAt(t, filepath.Join(dir, "second"), map[string]string{
		"prod":    "https://second.example:6443",
		"staging": "https://staging.example:6443",
	})

	raw, err := Kubeconfig{first, second}.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := raw.Clusters["prod"].Server; got != "https://first.example:6443" {
		t.Errorf("the merged `prod` names %s, so the later file won. kubectl gives the "+
			"earlier file the name, and a console that disagrees with the kubectl in its "+
			"own dock is how a command reaches the wrong cluster", got)
	}
	// And the loser's file still contributes everything it did not lose.
	if raw.Clusters["staging"] == nil {
		t.Error("`staging` is only in the second file and did not survive the merge, so the " +
			"second file was not read at all")
	}
}

// kubectl's file semantics, which this must match rather than invent.
func TestAMissingFileIsSkippedAndAnUnreadableOneIsAnError(t *testing.T) {
	dir := t.TempDir()
	good := writeKubeconfigAt(t, filepath.Join(dir, "good"), map[string]string{
		"real": "https://real.example:6443",
	})

	// Absent. kubectl says nothing about it, and a KUBECONFIG naming a file
	// that lives on the operator's other laptop is deliberate.
	raw, err := Kubeconfig{filepath.Join(dir, "not-here"), good}.Load()
	if err != nil {
		t.Fatalf("a missing path failed the load: %v. `ExplicitPath` behaves that way and "+
			"`Precedence` must not, or a list is unusable the moment one entry is for "+
			"another machine", err)
	}
	if raw.Contexts["real"] == nil {
		t.Error("the file that was there did not survive a sibling being absent")
	}

	// There and not a kubeconfig. An empty or half-written bind mount is
	// exactly this shape, and it must not be read as a file with no clusters.
	broken := filepath.Join(dir, "broken")
	if err := os.WriteFile(broken, []byte("this is not a kubeconfig: {{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Kubeconfig{broken, good}).Load(); err == nil {
		t.Error("a file that exists and does not parse was skipped. That is a fifth of an " +
			"operator's clusters silently missing from the rail, with a page that draws " +
			"the rest and says nothing")
	}
}

// Every path missing is a failure, and not an installation with no clusters.
//
// This is the one case where "skip it" is the wrong answer, and it is the
// behavior of a single kubeconfig that is not there: NewManager fails, and
// internal/launch turns that into a refusal to start.
func TestEveryPathMissingIsAFailureAndNotAnEmptyKubeconfig(t *testing.T) {
	dir := t.TempDir()
	_, err := Kubeconfig{filepath.Join(dir, "one"), filepath.Join(dir, "two")}.Load()
	if err == nil {
		t.Fatal("a KUBECONFIG whose every file is missing loaded as an empty kubeconfig. " +
			"That is an empty bind mount reported as a console nobody has added a " +
			"cluster to — a failed answer drawn as an empty one, delivered by a mount")
	}
	for _, want := range []string{"one", "two"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error is %q and does not name %s. An operator cannot act on "+
				"\"the kubeconfig could not be read\" over several files", err, want)
		}
	}
}

func TestAnEmptyListRefusesRatherThanReturningNothing(t *testing.T) {
	if _, err := (Kubeconfig{}).Load(); err == nil {
		t.Error("loading no files at all succeeded")
	}
}

// The wiring of the context list: the rail is drawn from Manager.Contexts, and
// it has to hold contexts from every file.
func TestTheContextListHoldsContextsFromEveryFile(t *testing.T) {
	dir := t.TempDir()
	first := writeKubeconfigAt(t, filepath.Join(dir, "first"), map[string]string{
		"from-first": "https://first.example:6443",
	})
	second := writeKubeconfigAt(t, filepath.Join(dir, "second"), map[string]string{
		"from-second": "https://second.example:6443",
	})

	m, err := NewManager(first, second)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	for _, name := range []string{"from-first", "from-second"} {
		if !m.Known(name) {
			t.Errorf("%q is not known, so it is not on the rail and no URL naming it "+
				"resolves. Contexts() is %v", name, m.Contexts())
		}
		// A cluster name from a URL is not always a context name: the way in is
		// Resolve, and it has to work for a context from either file.
		if got, ok := m.Resolve(MakeDNSFriendly(name)); !ok || got != name {
			t.Errorf("%q does not resolve from its URL form", name)
		}
	}
}

// **The per-context client is the second reader, and it is a separate seam.**
//
// `Contexts()` comes from `Reload`, and a client comes from its own
// `ClientConfigLoadingRules`. Those are two spellings of the path, and covering
// only the first would put a context on the rail that answers "unknown cluster"
// — or worse, resolves to another file's entry of the same name. So this builds
// a client for a context that exists **only in the second file**, which is the
// case that fails if the rules still name one file.
//
// No network: building a client runs the exec plugin for nothing and makes no
// request, which is why this is a unit test at all.
func TestAClientCanBeBuiltForAContextFromAnyFile(t *testing.T) {
	dir := t.TempDir()
	first := writeKubeconfigAt(t, filepath.Join(dir, "first"), map[string]string{
		"from-first": "https://first.example:6443",
	})
	second := writeKubeconfigAt(t, filepath.Join(dir, "second"), map[string]string{
		"from-second": "https://second.example:6443",
	})

	m, err := NewManager(first, second)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	client, err := m.Client("from-second")
	if err != nil {
		t.Fatalf("building a client for a context out of the second file: %v. The context "+
			"list and the client are two readers of the kubeconfig, and this is the one "+
			"that fails when only the first was widened", err)
	}
	if got := client.Config.Host; got != "https://second.example:6443" {
		t.Errorf("the client for `from-second` points at %s", got)
	}
}

// The survey: which file each context came from, and which names one file took
// from another.
func TestTheSurveySaysWhichFileEachContextCameFromAndWhatWasShadowed(t *testing.T) {
	dir := t.TempDir()
	first := writeKubeconfigAt(t, filepath.Join(dir, "first"), map[string]string{
		"prod": "https://first.example:6443",
	})
	second := writeKubeconfigAt(t, filepath.Join(dir, "second"), map[string]string{
		"prod": "https://second.example:6443",
		"dev":  "https://dev.example:6443",
	})
	absent := filepath.Join(dir, "not-here")

	survey := Kubeconfig{first, second, absent}.Survey()

	if len(survey.Files) != 3 {
		t.Fatalf("the survey covers %d of 3 files", len(survey.Files))
	}
	// Precedence order, not directory order: the page and the log both say
	// "first wins", and a list in another order makes that sentence a lie.
	if survey.Files[0].Path != first || survey.Files[1].Path != second {
		t.Errorf("the survey is not in precedence order: %s then %s",
			survey.Files[0].Path, survey.Files[1].Path)
	}
	if !survey.Files[2].Absent || survey.Files[2].Read() {
		t.Error("a path that is not there was not reported as absent")
	}
	if strings.Join(survey.Files[1].Contexts, ",") != "dev,prod" {
		t.Errorf("the second file declares %v; the page draws this as \"which cluster is "+
			"in which file\"", survey.Files[1].Contexts)
	}
	if got := survey.Read(); len(got) != 2 {
		t.Errorf("Read() is %v, and internal/health uses it to tell a vanished mount from "+
			"a path that was never there", got)
	}

	// The shadow, which is the whole reason this exists: `prod` is declared
	// twice, the second entry is discarded whole, and nothing else in the
	// console can name it.
	//
	// **One entry and not three.** A context, the cluster it names and the user
	// it authenticates as conventionally share a name, so the ordinary incident
	// shadows all three maps — and three near-identical warnings for it is how
	// an operator learns to skip the section. The folding is asserted here
	// rather than left to the page, because it is the survey that decides.
	if len(survey.Shadowed) != 1 {
		t.Fatalf("one name shadowed in three maps produced %d entries, want 1: %+v\n"+
			"From the rail, from a URL and from the dock the loser simply does not exist — "+
			"there is no error and nothing to click, so this report is its only trace, and "+
			"a report nobody reads is not one", len(survey.Shadowed), survey.Shadowed)
	}
	shadow := survey.Shadowed[0]
	if shadow.Name != "prod" {
		t.Fatalf("the shadowed name is reported as %q", shadow.Name)
	}
	if shadow.Winner != first {
		t.Errorf("the winner is reported as %s, and the merge gives it to %s",
			shadow.Winner, first)
	}
	if len(shadow.Losers) != 1 || shadow.Losers[0] != second {
		t.Errorf("the losers are %v, want just %s", shadow.Losers, second)
	}

	// **All three maps, and the user is the dangerous one.** Two files with a
	// `kubernetes-admin` or `default` entry — which is what every kubeadm
	// cluster is born with — means the second file's context authenticates with
	// the *first* file's credential, against a server the first file never
	// named. Contexts first, because that is the order an operator can act on.
	if got := strings.Join(shadow.Kinds, ","); got != "context,cluster,user" {
		t.Errorf("the shadowed kinds are %q, want \"context,cluster,user\". A missing `user` "+
			"there is a context authenticating as somebody else with nothing saying so", got)
	}

	// The sentence names both files and every kind, because "one name is
	// declared twice" over five files is not something an operator can act on.
	sentence := shadow.Sentence()
	for _, want := range []string{first, second, "prod", "context, cluster and user"} {
		if !strings.Contains(sentence, want) {
			t.Errorf("the sentence %q does not name %s", sentence, want)
		}
	}
}

// The folding is on the *incident* and not on the name: a context shadowed by
// one file and a user shadowed by another are two things to fix, and rolling
// them into one sentence would name the wrong file for one of them.
func TestTwoDifferentShadowsOfOneNameStayTwoEntries(t *testing.T) {
	dir := t.TempDir()
	// `shared` is a context in the first two files and a user in the first and
	// third, so the winner is the same and the *losers* are not.
	first := writeKubeconfigAt(t, filepath.Join(dir, "first"), map[string]string{
		"shared": "https://first.example:6443",
	})
	second := writeKubeconfigAt(t, filepath.Join(dir, "second"), map[string]string{
		"shared": "https://second.example:6443",
	})
	third := filepath.Join(dir, "third")
	doc := "apiVersion: v1\nkind: Config\nusers:\n- name: shared\n  user:\n    token: OTHER\n"
	if err := os.WriteFile(third, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	shadows := Kubeconfig{first, second, third}.Survey().Shadowed
	if len(shadows) != 2 {
		t.Fatalf("got %d entries, want 2 — the context and cluster shadowed by one file, "+
			"the user by two: %+v", len(shadows), shadows)
	}
	for _, s := range shadows {
		if len(s.Losers) == 2 && strings.Join(s.Kinds, ",") != "user" {
			t.Errorf("the entry with two losers reports kinds %v; only the user is shadowed "+
				"by both files", s.Kinds)
		}
	}
}

// A name in one file only is not a shadow, and saying it is would be a warning
// that fires on every start — which is one nobody reads on the day it means
// something.
func TestOneFileDeclaringANameOnceIsNotAShadow(t *testing.T) {
	dir := t.TempDir()
	one := writeKubeconfigAt(t, filepath.Join(dir, "one"), map[string]string{
		"a": "https://a.example:6443",
		"b": "https://b.example:6443",
	})
	if got := (Kubeconfig{one}).Survey().Shadowed; len(got) != 0 {
		t.Errorf("a single kubeconfig reported %v as shadowed", got)
	}
}

// A file that parsed and declares nothing is not a file that could not be read.
func TestAFileThatParsesAndDeclaresNothingIsNotAProblem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(path, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := (Kubeconfig{path}).Survey().Files[0]
	if !file.Read() || file.Absent || file.Problem != "" {
		t.Errorf("an empty kubeconfig was reported as %+v rather than as read and empty", file)
	}
}

// --- the wiring, which is what a helper test cannot see ----------------------

// **Nothing outside this file reads the operator's kubeconfig off disk.**
//
// Four things read the path: the manager's context list, the manager's
// per-context client, the readiness probe, and the per-session kubeconfig the
// dock's terminals and the desktop windows run against. Each doing its own
// `clientcmd.LoadFromFile` would split a list in four places, and a fifth
// reader added later would be a fifth chance to treat the list as one file.
//
// The worst of them to lose is the terminal's, which is why this test is worth
// more than the type alone: a session opened on a context that came from
// the *second* file, built from the first alone, hands `kubectl` a kubeconfig
// that does not contain the context it was asked for.
//
// Reading the source, because the defect is an absence and an absence has no
// code path to drive. Test files are excluded: they legitimately read the
// *session* kubeconfigs this project writes, which is a different file.
func TestNothingReadsTheOperatorsKubeconfigOutsideThisFile(t *testing.T) {
	found := 0
	forEachSourceFile(t, func(path, text string) {
		for i, line := range strings.Split(text, "\n") {
			if !strings.Contains(line, "clientcmd.LoadFromFile(") {
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if filepath.Base(path) == "kubeconfig.go" &&
				filepath.Base(filepath.Dir(path)) == "cluster" {
				found++
				continue
			}
			t.Errorf("%s:%d reads a kubeconfig off disk itself:\n\t%s\n"+
				"KUBECONFIG is a list, and a second reader is a second place for the "+
				"merge to be lost — which shows up as a context on the rail that "+
				"`kubectl` in the dock beside it cannot find. Go through "+
				"cluster.Kubeconfig.Load.", path, i+1, strings.TrimSpace(line))
		}
	})
	if found == 0 {
		t.Error("cluster/kubeconfig.go no longer reads any file, so this test has stopped " +
			"looking at anything")
	}
}

// **KUBECONFIG is read in one place.**
//
// A second reader of the variable would have its own idea of the fallback and
// its own copy of the split. Two splitters are two answers to "what files is
// this?", and the one that is wrong is the one nobody is looking at.
func TestKUBECONFIGIsReadInOnePlace(t *testing.T) {
	readers := map[string]int{}
	forEachSourceFile(t, func(path, text string) {
		for i, line := range strings.Split(text, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			// The variable fetched, in either spelling. A *write* into a child
			// process's environment is not a read and is deliberately allowed:
			// internal/terminal writes a narrowed one per session, which is the
			// whole point of that package.
			if !strings.Contains(line, `os.Getenv("KUBECONFIG")`) &&
				!strings.Contains(line, "os.Getenv(EnvKubeconfig)") &&
				!strings.Contains(line, "os.Getenv(cluster.EnvKubeconfig)") {
				continue
			}
			readers[fmt.Sprintf("%s:%d", path, i+1)]++
		}
	})
	if len(readers) != 1 {
		t.Errorf("KUBECONFIG is read in %d places: %v\nIt must be read by "+
			"cluster.KubeconfigFromEnv and nowhere else, because every reader needs the "+
			"same split on os.PathListSeparator and the same fallback, and a second copy "+
			"is a second chance to take the whole list as one filename",
			len(readers), readers)
	}
}

// forEachSourceFile walks this project's own non-test Go, which both source
// assertions above need.
//
// Rooted by walking up to the module file rather than by a relative path, so
// this keeps working if the package moves.
func forEachSourceFile(t *testing.T, fn func(path, text string)) {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("locating the module root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("%s is not the module root: %v", root, err)
	}

	for _, tree := range []string{"internal", "cmd"} {
		walked := 0
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			walked++
			fn(path, string(raw))
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", tree, err)
		}
		if walked == 0 {
			t.Fatalf("found no Go under %s, so both source assertions in this file are "+
				"passing over nothing", tree)
		}
	}
}
