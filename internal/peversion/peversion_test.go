package peversion

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// entry is one String of a version resource, as a linker writes it: a
// six-byte header, the key and its terminator, padding to four bytes, the
// value and its terminator.
func entry(key, value string) []byte {
	out := []byte{0x30, 0x00, 0x0a, 0x00, 0x01, 0x00}
	out = append(out, utf16LE(key)...)
	out = append(out, 0, 0)
	for len(out)%4 != 0 {
		out = append(out, 0)
	}
	out = append(out, utf16LE(value)...)
	return append(out, 0, 0)
}

func TestTheValueAfterTheKeyIsReadWhole(t *testing.T) {
	var data []byte
	data = append(data, entry("ProductName", "KubeTower")...)
	data = append(data, entry("ProductVersion", "0.1.0-rc.1")...)
	data = append(data, entry("FileVersion", "0.1.0-rc.1")...)

	for key, want := range map[string]string{
		"ProductName":    "KubeTower",
		"ProductVersion": "0.1.0-rc.1",
		"FileVersion":    "0.1.0-rc.1",
	} {
		if got, ok := Find(data, key); !ok || got != want {
			t.Errorf("Find(%q) = %q, %v; want %q", key, got, ok, want)
		}
	}
	// **Whole, not a prefix**: 0.1.0 is not what a resource saying
	// 0.1.0-rc.1 carries, which a substring search would have passed.
	if got, _ := Find(data, "ProductVersion"); got == "0.1.0" {
		t.Error("the value was cut short")
	}
	if _, ok := Find(data, "CompanyName"); ok {
		t.Error("found a key that is not there")
	}
}

// TestAKeyAtAnOddOffsetIsNotAKey. UTF-16 is read two bytes at a time from
// the start of the section, so a match straddling two units is not the key.
func TestAKeyAtAnOddOffsetIsNotAKey(t *testing.T) {
	data := append([]byte{0x01}, entry("ProductName", "Elsewhere")...)
	if got, ok := Find(data, "ProductName"); ok {
		t.Errorf("read %q from a key at an odd offset", got)
	}
}

// TestThisTestBinaryIsNotKubeTower reads a real PE file where there is one:
// the test binary itself on Windows, which has no version resource naming
// KubeTower and must not be read as having one.
func TestThisTestBinaryIsNotKubeTower(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the test binary is a PE file only on Windows")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if name, err := Value(self, "ProductName"); err == nil && name == "KubeTower" {
		t.Errorf("%s reads as KubeTower", filepath.Base(self))
	}
	if _, err := Value(filepath.Join(t.TempDir(), "absent.exe"), "ProductName"); err == nil {
		t.Error("a missing file was read")
	}
}
