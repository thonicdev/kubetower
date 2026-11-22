package terminal

import (
	"bytes"
	"errors"
	"testing"
)

type failingWriter struct{ after int }

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.after <= 0 {
		return 0, errors.New("the browser went away")
	}
	f.after -= len(p)
	return len(p), nil
}

// Up to the limit reaches the writer; past it the command is stopped, once,
// and every byte is still reported written so the stream does not fail.
func TestACappedWriterPassesTheLimitAndStopsTheCommand(t *testing.T) {
	var out bytes.Buffer
	stops := 0
	c := &cappedWriter{w: &out, limit: 10, stop: func() { stops++ }}
	for _, chunk := range []string{"abcd", "efghij", "klmn", "op"} {
		if n, err := c.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	written, cut, failed := c.state()
	if out.String() != "abcdefghij" || written != 10 || !cut || failed != nil || stops != 1 {
		t.Errorf("wrote %q (%d), cut %v, failed %v, stopped %d times", out.String(), written, cut, failed, stops)
	}
}

// Exactly the limit is not a cut.
func TestACappedWriterAtItsLimitIsNotCut(t *testing.T) {
	var out bytes.Buffer
	c := &cappedWriter{w: &out, limit: 4, stop: func() { t.Error("stopped at exactly the limit") }}
	_, _ = c.Write([]byte("abcd"))
	if _, cut, _ := c.state(); cut {
		t.Error("a write of exactly the limit reads as cut")
	}
}

// A writer that fails stops the command and is the answer.
func TestACappedWriterWhoseWriterFailsStopsTheCommand(t *testing.T) {
	stops := 0
	c := &cappedWriter{w: &failingWriter{after: 4}, limit: 100, stop: func() { stops++ }}
	_, _ = c.Write([]byte("abcd"))
	_, _ = c.Write([]byte("efgh"))
	_, _ = c.Write([]byte("ijkl"))
	written, _, failed := c.state()
	if failed == nil || written != 4 || stops != 1 {
		t.Errorf("written %d, failed %v, stopped %d times", written, failed, stops)
	}
}
