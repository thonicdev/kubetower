package terminal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A Claude tab that comes back after a reload gets its conversation
// back. The decision is `chooseConversation`, kept apart from the PTY so every
// branch is a table row here rather than a process.

const someID = "3f2504e0-4f89-11d3-9a0c-0305e82c3301"

func TestASessionIdIsAUUIDOrNothing(t *testing.T) {
	for _, ok := range []string{"", someID, "00000000-0000-0000-0000-000000000000"} {
		if !validSession(ok) {
			t.Errorf("%q refused, want accepted", ok)
		}
	}
	for _, bad := range []string{
		"3F2504E0-4F89-11D3-9A0C-0305E82C3301", // the browser writes lower-case, and one spelling is one file
		"3f2504e0-4f89-11d3-9a0c-0305e82c330",  // short
		"../../etc/passwd",                     // the id is a file name under the CLI's config
		"3f2504e0-4f89-11d3-9a0c-0305e82c3301\n",
		"latest",
	} {
		if validSession(bad) {
			t.Errorf("%q accepted, want refused", bad)
		}
	}
}

// The slug is the CLI's, not ours, and it is observed rather than derived: a
// working directory of `C:\Users\me\Documents\Tech-Code\GitHub` is kept under
// `.claude/projects/C--Users-me-Documents-Tech-Code-GitHub/`.
func TestTheProjectSlugIsTheCLIs(t *testing.T) {
	for _, c := range []struct{ dir, want string }{
		{`C:\Users\me\Documents\Tech-Code\GitHub`, "C--Users-me-Documents-Tech-Code-GitHub"},
		{"/var/lib/kubetower/claude-workspace/s03", "-var-lib-kubetower-claude-workspace-s03"},
		{"/tmp/a b.c_d", "-tmp-a-b-c-d"},
	} {
		if got := claudeProjectSlug(c.dir); got != c.want {
			t.Errorf("slug(%q) = %q, want %q", c.dir, got, c.want)
		}
	}
	got := claudeTranscriptPath("/home/kt/.claude", "/var/lib/kubetower/claude-workspace/s03", someID)
	want := filepath.Join("/home/kt/.claude", "projects", "-var-lib-kubetower-claude-workspace-s03", someID+".jsonl")
	if got != want {
		t.Errorf("transcript path = %q, want %q", got, want)
	}
}

// transcriptIn writes an empty transcript for id as the CLI would have, for a
// session that ran in workspace, under configDir.
func transcriptIn(t *testing.T, configDir, workspace, id string) {
	t.Helper()
	path := claudeTranscriptPath(configDir, workspace, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("making %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// The trap: conversations live per working directory and the
// directories are reused, so the transcript has to be found across every slot
// and not looked for in whichever slot happens to be free.
func TestTheTranscriptIsFoundInWhicheverWorkspaceHeldIt(t *testing.T) {
	term := claudeTerminal(t, 4)
	configDir := t.TempDir()
	workspaces := term.claudeWorkspaces()
	third := workspaces[3]
	transcriptIn(t, configDir, third, someID)

	dir, found := findClaudeTranscript(configDir, someID, workspaces)
	if !found || dir != third {
		t.Fatalf("found=%t in %q, want the transcript in %q", found, dir, third)
	}
	if _, found := findClaudeTranscript(configDir, "3f2504e0-4f89-11d3-9a0c-0305e82c3302", workspaces); found {
		t.Error("an id with no transcript was reported found")
	}
	if _, found := findClaudeTranscript("", someID, workspaces); found {
		t.Error("with no config directory there is nowhere to look, and yet something was found")
	}
}

func TestEachBranchOfTheDecision(t *testing.T) {
	term := claudeTerminal(t, 3)
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	workspaces := term.claudeWorkspaces()
	parent, slot1, slot2 := workspaces[0], workspaces[1], workspaces[2]

	// No id: no flag reaches the launcher.
	if got := term.chooseConversation(""); got != (conversation{}) {
		t.Errorf("no id gave %+v, want nothing at all", got)
	}

	// An id nobody has a transcript for is a first open: the id is the
	// session's, and the slot is whichever is free.
	if got := term.chooseConversation(someID); got != (conversation{session: someID}) {
		t.Errorf("a first open gave %+v, want --session-id and no workspace", got)
	}

	// The transcript in the shared parent: resumed there, nothing taken.
	transcriptIn(t, configDir, parent, someID)
	if got := term.chooseConversation(someID); got != (conversation{workspace: parent, resume: someID}) {
		t.Errorf("a conversation in the parent gave %+v, want it resumed there", got)
	}
	if live := term.claudeSlots.live(); live != 0 {
		t.Errorf("%d slots are out after a resume in the parent, want 0", live)
	}

	// The transcript in a free slot: that slot is taken, and it is resumed.
	other := "3f2504e0-4f89-11d3-9a0c-0305e82c3302"
	transcriptIn(t, configDir, slot2, other)
	got := term.chooseConversation(other)
	if got != (conversation{workspace: slot2, resume: other}) {
		t.Fatalf("a conversation in a free slot gave %+v, want it resumed in %s", got, slot2)
	}
	if live := term.claudeSlots.live(); live != 1 {
		t.Errorf("%d slots are out after a resume in a slot, want 1", live)
	}

	// The same transcript while its slot is out to another live session: still
	// resumed, in whichever slot is free. The CLI finds a transcript across
	// project directories - `claude --resume <id>` from s03 resumes one kept
	// under s02's slug - so refusing here would refuse a resume the CLI can
	// make. No workspace is chosen, so the
	// session takes a free slot as a first open does.
	again := term.chooseConversation(other)
	if again != (conversation{resume: other}) {
		t.Fatalf("a conversation whose slot is out gave %+v, want it resumed in any free slot", again)
	}
	// And the busy slot was not handed out a second time: the decision took
	// nothing, and the slot it found the transcript in stays the other
	// session's.
	if live := term.claudeSlots.live(); live != 1 {
		t.Errorf("%d slots are out, want the same 1", live)
	}

	// The second live session keeps its own notes. The first is framed in
	// slot2, the resumed one is handed a slot of its own - not slot2 - and its
	// notes are written there, as newClaudeSession does.
	if err := term.writeClaudeContext(claudeView{
		Cluster: "prod", Kind: "Pods", Namespace: "payments", Name: "api-live",
	}, slot2); err != nil {
		t.Fatalf("framing the live session: %v", err)
	}
	resumedIn := term.claudeSlots.take()
	if resumedIn == "" || resumedIn == slot2 {
		t.Fatalf("the resumed session was handed %q, want a free slot other than %s", resumedIn, slot2)
	}
	if resumedIn != slot1 {
		t.Errorf("the resumed session was handed %q, want the one free slot %s", resumedIn, slot1)
	}
	if err := term.writeClaudeContext(claudeView{
		Cluster: "prod", Kind: "Pods", Namespace: "payments", Name: "api-resumed",
	}, resumedIn); err != nil {
		t.Fatalf("framing the resumed session: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(slot2, claudeContextName))
	if err != nil {
		t.Fatalf("no notes in %s: %v", slot2, err)
	}
	if notes := string(raw); !strings.Contains(notes, "api-live") || strings.Contains(notes, "api-resumed") {
		t.Errorf("the live session's notes in %s were rewritten by the resumed one:\n%s", slot2, notes)
	}
}

// Once a conversation has been resumed in another slot, two workspaces may
// hold a transcript under its id: whether the CLI goes on writing to the file
// it resumed from or starts one under the new directory's slug is not
// measured here. So the lookup takes the one written last, which is the
// conversation as it stands, and never an older copy that happens to come
// first in the slot order.
func TestTheNewestTranscriptWinsWhenTwoWorkspacesHoldOne(t *testing.T) {
	term := claudeTerminal(t, 3)
	configDir := t.TempDir()
	workspaces := term.claudeWorkspaces()
	slot1, slot2 := workspaces[1], workspaces[2]

	transcriptIn(t, configDir, slot1, someID)
	transcriptIn(t, configDir, slot2, someID)
	older := time.Now().Add(-time.Hour)
	if err := os.Chtimes(claudeTranscriptPath(configDir, slot1, someID), older, older); err != nil {
		t.Fatalf("aging the first transcript: %v", err)
	}
	if dir, found := findClaudeTranscript(configDir, someID, workspaces); !found || dir != slot2 {
		t.Errorf("found=%t in %q, want the newer transcript in %s", found, dir, slot2)
	}

	// And the other way round, so the answer is the time and not the order.
	if err := os.Chtimes(claudeTranscriptPath(configDir, slot2, someID), older.Add(-time.Hour), older.Add(-time.Hour)); err != nil {
		t.Fatalf("aging the second transcript: %v", err)
	}
	if dir, found := findClaudeTranscript(configDir, someID, workspaces); !found || dir != slot1 {
		t.Errorf("found=%t in %q, want the newer transcript in %s", found, dir, slot1)
	}
}

func TestTakeThisHandsOutOnlyAFreeSlot(t *testing.T) {
	term := claudeTerminal(t, 2)
	slot1 := term.claudeWorkspaces()[1]
	if !term.claudeSlots.takeThis(slot1) {
		t.Fatal("a free slot was refused")
	}
	if term.claudeSlots.takeThis(slot1) {
		t.Error("a slot that is out was handed out again, which is two sessions in one workspace")
	}
	if term.claudeSlots.takeThis(term.claudeWorkspace()) {
		t.Error("the parent is not a slot and must never be taken")
	}
	if term.claudeSlots.takeThis(filepath.Join(term.claudeWorkspace(), "nope")) {
		t.Error("a directory that is not a workspace was handed out")
	}
	term.claudeSlots.give(slot1)
	if !term.claudeSlots.takeThis(slot1) {
		t.Error("the freed slot was refused")
	}
}

// Through a real shell, like the launcher's other tests: the two flags are
// single-valued, sit between the mode and the variadic flag, and an empty
// variable leaves no argument behind.
func TestTheLauncherCarriesTheConversationBetweenTheModeAndTheVariadicFlag(t *testing.T) {
	args, _ := runLauncher(t, map[string]string{
		"KT_CLAUDE_MODE":    "auto",
		"KT_CLAUDE_RESUME":  someID,
		"KT_CLAUDE_ALLOWED": "Bash(kubectl get:*)",
	})
	want := []string{"--permission-mode", "auto", "--resume", someID, "--allowedTools", "Bash(kubectl get:*)"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("with a resume the launcher assembled %#v, want %#v", args, want)
	}

	args, _ = runLauncher(t, map[string]string{
		"KT_CLAUDE_MODE":    "auto",
		"KT_CLAUDE_SESSION": someID,
	})
	want = []string{"--permission-mode", "auto", "--session-id", someID}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("with a first open the launcher assembled %#v, want %#v", args, want)
	}

	args, _ = runLauncher(t, map[string]string{"KT_CLAUDE_MODE": "auto"})
	for _, a := range args {
		if a == "--resume" || a == "--session-id" {
			t.Errorf("with no conversation the launcher still passed %s: %#v", a, args)
		}
	}
}

// The PowerShell launcher is not run here - there is no `pwsh` on the runner
// and the shape is asserted the way its siblings are, on the text: the same two
// variables, the same order, one value each.
func TestThePowerShellLauncherCarriesTheSameTwoFlags(t *testing.T) {
	for _, line := range []string{
		`if ($env:KT_CLAUDE_RESUME) { $kt += '--resume'; $kt += $env:KT_CLAUDE_RESUME }`,
		`if ($env:KT_CLAUDE_SESSION) { $kt += '--session-id'; $kt += $env:KT_CLAUDE_SESSION }`,
	} {
		if !strings.Contains(claudeLauncherPowerShell, line) {
			t.Errorf("the PowerShell launcher lacks:\n%s", line)
		}
	}
	mode := strings.Index(claudeLauncherPowerShell, "--permission-mode")
	resume := strings.Index(claudeLauncherPowerShell, "--resume")
	allowed := strings.Index(claudeLauncherPowerShell, "--allowedTools")
	if !(mode < resume && resume < allowed) {
		t.Error("the conversation flags are not between the mode and the variadic flag")
	}
}
