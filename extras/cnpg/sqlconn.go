package cnpg

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/thonicdev/kubetower/extension"
)

// The SQL door: one CloudNativePG cluster's statistics and tables, read with
// fixed, read-only statements, the Redis viewer's model.
//
//   - **A tunnel reviewed for the person**, to the cluster's read-write
//     Service (`<name>-rw`, port 5432), through Host.Forward: a person who
//     may not port-forward into the namespace reads nothing.
//   - **The password of the cluster's application Secret** (`<name>-app`, or
//     the one `spec.bootstrap.initdb.secret` names), read through
//     Host.Reveal for the person and audited, and never kept: it goes down
//     that one tunnel and is dropped with the request. It is sent only once
//     the tunnel says which pod it reached and that pod is one of this
//     cluster's instances.
//   - **TLS**, checked against the cluster's CA (`<name>-ca`, `ca.crt`) when
//     the person may read it, so the password is only ever sent encrypted.
//   - **Read-only by construction**: the session's default is
//     `default_transaction_read_only`, every statement runs in a `BEGIN READ
//     ONLY` transaction with a statement and a lock timeout, and the
//     statements are this package's own constants; a schema or table name
//     from the request is looked up in the catalog first and only ever
//     reaches SQL quoted as an identifier.
//
// Nothing is kept between requests: a request opens its tunnel and its
// connection, and closes both.

// The door's ceilings.
const (
	// postgresPort is CloudNativePG's Services' port.
	postgresPort = 5432
	// statementTimeout bounds every statement, connectTimeout the dial.
	statementTimeout = 5 * time.Second
	connectTimeout   = 10 * time.Second
)

// Codes of the SQL door, as the page spells them (web/app/extras/cnpg/data.ts).
const (
	CodeNoCredential  = "no_credential"
	CodeSecretDenied  = "secret_denied"
	CodeSecretUnsure  = "secret_unreviewed"
	CodeTunnelFailed  = "tunnel_failed"
	CodeTunnelRefused = "tunnel_refused"
	CodePodUnknown    = "pod_unknown"
	CodeUnreachable   = "unreachable"
	CodeSQLFailed     = "sql_failed"
	CodeNoSuchTable   = "no_such_table"
	// CodeNoSuchDatabase and CodeDatabaseDenied answer a database picked
	// from the list: gone since, or one the cluster's user may not open.
	CodeNoSuchDatabase = "no_such_database"
	CodeDatabaseDenied = "database_denied"
	// CodeSQLTimeout is a read Postgres did not finish in time.
	CodeSQLTimeout = "sql_timeout"
)

// querier runs one statement and hands each row over as text, its columns'
// names and type OIDs first. The live one is pgconn's; the tests' is a fake.
type querier interface {
	query(ctx context.Context, sql string, args []string, each func(cols []column, row [][]byte) error) error
	close()
}

// column is one result column: its name and its type's OID.
type column struct {
	name string
	oid  uint32
}

// sqlSession is one request's tunnel, connection and transaction.
type sqlSession struct {
	q        querier
	tunnel   extension.Tunnel
	database string
	verified bool
}

func (s *sqlSession) close() {
	if s.q != nil {
		s.q.close()
	}
	if s.tunnel.Stop != nil {
		s.tunnel.Stop()
	}
}

// dial is how the session reaches Postgres: pgconn in the console, a fake in
// the tests. It receives what the door decided and returns a querier already
// inside its read-only transaction.
type dial func(ctx context.Context, addr string, c credential, tlsConfig *tls.Config) (querier, error)

// credential is what the application Secret holds, for one request.
type credential struct {
	user, password, database string
}

// openSQL opens the door to one cluster for the request, or answers the
// request itself and returns false.
//
// database is the database to open, or "" for the Secret's own. It only ever
// reaches the connection's startup message, never a statement, and Postgres
// itself checks the user may connect to it.
func (x CNPG) openSQL(w http.ResponseWriter, r *http.Request, h extension.Host, cluster, namespace, name, database string) (*sqlSession, bool) {
	_, crd, ok := resolveClusters(w, r, h, cluster)
	if !ok {
		return nil, false
	}
	c, ok := readCluster(w, r, h, cluster, crd, namespace, name)
	if !ok {
		return nil, false
	}
	secret := str(c.Object, "spec", "bootstrap", "initdb", "secret", "name")
	if secret == "" {
		secret = name + "-app"
	}
	// The tunnel first, as Redis does: a refusal (a read-only cluster, a
	// port-forward the person may not open) answers before any Secret is
	// revealed and audited.
	tunnel, err := h.Forward(r.Context(), cluster, namespace, name+"-rw", postgresPort)
	switch {
	case errors.Is(err, extension.ErrReadOnly):
		writeProblem(w, http.StatusForbidden, CodeReadOnly, cluster+" is read-only: no tunnel is opened to its databases.", err)
		return nil, false
	case errors.Is(err, extension.ErrDenied), errors.Is(err, extension.ErrUnreviewable):
		status := http.StatusForbidden
		if errors.Is(err, extension.ErrUnreviewable) {
			status = http.StatusServiceUnavailable
		}
		writeProblem(w, status, CodeTunnelRefused, "You may not port-forward to "+namespace+"/"+name+"-rw.", err)
		return nil, false
	case err != nil:
		writeProblem(w, http.StatusBadGateway, CodeTunnelFailed, "No tunnel to "+namespace+"/"+name+"-rw could be opened.", err)
		return nil, false
	}
	s := &sqlSession{tunnel: tunnel}
	// The password goes only to a pod this cluster's own.
	if !tunnel.PodKnown || !isInstance(r, h, cluster, namespace, name, tunnel.Pod) {
		s.close()
		writeProblem(w, http.StatusConflict, CodePodUnknown, "The tunnel could not say which of "+name+"'s instances it reached, so no password was sent.", nil)
		return nil, false
	}
	cred, ok := readCredential(w, r, h, cluster, namespace, secret)
	if !ok {
		s.close()
		return nil, false
	}
	if database != "" {
		cred.database = database
	}
	s.database = cred.database

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: name + "-rw." + namespace + ".svc"}
	if pem, err := h.Reveal(r, cluster, namespace, name+"-ca", "ca.crt"); err == nil {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM([]byte(pem)) {
			tlsConfig.RootCAs = pool
			s.verified = true
		}
	}
	if !s.verified {
		// Encrypted, unverified: the tunnel already ends on the pod the
		// port-forward pinned, and the page says the certificate was not
		// checked.
		tlsConfig.InsecureSkipVerify = true //nolint:gosec // see above: the tunnel pins the pod, and the page says so
	}

	open := x.dial
	if open == nil {
		open = dialPostgres
	}
	q, err := open(r.Context(), tunnel.Addr, cred, tlsConfig)
	if err != nil {
		s.close()
		var pgErr *pgconn.PgError
		switch {
		case errors.As(err, &pgErr) && pgErr.Code == "3D000":
			writeProblem(w, http.StatusNotFound, CodeNoSuchDatabase, "There is no database "+cred.database+" in "+namespace+"/"+name+".", err)
		case errors.As(err, &pgErr) && pgErr.Code == "42501":
			writeProblem(w, http.StatusForbidden, CodeDatabaseDenied, "The cluster's user may not open the database "+cred.database+".", err)
		default:
			writeProblem(w, http.StatusBadGateway, CodeUnreachable, "Postgres in "+namespace+"/"+name+" did not answer.", err)
		}
		return nil, false
	}
	s.q = q
	return s, true
}

// readCredential reads the application Secret's user, password and database
// for the person. Its absence and its refusal are answers of their own.
func readCredential(w http.ResponseWriter, r *http.Request, h extension.Host, cluster, namespace, secret string) (credential, bool) {
	var c credential
	for _, field := range []struct {
		key      string
		into     *string
		optional bool
	}{{"username", &c.user, false}, {"password", &c.password, false}, {"dbname", &c.database, true}} {
		value, err := h.Reveal(r, cluster, namespace, secret, field.key)
		switch {
		case err == nil:
			*field.into = value
		case field.optional && errors.Is(err, extension.ErrNoSuchKey):
		case errors.Is(err, extension.ErrRevealDenied):
			writeProblem(w, http.StatusForbidden, CodeSecretDenied, "You may not read the Secret "+namespace+"/"+secret+" that holds the cluster's password.", err)
			return c, false
		case errors.Is(err, extension.ErrRevealUnreviewed):
			writeProblem(w, http.StatusServiceUnavailable, CodeSecretUnsure, "The console could not review reading the Secret "+namespace+"/"+secret+".", err)
			return c, false
		case errors.Is(err, extension.ErrNoSuchSecret), errors.Is(err, extension.ErrNoSuchKey):
			writeProblem(w, http.StatusNotFound, CodeNoCredential, "No "+field.key+" in the Secret "+namespace+"/"+secret+".", err)
			return c, false
		default:
			writeProblem(w, http.StatusBadGateway, CodeNoCredential, "The Secret "+namespace+"/"+secret+" could not be read.", err)
			return c, false
		}
	}
	if c.database == "" {
		c.database = "app"
	}
	return c, true
}

// isInstance is whether pod is one of the cluster's instances, as the person
// reads it: the operator's label names the cluster.
func isInstance(r *http.Request, h extension.Host, cluster, namespace, name, pod string) bool {
	if pod == "" {
		return false
	}
	page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Namespace: namespace, FieldSelector: "metadata.name=" + pod, Limit: 1})
	if err != nil {
		return false
	}
	for i := range page.Items {
		if page.Items[i].GetName() == pod && page.Items[i].GetLabels()["cnpg.io/cluster"] == name {
			return true
		}
	}
	return false
}

// dialPostgres is the live dial: pgconn, configured by hand so that nothing
// of the console's own environment (PGHOST, a .pgpass file) takes part.
func dialPostgres(ctx context.Context, addr string, c credential, tlsConfig *tls.Config) (querier, error) {
	host, portText, ok := strings.Cut(addr, ":")
	port, err := strconv.ParseUint(portText, 10, 16)
	if !ok || err != nil {
		return nil, fmt.Errorf("the tunnel's address %q", addr)
	}
	cfg, err := pgConfig(host, uint16(port), c, tlsConfig)
	if err != nil {
		return nil, err
	}
	conn, err := pgconn.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	begin := fmt.Sprintf("BEGIN READ ONLY; SET LOCAL statement_timeout = %d; SET LOCAL lock_timeout = 1000; SET LOCAL idle_in_transaction_session_timeout = %d",
		statementTimeout.Milliseconds(), (2 * statementTimeout).Milliseconds())
	if err := conn.Exec(ctx, begin).Close(); err != nil {
		_ = conn.Close(context.Background())
		return nil, err
	}
	return &pgQuerier{conn: conn}, nil
}

// pgConfig is the connection's configuration. The session is read-only from
// its startup message, and in UTC, so a timestamptz reads the same whatever
// the server's own time zone.
func pgConfig(host string, port uint16, c credential, tlsConfig *tls.Config) (*pgconn.Config, error) {
	cfg, err := pgconn.ParseConfig("sslmode=require")
	if err != nil {
		return nil, err
	}
	cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.Database = host, port, c.user, c.password, c.database
	cfg.TLSConfig, cfg.Fallbacks, cfg.ConnectTimeout = tlsConfig, nil, connectTimeout
	cfg.RuntimeParams = map[string]string{"application_name": "kubetower", "default_transaction_read_only": "on", "TimeZone": "UTC"}
	cfg.OnNotice, cfg.OnNotification = nil, nil
	return cfg, nil
}

type pgQuerier struct{ conn *pgconn.PgConn }

func (p *pgQuerier) query(ctx context.Context, sql string, args []string, each func([]column, [][]byte) error) error {
	params := make([][]byte, len(args))
	for i, a := range args {
		params[i] = []byte(a)
	}
	rr := p.conn.ExecParams(ctx, sql, params, nil, nil, nil)
	fields := rr.FieldDescriptions()
	cols := make([]column, len(fields))
	for i, f := range fields {
		cols[i] = column{name: f.Name, oid: f.DataTypeOID}
	}
	var stop error
	for rr.NextRow() {
		if stop == nil {
			stop = each(cols, rr.Values())
		}
	}
	_, err := rr.Close()
	if errors.Is(stop, errEnough) {
		stop = nil
	}
	if stop != nil {
		return stop
	}
	return err
}

func (p *pgQuerier) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = p.conn.Exec(ctx, "ROLLBACK").Close()
	_ = p.conn.Close(ctx)
}

// errEnough stops a row walk that has what it needs; it is not an error.
var errEnough = errors.New("enough rows")
