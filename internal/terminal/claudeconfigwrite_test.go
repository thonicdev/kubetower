package terminal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The config this writes is the operator's own.
//
// `claudeConfigFile` is the real `~/.claude.json`, holding their projects and
// their settings, of which KubeTower owns three keys. A bare `os.WriteFile` —
// truncate, then write — killed in between would leave that file unparseable,
// and Claude Code will not start.

// TestTheClaudeConfigIsReplacedAtomically.
//
// Three assertions, and the middle one is the point: nothing partial is ever at
// the destination path, and no debris is left beside it.
func TestTheClaudeConfigIsReplacedAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")

	original := []byte(`{"projects":{"/work":{"history":["a","b"]}},"theme":"dark"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	replacement, err := json.MarshalIndent(map[string]any{
		"projects":               map[string]any{"/work": map[string]any{"history": []string{"a", "b"}}},
		"theme":                  "dark",
		"hasCompletedOnboarding": true,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	if err := writeConfigAtomically(path, replacement); err != nil {
		t.Fatalf("writeConfigAtomically: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("what was written is not parseable JSON, which is the failure this exists to "+
			"avoid: %v", err)
	}
	if parsed["hasCompletedOnboarding"] != true {
		t.Errorf("the change did not land: %v", parsed)
	}
	if parsed["theme"] != "dark" {
		t.Errorf("the operator's own keys were lost: %v", parsed)
	}

	// No debris in the operator's home. The temp file is created beside the
	// target so the rename stays on one filesystem, which means a failure to
	// clean up is a stray file in a directory that is not ours.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".kubetower-claude-") {
			t.Errorf("a temporary file was left in the operator's home: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("the directory holds %d entries, want just the config", len(entries))
	}
}

// TestAFailedConfigWriteLeavesTheOriginalIntact. The half that matters more than
// the success path: a truncate-then-write cannot offer this at all.
func TestAFailedConfigWriteLeavesTheOriginalIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	original := []byte(`{"theme":"dark"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	// A target that cannot be renamed over: a directory of that name is the
	// portable way to make Rename fail on both Windows and Unix.
	blocked := filepath.Join(dir, "blocked.json")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if err := writeConfigAtomically(blocked, []byte(`{"x":1}`)); err == nil {
		t.Error("writing over a directory succeeded, so the error path is untested")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("the untouched config changed: %q", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".kubetower-claude-") {
			t.Errorf("a failed write left a temporary file behind: %s", e.Name())
		}
	}
}

// TestTheConfigWriteIsNotABareWriteFile pins the shape, because the tidy-up that
// reverses this reads as a simplification: one call instead of eight.
func TestTheConfigWriteIsNotABareWriteFile(t *testing.T) {
	source, err := os.ReadFile("claude.go")
	if err != nil {
		t.Fatalf("reading claude.go: %v", err)
	}
	text := string(source)
	if strings.Contains(text, "os.WriteFile(path, raw") {
		t.Error("the config is written with os.WriteFile again: truncate-then-write over the " +
			"operator's own ~/.claude.json, which a kill in between leaves unparseable")
	}
	if !strings.Contains(text, "tmp.Sync()") {
		t.Error("the temp file is not synced before the rename; rename-atomic is not durable, " +
			"and a crash then leaves a zero-length config")
	}
}
