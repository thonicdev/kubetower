package terminal

// The wire between a browser and something that behaves like a terminal. A
// local shell and `exec` into a pod share it, so the UI has one terminal
// component and a framing fix applies to both.
//
//	client to server  JSON: keyboard input, or a resize
//	server to client  the raw terminal output, in binary frames

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// sameOrigin rejects WebSocket upgrades coming from another site.
//
// Otherwise any page the operator visits could open a socket to the terminal
// carrying the session cookie (cross-site WebSocket hijacking). SameSite=Lax
// already blocks it, but that is one defense, with semantics that vary between
// browsers, in front of a root-equivalent shell.
//
// A missing Origin is accepted: browsers always send one on a WebSocket
// handshake, so its absence means a non-browser client, which a hostile page
// cannot drive and which still needs a valid session.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	// A reverse proxy in front forwards the original Host header, so the two
	// compare directly.
	return strings.EqualFold(u.Host, r.Host)
}

// clientMessage is what the browser sends.
type clientMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// wire is the frame vocabulary a stream speaks. The two halves of the dock
// speak different wires, so the session code is one and the framing a
// parameter, with the differences reviewable in one file.
type wire int

const (
	// wireRaw is the raw wire: binary frames from the server, because a PTY read
	// can stop mid-character and a text frame must be valid UTF-8; JSON from the
	// client, because keystrokes and resizes need a discriminator.
	// TestOutputIsSentAsBinaryFramesEvenWhenSplitMidCharacter pins it.
	wireRaw wire = iota
	// wireFramed is api/websocket.md's: JSON both ways with the bytes
	// base64-encoded (half a character is still valid base64). It costs a third
	// more traffic and a JSON parse per frame, and buys a refusal that is always
	// framed: a `state` frame in the envelope's vocabulary that a client can branch
	// on, where the raw wire only has a red line in the output.
	wireFramed
)

// The state vocabulary a framed refusal carries, and the reason codes this
// package produces.
//
// Spelled here rather than imported from internal/api: that package is the
// contract and must not learn about client-go, PTYs or kubeconfigs, and a
// domain package importing it would invert that (internal/web is where the two
// meet). A test reads `api/errors.md`'s own list and asserts this package
// produces only names in it.
const (
	stateForbidden   = "forbidden"
	stateUnreadable  = "unreadable"
	stateUnreachable = "unreachable"
	// stateAbsent is a thing asked for that is not there: a debug session's pod,
	// or its target container.
	stateAbsent = "absent"

	// codeVerbDenied is this console refusing, rather than the cluster: the
	// read-only fence, or a profile with no local shell.
	codeVerbDenied = "verb_denied"
	// codeNotSupportedHere is the ceiling: the gesture is fine, and this process
	// will not do it now.
	codeNotSupportedHere = "not_supported_here"
	// codeNotInstalled is a capability this build does not carry at all, as
	// opposed to one it declines.
	codeNotInstalled = "not_installed"
	// codeUnclassified is anything that went wrong opening a session where this
	// package knows the sentence and not the category.
	codeUnclassified = "unclassified"
	// codeNoSuchObject goes with stateAbsent.
	codeNoSuchObject = "no_such_object"
	// codeContainerNotStarted is a debug container that will not start on its
	// own: an image that cannot be pulled, or a container that ended.
	codeContainerNotStarted = "container_not_started"
	// codeTimeout is a debug container still not running at the wait's bound.
	codeTimeout = "timeout"
	// codeNoShell is a pod shell whose program the container runtime could not
	// find in the image (notInImage): a distroless or scratch image, where the
	// remedy is a container that has one (a debug container), not a retry.
	codeNoShell = "no_shell"
	// codePodSecurity is a debug container the API server refused because the
	// namespace's Pod Security level forbids it, which no RBAC grant changes.
	codePodSecurity = "pod_security"
	// codeAdmissionDenied is a debug container an admission webhook refused: a
	// policy engine's decision, in the namespace or cluster, not RBAC.
	codeAdmissionDenied = "admission_denied"
)

// serverFrame is what the framed wire sends: one type for every frame kind, so
// the discriminator is in one place.
type serverFrame struct {
	Type   string        `json:"type"`
	Stream string        `json:"stream,omitempty"`
	Data   string        `json:"data,omitempty"`
	State  string        `json:"state,omitempty"`
	Reason *framedReason `json:"reason,omitempty"`
	// Code is the process's exit status where this server knows it, and absent
	// where it does not, rather than a fabricated 0. The PTY seam (pty.go) carries
	// no status (on Windows there is no *exec.Cmd to ask). A pod exec and a node
	// shell are the exception: the API server reports a non-zero status
	// (client-go's CodeExitError). A zero is absent there too, since client-go
	// returns neither error nor number for it.
	Code *int `json:"code,omitempty"`
}

type framedReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// stream is the socket plus the write lock gorilla/websocket lacks: it allows
// one reader and one writer at a time, and output, the skew warning and notices
// come from different goroutines.
type stream struct {
	conn    *websocket.Conn
	writeMu sync.Mutex

	// wire is the frame vocabulary this socket speaks. Read-only after
	// construction, so keepAlive and copyFrom may read it.
	wire wire

	// Per stream so a test can shorten them without shared state; read-only after
	// newStream, so keepAlive may read them.
	pongWait   time.Duration
	pingPeriod time.Duration
	writeWait  time.Duration

	// gone is closed once this socket is finished with, whoever noticed first:
	// pump's read failing, an undeliverable ping or write, or a caller closing it.
	// Sessions wait on it because pump, which notices by reading, does not read
	// while parked in a write to a stdin nobody drains; otherwise a session could
	// hold its slot for good.
	gone      chan struct{}
	closeOnce sync.Once

	// heard counts what the person sent (keystrokes, pastes, resizes) for the
	// idle bound (watch.go). Pongs are not in it: the browser answers them on its
	// own.
	heard atomic.Uint64
}

// inputs is how many frames from the person pump has taken so far.
func (s *stream) inputs() uint64 { return s.heard.Load() }

// maxFrame is the largest message a terminal socket accepts from a browser.
//
// gorilla/websocket has no default limit, ReadMessage buffers the whole
// message, and pump base64-decodes a second copy, so one huge frame could OOM
// the process, which under the server profile is everyone's console. A megabyte
// is about 750 KB of pasted text on the framed wire; the front end splits longer
// pastes (web/ui/src/api/termsocket.ts). Past it gorilla answers 1009 from the
// frame header, before buffering the payload.
const maxFrame = 1 << 20

// pongWait is how long a socket may say nothing before it is treated as gone,
// and pingPeriod how often we make it say something.
//
// A terminal is idle most of the time, so silence means nothing alone; an idle
// peer still answers a ping, since the browser's WebSocket stack replies without
// our JavaScript, while a suspended laptop does not. pingPeriod leaves room for
// one lost ping under pongWait.
//
// Constants copied into each stream rather than package vars a test could
// shorten, which would race with a still-running keepAlive.
const (
	pongWait   = 60 * time.Second
	pingPeriod = 25 * time.Second
	writeWait  = 10 * time.Second
)

// newStream wraps the socket and starts the keepalive.
//
// Without the read deadline, ReadMessage blocks for ever when a peer vanishes
// without closing (a suspended laptop, a dropped Wi-Fi association, an expired
// NAT entry), and since the upgrade hijacked the connection, r.Context() is
// never canceled either. That would leak a live shell and its session
// kubeconfig, one of the session slots in limit.go, and for a node shell the
// privileged pod, whose activeDeadlineSeconds is meant to be the backstop, not
// the mechanism.
//
// The write deadline in Write is the other half: a gone peer whose kernel still
// accepts bytes fills a TCP window and then blocks the writer for ever.
func newStream(conn *websocket.Conn, speaks wire) *stream {
	return newTimedStream(conn, speaks, pongWait, pingPeriod, writeWait)
}

// newTimedStream is newStream with the timings as parameters, for the test
// that watches a peer stop answering.
func newTimedStream(conn *websocket.Conn, speaks wire, pong, ping, write time.Duration) *stream {
	s := &stream{conn: conn, wire: speaks, pongWait: pong, pingPeriod: ping, writeWait: write,
		gone: make(chan struct{})}

	conn.SetReadLimit(maxFrame)
	_ = conn.SetReadDeadline(time.Now().Add(s.pongWait))
	// Every pong pushes the deadline out. Set before any read starts, because
	// gorilla dispatches control frames from inside ReadMessage.
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(s.pongWait))
	})

	go s.keepAlive()
	return s
}

// keepAlive pings until the socket stops accepting them, then closes it.
//
// An undeliverable ping ends the session, not only this loop: pump's read
// deadline only works while pump reads, and a pump parked in a write to an
// undrained stdin never does. Closing fires gone, which every session waits on.
//
// It stops as soon as the stream closes, leaving no goroutine for a ping
// period.
func (s *stream) keepAlive() {
	ticker := time.NewTicker(s.pingPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-s.gone:
			return
		case <-ticker.C:
		}
		s.writeMu.Lock()
		_ = s.conn.SetWriteDeadline(time.Now().Add(s.writeWait))
		err := s.conn.WriteMessage(websocket.PingMessage, nil)
		s.writeMu.Unlock()
		if err != nil {
			s.close()
			return
		}
	}
}

// Write sends terminal output to the browser, in binary frames: a PTY read can
// stop mid-character, and a text frame must be valid UTF-8 or the connection
// aborts. The browser reassembles the bytes and xterm.js decodes them.
func (s *stream) Write(p []byte) (int, error) {
	if s.wire == wireFramed {
		// Base64 inside a JSON text frame: half a character is still valid base64, so
		// nothing needs reassembling.
		if err := s.send(serverFrame{
			Type:   "data",
			Stream: "stdout",
			Data:   base64.StdEncoding.EncodeToString(p),
		}); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	s.writeMu.Lock()
	// Bounded, for the reason on newStream: a gone peer whose kernel still buffers
	// would block this call for ever once its window closes.
	_ = s.conn.SetWriteDeadline(time.Now().Add(s.writeWait))
	err := s.conn.WriteMessage(websocket.BinaryMessage, p)
	s.writeMu.Unlock()
	if err != nil {
		// gorilla leaves a connection unusable after a failed write, so the browser
		// is gone as far as this session can know; tell whoever waits on gone.
		s.close()
		return 0, err
	}
	return len(p), nil
}

// notice writes a line of our own into the terminal, in yellow so it is not
// mistaken for a command's output.
func (s *stream) notice(text string) {
	_, _ = s.Write([]byte("\r\n\033[33m" + text + "\033[0m\r\n"))
}

// fail writes a line of our own in red, for what stops a session opening,
// which would otherwise look like a terminal that connected and did nothing.
func (s *stream) fail(text string) {
	_, _ = s.Write([]byte("\r\n\033[31m" + text + "\033[0m\r\n"))
}

// send writes one frame of the framed wire, under the same lock and the same
// deadline as everything else this socket writes.
func (s *stream) send(frame serverFrame) error {
	body, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(s.writeWait))
	err = s.conn.WriteMessage(websocket.TextMessage, body)
	s.writeMu.Unlock()
	if err != nil {
		// As in Write: an undeliverable frame means the browser has gone.
		s.close()
	}
	return err
}

// refuse says no, in whichever vocabulary this socket speaks. On the raw wire
// it is a red sentence a client cannot tell from command output; on the framed
// wire a `state` frame with the envelope's vocabulary, so a pane can draw the
// session cap and a read-only cluster differently, and a refusal is never a
// silent close. Both arms write something.
func (s *stream) refuse(state, code, message string) {
	if s.wire == wireFramed {
		_ = s.send(serverFrame{
			Type:   "state",
			State:  state,
			Reason: &framedReason{Code: code, Message: message},
		})
		return
	}
	s.fail(message)
}

// ended says the far end finished, and must be said before the socket closes,
// by whoever closes it: after relay or runExec returned, their own goroutine has
// already closed the socket, and the browser would see a bare close and report
// a lost connection after every ordinary `exit`.
//
// code is the far end's exit status where reported (a pod exec or a node shell)
// and nil where nobody knows (every PTY session; see serverFrame.Code). A nil is
// never written as 0.
//
// On the raw wire an ordinary end is the socket closing; a reported status is
// a red line in client-go's words, as that wire has no frame for a number.
func (s *stream) ended(code *int) {
	if s.wire != wireFramed {
		if code != nil {
			s.fail(fmt.Sprintf("exec ended: command terminated with exit code %d", *code))
		}
		return
	}
	_ = s.send(serverFrame{Type: "exit", Code: code})
}

// farewellWait bounds the goodbye a closing console writes to each socket.
const farewellWait = time.Second

// farewell tells the browser why its session is ending, in whichever wire it
// speaks, and closes the socket within farewellWait whatever the socket is
// doing. refuse waits for the write lock, which a write to a browser that
// stopped reading holds for up to writeWait, then writes for up to writeWait
// again; sixteen such sockets would make a shutdown or sign-out wait minutes.
// So the connection's deadline is pulled in first, and the socket is closed at
// the bound whether or not the goodbye was written.
func (s *stream) farewell(state, code, message string) {
	if c := s.conn.UnderlyingConn(); c != nil {
		_ = c.SetWriteDeadline(time.Now().Add(farewellWait))
	}
	said := make(chan struct{})
	go func() {
		defer close(said)
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		select {
		case <-s.gone:
			return
		default:
		}
		kind, body := websocket.BinaryMessage, []byte("\r\n\033[31m"+message+"\033[0m\r\n")
		if s.wire == wireFramed {
			framed, err := json.Marshal(serverFrame{Type: "state", State: state,
				Reason: &framedReason{Code: code, Message: message}})
			if err != nil {
				return
			}
			kind, body = websocket.TextMessage, framed
		}
		_ = s.conn.SetWriteDeadline(time.Now().Add(farewellWait))
		_ = s.conn.WriteMessage(kind, body)
	}()
	select {
	case <-said:
	case <-time.After(farewellWait):
	}
	s.close()
}

// close ends the socket and tells every session waiting on gone. Safe from any
// goroutine, any number of times.
func (s *stream) close() {
	s.closeOnce.Do(func() {
		_ = s.conn.Close()
		close(s.gone)
	})
}

// closed is what a session waits on to learn the browser side has finished,
// whatever its own goroutines are doing. See gone.
func (s *stream) closed() <-chan struct{} { return s.gone }

// copyFrom relays terminal output until the source ends. Not io.Copy, so the
// buffer size is ours: it bounds how much of a `kubectl logs -f` torrent goes in
// one frame, which the browser decodes before painting.
func (s *stream) copyFrom(src io.Reader) error {
	buf := make([]byte, 8192)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := s.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			return err
		}
	}
}

// pump reads client messages until the socket closes, writing keystrokes to
// dst and handing resizes to resize, which may be nil.
//
// It returns when the socket fails: the browser going away, or the connection
// closed because the far end finished; neither is an error. A frame past
// maxFrame is such a failure (gorilla answers 1009).
//
// It can be parked in dst.Write when what is behind dst does not read (a pod's
// stdin pipe, a full PTY), where it cannot see the browser leave. The session's
// goroutine waiting on closed() must then unblock dst for pump to return.
func (s *stream) pump(dst io.Writer, resize func(cols, rows uint16)) {
	// The read deadline is re-armed where reading starts. Only a read moves it,
	// since gorilla handles pongs inside ReadMessage. A node shell can spend up to
	// nodeShellStartTimeout before anything reads, and past pongWait the first read
	// would fail on a stale deadline, ending the session at its first keystroke. A
	// browser that really left meanwhile is still caught by keepAlive's ping.
	_ = s.conn.SetReadDeadline(time.Now().Add(s.pongWait))
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			s.close()
			return
		}

		var msg clientMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			// A frame we cannot parse is dropped, not fatal: a message from a newer UI
			// must not end the session.
			continue
		}

		switch msg.Type {
		case "in", "data", "resize":
			s.heard.Add(1)
		}
		switch msg.Type {
		case "in":
			// The raw wire's keystroke frame: the text as it was typed.
			if s.wire != wireRaw {
				continue
			}
			if _, err := io.WriteString(dst, msg.Data); err != nil {
				return
			}
		case "data":
			// The framed wire's, base64-encoded. Invalid base64 is dropped, never written
			// raw, which would put text in the shell that nobody typed.
			if s.wire != wireFramed {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(msg.Data)
			if err != nil {
				continue
			}
			if _, err := dst.Write(raw); err != nil {
				return
			}
		case "resize":
			// Zero in either dimension is what a browser sends for a hidden tab; applied,
			// it would give a 0x0 terminal and lose everything drawn while hidden.
			if resize != nil && msg.Cols > 0 && msg.Rows > 0 {
				resize(msg.Cols, msg.Rows)
			}
		}
	}
}
