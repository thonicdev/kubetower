package forward

// The three endpoints over the manager: open one, list them, close one.
//
// Not mounted here: internal/web owns the router, so every route sits inside
// its session guard. These hand out a socket into a workload, and the list alone
// says what is being looked at. They answer JSON; how the panel looks is
// internal/web's decision.

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/thonicdev/kubetower/textutil"
)

// OpenHandler opens a tunnel, or hands back the one already open on that
// target. It accepts a form body or a JSON one, with the same field names:
//
//	cluster    a kubeconfig context name, or its URL-safe form   required
//	namespace                                                    required
//	service    the service to forward to                         one of the two
//	pod        the pod to forward to                             one of the two
//	port       the service port, or the container port on a pod  required for a pod
//	portName     the port's name as the object writes it         optional
//	appProtocol  the port's appProtocol, if it declares one      optional
//
// The last two only decide whether the link says http or https (scheme.go);
// they never reach a cluster and are not part of a tunnel's identity.
//
// It answers 200 and one Tunnel, also for a repeated request that returns the
// existing tunnel, so the caller need not tell the two apart:
//
//	{"id":"9f2c4a1b8e07","cluster":"docker-desktop","namespace":"kube-system",
//	 "kind":"service","name":"kube-dns","target":"service/kube-dns",
//	 "port":9153,"podPort":9153,"pod":"coredns-7d6c8b9f4-xk2vp",
//	 "localPort":51423,"localAddr":"127.0.0.1:51423","scheme":"http",
//	 "opened":"2026-08-17T09:12:44Z","age":"0s","healthy":true,"readOnly":false}
//
// A failure is one line of text with a status from StatusFor: 400 malformed,
// 404 not there, 403 refused, 409 at the limit, 502 a cluster that would not
// answer.
func (m *Manager) OpenHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use POST to open a forward", http.StatusMethodNotAllowed)
			return
		}
		req, err := requestFrom(r)
		if err != nil {
			http.Error(w, textutil.FirstLineN(err.Error(), errorLine), http.StatusBadRequest)
			return
		}

		tunnel, err := m.Open(r.Context(), req)
		if err != nil {
			http.Error(w, textutil.FirstLineN(err.Error(), errorLine), StatusFor(err))
			return
		}
		writeJSON(w, tunnel, m.logf)
	})
}

// listView is what the list endpoint answers: an object rather than a bare
// array, so the panel reads the limit instead of hard-coding it.
type listView struct {
	Count   int      `json:"count"`
	Limit   int      `json:"limit"`
	Tunnels []Tunnel `json:"tunnels"`
}

// ListHandler reports every open tunnel, oldest first. No parameters.
//
//	{"count":1,"limit":16,"tunnels":[ …one Tunnel per open forward… ]}
//
// `healthy` is false, with `error` set, for a tunnel that has stopped and not
// yet been reaped.
func (m *Manager) ListHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tunnels := m.List()
		writeJSON(w, listView{Count: len(tunnels), Limit: m.Limit(), Tunnels: tunnels}, m.logf)
	})
}

// CloseHandler tears one tunnel down, on POST or DELETE. It reads the id from
// the path value `id`, the body or the query, whichever is there, so the mount
// is the router's choice.
//
// 204 when it closed one, 404 when there was no such tunnel, which is also what
// a double click produces.
func (m *Manager) CloseHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodDelete:
		default:
			http.Error(w, "use POST or DELETE to close a forward", http.StatusMethodNotAllowed)
			return
		}

		id := strings.TrimSpace(r.PathValue("id"))
		if id == "" {
			_ = r.ParseForm()
			id = strings.TrimSpace(r.Form.Get("id"))
		}
		if id == "" {
			http.Error(w, "an id is required", http.StatusBadRequest)
			return
		}
		if !m.Close(id) {
			http.Error(w, "no such forward", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// requestFrom reads an open request from a JSON body or a form body; both
// are first-class.
func requestFrom(r *http.Request) (Request, error) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var body struct {
			Cluster   string `json:"cluster"`
			Namespace string `json:"namespace"`
			Service   string `json:"service"`
			Pod       string `json:"pod"`
			// A number or a string: "9153" and 9153 are the same intent.
			Port json.Number `json:"port"`
			// Optional, and only used to pick the link's scheme.
			PortName    string `json:"portName"`
			AppProtocol string `json:"appProtocol"`
		}
		// Bounded: a handful of short strings need no more, and an unbounded decode
		// spends memory on the client's say-so.
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&body); err != nil {
			return Request{}, err
		}
		port, err := parsePort(body.Port.String())
		if err != nil {
			return Request{}, err
		}
		return Request{
			Cluster:     strings.TrimSpace(body.Cluster),
			Namespace:   strings.TrimSpace(body.Namespace),
			Service:     strings.TrimSpace(body.Service),
			Pod:         strings.TrimSpace(body.Pod),
			Port:        port,
			PortName:    strings.TrimSpace(body.PortName),
			AppProtocol: strings.TrimSpace(body.AppProtocol),
		}, nil
	}

	if err := r.ParseForm(); err != nil {
		return Request{}, err
	}
	port, err := parsePort(r.Form.Get("port"))
	if err != nil {
		return Request{}, err
	}
	return Request{
		Cluster:     strings.TrimSpace(r.Form.Get("cluster")),
		Namespace:   strings.TrimSpace(r.Form.Get("namespace")),
		Service:     strings.TrimSpace(r.Form.Get("service")),
		Pod:         strings.TrimSpace(r.Form.Get("pod")),
		Port:        port,
		PortName:    strings.TrimSpace(r.Form.Get("portName")),
		AppProtocol: strings.TrimSpace(r.Form.Get("appProtocol")),
	}, nil
}

// parsePort reads the one numeric field. Absent means zero, which
// Request.validate accepts for a service and refuses for a pod; present and not
// a number is an error rather than a silent zero.
func parsePort(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	return port, nil
}

func writeJSON(w http.ResponseWriter, v any, logf func(string, ...any)) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// The list is live state; a cached copy would show a forward that is gone.
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil && logf != nil {
		logf("port-forward: writing the response: %v", err)
	}
}

// StatusFor maps an error to an HTTP status, so a client mistake is not a
// 500: a missing service reads 404 and a cluster that would not answer reads as
// a bad gateway.
//
// Exported because two interfaces sit over one manager, this package's
// handlers and the framed `/api/forwards` operations; the second maps this
// number onto its error vocabulary instead of classifying the sentence again,
// so one refusal gets one status.
func StatusFor(err error) int {
	// The typed predicates first: a Kubernetes error already carries its status,
	// and apierrors unwraps. The sentences below are mostly this package's own and
	// are not API errors.
	switch {
	case apierrors.IsNotFound(err):
		return http.StatusNotFound
	case apierrors.IsForbidden(err):
		return http.StatusForbidden
	case apierrors.IsBadRequest(err), apierrors.IsInvalid(err):
		return http.StatusBadRequest
	}

	message := strings.ToLower(err.Error())
	switch {
	// A misconfigured range before anything that reads like a client mistake:
	// its message ends in "is not a port number", which the 400 list below would
	// otherwise claim. It is a configuration fault, hence 503.
	case strings.Contains(message, strings.ToLower(EnvPorts)+":"),
		strings.Contains(message, strings.ToLower(EnvPorts)+"="),
		strings.Contains(message, strings.ToLower(EnvPorts)+" wants"):
		return http.StatusServiceUnavailable
	case strings.Contains(message, "which is the limit"),
		// The range is full: a 409 like the limit, since nothing is wrong with the
		// cluster and closing a forward fixes it.
		strings.Contains(message, "no local port is free"):
		return http.StatusConflict
	case strings.Contains(message, "marked read-only"):
		return http.StatusForbidden
	case strings.Contains(message, "forbidden"):
		return http.StatusForbidden
	case strings.Contains(message, "not found"),
		strings.Contains(message, "no running pod"),
		strings.Contains(message, "unknown cluster"):
		return http.StatusNotFound
	case strings.Contains(message, "is required"),
		strings.Contains(message, "not both"),
		strings.Contains(message, "give a service or a pod"),
		strings.Contains(message, "needs a port"),
		strings.Contains(message, "is not a port"),
		strings.Contains(message, "is not a kubernetes name"),
		strings.Contains(message, "has to be named"),
		strings.Contains(message, "does not expose port"),
		strings.Contains(message, "no pod selector"):
		return http.StatusBadRequest
	case strings.Contains(message, "shutting down"),
		strings.Contains(message, "not configured"):
		return http.StatusServiceUnavailable
	}
	return http.StatusBadGateway
}
