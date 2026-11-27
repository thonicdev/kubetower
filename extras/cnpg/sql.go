package cnpg

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/thonicdev/kubetower/extension"
)

// What the SQL door reads, every statement a constant of this file: the
// cluster's statistics, its schemas and tables, and the first rows of one
// table. A request names a schema and a table; both are looked up in the
// catalog, for the session's user, before they reach a statement, and then
// only as quoted identifiers.

// The ceilings on one answer.
const (
	// MaxDatabases is how many databases the statistics list, largest first.
	MaxDatabases = 50
	// MaxReplicas is how many standbys the statistics list.
	MaxReplicas = 20
	// MaxTables is how many tables and views the tables list holds.
	MaxTables = 1000
	// MaxRows is how many rows of one table are read.
	MaxRows = 100
	// MaxColumns is how many of a table's columns are read.
	MaxColumns = 100
	// MaxCell is how many characters of one value are read; Postgres cuts
	// them, so a long value never crosses whole.
	MaxCell = 512
	// MaxHexBytes is how many of a binary value's bytes are read.
	MaxHexBytes = 16
	// MaxAnswer bounds a page of rows as JSON: a page stops before the row
	// that would cross it.
	MaxAnswer = 4 << 20
)

// Stats is `GET /clusters/{namespace}/{name}/stats`.
type Stats struct {
	Database string `json:"database"`
	// Verified is whether the server's certificate was checked against the
	// cluster's CA.
	Verified       bool   `json:"verified"`
	Version        string `json:"version"`
	StartedAt      string `json:"startedAt,omitempty"`
	Connections    int64  `json:"connections"`
	MaxConnections int64  `json:"maxConnections"`
	// CacheHit is blocks found in shared buffers over blocks asked, across
	// the databases; absent before anything was read.
	CacheHit  *float64 `json:"cacheHit,omitempty"`
	Commits   int64    `json:"commits"`
	Rollbacks int64    `json:"rollbacks"`
	Deadlocks int64    `json:"deadlocks"`
	// Databases are the databases the user may connect to, largest first.
	Databases []DatabaseSize `json:"databases"`
	Replicas  []Replica      `json:"replicas"`
}

// DatabaseSize is one database's size on disk.
type DatabaseSize struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// Replica is one standby the primary streams to. Its lag needs the
// pg_monitor role, which the application user does not hold: absent then.
type Replica struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	LagBytes *int64 `json:"lagBytes,omitempty"`
}

// Tables is `GET /clusters/{namespace}/{name}/tables`.
type Tables struct {
	Database string `json:"database"`
	// Databases are the databases the user may connect to, the one read
	// among them.
	Databases []string `json:"databases"`
	Verified  bool     `json:"verified"`
	Schemas   []string `json:"schemas"`
	Tables    []Table  `json:"tables"`
	// Complete is false when MaxTables cut the list.
	Complete bool `json:"complete"`
}

// Table is one table, view or materialized view the user may select from.
type Table struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	// Kind is table, partitioned, view, materialized or foreign.
	Kind string `json:"kind"`
	// Rows is Postgres' own estimate (reltuples), -1 before the first
	// ANALYZE; Bytes is the table with its indexes and TOAST.
	Rows  int64 `json:"rows"`
	Bytes int64 `json:"bytes"`
}

// Rows is `GET /clusters/{namespace}/{name}/tables/{schema}/{table}`.
type Rows struct {
	Schema   string   `json:"schema"`
	Table    string   `json:"table"`
	Columns  []Column `json:"columns"`
	Rows     [][]Cell `json:"rows"`
	Verified bool     `json:"verified"`
	// ColumnsTotal is every column; MaxColumns are read.
	ColumnsTotal int `json:"columnsTotal"`
	// More is true when the table holds rows after these.
	More bool `json:"more"`
	// Key is how the pages follow each other (page.go): "primary", "ctid"
	// or "offset".
	Key string `json:"key"`
	// Next asks for the page after this one, sent back as the cursor;
	// absent on the last page, or past what an offset may reach.
	Next string `json:"next,omitempty"`
	// Start is the first row's position, from 0.
	Start int `json:"start"`
	// Bounded is true when the page stopped short of MaxRows to stay under
	// MaxAnswer; Next then goes on from its last row.
	Bounded bool `json:"bounded,omitempty"`
}

// Column is one column: its name and its type as Postgres spells it.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Primary marks a column of the primary key.
	Primary bool `json:"primary,omitempty"`
}

// Cell is one value: absent for NULL, cut past MaxCell characters (Cut),
// and a binary value given as its size and its first MaxHexBytes bytes in
// hex (Bytes, Hex).
type Cell struct {
	Null  bool   `json:"null,omitempty"`
	Text  string `json:"text,omitempty"`
	Cut   bool   `json:"cut,omitempty"`
	Bytes *int64 `json:"bytes,omitempty"`
	Hex   string `json:"hex,omitempty"`
}

const (
	sqlVersion     = `SELECT current_setting('server_version'), pg_postmaster_start_time()::text, current_setting('max_connections')::bigint, (SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend')`
	sqlTotals      = `SELECT coalesce(sum(blks_hit), 0), coalesce(sum(blks_read), 0), coalesce(sum(xact_commit), 0), coalesce(sum(xact_rollback), 0), coalesce(sum(deadlocks), 0) FROM pg_stat_database`
	sqlDatabases   = `SELECT datname, pg_database_size(oid) FROM pg_database WHERE datallowconn AND has_database_privilege(oid, 'CONNECT') ORDER BY 2 DESC, 1 LIMIT 50`
	sqlReplicas    = `SELECT coalesce(application_name, ''), coalesce(state, ''), CASE WHEN replay_lsn IS NULL THEN NULL ELSE pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn)::bigint END FROM pg_stat_replication ORDER BY 1 LIMIT 20`
	sqlSchemas     = `SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema' AND has_schema_privilege(oid, 'USAGE') ORDER BY 1 LIMIT 200`
	sqlTables      = `SELECT n.nspname, c.relname, c.relkind::text, c.reltuples::bigint, pg_total_relation_size(c.oid) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f') AND n.nspname NOT LIKE 'pg\_%' AND n.nspname <> 'information_schema' AND has_table_privilege(c.oid, 'SELECT') ORDER BY 1, 2 LIMIT 1001`
	sqlTableOID    = `SELECT c.oid::text, c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r', 'p', 'v', 'm', 'f') AND has_table_privilege(c.oid, 'SELECT')`
	sqlColumns     = `SELECT attname, format_type(atttypid, atttypmod), (atttypid = 'bytea'::regtype)::text FROM pg_attribute WHERE attrelid = $1::oid AND attnum > 0 AND NOT attisdropped ORDER BY attnum`
	sqlRowsPrefix  = `SELECT `
	sqlRowsSuffix  = ` LIMIT 101`
	sqlCellText    = `left((%s)::text, 513)`
	sqlCellBinary  = `octet_length(%s)::text || ':' || encode(substring(%s from 1 for 16), 'hex')`
	relkindWord    = "rpvmf"
	relkindNames   = "table,partitioned,view,materialized,foreign"
	maxSchemaNames = 200
)

func (x CNPG) stats(h extension.Host) http.Handler {
	return x.sqlRoute(h, func(ctx context.Context, s *sqlSession, _ sqlAsk) (any, error) {
		out := Stats{Database: s.database, Verified: s.verified, Databases: []DatabaseSize{}, Replicas: []Replica{}}
		err := s.q.query(ctx, sqlVersion, nil, func(_ []column, row [][]byte) error {
			out.Version, out.StartedAt = text(row, 0), text(row, 1)
			out.MaxConnections, out.Connections = integer(row, 2), integer(row, 3)
			return errEnough
		})
		if err != nil {
			return nil, err
		}
		err = s.q.query(ctx, sqlTotals, nil, func(_ []column, row [][]byte) error {
			hit, read := integer(row, 0), integer(row, 1)
			if hit+read > 0 {
				ratio := float64(hit) / float64(hit+read)
				out.CacheHit = &ratio
			}
			out.Commits, out.Rollbacks, out.Deadlocks = integer(row, 2), integer(row, 3), integer(row, 4)
			return errEnough
		})
		if err != nil {
			return nil, err
		}
		err = s.q.query(ctx, sqlDatabases, nil, func(_ []column, row [][]byte) error {
			if len(out.Databases) < MaxDatabases {
				out.Databases = append(out.Databases, DatabaseSize{Name: text(row, 0), Bytes: integer(row, 1)})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		// A replica's lag: absent where Postgres hides it from this user.
		err = s.q.query(ctx, sqlReplicas, nil, func(_ []column, row [][]byte) error {
			if len(out.Replicas) < MaxReplicas {
				rep := Replica{Name: text(row, 0), State: text(row, 1)}
				if row[2] != nil {
					lag := integer(row, 2)
					rep.LagBytes = &lag
				}
				out.Replicas = append(out.Replicas, rep)
			}
			return nil
		})
		return out, err
	})
}

func (x CNPG) tables(h extension.Host) http.Handler {
	return x.sqlRoute(h, func(ctx context.Context, s *sqlSession, _ sqlAsk) (any, error) {
		out := Tables{Database: s.database, Databases: []string{}, Verified: s.verified, Schemas: []string{}, Tables: []Table{}, Complete: true}
		err := s.q.query(ctx, sqlDatabases, nil, func(_ []column, row [][]byte) error {
			if len(out.Databases) < MaxDatabases {
				out.Databases = append(out.Databases, text(row, 0))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		err = s.q.query(ctx, sqlSchemas, nil, func(_ []column, row [][]byte) error {
			if len(out.Schemas) < maxSchemaNames {
				out.Schemas = append(out.Schemas, text(row, 0))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		err = s.q.query(ctx, sqlTables, nil, func(_ []column, row [][]byte) error {
			if len(out.Tables) == MaxTables {
				out.Complete = false
				return errEnough
			}
			out.Tables = append(out.Tables, Table{Schema: text(row, 0), Name: text(row, 1), Kind: relkind(text(row, 2)), Rows: integer(row, 3), Bytes: integer(row, 4)})
			return nil
		})
		return out, err
	})
}

// quoteIdent is an identifier as SQL quotes it: in double quotes, a double
// quote doubled. The only way a name from a request reaches a statement, and
// only once the catalog has named it.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func relkind(k string) string {
	names := strings.Split(relkindNames, ",")
	if i := strings.Index(relkindWord, k); len(k) == 1 && i >= 0 {
		return names[i]
	}
	return k
}

func text(row [][]byte, i int) string {
	if i >= len(row) || row[i] == nil {
		return ""
	}
	return string(row[i])
}

func integer(row [][]byte, i int) int64 {
	n, _ := strconv.ParseInt(strings.SplitN(text(row, i), ".", 2)[0], 10, 64)
	return n
}

// sqlProblem is an answer a route worded itself.
type sqlProblem struct {
	status        int
	code, message string
}

func (p *sqlProblem) Error() string { return p.message }

// sqlRoute opens the door, runs read, closes the door, and answers.
func (x CNPG) sqlRoute(h extension.Host, read func(context.Context, *sqlSession, sqlAsk) (any, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, namespace, name := r.PathValue("cluster"), r.PathValue("namespace"), r.PathValue("name")
		ask, err := readAsk(r)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, err.Error(), nil)
			return
		}
		ask.schema, ask.table = r.PathValue("schema"), r.PathValue("table")
		s, ok := x.openSQL(w, r, h, cluster, namespace, name, ask.database)
		if !ok {
			return
		}
		defer s.close()
		ctx, cancel := context.WithTimeout(r.Context(), 3*statementTimeout)
		defer cancel()
		out, err := read(ctx, s, ask)
		var p *sqlProblem
		var pgErr *pgconn.PgError
		switch {
		case errors.As(err, &p):
			writeProblem(w, p.status, p.code, p.message, nil)
		case err != nil && r.Context().Err() != nil:
			// The page went away: nobody reads an answer.
			return
		case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &pgErr) && pgErr.Code == "57014"):
			writeProblem(w, http.StatusGatewayTimeout, CodeSQLTimeout, "Postgres in "+namespace+"/"+name+" did not finish the read in time.", err)
		case err != nil:
			writeProblem(w, http.StatusBadGateway, CodeSQLFailed, "Postgres in "+namespace+"/"+name+" did not answer the read.", err)
		default:
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, out)
		}
	})
}
