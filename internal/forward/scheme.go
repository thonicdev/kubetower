package forward

// Which scheme a tunnel's local address should be dialled with.
//
// TLS travels through a port-forward fine; what can go wrong is the link. A
// plain "http://127.0.0.1:51423" for a TLS port answers "The plain HTTP request
// was sent to an HTTPS port" or an empty reply, which reads as a broken tunnel.
//
// The guess is made here, not in the browser, so there is one table. The
// operator can override it on the row, since a guess is sometimes wrong.
//
// An https tunnel is dialled as 127.0.0.1, which no in-cluster certificate
// names, so every https forward shows a certificate warning. That is expected
// and is not a fault of the tunnel; switching to http instead would turn a
// readable warning into an error.

import "strings"

const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"
)

// tlsPorts are the numbers that mean TLS by convention: https, API servers and
// webhooks, cert-manager and controller-runtime's webhook port, the kubelet,
// and Istio's gateway. A number is the weakest evidence, so it is consulted
// last.
var tlsPorts = map[int]bool{
	443:   true,
	6443:  true,
	8443:  true,
	9443:  true,
	10250: true,
	15443: true,
}

// tlsAppProtocols and plainAppProtocols are the `appProtocol` values this
// recognizes. That field exists to answer exactly this question, so a declared
// one wins over the name and the number.
//
// An unrecognized value (`grpc`) says nothing about TLS and falls through
// rather than reading as plaintext. Kubernetes allows IANA names and
// domain-prefixed ones, hence the `kubernetes.io/` entries.
var (
	tlsAppProtocols = map[string]bool{
		"https":             true,
		"tls":               true,
		"ssl":               true,
		"wss":               true,
		"kubernetes.io/wss": true,
	}
	plainAppProtocols = map[string]bool{
		"http":              true,
		"ws":                true,
		"h2c":               true,
		"kubernetes.io/ws":  true,
		"kubernetes.io/h2c": true,
	}
)

// tlsNames are port names that mean TLS without saying https. `webhook`
// matters most: an admission webhook must be served over TLS, and its port is
// often named exactly that.
var tlsNames = map[string]bool{
	"webhook":  true,
	"webhooks": true,
	"wss":      true,
	"tls":      true,
	"secure":   true,
}

// schemeFor decides between http and https for one forwarded port, in
// descending order of authority:
//
//  1. `appProtocol`, when the object declares one this recognizes;
//  2. the port's name: anything containing https, tls or ssl, or a name in
//     tlsNames. A name containing http (but not https) states plaintext and
//     stops the search, so `http` on 8443 stays http;
//  3. the port number, from tlsPorts.
//
// Anything else is http: most things are, and a wrong https adds a certificate
// warning to the wrong answer.
func schemeFor(appProtocol, portName string, port int) string {
	if scheme, ok := fromAppProtocol(appProtocol); ok {
		return scheme
	}
	if scheme, ok := fromPortName(portName); ok {
		return scheme
	}
	if tlsPorts[port] {
		return schemeHTTPS
	}
	return schemeHTTP
}

func fromAppProtocol(appProtocol string) (string, bool) {
	switch value := strings.ToLower(strings.TrimSpace(appProtocol)); {
	case value == "":
		return "", false
	case tlsAppProtocols[value]:
		return schemeHTTPS, true
	case plainAppProtocols[value]:
		return schemeHTTP, true
	default:
		return "", false
	}
}

func fromPortName(portName string) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(portName))
	if name == "" {
		return "", false
	}
	switch {
	// https before http, or every name containing https would match the
	// plaintext rule first.
	case strings.Contains(name, "https"),
		strings.Contains(name, "tls"),
		strings.Contains(name, "ssl"),
		tlsNames[name]:
		return schemeHTTPS, true
	case strings.Contains(name, "http"), strings.Contains(name, "h2c"), name == "ws":
		return schemeHTTP, true
	}
	return "", false
}
