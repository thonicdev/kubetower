package netinspect

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/extension"
)

// The codes a failure answers with: what the page branches on.
const (
	// CodeListForbidden and CodeListUnreviewed are the console's own, for a
	// read refused for the person (extension.RefusedRead).
	CodeListForbidden  = extension.CodeListForbidden
	CodeListUnreviewed = extension.CodeListUnreviewed
	// CodeListFailed: the cluster was asked and did not answer. 502.
	CodeListFailed = "list_failed"
	// CodeUnknownCluster: no such context in the kubeconfig. 404.
	CodeUnknownCluster = "unknown_cluster"
	// CodeBadRequest: the question is not one (a verdict between two
	// addresses, a port that is not one). 400.
	CodeBadRequest = "bad_request"
	// CodeNoSuchPod: the pod is not among those the person may list. 404.
	CodeNoSuchPod = "no_such_pod"
	// CodeFinishedPod: the pod is there but has finished (Succeeded or
	// Failed), so it has no network to show. 404, told apart from a gone pod.
	CodeFinishedPod = "pod_finished"
)

// problem is the failure body: code, the sentence, and the cause.
type problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

// readOrFail reads the model, or writes why it could not and answers nil.
// Three outcomes kept apart (R5 §5): refused for the person (403/503), a
// read that failed (502), an unknown cluster (404). An empty cluster is none
// of these: it is a model with nothing in it.
func readOrFail(w http.ResponseWriter, r *http.Request, h extension.Host) *read {
	cluster := r.PathValue("cluster")
	rd, err := gather(r, h, cluster)
	if err == nil {
		return rd
	}
	if errors.Is(err, extension.ErrUnknownCluster) {
		writeProblem(w, http.StatusNotFound, CodeUnknownCluster, extension.UnknownClusterText(cluster), nil)
		return nil
	}
	what := "pods"
	var re *readError
	if errors.As(err, &re) {
		what = re.what
	}
	denied := "The cluster says you may not list its pods, so what its NetworkPolicies allow cannot be drawn."
	failed := "This cluster's pods could not be read, so what its NetworkPolicies allow cannot be drawn."
	if what == "networkpolicies" {
		denied = "The cluster says you may not list its NetworkPolicies, so which rules apply cannot be shown. " +
			"This is not a cluster without rules."
		failed = "This cluster's NetworkPolicies could not be read, so which rules apply is unknown. " +
			"This is not a cluster without rules."
	}
	if refused, ok := extension.RefusedRead(err, denied); ok {
		writeProblem(w, refused.Status, refused.Code, refused.Message, err)
		return nil
	}
	writeProblem(w, http.StatusBadGateway, CodeListFailed, failed, err)
	return nil
}

// overviewHandler is `GET /?ns=&show=&q=&offset=`.
func overviewHandler(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		offset, err := strconv.Atoi(q.Get("offset"))
		if q.Get("offset") != "" && (err != nil || offset < 0) {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, "The offset is a row number, from 0.", nil)
			return
		}
		switch q.Get("show") {
		case "", showIsolatedIn, showIsolatedOut, showOpen, showHostNetwork:
		default:
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, "show is one of in, out, open and host.", nil)
			return
		}
		rd := readOrFail(w, r, h)
		if rd == nil {
			return
		}
		writeJSON(w, http.StatusOK, rd.overview(q.Get("ns"), q.Get("show"), q.Get("q"), offset))
	})
}

// verdictHandler is `GET /verdict?from=&to=&port=&protocol=`.
func verdictHandler(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		rd := readOrFail(w, r, h)
		if rd == nil {
			return
		}
		var v Verdict
		var err error
		if strings.HasPrefix(strings.TrimSpace(q.Get("to")), servicePrefix) {
			var svc *corev1.Service
			if svc, err = serviceRead(r, h, r.PathValue("cluster"), strings.TrimSpace(q.Get("to"))); err == nil {
				v, err = rd.serviceVerdict(q.Get("from"), svc, q.Get("port"), q.Get("protocol"))
			}
		} else {
			v, err = rd.verdict(q.Get("from"), q.Get("to"), q.Get("port"), q.Get("protocol"))
		}
		if err != nil {
			var bad *badQuestion
			if errors.As(err, &bad) {
				writeProblem(w, http.StatusBadRequest, CodeBadRequest, bad.msg, nil)
				return
			}
			var re *readError
			if errors.As(err, &re) {
				if refused, ok := extension.RefusedRead(err, "The cluster says you may not list its Services."); ok {
					writeProblem(w, refused.Status, refused.Code, refused.Message, err)
					return
				}
				writeProblem(w, http.StatusBadGateway, CodeListFailed, "The Service could not be read.", err)
				return
			}
			writeProblem(w, http.StatusInternalServerError, "verdict_failed", "The verdict could not be computed.", err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
}

func writeProblem(w http.ResponseWriter, status int, code, message string, err error) {
	body := problem{Code: code, Message: message}
	if err != nil {
		body.Detail = short(err)
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		status = http.StatusInternalServerError
		data, _ = json.Marshal(problem{Code: "encode_failed", Message: "The answer could not be encoded."})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}

// flowsHandler is `POST /flows?ns=`: what Hubble saw. The person's own
// listing is read first (readOrFail), so the flows are filtered by what they
// may see before the tunnel to Relay is opened.
func flowsHandler(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rd := readOrFail(w, r, h)
		if rd == nil {
			return
		}
		writeJSON(w, http.StatusOK, rd.observe(r, h, r.PathValue("cluster"), r.URL.Query().Get("ns")))
	})
}
