package terminal

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A kubeconfig context name is somebody else's data - whatever a colleague, a
// cloud CLI or a merged kubeconfig put there - and the prompt is the one place
// it is handed to a shell's own expansion rules.

// hostileContexts are names a kubeconfig can carry, each aimed at one of the
// three prompt languages: bash's `$(…)`, backticks, `\` escapes and `!`;
// PowerShell's quotes, ASCII and the four typographic ones it also ends a
// literal on; cmd's `$` codes.
var hostileContexts = []string{
	"prod-$(touch pwned)",
	"prod-`touch pwned`",
	`prod-\$(id)`,
	"prod-!!",
	"prod'; Remove-Item C:\\ -Recurse; '",
	"prod\u2019; Remove-Item C:\\ -Recurse; \u2018",
	"prod-$E[31m$P",
	"prod\nexit",
	"prod %PATH% & calc",
}

// promptSafe is what a prompt tag may hold once promptTag has had it. The
// set is `[A-Za-z0-9._@:-]`, plus `_`: GKE's contexts are
// `gke_<project>_<zone>_<name>`, and `_` means nothing in any of the three
// prompt languages. The read-only marker's space is the one other character,
// and promptTag writes it itself.
func promptSafe(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
		strings.ContainsRune("._@:-_", r)
}

func TestAPromptTagKeepsOnlyCharactersNoShellReadsAsSyntax(t *testing.T) {
	for _, name := range hostileContexts {
		for _, readOnly := range []bool{false, true} {
			tag, _ := promptTag(name, readOnly)
			short := strings.TrimSuffix(tag, " ro")
			if readOnly && short == tag {
				t.Errorf("%q, read-only: the tag %q lost its marker", name, tag)
			}
			for _, r := range short {
				if !promptSafe(r) {
					t.Errorf("%q: the prompt tag %q carries %q, which a shell can read as syntax", name, tag, r)
				}
			}
		}
	}
}

// And the names that are only names come through as they are: shortening an
// ARN and keeping a GKE context legible is what the tag is for.
func TestAnOrdinaryContextNameIsDrawnAsItIs(t *testing.T) {
	for name, want := range map[string]string{
		"arn:aws:eks:eu-west-1:123456789012:cluster/example-prod-eks": "example-prod-eks",
		"gke_my-project_europe-west1_prod":                            "gke_my-project_europe-west1_prod",
		"kind-dev":                                                    "kind-dev",
		"admin@prod.example.com":                                      "admin@prod.example.com",
		"docker-desktop":                                              "docker-desktop",
	} {
		if tag, _ := promptTag(name, false); tag != want {
			t.Errorf("promptTag(%q) = %q, want %q", name, tag, want)
		}
	}
}

// **A real shell, not a reading**: bash executes `$(…)` in PS1 at every prompt
// (promptvars is on by default), so a context called `prod-$(touch pwned)` would
// run `touch` the moment the dock's shell drew its first prompt. Run here against
// the real bash, interactive, with the prompt the session exports. It needs
// bash on PATH and nothing else, so it runs on Linux and macOS, and skips where
// there is none (a Windows runner without Git Bash).
func TestAContextNameRunsNothingWhenBashDrawsThePrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the POSIX prompt is not what a Windows dock shell draws; PowerShell's is tested below")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash on PATH: " + err.Error())
	}
	for _, name := range []string{"prod-$(touch pwned)", "prod-`touch pwned`"} {
		dir := t.TempDir()
		cmd := exec.Command(bash, "--norc", "--noprofile", "-i")
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "PS1=" + buildPrompt(name, false)}
		cmd.Stdin = strings.NewReader("exit\n")
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("bash did not run: %v\n%s", err, out.String())
		}
		if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
			t.Errorf("a context called %q ran a command when bash drew the prompt %q",
				name, buildPrompt(name, false))
		}
	}
}

// psQuotes are the characters PowerShell's tokenizer ends a single-quoted
// literal on: the ASCII quote and U+2018 to U+201B (`IsSingleQuote` in its
// CharExtensions). A typographic quote is the one a copy from a document or a
// chat message brings with it.
const psQuotes = "'\u2018\u2019\u201a\u201b"

// psLiteral reads one PowerShell single-quoted literal off the front of s, the
// way PowerShell's tokenizer does - a quote ends the literal unless another
// quote follows it, and a doubled quote stands for one - and answers its value
// and what follows it. ok is false when s does not start with a quote or the
// literal never ends.
//
// It is the check PowerShell itself would make, written in Go so that it runs
// on every host: no `pwsh` is needed to know whether a literal ends where it
// was meant to.
func psLiteral(s string) (value, rest string, ok bool) {
	runes := []rune(s)
	if len(runes) == 0 || !strings.ContainsRune(psQuotes, runes[0]) {
		return "", "", false
	}
	var b strings.Builder
	for i := 1; i < len(runes); i++ {
		if strings.ContainsRune(psQuotes, runes[i]) {
			if i+1 < len(runes) && strings.ContainsRune(psQuotes, runes[i+1]) {
				i++
				b.WriteRune(runes[i])
				continue
			}
			return b.String(), string(runes[i+1:]), true
		}
		b.WriteRune(runes[i])
	}
	return "", "", false
}

func TestEveryKindOfPowerShellQuoteIsDoubled(t *testing.T) {
	for _, name := range hostileContexts {
		quoted := psQuote(name)
		value, rest, ok := psLiteral(quoted)
		if !ok || value != name || rest != "" {
			t.Errorf("psQuote(%q) = %s, which PowerShell reads as the literal %q followed by %q",
				name, quoted, value, rest)
		}
	}
}

// Every literal the launch script writes a name into must hold that name and
// end where it was meant to, with a typographic quote in the name.
func TestATypographicQuoteCannotEndALiteralInTheLaunchScript(t *testing.T) {
	cluster := "prod\u2019; Remove-Item C:\\ -Recurse; \u2018"
	script := string(writeLaunchScript(localRequest{Kind: "pods", Name: "x\u201bz"}, cluster, "kc"))

	line := lineStarting(t, script, "$env:KUBETOWER_CLUSTER = ")
	if value, rest, ok := psLiteral(line); !ok || value != cluster || rest != "" {
		t.Errorf("the cluster's literal reads as %q followed by %q:\n%s", value, rest, script)
	}
	banner := lineStarting(t, script, "Write-Host 'KubeTower")
	if value, rest, ok := psLiteral("'KubeTower" + banner); !ok || value != "KubeTower — "+cluster ||
		rest != " -ForegroundColor Cyan" {
		t.Errorf("the banner's literal reads as %q followed by %q:\n%s", value, rest, script)
	}
	selected := lineStarting(t, script, "Write-Host 'selected")
	if value, rest, ok := psLiteral("'selected" + selected); !ok || value != "selected: pods x\u201bz" ||
		rest != " -ForegroundColor DarkGray" {
		t.Errorf("the selection's literal reads as %q followed by %q:\n%s", value, rest, script)
	}
}

// **And the file is read as UTF-8.** Windows PowerShell 5.1 reads a `.ps1`
// without a byte order mark in the machine's ANSI code page. Read as
// Windows-1252, the UTF-8 bytes of a character such as U+2011 (E2 80 91) come
// out as `â€‘` - and `‘` is a quote, so a name the escaping above handled
// correctly would end its literal anyway. The BOM is what makes 5.1 read the
// bytes that were written.
func TestTheLaunchScriptIsWrittenWithAUTF8ByteOrderMark(t *testing.T) {
	script := writeLaunchScript(localRequest{}, "prod", "kc")
	if !bytes.HasPrefix(script, []byte{0xEF, 0xBB, 0xBF}) {
		t.Errorf("the launch script does not start with a UTF-8 byte order mark: % x", script[:min(len(script), 8)])
	}
	if bytes.Count(script, []byte{0xEF, 0xBB, 0xBF}) != 1 {
		t.Error("the byte order mark is written more than once")
	}
}

// The PowerShell prompt function puts the tag in a single-quoted literal of its
// own; with every quote doubled and the tag reduced to safe characters, that
// literal holds exactly the tag.
func TestThePowerShellPromptKeepsTheTagInsideItsLiteral(t *testing.T) {
	for _, name := range hostileContexts {
		prompt := powerShellPrompt(name, false)
		const head = "$global:KubeTowerCluster = "
		if !strings.HasPrefix(prompt, head) {
			t.Fatalf("the prompt no longer starts with %q: %s", head, prompt)
		}
		tag, _ := promptTag(name, false)
		value, rest, ok := psLiteral(strings.TrimPrefix(prompt, head))
		if !ok || value != tag || !strings.HasPrefix(rest, "; function global:prompt") {
			t.Errorf("%q: the prompt's literal reads as %q followed by %q", name, value, rest)
		}
	}
}

func lineStarting(t *testing.T, script, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	t.Fatalf("no line starts with %q:\n%s", prefix, script)
	return ""
}
