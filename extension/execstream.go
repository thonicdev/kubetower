package extension

import (
	"io"
	"net/http"
	"time"
)

// ExecStreamer is the console's exec door for a file a container holds that
// is too large for Exec's buffer: a heap dump, a Flight Recorder file. The
// Host the core hands an extension implements it; an extension asks with a
// type assertion, so an extension that needs none compiles unchanged and Host
// gains no method.
type ExecStreamer interface {
	// ExecStream runs one command as Exec does - reviewed for the person as
	// `create pods/exec` on the pod by name, recorded with the argv and the
	// extension before it runs and with its outcome after, counted with Exec's
	// commands against the same ceiling and share, no shell, no stdin, no
	// terminal - but copies its standard output to stdout as it arrives
	// instead of keeping it.
	//
	//   - **At most limit bytes reach stdout**, and limit is at most
	//     MaxExecStreamBytes: past it the command is stopped and the result
	//     says Truncated. ExecStreamStderrLimit bytes of its standard error are
	//     kept, and past them it is stopped too.
	//   - **The deadline is ExecQuery.Timeout**, ExecTimeout when zero, at most
	//     MaxExecStreamTimeout: a large file takes longer than a diagnostic.
	//   - A write to stdout that fails stops the command and is the error
	//     answered; what reached stdout before it is Written.
	//   - **What it streams is never recorded**: the record names the argv,
	//     never the content.
	//
	// The errors are Exec's: ErrDenied, ErrUnreviewable, ErrExecBusy,
	// ErrExecTimedOut, ErrNoSuchCommand, ErrUnknownCluster, wrapped.
	ExecStream(r *http.Request, cluster string, q ExecQuery, stdout io.Writer, limit int64) (ExecStreamResult, error)
}

// ExecStreamResult is how a streamed command ended.
type ExecStreamResult struct {
	// Written is how many bytes of its standard output reached the writer.
	Written  int64
	Stderr   []byte
	ExitCode int
	// Truncated is the output reaching its limit: the command was stopped
	// there, and what it would have written after did not reach the writer.
	Truncated bool
}

const (
	// MaxExecStreamBytes is the most a streamed command may write: one GiB.
	MaxExecStreamBytes = 1 << 30
	// ExecStreamStderrLimit is how much of a streamed command's standard
	// error is kept.
	ExecStreamStderrLimit = 64 << 10
	// MaxExecStreamTimeout is the longest deadline a streamed command may ask
	// for.
	MaxExecStreamTimeout = 10 * time.Minute
)
