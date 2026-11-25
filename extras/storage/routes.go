package storage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thonicdev/kubetower/extension"
)

// The codes are protocol, not display text: the front end names each one.
const (
	CodeUnknownCluster = "unknown_cluster"
	CodeBadNamespace   = "bad_namespace"
	CodeBadName        = "bad_name"
	CodeListFailed     = "list_failed"
	CodeNoClaim        = "no_claim"
	CodeListForbidden  = extension.CodeListForbidden
	CodeListUnreviewed = extension.CodeListUnreviewed
)

// The one step whose failure is the request's: without the claims there is no
// page.
const stepClaims = "claims"

type problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

// ClaimAnswer is the Storage tab of one claim.
type ClaimAnswer struct {
	Now        string          `json:"now"`
	Claim      Claim           `json:"claim"`
	Volume     *Volume         `json:"volume,omitempty"`
	Class      *Class          `json:"class,omitempty"`
	Fill       FillSource      `json:"fill"`
	MountsRead Section         `json:"mountsRead"`
	Snapshots  SnapshotSection `json:"snapshots"`
	// History is the claim's snapshots, newest first.
	History []Snapshot `json:"history"`
}

// PodAnswer is the Storage tab of one pod: the claims it mounts.
type PodAnswer struct {
	Now       string          `json:"now"`
	Claims    []Claim         `json:"claims"`
	Fill      FillSource      `json:"fill"`
	Snapshots SnapshotSection `json:"snapshots"`
}

func overviewHandler(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		ns := r.URL.Query().Get("ns")
		if ns != "" && len(validation.IsDNS1123Label(ns)) > 0 {
			writeProblem(w, http.StatusBadRequest, CodeBadNamespace, "That is not a namespace name.", nil)
			return
		}
		got, err := read(r, h, cluster, ns, now())
		if err != nil {
			writeFailure(w, cluster, err)
			return
		}
		writeJSON(w, http.StatusOK, got.answer)
	})
}

// object reads the scope one tab is about, after checking its address.
func object(w http.ResponseWriter, r *http.Request, h extension.Host) (*reading, string, string, bool) {
	cluster, ns, name := r.PathValue("cluster"), r.PathValue("namespace"), r.PathValue("name")
	if len(validation.IsDNS1123Label(ns)) > 0 {
		writeProblem(w, http.StatusBadRequest, CodeBadNamespace, "That is not a namespace name.", nil)
		return nil, "", "", false
	}
	if len(validation.IsDNS1123Subdomain(name)) > 0 {
		writeProblem(w, http.StatusBadRequest, CodeBadName, "That is not an object name.", nil)
		return nil, "", "", false
	}
	got, err := read(r, h, cluster, ns, now())
	if err != nil {
		writeFailure(w, cluster, err)
		return nil, "", "", false
	}
	return got, ns, name, true
}

func claimHandler(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ns, name, ok := object(w, r, h)
		if !ok {
			return
		}
		a := got.answer
		for _, c := range a.Claims {
			if c.Namespace != ns || c.Name != name {
				continue
			}
			out := ClaimAnswer{Now: a.Now, Claim: c, Fill: a.Fill, MountsRead: a.MountsRead, Snapshots: a.Snapshots,
				History: got.history[ns+"/"+name]}
			if out.History == nil {
				out.History = []Snapshot{}
			}
			if len(out.History) > maxHistory {
				out.History = out.History[:maxHistory]
			}
			for i := range a.Volumes {
				if a.Volumes[i].Name == c.Volume && c.Volume != "" {
					v := a.Volumes[i]
					out.Volume = &v
				}
			}
			for i := range a.Classes {
				if a.Classes[i].Name == c.Class && c.Class != "" {
					k := a.Classes[i]
					out.Class = &k
				}
			}
			writeJSON(w, http.StatusOK, out)
			return
		}
		writeProblem(w, http.StatusNotFound, CodeNoClaim, "No such claim.", nil)
	})
}

func podHandler(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ns, name, ok := object(w, r, h)
		if !ok {
			return
		}
		a := got.answer
		out := PodAnswer{Now: a.Now, Claims: []Claim{}, Fill: a.Fill, Snapshots: a.Snapshots}
		if got.pods == nil {
			// The pods could not be read: which claims this one mounts is unknown,
			// which is not "none".
			sec := a.MountsRead
			status := http.StatusBadGateway
			code := CodeListFailed
			if sec.State == StateRefused {
				status, code = http.StatusForbidden, CodeListForbidden
			}
			writeProblem(w, status, code, "The pods could not be read.", errors.New(sec.Detail))
			return
		}
		// The claims this pod mounts, by the rule the claims' Mount follows: a
		// claim that only shares an ephemeral volume's name is not the pod's.
		if _, found := got.pods[ns+"/"+name]; found {
			for _, claim := range got.mounts[ns+"/"+name] {
				for _, c := range a.Claims {
					if c.Namespace == ns && c.Name == claim {
						out.Claims = append(out.Claims, c)
					}
				}
			}
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// writeFailure answers a request that could not be read: an unknown cluster is
// a 404, a refusal for the person a 403 or 503, the console's own account not
// granted a 503, anything else a 502. A request that is gone gets nothing.
func writeFailure(w http.ResponseWriter, cluster string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	if errors.Is(err, extension.ErrUnknownCluster) {
		writeProblem(w, http.StatusNotFound, CodeUnknownCluster, extension.UnknownClusterText(cluster), nil)
		return
	}
	if refused, ok := extension.ConsoleNotGranted(err); ok {
		writeProblem(w, refused.Status, refused.Code, refused.Message, err)
		return
	}
	if refused, ok := extension.RefusedRead(err, "You may not list the PersistentVolumeClaims here."); ok {
		writeProblem(w, refused.Status, refused.Code, refused.Message, err)
		return
	}
	writeProblem(w, http.StatusBadGateway, CodeListFailed, "The PersistentVolumeClaims could not be read.", err)
}

func writeProblem(w http.ResponseWriter, status int, code, message string, err error) {
	body := problem{Code: code, Message: message}
	if err != nil {
		body.Detail = shortError(err)
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// parseBytes is a quantity's bytes, zero when it does not parse.
func parseBytes(s string) int64 {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	return q.Value()
}
