package extension

import (
	"errors"
	"time"
)

// ExecQuery is one command run in one container of one pod, through Host.Exec.
type ExecQuery struct {
	Namespace string
	Pod       string
	// Container is the container to run in. Required: a pod's default
	// container is an annotation the person did not choose, and a command
	// meant for the application must not land in its sidecar.
	Container string
	// Command is the argv, run as is: there is no shell, so nothing in it is
	// expanded or split. **It is recorded in the audit log**, so it must never
	// carry a secret.
	Command []string
	// Timeout bounds the run, from the request to the last byte. Zero is
	// ExecTimeout; above MaxExecTimeout the query is refused.
	Timeout time.Duration
}

// ExecResult is what a command that ran said.
type ExecResult struct {
	Stdout []byte
	Stderr []byte
	// ExitCode is the command's exit status, as the container runtime
	// reported it. A command that ran and failed is a result, not an error.
	ExitCode int
	// Truncated is true when the output reached ExecOutputLimit: the command
	// was stopped there, and what it would have written after is not here.
	Truncated bool
}

const (
	// ExecOutputLimit is the most bytes of output, stdout and stderr
	// together, a command may send back. A thread dump of a few thousand
	// threads fits; an output past it is cut and ExecResult.Truncated says so.
	ExecOutputLimit = 4 << 20
	// ExecTimeout is a command's deadline when ExecQuery.Timeout is zero.
	ExecTimeout = 30 * time.Second
	// MaxExecTimeout is the longest deadline a query may ask for.
	MaxExecTimeout = 2 * time.Minute
)

var (
	// ErrExecTimedOut is a command that did not finish within its deadline.
	// It was stopped; what it wrote until then is not returned.
	ErrExecTimedOut = errors.New("the command did not finish within its deadline and was stopped")
	// ErrNoSuchCommand is a command the container could not start because its
	// program is not in the image - a JRE image has no jcmd, a distroless one
	// has no shell. Read from the container runtime's message, which is the
	// only place it is said.
	ErrNoSuchCommand = errors.New("the program is not in this container's image")
	// ErrExecBusy is a command refused because the commands extensions run are
	// already at their limit, the console's or one person's share of it.
	ErrExecBusy = errors.New("too many commands run by extensions are already running")
)
