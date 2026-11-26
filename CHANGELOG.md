# Changelog

Every release of KubeTower, newest first. Versions are bare semver tags
(`0.1.0`, never `v0.1.0`); the binaries, packages and image of a version are
built from its tag.

This file is built into the console, whose What's new dialog shows each
release's highlights: every entry under `### Added`, and an entry under
`### Fixed` that ends with `<!-- highlight -->`, by its bold lead, at most five
per release. Keep the bold lead one short sentence: it is all the dialog draws.
An entry that introduces an extra carries `<!-- extra: <key> -->`, and the
dialog draws it first, as a New extra line.

## Unreleased

### Added

- **Storage Inspector reads fill from Prometheus or VictoriaMetrics.** How
  full each mounted claim is comes from the kubelets' own
  `kubelet_volume_stats_*` series where the cluster's metrics backend holds
  them, read for the claims the person may list; the kubelets, through
  `nodes/proxy`, measure the claims it does not hold. A badge beside Mounts
  names what gave the figures, and a page where nothing could be read says No
  metrics available, with what grants the read on hover. Extras reach it
  through a new Host door, `VolumeFill` (#91).
- **Python in the Runtime Inspector.** A Python pod's tab lists its Python
  processes, read out of /proc with nothing installed in the pod: the
  interpreter's version, memory and peak, threads and start time. Where the
  application serves prometheus_client's metrics, it adds the version, the
  garbage collector per generation and the process's memory, CPU and open
  files (#144).

## 0.2.2 — Clearer What's new, automatic updates and VictoriaMetrics

### Added

- **What's new lists New and Fixed.** Each highlight is one line under New,
  with a blue +, or under Fixed, with a green check, in a short sentence taken
  from this changelog. An extra a release introduces comes first, as New extra
  and its name, even when the release lists it past the fifth highlight (#121,
  #123, #125, #126).
- **Updates install themselves, on quit by default.** Settings › Infos'
  "Install updates" becomes "Install updates automatically", on by default: a
  newer release is downloaded and verified on its own, then installed as
  KubeTower quits, starting nothing, or, set to At once, as soon as no
  terminal and no port-forward is open. A .deb or .rpm, which asks for a
  password, stops at Ready with Install. A folder KubeTower cannot write to
  shows Not writable once the download is verified (with the setting off, on
  Install, as before), and an install that fails on quit is said at the next
  start and not tried again until Retry. Off, Download and Install stay by
  hand; nothing installs on quit under `--unattended` (#122, #124).
- **VictoriaMetrics as a metrics source.** Discovery finds vmsingle and
  vmselect where no Prometheus runs, as vm-operator, the
  victoria-metrics-k8s-stack chart and the victoria-metrics-single and
  victoria-metrics-cluster charts install them, and `KT_PROMETHEUS_SERVICE`
  takes a path, `vm/vmselect-main:8481/select/0/prometheus`. VictoriaMetrics
  answers every query the console sends. Rate and increase values can differ
  from Prometheus', by 10 to 25 % on a point for CPU and network, and far more
  on a p99 latency or a counter that rarely moves. A container that just ended
  or restarted leaves VictoriaMetrics' sums at once, where Prometheus keeps it
  for five minutes. Everything else reads the same on the same data (#129,
  #131).
- **Monitoring in Settings › General.** A badge says what answers on the
  current cluster: Prometheus, VictoriaMetrics, metrics-server or no metrics.
  On a desktop that keeps its state, a field takes the Service,
  `namespace/service:port[/path]`, where no history source is found or one was
  set here; it is kept for that cluster whether the API is called with its
  context's name or its DNS-friendly form. Under the server profile it shows
  `KT_PROMETHEUS_SERVICE`, read only (#130, #134, #135, #140).
- **A container's image has a line of its own.** On a pod's Containers tab,
  the image moves under the container's name. The image's name stays whole and
  the path before it is cut at its end when the line is too short (a name too
  long on its own is cut too), then come the version badge and the copy of the
  exact reference, which go under the name when both do not fit (#136, #137).

### Fixed

- **Missing metrics read as one quiet line.** A metrics panel that is drawn
  when its read fails now says No metrics available in muted text, with the
  reason on hover (#127, #132).
  - The Overview's Over time (unreachable, unreadable, refused) and Most used
    (unreadable, refused), and a Node's or Pod's Metrics tab on its first read,
    drop the red boxed notice, its sentence written twice and its Try again
    button for that line, with a small retry icon where the reason says asking
    again could help. As before, Over time draws nothing when no metrics backend
    is installed, and Most used draws nothing when its backend is absent or
    unreachable.
  - KubeAdjust's red "not measured" pill and red sentence on a page nothing
    could measure, and the Runtime Inspector's "No Prometheus" and red
    "Prometheus unreadable" pills, become the same words as a neutral badge or
    line. They carry no retry icon; the Runtime Inspector keeps its Read again
    button.
  - A page built from the cluster's own refusal keeps that refusal's notice.
- **A refused Goldmane client certificate reads as a TLS failure.** Network
  Inspector could report Goldmane as not answering when it had refused the
  client certificate: the connection was reset before its TLS alert was read.
  It now reads Goldmane's first answer before sending anything, so the refusal
  is named reliably (#141).

## 0.2.1 — Welcome dialog, favorite extras and update fixes

### Added

- **Welcome and What's new in a dialog.** They open in the dialog an
  extra's About uses, instead of a band atop the page. Welcome shows the
  KubeTower mark and a card per view (Clusters, Overview, Network Inspector,
  Runtime Inspector, Event Intelligence, Extras), each opening its view, or
  Settings › Extra for this cluster where that extra is off. What's new marks
  each highlight Added or Fixed (#107, #112).
- **Favorite extras.** A star on each extra, in Settings › Extra and on its
  row of the menu, marks it a favorite of that cluster: it joins the menu's
  Favorites section beside the favorite kinds, in the order marked, and a
  link to it sits under the cluster's head on the Overview. Kept where the
  favorite kinds are (#111, #114).
- **An image's version is always visible.** Wherever a container's image is
  shown - a Pod's and a workload's containers, the Shell's container
  choice, the Runtime Inspector - its tag, or a short digest, is a badge of
  its own after a `:` or `@`, the repository is cut from the left
  (…/team/app), and the full reference is on hover, with a copy where there
  is room (#110, #113, #116).
- **Storage Inspector**, a new read-only extra (key `storage`): the
  PersistentVolumes, PersistentVolumeClaims and StorageClasses, their
  orphans, how full each mounted claim is (graded at 80 and 90 %), and each
  claim's last VolumeSnapshot; a Storage tab on claims and pods, and a tile
  on the Overview. Extras can now draw a tile on the Overview (#90, #95). <!-- extra: storage -->

### Fixed

- **Updates install from the Windows window.** Installing an update from the
  window asked KubeTower to stop, but the window ignored it: KubeTower never
  exited, the setup never ran, and "Installing…" spun for ever. The window
  now closes, the setup runs, and the new version starts. If KubeTower does
  not stop within 90 seconds, the install goes back to Ready with the reason,
  and Install is offered again. Check now no longer loses a downloaded
  update, its Install button or the "Install updates" setting. Each install's
  steps are written to `update-install.log` in the state folder (#96, #97).
  From 0.2.0 on Windows: close the window after Install; the setup then
  runs. <!-- highlight -->
- **Updates on Linux and macOS say what happened.** A console that did not
  stop after the new build was in place says the update is installed and
  KubeTower must be started again, rather than that nothing was installed. A
  password prompt for a .deb or .rpm left open is given up after 5 minutes.
  A refused or unanswered prompt goes back to Ready with the verified file
  kept, so Install needs no new download. A helper waiting to restart
  KubeTower is stopped with the install, and logs its steps to
  `update-install.log`. On Windows, a second Install closes the window too,
  and the window goes as soon as the update asks (#101, #106).
- **Updates: an install that never came back is said after a sleep.** Once
  the machine wakes, "Installing…" turns to Failed on the next read rather
  than up to three minutes later (#99, #108).
- **Runtime Inspector: a Node.js process that exited is said.** A chosen
  process that ended before its inspector opened shows `pid N exited` beside
  the ones left, or one line when none is, instead of an empty choice. A heap
  snapshot that holds no byte fails instead of saving an empty file (#100,
  #105).
- **Event Intelligence: the held count follows the namespace.** Filtered to a
  namespace, `N of M` counts its Warnings against its own tiles, not the
  cluster's (#100, #105).
- **What's new: "Not saved" is announced** to a screen reader (#100, #105).
- **The release notes link the signing key at the release's tag**, as they
  say, instead of on `main` (#100, #105).

## 0.2.0 — Runtime Inspector, Event Intelligence timeline and What's new

### Added

- **The Runtime Inspector detects each container's runtime**: Java, Go,
  Node.js or Python, from the pod's spec (the command line, then the
  environment, then the image), or the pod annotation `kubetower.io/runtime`
  (`python`, or `app=go,metrics=none` per container). The Runtime column
  names each runtime (`Java · Go`), and a pod gets one tab per runtime it
  runs, with the hints as badges. Diagnostics are Java's, Go's and
  Node.js's; Python is detected only (#19, #31, #81).
- **Go: goroutines and profiles through pprof**, on a click: goroutines
  grouped by state and stack, and the heap profile, a CPU profile (10, 30 or
  60 s) and a trace (5 or 10 s) as downloads. The port is the
  `kubetower.io/pprof-port` annotation, a container port named `pprof`, or
  6060. It goes through a tunnel to the pod, reviewed and recorded as
  `create pods/portforward`; no signal is sent and nothing attaches (#20,
  #21, #33, #35).
- **Node.js: the inspector on a confirmed click.** Opening it runs one fixed
  `node -e` script in the container (reviewed as `create pods/exec`,
  recorded), which calls `process._debugProcess`; the inspector then stays
  open until the process restarts. Through it: a heap snapshot (confirmed,
  at most 1 GiB), a CPU profile and the diagnostic report, with
  secret-looking environment values masked. A process started with
  `--disable-sigusr1` is never opened (#22, #36).
- **Event Intelligence: a readable timeline when many nodes join.** Node
  changes close in time are one marker (`+17`, `!1`), each workload is one
  row of density cells unfolding by reason, Warnings are shown first
  (`Normal` on a toggle), and the changes Warnings came after are cards
  above the timeline that light what they relate to. A cut answer shows a
  count (`2 012 of 2 058`) (#86, #89).
- **Hubble UI and Whisker in a window of their own**, in the desktop on
  Windows and macOS, with no console session in it; **↗** still opens the
  default browser. The window stays on the tunnel's address, closes the
  tunnel it opened, and closes with the console. Linux, a browser and a
  console in a cluster are unchanged (#65, #79). Not tried on a real Windows
  or macOS machine yet.
- **A welcome tour, and what's new after an upgrade.** A banner atop the
  console shows a tour of the main views, or the highlights of every release
  since the last one seen, read from this file built into the console.
  **Settings › Infos › What's new** opens it again (#80, #83).

### Changed

- **The JVM Inspector is the Runtime Inspector**, key `runtime`. Nothing to
  change on upgrade: `jvm` is still read wherever it was written -
  `KT_EXTRAS` and the chart's `extras`, the stored switch, bookmarks and tab
  links, API addresses under `/x/<cluster>/jvm/` (a 308 to `runtime`), and a
  ClusterRole granting `use` on `extras.kubetower.dev/jvm`. Grant `runtime`
  in new roles (#18, #27).
- **A cluster in `KT_READONLY_CLUSTERS` runs no diagnostic.** jcmd, pprof,
  the Node.js inspector and its snapshot answer `read_only` ("Read-only
  cluster") before any tunnel opens or command runs, and the refusal is now
  written to the audit log (#85, #87).

### Fixed

- **Runtime Inspector: a link from another site runs nothing.** The routes
  that run jcmd, open a tunnel or take a dump answer a POST from the console
  only; the JVM tab's tools list was a GET in 0.1.2 (#82, #87).
- **Runtime Inspector: `npm start` and a Python server are not read as
  Java** when `JAVA_HOME` is set or the image is a JDK, and npm's own
  process is not offered beside the application (#11, #19, #81, #87).
- **Postgres (CNPG): Read lag names a read-only cluster as one**, instead of
  "you may not run commands in this pod" (#11, #88).
- **Event Intelligence: the held badge shows only when the lists miss
  Warnings**, not "340 of 340" on an answer with nothing cut; and an Event
  dated after the answer was built (read during the read, or from a node
  whose clock runs ahead) stays in the last cell of its row (#86, #93).

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
