package apiraw

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

// client is a raw REST client for one test server answering `status` and
// `body` with `contentType`.
func client(t *testing.T, status int, contentType, body string) rest.Interface {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	c, err := rest.UnversionedRESTClientFor(&rest.Config{
		Host:          server.URL,
		ContentConfig: rest.ContentConfig{NegotiatedSerializer: scheme.Codecs.WithoutConversion()},
	})
	if err != nil {
		t.Fatalf("a REST client: %v", err)
	}
	return c
}

// What Do is for: a JSON Status refusal keeps the API server's sentence and
// its status, where DoRaw said "unknown".
func TestAStatusRefusalKeepsItsWordsAndItsStatus(t *testing.T) {
	c := client(t, http.StatusForbidden, "application/json",
		`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403,`+
			`"message":"pods.metrics.k8s.io is forbidden: User \"jane\" cannot list resource \"pods\""}`)
	_, err := Do(context.Background(), c.Get().AbsPath("/apis/metrics.k8s.io/v1beta1/pods"))
	if err == nil || !strings.Contains(err.Error(), `User "jane" cannot list`) {
		t.Errorf("err = %v; want the API server's own sentence", err)
	}
	if !apierrors.IsForbidden(err) {
		t.Errorf("%v is not readable as a 403", err)
	}
}

// And what it keeps of DoRaw: the body comes back with the error, and an
// answer comes back as itself.
func TestTheBodyComesBackWithAnErrorAndWithoutOne(t *testing.T) {
	const envelope = `{"status":"error","errorType":"bad_data","error":"parse error"}`
	body, err := Do(context.Background(), client(t, http.StatusBadRequest, "application/json", envelope).Get().AbsPath("/q"))
	if err == nil || string(body) != envelope {
		t.Errorf("a 400 gave body %q, err %v; want the envelope and an error", body, err)
	}
	body, err = Do(context.Background(), client(t, http.StatusOK, "application/json", `{"items":[]}`).Get().AbsPath("/q"))
	if err != nil || string(body) != `{"items":[]}` {
		t.Errorf("a 200 gave body %q, err %v", body, err)
	}
}

// DoCapped reads an answer at the cap whole, and refuses one byte more with
// the caller's sentence rather than a truncated body.
func TestDoCappedRefusesTheByteAfterTheCap(t *testing.T) {
	tooLarge := errors.New("the answer is too large to read")
	at := strings.Repeat("x", 1000)
	body, err := DoCapped(context.Background(), client(t, http.StatusOK, "application/json", at).Get().AbsPath("/q"), 1000, tooLarge)
	if err != nil || string(body) != at {
		t.Errorf("an answer at the cap gave %d bytes and %v; want it whole", len(body), err)
	}
	body, err = DoCapped(context.Background(), client(t, http.StatusOK, "application/json", at+"x").Get().AbsPath("/q"), 1000, tooLarge)
	if !errors.Is(err, tooLarge) || body != nil {
		t.Errorf("an answer one byte over the cap gave %d bytes and %v; want nothing and the sentence", len(body), err)
	}
}

// And it keeps what Do is for: a JSON Status refusal keeps its words.
func TestDoCappedKeepsARefusalsWords(t *testing.T) {
	c := client(t, http.StatusForbidden, "application/json",
		`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403,`+
			`"message":"nodes \"a\" is forbidden: User \"jane\" cannot get resource \"nodes/proxy\""}`)
	_, err := DoCapped(context.Background(), c.Get().AbsPath("/api/v1/nodes/a/proxy/stats/summary"), 1<<20, errors.New("too large"))
	if err == nil || !strings.Contains(err.Error(), `User "jane" cannot get`) || !apierrors.IsForbidden(err) {
		t.Errorf("err = %v; want the API server's own sentence, readable as a 403", err)
	}
}
