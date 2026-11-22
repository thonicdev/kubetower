//go:build windows

package launch

// On Windows, every file that
// holds a secret or a live code has an explicit, protected DACL that names its
// owner alone.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ownerOnly reports what is wrong with path's DACL, or "" when it is
// protected and holds one entry: an allow for the current user.
func ownerOnly(t *testing.T, path string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return "the DACL is not protected, so it inherits its directory's"
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl == nil || dacl.AceCount != 1 {
		return "the DACL does not hold exactly one entry"
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !sid.Equals(user.User.Sid) {
		return "the one entry is not an allow for the current user"
	}
	return ""
}

func TestTheLauncherSecretIsTheOwnersAloneOnWindows(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KT_STATE_DIR", dir)
	if _, err := launcherSecret(true); err != nil {
		t.Fatal(err)
	}
	if wrong := ownerOnly(t, filepath.Join(dir, launcherFile)); wrong != "" {
		t.Error(wrong)
	}
}

// A secret written without the DACL, with
// its directory's inherited ACL, is narrowed when the instance starts.
func TestAnExistingLauncherSecretIsNarrowedOnWindows(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KT_STATE_DIR", dir)
	path := filepath.Join(dir, launcherFile)
	if err := os.WriteFile(path, []byte(strings.Repeat("ab", launcherSecretBytes)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ownerOnly(t, path) == "" {
		t.Fatal("a plain write is already owner-only here, so this test proves nothing")
	}
	if _, err := launcherSecret(true); err != nil {
		t.Fatal(err)
	}
	if wrong := ownerOnly(t, path); wrong != "" {
		t.Errorf("the existing secret was not narrowed: %s", wrong)
	}
}

func TestTheLaunchPageIsTheOwnersAloneOnWindows(t *testing.T) {
	path, err := writeLaunchPage(t.TempDir(), "http://127.0.0.1:5823/auth/launch?code=x", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if wrong := ownerOnly(t, path); wrong != "" {
		t.Error(wrong)
	}
}

func TestTheRecordedAddressIsTheOwnersAloneOnWindows(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KT_STATE_DIR", dir)
	if err := writeInstanceAddress("127.0.0.1:5823"); err != nil {
		t.Fatal(err)
	}
	if wrong := ownerOnly(t, filepath.Join(dir, instanceAddressFile)); wrong != "" {
		t.Error(wrong)
	}
}

// credentials.json holds the session signing key
// and PASSWORD-ONCE.txt a password in plain text. Both are owner-only once
// written, and an earlier version's credentials.json is narrowed when read.
func TestTheCredentialsAreTheOwnersAloneOnWindows(t *testing.T) {
	dir := aFirstRun(t, "127.0.0.1:5823", "false")
	if _, _, err := localCredentialsFor(false); err != nil {
		t.Fatal(err)
	}
	if wrong := ownerOnly(t, filepath.Join(dir, credentialsFile)); wrong != "" {
		t.Errorf("a new credentials.json: %s", wrong)
	}

	once, err := writePasswordOnce("admin", t.Name(), filepath.Join(dir, credentialsFile))
	if err != nil {
		t.Fatal(err)
	}
	if wrong := ownerOnly(t, once); wrong != "" {
		t.Errorf("%s: %s", passwordOnceFile, wrong)
	}
}

func TestAnEarlierCredentialsFileIsNarrowedWhenReadOnWindows(t *testing.T) {
	dir := aFirstRun(t, "127.0.0.1:5823", "false")
	path := filepath.Join(dir, credentialsFile)
	earlier := `{"user":"admin","password_hash":"","session_secret":"` + strings.Repeat("ab", 32) + `"}`
	if err := os.WriteFile(path, []byte(earlier), 0o600); err != nil {
		t.Fatal(err)
	}
	if ownerOnly(t, path) == "" {
		t.Fatal("a plain write is already owner-only here, so this test proves nothing")
	}
	if _, _, err := localCredentialsFor(false); err != nil {
		t.Fatal(err)
	}
	if wrong := ownerOnly(t, path); wrong != "" {
		t.Errorf("an earlier credentials.json was not narrowed: %s", wrong)
	}
}
