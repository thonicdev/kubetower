package cluster

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Finding the kubeconfigs nobody named.
//
// `KUBECONFIG` names files. The container names one, `/home/kubetower/.kube/
// config`, and mounts the operator's whole `~/.kube` behind it; if the
// kubeconfigs there go by other names, every named path is missing, `Load`
// refuses, and start-up fails naming the file it wanted and not the ones beside
// it.
//
// Discovery only **extends the list**. The merge stays client-go's
// `ClientConfigLoadingRules.Precedence`, so the console and the dock's
// `kubectl` agree on which `prod` they mean, and discovered files come
// **after** the named ones, so an explicit `KUBECONFIG` wins.
//
// The order is sorted because first-wins makes it the answer when two files
// declare the same context: it must not vary between restarts. A stable order
// can still be wrong, which is why the shadowing survey reports it.

const (
	// maxKubeconfigBytes is the largest file this will try to parse.
	// A kubeconfig is a few tens of kilobytes even with embedded certificates;
	// this bounds what a scan costs when `~/.kube` holds something large.
	maxKubeconfigBytes = 4 << 20

	// maxScanned is how many files one directory is examined for.
	//
	// A ceiling, not a tuning knob: the scan runs before anything is served.
	// Reaching it is reported, because a truncated scan and an empty
	// directory must not look the same.
	maxScanned = 200
)

// Discovered is one file the scan looked at.
type Discovered struct {
	// Path is the file.
	Path string
	// Contexts is how many it declares. Zero with an empty Skipped means a
	// kubeconfig with no contexts in it, which is not useful and not an error.
	Contexts int
	// Skipped says why this file is not in the list, and is empty for the ones
	// that are, so "why is my cluster not there" has an answer at start-up.
	Skipped string
}

// Scan is what one directory yielded.
type Scan struct {
	// Dir is the directory examined.
	Dir string
	// Unreadable is why it could not be listed, empty when it could.
	Unreadable string
	// Truncated says the directory held more than maxScanned entries.
	Truncated bool
	// Entries is how many things the directory held, before any filtering.
	// Zero and readable is what Docker Desktop creates when a bind mount's
	// source is missing, which WhyNothing tells apart from a directory of the
	// wrong files.
	Entries int
	// Files is every candidate considered, kept and skipped alike.
	Files []Discovered
}

// Discover extends the list with the kubeconfigs sitting beside the named ones.
//
// The directories examined are the parents of the paths already in the list,
// because what an operator mounts is a directory. Each is examined once.
//
// It never fails: an unlistable directory or an unparsable file is recorded
// and left out. A scan that exists to prevent a refused start-up must not
// cause one.
func (k Kubeconfig) Discover() (Kubeconfig, []Scan) {
	named := map[string]bool{}
	var dirs []string
	seenDir := map[string]bool{}
	for _, path := range k {
		named[filepath.Clean(path)] = true
		dir := filepath.Dir(path)
		if dir == "" || seenDir[dir] {
			continue
		}
		seenDir[dir] = true
		dirs = append(dirs, dir)
	}

	out := append(Kubeconfig(nil), k...)
	var scans []Scan

	for _, dir := range dirs {
		scan := Scan{Dir: dir}
		entries, err := os.ReadDir(dir)
		if err != nil {
			// Not a failure: usually a named file in a missing directory, which
			// `Load` reports better.
			scan.Unreadable = err.Error()
			scans = append(scans, scan)
			continue
		}
		scan.Entries = len(entries)
		if len(entries) > maxScanned {
			scan.Truncated = true
			entries = entries[:maxScanned]
		}

		var found []string
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			if why := unusable(entry, path, named); why != "" {
				if why != skipQuietly {
					scan.Files = append(scan.Files, Discovered{Path: path, Skipped: why})
				}
				continue
			}
			contexts, why := kubeconfigContexts(path)
			if why != "" {
				scan.Files = append(scan.Files, Discovered{Path: path, Skipped: why})
				continue
			}
			if contexts == 0 {
				scan.Files = append(scan.Files, Discovered{
					Path: path, Skipped: "it is a kubeconfig with no contexts in it"})
				continue
			}
			scan.Files = append(scan.Files, Discovered{Path: path, Contexts: contexts})
			found = append(found, path)
		}

		// Sorted after filtering, because first-wins makes this order the
		// answer.
		sort.Strings(found)
		out = append(out, found...)
		scans = append(scans, scan)
	}

	return out, scans
}

// skipQuietly marks a candidate not worth reporting: a directory, or a file
// already named in the list. Reporting those would bury the line an operator
// is looking for.
const skipQuietly = "\x00quiet"

// unusable rejects what cannot be a kubeconfig before it is opened.
//
// Properties of the directory entry only, so a directory of a thousand
// certificates costs a thousand `Stat`s rather than a thousand YAML parses.
func unusable(entry os.DirEntry, path string, named map[string]bool) string {
	if entry.IsDir() {
		// `~/.kube/cache` and `~/.kube/http-cache` hold kubectl's discovery
		// cache, thousands of files and no kubeconfig.
		return skipQuietly
	}
	if named[filepath.Clean(path)] {
		return skipQuietly
	}

	name := entry.Name()
	if strings.HasPrefix(name, ".") {
		return skipQuietly
	}
	// kubectl's `config.lock`, and editors' backups and swap files.
	for _, suffix := range []string{".lock", ".bak", ".swp", ".tmp", "~"} {
		if strings.HasSuffix(name, suffix) {
			return skipQuietly
		}
	}

	info, err := entry.Info()
	if err != nil {
		return "it could not be examined (" + err.Error() + ")"
	}
	if !info.Mode().IsRegular() {
		// A symlink to a kubeconfig resolves to a regular file; a socket or a
		// device does not.
		return skipQuietly
	}
	if info.Size() == 0 {
		return skipQuietly
	}
	if info.Size() > maxKubeconfigBytes {
		return "it is " + strconv.FormatInt(info.Size()/(1<<20), 10) +
			" MiB, which no kubeconfig is"
	}
	return ""
}

// kubeconfigContexts asks how many contexts a candidate declares, and turns a
// parse failure into a sentence.
//
// The reading is `contextsIn` in kubeconfig.go, the one place an operator's
// kubeconfig is opened. Valid YAML that is not a kubeconfig arrives here as
// zero contexts, not as an error.
func kubeconfigContexts(path string) (int, string) {
	contexts, err := contextsIn(path)
	if err != nil {
		return 0, "it is not a kubeconfig (" + firstLineOf(err.Error()) + ")"
	}
	return contexts, ""
}

// firstLineOf keeps a parse error to one line: a YAML error can quote the file,
// and the file holds credentials.
func firstLineOf(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	const cap = 120
	if len(s) > cap {
		s = s[:cap-1] + "…"
	}
	return s
}

// Sentence is what the start-up log says about one directory.
//
// Empty when there is nothing worth saying: a directory whose only file is the
// one already named is the ordinary case and stays silent. Otherwise one line:
// what was added, and what was passed over and why.
func (s Scan) Sentence() string {
	if s.Unreadable != "" {
		return "kubeconfig discovery: " + s.Dir + " could not be listed (" + s.Unreadable +
			"), so only the files named in KUBECONFIG were read"
	}

	var kept, skipped []string
	for _, f := range s.Files {
		if f.Skipped == "" {
			kept = append(kept, filepath.Base(f.Path)+" ("+strconv.Itoa(f.Contexts)+")")
			continue
		}
		skipped = append(skipped, filepath.Base(f.Path)+": "+f.Skipped)
	}
	if len(kept) == 0 && len(skipped) == 0 && !s.Truncated {
		return ""
	}

	out := "kubeconfig discovery in " + s.Dir + ": "
	if len(kept) == 0 {
		out += "nothing else here is a kubeconfig"
	} else {
		out += "also reading " + strings.Join(kept, ", ") + " (contexts in brackets)"
	}
	if len(skipped) > 0 {
		out += "; passed over " + strings.Join(skipped, "; ")
	}
	if s.Truncated {
		out += "; the directory holds more than " + strconv.Itoa(maxScanned) +
			" entries and only the first were examined"
	}
	return out
}

// WhyNothing explains a kubeconfig list that yielded nothing, in the words of
// what was actually on disk.
//
// Discover cannot help a directory with no kubeconfig at all, the commoner
// mistake: Docker Desktop creates a missing bind-mount source empty, and
// `Scan.Sentence` stays quiet when a scan found nothing. This says so.
//
// Facts only: this package does not know it is in a container, so the caller
// adds the advice about mounts.
func WhyNothing(k Kubeconfig, scans []Scan) string {
	var parts []string
	for _, scan := range scans {
		switch {
		case scan.Unreadable != "":
			parts = append(parts, scan.Dir+" could not be listed ("+scan.Unreadable+")")
		case scan.Entries == 0:
			parts = append(parts, scan.Dir+" exists and is empty")
		default:
			kept := 0
			for _, f := range scan.Files {
				if f.Skipped == "" {
					kept++
				}
			}
			// A named file present in this directory counts, though Discover
			// leaves it out of scan.Files. Otherwise the ordinary case, a
			// directory holding exactly the configured kubeconfig, would be
			// described as holding none.
			for _, path := range k {
				if filepath.Dir(path) != scan.Dir {
					continue
				}
				if _, err := os.Stat(path); err == nil {
					kept++
				}
			}
			if kept == 0 {
				parts = append(parts, scan.Dir+" holds "+strconv.Itoa(scan.Entries)+
					" entr"+plural(scan.Entries, "y", "ies")+" and none of them is a kubeconfig")
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "no kubeconfig could be read: " + strings.Join(parts, "; ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
