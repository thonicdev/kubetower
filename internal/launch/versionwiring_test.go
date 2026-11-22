package launch

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestTheSettingsPageIsHandedTheSameLineTheFlagPrints.
//
// The Settings page says which build it is running, and the requirement is not
// "show a version" — it is **show the same string `--version` prints**,
// because two spellings of one fact drift.
//
// The compiler cannot help here. `Version: version` compiles, renders, and puts
// `0.4.1` on the page where `kubetower 0.4.1 (abc1234, built …, go1.26.7
// windows/amd64)` belongs — a support exchange then gets half the answer and
// nobody notices, because half an answer looks like an answer. Neither can a
// rendering test: it asserts that whatever was handed over is drawn, which is
// exactly the thing that stays true after the mutation.
//
// So this reads the assembly. It is the same gesture `internal/web`'s extras
// guard makes — walk the source for the shape, rather than write down today's
// list — and it fails on any wiring that composes the line from the parts
// instead of asking the one function that composes it.
func TestTheSettingsPageIsHandedTheSameLineTheFlagPrints(t *testing.T) {
	raw, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatalf("reading the assembly: %v", err)
	}
	source := string(raw)

	// Whitespace-insensitive: gofmt aligns the field names in that literal and
	// the alignment moves whenever a longer field is added beside it.
	for _, want := range []struct {
		what  string
		shape *regexp.Regexp
		why   string
	}{
		{
			what:  "Version",
			shape: regexp.MustCompile(`Version:\s*versionLine\(\),`),
			why: "the page must print the line `--version` prints, from the one function " +
				"that composes it — not the `version` variable, and not a string built here",
		},
		{
			what:  "Development",
			shape: regexp.MustCompile(`Development:\s*developmentBuild\(\),`),
			why: "whether this binary was stamped is decided where the sentinel lives, " +
				"not by a page reading `dev` back out of a string it was handed",
		},
	} {
		if !want.shape.MatchString(source) {
			t.Errorf("internal/launch/run.go does not fill web.SetupInfo.%s as `%s`: %s",
				want.what, want.shape, want.why)
		}
	}

	// And the flag is still the other reader of the same function, which is the
	// half of "one source" that lives outside the struct.
	if !strings.Contains(source, "fmt.Println(versionLine())") {
		t.Errorf("`--version` no longer prints versionLine(), so the page and the flag have " +
			"stopped being the same fact")
	}
}

// TestADevelopmentBuildSaysSoRatherThanSayingNothing.
//
// `developmentBuild` is one comparison, and the thing worth holding is that it
// is a comparison against the value `version` actually defaults to. Writing the
// sentinel out a second time is how a build that was never stamped comes to be
// described as a release — the page would then invite somebody to quote a
// version in a bug report that no artifact carries.
func TestADevelopmentBuildSaysSoRatherThanSayingNothing(t *testing.T) {
	if !developmentBuild() {
		t.Fatalf("the test binary reports itself as a stamped release (version %q). Nothing "+
			"stamps a `go test` build, so this is the sentinel drifting from the default",
			version)
	}
	if !strings.Contains(versionLine(), devVersion) {
		t.Errorf("versionLine() is %q and does not contain %q, so what the page prints and "+
			"what it says about it come from two different facts", versionLine(), devVersion)
	}

	restore := version
	t.Cleanup(func() { version = restore })
	version = "0.4.1"
	if developmentBuild() {
		t.Errorf("a stamped build still reports itself as a development one, so a release " +
			"would tell the operator its version is not real")
	}
}
