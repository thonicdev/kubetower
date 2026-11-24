package updater

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEveryMacInstallationHasAnInstaller(t *testing.T) {
	for _, kind := range []Kind{MacApp, MacTar} {
		if installers[kind] == nil {
			t.Errorf("no installer for %s", kind)
		}
	}
}

func TestAMacTarInstallIsReplacedFromTheArchive(t *testing.T) {
	var started string
	was := restart
	restart = func(path string, _ []string) error { started = path; return nil }
	t.Cleanup(func() { restart = was })
	bin := filepath.Join(t.TempDir(), "kubetower")
	_ = os.WriteFile(bin, []byte("0.1.2"), 0o755)
	archive := tarGz(t, map[string]string{"./kubetower": "0.1.3"}, nil)
	if err := installers[MacTar](context.Background(), Plan{File: archive, Installation: Installation{Kind: MacTar, Path: bin}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(bin); string(got) != "0.1.3" || started != bin {
		t.Fatalf("%q, restarted %q", got, started)
	}
}
