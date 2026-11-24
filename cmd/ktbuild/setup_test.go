package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheSetupIsAWindowsAmd64Thing. The wizard wraps the one Windows binary a
// release ships; there is no arm64 Windows build (release.yml's header says
// why), and -stage means nothing without -setup.
func TestTheSetupIsAWindowsAmd64Thing(t *testing.T) {
	for _, bad := range []target{
		{goos: "linux", goarch: "amd64", setup: true},
		{goos: "darwin", goarch: "arm64", setup: true},
		{goos: "windows", goarch: "arm64", setup: true},
		{goos: "windows", goarch: "amd64", stage: "somewhere"},
		{goos: "windows", goarch: "amd64", setup: true, version: "70000.0.0"},
	} {
		if err := bad.validate(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	for _, good := range []target{
		{goos: "windows", goarch: "amd64", setup: true},
		{goos: "windows", goarch: "amd64", setup: true, stage: "stage", version: "0.1.0-rc.1", built: "2026-10-15"},
	} {
		if err := good.validate(); err != nil {
			t.Errorf("refused %+v: %v", good, err)
		}
	}
}

// TestNoBuildLinksAnInstallerStamp. The setup is a separate wizard, not this
// binary with `installMode=setup` stamped into it, and a stamp left in the
// flags would name a variable that does not exist - which the linker accepts
// silently.
func TestNoBuildLinksAnInstallerStamp(t *testing.T) {
	for _, tc := range []target{
		{goos: "windows", goarch: "amd64"},
		{goos: "windows", goarch: "amd64", setup: true, version: "0.1.0"},
	} {
		if flags := ldflags(tc); strings.Contains(flags, "installMode") {
			t.Errorf("%+v links %s", tc, flags)
		}
	}
}

// TestTheVersionResourceGetsFourNumbers. A version resource's fixed part is
// four 16-bit numbers; the string part carries the version as given.
func TestTheVersionResourceGetsFourNumbers(t *testing.T) {
	for in, want := range map[string]string{
		"":           "0.0.0.0",
		"0.1.0":      "0.1.0.0",
		"0.1.0-rc.1": "0.1.0.0",
		"12.34.56":   "12.34.56.0",
	} {
		got, err := viVersion(in)
		if err != nil || got != want {
			t.Errorf("viVersion(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"0.1", "1.2.3.4", "65536.0.0", "a.b.c"} {
		if got, err := viVersion(bad); err == nil {
			t.Errorf("viVersion(%q) = %q, want a refusal", bad, got)
		}
	}
}

// TestMakensisIsToldEverythingTheScriptRequires. The script refuses to build
// without VERSION, VIVERSION, OUTFILE and ICON, and a warning has to fail the
// build rather than scroll past.
func TestMakensisIsToldEverythingTheScriptRequires(t *testing.T) {
	args, err := makensisArgs("/src", "/out/kubetower-0.1.0-setup.exe", "0.1.0-rc.1", true)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-WX", "-NOCD", "-DVERSION=0.1.0-rc.1", "-DVIVERSION=0.1.0.0",
		"-DOUTFILE=/out/kubetower-0.1.0-setup.exe", "-DICON=", "-DNOTICES"} {
		if !strings.Contains(joined, want) {
			t.Errorf("makensis is not given %q: %s", want, joined)
		}
	}
	if last := args[len(args)-1]; !strings.HasSuffix(filepath.ToSlash(last), setupScript) {
		t.Errorf("the script is not the last argument, so a define after it is ignored: %s", joined)
	}

	dev, err := makensisArgs("/src", "/out/kubetower-setup.exe", "", false)
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(dev, " ")
	if !strings.Contains(joined, "-DVERSION=dev") || strings.Contains(joined, "-DNOTICES") {
		t.Errorf("a build with no version and no notices file is described as: %s", joined)
	}
}

// TestTheScriptDeclaresEveryDefineThisPasses. The two halves of one interface,
// in two languages: a define the script never reads is a setting with no
// effect, and one it requires and is not given stops the build.
func TestTheScriptDeclaresEveryDefineThisPasses(t *testing.T) {
	script := readScript(t)
	for _, name := range []string{"VERSION", "VIVERSION", "OUTFILE", "ICON", "NOTICES", "LICENSE_FILE"} {
		if !strings.Contains(script, "${"+name+"}") && !strings.Contains(script, "!ifdef "+name) {
			t.Errorf("%s does not read the define %s", setupScript, name)
		}
	}
	if !strings.Contains(script, `!define EXE "`+setupExe+`"`) || !strings.Contains(script, `File "${EXE}"`) ||
		!strings.Contains(script, `File "`+setupNotices+`"`) {
		t.Errorf("%s does not install %s and %s from the stage", setupScript, setupExe, setupNotices)
	}
	for _, name := range setupLicences {
		if !strings.Contains(script, `File "`+name+`"`) {
			t.Errorf("%s does not install %s", setupScript, name)
		}
	}
}

// TestTheSetupIsBuiltAndReadBack runs makensis for real, around a small GUI
// program standing in for kubetower.exe, and reads the result back the way a
// release does. Skipped where makensis is not installed - the pool and most
// development machines - and said so: ci.yml's `setup` job installs it and
// runs this with KT_REQUIRE_MAKENSIS set, which turns that skip into a failure,
// so a runner that lost makensis cannot pass this by skipping it.
func TestTheSetupIsBuiltAndReadBack(t *testing.T) {
	makensis, err := exec.LookPath("makensis")
	if err != nil {
		if os.Getenv("KT_REQUIRE_MAKENSIS") != "" {
			t.Fatalf("KT_REQUIRE_MAKENSIS is set and makensis is not on PATH: %v", err)
		}
		t.Skipf("makensis is not on PATH, so building the setup is UNCHECKED here (ci.yml's setup job runs it): %v", err)
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go is not on PATH: %v", err)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module hello\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	cmd := exec.Command(gobin, "build", "-ldflags", "-s -w "+windowsGUI, "-o", filepath.Join(stage, setupExe), ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=windows", "GOARCH=amd64", "GOTOOLCHAIN=local", "GOFLAGS=")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the stand-in: %v\n%s", err, raw)
	}
	for _, name := range append([]string{setupNotices}, setupLicences...) {
		from := filepath.Join(root, name)
		if name == setupNotices {
			from = filepath.Join(root, "LICENSE") // any text will do here
		}
		raw, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, name), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out := filepath.Join(t.TempDir(), "kubetower-9.8.7-rc.1-setup.exe")
	if err := makeSetup(makensis, root, stage, out, "9.8.7-rc.1"); err != nil {
		t.Fatalf("makeSetup: %v", err)
	}
	if err := checkSetup(out, "9.8.7-rc.1"); err != nil {
		t.Errorf("the setup does not read back: %v", err)
	}
	if err := checkSetup(out, "1.2.3"); err == nil {
		t.Error("the read-back passed a setup as carrying a version it does not")
	}
	// A prefix of the version is not the version: 9.8.7 is in 9.8.7-rc.1.
	if err := checkSetup(out, "9.8.7"); err == nil {
		t.Error("the read-back passed 9.8.7 on a setup that says 9.8.7-rc.1")
	}
}

// TestTheReadBackRefusesAConsoleProgram. The setup is a GUI program; a console
// one would open a cmd window behind the wizard. Exercised on a console
// stand-in, since makensis only ever writes GUI stubs and a check that only
// ever sees what it accepts is never tested.
func TestTheReadBackRefusesAConsoleProgram(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go is not on PATH: %v", err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module hello\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "console.exe")
	cmd := exec.Command(gobin, "build", "-ldflags", "-s -w", "-o", out, ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=windows", "GOARCH=amd64", "GOTOOLCHAIN=local", "GOFLAGS=")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the stand-in: %v\n%s", err, raw)
	}
	err = checkSetup(out, "dev")
	if err == nil || !strings.Contains(err.Error(), "GUI subsystem") {
		t.Errorf("checkSetup on a console program = %v, want a refusal naming the subsystem", err)
	}
}

func readScript(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(setupScript)))
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s is missing, and -setup builds nothing without it", setupScript)
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestEveryDialogOfTheSetupHasASilentAnswer. The console updates a per-user
// install by running the setup with /S (internal/updater, #50): a MessageBox
// with no /SD would wait for a click nobody can see.
func TestEveryDialogOfTheSetupHasASilentAnswer(t *testing.T) {
	script := readScript(t)
	boxes := 0
	for i, line := range strings.Split(script, "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, ";") || !strings.Contains(code, "MessageBox ") {
			continue
		}
		boxes++
		if !strings.Contains(code, "/SD ") {
			t.Errorf("%s:%d: a MessageBox with no /SD answer blocks a silent install: %s", setupScript, i+1, code)
		}
	}
	if boxes == 0 {
		t.Fatalf("%s has no MessageBox, so this checked nothing", setupScript)
	}
}
