package overview

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

// A raw read past the cap is refused with a sentence, not held whole; one
// within it reads as itself.
func TestARawReadPastTheCapIsRefusedWithASentence(t *testing.T) {
	defer func(was int64) { maxRawRead = was }(maxRawRead)
	maxRawRead = 1024
	answer := `{"items":[]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()
	c, err := rest.UnversionedRESTClientFor(&rest.Config{
		Host:          srv.URL,
		ContentConfig: rest.ContentConfig{NegotiatedSerializer: scheme.Codecs.WithoutConversion()},
	})
	if err != nil {
		t.Fatalf("a REST client: %v", err)
	}

	raw, err := getRaw(context.Background(), c, "/apis/metrics.k8s.io/v1beta1/pods")
	if err != nil || string(raw) != answer {
		t.Fatalf("a small answer gave %q and %v; want it whole", raw, err)
	}
	answer = `{"items":[],"pad":"` + strings.Repeat("x", 2048) + `"}`
	raw, err = getRaw(context.Background(), c, "/apis/metrics.k8s.io/v1beta1/pods")
	if err == nil || raw != nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("an answer past the cap gave %d bytes and %v; want nothing and the sentence", len(raw), err)
	}
}
