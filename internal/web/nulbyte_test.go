package web

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// No source file in this repository contains a NUL byte.
//
// **This is a tooling defect, not a coding one.** An editing pipeline can write a
// literal `\x00` where a space was typed, in Go and in JavaScript sources; a Go file
// then fails with `illegal character NUL`.
//
// The JavaScript cases are the nastier half, because a NUL inside a string literal
// is *valid* JavaScript: used as a key separator it works, since the write and the
// read agree, so nothing fails. What it costs is debugging a feature that looks
// broken from outside - a hand-seeded key with a space in it does not match a
// stored key with a NUL, and the two are indistinguishable in every tool that
// prints them.
//
// It also makes `grep` treat the file as binary, which silently empties any sweep
// over it.
//
// So: a byte no source file in this project has any reason to contain, checked once
// over the tree. Cheap, and it turns a class of invisible corruption into a red
// build.
func TestNoSourceFileContainsANULByte(t *testing.T) {
	suffixes := []string{".go", ".js", ".css", ".html", ".md", ".yml", ".yaml", ".ps1", ".mod", ".sum", ".example"}

	var offenders []string
	root := filepath.Join("..", "..")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		keep := false
		for _, s := range suffixes {
			if strings.HasSuffix(path, s) {
				keep = true
				break
			}
		}
		if !keep {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if n := bytes.Count(raw, []byte{0}); n > 0 {
			line := bytes.Count(raw[:bytes.IndexByte(raw, 0)], []byte("\n")) + 1
			offenders = append(offenders, filepath.ToSlash(path)+" — "+itoaNul(n)+" NUL, first at line "+itoaNul(line))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("%d source file(s) contain a NUL byte:\n  %s\n\n"+
			"In Go this is a compile error and announces itself. In JavaScript it is a valid "+
			"string character, so the file works, `grep` treats it as binary and silently "+
			"matches nothing, and the only symptom is a feature that behaves strangely from "+
			"outside. Replace it with whatever character was meant — a space, every time so "+
			"far.", len(offenders), strings.Join(offenders, "\n  "))
	}
}

func itoaNul(n int) string {
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
