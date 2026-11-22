package health

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The home's session card lists every SSO session the cache holds
// where SSO() keeps only the one that matters. The two must
// not disagree about which that is.

func writeSession(t *testing.T, dir, file string, payload map[string]any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEverySSOSessionIsListedOncePerNameFurthestExpiryFirst(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	stamp := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }

	writeSession(t, dir, "a.json", map[string]any{
		"expiresAt": stamp(-2 * time.Hour), "accessToken": "t", "sessionName": "old",
	})
	writeSession(t, dir, "b.json", map[string]any{
		"expiresAt": stamp(6 * time.Hour), "accessToken": "t", "sessionName": "corp",
	})
	// The same session in a second file, earlier: the later one stands.
	writeSession(t, dir, "c.json", map[string]any{
		"expiresAt": stamp(1 * time.Hour), "accessToken": "t", "sessionName": "corp",
	})
	writeSession(t, dir, "d.json", map[string]any{
		"expiresAt": stamp(3 * time.Hour), "accessToken": "t", "startUrl": "https://lab.awsapps.com/start",
	})
	// A client registration: an expiry and no token, so not a session.
	writeSession(t, dir, "reg.json", map[string]any{
		"expiresAt": stamp(90 * 24 * time.Hour), "clientId": "x",
	})

	h := New(Options{AWSDir: dir})
	h.now = func() time.Time { return now }

	list := h.SSOSessions()
	if !list.Checked || list.Problem != "" {
		t.Fatalf("checked=%v problem=%q, want a cache that read", list.Checked, list.Problem)
	}
	var names, states []string
	for _, session := range list.Sessions {
		names = append(names, session.Session)
		states = append(states, session.State)
	}
	wantNames := []string{"corp", "https://lab.awsapps.com/start", "old"}
	wantStates := []string{SSOValid, SSOValid, SSOExpired}
	if len(names) != len(wantNames) {
		t.Fatalf("sessions %v, want %v: one per name, the registration left out", names, wantNames)
	}
	for i := range wantNames {
		if names[i] != wantNames[i] || states[i] != wantStates[i] {
			t.Errorf("session %d = %s/%s, want %s/%s", i, names[i], states[i], wantNames[i], wantStates[i])
		}
	}
	if got := list.Sessions[0].ExpiresAt; !got.Equal(now.Add(6 * time.Hour)) {
		t.Errorf("corp expires %v, want the later of its two files", got)
	}

	best := h.SSO()
	if best.Session != "corp" || !best.Found || !best.Checked {
		t.Errorf("SSO() = %+v, want the list's first entry", best)
	}
}

func TestAnSSOCacheThatWillNotListListsNoSessionAndSaysSo(t *testing.T) {
	dir := t.TempDir()
	h := New(Options{AWSDir: dir})
	h.readDir = refusingDir(&fs.PathError{Op: "open", Path: dir, Err: fs.ErrPermission})

	list := h.SSOSessions()
	if list.Checked {
		t.Fatal("a cache that could not be listed reported itself checked: the card would " +
			"say there is no session where nobody could look")
	}
	if len(list.Sessions) != 0 || list.Problem == "" {
		t.Errorf("sessions=%d problem=%q, want none and a reason", len(list.Sessions), list.Problem)
	}
}

func TestAnSSOCacheThatIsNotThereListsNoSessionAsAnAnswer(t *testing.T) {
	h := New(Options{AWSDir: filepath.Join(t.TempDir(), "absent")})
	list := h.SSOSessions()
	if !list.Checked || len(list.Sessions) != 0 {
		t.Errorf("checked=%v sessions=%d, want checked and empty", list.Checked, len(list.Sessions))
	}
	if got := h.SSO(); !got.Checked || got.Found || got.State != SSOAbsent {
		t.Errorf("SSO() = %+v, want checked and absent", got)
	}
}

// A session file carrying neither `sessionName` nor `startUrl` has no name to
// key on. Two of them are two sessions, not one.
func TestTwoUnnamedSSOSessionsAreNotFoldedIntoOne(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	writeSession(t, dir, "a.json", map[string]any{
		"expiresAt": now.Add(time.Hour).Format(time.RFC3339), "accessToken": "t",
	})
	writeSession(t, dir, "b.json", map[string]any{
		"expiresAt": now.Add(2 * time.Hour).Format(time.RFC3339), "accessToken": "t",
	})

	h := New(Options{AWSDir: dir})
	h.now = func() time.Time { return now }

	list := h.SSOSessions()
	if len(list.Sessions) != 2 {
		t.Fatalf("%d sessions, want the two unnamed files kept apart", len(list.Sessions))
	}
	for _, session := range list.Sessions {
		if session.Session != "" {
			t.Errorf("an unnamed session was given the name %q", session.Session)
		}
	}
}
