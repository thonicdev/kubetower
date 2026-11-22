package launch

// A browser window's one-time code is
// never put on a command line, where on Linux every local account can read it.

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const pageCode = "a-browser-windows-one-time-code-0123456789AB"

func fixedMint(native bool) (string, error) { return pageCode, nil }

// pagePath is the file a file:// address names.
func pagePath(t *testing.T, target string) string {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "file" {
		t.Fatalf("the browser was handed %q; want a file:// address", target)
	}
	p := u.Path
	if runtime.GOOS == "windows" {
		p = strings.TrimPrefix(p, "/")
	}
	return filepath.FromSlash(p)
}

func TestABrowserWindowsCodeIsNeverOnItsCommandLine(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KT_STATE_DIR", dir)

	target := browserTarget("127.0.0.1:5823", fixedMint, "")
	if strings.Contains(target, pageCode) {
		t.Fatalf("the browser is handed the code itself: %q", target)
	}
	_, args, _ := chromiumAppCommand(target)
	for _, arg := range args {
		if strings.Contains(arg, pageCode) {
			t.Errorf("Chromium's argv carries the code: %q", arg)
		}
	}

	path := pagePath(t, target)
	if filepath.Dir(path) != dir {
		t.Errorf("the page is in %s; want the state directory %s", filepath.Dir(path), dir)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the page the browser was handed cannot be read: %v", err)
	}
	if !strings.Contains(string(raw), windowURL("127.0.0.1:5823", pageCode)) {
		t.Errorf("the page does not forward to the window's launch address:\n%s", raw)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("the page is %o; want 600", perm)
		}
	}
}

func TestNoPageIsWrittenWhereThereIsNoCode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KT_STATE_DIR", dir)
	if got := browserTarget("127.0.0.1:5823", nil, ""); got != consoleURL("127.0.0.1:5823") {
		t.Errorf("with no mint the browser is handed %q; want the bare console address", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a page was written with no code to carry: %v", entries)
	}
}

func TestThePageCannotBeBrokenOutOfByItsAddress(t *testing.T) {
	page := launchPage(`http://127.0.0.1:5823/auth/launch?code=x"></script><script>alert(1)</script>&a=<b>`)
	if strings.Contains(page, "<script>alert(1)") || strings.Contains(page, `"></script>`) {
		t.Errorf("the address ended the page's markup:\n%s", page)
	}
}

func TestOldPagesAreSweptAndTheirNeighboursKept(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old, err := writeLaunchPage(dir, "http://127.0.0.1:5823/", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(old, now.Add(-launchPageLife), now.Add(-launchPageLife)); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(other, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh, err := writeLaunchPage(dir, "http://127.0.0.1:5823/", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("a page older than its life was kept")
	}
	for _, keep := range []string{fresh, other} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s was swept: %v", filepath.Base(keep), err)
		}
	}
}

func TestAFileAddressNamesThePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launch-1.html")
	if got := pagePath(t, fileURL(path)); got != path {
		t.Errorf("fileURL(%q) names %q", path, got)
	}
}
