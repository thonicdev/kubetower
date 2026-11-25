# Changelog

Every release of KubeTower, newest first. Versions are bare semver tags
(`0.1.0`, never `v0.1.0`); the binaries, packages and image of a version are
built from its tag.

This file is built into the console, whose What's new banner shows each
release's highlights: every entry under `### Added`, and an entry under
`### Fixed` that ends with `<!-- highlight -->`, by its bold lead, at most five
per release. Keep the bold lead short: it is all the banner draws.

## Unreleased

### Added

- **Storage Inspector**, a new read-only extra (key `storage`): the
  PersistentVolumes, PersistentVolumeClaims and StorageClasses, their orphans,
  how full each claim is (graded at 80 and 90 %), and each claim's last
  VolumeSnapshot; a Storage tab on claims and pods, and a tile on the
  Overview. Extras can now draw a tile on the Overview (#90).

### Changed

- **The JVM Inspector is the Runtime Inspector**, key `runtime`. Nothing to
  change on upgrade: `jvm` is still read wherever it was written - `KT_EXTRAS`
  and the chart's `extras`, the stored switch, bookmarks and tab links, API
  addresses under `/x/<cluster>/jvm/` (a 308 to `runtime`), and a ClusterRole
  granting `use` on `extras.kubetower.dev/jvm`. Grant `runtime` in new roles
  (#18).

## 0.1.2 — signed updates, Redis viewer and Network Inspector fixes

### Added

- **Releases are signed.** `checksums.txt` is published with an Ed25519
  signature, `checksums.txt.sig`, and the desktop verifies it against a key
  built into the binary before it trusts any hash (#46, #52).
- **The desktop updates itself.** Settings offers a newer release in two
  clicks: **Download** fetches the file for this installation from GitHub and
  keeps it only when its sha256 matches the signed `checksums.txt`; **Restart
  to update** installs it and starts KubeTower again, counting the terminals
  and port-forwards it ends. A badge on Settings, an update banner, the
  progress in place, and **Install updates** in Settings › Infos to turn it
  off (#45, #47, #48, #53, #55).
  - Windows: the setup, silently and per user, or the zip (#50, #57).
  - macOS: the app from the disk image, swapped in place, or the `.tar.gz`
    (#51, #64).
  - Linux: the AppImage and the `.tar.gz` in place, the `.deb` and `.rpm`
    through `pkexec` with `apt-get` or `dpkg -i`, `dnf`, `zypper` or
    `rpm -U` (#49, #56).
  - A folder that is not writable says **Not writable**, with **Open folder**
    on the verified file (#56, #57, #64).
  - Never in a cluster or a container, never a downgrade, never a file the
    signature does not vouch for.
  - The Windows and macOS installs have not been tried on a real machine
    yet.
- **Settings has a Back control** to the page it was opened from, its
  filters and open object included (#13, #30).
- **Settings › Extra reads at a glance.** Each extra has its icon, Edit and
  About sit side by side, and About only informs (#14, #29). An extra the
  cluster gives a reason to turn on carries a **Suggested** badge, the reason
  in its tooltip, and comes first (#62, #63).
- **Network Inspector: the namespace matrix stays readable on large
  clusters.** It draws the busiest namespaces and folds the rest into
  "others", with Find, an order and an Active filter kept in the address
  (#15, #25).
- **Network Inspector: a pod's network picture opens enlarged**, in the same
  dialog as its metrics (#16, #32).
- **Redis viewer: an ExternalName Service to a managed Redis is listed** as
  outside the cluster, and never connected to: a host that names Redis or
  Valkey, Amazon ElastiCache and MemoryDB, Azure Cache for Redis Enterprise,
  Redis Cloud, and Aiven when its name or port also says Redis (#43, #74,
  #75, #76).

### Fixed

- **Redis viewer: more Redis and Valkey are recognised.**
  - an image a private registry renamed (`infra-valkey`, `redis-cluster`)
    (#39, #40);
  - an `app` or `app.kubernetes.io/name` label that contains `redis` or
    `valkey`, on the Service or its pods (#42, #54);
  - a container that runs `redis-server` or `valkey-server` from an image of
    any name (#71, #72).

  A pod's label counts only for a port that reaches it, and more tools
  (dashboards, exporters, GUIs) are left out (#66, #68).
- **Redis viewer: a tile that could not be checked says so**, with a
  `not checked` badge and the reason in its tooltip, when the namespace cannot
  be read (#41, #44).
- **Redis viewer: its menu entry is back** once it is turned on (#23, #24).
- **Network Inspector: Hubble UI and Whisker open from the desktop window**,
  in the default browser, instead of failing (#26, #28).
- **Network Inspector: a rule open to everyone** no longer draws its
  namespace as refused in the matrix (#67, #69).
- **Event Intelligence: long names stay readable.** They are cut in the
  middle, keeping the part that tells pods apart, and the page keeps its
  width (#34, #37).
- **The unused kinds toggle says Show or Hide** by its state (#60, #61).
- **Credential plugins other than the AWS CLI keep their own state.** Only
  the AWS CLI runs under the private home; every other credential plugin
  sees the real one again (#10, #73).

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
