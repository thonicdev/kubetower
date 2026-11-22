package launch

// An install upgraded onto the
// loopback desktop keeps the password an earlier version drew, and its files,
// and says so rather than "password read from", which claims it is in use.

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestAnUpgradedLoopbackDesktopSaysItsPasswordIsKeptAndNotOffered(t *testing.T) {
	dir := aFirstRun(t, "127.0.0.1:5823", "false")
	hash, err := bcrypt.GenerateFromPassword([]byte(t.Name()), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	earlier, err := json.Marshal(credentials{User: "admin", PasswordHash: string(hash),
		SessionSecret: hex.EncodeToString(key)})
	if err != nil {
		t.Fatal(err)
	}
	creds := filepath.Join(dir, credentialsFile)
	once := filepath.Join(dir, passwordOnceFile)
	if err := os.WriteFile(creds, earlier, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(once, []byte("an earlier version's drawn password\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	said := captureLog(t)
	front, _, err := frontDoor(false, nil)
	if err != nil {
		t.Fatalf("frontDoor: %v", err)
	}
	if front.OffersPassword() {
		t.Error("the upgraded loopback desktop offers the earlier password")
	}
	if strings.Contains(said.String(), "password read from") {
		t.Errorf("the start says the earlier password is read for use:\n%s", said.String())
	}
	for _, want := range []string{"it is not offered here", once} {
		if !strings.Contains(said.String(), want) {
			t.Errorf("the start does not say %q:\n%s", want, said.String())
		}
	}
	if raw, err := os.ReadFile(creds); err != nil || !strings.Contains(string(raw), string(hash)) {
		t.Errorf("credentials.json lost the earlier password's hash (%v)", err)
	}
	if _, err := os.Stat(once); err != nil {
		t.Errorf("%s was deleted: %v", passwordOnceFile, err)
	}
}
