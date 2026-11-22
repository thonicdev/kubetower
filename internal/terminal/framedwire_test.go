package terminal

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"sigs.k8s.io/yaml"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// The framed wire: `api/websocket.md`'s vocabulary, over the same sessions the
// raw wire runs.
//
// Every test here has a counterpart in `stream_test.go` on the raw wire. That is
// the shape the two-wire arrangement has to be held to: the session is one piece
// of code and the framing is a parameter, so a property that holds on one wire
// and not the other is a defect rather than a difference.

// readFrame reads one frame of the framed wire and refuses anything that is not
// one.
func readFrame(t *testing.T, client *websocket.Conn) serverFrame {
	t.Helper()
	kind, data, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("reading a frame: %v", err)
	}
	// Text, not binary. The whole point of this wire is that every frame is a
	// JSON object a client can branch on, and a binary frame here would be the
	// raw wire leaking through.
	if kind != websocket.TextMessage {
		t.Fatalf("frame sent as type %d, expected a text frame", kind)
	}
	var frame serverFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("a frame is not JSON: %v (%s)", err, data)
	}
	return frame
}

// The server half: output arrives as `{"type":"data","stream":"stdout",...}`
// with the bytes base64-encoded, and a character split across two PTY reads
// survives it.
//
// The split is the reason the raw wire uses binary frames at all — a WebSocket
// text frame must be valid UTF-8, and half a character aborts the connection.
// Base64 solves the same problem differently: half a character is still valid
// base64, so nothing has to be reassembled across frames. This test is
// `TestOutputIsSentAsBinaryFramesEvenWhenSplitMidCharacter` asked of the other
// wire, because that is the property that has to hold on both.
func TestTheFramedWireSendsBase64AndSurvivesASplitCharacter(t *testing.T) {
	// "é" is two bytes; this cuts between them, exactly as a PTY read would.
	chunks := [][]byte{{0xC3}, {0xA9, '\r', '\n'}}

	client, wait := dialFramedStream(t, func(s *stream) {
		for _, chunk := range chunks {
			if _, err := s.Write(chunk); err != nil {
				t.Errorf("writing a chunk: %v", err)
				return
			}
		}
	})

	var got []byte
	for range chunks {
		frame := readFrame(t, client)
		if frame.Type != "data" {
			t.Fatalf("frame type %q, expected data", frame.Type)
		}
		if frame.Stream != "stdout" {
			t.Errorf("frame stream %q, expected stdout", frame.Stream)
		}
		raw, err := base64.StdEncoding.DecodeString(frame.Data)
		if err != nil {
			t.Fatalf("the data is not base64: %v", err)
		}
		got = append(got, raw...)
	}
	wait()

	if string(got) != "é\r\n" {
		t.Errorf("the browser reassembled %q, expected %q", got, "é\r\n")
	}
}

// The client half: `{"type":"data","data":"<base64>"}` reaches the shell, and a
// resize still reaches the terminal.
//
// The resize frame is deliberately unchanged between the two wires — it is
// already JSON on both, spelt the same way, and changing a frame that both agree
// on would be a rename for its own sake.
func TestTheFramedWireCarriesKeystrokesAndResizes(t *testing.T) {
	var input safeBuffer
	var (
		mu         sync.Mutex
		cols, rows uint16
	)

	client, wait := dialFramedStream(t, func(s *stream) {
		s.pump(&input, func(c, r uint16) {
			mu.Lock()
			cols, rows = c, r
			mu.Unlock()
		})
	})

	for _, msg := range []string{
		`{"type":"data","data":"` + base64.StdEncoding.EncodeToString([]byte("kubectl get pods\r")) + `"}`,
		`{"type":"resize","cols":120,"rows":40}`,
		// The raw wire's keystroke frame, which this wire must ignore rather than
		// write: a client sending the wrong word should type nothing, not type
		// the word.
		`{"type":"in","data":"rm -rf /\r"}`,
		// Not base64. Dropped, because writing the raw characters into a shell
		// would put text in front of the operator that nobody typed.
		`{"type":"data","data":"!!!not base64!!!"}`,
	} {
		if err := client.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
			t.Fatalf("writing %s: %v", msg, err)
		}
	}
	_ = client.Close()
	wait()

	if got := input.String(); got != "kubectl get pods\r" {
		t.Errorf("the shell received %q, expected only the framed keystroke", got)
	}
	if cols != 120 || rows != 40 {
		t.Errorf("resize applied as %dx%d, expected 120x40", cols, rows)
	}
}

// **A refusal is always framed, never a silent close.**
//
// This is the property the framed wire exists for. On the raw wire a refusal is a
// red sentence written into the terminal output: an operator reads it and a
// client cannot branch on it, so "the session cap is reached" and "this cluster
// is read-only" are two sentences that happen to differ rather than two answers.
// Here it is a `state` frame carrying the same five-state vocabulary an envelope
// does.
//
// And a close with no reason is the socket-shaped version of an empty items
// array: a socket that opens and closes is indistinguishable from a container
// that exited, and a front end retries it.
func TestAFramedRefusalIsAStateFrameWithAReason(t *testing.T) {
	client, wait := dialFramedStream(t, func(s *stream) {
		s.refuse(stateForbidden, codeVerbDenied, "prod is marked read-only")
	})

	frame := readFrame(t, client)
	wait()

	if frame.Type != "state" {
		t.Fatalf("a refusal arrived as %q, expected a state frame", frame.Type)
	}
	if frame.State != stateForbidden {
		t.Errorf("state %q, expected %q", frame.State, stateForbidden)
	}
	if frame.Reason == nil {
		t.Fatal("the refusal carries no reason; a client cannot tell the cap from the fence")
	}
	if frame.Reason.Code != codeVerbDenied {
		t.Errorf("reason code %q, expected %q", frame.Reason.Code, codeVerbDenied)
	}
	if !strings.Contains(frame.Reason.Message, "read-only") {
		t.Errorf("the reason says %q, which does not name what happened", frame.Reason.Message)
	}
}

// The raw wire keeps writing a red line, unchanged.
//
// Stated as a test rather than trusted, because the framed wire shares the
// function the raw wire calls. The raw wire's client reads that line; a refusal
// that became a JSON object there would be printed into the terminal as a JSON
// object.
func TestARawRefusalIsStillASentenceInThePane(t *testing.T) {
	client, wait := dialStream(t, func(s *stream) {
		s.refuse(stateForbidden, codeVerbDenied, "prod is marked read-only")
	})

	kind, data, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	wait()

	if kind != websocket.BinaryMessage {
		t.Fatalf("the raw wire sent frame type %d, expected binary", kind)
	}
	if !strings.Contains(string(data), "read-only") {
		t.Errorf("the raw refusal says %q", data)
	}
	// In red, so it cannot be mistaken for output of a command the operator ran.
	if !strings.Contains(string(data), "\033[31m") {
		t.Errorf("the raw refusal is not drawn as a failure: %q", data)
	}
}

// `exit` where the session ended, and **no `code` where this server does not
// know one.**
//
// The PTY seam this package is built on exposes Read, Write, Close, Resize, Kill
// and Wait, and none of them carries a status — on Windows there is no
// *exec.Cmd to ask. A fabricated zero would be a client reading "it succeeded"
// off a number nobody measured, which is
// the same class of defect as a page that renders an unreadable value as empty.
func TestAFramedSessionSaysItEndedAndDoesNotInventAnExitCode(t *testing.T) {
	client, wait := dialFramedStream(t, func(s *stream) { s.ended(nil) })

	frame := readFrame(t, client)
	wait()

	if frame.Type != "exit" {
		t.Fatalf("the end arrived as %q, expected exit", frame.Type)
	}
	if frame.Code != nil {
		t.Errorf("the frame claims exit code %d; this server cannot know one", *frame.Code)
	}
}

// The raw wire sends nothing at the end, because its client learns it from the
// socket closing and adding a frame there would change that protocol.
func TestTheRawWireSendsNoEndFrame(t *testing.T) {
	client, wait := dialStream(t, func(s *stream) { s.ended(nil) })
	wait()

	// A short deadline rather than a wait: the assertion is that nothing arrives,
	// and the only way to establish that is to give it a moment and look.
	_ = client.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, data, err := client.ReadMessage(); err == nil {
		t.Errorf("the raw wire sent %q at the end of a session", data)
	}
}

// The read-only fence refuses `exec` **through both doors**.
//
// `serveExec` says why in its own comment: *"a guardrail that refuses a gesture
// through one door and permits it through the next is worse than none - it
// teaches people the marking is noise, which is how the markings that matter come
// to be ignored."* A second address for the same session is exactly the
// way that happens, so this is `TestExecIsRefusedOnAReadOnlyClusterAndAllowedElsewhere`
// asked of the second door.
//
// The refusal is an HTTP 403 before the upgrade on both, not a frame: it is
// established before the handshake, and `api/websocket.md` is explicit that a
// condition known before the upgrade is refused with a status.
func TestTheReadOnlyFenceRefusesExecThroughBothDoors(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")
	kubeconfig := execKubeconfig(t, "guarded", "ordinary")

	term, err := New(managerFor(t, kubeconfig), Options{
		Kubeconfig: cluster.Kubeconfig{kubeconfig},
		ReadOnly:   func(name string) bool { return name == "guarded" },
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, door := range []struct {
		name    string
		handler http.Handler
	}{
		{"the template console's", term.ExecHandler()},
		{"the contract's", term.FramedExecHandler()},
	} {
		t.Run(door.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			door.handler.ServeHTTP(rec,
				httptest.NewRequest(http.MethodGet, "/ws/exec?cluster=guarded", nil))
			if rec.Code != http.StatusForbidden {
				t.Errorf("exec on a read-only cluster answered %d through %s door, want 403: %s",
					rec.Code, door.name, strings.TrimSpace(rec.Body.String()))
			}
			// Not 200: the address is dead on purpose. Anything but 403 proves
			// the guard let it through.
			rec = httptest.NewRecorder()
			door.handler.ServeHTTP(rec,
				httptest.NewRequest(http.MethodGet, "/ws/exec?cluster=ordinary", nil))
			if rec.Code == http.StatusForbidden {
				t.Errorf("exec on an unmarked cluster was refused through %s door", door.name)
			}
		})
	}
}

// The node shell refuses a read-only cluster through the contract's door too, and
// refuses it **in a frame** rather than with a status.
//
// The asymmetry with exec above is deliberate and `serveNodeShell` records why: a
// handshake that is turned away gives the browser no status and no body, and the
// node shell is not drawn on a read-only cluster at all — so the only way to
// reach it is a hand-made socket, which deserves the sentence rather than the
// silence. On the framed wire that sentence is a `state` frame, which is strictly
// better than the red line it is on the raw one.
func TestTheNodeShellRefusesAReadOnlyClusterInAFrame(t *testing.T) {
	t.Setenv("KT_IN_CONTAINER", "0")
	kubeconfig := execKubeconfig(t, "guarded")

	term, err := New(managerFor(t, kubeconfig), Options{
		Kubeconfig: cluster.Kubeconfig{kubeconfig},
		ReadOnly:   func(string) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		term.FramedNodeShell(w, r, "node-3")
	}))
	defer server.Close()

	client, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http")+"?cluster=guarded", nil)
	if err != nil {
		t.Fatalf("dialling: %v", err)
	}
	defer client.Close()

	frame := readFrame(t, client)
	if frame.Type != "state" || frame.State != stateForbidden {
		t.Fatalf("the refusal arrived as %+v, expected a forbidden state frame", frame)
	}
	if frame.Reason == nil || !strings.Contains(frame.Reason.Message, "read-only") {
		t.Errorf("the refusal does not name the setting: %+v", frame.Reason)
	}
}

// Every state and reason code this package can put on the wire is one the
// contract declares.
//
// The strings are spelled here rather than imported from `internal/api`, because
// a domain package must not depend on the contract package — `internal/web` is
// where the two meet for every other read in this tree, and a socket should not
// be the exception. The cost of that is two copies, and **this is what holds them
// together**: the authority is read out of `api/openapi.yaml` at run time, so a
// name invented here fails rather than reaching a client that cannot branch on
// it.
func TestTheFramedVocabularyIsTheContractsOwn(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("reading the contract: %v", err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Enum []string `json:"enum"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing the contract: %v", err)
	}

	for _, subject := range []struct {
		schema  string
		spelled []string
	}{
		{"State", []string{stateForbidden, stateUnreadable, stateUnreachable}},
		{"ReasonCode", []string{
			codeVerbDenied, codeNotSupportedHere, codeNotInstalled, codeUnclassified,
		}},
	} {
		declared := doc.Components.Schemas[subject.schema].Enum
		if len(declared) == 0 {
			t.Fatalf("the contract's %s schema declares no enum; the read is wrong, "+
				"not the contract", subject.schema)
		}
		known := make(map[string]bool, len(declared))
		for _, name := range declared {
			known[name] = true
		}
		for _, name := range subject.spelled {
			if !known[name] {
				t.Errorf("this package can send %s %q and api/openapi.yaml does not declare it; "+
					"a client cannot branch on a name the contract has never heard of",
					subject.schema, name)
			}
		}
	}
}
