package netinspect

import (
	"net/http"

	"github.com/thonicdev/kubetower/extension"
)

// podHandler is `GET /pods/{namespace}/{name}`: the pod's tab and its
// shortcut tile.
func podHandler(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rd := readOrFail(w, r, h)
		if rd == nil {
			return
		}
		namespace, name := r.PathValue("namespace"), r.PathValue("name")
		answer, ok := rd.podAnswer(namespace, name)
		if !ok {
			if rd.model.finished[namespace+"/"+name] {
				writeProblem(w, http.StatusNotFound, CodeFinishedPod,
					"The pod "+namespace+"/"+name+" has finished, so it has no network to show.", nil)
				return
			}
			writeProblem(w, http.StatusNotFound, CodeNoSuchPod,
				"There is no pod "+namespace+"/"+name+" that you may list: it may have been deleted.", nil)
			return
		}
		writeJSON(w, http.StatusOK, answer)
	})
}

// cellsHandler is `GET /cells`: every pod's isolation for the Pods list's
// column, one read for the whole list.
func cellsHandler(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rd := readOrFail(w, r, h)
		if rd == nil {
			return
		}
		writeJSON(w, http.StatusOK, rd.cells())
	})
}
