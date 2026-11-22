package terminal

import (
	"fmt"
	"os"
	"path/filepath"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Giving each terminal session its own AWS CLI credential cache.
//
// The AWS CLI files an assumed role under `~/.aws/cli/cache/<sha1>.json` and
// writes it with open(O_WRONLY|O_CREAT), truncate(), write(): no O_TRUNC, no
// lock, no atomic rename. Two `aws` processes writing one entry both write from
// offset zero, the loser's tail survives, and the file becomes two JSON
// documents end to end that every later read fails on, with nothing more than
// `exec: executable aws failed with exit code 255` to show for it.
// internal/cluster/awscache.go separates the in-process clients; this
// separates the dock's `kubectl` and `helm`, whose EKS exec block is the same
// `aws eks get-token`.
//
// # Per session, not per context
//
// internal/cluster keys its homes on the context because its clients are
// shared. Two terminals on one cluster are two `aws` processes, and keying on
// the context would put them on one entry again. So the home is the session's,
// inside its working directory, and is removed with it (`session.close`): a
// corrupt cache cannot outlive its session.
//
// # Through the kubeconfig, not the shell's environment
//
// The lever is HOME: the cache directory is a constant
// (`~/.aws/cli/cache` in awscli/customizations/assumerole.py) with no variable
// of its own. HOME in the shell's environment would also move the Claude
// config, helm's repository list and the shell history. The session kubeconfig
// carries an `env` list per exec block that client-go hands to the credential
// plugin alone, so the redirect reaches that process and no other; it is the
// same field internal/cluster sets on its rest.Config.
//
// # What must not move with it
//
//   - `~/.aws/config` and `~/.aws/credentials` hold the profiles.
//     AWS_CONFIG_FILE and AWS_SHARED_CREDENTIALS_FILE point back at them, set
//     only when this process's environment does not already name them.
//   - `~/.aws/sso/cache` holds the SSO token, renewed in place, so it must
//     stay shared. It has no variable either (a constant in botocore), so it is
//     linked into the new home. If the link cannot be made the redirect is not
//     done at all: breaking every SSO context to fix the assume-role cache is
//     worse. On Windows without the symlink privilege, the separation is off.
//
// Cost: a session's cache starts empty, so a terminal on an EKS cluster costs
// one AssumeRole; the CLI caches within the session after that.
//
// Not covered: two `kubectl` commands run concurrently inside one terminal
// still share that session's entry. Closing that would need a lock inside a CLI
// this project does not own.

// sessionAWSHomeDir is the directory, inside a session's working directory, that
// becomes HOME for that session's credential plugin.
const sessionAWSHomeDir = "aws-home"

// sessionAWSHome is one session's private AWS CLI credential cache. A value so
// a test can point every path at a temporary directory. An empty Dir disables
// the separation, which happens when there is no home directory to point the
// shared files back at.
type sessionAWSHome struct {
	// Dir is the per-session HOME.
	Dir string
	// SSODir is the operator's own ~/.aws/sso, linked into Dir so the session
	// token stays shared and renewable. Empty when there is no such directory, as
	// on an installation with no SSO profiles.
	SSODir string
	// ConfigFile and CredentialsFile are the shared ~/.aws/config and
	// ~/.aws/credentials, set only when this process's environment does not
	// already name them: the plugin inherits this environment, and a value already
	// supplied must not be second-guessed.
	ConfigFile      string
	CredentialsFile string
}

// newSessionAWSHome describes the private home a session's exec plugins get,
// under the session's own working directory.
func newSessionAWSHome(workdir string) sessionAWSHome {
	if workdir == "" {
		return sessionAWSHome{}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// No home means no shared config to point back at; leave the exec blocks
		// alone.
		return sessionAWSHome{}
	}
	aws := filepath.Join(home, ".aws")

	h := sessionAWSHome{Dir: filepath.Join(workdir, sessionAWSHomeDir)}
	if info, err := os.Stat(filepath.Join(aws, "sso")); err == nil && info.IsDir() {
		h.SSODir = filepath.Join(aws, "sso")
	}
	if os.Getenv("AWS_CONFIG_FILE") == "" {
		h.ConfigFile = filepath.Join(aws, "config")
	}
	if os.Getenv("AWS_SHARED_CREDENTIALS_FILE") == "" {
		h.CredentialsFile = filepath.Join(aws, "credentials")
	}
	return h
}

// separate gives every exec block in a session kubeconfig its own AWS CLI
// credential cache, by giving it this session's HOME. It reports whether
// anything changed, so the caller can log it.
//
// A kubeconfig with no exec block is untouched and no directory is created. An
// exec block that sets HOME itself is left alone: the operator meant it.
func (h sessionAWSHome) separate(cfg *clientcmdapi.Config) (bool, error) {
	if cfg == nil || h.Dir == "" {
		return false, nil
	}

	targets := make([]*clientcmdapi.ExecConfig, 0, len(cfg.AuthInfos))
	for _, user := range cfg.AuthInfos {
		if user == nil || user.Exec == nil {
			continue
		}
		if hasExecEnv(user.Exec.Env, "HOME") {
			continue
		}
		targets = append(targets, user.Exec)
	}
	if len(targets) == 0 {
		return false, nil
	}

	// 0700: this holds live STS credentials, under a work root shared by every
	// session.
	if err := os.MkdirAll(filepath.Join(h.Dir, ".aws"), 0o700); err != nil {
		return false, fmt.Errorf("making the session AWS home: %w", err)
	}
	if h.SSODir != "" {
		// Before any environment is written: without its SSO token cache the home
		// authenticates nothing on an SSO profile, so if this fails the redirect must
		// not happen.
		if err := shareAWSDirectory(h.SSODir, filepath.Join(h.Dir, ".aws", "sso")); err != nil {
			return false, fmt.Errorf("sharing the SSO token cache into the session AWS home: %w", err)
		}
	}

	for _, exec := range targets {
		env := exec.Env
		// HOME for everything else, USERPROFILE for Windows, where ntpath.expanduser
		// reads it. Both, so the result does not depend on the platform.
		env = addExecEnv(env, "HOME", h.Dir)
		env = addExecEnv(env, "USERPROFILE", h.Dir)
		if h.ConfigFile != "" {
			env = addExecEnv(env, "AWS_CONFIG_FILE", h.ConfigFile)
		}
		if h.CredentialsFile != "" {
			env = addExecEnv(env, "AWS_SHARED_CREDENTIALS_FILE", h.CredentialsFile)
		}
		exec.Env = env
	}
	return true, nil
}

// hasExecEnv reports whether an exec block already names a variable.
func hasExecEnv(env []clientcmdapi.ExecEnvVar, name string) bool {
	for _, v := range env {
		if v.Name == name {
			return true
		}
	}
	return false
}

// addExecEnv appends a variable unless the exec block already names it. Never
// replace or rebuild: an EKS exec block carries AWS_PROFILE, the only thing
// telling the CLI which role to assume.
func addExecEnv(env []clientcmdapi.ExecEnvVar, name, value string) []clientcmdapi.ExecEnvVar {
	if hasExecEnv(env, name) {
		return env
	}
	return append(env, clientcmdapi.ExecEnvVar{Name: name, Value: value})
}

// shareAWSDirectory makes link a symlink to target, idempotently.
func shareAWSDirectory(target, link string) error {
	if existing, err := os.Readlink(link); err == nil {
		if existing == target {
			return nil
		}
		if err := os.Remove(link); err != nil {
			return fmt.Errorf("replacing the link at %s: %w", link, err)
		}
	} else if _, err := os.Lstat(link); err == nil {
		// Something that is not a link is there. Refuse rather than delete a real
		// directory that may hold someone's session tokens.
		return fmt.Errorf("%s exists and is not a link to %s", link, target)
	}
	if err := os.Symlink(target, link); err != nil {
		return fmt.Errorf("linking %s to %s: %w", link, target, err)
	}
	return nil
}
