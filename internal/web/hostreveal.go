package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/validate/content"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/identity"
)

// listSegments refuses a ListQuery whose group, version, resource or namespace
// is not one path segment, before anything is reviewed or read. The dynamic
// client joins them with path.Join, which cleans `x/../secrets` into `secrets`
// after isSecrets and the review saw the other spelling; so each is checked as
// Kubernetes checks a segment (no `/`, no `%`, not `.` or `..`), and the
// resource is never empty. What is reviewed is then what is sent.
func listSegments(q extension.ListQuery) error {
	parts := []struct{ what, value string }{
		{"group", q.GVR.Group}, {"version", q.GVR.Version},
		{"resource", q.GVR.Resource}, {"namespace", q.Namespace},
	}
	for _, part := range parts {
		if part.value == "" {
			if part.what == "resource" {
				return errors.New("a list names the resource it reads; this one names none")
			}
			continue
		}
		if problems := content.IsPathSegmentName(part.value); len(problems) != 0 {
			return fmt.Errorf("the %s %q of a list is not one path segment (%s), so it is not read",
				part.what, part.value, strings.Join(problems, "; "))
		}
	}
	return nil
}

// isSecrets is whether a group and resource name core Secrets, the one kind
// Host.List refuses. Case-folded, so a capital letter cannot walk past it.
func isSecrets(group, resource string) bool {
	return group == "" && strings.EqualFold(strings.TrimSpace(resource), "secrets")
}

// noKey is the outcome record's word for a Secret read that held no such key:
// the cluster answered, and the key was not in the answer.
const noKey = "NoSuchKey"

// Reveal reads one key of one named Secret for the person behind r, in the same
// order as the console's own RevealEnv:
//
//  1. reviewed as `get secrets/<name>` for the person, by name (a review by
//     kind would over-grant a Role that names one Secret);
//  2. the attempt recorded, refused reviews included, before anything is read;
//  3. an attempt that could not be recorded refuses the read;
//  4. read inside Gate.Read, and the outcome recorded on every path.
//
// Only an Allowed answer reads, on both profiles: a review that could not be
// made refuses even on the desktop. The value goes nowhere but the return: not in
// the record, the process line or an error.
func (h host) Reveal(r *http.Request, name, namespace, secret, key string) (string, error) {
	if h.ext == "" {
		return "", errors.New("Reveal is answered only to a mounted extension, so the reveal can name who asked; " +
			"this door was handed to none")
	}
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(secret) == "" || key == "" {
		return "", fmt.Errorf("a reveal names one key of one Secret in one namespace; asked for key %q of %q in %q",
			key, secret, namespace)
	}
	client, err := h.client(r, name)
	if err != nil {
		return "", err
	}
	ctx := r.Context()
	attrs := access.Attributes{Verb: "get", Version: "v1", Resource: "secrets", Namespace: namespace, Name: secret}
	answer := h.s.gate.Review(ctx, client.Context, attrs)

	record := h.s.gate.Record(ctx, audit.Reveal, client.Context, attrs, answer.Decision)
	record.Key = key
	record.Extension = h.ext
	pending, unrecorded := h.s.gate.Begin(ctx, record)
	said := revealSaid{ctx: ctx, s: h.s, cluster: client.Context, attrs: attrs, key: key, ext: h.ext}

	if !answer.Allowed() {
		h.s.gate.End(ctx, pending, audit.Refused, "")
		said.line(answer.Decision, "refused", "")
		return "", refusedReveal(&access.Refused{Cluster: client.Context, Attributes: attrs, Answer: answer})
	}
	if unrecorded != nil {
		said.line(answer.Decision, "refused", "unrecorded")
		return "", fmt.Errorf("%w: %w", extension.ErrRevealUnreviewed, unrecorded)
	}

	var data map[string][]byte
	read := false
	_, err = h.s.gate.Read(ctx, client.Context, attrs, func(ctx context.Context) error {
		read = true
		got, err := client.Clients.CoreV1().Secrets(namespace).Get(ctx, secret, metav1.GetOptions{})
		if err != nil {
			return err
		}
		data = got.Data
		return nil
	})

	var refused *access.Refused
	switch {
	case !read && errors.As(err, &refused):
		// Allowed a moment ago and not now: the held answer expired in between. Nothing
		// was read.
		h.s.gate.End(ctx, pending, audit.Refused, "")
		said.line(refused.Answer.Decision, "refused", "")
		return "", refusedReveal(refused)
	case apierrors.IsNotFound(err):
		h.s.gate.End(ctx, pending, audit.Failed, string(metav1.StatusReasonNotFound))
		said.line(access.Allowed, "absent", string(metav1.StatusReasonNotFound))
		return "", fmt.Errorf("%w: %s in namespace %s on cluster %q", extension.ErrNoSuchSecret, secret, namespace, client.Context)
	case err != nil:
		word := access.FailureWord(err)
		h.s.gate.End(ctx, pending, audit.Failed, word)
		said.line(access.Allowed, "failed", word)
		return "", fmt.Errorf("the Secret %s in namespace %s on cluster %q could not be read: %w", secret, namespace, client.Context, err)
	}
	value, ok := data[key]
	if !ok {
		h.s.gate.End(ctx, pending, audit.Failed, noKey)
		said.line(access.Allowed, "absent", noKey)
		return "", fmt.Errorf("%w: %q in Secret %s in namespace %s", extension.ErrNoSuchKey, key, secret, namespace)
	}
	h.s.gate.End(ctx, pending, audit.Applied, "")
	said.line(access.Allowed, "ok", "")
	return string(value), nil
}

// refusedReveal is a review that did not allow the reveal, as the contract's
// two sentinels, keeping the refusal's own sentence and type.
func refusedReveal(refused *access.Refused) error {
	if refused.Answer.Decision == access.Denied {
		return fmt.Errorf("%w: %w", extension.ErrRevealDenied, refused)
	}
	return fmt.Errorf("%w: %w", extension.ErrRevealUnreviewed, refused)
}

// revealSaid writes the process log's line for one reveal through Host.Reveal, in
// revealLine's `reveal:` shape with the extension's key, so one grep finds every
// reveal. Names and words only, escaped and ended by identity.Line.
type revealSaid struct {
	ctx     context.Context
	s       *Server
	cluster string
	attrs   access.Attributes
	key     string
	ext     string
	// console marks a read made with the console's own credentials on the
	// person's behalf (ConfiguredSecret), said as `via=console`.
	console bool
}

func (r revealSaid) line(decision access.Decision, outcome, reason string) {
	line := fmt.Sprintf("reveal: cluster=%q kind=secrets namespace=%s name=%s key=%s extension=%s decision=%s outcome=%s",
		r.cluster, r.attrs.Namespace, r.attrs.Name, r.key, r.ext, decision, outcome)
	if r.console {
		line += " via=console"
	}
	if reason != "" {
		line += " reason=" + reason
	}
	r.s.log("%s", identity.Line(r.ctx, "%s", line))
}
