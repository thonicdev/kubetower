package argocd_test

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The diff reaches argocd-server through the console's own tunnel
// (Host.ForwardAsConsole; the test host's Forward refuses), so what a person
// is shown of it is held to their own reviewed reads: the object, and the
// Application that lists it. These pin that the tunnel is opened only after
// both stood, and that nothing but that one object's item leaves the answer.

// A person who may not read Applications gets the drift from the object
// alone, and the console opens no tunnel and sends no token for them.
func TestNoTunnelForAPersonWhoMayNotReadTheApplication(t *testing.T) {
	cert, ca := selfSigned(t, argoService)
	seen := &recorder{}
	host := diffHost(t, addr(argoServer(t, cert, seen)), "s3cret", ca)
	host.listErr["applications"] = forbidden("argoproj.io", "applications")

	got, status, body := askTracked(t, host, guestbookUI)
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s", status, body)
	}
	if got.ArgoDiff != nil || len(host.tunnels) != 0 || seen.count() != 0 {
		t.Errorf("diff %+v, tunnels %v, argocd-server asked %d times: want no diff and no tunnel", got.ArgoDiff, host.tunnels, seen.count())
	}
}

// An Application the person may not read - their list holds others, not the
// one the object names - is absent, and so is its diff.
func TestNoTunnelForAnApplicationOutsideThePersonsRights(t *testing.T) {
	cert, ca := selfSigned(t, argoService)
	seen := &recorder{}
	host := diffHost(t, addr(argoServer(t, cert, seen)), "s3cret", ca)
	other := managing("Synced")
	other.SetName("payments")
	host.pages["applications"] = []unstructured.Unstructured{other}

	got, status, body := askTracked(t, host, guestbookUI)
	if status != http.StatusOK {
		t.Fatalf("status %d, body %s", status, body)
	}
	if got.ArgoDiff != nil || got.Application != nil || len(host.tunnels) != 0 || seen.count() != 0 {
		t.Errorf("application %+v, diff %+v, tunnels %v, argocd-server asked %d times: want neither, and no tunnel",
			got.Application, got.ArgoDiff, host.tunnels, seen.count())
	}
}

// An object the person may not read is refused, before any tunnel.
func TestNoTunnelForAnObjectThePersonMayNotRead(t *testing.T) {
	cert, ca := selfSigned(t, argoService)
	seen := &recorder{}
	host := diffHost(t, addr(argoServer(t, cert, seen)), "s3cret", ca)
	host.listErr["deployments"] = forbidden("apps", "deployments")

	_, status, _ := askTracked(t, host, guestbookUI)
	if status != http.StatusForbidden {
		t.Errorf("status %d, want 403", status)
	}
	if len(host.tunnels) != 0 || seen.count() != 0 {
		t.Errorf("tunnels %v, argocd-server asked %d times: want none", host.tunnels, seen.count())
	}
}

// argocd-server answers about every resource of the Application it is asked
// for. The console's tunnel reads all of it, so only the item naming the
// object the person read is kept: another namespace's Secret, served first,
// never reaches the answer.
func TestOnlyThePersonsObjectLeavesArgoCDsAnswer(t *testing.T) {
	cert, ca := selfSigned(t, argoService)
	seen := &recorder{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.saw(r.Header.Get("Authorization"))
		state := func(obj map[string]any) string {
			raw, _ := json.Marshal(obj)
			return string(raw)
		}
		secret := state(map[string]any{"apiVersion": "v1", "kind": "Secret", "stringData": map[string]any{"password": "not-for-grace"}})
		live := func(replicas int) string {
			return state(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "spec": map[string]any{"replicas": replicas}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
			{"group": "", "kind": "Secret", "namespace": "payments", "name": "db",
				"targetState": secret, "predictedLiveState": secret, "normalizedLiveState": secret},
			{"group": "apps", "kind": "Deployment", "namespace": "payments", "name": "guestbook-ui",
				"targetState": live(9), "predictedLiveState": live(9), "normalizedLiveState": live(1)},
			{"group": "apps", "kind": "Deployment", "namespace": "guestbook", "name": "guestbook-ui",
				"targetState": live(5), "predictedLiveState": live(5), "normalizedLiveState": live(3)},
		}})
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.StartTLS()
	t.Cleanup(server.Close)
	host := diffHost(t, addr(server), "s3cret", ca)

	got, status, body := askTracked(t, host, guestbookUI)
	if status != http.StatusOK || got.ArgoDiff == nil {
		t.Fatalf("status %d, body %s", status, body)
	}
	if !slices.Equal(host.tunnels, []string{"argocd/argocd-server"}) {
		t.Errorf("tunnels %v, want the console's one to argocd-server", host.tunnels)
	}
	d := got.ArgoDiff
	if !d.Compared || len(d.Fields) != 1 || d.Fields[0].Path != "spec.replicas" || d.Fields[0].Applied != "5" || d.Fields[0].Live != "3" {
		t.Errorf("diff %+v, want guestbook/guestbook-ui's spec.replicas: git 5, live 3", d)
	}
	for _, leaked := range []string{"not-for-grace", "payments", `"9"`} {
		if strings.Contains(string(body), leaked) {
			t.Errorf("another resource of the Application reached the answer (%q):\n%s", leaked, body)
		}
	}
}
