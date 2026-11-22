package web

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/audit"
)

// ExecStream is Exec's door with the output streamed: the same review, the
// same record naming the argv, the same count, and a bound of its own.

func streamer(t *testing.T, s *Server) extension.ExecStreamer {
	t.Helper()
	st, ok := s.hostFor(execExt).(extension.ExecStreamer)
	if !ok {
		t.Fatal("the Host handed to an extension does not stream")
	}
	return st
}

func catQuery() extension.ExecQuery {
	return extension.ExecQuery{Namespace: "shop", Pod: "api-1", Container: "app",
		Command: []string{"cat", "/tmp/kubetower-heap.hprof"}}
}

func TestAStreamThePersonMayNotMakeIsRefusedUnsentAndRecordedWithItsCommand(t *testing.T) {
	sink := &revealSink{}
	s, fake := hostExecServer(t, func(accesstest.Question) (bool, error) { return false, nil }, sink)
	var out bytes.Buffer
	_, err := streamer(t, s).ExecStream(asGrace(), "prod", catQuery(), &out, 1<<20)
	if !errors.Is(err, extension.ErrDenied) {
		t.Fatalf("a denied stream answered %v, want ErrDenied", err)
	}
	if askedToExec(fake) || out.Len() != 0 {
		t.Errorf("a denied stream reached the cluster (%v) or wrote %d bytes", fake.asked(), out.Len())
	}
	got := sink.records(t)
	if len(got) != 2 || got[1].Outcome != audit.Refused {
		t.Fatalf("a denied stream wrote %+v, want a refused attempt and outcome", got)
	}
	for _, r := range got {
		if r.Act != audit.Exec || r.Subresource != "exec" || r.Name != "api-1" || r.Extension != execExt ||
			r.Command != `"cat" "/tmp/kubetower-heap.hprof"` || r.Decision != string(access.Denied) {
			t.Errorf("the %s record reads %+v", r.Phase, r)
		}
	}
}

// A stream allowed by the review reaches the cluster's exec, recorded.
func TestAStreamThePersonMayMakeReachesTheClustersExec(t *testing.T) {
	sink := &revealSink{}
	s, fake := hostExecServer(t, func(accesstest.Question) (bool, error) { return true, nil }, sink)
	_, _ = streamer(t, s).ExecStream(asGrace(), "prod", catQuery(), &bytes.Buffer{}, 1<<20)
	if !askedToExec(fake) {
		t.Errorf("an allowed stream never reached the cluster: %v", fake.asked())
	}
	if got := sink.records(t); len(got) == 0 || got[0].Phase != audit.Attempt || got[0].Command != `"cat" "/tmp/kubetower-heap.hprof"` {
		t.Errorf("an allowed stream wrote %+v", got)
	}
}

// What cannot be bounded is refused before anything is counted or asked.
func TestAStreamOutsideItsBoundsIsRefusedUnasked(t *testing.T) {
	for name, tc := range map[string]struct {
		q     extension.ExecQuery
		limit int64
		nilW  bool
	}{
		"no limit":   {catQuery(), 0, false},
		"past a GiB": {catQuery(), extension.MaxExecStreamBytes + 1, false},
		"a deadline too long": {func() extension.ExecQuery {
			q := catQuery()
			q.Timeout = extension.MaxExecStreamTimeout + time.Second
			return q
		}(), 1 << 20, false},
		"nowhere to write": {catQuery(), 1 << 20, true},
		"no container":     {func() extension.ExecQuery { q := catQuery(); q.Container = ""; return q }(), 1 << 20, false},
	} {
		t.Run(name, func(t *testing.T) {
			asked := false
			s, fake := hostExecServer(t, func(accesstest.Question) (bool, error) { asked = true; return true, nil }, &revealSink{})
			var w *bytes.Buffer
			if !tc.nilW {
				w = &bytes.Buffer{}
			}
			var err error
			if w == nil {
				_, err = streamer(t, s).ExecStream(asGrace(), "prod", tc.q, nil, tc.limit)
			} else {
				_, err = streamer(t, s).ExecStream(asGrace(), "prod", tc.q, w, tc.limit)
			}
			if err == nil || asked || askedToExec(fake) {
				t.Errorf("answered %v, reviewed %v, sent %v", err, asked, askedToExec(fake))
			}
		})
	}
}

// A deadline Exec would refuse is a stream's to ask for, up to its own.
func TestAStreamMayAskForALongerDeadlineThanExec(t *testing.T) {
	q := catQuery()
	q.Timeout = extension.MaxExecTimeout + time.Minute
	if _, err := execQueryProblems(q, extension.MaxExecTimeout); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("Exec accepted %s: %v", q.Timeout, err)
	}
	if got, err := execQueryProblems(q, extension.MaxExecStreamTimeout); err != nil || got != q.Timeout {
		t.Errorf("a stream refused %s: %v", q.Timeout, err)
	}
}
