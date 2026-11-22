package auth

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// **Changing the password ends the sessions the old one opened.** A
// shared-account session is a stateless, signed cookie; signed with
// KT_SESSION_SECRET alone, a deployment that changed KT_PASSWORD_HASH because
// the password had leaked, and left the secret as it was, would leave every
// session the leaked password had opened valid for up to thirty days idle and
// ninety in all. A shared-account token is signed with a key derived from
// both, so the password hash is one of the levers that ends them.

// withHash is testAuth with another password, and the same session key.
func withHash(t *testing.T, password string) *Auth {
	t.Helper()
	a := testAuth(t)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(Config{
		User:         a.cfg.User,
		PasswordHash: string(hash),
		SessionKey:   a.cfg.SessionKey,
		SessionIdle:  a.cfg.SessionIdle,
		SessionMax:   a.cfg.SessionMax,
		CookieName:   a.cfg.CookieName,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

// The acceptance test: a token minted under hash A is
// refused under hash B - for each way a shared-account session is opened.
func TestATokenMintedUnderOnePasswordIsRefusedUnderTheNext(t *testing.T) {
	before := testAuth(t)
	after := withHash(t, "a-new-password-after-the-leak")
	if string(before.cfg.SessionKey) != string(after.cfg.SessionKey) {
		t.Fatal("the two front doors must share the session key; only the password changed")
	}
	now := time.Now()
	for _, opened := range []openedBy{byPassword, byLaunch, byWindow} {
		token, _ := before.mint(now, now, "", opened)
		if !before.validToken(token) {
			t.Fatalf("opened %d: the token is not valid under the password that minted it", opened)
		}
		if after.validToken(token) {
			t.Errorf("opened %d: a session opened under the old password is still valid after it changed, "+
				"with the session key untouched", opened)
		}
	}
}

// **And nothing else ends them.** A restart with the same password and the same
// key keeps every session - which is what the desktop's stored key is for, and
// what several replicas sharing one secret rely on.
func TestTheSamePasswordAndKeyKeepTheSession(t *testing.T) {
	a := testAuth(t)
	b, err := New(Config{
		User: a.cfg.User, PasswordHash: a.cfg.PasswordHash, SessionKey: a.cfg.SessionKey,
		SessionIdle: a.cfg.SessionIdle, SessionMax: a.cfg.SessionMax, CookieName: a.cfg.CookieName,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !b.validToken(a.issueToken()) {
		t.Error("a second front door with the same password and key refused the first one's session")
	}
}

// A single sign-on session is not the shared password's, so changing the
// password does not end it: its token is signed with the session key as before,
// and its record in the store is what a sign-out ends.
func TestAPasswordChangeLeavesASingleSignOnTokenAlone(t *testing.T) {
	before := testAuth(t)
	after := withHash(t, "a-new-password-after-the-leak")
	now := time.Now()
	sid := strings.Repeat("ab", sessionIDBytes)
	token, _ := before.mint(now, now, sid, byPassword)
	c, ok := after.parseToken(token)
	if !ok || c.sid != sid {
		t.Errorf("a single sign-on token did not survive a change of the shared password: ok=%v sid=%q", ok, c.sid)
	}
}

// **The two keys cannot stand in for each other.** A shared-account payload
// signed with the session key - which is how a cookie of an older shape is
// signed - is refused, and so is a single sign-on payload signed with
// the shared account's key: each kind of token verifies under its own key only.
func TestEachKindOfTokenVerifiesUnderItsOwnKeyOnly(t *testing.T) {
	a := testAuth(t)
	now := time.Now()

	shared, _ := a.mint(now, now, "", byPassword)
	payload, _, _ := strings.Cut(shared, "|")
	if _, ok := a.parseToken(payload + "|" + a.sign(payload)); ok {
		t.Error("a shared-account payload signed with the bare session key was accepted: " +
			"every cookie from before the password was bound in would still open the console")
	}

	sid := strings.Repeat("cd", sessionIDBytes)
	single, _ := a.mint(now, now, sid, byPassword)
	payload, _, _ = strings.Cut(single, "|")
	if _, ok := a.parseToken(payload + "|" + macOf(a.accountKey, payload)); ok {
		t.Error("a single sign-on payload signed with the shared account's key was accepted")
	}
}
