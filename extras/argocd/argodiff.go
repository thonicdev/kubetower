package argocd

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/extras/argocd/drift"
	"github.com/thonicdev/kubetower/textutil"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Argo CD's own diff, through argocd-server.
//
// The operator stores a read-only Argo CD token (an account allowed
// `applications, get` only) in a Secret beside the console. The token is read
// with the console's credentials, through the one door that reads only what
// the console's configuration names (Host.ConfiguredSecret, held to
// Configured below), and never leaves this process: it goes to
// argocd-server through a tunnel the console digs for itself
// (Host.ForwardAsConsole), so a person needs no port-forward into Argo CD's
// namespace, which would also reach argocd-repo-server and argocd-redis.
//
// **What a person is shown of it is theirs to read, and nothing else.** The
// tunnel is opened only after two of their own reviewed reads stood: the
// object (Delivery.resource, through Host.List by name) and the Application
// that lists it (findApplication, the same). argocd-server is asked about that
// one object, and of its answer only the item naming that object - group,
// kind, namespace and name - is kept; every other resource of the Application
// is dropped unread. A Secret's fields are masked.
//
// Unlike the last-applied drift, Argo CD compares git's rendering with the
// live object, ignoreDifferences and normalization included, so an unsynced
// commit shows too.

// TokenSecretEnv overrides where the token is: `namespace/name`.
const TokenSecretEnv = "KT_ARGOCD_TOKEN_SECRET"

// TokenSecretName is the Secret's default name, in the console's namespace.
const TokenSecretName = "kubetower-argocd-token"

// TokenKey is the key in the Secret that holds the token.
const TokenKey = "token"

// CAKey is the key in the token's Secret that holds, in PEM, what argocd-server's
// certificate is verified against: the CA that signed it, or the certificate
// itself. Without it, the system's roots.
//
// Argo CD's self-signed certificate (`tls.crt` in `argocd-secret`) is valid
// for a year and never renewed, so pinning it works only until then; a
// certificate in `argocd-server-tls` from a CA the operator controls lasts.
const CAKey = "ca.crt"

// ServerServiceEnv names argocd-server's Service: `namespace/name`.
const ServerServiceEnv = "KT_ARGOCD_SERVER_SERVICE"

// ServerServiceDefault is where Argo CD's upstream manifests, or a Helm release
// named argocd in the namespace argocd, put it.
const ServerServiceDefault = "argocd/argocd-server"

// ServerNameEnv is the name argocd-server's certificate is verified for. Unset,
// the configured Service's own DNS name, `<name>.<namespace>.svc`. Set it when
// the certificate names something else, such as the ingress's host.
const ServerNameEnv = "KT_ARGOCD_SERVER_NAME"

// serviceAccountNamespace is where an in-cluster console runs.
const serviceAccountNamespace = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// ArgoDiff is Argo CD's own comparison of the object, or why there is none.
type ArgoDiff struct {
	// Compared is true when argocd-server answered for this object.
	Compared bool          `json:"compared"`
	Masked   bool          `json:"masked"`
	Fields   []drift.Field `json:"fields"`
	// Setup is true when the token's Secret does not exist: the page then
	// says how to turn the diff on, rather than a failure.
	Setup bool `json:"setup"`
	// Secret is where the token is looked for, `namespace/name`.
	Secret string `json:"secret,omitempty"`
	Note   string `json:"note,omitempty"`
	// NotInGit is argocd-server's answer that git no longer has the object,
	// an answer rather than a failure.
	NotInGit bool `json:"notInGit,omitempty"`
}

// namespacedName reads a `namespace/name` variable, or says why it cannot. An
// unset variable is ok=false, for the caller's default; a value that is not
// exactly two non-empty parts is an error, never a silent default.
func namespacedName(env string) (ns, name string, ok bool, err error) {
	v := strings.TrimSpace(os.Getenv(env))
	if v == "" {
		return "", "", false, nil
	}
	ns, name, cut := strings.Cut(v, "/")
	if !cut || ns == "" || name == "" || strings.Contains(name, "/") {
		return "", "", false, fmt.Errorf("%s is %q, not namespace/name", env, v)
	}
	return ns, name, true, nil
}

// tokenSecret is where the token is: the override, or the console's own
// namespace (kubetower outside a cluster).
func tokenSecret() (string, string, error) {
	ns, name, ok, err := namespacedName(TokenSecretEnv)
	if ok || err != nil {
		return ns, name, err
	}
	ns = "kubetower"
	if raw, err := os.ReadFile(serviceAccountNamespace); err == nil && strings.TrimSpace(string(raw)) != "" {
		ns = strings.TrimSpace(string(raw))
	}
	return ns, TokenSecretName, nil
}

// serverService is argocd-server's Service as the configuration names it,
// never as a label search finds it: a label is written by whoever may create a
// Service, and the token goes where the Service leads.
func serverService() (string, string, error) {
	ns, name, ok, err := namespacedName(ServerServiceEnv)
	if ok || err != nil {
		return ns, name, err
	}
	ns, name, _ = strings.Cut(ServerServiceDefault, "/")
	return ns, name, nil
}

// serverName is the name argocd-server's certificate must be valid for.
func serverName(namespace, name string) string {
	if v := strings.TrimSpace(os.Getenv(ServerNameEnv)); v != "" {
		return v
	}
	return name + "." + namespace + ".svc"
}

// argoDiff asks argocd-server for the object's diff. Every failure is a
// sentence, never the route's failure: the rest of the tab stands without it.
func argoDiff(r *http.Request, h extension.Host, cluster string, app *ApplicationDetail, t target, object unstructured.Unstructured) ArgoDiff {
	out := ArgoDiff{Fields: []drift.Field{}, Masked: object.GetKind() == "Secret" && t.gvr.Group == ""}
	ns, name, err := tokenSecret()
	if err != nil {
		out.Note = "Argo CD was not asked: " + err.Error() + "."
		return out
	}
	out.Secret = ns + "/" + name
	// Read as the console, through the door that reads only what this
	// console's configuration names (Configured), and records the read.
	token, err := h.ConfiguredSecret(r, cluster, ns, name, TokenKey)
	switch {
	case errors.Is(err, extension.ErrNoSuchSecret):
		out.Setup = true
		out.Note = "Argo CD's own diff is off on this cluster: no Secret " + out.Secret + " holds a read-only Argo CD token."
		return out
	case errors.Is(err, extension.ErrNoSuchKey):
		out.Note = "The Secret " + out.Secret + " has no " + TokenKey + " key, so Argo CD was not asked."
		return out
	case err != nil:
		out.Note = "The Secret " + out.Secret + " that holds Argo CD's token could not be read with the console's credentials: " + textutil.FirstLine(err.Error())
		return out
	}
	token = strings.TrimSpace(token)
	if token == "" {
		out.Note = "The Secret " + out.Secret + " has no " + TokenKey + " key, so Argo CD was not asked."
		return out
	}
	// The CA is optional: absent, argocd-server is checked against the
	// system's roots.
	pem, err := h.ConfiguredSecret(r, cluster, ns, name, CAKey)
	if err != nil && !errors.Is(err, extension.ErrNoSuchKey) {
		out.Note = "The Secret " + out.Secret + " that holds Argo CD's token could not be read with the console's credentials: " + textutil.FirstLine(err.Error())
		return out
	}

	svcNamespace, svcName, err := serverService()
	if err != nil {
		out.Note = "Argo CD was not asked: " + err.Error() + "."
		return out
	}
	svc, err := h.ConfiguredService(r, cluster, svcNamespace, svcName)
	if errors.Is(err, extension.ErrNoSuchService) {
		out.Note = "argocd-server's Service " + svcNamespace + "/" + svcName + " was not found, so Argo CD could not be asked for its diff. " + ServerServiceEnv + " names it."
		return out
	}
	if err != nil {
		out.Note = "argocd-server's Service " + svcNamespace + "/" + svcName + " could not be read with the console's credentials, so Argo CD was not asked (the console's role must allow get on it): " + textutil.FirstLine(err.Error())
		return out
	}
	port := 0
	for _, p := range svc.Spec.Ports {
		if port == 0 || p.Name == "https" || p.Port == 443 {
			port = int(p.Port)
		}
	}
	if port == 0 {
		out.Note = "argocd-server's Service " + svcNamespace + "/" + svcName + " has no port, so Argo CD could not be asked."
		return out
	}
	tlsConfig, err := serverTLS(serverName(svcNamespace, svcName), []byte(pem))
	if err != nil {
		out.Note = "The " + CAKey + " key of the Secret " + out.Secret + " holds no certificate argocd-server could be checked against, so Argo CD was not asked."
		return out
	}
	tunnel, err := h.ForwardAsConsole(r, cluster, svcNamespace, svcName, port)
	if err != nil {
		out.Note = "A tunnel to argocd-server could not be opened by the console: " + textutil.FirstLine(err.Error())
		return out
	}
	defer tunnel.Stop()

	query := url.Values{
		"appNamespace": {app.Namespace},
		"group":        {t.gvr.Group},
		"kind":         {t.kind},
		"namespace":    {object.GetNamespace()},
		"name":         {object.GetName()},
	}
	path := "/api/v1/applications/" + url.PathEscape(app.Name) + "/managed-resources?" + query.Encode()
	body, status, err := askArgo(r.Context(), tunnel.Addr, path, token, tlsConfig)
	var unverified *tls.CertificateVerificationError
	switch {
	case errors.As(err, &unverified):
		out.Note = "argocd-server's certificate could not be verified as " + tlsConfig.ServerName + ", so the token was not sent: " + textutil.FirstLine(unverified.Err.Error()) + ". Put the CA that signed it, or the certificate itself, under the key " + CAKey + " of the Secret " + out.Secret + ", and set " + ServerNameEnv + " when the certificate names argocd-server otherwise."
		return out
	case errors.Is(err, errPlainHTTP):
		out.Note = "argocd-server did not answer over TLS, and the token is never sent over plain HTTP: run argocd-server without --insecure to show Argo CD's diff here."
		return out
	case err != nil:
		out.Note = "argocd-server could not be asked: " + textutil.FirstLine(err.Error())
		return out
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		out.Note = fmt.Sprintf("argocd-server refused the token in %s (%d): it has expired, or its account may not get this Application.", out.Secret, status)
		return out
	case status == http.StatusNotFound:
		out.Note = "argocd-server does not know the Application " + app.Namespace + "/" + app.Name + "."
		return out
	case status != http.StatusOK:
		out.Note = fmt.Sprintf("argocd-server answered %d.", status)
		return out
	}

	var doc struct {
		Items []struct {
			Group               string `json:"group"`
			Kind                string `json:"kind"`
			Namespace           string `json:"namespace"`
			Name                string `json:"name"`
			TargetState         string `json:"targetState"`
			NormalizedLiveState string `json:"normalizedLiveState"`
			PredictedLiveState  string `json:"predictedLiveState"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		out.Note = "argocd-server's answer could not be read."
		return out
	}
	for _, item := range doc.Items {
		if item.Kind != t.kind || item.Name != object.GetName() || item.Namespace != object.GetNamespace() || item.Group != t.gvr.Group {
			continue
		}
		predicted, live := decodeState(item.PredictedLiveState), decodeState(item.NormalizedLiveState)
		switch {
		case item.TargetState == "" || item.TargetState == "null":
			out.Note = "Git no longer has this object: Argo CD would delete it on a sync with pruning."
			out.NotInGit = true
			return out
		case predicted == nil:
			out.Note = "Argo CD could not predict this object after a sync, so there is no diff to draw."
			return out
		}
		out.Compared = true
		out.Fields = drift.Compare(predicted, live, drift.Options{Mask: out.Masked})
		return out
	}
	out.Note = "argocd-server did not list this object among the Application's resources."
	return out
}

// serverTLS is how argocd-server's certificate is checked: it must be valid
// for serverName and chain to the PEM the token's Secret holds under ca.crt,
// or to the system's roots when that key is absent. The tunnel lands on a pod,
// so the address dialled is not the name checked.
func serverTLS(serverName string, pem []byte) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	if len(strings.TrimSpace(string(pem))) == 0 {
		return config, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("no certificate in " + CAKey)
	}
	config.RootCAs = pool
	return config, nil
}

// askArgo GETs a path from argocd-server through the tunnel, over TLS checked
// as serverTLS says. There is no plain HTTP: the request carries the token, and
// an answer that is not TLS, or a certificate that does not verify, ends the
// call before the token is written.
func askArgo(ctx context.Context, addr, path, token string, config *tls.Config) ([]byte, int, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
		// No Proxy: the tunnel is local, and a proxy would see the token. The
		// handshake is dialTLS's, so what refused it survives to the caller.
		// No keep-alive: the tunnel is torn down after this one request, and a
		// pooled connection would outlive it.
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialTLS(ctx, network, addr, config)
			},
		},
		// A redirect could lead the token elsewhere.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return body, resp.StatusCode, err
}

// errPlainHTTP is a handshake answered in plain HTTP: argocd-server run with
// --insecure.
var errPlainHTTP = errors.New("argocd-server answered in plain HTTP")

// dialTLS dials and completes the handshake itself, so that an answer in plain
// HTTP reaches the caller as errPlainHTTP; net/http's own handshake would
// replace the tls.RecordHeaderError with a sentence.
func dialTLS(ctx context.Context, network, addr string, config *tls.Config) (net.Conn, error) {
	var d net.Dialer
	raw, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	conn := tls.Client(raw, config)
	if err := conn.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		var header tls.RecordHeaderError
		if errors.As(err, &header) && bytes.HasPrefix(header.RecordHeader[:], []byte("HTTP/")) {
			return nil, fmt.Errorf("%w: %w", errPlainHTTP, err)
		}
		return nil, err
	}
	return conn, nil
}

func decodeState(raw string) map[string]any {
	if raw == "" || raw == "null" {
		return nil
	}
	var out map[string]any
	if json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return out
}

// Configured is what this page reads with the console's credentials on
// cluster, from the configuration alone: the token's Secret (TokenSecretEnv,
// or TokenSecretName in the console's own namespace) and argocd-server's
// Service (ServerServiceEnv, or ServerServiceDefault). A value that cannot be
// parsed names nothing, and the page says why on its own.
func (Delivery) Configured(string, func(string) string) extension.Configuration {
	var out extension.Configuration
	if ns, name, err := tokenSecret(); err == nil {
		out.Secrets = append(out.Secrets, extension.ObjectName{Namespace: ns, Name: name})
	}
	if ns, name, err := serverService(); err == nil {
		out.Services = append(out.Services, extension.ObjectName{Namespace: ns, Name: name})
	}
	return out
}

var _ extension.Configured = Delivery{}
