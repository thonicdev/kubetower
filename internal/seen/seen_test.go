package seen

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/statefile"
)

func TestTheVersionSeenSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, nil)
	if s.Version() != "" {
		t.Fatalf("a fresh store answers %q", s.Version())
	}
	if err := s.Set("0.2.0"); err != nil {
		t.Fatal(err)
	}
	if got := New(dir, nil).Version(); got != "0.2.0" {
		t.Fatalf("after a restart: %q", got)
	}
	if info, err := os.Stat(filepath.Join(dir, FileName)); err != nil || info.Mode().Perm() != statefile.FilePerm {
		t.Fatalf("%v %v", info, err)
	}
}

func TestOnlyAReleaseVersionIsKept(t *testing.T) {
	s := New(t.TempDir(), nil)
	for _, bad := range []string{"", "dev", "v0.2.0", "0.2", "0.2.0 ", "0.2.0-rc.1;rm", "0.2.0-rc-1", "0.2.0-"} {
		if err := s.Set(bad); err == nil {
			t.Errorf("%q kept", bad)
		}
	}
	if err := s.Set("0.2.0-rc.1"); err != nil || s.Version() != "0.2.0-rc.1" {
		t.Fatalf("%v %q", err, s.Version())
	}
	// Whatever cmd/ktbuild stamps is kept: release.yml bounds neither the
	// digits nor the pre-release, so neither does the store.
	for _, good := range []string{"1234567.0.0", "0.2.0-" + strings.Repeat("rc.10", 10)} {
		if err := s.Set(good); err != nil || s.Version() != good {
			t.Errorf("%q: %v %q", good, err, s.Version())
		}
	}
}

// The API contract spells the same rule, so a client that checks it before
// writing never meets a 400.
func TestTheContractSpellsTheStoresPattern(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "pattern: '" + Pattern + "'"; !strings.Contains(string(raw), want) {
		t.Fatalf("api/openapi.yaml does not carry %s", want)
	}
}

func TestAnUnusableFileIsForgotten(t *testing.T) {
	dir := t.TempDir()
	for _, raw := range []string{"{", `{"version":"dev"}`} {
		_ = os.WriteFile(filepath.Join(dir, FileName), []byte(raw), 0o600)
		if got := New(dir, nil).Version(); got != "" {
			t.Errorf("%s: %q", raw, got)
		}
	}
}

func TestAWriteThatFailsStaysInEffect(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	dir := t.TempDir()
	s := New(dir, nil)
	_ = os.Chmod(dir, 0o500)
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := s.Set("0.2.0"); !errors.Is(err, statefile.ErrNotSaved) || s.Version() != "0.2.0" {
		t.Fatalf("%v %q", err, s.Version())
	}
}

func TestANilOrMemoryStoreKeepsNothingOnDisk(t *testing.T) {
	var none *Store
	if none.Version() != "" {
		t.Fatal("a nil store answers")
	}
	s := New("", nil)
	if err := s.Set("0.2.0"); err != nil || s.Version() != "0.2.0" {
		t.Fatalf("%v %q", err, s.Version())
	}
}
