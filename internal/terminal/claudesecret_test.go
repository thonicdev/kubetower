package terminal

// The assistant is not opened on a Secret.
//
// The assistant must not read Secrets, and hiding its control on a Secret is
// the cosmetic half. `api/websocket.md` is explicit about which half is the
// contract: *"the capability flag is the front end's hint; the refusal is the
// contract. Every channel checks its own condition on the upgrade."* A control
// that is not drawn is still an address anybody who can reach the socket can
// type, so the refusal below is the part that holds.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/thonicdev/kubetower/internal/cluster"
)

func assistantServer(t *testing.T, contexts ...string) string {
	t.Helper()
	// The loopback rewrite must not fire: these addresses are deliberately dead
	// and have to stay dead, so a guard that stopped refusing would hang here
	// rather than reach a cluster. See internal/cluster/loopback.go.
	t.Setenv("KT_IN_CONTAINER", "0")
	kubeconfig := execKubeconfig(t, contexts...)

	term, err := New(managerFor(t, kubeconfig), Options{Kubeconfig: cluster.Kubeconfig{kubeconfig}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(term.FramedAssistantHandler())
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func dialAssistant(t *testing.T, target string) serverFrame {
	t.Helper()
	client, _, err := websocket.DefaultDialer.Dial(target, http.Header{})
	if err != nil {
		t.Fatalf("the handshake failed, so the refusal could not be read: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return readFrame(t, client)
}

// The refusal itself, on every spelling of the kind that can address one.
//
// **The plural is in the list because the console has two names for a kind.**
// The drawer sends `DetailPage.title` — "Secret", as a person writes it — and
// the route carries the discovery plural, `secrets`. `validKind` accepts both,
// so a gate that knew only the first would be stepped around by typing the
// second, which is the spelling already in the address bar.
func TestAnAssistantSessionIsRefusedOnASecret(t *testing.T) {
	url := assistantServer(t, "lab")

	for _, kind := range []string{"Secret", "secret", "secrets", "SECRET", "Secrets"} {
		t.Run(kind, func(t *testing.T) {
			frame := dialAssistant(t, url+
				"/kt-api/assistant?cluster=lab&kind="+kind+"&namespace=argo&name=registry-pull")

			if frame.Type != "state" || frame.State != stateForbidden {
				t.Fatalf("the refusal arrived as %+v, expected a forbidden state frame", frame)
			}
			if frame.Reason == nil || frame.Reason.Code != codeVerbDenied {
				t.Fatalf("the refusal is not this console declining: %+v", frame.Reason)
			}
			// A bare 404 tells the operator their button is broken. The sentence
			// is what tells them it is a rule, so it is asserted rather than left
			// to whoever edits the string next.
			for _, want := range []string{"Secret", "read"} {
				if !strings.Contains(frame.Reason.Message, want) {
					t.Errorf("the refusal does not say why (%q missing): %q", want, frame.Reason.Message)
				}
			}
		})
	}
}

// The other half, and the half that fails silently: a guard that refuses
// everything passes the test above. A Pod must still reach the session.
//
// What a Pod gets here depends on the machine — a session where Claude Code is
// on the PATH, a `not_installed` refusal where it is not — and that is exactly
// why the assertion is on the refusal *not* being this one rather than on what
// it is. Either outcome proves the request traveled past the subject check into
// the ordinary path, and the test says the same thing on a build machine with no
// Claude binary as on a laptop with one.
func TestAnAssistantSessionOnAPodIsNotRefusedAsASecret(t *testing.T) {
	url := assistantServer(t, "lab")
	frame := dialAssistant(t, url+
		"/kt-api/assistant?cluster=lab&kind=Pod&namespace=argo&name=api-0")

	if frame.Reason != nil && frame.Reason.Code == codeVerbDenied {
		t.Fatalf("a Pod was refused by the Secret guard: %+v", frame.Reason)
	}
	if frame.Reason != nil && strings.Contains(frame.Reason.Message, "Secret") {
		t.Fatalf("a Pod was told about Secrets: %q", frame.Reason.Message)
	}
}

// And a ConfigMap is not a Secret. The boundary is the operator's word — they
// said Secrets — and this repository already states the distinction in the
// contract: *"A ConfigMap is not a credential store and its value is resolved on
// the panel open and drawn"* (`api/openapi.yaml`, the Data member). A guard that
// quietly widened to every key/value object would take the assistant off a kind
// the console deliberately shows in full.
func TestAConfigMapIsNotTreatedAsASecret(t *testing.T) {
	url := assistantServer(t, "lab")
	frame := dialAssistant(t, url+
		"/kt-api/assistant?cluster=lab&kind=ConfigMap&namespace=argo&name=app-config")

	if frame.Reason != nil && frame.Reason.Code == codeVerbDenied {
		t.Fatalf("a ConfigMap was refused by the Secret guard: %+v", frame.Reason)
	}
}
