package updater

import (
	"archive/zip"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTheSetupRunsSilentlyAfterExitThenStartsKubeTower(t *testing.T) {
	got := setupScript(4242, `C:\Users\o'neil\state\update\kubetower-0.1.3-setup.exe`,
		`C:\Users\o'neil\AppData\Local\Programs\KubeTower\kubetower.exe`, nil, `C:\Users\o'neil\state\update-install.log`, true)
	for _, want := range []string{
		"$kt_log='C:\\Users\\o''neil\\state\\update-install.log'",
		"Wait-Process -Id 4242",
		// /D= last and unquoted inside its literal, as NSIS reads it.
		`Start-Process -FilePath 'C:\Users\o''neil\state\update\kubetower-0.1.3-setup.exe' -ArgumentList '/S','/D=C:\Users\o''neil\AppData\Local\Programs\KubeTower' -Wait -PassThru`,
		"L ('setup exit code ' + $p.ExitCode)",
		`Start-Process -FilePath 'C:\Users\o''neil\AppData\Local\Programs\KubeTower\kubetower.exe'`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the script lacks %q:\n%s", want, got)
		}
	}
	order := []string{"waiting for KubeTower (pid 4242)", "Wait-Process", "KubeTower exited", "'/S'", "setup exit code", "starting "}
	for i := 1; i < len(order); i++ {
		if strings.Index(got, order[i-1]) > strings.Index(got, order[i]) {
			t.Errorf("%q comes after %q:\n%s", order[i-1], order[i], got)
		}
	}
}

// On quit (#122) the helper installs once KubeTower has exited and starts
// nothing: the next start runs the new build.
func TestOnQuitTheHelpersInstallAndStartNothing(t *testing.T) {
	for name, script := range map[string]string{
		"setup": setupScript(9, `C:\s\setup.exe`, `C:\k\kubetower.exe`, []string{"--x"}, `C:\s\update-install.log`, false),
		"swap":  swapScript(9, `C:\k\.next.exe`, `C:\k\kubetower.exe`, []string{"--x"}, `C:\s\update-install.log`, false),
	} {
		if !strings.Contains(script, "Wait-Process -Id 9") || !strings.Contains(script, "installed on quit; nothing started") {
			t.Errorf("%s: %s", name, script)
		}
		if strings.Contains(script, `Start-Process -FilePath 'C:\k\kubetower.exe'`) || strings.Contains(script, "starting ") {
			t.Errorf("%s starts KubeTower on quit:\n%s", name, script)
		}
	}
}

// The log holds the script's own words, paths and exit codes, and nothing the
// console was started with: its arguments may carry a listen address or a
// flag, and the install never logs a URL or a token.
func TestTheHelpersLogNoArgumentsAndNoURL(t *testing.T) {
	args := []string{"--listen", "127.0.0.1:1", "--token=s3cr3t"}
	for _, script := range []string{
		setupScript(1, `C:\s\setup.exe`, `C:\k\kubetower.exe`, args, `C:\s\update-install.log`, true),
		swapScript(1, `C:\k\.next.exe`, `C:\k\kubetower.exe`, args, `C:\s\update-install.log`, true),
	} {
		for _, line := range strings.Split(script, "L ")[1:] {
			if strings.Contains(line, "s3cr3t") && !strings.Contains(line, "Start-Process") {
				t.Errorf("a log line carries an argument: %s", line)
			}
		}
		if strings.Contains(script, "http") {
			t.Errorf("a helper names a URL:\n%s", script)
		}
	}
}

func TestTheZipSwapMovesTheNewExeAfterExit(t *testing.T) {
	got := swapScript(7, `D:\tools\.kubetower-update-1.exe`, `D:\tools\kubetower.exe`, []string{"--listen", "127.0.0.1:1;x"}, `D:\s\update-install.log`, true)
	for _, want := range []string{
		"Wait-Process -Id 7 -ErrorAction SilentlyContinue",
		`Move-Item -LiteralPath 'D:\tools\.kubetower-update-1.exe' -Destination 'D:\tools\kubetower.exe' -Force -ErrorAction Stop`,
		`Start-Process -FilePath 'D:\tools\kubetower.exe' -ArgumentList '--listen','127.0.0.1:1;x'`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the script lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "Wait-Process") > strings.Index(got, "Move-Item") {
		t.Error("the move runs before the console has exited")
	}
}

func TestPowerShellLiteralsExpandNothing(t *testing.T) {
	for in, want := range map[string]string{
		`a$(calc)b`: `'a$(calc)b'`,
		"it's":      `'it''s'`,
		"`n":        "'`n'",
		// PowerShell closes a single-quoted string on U+2018 to U+201B as well.
		`C:\Users\O’Neil`: `'C:\Users\O’’Neil'`,
		"‘a’b‚c‛d":        "'‘‘a’’b‚‚c‛‛d'",
	} {
		if got := psQuote(in); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}

func zipOf(t *testing.T, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubetower.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for name, body := range files {
		out, _ := w.Create(name)
		_, _ = out.Write([]byte(body))
	}
	_ = w.Close()
	_ = f.Close()
	return path
}

func TestTheZipGivesItsRootExeBesideTheRunningOne(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "kubetower.exe")
	next, err := writeFromZip(exe, zipOf(t, map[string]string{"kubetower.exe": "0.1.3", "LICENSE": "x", "bin/kubetower.exe": "no"}), "kubetower.exe")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(next) != dir {
		t.Errorf("written to %s, not beside the running exe", next)
	}
	if got, _ := os.ReadFile(next); string(got) != "0.1.3" {
		t.Errorf("%q", got)
	}
	if _, err := writeFromZip(exe, zipOf(t, map[string]string{"bin/kubetower.exe": "no"}), "kubetower.exe"); !errors.Is(err, ErrNoBinary) {
		t.Errorf("no root exe: %v", err)
	}
}

func TestNoHelperStartsWithoutAWayToStop(t *testing.T) {
	started, stopped := 0, 0
	start := func(string) error { started++; return nil }
	if err := finish("script", start, nil); !errors.Is(err, errNoStop) || started != 0 {
		t.Fatalf("%v, %d started without a stop", err, started)
	}
	if err := finish("script", start, func() { stopped++ }); err != nil || started != 1 || stopped != 1 {
		t.Fatalf("%v, started %d, stopped %d", err, started, stopped)
	}
	// A helper that did not start does not stop the console.
	if err := finish("script", func(string) error { return errors.New("no powershell") }, func() { stopped++ }); err == nil || stopped != 1 {
		t.Fatalf("%v, stopped %d", err, stopped)
	}
}

func TestSetStopIsTheWayTheHelperStops(t *testing.T) {
	was := stopper
	t.Cleanup(func() { stopper = was })
	called := false
	SetStop(func() { called = true })
	stopper()
	if !called {
		t.Fatal("SetStop did not set the stop")
	}
}

func TestAZipFolderThatIsNotWritableIsInstalledByHand(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("a read-only directory is not read-only here")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "kubetower.exe")
	_ = os.WriteFile(exe, []byte("0.1.2"), 0o755)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	_, err := writeFromZip(exe, zipOf(t, map[string]string{"kubetower.exe": "0.1.3"}), "kubetower.exe")
	if !errors.Is(err, ErrNotWritable) || errors.Is(err, fs.ErrPermission) {
		t.Fatalf("%v, want ErrNotWritable and no raw access error", err)
	}
}

func TestTheSetupFolderIsTheRunningExesFolder(t *testing.T) {
	for in, want := range map[string]string{
		`C:\Program Files\KubeTower\kubetower.exe`: `C:\Program Files\KubeTower`,
		`C:\kubetower.exe`:                         `C:\`,
		`D:/tools/kubetower.exe`:                   `D:/tools`,
	} {
		if got := windowsDir(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestAZipEntryOverTheLimitIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.zip")
	f, _ := os.Create(path)
	w := zip.NewWriter(f)
	// A raw entry keeps the size it declares: more than MaxAsset, two bytes held.
	out, err := w.CreateRaw(&zip.FileHeader{Name: "kubetower.exe", Method: zip.Store,
		CompressedSize64: 2, UncompressedSize64: MaxAsset + 1})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = out.Write([]byte("no"))
	_ = w.Close()
	_ = f.Close()
	dir := t.TempDir()
	_, err = writeFromZip(filepath.Join(dir, "kubetower.exe"), path, "kubetower.exe")
	if err == nil || !strings.Contains(err.Error(), "declares") {
		t.Fatalf("%v, want refused", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("left %v", entries)
	}
}
