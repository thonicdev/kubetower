package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/thonicdev/kubetower/internal/ekstoken"
)

// Giving each context its own AWS CLI credential cache.
//
// An EKS context's exec block is `aws eks get-token`, and a profile that
// assumes a role caches the result in `~/.aws/cli/cache/<sha1>.json`. The CLI
// writes that file with no lock, no `O_TRUNC` and no atomic rename, so two
// concurrent writers can leave two JSON documents end to end, and the profile
// then fails until the file is deleted.
//
// Several contexts routinely share one profile, so the fix removes the
// sharing: a lock could not cover the operator's own terminals, which are
// other processes. The lever is `HOME`: the CLI expands `~` from `HOME`
// (`USERPROFILE` on Windows, so both are set), so a per-context `HOME` in the
// exec environment is a per-context cache, with no cooperation from the CLI.
//
// Two things must not move with it:
//
//   - `~/.aws/config` and `~/.aws/credentials`: `AWS_CONFIG_FILE` and
//     `AWS_SHARED_CREDENTIALS_FILE` override the `~` default, and are set here
//     when the process environment does not already set them.
//   - `~/.aws/sso/cache`, the SSO session token, which must stay shared for
//     renewal and has **no environment variable**. The real directory is
//     linked back into the new home; if the link cannot be made, nothing is
//     redirected, rather than fixing the assume-role cache by breaking SSO.
//
// Not fixed: the SSO token cache uses the same unlocked writer, but it cannot
// be separated without breaking renewal, and a token refresh is far rarer.
//
// The cost is one AssumeRole per context per token lifetime, where a shared
// cache makes one per profile.

// awsHomeDirName is the directory the per-context homes are created under,
// inside os.TempDir().
//
// Temporary rather than the state directory: these are live STS credentials,
// which should go away with the process, and a cache that starts empty cannot
// start corrupt. A restart costs one AssumeRole per context in use.
const awsHomeDirName = "kubetower-aws"

// awsHomeKeyHexLen is how much of the context's digest goes in its directory
// name. Twelve hex characters is 48 bits: no collision among a kubeconfig's
// contexts, and short enough to stay readable.
const awsHomeKeyHexLen = 12

// awsHomeNameLen caps the readable half of a directory name. An EKS context is
// often named after its ARN; the digest makes the name unique, so this half only
// has to be recognisable.
const awsHomeNameLen = 48

// awsHome is where the AWS files that must stay shared live, and where a
// per-context HOME may be built.
//
// A value rather than package-level functions so a test can point every path at
// a temporary directory. An empty Base disables the separation: with no home
// directory there are no shared files to point the redirect back at.
type awsHome struct {
	// Base is the directory per-context homes are created under.
	Base string
	// SSODir is the operator's own ~/.aws/sso, linked into every per-context
	// home so the session token stays shared and renewable, whether or not it
	// exists yet (linkSSO). Empty shares nothing.
	SSODir string
	// ConfigFile and CredentialsFile are the shared ~/.aws/config and
	// ~/.aws/credentials. Set only when the process environment does not already
	// name them, since the exec plugin inherits it.
	ConfigFile      string
	CredentialsFile string
	// Off is why the separation is not happening at all, or empty when it is.
	//
	// **Decided once for the whole process**: the SSO link fails the same way
	// for every context (a Windows host without the symlink privilege), so one
	// probe and one sentence, and no per-context directory nothing will use.
	Off string
}

// defaultAWSHome reads the machine.
func defaultAWSHome() awsHome {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// No home means no shared config to point back at: leave HOME alone.
		return awsHome{}
	}
	aws := filepath.Join(home, ".aws")

	// SSODir is set whether or not it exists yet: a machine that has never run
	// `aws sso login` has none at start, and the first login creates it later.
	// separate() makes it for an `aws` plugin, so that login is seen through the
	// link instead of being missed until a restart.
	h := awsHome{Base: filepath.Join(os.TempDir(), awsHomeDirName), SSODir: filepath.Join(aws, "sso")}
	if info, err := os.Stat(h.SSODir); err == nil && info.IsDir() {
		// Asked once, before any context: a private home without the SSO token
		// cache authenticates nothing on an SSO profile, so without the link the
		// whole mechanism is off.
		if err := canSymlink(h.Base); err != nil {
			h.Off = fmt.Sprintf("this host cannot create the symlink that shares the SSO token "+
				"cache (%v), so every context keeps the shared AWS credential cache. On Windows "+
				"that is the symlink privilege; Developer Mode grants it", err)
		}
	}
	if os.Getenv("AWS_CONFIG_FILE") == "" {
		h.ConfigFile = filepath.Join(aws, "config")
	}
	if os.Getenv("AWS_SHARED_CREDENTIALS_FILE") == "" {
		h.CredentialsFile = filepath.Join(aws, "credentials")
	}
	return h
}

// separate gives a context's exec plugin its own AWS CLI credential cache, by
// giving it its own HOME.
//
// It returns the directory it settled on and whether anything changed, so the
// caller can say so out loud.
//
// A context with no exec block has no cache to corrupt and is untouched. An
// exec block that sets HOME itself is untouched too: the operator meant it.
func (h awsHome) separate(cfg *rest.Config, context string) (string, bool, error) {
	if cfg == nil || cfg.ExecProvider == nil || h.Base == "" {
		return "", false, nil
	}
	if hasEnv(cfg.ExecProvider.Env, "HOME") {
		return "", false, nil
	}
	if h.Off != "" {
		// Off was established and announced once at startup: not a per-cluster
		// error.
		return "", false, nil
	}

	dir := filepath.Join(h.Base, awsHomeKey(context))
	// 0700: these directories hold live credentials, and Base is under a
	// world-writable temporary directory.
	if err := os.MkdirAll(filepath.Join(dir, ".aws"), 0o700); err != nil {
		return "", false, fmt.Errorf("making an AWS home for %q: %w", context, err)
	}
	if h.SSODir != "" {
		// Before the environment is written: without the SSO cache the redirect
		// must not happen.
		if err := linkSSO(h.SSODir, filepath.Join(dir, ".aws", "sso"), ekstoken.UsesAWSCLI(cfg.ExecProvider)); err != nil {
			// Remove the unused directory: nothing prunes this temporary tree.
			_ = os.RemoveAll(dir)
			return "", false, fmt.Errorf("sharing the SSO token cache into the AWS home for %q: %w", context, err)
		}
	}

	env := cfg.ExecProvider.Env
	// HOME for Unix, USERPROFILE because Windows' expanduser reads that.
	env = addEnv(env, "HOME", dir)
	env = addEnv(env, "USERPROFILE", dir)
	if h.ConfigFile != "" {
		env = addEnv(env, "AWS_CONFIG_FILE", h.ConfigFile)
	}
	if h.CredentialsFile != "" {
		env = addEnv(env, "AWS_SHARED_CREDENTIALS_FILE", h.CredentialsFile)
	}
	cfg.ExecProvider.Env = env

	return dir, true, nil
}

// hasEnv reports whether an exec block already names a variable.
func hasEnv(env []clientcmdapi.ExecEnvVar, name string) bool {
	for _, v := range env {
		if v.Name == name {
			return true
		}
	}
	return false
}

// addEnv appends a variable unless the exec block already names it.
//
// Append, never replace or rebuild: the exec block's `AWS_PROFILE` is what
// tells the CLI which role to assume, and losing it fails as a missing
// profile. Idempotent, because rest.CopyConfig shares the *api.ExecConfig by
// pointer, so this can run twice on one exec block.
func addEnv(env []clientcmdapi.ExecEnvVar, name, value string) []clientcmdapi.ExecEnvVar {
	if hasEnv(env, name) {
		return env
	}
	return append(env, clientcmdapi.ExecEnvVar{Name: name, Value: value})
}

// linkSSO links the operator's SSO token cache into a private home.
//
// When it does not exist yet and the plugin is the AWS CLI, it is created, 0700
// as the CLI makes it: the first `aws sso login` then lands where the link
// points. When it cannot be linked after being created here (a Windows host
// without the symlink privilege), it is taken back out and the home goes
// without the link, which is what such a host had before; the same failure on
// a directory that was already there is an error, and nothing is redirected.
// Any other plugin gets the link only to a directory that exists, and nothing
// is created for it.
func linkSSO(ssoDir, link string, aws bool) error {
	if isDir(ssoDir) {
		return shareDirectory(ssoDir, link)
	}
	if !aws {
		return nil
	}
	parent := filepath.Dir(ssoDir)
	hadParent := isDir(parent)
	if err := os.MkdirAll(ssoDir, 0o700); err != nil {
		return fmt.Errorf("making the SSO token cache at %s: %w", ssoDir, err)
	}
	if err := shareDirectory(ssoDir, link); err != nil {
		// Empty directories only: os.Remove refuses anything else.
		_ = os.Remove(ssoDir)
		if !hadParent {
			_ = os.Remove(parent)
		}
	}
	return nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// shareDirectory makes link a symlink to target, idempotently.
func shareDirectory(target, link string) error {
	// **The target must exist**: Unix creates a dangling symlink without
	// complaint, and every SSO context would then authenticate against an empty
	// token cache. Windows refuses the call for another reason, so a Windows
	// test alone would not see this.
	if info, err := os.Stat(target); err != nil {
		return fmt.Errorf("the SSO token cache at %s cannot be read: %w", target, err)
	} else if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", target)
	}

	if existing, err := os.Readlink(link); err == nil {
		if existing == target {
			return nil
		}
		if err := os.Remove(link); err != nil {
			return fmt.Errorf("replacing the link at %s: %w", link, err)
		}
	} else if _, err := os.Lstat(link); err == nil {
		// Something that is not a link is there. Refuse rather than delete a
		// real directory that may hold session tokens.
		return fmt.Errorf("%s exists and is not a link to %s", link, target)
	}
	if err := os.Symlink(target, link); err != nil {
		return fmt.Errorf("linking %s to %s: %w", link, target, err)
	}
	return nil
}

// awsHomeKey is a context's directory name: recognisable, and unique.
//
// A context name is not a filename (an EKS one is an ARN), so the readable
// half is sanitized and clipped, and the digest of the *whole* name keeps two
// contexts apart when they differ only past the clip.
func awsHomeKey(name string) string {
	sum := sha256.Sum256([]byte(name))
	return safeDirName(name) + "-" + hex.EncodeToString(sum[:])[:awsHomeKeyHexLen]
}

// safeDirName reduces a context name to something a filesystem accepts.
//
// Everything outside a conservative set becomes an underscore, so the output
// is ASCII and safe to clip by byte. A name that reduces to nothing becomes
// "context".
func safeDirName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "._")
	if out == "" {
		return "context"
	}
	if len(out) > awsHomeNameLen {
		out = out[:awsHomeNameLen]
	}
	return out
}

// canSymlink reports whether this host lets this process create a symlink,
// by making one and removing it.
//
// Asked rather than inferred from GOOS: Windows grants it to an administrator
// or in Developer Mode only, and a Unix filesystem may have no symlinks. The
// probe runs under the base the real homes go in, since the answer can differ
// per filesystem.
func canSymlink(base string) error {
	if base == "" {
		return errors.New("no directory to test in")
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(base, "symlink-probe-")
	if err != nil {
		return err
	}
	// Removed whatever happens, so the probe leaves nothing behind.
	defer os.RemoveAll(dir)

	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		return err
	}
	return os.Symlink(target, filepath.Join(dir, "link"))
}
