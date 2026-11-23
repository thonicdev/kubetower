package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReplaceWithSwapsTheFileWhole(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "kubetower")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceWith(dst, strings.NewReader("new")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "new" {
		t.Fatalf("%q", got)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(dst); info.Mode().Perm() != 0o755 {
			t.Errorf("mode %v, want 0755", info.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("left beside it: %v", entries)
	}
}

func TestAReplaceThatCannotWriteLeavesTheFileAsItWas(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("a read-only directory is not read-only here")
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "kubetower")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	err := replaceWith(dst, strings.NewReader("new"))
	if err == nil || !strings.Contains(err.Error(), "is not writable") {
		t.Fatalf("%v, want not writable", err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "old" {
		t.Errorf("the file changed: %q", got)
	}
}

func tarGz(t *testing.T, entries map[string]string, links map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	for name, target := range links {
		_ = tw.WriteHeader(&tar.Header{Name: name, Linkname: target, Typeflag: tar.TypeSymlink})
	}
	_ = tw.Close()
	_ = gz.Close()
	file := filepath.Join(t.TempDir(), "kubetower.tar.gz")
	if err := os.WriteFile(file, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestTheTarGzGivesItsRootBinaryOnly(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "kubetower")
	_ = os.WriteFile(dst, []byte("old"), 0o755)
	archive := tarGz(t, map[string]string{"./LICENSE": "agpl", "./kubetower": "new binary", "./docs/kubetower": "not this"}, nil)
	if err := replaceFromTarGz(dst, archive, "kubetower"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "new binary" {
		t.Fatalf("%q", got)
	}
	// A symlink of that name, or no such file: nothing replaced.
	for name, archive := range map[string]string{
		"a symlink":  tarGz(t, map[string]string{"./LICENSE": "x"}, map[string]string{"./kubetower": "/etc/passwd"}),
		"no binary":  tarGz(t, map[string]string{"./LICENSE": "x"}, nil),
		"one deeper": tarGz(t, map[string]string{"./bin/kubetower": "x"}, nil),
	} {
		if err := replaceFromTarGz(dst, archive, "kubetower"); !errors.Is(err, ErrNoBinary) {
			t.Errorf("%s: %v, want ErrNoBinary", name, err)
		}
		if got, _ := os.ReadFile(dst); string(got) != "new binary" {
			t.Errorf("%s: the file changed to %q", name, got)
		}
	}
}

func TestThePackageIsInstalledThroughPkexecByTheFirstManagerFound(t *testing.T) {
	have := func(tools ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, tool := range tools {
				if tool == name {
					return "/usr/bin/" + name, nil
				}
			}
			return "", exec.ErrNotFound
		}
	}
	for _, tc := range []struct {
		name  string
		kind  Kind
		tools []string
		want  string
		err   error
	}{
		{"apt-get", LinuxDeb, []string{"pkexec", "apt-get", "dpkg"}, "/usr/bin/pkexec /usr/bin/apt-get install -y /s/x.deb", nil},
		{"dpkg alone", LinuxDeb, []string{"pkexec", "dpkg"}, "/usr/bin/pkexec /usr/bin/dpkg -i /s/x.deb", nil},
		{"dnf", LinuxRPM, []string{"pkexec", "dnf", "rpm"}, "/usr/bin/pkexec /usr/bin/dnf install -y /s/x.deb", nil},
		{"zypper", LinuxRPM, []string{"pkexec", "zypper", "rpm"}, "/usr/bin/pkexec /usr/bin/zypper --non-interactive install /s/x.deb", nil},
		{"rpm alone", LinuxRPM, []string{"pkexec", "rpm"}, "/usr/bin/pkexec /usr/bin/rpm -U /s/x.deb", nil},
		{"no pkexec", LinuxDeb, []string{"apt-get"}, "", ErrNoPackageTool},
		{"no manager", LinuxRPM, []string{"pkexec"}, "", ErrNoPackageTool},
		{"not a package", LinuxTar, []string{"pkexec", "dpkg"}, "", ErrUnsupported},
	} {
		argv, err := packageCommand(tc.kind, "/s/x.deb", have(tc.tools...))
		if got := strings.Join(argv, " "); got != tc.want || !errors.Is(err, tc.err) {
			t.Errorf("%s: %q %v, want %q %v", tc.name, got, err, tc.want, tc.err)
		}
	}
}
