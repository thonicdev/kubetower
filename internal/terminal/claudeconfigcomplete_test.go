package terminal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A workspace entry that already exists is completed rather than skipped.
//
// An entry carrying only
//
//	{"hasTrustDialogAccepted": true}
//
// and not `hasCompletedProjectOnboarding` must get both: a guard that writes
// them only when the workspace key is absent would never finish the entry.
// Claude Code's trust dialog defaults to **"No, exit"**, so an operator
// pressing Enter there quits.
//
// **What is not claimed here.** Whether the missing key is what puts the dialog
// on screen is a fact about a third-party binary; this pins the guard on its
// own terms.
func TestAHalfWrittenWorkspaceEntryIsCompletedRatherThanSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")

	// An entry written by something else, plus a key that is Claude Code's own
	// and must survive: a project entry holds session history, and a change that
	// threw that away would be a worse defect than the dialog it fixes.
	before := map[string]any{
		"projects": map[string]any{
			"C:/ws/s01": map[string]any{
				"hasTrustDialogAccepted": true,
				"history":                []any{"a session nobody wants deleted"},
			},
		},
		"somethingElseEntirely": "left alone",
	}
	raw, err := json.Marshal(before)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	prepareClaudeConfig(path, dir, []string{"C:/ws/s01", "C:/ws/s02"}, "test", func(string, ...any) {})

	after := map[string]any{}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if err := json.Unmarshal(written, &after); err != nil {
		t.Fatalf("the file is no longer JSON: %v", err)
	}

	projects, _ := after["projects"].(map[string]any)
	if projects == nil {
		t.Fatalf("projects went missing: %s", written)
	}

	existing, _ := projects["C:/ws/s01"].(map[string]any)
	if existing == nil {
		t.Fatalf("the existing workspace went missing: %s", written)
	}
	if done, _ := existing["hasCompletedProjectOnboarding"].(bool); !done {
		t.Errorf("the half-written entry was skipped rather than completed: %v — "+
			"the key that was missing is the one the guard was waiting for", existing)
	}
	if trusted, _ := existing["hasTrustDialogAccepted"].(bool); !trusted {
		t.Errorf("the trust record was lost while completing the entry: %v", existing)
	}
	if _, kept := existing["history"]; !kept {
		t.Errorf("the entry was replaced rather than merged into, so Claude Code's own "+
			"state went with it: %v", existing)
	}

	// The absent workspace still gets both, which keeps working.
	fresh, _ := projects["C:/ws/s02"].(map[string]any)
	if fresh == nil {
		t.Fatalf("the new workspace was not written: %s", written)
	}
	for _, key := range []string{"hasTrustDialogAccepted", "hasCompletedProjectOnboarding"} {
		if set, _ := fresh[key].(bool); !set {
			t.Errorf("a fresh workspace lacks %s: %v", key, fresh)
		}
	}

	if after["somethingElseEntirely"] != "left alone" {
		t.Errorf("the rest of the operator's config did not survive: %s", written)
	}
}

// Nothing is written when there is nothing to change.
//
// The write rewrites the operator's own `~/.claude.json`, and the comment on
// that write says why doing it more often than necessary is the wrong trade.
// Ensuring two keys rather than skipping an entry must not turn every start-up
// into a rewrite of that file.
func TestACompleteConfigIsLeftUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")

	raw, err := json.Marshal(map[string]any{
		"projects": map[string]any{
			"C:/ws/s01": map[string]any{
				"hasTrustDialogAccepted":        true,
				"hasCompletedProjectOnboarding": true,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	stamp, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	prepareClaudeConfig(path, dir, []string{"C:/ws/s01"}, "test", func(string, ...any) {})

	again, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !again.ModTime().Equal(stamp.ModTime()) {
		t.Errorf("a config that needed no change was rewritten anyway")
	}
}
