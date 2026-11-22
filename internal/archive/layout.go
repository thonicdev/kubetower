package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// dayLayout is the name of a day directory; it sorts lexically in date order.
const dayLayout = "2006-01-02"

// dayPattern is what the pruner will consent to remove. Anything else in the
// root, settings.json included, is left alone.
var dayPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// safeSegment is a path component that can be used verbatim: no separator, no
// dot-dot, no colon (which Windows refuses and an EKS context name is full of),
// nothing a shell or a filesystem will reinterpret.
var safeSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,110}$`)

// segment turns any name into one path component.
//
// A safe name passes through unchanged. Any other (an EKS ARN context has
// colons and a slash) is sanitized and suffixed with eight hex digits of its
// SHA-256, so two names that sanitize alike never share a directory.
func segment(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "unnamed-" + shortHash(name)
	}
	if safeSegment.MatchString(trimmed) && trimmed != "." && trimmed != ".." {
		return trimmed
	}
	var b strings.Builder
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	cleaned := strings.Trim(b.String(), "-._")
	if len(cleaned) > 80 {
		cleaned = cleaned[:80]
	}
	if cleaned == "" {
		cleaned = "x"
	}
	return cleaned + "-" + shortHash(trimmed)
}

func shortHash(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:4])
}

// dayDir is <root>/<day>.
func (a *Archive) dayDir(day string) string { return filepath.Join(a.root, day) }

// clusterDir is <root>/<day>/<cluster>, created on demand together with a file
// that records the context name verbatim, since the directory name may be
// sanitized and hashed.
func (a *Archive) clusterDir(day, cluster string) (string, error) {
	if a.root == "" {
		return "", fmt.Errorf("the archive has no directory")
	}
	if strings.TrimSpace(cluster) == "" {
		return "", fmt.Errorf("a cluster name is required")
	}
	dir := filepath.Join(a.root, day, segment(cluster))
	if err := a.inRoot(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return "", err
	}
	marker := filepath.Join(dir, clusterFileName)
	if _, err := os.Stat(marker); os.IsNotExist(err) {
		if err := os.WriteFile(marker, []byte(cluster+"\n"), filePerm); err != nil {
			// Not fatal: the data is still recorded, only the display name is
			// lost, and readCluster falls back to the directory name.
			a.logf("archive: %s could not be written: %v", marker, err)
		}
	}
	return dir, nil
}

// readCluster is the context name a cluster directory holds, falling back to
// the directory name so a reader never shows nothing.
func readCluster(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, clusterFileName))
	if err == nil {
		if name := strings.TrimSpace(string(raw)); name != "" {
			return name
		}
	}
	return filepath.Base(dir)
}

// inRoot is the guard that makes this package unable to delete anything it does
// not own. Every path it creates or removes goes through it: a path not
// strictly inside the archive root is refused before any filesystem call. The
// root itself is refused too, since removing it would take settings.json.
func (a *Archive) inRoot(path string) error {
	if a.root == "" {
		return fmt.Errorf("the archive has no directory")
	}
	root := filepath.Clean(a.root)
	switch root {
	case "", ".", string(filepath.Separator):
		return fmt.Errorf("refusing to work in %q: that is not an archive root", root)
	}
	target := filepath.Clean(path)
	if !filepath.IsAbs(target) && filepath.IsAbs(root) {
		target = filepath.Clean(filepath.Join(root, path))
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("%s cannot be placed relative to the archive root %s: %w", path, root, err)
	}
	if rel == "." {
		return fmt.Errorf("refusing to operate on the archive root %s itself", root)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to operate on %s: it is outside the archive root %s", target, root)
	}
	return nil
}

// days lists the day directories that exist, oldest first, skipping anything
// that is not day-shaped.
//
// An absent root is ordinary (created on the first write) and answers
// `nil, nil`. A root that will not list is an error: callers would otherwise
// read it as empty, and retention and the size ceiling would silently stop.
func (a *Archive) days() ([]string, error) {
	if a.root == "" {
		return nil, nil
	}
	entries, err := a.readDir(a.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		a.logf("archive: %s could not be listed: %v", a.root, err)
		return nil, fmt.Errorf("the archive directory %s exists and could not be listed: %w", a.root, err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || !dayPattern.MatchString(e.Name()) {
			continue
		}
		if _, err := time.Parse(dayLayout, e.Name()); err != nil {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// daysBetween narrows the day list to a time range, both bounds optional,
// compared as UTC days like the directories.
func daysBetween(days []string, since, until time.Time) []string {
	out := make([]string, 0, len(days))
	var lo, hi string
	if !since.IsZero() {
		lo = since.UTC().Format(dayLayout)
	}
	if !until.IsZero() {
		hi = until.UTC().Format(dayLayout)
	}
	for _, day := range days {
		if lo != "" && day < lo {
			continue
		}
		if hi != "" && day > hi {
			continue
		}
		out = append(out, day)
	}
	return out
}
