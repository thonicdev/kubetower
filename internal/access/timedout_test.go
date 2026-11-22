package access_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
)

// The record reads the transport, not only the closure's error: a write the
// API server timed out on is `timed-out` even when what the closure hands
// back is the answer to client-go's retry - here a 409 AlreadyExists for the
// create the first, timed-out attempt may well have made - and no caller has
// classified it. internal/edit classifies its own writes
// too; the terminal's and the port-forward's closures do not.
func TestAWriteTheAPIServerTimedOutOnIsRecordedTimedOutWhateverCameAfter(t *testing.T) {
	var arrived atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		if arrived.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Timeout","code":504,`+
				`"message":"Timeout: request did not complete within requested timeout"}`)
			return
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"AlreadyExists",`+
			`"code":409,"message":"configmaps \"settings\" already exists"}`)
	}))
	defer srv.Close()
	cfg := &rest.Config{Host: srv.URL}
	access.Guard(cfg, "prod", nil)
	clients := clientsetFor(t, cfg)
	s := &sink{}
	g := recordingGate(&accesstest.Reviews{}, s, &processLog{})
	create := access.Attributes{Verb: "create", Version: "v1", Resource: "configmaps", Namespace: "shop"}

	_, err := g.Perform(alice(), "prod", create, func(ctx context.Context) error {
		_, err := clients.CoreV1().ConfigMaps("shop").Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "settings", Namespace: "shop"}}, metav1.CreateOptions{})
		return err
	})
	if arrived.Load() != 2 || !apierrors.IsAlreadyExists(err) {
		t.Fatalf("the API server received %d creates and the closure answered %v, want the 504's retry answered 409",
			arrived.Load(), err)
	}
	got := s.records(t)
	if len(got) != 2 || got[1].Outcome != audit.Failed || got[1].Reason != "timed-out" {
		t.Errorf("the record reads %+v, want failed/timed-out", got)
	}
}
