package binpath

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// sep is the platform's PATH separator, used so these tests read the same way on
// the Windows host this is developed on and in the Linux build container.
var sep = string(os.PathListSeparator)

func TestTheTableNamesTheDirectoriesTheseToolsInstallInto(t *testing.T) {
	home := filepath.Join("home", "operator")
	userBin := filepath.Join(home, ".local", "bin")

	for _, tc := range []struct {
		goos string
		want []string
	}{
		{
			goos: "darwin",
			want: []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin", userBin},
		},
		{
			goos: "linux",
			want: []string{"/usr/local/bin", "/home/linuxbrew/.linuxbrew/bin", userBin},
		},
		// The one that must stay empty. See the package comment: a GUI process
		// on Windows inherits the machine PATH from the registry, so there is no
		// gap, and a fallback nobody needs is a way to change which binary
		// authenticates somebody to production.
		{goos: "windows", want: nil},
		{goos: "plan9", want: nil},
	} {
		got := candidates(tc.goos, home)
		if strings.Join(got, sep) != strings.Join(tc.want, sep) {
			t.Errorf("candidates(%q):\n got %q\nwant %q", tc.goos, got, tc.want)
		}
	}
}

// The Homebrew prefix has to come before /usr/local/bin, and the user-local
// directory has to come after both, and neither is arbitrary — see candidates.
func TestTheUserLocalDirectoryIsLastSoAStalePipInstallCannotWin(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		got := candidates(goos, filepath.Join("home", "operator"))
		if len(got) == 0 {
			t.Fatalf("%s: no candidates at all", goos)
		}
		last := got[len(got)-1]
		if !strings.HasSuffix(last, filepath.Join(".local", "bin")) {
			t.Errorf("%s: the user-local directory must be last, got %q last", goos, last)
		}
		for i, dir := range got {
			if dir == "/usr/local/bin" {
				for _, earlier := range got[:i] {
					if strings.HasSuffix(earlier, filepath.Join(".local", "bin")) {
						t.Errorf("%s: %q sits before /usr/local/bin", goos, earlier)
					}
				}
			}
		}
	}
}

// A home that could not be read costs the one home-relative entry and nothing
// else: the absolute directories are the ones that matter.
func TestAnUnreadableHomeStillYieldsTheAbsoluteDirectories(t *testing.T) {
	got := candidates("darwin", "")
	if len(got) != 3 {
		t.Fatalf("want the three absolute darwin directories, got %q", got)
	}
	for _, dir := range got {
		if !strings.HasPrefix(dir, "/") {
			t.Errorf("%q is not absolute", dir)
		}
	}
}

func TestOnlyDirectoriesThatExistAndAreNotAlreadyOnPathAreAdded(t *testing.T) {
	real := map[string]bool{"/opt/homebrew/bin": true, "/usr/local/bin": true}
	exists := func(p string) bool { return real[filepath.ToSlash(p)] }

	current := "/usr/bin" + sep + "/bin" + sep + "/usr/local/bin"
	got := toAdd([]string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin"}, current, exists)

	want := []string{filepath.Clean("/opt/homebrew/bin")}
	if strings.Join(got, sep) != strings.Join(want, sep) {
		t.Errorf("got %q, want %q — /usr/local/bin is already on PATH and /opt/local/bin does not exist", got, want)
	}
}

// A PATH written with a trailing or doubled separator must not make an entry
// match the empty string, and a list naming one place twice must not add it
// twice.
func TestPathComparisonIgnoresEmptyEntriesAndNeverAddsOneTwice(t *testing.T) {
	exists := func(string) bool { return true }

	got := toAdd([]string{"/opt/homebrew/bin", "/opt/homebrew/bin"}, sep+sep, exists)
	want := []string{filepath.Clean("/opt/homebrew/bin")}
	if strings.Join(got, sep) != strings.Join(want, sep) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The whole point of the design, asserted on the join itself rather than through
// Augment.
//
// Through Augment it cannot be asserted at all on Windows, where nothing is ever
// added and the function returns before the join — a swap of the two operands
// and a removal of the empty-PATH guard both survived as mutations while this
// test did not exist.
func TestTheOperatorsOwnPathComesFirstAndAnEmptyOneGainsNoEmptyEntry(t *testing.T) {
	add := []string{"/opt/homebrew/bin", "/usr/local/bin"}

	got := appended("/pinned/bin", add)
	want := "/pinned/bin" + sep + "/opt/homebrew/bin" + sep + "/usr/local/bin"
	if got != want {
		t.Errorf("the operator's own PATH must come first:\n got %q\nwant %q", got, want)
	}
	if first := filepath.SplitList(got)[0]; first != "/pinned/bin" {
		t.Errorf("first entry is %q, want the operator's own", first)
	}

	empty := appended("", add)
	if want := "/opt/homebrew/bin" + sep + "/usr/local/bin"; empty != want {
		t.Errorf("an empty PATH must gain no empty entry:\n got %q\nwant %q", empty, want)
	}
	for _, dir := range filepath.SplitList(empty) {
		if dir == "" {
			t.Errorf("empty entry in %q — on POSIX that is the working directory", empty)
		}
	}
}

// The same two properties end to end, which is what actually runs on darwin and
// linux. Kept beside the unit test above rather than replaced by it: this one
// proves Augment wires the join up, and only that one can see the join's order.
func TestAugmentAppendsAfterTheOperatorsOwnPathAndNeverBeforeIt(t *testing.T) {
	pinned := t.TempDir()
	t.Setenv("PATH", pinned)

	added := Augment()

	got := os.Getenv("PATH")
	if !strings.HasPrefix(got, pinned) {
		t.Fatalf("the operator's own PATH must stay first: PATH is now %q", got)
	}
	entries := filepath.SplitList(got)
	if entries[0] != pinned {
		t.Errorf("first PATH entry is %q, want the operator's own %q", entries[0], pinned)
	}
	for _, dir := range added {
		if !strings.Contains(got, dir) {
			t.Errorf("Augment reported adding %q but PATH does not contain it", dir)
		}
	}
	// On Windows there is nothing to add at all, which is the platform half of
	// this assertion and is checked here as well as in the table test: the whole
	// mechanism must be inert there, not merely empty-listed.
	if runtime.GOOS == "windows" {
		if len(added) != 0 {
			t.Errorf("Windows must gain nothing, got %q", added)
		}
		if got != pinned {
			t.Errorf("Windows PATH was rewritten: %q", got)
		}
	}
}

// An empty PATH must not gain a leading separator, because an empty entry in a
// POSIX PATH is the current working directory.
func TestAnEmptyPathNeverGainsAnEmptyEntry(t *testing.T) {
	t.Setenv("PATH", "")

	Augment()

	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			t.Fatalf("PATH gained an empty entry: %q", os.Getenv("PATH"))
		}
	}
	if strings.HasPrefix(os.Getenv("PATH"), sep) {
		t.Fatalf("PATH begins with a separator: %q", os.Getenv("PATH"))
	}
}
