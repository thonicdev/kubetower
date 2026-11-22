package jvm

import (
	"context"
	"net/http"

	"github.com/thonicdev/kubetower/extension"
)

// toolsAnswer is `GET /pod/{namespace}/{name}/{container}/tools`.
type toolsAnswer struct {
	// JCmd is true when jcmd ran in the container. Whether it listed a JVM
	// does not matter: the tool is there.
	JCmd bool `json:"jcmd"`
}

// tools asks whether the container has jcmd, so the page can grey the buttons
// that need it instead of letting a click end in no_jcmd. It runs `jcmd -l`,
// the same command and the same exec door as an inspection's first step:
// reviewed as pods/exec for the person and recorded. A container with no jcmd
// answers the no_jcmd problem; every other failure is the exec's own.
func (JVM) tools(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), revealBudget)
		defer cancel()
		r = r.WithContext(ctx)
		t, ok := resolve(w, r, h)
		if !ok {
			return
		}
		run := t.runner(h, r)
		res, err := run.list()
		switch {
		case err != nil:
			writeExecFailure(w, t.cluster, err)
		case !run.plain && missingJcmd(res):
			writeProblem(w, http.StatusUnprocessableEntity, CodeNoJcmd, noJcmdText, nil)
		default:
			writeJSON(w, http.StatusOK, toolsAnswer{JCmd: true})
		}
	})
}
