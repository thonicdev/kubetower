package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/logs"
)

// A follow opened on a container still being created, as
// the panel receives it: a `pending` frame naming the reason, then the
// container's lines on the same stream, then the ordinary end. It is not framed
// as `unreachable` and an `end`, after which the panel would rightly never ask
// again.
//
// The subject is the framing. internal/logs has the wait's own tests; what only
// this package decides is that the notice is `pending` and not a failure, since
// the panel draws a failure as the reason the stream stopped and a `pending`
// frame as a notice the first line replaces.
func TestAFollowWaitingForItsContainerIsFramedPendingAndThenCarriesItsLines(t *testing.T) {
	var looks atomic.Int32
	running := func() bool { return looks.Load() >= 2 }
	apiserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pods/web/log"):
			if !running() {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(metav1.Status{
					TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
					Status:   metav1.StatusFailure, Reason: metav1.StatusReasonBadRequest, Code: 400,
					Message: `container "app" in pod "web" is waiting to start: ContainerCreating`,
				})
				return
			}
			_, _ = fmt.Fprintln(w, "listening on :8080")
		case strings.HasSuffix(r.URL.Path, "/pods/web"):
			looks.Add(1)
			state := corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}
			if running() {
				state = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(corev1.Pod{
				TypeMeta:   metav1.TypeMeta{Kind: "Pod", APIVersion: "v1"},
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
				Spec:       corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{Name: "app"}}},
				Status: corev1.PodStatus{Phase: corev1.PodPending,
					ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: state}}},
			})
		default:
			http.Error(w, "not this test's endpoint: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(apiserver.Close)
	server := &Server{logs: logs.New(fakeLogClients{t: t, host: apiserver.URL}, accesstest.Allow(), nil, nil)}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var got []api.LogEvent
	if f := (apiDock{server}).StreamLogs(ctx,
		api.LogQuery{
			ObjectRef: api.ObjectRef{Cluster: "prod", Namespace: "default", Name: "web"},
			Container: "app", Follow: true, TailLines: 200,
		},
		func(event api.LogEvent) error { got = append(got, event); return nil }); f != nil {
		t.Fatalf("streaming: %+v", f)
	}

	if len(got) != 3 {
		t.Fatalf("%d events, want a notice, a line and an end: %+v", len(got), got)
	}
	notice := got[0]
	if notice.Type != "state" || notice.State != api.StatePending || notice.Reason == nil ||
		notice.Reason.Code != api.CodeContainerNotStarted {
		t.Fatalf("the first event is %+v, want a pending state carrying %q", notice, api.CodeContainerNotStarted)
	}
	if notice.Reason.Message != "waiting for the container to start: ContainerCreating" {
		t.Errorf("the notice says %q", notice.Reason.Message)
	}
	if got[1].Type != "line" || got[1].Text != "listening on :8080" || got[1].Container != "app" {
		t.Errorf("the second event is %+v, want the container's line", got[1])
	}
	if got[2].Type != "end" {
		t.Errorf("the last event is %+v, want an end", got[2])
	}
}
