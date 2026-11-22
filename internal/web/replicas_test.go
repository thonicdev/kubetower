package web

import (
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
)

// What one of several replicas says about itself, and what its Ports panel draws.
//
// `internal/api` decides which addresses exist and asserts it by walking its
// router; this is the half that describes the result to the front end, so that
// it draws no control whose address is missing. Both arms, because "false" is
// also what a capability nobody set reads as.

func TestOneOfSeveralReplicasSaysItKeepsNoPreferencesAndOpensNoForward(t *testing.T) {
	s := serverFor(t, true)
	s.replicated = true
	s.forwards = nil
	caps := apiProfile{s}.Capabilities(nil)
	if caps.Preferences {
		t.Error("one of several replicas says it keeps preferences, and it registers none of the writes")
	}
	if !caps.Replicated {
		t.Error("one of several replicas does not say so, and the settings page keys its writes on it")
	}
	if caps.PortForward {
		t.Error("one of several replicas says it can forward, and it has no forwarder")
	}

	why := s.forwardingRefusal()
	if !strings.Contains(why, "several replicas") {
		t.Errorf("the reason a port cannot be forwarded does not say why: %q", why)
	}
}

// TestTheServerProfileKeepsItsPortsForwardable pins what the Ports panel draws
// in cluster, with one replica or several: `capabilities.portForward` is false,
// which is *show the command*, and the command line is drawn only for a port
// marked forwardable. The forwarder is nil there by construction, so a gate on
// "no forwarder" alone would mark every port unforwardable and the
// `kubectl port-forward` line would vanish under the whole profile.
func TestTheServerProfileKeepsItsPortsForwardable(t *testing.T) {
	for _, replicated := range []bool{false, true} {
		s := serverFor(t, true)
		s.replicated = replicated
		s.forwards = nil
		ports := s.portsFor("prod", []api.ObjectPort{{Port: 80, Forwardable: true}, {Port: 443, Forwardable: true}})
		for _, port := range ports {
			if !port.Forwardable || port.Reason != nil {
				t.Errorf("in cluster (several replicas: %v), port %d is drawn as %+v: the panel would draw "+
					"no kubectl port-forward command for it", replicated, port.Port, port)
			}
		}
	}
}

// The control: outside the profile, a console with no forwarder draws every port
// as a disabled control carrying why - the answer the profile must not inherit.
func TestADesktopWithNoForwarderDisablesItsPorts(t *testing.T) {
	s := serverFor(t, false)
	s.forwards = nil
	why := s.forwardingRefusal()
	if why == "" {
		t.Fatal("a console with no forwarder says it forwards")
	}
	for _, port := range s.portsFor("prod", []api.ObjectPort{{Port: 80, Forwardable: true}}) {
		if port.Forwardable || port.Reason == nil || port.Reason.Message != why {
			t.Errorf("port %d is drawn as %+v, expected a disabled control carrying why", port.Port, port)
		}
	}
}

// One replica keeps preferences on the desktop only: under the server profile
// the twelve writes are not registered on one replica either, because
// what they keep would be everybody's. It is still not `replicated`.
func TestOneReplicaSaysItKeepsPreferences(t *testing.T) {
	for _, inCluster := range []bool{false, true} {
		s := serverFor(t, inCluster)
		caps := apiProfile{s}.Capabilities(nil)
		if caps.Preferences == inCluster {
			t.Errorf("a single replica (in cluster: %v) says preferences = %v", inCluster, caps.Preferences)
		}
		if caps.Replicated {
			t.Errorf("a single replica (in cluster: %v) says it is one of several", inCluster)
		}
		if why := s.forwardingRefusal(); s.forwards != nil && why != "" {
			t.Errorf("a single replica with a forwarder refuses to forward: %q", why)
		}
	}
}

// On a desktop that forwards, a cluster marked read-only
// draws its forwardable ports as disabled controls carrying the marking's
// sentence, because the forwarder refuses it - a button whose click is then
// refused is the silent half to avoid. A UDP port keeps its own reason; an
// unmarked cluster keeps its button; the server profile keeps its command.
func TestAReadOnlyClusterDisablesItsForwardsWithTheMarking(t *testing.T) {
	s := serverFor(t, false)
	s.forwards = forwardsFixture{}
	udp := api.Because(api.CodeNotSupportedHere, "port-forwarding is TCP, and this port is UDP")
	ports := func() []api.ObjectPort {
		return []api.ObjectPort{{Port: 80, Forwardable: true}, {Port: 53, Protocol: "UDP", Reason: &udp}}
	}

	s.readOnly = func(name string) bool { return name == "prod" }
	got := s.portsFor("prod", ports())
	if got[0].Forwardable || got[0].Reason == nil ||
		!strings.Contains(got[0].Reason.Message, "marked read-only in KT_READONLY_CLUSTERS") {
		t.Errorf("port 80 on a read-only cluster is drawn as %+v, expected a disabled control naming the marking", got[0])
	}
	if got[0].Reason != nil && got[0].Reason.Code != api.CodeVerbDenied {
		t.Errorf("the read-only reason's code is %q", got[0].Reason.Code)
	}
	if got[1].Reason == nil || got[1].Reason.Message != udp.Message {
		t.Errorf("the UDP port lost its own reason: %+v", got[1])
	}

	s.readOnly = func(name string) bool { return name == "elsewhere" }
	if got := s.portsFor("prod", ports()); !got[0].Forwardable || got[0].Reason != nil {
		t.Errorf("port 80 on an unmarked cluster is drawn as %+v, expected a Forward button", got[0])
	}

	profile := serverFor(t, true)
	profile.readOnly = func(string) bool { return true }
	if got := profile.portsFor("prod", ports()); !got[0].Forwardable {
		t.Errorf("under the server profile a read-only cluster lost its kubectl command: %+v", got[0])
	}
}
