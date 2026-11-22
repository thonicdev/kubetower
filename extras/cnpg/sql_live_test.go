package cnpg

import (
	"context"
	"crypto/tls"
	"os"
	"strings"
	"testing"
)

// Against a real Postgres, when KT_TEST_POSTGRES names one (host:port, user
// and password `postgres`, TLS on): the read-only transaction, the timeout,
// and the statements as Postgres parses them. Skipped otherwise; the CI has
// no Postgres, and the fake above holds the rest.
//
//	docker run -d --name kt-pg -p 55439:5432 -e POSTGRES_PASSWORD=postgres postgres:17 \
//	  -c ssl=on -c ssl_cert_file=/etc/ssl/certs/ssl-cert-snakeoil.pem \
//	  -c ssl_key_file=/etc/ssl/private/ssl-cert-snakeoil.key
//	KT_TEST_POSTGRES=127.0.0.1:55439 go test ./extras/cnpg/ -run Live
func TestLivePostgres(t *testing.T) {
	addr := os.Getenv("KT_TEST_POSTGRES")
	if addr == "" {
		t.Skip("KT_TEST_POSTGRES names no Postgres")
	}
	ctx := context.Background()
	cred := credential{user: "postgres", password: "postgres", database: "postgres"}
	insecure := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // a local test server's snakeoil certificate

	q, err := dialPostgres(ctx, addr, cred, insecure)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	// Read-only: a write in the door's transaction is refused by Postgres.
	err = q.query(ctx, "CREATE TABLE kt_should_not_exist (id int)", nil, func([]column, [][]byte) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("a write in the door: %v", err)
	}
}

// Every statement of the door parses and runs on a real Postgres.
func TestLiveStatementsRun(t *testing.T) {
	addr := os.Getenv("KT_TEST_POSTGRES")
	if addr == "" {
		t.Skip("KT_TEST_POSTGRES names no Postgres")
	}
	ctx := context.Background()
	cred := credential{user: "postgres", password: "postgres", database: "postgres"}
	insecure := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // a local test server's snakeoil certificate
	for _, statement := range []string{sqlVersion, sqlTotals, sqlDatabases, sqlReplicas, sqlSchemas, sqlTables} {
		q, err := dialPostgres(ctx, addr, cred, insecure)
		if err != nil {
			t.Fatal(err)
		}
		rows := 0
		err = q.query(ctx, statement, nil, func(_ []column, _ [][]byte) error { rows++; return nil })
		q.close()
		if err != nil {
			t.Errorf("%.60s…: %v", statement, err)
		}
	}
	q, err := dialPostgres(ctx, addr, cred, insecure)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	oid := ""
	if err := q.query(ctx, sqlTableOID, []string{"pg_catalog", "pg_class"}, func(_ []column, row [][]byte) error { oid = string(row[0]); return nil }); err != nil || oid == "" {
		t.Fatalf("pg_class's oid: %q %v", oid, err)
	}
	n := 0
	if err := q.query(ctx, sqlColumns, []string{oid}, func(_ []column, _ [][]byte) error { n++; return nil }); err != nil || n == 0 {
		t.Fatalf("pg_class's columns: %d %v", n, err)
	}
	cut := 0
	statement := sqlRowsPrefix + strings.Replace(sqlCellText, "%s", quoteIdent("relname"), 1) + ", " + strings.Replace(sqlCellBinary, "%s", "'\\x00ff'::bytea", 1) + " FROM " + quoteIdent("pg_catalog") + "." + quoteIdent("pg_class") + sqlRowsSuffix
	if err := q.query(ctx, statement, nil, func(_ []column, row [][]byte) error {
		cut++
		if string(row[1]) != "2" {
			t.Errorf("bytea size %q, want 2", row[1])
		}
		return nil
	}); err != nil || cut == 0 || cut > 101 {
		t.Fatalf("rows: %d %v", cut, err)
	}
}
