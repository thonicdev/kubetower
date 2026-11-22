package forward

// The three endpoints, exercised the way the UI and curl will call them.
//
// These are the shapes internal/web will register against, so a change that
// renames a field or moves a status code breaks here rather than in a browser.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func post(t *testing.T, h http.Handler, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/kt-api/forward", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestOpenHandlerTakesAFormAndAnswersTheTunnel(t *testing.T) {
	m := testManager(t, &fakeDigger{})

	w := post(t, m.OpenHandler(), url.Values{
		"cluster":   {"docker-desktop"},
		"namespace": {"kube-system"},
		"service":   {"kube-dns"},
		"port":      {"9153"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("open answered %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("content type is %q", got)
	}

	var tunnel Tunnel
	if err := json.Unmarshal(w.Body.Bytes(), &tunnel); err != nil {
		t.Fatalf("decoding the answer: %v (%s)", err, w.Body.String())
	}
	if tunnel.ID == "" || tunnel.LocalAddr == "" || tunnel.Pod == "" {
		t.Errorf("the answer is missing what the operator needs: %+v", tunnel)
	}
	if !strings.HasPrefix(tunnel.LocalAddr, "127.0.0.1:") {
		t.Errorf("the tunnel is on %q; a forward must bind the loopback", tunnel.LocalAddr)
	}
}

func TestOpenHandlerTakesJSONToo(t *testing.T) {
	m := testManager(t, &fakeDigger{})

	body := `{"cluster":"docker-desktop","namespace":"kube-system","pod":"coredns-1","port":9153}`
	r := httptest.NewRequest(http.MethodPost, "/kt-api/forward", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	m.OpenHandler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("open answered %d: %s", w.Code, w.Body.String())
	}
	var tunnel Tunnel
	if err := json.Unmarshal(w.Body.Bytes(), &tunnel); err != nil {
		t.Fatal(err)
	}
	if tunnel.Kind != "pod" || tunnel.Name != "coredns-1" {
		t.Errorf("the tunnel describes itself as %s/%s", tunnel.Kind, tunnel.Name)
	}
}

func TestOpenHandlerRefusesAnythingButPost(t *testing.T) {
	m := testManager(t, &fakeDigger{})
	r := httptest.NewRequest(http.MethodGet, "/kt-api/forward", nil)
	w := httptest.NewRecorder()
	m.OpenHandler().ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("a GET to the open endpoint answered %d", w.Code)
	}
}

func TestOpenHandlerSaysWhoseFaultItIs(t *testing.T) {
	m := testManager(t, &fakeDigger{})

	cases := []struct {
		name string
		form url.Values
		want int
	}{
		{"nothing at all", url.Values{}, http.StatusBadRequest},
		{"a port that is not a number", url.Values{
			"cluster": {"docker-desktop"}, "namespace": {"kube-system"},
			"service": {"kube-dns"}, "port": {"nine thousand"},
		}, http.StatusBadRequest},
		{"a cluster that is not in the kubeconfig", url.Values{
			"cluster": {"nowhere"}, "namespace": {"kube-system"},
			"service": {"kube-dns"}, "port": {"9153"},
		}, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if w := post(t, m.OpenHandler(), tc.form); w.Code != tc.want {
				t.Errorf("answered %d, wanted %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestOpenHandlerAnswers409AtTheLimit(t *testing.T) {
	m := testManager(t, &fakeDigger{})
	m.limit = 1

	form := url.Values{
		"cluster": {"docker-desktop"}, "namespace": {"kube-system"},
		"service": {"kube-dns"}, "port": {"9153"},
	}
	if w := post(t, m.OpenHandler(), form); w.Code != http.StatusOK {
		t.Fatalf("the first open answered %d", w.Code)
	}
	form.Set("port", "53")
	w := post(t, m.OpenHandler(), form)
	if w.Code != http.StatusConflict {
		t.Errorf("past the limit the endpoint answered %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "1 port-forwards") {
		t.Errorf("the refusal does not name the limit: %s", w.Body.String())
	}
}

func TestListHandlerReportsTheSetAndTheLimit(t *testing.T) {
	m := testManager(t, &fakeDigger{})
	if _, err := m.Open(context.Background(), dnsRequest()); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/kt-api/forward", nil)
	w := httptest.NewRecorder()
	m.ListHandler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("list answered %d", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("the list is cacheable (%q); a stale one shows forwards that are gone", got)
	}
	var view listView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Count != 1 || len(view.Tunnels) != 1 {
		t.Errorf("the list reports %d tunnels and carries %d", view.Count, len(view.Tunnels))
	}
	if view.Limit != maxOpen {
		t.Errorf("the list reports a limit of %d", view.Limit)
	}
	if !view.Tunnels[0].Healthy || view.Tunnels[0].Age == "" {
		t.Errorf("the row is missing what a table draws: %+v", view.Tunnels[0])
	}
}

func TestCloseHandlerTakesTheIdFromTheFormOrThePath(t *testing.T) {
	m := testManager(t, &fakeDigger{})

	first, err := m.Open(context.Background(), dnsRequest())
	if err != nil {
		t.Fatal(err)
	}
	w := post(t, m.CloseHandler(), url.Values{"id": {first.ID}})
	if w.Code != http.StatusNoContent {
		t.Fatalf("closing by form answered %d: %s", w.Code, w.Body.String())
	}
	if got := len(m.List()); got != 0 {
		t.Errorf("%d tunnels remain", got)
	}

	// The other mount: DELETE /kt-api/forward/{id}. The router owns that choice,
	// so both have to work.
	second, err := m.Open(context.Background(), dnsRequest())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("DELETE /kt-api/forward/{id}", m.CloseHandler())
	r := httptest.NewRequest(http.MethodDelete, "/kt-api/forward/"+second.ID, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("closing by path answered %d: %s", rec.Code, rec.Body.String())
	}
	if got := len(m.List()); got != 0 {
		t.Errorf("%d tunnels remain after the path close", got)
	}
}

func TestCloseHandlerOnSomethingThatIsNotThere(t *testing.T) {
	m := testManager(t, &fakeDigger{})

	if w := post(t, m.CloseHandler(), url.Values{}); w.Code != http.StatusBadRequest {
		t.Errorf("closing with no id answered %d", w.Code)
	}
	if w := post(t, m.CloseHandler(), url.Values{"id": {"nope"}}); w.Code != http.StatusNotFound {
		t.Errorf("closing an unknown id answered %d", w.Code)
	}
}

// A manager built without a cluster manager cannot forward, and must say so
// rather than panic. web.Options treats every dependency as optional for the
// same reason: a console that will not start is worse than one that admits what
// it cannot do.
func TestAManagerWithNoClustersRefusesPolitely(t *testing.T) {
	m := New(Options{})
	w := post(t, m.OpenHandler(), url.Values{
		"cluster": {"docker-desktop"}, "namespace": {"kube-system"},
		"service": {"kube-dns"}, "port": {"9153"},
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("answered %d: %s", w.Code, w.Body.String())
	}
}

// Both encodings carry the two fields the scheme is decided from, or the guess
// is made on nothing: a form-shaped click from the drawer and a JSON one from
// curl must reach the same answer for the same port.
func TestOpenHandlerCarriesThePortNameAndAppProtocol(t *testing.T) {
	m := testManager(t, &fakeDigger{})

	w := post(t, m.OpenHandler(), url.Values{
		"cluster":   {"docker-desktop"},
		"namespace": {"ingress"},
		"service":   {"controller"},
		"port":      {"8080"},
		"portName":  {"https-web"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("open answered %d: %s", w.Code, w.Body.String())
	}
	var fromForm Tunnel
	if err := json.Unmarshal(w.Body.Bytes(), &fromForm); err != nil {
		t.Fatal(err)
	}
	if fromForm.Scheme != schemeHTTPS {
		t.Errorf("a port named https-web came back as %q; the form field did not arrive", fromForm.Scheme)
	}

	// And the declaration beating the number, over JSON. 443 with
	// `appProtocol: http` is the case where believing the number is wrong.
	body := `{"cluster":"docker-desktop","namespace":"odd","service":"odd","port":443,"appProtocol":"http"}`
	r := httptest.NewRequest(http.MethodPost, "/kt-api/forward", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	m.OpenHandler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("open answered %d: %s", w.Code, w.Body.String())
	}
	var fromJSON Tunnel
	if err := json.Unmarshal(w.Body.Bytes(), &fromJSON); err != nil {
		t.Fatal(err)
	}
	if fromJSON.Scheme != schemeHTTP {
		t.Errorf("appProtocol http on 443 came back as %q; the JSON field did not arrive", fromJSON.Scheme)
	}
}

// The list endpoint carries the scheme too. It is the only field on a Tunnel
// whose absence is invisible — a row would simply build the wrong URL — so a
// silently dropped json tag is worth a test of its own.
func TestListHandlerReportsTheScheme(t *testing.T) {
	m := testManager(t, &fakeDigger{})
	if _, err := m.Open(context.Background(), Request{
		Cluster: "docker-desktop", Namespace: "ingress", Service: "controller",
		Port: 443, PortName: "https",
	}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/kt-api/forward", nil)
	w := httptest.NewRecorder()
	m.ListHandler().ServeHTTP(w, r)

	var view struct {
		Tunnels []Tunnel `json:"tunnels"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Tunnels) != 1 {
		t.Fatalf("the list holds %d tunnels", len(view.Tunnels))
	}
	if view.Tunnels[0].Scheme != schemeHTTPS {
		t.Errorf("the listed tunnel says scheme %q", view.Tunnels[0].Scheme)
	}
	if !strings.Contains(w.Body.String(), `"scheme"`) {
		t.Errorf("the JSON has no scheme field:\n%s", w.Body.String())
	}
}
