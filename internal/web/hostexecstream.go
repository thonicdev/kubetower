package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/terminal"
)

var _ extension.ExecStreamer = host{}

// ExecStream is Exec with the standard output copied to stdout as it arrives:
// checked, counted, then reviewed and recorded by Gate.Perform with the
// extension and the argv on both records, and run inside it. What it streams
// is never recorded.
func (h host) ExecStream(r *http.Request, name string, q extension.ExecQuery, stdout io.Writer, limit int64) (extension.ExecStreamResult, error) {
	if h.ext == "" {
		return extension.ExecStreamResult{}, errors.New("ExecStream is answered only to a mounted extension, so the " +
			"record can name who asked; this door was handed to none")
	}
	timeout, err := execQueryProblems(q, extension.MaxExecStreamTimeout)
	if err != nil {
		return extension.ExecStreamResult{}, err
	}
	if limit <= 0 || limit > extension.MaxExecStreamBytes {
		return extension.ExecStreamResult{}, fmt.Errorf("a streamed command writes between 1 byte and %d bytes; asked for %d",
			int64(extension.MaxExecStreamBytes), limit)
	}
	if stdout == nil {
		return extension.ExecStreamResult{}, errors.New("a streamed command needs somewhere to write its output")
	}
	client, err := h.client(r, name)
	if err != nil {
		return extension.ExecStreamResult{}, err
	}
	if err := h.refuseReadOnly(client.Context); err != nil {
		return extension.ExecStreamResult{}, err
	}
	release, err := h.s.execShare().take(r.Context())
	if err != nil {
		return extension.ExecStreamResult{}, err
	}
	defer release()

	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	ctx = access.WithNote(ctx, access.Note{Extension: h.ext, Command: quoteArgv(q.Command)})
	attrs := execAttributes(q.Namespace, q.Pod)

	var out terminal.StreamOutput
	_, err = h.s.gate.Perform(ctx, client.Context, attrs, func(ctx context.Context) error {
		var err error
		out, err = terminal.StreamCommand(ctx, client, terminal.Command{
			Namespace: q.Namespace, Pod: q.Pod, Container: q.Container, Argv: q.Command,
		}, stdout, limit, extension.ExecStreamStderrLimit)
		return err
	})
	result := extension.ExecStreamResult{Written: out.Written, Stderr: out.Stderr, ExitCode: out.ExitCode, Truncated: out.Truncated}
	var refused *access.Refused
	switch {
	case err == nil:
		return result, nil
	case errors.As(err, &refused):
		return extension.ExecStreamResult{}, err
	case errors.Is(err, terminal.ErrNotInImage):
		return extension.ExecStreamResult{}, fmt.Errorf("%w: %q in container %s of pod %s/%s",
			extension.ErrNoSuchCommand, q.Command[0], q.Container, q.Namespace, q.Pod)
	case errors.Is(ctx.Err(), context.DeadlineExceeded) && r.Context().Err() == nil:
		return result, fmt.Errorf("%w (%s, in container %s of pod %s/%s)",
			extension.ErrExecTimedOut, timeout, q.Container, q.Namespace, q.Pod)
	}
	return result, fmt.Errorf("streaming %q from container %s of pod %s/%s on cluster %q: %w",
		q.Command[0], q.Container, q.Namespace, q.Pod, client.Context, err)
}
