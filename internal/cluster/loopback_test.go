package cluster

import (
	"testing"

	"k8s.io/client-go/rest"
)

func TestLoopbackAddressesAreRedirectedToTheHost(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"docker-desktop", "https://127.0.0.1:55594", "https://" + gatewayHost + ":55594"},
		{"kind", "https://127.0.0.1:6443", "https://" + gatewayHost + ":6443"},
		{"spelled as a name", "https://localhost:6443", "https://" + gatewayHost + ":6443"},
		{"spelled in capitals", "https://LOCALHOST:6443", "https://" + gatewayHost + ":6443"},
		{"minikube off center", "https://127.0.0.2:8443", "https://" + gatewayHost + ":8443"},
		{"IPv6", "https://[::1]:6443", "https://" + gatewayHost + ":6443"},
		{"no port", "https://127.0.0.1", "https://" + gatewayHost},
		{"a path survives", "https://127.0.0.1:6443/k8s", "https://" + gatewayHost + ":6443/k8s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, changed := loopbackTarget(c.in)
			if !changed {
				t.Fatalf("%s was left alone; it is the loopback and this process is in a container", c.in)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestEveryOtherAddressIsLeftAlone(t *testing.T) {
	// The rewrite is a targeted repair, not a policy. Touching a real cluster's
	// address would send production traffic to the developer's own machine.
	untouched := []string{
		"https://ABCD1234.gr7.eu-west-3.eks.amazonaws.com",
		"https://10.0.0.2:6443",
		"https://kubernetes.default.svc",
		"https://127-0-0-1.example.com:6443", // looks like it, is not
		"https://192.0.2.10:6443",
		"",
		"::not a url::",
	}
	for _, addr := range untouched {
		if got, changed := loopbackTarget(addr); changed {
			t.Errorf("%q was rewritten to %q and should not have been", addr, got)
		}
	}
}

// The half that is easy to forget. Redirecting the address without correcting
// the name turns "connection refused" into a certificate error — still broken,
// and now it looks like the cluster's fault.
func TestTheCertificateNameIsCorrectedTogetherWithTheAddress(t *testing.T) {
	cfg := &rest.Config{Host: "https://127.0.0.1:55594"}

	to, changed := redirectLoopback(cfg, true)
	if !changed {
		t.Fatal("a loopback address inside a container must be redirected")
	}
	if cfg.Host != to || cfg.Host != "https://"+gatewayHost+":55594" {
		t.Errorf("Host is %q, want %q", cfg.Host, "https://"+gatewayHost+":55594")
	}
	if cfg.TLSClientConfig.ServerName != certificateName {
		t.Errorf("ServerName is %q, want %q — the API server's certificate names %q, never %q",
			cfg.TLSClientConfig.ServerName, certificateName, certificateName, gatewayHost)
	}
}

// Verification itself must survive the rewrite. Only the name being matched
// changes; the CA still decides.
func TestTheRewriteDoesNotDisableVerification(t *testing.T) {
	cfg := &rest.Config{Host: "https://127.0.0.1:6443"}
	cfg.TLSClientConfig.CAData = []byte("a certificate authority")

	redirectLoopback(cfg, true)

	if cfg.TLSClientConfig.Insecure {
		t.Error("the redirect turned off certificate verification; it must only change the expected name")
	}
	if string(cfg.TLSClientConfig.CAData) != "a certificate authority" {
		t.Error("the redirect dropped the cluster's CA")
	}
}

// On the host, host.docker.internal is absent or points elsewhere. Rewriting
// there breaks every local cluster for anyone running the binary directly.
func TestOnTheHostNothingIsRewritten(t *testing.T) {
	cfg := &rest.Config{Host: "https://127.0.0.1:55594"}

	if _, changed := redirectLoopback(cfg, false); changed {
		t.Fatal("outside a container the loopback means the loopback")
	}
	if cfg.Host != "https://127.0.0.1:55594" {
		t.Errorf("Host was changed to %q", cfg.Host)
	}
	if cfg.TLSClientConfig.ServerName != "" {
		t.Errorf("ServerName was set to %q outside a container", cfg.TLSClientConfig.ServerName)
	}
}

func TestTheEnvironmentCanForceTheAnswerBothWays(t *testing.T) {
	for _, c := range []struct {
		value string
		want  bool
	}{{"1", true}, {"true", true}, {"TRUE", true}, {"0", false}, {"false", false}, {"False", false}} {
		t.Setenv("KT_IN_CONTAINER", c.value)
		if got := containerised(); got != c.want {
			t.Errorf("KT_IN_CONTAINER=%q gave %v, want %v", c.value, got, c.want)
		}
	}
}

func TestANilConfigIsNotADereference(t *testing.T) {
	if _, changed := redirectLoopback(nil, true); changed {
		t.Error("a nil config reported a change")
	}
}

// KT_IN_CONTAINER is read with the yes/no vocabulary every other KT_ switch
// uses - `1/true/yes/on` against `0/false/no/off` - and an unrecognized value
// is said out loud and left to detection.
//
// `no` and `off`, the two words a person writes to turn a thing off, must not
// *force* container mode: every 127.0.0.1 cluster would be rewritten to
// host.docker.internal and the loopback desktop would stop being passwordless.
// The detection column is what /.dockerenv would say, both
// ways, so a case that ignores the variable shows as following it.
func TestTheContainerSwitchReadsYesAndNoAndFallsBackOnAnythingElse(t *testing.T) {
	cases := []struct {
		raw      string
		detected bool
		want     bool
		logged   bool
	}{
		{"", false, false, false},
		{"", true, true, false},
		{"1", false, true, false},
		{"true", false, true, false},
		{"TRUE", false, true, false},
		{"yes", false, true, false},
		{"on", false, true, false},
		{" On ", false, true, false},
		{"0", true, false, false},
		{"false", true, false, false},
		{"no", true, false, false},
		{"off", true, false, false},
		{"OFF", true, false, false},
		{" no ", true, false, false},
		{"nope", false, false, true},
		{"nope", true, true, true},
		{"2", false, false, true},
		{"enabled", true, true, true},
	}
	for _, c := range cases {
		t.Run(c.raw+"/detected="+map[bool]string{true: "yes", false: "no"}[c.detected], func(t *testing.T) {
			var lines []string
			got := containerVerdict(c.raw, func() bool { return c.detected },
				func(format string, args ...any) { lines = append(lines, format) })
			if got != c.want {
				t.Errorf("KT_IN_CONTAINER=%q with detection saying %t read as %t, want %t",
					c.raw, c.detected, got, c.want)
			}
			if logged := len(lines) > 0; logged != c.logged {
				t.Errorf("KT_IN_CONTAINER=%q logged %v; an unrecognized value is said, a recognized one is not",
					c.raw, lines)
			}
		})
	}
}
