package extension

import "errors"

// The answers Host.Reveal and Host.List give about a Secret, each wrapped with
// the sentence that says which Secret, which key and why.
//
// Sentinels, not messages: a page has to answer "you may not read that Secret"
// differently from "the console could not ask" and from "there is no such
// Secret", and matching a message breaks on the first rewording. An error that
// is none of these is a read that failed, and must not be drawn as a refusal or
// as an absence.
var (
	// ErrRevealDenied: the cluster says the person behind the request may not
	// `get` that Secret. Nothing was read. A 403, for a page that maps one.
	ErrRevealDenied = errors.New("you may not read that Secret")
	// ErrRevealUnreviewed: the console could not ask whether the person may
	// read it, or could not write the record of the reveal before reading it,
	// and it reads nothing it could not review and record. Not a denial, so a
	// 503, worth trying again.
	ErrRevealUnreviewed = errors.New("the console could not review or record reading that Secret, so it read nothing")
	// ErrNoSuchSecret: the person may read it and the cluster holds no Secret
	// of that name in that namespace.
	ErrNoSuchSecret = errors.New("no such Secret")
	// ErrNoSuchKey: the Secret was read and holds no key of that name.
	ErrNoSuchKey = errors.New("the Secret holds no such key")
	// ErrSecretsNotListed is Host.List refusing core `v1/secrets`: a list
	// hands over every value of every Secret it returns. Answered before
	// anything is read.
	ErrSecretsNotListed = errors.New("secrets are not read through Host.List: a value an extension needs comes " +
		"through Host.Reveal, one key of one named Secret, reviewed for the person and recorded")
)

// Tunnel is what Host.Forward opened.
type Tunnel struct {
	// Addr is what to dial, "127.0.0.1:34567".
	Addr string
	// Pod is the name of the pod the tunnel reached, in the namespace Forward
	// was asked for: for a Service, the one of its pods the forwarder chose. A
	// credential read for a pod is sent down this tunnel only when this names
	// that pod.
	Pod string
	// PodKnown says whether the tunnel could name its pod. When false, Pod is
	// empty, and a page that needs the pod must refuse rather than take the
	// empty name for one.
	PodKnown bool
	// Stop tears the tunnel down and gives its slot back. Safe to call more
	// than once; a page must call it when it is done.
	Stop func()
}
