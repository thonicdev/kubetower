package updater

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheSetupRunsSilentlyAfterExitThenStartsKubeTower(t *testing.T) {
	got := setupScript(4242, `C:\Users\o'neil\state\update\kubetower-0.1.3-setup.exe`,
		`C:\Users\o'neil\AppData\Local\Programs\KubeTower\kubetower.exe`, nil)
	for _, want := range []string{
		"Wait-Process -Id 4242",
		`Start-Process -FilePath 'C:\Users\o''neil\state\update\kubetower-0.1.3-setup.exe' -ArgumentList '/S' -Wait`,
		`Start-Process -FilePath 'C:\Users\o''neil\AppData\Local\Programs\KubeTower\kubetower.exe'`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the script lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "Wait-Process") > strings.Index(got, "/S") {
		t.Error("the setup runs before the console has exited")
	}
}

func TestTheZipSwapMovesTheNewExeAfterExit(t *testing.T) {
	got := swapScript(7, `D:\tools\.kubetower-update-1.exe`, `D:\tools\kubetower.exe`, []string{"--listen", "127.0.0.1:1;x"})
	want := "$ErrorActionPreference='Continue'; Wait-Process -Id 7 -ErrorAction SilentlyContinue; " +
		`Move-Item -LiteralPath 'D:\tools\.kubetower-update-1.exe' -Destination 'D:\tools\kubetower.exe' -Force; ` +
		`Start-Process -FilePath 'D:\tools\kubetower.exe' -ArgumentList '--listen','127.0.0.1:1;x'`
	if got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
}

func TestPowerShellLiteralsExpandNothing(t *testing.T) {
	for in, want := range map[string]string{
		`a$(calc)b`: `'a$(calc)b'`,
		"it's":      `'it''s'`,
		"`n":        "'`n'",
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
