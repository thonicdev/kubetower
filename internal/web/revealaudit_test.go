package web

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/audit"
)

// A reveal is recorded like a write: the attempt
// naming the key before the value is read, the outcome after, and an attempt
// that cannot be written refuses the reveal before anything is read.

type revealSink struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	broken bool
}

func (s *revealSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken {
		return 0, errors.New("write /dev/stdout: broken pipe")
	}
	return s.buf.Write(p)
}

func (s *revealSink) records(t *testing.T) []audit.Record {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []audit.Record
	scanner := bufio.NewScanner(bytes.NewReader(s.buf.Bytes()))
	for scanner.Scan() {
		var r audit.Record
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			t.Fatalf("a line on the audit sink is not a record: %v\n%s", err, scanner.Text())
		}
		out = append(out, r)
	}
	return out
}

func revealServer(t *testing.T, allowSecrets bool, out io.Writer) (*Server, *surfaceServer) {
	t.Helper()
	manager, fake := surfaceManager(t)
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		return q.Attributes.Resource != "secrets" || allowSecrets, nil
	}}
	s, err := New(Options{Clusters: manager, InCluster: true,
		Access: access.New(access.Options{Reviews: reviews.For, InCluster: true, Audit: audit.New(out)})})
	if err != nil {
		t.Fatal(err)
	}
	return s, fake
}

var revealPassword = api.EnvQuery{
	ObjectRef: api.ObjectRef{Cluster: "prod", Kind: "secrets", Namespace: "shop", Name: "db"}, Key: "password",
}

func readTheSecret(fake *surfaceServer) bool {
	for _, path := range fake.asked() {
		if strings.Contains(path, "/secrets/db") {
			return true
		}
	}
	return false
}

func TestARevealIsRecordedWithItsKeyAndNeverItsValue(t *testing.T) {
	sink := &revealSink{}
	s, _ := revealServer(t, true, sink)
	value, fail := apiReads{s}.RevealEnv(grace(), revealPassword)
	if fail != nil || value.Value != "hunter2" {
		t.Fatalf("the reveal answered %+v %+v", value, fail)
	}
	got := sink.records(t)
	if len(got) != 2 || got[0].Phase != audit.Attempt || got[1].Phase != audit.Outcome || got[0].ID != got[1].ID {
		t.Fatalf("one reveal wrote %+v, want an attempt and its outcome", got)
	}
	r := got[1]
	if r.Act != audit.Reveal || r.Verb != "get" || r.Resource != "secrets" || r.Namespace != "shop" ||
		r.Name != "db" || r.Key != "password" || r.Outcome != audit.Applied || r.Who != "grace@example.com" {
		t.Errorf("the outcome record reads %+v", r)
	}
	if strings.Contains(sink.buf.String(), "hunter2") || strings.Contains(sink.buf.String(), "aHVudGVyMg") {
		t.Errorf("the value reached the audit record:\n%s", sink.buf.String())
	}
}

func TestARevealWhoseRecordCannotBeWrittenReadsNothing(t *testing.T) {
	sink := &revealSink{broken: true}
	s, fake := revealServer(t, true, sink)
	value, fail := apiReads{s}.RevealEnv(grace(), revealPassword)
	if fail == nil || value.Value != "" {
		t.Fatalf("an unrecorded reveal answered %+v %+v", value, fail)
	}
	if fail.State != api.StateUnreadable || !strings.Contains(fail.Reason.Message, "could not write the audit record") {
		t.Errorf("the refusal reads %+v", fail)
	}
	if readTheSecret(fake) {
		t.Errorf("the Secret was read although the reveal's record could not be written: %v", fake.asked())
	}
}

func TestARefusedRevealIsRecordedAsRefused(t *testing.T) {
	sink := &revealSink{}
	s, fake := revealServer(t, false, sink)
	_, fail := apiReads{s}.RevealEnv(grace(), revealPassword)
	if fail == nil || fail.State != api.StateForbidden {
		t.Fatalf("a denied reveal answered %+v", fail)
	}
	got := sink.records(t)
	if len(got) != 2 || got[0].Decision != "denied" || got[1].Outcome != audit.Refused || got[1].Key != "password" {
		t.Fatalf("a denied reveal wrote %+v, want an attempt and a refused outcome", got)
	}
	if readTheSecret(fake) {
		t.Errorf("the Secret was read although the reveal was denied")
	}
}
