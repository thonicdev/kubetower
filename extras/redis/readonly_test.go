package redis

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Nothing in this package can send a command that writes.
//
// **The claim this whole design exists to make good.** The guard is
// structural: every command goes through `do`, `do` refuses anything outside
// `readOnly`, and this test looks for the ways round it. wire_test.go then
// checks the same claim where it matters, on a server's socket.
func TestNothingSendsACommandRoundTheAllowlist(t *testing.T) {
	// Every method of valkey-go's client is a way to send something that never
	// reaches `do` - `c.vk.Do(ctx, c.vk.B().Set()...)` from anywhere else, or a
	// `DoMulti`, a `Receive`, a `Dedicated`. So the rule is not "no writes", it
	// is **one door**: the client is named `vk` and touched in exactly the
	// three ways below.
	direct := regexp.MustCompile(`\bvk\.[A-Z]\w*\(`)
	allowed := map[string]bool{
		"vk.Do(":    true, // the one send, inside do()
		"vk.B(":     true, // the builder do() hands its arguments to
		"vk.Close(": true,
	}

	sends := 0
	for _, name := range sourceFiles(t) {
		text := readFile(t, name)
		for _, call := range direct.FindAllString(text, -1) {
			if call == "vk.Do(" {
				sends++
			}
			if allowed[call] {
				continue
			}
			t.Errorf("%s calls %s directly. Every command goes through do(), which is what "+
				"makes `read-only` a property of this package rather than a promise in a "+
				"comment - a typed helper is one door more than there should be.", name, call)
		}
	}
	if sends != 1 {
		t.Errorf("vk.Do( appears %d times; it must appear once, in do(), after the allowlist", sends)
	}
}

// And the allowlist itself holds nothing that writes.
//
// The list is edited by hand, so the test that matters is not "is it enforced"
// (above) but "is it right".
func TestTheAllowlistHoldsNoCommandThatChangesAnything(t *testing.T) {
	forbidden := map[string]string{
		"SET":       "writes a key",
		"DEL":       "deletes a key",
		"UNLINK":    "deletes a key",
		"EXPIRE":    "changes when a key dies",
		"PERSIST":   "changes when a key dies",
		"RENAME":    "moves a key",
		"FLUSHDB":   "empties a database",
		"FLUSHALL":  "empties every database",
		"CONFIG":    "changes the server",
		"SHUTDOWN":  "stops the server",
		"KEYS":      "blocks the server for the length of the keyspace - SCAN is the read",
		"MONITOR":   "streams every command every client sends, which is other people's data",
		"SUBSCRIBE": "never returns",
		"SCRIPT":    "loads code into the server",
		"EVAL":      "runs code in the server, which can write",
		"SELECT":    "changes the connection's database - it is chosen at dial, by the handshake",
		"MEMORY":    "is a container command; only `MEMORY USAGE` may be listed, never the bare word",
		"OBJECT":    "is a container command; only `OBJECT ENCODING` may be listed",
	}

	for command, why := range forbidden {
		if readOnly[command] {
			t.Errorf("the allowlist holds %q, which %s", command, why)
		}
	}
}

// A container command's subcommand decides, and the first word must not carry it.
func TestAContainerCommandIsJudgedOnItsSubcommand(t *testing.T) {
	for _, c := range []struct {
		args    []string
		allowed bool
		why     string
	}{
		{[]string{"MEMORY", "USAGE", "k"}, true, "the read this console uses"},
		{[]string{"MEMORY", "PURGE"}, false, "frees memory, which is a change to the server"},
		{[]string{"MEMORY", "DOCTOR"}, false, "not on the list, whatever it does"},
		{[]string{"OBJECT", "ENCODING", "k"}, true, "the read this console uses"},
		{[]string{"OBJECT", "FREQ", "k"}, false, "not on the list"},
		{[]string{"CONFIG", "GET", "maxmemory"}, false, "CONFIG is not this console's to touch at all"},
		{[]string{"CONFIG", "SET", "maxmemory", "1"}, false, "changes the server"},
		{[]string{"SCAN", "0", "COUNT", "500"}, true, "the paged read"},
		{[]string{"KEYS", "*"}, false, "blocks the server"},
		{[]string{"MONITOR"}, false, "streams every command every client sends"},
		{[]string{"XRANGE", "s", "-", "+", "COUNT", "100"}, true, "reads one page of a stream"},
		{[]string{"XREAD", "BLOCK", "0", "STREAMS", "s", "$"}, false, "blocks, and is not on the list"},
		{[]string{"SET", "k", "v"}, false, "writes"},
		{[]string{"set", "k", "v"}, false, "writes, in lower case"},
		{[]string{"GET", "k"}, true, "reads"},
		{[]string{}, false, "is nothing at all"},
	} {
		err := allowed(c.args)
		if c.allowed && err != nil {
			t.Errorf("%v was refused (%s): %v", c.args, c.why, err)
		}
		if !c.allowed && err == nil {
			t.Errorf("%v was allowed through, and it %s", c.args, c.why)
		}
		if !c.allowed && err != nil && !errors.Is(err, errNotARead) {
			t.Errorf("%v was refused with %v, which is not this package's refusal", c.args, err)
		}
	}
}

// A closed connection refuses rather than dereferencing nothing.
func TestAClosedConnectionRefusesInsteadOfPanicking(t *testing.T) {
	c := &Conn{}
	if _, err := c.do(t.Context(), "PING"); err == nil {
		t.Error("a command on a connection with no client returned no error")
	} else if !strings.Contains(err.Error(), "closed") {
		t.Errorf("the refusal does not say the connection is closed: %v", err)
	}
	// Twice, because Close is documented as safe to call more than once and a
	// failure path calls it and then the request's unwind does again.
	c.Close()
	c.Close()
}

// The container check earns its place on the one case the allowlist test cannot
// reach: a bare container word put on the allowlist by a future edit reaching
// for `CLIENT LIST`, where only the container check stands between that and
// `CLIENT KILL`.
func TestABareContainerWordOnTheAllowlistStillRefusesItsSubcommands(t *testing.T) {
	readOnly["CLIENT"] = true
	t.Cleanup(func() { delete(readOnly, "CLIENT") })

	if err := allowed([]string{"CLIENT", "KILL", "id", "7"}); err == nil {
		t.Error("`CLIENT KILL` was allowed because `CLIENT` had been listed as though it " +
			"were a command. A container command's first word decides nothing, and the " +
			"allowlist is edited by hand")
	}
	if err := allowed([]string{"CLIENT", "LIST"}); err == nil {
		t.Error("even the harmless subcommand must be listed as a pair; a bare container " +
			"word on the allowlist is always a mistake, whichever subcommand follows it")
	}
}

func sourceFiles(t *testing.T) []string {
	t.Helper()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing the package: %v", err)
	}
	var out []string
	for _, name := range names {
		if !strings.HasSuffix(name, "_test.go") {
			out = append(out, name)
		}
	}
	if len(out) < 5 {
		t.Fatalf("found %d source files; this test is running somewhere unexpected", len(out))
	}
	return out
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(raw)
}

// The allowlist is exactly twenty reads - the prototype's, with HSCAN and
// SSCAN in place of HGETALL and SMEMBERS, and XRANGE to read a stream's
// entries a page at a time. The test above
// names what must never be on it; this pins what is, so adding anything - a
// harmless-looking `HSET`, or `GETRANGE` for a convenience - fails here and
// is a decision made in review rather than an edit nobody noticed.
func TestTheAllowlistIsExactlyTheTwentyReads(t *testing.T) {
	want := []string{
		"PING", "INFO", "DBSIZE", "SCAN", "TYPE", "TTL", "GET", "STRLEN", "LLEN", "LRANGE",
		// HSCAN and SSCAN replaced HGETALL and SMEMBERS, which read a whole
		// hash or set with no bound.
		"HLEN", "HSCAN", "SCARD", "SSCAN", "ZCARD", "ZRANGE", "XLEN", "MEMORY USAGE", "OBJECT ENCODING",
		// XRANGE reads a stream's entries, always sent with a COUNT
		// (wire_test.go proves it on the socket).
		"XRANGE",
	}
	if len(readOnly) != len(want) {
		t.Errorf("the allowlist holds %d entries, want exactly %d", len(readOnly), len(want))
	}
	for _, name := range want {
		if !readOnly[name] {
			t.Errorf("the allowlist lost %q", name)
		}
	}
	for name := range readOnly {
		if !slices.Contains(want, name) {
			t.Errorf("the allowlist gained %q, which this test does not know: add it here only as a decision", name)
		}
	}
}

// The guard judges the bytes that are sent: a command is one bare ASCII word,
// upper-cased by ASCII rules only, so neither a two-word string handed over as
// one argument nor a Unicode look-alike reads as a command on the list.
func TestACommandIsOneASCIIWord(t *testing.T) {
	for _, args := range [][]string{
		{"MEMORY USAGE", "k"},
		{"MEMORY USAGE"},
		{"ſcan", "0"}, // U+017F, which strings.ToUpper maps to S
		{"GET\r\nSET", "k"},
		{" GET", "k"},
		{"", "k"},
	} {
		if err := allowed(args); err == nil {
			t.Errorf("%q was allowed through", args)
		}
	}
	if err := allowed([]string{"scan", "0", "COUNT", "500"}); err != nil {
		t.Errorf("a lower-case read was refused: %v", err)
	}
}
