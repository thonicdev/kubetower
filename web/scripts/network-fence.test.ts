import { ESLint } from 'eslint';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

/**
 * The fence `eslint.config.js` puts around the network and web storage fires on
 * every spelling of every name, in shipped code of every extension, and stops
 * where it is meant to stop.
 *
 * Built like `no-web-to-native.test.ts`: text linted through the real config,
 * under the names of real or plausible files, because the defects it guards
 * against are the shape of the config itself - a spelling no rule names, a file
 * extension no block matches, an exemption that re-lists the network names and
 * forgets one.
 */
// `import.meta.dirname` rather than a `URL`: under jsdom `import.meta.url` is
// not a `file:` URL.
const web = resolve(import.meta.dirname, '..');

const FENCE_RULES = new Set(['no-restricted-globals', 'no-restricted-properties']);

// A fixture ESLint could not parse reports no restriction either, so a parse
// failure is thrown rather than filtered out: otherwise the case that expects
// silence would pass on code nothing read.
async function restricted(code: string, filePath: string): Promise<string[]> {
  const [result] = await new ESLint({ cwd: web }).lintText(code, { filePath });
  const fatal = result?.messages.find((m) => m.fatal);
  if (result === undefined || fatal !== undefined) {
    throw new Error(`ESLint did not read ${filePath}: ${fatal?.message ?? 'no result'}`);
  }
  return result.messages
    .filter((m) => m.ruleId !== null && FENCE_RULES.has(m.ruleId))
    .map((m) => `${m.ruleId}: ${m.message}`);
}

const network = [
  "self.fetch('/x');",
  'new XMLHttpRequest();',
  'window.XMLHttpRequest;',
  "navigator.sendBeacon('/x');",
];
const storage = ['self.localStorage;'];

describe('the network and storage fence', () => {
  // Shipped code, and shipped code of an extension: every spelling.
  for (const filePath of ['ui/src/pages/probe.tsx', 'app/extras/probe.ts']) {
    for (const code of [...network, ...storage]) {
      it(`refuses ${code} in ${filePath}`, async () => {
        expect(await restricted(code, filePath)).not.toEqual([]);
      }, 30_000);
    }
  }

  // The storage exemption re-lists the network names; it must list them all.
  for (const code of network) {
    it(`refuses ${code} in a file exempted for storage`, async () => {
      expect(await restricted(code, 'ui/src/shell/kindsMemory.ts')).not.toEqual([]);
    }, 30_000);
  }

  it('fences a JavaScript file in shipped code', async () => {
    expect(await restricted("fetch('/x');", 'ui/src/pages/probe.js')).not.toEqual([]);
  }, 30_000);

  // The other side: the fence stops at the network module.
  it('lets the network module talk to the network', async () => {
    expect(await restricted("fetch('/x');", 'ui/src/api/probe.ts')).toEqual([]);
  }, 30_000);
});
