package health

import (
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// Denial says whether the API server refused the identity (401) or the
// request (403).
//
// The two send an operator to different places: a 401 is a session they renew
// themselves, a 403 is an RBAC binding that retrying cannot change. `classify`
// and `internal/web`'s `stateOf` both ask Denied so there is one opinion; a
// lapsed `aws sso login` must not read as wrong permissions.
type Denial int

const (
	// DenialNone — not a refusal by the API server; the caller's other arms apply.
	DenialNone Denial = iota
	// DenialUnauthorized — 401. The identity was not accepted at all.
	DenialUnauthorized
	// DenialForbidden — 403. The identity was accepted and may not do this.
	DenialForbidden
)

// Denied tells a 401 from a 403, and both from everything else.
//
// The typed predicates come first: they read the status the API server sent,
// through any wrapping. The string fallback is for errors with no type to
// read: a log stream's failure arriving as event text, and an exec credential
// plugin refused inside client-go before any status exists ("You must be
// logged in to the server (Unauthorized)"). The request URL is stripped first,
// since `?timeout=32s` rides in every address.
//
// Unauthorized is tested before forbidden in both halves: a sentence that
// mentions both is a 401 explaining itself.
func Denied(err error) Denial {
	if err == nil {
		return DenialNone
	}
	switch {
	case apierrors.IsUnauthorized(err):
		return DenialUnauthorized
	case apierrors.IsForbidden(err):
		return DenialForbidden
	}
	lower := strings.ToLower(withoutRequestURL(err.Error()))
	switch {
	case contains(lower, "unauthorized", "you must be logged in"):
		return DenialUnauthorized
	case contains(lower, "forbidden"):
		return DenialForbidden
	}
	return DenialNone
}
