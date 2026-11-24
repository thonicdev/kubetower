import { readFileSync } from 'node:fs';

import type { Plugin } from 'vite';

/** The module that holds CHANGELOG.md's text. */
export const CHANGELOG_MODULE = 'virtual:kubetower-changelog';

/**
 * CHANGELOG.md, at the repository's root, as a module's default export: What's
 * new (#80, `ui/src/whatsnew/changelog.ts`) draws each release's highlights
 * from it, and nothing is fetched at run time. A plugin rather than a `?raw`
 * import: the file lies outside `web/`, which Vite and Vitest refuse to serve,
 * and widening that allow list would serve the rest of the repository in dev.
 */
export function changelog(file: string): Plugin {
  const resolved = `\0${CHANGELOG_MODULE}`;
  return {
    name: 'kubetower-changelog',
    resolveId(id) {
      return id === CHANGELOG_MODULE ? resolved : undefined;
    },
    load(id) {
      if (id !== resolved) return undefined;
      this.addWatchFile(file);
      return `export default ${JSON.stringify(readFileSync(file, 'utf8'))};`;
    },
  };
}
