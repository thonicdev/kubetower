package launch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheVerboseReportExpiresWithTheZeroSeries.
//
// The gate is the version so that this decision does not have to be remembered:
// a 0.x is handed to a handful of colleagues who are expected to send their log
// back, a 1.0 is not, and thirty lines of environment on every start of a
// released console is noise the day it stops being the answer.
func TestTheVerboseReportExpiresWithTheZeroSeries(t *testing.T) {
	cases := []struct {
		release, setting string
		want             bool
	}{
		{"dev", "", true},
		{"0.6.1", "", true},
		{"0.10.0", "", true},
		{"1.0.0", "", false},
		{"2.3.4", "", false},
		{"", "", true},

		// The override, both ways. `off` is for somebody running a 0.x in
		// earnest; `on` is for asking a 1.x user for the same report without
		// cutting them a special build.
		{"0.6.1", "off", false},
		{"0.6.1", "false", false},
		{"0.6.1", "0", false},
		{"1.0.0", "on", true},
		{"1.0.0", "true", true},
		{"1.0.0", " ON ", true},
		// Something that is neither falls back to the version, rather than
		// being read as "on" -- a typo must not turn a released console verbose.
		{"1.0.0", "verbose", false},
		{"0.6.1", "verbose", true},
	}
	for _, tc := range cases {
		t.Run(tc.release+"/"+tc.setting, func(t *testing.T) {
			if got := verboseLaunch(tc.release, tc.setting); got != tc.want {
				t.Errorf("verboseLaunch(%q, %q) = %v, want %v", tc.release, tc.setting, got, tc.want)
			}
		})
	}
}

// TestNoCredentialReachesTheReport is the one that matters, because this block
// is written to a file and pasted into a chat by somebody who is not reading it
// first.
//
// The rule is allowlist and not denylist: a value is printed only if its name is
// on a closed list. So the assertion is on the *values* -- every secret below is
// given a distinctive one, and none of them may appear anywhere in the report.
func TestNoCredentialReachesTheReport(t *testing.T) {
	const marker = "CANARY-must-not-be-logged"
	environ := []string{
		"KT_PASSWORD_HASH=$2a$14$" + marker,
		"KT_SESSION_SECRET=" + marker,
		"AWS_SECRET_ACCESS_KEY=" + marker,
		"AWS_SESSION_TOKEN=" + marker,
		"AWS_ACCESS_KEY_ID=AKIA" + marker,
		"HTTPS_PROXY=http://jean:" + marker + "@proxy.example:8080",
		"KT_LISTEN=127.0.0.1:5823",
		"AWS_PROFILE=prod-devops",
	}

	report := strings.Join(envReport(environ), "\n")
	if strings.Contains(report, marker) {
		t.Fatalf("a credential is in the report:\n%s", report)
	}

	// And the other half, which a report that printed nothing would also pass:
	// the safe values have to be there, because a block with no facts in it is
	// not a diagnosis.
	for _, want := range []string{"KT_LISTEN=127.0.0.1:5823", "AWS_PROFILE=prod-devops"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not carry %q, so it answers nothing:\n%s", want, report)
		}
	}
	// The secrets are still *named*. "AWS_SECRET_ACCESS_KEY is set" answers a
	// great deal -- it is how you learn that this process is authenticating with
	// something the kubeconfig never named -- and reveals nothing.
	for _, want := range []string{"KT_PASSWORD_HASH", "AWS_SESSION_TOKEN", "HTTPS_PROXY"} {
		if !strings.Contains(report, want) {
			t.Errorf("%s is set and the report does not even name it:\n%s", want, report)
		}
	}
}

// TestASecretIsNeverPrintedEvenIfSomebodyAllowlistsIt.
//
// Two guards over one case, and neither is established
// until it is tested where the other cannot reach. The allowlist above is a
// list, and a list gets rows added to it by somebody in a hurry -- so this puts
// the row in and proves the second guard catches it. Without this the
// `secretish` half is dead code that nothing can reach.
func TestASecretIsNeverPrintedEvenIfSomebodyAllowlistsIt(t *testing.T) {
	const marker = "CANARY-allowlisted-by-mistake"
	const name = "KT_FUTURE_SIGNING_KEY"

	printableEnv[name] = true
	t.Cleanup(func() { delete(printableEnv, name) })

	report := strings.Join(envReport([]string{name + "=" + marker}), "\n")
	if strings.Contains(report, marker) {
		t.Fatalf("an allowlisted name whose value is plainly a secret was printed:\n%s", report)
	}
	if !strings.Contains(report, name) {
		t.Errorf("it was not even named:\n%s", report)
	}
}

// TestTheSecondGuardKnowsASecretWhenItSeesOne, driven by name alone. Its own
// test because the one above can only reach it through the allowlist, and this
// is the vocabulary the whole guard is.
func TestTheSecondGuardKnowsASecretWhenItSeesOne(t *testing.T) {
	for _, name := range []string{
		"KT_PASSWORD_HASH", "KT_SESSION_SECRET", "AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN", "AWS_ACCESS_KEY_ID", "KT_ANY_FUTURE_TOKEN",
		"SOME_APIKEY", "MY_PRIVATE_THING", "GITHUB_TOKEN", "DB_PASSWD",
	} {
		if !secretish(name) {
			t.Errorf("%s does not read as a secret, so allowlisting it would print it", name)
		}
	}
	// And it must not be so eager that the useful variables fall to it: a guard
	// that refuses everything makes the report empty, which is the failure this
	// whole file exists to avoid.
	for _, name := range []string{
		"KUBECONFIG", "KT_LISTEN", "AWS_PROFILE", "AWS_REGION", "KT_EXTRAS",
		"KT_READONLY_CLUSTERS", "HOME", "AWS_CONFIG_FILE",
	} {
		if secretish(name) {
			t.Errorf("%s reads as a secret, so it will never be printed and the report loses it",
				name)
		}
	}
}

// TestTheReportSaysWhatABugReportNeeds.
//
// Not a style assertion. Each string below is a fact a log needs to carry for
// a launch failure to be diagnosed from it, and the report exists for that and
// nothing else -- so if one of them stops being produced, the file has quietly
// become decoration.
func TestTheReportSaysWhatABugReportNeeds(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "PASSWORD-ONCE.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seeding the state directory: %v", err)
	}

	lines := launchReport(launchFacts{
		Release:   "0.6.1",
		Version:   "kubetower 0.6.1 (built 2026-09-07, go1.26.7 darwin/arm64)",
		Exe:       "/Users/jean/Applications/KubeTower.app/Contents/MacOS/kubetower",
		GOOS:      "darwin",
		Args:      []string{"--app"},
		Console:   true,
		Bundled:   true,
		WindowWhy: "opening a window: this executable is inside an application bundle",
		StateDir:  dir,
		Environ:   []string{"AWS_PROFILE=prod-devops", "KUBECONFIG=/Users/jean/.kube/config"},
		Look: func(name string) (string, error) {
			if name == "aws" {
				return "/opt/homebrew/bin/aws", nil
			}
			return "", errors.New("not found")
		},
	})
	report := strings.Join(lines, "\n")

	for _, tc := range []struct{ why, want string }{
		{"the launcher", "application bundle"},
		{"the window decision is said", "opening a window"},
		{"internal/binpath: a launcher's PATH", "kubectl: not found"},
		{"internal/binpath: what did resolve", "/opt/homebrew/bin/aws"},
		{"as whom a credential plugin signs", "AWS_PROFILE=prod-devops"},
		{"which kubeconfig", "KUBECONFIG=/Users/jean/.kube/config"},
		{"nobody has ever logged in", "PASSWORD-ONCE.txt is still there"},
		{"console is not measured off Windows", "assumed"},
		{"the block can be found in a long log", "launch report"},
		{"and its end, so a paste is complete", "end of launch report"},
	} {
		if !strings.Contains(report, tc.want) {
			t.Errorf("%s: the report does not say %q\n%s", tc.why, tc.want, report)
		}
	}
}

// TestAnUnrecognisedArgumentIsCountedAndNotEchoed.
//
// Nothing in this CLI takes a secret on the command line today. A report that
// echoed whatever it was handed would be a hole waiting for the flag that does,
// and this is the cheapest possible time to close it.
func TestAnUnrecognisedArgumentIsCountedAndNotEchoed(t *testing.T) {
	got := argvNote([]string{"--app", "--token=hunter2", "--bundle=/tmp/out"})
	if strings.Contains(got, "hunter2") {
		t.Errorf("an unrecognized argument was echoed: %q", got)
	}
	if !strings.Contains(got, "--app") {
		t.Errorf("the recognized flags are gone, so the line answers nothing: %q", got)
	}
	if !strings.Contains(got, "1 not recognized") {
		t.Errorf("the unrecognized argument is not even counted: %q", got)
	}
	if !strings.Contains(got, "--bundle=...") {
		t.Errorf("--bundle should be named without its path: %q", got)
	}
	if got := argvNote(nil); got != "(none)" {
		t.Errorf("argvNote(nil) = %q, want %q", got, "(none)")
	}
}

// TestAStateDirectoryThatCannotBeListedSaysSo. In its smallest
// form: an empty listing and a failed one are different answers, and the second
// is the one that explains why nothing is remembered between starts.
func TestAStateDirectoryThatCannotBeListedSaysSo(t *testing.T) {
	lines := strings.Join(stateReport(filepath.Join(t.TempDir(), "does-not-exist")), "\n")
	if !strings.Contains(lines, "cannot be listed") {
		t.Errorf("a missing state directory is reported as an empty one:\n%s", lines)
	}

	empty := strings.Join(stateReport(t.TempDir()), "\n")
	if !strings.Contains(empty, "first run") {
		t.Errorf("an empty state directory is not reported as a first run:\n%s", empty)
	}
	if strings.Contains(empty, "cannot be listed") {
		t.Errorf("an empty state directory is reported as unreadable:\n%s", empty)
	}
}

// TestTheAllowlistAndTheSecondGuardDoNotContradictEachOther.
//
// A row on `printableEnv` that `secretish` also matches is a row that reads as
// "printed" and never prints. That is not a leak -- the safe direction -- but it
// is a lie about what this file does: `AWS_SHARED_CREDENTIALS_FILE` on the
// allowlist would be suppressed by the substring `CREDENTIAL` while
// `AWS_CONFIG_FILE` beside it printed.
//
// The second guard wins by design, so this refuses the *list* rather than the
// guard: a name the guard claims does not belong on the allowlist at all.
func TestTheAllowlistAndTheSecondGuardDoNotContradictEachOther(t *testing.T) {
	for name := range printableEnv {
		if secretish(name) {
			t.Errorf("%s is on printableEnv and is also caught by secretish, so the row reads "+
				"as printed and never prints. Take it off the list, or rename what it is "+
				"called", name)
		}
	}
}

// TestTheLaunchReportRunsAfterThePathIsAugmented.
//
// `toolReport` resolves kubectl, helm, aws and claude against this process's
// PATH and its doc comment says it runs after `internal/binpath` has augmented
// it. Before the augmentation, a KubeTower.app opened from the Dock would print
// `PATH: 4 entries` and four `not found`s two lines before the process repaired
// both -- a block an operator pastes, asserting as fact the exact misdiagnosis
// `internal/binpath` was written to prevent.
//
// Read from the source because the seam is inside `main()`, which needs a
// kubeconfig, a state directory and a listener to reach. It cannot see that the
// report is right; it can see that the two statements are still in the order the
// report's own promise depends on.
func TestTheLaunchReportRunsAfterThePathIsAugmented(t *testing.T) {
	source, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatalf("reading run.go: %v", err)
	}
	text := string(source)

	augment := strings.Index(text, "binpath.Augment()")
	report := strings.Index(text, "launchReport(launchFacts{")
	if augment < 0 {
		t.Fatal("nothing augments PATH any more; internal/binpath's finding is back")
	}
	if report < 0 {
		t.Fatal("the launch report is gone")
	}
	if report < augment {
		t.Error("the launch report is emitted before binpath.Augment, so it reports the PATH " +
			"and the tools of a process that has not yet fixed either -- which is the " +
			"misdiagnosis internal/binpath exists to prevent, now printed as fact")
	}
}

// TestAnEmptyValueIsNotReportedAsSet.
//
// Every optional variable in compose.dev.yaml is written `NAME: ${NAME:-}`, so
// inside a container it is present and empty. A report listing them all --
// `KT_PASSWORD_HASH` among them, under "values not printed" -- would let a
// reader conclude a password hash is configured while the process is in the
// middle of drawing a first-run password.
func TestAnEmptyValueIsNotReportedAsSet(t *testing.T) {
	report := strings.Join(envReport([]string{
		"KT_PASSWORD_HASH=",
		"KT_ARCHIVE=",
		"KT_EXTRAS=",
		"KT_LISTEN=:5823",
	}), "\n")

	for _, empty := range []string{"KT_PASSWORD_HASH", "KT_ARCHIVE", "KT_EXTRAS"} {
		if strings.Contains(report, empty) {
			t.Errorf("%s is empty and the report calls it set:\n%s", empty, report)
		}
	}
	// The other half, which dropping everything would also pass.
	if !strings.Contains(report, "KT_LISTEN=:5823") {
		t.Errorf("a variable that does have a value went with the empty ones:\n%s", report)
	}
}
