// Package audit is the console's record of who did what: one structured line
// per act, on a sink of its own, written before the act and refusing it when
// it cannot be.
//
// Under the server profile the console acts with one ServiceAccount, so the
// API server's audit cannot say who asked; this record is the only one of the
// person. The process log will not do: a file on a volume the audited process
// can rewrite is evidence held by the accused. So in the server profile the
// record goes to stdout and nothing else writes there: a container's stdout
// leaves the pod line by line, out of this process's reach.
//
// One JSON object per line: every common log pipeline parses it without a
// grammar, and the structure is the escaping, so no value can end a record or
// begin a forged one.
//
// A failed write of the record refuses the act, and a record can only refuse
// what has not happened, so each act writes two records sharing a random id:
//
//   - attempt, after the review and before the request is sent; if it cannot
//     be written the request is not sent;
//   - outcome, after the request: `applied`, `refused` or `failed` with a word
//     for why. It repeats every field of the attempt.
//
// An attempt with no outcome must be read as a request that was sent. A
// failed outcome write cannot refuse anything; the caller logs it and the act
// stands.
//
// A record never carries a manifest body, a Secret's value or an API server's
// message: Record has no field a value could be put in.
package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/thonicdev/kubetower/internal/identity"
)

// The acts a record is about.
const (
	// Write is a request that changes an object: create, patch, update, delete,
	// an eviction.
	Write = "write"
	// Exec is a shell inside a pod (`create pods/exec` or `pods/attach`). The
	// attempt is written as it opens and the outcome when it closes.
	Exec = "exec"
	// PortForward is a tunnel into a pod (`create pods/portforward`). The
	// outcome is whether it opened.
	PortForward = "portforward"
	// Reveal is a masked value handed to a view (`get` on the object holding
	// it). The record names the key and never carries the value.
	Reveal = "reveal"
	// Extension is an extension turned on or off on a cluster, or one of its
	// settings written, under the server profile: a write to the console's own
	// state that changes what every person of the cluster sees, reviewed as
	// `use` on the extension. Change says what it changed.
	Extension = "extension"
)

// The two phases.
const (
	Attempt = "attempt"
	Outcome = "outcome"
)

// The outcomes.
const (
	// Applied is the request sent and carried out: a write applied, a shell
	// opened and run to its end, a tunnel opened, a value handed over.
	Applied = "applied"
	// Refused is the act not made: the review said no or could not be made.
	Refused = "refused"
	// Failed is the request made and not carried out. Reason says why, in a
	// word.
	Failed = "failed"
)

// Record is one line: who, the verb, the resource and where, the review's
// decision, the time, and the outcome.
//
// The resource fields are a SubjectAccessReview's ResourceAttributes: the
// question reviewed, which is the request sent (internal/access's transport
// refuses one that is not). Copied in rather than taken as access.Attributes
// so that this package stays a leaf.
type Record struct {
	Time  string `json:"time"`
	ID    string `json:"id"`
	Phase string `json:"phase"`
	Act   string `json:"act"`

	Cluster     string `json:"cluster"`
	Verb        string `json:"verb"`
	Group       string `json:"group,omitempty"`
	Version     string `json:"version,omitempty"`
	Resource    string `json:"resource"`
	Subresource string `json:"subresource,omitempty"`
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	// Key is the one key a reveal named. Never its value.
	Key string `json:"key,omitempty"`
	// Change is what an Extension act changed, in the console's words
	// (`enabled=true, was false`). Empty on every other act.
	Change string `json:"change,omitempty"`
	// Extension is the key of the extension that asked, on an act made through
	// extension.Host. Empty on the console's own.
	Extension string `json:"extension,omitempty"`
	// Command is the argv of a command an extension ran through
	// extension.Host.Exec, each argument quoted. Empty on every other act, the
	// console's own shells included: what a person types in a shell is theirs,
	// and never recorded.
	Command string `json:"command,omitempty"`
	// DryRun marks a request sent with `dryRun=All`. Recorded, because it is
	// still a request made with the ServiceAccount's rights for a person, and
	// without the flag `applied` would claim a change that did not happen.
	DryRun bool `json:"dryRun,omitempty"`
	// Force marks a delete sent with a grace period of zero ("Force delete"):
	// the object's terminationGracePeriodSeconds was not waited for.
	Force bool `json:"force,omitempty"`

	// Decision is the review's answer: allowed, denied or unreviewable; or
	// not_in_profile, a request the profile does not make, refused unasked; or
	// read_only, an act refused unasked on a cluster in KT_READONLY_CLUSTERS.
	Decision string `json:"decision"`
	// Outcome and Reason are on the outcome record only.
	Outcome string `json:"outcome,omitempty"`
	// Reason is a word, never a message: an API server's message can quote the
	// value it refused.
	Reason string `json:"reason,omitempty"`

	// Who is the provider's name for the person, or the shared account's user
	// name; empty when no session reached the act. Via is how it was
	// established (sso, password, launch, unattributed) and says whether Who
	// names anybody (password and launch do not).
	//
	// Via is `console` for an act the console makes for itself (AsConsole),
	// reviewed as its own rights. Who is then empty and the person whose page
	// caused it is OnBehalfOf: written as Who, the record would attribute to
	// them an act the review never asked about them.
	Who        string `json:"who"`
	Via        string `json:"via"`
	OnBehalfOf string `json:"onBehalfOf,omitempty"`
}

// The Via of an act nobody is behind, and of an act the console makes for
// itself.
const (
	Unattributed = "unattributed"
	Console      = "console"
)

// Attribute fills Who and Via from the identity ctx carries.
func (r *Record) Attribute(ctx context.Context) {
	r.OnBehalfOf = ""
	who, ok := identity.From(ctx)
	if !ok {
		r.Who, r.Via = "", Unattributed
		return
	}
	r.Who, r.Via = who.Name, who.Method
}

// AttributeToConsole fills Via as the console's own act, and the person whose
// request caused it - if any - as OnBehalfOf.
func (r *Record) AttributeToConsole(ctx context.Context) {
	r.Who, r.Via, r.OnBehalfOf = "", Console, ""
	if who, ok := identity.From(ctx); ok {
		r.OnBehalfOf = who.Name
	}
}

// Log writes records to one writer, one line per Write call.
//
// The zero value is not usable. A nil *Log writes nothing and never fails:
// that is the desktop, where stdout is a NULL handle on Windows under
// `-H windowsgui` and a failing sink would refuse every write. Whether a
// profile may run without one is internal/access's decision.
type Log struct {
	mu      sync.Mutex
	out     io.Writer
	now     func() time.Time
	id      func() (string, error)
	timeout time.Duration
	// broken is why the log stopped writing: a write failed, came back short
	// or did not come back in time. It does not recover.
	broken error
}

// WriteTimeout is how long one record may take to reach the sink before the
// log is declared broken.
// Five seconds, the bound one access review gets: a healthy collector takes
// a line in microseconds, and a stopped one will not catch up while a person
// waits.
const WriteTimeout = 5 * time.Second

// New is a Log writing to out - os.Stdout in the server profile.
//
// One Write call per record, under a lock, so two acts cannot interleave on
// one line. A write that errors, is short, or has not returned within
// WriteTimeout breaks the log for the rest of the process. A closed-pipe
// stdout kills the process with SIGPIPE, which also fails closed.
func New(out io.Writer) *Log {
	return NewWithTimeout(out, WriteTimeout)
}

// NewWithTimeout is New with another bound on one write, for tests.
func NewWithTimeout(out io.Writer, timeout time.Duration) *Log {
	return &Log{out: out, now: time.Now, id: newID, timeout: timeout}
}

// Broken is why the log no longer writes, or nil while it does. A nil Log is
// never broken.
func (l *Log) Broken() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.broken
}

// newID is 16 random bytes, hex: unique across replicas without coordinating,
// and carrying nothing about the act.
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Pending is an act whose attempt is on record and whose outcome is not yet.
type Pending struct {
	log    *Log
	record Record
}

// ID is the id the two records share; empty on a nil Log.
func (p *Pending) ID() string {
	if p == nil {
		return ""
	}
	return p.record.ID
}

// Begin writes the attempt record of r (its Time, ID and Phase set here) and
// answers the act's handle. An error means the act must not be made. On a nil
// Log it writes nothing and answers a handle whose End writes nothing.
func (l *Log) Begin(r Record) (*Pending, error) {
	if l == nil {
		return &Pending{}, nil
	}
	id, err := l.id()
	if err != nil {
		return nil, fmt.Errorf("no id for the audit record: %w", err)
	}
	r.ID = id
	r.Phase = Attempt
	r.Outcome, r.Reason = "", ""
	if err := l.write(r); err != nil {
		return nil, err
	}
	return &Pending{log: l, record: r}, nil
}

// End writes the outcome record: the attempt's fields, the outcome, and a word
// for why. An error is the record not written; the act stands and the caller
// logs it.
func (p *Pending) End(outcome, reason string) error {
	if p == nil || p.log == nil {
		return nil
	}
	r := p.record
	r.Phase = Outcome
	r.Outcome, r.Reason = outcome, reason
	return p.log.write(r)
}

var (
	// ErrShortWrite is a sink that took part of a record.
	ErrShortWrite = errors.New("the audit sink took part of the record")
	// ErrStalled is a sink that did not take a record within the write's bound.
	ErrStalled = errors.New("the audit sink did not take the record in time")
	// ErrBroken is what every write answers once one has failed.
	ErrBroken = errors.New("the audit log stopped writing earlier in this process, so it records nothing more")
)

// write puts one record on the sink, or breaks the log.
//
// One failure breaks it for good: a failed write can leave part of a line
// with no newline, gluing the next record to it, and a timed-out write is
// still in flight and may land in the middle of the next. Neither can be
// repaired here, so every later act is refused and a restart is the
// recovery.
//
// The wait is bounded because a collector that stops reading would otherwise
// block the lock and every act behind it, silently. The write cannot be
// canceled; it is left on its goroutine and the log is broken.
func (l *Log) write(r Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.broken != nil {
		return fmt.Errorf("%w: %w", ErrBroken, l.broken)
	}
	r.Time = l.now().UTC().Format(time.RFC3339Nano)
	r.bound()
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	line = append(escapeInvisible(line), '\n')

	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := l.out.Write(line)
		done <- result{n, err}
	}()
	timer := time.NewTimer(l.timeout)
	defer timer.Stop()
	select {
	case got := <-done:
		switch {
		case got.err != nil:
			l.broken = got.err
		case got.n < len(line):
			l.broken = ErrShortWrite
		}
	case <-timer.C:
		l.broken = ErrStalled
	}
	return l.broken
}

// The bounds on a field, in bytes.
//
// Every string on a record comes from somebody (a URL, a query, an identity
// provider). Unbounded, one request with a megabyte in its name writes two
// megabyte records, denied or not, and a runtime splits a line past 16 KiB
// into partial lines. A name is cut at 253 bytes, Kubernetes' longest (a DNS
// subdomain); other fields at a kibibyte. A cut value says how much was cut.
const (
	nameBound  = 253
	otherBound = 1024
)

// bound cuts every field to its bound.
func (r *Record) bound() {
	for _, field := range []*string{&r.Namespace, &r.Name, &r.Key, &r.Extension, &r.Resource, &r.Subresource, &r.Group, &r.Version, &r.Verb} {
		*field = cut(*field, nameBound)
	}
	for _, field := range []*string{&r.Cluster, &r.Who, &r.OnBehalfOf, &r.Reason, &r.Decision, &r.Act, &r.Via, &r.Outcome, &r.Command} {
		*field = cut(*field, otherBound)
	}
}

// cut is s at most limit bytes, on a rune boundary, followed by a marker
// saying how many bytes were dropped.
func cut(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	end := limit
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return fmt.Sprintf("%s…[+%d bytes]", s[:end], len(s)-end)
}

// escapeInvisible writes the runes that move or hide text as JSON escapes.
//
// encoding/json escapes every C0 control and the two Unicode line breaks, so
// no value can end a record. It leaves raw three sets a terminal still acts
// on, and `kubectl logs` in a terminal is how a record is read in a hurry:
//
//   - the C1 controls, U+0080-U+009F (NEL breaks lines in some viewers,
//     U+009B begins an escape sequence);
//   - the bidirectional embeddings, overrides and isolates (U+202A-U+202E,
//     U+2066-U+2069), which can draw a field after one written before it;
//   - the implicit direction marks U+200E, U+200F and U+061C.
//
// The escaped line decodes to the same value. These runes can only occur
// inside a string in json.Marshal's output, so the result is valid JSON.
func escapeInvisible(line []byte) []byte {
	if !hasInvisible(line) {
		return line
	}
	out := make([]byte, 0, len(line)+16)
	for i := 0; i < len(line); {
		r, size := utf8.DecodeRune(line[i:])
		if isInvisible(r) {
			out = fmt.Appendf(out, `\u%04x`, r)
		} else {
			out = append(out, line[i:i+size]...)
		}
		i += size
	}
	return out
}

func hasInvisible(line []byte) bool {
	for i := 0; i < len(line); {
		r, size := utf8.DecodeRune(line[i:])
		if isInvisible(r) {
			return true
		}
		i += size
	}
	return false
}

func isInvisible(r rune) bool {
	return (r >= 0x80 && r <= 0x9f) || r == 0x200e || r == 0x200f || r == 0x061c ||
		(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}
