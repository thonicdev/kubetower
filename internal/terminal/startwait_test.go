package terminal

import (
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestASessionThatStartsSlowerThanThePongWaitStillReads: a node shell creates
// its pod and waits for it before pump reads anything, and only a read moves
// the deadline newStream armed. With a pong wait of 100 ms and a start of
// 200 ms, the first read would fail on a deadline already behind it, the
// keystroke the browser had sent would never be delivered and the session
// would end. pump re-arms the deadline where reading starts.
func TestASessionThatStartsSlowerThanThePongWaitStillReads(t *testing.T) {
	var input safeBuffer
	client, wait := dialTimedStream(t, wireRaw, 100*time.Millisecond, time.Hour, writeWait, func(s *stream) {
		time.Sleep(200 * time.Millisecond) // the node shell's pod being created
		s.pump(&input, nil)
	})

	if err := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"in","data":"uptime\r"}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for input.String() == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	_ = client.Close()
	wait()

	if got := input.String(); got != "uptime\r" {
		t.Errorf("pump received %q after a start longer than the pong wait, want %q", got, "uptime\r")
	}
}
