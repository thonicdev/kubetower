package forward

// The scheme guess, and the three cases that decide its shape.
//
// It is a pure function over (appProtocol, name, number), so the whole rule is
// testable without a cluster, a listener or a browser — which is the reason the
// decision lives on this side rather than in dock.js.

import (
	"context"
	"testing"
)

func TestSchemeForReadsWhatKubernetesSays(t *testing.T) {
	cases := []struct {
		why         string
		appProtocol string
		name        string
		port        int
		want        string
	}{
		// The declaration beats the number, in both directions. This is the pair
		// the ordering exists for: `appProtocol` is the field Kubernetes added to
		// answer exactly this question, so a service saying `http` on 443 is
		// telling us the number is misleading, and it must be believed.
		{"appProtocol http on 443", "http", "https", 443, schemeHTTP},
		{"appProtocol https on 80", "https", "http", 80, schemeHTTPS},
		{"appProtocol is case-insensitive", "HTTPS", "", 8080, schemeHTTPS},
		{"appProtocol domain-prefixed wss", "kubernetes.io/wss", "", 8080, schemeHTTPS},
		{"appProtocol domain-prefixed h2c on 443", "kubernetes.io/h2c", "", 443, schemeHTTP},

		// An appProtocol this does not recognize says nothing about TLS either
		// way, so it must fall through rather than be read as plaintext.
		{"grpc on 443 falls through to the number", "grpc", "", 443, schemeHTTPS},
		{"grpc on 8080 falls through to the number", "grpc", "", 8080, schemeHTTP},

		// The name beats the number, also in both directions.
		{"named https on 8080", "", "https", 8080, schemeHTTPS},
		{"named https-web on 8080", "", "https-web", 8080, schemeHTTPS},
		{"named metrics-tls on 8080", "", "metrics-tls", 8080, schemeHTTPS},
		{"named webhook on 8080", "", "webhook", 8080, schemeHTTPS},
		{"named http on 8443", "", "http", 8443, schemeHTTP},
		{"named http-metrics on 443", "", "http-metrics", 443, schemeHTTP},
		{"a name nothing recognizes falls through", "", "postgres", 443, schemeHTTPS},

		// And the number, when there is nothing else.
		{"unnamed 8080", "", "", 8080, schemeHTTP},
		{"unnamed 443", "", "", 443, schemeHTTPS},
		{"unnamed 8443", "", "", 8443, schemeHTTPS},
		{"unnamed 6443", "", "", 6443, schemeHTTPS},
		{"unnamed 9443", "", "", 9443, schemeHTTPS},
		{"unnamed 10250", "", "", 10250, schemeHTTPS},
		{"unnamed 15443", "", "", 15443, schemeHTTPS},
		{"unnamed 80", "", "", 80, schemeHTTP},
		{"unnamed 9153", "", "", 9153, schemeHTTP},
		{"nothing at all", "", "", 0, schemeHTTP},
	}

	for _, c := range cases {
		t.Run(c.why, func(t *testing.T) {
			got := schemeFor(c.appProtocol, c.name, c.port)
			if got != c.want {
				t.Errorf("schemeFor(%q, %q, %d) = %q, want %q",
					c.appProtocol, c.name, c.port, got, c.want)
			}
		})
	}
}

// The scheme reaches the tunnel the row is drawn from, or the whole rule is
// unreachable: the browser reads exactly one field.
func TestOpenStampsTheSchemeOnTheTunnel(t *testing.T) {
	cases := []struct {
		why  string
		req  Request
		want string
	}{
		{
			why:  "a plain metrics port",
			req:  Request{Cluster: "docker-desktop", Namespace: "kube-system", Service: "kube-dns", Port: 9153, PortName: "metrics"},
			want: schemeHTTP,
		},
		{
			why:  "an ingress controller's 443",
			req:  Request{Cluster: "docker-desktop", Namespace: "ingress", Service: "controller", Port: 443, PortName: "https"},
			want: schemeHTTPS,
		},
		{
			why:  "a webhook, named and on 9443",
			req:  Request{Cluster: "docker-desktop", Namespace: "cert-manager", Service: "webhook", Port: 9443, PortName: "webhook"},
			want: schemeHTTPS,
		},
		{
			why:  "a 443 the service says is plaintext",
			req:  Request{Cluster: "docker-desktop", Namespace: "odd", Service: "odd", Port: 443, AppProtocol: "http"},
			want: schemeHTTP,
		},
	}

	for _, c := range cases {
		t.Run(c.why, func(t *testing.T) {
			m := testManager(t, &fakeDigger{})
			tunnel, err := m.Open(context.Background(), c.req)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if tunnel.Scheme != c.want {
				t.Errorf("Scheme = %q, want %q", tunnel.Scheme, c.want)
			}
			// The row builds its href as scheme + "://" + localAddr, so an empty
			// scheme would produce "://127.0.0.1:51423" — a link that opens
			// nothing, silently.
			if tunnel.Scheme == "" {
				t.Error("the scheme is empty, which the dock would render as ://127.0.0.1:…")
			}
		})
	}
}

// A port asked for with nothing said about it still has to come back with a
// scheme, and the number it is judged on is where the tunnel landed — the only
// number there is when the operator asked for none.
func TestOpenWithoutAPortStillDecidesAScheme(t *testing.T) {
	digger := &fakeDigger{}
	m := testManager(t, digger)

	tunnel, err := m.Open(context.Background(), Request{
		Cluster: "docker-desktop", Namespace: "kube-system", Service: "single-port",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// The fake lands on 8080 when nothing was asked for, which is plaintext.
	if tunnel.Port != 8080 {
		t.Fatalf("Port = %d, want the port the tunnel landed on (8080)", tunnel.Port)
	}
	if tunnel.Scheme != schemeHTTP {
		t.Errorf("Scheme = %q, want %q", tunnel.Scheme, schemeHTTP)
	}
}

// Two clicks on the same port are one tunnel, and the name and appProtocol must
// not change that: they are evidence about a port, not part of its identity. A
// requestKey that included them would hand the operator two tunnels on two local
// ports for one button, which is the failure Open's idempotence exists to
// prevent.
func TestTheSchemeFieldsAreNotPartOfATargetsIdentity(t *testing.T) {
	base := Request{Cluster: "docker-desktop", Namespace: "kube-system", Service: "kube-dns", Port: 9153}
	named := base
	named.PortName = "metrics"
	named.AppProtocol = "http"

	if requestKey(base) != requestKey(named) {
		t.Errorf("keys differ:\n  %q\n  %q\nnaming a port must not make it a second target",
			requestKey(base), requestKey(named))
	}
}
