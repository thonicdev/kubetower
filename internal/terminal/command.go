package terminal

import (
	"context"
	"errors"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// Command is one argv run in one container, with no shell, no stdin and no
// terminal: what extension.Host.Exec runs.
type Command struct {
	Namespace string
	Pod       string
	Container string
	Argv      []string
}

// Output is what a Command wrote and how it ended.
type Output struct {
	Stdout, Stderr []byte
	ExitCode       int
	// Truncated is the output reaching the limit RunCommand was given: the
	// command was stopped there.
	Truncated bool
}

// ErrNotInImage is a command whose program the container runtime could not
// find in the image.
var ErrNotInImage = errors.New("the program is not in this container's image")

// RunCommand runs c and waits for it, keeping at most limit bytes of its output.
//
// **It is a request that changes a cluster** (`create pods/exec`), so it is made
// only from inside a closure handed to access.Gate.Perform, on the context that
// closure is given: the choke-point walk holds it there, and the transport
// guard refuses it on any other context.
//
// A command that ran and exited non-zero is an Output with its ExitCode and a
// nil error. Reaching the limit stops the command and is an Output with
// Truncated, also nil. Any other end is the error.
func RunCommand(ctx context.Context, client *cluster.Client, c Command, limit int) (Output, error) {
	if client == nil {
		return Output{}, errors.New("no client for this cluster")
	}
	if len(c.Argv) == 0 {
		return Output{}, errors.New("a command names the program it runs; this one names none")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	req := client.Clients.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(c.Namespace).Name(c.Pod).SubResource("exec").
		VersionedParams(execOptions(c), scheme.ParameterCodec)
	executor, err := newExecutor(cluster.StreamingConfig(client.Config), req.URL())
	if err != nil {
		return Output{}, err
	}

	out := &boundedOutput{limit: limit, full: cancel}
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: out.stream(&out.stdout),
		Stderr: out.stream(&out.stderr),
	})
	result := out.result()
	code, err := commandEnded(err, result.Truncated)
	if err != nil {
		return Output{}, err
	}
	result.ExitCode = code
	return result, nil
}

// execOptions is what the exec request for c asks of the kubelet. The request
// itself is built where it is sent (RunCommand, StreamCommand): the choke
// point's walk reads the Post in the function Gate.Perform calls.
func execOptions(c Command) *corev1.PodExecOptions {
	return &corev1.PodExecOptions{Container: c.Container, Command: c.Argv, Stdout: true, Stderr: true}
}

// commandEnded reads how a streamed command ended: its exit status, or the
// error that is the answer. truncated is the output reaching its limit, which
// canceled the stream: what came back is the answer, cut.
func commandEnded(err error, truncated bool) (int, error) {
	var status utilexec.CodeExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &status):
		return status.Code, nil
	case truncated:
		return 0, nil
	case notInImage(err):
		return 0, errors.Join(ErrNotInImage, err)
	}
	return 0, err
}

// notInImage reads the container runtime's own sentence for a program that is
// not there (runc: `exec: "jcmd": executable file not found in $PATH`; crun:
// `executable file not found`, or `No such file or directory` for a path). It is
// the only place the runtime says so: the API server wraps it in a 500.
func notInImage(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "executable file not found") ||
		(strings.Contains(msg, "exec") && strings.Contains(msg, "no such file or directory")) ||
		(strings.Contains(msg, "exec") && strings.Contains(msg, "No such file or directory"))
}

// boundedOutput keeps stdout and stderr up to one shared limit, and cancels the
// command the moment the limit is reached.
type boundedOutput struct {
	limit int
	full  context.CancelFunc

	mu        sync.Mutex
	stdout    []byte
	stderr    []byte
	truncated bool
}

type boundedStream struct {
	out *boundedOutput
	buf *[]byte
}

func (o *boundedOutput) stream(buf *[]byte) *boundedStream { return &boundedStream{out: o, buf: buf} }

// Write keeps what fits and reports every byte written, so the stream does not
// fail on a short write; past the limit it stops the command instead.
func (s *boundedStream) Write(p []byte) (int, error) {
	o := s.out
	o.mu.Lock()
	defer o.mu.Unlock()
	room := o.limit - len(o.stdout) - len(o.stderr)
	if room <= 0 || len(p) > room {
		if room > 0 {
			*s.buf = append(*s.buf, p[:room]...)
		}
		if !o.truncated {
			o.truncated = true
			o.full()
		}
		return len(p), nil
	}
	*s.buf = append(*s.buf, p...)
	return len(p), nil
}

func (o *boundedOutput) result() Output {
	o.mu.Lock()
	defer o.mu.Unlock()
	return Output{Stdout: o.stdout, Stderr: o.stderr, Truncated: o.truncated}
}
