package web

import "testing"

// A build with no terminal layer at all offers neither, and `claude` being nil
// is a real configuration rather than a defensive check.
func TestNoTerminalLayerOffersNeither(t *testing.T) {
	caps := apiProfile{&Server{}}.Capabilities(nil)
	if caps.LocalShell || caps.DesktopTerminal {
		t.Fatalf("localShell=%v desktopTerminal=%v on a build with no terminal layer", caps.LocalShell, caps.DesktopTerminal)
	}
	if caps.Mode != "container" {
		t.Fatalf("mode = %q", caps.Mode)
	}
}
