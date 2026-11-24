# Changelog

Every release of KubeTower, newest first. Versions are bare semver tags
(`0.1.0`, never `v0.1.0`); the binaries, packages and image of a version are
built from its tag.

This file is built into the console, whose What's new banner shows each
release's highlights: every entry under `### Added`, and an entry under
`### Fixed` that ends with `<!-- highlight -->`, by its bold lead, at most five
per release. Keep the bold lead short: it is all the banner draws.

## 0.1.1 — AWS sign-in fixes

### Fixed

- **`aws eks get-token` arguments are read as the AWS CLI reads them.**
  An abbreviated option such as `--role` now assumes the role it names,
  instead of being dropped and the token signed as the profile's own
  identity (a bare 401, shown as a lapsed session). `--opt=value`, the last
  occurrence and any unambiguous prefix read the same way; anything the CLI
  would refuse or read otherwise is left to the CLI (#6, #8).
- **An edited kubeconfig is used without a restart.** The desktop reads its
  kubeconfig files again when they change and on a cluster's Retry. Only the
  clusters whose entry changed are reconnected, and a save caught halfway
  (an empty file, no contexts) keeps the previous kubeconfig (#6, #8).
- **A first `aws sso login` after start is seen at once.** The private home
  an AWS CLI credential plugin and a dock terminal run under now links
  `~/.aws/sso` even when it did not exist yet, creating it for the AWS CLI
  only (#7, #9).

## 0.1.0 — first public release

### The console

- **One binary, two profiles.** A desktop window on the clusters in your
  kubeconfig (Windows, macOS, Linux), or a shared console inside a cluster
  behind OpenID Connect single sign-on (`--incluster`).
- **Your RBAC, enforced.** In a cluster, every read and every action is checked
  against the signed-in person's own permissions and recorded in an audit log.
- **Safe by construction.** The Go back end is the only Kubernetes client; no
  credential reaches the browser. Every write shows a dry run first.
- **Everything in one place.** Every kind, custom resources included; logs,
  shells, port-forwards, metrics, Helm releases, several clusters at once.
- **No telemetry.** The only call the desktop makes on its own is a check for a
  newer release (`KT_UPDATE_CHECK=off` turns it off).

### Extras, off by default, switched on per cluster

Argo CD (Delivery Center, Flux included), Cert Manager, DNS, Event
Intelligence, JVM Inspector, KubeAdjust, KubeCron, Network Inspector, Pod
Debugging, Postgres (CNPG), Redis viewer, Security Center. See
[`docs/EXTRAS.md`](docs/EXTRAS.md).

### Distribution

- Windows setup and zip, macOS disk images (Apple silicon and Intel), Linux
  `.deb`, `.rpm`, `.AppImage` and archives, every file listed in
  `checksums.txt`.
- Container image `ghcr.io/thonicdev/kubetower:0.1.0`, installed through the
  [Helm chart](https://github.com/thonicdev/kubetower-helm-charts). It carries
  no Claude Code binary.
- The Windows and macOS builds are not code-signed yet:
  [`docs/INSTALL.md`](docs/INSTALL.md) says how to check a download.

### License

GNU AGPL-3.0-only for the whole product, extras included. Copyright (C) 2026
The KubeTower Authors. Contributions are open to anyone; see
[`CONTRIBUTING.md`](CONTRIBUTING.md).
