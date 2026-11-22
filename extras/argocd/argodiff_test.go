package argocd_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/extension"
	argocd "github.com/thonicdev/kubetower/extras/argocd"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// Argo CD's own diff, through argocd-server with a read-only token,
// sent only to the Service the configuration
// names, over verified TLS.

// argoService is the name Argo CD's own self-signed certificate carries for
// its Service in the namespace argocd.
const argoService = "argocd-server.argocd.svc"

// selfSigned is a certificate for the given names, signed by itself, as Argo
// CD generates one when nobody gave it another; certPEM is what an operator
// copies under ca.crt.
func selfSigned(t *testing.T, names ...string) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{Organization: []string{"Argo CD"}},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// recorder is every Authorization header a server received.
type recorder struct {
	mu   sync.Mutex
	auth []string
}

func (r *recorder) saw(h string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.auth = append(r.auth, h)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.auth)
}

// argoHandler answers managed-resources for guestbook as argocd-server would.
func argoHandler(seen *recorder, status int, predictedReplicas int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.saw(r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/v1/applications/guestbook/managed-resources") || r.URL.Query().Get("appNamespace") != "argocd" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		state := func(replicas int) string {
			if replicas < 0 {
				return "null" // git no longer has it
			}
			raw, _ := json.Marshal(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "spec": map[string]any{"replicas": replicas}})
			return string(raw)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{
			"group": "apps", "kind": "Deployment", "namespace": "guestbook", "name": "guestbook-ui",
			"targetState": state(predictedReplicas), "predictedLiveState": state(max(predictedReplicas, 0)), "normalizedLiveState": state(3),
		}}})
	})
}

// argoServer is argocd-server speaking TLS with the given certificate, git
// asking for five replicas.
func argoServer(t *testing.T, cert tls.Certificate, seen *recorder) *httptest.Server {
	t.Helper()
	return argoServerPredicting(t, cert, seen, 5)
}

// argoServerPredicting is argoServer with git's replicas given; -1 is an
// object git no longer has.
func argoServerPredicting(t *testing.T, cert tls.Certificate, seen *recorder, predictedReplicas int) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(argoHandler(seen, http.StatusOK, predictedReplicas))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func addr(server *httptest.Server) string {
	return server.Listener.Addr().String()
}

// service is an argocd-server Service, labeled as both installs label it.
func service(namespace, name string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: map[string]string{"app.kubernetes.io/name": "argocd-server"}},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "https", Port: 443}}},
	}
}

// diffHost holds the token (and ca.crt, when given) in the console's Secret,
// argocd-server's Service where both installs put it, and a tunnel to server
// for it when server is not empty.
func diffHost(t *testing.T, server string, token string, ca []byte) *argoHost {
	t.Helper()
	t.Setenv(argocd.TokenSecretEnv, "kubetower/kubetower-argocd-token")
	host := trackedHost()
	host.pages["deployments"] = []unstructured.Unstructured{deployment(3, guestbookMark)}
	objects := []runtime.Object{service("argocd", "argocd-server")}
	if token != "" {
		data := map[string][]byte{"token": []byte(token)}
		if ca != nil {
			data[argocd.CAKey] = ca
		}
		objects = append(objects, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "kubetower", Name: "kubetower-argocd-token"}, Data: data})
	}
	host.kube = objects
	host.forward = map[string]string{}
	if server != "" {
		host.forward["argocd/argocd-server"] = server
	}
	return host
}

const guestbookUI = "kind=deployments&namespace=guestbook&name=guestbook-ui"

func TestArgoCDsOwnDiffIsReadWithTheToken(t *testing.T) {
	cert, ca := selfSigned(t, "localhost", "argocd-server", argoService)
	seen := &recorder{}
	server := argoServer(t, cert, seen)
	got, status, body := askTracked(t, diffHost(t, addr(server), "s3cret", ca), guestbookUI)
	if status != http.StatusOK || got.ArgoDiff == nil {
		t.Fatalf("status %d, body %s", status, body)
	}
	d := got.ArgoDiff
	if !d.Compared || len(d.Fields) != 1 || d.Fields[0].Path != "spec.replicas" || d.Fields[0].Applied != "5" || d.Fields[0].Live != "3" {
		t.Errorf("diff %+v, want spec.replicas: git 5, live 3", d)
	}
	if strings.Contains(string(body), "s3cret") {
		t.Error("the token left the server in the answer")
	}
}

// An object git no longer has is an answer, marked as one, and never a failed
// comparison.
func TestAnObjectGitNoLongerHasIsAnAnswer(t *testing.T) {
	cert, ca := selfSigned(t, argoService)
	server := argoServerPredicting(t, cert, &recorder{}, -1)
	got, status, body := askTracked(t, diffHost(t, addr(server), "s3cret", ca), guestbookUI)
	if status != http.StatusOK || got.ArgoDiff == nil {
		t.Fatalf("status %d, body %s", status, body)
	}
	if d := got.ArgoDiff; d.Compared || !d.NotInGit || !strings.Contains(d.Note, "Git no longer has this object") {
		t.Errorf("diff %+v, want not compared, notInGit, and the sentence", d)
	}
}

func TestWithoutTheSecretTheDiffSaysHowToTurnItOn(t *testing.T) {
	got, _, _ := askTracked(t, diffHost(t, "", "", nil), guestbookUI)
	if got.ArgoDiff == nil || !got.ArgoDiff.Setup || got.ArgoDiff.Secret != "kubetower/kubetower-argocd-token" || got.ArgoDiff.Compared {
		t.Errorf("diff %+v, want setup asked for, the Secret named", got.ArgoDiff)
	}
	if len(got.Drift.Fields) != 1 {
		t.Errorf("drift %+v, want the last-applied drift all the same", got.Drift)
	}
}

func TestARefusedTokenAndARefusedTunnelAreSentences(t *testing.T) {
	cert, ca := selfSigned(t, argoService)
	server := argoServer(t, cert, &recorder{})
	got, _, _ := askTracked(t, diffHost(t, addr(server), "wrong", ca), guestbookUI)
	if got.ArgoDiff == nil || got.ArgoDiff.Compared || !strings.Contains(got.ArgoDiff.Note, "refused the token") {
		t.Errorf("diff %+v, want the refusal said", got.ArgoDiff)
	}
	got, _, _ = askTracked(t, diffHost(t, "", "s3cret", ca), guestbookUI)
	if got.ArgoDiff == nil || !strings.Contains(got.ArgoDiff.Note, "tunnel to argocd-server could not be opened") {
		t.Errorf("diff %+v, want the tunnel refusal said", got.ArgoDiff)
	}
}

// A Service labeled as argocd-server's, in a namespace that sorts before Argo
// CD's, never receives the token: the console tunnels to the Service its
// configuration names and lists none by label.
func TestADecoyLabelledServiceNeverGetsTheToken(t *testing.T) {
	cert, ca := selfSigned(t, argoService)
	real, decoy := &recorder{}, &recorder{}
	realServer := argoServer(t, cert, real)
	// The decoy even holds a certificate that verifies, so only where the
	// tunnel goes keeps the token from it.
	decoyServer := argoServer(t, cert, decoy)

	host := diffHost(t, addr(realServer), "s3cret", ca)
	host.kube = append(host.kube, service("aaa-tenant", "argocd-server"))
	host.forward["aaa-tenant/argocd-server"] = addr(decoyServer)

	got, _, _ := askTracked(t, host, guestbookUI)
	if got.ArgoDiff == nil || !got.ArgoDiff.Compared {
		t.Errorf("diff %+v, want Argo CD's diff from the configured Service", got.ArgoDiff)
	}
	if decoy.count() != 0 || slices.Contains(host.tunnels, "aaa-tenant/argocd-server") {
		t.Errorf("tunnels %v, decoy saw %d requests: the token went to the decoy", host.tunnels, decoy.count())
	}
	if real.count() != 1 {
		t.Errorf("argocd-server saw %d requests, want 1", real.count())
	}

	// Without the configured Service, the decoy is still not taken for it.
	host = diffHost(t, addr(realServer), "s3cret", ca)
	host.kube = []runtime.Object{host.kube[1], service("aaa-tenant", "argocd-server")}
	host.forward = map[string]string{"aaa-tenant/argocd-server": addr(decoyServer)}
	got, _, _ = askTracked(t, host, guestbookUI)
	if got.ArgoDiff == nil || got.ArgoDiff.Compared || !strings.Contains(got.ArgoDiff.Note, "argocd/argocd-server was not found") {
		t.Errorf("diff %+v, want the configured Service said missing", got.ArgoDiff)
	}
	if decoy.count() != 0 || len(host.tunnels) != 0 {
		t.Errorf("tunnels %v, decoy saw %d requests, want none", host.tunnels, decoy.count())
	}
}

// KT_ARGOCD_SERVER_SERVICE moves where the token goes, and its certificate is
// then checked against that Service's name.
func TestTheConfigurationNamesArgoCDsService(t *testing.T) {
	cert, ca := selfSigned(t, "argo.gitops.svc")
	seen := &recorder{}
	server := argoServer(t, cert, seen)
	host := diffHost(t, "", "s3cret", ca)
	host.kube[0] = service("gitops", "argo")
	host.forward["gitops/argo"] = addr(server)
	t.Setenv(argocd.ServerServiceEnv, "gitops/argo")

	got, _, _ := askTracked(t, host, guestbookUI)
	if got.ArgoDiff == nil || !got.ArgoDiff.Compared || !slices.Equal(host.tunnels, []string{"gitops/argo"}) {
		t.Errorf("diff %+v, tunnels %v, want the diff through gitops/argo alone", got.ArgoDiff, host.tunnels)
	}

	t.Setenv(argocd.ServerServiceEnv, "argo")
	host.tunnels = nil
	got, _, _ = askTracked(t, host, guestbookUI)
	if got.ArgoDiff == nil || got.ArgoDiff.Compared || !strings.Contains(got.ArgoDiff.Note, "not namespace/name") || len(host.tunnels) != 0 {
		t.Errorf("diff %+v, tunnels %v, want a malformed value said, and no tunnel", got.ArgoDiff, host.tunnels)
	}
}

// A certificate that does not verify ends the call before the token is
// written, with a sentence the page shows; it is not retried over plain HTTP.
func TestAnUnverifiableCertificateIsRefusedWithASentence(t *testing.T) {
	cert, _ := selfSigned(t, argoService)
	_, otherCA := selfSigned(t, argoService)
	wrongName, wrongNameCA := selfSigned(t, "argocd-server.elsewhere.svc")

	for _, c := range []struct {
		name string
		cert tls.Certificate
		ca   []byte
	}{
		{"no ca.crt, and not a public CA", cert, nil},
		{"ca.crt is another certificate", cert, otherCA},
		{"the certificate names another Service", wrongName, wrongNameCA},
	} {
		t.Run(c.name, func(t *testing.T) {
			seen := &recorder{}
			server := argoServer(t, c.cert, seen)
			got, _, body := askTracked(t, diffHost(t, addr(server), "s3cret", c.ca), guestbookUI)
			if got.ArgoDiff == nil || got.ArgoDiff.Compared || !strings.Contains(got.ArgoDiff.Note, "could not be verified as "+argoService+", so the token was not sent") || !strings.Contains(got.ArgoDiff.Note, "ca.crt") {
				t.Errorf("diff %+v, want the refusal said", got.ArgoDiff)
			}
			if seen.count() != 0 {
				t.Errorf("argocd-server saw %d requests, want none", seen.count())
			}
			if strings.Contains(string(body), "s3cret") {
				t.Error("the token left the server in the answer")
			}
		})
	}
}

func TestACaCrtWithNoCertificateIsASentence(t *testing.T) {
	cert, _ := selfSigned(t, argoService)
	host := diffHost(t, addr(argoServer(t, cert, &recorder{})), "s3cret", []byte("not a certificate"))
	got, _, _ := askTracked(t, host, guestbookUI)
	if got.ArgoDiff == nil || got.ArgoDiff.Compared || !strings.Contains(got.ArgoDiff.Note, "holds no certificate") || len(host.tunnels) != 0 {
		t.Errorf("diff %+v, tunnels %v, want the bad ca.crt said before any tunnel", got.ArgoDiff, host.tunnels)
	}
}

// argocd-server run with --insecure speaks plain HTTP: the token is not sent
// to it, over http:// or otherwise, and the page says why.
func TestNoTokenLeavesOverPlainHTTP(t *testing.T) {
	seen := &recorder{}
	plain := httptest.NewServer(argoHandler(seen, http.StatusOK, 5))
	defer plain.Close()
	_, ca := selfSigned(t, argoService)

	got, _, _ := askTracked(t, diffHost(t, addr(plain), "s3cret", ca), guestbookUI)
	if seen.count() != 0 {
		t.Errorf("a plain HTTP argocd-server received %v", seen.auth)
	}
	if got.ArgoDiff == nil || got.ArgoDiff.Compared || !strings.Contains(got.ArgoDiff.Note, "never sent over plain HTTP") {
		t.Errorf("diff %+v, want the refusal said", got.ArgoDiff)
	}
}

// A redirect is not followed: it could lead the token elsewhere.
func TestARedirectIsNotFollowedWithTheToken(t *testing.T) {
	cert, ca := selfSigned(t, argoService)
	elsewhere := &recorder{}
	target := argoServer(t, cert, elsewhere)
	redirect := httptest.NewUnstartedServer(http.RedirectHandler("https://"+addr(target)+"/api/v1/applications/guestbook/managed-resources?appNamespace=argocd", http.StatusFound))
	redirect.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	redirect.StartTLS()
	defer redirect.Close()

	got, _, _ := askTracked(t, diffHost(t, addr(redirect), "s3cret", ca), guestbookUI)
	if elsewhere.count() != 0 {
		t.Errorf("the redirect's target received %v", elsewhere.auth)
	}
	if got.ArgoDiff == nil || got.ArgoDiff.Compared || !strings.Contains(got.ArgoDiff.Note, "answered 302") {
		t.Errorf("diff %+v, want the redirect's status said", got.ArgoDiff)
	}
}

// A Helm release not named argocd names its Service otherwise, while Argo CD's
// self-signed certificate still names argocd-server.<namespace>.svc:
// KT_ARGOCD_SERVER_NAME is what is verified then, and without it the
// certificate is refused before the token is written.
func TestTheServerNameVerifiedIsConfigurable(t *testing.T) {
	cert, ca := selfSigned(t, "localhost", "argocd-server", "argocd-server.argocd", argoService)
	seen := &recorder{}
	server := argoServer(t, cert, seen)
	helm := func() *argoHost {
		host := diffHost(t, "", "s3cret", ca)
		host.kube[0] = service("argocd", "argo-cd-argocd-server")
		host.forward["argocd/argo-cd-argocd-server"] = addr(server)
		return host
	}
	t.Setenv(argocd.ServerServiceEnv, "argocd/argo-cd-argocd-server")

	got, _, _ := askTracked(t, helm(), guestbookUI)
	if got.ArgoDiff == nil || got.ArgoDiff.Compared || !strings.Contains(got.ArgoDiff.Note, "could not be verified as argo-cd-argocd-server.argocd.svc") || !strings.Contains(got.ArgoDiff.Note, argocd.ServerNameEnv) {
		t.Errorf("diff %+v, want the certificate refused for the Service's own name, and %s named", got.ArgoDiff, argocd.ServerNameEnv)
	}
	if seen.count() != 0 {
		t.Errorf("argocd-server saw %d requests before its name was configured, want none", seen.count())
	}

	t.Setenv(argocd.ServerNameEnv, argoService)
	host := helm()
	got, _, _ = askTracked(t, host, guestbookUI)
	if got.ArgoDiff == nil || !got.ArgoDiff.Compared || !slices.Equal(host.tunnels, []string{"argocd/argo-cd-argocd-server"}) {
		t.Errorf("diff %+v, tunnels %v, want the diff through argo-cd-argocd-server, verified as %s", got.ArgoDiff, host.tunnels, argoService)
	}
	if seen.count() != 1 {
		t.Errorf("argocd-server saw %d requests, want 1", seen.count())
	}
}

// A Service the console may not read is not "not found": the sentence carries
// the refusal and points at the console's role, not at the variable.
func TestAServiceTheConsoleMayNotReadIsNotNotFound(t *testing.T) {
	cert, ca := selfSigned(t, argoService)
	seen := &recorder{}
	host := diffHost(t, addr(argoServer(t, cert, seen)), "s3cret", ca)
	host.react = func(c *fake.Clientset) {
		c.PrependReactor("get", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "argocd-server", nil)
		})
	}
	got, _, _ := askTracked(t, host, guestbookUI)
	if got.ArgoDiff == nil || got.ArgoDiff.Compared || !strings.Contains(got.ArgoDiff.Note, "could not be read with the console's credentials") || !strings.Contains(got.ArgoDiff.Note, "forbidden") || strings.Contains(got.ArgoDiff.Note, "not found") {
		t.Errorf("diff %+v, want the refusal said, not a missing Service", got.ArgoDiff)
	}
	if seen.count() != 0 || len(host.tunnels) != 0 {
		t.Errorf("tunnels %v, argocd-server saw %d requests, want none", host.tunnels, seen.count())
	}
}

// A token Secret named by a malformed value is a sentence, never a silent
// fall back to the default Secret.
func TestAMalformedTokenSecretIsASentence(t *testing.T) {
	cert, ca := selfSigned(t, argoService)
	seen := &recorder{}
	host := diffHost(t, addr(argoServer(t, cert, seen)), "s3cret", ca)
	for _, v := range []string{"kubetower", "kubetower/a/b", "/x", "x/"} {
		t.Setenv(argocd.TokenSecretEnv, v)
		got, _, _ := askTracked(t, host, guestbookUI)
		if got.ArgoDiff == nil || got.ArgoDiff.Compared || !strings.Contains(got.ArgoDiff.Note, argocd.TokenSecretEnv) || !strings.Contains(got.ArgoDiff.Note, "not namespace/name") {
			t.Errorf("%q: diff %+v, want the malformed value said", v, got.ArgoDiff)
		}
	}
	if seen.count() != 0 || len(host.tunnels) != 0 {
		t.Errorf("tunnels %v, argocd-server saw %d requests, want none", host.tunnels, seen.count())
	}
}

// Something that is neither TLS nor HTTP is not taken for an argocd-server run
// with --insecure: the handshake's own error is said, and no token is sent.
func TestABannerThatIsNotHTTPIsNotCalledInsecure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("SSH-2.0-OpenSSH_9.6\r\n"))
			_ = conn.Close()
		}
	}()
	_, ca := selfSigned(t, argoService)
	got, _, _ := askTracked(t, diffHost(t, listener.Addr().String(), "s3cret", ca), guestbookUI)
	if got.ArgoDiff == nil || got.ArgoDiff.Compared || strings.Contains(got.ArgoDiff.Note, "--insecure") || !strings.Contains(got.ArgoDiff.Note, "argocd-server could not be asked") {
		t.Errorf("diff %+v, want the handshake's own failure, not --insecure", got.ArgoDiff)
	}
}

// The token is read through the door that reads only what the configuration
// names, and the Service it goes to likewise: Configured names the two, and
// the diff asks for nothing else.
func TestArgoCDsTokenIsReadOnlyWhereTheConfigurationNamesIt(t *testing.T) {
	cert, ca := selfSigned(t, argoService)
	host := diffHost(t, addr(argoServer(t, cert, &recorder{})), "s3cret", ca)
	got, _, body := askTracked(t, host, guestbookUI)
	if got.ArgoDiff == nil || !got.ArgoDiff.Compared {
		t.Fatalf("diff %+v", got.ArgoDiff)
	}
	want := []string{"kubetower/kubetower-argocd-token#token", "kubetower/kubetower-argocd-token#" + argocd.CAKey}
	if !slices.Equal(host.secretReads, want) {
		t.Errorf("secret reads %v, want %v", host.secretReads, want)
	}
	if strings.Contains(string(body), "s3cret") {
		t.Error("the token left the server in the answer")
	}
	cfg := argocd.New().Configured("a-cluster", func(string) string { return "" })
	if len(cfg.Secrets) != 1 || cfg.Secrets[0] != (extension.ObjectName{Namespace: "kubetower", Name: "kubetower-argocd-token"}) ||
		len(cfg.Services) != 1 || cfg.Services[0] != (extension.ObjectName{Namespace: "argocd", Name: "argocd-server"}) {
		t.Errorf("Configured names %+v", cfg)
	}
	// A value that is not namespace/name names nothing, so nothing is read.
	t.Setenv(argocd.TokenSecretEnv, "only-a-name")
	if cfg := argocd.New().Configured("a-cluster", func(string) string { return "" }); len(cfg.Secrets) != 0 {
		t.Errorf("a malformed %s names %+v", argocd.TokenSecretEnv, cfg.Secrets)
	}
}
