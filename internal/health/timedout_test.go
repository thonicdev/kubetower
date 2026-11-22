package health

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"
)

// connectFailure is a failed dial the way client-go hands it back: a url.Error
// around a net.OpError around the operating system's own error, with
// `?timeout=30s` in the address.
func connectFailure(cause error) error {
	return &url.Error{
		Op:  "Get",
		URL: "https://10.255.255.1:6443/api?timeout=30s",
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connectex", cause)},
	}
}

// A connect that timed out is a timeout, on Windows too.
//
// Winsock's 10060 is a net.Error whose Timeout() is false, and its sentence -
// "did not properly respond after a period of time", localized - holds no
// phrase: on the Windows binary the commonest real timeout would read
// `cluster_unreachable`. On Linux a dial's ETIMEDOUT reads "connection timed
// out", which needs a phrase of its own once its type is lost.
func TestAConnectThatTimedOutIsATimeout(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
	}{
		// By number: on every platform the text of Errno(10060) here is
		// either Winsock's sentence or "errno 10060", neither of which holds a
		// phrase, so only the number arm can answer.
		{"Winsock's 10060", connectFailure(wsaetimedout)},
		{"ETIMEDOUT", connectFailure(syscall.ETIMEDOUT)},
		// By phrase, the type lost on the way.
		{"Linux's sentence, as text", errors.New(`Get "https://10.0.0.1:6443/api?timeout=30s": dial tcp 10.0.0.1:6443: connect: connection timed out`)},
	} {
		if !TimedOut(c.err) {
			t.Errorf("%s: TimedOut(%v) = false", c.name, c.err)
		}
		if state, _, _ := classify(c.err); state != StateTimeout {
			t.Errorf("%s: classify said %q; the health page reads the same failure as a timeout", c.name, state)
		}
	}
	// And a refused connection, its neighbor by number, is not one.
	if TimedOut(connectFailure(wsaeconnrefused)) || TimedOut(connectFailure(syscall.ECONNREFUSED)) {
		t.Error("a refused connection read as a timeout")
	}
}

// fakeNetError is a network error whose text says nothing about time: only
// its Timeout() can tell TimedOut what happened.
type fakeNetError struct{ timeout bool }

func (e fakeNetError) Error() string   { return "the transport gave up" }
func (e fakeNetError) Timeout() bool   { return e.timeout }
func (e fakeNetError) Temporary() bool { return false }

// The net.Error arm is load-bearing: a timeout whose text holds no phrase is
// still a timeout when the error says so, and only then.
func TestANetworkErrorThatSaysItTimedOutIsATimeout(t *testing.T) {
	if !TimedOut(fmt.Errorf("listing pods: %w", fakeNetError{timeout: true})) {
		t.Error("a net.Error with Timeout() true, wrapped, read as something else")
	}
	if TimedOut(fmt.Errorf("listing pods: %w", fakeNetError{timeout: false})) {
		t.Error("a net.Error with Timeout() false read as a timeout")
	}
}

// The address is not read for a phrase.
//
// Every phrase holds a space and no address does, so with today's list the
// stripping changes nothing - so without this test, removing it would leave
// every test green. What it guards is a one-word phrase added later: the bare
// word "timeout" matches `?timeout=30s`. Added here for the length of the
// test, it must still not match the address.
func TestTheAddressIsNotReadForAPhrase(t *testing.T) {
	saved := timeoutPhrases
	t.Cleanup(func() { timeoutPhrases = saved })
	timeoutPhrases = append(append([]string(nil), saved...), "timeout")

	refused := errors.New(`Get "https://10.0.0.1:6443/api/v1/pods?limit=500&timeout=30s": EOF`)
	if TimedOut(refused) {
		t.Errorf("TimedOut read %q as a timeout: the address was read for a phrase", refused)
	}
	if !TimedOut(errors.New("read: timeout")) {
		t.Error("the one-word phrase did not match outside the address, so this test proves nothing")
	}
}
