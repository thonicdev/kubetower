import { ESLint } from 'eslint';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

/**
 * The fence `eslint.config.js` puts around the window - `NO_CHANNEL` there -
 * fires, in every block of the config, on every spelling.
 *
 * A lint rule that has only ever passed has not been shown to look: a fence
 * can match nothing that exists and stay silent. This probe lints text through the real config, under the
 * names of real files, because what it guards is precisely that a later block
 * does not quietly replace the list: the network module, the storage files and
 * the tests each override `no-restricted-globals` for their own reasons.
 */
// `import.meta.dirname` rather than a `URL`: under jsdom `import.meta.url` is
// not a `file:` URL.
const web = resolve(import.meta.dirname, '..');

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
    .filter((m) => m.message.includes('no web→native channel'))
    .map((m) => `${m.ruleId}: ${m.message.split('.')[0]}`);
}

const spellings = [
  // The direct spellings.
  'window.webkit.messageHandlers.x.postMessage(1);',
  'webkit.messageHandlers.x.postMessage(1);',
  'globalThis.chrome.webview.postMessage(1);',
  'chrome.webview.postMessage(1);',
  'self.webkit;',
  // What TypeScript would actually make somebody write: neither global is typed.
  '(window as unknown as { webkit: { messageHandlers: unknown } }).webkit.messageHandlers;',
  '(window as unknown as { chrome: { webview: unknown } }).chrome.webview;',
];

// One file under each block that sets `no-restricted-globals`: shipped code,
// the network module, a storage file, and a suite.
const files = [
  'ui/src/shell/Probe.tsx',
  'ui/src/api/probe.ts',
  'ui/src/shell/menuMemory.ts',
  'ui/src/shell/Probe.test.tsx',
];

describe('the web→native fence', () => {
  for (const filePath of files) {
    it(`refuses every spelling of the window's globals in ${filePath}`, async () => {
      for (const code of spellings) {
        expect(await restricted(code, filePath), code).not.toEqual([]);
      }
    }, 30_000);
  }

  it('leaves alone a prop that happens to be called chrome', async () => {
    // `ObjectDetail`'s `chrome` is its drawer or page layout, destructured from
    // props - the reason `chrome` is refused on the global objects only.
    const code = "export function F({ chrome }: { chrome: 'drawer' | 'page' }) { return chrome; }";
    expect(await restricted(code, 'ui/src/shell/Probe.tsx')).toEqual([]);
  }, 30_000);
});
