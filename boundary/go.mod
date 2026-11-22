// The second module, and its whole reason is the line above `module`.
//
// `github.com/thonicdev/kubetower/extension` and `@kubetower/ui` are the two
// halves of the contract every extension compiles against. The extras that
// ship are packages of the core module (`extras/`), which the
// compiler would let import `internal/`; a test refuses it
// (`extras/boundary_test.go`). This module is still
// the one consumer the compiler itself holds to the contract, which is what an
// extension built in another repository would be. The other implementation of
// `extension.Extension` in the tree, `internal/web/fixtureextension_test.go`, is
// `package web`, inside `internal/`, and therefore on the wrong side of the
// boundary: it could reach anything. A real extra can reach six methods.
//
// # The module path is the mechanism, not a label
//
// **Go's internal rule is path-based.** An import of a path containing the
// element `internal` is refused unless the importing code is inside the tree
// rooted at the parent of that `internal` directory — and "the tree" is decided
// by the *module path*, not by where the directory happens to sit on disk. So a
// nested module named `github.com/thonicdev/kubetower/boundary` would be inside
// that tree and would be allowed to import `internal/web` exactly as the fixture
// does. It would compile, the tests would pass, and it would prove nothing while
// looking like it proved everything.
//
// Hence `boundary.kubetower.invalid`: outside the core's path, so the compiler
// enforces what this module is here to assert. `.invalid` is reserved by RFC
// 2606 and can never resolve, which is the second half of the same idea — a
// module path nobody can ever publish something at.
//
// # What it does and does not establish
//
// **Does:** every method of `extension.Host` is callable from a package the
// internal rule shuts out, with every type in its signature nameable from there;
// `app.Register` accepts an extension declared outside the core; the contract
// fails to compile here when a method leaves it.
//
// **Does not:** that the *console's own* host behaves correctly. That host is
// `internal/web.host` and is unreachable from here by construction, which is the
// point rather than a gap — its behaviour is tested in `internal/web`, from
// inside, where it lives.
module boundary.kubetower.invalid

go 1.26.0

toolchain go1.27.4

require (
	github.com/thonicdev/kubetower v0.0.0
	go.uber.org/goleak v1.3.0
	golang.org/x/crypto v0.58.0
	k8s.io/api v0.37.1
	k8s.io/apimachinery v0.37.1
	k8s.io/client-go v0.37.1
)

require (
	github.com/aws/aws-sdk-go-v2 v1.47.4 // indirect
	github.com/aws/aws-sdk-go-v2/config v1.33.10 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.20.10 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.20.4 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.7 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.7 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.22 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.10.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.38.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.43.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.51.5 // indirect
	github.com/aws/smithy-go v1.28.6 // indirect
	github.com/coreos/go-oidc/v3 v3.21.0 // indirect
	github.com/creack/pty v1.1.24 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/ebitengine/purego v0.11.2 // indirect
	github.com/emicklei/go-restful/v3 v3.13.0 // indirect
	github.com/fxamacker/cbor/v2 v2.9.1 // indirect
	github.com/go-jose/go-jose/v4 v4.1.6 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-openapi/jsonpointer v1.0.0 // indirect
	github.com/go-openapi/jsonreference v1.0.0 // indirect
	github.com/go-openapi/swag v0.27.1 // indirect
	github.com/go-openapi/swag/cmdutils v0.27.1 // indirect
	github.com/go-openapi/swag/conv v0.27.1 // indirect
	github.com/go-openapi/swag/fileutils v0.27.1 // indirect
	github.com/go-openapi/swag/jsonutils v0.27.1 // indirect
	github.com/go-openapi/swag/loading v0.27.1 // indirect
	github.com/go-openapi/swag/mangling v0.27.1 // indirect
	github.com/go-openapi/swag/netutils v0.27.1 // indirect
	github.com/go-openapi/swag/pools v0.27.1 // indirect
	github.com/go-openapi/swag/stringutils v0.27.1 // indirect
	github.com/go-openapi/swag/typeutils v0.27.1 // indirect
	github.com/go-openapi/swag/yamlutils v0.27.1 // indirect
	github.com/google/gnostic-models v0.7.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.4-0.20250319132907-e064f32e3674 // indirect
	github.com/jchv/go-webview2 v0.0.0-20260205173254-56598839c808 // indirect
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/moby/spdystream v0.5.1 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/valkey-io/valkey-go v1.0.79 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/oauth2 v0.37.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/term v0.47.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
	gopkg.in/evanphx/json-patch.v4 v4.13.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	k8s.io/klog/v2 v2.140.0 // indirect
	k8s.io/kube-openapi v0.0.0-20260721132016-d427ff9ee9ad // indirect
	k8s.io/streaming v0.37.2 // indirect
	k8s.io/utils v0.0.0-20260626114624-be93311217bd // indirect
	sigs.k8s.io/json v0.0.0-20250730193827-2d320260d730 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.4.2 // indirect
	sigs.k8s.io/yaml v1.6.0 // indirect
)

// The module under test is the working copy, never a published version. There
// is no published version: this repository has never cut a release.
replace github.com/thonicdev/kubetower => ../
