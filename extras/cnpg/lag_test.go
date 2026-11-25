package cnpg

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/extension"
)

const lagPath = "/x/kind/cnpg/clusters/db/pg/lag"

func lagHost(res extension.ExecResult, err error) *fakeHost {
	h := sqlHost()
	h.execFn = func(extension.ExecQuery) (extension.ExecResult, error) { return res, err }
	return h
}

// The argv is the one fixed constant, run in the primary's postgres
// container with a deadline, and nothing of the request or the cluster is in it.
func TestLagRunsTheOneFixedArgvInThePrimary(t *testing.T) {
	h := lagHost(extension.ExecResult{Stdout: []byte("pg-2|streaming|async|0.1|0.2|0.3\n")}, nil)
	rec := serve(t, h, lagPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(h.execs) != 1 {
		t.Fatalf("%d execs", len(h.execs))
	}
	q := h.execs[0]
	want := []string{"psql", "-XAt", "-c",
		"SELECT application_name, state, sync_state, EXTRACT(EPOCH FROM write_lag), EXTRACT(EPOCH FROM flush_lag), " +
			"EXTRACT(EPOCH FROM replay_lag) FROM pg_stat_replication ORDER BY application_name"}
	if !reflect.DeepEqual(q.Command, want) {
		t.Errorf("argv = %q", q.Command)
	}
	if q.Namespace != "db" || q.Pod != "pg-1" || q.Container != "postgres" || q.Timeout != lagTimeout || q.Timeout > 30*time.Second {
		t.Errorf("query = %+v", q)
	}
	// Odd names reach no argv: the statement is read-only and constant.
	up := strings.ToUpper(q.Command[3])
	for _, bad := range []string{"INSERT", "UPDATE", "DELETE", "DROP", "ALTER", "CREATE", "COPY", "PG_TERMINATE", ";"} {
		if strings.Contains(up, bad) {
			t.Errorf("the statement holds %q", bad)
		}
	}
	// Neither a password nor the Secret is read for it.
	if len(h.revealed) != 0 || len(h.forwarded) != 0 {
		t.Errorf("revealed %v, forwarded %v", h.revealed, h.forwarded)
	}
	got := decode[Lag](t, rec)
	if got.Primary != "pg-1" || len(got.Replicas) != 1 || got.Replicas[0].Name != "pg-2" || *got.Replicas[0].Replay != 0.3 {
		t.Errorf("answer %+v", got)
	}
}

// Real psql -XAt output of the query, from a primary streaming to a standby,
// on PostgreSQL 16, 17 and 18.
func TestLagParsesPsqlOutputOfPostgres16And17And18(t *testing.T) {
	for _, tc := range []struct {
		version              string
		write, flush, replay float64
	}{
		{"16", 0.000614, 0.003360, 0.003495},
		{"17", 0.001341, 0.009888, 0.012835},
		{"18", 0.001254, 0.006526, 0.010715},
	} {
		out, err := os.ReadFile("testdata/pg_stat_replication-" + tc.version + ".txt")
		if err != nil {
			t.Fatal(err)
		}
		rows, err := parseLag(out)
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s: %v %+v", tc.version, err, rows)
		}
		r := rows[0]
		if r.Name != "pg-2" || r.State != "streaming" || r.Sync != "async" || *r.Write != tc.write || *r.Flush != tc.flush || *r.Replay != tc.replay {
			t.Errorf("%s: %+v", tc.version, r)
		}
	}
}

// A NULL lag is empty in psql's output and absent in the answer: never 0.
func TestLagNullIsAbsentNotZero(t *testing.T) {
	rows, err := parseLag([]byte("pg-2|streaming|sync|||\npg-3|catchup|async|1.5||2.25\n"))
	if err != nil || len(rows) != 2 {
		t.Fatal(err, rows)
	}
	if rows[0].Write != nil || rows[0].Flush != nil || rows[0].Replay != nil {
		t.Errorf("a NULL lag was drawn: %+v", rows[0])
	}
	if rows[1].Flush != nil || *rows[1].Write != 1.5 || *rows[1].Replay != 2.25 {
		t.Errorf("%+v", rows[1])
	}
	rows, err = parseLag(nil)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Errorf("empty output: %v %#v", err, rows)
	}
}

func TestLagRefusesOutputItCannotRead(t *testing.T) {
	for _, bad := range []string{"pg-2|streaming|async|0.1|0.2\n", "pg-2|streaming|async|x|0.2|0.3\n", "pg-2|a|b|NaN|0|0\n", "pg-2|a|b|-1|0|0\n", "|a|b|1|1|1\n", "a|b|c|d|e|f|g\n"} {
		if _, err := parseLag([]byte(bad)); err == nil {
			t.Errorf("%q was read", bad)
		}
	}
	rec := serve(t, lagHost(extension.ExecResult{Stdout: []byte("garbage\n")}, nil), lagPath)
	if p := decode[problem](t, rec); rec.Code != http.StatusBadGateway || p.Code != CodeLagUnreadable {
		t.Errorf("%d %+v", rec.Code, p)
	}
}

// Each way the exec gives no lag is its own answer, none drawn as 0 or as an
// empty list of standbys.
func TestLagEveryFailureHasItsOwnAnswer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		res    extension.ExecResult
		err    error
		status int
		code   string
	}{
		{"refused", extension.ExecResult{}, fmt.Errorf("exec: %w", extension.ErrDenied), http.StatusForbidden, CodeExecForbidden},
		{"read-only", extension.ExecResult{}, fmt.Errorf("%w: prod is marked read-only", extension.ErrReadOnly), http.StatusForbidden, CodeReadOnly},
		{"unreviewable", extension.ExecResult{}, fmt.Errorf("exec: %w", extension.ErrUnreviewable), http.StatusServiceUnavailable, CodeExecUnreviewed},
		{"no psql", extension.ExecResult{}, extension.ErrNoSuchCommand, http.StatusUnprocessableEntity, CodeNoPsql},
		{"timeout", extension.ExecResult{}, extension.ErrExecTimedOut, http.StatusGatewayTimeout, CodeExecTimedOut},
		{"busy", extension.ExecResult{}, extension.ErrExecBusy, http.StatusTooManyRequests, CodeExecBusy},
		{"unknown cluster", extension.ExecResult{}, fmt.Errorf("x: %w", extension.ErrUnknownCluster), http.StatusNotFound, CodeUnknownCluster},
		{"failed", extension.ExecResult{}, errors.New("stream closed"), http.StatusBadGateway, CodeExecFailed},
		{"truncated", extension.ExecResult{Stdout: []byte("pg-2|streaming|async|0.1|0.2|0.3\n"), Truncated: true}, nil, http.StatusBadGateway, CodeLagTruncated},
		{"psql error", extension.ExecResult{ExitCode: 2, Stderr: []byte("psql: error: connection to server on socket failed\nsecond line")}, nil, http.StatusBadGateway, CodeLagQueryFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(t, lagHost(tc.res, tc.err), lagPath)
			p := decode[problem](t, rec)
			if rec.Code != tc.status || p.Code != tc.code {
				t.Fatalf("%d %+v, want %d %s", rec.Code, p, tc.status, tc.code)
			}
			if strings.Contains(rec.Body.String(), "second line") {
				t.Errorf("more than psql's first line: %s", rec.Body)
			}
		})
	}
}

// The output read is bounded: a result past MaxLagOutput is not parsed.
func TestLagOutputIsBounded(t *testing.T) {
	big := bytes.Repeat([]byte("pg-2|streaming|async|0.1|0.2|0.3\n"), MaxLagOutput/30)
	rec := serve(t, lagHost(extension.ExecResult{Stdout: big}, nil), lagPath)
	if p := decode[problem](t, rec); p.Code != CodeLagTruncated {
		t.Fatalf("%d %+v", rec.Code, p)
	}
	// And no more than MaxReplicas standbys are kept from a legal output.
	rows, err := parseLag(bytes.Repeat([]byte("pg-2|streaming|async|0.1|0.2|0.3\n"), MaxReplicas+30))
	if err != nil || len(rows) != MaxReplicas {
		t.Errorf("%d rows, %v", len(rows), err)
	}
}

// The exec carries a deadline: a host that never answers is cut by it.
func TestLagGivesTheExecADeadline(t *testing.T) {
	h := lagHost(extension.ExecResult{Stdout: nil}, nil)
	rec := serve(t, h, lagPath)
	if h.execs[0].Timeout <= 0 || rec.Code != http.StatusOK {
		t.Fatalf("%d, timeout %s", rec.Code, h.execs[0].Timeout)
	}
	if got := decode[Lag](t, rec); got.Replicas == nil || len(got.Replicas) != 0 {
		t.Errorf("a primary streaming to nobody is an empty list: %#v", got)
	}
}

// A refused read of the Cluster runs nothing.
func TestLagRunsNothingWhenTheClusterIsNotReadable(t *testing.T) {
	h := sqlHost()
	h.listErr["clusters"] = fmt.Errorf("listing: %w", extension.ErrDenied)
	h.execFn = func(extension.ExecQuery) (extension.ExecResult, error) { return extension.ExecResult{}, nil }
	rec := serve(t, h, lagPath)
	if rec.Code != http.StatusForbidden || len(h.execs) != 0 {
		t.Fatalf("%d, %d execs", rec.Code, len(h.execs))
	}
}
