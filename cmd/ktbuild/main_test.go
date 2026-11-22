package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestOnlyWindowsIsLinkedForTheGUISubsystem. The flag selects the subsystem on
// a PE file and means nothing anywhere else; on another target it is at best
// ignored and at worst a link error nobody would recognize.
func TestOnlyWindowsIsLinkedForTheGUISubsystem(t *testing.T) {
	for _, tc := range []struct {
		goos string
		want bool
	}{
		{"windows", true},
		{"linux", false},
		{"darwin", false},
	} {
		got := strings.Contains(ldflags(target{goos: tc.goos, goarch: "amd64"}), windowsGUI)
		if got != tc.want {
			t.Errorf("%s: %s present = %v, want %v", tc.goos, windowsGUI, got, tc.want)
		}
	}
}

// TestAnUnstampedBuildSaysDevAndNoBuildStampsAnEdition. A version that nobody
// gave must not be invented. There is no edition, and `-X` on a
// name that does not exist is silently ignored, so a stamp of one would read as
// a setting that does something.
func TestAnUnstampedBuildSaysDevAndNoBuildStampsAnEdition(t *testing.T) {
	plain := ldflags(target{goos: "linux", goarch: "amd64"})
	if strings.Contains(plain, stampVersion) || strings.Contains(plain, stampBuilt) {
		t.Errorf("an unstamped build stamps a version or a date: %s", plain)
	}
	stamped := ldflags(target{goos: "linux", goarch: "arm64", version: "0.1.0-rc.1", built: "2026-10-10"})
	for _, want := range []string{stampVersion + "0.1.0-rc.1", stampBuilt + "2026-10-10"} {
		if !strings.Contains(stamped, want) {
			t.Errorf("a release build does not carry %q: %s", want, stamped)
		}
	}
	for _, flags := range []string{plain, stamped} {
		if strings.Contains(flags, "launch.edition=") {
			t.Errorf("a build stamps an edition, and there is none: %s", flags)
		}
	}
}

// TestAStampCannotCarryAnotherFlag. Every value ends up inside -ldflags, which
// is split on spaces.
func TestAStampCannotCarryAnotherFlag(t *testing.T) {
	for _, bad := range []target{
		{goos: "linux", version: "v0.1.0"},
		{goos: "linux", version: "0.1"},
		{goos: "linux", version: "0.1.0 -X main.x=y"},
		{goos: "linux", built: "2026-10-10T00:00:00Z"},
		{goos: "linux", built: "today"},
	} {
		if err := bad.validate(); err == nil {
			t.Errorf("accepted version %q built %q", bad.version, bad.built)
		}
	}
	for _, good := range []target{
		{goos: "linux", version: "0.1.0"},
		{goos: "linux", version: "0.1.0-rc.1", built: "2026-10-10"},
	} {
		if err := good.validate(); err != nil {
			t.Errorf("refused version %q built %q: %v", good.version, good.built, err)
		}
	}
}

// TestTheBinaryCheckTellsAGUIBuildFromAConsoleOne. The check is what turns
// "the flag was passed" into "the binary is a GUI program", so it is run on two
// real binaries: a stdlib-only program linked both ways, cross-compiled from
// whatever this test runs on.
func TestTheBinaryCheckTellsAGUIBuildFromAConsoleOne(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go is not on PATH, and this test builds two binaries with it: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module hello\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := func(name string, flags string) string {
		t.Helper()
		out := filepath.Join(dir, name)
		cmd := exec.Command(gobin, "build", "-ldflags", flags, "-o", out, ".")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=windows", "GOARCH=amd64", "GOTOOLCHAIN=local", "GOFLAGS=")
		if raw, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("building %s: %v\n%s", name, err, raw)
		}
		return out
	}
	gui := link("gui.exe", "-s -w "+windowsGUI)
	console := link("console.exe", "-s -w")

	if err := checkWindowsBinary(gui, ""); err != nil {
		t.Errorf("a binary linked %s was refused: %v", windowsGUI, err)
	}
	if err := checkWindowsBinary(console, ""); err == nil {
		t.Errorf("a console-subsystem binary passed the GUI check")
	}
	// No version resource in either, so a version asked for must be missing.
	if err := checkWindowsBinary(gui, "9.9.9"); err == nil {
		t.Errorf("a binary with no version resource passed as carrying 9.9.9")
	}
}
