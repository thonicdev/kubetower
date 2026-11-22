package cnpg

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

// Replication lag in seconds, read on a click: one Host.Exec in the primary's
// postgres container running psql with one fixed query. Nothing the person
// types, and nothing of the Cluster's or a pod's, is part of the argv: it is
// the constant lagArgv below, which is also what the audit record names.
//
// The exec is reviewed for the person as `create pods/exec` on the primary
// by Host.Exec, and bounded there (output, deadline, count). psql reads
// nothing secret: inside a CloudNativePG pod it connects through the
// instance's own socket, so no password is read, sent or shown.

// lagQuery is the one statement: read-only, on a view. Each lag is an
// interval, NULL while a standby has replayed nothing lately, and so is
// empty in psql's output - an absent measurement, never a 0.
const lagQuery = "SELECT application_name, state, sync_state, " +
	"EXTRACT(EPOCH FROM write_lag), EXTRACT(EPOCH FROM flush_lag), EXTRACT(EPOCH FROM replay_lag) " +
	"FROM pg_stat_replication ORDER BY application_name"

// lagArgv is the argv, run as is (no shell): -X skips any psqlrc, -A and -t
// give bare rows separated by '|'.
var lagArgv = [...]string{"psql", "-XAt", "-c", lagQuery}

// lagContainer is the container CloudNativePG runs Postgres in.
const lagContainer = "postgres"

const (
	// lagTimeout bounds the exec, from the request to its last byte.
	lagTimeout = 10 * time.Second
	// MaxLagOutput is the most bytes of psql's output read; one row is under
	// 150, and MaxReplicas rows are the most kept.
	MaxLagOutput = 64 << 10
	// maxLagStderr is how much of psql's own complaint is shown.
	maxLagStderr = 300
)

const (
	CodeNoPrimary      = "no_primary"
	CodeExecForbidden  = "exec_forbidden"
	CodeExecUnreviewed = "exec_unreviewed"
	CodeNoPsql         = "no_psql"
	CodeExecBusy       = "exec_busy"
	CodeExecTimedOut   = "exec_timed_out"
	CodeExecFailed     = "exec_failed"
	CodeLagTruncated   = "lag_truncated"
	CodeLagQueryFailed = "lag_query_failed"
	CodeLagUnreadable  = "lag_unreadable"
)

// ReplicaLag is one standby the primary streams to. A lag is in seconds and
// absent when Postgres reported none (NULL), which says nothing was measured:
// it is not zero.
type ReplicaLag struct {
	Name   string   `json:"name"`
	State  string   `json:"state"`
	Sync   string   `json:"sync"`
	Write  *float64 `json:"write,omitempty"`
	Flush  *float64 `json:"flush,omitempty"`
	Replay *float64 `json:"replay,omitempty"`
}

// Lag is `GET /clusters/{namespace}/{name}/lag`. An empty Replicas is an
// answer too: the primary streams to nobody.
type Lag struct {
	Primary  string       `json:"primary"`
	Replicas []ReplicaLag `json:"replicas"`
}

// parseLag reads psql's `-XAt` output of lagQuery: one line per standby, six
// fields. A line of another shape, or a value that is not a finite,
// non-negative number, fails the whole read: a half-read answer is not drawn.
func parseLag(out []byte) ([]ReplicaLag, error) {
	rows := []ReplicaLag{}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\r\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(strings.TrimRight(line, "\r"), "|")
		if len(f) != 6 || f[0] == "" {
			return nil, errors.New("a line of the output is not a standby's six fields")
		}
		if len(rows) == MaxReplicas {
			break
		}
		r := ReplicaLag{Name: textutil.Clip(f[0], 63), State: textutil.Clip(f[1], 32), Sync: textutil.Clip(f[2], 32)}
		for i, dst := range []**float64{nil, nil, nil, &r.Write, &r.Flush, &r.Replay} {
			if dst == nil || f[i] == "" {
				continue
			}
			v, err := strconv.ParseFloat(f[i], 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				return nil, errors.New("a lag in the output is not a number of seconds")
			}
			*dst = &v
		}
		rows = append(rows, r)
	}
	return rows, nil
}

func (x CNPG) lag(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, namespace, name := r.PathValue("cluster"), r.PathValue("namespace"), r.PathValue("name")
		_, crd, ok := resolveClusters(w, r, h, cluster)
		if !ok {
			return
		}
		c, ok := readCluster(w, r, h, cluster, crd, namespace, name)
		if !ok {
			return
		}
		primary := ""
		for _, in := range summarise(c, nil, 0, 0, x.clock().UTC()).Instances {
			if in.Role == "primary" {
				primary = in.Name
			}
		}
		if primary == "" {
			writeProblem(w, http.StatusConflict, CodeNoPrimary, "This cluster reports no primary right now.", nil)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), lagTimeout+5*time.Second)
		defer cancel()
		res, err := h.Exec(r.WithContext(ctx), cluster, extension.ExecQuery{
			Namespace: namespace, Pod: primary, Container: lagContainer, Command: lagArgv[:], Timeout: lagTimeout,
		})
		if err != nil {
			writeLagFailure(w, cluster, err)
			return
		}
		switch {
		case res.Truncated || len(res.Stdout) > MaxLagOutput:
			writeProblem(w, http.StatusBadGateway, CodeLagTruncated, "psql's answer was cut: nothing of it is drawn.", nil)
		case res.ExitCode != 0:
			writeProblem(w, http.StatusBadGateway, CodeLagQueryFailed, "psql did not answer the query.",
				errors.New(textutil.FirstLineN(string(res.Stderr[:min(len(res.Stderr), 4096)]), maxLagStderr)))
		default:
			rows, perr := parseLag(res.Stdout)
			if perr != nil {
				writeProblem(w, http.StatusBadGateway, CodeLagUnreadable, "psql's answer is not in the shape this page reads.", perr)
				return
			}
			writeJSON(w, http.StatusOK, Lag{Primary: primary, Replicas: rows})
		}
	})
}

// writeLagFailure answers an exec that gave no result: refused for the
// person, refused as too busy, not possible in this image, too slow, or failed.
func writeLagFailure(w http.ResponseWriter, cluster string, err error) {
	switch {
	case errors.Is(err, extension.ErrUnknownCluster):
		writeProblem(w, http.StatusNotFound, CodeUnknownCluster, extension.UnknownClusterText(cluster), err)
	case errors.Is(err, extension.ErrDenied):
		writeProblem(w, http.StatusForbidden, CodeExecForbidden,
			"The cluster says you may not run commands in this pod (create pods/exec), so nothing was run.", err)
	case errors.Is(err, extension.ErrUnreviewable):
		writeProblem(w, http.StatusServiceUnavailable, CodeExecUnreviewed,
			"The console could not ask the cluster whether you may run commands in this pod, so it ran none.", err)
	case errors.Is(err, extension.ErrNoSuchCommand):
		writeProblem(w, http.StatusUnprocessableEntity, CodeNoPsql, "There is no psql in the primary's postgres container.", nil)
	case errors.Is(err, extension.ErrExecBusy):
		writeProblem(w, http.StatusTooManyRequests, CodeExecBusy, "Too many commands run by extensions are already running.", err)
	case errors.Is(err, extension.ErrExecTimedOut):
		writeProblem(w, http.StatusGatewayTimeout, CodeExecTimedOut, "psql did not answer within its deadline and was stopped.", err)
	case errors.Is(err, context.Canceled):
	default:
		writeProblem(w, http.StatusBadGateway, CodeExecFailed, "psql could not be run in the primary.", err)
	}
}
