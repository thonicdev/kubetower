// Package ekstoken mints an EKS bearer token in this process, instead of
// running the AWS CLI to get one.
//
// An EKS context authenticates through a kubeconfig exec block running
// `aws eks get-token --region <r> --cluster-name <c>` (sometimes with
// `--role-arn`, with `AWS_PROFILE` in its environment). What comes back is a
// presigned STS GetCallerIdentity URL, signed with SigV4 with
// `x-k8s-aws-id: <cluster>` as a signed header, base64-encoded behind the
// prefix `k8s-aws-v1.`. The API server replays it against STS to learn who is
// calling. This package signs the same URL in process, which:
//
//   - starts no child process, so no console window flashes on Windows (a
//     GUI-subsystem parent gets a visible console for every console child);
//   - writes nothing to `~/.aws/cli/cache`, which the CLI corrupts when two
//     processes write one entry (no truncation, lock or atomic rename);
//     credentials are resolved once and held in memory;
//   - saves a process launch and a Python start, 150 ms and up per token.
//
// It is the server's path to an API server only: `kubectl` and `helm` in the
// dock's terminals still run the kubeconfig's exec block.
//
// It refuses anything it does not recognize: any other exec block is left as
// it was for client-go to run. A fallback that mints the wrong token would be
// far worse than a subprocess.
package ekstoken

import (
	"strings"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Spec is everything an EKS token needs, read out of an exec block.
type Spec struct {
	// Cluster is the EKS cluster name: the signed `x-k8s-aws-id` header that
	// makes the token usable against this cluster and no other.
	Cluster string
	// Region is where STS is reached and where the cluster lives.
	Region string
	// RoleARN is the role to assume first, or empty to use the profile's own
	// credentials.
	RoleARN string
	// Profile is which set of credentials to use: `--profile` from the
	// arguments, otherwise `AWS_PROFILE` from the block's environment. Empty
	// means the default resolution.
	//
	// Args first, because that is the CLI's precedence. Reading only the
	// environment would turn `--profile prod-admin` into the default credential
	// chain and mint a valid token for the wrong IAM identity, refused with a
	// bare `Unauthorized` where a dock terminal running the real CLI works.
	Profile string
}

// Parse reads an exec block and reports whether it is an `aws eks get-token`
// invocation this package can replace.
//
// False is the safe answer and is returned generously: anything unfamiliar
// leaves client-go running the block itself. That costs a subprocess;
// guessing wrong costs authenticating as somebody else, or against a cluster
// the operator did not name.
func Parse(exec *clientcmdapi.ExecConfig) (Spec, bool) {
	if exec == nil {
		return Spec{}, false
	}
	if !isAWS(exec.Command) {
		return Spec{}, false
	}
	// `eks` and `get-token` are positional and, in the kubeconfigs AWS writes,
	// not first (`--region eu-west-3 eks get-token --cluster-name x`), so they
	// are looked for anywhere, adjacent.
	if !hasSubcommand(exec.Args, "eks", "get-token") {
		return Spec{}, false
	}

	spec := Spec{
		Cluster: flagValue(exec.Args, "--cluster-name"),
		Region:  flagValue(exec.Args, "--region"),
		RoleARN: flagValue(exec.Args, "--role-arn"),
		Profile: profile(exec),
	}
	if spec.Cluster == "" {
		// Without it there is no `x-k8s-aws-id` to sign, and a token signed for
		// the wrong cluster is refused by the right one.
		return Spec{}, false
	}
	if spec.Region == "" {
		// The CLI would fall back to the profile's region; guessing it from the
		// shared config could point STS at another region, so client-go runs the
		// CLI instead.
		return Spec{}, false
	}
	return spec, true
}

// UsesAWSCLI reports whether an exec block runs the AWS CLI, whatever the
// subcommand: wider than Parse, which answers only for the `eks get-token`
// shape it can replace in process.
//
// It reads the exec command alone, so a wrapper that runs the CLI
// (`aws-vault exec … -- aws`, `aws-iam-authenticator`, `granted`, a shell
// script) is not recognized.
func UsesAWSCLI(exec *clientcmdapi.ExecConfig) bool {
	return exec != nil && isAWS(exec.Command)
}

// isAWS recognizes the CLI however it is written down.
//
// `aws`, a path to it, and `aws.exe` are the same tool. A wrapper called
// `aws-sso` or `aws-vault` is not: it does something this package does not,
// and replacing it would quietly change how the operator authenticates.
func isAWS(command string) bool {
	base := command
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(strings.ToLower(base), ".exe")
	return base == "aws"
}

// hasSubcommand reports whether two positional words appear next to each other,
// in order.
func hasSubcommand(args []string, first, second string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == first && args[i+1] == second {
			return true
		}
	}
	return false
}

// flagValue reads `--name value` or `--name=value`: the CLI takes both, and
// a kubeconfig is edited by hand.
func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v
		}
	}
	return ""
}

// profile is which credentials the block asked for: `--profile` before
// `AWS_PROFILE`, the CLI's precedence (see Spec.Profile). Both occur:
// `aws eks update-kubeconfig` writes the variable, hand edits the flag.
func profile(exec *clientcmdapi.ExecConfig) string {
	if flag := flagValue(exec.Args, "--profile"); flag != "" {
		return flag
	}
	return envValue(exec.Env, "AWS_PROFILE")
}

func envValue(env []clientcmdapi.ExecEnvVar, name string) string {
	for _, v := range env {
		if v.Name == name {
			return v.Value
		}
	}
	return ""
}
