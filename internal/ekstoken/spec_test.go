package ekstoken

import (
	"testing"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// The two *shapes* that sit side by side in a real kubeconfig: an admin context
// that assumes a role and its plain twin that does not. The shapes are those of
// a real file and the **values are not** — account id, cluster, role and
// profile are all invented here, because a repository is not somebody's
// configuration and this one gets handed to other people
// (docs/DEFINITION-OF-DONE.md §6.C). `111122223333` is AWS's own documentation
// placeholder.
//
// What has to be faithful is the argument *order*: `--region` comes before the
// `eks get-token` positionals, which is not where anybody would put it by hand
// and is exactly what a parser written from imagination gets wrong.
func awsExec(args []string, profile string) *clientcmdapi.ExecConfig {
	exec := &clientcmdapi.ExecConfig{
		APIVersion: "client.authentication.k8s.io/v1beta1",
		Command:    "aws",
		Args:       args,
	}
	if profile != "" {
		exec.Env = []clientcmdapi.ExecEnvVar{{Name: "AWS_PROFILE", Value: profile}}
	}
	return exec
}

func TestTheRealKubeconfigShapesAreRecognised(t *testing.T) {
	cases := []struct {
		name string
		exec *clientcmdapi.ExecConfig
		want Spec
	}{
		{
			name: "with a role to assume",
			exec: awsExec([]string{
				"--region", "eu-west-3", "eks", "get-token",
				"--cluster-name", "example-nonprod-eks", "--output", "json",
				"--role-arn", "arn:aws:iam::111122223333:role/example-eks-devops",
			}, "example-devops"),
			want: Spec{
				Cluster: "example-nonprod-eks",
				Region:  "eu-west-3",
				RoleARN: "arn:aws:iam::111122223333:role/example-eks-devops",
				Profile: "example-devops",
			},
		},
		{
			name: "the profile's own credentials",
			exec: awsExec([]string{
				"--region", "eu-west-3", "eks", "get-token",
				"--cluster-name", "example-nonprod-eks", "--output", "json",
			}, "example-admin"),
			want: Spec{Cluster: "example-nonprod-eks", Region: "eu-west-3", Profile: "example-admin"},
		},
		{
			name: "= instead of a space, which the CLI also takes",
			exec: awsExec([]string{"eks", "get-token", "--cluster-name=prod", "--region=eu-west-1"}, ""),
			want: Spec{Cluster: "prod", Region: "eu-west-1"},
		},
		{
			name: "aws.exe by absolute path",
			exec: func() *clientcmdapi.ExecConfig {
				e := awsExec([]string{"--region", "eu-west-1", "eks", "get-token", "--cluster-name", "p"}, "")
				e.Command = `C:\Program Files\Amazon\AWSCLIV2\aws.exe`
				return e
			}(),
			want: Spec{Cluster: "p", Region: "eu-west-1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Parse(tc.exec)
			if !ok {
				t.Fatalf("an ordinary EKS exec block was not recognized, so this context keeps " +
					"spawning a subprocess for every token")
			}
			if got != tc.want {
				t.Errorf("Parse = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestAnythingUnfamiliarIsLeftAlone is the safety property, and it is the one
// worth more than the parsing.
//
// False means client-go keeps running the block itself, which costs a
// subprocess. A wrong true means authenticating with a token this package built
// from an invocation it misread — as somebody else, or against a cluster the
// operator did not name. The asymmetry is total, so the refusals are generous.
func TestAnythingUnfamiliarIsLeftAlone(t *testing.T) {
	cases := []struct {
		name string
		exec *clientcmdapi.ExecConfig
	}{
		{"nothing at all", nil},
		{"a different tool", &clientcmdapi.ExecConfig{Command: "kubelogin", Args: []string{"get-token"}}},
		{
			// The one that would be most tempting to accept: it is *about* aws,
			// it does *something* like this, and it does not do this.
			name: "a wrapper whose name starts with aws",
			exec: &clientcmdapi.ExecConfig{Command: "aws-vault", Args: []string{
				"exec", "prod", "--", "aws", "eks", "get-token", "--cluster-name", "p", "--region", "eu-west-1",
			}},
		},
		{"aws, but not eks get-token", awsExec([]string{"sts", "get-caller-identity"}, "")},
		{"eks, but another subcommand", awsExec([]string{"eks", "list-clusters", "--region", "eu-west-1"}, "")},
		{
			// Without it there is no x-k8s-aws-id to sign, and a token signed for
			// the wrong cluster is refused by the right one.
			name: "no cluster name",
			exec: awsExec([]string{"eks", "get-token", "--region", "eu-west-1"}, ""),
		},
		{
			// The CLI would fall back to the profile's region. Guessing at it
			// here would point STS somewhere the block never named.
			name: "no region",
			exec: awsExec([]string{"eks", "get-token", "--cluster-name", "p"}, ""),
		},
		{"the words are there but not adjacent", awsExec([]string{"eks", "--region", "get-token", "--cluster-name", "p"}, "")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if spec, ok := Parse(tc.exec); ok {
				t.Errorf("Parse accepted an invocation it does not understand, as %+v. client-go "+
					"would stop running the block and this package would authenticate in its "+
					"place", spec)
			}
		})
	}
}

// `--profile` in the arguments is a profile, and ignoring it authenticates as
// somebody else.
//
// # Why this is not a tidiness test
//
// The symptom it guards: *"every cluster page says unauthorized while a
// terminal on the same cluster has full rights"*. A Parse that read the profile
// from `AWS_PROFILE` in the block's environment and nowhere else would parse an
// exec block written as `aws --profile prod-admin --region … eks get-token …` —
// which is what a hand-edited kubeconfig and several tools produce —
// **successfully**, with `Profile: ""`.
//
// That is the worst available outcome, and it is worth being precise about why.
// Parse returning false would cost a subprocess. Parse returning true with
// no profile makes `token.go` call `LoadDefaultConfig` with no
// `WithSharedConfigProfile`, so the SDK resolves the *default* credential chain
// and signs a **valid** presigned URL for whatever identity that finds. The API
// server replays it, learns a caller it has no aws-auth mapping for, and answers
// a bare `Unauthorized` — while the dock's terminal, running the real CLI with
// the real flag, has full rights on the same cluster. Nothing anywhere says a
// profile was dropped.
//
// Reading the flag is preferred to refusing on it. Refusing is the doctrine's
// default and would be defensible; reading is strictly better because it is
// exactly what the CLI does, it keeps the in-process token for these contexts,
// and it needs no fallback to a binary that may not be on PATH at all (see
// internal/binpath).
func TestTheProfileIsReadFromTheArgumentsToo(t *testing.T) {
	base := []string{"--region", "eu-west-3", "eks", "get-token", "--cluster-name", "prod"}

	for _, tc := range []struct {
		name string
		args []string
		env  string
		want string
	}{
		{
			name: "--profile with a space",
			args: append([]string{"--profile", "prod-admin"}, base...),
			want: "prod-admin",
		},
		{
			name: "--profile= with an equals sign, which the CLI also takes",
			args: append([]string{"--profile=prod-admin"}, base...),
			want: "prod-admin",
		},
		{
			name: "after the positionals rather than before them",
			args: append(append([]string{}, base...), "--profile", "prod-admin"),
			want: "prod-admin",
		},
		{
			name: "AWS_PROFILE alone still works, which is what the CLI writes",
			args: base,
			env:  "from-the-environment",
			want: "from-the-environment",
		},
		{
			// The CLI's own precedence: a command-line option beats the
			// environment. Getting this backwards would authenticate as the
			// wrong one of two identities that both exist, which is harder to
			// spot than authenticating as none.
			name: "both, and the flag wins",
			args: append([]string{"--profile", "from-the-flag"}, base...),
			env:  "from-the-environment",
			want: "from-the-flag",
		},
		{
			name: "neither, which means the default chain and is a legitimate shape",
			args: base,
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, ok := Parse(awsExec(tc.args, tc.env))
			if !ok {
				t.Fatalf("Parse refused %v", tc.args)
			}
			if spec.Profile != tc.want {
				t.Errorf("Profile = %q, want %q — a dropped profile mints a valid token for "+
					"the wrong identity, and the cluster answers Unauthorized",
					spec.Profile, tc.want)
			}
		})
	}
}
