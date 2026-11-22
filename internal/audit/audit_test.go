package audit_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/identity"
)

// lines decodes every record on out, failing on a line that is not one JSON
// object - which is the property a collector relies on.
func lines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var records []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("a line on the sink is not a JSON object: %v\n%s", err, scanner.Text())
		}
		records = append(records, record)
	}
	return records
}

func alice() context.Context {
	return identity.With(context.Background(), identity.Who{Name: "alice@example.com", Method: identity.SSO})
}

func deletion(ctx context.Context) audit.Record {
	r := audit.Record{Act: audit.Write, Cluster: "prod", Verb: "delete", Version: "v1", Resource: "pods",
		Namespace: "shop", Name: "api-1", Decision: "allowed"}
	r.Attribute(ctx)
	return r
}

// An act is two lines, attempt then outcome, each a whole record, sharing one
// id - and a second act has another.
func TestAnActIsAnAttemptAndAnOutcomeSharingAnID(t *testing.T) {
	var out bytes.Buffer
	log := audit.New(&out)

	first, err := log.Begin(deletion(alice()))
	if err != nil {
		t.Fatal(err)
	}
	if got := lines(t, out.String()); len(got) != 1 || got[0]["phase"] != audit.Attempt {
		t.Fatalf("Begin did not write the attempt alone: %v", got)
	}
	if err := first.End(audit.Applied, ""); err != nil {
		t.Fatal(err)
	}
	second, _ := log.Begin(deletion(alice()))
	_ = second.End(audit.Failed, "Conflict")

	got := lines(t, out.String())
	if len(got) != 4 {
		t.Fatalf("two acts wrote %d records, want 4:\n%s", len(got), out.String())
	}
	if got[0]["id"] == "" || got[0]["id"] != got[1]["id"] || got[2]["id"] != got[3]["id"] || got[0]["id"] == got[2]["id"] {
		t.Errorf("the ids do not join each act's two records and only them: %v %v %v %v",
			got[0]["id"], got[1]["id"], got[2]["id"], got[3]["id"])
	}
	if first.ID() != got[0]["id"] {
		t.Errorf("the handle's id %q is not the records' %v", first.ID(), got[0]["id"])
	}
	for i, want := range []struct{ phase, outcome, reason string }{
		{audit.Attempt, "", ""}, {audit.Outcome, audit.Applied, ""},
		{audit.Attempt, "", ""}, {audit.Outcome, audit.Failed, "Conflict"},
	} {
		record := got[i]
		outcome, _ := record["outcome"].(string)
		reason, _ := record["reason"].(string)
		if record["phase"] != want.phase || outcome != want.outcome || reason != want.reason {
			t.Errorf("record %d is %v, want phase %s outcome %q reason %q", i, record, want.phase, want.outcome, want.reason)
		}
		// Every record stands alone: who, what, where and the decision on
		// both halves, so a reader of the outcome need not find the attempt.
		for field, value := range map[string]any{"act": "write", "cluster": "prod", "verb": "delete",
			"resource": "pods", "namespace": "shop", "name": "api-1", "decision": "allowed",
			"who": "alice@example.com", "via": "sso"} {
			if record[field] != value {
				t.Errorf("record %d has %s=%v, want %v", i, field, record[field], value)
			}
		}
		if _, ok := record["time"].(string); !ok {
			t.Errorf("record %d has no time: %v", i, record)
		}
	}
}

// A context with nobody in it says so, rather than leaving the fields out.
func TestAnActNobodyIsBehindSaysUnattributed(t *testing.T) {
	var out bytes.Buffer
	_, _ = audit.New(&out).Begin(deletion(context.Background()))
	got := lines(t, out.String())
	if got[0]["who"] != "" || got[0]["via"] != "unattributed" {
		t.Errorf("an act nobody was behind reads who=%v via=%v", got[0]["who"], got[0]["via"])
	}
}

// A name a person chooses cannot end a record or forge the next one, and
// cannot reorder what a terminal draws.
func TestANameCannotBreakARecord(t *testing.T) {
	var out bytes.Buffer
	ctx := identity.With(context.Background(), identity.Who{Name: "eve\n{\"who\":\"ceo\"}‮", Method: identity.SSO})
	r := deletion(ctx)
	r.Name = "api-1\r\n ⁦x"
	pending, err := audit.New(&out).Begin(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = pending.End(audit.Applied, "")

	if n := strings.Count(out.String(), "\n"); n != 2 {
		t.Fatalf("two records took %d lines:\n%s", n, out.String())
	}
	if strings.ContainsAny(out.String(), "‮⁦ \r") {
		t.Errorf("a control that reorders or breaks a line reached the sink raw:\n%q", out.String())
	}
	got := lines(t, out.String())
	if got[0]["who"] != "eve\n{\"who\":\"ceo\"}‮" || got[0]["name"] != "api-1\r\n ⁦x" {
		t.Errorf("the values did not decode to what was sent: who=%q name=%q", got[0]["who"], got[0]["name"])
	}
}

type failing struct{ err error }

func (f failing) Write([]byte) (int, error) { return 0, f.err }

type short struct{}

func (short) Write(p []byte) (int, error) { return len(p) / 2, nil }

// A sink that fails, or takes part of a line, is a failed Begin: the caller
// refuses the act.
func TestAFailedOrShortWriteIsAnError(t *testing.T) {
	full := errors.New("no space left on device")
	if _, err := audit.New(failing{full}).Begin(deletion(alice())); !errors.Is(err, full) {
		t.Errorf("a failing sink answered %v", err)
	}
	if _, err := audit.New(short{}).Begin(deletion(alice())); !errors.Is(err, audit.ErrShortWrite) {
		t.Errorf("a short write answered %v", err)
	}
}

// A nil Log is the desktop's: it records nothing and refuses nothing.
func TestANilLogWritesNothingAndNeverFails(t *testing.T) {
	var log *audit.Log
	pending, err := log.Begin(deletion(alice()))
	if err != nil || pending == nil {
		t.Fatalf("a nil log answered %v, %v", pending, err)
	}
	if err := pending.End(audit.Applied, ""); err != nil || pending.ID() != "" {
		t.Errorf("a nil log's handle answered %v, id %q", err, pending.ID())
	}
}

// An act the console makes for itself names the console, and the person
// whose request caused it as onBehalfOf - never as who.
func TestAConsoleActNamesTheConsoleAndThePersonItWasFor(t *testing.T) {
	var out bytes.Buffer
	r := deletion(context.Background())
	r.AttributeToConsole(alice())
	_, _ = audit.New(&out).Begin(r)
	got := lines(t, out.String())
	if got[0]["who"] != "" || got[0]["via"] != audit.Console || got[0]["onBehalfOf"] != "alice@example.com" {
		t.Errorf("a console act reads who=%v via=%v onBehalfOf=%v", got[0]["who"], got[0]["via"], got[0]["onBehalfOf"])
	}
}

// A field a person chose is cut at its bound and says so, so one request
// cannot write a record a container runtime splits into partial lines.
func TestAFieldIsCutAtItsBound(t *testing.T) {
	var out bytes.Buffer
	ctx := identity.With(context.Background(), identity.Who{Name: strings.Repeat("é", 4000), Method: identity.SSO})
	r := deletion(ctx)
	r.Name = strings.Repeat("a", 1<<20)
	r.Key = strings.Repeat("k", 300)
	_, _ = audit.New(&out).Begin(r)

	if n := out.Len(); n > 4096 {
		t.Fatalf("a record with a megabyte name is %d bytes", n)
	}
	got := lines(t, out.String())
	name, _ := got[0]["name"].(string)
	if !strings.HasPrefix(name, strings.Repeat("a", 253)+"…[+") || !strings.HasSuffix(name, " bytes]") {
		t.Errorf("the name was not cut at 253 with a marker: %q…", name[:min(len(name), 300)])
	}
	who, _ := got[0]["who"].(string)
	if !strings.Contains(who, "…[+") || !utf8.ValidString(who) {
		t.Errorf("the user name was not cut on a rune boundary with a marker")
	}
}

type stalled struct{ release chan struct{} }

func (s stalled) Write(p []byte) (int, error) { <-s.release; return len(p), nil }

// A sink that stops taking records is a failed write after the bound, not an
// act that waits for ever - and the log refuses every record after it.
func TestAStalledSinkFailsWithinItsBoundAndBreaksTheLog(t *testing.T) {
	sink := stalled{release: make(chan struct{})}
	defer close(sink.release)
	log := audit.NewWithTimeout(sink, 50*time.Millisecond)
	start := time.Now()
	if _, err := log.Begin(deletion(alice())); !errors.Is(err, audit.ErrStalled) {
		t.Fatalf("a stalled sink answered %v", err)
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Errorf("the write waited %s", waited)
	}
	if _, err := log.Begin(deletion(alice())); !errors.Is(err, audit.ErrBroken) || log.Broken() == nil {
		t.Errorf("after a stall the log wrote again: %v", err)
	}
}

type halfThenWhole struct {
	buf   bytes.Buffer
	calls int
}

func (h *halfThenWhole) Write(p []byte) (int, error) {
	h.calls++
	if h.calls == 1 {
		h.buf.Write(p[:len(p)/2])
		return len(p) / 2, nil
	}
	return h.buf.Write(p)
}

// After a short write the log stops: the fragment it left has no newline, and
// a record written after it would be glued to it.
func TestAShortWriteBreaksTheLogRatherThanGluingTheNextRecord(t *testing.T) {
	sink := &halfThenWhole{}
	log := audit.New(sink)
	if _, err := log.Begin(deletion(alice())); !errors.Is(err, audit.ErrShortWrite) {
		t.Fatalf("a short write answered %v", err)
	}
	if _, err := log.Begin(deletion(alice())); !errors.Is(err, audit.ErrBroken) {
		t.Fatalf("the log wrote after a short write: %v", err)
	}
	if sink.calls != 1 {
		t.Errorf("the sink was written %d times; nothing may follow a fragment", sink.calls)
	}
}

// The C1 controls and the implicit direction marks, which encoding/json leaves
// raw, are escaped like the bidirectional controls.
func TestC1ControlsAndDirectionMarksAreEscaped(t *testing.T) {
	var out bytes.Buffer
	r := deletion(alice())
	r.Name = "a\u0085b\u009b31mc‎d‏e؜f"
	_, _ = audit.New(&out).Begin(r)
	if strings.ContainsAny(out.String(), "\u0085\u009b‎‏؜") {
		t.Errorf("a C1 control or a direction mark reached the sink raw: %q", out.String())
	}
	if got := lines(t, out.String()); got[0]["name"] != r.Name {
		t.Errorf("the name decoded to %q, want %q", got[0]["name"], r.Name)
	}
}
