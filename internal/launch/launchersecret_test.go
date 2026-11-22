package launch

// A launcher secret other
// accounts can read is not trusted, and two first launches at once end up
// holding the same secret as the file.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTooOpenReadsTheModeOffWindowsOnly(t *testing.T) {
	for _, c := range []struct {
		goos string
		mode os.FileMode
		want bool
	}{
		{"linux", 0o600, false},
		{"linux", 0o400, false},
		{"linux", 0o640, true},
		{"darwin", 0o644, true},
		{"linux", 0o604, true},
		{"windows", 0o666, false},
	} {
		if got := tooOpen(c.goos, c.mode); got != c.want {
			t.Errorf("tooOpen(%s, %o) = %v; want %v", c.goos, c.mode, got, c.want)
		}
	}
}

func TestALauncherSecretOthersCanReadIsRefusedAndReplaced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not the permission on Windows; the DACL test covers it")
	}
	dir := t.TempDir()
	t.Setenv("KT_STATE_DIR", dir)
	path := filepath.Join(dir, launcherFile)
	exposed := strings.Repeat("ab", launcherSecretBytes)
	if err := os.WriteFile(path, []byte(exposed+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	if got, err := launcherSecret(false); err == nil {
		t.Fatalf("a second launch trusted a world-readable secret %q", got)
	}
	replaced, err := launcherSecret(true)
	if err != nil {
		t.Fatalf("the instance could not replace a world-readable secret: %v", err)
	}
	if replaced == exposed {
		t.Error("the instance kept the secret other accounts could read")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the replacement is %o; want 600", perm)
	}
}

// The race, made deterministic: another first launch publishes its secret
// between this one drawing its own and publishing it. This one must take the
// winner's rather than overwrite it.
func TestTwoFirstLaunchesAtOnceHoldTheSameSecret(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KT_STATE_DIR", dir)
	path := filepath.Join(dir, launcherFile)
	winner := strings.Repeat("cd", launcherSecretBytes)
	beforePublish = func() {
		if err := publishSecret(path, []byte(winner+"\n")); err != nil {
			t.Errorf("the other launch could not publish: %v", err)
		}
	}
	t.Cleanup(func() { beforePublish = func() {} })

	got, err := launcherSecret(true)
	if err != nil {
		t.Fatal(err)
	}
	if got != winner {
		t.Errorf("this launch holds %q; want the winner's %q", got, winner)
	}
	onDisk, err := readLauncherSecret(path)
	if err != nil || onDisk != winner {
		t.Errorf("the file holds %q (%v); want the winner's", onDisk, err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, launcherFile+".*"))
	if len(leftovers) != 0 {
		t.Errorf("temporary files were left behind: %v", leftovers)
	}
}

func TestPublishingNeverReplacesASecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), launcherFile)
	first := strings.Repeat("ef", launcherSecretBytes)
	if err := publishSecret(path, []byte(first)); err != nil {
		t.Fatal(err)
	}
	if err := publishSecret(path, []byte(strings.Repeat("01", launcherSecretBytes))); !os.IsExist(err) {
		t.Errorf("a second publish answered %v; want os.ErrExist", err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != first {
		t.Errorf("the file now holds %q; want the first secret", raw)
	}
}
