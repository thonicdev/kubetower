package terminal

import (
	"context"
	"errors"
	"io"
	"sync"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// StreamOutput is what a streamed Command did: how many bytes of its standard
// output reached the writer, what it wrote on its standard error (kept, up to
// a bound), and how it ended.
type StreamOutput struct {
	Written  int64
	Stderr   []byte
	ExitCode int
	// Truncated is the standard output reaching the limit StreamCommand was
	// given, or the standard error reaching its own: the command was stopped
	// there.
	Truncated bool
}

// StreamCommand runs c as RunCommand does, but copies its standard output to
// stdout as it arrives instead of keeping it: a file a container holds, too
// large to buffer. At most limit bytes reach stdout and stderrLimit bytes of
// its standard error are kept; past either the command is stopped and the
// answer says Truncated. A write to stdout that fails stops the command too,
// and is the error answered.
func StreamCommand(ctx context.Context, client *cluster.Client, c Command, stdout io.Writer, limit int64, stderrLimit int) (StreamOutput, error) {
	if client == nil {
		return StreamOutput{}, errors.New("no client for this cluster")
	}
	if len(c.Argv) == 0 {
		return StreamOutput{}, errors.New("a command names the program it runs; this one names none")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	req := client.Clients.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(c.Namespace).Name(c.Pod).SubResource("exec").
		VersionedParams(execOptions(c), scheme.ParameterCodec)
	executor, err := newExecutor(cluster.StreamingConfig(client.Config), req.URL())
	if err != nil {
		return StreamOutput{}, err
	}
	out := &cappedWriter{w: stdout, limit: limit, stop: cancel}
	errs := &boundedOutput{limit: stderrLimit, full: cancel}
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: out,
		Stderr: errs.stream(&errs.stderr),
	})
	kept := errs.result()
	written, cut, failed := out.state()
	if failed != nil {
		return StreamOutput{Written: written, Stderr: kept.Stderr}, failed
	}
	result := StreamOutput{Written: written, Stderr: kept.Stderr, Truncated: cut || kept.Truncated}
	code, err := commandEnded(err, result.Truncated)
	if err != nil {
		return StreamOutput{Written: written, Stderr: kept.Stderr}, err
	}
	result.ExitCode = code
	return result, nil
}

// cappedWriter passes at most limit bytes to w, and stops the command past it
// or when w fails.
type cappedWriter struct {
	w     io.Writer
	limit int64
	stop  context.CancelFunc

	mu      sync.Mutex
	written int64
	cut     bool
	failed  error
}

// Write reports every byte written once the limit is reached or w has failed,
// so the stream does not fail on a short write; the command is stopped
// instead.
func (c *cappedWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cut || c.failed != nil {
		return len(p), nil
	}
	chunk := p
	if room := c.limit - c.written; int64(len(chunk)) > room {
		chunk = chunk[:max(room, 0)]
		c.cut = true
	}
	if len(chunk) > 0 {
		n, err := c.w.Write(chunk)
		c.written += int64(n)
		if err == nil && n < len(chunk) {
			err = io.ErrShortWrite
		}
		if err != nil {
			c.failed = err
		}
	}
	if c.cut || c.failed != nil {
		c.stop()
	}
	return len(p), nil
}

func (c *cappedWriter) state() (int64, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.written, c.cut, c.failed
}
