package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// --- Terminals --------------------------------------------------------------

// openDesktopTerminal opens a terminal on the machine running the console.
// Refused with 403 where `capabilities.desktopTerminal` is false: in a shared
// profile the shell would run with the process's credentials, not the
// caller's, and hiding the button is not a control.
func (a *API) openDesktopTerminal(w http.ResponseWriter, r *http.Request) {
	var body DesktopTerminalRequest
	// The body is optional here, so an empty request is not malformed.
	if r.ContentLength > 0 {
		if !decode(w, r, maxBody, &body) {
			return
		}
	}
	if a.deps.Dock == nil {
		missing(w, "a dock")
		return
	}
	session, err := a.deps.Dock.OpenDesktopTerminal(body)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, session)
}

// --- Logs -------------------------------------------------------------------

func (a *API) listLogContainers(w http.ResponseWriter, r *http.Request) {
	name, ok := a.clusterQuery(w, r)
	if !ok {
		return
	}
	ref, ok := a.objectFromQuery(w, r, name)
	if !ok {
		return
	}
	if a.deps.Dock == nil {
		writeOK(w, ListFailed[Container](StateAbsent,
			Because(CodeNotInstalled, "this build has no log reader")))
		return
	}
	items, f := a.deps.Dock.LogContainers(r.Context(), ref)
	writeOK(w, listOf(items, f))
}

// streamLogs is the one streaming surface in this contract, framed rather than
// raw because a stream that cannot carry a state change cannot tell a quiet
// container from a broken one.
//
// A condition established before the first line is still a frame, not a
// status: the request was answered (the pod is gone, the verb is denied), and a
// 403 would lose which of the five states it was.
//
// `before` turns it into a page backwards (`internal/logs/earlier.go`), with no
// cursor because `corev1.PodLogOptions` has none: `before` is how many lines
// the caller holds, the server grows the tail and keeps its front, and the
// page has its own ceiling independent of `tailLines`. A follow is unbounded,
// so a refusal must be a frame; a page is bounded, so `fetch` can read the
// status and the sentence, which an `EventSource` cannot.
func (a *API) streamLogs(w http.ResponseWriter, r *http.Request) {
	name, ok := a.clusterQuery(w, r)
	if !ok {
		return
	}
	ref, ok := a.objectFromQuery(w, r, name)
	if !ok {
		return
	}
	q := r.URL.Query()
	container := q.Get("container")
	if len(container) > 253 {
		badRequest(w, "container is too long")
		return
	}
	query := LogQuery{
		ObjectRef: ref,
		Container: container,
		Follow:    queryBool(q, "follow", true),
		TailLines: queryInt(q, "tailLines", 2000, 1, 100000),
		Previous:  queryBool(q, "previous", false),
		// A property of the request: the apiserver prefixes the instant or not,
		// and it cannot be added afterwards, so the panel's toggle re-opens the
		// stream. When on, the stamp arrives as `LogEvent.at`, taken off
		// `LogEvent.text`.
		Timestamps: queryBool(q, "timestamps", false),
	}

	// `before` present means a page backwards. A parameter rather than a mode,
	// so the panel's opening read is an ordinary stream request.
	if raw := q.Get("before"); raw != "" {
		a.earlierLogs(w, r, query, raw, q.Get("page"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	emit := sseEmitter(w, sseWriteWait)
	// The deadline is on the connection, which the browser keeps alive for
	// its next request, so it is cleared when the stream is done.
	defer func() { _ = http.NewResponseController(w).SetWriteDeadline(time.Time{}) }()

	if a.deps.Dock == nil {
		_ = emit(LogEvent{Type: "state", State: StateAbsent,
			Reason: ptrReason(Because(CodeNotInstalled, "this build has no log reader"))})
		_ = emit(LogEvent{Type: "end"})
		return
	}
	if f := a.deps.Dock.StreamLogs(r.Context(), query, emit); f != nil {
		_ = emit(LogEvent{Type: "state", State: f.State, Reason: ptrReason(f.Reason)})
		_ = emit(LogEvent{Type: "end"})
	}
}

// earlierLogs answers one page backwards, as JSON. The two numbers are parsed
// here rather than through queryInt so a wrong one gets a sentence, not a bare
// 400. Clamping is `internal/logs`'s, which owns the ceilings.
func (a *API) earlierLogs(w http.ResponseWriter, r *http.Request, query LogQuery, rawBefore, rawPage string) {
	before, err := strconv.Atoi(rawBefore)
	if err != nil || before < 1 {
		badRequest(w, "before must be how many lines you already hold, as a whole "+
			"number greater than zero; got "+strconv.Quote(rawBefore))
		return
	}
	page := 0
	if rawPage != "" {
		page, err = strconv.Atoi(rawPage)
		if err != nil || page < 1 {
			badRequest(w, "page must be how many earlier lines to fetch, as a whole "+
				"number greater than zero; got "+strconv.Quote(rawPage))
			return
		}
	}
	// A page never follows: `follow=true` would keep a finished block open.
	query.Follow = false
	if a.deps.Dock == nil {
		writeOK(w, ValueFailed[LogPage](StateAbsent,
			Because(CodeNotInstalled, "this build has no log reader")))
		return
	}
	answer, f := a.deps.Dock.EarlierLogs(r.Context(), query, before, page)
	if answer.Lines == nil {
		// An empty array, not `null`: a client reads its length. Set before the
		// envelope so it holds on the `ok` and partial paths alike.
		answer.Lines = []string{}
	}
	// Enveloped, unlike the forwards list, because this is a cluster read:
	// `forbidden`, `absent` and `unreachable` are drawn differently. The stop
	// reasons inside the payload say this page arrived and none will follow.
	writeOK(w, valueOf(answer, f))
}

func ptrReason(r Reason) *Reason { return &r }

// --- Port-forwards ----------------------------------------------------------

// listForwards is process state rather than a cluster read, so it is a plain
// array and not an envelope.
func (a *API) listForwards(w http.ResponseWriter, r *http.Request) {
	if a.deps.Dock == nil {
		missing(w, "a dock")
		return
	}
	forwards, err := a.deps.Dock.Forwards()
	if err != nil {
		fail(w, err)
		return
	}
	if forwards == nil {
		forwards = []Forward{}
	}
	writeOK(w, forwards)
}

func (a *API) openForward(w http.ResponseWriter, r *http.Request) {
	var body ForwardRequest
	if !decode(w, r, maxBody, &body) {
		return
	}
	if body.Cluster == "" || body.Namespace == "" {
		badRequest(w, "cluster and namespace are required")
		return
	}
	// Exactly one of the two: with both, the target would be whichever the
	// layer below read first.
	if (body.Pod == "") == (body.Service == "") {
		badRequest(w, "a forward names a pod or a service, and exactly one of them")
		return
	}
	if body.RemotePort < 1 || body.RemotePort > 65535 {
		badRequest(w, "remotePort is a port number")
		return
	}
	if a.deps.Dock == nil {
		missing(w, "a dock")
		return
	}
	forward, err := a.deps.Dock.OpenForward(r.Context(), body)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, forward)
}

// closeForward is the one route that closes a forward.
func (a *API) closeForward(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" || len(id) > 64 {
		badRequest(w, "an id is required")
		return
	}
	if a.deps.Dock == nil {
		missing(w, "a dock")
		return
	}
	if err := a.deps.Dock.CloseForward(id); err != nil {
		fail(w, err)
		return
	}
	writeNoContent(w)
}

// sseWriteWait bounds each write and flush of a log stream to the browser.
const sseWriteWait = 10 * time.Second

// sseEmitter writes one event per call, each within wait.
//
// Without a bound, a browser that stopped reading (asleep, a stalled proxy)
// blocks the write once the socket buffers fill, and the follow holds its slot
// past its thirty-minute ceiling, which is only checked between lines. With
// it, the write fails and the reader stops on emit's error (internal/logs,
// drive). The flush is where bytes leave, so its error is returned too.
func sseEmitter(w http.ResponseWriter, wait time.Duration) func(LogEvent) error {
	rc := http.NewResponseController(w)
	return func(ev LogEvent) error {
		body, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		var b strings.Builder
		b.WriteString("event: message\ndata: ")
		b.Write(body)
		b.WriteString("\n\n")
		// Every writer net/http hands a handler takes a deadline; one that cannot
		// is written to without.
		_ = rc.SetWriteDeadline(time.Now().Add(wait))
		if _, err := w.Write([]byte(b.String())); err != nil {
			return err
		}
		if err := rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		return nil
	}
}
