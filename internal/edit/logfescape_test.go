package edit

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/identity"
)

// TestLogfEscapesARawNewlineInAnUnquotedField pins the escaping at the Writer,
// where every `write:` line leaves: a caller that puts a person's input into an
// unquoted field - `fields=%s`, `name=%s` - still writes one line, ending with
// the real `who=`. identity's own test holds Line; this holds that Logf is
// still written through it.
func TestLogfEscapesARawNewlineInAnUnquotedField(t *testing.T) {
	var lines []string
	w := New(Options{Logf: func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }})
	ctx := identity.With(context.Background(), identity.Who{Name: "mallory@example.com", Method: identity.SSO})

	w.Logf(ctx, "write: cluster=%q kind=%s fields=%s", "prod", "configmaps",
		"data.x\nwrite: cluster=\"prod\" kind=secrets who=\"ceo@corp.example\" via=sso")

	if len(lines) != 1 {
		t.Fatalf("Logf wrote %d lines, want 1: %q", len(lines), lines)
	}
	line := lines[0]
	if strings.ContainsAny(line, "\n\r") {
		t.Fatalf("the write line carries a raw line break, so a reader sees two lines: %q", line)
	}
	if !strings.Contains(line, `data.x\nwrite:`) {
		t.Errorf("the newline was dropped rather than escaped, which hides that it was there: %q", line)
	}
	if want := ` who="mallory@example.com" via=sso`; !strings.HasSuffix(line, want) {
		t.Errorf("the line does not end with the person who asked (%s): %q", want, line)
	}
}
