// Conventional Commits, on commit messages and on pull-request titles.
// The config has to be in the tree: commitlint with no config refuses every
// subject it is given, valid ones included, with "Please add rules to your
// commitlint.config.js". A check that cannot pass is not a check.
module.exports = {
  extends: ['@commitlint/config-conventional'],

  // A merge commit describes no change of its own, and the preset already
  // knows it: `defaultIgnores` skips git's own `Merge branch ...` and
  // `Merge pull request ...`. A merge subject written by hand as
  // `merge: <what came in>` reads better than git's default but is not a
  // Conventional Commit, so the preset's rule is extended to that spelling.
  //
  // It is deliberately narrow. `^merge: ` and nothing else: a commit that
  // merges nothing and calls itself `merge` still has to say what it changed,
  // because this ignores a *shape*, not a word.
  ignores: [(message) => /^merge: /.test(message)],

  rules: {
    // The scopes this repository accepts, committed beside the config so
    // that "is this a valid scope" has an answer in the tree rather than in
    // somebody's memory - and a misspelt scope (`fix(clsuter)`) is refused
    // here instead of landing on `main`. They are the console's parts, the
    // extension contract, and a Go package name where that is more specific.
    //
    // Adding a scope is part of the change that first uses it: one line here,
    // in the same commit. A stale list does not fail loudly - it refuses a
    // legitimate commit - so the cost of a missing entry is a red run, and the
    // remedy is this line rather than a vaguer scope.
    //
    // A commit with no scope stays valid (`ci: ...`): the preset sets no
    // `scope-empty`, and `scope-enum` does not apply to an empty scope.
    'scope-enum': [
      2,
      'always',
      [
        // The console's parts and the repository's own machinery.
        'api',
        'app',
        'audit',
        'ci',
        'cmd',
        'core',
        'deps',
        'docs',
        'extension',
        'extra',
        'release',
        'security',
        'settings',
        'state',
        'ui',
        'web',
        // Package names, where one is more specific than the above.
        'access',
        'actions',
        // The extras, packages of this module (`extras/<name>`).
        'argocd',
        'auth',
        'certmanager',
        'cluster',
        'cnpg',
        'dns',
        'edit',
        'events',
        'forward',
        'helm',
        'jvm',
        'kubeadjust',
        'kubecron',
        'launch',
        'logs',
        'metrics',
        'netinspect',
        'pending',
        'redis',
        'resources',
        'svcgroup',
        'terminal',
        'update',
      ],
    ],

    'subject-case': [2, 'always', 'lower-case'],

    // The preset caps body and footer lines at 100 characters. A squash
    // commit's body is built by GitHub from the branch's commit messages, and
    // does not exist until Merge is pressed, so no PR check can see it; a line
    // that grows past 100 there would be refused on `main`, after the merge.
    //
    // The header rules hold, and they are what the guard is for: a subject
    // that is a Conventional Commit. The body is prose, and some of it is
    // written by a machine - a squash, dependabot - that does not wrap at
    // 100. A cap on it refuses generated text on `main`, after the merge,
    // where nothing can be amended. Off, both of them.
    'body-max-line-length': [0],
    'footer-max-line-length': [0],
  },
};
