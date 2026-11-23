// KubeTower — one Go binary, the console alone.
//
// Deliberately few dependencies. client-go because talking to Kubernetes by
// hand is not a thing anyone should do twice; gorilla/websocket because the
// terminal already uses it and it is the same protocol; creack/pty for the
// terminal itself; robfig/cron/v3 because it is the parser the Kubernetes
// CronJob controller itself uses (pkg/controller/cronjob), and the CronJobs
// list's next-run column predicts what that controller will do — a hand-rolled
// five-field split that disagreed with it on `@monthly`, on a range with a step,
// or on the day-of-month/day-of-week or-not-and rule would produce exactly the
// confident wrong countdown the column is meant to avoid. It has no
// dependencies of its own. Everything else is the standard library.
//
// `schedule` rests on one call site: `internal/resources/rows.go` asks it for a
// CronJob's next run. Worth knowing before somebody refactors that column.
//
// **No database or Redis driver in the console itself**, measured rather than
// assumed: before the extras, `go list -deps ./cmd/kubetower | grep -E
// 'pgx|go-redis|go-sql-driver'` was empty. The one SQL driver is the Postgres
// (CNPG) extra's, below.
//
// **Two modules serve single sign-on**, and each is the
// smallest thing that does its job. `github.com/coreos/go-oidc/v3` verifies an ID
// token's signature against the provider's rotating key set, which is the one
// part of an OIDC flow that must not be written by hand - and it is what
// kube-apiserver's own OIDC authenticator is built on. It brings one module of
// its own, `go-jose`. `golang.org/x/oauth2` does the code exchange and PKCE, and
// is also client-go's indirect dependency. Apache-2.0, Apache-2.0
// and BSD-3-Clause: all three combine into an AGPL-3.0 work.
//
// **One more, for several replicas**: `github.com/valkey-io/valkey-go`,
// Valkey's own client, Apache-2.0, which combines into an AGPL-3.0 work. It is
// what several replicas share their sessions, sign-ins in progress and
// confirmation plans through (`internal/sharedstate`). go-redis would do the
// same job; this keeps the answer above empty. It links three packages of its own and one module
// already here, `golang.org/x/sys` - measured, not assumed:
//
//	go list -deps ./cmd/kubetower | grep valkey
//	  → valkey-go, valkey-go/internal/cmds, valkey-go/internal/util, internal/sharedstate/valkeystore
//
// Its test-only requirement, gomega, appears in go.sum and in no build.
// `internal/api` and `internal/auth` are written against the interface and link
// none of it.
//
// **It is also the Redis viewer's client** (`extras/redis`): one Redis client in
// the module rather than two, Valkey's own, which speaks to Redis and Valkey
// alike - so the answer above stays empty and no module is added. It is
// imported from those two packages and nowhere else, which
// `extras/redis/driver_test.go` checks over every file of the module: a driver
// must not be linked into a package that is not a viewer. With the viewer
// registered, the same measurement adds the viewer and nothing else:
//
//	go list -deps ./cmd/kubetower | grep -E 'valkey|go-redis|pgx|go-sql-driver|extras/redis'
//	  → valkey-go/internal/cmds, valkey-go/internal/util, valkey-go,
//	    internal/sharedstate/valkeystore, extras/redis
//
// **`github.com/jackc/pgx/v5`, its connection package alone, is the Postgres
// (CNPG) extra's SQL door** (`extras/cnpg/sqlconn.go`): the statistics and
// the first rows of a table, through fixed read-only statements. MIT, which
// combines into an AGPL-3.0 work. Only `pgconn` and what it needs are linked -
// no pool, no type registry - and only from that package, which
// `extras/redis/driver_test.go` checks over every file of the module:
//
//	go list -deps ./cmd/kubetower | grep jackc
//	  → pgpassfile, pgservicefile, pgx/v5/pgconn and its internal
//	    packages (iobufpool, pgio, ctxwatch, bgreader, pgproto3)
//
// **Two more draw the desktop window, one per platform, and each is linked only
// on its own.** `github.com/jchv/go-webview2` (MIT) embeds WebView2 on Windows;
// `github.com/ebitengine/purego` (Apache-2.0) reaches AppKit and WKWebView on
// macOS (`internal/launch/nativewindow_darwin.go`). Both were chosen for the
// same property: **no cgo**, so every binary still cross-compiles with
// `CGO_ENABLED=0` from a host with no C compiler. Both combine into an AGPL-3.0
// work. purego has no dependencies of its own. Measured, not assumed:
//
//	GOOS=darwin  go list -deps ./cmd/kubetower | grep -c purego      → 4
//	GOOS=linux   go list -deps ./cmd/kubetower | grep -c purego      → 0
//	GOOS=windows go list -deps ./cmd/kubetower | grep -c go-webview2 → 4
//
// The Linux binaries and the container carry neither.
//
// No web framework: net/http's ServeMux has had method and pattern routing
// since 1.22, and this is an application with about thirty routes.
module github.com/thonicdev/kubetower

go 1.26.0

// toolchain pins what *builds* this, which is not the same question as `go` above.
// `go` is the minimum language version the source needs; this is the compiler the
// build actually runs. A patch release of the standard library can fix security
// advisories (html/template URL escaping, for one), and a host build uses
// whatever compiler the developer installed; this line makes that loop fetch a
// current compiler instead of trusting the laptop to be up to date. The `go`
// directive stays lower: nothing here needs a newer language feature, and
// raising the minimum would refuse a contributor on an older Go for no gain.
toolchain go1.27.4

require (
	github.com/aws/aws-sdk-go-v2 v1.47.4
	github.com/aws/aws-sdk-go-v2/config v1.33.10
	github.com/aws/aws-sdk-go-v2/credentials v1.20.10
	github.com/aws/aws-sdk-go-v2/service/sts v1.51.5
	github.com/aws/smithy-go v1.28.6
	github.com/coreos/go-oidc/v3 v3.21.0
	github.com/creack/pty v1.1.24
	github.com/ebitengine/purego v0.11.2
	github.com/gorilla/websocket v1.5.4-0.20250319132907-e064f32e3674
	github.com/jackc/pgx/v5 v5.9.0
	github.com/jchv/go-webview2 v0.0.0-20260205173254-56598839c808
	github.com/robfig/cron/v3 v3.0.1
	github.com/valkey-io/valkey-go v1.0.79
	go.uber.org/goleak v1.3.0
	golang.org/x/crypto v0.58.0
	golang.org/x/net v0.62.0
	golang.org/x/oauth2 v0.37.0
	golang.org/x/sys v0.49.0
	golang.org/x/term v0.47.0
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af
	k8s.io/api v0.37.1
	k8s.io/apimachinery v0.37.1
	k8s.io/client-go v0.37.1
	k8s.io/streaming v0.37.2
	sigs.k8s.io/yaml v1.6.0
)

require (
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.20.4 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.7 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.7 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.22 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.10.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.38.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.43.5 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
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
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/moby/spdystream v0.5.1 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	gopkg.in/evanphx/json-patch.v4 v4.13.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	k8s.io/klog/v2 v2.140.0 // indirect
	k8s.io/kube-openapi v0.0.0-20260721132016-d427ff9ee9ad // indirect
	k8s.io/utils v0.0.0-20260626114624-be93311217bd // indirect
	sigs.k8s.io/json v0.0.0-20250730193827-2d320260d730 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.4.2 // indirect
)
