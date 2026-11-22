package ekstoken

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// `aws sso login` in a terminal, then Refresh, on a provider built while the
// session had lapsed: the next token is signed, with no restart.
//
// Nothing about the session may be held from the failed attempt: the provider
// is built once per context and lives as long as the process, so a failure
// kept anywhere in it would be the same "session expired" until the console is
// restarted. The SSO portal is a local server (AWS_ENDPOINT_URL_SSO); the
// token cache is the file the CLI writes.
func TestALoginAfterAFailureIsUsedByTheSameProvider(t *testing.T) {
	awsFiles(t, ssoSessionProfile, "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	const accessToken = "fixture-access-token"
	var asked atomic.Int32
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		if r.URL.Path != "/federation/credentials" || r.Header.Get("x-amz-sso_bearer_token") != accessToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Session token not found or invalid"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"roleCredentials":{"accessKeyId":%q,"secretAccessKey":%q,"sessionToken":"s","expiration":%d}}`,
			fakeKeyID, fakeSecret, time.Now().Add(time.Hour).UnixMilli())
	}))
	defer portal.Close()
	t.Setenv("AWS_ENDPOINT_URL_SSO", portal.URL)

	provider, err := New(context.Background(), Spec{Cluster: "prod", Region: "eu-west-3", Profile: "prod-devops"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// No session yet: refused before anything reaches the portal, in the words
	// internal/health classifies as an SSO session to renew.
	if _, err := provider.Token(context.Background()); err == nil {
		t.Fatal("a token was signed with no SSO session")
	} else if msg := strings.ToLower(err.Error()); !strings.Contains(msg, "signing a token for") ||
		!strings.Contains(msg, "cached sso token") {
		t.Fatalf("the failure does not read as a lapsed session: %v", err)
	}

	// `aws sso login --sso-session corp` (or `--profile prod-devops`).
	cache := filepath.Join(home, ".aws", "sso", "cache")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum([]byte("corp"))
	body, _ := json.Marshal(map[string]string{
		"accessToken": accessToken,
		"expiresAt":   time.Now().Add(8 * time.Hour).UTC().Format(time.RFC3339),
		"region":      "eu-west-1",
		"startUrl":    "https://example.awsapps.com/start",
	})
	if err := os.WriteFile(filepath.Join(cache, hex.EncodeToString(sum[:])+".json"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	// Refresh.
	token, err := provider.Token(context.Background())
	if err != nil {
		t.Fatalf("the same provider still fails after the login: %v", err)
	}
	if !strings.HasPrefix(token, tokenPrefix) || asked.Load() == 0 {
		t.Fatalf("token %.20q…, portal asked %d times", token, asked.Load())
	}
	url, err := DecodeForDiagnosis(token)
	if err != nil || !strings.Contains(url, fakeKeyID) {
		t.Fatalf("the token was not signed with the session's role credentials: %v", err)
	}
}
