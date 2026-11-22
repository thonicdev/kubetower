package launch

import (
	"errors"
	"go/ast"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The parts of `--install` and the Windows setup that can be exercised without
// installing anything.
//
// **Nothing here calls `runInstall` or `runUninstall`.** On macOS and Linux
// they write desktop entries into the account running the tests, and on
// Windows `runUninstall` launches a real uninstaller - a change to the
// operator's system as a side effect of checking the code. So the file
// operations are exercised against temporary directories, and the setup's
// install and uninstall are left to the one thing that can actually judge
// them, which is running it on a Windows machine and looking.

// TestInstallingOverARunningCopyKeepsAWorkingApplication. The interesting
// failure is not the copy, it is the *recovery*: `replaceExecutable` moves the
// installed file aside before writing, and if that write then fails it has to put
// it back. Otherwise an install that failed leaves a directory with no executable
// in it and a Start-menu entry pointing at nothing — strictly worse
// than not having tried.
func TestInstallingOverARunningCopyKeepsAWorkingApplication(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "kubetower.exe")
	if err := os.WriteFile(target, []byte("the installed one"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A source that cannot be read is the simplest way to make the second copy
	// fail after the rename has already happened, which is the branch that has
	// to restore.
	if _, err := replaceExecutable(filepath.Join(dir, "nothing-here"), target); err == nil {
		t.Fatal("replaceExecutable accepted a source that does not exist")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("the installed executable is gone after a failed install: %v", err)
	}
	if string(got) != "the installed one" {
		t.Errorf("the installed executable is now %q; a failed install must leave the old one", got)
	}
}

// TestASuccessfulInstallReplacesTheBytes. The ordinary path, asserted because
// `copyFile` writes to a temporary name and renames, and a rename that landed
// somewhere other than the target would leave the old binary in place and report
// success.
func TestASuccessfulInstallReplacesTheBytes(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "downloaded")
	target := filepath.Join(dir, "installed", "kubetower")

	if err := os.WriteFile(source, []byte("the new version"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	displaced, err := replaceExecutable(source, target)
	if err != nil {
		t.Fatalf("replaceExecutable: %v", err)
	}
	if displaced {
		t.Error("reported displacing a locked executable when the target was writable; " +
			"that note tells the operator their open window is the old build")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "the new version" {
		t.Errorf("target holds %q, want the source bytes", got)
	}

	// And nothing temporary was left behind. CreateTemp names are the one thing
	// here that would accumulate silently, one per install, in a directory
	// nobody opens.
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".kubetower-") {
			t.Errorf("a temporary file survived the install: %s", e.Name())
		}
	}
}

// TestTheSweepTakesDisplacedBinariesAndNothingElse. `sweepAside` runs at the top
// of every install and deletes by suffix, which is a pattern that removes the
// wrong file the moment the suffix is loosened. The executable itself and
// anything the operator put there have to survive it.
func TestTheSweepTakesDisplacedBinariesAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	keep := []string{"kubetower.exe", "kubetower.log", "notes.old.txt"}
	remove := []string{"kubetower.exe.old", "kubetower.old"}

	for _, name := range append(append([]string{}, keep...), remove...) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	sweepAside(dir)

	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("the sweep deleted %s, which it had no business touching", name)
		}
	}
	for _, name := range remove {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s survived the sweep", name)
		}
	}
}

// TestRemovingWhatIsNotThereIsNotAFailure. Uninstalling twice, or uninstalling
// after the shortcut was tidied away by hand, is ordinary. `removeIfPresent`
// reporting those as removals would make the report claim things it did not do.
func TestRemovingWhatIsNotThereIsNotAFailure(t *testing.T) {
	dir := t.TempDir()
	if removeIfPresent(filepath.Join(dir, "never-existed")) {
		t.Error("removeIfPresent claimed to remove a file that was not there")
	}

	present := filepath.Join(dir, "shortcut")
	if err := os.WriteFile(present, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !removeIfPresent(present) {
		t.Error("removeIfPresent did not report removing a file that was there")
	}
	if removeIfPresent(present) {
		t.Error("removeIfPresent reported removing the same file twice")
	}
}

// TestTheUninstallerSaysItKeptYourSettings.
//
// **The state directory is never removed, and this is the sentence that turns
// that from a silent omission into an offer.** It holds the credentials file,
// the cluster names and colors, the rail arrangement, the column widths and the
// event archive — the last of which is the only thing in this project that
// cannot be regenerated from a cluster. An uninstaller that deleted it because
// somebody was moving between versions would have destroyed the one irreplaceable
// thing here; one that keeps it silently leaves an operator believing it is gone.
func TestTheUninstallerSaysItKeptYourSettings(t *testing.T) {
	body := uninstalledHelp([]string{`C:\somewhere\kubetower.exe`}, nil)

	for _, phrase := range []string{"left alone", "Reinstalling", "Delete it by hand"} {
		if !strings.Contains(body, phrase) {
			t.Errorf("the uninstall report never says %q:\n%s", phrase, body)
		}
	}
	// And it must name the directory, because "your settings were kept" without
	// a path is a fact nobody can act on.
	if !strings.Contains(body, "kubetower") {
		t.Errorf("the uninstall report does not name the state directory:\n%s", body)
	}
}

// TestTheInstallReportSaysWhatNobodyGuesses. The facts nobody guesses - no
// password on this machine, loopback only, a password only in a container or
// beyond loopback - in two lines, and the commands that undo a mistake. The
// Infos tab says only the version, so the report does not point there.
func TestTheInstallReportSaysWhatNobodyGuesses(t *testing.T) {
	body := installedHelp(installation{
		Binary:  "/home/x/.local/bin/kubetower",
		Entries: []string{"launcher      KubeTower"},
	})

	for _, phrase := range []string{
		"already signed in", // what opening it does
		"this machine only", // loopback, by design
		"no password here",  // what nobody guesses
		"--reset-password",  // there is a way back from a lost password
		"--uninstall",       // how to undo what was just done
		// What it reads and when it first reaches out, which the banner says
		// on macOS and Linux.
		"same kubeconfig your kubectl does",
	} {
		if !strings.Contains(body, phrase) {
			t.Errorf("the install report never mentions %q:\n%s", phrase, body)
		}
	}
	if !strings.Contains(body, "/home/x/.local/bin/kubetower") {
		t.Error("the install report does not say where the executable went")
	}
}

// TestTheInstallBoxIsGone. The Windows install is the setup wizard's, and no
// install message box may come back: the heading such a box carried must not
// appear in this package's sources. The phrase is assembled so that this file
// does not match itself.
func TestTheInstallBoxIsGone(t *testing.T) {
	heading := "Three things " + "nobody guesses"
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no Go files found, so this test checked nothing")
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), heading) {
			t.Errorf("%s carries %q, the heading of an install message box the setup wizard replaces", file, heading)
		}
	}
}

// TestEveryPathInAReportCanBePasted.
//
// A Windows path through `%q` comes back as `C:\\Users\\someone\\…` — a correct
// Go literal and a line nobody can paste into a shell, and a mistake that is
// easy to repeat, which is why it is a test and not a note. The class is the
// same one the first-run password box is about: **shown is not the same as
// obtainable.**
func TestEveryPathInAReportCanBePasted(t *testing.T) {
	windows := `C:\Users\someone\AppData\Local\Programs\KubeTower\kubetower.exe`

	body := installedHelp(installation{
		Binary:  windows,
		Entries: []string{"Start menu    KubeTower"},
	})
	if strings.Contains(body, `\\`) {
		t.Errorf("a path was written with doubled separators — %%q instead of quotes:\n%s", body)
	}
	if !strings.Contains(body, windows) {
		t.Errorf("the install path does not appear verbatim and so cannot be pasted:\n%s", body)
	}

	// The uninstall line has to name the executable in full rather than say
	// `kubetower --uninstall`: the install directory is not on PATH on Windows,
	// so the bare command is one the operator's shell cannot find.
	if !strings.Contains(body, `"`+windows+`" --uninstall`) {
		t.Errorf("the removal line does not give a runnable command:\n%s", body)
	}

	if out := uninstalledHelp([]string{windows}, nil); strings.Contains(out, `\\`) {
		t.Errorf("the uninstall report doubled a path's separators:\n%s", out)
	}
}

// TestAHalfDoneInstallSaysWhichHalf. A shortcut that could not be written is not
// a failed install — the binary is in place and runnable — but an install that
// reported plain success over it would leave "it is not in my Start menu" with no
// answer except reinstalling.
func TestAHalfDoneInstallSaysWhichHalf(t *testing.T) {
	body := installedHelp(installation{
		Binary: "/home/x/.local/bin/kubetower",
		Notes:  []string{"the Start-menu shortcut could not be written: access denied"},
	})
	if !strings.Contains(body, "access denied") {
		t.Errorf("a note was dropped from the report:\n%s", body)
	}
}

// TestBothFlagsAreInTheHelp. `--help` is one of two places a person finds out
// this exists — the other is a downloadable file with `Setup`
// in its name — so a flag missing from it is a flag nobody uses.
func TestBothFlagsAreInTheHelp(t *testing.T) {
	help := usage()
	for _, flag := range []string{"--install", "--uninstall", bundleFlag} {
		if !strings.Contains(help, flag) {
			t.Errorf("--help does not mention %s, so nobody will find it", flag)
		}
	}
}

// TestOnlyTheRegisteredUninstallerIsStarted. `--uninstall` starts
// `uninstall.exe` from the folder Apps & features records and from nowhere
// else - not beside a portable copy, where somebody else's uninstaller may sit
// - and only a file whose version resource
// names KubeTower.
func TestOnlyTheRegisteredUninstallerIsStarted(t *testing.T) {
	dir := filepath.Join("C:", "Users", "x", "AppData", "Local", "Programs", "KubeTower")
	for _, registered := range []string{dir, `"` + dir + `"`, "  " + dir + " "} {
		got, err := registeredUninstaller(registered)
		if err != nil || got != filepath.Join(dir, uninstallerName) {
			t.Errorf("registeredUninstaller(%q) = %q, %v", registered, got, err)
		}
	}
	for _, unregistered := range []string{"", "  ", `""`} {
		if got, err := registeredUninstaller(unregistered); err == nil {
			t.Errorf("registeredUninstaller(%q) = %q: with nothing registered nothing may be started", unregistered, got)
		}
	}

	// A file with the right name that is not KubeTower's is refused unread
	// by anything but its version resource; so is a missing one.
	foreign := filepath.Join(t.TempDir(), uninstallerName)
	if err := os.WriteFile(foreign, []byte("MZ not really"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkUninstaller(foreign); err == nil {
		t.Error("checkUninstaller accepted a file whose version resource does not name KubeTower")
	}
	if err := checkUninstaller(filepath.Join(t.TempDir(), uninstallerName)); err == nil {
		t.Error("checkUninstaller accepted a file that does not exist")
	}
}

// TestTheBundleFlagTakesTheLastValue. A
// value that can arrive twice has to have one rule about which one counts, and
// the later one is the one somebody meant.
func TestTheBundleFlagTakesTheLastValue(t *testing.T) {
	if got := bundleTarget([]string{"--app", "--version"}); got != "" {
		t.Errorf("bundleTarget found %q where the flag is absent", got)
	}
	if got := bundleTarget([]string{bundleFlag + "first", bundleFlag + "second"}); got != "second" {
		t.Errorf("bundleTarget = %q, want the last value", got)
	}
	// An empty value is the same as absent rather than "the current directory":
	// `--bundle=` writing a KubeTower.app into wherever the build happened to be
	// is a bundle nothing packs and nobody deletes.
	if got := bundleTarget([]string{bundleFlag}); got != "" {
		t.Errorf("bundleTarget = %q for an empty value; that must not mean the working directory", got)
	}
}

// TestTheSetupAndTheBinaryAgreeOnTheInstall.
//
// The setup script and this package share three facts, and each fails
// silently when they drift:
//
//   - **the Apps & features key.** The binary's `--install` wrote it; the
//     setup must write the same one, or an upgrade over that install leaves
//     two rows in Installed apps - a second uninstall mechanism;
//   - **the uninstaller's name**, which `--uninstall` looks for;
//   - **per user**: no elevation, the per-user default folder, and the state
//     directory kept unless a box is ticked.
//
// Read rather than run, because running the setup installs something.
func TestTheSetupAndTheBinaryAgreeOnTheInstall(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "packaging", "windows", "kubetower.nsi"))
	if err != nil {
		t.Fatalf("packaging/windows/kubetower.nsi is the Windows setup, and it is missing: %v", err)
	}
	script := string(raw)
	for _, want := range []string{
		`!define UNINSTALL_KEY "` + installKey + `"`,
		`!define UNINSTALLER "` + uninstallerName + `"`,
		"RequestExecutionLevel user",
		`InstallDir "$LOCALAPPDATA\Programs\KubeTower"`,
		`InstallDirRegKey HKCU "${UNINSTALL_KEY}" "InstallLocation"`,
		// The state directory goes only when asked: `/o` is a section that
		// starts unticked.
		`Section /o "un.`,
		// Both shortcuts open the window, as the Start-menu one does.
		`"$INSTDIR\${EXE}" "--app"`,
		`!define MUI_FINISHPAGE_RUN_PARAMETERS "--app"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the setup script does not carry %q", want)
		}
	}
	for _, line := range scriptCode(script) {
		for _, refused := range []*regexp.Regexp{
			regexp.MustCompile(`\bHKLM\b`), regexp.MustCompile(`(?i)HKEY_LOCAL_MACHINE`),
			regexp.MustCompile(`\bHKCR\b`), regexp.MustCompile(`(?i)HKEY_CLASSES_ROOT`),
			regexp.MustCompile(`\bHKU\b`), regexp.MustCompile(`(?i)HKEY_USERS`),
			regexp.MustCompile(`\bSHCTX\b`), regexp.MustCompile(`(?i)SetShellVarContext\s+all`),
			regexp.MustCompile(`(?i)RequestExecutionLevel\s+(admin|highest)`),
			regexp.MustCompile(`(?i)\$PROGRAMFILES`), regexp.MustCompile(`MultiUser`),
		} {
			if refused.MatchString(line) {
				t.Errorf("the setup script does %q: the install is per user only", line)
			}
		}
	}
}

// scriptCode is the setup script without its comments and blank lines, each
// line trimmed: what the script does, not what it says about it.
func scriptCode(script string) []string {
	var code []string
	for _, line := range strings.Split(script, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, ";") {
			code = append(code, trimmed)
		}
	}
	return code
}

// TestTheSetupDeletesOnlyWhatItInstalled. The rules that keep the setup from
// destroying something that is not its own. Each is a mutation of the script
// that nothing else would catch:
//
//   - **No recursive removal but the state folder's.** `RMDir /r "$INSTDIR"`
//     on a folder chosen as Downloads deletes Downloads.
//   - **No wildcard delete.** `Delete "$INSTDIR\*.*"` is the same thing one
//     level down.
//   - **The state folder is `$PROFILE\.kubetower` exactly.** Widened to
//     `$PROFILE`, the "delete my settings" box deletes the profile.
//   - **Every prompt has an answer under /S.** A MessageBox without `/SD`
//     waits for a click nobody will make, and a silent install hangs.
//   - **A used folder is asked about**, on the directory page and again in the
//     install section for /S, and the size is the section's, not a walk of the
//     folder. The notices file is deleted only by a build that installed it.
func TestTheSetupDeletesOnlyWhatItInstalled(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "packaging", "windows", "kubetower.nsi"))
	if err != nil {
		t.Fatalf("packaging/windows/kubetower.nsi: %v", err)
	}
	code := scriptCode(string(raw))

	recursive, prompts := 0, 0
	for i, line := range code {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "rmdir") && strings.Contains(lower, "/r") {
			recursive++
			if line != `RMDir /r "${STATE_DIR}"` {
				t.Errorf("recursive removal of something other than the state folder: %q", line)
			}
		}
		if strings.HasPrefix(lower, "delete") && strings.ContainsAny(line, "*?") {
			t.Errorf("a wildcard delete: %q", line)
		}
		if strings.HasPrefix(lower, "messagebox") {
			prompts++
			if !strings.Contains(line, " /SD ") {
				t.Errorf("a prompt with no answer under /S, so a silent run would hang on it: %q", line)
			}
		}
		if strings.Contains(line, `"$INSTDIR\THIRD_PARTY_NOTICES.txt"`) && strings.HasPrefix(lower, "delete") {
			if i == 0 || code[i-1] != "!ifdef NOTICES" {
				t.Errorf("the notices file is deleted by a build that may not have installed it: %q", line)
			}
		}
		if strings.Contains(line, "${GetSize}") {
			t.Errorf("the size is measured by walking a folder that may hold anything: %q", line)
		}
	}
	if recursive != 1 {
		t.Errorf("%d recursive removals; want exactly one, of the state folder, in the unticked section", recursive)
	}
	if prompts == 0 {
		t.Error("no MessageBox found, so the /SD rule checked nothing")
	}
	for _, want := range []string{
		`!define STATE_DIR "$PROFILE\.kubetower"`,
		`RMDir "$INSTDIR"`,
		`!define MUI_PAGE_CUSTOMFUNCTION_LEAVE checkFolder`,
		`Call checkFolder`,
		`SectionGetSize ${SecApp} $0`,
		`Section /o "un.Also delete my KubeTower settings" UnSecState`,
	} {
		found := false
		for _, line := range code {
			if line == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the setup script has no line %q", want)
		}
	}
}

// TestTheSetupHasABuildPath. A setup script nothing builds is the "built and
// not reachable" family one level up: the Taskfile, ci.yml and release.yml all
// have to build it through cmd/ktbuild, which is what runs makensis and reads
// the result back. The workflows are not in the image's build context
// (`.dockerignore` drops `.github`), so only their absence skips, and only
// that half.
func TestTheSetupHasABuildPath(t *testing.T) {
	taskfile, err := os.ReadFile(filepath.Join("..", "..", "Taskfile.yaml"))
	if err != nil {
		t.Fatalf("Taskfile.yaml: %v", err)
	}
	// The command itself, not a substring a file name would also satisfy:
	// `kubetower-setup.exe` contains "-setup" too.
	if want := "go run ./cmd/ktbuild -os windows -arch amd64 -setup "; !strings.Contains(string(taskfile), "build:setup:") ||
		!strings.Contains(string(taskfile), want) {
		t.Errorf("Taskfile.yaml has no build:setup task running %q", want)
	}
	for file, wants := range map[string][]string{
		"ci.yml": {"go run ./cmd/ktbuild -os windows -arch amd64 -setup ", "apt-get install"},
		"release.yml": {"go run ./cmd/ktbuild -os windows -arch amd64 -setup -stage ",
			"kubetower-${VERSION}-setup.exe", "apt-get install"},
	} {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", file))
			if errors.Is(err, fs.ErrNotExist) {
				t.Skipf("%s is not in this tree (the image's build context excludes it), so whether "+
					"it builds the setup is UNCHECKED here: %v", file, err)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range wants {
				if !strings.Contains(string(raw), want) {
					t.Errorf("%s does not carry %q, so it does not build the setup", file, want)
				}
			}
		})
	}
}

// TestTheMacOSBundleHasAProducer: the bundle the code writes has something
// that packs it.
//
// `bundleDirName`, `bundleExecutableName` and `bundleFlag` are a wire format
// between the code that writes a `KubeTower.app` (`--bundle=DIR`, installbundle.go)
// and whatever packs that bundle into a download: `.github/workflows/dmg.yml`,
// which release.yml and ci.yml both call. It runs
// `--bundle=`, drops the darwin binary at `Contents/MacOS/kubetower`, copies the
// bundle's icon to the volume, and packs `KubeTower.app`. A rename on either
// side that the other does not follow is an image whose app has no executable,
// or one that installs under another name than `--install` gives it - neither
// is a build failure, so it has to fail here. Grepped, because the workflow
// cannot be run from a test.
//
// Skipped only where .github is absent: the image's build context drops it.
func TestTheMacOSBundleHasAProducer(t *testing.T) {
	workflows := filepath.Join("..", "..", ".github", "workflows")
	dmg, err := os.ReadFile(filepath.Join(workflows, "dmg.yml"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("dmg.yml is not in this tree (the image's build context excludes .github), so "+
			"the bundle's producer is UNCHECKED here: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		bundleFlag,
		`app="$stage/` + bundleDirName + `"`,
		`"$app/Contents/MacOS/` + bundleExecutableName + `"`,
		"Contents/Resources/" + bundleExecutableName + ".icns",
		".VolumeIcon.icns",
		"attr / C",
		// The seal: without it a quarantined download reads as damaged.
		`rcodesign sign "$app"`,
		"kubetower-${VERSION}-${name}.dmg",
		"arm64:arm64:arm64", "x64:amd64:x86_64",
	} {
		if !strings.Contains(string(dmg), want) {
			t.Errorf("dmg.yml does not carry %q, so it and installbundle.go have drifted apart", want)
		}
	}
	// And both callers call it: a producer nothing runs is not one.
	for _, caller := range []string{"release.yml", "ci.yml"} {
		raw, err := os.ReadFile(filepath.Join(workflows, caller))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "uses: ./.github/workflows/dmg.yml") {
			t.Errorf("%s does not call dmg.yml, so it does not make the macOS disk images", caller)
		}
	}
}

// TestUninstallingNeverCreatesALogInTheDirectoryItIsRemoving.
//
// A source-level guard, of the same shape as the log-gate test in
// `logfile_test.go`: Run opens `kubetower.log` on every start that serves, and
// `--uninstall` is not one - handling the setup branch after that would mean
// every uninstall writes a file into the state directory on its way out.
//
// Read rather than run because Run is not callable from a test, and the
// ordering is the whole of the property. The two halves live in two functions,
// so it reads both: the setup branch is in leaveEarly, the log file is opened
// by startLog and nothing else, and Run calls leaveEarly - and returns when it
// answered - before it calls startLog.
func TestUninstallingNeverCreatesALogInTheDirectoryItIsRemoving(t *testing.T) {
	fset, funcs := runSource(t)

	early := funcs["leaveEarly"]
	if early == nil {
		t.Fatal("leaveEarly is gone from run.go, so this test reads the wrong function")
	}
	setupBranch := false
	ast.Inspect(early.Body, func(n ast.Node) bool {
		if cond, ok := n.(*ast.IfStmt); ok {
			if bin, ok := cond.Cond.(*ast.BinaryExpr); ok && bin.Op == token.NEQ {
				if id, ok := bin.X.(*ast.Ident); ok && id.Name == "setup" {
					setupBranch = true
				}
			}
		}
		return true
	})
	if !setupBranch {
		t.Fatal("the --install/--uninstall branch is gone from leaveEarly")
	}

	if callers := callersOf(funcs, "logToFile"); len(callers) != 1 || callers[0] != "startLog" {
		t.Fatalf("logToFile is called from %v in run.go, want startLog alone", callers)
	}

	run := funcs["Run"]
	leave, logging := firstCall(run, "leaveEarly"), firstCall(run, "startLog")
	if leave == token.NoPos || logging == token.NoPos {
		t.Fatalf("Run no longer calls both leaveEarly (%v) and startLog (%v)",
			leave != token.NoPos, logging != token.NoPos)
	}
	if leave > logging {
		t.Errorf("Run opens the log file (%s) before handling --uninstall (%s), so an uninstall "+
			"writes kubetower.log into the directory it is about to remove",
			fset.Position(logging), fset.Position(leave))
	}

	// And it leaves when leaveEarly answered: called for its result and
	// ignored, the uninstall would run and then carry on into startLog.
	returns := false
	ast.Inspect(run.Body, func(n ast.Node) bool {
		cond, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		asked := false
		if call, ok := cond.Cond.(*ast.CallExpr); ok && calledName(call) == "leaveEarly" {
			asked = true
		}
		// `if left, err := leaveEarly(...); left { return err }`.
		if init, ok := cond.Init.(*ast.AssignStmt); ok && len(init.Rhs) == 1 {
			if call, ok := init.Rhs[0].(*ast.CallExpr); ok && calledName(call) == "leaveEarly" {
				asked = true
			}
		}
		if asked {
			body := cond.Body.List
			if len(body) > 0 {
				_, returns = body[len(body)-1].(*ast.ReturnStmt)
			}
		}
		return true
	})
	if !returns {
		t.Error("Run does not return when leaveEarly answered, so an uninstall goes on to open the log")
	}
}
