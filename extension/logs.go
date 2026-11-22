package extension

import "errors"

// The answers Host.MayReadLogs gives when the person may not read a pod's log,
// each wrapped with the sentence that says who may not do what, and where.
//
// Sentinels, not messages: a page answers "you may not read this log"
// differently from "the console could not ask", and matching a message breaks
// on the first rewording. Any other error is the request's - an unknown cluster
// (ErrUnknownCluster, wrapped) or a namespace that cannot be one - and is
// neither a refusal nor an answer about the person.
var (
	// ErrLogsDenied: the cluster says the person behind the request may not
	// `get pods/log` in that namespace. A 403, for a page that maps one.
	ErrLogsDenied = errors.New("you may not read pods' logs there")
	// ErrLogsUnreviewed: the console could not ask whether the person may read
	// them. Not a denial, so a 503, worth trying again; nothing is shown on an
	// answer nobody gave.
	ErrLogsUnreviewed = errors.New("the console could not review reading pods' logs there, so it shows none")
)
