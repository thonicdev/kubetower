package web

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/seen"
)

// What's new (#80): the release seen is kept in the state directory and read
// back with the preferences; the capabilities carry this build's release.
func TestTheVersionSeenIsReadBackWithThePreferences(t *testing.T) {
	prefs, server := prefsUnder(t)
	server.seen = seen.New(t.TempDir(), nil)
	if got := prefs.Preferences().SeenVersion; got != "" {
		t.Fatalf("nothing seen yet, read %q", got)
	}
	if err := prefs.SetSeenVersion("0.2.0"); err != nil {
		t.Fatal(err)
	}
	if got := prefs.Preferences().SeenVersion; got != "0.2.0" {
		t.Fatalf("read %q, want 0.2.0", got)
	}
	if err := prefs.SetSeenVersion("dev"); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("%v, want a refusal", err)
	}
}

func TestTheCapabilitiesCarryTheRelease(t *testing.T) {
	server := &Server{setup: SetupInfo{Version: "kubetower 0.2.0 (built 2026-12-01)", Release: "0.2.0"}}
	caps := apiProfile{server}.Capabilities(httptest.NewRequest("GET", "/api/capabilities", nil))
	if caps.Release != "0.2.0" {
		t.Fatalf("release %q", caps.Release)
	}
}
