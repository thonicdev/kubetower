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
//
// The arguments are read as the CLI reads them (readArgs), abbreviations
// included: the CLI takes `--role` for `--role-arn`, and reading only the full
// spelling signed as the profile's own role, which the cluster refused with a
// bare 401 while kubectl in a terminal worked.
func Parse(exec *clientcmdapi.ExecConfig) (Spec, bool) {
	if exec == nil {
		return Spec{}, false
	}
	if !isAWS(exec.Command) {
		return Spec{}, false
	}
	args, ok := readArgs(exec.Args)
	if !ok {
		return Spec{}, false
	}
	// `eks` and `get-token` are the only positionals and, in the kubeconfigs AWS
	// writes, not first (`--region eu-west-3 eks get-token --cluster-name x`).
	if len(args.positionals) != 2 || args.positionals[0] != "eks" || args.positionals[1] != "get-token" {
		return Spec{}, false
	}
	if _, outposts := args.values["--cluster-id"]; outposts {
		// A local cluster on Outposts, which `x-k8s-aws-id` names by its id: not a
		// shape this package has been checked against.
		return Spec{}, false
	}
	spec := Spec{
		Cluster: args.values["--cluster-name"],
		Region:  args.values["--region"],
		RoleARN: args.values["--role-arn"],
		// The flag before `AWS_PROFILE`, the CLI's precedence (see Spec.Profile).
		// Both occur: `aws eks update-kubeconfig` writes the variable, hand
		// edits the flag.
		Profile: args.values["--profile"],
	}
	if spec.Profile == "" {
		spec.Profile = envValue(exec.Env, "AWS_PROFILE")
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

// The options of `aws eks get-token` in the AWS CLI v2, true where one takes a
// value. The CLI parses with argparse and its default `allow_abbrev`: the
// global options first, over every argument (MainArgParser), then the
// command's own over what is left (ArgTableArgParser). From awscli/argparser.py,
// awscli/data/cli.json and awscli/customizations/eks/get_token.py.
var (
	awsGlobalOptions = map[string]bool{
		"--ca-bundle": true, "--cli-auto-prompt": false, "--cli-binary-format": true,
		"--cli-connect-timeout": true, "--cli-error-format": true, "--cli-read-timeout": true,
		"--color": true, "--debug": false, "--endpoint-url": true, "--help": false,
		"--no-cli-auto-prompt": false, "--no-cli-pager": false, "--no-paginate": false,
		"--no-sign-request": false, "--no-verify-ssl": false, "--output": true,
		"--profile": true, "--query": true, "--region": true, "--version": false,
	}
	getTokenOptions = map[string]bool{
		"--cluster-id": true, "--cluster-name": true, "--role-arn": true,
	}
)

// awsArgs is an argument list as the CLI understood it: each option under its
// full name, the last occurrence winning as in argparse, and the positionals.
type awsArgs struct {
	values      map[string]string
	positionals []string
}

// readArgs reads an argument list as the CLI does, or refuses one the CLI would
// refuse or that cannot be read for certain: an ambiguous abbreviation
// (`--cluster` is `--cluster-name` or `--cluster-id`), an option neither parser
// knows, a short option, a missing value.
func readArgs(raw []string) (awsArgs, bool) {
	out := awsArgs{values: map[string]string{}}
	for i := 0; i < len(raw); i++ {
		arg := raw[i]
		if !strings.HasPrefix(arg, "-") {
			out.positionals = append(out.positionals, arg)
			continue
		}
		if !strings.HasPrefix(arg, "--") || arg == "--" {
			return awsArgs{}, false
		}
		name, value, inline := strings.Cut(arg, "=")
		full, takesValue, ok := resolveOption(name)
		if !ok || full == "--help" || full == "--version" {
			// --help and --version print and exit: no token comes back.
			return awsArgs{}, false
		}
		if _, own := getTokenOptions[full]; own && len(out.positionals) < 2 {
			// The command's options are parsed after the command is known: before
			// `eks get-token`, the CLI refuses them.
			return awsArgs{}, false
		}
		switch {
		case !takesValue && inline:
			return awsArgs{}, false
		case !takesValue:
			value = ""
		case !inline:
			// argparse does not take a word starting with `-` as a value.
			if i+1 >= len(raw) || strings.HasPrefix(raw[i+1], "-") {
				return awsArgs{}, false
			}
			i++
			value = raw[i]
		}
		out.values[full] = value
	}
	return out, true
}

// resolveOption is argparse's lookup, one parser after the other: the exact
// name, else the one option it abbreviates. Several is the CLI's "ambiguous
// option" error, and a refusal here too.
func resolveOption(name string) (full string, takesValue, ok bool) {
	for _, table := range []map[string]bool{awsGlobalOptions, getTokenOptions} {
		if value, exact := table[name]; exact {
			return name, value, true
		}
		var matches []string
		for option := range table {
			if strings.HasPrefix(option, name) {
				matches = append(matches, option)
			}
		}
		switch len(matches) {
		case 0:
			continue
		case 1:
			return matches[0], table[matches[0]], true
		default:
			return "", false, false
		}
	}
	return "", false, false
}

func envValue(env []clientcmdapi.ExecEnvVar, name string) string {
	for _, v := range env {
		if v.Name == name {
			return v.Value
		}
	}
	return ""
}
