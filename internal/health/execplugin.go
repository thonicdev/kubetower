package health

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// client-go sends an exec plugin's stderr to this process's own, so a failed
// plugin (any exec block internal/ekstoken does not sign itself) reads only
// "exec: executable aws failed with exit code 255". The most common cause is a
// lapsed SSO session. On that one arm the plugin is run once more with its
// output kept, and the output is classified like any error; the probe backs
// off (backgroundDue), and an interactive plugin is not run.

// pluginOutputMax bounds what is kept of the plugin's output: the needles are
// in its first lines, and a plugin that prints a page must not fill memory.
const pluginOutputMax = 8 << 10

// pluginWaitDelay is how long the re-run waits, once the plugin has exited or
// its deadline has passed, for the pipes to close. A child the plugin left
// behind can hold them open for as long as it lives, and the re-run holds the
// process-wide rerunSlot until they close.
const pluginWaitDelay = 2 * time.Second

// runPlugin runs the exec block as client-go would and returns what it printed
// on both streams. ctx bounds it: a plugin that hangs is killed at the probe's
// own deadline, and a child it left behind is killed with it.
var runPlugin = defaultRunPlugin

func defaultRunPlugin(ctx context.Context, provider *clientcmdapi.ExecConfig) string {
	cmd := exec.CommandContext(ctx, provider.Command, provider.Args...)
	cmd.Env = os.Environ()
	for _, env := range provider.Env {
		cmd.Env = append(cmd.Env, env.Name+"="+env.Value)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &capped{&out}, &capped{&out}
	cmd.WaitDelay = pluginWaitDelay
	ownGroup(cmd)
	_ = cmd.Run() // the failure is known; what it said is the point
	// A plugin that exited before its deadline never reached cmd.Cancel, and a
	// child it started is still running.
	killGroup(cmd)
	return out.String()
}

// rerunSlot holds the one plugin re-run allowed at a time (heard).
var rerunSlot = make(chan struct{}, 1)

// capped drops whatever goes past pluginOutputMax.
type capped struct{ buf *bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := pluginOutputMax - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// execOf is the exec block client-go runs for this client, or nil where there
// is none - including a recognized EKS block, which is signed in this process
// and is not an exec block at all (`cluster.Manager.mintInProcess`).
func execOf(client *cluster.Client) *clientcmdapi.ExecConfig {
	if client == nil || client.Config == nil {
		return nil
	}
	if provider := client.Config.ExecProvider; provider != nil && provider.Command != "" {
		return provider
	}
	return nil
}

// heard reclassifies a refused plugin from its own output. Only the SSO arm is
// taken from it: that is the failure the operator fixes in one command, and
// the one the page must not miss. Anything else keeps the plugin arm, which
// already names the log the output is in.
func heard(ctx context.Context, result probeResult, provider *clientcmdapi.ExecConfig) probeResult {
	if result.state != StatePlugin || provider == nil {
		return result
	}
	// `Always` is a plugin that asks a person, and this process has nobody to
	// answer it.
	if provider.InteractiveMode == clientcmdapi.AlwaysExecInteractiveMode {
		return result
	}
	// One re-run at a time, process-wide: concurrent AWS CLI runs over one cache
	// corrupt it (see credcache.go). A probe that cannot have the slot before
	// its deadline keeps the plugin arm.
	select {
	case rerunSlot <- struct{}{}:
		defer func() { <-rerunSlot }()
	case <-ctx.Done():
		return result
	}
	said := strings.TrimSpace(runPlugin(ctx, provider))
	if said == "" {
		return result
	}
	if state, detail, _ := classify(errors.New(said)); state == StateSSO && ssoWorded(said) {
		result.state, result.detail = state, detail
	}
	return result
}

// ssoWorded is whether text is an SSO session's own failure, not a bare 401.
// classify's 401 arm also returns StateSSO, and a plugin printing
// "Unauthorized" has not said its session lapsed.
func ssoWorded(text string) bool {
	return contains(strings.ToLower(text), "token has expired", "sso session", "expired or invalid",
		"refresh token", "sso-session", "cached sso token")
}

// awsProfile is the AWS profile an exec block names: `--profile` in its
// arguments, else `AWS_PROFILE` in its own env. Empty when it names none.
func awsProfile(provider *clientcmdapi.ExecConfig) string {
	if provider == nil {
		return ""
	}
	for i, arg := range provider.Args {
		if value, ok := strings.CutPrefix(arg, "--profile="); ok {
			return value
		}
		if arg == "--profile" && i+1 < len(provider.Args) {
			return provider.Args[i+1]
		}
	}
	for _, env := range provider.Env {
		if env.Name == "AWS_PROFILE" {
			return env.Value
		}
	}
	return ""
}

// usesAWS is whether this context's credentials come from AWS at all: signed
// in this process for EKS, or an exec block that is `aws`, names an AWS
// profile, or mentions EKS. An OIDC or GKE context answering 401 is also
// StateSSO, and handing it `aws sso login` would be a command for somebody
// else's problem.
func usesAWS(client *cluster.Client, provider *clientcmdapi.ExecConfig) bool {
	if client != nil && client.EKS != nil {
		return true
	}
	if provider == nil {
		return false
	}
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(provider.Command)), ".exe")
	if base == "aws" || awsProfile(provider) != "" {
		return true
	}
	return strings.Contains(strings.ToLower(strings.Join(provider.Args, " ")), "eks")
}

// ssoCommand is the command that renews this context's SSO session, or "" where
// the context is not an AWS one. The profile is the kubeconfig's own when it
// names one: `aws sso login --profile p` renews the session that profile uses,
// in either the legacy or the `sso-session` form.
func ssoCommand(client *cluster.Client, provider *clientcmdapi.ExecConfig) string {
	if !usesAWS(client, provider) {
		return ""
	}
	profile := awsProfile(provider)
	if profile == "" && client != nil && client.EKS != nil {
		profile = client.EKS.Profile
	}
	if profile == "" {
		return "aws sso login"
	}
	if strings.HasPrefix(profile, "-") {
		// `--profile -x` reads as a second option; the joined form cannot.
		return "aws sso login --profile=" + shellWord(profile)
	}
	return "aws sso login --profile " + shellWord(profile)
}

// plainWord is a profile name a shell reads as itself.
var plainWord = regexp.MustCompile(`^[A-Za-z0-9._@+=:/-]+$`)

// shellWord is a kubeconfig's profile name made safe to paste: as it is when
// it is a plain word, single-quoted otherwise. The command is offered for
// copying into a terminal, and the profile comes from a file this console
// did not write - `--profile "x; curl … | sh"` must arrive as one argument.
func shellWord(word string) string {
	if plainWord.MatchString(word) {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}
