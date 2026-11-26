package cnpg

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/thonicdev/kubetower/extension"
)

// A table's rows, a page at a time. `POST /clusters/{namespace}/{name}/tables/
// {schema}/{table}` with `{"database", "cursor"}` in its body: the cursor may
// hold a primary key's values, which have no place in an address or a log.
//
// How the pages follow each other depends on the table:
//
//   - by its primary key, `WHERE (k1, k2) > ($1::t1, $2::t2) ORDER BY k1, k2`,
//     for a table or a partitioned table that has one: stable, and as fast on
//     the last page as on the first;
//   - by ctid, `WHERE ctid > $1::tid ORDER BY ctid`, for a table or a
//     materialized view without one;
//   - by offset, for a view, a foreign table or a partitioned table without
//     a key, up to maxOffset rows: Postgres reads every skipped row.
//
// A cursor's values are only ever statement parameters, cast to the type the
// catalog gives the key's column; the names are the catalog's, quoted.

const (
	// sqlPrimaryKey is a table's primary key, its columns in the key's order.
	sqlPrimaryKey = `SELECT a.attname, format_type(a.atttypid, a.atttypmod) FROM pg_index i CROSS JOIN LATERAL unnest(i.indkey::int2[]) WITH ORDINALITY AS k(attnum, n) JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum WHERE i.indrelid = $1::oid AND i.indisprimary ORDER BY k.n`

	keyPrimary = "primary"
	keyCtid    = "ctid"
	keyOffset  = "offset"

	// maxOffset is how far pages by offset go.
	maxOffset = 10_000
	// maxCursor bounds a cursor, in its encoded form: a key too long to send
	// back ends the paging rather than growing a request without bound.
	maxCursor = 8 << 10
	// maxAsk bounds a request's body.
	maxAsk = maxCursor + 1<<10
	// maxIdentifier is Postgres' NAMEDATALEN - 1.
	maxIdentifier = 63
)

// sqlAsk is what a SQL route reads from its request.
type sqlAsk struct {
	database, cursor string
	schema, table    string
}

// readAsk reads the database (`?db=` on a GET, the body on a POST) and the
// cursor (a POST's body). Neither is ever written to a log.
func readAsk(r *http.Request) (sqlAsk, error) {
	var ask sqlAsk
	if r.Method == http.MethodPost {
		var body struct {
			Database string `json:"database"`
			Cursor   string `json:"cursor"`
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxAsk+1))
		switch {
		case err != nil:
			return ask, errors.New("the request's body could not be read")
		case len(raw) > maxAsk:
			return ask, errors.New("the request's body is longer than this console sends")
		case len(raw) > 0 && json.Unmarshal(raw, &body) != nil:
			return ask, errors.New("the request's body is not the JSON this console sends")
		}
		ask.database, ask.cursor = body.Database, body.Cursor
	} else {
		ask.database = r.URL.Query().Get("db")
	}
	if len(ask.database) > maxIdentifier || !utf8.ValidString(ask.database) || strings.ContainsRune(ask.database, 0) {
		return ask, errors.New("db does not name a database")
	}
	if len(ask.cursor) > maxCursor {
		return ask, errors.New("the cursor is longer than this console hands out")
	}
	return ask, nil
}

// pageCursor is a cursor before it is encoded: how it pages, the position of
// the page it asks for, and the last row's key (primary) or ctid.
type pageCursor struct {
	// Table is the table's oid: a cursor of another table, even one of the
	// same shape, is refused.
	Table  string   `json:"t"`
	Key    string   `json:"k"`
	Start  int      `json:"s"`
	Values []string `json:"v,omitempty"`
}

func encodeCursor(c pageCursor) string {
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

var errWrongCursor = &sqlProblem{http.StatusBadRequest, CodeBadRequest,
	"The page asked for belongs to another reading of this table. Open it again from its first page."}

// decodeCursor reads a cursor for the table oid, paged by key over
// keyColumns columns. The first page's cursor is "".
func decodeCursor(text, oid, key string, keyColumns int) (pageCursor, error) {
	if text == "" {
		return pageCursor{Table: oid, Key: key}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return pageCursor{}, errWrongCursor
	}
	var c pageCursor
	if json.Unmarshal(raw, &c) != nil || c.Table != oid || c.Key != key || c.Start < 0 {
		return pageCursor{}, errWrongCursor
	}
	switch key {
	case keyPrimary:
		if len(c.Values) != keyColumns {
			return pageCursor{}, errWrongCursor
		}
	case keyCtid:
		if len(c.Values) != 1 {
			return pageCursor{}, errWrongCursor
		}
	default:
		if len(c.Values) != 0 || c.Start > maxOffset {
			return pageCursor{}, errWrongCursor
		}
	}
	return c, nil
}

// keyColumn is one column of a primary key.
type keyColumn struct{ name, typ string }

// pagedBy is how a table of relkind (one letter) with a primary key of
// keyColumns columns pages.
func pagedBy(relkind string, keyColumns int) string {
	switch {
	case keyColumns > 0 && (relkind == "r" || relkind == "p"):
		return keyPrimary
	case relkind == "r" || relkind == "m":
		return keyCtid
	}
	return keyOffset
}

func (x CNPG) rows(h extension.Host) http.Handler {
	return x.sqlRoute(h, func(ctx context.Context, s *sqlSession, ask sqlAsk) (any, error) {
		schema, table := ask.schema, ask.table
		if !utf8.ValidString(schema) || !utf8.ValidString(table) || strings.ContainsRune(schema+table, 0) {
			return nil, &sqlProblem{http.StatusBadRequest, CodeBadRequest, "The address does not name a table."}
		}
		oid, relkind := "", ""
		if err := s.q.query(ctx, sqlTableOID, []string{schema, table}, func(_ []column, row [][]byte) error {
			oid, relkind = text(row, 0), text(row, 1)
			return errEnough
		}); err != nil {
			return nil, err
		}
		if oid == "" {
			return nil, &sqlProblem{http.StatusNotFound, CodeNoSuchTable, "There is no table " + schema + "." + table + " the cluster's user may read."}
		}
		var keys []keyColumn
		if err := s.q.query(ctx, sqlPrimaryKey, []string{oid}, func(_ []column, row [][]byte) error {
			keys = append(keys, keyColumn{name: text(row, 0), typ: text(row, 1)})
			return nil
		}); err != nil {
			return nil, err
		}
		key := pagedBy(relkind, len(keys))
		cursor, err := decodeCursor(ask.cursor, oid, key, len(keys))
		if err != nil {
			return nil, err
		}
		inKey := map[string]bool{}
		if key == keyPrimary {
			for _, k := range keys {
				inKey[k.name] = true
			}
		}

		out := Rows{Schema: schema, Table: table, Columns: []Column{}, Rows: [][]Cell{}, Verified: s.verified, Key: key, Start: cursor.Start}
		var binary []bool
		var exprs []string
		if err := s.q.query(ctx, sqlColumns, []string{oid}, func(_ []column, row [][]byte) error {
			out.ColumnsTotal++
			if len(out.Columns) == MaxColumns {
				return nil
			}
			out.Columns = append(out.Columns, Column{Name: text(row, 0), Type: text(row, 1), Primary: inKey[text(row, 0)]})
			isBinary := text(row, 2) == "true"
			binary = append(binary, isBinary)
			format := sqlCellText
			if isBinary {
				format = sqlCellBinary
			}
			exprs = append(exprs, strings.ReplaceAll(format, "%s", quoteIdent(text(row, 0))))
			return nil
		}); err != nil {
			return nil, err
		}
		if len(exprs) == 0 {
			return out, nil
		}
		shown := len(exprs)
		statement, args := pageStatement(exprs, quoteIdent(schema)+"."+quoteIdent(table), key, keys, cursor)
		var last []string
		size := 0
		err = s.q.query(ctx, statement, args, func(_ []column, row [][]byte) error {
			if len(out.Rows) == MaxRows {
				out.More = true
				return errEnough
			}
			cells := make([]Cell, min(shown, len(row)))
			grows := 2
			for i := range cells {
				cells[i] = cellOf(row[i], i < len(binary) && binary[i])
				grows += cellSize(cells[i])
			}
			// The row that would cross MaxAnswer starts the next page; a
			// first row always goes, as it is bounded by the cells' caps.
			if len(out.Rows) > 0 && size+grows > MaxAnswer-answerRoom {
				out.More, out.Bounded = true, true
				return errEnough
			}
			size += grows
			out.Rows = append(out.Rows, cells)
			last = last[:0]
			for i := shown; i < len(row); i++ {
				last = append(last, text(row, i))
			}
			return nil
		})
		var pgErr *pgconn.PgError
		if err != nil && ask.cursor != "" && errors.As(err, &pgErr) && strings.HasPrefix(pgErr.Code, "22") {
			// A cursor value that does not read as the key's type: not one
			// this table handed out.
			return nil, errWrongCursor
		}
		if err != nil {
			return nil, err
		}
		if out.More {
			next := pageCursor{Table: oid, Key: key, Start: cursor.Start + len(out.Rows)}
			if key != keyOffset {
				next.Values = append([]string(nil), last...)
			}
			if key != keyOffset || next.Start < maxOffset {
				if encoded := encodeCursor(next); len(encoded) <= maxCursor {
					out.Next = encoded
				}
			}
		}
		return out, nil
	})
}

// pageStatement is the statement that reads one page of from, and its
// parameters: the shown columns, then the key's (or the ctid) as full text
// for the next cursor; past the cursor; in the key's order; MaxRows + 1 rows,
// the last only saying there are more.
func pageStatement(exprs []string, from, key string, keys []keyColumn, c pageCursor) (string, []string) {
	var b strings.Builder
	b.WriteString(sqlRowsPrefix)
	b.WriteString(strings.Join(exprs, ", "))
	var args []string
	switch key {
	case keyPrimary:
		// The order names the table's columns, qualified: a bare name would
		// be the output column of that name, the key's text, and sort as text.
		names := make([]string, len(keys))
		for i, k := range keys {
			names[i] = from + "." + quoteIdent(k.name)
			b.WriteString(", (" + quoteIdent(k.name) + ")::text")
		}
		b.WriteString(" FROM " + from)
		if len(c.Values) > 0 {
			params := make([]string, len(keys))
			for i, k := range keys {
				// The type is the catalog's own spelling (format_type), never
				// the request's.
				params[i] = "$" + strconv.Itoa(i+1) + "::" + k.typ
			}
			b.WriteString(" WHERE (" + strings.Join(names, ", ") + ") > (" + strings.Join(params, ", ") + ")")
			args = c.Values
		}
		b.WriteString(" ORDER BY " + strings.Join(names, ", "))
	case keyCtid:
		b.WriteString(", ctid::text FROM " + from)
		if len(c.Values) > 0 {
			b.WriteString(" WHERE " + from + ".ctid > $1::tid")
			args = c.Values
		}
		b.WriteString(" ORDER BY " + from + ".ctid")
	default:
		b.WriteString(" FROM " + from)
	}
	b.WriteString(sqlRowsSuffix)
	if key == keyOffset && c.Start > 0 {
		b.WriteString(" OFFSET " + strconv.Itoa(c.Start))
	}
	return b.String(), args
}

// answerRoom is what a page holds besides its rows: the columns and the
// fields around them, well under this.
const answerRoom = 64 << 10

// cellSize is a cell's size as JSON, from above: each character as the
// encoder escapes it (a control character or <, >, & as \u00XX), and room
// for the field names.
func cellSize(c Cell) int {
	n := 40 + len(c.Hex)
	for _, r := range c.Text {
		switch {
		case r == '"' || r == '\\':
			n += 2
		case r < 0x20 || r == '<' || r == '>' || r == '&' || r == 0x2028 || r == 0x2029 || r == utf8.RuneError:
			n += 6
		default:
			n += utf8.RuneLen(r)
		}
	}
	return n
}

// cellOf is one value as the page draws it.
func cellOf(v []byte, binary bool) Cell {
	switch {
	case v == nil:
		return Cell{Null: true}
	case binary:
		size, hex, _ := strings.Cut(string(v), ":")
		n, _ := strconv.ParseInt(size, 10, 64)
		return Cell{Bytes: &n, Hex: hex}
	}
	s := string(v)
	if utf8.RuneCountInString(s) > MaxCell {
		runes := []rune(s)
		return Cell{Text: string(runes[:MaxCell]), Cut: true}
	}
	return Cell{Text: s}
}
