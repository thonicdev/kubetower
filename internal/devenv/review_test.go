package devenv

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sourcesTheWholeFile matches the other way to hand a process every line of
// .env: the shell's own `.` or `source`, usually inside `set -a`.
// `set -a && . ./.env && set +a && go run ./cmd/devwatch -dotenv .env ...`
// leaks the whole file just as Task's `dotenv` key would.
var sourcesTheWholeFile = regexp.MustCompile(`(^|[\s;&|(])(\.|source)\s+["']?(\./)?\.env\b|set\s+-a\b|set\s+-o\s+allexport`)

// The pattern itself, so a regexp that matches nothing cannot make the Taskfile
// check pass.
func TestThePatternForSourcingDotEnv(t *testing.T) {
	for _, bad := range []string{
		"set -a && . ./.env && set +a && go run ./cmd/devwatch",
		"source .env",
		". .env",
		"x; . './.env'",
		"set -o allexport",
	} {
		if !sourcesTheWholeFile.MatchString(bad) {
			t.Errorf("the pattern misses %q", bad)
		}
	}
	for _, fine := range []string{
		"go run ./cmd/devwatch -dotenv .env -pkg ./cmd/kubetower -- --no-window --unattended",
		"cp .env.example .env",
		"printf '%s' \"$1\" | go run ./cmd/devpassword",
	} {
		if sourcesTheWholeFile.MatchString(fine) {
			t.Errorf("the pattern flags %q, which loads nothing", fine)
		}
	}
}

// TestAStateDirTheFileCannotMeanIsRefused: .env is read literally, so a value
// that only makes sense expanded would become a directory relative to the
// clone, holding credentials.json. Refused, unless the shell overrides it.
func TestAStateDirTheFileCannotMeanIsRefused(t *testing.T) {
	absolute := filepath.ToSlash(filepath.Join(t.TempDir(), ".kubetower-dev"))
	for _, value := range []string{
		"${USERPROFILE}/.kubetower-dev",
		"$HOME/.kubetower-dev",
		"~/.kubetower-dev",
		".kubetower-dev",
		absolute + " # mine",
	} {
		f := Parse([]byte("KT_STATE_DIR=" + value + "\n"))
		f.Path = ".env"
		_, _, _, err := Apply([]string{"PATH=/usr/bin"}, f)
		if err == nil || !strings.Contains(err.Error(), "KT_STATE_DIR") {
			t.Errorf("KT_STATE_DIR=%s was taken (err %v); read literally, it is not a place anybody meant",
				value, err)
		}
		if _, _, _, err := Apply([]string{"KT_STATE_DIR=" + absolute}, f); err != nil {
			t.Errorf("KT_STATE_DIR=%s was refused although the shell overrides it: %v", value, err)
		}
	}
	f := Parse([]byte("KT_STATE_DIR='" + absolute + "'\n"))
	if _, added, _, err := Apply(nil, f); err != nil || len(added) != 1 {
		t.Errorf("an absolute KT_STATE_DIR was not taken: added %v, err %v", added, err)
	}
}

// TestTheReportTellsTheEmptyAnswersApart, and names what it skipped without
// ever printing a value.
func TestTheReportTellsTheEmptyAnswersApart(t *testing.T) {
	missing, err := Load(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatal(err)
	}
	none := Parse([]byte("KT_USER=admin\n"))
	none.Path = ".env"
	shadowedFile := Parse([]byte("KT_SESSION_SECRET=0123456789abcdef0123456789abcdef\n"))
	shadowedFile.Path = ".env"

	a := strings.Join(missing.Report(nil, nil), "\n")
	b := strings.Join(none.Report(nil, nil), "\n")
	c := strings.Join(shadowedFile.Report(nil, []string{"KT_SESSION_SECRET"}), "\n")
	if a == b || b == c || a == c {
		t.Errorf("two empty answers read the same:\nmissing: %s\nnone: %s\nshadowed: %s", a, b, c)
	}
	if !strings.HasPrefix(a, "no ") || !strings.Contains(b, "sets none of") ||
		!strings.Contains(c, "the shell already sets KT_SESSION_SECRET") {
		t.Errorf("unexpected wording:\nmissing: %s\nnone: %s\nshadowed: %s", a, b, c)
	}

	guarded := Parse([]byte("KT_READONLY_CLUSTERS=prod-eu\nKT_FORWARD_PORTS=30000-30031\nKT_EMPTY=\nOTHER=x\n"))
	guarded.Path = ".env"
	said := strings.Join(guarded.Report(nil, nil), "\n")
	if !strings.Contains(said, "KT_READONLY_CLUSTERS") || !strings.Contains(said, "KT_FORWARD_PORTS") {
		t.Errorf("the skipped KT_ names are not reported: %s", said)
	}
	for _, leak := range []string{"prod-eu", "30000", "KT_EMPTY", "OTHER"} {
		if strings.Contains(said, leak) {
			t.Errorf("the report carries %q (a value, an empty key or a non-KT_ name): %s", leak, said)
		}
	}
}

// A byte-order mark and `export<TAB>` are read like any other line.
func TestParserEdges(t *testing.T) {
	bom := string([]byte{0xEF, 0xBB, 0xBF})
	for _, content := range []string{bom + "KT_SESSION_SECRET=abc\n", "export\tKT_SESSION_SECRET=abc\n"} {
		if got := Allowed([]byte(content)); got["KT_SESSION_SECRET"] != "abc" {
			t.Errorf("%q gave %v", content, got)
		}
	}
}
