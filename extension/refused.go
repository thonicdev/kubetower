package extension

import (
	"errors"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/thonicdev/kubetower/internal/access"
)

// The two ways the console refuses a read for the person behind a request,
// as opposed to failing to make it.
//
// A refusal is classified with errors.Is against these, never by its message
// and never by the HTTP status a page might pick: ErrDenied is an answer about
// the person, which retrying does not change and which a page draws as one,
// and ErrUnreviewable is the console being unable to ask, which a retry may
// change. Anything that is neither, nor ErrUnknownCluster, is a read that
// failed.
//
// List, Usage, ContainerUsage, UsageWindow and Forward answer them. Reveal and
// MayReadLogs have sentinels of their own (ErrRevealDenied, ErrLogsDenied and
// their Unreviewed pairs), and those are what to ask of them.
// They are the access review's own values, delegated to `internal/access` as
// Trustworthy is to `internal/kubeletstats`, so there is one copy of each and
// the review's refusal answers errors.Is for them wherever it is wrapped.
var (
	// ErrDenied: the cluster says the person behind the request may not make
	// this read. It also satisfies apierrors.IsForbidden, as a 403 the cluster
	// gave the console's own read does; errors.Is(err, ErrDenied) is the test
	// that says the person was refused rather than the console's credentials.
	ErrDenied = access.ErrDenied
	// ErrUnreviewable: the console could not ask the cluster whether the
	// person may make this read, so it did not make it. Under the server
	// profile that refuses; on the desktop a review that cannot be made lets
	// the read through, so a page there rarely sees it.
	ErrUnreviewable = access.ErrUnreviewable
)

// ErrChanged: a write the console refused because the object changed, or was
// created again, after the console read it. RefreshApplication answers it
// wrapped; asking again reads the object afresh.
var ErrChanged = errors.New("the object changed since the console read it")

// ErrConsoleNotGranted: the person's own review allowed the read and the API
// server then refused the console's own account (its ServiceAccount) - a grant
// the installation lacks, which no right of the person's changes and which
// whoever installed the console adds. Only under the server profile, where the
// console reads as itself; on the desktop the account is the person's and a 403
// stays theirs. List answers it wrapped, and the error still satisfies
// apierrors.IsForbidden, so a page that sorts by that keeps its behavior. It is
// not part of RefusedRead: a page asks ConsoleNotGranted, first.
var ErrConsoleNotGranted = errors.New("the console's own account is not granted this read")

// NotGranted wraps a Forbidden the API server gave the console's own read, so
// that errors.Is(err, ErrConsoleNotGranted) holds and the original is kept.
func NotGranted(err error) error {
	if err == nil {
		return nil
	}
	return &notGranted{err: err}
}

type notGranted struct{ err error }

func (n *notGranted) Error() string        { return n.err.Error() }
func (n *notGranted) Unwrap() error        { return n.err }
func (n *notGranted) Is(target error) bool { return target == ErrConsoleNotGranted }

// ConsoleNotGranted sorts a read the console's own account was refused from
// the person's refusal and from a failure: 503 CodeConsoleNotGranted, naming
// the resource. Not a 403, which would read as the person's, nor a 502, which
// would read as a cluster that did not answer.
func ConsoleNotGranted(err error) (Refusal, bool) {
	if !errors.Is(err, ErrConsoleNotGranted) {
		return Refusal{}, false
	}
	what := "what this read needs"
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		if d := status.Status().Details; d != nil && d.Kind != "" {
			what = d.Kind
			if d.Group != "" {
				what += "." + d.Group
			}
		}
	}
	return Refusal{Status: http.StatusServiceUnavailable, Code: CodeConsoleNotGranted,
		Message: "The installation does not grant the console's own account the right to list " + what +
			" on this cluster, so nothing was read. This is not a refusal of your rights."}, true
}

// The codes a page answers a read refused for the person with, as its pages
// spell them: protocol, the same in every extra.
const (
	// CodeListForbidden: the cluster refused the read. 403.
	CodeListForbidden = "list_forbidden"
	// CodeListUnreviewed: the console could not ask the cluster whether the
	// person may make the read, so it made none. A failure to ask, which a
	// retry may change, and never a 502: the cluster was not read. 503.
	CodeListUnreviewed = "list_unreviewed"
	// CodeConsoleNotGranted: the console's own account lacks a grant the
	// installation should have made (ErrConsoleNotGranted). 503.
	CodeConsoleNotGranted = "console_not_granted"
)

// UnreviewedText is the console's sentence for a read it could not review for
// the person, whatever was being read, for a page to answer with instead of
// writing its own.
const UnreviewedText = "The console could not ask the cluster whether you may make this read, so it made none. Try again."

// Refusal is how a page answers a read refused for the person: the status,
// the code and the sentence.
type Refusal struct {
	Status  int
	Code    string
	Message string
}

// RefusedRead sorts a read refused for the person from one that failed, with
// errors.Is as this file says, and reports whether it was refused:
//
//   - ErrDenied: 403 CodeListForbidden, saying denied - the page's own
//     sentence of what the person may not read;
//   - ErrUnreviewable: 503 CodeListUnreviewed, saying UnreviewedText.
//
// Anything else is false, and the caller's: a read that failed, an unknown
// cluster, or a 403 the cluster gave the console's own credentials.
func RefusedRead(err error, denied string) (Refusal, bool) {
	switch {
	case errors.Is(err, ErrDenied):
		return Refusal{Status: http.StatusForbidden, Code: CodeListForbidden, Message: denied}, true
	case errors.Is(err, ErrUnreviewable):
		return Refusal{Status: http.StatusServiceUnavailable, Code: CodeListUnreviewed, Message: UnreviewedText}, true
	}
	return Refusal{}, false
}
