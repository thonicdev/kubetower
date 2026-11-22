package health

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The AWS CLI's assume-role cache, `~/.aws/cli/cache/<sha1>.json`, is written
// with no O_TRUNC, no lock and no atomic rename. Two `aws` processes writing one
// entry (several contexts on one profile probed at once, or the host's CLI on a
// bind-mounted `~/.aws`) can leave two JSON documents end to end, and the
// profile stays dead until the file is deleted.
//
// The error carries nothing: the CLI prints a bare SHA-1 and client-go keeps
// only "exec: executable aws failed with exit code 255". So the cache is
// diagnosed by looking: an entry that does not parse is corrupt.

// credCacheMaxEntries bounds the scan of a directory outside this process's
// control, so a page render never walks thousands of files.
const credCacheMaxEntries = 256

// credCacheMaxBytes is the largest file worth reading: a credential document is
// about 1.5KB, so anything past this is not one.
const credCacheMaxBytes = 64 << 10

// hostCredDirEnv names the operator's own cache, mounted read-only into a
// container.
//
// In a container the console's own cache is a private volume with a directory
// per context (internal/cluster/awscache.go), which hides the operator's cache;
// that one can still break their terminal. It is mounted read-only so this
// process can name a broken file without becoming one of its writers. Unset on
// the host, where the first directory is the operator's own.
const hostCredDirEnv = "KT_HOST_AWS_CACHE"

// CredentialCache is what the assume-role cache looks like on disk right now.
//
// It carries file names only, never contents: the files hold live temporary
// credentials.
type CredentialCache struct {
	// Dir is where this looked, so the page can name the file to delete.
	Dir string `json:"dir,omitempty"`
	// Total is how many entries were examined.
	Total int `json:"total"`
	// Corrupt is the entries that do not parse, sorted.
	Corrupt []string `json:"corrupt,omitempty"`
	// Unreadable is entries that could not be opened at all: a permissions
	// problem, with a different fix from a corruption.
	Unreadable []string `json:"unreadable,omitempty"`
	// Host marks a report about the operator's own cache rather than this
	// process's: a broken entry there breaks their terminal, not the console's
	// clusters, so the sentence differs.
	Host bool `json:"host,omitempty"`
	// Checked is false when there was no directory to look in, the ordinary
	// case with no assume-role profiles. It is not a finding.
	Checked bool `json:"checked"`
	// Problem is why an existing directory could not be listed; empty when
	// nothing went wrong or the directory is absent.
	//
	// It is a third answer, as Session.Checked is: without it a directory that
	// refuses to list would draw no banner, for the one failure the error itself
	// cannot diagnose. An absent directory stays silent, since that is the
	// ordinary state for an operator who only uses SSO.
	Problem string `json:"problem,omitempty"`
}

// Broken reports whether there is a file to name for deletion. It stays false
// when the directory could not be listed: there is no file to name then.
func (c CredentialCache) Broken() bool {
	return len(c.Corrupt) > 0 || len(c.Unreadable) > 0
}

// Unknown reports that this process could not look. See Problem.
func (c CredentialCache) Unknown() bool { return c.Problem != "" }

// CredentialCacheDir is the directory scanned, derived from the SSO cache path
// so there is one answer to "where is ~/.aws".
func (h *Health) CredentialCacheDir() string {
	if h.credDir != "" {
		return h.credDir
	}
	if h.awsDir == "" {
		return ""
	}
	// awsDir is <aws>/sso/cache; the credential cache is <aws>/cli/cache.
	aws := filepath.Dir(filepath.Dir(h.awsDir))
	return filepath.Join(aws, "cli", "cache")
}

// CredentialCache scans the cache and names what is wrong with it. A failure
// to list an existing directory is reported in Problem; an absent or empty one
// is not a finding.
func (h *Health) CredentialCache() CredentialCache {
	return h.scanCredentialCache(h.CredentialCacheDir(), false)
}

// HostCredentialCache is the same scan over the operator's own cache, when this
// process can see it. See hostCredDirEnv.
func (h *Health) HostCredentialCache() CredentialCache {
	dir := strings.TrimSpace(os.Getenv(hostCredDirEnv))
	if h.hostCredDir != "" {
		dir = h.hostCredDir
	}
	report := h.scanCredentialCache(dir, true)
	return report
}

func (h *Health) scanCredentialCache(dir string, host bool) CredentialCache {
	if dir == "" {
		return CredentialCache{Host: host}
	}
	// h.readDir, so a test can produce a directory that exists and will not
	// list, which no filesystem trick does portably.
	entries, err := h.readDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// No assume-role profiles: an answer, not a failure to look.
			return CredentialCache{Dir: dir, Host: host}
		}
		// The reason without the path: Dir carries it, and repeating it pushes
		// the banner's reason past its 200-character cut.
		return CredentialCache{Dir: dir, Host: host, Problem: reasonOnly(err)}
	}

	out := CredentialCache{Dir: dir, Checked: true, Host: host}
	for _, entry := range entries {
		if out.Total >= credCacheMaxEntries {
			break
		}
		name := entry.Name()
		// `.json` only: the CLI's `<sha1>.json.corrupt.bak` copies are expected
		// to be unparseable, and deleting them changes nothing.
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		if info, err := entry.Info(); err == nil && info.Size() > credCacheMaxBytes {
			continue
		}
		out.Total++

		raw, err := h.readFile(filepath.Join(dir, name))
		if err != nil {
			out.Unreadable = append(out.Unreadable, name)
			continue
		}
		// json.Valid rejects trailing data, which is the signature of two
		// documents end to end; the fields inside do not matter.
		if !json.Valid(raw) {
			out.Corrupt = append(out.Corrupt, name)
		}
	}
	sort.Strings(out.Corrupt)
	sort.Strings(out.Unreadable)
	return out
}
