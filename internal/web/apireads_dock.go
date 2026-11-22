package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/forward"
	"github.com/thonicdev/kubetower/internal/logs"
)

type apiDock struct{ s *Server }

func (d apiDock) OpenDesktopTerminal(req api.DesktopTerminalRequest) (api.TerminalSession, error) {
	if d.s.claude == nil || !d.s.claude.DesktopTerminalSupported() {
		// Refused rather than hidden: in a shared profile this shell would run with the
		// process's own credentials.
		return api.TerminalSession{}, fmt.Errorf(
			"%w: this build cannot open a terminal on the machine running the console",
			api.ErrNotInProfile)
	}
	if req.Cluster == "" {
		// The window's point is a KUBECONFIG pointing at one cluster; one pointed at
		// nothing would be a shell on whatever context the person's kubeconfig names.
		return api.TerminalSession{}, fmt.Errorf(
			"%w: a cluster is required — the window is pointed at one and nothing else",
			api.ErrRefused)
	}
	if err := d.s.claude.OpenDesktopTerminal(
		req.Cluster, req.Namespace, req.Kind, req.Name); err != nil {
		return api.TerminalSession{}, fmt.Errorf("%w: %s", api.ErrRefused, firstLine(err.Error()))
	}
	opened := time.Now()
	return api.TerminalSession{
		ID: fmt.Sprintf("desktop-%d", opened.UnixNano()),
		// No channel: an application window, not a socket.
		OpenedAt: &opened,
	}, nil
}

func (d apiDock) LogContainers(ctx context.Context, ref api.ObjectRef) ([]api.Container, *api.Failure) {
	scope := api.Scope{Cluster: ref.Cluster, Namespace: ref.Namespace, Name: ref.Name, Resource: "pods"}
	if d.s.logs == nil {
		return nil, api.Absent("this build has no log reader").Within(scope)
	}
	found, err := d.s.logs.Containers(ctx, ref.Cluster, ref.Namespace, ref.Name)
	if err != nil {
		return nil, stateOf(err, scope)
	}
	out := make([]api.Container, 0, len(found))
	for _, container := range found {
		// Restarts is the only thing that tells the browser whether a previous run
		// exists before it asks: an EventSource never sees a failed handshake's status,
		// so the "previous terminated container not found" 404 would show as an empty
		// pane. Ready travels with it, shown on the same chip.
		restarts := int(container.RestartCount)
		out = append(out, api.Container{
			Name:     container.Name,
			Init:     boolOf(container.Init),
			Ready:    boolOf(container.Ready),
			Restarts: &restarts,
		})
	}
	return out, nil
}

// StreamLogs is the framing the contract adds over `logs.Streamer.Follow`, which
// holds the follow ceiling, the thirty-minute deadline and the archive tee. The
// streamer's events become `line`, `state` and `end`, so a log that stopped
// because the pod went away reads differently from one nothing is writing to.
//
//   - Anything that is not a clean finish emits a `state` frame and then `end`,
//     so "end of log", "stopped after 8 MiB", "stopped after 30m" and "the read
//     failed" stay distinct, each with a reason code. The one `state` that is
//     not an ending is `pending` with `container_not_started`: a follow waiting
//     for its container.
//   - `atStart` is set only where established: a bounded read that returned
//     fewer lines than asked reached the beginning; a follow says nothing.
//   - With `timestamps`, the API server's RFC3339Nano prefix goes into `At`
//     and out of `Text`.
func (d apiDock) StreamLogs(ctx context.Context, q api.LogQuery, emit func(api.LogEvent) error) *api.Failure {
	scope := api.Scope{Cluster: q.Cluster, Namespace: q.Namespace, Name: q.Name, Resource: "pods"}
	if d.s.logs == nil {
		return api.Absent("this build has no log reader").Within(scope)
	}
	req := logs.Request{
		Cluster:    q.Cluster,
		Namespace:  q.Namespace,
		Pod:        q.Name,
		Container:  q.Container,
		Tail:       int64(q.TailLines),
		Previous:   q.Previous,
		Follow:     q.Follow,
		Timestamps: q.Timestamps,
	}

	// Resolved first so every line carries its container: a caller that named none
	// gets the first main container, and the panel must be told which.
	// internal/logs answers that default; Follow then reuses the name, so the pod is
	// read at most once.
	resolved, err := d.s.logs.Resolve(ctx, req)
	if err != nil {
		return stateOf(err, scope)
	}
	req = resolved

	var (
		lines  int64
		failed *api.Failure
	)
	tail := logs.EffectiveTail(req)

	err = d.s.logs.Follow(ctx, req, func(event logs.Event) error {
		switch event.Kind {
		case logs.EventLine:
			lines++
			at, text := splitStamp(req.Timestamps, event.Text)
			return emit(api.LogEvent{Type: "line", At: at, Container: req.Container, Text: text})

		case logs.EventTruncated:
			// More log exists and this console will not read it: api.CodeTruncated.
			return emit(api.LogEvent{Type: "state", State: api.StateUnreadable,
				Reason: reasonPtr(api.Because(api.CodeTruncated, event.Text))})

		case logs.EventError:
			// Mid-stream, so not a status; classified by stateOf like every other read.
			f := stateOf(errors.New(event.Text), scope)
			return emit(api.LogEvent{Type: "state", State: f.State, Reason: reasonPtr(f.Reason)})

		case logs.EventWaiting:
			// Waiting for the container to start: `pending`, a notice the first line
			// replaces.
			return emit(api.LogEvent{Type: "state", State: api.StatePending,
				Reason: reasonPtr(api.Because(api.CodeContainerNotStarted, event.Text))})

		case logs.EventNotStarted:
			// The wait is over and the container never ran: not retryable until someone
			// fixes the image or configuration.
			return emit(api.LogEvent{Type: "state", State: api.StateUnreadable,
				Reason: reasonPtr(api.Because(api.CodeContainerNotStarted, event.Text).Retry(false))})

		case logs.EventRefused:
			// The sign-in it was opened under has ended, or its review now says no: a
			// refusal, not retryable as it stands.
			return emit(api.LogEvent{Type: "state", State: api.StateForbidden,
				Reason: reasonPtr(api.Because(api.CodeVerbDenied, event.Text).Retry(false))})

		case logs.EventEnd:
			// "end of log" is a natural finish. The deadline is our own ceiling with more to
			// read: unreadable and retryable, since reopening carries on.
			if event.Text != "end of log" {
				return emit(api.LogEvent{Type: "state", State: api.StateUnreadable,
					Reason: reasonPtr(api.Because(api.CodeTimeout, event.Text).Retry(true))})
			}
		}
		return nil
	})
	if err != nil {
		// Established before the first line (the ceiling, an unknown cluster, a missing
		// pod): still a frame, since the handler has already written 200.
		failed = stateOf(err, scope)
		if emitErr := emit(api.LogEvent{Type: "state", State: failed.State,
			Reason: reasonPtr(failed.Reason)}); emitErr != nil {
			return nil
		}
	}

	end := api.LogEvent{Type: "end"}
	if failed == nil && !req.Follow && lines < tail {
		end.AtStart = boolOf(true)
	}
	_ = emit(end)
	return nil
}

func reasonPtr(r api.Reason) *api.Reason { return &r }

// splitStamp takes the API server's timestamp off a line and returns it as an
// instant, only where the read asked for one: otherwise a date-shaped first word
// belongs to the container. A stamped line that does not parse keeps its text
// whole.
func splitStamp(asked bool, line string) (*time.Time, string) {
	if !asked {
		return nil, line
	}
	space := strings.IndexByte(line, ' ')
	if space <= 0 {
		return nil, line
	}
	at, err := time.Parse(time.RFC3339Nano, line[:space])
	if err != nil {
		return nil, line
	}
	return &at, line[space+1:]
}

// EarlierLogs is one page backwards over `logs.Streamer.Earlier`, which holds
// everything that makes paging correct (the window grows, the ceiling is taken
// before anything opens, `previous` is never flipped). This adds the five states
// around the answer and the stop reason as a member a client can branch on.
// `Stop` and `Note` travel together; the sentences are internal/logs'.
func (d apiDock) EarlierLogs(ctx context.Context, q api.LogQuery, before, page int) (api.LogPage, *api.Failure) {
	scope := api.Scope{Cluster: q.Cluster, Namespace: q.Namespace, Name: q.Name, Resource: "pods"}
	if d.s.logs == nil {
		return api.LogPage{}, api.Absent("this build has no log reader").Within(scope)
	}
	req := logs.Request{
		Cluster:   q.Cluster,
		Namespace: q.Namespace,
		Pod:       q.Name,
		Container: q.Container,
		Previous:  q.Previous,
		// Never follow: a page backwards is a fixed read.
		Follow:     false,
		Timestamps: q.Timestamps,
	}
	// Resolved as the stream resolves, so the panel is told the default container.
	resolved, err := d.s.logs.Resolve(ctx, req)
	if err != nil {
		return api.LogPage{}, stateOf(err, scope)
	}
	answer, err := d.s.logs.Earlier(ctx, resolved, int64(before), int64(page))
	if err != nil {
		// The question could not be asked; "nothing earlier" is an answer and arrives in
		// Stop.
		return api.LogPage{}, stateOf(err, scope)
	}
	return api.LogPage{
		Lines: answer.Lines,
		Stop:  api.LogStop(answer.Stop),
		Note:  answer.Note,
	}, nil
}

// forwardingRefusal is why this console opens no forward, or "" where it does.
func (s *Server) forwardingRefusal() string {
	switch {
	case s.replicated:
		return "this console runs as several replicas, and a forward is a listener inside one of them: " +
			"the next request may reach another. Forward from your own machine with kubectl port-forward"
	case s.forwards == nil:
		return "this console has no port-forwarder"
	}
	return ""
}

// forwardSet is the forward manager, where this build has one. Otherwise
// `ErrNotInProfile`, not `[]`: "there are none" is not "I cannot see them".
func (d apiDock) forwardSet() (ForwardSet, error) {
	if d.s.forwards == nil {
		return nil, fmt.Errorf("%w: this build has no port-forwarder", api.ErrNotInProfile)
	}
	set, ok := d.s.forwards.(ForwardSet)
	if !ok {
		return nil, fmt.Errorf(
			"%w: this build's port-forwarder cannot be asked for its live set", api.ErrNotInProfile)
	}
	return set, nil
}

func (d apiDock) Forwards() ([]api.Forward, error) {
	set, err := d.forwardSet()
	if err != nil {
		return nil, err
	}
	live := set.List()
	out := make([]api.Forward, 0, len(live))
	for _, tunnel := range live {
		out = append(out, forwardOf(tunnel))
	}
	return out, nil
}

func (d apiDock) OpenForward(ctx context.Context, req api.ForwardRequest) (api.Forward, error) {
	set, err := d.forwardSet()
	if err != nil {
		return api.Forward{}, err
	}
	tunnel, err := set.Open(ctx, forward.Request{
		Cluster:   req.Cluster,
		Namespace: req.Namespace,
		// Exactly one is set; `internal/forward` refuses the pair itself.
		Pod:     req.Pod,
		Service: req.Service,
		Port:    req.RemotePort,
		// Carried so the server decides the scheme. Not part of a target's identity, so
		// two clicks on one port are one tunnel.
		PortName:    req.PortName,
		AppProtocol: req.AppProtocol,
	})
	if err != nil {
		return api.Forward{}, forwardRefusal(err)
	}
	return forwardOf(tunnel), nil
}

func (d apiDock) CloseForward(id string) error {
	set, err := d.forwardSet()
	if err != nil {
		return err
	}
	if !set.Close(id) {
		// A forward that is not there: ErrNotFound, the contract's 404, not a 500 for a
		// client closing one another tab already closed.
		return fmt.Errorf("%w: no port-forward %q is open", api.ErrNotFound, id)
	}
	return nil
}

// forwardOf is the one place a tunnel becomes a contract object. `Pod` is the
// replica actually reached: a service forward picks one pod and stays on it.
// `RemotePort` is what was asked for; for a named targetPort the tunnel lands on
// `Tunnel.PodPort`, which the contract has no member for.
func forwardOf(tunnel forward.Tunnel) api.Forward {
	out := api.Forward{
		ID:         tunnel.ID,
		Cluster:    tunnel.Cluster,
		Namespace:  tunnel.Namespace,
		Pod:        tunnel.Pod,
		RemotePort: tunnel.Port,
		Address:    tunnel.LocalAddr,
		Scheme:     tunnel.Scheme,
	}
	// What was asked for, when it was a Service; on a pod forward the contract
	// already carries the pod reached.
	if tunnel.Kind == "service" {
		out.Service = tunnel.Name
	}
	if !tunnel.Opened.IsZero() {
		opened := tunnel.Opened
		out.OpenedAt = &opened
	}
	return out
}

// forwardRefusal maps the forwarder's classification onto the contract's error
// vocabulary through `forward.StatusFor`, so one refusal cannot map two ways.
// The sentence travels whole.
func forwardRefusal(err error) error {
	// The access review's answer first: a denial is the cluster's no, and a review
	// that could not be made is a 503.
	if errors.Is(err, access.ErrUnreviewable) {
		return fmt.Errorf("%w: %s", api.ErrUnreviewable, firstLine(err.Error()))
	}
	if errors.Is(err, access.ErrDenied) {
		return fmt.Errorf("%w: %s", api.ErrRefused, firstLine(err.Error()))
	}
	// The read-only marking is the guardrail, not authorization: `read_only`.
	if refusal := (*forward.ReadOnlyRefusal)(nil); errors.As(err, &refusal) {
		return fmt.Errorf("%w: %s", api.ErrReadOnly, firstLine(err.Error()))
	}
	switch forward.StatusFor(err) {
	case http.StatusConflict:
		// The ceiling, or the local port range full: closing a forward fixes both, so
		// it has its own response.
		return fmt.Errorf("%w: %s", api.ErrLimitReached, firstLine(err.Error()))
	case http.StatusForbidden:
		return fmt.Errorf("%w: %s", api.ErrRefused, firstLine(err.Error()))
	}
	return errors.New(firstLine(err.Error()))
}

func boolOf(b bool) *bool { return &b }
