package report

// The wire shapes, and why this page decodes Argo CD's objects into structs
// rather than `unstructured`.
//
// Most of an Application's bytes are fields the page never draws:
// `status.history` (each entry a full copy of the source) and
// `status.operationState` are about three quarters of it. The API offers no
// field projection, a custom resource's fieldSelector takes only name and
// namespace, and Table conversion drops the conditions and the source, so the
// bytes arrive anyway. Decoding into structs that name only the drawn fields
// lets `encoding/json` skip the rest without allocating, about twice as fast
// as `unstructured`.
//
// Items are decoded one at a time, although one call is faster: a typed decode
// is strict, and one malformed Application would otherwise fail the whole list
// instead of being left out and counted.

import (
	"encoding/json"
	"fmt"
)

// rawMeta is the three fields of an object's metadata this page uses.
//
// DeletionTimestamp is what tells an Application stuck on a finalizer from a
// working one: it stays `Healthy` and `Synced` while being deleted. It is a
// string, not a `metav1.Time`, whose UnmarshalJSON fails the whole object on
// an unparsable value; presence decides that the object is being deleted.
type rawMeta struct {
	Name              string `json:"name"`
	Namespace         string `json:"namespace"`
	DeletionTimestamp string `json:"deletionTimestamp"`
}

// rawCondition is one `status.conditions` entry. An ApplicationSet writes
// `Status` and an Application does not, so nothing may require it.
type rawCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// rawSource is where an Application comes from. `spec.source` on a single-source
// Application, an entry of `spec.sources` on a multi-source one.
type rawSource struct {
	RepoURL        string `json:"repoURL"`
	Path           string `json:"path"`
	TargetRevision string `json:"targetRevision"`
}

// rawOperationState is `status.operationState`, minus the copies of the source
// it carries. It is a pointer on rawApplication because a never-synced
// Application has none, which must not read as a success.
//
// The timestamps are strings parsed by `timeAt`, not `metav1.Time`, so an
// unparsable value loses the time rather than the Application.
type rawOperationState struct {
	Phase      string `json:"phase"`
	Message    string `json:"message"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt"`
	Operation  struct {
		Sync struct {
			Revision string `json:"revision"`
		} `json:"sync"`
		InitiatedBy struct {
			// Either a username or automated, never both; neither means nobody
			// and nothing synced it.
			Username  string `json:"username"`
			Automated bool   `json:"automated"`
		} `json:"initiatedBy"`
	} `json:"operation"`
}

// rawHistory is one entry of `status.history`: a deployment Argo CD made. It
// leaves out `source`, the bulk of each entry, so the history costs a scan of
// those bytes and no allocation.
//
// ID is a pointer because Argo CD numbers deployments from 0, and the oldest
// kept id is the only count of how many were trimmed: absent must not read as
// zero.
type rawHistory struct {
	ID              *int64 `json:"id"`
	Revision        string `json:"revision"`
	DeployedAt      string `json:"deployedAt"`
	DeployStartedAt string `json:"deployStartedAt"`
	// Either a username or automated, never both.
	InitiatedBy struct {
		Username  string `json:"username"`
		Automated bool   `json:"automated"`
	} `json:"initiatedBy"`
}

// rawApplication is an Argo CD Application, reduced to what this page draws.
// Every field it leaves out is left out on purpose (see the file's header).
type rawApplication struct {
	Metadata rawMeta `json:"metadata"`
	Spec     struct {
		Project string `json:"project"`
		// A pointer, so a `spec` with no `source` is told from an empty one;
		// the first sends readApplication to `spec.sources`.
		Source  *rawSource  `json:"source"`
		Sources []rawSource `json:"sources"`
	} `json:"spec"`
	Status struct {
		// `status.sync` carries no timestamp, so nothing can say when an
		// Application went OutOfSync (see SyncSinceUnrecorded).
		Sync struct {
			Status   string `json:"status"`
			Revision string `json:"revision"`
		} `json:"sync"`
		// `status.health.lastTransitionTime` is a genuine transition: Argo CD's
		// controller sets it only when the health status changes.
		Health struct {
			Status             string `json:"status"`
			Message            string `json:"message"`
			LastTransitionTime string `json:"lastTransitionTime"`
		} `json:"health"`
		// `status.reconciledAt` is when Argo CD last compared the Application.
		// It moves every few minutes whatever the state, so it dates the readings,
		// never when anything started.
		ReconciledAt   string             `json:"reconciledAt"`
		History        []rawHistory       `json:"history"`
		Conditions     []rawCondition     `json:"conditions"`
		OperationState *rawOperationState `json:"operationState"`
	} `json:"status"`
}

// rawSet is an ApplicationSet: its identity and the conditions that say whether
// it is working.
type rawSet struct {
	Metadata rawMeta `json:"metadata"`
	Status   struct {
		Conditions []rawCondition `json:"conditions"`
	} `json:"status"`
}

// rawProject is an AppProject; Argo CD writes no meaningful status on one.
type rawProject struct {
	Metadata rawMeta `json:"metadata"`
	Spec     struct {
		Description string `json:"description"`
	} `json:"spec"`
}

// decodeItems turns items into objects, one at a time, and says how many it
// could not read. The count is returned so the page can say the area is
// partial, rather than failing the whole list over one object.
func decodeItems[T any](items []json.RawMessage) ([]T, int) {
	out := make([]T, 0, len(items))
	var refused int
	for _, raw := range items {
		var item T
		if err := json.Unmarshal(raw, &item); err != nil {
			refused++
			continue
		}
		out = append(out, item)
	}
	return out, refused
}

// refusalNote words what decodeItems left out, or returns "" when it left out
// nothing.
func refusalNote(refused int, plural string) string {
	if refused <= 0 {
		return ""
	}
	return fmt.Sprintf("%d of the %s this cluster returned could not be read and are not counted below; "+
		"an object whose fields are not the shape Argo documents is left out rather than taken as empty.",
		refused, plural)
}
