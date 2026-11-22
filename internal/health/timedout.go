package health

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// timeoutPhrases are the words a failure that ran out of time is written in,
// where it arrives with nothing typed to read.
//
// Phrases, never the bare word "timeout": client-go puts `?timeout=30s` in
// every request URL, and a context name can carry the word too. Each phrase is
// one a library writes for a deadline that passed:
//
//	i/o timeout                 net, a read or a dial that ran out of time
//	context deadline exceeded   context.DeadlineExceeded's own text
//	client.timeout exceeded     net/http, `(Client.Timeout exceeded while …)`
//	handshake timeout           net/http, `TLS handshake timeout`
//	timeout awaiting            net/http and http2, `timeout awaiting response headers`
//	in the time allotted        apimachinery, a 504 whose type was lost on the way
//	connection timed out        ETIMEDOUT's text on Linux and macOS, `connect: connection timed out`
var timeoutPhrases = []string{
	"i/o timeout", "context deadline exceeded", "client.timeout exceeded",
	"handshake timeout", "timeout awaiting", "in the time allotted",
	"connection timed out",
}

// wsaetimedout is Winsock's number for a connect that nobody answered, the
// commonest timeout on Windows: a blackholed address fails after about 21 s,
// before client-go's dial timeout, as a net.Error whose Timeout() is false and
// whose localized sentence holds none of the phrases above. A literal, because
// the named constant exists only in the Windows build of syscall.
const wsaetimedout = syscall.Errno(10060)

// timedOutConnecting reports whether the failure was a connect that timed out,
// on either family of error numbers.
func timedOutConnecting(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == syscall.ETIMEDOUT || errno == wsaetimedout
}

// TimedOut reports whether a failure was a deadline passing - the request's,
// the server's, or the context's - rather than an answer. internal/web's
// `stateOf` asks it so there is one word list.
//
// The typed checks come first (a 504, a ServerTimeout, a passed context
// deadline, a net.Error timeout, a timed-out connect by error number), then
// the phrases, for errors that arrive as text such as a log stream's failure.
// The request URL is removed before the phrases are read, so a one-word phrase
// could never match `?timeout=30s`.
func TimedOut(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if timedOutConnecting(err) {
		return true
	}
	return contains(strings.ToLower(withoutRequestURL(err.Error())), timeoutPhrases...)
}
