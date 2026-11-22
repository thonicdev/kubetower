# Licensing

This repository is **GNU AGPL-3.0-only**, all of it. The full text is in
[`LICENSE`](LICENSE).

There is one license and one product. Every feature of the console is in this
repository and under that license, including the pages that are switched on per
cluster. There is no paid edition, no proprietary module and no separate
end-user agreement.

Contributions are open to anyone: see
[Copyright and contributions](#copyright-and-contributions).

---

## What the AGPL asks of you

Most licenses trigger on **distribution**: modify the software, keep it to
yourself, and you owe nothing. The AGPL adds one clause that matters for something
like this.

**Section 13.** If you modify this software and let people use it **over a
network**, you must offer those users the corresponding source of your modified
version.

That clause is why this license and not the GPL. This is a web console: it is
deployed in a container and reached with a browser. Under the plain GPL, the
common case — running a modified copy for your team — would carry no obligation at
all. Under the AGPL it does.

**What it does not ask.** Nothing, if you are the only user. Run it, modify it,
never let anyone else near it, and no obligation arises — an obligation with no
recipient has nobody to owe it to. It begins when a second person is involved.

**Using it unmodified** creates no obligation of any kind. Deploy it, point it at
your clusters, tell your colleagues. That is what it is for.

---

## One product, one license

Some of the console's pages are **extras**: features many clusters have no use
for, because each reads something only some clusters run. A new cluster starts
with every extra switched off, and each one is switched on per cluster. That
switch is a setting, not a license boundary. An extra is built from this
repository like every other page, and it is under the same license.

What follows from that, so you do not have to work it out:

- **What you download is this repository, built.** A KubeTower release is one
  executable, and every line of KubeTower in it comes from this tree. The AGPL
  covers all of it, and section 13's obligation runs against a modification of
  any of it.
- **Nothing is held back.** No feature is sold separately, and none is a delayed
  release of something kept elsewhere.
- **Other people's code keeps its own license.** The executable also carries
  third-party libraries; see [Third-party code](#third-party-code).

### Building it yourself

**You can build the whole console from this repository alone**, with no access
to anything else. It needs Go for the back end and Node for the front end — both
halves are here — and what you get is the console, not a demo of it:

```
go build ./cmd/kubetower    # the back end
task build                  # the same plus the front end: the console
```

**Build only the first and the binary starts with no front end**, which it says at
startup rather than leaving you to discover — `web/dist/.gitkeep` is a committed
placeholder so that a back-end contributor can work without Node, and it means the
embed cannot fail when the bundle is absent.

---

## Copyright and contributions

Copyright (C) 2026 The KubeTower Authors. **The KubeTower Authors** are everyone
whose work is in this tree, as its git history records them; there is no separate
list.

**Contributions are open to anyone, with no agreement to sign.** A contribution is
licensed under the AGPL-3.0-only, the license of this repository, and its author
keeps the copyright in it. Nobody is asked to assign copyright or to grant a right
to relicense. See [`CONTRIBUTING.md`](CONTRIBUTING.md) for how.

---

## Trademarks

The AGPL grants rights in the **code**. It grants nothing in the name.

The project name and its marks are not licensed by `LICENSE`, and a fork may not
present itself under them. This is the ordinary arrangement, and it is the part
that survives even a complete fork: take the code, rename the result.

---

## Third-party code

Dependencies keep their own licenses, and their notices are preserved. Nothing here
relicenses anybody else's work.

---

## Questions

Anything about the license or whether a use is covered: open an issue.
Nobody should have to guess about this.
