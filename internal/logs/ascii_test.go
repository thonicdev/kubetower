package logs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// What this process logs is ASCII, because the console it lands in is not ours to
// choose.
//
// Read through `docker compose logs`, valid UTF-8 such as `→` (U+2192) or an em
// dash is drawn by a Windows console on code page 850 or 1252 as two or three
// parasites each. Nothing is corrupted; the terminal simply does not read what
// we write.
//
// This is the same family as a PowerShell pipe adding `\r` and Compose eating
// `$`: the environment mangles what we emit, and the
// only reliable answer is to emit less. It is deliberately **not** a rule about
// the rest of the project — an em dash in a comment, a template or a page is read
// in an editor or a browser, both of which have been UTF-8 for twenty years. It is
// a rule about the one output whose renderer is a terminal.
//
// This test exists so a new log line argues with
// something, because the em dash is the house style everywhere else in this
// codebase and reaching for one here is the natural mistake.
func TestNothingThisProcessLogsIsUnprintableInATerminal(t *testing.T) {
	// The call shapes this project logs through. `say.Log` is internal/statefile's
	// complainer, which is how the four state stores report.
	calls := regexp.MustCompile(`log\.Printf|log\.Fatalf|log\.Println|logf\(|\.logf\(|say\.Log`)

	var offenders []string
	err := filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if name := info.Name(); name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if !calls.MatchString(line) {
				continue
			}
			for _, r := range line {
				if r > 127 {
					offenders = append(offenders,
						filepath.ToSlash(path)+":"+itoa(i+1)+" contains "+string(r))
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("%d log line(s) carry a character a Windows console cannot draw:\n  %s\n\n"+
			"Use `-` for an em dash and `->` for an arrow. The operator reads these through "+
			"`docker compose logs`, and on code page 850 or 1252 each one becomes two or "+
			"three parasites — which reads as corruption rather than as a font.",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// itoa without importing strconv for one call in a test.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
