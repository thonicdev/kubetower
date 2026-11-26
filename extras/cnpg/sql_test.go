package cnpg

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/extension"
)

// The SQL door against a fake Postgres: which Secret and which tunnel, what
// each statement is, and what the answers hold, refusals apart.

// fakeDB answers the door's statements from a table of prefixes; every
// statement it was asked is recorded, with its arguments.
type fakeDB struct {
	answers map[string]func(args []string, each func([]column, [][]byte) error) error
	asked   []string
	closed  bool
}

func (f *fakeDB) query(_ context.Context, sql string, args []string, each func([]column, [][]byte) error) error {
	f.asked = append(f.asked, sql+" "+strings.Join(args, ","))
	// The longest prefix that matches: every statement starts with SELECT.
	best := ""
	for prefix := range f.answers {
		if strings.HasPrefix(sql, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best == "" {
		return nil
	}
	err := f.answers[best](args, each)
	if errors.Is(err, errEnough) {
		return nil
	}
	return err
}

func (f *fakeDB) close() { f.closed = true }

func rowsOf(rows ...[]string) func([]string, func([]column, [][]byte) error) error {
	return func(_ []string, each func([]column, [][]byte) error) error {
		for _, r := range rows {
			values := make([][]byte, len(r))
			for i, v := range r {
				if v != "\x00null" {
					values[i] = []byte(v)
				}
			}
			if err := each(nil, values); err != nil {
				return err
			}
		}
		return nil
	}
}

// sqlHost is a cluster `db/pg` the person may read, its app Secret and CA,
// and a tunnel to its primary.
func sqlHost() *fakeHost {
	h := newHost()
	h.add("clusters", pgCluster("db", "pg", 3, nil, nil))
	h.add("pods", podDoc("db", "pg", 1, "primary", "w1"))
	h.secrets = map[string]string{"db/pg-app#username": "app", "db/pg-app#password": "s3cret", "db/pg-app#dbname": "shop"}
	h.tunnel = &extension.Tunnel{Addr: "127.0.0.1:5999", Pod: "pg-1", PodKnown: true, Stop: func() {}}
	return h
}

func withDB(db *fakeDB, seen *credential) CNPG {
	x := testExtra()
	x.dial = func(_ context.Context, addr string, c credential, tlsConfig *tls.Config) (querier, error) {
		if seen != nil {
			*seen = c
		}
		return db, nil
	}
	return x
}

func serveWith(t *testing.T, x CNPG, h extension.Host, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	for _, rt := range x.Routes(h) {
		mux.Handle(rt.Method+" /x/{cluster}/cnpg"+rt.Pattern, rt.Handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// servePage asks a table's rows as the page does: a POST, the database and
// the cursor in its body.
func servePage(t *testing.T, x CNPG, h extension.Host, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	for _, rt := range x.Routes(h) {
		mux.Handle(rt.Method+" /x/{cluster}/cnpg"+rt.Pattern, rt.Handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

func TestStatsAreReadThroughTheClustersOwnSecretAndATunnel(t *testing.T) {
	h := sqlHost()
	db := &fakeDB{answers: map[string]func([]string, func([]column, [][]byte) error) error{
		sqlVersion:   rowsOf([]string{"17.2", "2026-11-10 08:00:00+00", "200", "12"}),
		sqlTotals:    rowsOf([]string{"990", "10", "5000", "7", "0"}),
		sqlDatabases: rowsOf([]string{"shop", "104857600"}, []string{"postgres", "8000000"}),
		sqlReplicas:  rowsOf([]string{"pg-2", "streaming", "\x00null"}, []string{"pg-3", "streaming", "2048"}),
	}}
	var cred credential
	rec := serveWith(t, withDB(db, &cred), h, "/x/kind/cnpg/clusters/db/pg/stats")
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d: %s", rec.Code, rec.Body.String())
	}
	got := decode[Stats](t, rec)
	if got.Database != "shop" || got.Connections != 12 || got.MaxConnections != 200 || got.Version != "17.2" ||
		got.CacheHit == nil || *got.CacheHit != 0.99 || got.Commits != 5000 || got.Rollbacks != 7 {
		t.Errorf("stats %+v", got)
	}
	if len(got.Databases) != 2 || got.Databases[0].Name != "shop" || got.Databases[0].Bytes != 104857600 {
		t.Errorf("databases %+v", got.Databases)
	}
	if len(got.Replicas) != 2 || got.Replicas[0].LagBytes != nil || got.Replicas[1].LagBytes == nil || *got.Replicas[1].LagBytes != 2048 {
		t.Errorf("replicas %+v: a hidden lag is absent, never zero", got.Replicas)
	}
	if cred != (credential{user: "app", password: "s3cret", database: "shop"}) {
		t.Errorf("credential %+v", cred)
	}
	if strings.Join(h.forwarded, ",") != "db/pg-rw:5432" {
		t.Errorf("tunnels %v, want the read-write Service only", h.forwarded)
	}
	if strings.Contains(rec.Body.String(), "s3cret") {
		t.Error("the password left in the answer")
	}
	if !db.closed {
		t.Error("the connection was left open")
	}
	if got.Verified {
		t.Error("no CA was readable, and the answer says the certificate was checked")
	}
}

func TestTheSecretAndTheTunnelAreRefusalsOfTheirOwn(t *testing.T) {
	for _, c := range []struct {
		name   string
		set    func(*fakeHost)
		status int
		code   string
	}{
		{"secret denied", func(h *fakeHost) { h.revealErr = extension.ErrRevealDenied }, http.StatusForbidden, CodeSecretDenied},
		{"secret unreviewed", func(h *fakeHost) { h.revealErr = extension.ErrRevealUnreviewed }, http.StatusServiceUnavailable, CodeSecretUnsure},
		{"no secret", func(h *fakeHost) { h.revealErr = extension.ErrNoSuchSecret }, http.StatusNotFound, CodeNoCredential},
		{"tunnel denied", func(h *fakeHost) { h.forwardErr = extension.ErrDenied }, http.StatusForbidden, CodeTunnelRefused},
		{"tunnel failed", func(h *fakeHost) { h.forwardErr = errors.New("dial: connection refused") }, http.StatusBadGateway, CodeTunnelFailed},
		{"unknown pod", func(h *fakeHost) { h.tunnel.PodKnown = false }, http.StatusConflict, CodePodUnknown},
		{"another pod", func(h *fakeHost) { h.tunnel.Pod = "intruder-0" }, http.StatusConflict, CodePodUnknown},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := sqlHost()
			c.set(h)
			sent := false
			x := testExtra()
			x.dial = func(context.Context, string, credential, *tls.Config) (querier, error) {
				sent = true
				return &fakeDB{}, nil
			}
			rec := serveWith(t, x, h, "/x/kind/cnpg/clusters/db/pg/stats")
			if rec.Code != c.status || !strings.Contains(rec.Body.String(), `"code":"`+c.code+`"`) {
				t.Fatalf("answered %d %s, want %d %s", rec.Code, rec.Body.String(), c.status, c.code)
			}
			if sent {
				t.Error("Postgres was dialled after a refusal")
			}
		})
	}
}

func TestACustomBootstrapSecretIsTheOneRead(t *testing.T) {
	h := sqlHost()
	h.objects["clusters"] = nil
	h.add("clusters", pgCluster("db", "pg", 3, map[string]any{"bootstrap": map[string]any{"initdb": map[string]any{"secret": map[string]any{"name": "pg-owner"}}}}, nil))
	h.secrets = map[string]string{"db/pg-owner#username": "owner", "db/pg-owner#password": "x"}
	var cred credential
	rec := serveWith(t, withDB(&fakeDB{}, &cred), h, "/x/kind/cnpg/clusters/db/pg/stats")
	if rec.Code != http.StatusOK || cred.user != "owner" || cred.database != "app" {
		t.Fatalf("answered %d, credential %+v", rec.Code, cred)
	}
}

func TestTablesListWhatTheUserMaySelect(t *testing.T) {
	db := &fakeDB{answers: map[string]func([]string, func([]column, [][]byte) error) error{
		sqlSchemas: rowsOf([]string{"public"}, []string{"sales"}),
		sqlTables:  rowsOf([]string{"public", "orders", "r", "1200", "245760"}, []string{"sales", "v_month", "v", "-1", "0"}),
	}}
	rec := serveWith(t, withDB(db, nil), sqlHost(), "/x/kind/cnpg/clusters/db/pg/tables")
	got := decode[Tables](t, rec)
	if rec.Code != http.StatusOK || strings.Join(got.Schemas, ",") != "public,sales" || len(got.Tables) != 2 ||
		got.Tables[0].Kind != "table" || got.Tables[0].Rows != 1200 || got.Tables[1].Kind != "view" || !got.Complete {
		t.Fatalf("answered %d %+v", rec.Code, got)
	}
}

func TestATablesRowsAreReadQuotedCappedAndCut(t *testing.T) {
	long := strings.Repeat("é", MaxCell+1)
	var rows [][]string
	for i := 0; i < MaxRows+1; i++ {
		rows = append(rows, []string{"1", long, "\x00null", "2048:25504446"})
	}
	db := &fakeDB{answers: map[string]func([]string, func([]column, [][]byte) error) error{
		sqlTableOID: func(args []string, each func([]column, [][]byte) error) error {
			if strings.Join(args, "|") != `we"ird;|t"bl` {
				return nil
			}
			return each(nil, [][]byte{[]byte("16384"), []byte("v")})
		},
		sqlPrimaryKey: rowsOf(),
		sqlColumns:    rowsOf([]string{"id", "integer", "false"}, []string{"note", "text", "false"}, []string{"gone", "text", "false"}, []string{"blob", "bytea", "true"}),
		sqlRowsPrefix: rowsOf(rows...),
	}}
	rec := servePage(t, withDB(db, nil), sqlHost(), `/x/kind/cnpg/clusters/db/pg/tables/we%22ird%3B/t%22bl`, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d: %s", rec.Code, rec.Body.String())
	}
	got := decode[Rows](t, rec)
	if len(got.Rows) != MaxRows || !got.More || len(got.Columns) != 4 {
		t.Fatalf("%d rows, more %v, %d columns", len(got.Rows), got.More, len(got.Columns))
	}
	first := got.Rows[0]
	if !first[1].Cut || len([]rune(first[1].Text)) != MaxCell || !first[2].Null || first[3].Bytes == nil || *first[3].Bytes != 2048 || first[3].Hex != "25504446" {
		t.Errorf("cells %+v", first)
	}
	statement := db.asked[len(db.asked)-1]
	want := `SELECT left(("id")::text, 513), left(("note")::text, 513), left(("gone")::text, 513), octet_length("blob")::text || ':' || encode(substring("blob" from 1 for 16), 'hex') FROM "we""ird;"."t""bl" LIMIT 101`
	if strings.TrimSpace(statement) != want {
		t.Errorf("statement\n%s\nwant\n%s", statement, want)
	}
}

func TestATableTheCatalogueDoesNotNameIsNotRead(t *testing.T) {
	db := &fakeDB{answers: map[string]func([]string, func([]column, [][]byte) error) error{}}
	rec := servePage(t, withDB(db, nil), sqlHost(), "/x/kind/cnpg/clusters/db/pg/tables/public/secret_stuff", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), CodeNoSuchTable) {
		t.Fatalf("answered %d %s", rec.Code, rec.Body.String())
	}
	for _, q := range db.asked {
		if strings.HasPrefix(q, sqlRowsPrefix) && !strings.HasPrefix(q, sqlTableOID) && strings.Contains(q, "FROM \"public\"") {
			t.Errorf("a table the catalog did not name was read: %s", q)
		}
	}
}

// A wide table answers within a time and a memory ceiling: Postgres cuts
// each value, so what crosses is bounded by MaxColumns × MaxRows × MaxCell.
func TestAWideTableIsBounded(t *testing.T) {
	var cols [][]string
	for i := 0; i < 200; i++ {
		cols = append(cols, []string{"c" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + strings.Repeat("0", i/26), "text", "false"})
	}
	row := make([]string, MaxColumns)
	for i := range row {
		row[i] = strings.Repeat("v", MaxCell+1)
	}
	var rows [][]string
	for i := 0; i < MaxRows+1; i++ {
		rows = append(rows, row)
	}
	db := &fakeDB{answers: map[string]func([]string, func([]column, [][]byte) error) error{
		sqlTableOID:   rowsOf([]string{"1", "v"}),
		sqlPrimaryKey: rowsOf(),
		sqlColumns:    rowsOf(cols...),
		sqlRowsPrefix: rowsOf(rows...),
	}}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	rec := servePage(t, withDB(db, nil), sqlHost(), "/x/kind/cnpg/clusters/db/pg/tables/public/wide", "")
	took := time.Since(start)
	runtime.ReadMemStats(&after)
	got := decode[Rows](t, rec)
	if got.ColumnsTotal != 200 || len(got.Columns) != MaxColumns || len(got.Rows) != MaxRows {
		t.Fatalf("columns %d of %d, rows %d", len(got.Columns), got.ColumnsTotal, len(got.Rows))
	}
	if took > 5*time.Second {
		t.Errorf("took %s", took)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 512<<20 {
		t.Errorf("allocated %d MiB", grew>>20)
	}
	if rec.Body.Len() > 16<<20 {
		t.Errorf("answer of %d MiB", rec.Body.Len()>>20)
	}
}

func TestQuoteIdentDoublesQuotes(t *testing.T) {
	for in, want := range map[string]string{`a`: `"a"`, `a"b`: `"a""b"`, `"; DROP TABLE x; --`: `"""; DROP TABLE x; --"`} {
		if got := quoteIdent(in); got != want {
			t.Errorf("quoteIdent(%q) = %s, want %s", in, got, want)
		}
	}
}
