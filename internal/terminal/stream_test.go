package terminal

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// --- cross-site WebSocket protection -----------------------------------------

func TestSameOriginAcceptsMatchingOrigin(t *testing.T) {
	r := &http.Request{Host: "127.0.0.1:5823", Header: http.Header{}}
	r.Header.Set("Origin", "http://127.0.0.1:5823")
	if !sameOrigin(r) {
		t.Error("a same-origin handshake was rejected")
	}
}

func TestSameOriginRejectsForeignOrigin(t *testing.T) {
	// The attack this guards against: a page the operator visits opens a socket
	// to the terminal, and the browser attaches the session cookie.
	for _, origin := range []string{
		"http://evil.example",
		"https://evil.example",
		"http://127.0.0.1:9999",              // right host, wrong port
		"http://localhost:5823",              // a different host, even if it resolves alike
		"http://127.0.0.1:5823.evil.example", // a suffix that looks like the real one
		"::not a url",
	} {
		r := &http.Request{Host: "127.0.0.1:5823", Header: http.Header{}}
		r.Header.Set("Origin", origin)
		if sameOrigin(r) {
			t.Errorf("origin %q was accepted", origin)
		}
	}
}

func TestSameOriginAcceptsAbsentOrigin(t *testing.T) {
	// Browsers always send Origin on a WebSocket handshake, so an absent header
	// means a non-browser client, which no hostile page can drive. It still
	// needs a valid session to reach this point.
	r := &http.Request{Host: "127.0.0.1:5823", Header: http.Header{}}
	if !sameOrigin(r) {
		t.Error("a client without an Origin header was rejected")
	}
}

// --- the wire ----------------------------------------------------------------

// dialStream stands a server up, hands the server side of one socket to serve,
// and returns the browser side plus a function that waits for serve to finish.
func dialStream(t *testing.T, serve func(*stream)) (*websocket.Conn, func()) {
	t.Helper()
	return dialTimedStream(t, wireRaw, pongWait, pingPeriod, writeWait, serve)
}

// dialFramedStream is dialStream speaking api/websocket.md's vocabulary instead
// of the raw wire. Both wires run over the same session code, so every test
// that has one has to be able to have the other.
func dialFramedStream(t *testing.T, serve func(*stream)) (*websocket.Conn, func()) {
	t.Helper()
	return dialTimedStream(t, wireFramed, pongWait, pingPeriod, writeWait, serve)
}

// dialTimedStream is dialStream with the keep-alive timings named, for the two
// tests that have to watch one expire. Passed down to the stream rather than set
// on the package: with package vars, a test's restore would write one while a
// live keepAlive read it.
func dialTimedStream(t *testing.T, speaks wire, pong, ping, write time.Duration, serve func(*stream)) (*websocket.Conn, func()) {
	t.Helper()

	served := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: sameOrigin}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(served)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		defer conn.Close()
		serve(newTimedStream(conn, speaks, pong, ping, write))
	}))
	t.Cleanup(srv.Close)

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dialling the test server: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	wait := func() {
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Fatal("the server side never finished")
		}
	}
	return client, wait
}

// safeBuffer, because pump writes from the server goroutine and the test reads
// from its own.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestPumpDeliversKeystrokesAndResizes(t *testing.T) {
	var input safeBuffer
	var (
		mu         sync.Mutex
		cols, rows uint16
	)

	client, wait := dialStream(t, func(s *stream) {
		s.pump(&input, func(c, r uint16) {
			mu.Lock()
			cols, rows = c, r
			mu.Unlock()
		})
	})

	for _, msg := range []string{
		`{"type":"in","data":"kubectl get pods\r"}`,
		`{"type":"resize","cols":120,"rows":40}`,
		`not json at all`,                     // must be dropped, not fatal
		`{"type":"resize","cols":0,"rows":0}`, // a hidden tab; must be ignored
		`{"type":"something-new","data":"x"}`, // a newer UI; must be ignored
		`{"type":"in","data":"exit\r"}`,
	} {
		if err := client.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
			t.Fatalf("writing %s: %v", msg, err)
		}
	}
	_ = client.Close()
	wait()

	if got, want := input.String(), "kubectl get pods\rexit\r"; got != want {
		t.Errorf("the shell received %q, expected %q", got, want)
	}
	mu.Lock()
	defer mu.Unlock()
	// 120x40 and not 0x0: a zero-dimension resize would give a 0x0 terminal and
	// everything drawn while the tab was hidden would be lost.
	if cols != 120 || rows != 40 {
		t.Errorf("resize applied as %dx%d, expected 120x40", cols, rows)
	}
}

// A PTY read can stop halfway through a multi-byte character. A text frame must
// be valid UTF-8, so a split character would abort the connection instead of
// being reassembled by the browser.
func TestOutputIsSentAsBinaryFramesEvenWhenSplitMidCharacter(t *testing.T) {
	// "é" is two bytes; this cuts between them, exactly as a PTY read would.
	chunks := [][]byte{{0xC3}, {0xA9, '\r', '\n'}}

	client, wait := dialStream(t, func(s *stream) {
		for _, chunk := range chunks {
			if _, err := s.Write(chunk); err != nil {
				t.Errorf("writing a chunk: %v", err)
				return
			}
		}
	})

	var got []byte
	for range chunks {
		kind, data, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("reading a frame: %v", err)
		}
		if kind != websocket.BinaryMessage {
			t.Fatalf("frame sent as type %d, expected a binary frame", kind)
		}
		got = append(got, data...)
	}
	wait()

	if string(got) != "é\r\n" {
		t.Errorf("the browser reassembled %q, expected %q", got, "é\r\n")
	}
}

func TestCopyFromRelaysUntilTheSourceEnds(t *testing.T) {
	client, wait := dialStream(t, func(s *stream) {
		_ = s.copyFrom(strings.NewReader("total 0\r\n"))
	})

	_, data, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("reading the relayed output: %v", err)
	}
	wait()

	if string(data) != "total 0\r\n" {
		t.Errorf("relayed %q", data)
	}
}

func TestNoticeAndFailAreDistinguishableFromCommandOutput(t *testing.T) {
	client, wait := dialStream(t, func(s *stream) {
		s.notice("kubectl is two minors ahead")
		s.fail("cannot open a session")
	})

	var lines []string
	for range 2 {
		_, data, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("reading: %v", err)
		}
		lines = append(lines, string(data))
	}
	wait()

	if !strings.Contains(lines[0], "\033[33m") {
		t.Errorf("a notice was not sent in yellow: %q", lines[0])
	}
	if !strings.Contains(lines[1], "\033[31m") {
		t.Errorf("a failure was not sent in red: %q", lines[1])
	}
}

// Concurrent writers are the normal case — terminal output and the skew warning
// come from different goroutines — and gorilla/websocket tolerates exactly one
// writer at a time. Run with -race, this fails without the lock.
func TestConcurrentWritesAreSerialised(t *testing.T) {
	const writers, each = 8, 20

	client, wait := dialStream(t, func(s *stream) {
		var wg sync.WaitGroup
		for range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range each {
					_, _ = s.Write([]byte("x"))
				}
			}()
		}
		wg.Wait()
	})

	received := 0
	for received < writers*each {
		_, _, err := client.ReadMessage()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("after %d frames: %v", received, err)
		}
		received++
	}
	wait()

	if received != writers*each {
		t.Errorf("received %d frames, expected %d", received, writers*each)
	}
}

// --- the prompt ---------------------------------------------------------------

func TestPromptShortensAnARNAndMarksReadOnly(t *testing.T) {
	const arn = "arn:aws:eks:eu-west-1:123456789012:cluster/example-prod-eks"

	writable := buildPrompt(arn, false)
	if !strings.Contains(writable, "[example-prod-eks]") {
		t.Errorf("the prompt did not shorten the ARN: %q", writable)
	}
	if strings.Contains(writable, "arn:aws") {
		t.Errorf("the prompt kept the full ARN, which is unreadable: %q", writable)
	}

	readOnly := buildPrompt(arn, true)
	if !strings.Contains(readOnly, "[example-prod-eks ro]") {
		t.Errorf("a read-only cluster was not marked in the prompt: %q", readOnly)
	}
	if !strings.Contains(readOnly, "\\033[33m") {
		t.Errorf("a read-only prompt was not yellow: %q", readOnly)
	}
}

// A peer that vanishes *without* closing.
//
// A suspended laptop, a dropped Wi-Fi association and an expiring NAT entry all
// leave the socket open as far as this process is concerned — there is no FIN in
// any of them — so without a deadline ReadMessage blocks forever and pump never
// returns. And because the upgrade hijacks the connection, r.Context() never
// fires either, so nothing else notices. What would stay behind: a live shell, its session kubeconfig,
// one of sixteen session slots, and for a node shell a hostPID/hostIPC/
// hostNetwork pod with the node's root filesystem mounted at /host.
//
// Simulated by taking away the client's pong. gorilla answers pings by default,
// which is exactly what a live-but-idle browser does; replacing that handler with
// one that says nothing is a peer that is gone but whose socket is not.
func TestAPeerThatStopsAnsweringEndsTheSession(t *testing.T) {
	returned := make(chan struct{})
	client, wait := dialTimedStream(t, wireRaw, 150*time.Millisecond, 40*time.Millisecond, 150*time.Millisecond, func(s *stream) {
		s.pump(io.Discard, nil)
		close(returned)
	})

	// The peer is still there at the TCP level and answers nothing above it.
	client.SetPingHandler(func(string) error { return nil })
	// Something has to drive the client's read loop for it to see frames at all;
	// this one deliberately never replies.
	go func() {
		for {
			if _, _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("pump never returned: a peer that stopped answering held the session open, " +
			"and with it the shell, its kubeconfig, a session slot and any node-shell pod")
	}
	wait()
}

// The other direction, and the one that must not regress: an idle peer is not an
// absent one. A shell sitting at a prompt sends nothing for as long as the
// operator is reading, and the only thing distinguishing it from a dead socket is
// that the browser's own WebSocket stack answers pings without any JavaScript.
//
// **The read deadline runs on a clock the test moves** (readClock below), and
// the clock moves only once a pong has pushed the deadline out. On the wall
// clock - a 150ms pongWait, a 40ms ping - a machine that does not schedule a
// ping, its pong or the read for 150ms breaks it, which a CPU-throttled
// container does routinely. Driven by pongs, a stalled scheduler makes this
// test slower and never makes it fail.
//
// The second half keeps the first honest: the same clock, moved past a
// pongWait once the peer stops answering, does end the session. A clock that
// was not wired in would otherwise pass the first half by never firing.
func TestAnIdlePeerThatStillAnswersIsNotDropped(t *testing.T) {
	returned := make(chan struct{})
	client, clock := dialClockedStream(t, func(s *stream) {
		s.pump(io.Discard, nil)
		close(returned)
	})

	// What gorilla's default handler does, which is what a browser does, plus
	// a switch so the same peer can stop answering in the second half.
	var answering atomic.Bool
	answering.Store(true)
	client.SetPingHandler(func(data string) error {
		if answering.Load() {
			_ = client.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(time.Second))
		}
		return nil
	})
	go func() {
		for {
			if _, _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// Six steps of three quarters of a pongWait: four and a half pongWaits of
	// saying nothing at all. Each step waits for a pong to have pushed the
	// deadline since the last one; the first wait is met by the deadline the
	// stream starts with.
	for step := range 7 {
		select {
		case <-returned:
			t.Fatalf("an idle terminal was dropped after %v on the clock, even though its peer "+
				"was answering pings", time.Duration(step)*pongWait*3/4)
		case <-clock.pushed:
		case <-time.After(10 * time.Second):
			t.Fatal("no pong pushed the read deadline out within 10s: the ping, the pong, or the " +
				"handler that moves the deadline is missing")
		}
		if step < 6 {
			clock.advance(pongWait * 3 / 4)
		}
	}

	// The peer goes quiet. A pong already on its way can push the deadline once
	// more, so the clock is moved past it again until it bites.
	answering.Store(false)
	for {
		clock.advance(pongWait + time.Second)
		select {
		case <-returned:
			return
		case <-clock.pushed:
		case <-time.After(10 * time.Second):
			t.Fatal("a peer that stopped answering outlived a pongWait on the clock: the clock " +
				"this test moves is not the one the stream's read deadline runs on")
		}
	}
}

// dialClockedStream is dialTimedStream with the server side's read deadline on
// a readClock, at the production pongWait, and a ping every 10ms of real time.
func dialClockedStream(t *testing.T, serve func(*stream)) (*websocket.Conn, *readClock) {
	t.Helper()

	clock := &readClock{pushed: make(chan struct{}, 1)}
	upgrader := websocket.Upgrader{CheckOrigin: sameOrigin}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		defer conn.Close()
		serve(newTimedStream(conn, wireRaw, pongWait, 10*time.Millisecond, writeWait))
	}))
	srv.Listener = clockedListener{Listener: srv.Listener, clock: clock}
	srv.Start()
	t.Cleanup(srv.Close)

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dialling the test server: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, clock
}

// readClock is a clock for one socket's read deadline, moved by the test.
//
// The stream sets its deadline as production does: time.Now() plus pongWait.
// The socket under it takes what is left of that window and counts it from
// the clock's own now instead, and gives the kernel no deadline at all - until
// the test moves the clock past one, when the socket is handed a deadline
// already gone, which fails the read in progress exactly as a lapsed one does.
// So a read times out only when the test says the time has passed, never
// because the machine was slow, and every window the stream asks for is
// honored in full.
type readClock struct {
	mu   sync.Mutex
	conn net.Conn      // the server side of the socket
	now  time.Duration // how far the test has moved the clock
	due  time.Duration // when the read deadline falls, on this clock
	set  bool          // a deadline is in force

	// pushed is signalled each time the deadline is moved into the future: at
	// the start, and by every pong after it. advance empties it, so a signal
	// received after an advance was sent after it.
	pushed chan struct{}
}

func (c *readClock) setReadDeadline(conn net.Conn, d time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conn = conn
	if d.IsZero() {
		c.set = false
		return conn.SetReadDeadline(time.Time{})
	}
	left := time.Until(d)
	if left <= 0 {
		// Already gone: the HTTP server does this to stop its own read when
		// the socket is hijacked. Passed through as it is.
		c.set = false
		return conn.SetReadDeadline(d)
	}
	c.set, c.due = true, c.now+left
	select {
	case c.pushed <- struct{}{}:
	default:
	}
	return conn.SetReadDeadline(time.Time{})
}

// advance moves the clock, and fails the read in progress if that takes it
// past the deadline.
func (c *readClock) advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now += by
	if c.set && c.conn != nil && c.now >= c.due {
		_ = c.conn.SetReadDeadline(time.Unix(1, 0))
	}
	select {
	case <-c.pushed:
	default:
	}
}

// clockedListener hands the HTTP server sockets whose read deadlines run on
// clock. A hijacked connection is the one Accept returned, so the WebSocket
// gorilla builds on it sets its deadlines here.
type clockedListener struct {
	net.Listener
	clock *readClock
}

func (l clockedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return clockedConn{Conn: conn, clock: l.clock}, nil
}

type clockedConn struct {
	net.Conn
	clock *readClock
}

func (c clockedConn) SetReadDeadline(d time.Time) error {
	return c.clock.setReadDeadline(c.Conn, d)
}

func (c clockedConn) SetDeadline(d time.Time) error {
	if err := c.clock.setReadDeadline(c.Conn, d); err != nil {
		return err
	}
	return c.Conn.SetWriteDeadline(d)
}

// The write half of a socket whose peer has gone.
//
// The read deadline catches a peer that stops answering. It does nothing about a
// peer that has gone while its kernel is still accepting bytes: the socket takes
// data until the window closes and then WriteMessage blocks for ever. That parks
// whichever goroutine is copying — on a followed `kubectl logs -f` it is the one
// relaying the torrent — with no error returned and nothing anywhere to observe.
// Deleting the SetWriteDeadline in Write would leave the whole suite green.
//
// Simulated by a client that connects and never reads a byte. That is what a
// closed lid looks like from this side, and it is the only way to fill the window
// without unplugging something.
func TestAPeerThatStopsReadingDoesNotBlockTheWriterForever(t *testing.T) {
	failed := make(chan error, 1)

	// A short write deadline so the test is quick; the property is identical at
	// ten seconds.
	client, _ := dialTimedStream(t, wireRaw, time.Minute, time.Minute, 200*time.Millisecond,
		func(s *stream) {
			// 64 KiB a frame: the point is to fill the socket's window, and the
			// smaller the frame the longer that takes.
			payload := make([]byte, 64*1024)
			for range 20000 {
				if _, err := s.Write(payload); err != nil {
					failed <- err
					return
				}
			}
			failed <- nil
		})
	// Held open and deliberately never read from. Closing it would be a peer that
	// went away politely, which needs no write deadline.
	defer client.Close()

	select {
	case err := <-failed:
		if err == nil {
			t.Fatal("every write succeeded against a peer that read nothing; " +
				"the socket cannot have absorbed that much, so the loop is wrong")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Write never returned against a peer that stopped reading: the goroutine " +
			"relaying terminal output is parked for ever, with no error and nothing to " +
			"observe — this is what the write deadline exists for")
	}
}
