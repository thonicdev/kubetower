package edit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
)

// What the write door hands the audit record: the reason the
// cluster gave rather than `error`, a dry run marked as one, and a write whose
// record cannot be written refused in its own words before it is sent.

type recordSink struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	broken bool
}

func (s *recordSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken {
		return 0, errors.New("write /dev/stdout: broken pipe")
	}
	return s.buf.Write(p)
}

func (s *recordSink) records(t *testing.T) []audit.Record {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []audit.Record
	scanner := bufio.NewScanner(bytes.NewReader(s.buf.Bytes()))
	for scanner.Scan() {
		var r audit.Record
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			t.Fatalf("not a record: %v\n%s", err, scanner.Text())
		}
		out = append(out, r)
	}
	return out
}

// recordedWriter is a Writer over an API server that answers a dry-run patch
// with the object and a real one with a 409, writing its records to sink.
func recordedWriter(t *testing.T, sink io.Writer) (*Writer, Target, *[]string, *[]string) {
	t.Helper()
	return recordedWriterLogging(t, sink, false)
}

// recordedWriterLogging is recordedWriter whose gate, when gateLogs, writes its
// own process-log lines (the `unrecorded:` one) to the same log as the door's.
func recordedWriterLogging(t *testing.T, sink io.Writer, gateLogs bool) (*Writer, Target, *[]string, *[]string) {
	t.Helper()
	var mu sync.Mutex
	reached := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		*reached = append(*reached, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("dryRun") == "All" {
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"settings","namespace":"shop","resourceVersion":"8"}}`)
			return
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Conflict","code":409,"message":"the object has been modified"}`)
	}))
	t.Cleanup(srv.Close)
	cfg := &rest.Config{Host: srv.URL}
	access.Guard(cfg, "prod", nil)
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	logged := &[]string{}
	logf := func(format string, args ...any) { *logged = append(*logged, fmt.Sprintf(format, args...)) }
	options := access.Options{Reviews: (&accesstest.Reviews{}).For, Audit: audit.New(sink)}
	if gateLogs {
		options.Logf = logf
	}
	w := New(Options{
		Resolve: fakeResolver{dyn: dyn, cluster: "prod"},
		Access:  access.New(options),
		Logf:    logf,
	})
	return w, Target{Cluster: "prod", Kind: "configmaps", Namespace: "shop", Name: "settings"}, reached, logged
}

// A patch the cluster refuses with a 409 is recorded with the cluster's
// reason, `Conflict` - not `error`, which is what a refused patch would read as
// if the panel's refusal did not unwrap to the API server's error - and the
// human line names the record.
func TestARefusedPatchIsRecordedWithTheClustersReason(t *testing.T) {
	sink := &recordSink{}
	w, target, _, logged := recordedWriter(t, sink)
	where, err := w.Locate(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = Send(context.Background(), where, target, []byte(`{"data":{"a":"b"}}`), false)
	if refusal, ok := AsRefusal(err); !ok || refusal.Kind != RefusedConflict {
		t.Fatalf("a 409 answered %v, want RefusedConflict", err)
	}
	got := sink.records(t)
	if len(got) != 2 || got[1].Outcome != audit.Failed || got[1].Reason != "Conflict" {
		t.Fatalf("a 409 was recorded as %+v, want failed/Conflict", got)
	}
	if len(*logged) != 1 || !strings.Contains((*logged)[0], "reason=conflict audit="+got[0].ID) {
		t.Errorf("the human line does not name the record: %q", *logged)
	}
}

// A preview is recorded as a dry run, so `applied` on it never reads as a
// change.
func TestADryRunIsRecordedAsOne(t *testing.T) {
	sink := &recordSink{}
	w, target, _, _ := recordedWriter(t, sink)
	where, _ := w.Locate(context.Background(), target)
	if _, _, err := Send(context.Background(), where, target, []byte(`{"data":{"a":"b"}}`), true); err != nil {
		t.Fatal(err)
	}
	got := sink.records(t)
	if len(got) != 2 || !got[0].DryRun || !got[1].DryRun || got[1].Outcome != audit.Applied {
		t.Fatalf("a dry run was recorded as %+v", got)
	}
}

// A write whose record cannot be written is refused in its own words, as
// RefusedUnrecorded, and nothing reaches the cluster.
func TestAWriteWhoseRecordFailsIsRefusedUnrecorded(t *testing.T) {
	sink := &recordSink{broken: true}
	w, target, reached, logged := recordedWriter(t, sink)
	where, _ := w.Locate(context.Background(), target)
	_, _, err := Send(context.Background(), where, target, []byte(`{"data":{"a":"b"}}`), false)
	refusal, ok := AsRefusal(err)
	if !ok || refusal.Kind != RefusedUnrecorded || !errors.Is(err, access.ErrUnrecorded) {
		t.Fatalf("an unrecorded write answered %v, want RefusedUnrecorded", err)
	}
	if len(*reached) != 0 {
		t.Errorf("the cluster saw %v", *reached)
	}
	if len(*logged) != 1 || !strings.Contains((*logged)[0], "outcome=refused reason=unrecorded") {
		t.Errorf("the human line reads %q", *logged)
	}
}

// A forced delete - a grace period of zero - is recorded as
// one, on its attempt and on its outcome; an ordinary delete is not.
func TestAForcedDeleteIsRecordedAsOne(t *testing.T) {
	for _, forced := range []bool{true, false} {
		sink := &recordSink{}
		w, target, _, _ := recordedWriter(t, sink)
		where, err := w.Locate(context.Background(), target)
		if err != nil {
			t.Fatal(err)
		}
		options := metav1.DeleteOptions{}
		if forced {
			zero := int64(0)
			options.GracePeriodSeconds = &zero
		}
		// The server answers 409: the act was still attempted, and both
		// records are written.
		_, _ = Delete(context.Background(), where, target, options)
		got := sink.records(t)
		if len(got) != 2 {
			t.Fatalf("forced=%t: %d records, want the attempt and the outcome: %+v", forced, len(got), got)
		}
		for _, r := range got {
			if r.Verb != "delete" || r.Force != forced {
				t.Errorf("forced=%t: recorded as %+v", forced, r)
			}
		}
	}
}

// A forced delete refused because its attempt record could not be written says
// force on both lines about it - the door's `write:` line and the gate's
// `unrecorded:` one - and an ordinary delete on neither.
func TestAnUnrecordedForcedDeleteSaysForceOnEveryLine(t *testing.T) {
	for _, forced := range []bool{true, false} {
		sink := &recordSink{broken: true}
		w, target, reached, logged := recordedWriterLogging(t, sink, true)
		where, err := w.Locate(context.Background(), target)
		if err != nil {
			t.Fatal(err)
		}
		options := metav1.DeleteOptions{}
		if forced {
			zero := int64(0)
			options.GracePeriodSeconds = &zero
		}
		_, err = Delete(context.Background(), where, target, options)
		if refusal, ok := AsRefusal(err); !ok || refusal.Kind != RefusedUnrecorded {
			t.Fatalf("forced=%t: an unrecorded delete answered %v, want RefusedUnrecorded", forced, err)
		}
		if len(*reached) != 0 {
			t.Errorf("forced=%t: the cluster saw %v", forced, *reached)
		}
		var door, gate int
		for _, line := range *logged {
			switch {
			case strings.HasPrefix(line, "write: ") && strings.Contains(line, "reason=unrecorded"):
				door++
			case strings.HasPrefix(line, "unrecorded: "):
				gate++
			default:
				continue
			}
			if said := strings.Contains(line, " force=true"); said != forced {
				t.Errorf("forced=%t: the line says force=%t: %s", forced, said, line)
			}
		}
		if door != 1 || gate != 1 {
			t.Errorf("forced=%t: want one door line and one unrecorded line, got %q", forced, *logged)
		}
	}
}
