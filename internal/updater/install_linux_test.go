package updater

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type restarted struct{ path string }

func stubRestart(t *testing.T) *restarted {
	t.Helper()
	got := &restarted{}
	was := restart
	restart = func(path string, _ []string) error { got.path = path; return nil }
	t.Cleanup(func() { restart = was })
	return got
}

func TestAnAppImageIsReplacedThenStartedAgain(t *testing.T) {
	r := stubRestart(t)
	dir := t.TempDir()
	image := filepath.Join(dir, "KubeTower.AppImage")
	_ = os.WriteFile(image, []byte("0.1.2"), 0o755)
	file := filepath.Join(t.TempDir(), "kubetower-0.1.3-x86_64.AppImage")
	_ = os.WriteFile(file, []byte("0.1.3"), 0o600)
	err := installers[LinuxAppImage](context.Background(), Plan{File: file, Installation: Installation{Kind: LinuxAppImage, Path: image}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(image); string(got) != "0.1.3" || r.path != image {
		t.Fatalf("%q, restarted %q", got, r.path)
	}
}

func TestATarInstallIsReplacedFromTheArchive(t *testing.T) {
	r := stubRestart(t)
	bin := filepath.Join(t.TempDir(), "kubetower")
	_ = os.WriteFile(bin, []byte("0.1.2"), 0o755)
	archive := tarGz(t, map[string]string{"./kubetower": "0.1.3", "./LICENSE": "x"}, nil)
	if err := installers[LinuxTar](context.Background(), Plan{File: archive, Installation: Installation{Kind: LinuxTar, Path: bin}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(bin); string(got) != "0.1.3" || r.path != bin {
		t.Fatalf("%q, restarted %q", got, r.path)
	}
}

func TestAFailedReplaceDoesNotRestart(t *testing.T) {
	r := stubRestart(t)
	bin := filepath.Join(t.TempDir(), "kubetower")
	_ = os.WriteFile(bin, []byte("0.1.2"), 0o755)
	archive := tarGz(t, map[string]string{"./LICENSE": "x"}, nil)
	if err := installers[LinuxTar](context.Background(), Plan{File: archive, Installation: Installation{Kind: LinuxTar, Path: bin}}); !errors.Is(err, ErrNoBinary) {
		t.Fatalf("%v", err)
	}
	if r.path != "" {
		t.Error("restarted after a failed replace")
	}
}

func TestPkexecRefusalsAreSaidPlainly(t *testing.T) {
	for code, want := range map[string]string{
		"126": "the password was not given",
		"127": "not authorized to install packages",
		"1":   "the package manager refused: E: broken",
	} {
		err := exec.Command("/bin/sh", "-c", "echo 'E: broken'; exit "+code).Run()
		got := pkexecFailure(err, []byte("E: broken\nmore"))
		if !strings.Contains(got.Error(), want) {
			t.Errorf("exit %s: %v, want %q", code, got, want)
		}
	}
}

func TestEveryLinuxInstallationHasAnInstaller(t *testing.T) {
	for _, kind := range []Kind{LinuxAppImage, LinuxTar, LinuxDeb, LinuxRPM} {
		if installers[kind] == nil {
			t.Errorf("no installer for %s", kind)
		}
	}
}
