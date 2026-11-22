import { describe, expect, it } from 'vitest';

/**
 * The terminal's renderer stays xterm's default, the DOM one.
 *
 * `terminal.ts`'s header records why, from a measurement of it against
 * `@xterm/addon-webgl`. A decision written only in a comment is undone by the
 * first change that never reads the comment - one `pnpm add` and one
 * `term.loadAddon(new WebglAddon())` - and nothing would say so: jsdom draws
 * nothing, so every other test passes under either renderer.
 *
 * So this reads the three places a renderer addon would have to appear, and
 * fails by name on each:
 *
 *   - **imported** by any shipped source file in `ui/src` or `app`. Test files
 *     are left out: they do not ship, and one that pulled the addon in would
 *     need it declared, which the next check catches;
 *   - **declared** in `web/package.json` or `web/ui/package.json`, under any
 *     dependency field;
 *   - **locked** in `web/pnpm-lock.yaml`, which also catches one arriving
 *     transitively.
 *
 * Changing the decision means deleting this file in the same change as the
 * header paragraph, which is the point: it cannot happen by accident.
 *
 * Read with `import.meta.glob` rather than `node:fs`, as `dialogLayers.test.ts`
 * does, so the paths are resolved by the bundler and not by whatever directory
 * a runner starts in.
 */

const RENDERER_ADDONS = ['@xterm/addon-webgl', '@xterm/addon-canvas'] as const;

/** A module specifier naming a renderer addon, in a static import, a dynamic
 *  `import()`, a re-export or a `require`. A mention in prose - backticks in a
 *  comment, as in `terminal.ts`'s header - is not one. It reads `.ts`/`.tsx`
 *  files and quoted specifiers only; the lockfile check below covers the gap. */
const IMPORTS_A_RENDERER =
  /\b(?:from|import|require)\s*\(?\s*['"]@xterm\/addon-(?:webgl|canvas)(?:\/[^'"]*)?['"]/;

const sources = import.meta.glob(
  [
    '../**/*.ts',
    '../**/*.tsx',
    '../../../app/**/*.ts',
    '../../../app/**/*.tsx',
    '!../**/*.test.ts',
    '!../**/*.test.tsx',
    '!../../../app/**/*.test.ts',
    '!../../../app/**/*.test.tsx',
  ],
  { query: '?raw', import: 'default', eager: true },
) as Record<string, string>;

const manifests = import.meta.glob(['../../../package.json', '../../package.json'], {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;

const lockfiles = import.meta.glob('../../../pnpm-lock.yaml', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>;

const DEPENDENCY_FIELDS = [
  'dependencies',
  'devDependencies',
  'peerDependencies',
  'optionalDependencies',
] as const;

describe('the terminal renderer', () => {
  it('recognizes an import of a renderer addon, and not a mention of one', () => {
    // Without this the guard below could pass on a pattern that matches nothing.
    expect(IMPORTS_A_RENDERER.test("import { WebglAddon } from '@xterm/addon-webgl';")).toBe(true);
    expect(IMPORTS_A_RENDERER.test('const m = await import("@xterm/addon-canvas");')).toBe(true);
    expect(IMPORTS_A_RENDERER.test("export { WebglAddon } from '@xterm/addon-webgl';")).toBe(true);
    expect(IMPORTS_A_RENDERER.test(' * `@xterm/addon-webgl` 0.19.0, Chromium 153')).toBe(false);
    expect(IMPORTS_A_RENDERER.test("import { FitAddon } from '@xterm/addon-fit';")).toBe(false);
  });

  it('is not imported by any shipped source file', () => {
    const files = Object.keys(sources);
    // The glob read the tree it was meant to: the chunk and its one importer
    // (keys are relative to this file, so the chunk is `./terminal.ts`), and
    // `app` as well as `ui/src`.
    expect(files).toContain('./terminal.ts');
    expect(files).toContain('./TerminalPane.tsx');
    expect(files).toContain('../../../app/main.tsx');
    expect(files.some((f) => f.endsWith('.test.ts') || f.endsWith('.test.tsx'))).toBe(false);

    const importers = files.filter((f) => IMPORTS_A_RENDERER.test(sources[f] ?? ''));
    expect(importers, 'a renderer addon is imported - see terminal.ts, "The renderer"').toEqual([]);
  });

  it('is not declared in either package.json', () => {
    expect(Object.keys(manifests)).toHaveLength(2);
    for (const [file, text] of Object.entries(manifests)) {
      const manifest = JSON.parse(text) as Record<string, Record<string, string> | undefined>;
      // The fit addon is declared, so the fields read are the ones that matter.
      const declared = DEPENDENCY_FIELDS.flatMap((field) => Object.keys(manifest[field] ?? {}));
      expect(declared, file).toContain('@xterm/xterm');
      for (const addon of RENDERER_ADDONS) expect(declared, file).not.toContain(addon);
    }
  });

  it('is not in the lockfile, directly or transitively', () => {
    const lock = Object.values(lockfiles)[0] ?? '';
    expect(lock).toContain('@xterm/addon-fit@');
    for (const addon of RENDERER_ADDONS) expect(lock).not.toContain(addon);
  });
});
