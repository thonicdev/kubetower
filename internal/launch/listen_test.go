package launch

import (
	"bytes"
	"log"
	"net"
	"os"
	"strings"
	"testing"
)

// Where this listens when nothing says otherwise.
//
// Every interface would put a console holding administrator credentials for
// production clusters onto whatever network the machine is on, behind one
// password, with nothing anywhere saying so: running the binary outside a
// container is supported and the fastest loop.
func TestTheDefaultAddressIsLoopback(t *testing.T) {
	if !strings.HasPrefix(defaultListen, "127.0.0.1:") {
		t.Fatalf("the default is %q. Anything that is not loopback puts this console on the "+
			"network for anyone who runs it without setting KT_LISTEN", defaultListen)
	}
	if reachable(defaultListen) {
		t.Errorf("%q is reachable from off the machine", defaultListen)
	}
}

// The image overrides it, and must keep doing so: inside a container the
// container is the boundary, and Docker cannot route a published port to a
// socket bound to the container's own loopback. Losing this line makes the
// container unreachable — which is the failure direction we want, but it should
// not happen by accident.
func TestTheImageListensOnEveryInterfaceOnPurpose(t *testing.T) {
	dockerfile, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Skipf("no Dockerfile beside this test: %v", err)
	}
	// Derived from defaultListen rather than written out, so moving the port is
	// one edit: a literal port here would be one more thing to remember.
	_, port, err := net.SplitHostPort(defaultListen)
	if err != nil {
		t.Fatalf("defaultListen %q is not host:port: %v", defaultListen, err)
	}
	want := "ENV KT_LISTEN=:" + port
	if !strings.Contains(string(dockerfile), want) {
		t.Errorf("the image does not set %q. With the binary defaulting to loopback the "+
			"published port cannot reach it, and the container answers nothing", want)
	}
}

// The two spellings that mean "everything" without looking like it.
func TestWhatCountsAsReachableFromTheNetwork(t *testing.T) {
	for _, listen := range []string{":5823", "0.0.0.0:5823", "[::]:5823"} {
		if !reachable(listen) {
			t.Errorf("%q was not recognized as listening on every interface, so nobody is "+
				"warned about it", listen)
		}
	}
	for _, listen := range []string{"127.0.0.1:5823", "localhost:5823", "[::1]:5823"} {
		if reachable(listen) {
			t.Errorf("%q was reported as reachable from the network; a warning nobody needs is "+
				"a warning everybody learns to skip", listen)
		}
	}
	// An address the http package will reject is not this warning's problem.
	if reachable("nonsense") {
		t.Error("an unparseable address produced a warning about network exposure")
	}
}

// The warning is actually emitted.
//
// **What this does not cover, stated rather than implied**: that `main` still
// calls `announceListen`. Reaching that line means starting a real server on a
// real port, and a test that does so to check a log message is a test that will
// be flaky for reasons unrelated to what it is about. So the message and the
// condition are pinned here, and the single call site is not — a mutation that
// deletes the call passes this file. It is one line, immediately before
// `srv.Serve`, and the ordinary "listening on" line goes with it, so its
// absence is visible the first time anyone starts the process.
//
// Without it the message can be perfect and never reach a log — a mutation that
// silences the condition would pass everything else in this file.
func TestAnExposedAddressIsSaidOutLoud(t *testing.T) {
	capture := func(listen string) string {
		var buf bytes.Buffer
		previous := log.Writer()
		log.SetOutput(&buf)
		defer log.SetOutput(previous)
		announceListen(listen, false)
		return buf.String()
	}

	exposed := capture(":8091")
	if !strings.Contains(exposed, "not loopback") {
		t.Errorf("listening on every interface said nothing about it:\n%s", exposed)
	}
	if !strings.Contains(exposed, "administrator credentials") {
		t.Errorf("the warning does not say what is at stake, so it reads as pedantry:\n%s", exposed)
	}
	if !strings.Contains(exposed, "KT_LISTEN") {
		t.Errorf("the warning does not say which knob to turn:\n%s", exposed)
	}

	// In a container the same address is correct and must not be flagged: the
	// container is the boundary, the image binds this deliberately, and a
	// warning on every start is one nobody reads on the day it matters.
	inside := func() string {
		var buf bytes.Buffer
		previous := log.Writer()
		log.SetOutput(&buf)
		defer log.SetOutput(previous)
		announceListen(":8091", true)
		return buf.String()
	}()
	if strings.Contains(inside, "not loopback") {
		t.Errorf("the container was warned about its own correct binding, on every start:\n%s", inside)
	}
	if !strings.Contains(inside, "listening on") {
		t.Errorf("the container no longer says where it listens:\n%s", inside)
	}

	quiet := capture("127.0.0.1:8091")
	if strings.Contains(quiet, "not loopback") {
		t.Errorf("loopback produced the warning; one that fires every time is one nobody "+
			"reads:\n%s", quiet)
	}
	if !strings.Contains(quiet, "listening on") {
		t.Errorf("the ordinary line is gone:\n%s", quiet)
	}
}
