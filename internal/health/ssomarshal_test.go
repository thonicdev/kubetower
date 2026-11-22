package health

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The SSO sessions read from the AWS CLI's cache marshal without the tokens
// the cache files hold: the list names a session and its expiry, and the access
// token is a live credential to every account the session reaches.
func TestMarshalledSSOSessionsCarryNoToken(t *testing.T) {
	const access, refresh = "a-known-access-token-that-must-not-leave", "a-known-refresh-token-that-must-not-leave"
	dir := t.TempDir()
	raw, err := json.Marshal(map[string]any{
		"accessToken":           access,
		"refreshToken":          refresh,
		"expiresAt":             time.Now().Add(8 * time.Hour).UTC().Format(time.RFC3339),
		"registrationExpiresAt": time.Now().Add(80 * 24 * time.Hour).UTC().Format(time.RFC3339),
		"startUrl":              "https://example.awsapps.com/start",
		"sessionName":           "prod",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	h := New(Options{AWSDir: dir})
	list := h.SSOSessions()
	if len(list.Sessions) != 1 || list.Sessions[0].Session != "prod" {
		t.Fatalf("the cache's one session was not read, so the check would see nothing: %+v", list)
	}
	for name, v := range map[string]any{"sessions": list, "session": h.SSO()} {
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, token := range []string{access, refresh} {
			if strings.Contains(string(out), token) {
				t.Errorf("the marshalled %s carries a token from the cache: %s", name, out)
			}
		}
	}
}
