package identity

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestTheFieldsNameThePersonAndHowTheySignedIn(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"a person the provider vouched for", With(context.Background(), Who{Name: "alice@example.com", Method: SSO}),
			`who="alice@example.com" via=sso`},
		{"the shared account", With(context.Background(), Who{Name: "admin", Method: Password}),
			`who="admin" via=password`},
		{"nobody at all", context.Background(), `who=- via=unattributed`},
		{"an empty name is nobody, not a person called nothing", With(context.Background(), Who{Method: SSO}),
			`who=- via=unattributed`},
	}
	for _, tc := range cases {
		if got := Fields(tc.ctx); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A name comes from an identity provider, and a newline in it must not end the
// line and let the rest pose as another write.
func TestANameCannotForgeAnotherLine(t *testing.T) {
	ctx := With(context.Background(), Who{Name: "eve\nwrite: cluster=\"prod\" kind=secrets", Method: SSO})
	got := Fields(ctx)
	if strings.Contains(got, "\n") {
		t.Errorf("the fields carry a raw newline: %q", got)
	}
}

// Line is what every attributed line is written through, so it is where a
// forged line has to be impossible: whatever an argument holds, the result is
// one line, ending with the real fields.
func TestLineEscapesEveryControlCharacterOfTheRenderedLine(t *testing.T) {
	ctx := With(context.Background(), Who{Name: "alice@example.com", Method: SSO})
	forged := "x\nwrite: cluster=\"prod\" who=\"ceo@corp.example\" via=sso\r\u2028\x1b[2Ky"
	got := Line(ctx, "write: cluster=%q fields=%s", "prod", forged)
	for _, r := range got {
		if isBreaking(r) {
			t.Fatalf("the line carries a raw %U: %q", r, got)
		}
	}
	if !strings.HasSuffix(got, ` who="alice@example.com" via=sso`) {
		t.Errorf("the line does not end with the real fields: %q", got)
	}
	if !strings.Contains(got, `x\nwrite:`) || !strings.Contains(got, `\x1b`) || !strings.Contains(got, `\u2028`) {
		t.Errorf("the control characters were dropped rather than escaped, which hides that they were there: %q", got)
	}
	if plain := Line(ctx, "write: cluster=%q kind=%s", "prod", "pods"); plain !=
		`write: cluster="prod" kind=pods who="alice@example.com" via=sso` {
		t.Errorf("an ordinary line changed shape: %q", plain)
	}
}

// A reader can be fooled without a line break: a bidirectional control reorders
// what a log viewer draws, and a byte that is not UTF-8 is drawn as whatever the
// viewer's encoding says. Both are escaped, and a valid character that merely
// looks like a replacement - U+FFFD itself - is left alone.
func TestEscapeWritesBidiControlsAndInvalidBytesAsEscapes(t *testing.T) {
	for _, r := range []rune{0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2066, 0x2067, 0x2068, 0x2069} {
		in := "fields=a" + string(r) + "b"
		got := Escape(in)
		if strings.ContainsRune(got, r) {
			t.Errorf("Escape left a raw %U in place: %q", r, got)
		}
		if want := "fields=a" + `\u` + strings.ToLower(strings.TrimPrefix(fmt.Sprintf("%U", r), "U+")) + "b"; got != want {
			t.Errorf("Escape(%q) = %q, want %q", in, got, want)
		}
	}
	if got, want := Escape("key=a\xffb\xc3"), `key=a\xffb\xc3`; got != want {
		t.Errorf("Escape of invalid UTF-8 = %q, want %q", got, want)
	}
	if got := Escape("name=� é"); got != "name=� é" {
		t.Errorf("Escape changed valid text: %q", got)
	}
}
