package terminal

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// A Claude tab that comes back after a reload gets its conversation back.
//
// A reload kills the PTY and the `claude` in it, and the conversation is the
// one thing a fresh session does not restore. Claude Code can resume one by id
// (`--resume <uuid>`) and be told its id up front (`--session-id <uuid>`); this
// file is the console's use of that.
//
// The browser draws the id and the server decides what it means. The dock
// stores a UUID with the tab and sends it as the channel's `session` query. The
// browser cannot know whether the process it started wrote a transcript, and the
// CLI refuses to resume one that has none, so the server looks for the
// transcript and answers `--resume` or `--session-id`.
//
// Claude Code keeps conversations per project directory
// (`<config>/projects/<slug>/<id>.jsonl`, the slug being the working directory
// with every non-alphanumeric character replaced by `-`), and sessions here run
// in reused workspace slots (claude.go). So the transcript is looked up across
// every workspace, and the session opens in the one holding it when that one is
// free.
//
// When that slot is out to another live session, the conversation is resumed
// from a free slot instead: the CLI looks across its project directories itself.
// Two live sessions still never share a workspace.

// sessionIDRE is the shape `crypto.randomUUID()` produces and the CLI accepts.
// Lower case only: the browser writes it that way, and one spelling is one
// transcript name.
var sessionIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// validSession reports whether s can name a conversation. The empty string,
// "no conversation", is valid.
func validSession(s string) bool {
	return s == "" || sessionIDRE.MatchString(s)
}

// claudeProjectSlug is how Claude Code names a working directory under
// `projects/`: every rune that is not an ASCII letter or digit becomes `-`, so
// `C:\Users\me\...` is stored as `C--Users-me-...`.
func claudeProjectSlug(dir string) string {
	var b strings.Builder
	b.Grow(len(dir))
	for _, r := range dir {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// claudeTranscriptPath is where the CLI keeps the conversation `id` held in
// `workspace`, under `configDir`.
func claudeTranscriptPath(configDir, workspace, id string) string {
	return filepath.Join(configDir, "projects", claudeProjectSlug(workspace), id+".jsonl")
}

// findClaudeTranscript looks for the conversation across every workspace this
// console opens sessions in (the shared parent and each slot) and names the one
// that holds it.
//
// The newest wins, not the first: a conversation resumed from another slot may
// leave a copy under a second slug (whether the CLI does so is not measured),
// and the older copy would bring back the conversation as it stood before. Equal
// times keep the first, so a single transcript is found in slot order.
func findClaudeTranscript(configDir, id string, workspaces []string) (workspace string, found bool) {
	if configDir == "" || id == "" {
		return "", false
	}
	var newest time.Time
	for _, dir := range workspaces {
		info, err := os.Stat(claudeTranscriptPath(configDir, dir, id))
		if err != nil || info.IsDir() {
			continue
		}
		if !found || info.ModTime().After(newest) {
			workspace, newest, found = dir, info.ModTime(), true
		}
	}
	return workspace, found
}

// takeThis hands out one named workspace, if it is a free slot. The shared
// parent is not a slot and is never taken: a resume living there runs there,
// shared. A slot that is out is refused, and the caller resumes elsewhere.
func (s *claudeSlots) takeThis(dir string) bool {
	if s == nil || dir == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, candidate := range s.dirs {
		if candidate != dir {
			continue
		}
		if s.used[i] {
			return false
		}
		s.used[i] = true
		return true
	}
	return false
}

// conversation is what newClaudeSession decided about the id it was handed.
type conversation struct {
	// workspace is where the session runs, when the conversation chose it; ""
	// means "take any free slot".
	workspace string
	// resume is the id handed to `--resume`, and session the id handed to
	// `--session-id`. At most one is set.
	resume  string
	session string
}

// chooseConversation is the decision, kept apart from the PTY so it can be
// tested without one:
//
//   - no id: a session with no conversation;
//   - an id with no transcript anywhere: a first open, `--session-id`, in
//     whatever slot is free;
//   - an id whose transcript is in the shared parent: resumed there;
//   - an id whose transcript is in a free slot: that slot is taken, resumed;
//   - an id whose transcript is in a slot that is out: resumed in whichever
//     slot is free, since the CLI finds the transcript from any of them; the
//     live session keeps its slot and its notes.
func (t *Terminal) chooseConversation(id string) conversation {
	if id == "" {
		return conversation{}
	}
	dir, found := findClaudeTranscript(claudeConfigDir(), id, t.claudeWorkspaces())
	if !found {
		return conversation{session: id}
	}
	if dir == t.claudeWorkspace() {
		return conversation{workspace: dir, resume: id}
	}
	if t.claudeSlots.takeThis(dir) {
		return conversation{workspace: dir, resume: id}
	}
	// Logged because this is the one branch where the conversation and the
	// session's directory differ, the first thing to check if a resume comes back
	// empty.
	t.logf("claude: conversation %s is held in %s, which another session is using; resuming it in a free workspace", id, dir)
	return conversation{resume: id}
}
