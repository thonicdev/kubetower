package web

import "testing"

// TestTheInfosTabIsToldHowThisConsoleIsSignedInto. The facts about how this
// console is signed into - no password here, loopback only - are said on the
// Infos tab, and the tab can only say them
// if this package hands them over. `listenAddress` is in the contract and
// drawn by the page, and this asserts that something fills it.
func TestTheInfosTabIsToldHowThisConsoleIsSignedInto(t *testing.T) {
	desktop := serverWithState(t, false)
	desktop.setup.ListenAddress = "127.0.0.1:5823"
	desktop.setup.WithoutPassword = true
	info := settingsOf(desktop).Info
	if info.ListenAddress != "127.0.0.1:5823" {
		t.Errorf("info.listenAddress = %q, want the address bound", info.ListenAddress)
	}
	if !info.WithoutPassword {
		t.Error("info.withoutPassword is false on a console that asks nobody for a password")
	}

	// And a console that said nothing is not described as passwordless: the
	// tab would tell somebody behind a password that there is none.
	if settingsOf(serverWithState(t, false)).Info.WithoutPassword {
		t.Error("info.withoutPassword is true on a console nobody said was passwordless")
	}
}
