package launch

// The window a double-click opens on the loopback desktop
// arrives signed in. Its code is minted by windowMint, which must follow the
// launch offer and not the password - gated on the password, the very desktop
// that has none opened its first window with no code and no way in.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/thonicdev/kubetower/internal/auth"
)

// spends reports whether code, presented from loopback, opens a session.
func spends(t *testing.T, front *auth.Auth, code string) bool {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, windowURL("127.0.0.1:5823", code), nil)
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	front.LaunchSignIn(rec, req)
	return rec.Code == http.StatusFound && rec.Header().Get("Location") == "/" && len(rec.Result().Cookies()) > 0
}

func TestTheLoopbackDesktopsOwnWindowIsMintedACode(t *testing.T) {
	aFirstRun(t, "127.0.0.1:5823", "false")
	front, _, err := frontDoor(false, nil)
	if err != nil {
		t.Fatalf("frontDoor: %v", err)
	}
	if front.OffersPassword() {
		t.Fatal("the loopback desktop offers a password; this test is about the one that does not")
	}
	front.OfferLaunches() // as Run does, outside --incluster

	mint := windowMint(front, false)
	if mint == nil {
		t.Fatal("the loopback desktop's own window is given no way to mint a code, so it opens on a " +
			"page with no way in")
	}
	for _, native := range []bool{true, false} {
		code, err := mint(native)
		if err != nil {
			t.Fatalf("mint(native %v): %v", native, err)
		}
		if !spends(t, front, code) {
			t.Errorf("the code minted for a window (native %v) opened no session", native)
		}
	}

	if windowMint(front, true) != nil {
		t.Error("a code can be minted under --incluster")
	}
}

func TestAWindowIsMintedACodeWhereThePasswordIsKept(t *testing.T) {
	aFirstRun(t, "127.0.0.1:5823", "false")
	hash, err := bcrypt.GenerateFromPassword([]byte(t.Name()), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KT_PASSWORD_HASH", string(hash))
	front, _, err := frontDoor(false, nil)
	if err != nil {
		t.Fatalf("frontDoor: %v", err)
	}
	if !front.OffersPassword() {
		t.Fatal("KT_PASSWORD_HASH did not keep the password")
	}
	front.OfferLaunches()

	mint := windowMint(front, false)
	if mint == nil {
		t.Fatal("a window is given no code where the password is kept")
	}
	code, err := mint(true)
	if err != nil {
		t.Fatal(err)
	}
	if !spends(t, front, code) {
		t.Error("the window's code opened no session where the password is kept")
	}
}

func TestNoCodeIsMintedBeforeLaunchesAreOffered(t *testing.T) {
	aFirstRun(t, "127.0.0.1:5823", "false")
	front, _, err := frontDoor(false, nil)
	if err != nil {
		t.Fatalf("frontDoor: %v", err)
	}
	if windowMint(front, false) != nil {
		t.Error("a mint is handed out although launches were never offered")
	}
}
