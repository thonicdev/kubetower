// Command ktbuild builds `cmd/kubetower` the one way it ships.
//
// Separate build paths drift apart (one that forgets `-H windowsgui` ships a
// Windows binary that opens a cmd window behind the application), so the flags
// live here and every build calls this: the Taskfile, ci.yml and release.yml.
// A Go program because on Windows neither bash nor make can be assumed.
//
//   - Links `-s -w`, and stamps `version` and `built` when given; an unstamped
//     build says `dev`.
//   - On Windows, links `-H windowsgui`, then reads the binary back and refuses
//     it unless its PE subsystem is the GUI one. console_windows.go is the other
//     half of that flag.
//   - With `-setup`, builds the Windows setup wizard with `makensis` around the
//     windows/amd64 binary, or with `-stage` around one already built
//     (setup.go).
//   - With `-setup` or a `-version`, a build that ships, refuses a tree whose
//     console was never built and reads the binary back for it;
//     `-check-console FILE` reads back a binary built elsewhere (console.go).
//   - With `-os darwin -arch universal`, links both darwin architectures and
//     writes one fat Mach-O itself, with no host tool, then reads both slices
//     back (fat.go).
//   - With a `-version` on windows/amd64, regenerates the version resource
//     Explorer shows from that version, checks it is in the binary, and puts the
//     committed resource (which says `dev`) back. That run needs the network
//     once, for go-winres.
//
// It signs nothing, names no release asset (release.yml does), and builds no
// macOS bundle or disk image (`kubetower --bundle` makes the bundle).
package main

import (
	"bytes"
	"debug/pe"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf16"
)

// The stamps and the subsystem flag, written out in full: tests in
// internal/launch read this file for them.
//
// `-X` with a path that names nothing is not an error: the linker ignores it and
// the binary calls itself `dev`. The variables live in internal/launch.
const (
	stampVersion = "-X github.com/thonicdev/kubetower/internal/launch.version="
	stampBuilt   = "-X github.com/thonicdev/kubetower/internal/launch.built="
	windowsGUI   = "-H windowsgui"

	// winresTool regenerates the Windows resource object. Pinned, and run with
	// `go run pkg@version`, so it leaves nothing in go.mod.
	winresTool = "github.com/tc-hib/go-winres@v0.3.3"
)

// The shapes a stamped value may take. Each ends up inside `-ldflags`, which is
// split on spaces, so anything wider could add a flag. The version is
// release.yml's tag rule without the `v`.
var (
	versionShape = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)
	builtShape   = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
)

type target struct {
	goos, goarch string
	version      string // empty: unstamped, says `dev`
	built        string // empty: unstamped, says `unknown`
	setup        bool   // the Windows setup wizard, setup.go
	stage        string // with setup: wrap the kubetower.exe already here
}

func main() {
	var t target
	var out, check string
	flag.StringVar(&t.goos, "os", runtime.GOOS, "target GOOS")
	flag.StringVar(&t.goarch, "arch", runtime.GOARCH, "target GOARCH, or universal with -os darwin")
	flag.StringVar(&t.version, "version", "", "release version without the v, e.g. 0.1.0 (empty: the build says dev)")
	flag.StringVar(&t.built, "built", "", "release date, YYYY-MM-DD (empty: the build says unknown)")
	flag.BoolVar(&t.setup, "setup", false, "build the Windows setup wizard with makensis (packaging/windows/kubetower.nsi)")
	flag.StringVar(&t.stage, "stage", "", "with -setup: wrap the kubetower.exe in this folder, beside LICENSE, LICENSING.md "+
		"and THIRD_PARTY_NOTICES.txt, instead of building one")
	flag.StringVar(&out, "o", "", "where to write the binary (required)")
	flag.StringVar(&check, "check-console", "", "build nothing: read the kubetower binary at this path back and "+
		"refuse it unless it embeds the console")
	flag.Parse()
	if flag.NArg() > 0 {
		fail(fmt.Errorf("unexpected arguments %q", flag.Args()))
	}
	if check != "" {
		if err := checkEmbedsConsole(check); err != nil {
			fail(fmt.Errorf("%s: %w", check, err))
		}
		fmt.Printf("ok   %s embeds the console\n", check)
		return
	}
	if err := build(t, out); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "ktbuild: "+err.Error())
	os.Exit(1)
}

func (t target) validate() error {
	if t.version != "" && !versionShape.MatchString(t.version) {
		return fmt.Errorf("-version %q is not MAJOR.MINOR.PATCH[-pre] (no leading v)", t.version)
	}
	if t.built != "" && !builtShape.MatchString(t.built) {
		return fmt.Errorf("-built %q is not YYYY-MM-DD", t.built)
	}
	if t.setup && (t.goos != "windows" || t.goarch != "amd64") {
		return fmt.Errorf("-setup builds the Windows setup for windows/amd64, and the target is %s/%s", t.goos, t.goarch)
	}
	if t.stage != "" && !t.setup {
		return errors.New("-stage names what a setup wraps, and this is not -setup")
	}
	if t.setup {
		if _, err := viVersion(t.version); err != nil {
			return err
		}
	}
	if t.goarch == universalArch && t.goos != "darwin" {
		return fmt.Errorf("-arch universal is a macOS format, and the target is %s", t.goos)
	}
	return nil
}

// ldflags is the whole of what makes a build the shipped one.
func ldflags(t target) string {
	flags := []string{"-s", "-w"}
	if t.version != "" {
		flags = append(flags, stampVersion+t.version)
	}
	if t.built != "" {
		flags = append(flags, stampBuilt+t.built)
	}
	// Never without console_windows.go's AttachConsole: the flag alone makes
	// `kubetower.exe --version` in a terminal print nothing.
	if t.goos == "windows" {
		flags = append(flags, windowsGUI)
	}
	return strings.Join(flags, " ")
}

// stampsResource reports whether this build regenerates the version resource.
// Only windows/amd64 carries one (`rsrc_windows_amd64.syso`), and only a build
// with a version has something to put in it.
func (t target) stampsResource() bool {
	return t.goos == "windows" && t.goarch == "amd64" && t.version != ""
}

func build(t target, out string) error {
	if out == "" {
		return errors.New("-o is required: name where the binary goes (outside the working tree; see Taskfile.yaml BUILD_DIR)")
	}
	if err := t.validate(); err != nil {
		return err
	}
	// Before linking or looking up makensis, so a console nobody built fails
	// fast. A -stage binary is read back by buildSetup instead.
	if t.shipsConsole() && t.stage == "" {
		root, err := moduleRoot()
		if err != nil {
			return err
		}
		if err := consoleBuilt(root); err != nil {
			return err
		}
	}
	if t.goarch == universalArch {
		return buildUniversal(t, out)
	}
	if t.setup {
		return buildSetup(t, out)
	}
	out, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	root, err := moduleRoot()
	if err != nil {
		return err
	}

	if t.stampsResource() {
		restore, err := stampResource(root, t.version)
		if err != nil {
			return err
		}
		defer restore()
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", ldflags(t), "-o", out, "./cmd/kubetower")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+t.goos, "GOARCH="+t.goarch)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build %s/%s: %w", t.goos, t.goarch, err)
	}

	if t.goos == "windows" {
		want := ""
		if t.stampsResource() {
			want = t.version
		}
		if err := checkWindowsBinary(out, want); err != nil {
			return fmt.Errorf("%s: %w", out, err)
		}
	}
	if t.shipsConsole() {
		if err := checkEmbedsConsole(out); err != nil {
			return fmt.Errorf("%s: %w", out, err)
		}
	}

	version := t.version
	if version == "" {
		version = "dev"
	}
	fmt.Printf("built binary %s/%s %s -> %s\n", t.goos, t.goarch, version, out)
	return nil
}

// moduleRoot is where `./cmd/kubetower` resolves, whatever directory this was
// started from. A variable so a test can point it at a tree with no console.
var moduleRoot = func() (string, error) {
	raw, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}
	gomod := strings.TrimSpace(string(raw))
	if gomod == "" || gomod == os.DevNull {
		return "", errors.New("not inside the kubetower module: run this from the repository")
	}
	return filepath.Dir(gomod), nil
}

// stampResource regenerates internal/launch's resource object with version and
// returns what puts the committed one back. A process killed in between leaves
// the regenerated object behind, which `git status` shows.
func stampResource(root, version string) (func(), error) {
	dir := filepath.Join(root, "internal", "launch")
	syso := filepath.Join(dir, "rsrc_windows_amd64.syso")
	committed, err := os.ReadFile(syso)
	if err != nil {
		return nil, fmt.Errorf("the committed resource object: %w", err)
	}
	restore := func() {
		if err := os.WriteFile(syso, committed, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "ktbuild: could not put back %s: %v - `git checkout -- %s` restores it\n", syso, err, syso)
		}
	}
	cmd := exec.Command("go", "run", winresTool, "make",
		"--in", filepath.Join("winres", "winres.json"), "--arch", "amd64",
		"--product-version", version, "--file-version", version)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		restore()
		return nil, fmt.Errorf("go-winres could not stamp %s into the version resource: %w", version, err)
	}
	return restore, nil
}

// checkWindowsBinary reads a built .exe back: its subsystem must be the GUI
// one, and a stamped version must be in it as a VERSIONINFO UTF-16 string.
func checkWindowsBinary(path, version string) error {
	f, err := pe.Open(path)
	if err != nil {
		return fmt.Errorf("not a PE file: %w", err)
	}
	var subsystem uint16
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		subsystem = oh.Subsystem
	case *pe.OptionalHeader32:
		subsystem = oh.Subsystem
	default:
		f.Close()
		return errors.New("no optional header, so no subsystem to check")
	}
	f.Close()
	if subsystem != pe.IMAGE_SUBSYSTEM_WINDOWS_GUI {
		return fmt.Errorf("linked for PE subsystem %d, not the Windows GUI subsystem (%d): "+
			"the Start-menu shortcut would open a cmd window behind the application", subsystem, pe.IMAGE_SUBSYSTEM_WINDOWS_GUI)
	}
	if version == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Contains(raw, utf16LE(version)) {
		return fmt.Errorf("the version resource does not carry %q, so Explorer's Properties dialog "+
			"would name another version than --version does", version)
	}
	return nil
}

// utf16LE is how a VERSIONINFO block stores a string.
func utf16LE(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 0, len(units)*2)
	for _, unit := range units {
		out = append(out, byte(unit), byte(unit>>8))
	}
	return out
}
