package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// rawCluster is a cluster whose raw REST client reaches `answer`.
func rawCluster(t *testing.T, answer http.HandlerFunc) *cluster.Client {
	t.Helper()
	server := httptest.NewServer(answer)
	t.Cleanup(server.Close)
	cfg := &rest.Config{Host: server.URL}
	clients, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("a client for the test server: %v", err)
	}
	return &cluster.Client{Context: testCluster, Config: cfg, Clients: clients}
}

// A 403 on metrics.k8s.io, sent the way the API server sends it - a JSON
// Status - keeps the API server's sentence and its status.
//
// DoRaw's error for a JSON answer has the literal message "unknown": through
// it, the discovery's detail would read "unknown" where the API server said who
// may not list what. The status is
// asserted too, because classify reads it: a 403 is not total, and the
// discovery goes on to look at metrics-server.
func TestARawRefusalKeepsTheAPIServersWords(t *testing.T) {
	client := rawCluster(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403,` +
			`"message":"metrics.k8s.io is forbidden: User \"jane\" cannot get path \"/apis/metrics.k8s.io/v1beta1\""}`))
	})

	_, err := restRaw(context.Background(), client, metricsAPIPath, nil)
	if err == nil {
		t.Fatal("a 403 read as an answer")
	}
	if !strings.Contains(err.Error(), `User "jane" cannot get path`) {
		t.Errorf("the error says %q; want the API server's own sentence, not client-go's \"unknown\"", err)
	}
	if !apierrors.IsForbidden(err) {
		t.Errorf("%v is not readable as a 403, and classify reads the status", err)
	}
}

// And the body still comes back with the error, which is what promAnswer
// passes on: Prometheus answers a bad query with 400 and an envelope naming
// the label it did not recognize, and that envelope is worth more to the
// caller than any error wrapped around it.
func TestARawPrometheusRejectionStillReachesPromAnswer(t *testing.T) {
	const envelope = `{"status":"error","errorType":"bad_data","error":"parse error: unexpected identifier"}`
	client := rawCluster(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(envelope))
	})

	body, err := promAnswer(restRaw(context.Background(), client, "/api/v1/namespaces/monitoring/services/prometheus:9090/proxy/api/v1/query", nil))
	if err != nil {
		t.Fatalf("promAnswer: %v; a Prometheus envelope is passed through, not turned into an error", err)
	}
	if string(body) != envelope {
		t.Errorf("body = %q, want Prometheus's envelope verbatim", body)
	}
}

// The services listed, no Prometheus matched, and metrics.k8s.io itself
// refused or failed. The discovery is a could-not-ask, and the error a
// session is refused with carries the status it was classified from, so
// internal/web's stateOf reads a 401 as a lapsed credential and a 503 as a
// cluster worth asking again - not both as one unclassified sentence.
func TestAMetricsServerFailureCarriesTheStatusItWasClassifiedFrom(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name string
		err  error
		is   func(error) bool
	}{
		{"a 401", apierrors.NewUnauthorized("Unauthorized"), apierrors.IsUnauthorized},
		{"a 503", apierrors.NewServiceUnavailable("the server is currently unable to handle the request"),
			apierrors.IsServiceUnavailable},
		{"a 504", apierrors.NewTimeoutError("the server was unable to return a response in the time allotted", 0),
			apierrors.IsTimeout},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.answer(rawAnswers{metricsAPIPath: {err: c.err}})
			found := h.metrics.Discover(ctx, testCluster, true)
			if found == nil || !found.CouldNotAsk() {
				t.Fatalf("discovery = %+v; metrics.k8s.io failing is a could-not-ask, not an absence", found)
			}
			_, err := h.metrics.OpenSession(ctx, testCluster)
			if err == nil || !strings.Contains(err.Error(), "could not be asked") {
				t.Fatalf("the session opened or said %v", err)
			}
			if !c.is(err) {
				t.Errorf("%v does not carry the status it was classified from", err)
			}
		})
	}
}
