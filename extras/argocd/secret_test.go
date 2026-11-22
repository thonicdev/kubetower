package argocd_test

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/extension"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A Secret an Application manages is answered without reading it: the core's
// Host.List refuses Secrets, so the tab once answered 502 with the core's
// sentence. Only the keys that differ are said, never a value.

const dbSecret = "kind=secrets&namespace=guestbook&name=db"

// secretHost is guestbook managing the Secret guestbook/db, OutOfSync, with
// Host.List refusing Secrets as the core's does.
func secretHost(t *testing.T, server string, ca []byte) *argoHost {
	t.Helper()
	host := diffHost(t, server, "s3cret", ca)
	host.discovery["v1"] = append(host.discovery["v1"], metav1.APIResource{Name: "secrets", Kind: "Secret", Namespaced: true})
	host.listErr["secrets"] = extension.ErrSecretsNotListed
	app := managing("OutOfSync")
	resources, _, _ := unstructured.NestedSlice(app.Object, "status", "resources")
	resources = append(resources, map[string]any{"version": "v1", "kind": "Secret", "namespace": "guestbook", "name": "db", "status": "OutOfSync"})
	_ = unstructured.SetNestedSlice(app.Object, resources, "status", "resources")
	host.pages["applications"] = []unstructured.Unstructured{app}
	return host
}

// secretServer is argocd-server answering the Secret as Argo CD does, values
// masked with runs of '+' (one length per distinct value).
func secretServer(t *testing.T, cert tls.Certificate) *httptest.Server {
	t.Helper()
	state := func(data map[string]any) string {
		raw, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "Secret", "data": data})
		return string(raw)
	}
	target := state(map[string]any{"password": "++++++++", "user": "+++++++++", "tls.crt": "++++++++++"})
	live := state(map[string]any{"password": "+++++++++++", "user": "+++++++++", "tls.crt": "++++++++++++"})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer s3cret" || r.URL.Query().Get("kind") != "Secret" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{
			"group": "", "kind": "Secret", "namespace": "guestbook", "name": "db",
			"targetState": target, "predictedLiveState": target, "normalizedLiveState": live,
		}}})
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func TestASecretIsAnsweredWithTheKeysThatDifferAndNoValue(t *testing.T) {
	cert, ca := selfSigned(t, argoService)
	host := secretHost(t, addr(secretServer(t, cert)), ca)
	got, status, body := askTracked(t, host, dbSecret)
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s; a Secret must not answer the core's refusal", status, body)
	}
	for _, q := range host.queries {
		if q.GVR.Resource == "secrets" {
			t.Errorf("the Secret was listed: %+v", q)
		}
	}
	if !got.Secret || !got.Managed || got.Resource == nil || got.Resource.Status != "OutOfSync" || got.Application == nil || got.Application.Name != "guestbook" {
		t.Fatalf("answer %+v, want a managed Secret, OutOfSync, of guestbook", got)
	}
	d := got.ArgoDiff
	if d == nil || !d.Compared || !d.Masked {
		t.Fatalf("diff %+v, want compared and masked", d)
	}
	var paths []string
	for _, f := range d.Fields {
		if f.Applied != "" || f.Live != "" {
			t.Errorf("field %+v carries a value", f)
		}
		paths = append(paths, f.Path)
	}
	if strings.Join(paths, ",") != `data.password,data["tls.crt"]` {
		t.Errorf("keys %v, want password and tls.crt (user is the same)", paths)
	}
	if strings.Contains(string(body), "+++") || strings.Contains(string(body), "Host.List") {
		t.Errorf("the answer carries a masked value or the core's sentence: %s", body)
	}
}

func TestASecretNoApplicationListsIsNotManagedAndNot502(t *testing.T) {
	host := secretHost(t, "", nil)
	host.pages["applications"] = []unstructured.Unstructured{managing("Synced")}
	got, status, body := askTracked(t, host, dbSecret)
	if status != http.StatusOK || got.Managed || !got.Secret {
		t.Fatalf("status %d, answer %+v (%s), want 200, not managed", status, got, body)
	}
}

func TestASecretWithoutTheTokenStillSaysItsSyncWord(t *testing.T) {
	host := secretHost(t, "", nil)
	host.kube = nil // no token Secret: the diff is off
	got, status, body := askTracked(t, host, dbSecret)
	if status != http.StatusOK || got.Resource == nil || got.Resource.Status != "OutOfSync" {
		t.Fatalf("status %d, body %s", status, body)
	}
	if got.ArgoDiff == nil || got.ArgoDiff.Compared || !got.ArgoDiff.Setup {
		t.Errorf("diff %+v, want the setup answer", got.ArgoDiff)
	}
}

func TestApplicationsRefusedForASecretIsANoteNotAFailure(t *testing.T) {
	host := secretHost(t, "", nil)
	host.listErr["applications"] = extension.ErrDenied
	got, status, body := askTracked(t, host, dbSecret)
	if status != http.StatusOK || got.Managed || got.ApplicationCode != "list_forbidden" {
		t.Fatalf("status %d, answer %+v (%s)", status, got, body)
	}
}
