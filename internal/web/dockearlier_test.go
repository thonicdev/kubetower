package web

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
)

// The other half of `internal/api/logpagemembers_test.go`, and the half that can
// actually be wrong.
//
// `internal/api`'s test drives a **fake** dock, so it proves the contract type
// can carry `stop` and `note` and that the handler emits what it is given. It
// cannot prove this adapter fills them from `logs.Streamer.Earlier`, because
// `internal/web` imports `internal/api` and not the other way round. A member
// declared in the contract, emitted by nothing and branched on by a client whose
// generated types say it is required falls into exactly that gap.
//
// The subject here is the **mapping**, not the paging. `internal/logs/earlier.go`
// has eleven test functions of its own and they are the parity spec for the
// window arithmetic; nothing below re-tests it. What is tested is that the three
// stop reasons survive the crossing with their sentences, and that "there is
// nothing earlier" arrives as an **answer** rather than as a failure — which is
// the distinction the whole envelope exists for.
func TestThePageBackwardsIsFilledFromTheStreamerThatPagesIt(t *testing.T) {
	// Three hundred lines against a window of 200 + 500, so the tail asks for
	// more than exists and the read reaches the oldest line the kubelet holds.
	// That is the `beginning` case, and it is the one a panel renders as an end.
	lines := make([]string, 0, 300)
	for i := range 300 {
		lines = append(lines, fmt.Sprintf("line %03d", i))
	}
	dock := apiDock{&Server{logs: logFixture(t, lines)}}

	page, failure := dock.EarlierLogs(context.Background(), api.LogQuery{
		ObjectRef: api.ObjectRef{Cluster: "prod", Namespace: "default", Name: "api-7f9c4b8d6-x2kqp"},
		TailLines: 200,
	}, 200, 500)

	// **"There is nothing earlier" is not a failure.** A Failure here would make
	// the panel draw `unreachable` for a container whose log it had read to the
	// end, which is the collapse this contract exists to remove, inverted.
	if failure != nil {
		t.Fatalf("paging back: %+v — reaching the beginning is an answer, not a failure", failure)
	}

	// 300 lines exist and the caller holds the last 200, so 100 are earlier.
	if len(page.Lines) != 100 {
		t.Fatalf("%d lines, want 100 — the engine's arithmetic, not this mapper's", len(page.Lines))
	}
	if page.Lines[0] != "line 000" {
		t.Errorf("the page starts at %q, want the oldest line; a page delivered newest-first "+
			"reads as the container having printed backwards", page.Lines[0])
	}

	// --- The two members that need a source ---------------

	if page.Stop != api.LogStopBeginning {
		t.Errorf("stop is %q, want %q — without it a panel cannot tell 'there is nothing older' "+
			"from 'there is more and this console will not fetch it'", page.Stop, api.LogStopBeginning)
	}
	if page.Note == "" {
		t.Fatal("the page carries no note; the sentence is written server-side so that curl and " +
			"the console say the same words, and a client composing its own is a second wording")
	}
	// The sentence is the engine's and is careful about something nobody would
	// think of on their own. Asserting it here rather than in `internal/logs`
	// because this is the crossing where a mapper could substitute its own.
	if !containsText(page.Note, "kubelet") {
		t.Errorf("the beginning note is %q and does not mention the kubelet; it must never say "+
			"'the container started here', because the kubelet rotates and the oldest line the "+
			"API returns is not the first line the process wrote", page.Note)
	}

	// --- Both directions, against the contract rather than against this file --
	declared := schemaProperties(t, "LogPage")
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("marshalling a page: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(encoded, &sent); err != nil {
		t.Fatalf("re-reading a page: %v", err)
	}
	for name := range declared {
		if _, ok := sent[name]; !ok {
			t.Errorf("api/openapi.yaml declares LogPage.%s and the mapper never fills it; "+
				"an absent optional member conforms, so nothing else in this tree would say so", name)
		}
	}
	for name := range sent {
		if _, ok := declared[name]; !ok {
			t.Errorf("the mapper sends LogPage.%s and api/openapi.yaml does not declare it", name)
		}
	}
}

// A page in the middle of a log has more behind it, so it names no stop — and
// that absence is a third answer rather than a missing one.
//
// This is the case a lazy implementation gets wrong in the safe-looking
// direction: always answering `beginning` makes the panel stop offering to page
// back after the very first gesture, and the operator sees a log that claims to
// have no history.
func TestAPageWithMoreBehindItNamesNoStop(t *testing.T) {
	// A thousand lines against a window of 100 + 200. The window fills, so there
	// is more behind it.
	lines := make([]string, 0, 1000)
	for i := range 1000 {
		lines = append(lines, fmt.Sprintf("line %04d", i))
	}
	dock := apiDock{&Server{logs: logFixture(t, lines)}}

	page, failure := dock.EarlierLogs(context.Background(), api.LogQuery{
		ObjectRef: api.ObjectRef{Cluster: "prod", Namespace: "default", Name: "api-7f9c4b8d6-x2kqp"},
	}, 100, 200)
	if failure != nil {
		t.Fatalf("paging back: %+v", failure)
	}
	if len(page.Lines) != 200 {
		t.Fatalf("%d lines, want a full page of 200", len(page.Lines))
	}
	if page.Stop != "" {
		t.Errorf("stop is %q on a page that has 700 lines behind it; a panel reading this stops "+
			"offering to page back after one gesture", page.Stop)
	}
	if page.Note != "" {
		t.Errorf("note is %q on a page with no stop; the sentence belongs to an ending", page.Note)
	}
}

// The rewind ceiling is refused **before** anything is opened against a cluster,
// and it comes back with a sentence telling the operator what to do instead.
//
// It is a different answer from `beginning` and the difference is the point:
// there *is* more log, and this console will not fetch it. A panel that rendered
// the two identically would tell an operator a log has no history when what
// happened is that the console stopped looking.
func TestTheRewindCeilingIsAnAnswerAndSaysWhatToDoInstead(t *testing.T) {
	dock := apiDock{&Server{logs: logFixture(t, []string{"never read"})}}

	page, failure := dock.EarlierLogs(context.Background(), api.LogQuery{
		ObjectRef: api.ObjectRef{Cluster: "prod", Namespace: "default", Name: "api-7f9c4b8d6-x2kqp"},
	}, 20000, 500)
	if failure != nil {
		t.Fatalf("the ceiling is an answer, not a failure: %+v", failure)
	}
	if page.Stop != api.LogStopCeiling {
		t.Fatalf("stop is %q, want %q", page.Stop, api.LogStopCeiling)
	}
	if len(page.Lines) != 0 {
		t.Errorf("%d lines past the ceiling; nothing was read and nothing may be delivered", len(page.Lines))
	}
	if !containsText(page.Note, "kubectl") {
		t.Errorf("the ceiling note is %q and does not say what to do instead; a refusal with no "+
			"way forward is a dead end", page.Note)
	}
}

func containsText(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
