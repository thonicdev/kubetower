package redis

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/thonicdev/kubetower/textutil"
)

// How long any single command may take, and the dial with it.
const commandTimeout = 10 * time.Second

// How much of a value is shown, and how much is ever read: a single Redis
// string may be half a gigabyte.
//
//   - A collection is read a page at a time, pageSize elements a page, and
//     never whole: the browser asks for the next page with the token the
//     previous one handed back (keys.go, Key).
//   - A list or a sorted set is paged by offset, and the server cuts the page
//     itself (LRANGE, ZRANGE).
//   - A hash or a set is scanned (HSCAN, SSCAN) from the cursor the token
//     carries, round after round until a page is held, the scan is over, or
//     maxPageRounds rounds have run. HGETALL and SMEMBERS are off the
//     allowlist: neither has a bound.
//   - A stream is read by XRANGE with a COUNT, from the entry after the last
//     one the previous page held.
//   - Every element of a collection is cut to maxElementBytes before it is
//     drawn, and marked when it was. Redis has no ranged read of one
//     element, so a long one still crosses the tunnel whole; at most a page
//     of them do.
//   - A string is read whole by GET only when STRLEN says it is at most
//     maxStringRead; maxValueBytes of it are shown, and the note says the whole
//     value was read. Above maxStringRead it is not read at all, and the note
//     says so: GET has no range, and GETRANGE is not on the allowlist.
const (
	maxValueBytes   = 64 * 1024
	maxStringRead   = 16 * 1024 * 1024
	maxScanPerRound = 500
	// pageSize is how many elements of a collection one page holds. A scanned
	// page may hold a few more: a round's elements are never dropped, because
	// the cursor has already moved past them.
	pageSize = 100
	// maxElementBytes is how much of one element of a collection is shown.
	maxElementBytes = 8 * 1024
	// maxPageRounds bounds a hash or set scan that keeps answering short
	// rounds (a round may legally return nothing). A page cut short still
	// hands back the cursor, so nothing is lost.
	maxPageRounds = 50
)

// readOnly is every command this package is allowed to send.
//
// Read-only is a property of the code: every command goes through do(), which
// refuses anything not in here before a byte is written;
// `TestNothingSendsACommandRoundTheAllowlist` greps the package for calls that
// bypass it, and wire_test.go records what reaches a server's socket.
//
// Two-word entries are container commands whose subcommand decides: `MEMORY
// USAGE` reads, `MEMORY PURGE` frees. Matching on the first word alone would let
// the second through on the strength of the first.
//
// Deliberately absent: KEYS (a stop-the-world scan; SCAN is here instead),
// MONITOR (a live feed of every client's commands), every SUBSCRIBE (never
// returns), every write, EVAL, SCRIPT and CONFIG.
//
// XRANGE reads a stream and changes nothing: unlike XREAD it never blocks,
// and unlike XREADGROUP it moves no consumer group. It is always sent with a
// COUNT (keys.go, streamPage).
var readOnly = map[string]bool{
	"PING":            true,
	"INFO":            true,
	"DBSIZE":          true,
	"SCAN":            true,
	"TYPE":            true,
	"TTL":             true,
	"GET":             true,
	"STRLEN":          true,
	"LLEN":            true,
	"LRANGE":          true,
	"HLEN":            true,
	"HSCAN":           true,
	"SCARD":           true,
	"SSCAN":           true,
	"ZCARD":           true,
	"ZRANGE":          true,
	"XLEN":            true,
	"XRANGE":          true,
	"MEMORY USAGE":    true,
	"OBJECT ENCODING": true,
}

// errNotARead is do() refusing a command. Its own sentinel, so a refusal by
// this package is never mistaken for the server's answer.
var errNotARead = errors.New("is not a read this console may send")

// errClosed is a command on a connection that has been closed.
var errClosed = errors.New("this connection is closed")

// Conn is one open connection, through one tunnel. It does not own the tunnel:
// the request that opened both closes both.
type Conn struct {
	// vk is the client, reached through do() only (see readOnly).
	vk valkey.Client

	// Engine is "Redis" or "Valkey", read from INFO rather than assumed.
	Engine  string
	Version string
	// Mode is the server's own word: "standalone", "cluster" or "sentinel".
	// This viewer reads single nodes, not a whole cluster: a Cluster node is
	// browsed and said to hold its own slots only, a Sentinel holds no keys.
	Mode string
	// Databases is how many this instance has, so the page can offer them.
	Databases int
	// Role is the server's own "primary" or "replica" from `INFO replication`,
	// empty when it did not say. Once probed, it replaces the role labels
	// guessed: after a failover the pod labeled master may be a replica.
	Role string
	// Replicas is `connected_slaves` on a primary: how many replicas are
	// attached to it right now. -1 when not said.
	Replicas int
}

// options is what open needs beyond the address.
type options struct {
	// password, when the instance wants one; empty means none. Never
	// rendered, logged or put in an error (secret.go).
	password string
	// database to select, in the client's handshake so SELECT stays out of
	// do().
	database int
}

// open dials an instance at addr - a tunnel's local address - and reads what it
// is.
//
// The client's own traffic is kept small and known, because wire_test.go
// asserts on every byte of it:
//
//   - ForceSingleClient: one server, never a cluster. Without it the client
//     asks `CLUSTER SLOTS` first, which is a command this package did not send.
//   - DisableCache: client-side caching turns on `CLIENT TRACKING`, a change to
//     the connection the server holds.
//   - DisableRetry: a command that failed once is reported, not re-sent.
//   - ClientSetInfo disabled: `CLIENT SETINFO` writes the library's name onto
//     the server's client list; nothing here needs it.
//
// What is left is the handshake: `HELLO 3` (carrying `AUTH default <password>`
// when there is one), `SELECT` when the database is not 0, and on a server too
// old for `HELLO` the RESP2 sequence (`AUTH`, `HELLO 2`, `SELECT`). Plus `PING`,
// which the client sends to keep an idle connection alive and which is on the
// allowlist anyway.
func open(ctx context.Context, addr string, opts options) (*Conn, error) {
	vk, err := valkey.NewClient(valkey.ClientOption{
		InitAddress:       []string{addr},
		Password:          opts.password,
		SelectDB:          opts.database,
		ForceSingleClient: true,
		DisableCache:      true,
		DisableRetry:      true,
		ClientSetInfo:     valkey.DisableClientSetInfo,
		Dialer:            net.Dialer{Timeout: commandTimeout},
		ConnWriteTimeout:  commandTimeout,
	})
	if err != nil {
		if vk != nil {
			// ForceSingleClient hands back a client even when the first dial
			// failed; it holds a goroutine until closed.
			vk.Close()
		}
		return nil, err
	}
	conn := &Conn{vk: vk}
	if err := conn.identify(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// Close is safe to call more than once.
func (c *Conn) Close() {
	if c.vk != nil {
		c.vk.Close()
		c.vk = nil
	}
}

// do is the only place a command is sent. It refuses anything outside readOnly
// before the client is touched, so nothing reaches the socket.
func (c *Conn) do(ctx context.Context, args ...string) (valkey.ValkeyMessage, error) {
	if err := allowed(args); err != nil {
		return valkey.ValkeyMessage{}, err
	}
	if c.vk == nil {
		return valkey.ValkeyMessage{}, errClosed
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	return c.vk.Do(ctx, c.vk.B().Arbitrary(args...).Build()).ToMessage()
}

// allowed decides whether a command may be sent, by name and - for the
// container commands - by subcommand.
func allowed(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("no command: %w", errNotARead)
	}
	name := asciiUpper(args[0])
	// The command is one bare word. `"MEMORY USAGE"` handed over as a single
	// argument would match the pair below by its spelling and reach the
	// server as one unknown word: refused here, whatever the server would do.
	if !isWord(name) {
		return fmt.Errorf("%q %w", args[0], errNotARead)
	}
	if len(args) > 1 {
		pair := name + " " + asciiUpper(args[1])
		if readOnly[pair] {
			return nil
		}
		// A container command whose subcommand is not allowed must not fall
		// through to the single-word check, or `MEMORY PURGE` would pass.
		if isContainer(name) {
			return fmt.Errorf("%s %s %w", name, args[1], errNotARead)
		}
	}
	if !readOnly[name] {
		return fmt.Errorf("%s %w", name, errNotARead)
	}
	return nil
}

// asciiUpper upper-cases ASCII letters only. strings.ToUpper maps `ſ` (U+017F)
// to `S`, so `ſcan` would read as SCAN here while the server, which compares
// bytes, reads something else: the guard must judge the bytes that are sent.
func asciiUpper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'a' <= c && c <= 'z' {
			b[i] = c - ('a' - 'A')
		}
	}
	return string(b)
}

// isWord is a command name: ASCII letters, at least one, nothing else.
func isWord(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

// isContainer names the commands whose first word decides nothing on its own.
func isContainer(name string) bool {
	switch name {
	case "MEMORY", "OBJECT", "CLIENT", "CLUSTER", "CONFIG", "ACL", "SCRIPT", "FUNCTION", "LATENCY", "XINFO":
		return true
	}
	return false
}

// text, number and list read one reply of the expected shape.
func (c *Conn) text(ctx context.Context, args ...string) (string, error) {
	msg, err := c.do(ctx, args...)
	if err != nil {
		return "", err
	}
	return msg.ToString()
}

func (c *Conn) number(ctx context.Context, args ...string) (int64, error) {
	msg, err := c.do(ctx, args...)
	if err != nil {
		return 0, err
	}
	return msg.AsInt64()
}

// identify reads INFO and settles what this is.
func (c *Conn) identify(ctx context.Context) error {
	raw, err := c.text(ctx, "INFO", "server")
	if err != nil {
		return fmt.Errorf("connected, but INFO failed: %w", err)
	}
	fields := parseInfo(raw)
	c.Engine, c.Version = engineOf(fields)
	c.Mode = fields["redis_mode"]
	if mode := fields["server_mode"]; mode != "" {
		c.Mode = mode
	}

	// A Cluster node says so in its own section too, and a server old enough
	// not to carry redis_mode still answers this.
	if raw, err := c.text(ctx, "INFO", "cluster"); err == nil && parseInfo(raw)["cluster_enabled"] == "1" {
		c.Mode = "cluster"
	}
	if c.Mode == "" {
		c.Mode = "standalone"
	}

	c.Databases = 16
	if c.Mode == "cluster" {
		// A Cluster node serves database 0 only.
		c.Databases = 1
	}
	c.Role, c.Replicas = "", -1
	if raw, err := c.text(ctx, "INFO", "replication"); err == nil {
		c.Role, c.Replicas = replicationOf(parseInfo(raw))
	}

	if raw, err := c.text(ctx, "INFO", "keyspace"); err == nil {
		if n := highestDatabase(parseInfo(raw)); n >= c.Databases {
			// An instance with more than the default databases says so only by
			// having keys in one.
			c.Databases = n + 1
		}
	}
	return nil
}

// engineOf reads the engine and its version from INFO server.
//
// Valkey reports `redis_version` too; its own `valkey_version` and
// `server_name:valkey` say which fork this is.
func engineOf(fields map[string]string) (engine, version string) {
	engine, version = "Redis", fields["redis_version"]
	if v := fields["valkey_version"]; v != "" || strings.EqualFold(fields["server_name"], "valkey") {
		engine = "Valkey"
		if v != "" {
			version = v
		}
	}
	if version == "" {
		version = "unknown"
	}
	return engine, version
}

// replicationOf reads `role` and `connected_slaves` from INFO replication,
// in this console's words: the server says `master` and `slave`, and the
// inventory says primary and replica everywhere else.
func replicationOf(fields map[string]string) (role string, replicas int) {
	replicas = -1
	switch fields["role"] {
	case "master":
		role = rolePrimary
		if n, err := strconv.Atoi(fields["connected_slaves"]); err == nil {
			replicas = n
		}
	case "slave", "replica":
		role = roleReplica
	}
	return role, replicas
}

// parseInfo turns INFO's `key:value` lines into a map, ignoring its `# Section`
// headings and blank lines.
func parseInfo(raw string) map[string]string {
	// RESP3 answers INFO with a verbatim string whose first bytes name its
	// format; the client hands them over as they came.
	raw = strings.TrimPrefix(raw, "txt:")
	out := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out
}

// highestDatabase reads the `dbN` keys INFO keyspace answers with.
func highestDatabase(fields map[string]string) int {
	highest := -1
	for key := range fields {
		if !strings.HasPrefix(key, "db") {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimPrefix(key, "db")); err == nil && n > highest {
			highest = n
		}
	}
	return highest
}

// Overview is what the page says about the instance itself.
type Overview struct {
	Keys       int64
	UsedMemory string
	// Keyspace is the databases that hold anything, in order.
	Keyspace []DatabaseSummary
}

// DatabaseSummary is one `dbN` line of INFO keyspace.
type DatabaseSummary struct {
	Index   int   `json:"index"`
	Keys    int64 `json:"keys"`
	Expires int64 `json:"expires"`
}

// Overview reads what the instance says about itself.
func (c *Conn) Overview(ctx context.Context) (Overview, error) {
	var out Overview

	size, err := c.number(ctx, "DBSIZE")
	if err != nil {
		return out, fmt.Errorf("DBSIZE failed: %w", err)
	}
	out.Keys = size

	if raw, err := c.text(ctx, "INFO", "memory"); err == nil {
		if n, err := strconv.ParseInt(parseInfo(raw)["used_memory"], 10, 64); err == nil {
			out.UsedMemory = textutil.Bytes(n)
		}
	}

	if raw, err := c.text(ctx, "INFO", "keyspace"); err == nil {
		for key, value := range parseInfo(raw) {
			if !strings.HasPrefix(key, "db") {
				continue
			}
			index, err := strconv.Atoi(strings.TrimPrefix(key, "db"))
			if err != nil {
				continue
			}
			summary := DatabaseSummary{Index: index}
			for _, part := range strings.Split(value, ",") {
				name, number, ok := strings.Cut(part, "=")
				if !ok {
					continue
				}
				n, err := strconv.ParseInt(number, 10, 64)
				if err != nil {
					continue
				}
				switch name {
				case "keys":
					summary.Keys = n
				case "expires":
					summary.Expires = n
				}
			}
			out.Keyspace = append(out.Keyspace, summary)
		}
		sort.Slice(out.Keyspace, func(i, j int) bool { return out.Keyspace[i].Index < out.Keyspace[j].Index })
	}
	return out, nil
}
