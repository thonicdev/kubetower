package api

import (
	"time"
)

// --- Dock -------------------------------------------------------------------

// DesktopTerminalRequest opens a terminal application window on the machine
// running the console, pointed at one cluster. It carries no size and no working
// directory: there is no pseudo-terminal to size, and the window starts in the
// session directory, where its kubeconfig lives.
type DesktopTerminalRequest struct {
	// Cluster is what the window's KUBECONFIG points at, and nothing else.
	Cluster string `json:"cluster"`
	// Namespace, Kind and Name are what was on screen, recorded as the console's
	// selection before the process starts.
	Namespace string `json:"namespace,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name,omitempty"`
}

// TerminalSession is the session and the channel to attach to. The server names
// the channel so the session and the socket cannot drift apart.
type TerminalSession struct {
	ID string `json:"id"`
	// Channel is absent for a desktop terminal, which has no socket.
	Channel  string     `json:"channel,omitempty"`
	OpenedAt *time.Time `json:"openedAt,omitempty"`
}

// Container is one container of a pod that can be followed.
type Container struct {
	Name     string `json:"name"`
	Init     *bool  `json:"init,omitempty"`
	Ready    *bool  `json:"ready,omitempty"`
	Restarts *int   `json:"restarts,omitempty"`
}

// LogEvent is one event of the log stream.
//
// A `state` event carries the envelope's state vocabulary mid-stream, so a log
// that stopped because the pod went away reads differently from one where
// nothing is being written. It is followed by an `end`, except a `pending` one
// with CodeContainerNotStarted, whose lines follow once the container starts.
type LogEvent struct {
	Type      string     `json:"type"`
	At        *time.Time `json:"at,omitempty"`
	Container string     `json:"container,omitempty"`
	Text      string     `json:"text,omitempty"`
	State     State      `json:"state,omitempty"`
	Reason    *Reason    `json:"reason,omitempty"`
	// AtStart says this is the oldest line the cluster still holds, which is not
	// necessarily the first the process wrote.
	AtStart *bool `json:"atStart,omitempty"`
}

// LogPage is one page backwards: the block of lines immediately preceding what
// the caller already holds, oldest first. It is a bounded JSON read rather than
// a stream frame, so its status can say whether the question could be asked.
//
// The Kubernetes log API has no cursor. `before` is how many lines the caller
// holds; the server reads a tail of `before + page` and keeps its front, the
// block preceding what the caller has (internal/logs/earlier.go).
type LogPage struct {
	// Lines is oldest first, and `[]` rather than null when there are none.
	Lines []string `json:"lines"`
	// Stop is empty while there may be another page, and otherwise says why
	// paging stopped. Only LogStopBeginning means there is nothing older.
	Stop LogStop `json:"stop,omitempty"`
	// Note is the sentence to put on screen, composed server-side so every client
	// says the same words. It never says "the container started here": the
	// kubelet rotates logs, so the oldest line held is not the first written.
	Note string `json:"note,omitempty"`
}

// LogStop is which of the three ways a page backwards stopped.
type LogStop string

const (
	// LogStopBeginning: the read reached the oldest line the kubelet still holds
	// for this container. The only one that is an ending.
	LogStopBeginning LogStop = "beginning"
	// LogStopCeiling: there are more lines and this console will not fetch them;
	// the note says to use kubectl.
	LogStopCeiling LogStop = "ceiling"
	// LogStopDense: the boundary was not reached within the scan budget, and
	// part of the block would prepend lines adjacent to nothing, a false history.
	LogStopDense LogStop = "dense"
)

// Forward is one live port-forward.
type Forward struct {
	ID        string `json:"id"`
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	// Pod is the replica the tunnel reached; Service is what was asked for, where
	// a Service was. A Service row matches on Service, not Pod, so it neither
	// claims a forward opened on the pod nor loses its own when a rollout
	// replaces the pod.
	Pod        string `json:"pod"`
	Service    string `json:"service,omitempty"`
	RemotePort int    `json:"remotePort"`
	Address    string `json:"address"`
	// Scheme is "http" or "https", the one to put in front of Address. Only the
	// server decides (internal/forward/scheme.go), so the browser keeps no second
	// copy of the rule to drift.
	Scheme   string     `json:"scheme,omitempty"`
	OpenedAt *time.Time `json:"openedAt,omitempty"`
}

// ForwardWindowRequest opens a forward's flow UI in a desktop window
// (openForwardWindow).
type ForwardWindowRequest struct {
	// Tool is "hubble" or "whisker".
	Tool string `json:"tool"`
	// Path is the absolute path and query on the forward, "/" when empty.
	Path string `json:"path,omitempty"`
	// CloseWithWindow says the forward was opened for this window.
	CloseWithWindow bool `json:"closeWithWindow,omitempty"`
}

// ForwardRequest opens one.
type ForwardRequest struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	// Pod and Service are alternatives and exactly one is set. A Service forward
	// picks a ready pod behind it and says which.
	Pod        string `json:"pod,omitempty"`
	Service    string `json:"service,omitempty"`
	RemotePort int    `json:"remotePort"`
	LocalPort  int    `json:"localPort,omitempty"`
	// PortName and AppProtocol let the server decide the scheme. They are not
	// part of the target's identity: two clicks on the same port are one tunnel.
	PortName    string `json:"portName,omitempty"`
	AppProtocol string `json:"appProtocol,omitempty"`
}
