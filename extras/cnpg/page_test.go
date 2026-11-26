package cnpg

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/thonicdev/kubetower/extension"
)

// A table's pages: by its primary key, by ctid, or by offset; the cursor
// that asks the next one; the database a request names.

type answers = map[string]func([]string, func([]column, [][]byte) error) error

// numbered is n rows of one column, 1 to n, and the key's text after it.
func numbered(n int) [][]string {
	rows := make([][]string, n)
	for i := range rows {
		v := strconv.Itoa(i + 1)
		rows[i] = []string{v, v}
	}
	return rows
}

func pageOf(t *testing.T, db *fakeDB, path, body string) Rows {
	t.Helper()
	rec := servePage(t, withDB(db, nil), sqlHost(), path, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d: %s", rec.Code, rec.Body.String())
	}
	return decode[Rows](t, rec)
}

func lastAsked(db *fakeDB) string { return db.asked[len(db.asked)-1] }

func TestPagesFollowThePrimaryKey(t *testing.T) {
	db := &fakeDB{answers: answers{
		sqlTableOID:   rowsOf([]string{"16384", "r"}),
		sqlPrimaryKey: rowsOf([]string{"region", "text"}, []string{"id", "bigint"}),
		sqlColumns:    rowsOf([]string{"id", "bigint", "false"}, []string{"note", "text", "false"}, []string{"region", "text", "false"}),
		sqlRowsPrefix: func(_ []string, each func([]column, [][]byte) error) error {
			for i := 1; i <= MaxRows+1; i++ {
				v := strconv.Itoa(i)
				if err := each(nil, [][]byte{[]byte(v), []byte("n"), []byte("eu"), []byte("eu"), []byte(v)}); err != nil {
					return err
				}
			}
			return nil
		},
	}}
	first := pageOf(t, db, "/x/kind/cnpg/clusters/db/pg/tables/public/orders", `{}`)
	if first.Key != keyPrimary || !first.More || first.Next == "" || first.Start != 0 || len(first.Rows) != MaxRows {
		t.Fatalf("first page: key %q more %v next %q start %d rows %d", first.Key, first.More, first.Next, first.Start, len(first.Rows))
	}
	if !first.Columns[0].Primary || first.Columns[1].Primary || !first.Columns[2].Primary {
		t.Errorf("key columns %+v", first.Columns)
	}
	if len(first.Rows[0]) != 3 {
		t.Errorf("the key's own text is a cell: %+v", first.Rows[0])
	}
	want := `SELECT left(("id")::text, 513), left(("note")::text, 513), left(("region")::text, 513), ("region")::text, ("id")::text FROM "public"."orders" ORDER BY "public"."orders"."region", "public"."orders"."id" LIMIT 101 `
	if lastAsked(db) != want {
		t.Errorf("statement\n%s\nwant\n%s", lastAsked(db), want)
	}

	second := pageOf(t, db, "/x/kind/cnpg/clusters/db/pg/tables/public/orders", `{"cursor":"`+first.Next+`"}`)
	want = `SELECT left(("id")::text, 513), left(("note")::text, 513), left(("region")::text, 513), ("region")::text, ("id")::text FROM "public"."orders" WHERE ("public"."orders"."region", "public"."orders"."id") > ($1::text, $2::bigint) ORDER BY "public"."orders"."region", "public"."orders"."id" LIMIT 101 eu,100`
	if lastAsked(db) != want {
		t.Errorf("statement\n%s\nwant\n%s", lastAsked(db), want)
	}
	if second.Start != MaxRows {
		t.Errorf("second page starts at %d", second.Start)
	}
}

func TestATableWithoutAKeyPagesByCtid(t *testing.T) {
	for _, relkind := range []string{"r", "m"} {
		db := &fakeDB{answers: answers{
			sqlTableOID:   rowsOf([]string{"16384", relkind}),
			sqlPrimaryKey: rowsOf(),
			sqlColumns:    rowsOf([]string{"n", "integer", "false"}),
			sqlRowsPrefix: rowsOf(append(numbered(MaxRows), []string{"0", "(9,9)"})...),
		}}
		first := pageOf(t, db, "/x/kind/cnpg/clusters/db/pg/tables/public/log", "")
		if first.Key != keyCtid || first.Next == "" {
			t.Fatalf("%s: key %q next %q", relkind, first.Key, first.Next)
		}
		if !strings.HasSuffix(strings.TrimSpace(lastAsked(db)), `, ctid::text FROM "public"."log" ORDER BY "public"."log".ctid LIMIT 101`) {
			t.Errorf("%s: %s", relkind, lastAsked(db))
		}
		pageOf(t, db, "/x/kind/cnpg/clusters/db/pg/tables/public/log", `{"cursor":"`+first.Next+`"}`)
		if !strings.HasSuffix(lastAsked(db), ` WHERE "public"."log".ctid > $1::tid ORDER BY "public"."log".ctid LIMIT 101 100`) {
			t.Errorf("%s: %s", relkind, lastAsked(db))
		}
	}
}

func TestAViewPagesByOffsetUpToItsCeiling(t *testing.T) {
	for _, relkind := range []string{"v", "f", "p"} {
		db := &fakeDB{answers: answers{
			sqlTableOID:   rowsOf([]string{"16384", relkind}),
			sqlPrimaryKey: rowsOf(),
			sqlColumns:    rowsOf([]string{"n", "integer", "false"}),
			sqlRowsPrefix: rowsOf(append(numbered(MaxRows), []string{"0"})...),
		}}
		first := pageOf(t, db, "/x/kind/cnpg/clusters/db/pg/tables/public/v", "")
		if first.Key != keyOffset || first.Next == "" || strings.Contains(lastAsked(db), "OFFSET") || strings.Contains(lastAsked(db), "ORDER BY") {
			t.Fatalf("%s: key %q next %q, %s", relkind, first.Key, first.Next, lastAsked(db))
		}
		second := pageOf(t, db, "/x/kind/cnpg/clusters/db/pg/tables/public/v", `{"cursor":"`+first.Next+`"}`)
		if !strings.HasSuffix(strings.TrimSpace(lastAsked(db)), " LIMIT 101 OFFSET 100") || second.Start != 100 {
			t.Errorf("%s: %s start %d", relkind, lastAsked(db), second.Start)
		}
	}
	// The page that reaches the ceiling has no next.
	db := &fakeDB{answers: answers{
		sqlTableOID:   rowsOf([]string{"16384", "v"}),
		sqlColumns:    rowsOf([]string{"n", "integer", "false"}),
		sqlPrimaryKey: rowsOf(),
		sqlRowsPrefix: rowsOf(append(numbered(MaxRows), []string{"0"})...),
	}}
	last := pageOf(t, db, "/x/kind/cnpg/clusters/db/pg/tables/public/v", `{"cursor":"`+encodeCursor(pageCursor{Table: "16384", Key: keyOffset, Start: maxOffset - MaxRows})+`"}`)
	if !last.More || last.Next != "" {
		t.Errorf("at the ceiling: more %v next %q", last.More, last.Next)
	}
}

func TestTheLastPageHasNoNext(t *testing.T) {
	db := &fakeDB{answers: answers{
		sqlTableOID:   rowsOf([]string{"16384", "r"}),
		sqlPrimaryKey: rowsOf([]string{"n", "integer"}),
		sqlColumns:    rowsOf([]string{"n", "integer", "false"}),
		sqlRowsPrefix: rowsOf(numbered(MaxRows)...),
	}}
	got := pageOf(t, db, "/x/kind/cnpg/clusters/db/pg/tables/public/t", "")
	if got.More || got.Next != "" || len(got.Rows) != MaxRows {
		t.Errorf("more %v next %q rows %d", got.More, got.Next, len(got.Rows))
	}
}

// A key too long to send back ends the paging rather than growing a request.
func TestAKeyTooLongForACursorEndsThePaging(t *testing.T) {
	long := strings.Repeat("k", maxCursor)
	db := &fakeDB{answers: answers{
		sqlTableOID:   rowsOf([]string{"16384", "r"}),
		sqlPrimaryKey: rowsOf([]string{"k", "text"}),
		sqlColumns:    rowsOf([]string{"k", "text", "false"}),
		sqlRowsPrefix: func(_ []string, each func([]column, [][]byte) error) error {
			for i := 0; i <= MaxRows; i++ {
				if err := each(nil, [][]byte{[]byte("k"), []byte(long)}); err != nil {
					return err
				}
			}
			return nil
		},
	}}
	got := pageOf(t, db, "/x/kind/cnpg/clusters/db/pg/tables/public/t", "")
	if !got.More || got.Next != "" {
		t.Errorf("more %v, next of %d bytes", got.More, len(got.Next))
	}
}

func TestACursorForAnotherReadingIsRefused(t *testing.T) {
	db := func() *fakeDB {
		return &fakeDB{answers: answers{
			sqlTableOID:   rowsOf([]string{"16384", "r"}),
			sqlPrimaryKey: rowsOf([]string{"a", "integer"}, []string{"b", "integer"}),
			sqlColumns:    rowsOf([]string{"a", "integer", "false"}),
		}}
	}
	for name, cursor := range map[string]string{
		"not base64":     "%%%",
		"not json":       "bm90IGpzb24",
		"another key":    encodeCursor(pageCursor{Table: "16384", Key: keyCtid, Start: 100, Values: []string{"(0,1)"}}),
		"one value":      encodeCursor(pageCursor{Table: "16384", Key: keyPrimary, Start: 100, Values: []string{"1"}}),
		"three values":   encodeCursor(pageCursor{Table: "16384", Key: keyPrimary, Start: 100, Values: []string{"1", "2", "3"}}),
		"before the top": encodeCursor(pageCursor{Table: "16384", Key: keyPrimary, Start: -1, Values: []string{"1", "2"}}),
	} {
		d := db()
		body, _ := json.Marshal(map[string]string{"cursor": cursor})
		rec := servePage(t, withDB(d, nil), sqlHost(), "/x/kind/cnpg/clusters/db/pg/tables/public/t", string(body))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), CodeBadRequest) {
			t.Errorf("%s: answered %d %s", name, rec.Code, rec.Body.String())
		}
		for _, q := range d.asked {
			if strings.Contains(q, `FROM "public"."t"`) {
				t.Errorf("%s: read %s", name, q)
			}
		}
	}
	// An offset past the ceiling.
	d := &fakeDB{answers: answers{sqlTableOID: rowsOf([]string{"16384", "v"}), sqlPrimaryKey: rowsOf(), sqlColumns: rowsOf([]string{"a", "integer", "false"})}}
	body, _ := json.Marshal(map[string]string{"cursor": encodeCursor(pageCursor{Table: "16384", Key: keyOffset, Start: maxOffset + 1})})
	if rec := servePage(t, withDB(d, nil), sqlHost(), "/x/kind/cnpg/clusters/db/pg/tables/public/v", string(body)); rec.Code != http.StatusBadRequest {
		t.Errorf("past the ceiling: answered %d", rec.Code)
	}
}

// The rows are a POST: the cursor never rides in an address.
func TestRowsAreNotReadFromAnAddress(t *testing.T) {
	db := &fakeDB{answers: answers{}}
	rec := serveWith(t, withDB(db, nil), sqlHost(), "/x/kind/cnpg/clusters/db/pg/tables/public/t?cursor=x")
	if rec.Code != http.StatusMethodNotAllowed || len(db.asked) != 0 {
		t.Errorf("answered %d after %d statements", rec.Code, len(db.asked))
	}
}

func TestTheDatabaseIsTheOneAsked(t *testing.T) {
	db := &fakeDB{answers: answers{sqlDatabases: rowsOf([]string{"shop", "1"}, []string{"reporting", "1"})}}
	var seen credential
	rec := serveWith(t, withDB(db, &seen), sqlHost(), "/x/kind/cnpg/clusters/db/pg/tables?db=reporting")
	got := decode[Tables](t, rec)
	if rec.Code != http.StatusOK || seen.database != "reporting" || got.Database != "reporting" || strings.Join(got.Databases, ",") != "shop,reporting" {
		t.Fatalf("answered %d, opened %q, %+v", rec.Code, seen.database, got)
	}
	rec = serveWith(t, withDB(db, &seen), sqlHost(), "/x/kind/cnpg/clusters/db/pg/tables")
	if rec.Code != http.StatusOK || seen.database != "shop" {
		t.Errorf("no db: answered %d, opened %q", rec.Code, seen.database)
	}
	pageDB := &fakeDB{answers: answers{sqlTableOID: rowsOf([]string{"1", "r"}), sqlPrimaryKey: rowsOf(), sqlColumns: rowsOf([]string{"a", "integer", "false"})}}
	if rec := servePage(t, withDB(pageDB, &seen), sqlHost(), "/x/kind/cnpg/clusters/db/pg/tables/public/t", `{"database":"reporting"}`); rec.Code != http.StatusOK || seen.database != "reporting" {
		t.Errorf("a page: answered %d, opened %q", rec.Code, seen.database)
	}
	for _, bad := range []string{strings.Repeat("d", maxIdentifier+1), "a%00b", "%ff"} {
		seen = credential{}
		rec := serveWith(t, withDB(db, &seen), sqlHost(), "/x/kind/cnpg/clusters/db/pg/tables?db="+bad)
		if rec.Code != http.StatusBadRequest || seen.user != "" {
			t.Errorf("%q: answered %d, dialled %v", bad, rec.Code, seen.user != "")
		}
	}
	for name, body := range map[string]string{"not json": "{", "too long": `{"cursor":"` + strings.Repeat("c", maxAsk) + `"}`} {
		rec := servePage(t, withDB(pageDB, nil), sqlHost(), "/x/kind/cnpg/clusters/db/pg/tables/public/t", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: answered %d", name, rec.Code)
		}
	}
}

func TestADatabaseTheUserMayNotOpenIsNamed(t *testing.T) {
	for code, want := range map[string]struct {
		status int
		code   string
	}{
		"3D000": {http.StatusNotFound, CodeNoSuchDatabase},
		"42501": {http.StatusForbidden, CodeDatabaseDenied},
		"28P01": {http.StatusBadGateway, CodeUnreachable},
	} {
		x := testExtra()
		x.dial = func(context.Context, string, credential, *tls.Config) (querier, error) {
			return nil, fmt.Errorf("connect: %w", &pgconn.PgError{Code: code})
		}
		rec := serveWith(t, x, sqlHost(), "/x/kind/cnpg/clusters/db/pg/tables?db=other")
		if rec.Code != want.status || !strings.Contains(rec.Body.String(), `"`+want.code+`"`) {
			t.Errorf("%s: answered %d %s", code, rec.Code, rec.Body.String())
		}
	}
}

// A page of wide rows of escaped characters stays under MaxAnswer, says it
// stopped short, and the next page goes on from its last row.
func TestAPageStaysUnderItsBound(t *testing.T) {
	cols := make([][]string, MaxColumns)
	row := make([]string, MaxColumns+1)
	for i := range cols {
		cols[i] = []string{"c" + strconv.Itoa(i), "text", "false"}
		row[i] = strings.Repeat("\x01", MaxCell)
	}
	db := &fakeDB{answers: answers{
		sqlTableOID:   rowsOf([]string{"16384", "r"}),
		sqlPrimaryKey: rowsOf(),
		sqlColumns:    rowsOf(cols...),
		sqlRowsPrefix: func(_ []string, each func([]column, [][]byte) error) error {
			for i := 0; i <= MaxRows; i++ {
				values := make([][]byte, len(row))
				for j, v := range row {
					values[j] = []byte(v)
				}
				values[len(row)-1] = []byte("(0," + strconv.Itoa(i+1) + ")")
				if err := each(nil, values); err != nil {
					return err
				}
			}
			return nil
		},
	}}
	rec := servePage(t, withDB(db, nil), sqlHost(), "/x/kind/cnpg/clusters/db/pg/tables/public/wide", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d", rec.Code)
	}
	if rec.Body.Len() > MaxAnswer {
		t.Fatalf("answer of %d bytes, over %d", rec.Body.Len(), MaxAnswer)
	}
	got := decode[Rows](t, rec)
	if !got.Bounded || !got.More || got.Next == "" || len(got.Rows) == 0 || len(got.Rows) >= MaxRows {
		t.Fatalf("bounded %v more %v next %q rows %d", got.Bounded, got.More, got.Next, len(got.Rows))
	}
	next, err := decodeCursor(got.Next, "16384", keyCtid, 0)
	if err != nil || next.Start != len(got.Rows) || next.Values[0] != "(0,"+strconv.Itoa(len(got.Rows))+")" {
		t.Errorf("next %+v %v", next, err)
	}
}

// A read-only cluster refuses the tunnel before any Secret is revealed.
func TestAReadOnlyClusterIsRefusedBeforeItsSecretIsRead(t *testing.T) {
	for _, path := range []string{"/x/kind/cnpg/clusters/db/pg/stats", "/x/kind/cnpg/clusters/db/pg/tables"} {
		h := sqlHost()
		h.forwardErr = fmt.Errorf("%w: kind is marked read-only", extension.ErrReadOnly)
		rec := serveWith(t, withDB(&fakeDB{}, nil), h, path)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"`+CodeReadOnly+`"`) || len(h.revealed) != 0 {
			t.Errorf("%s: answered %d %s after revealing %v", path, rec.Code, rec.Body.String(), h.revealed)
		}
	}
	h := sqlHost()
	h.forwardErr = fmt.Errorf("%w: kind is marked read-only", extension.ErrReadOnly)
	if rec := servePage(t, withDB(&fakeDB{}, nil), h, "/x/kind/cnpg/clusters/db/pg/tables/public/t", ""); rec.Code != http.StatusForbidden || len(h.revealed) != 0 {
		t.Errorf("rows: answered %d after revealing %v", rec.Code, h.revealed)
	}
}

// No Secret is revealed for a tunnel that reached a pod not the cluster's.
func TestNoSecretIsReadForAnUnknownPod(t *testing.T) {
	h := sqlHost()
	h.tunnel.Pod = "stranger-0"
	rec := serveWith(t, withDB(&fakeDB{}, nil), h, "/x/kind/cnpg/clusters/db/pg/tables")
	if rec.Code != http.StatusConflict || len(h.revealed) != 0 {
		t.Errorf("answered %d after revealing %v", rec.Code, h.revealed)
	}
}

func TestACursorOfAnotherTableOrTypeIsRefused(t *testing.T) {
	db := &fakeDB{answers: answers{
		sqlTableOID:   rowsOf([]string{"16384", "r"}),
		sqlPrimaryKey: rowsOf([]string{"id", "bigint"}),
		sqlColumns:    rowsOf([]string{"id", "bigint", "false"}),
		sqlRowsPrefix: func(args []string, _ func([]column, [][]byte) error) error {
			if len(args) > 0 {
				return &pgconn.PgError{Code: "22P02", Message: "invalid input syntax for type bigint"}
			}
			return nil
		},
	}}
	other := encodeCursor(pageCursor{Table: "99999", Key: keyPrimary, Start: 100, Values: []string{"7"}})
	if rec := servePage(t, withDB(db, nil), sqlHost(), "/x/kind/cnpg/clusters/db/pg/tables/public/t", `{"cursor":"`+other+`"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("another table's cursor: answered %d", rec.Code)
	}
	typed := encodeCursor(pageCursor{Table: "16384", Key: keyPrimary, Start: 100, Values: []string{"not a number"}})
	if rec := servePage(t, withDB(db, nil), sqlHost(), "/x/kind/cnpg/clusters/db/pg/tables/public/t", `{"cursor":"`+typed+`"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), CodeBadRequest) {
		t.Errorf("a value of another type: answered %d %s", rec.Code, rec.Body.String())
	}
}

func TestATimeoutIsNamedApartFromAFailure(t *testing.T) {
	for name, c := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"deadline":          {context.DeadlineExceeded, http.StatusGatewayTimeout, CodeSQLTimeout},
		"statement timeout": {&pgconn.PgError{Code: "57014"}, http.StatusGatewayTimeout, CodeSQLTimeout},
		"other":             {errors.New("boom"), http.StatusBadGateway, CodeSQLFailed},
	} {
		db := &fakeDB{answers: answers{sqlDatabases: func([]string, func([]column, [][]byte) error) error { return c.err }}}
		rec := serveWith(t, withDB(db, nil), sqlHost(), "/x/kind/cnpg/clusters/db/pg/tables")
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), `"`+c.code+`"`) {
			t.Errorf("%s: answered %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestTheSessionIsReadOnlyAndInUTC(t *testing.T) {
	cfg, err := pgConfig("127.0.0.1", 5432, credential{user: "app", password: "x", database: "app"}, &tls.Config{MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RuntimeParams["TimeZone"] != "UTC" || cfg.RuntimeParams["default_transaction_read_only"] != "on" || cfg.Database != "app" {
		t.Errorf("runtime params %v, database %q", cfg.RuntimeParams, cfg.Database)
	}
}

// The bound counts the columns' names and types too: a hundred long names
// of escaped characters still answer under MaxAnswer.
func TestAPageWithLongColumnNamesStaysUnderItsBound(t *testing.T) {
	cols := make([][]string, MaxColumns)
	row := make([]string, MaxColumns+1)
	for i := range cols {
		// The longest a catalog spells: 63-byte names, a type qualified by
		// its schema, every character one the encoder escapes.
		cols[i] = []string{strings.Repeat("<", 60) + strconv.Itoa(i), `"` + strings.Repeat("<", 63) + `"."` + strings.Repeat(">", 63) + `"`, "false"}
		row[i] = strings.Repeat("\x01", MaxCell)
	}
	db := &fakeDB{answers: answers{
		sqlTableOID:   rowsOf([]string{"16384", "r"}),
		sqlPrimaryKey: rowsOf(),
		sqlColumns:    rowsOf(cols...),
		sqlRowsPrefix: func(_ []string, each func([]column, [][]byte) error) error {
			for i := 0; i <= MaxRows; i++ {
				values := make([][]byte, len(row))
				for j, v := range row {
					values[j] = []byte(v)
				}
				values[len(row)-1] = []byte("(0," + strconv.Itoa(i+1) + ")")
				if err := each(nil, values); err != nil {
					return err
				}
			}
			return nil
		},
	}}
	rec := servePage(t, withDB(db, nil), sqlHost(), "/x/kind/cnpg/clusters/db/pg/tables/public/wide", "")
	if rec.Code != http.StatusOK || rec.Body.Len() > MaxAnswer {
		t.Fatalf("answered %d with %d bytes, over %d", rec.Code, rec.Body.Len(), MaxAnswer)
	}
}

// A view's own data error on a later page is a failure, not a stale cursor:
// an offset binds no value.
func TestAViewsDataErrorIsNotAStaleCursor(t *testing.T) {
	db := &fakeDB{answers: answers{
		sqlTableOID:   rowsOf([]string{"16384", "v"}),
		sqlPrimaryKey: rowsOf(),
		sqlColumns:    rowsOf([]string{"ratio", "numeric", "false"}),
		sqlRowsPrefix: func([]string, func([]column, [][]byte) error) error {
			return &pgconn.PgError{Code: "22012", Message: "division by zero"}
		},
	}}
	cursor := encodeCursor(pageCursor{Table: "16384", Key: keyOffset, Start: 100})
	rec := servePage(t, withDB(db, nil), sqlHost(), "/x/kind/cnpg/clusters/db/pg/tables/public/v", `{"cursor":"`+cursor+`"}`)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"`+CodeSQLFailed+`"`) {
		t.Errorf("answered %d %s", rec.Code, rec.Body.String())
	}
}

// What a page holds besides its rows is counted from above, the extreme
// column list and the longest cursor included: more than a flat 64 KiB.
func TestThePageHeadIsCountedFromAbove(t *testing.T) {
	head := Rows{Schema: strings.Repeat("<", 63), Table: strings.Repeat("&", 63), Rows: [][]Cell{}, Key: keyPrimary, More: true, Bounded: true,
		Next: strings.Repeat("A", maxCursor), Start: maxOffset}
	for i := 0; i < MaxColumns; i++ {
		head.Columns = append(head.Columns, Column{Name: strings.Repeat("\x01", 60) + strconv.Itoa(i), Type: `"` + strings.Repeat("<", 63) + `"."` + strings.Repeat("<", 63) + `"[]`, Primary: true})
	}
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(head); err != nil {
		t.Fatal(err)
	}
	if got := answerHead(head); got < b.Len() || b.Len() <= 64<<10 {
		t.Errorf("the head counts %d bytes; encoded, it is %d", got, b.Len())
	}
}
