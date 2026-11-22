# Contributing

**Contributions are welcome.** Issues, bug reports, questions and pull requests
from anyone. There is no agreement to sign.

**You keep the copyright in what you contribute, and you contribute it under the
project's license.** KubeTower is GNU AGPL-3.0-only; a pull request is offered
under that same license, nothing more and nothing less. The project's copyright
notice reads *Copyright (C) 2026 The KubeTower Authors*: the authors are everyone
whose work is in the tree, as the git history records them. There is no list to
add yourself to.

---

## Before you write code

**Open an issue first for anything larger than a fix.** Bug reports, feature
requests, questions about how something works or about whether a use is covered
are all wanted. Agreeing on the shape of a change before the code saves a
rewrite.

**Fork it.** The AGPL grants that and nothing here takes it back. Two things to
know before you do:

- **Section 13 applies to a console.** If you modify this software and let other
  people use your modified version over a network, you owe those users the
  corresponding source. Running an unmodified copy, or a modified copy only you
  use, creates no obligation at all. [`LICENSING.md`](LICENSING.md) has the whole
  of it.
- **The name is not licensed by `LICENSE`.** Take the code; rename the result.
  That is the ordinary arrangement and it is the part that survives a complete
  fork.

**Tell us it broke.** A report against a real cluster is worth more than a
feature request, and we cannot reproduce what we cannot see.

---

## Reporting a bug

What makes a report actionable, in the order it helps:

1. **The version.** `kubetower --version`, or the image tag. "Latest" is not a
   version — it names different code on different days.
2. **How you ran it** — desktop binary, container, or in-cluster — and on which
   operating system.
3. **The Kubernetes side**: server version (`kubectl version`), the provider if
   it is a managed cluster, and how the kubeconfig authenticates (an `exec`
   credential plugin, a token, a certificate).
4. **What you expected and what happened.** A status code, a screenshot, the
   exact message. "It doesn't work" and "it says X" are different reports and only
   one of them can be acted on.
5. **The log**, which is a file as well as the terminal — and **read it before
   you paste it.** Nothing should print a credential, but you are the last check
   on that, and a redacted log is fine.
6. **An empty answer and a failed answer are different answers.** "No pods" and
   "could not list pods" look alike on screen and are not the same bug. Say which
   one you saw, if you can tell.

For a vulnerability, or anything you would rather not put in public, follow
[`SECURITY.md`](SECURITY.md): use GitHub's **Report a vulnerability** on this
repository instead of an issue.

---

## The conventions this repository already runs on

A first pull request that fails a check on a rule nobody mentioned is a bad
first experience, so here they are; all of them are visible in the tree.

**Conventional Commits, on every commit message and every pull-request title.**
[`commitlint.config.js`](commitlint.config.js) is the configuration;
[`.github/workflows/guards.yml`](.github/workflows/guards.yml) enforces it. Three
traps worth naming:

- **Subjects are lower case.** That is an explicit rule in the config, not a
  default, and it is the one that catches people.
- **A scope must be on the config's list** (`scope-enum`), or be left out. A
  change that needs a new scope adds it to the list in the same commit.
- **The pull-request title is linted too**, because a squash merge takes the
  title as the commit subject. A conforming set of commits under a
  non-conforming title still lands a bad subject on the default branch.

**No machine attribution in the git history.** No `Co-Authored-By:` naming a
model or a vendor, no "generated with" line in a commit message or a pull-request
body, no commit authored by a non-human identity.
[`.github/workflows/guards.yml`](.github/workflows/guards.yml) refuses them by
name. If your editor or assistant adds such a trailer automatically, turn it off
before committing — this is the check people trip without realizing a tool did it
for them. Saying in a description *what* you did and *how* is wanted; stamping a
machine as an author of the work is not, because the history of this repository
is the provenance record of who wrote what.

**Every change reaches the default branch through a pull request.** Nothing is
pushed to `main` directly, including documentation. A pull request from a fork
runs CI on GitHub-hosted runners once a maintainer approves the run.

**Run the checks before the runners do.** `task ci` replays what `guards.yml` and `ci.yml` check that needs no runner, and must pass before a pull request leaves draft (`bash test/ci-local.sh --list` says what stays runner-only).
`task hooks:install` adds a pre-push hook that checks what a push changes in under a minute; `git push --no-verify` skips it.
`task ci` runs one at a time per machine and declines above a load of `KT_CI_MAX_LOAD` (6); `task ci -- --force` runs anyway.

**Everything is in English** — code, comments, documentation, and every string
the interface puts on screen. The prototype this replaces ran with English code
and a French interface for its first weeks; half a convention is worse than
either half, because every new string becomes a decision and the two halves
drift.

**What "done" means is written down**, and it is not "it compiles":
[`docs/DEFINITION-OF-DONE.md`](docs/DEFINITION-OF-DONE.md). Every line in it
names the thing that went wrong without it.

---

## Questions

Open an issue. Including a question about this file.
