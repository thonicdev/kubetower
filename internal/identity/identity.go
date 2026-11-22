// Package identity carries who a request is from, from the front door to the
// code that changes a cluster.
//
// The write paths must say who changed a cluster, because the API server's
// audit sees only this console's ServiceAccount. They must not import the
// front door (internal/auth), so both import this leaf instead.
//
// It carries no token and no credential: a name and how it was established,
// which is all the write line needs and nothing a log may not hold.
package identity

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// The three ways a request can have been signed in.
const (
	// SSO is a person the identity provider vouched for, and the name is theirs.
	SSO = "sso"
	// Password is the shared local account. The name names nobody: it is
	// whoever holds the password. The write line says `via=password` so a reader
	// does not take the user name for a person.
	Password = "password"
	// Launch is the same shared account, reached through a one-time code the
	// application minted for a window it opened. It names nobody either: it is
	// whoever can start the application on that machine.
	Launch = "launch"
)

// Who is the person behind a request, as far as this console knows.
type Who struct {
	// Name is the provider's name for the person, or the shared account's user
	// name.
	Name string
	// Method is SSO, Password or Launch.
	Method string
	// Groups are the person's groups as asserted to the API server: the
	// provider's, prefixed, with `system:` names refused at sign-in. Empty for
	// the shared account.
	//
	// Never on the log line: the list is the provider's to publish and can be
	// long, and the name answers who.
	Groups []string
	// Session tells one sign-in of this person from another: an opaque tag
	// derived one-way from a single sign-on session's id, empty for the shared
	// account. A sign-out closes what that session opened, not what another
	// browser of the same person opened. Not a credential (it cannot be turned
	// back into the id), and never on the log line.
	Session string
	// Window is whether the session was opened by the desktop's native window
	// through a one-time launch code rather than a typed password; always with
	// Launch. It names a door, not a person, so Key, Shared and the log line
	// ignore it. It is for a check that wants a factor the password does not give.
	Window bool
}

// Key is who this is as a counter or a registry keys a person: the method and
// the name together, so an SSO account whose name equals the shared account's
// user name does not share its quota or its sign-out.
func (w Who) Key() string { return w.Method + "\x00" + w.Name }

// Shared is whether this is the shared account, whose name is everybody who
// holds the password or can start the application: a share of capacity "per
// person" cannot tell those people apart, so it is not applied to it.
func (w Who) Shared() bool { return w.Method == Password || w.Method == Launch }

type key struct{}

// With returns ctx carrying who.
func With(ctx context.Context, who Who) context.Context {
	return context.WithValue(ctx, key{}, who)
}

// From reads who a context carries, or false when it carries nobody - a test,
// or a write started somewhere no session reached.
func From(ctx context.Context) (Who, bool) {
	who, ok := ctx.Value(key{}).(Who)
	return who, ok && who.Name != ""
}

// Fields renders the two `key=value` pairs every `write:` line ends with.
//
// The name comes from an identity provider and is quoted, so a newline in it
// cannot end the line and forge the next. A context with nobody says so
// (`who=- via=unattributed`) rather than leaving the fields out.
func Fields(ctx context.Context) string {
	who, ok := From(ctx)
	if !ok {
		return "who=- via=unattributed"
	}
	return fmt.Sprintf("who=%q via=%s", who.Name, who.Method)
}

// Line renders one attributed log line: the format with its arguments, **every
// control character in the result escaped**, then Fields(ctx).
//
// The whole rendered line is escaped, not just the name: a manifest apply
// names the field paths it touched, which are keys of the YAML the person
// sent, and a key holding a newline could start a forged line attributed to
// somebody else. Escaping here means no caller has to remember to.
func Line(ctx context.Context, format string, args ...any) string {
	return Escape(fmt.Sprintf(format, args...)) + " " + Fields(ctx)
}

// Escape writes every control character as an escape - `\n`, `\r`, `\t`, or
// `\xNN` / `\uNNNN` - including the three Unicode line breaks a log viewer
// may honor (NEL, LS, PS). Everything else is left as it was, so an ordinary
// line reads exactly as it did.
//
// Two more are escaped because a reader can be fooled without a line break:
// the bidirectional controls (U+202A-U+202E, U+2066-U+2069), which can make a
// viewer draw a field after one it was written before (Trojan Source), and
// invalid UTF-8, written `\xNN` so the bytes are the text a reader sees.
func Escape(s string) string {
	if strings.IndexFunc(s, isBreaking) < 0 && utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i-1])
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x80 && isBreaking(r):
			fmt.Fprintf(&b, `\x%02x`, r)
		case isBreaking(r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isBreaking is every rune Escape writes as an escape: the C0 controls and DEL,
// NEL, the two Unicode line breaks, and the bidirectional embeddings, overrides
// and isolates.
func isBreaking(r rune) bool {
	return r < 0x20 || r == 0x7f || r == 0x85 || r == 0x2028 || r == 0x2029 ||
		(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}
