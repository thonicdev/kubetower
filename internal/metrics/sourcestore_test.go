package metrics

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/statefile"
)

func quietStore(string, ...any) {}

func TestASourceIsKeptPerClusterAndSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	s := NewSourceStore(dir, quietStore)
	if s.Source("prod") != nil {
		t.Fatal("a fresh store has a source")
	}
	if _, err := s.Set("prod", " vm/vmselect-main:8481/select/0/prometheus "); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set("staging", "monitoring/prometheus:9090"); err != nil {
		t.Fatal(err)
	}

	again := NewSourceStore(dir, quietStore)
	if got := again.Source("prod"); got == nil || got.Address() != "vm/vmselect-main:8481/select/0/prometheus" {
		t.Fatalf("prod reads back as %+v", got)
	}
	if got := again.Source("staging"); got == nil || got.Address() != "monitoring/prometheus:9090" {
		t.Fatalf("staging reads back as %+v", got)
	}
	if again.Source("dev") != nil {
		t.Error("a cluster nobody set has a source")
	}
}

func TestEmptyReturnsAClusterToDiscovery(t *testing.T) {
	dir := t.TempDir()
	s := NewSourceStore(dir, quietStore)
	s.Set("prod", "vm/vmsingle-main:8428")
	if target, err := s.Set("prod", ""); err != nil || target != nil {
		t.Fatalf("%+v, %v", target, err)
	}
	if NewSourceStore(dir, quietStore).Source("prod") != nil {
		t.Fatal("the cleared source came back after a restart")
	}
}

func TestAValueThatIsNotATargetChangesNothing(t *testing.T) {
	s := NewSourceStore(t.TempDir(), quietStore)
	s.Set("prod", "vm/vmsingle-main:8428")
	for _, bad := range []string{"vmsingle", "vm/vmsingle-main:8428/../x", "vm/vmsingle-main:http"} {
		if _, err := s.Set("prod", bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if got := s.Source("prod"); got == nil || got.Address() != "vm/vmsingle-main:8428" {
		t.Fatalf("the source moved to %+v", got)
	}
}

// The file is editable by hand: a bad entry is skipped, not trusted, and the
// others still apply.
func TestABadEntryInTheFileIsDiscoveredInstead(t *testing.T) {
	dir := t.TempDir()
	raw := `{"sources":{"prod":"vm/vmsingle-main:8428","bad":"../../etc:1","":"vm/x:1"}}`
	if err := os.WriteFile(filepath.Join(dir, SourceFileName), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	var said []string
	s := NewSourceStore(dir, func(format string, _ ...any) { said = append(said, format) })
	if s.Source("prod") == nil || s.Source("bad") != nil || s.Source("") != nil {
		t.Fatalf("prod %+v bad %+v", s.Source("prod"), s.Source("bad"))
	}
	if len(said) == 0 {
		t.Error("the bad entries were dropped silently")
	}
}

func TestAReturnedSourceIsACopy(t *testing.T) {
	s := NewSourceStore("", quietStore)
	s.Set("prod", "vm/vmsingle-main:8428")
	s.Source("prod").Port = 1
	if s.Source("prod").Port != 8428 {
		t.Fatal("a caller changed the stored source")
	}
}

func TestASourceThatCannotBeWrittenIsStillInEffect(t *testing.T) {
	dir := t.TempDir()
	s := NewSourceStore(dir, quietStore)
	// A directory where the file should be: the write fails.
	if err := os.Mkdir(filepath.Join(dir, SourceFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := s.Set("prod", "vm/vmsingle-main:8428")
	if !errors.Is(err, statefile.ErrNotSaved) {
		t.Fatalf("err %v, want ErrNotSaved", err)
	}
	if s.Source("prod") == nil {
		t.Fatal("the source is not in effect")
	}
}

func TestTheStoreIsBounded(t *testing.T) {
	s := NewSourceStore("", quietStore)
	for i := range MaxSourceClusters {
		if _, err := s.Set("c"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+itoa(i), "vm/vmsingle-main:8428"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Set("one-more", "vm/vmsingle-main:8428"); err == nil {
		t.Fatal("the store grew past its bound")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}
