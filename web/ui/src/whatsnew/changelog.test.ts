import { existsSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';

import { describe, expect, it } from 'vitest';

import { compareVersions, highlightsBetween, MAX_HIGHLIGHTS, parseChangelog, RELEASES } from './changelog';
import { RELEASE } from './seenMemory';

const SAMPLE = `# Changelog

Every release, newest first.

## 0.3.0 — later

### Added

- **Saved views.** Filters kept per kind
  and per cluster (#90).
- **Pod logs: follow across restarts.**
- **One.**
- **Two.**
- **Three.**
- **Six, past the limit.**

## 0.2.0 — what's new

### Added

- **Storage Inspector**, a new extra. <!-- extra: storage -->
  Claims and volumes (#90).
- **What's new** after an upgrade, from the [changelog](CHANGELOG.md) (#80).

### Fixed

- **A fix nobody asked to see.** Long text.
- **\`aws eks get-token\` arguments read as the CLI reads them.** <!-- highlight -->
  More text (#6, #8).

## 0.1.2 — fixes only

### Fixed

- **A plain fix.**

## 0.1.0 — first public release

### The console

- **One binary, two profiles.**
`;

describe('the changelog', () => {
  const releases = parseChangelog(SAMPLE);

  it('takes Added entries and the Fixed ones marked as highlights, by their bold lead', () => {
    expect(releases.map((one) => one.version)).toEqual(['0.3.0', '0.2.0', '0.1.2', '0.1.0']);
    // Each says which section it came from, so Added and Fixed draw apart (#107).
    expect(releases[1]?.highlights).toEqual([
      { title: 'Storage Inspector', kind: 'added', extra: 'storage' },
      { title: "What's new", kind: 'added' },
      { title: 'aws eks get-token arguments read as the CLI reads them', kind: 'fixed' },
    ]);
    expect(releases[2]?.highlights).toEqual([]);
    expect(releases[3]?.highlights).toEqual([]);
  });

  it('names the extra an entry introduces, and never draws a marker as a word (#121)', () => {
    const [one] = parseChangelog('## 1.0.0\n\n### Added\n\n- **Saved views** <!-- extra: views --> and <!-- a note -->.\n- **Plain.**\n');
    expect(one?.highlights).toEqual([
      { title: 'Saved views', kind: 'added', extra: 'views' },
      { title: 'Plain', kind: 'added' },
    ]);
    // A lead with no bold: its first sentence, the markers gone.
    expect(parseChangelog('## 1.0.0\n\n### Added\n\n- Saved views <!-- extra: views -->. More.\n')[0]?.highlights).toEqual([
      { title: 'Saved views', kind: 'added', extra: 'views' },
    ]);
  });

  it('keeps an extra listed past the limit, before the cut (#125)', () => {
    const plain = ['One', 'Two', 'Three', 'Four', 'Five'].map((word) => `- **${word}.**`).join('\n');
    const [one] = parseChangelog(`## 1.0.0\n\n### Added\n\n${plain}\n- **Storage Inspector** <!-- extra: storage -->\n`);
    expect(one?.highlights.map((highlight) => [highlight.title, highlight.extra])).toEqual([
      ['Storage Inspector', 'storage'],
      ['One', undefined],
      ['Two', undefined],
      ['Three', undefined],
      ['Four', undefined],
    ]);
  });

  it('never promotes a Fixed entry marked as an extra and a highlight, nor lets it push an Added extra out (#182)', () => {
    const added = ['One', 'Two', 'Three', 'Four'].map((word) => `- **${word}.**`).join('\n');
    const [one] = parseChangelog(
      `## 1.0.0\n\n### Added\n\n${added}\n- **Storage Inspector** <!-- extra: storage -->\n\n` +
        '### Fixed\n\n- **Redis tile fixed** <!-- extra: redis --> <!-- highlight -->\n',
    );
    expect(one?.highlights.map((highlight) => [highlight.title, highlight.kind, highlight.extra])).toEqual([
      ['Storage Inspector', 'added', 'storage'],
      ['One', 'added', undefined],
      ['Two', 'added', undefined],
      ['Three', 'added', undefined],
      ['Four', 'added', undefined],
    ]);
    // With room, the fix is drawn as a fix, in its place.
    const [few] = parseChangelog('## 1.0.0\n\n### Added\n\n- **One.**\n\n### Fixed\n\n- **Redis tile fixed** <!-- extra: redis --> <!-- highlight -->\n');
    expect(few?.highlights).toEqual([
      { title: 'One', kind: 'added' },
      { title: 'Redis tile fixed', kind: 'fixed' },
    ]);
  });

  it('reads an extra key in lower case, with _ as - (#125)', () => {
    const [one] = parseChangelog('## 1.0.0\n\n### Added\n\n- **Saved views** <!--extra:Saved_Views-->\n');
    expect(one?.highlights).toEqual([{ title: 'Saved views', kind: 'added', extra: 'saved-views' }]);
  });

  it('keeps at most MAX_HIGHLIGHTS per release', () => {
    expect(releases[0]?.highlights).toHaveLength(MAX_HIGHLIGHTS);
    expect(releases[0]?.highlights[0]?.title).toBe('Saved views');
    expect(releases[0]?.highlights.map((one) => one.title)).not.toContain('Six, past the limit');
  });

  it('aggregates every release since the one seen, newest first, and only those with highlights', () => {
    expect(highlightsBetween(releases, '0.1.1', '0.3.0').map((one) => one.version)).toEqual(['0.3.0', '0.2.0']);
    expect(highlightsBetween(releases, '0.2.0', '0.2.0')).toEqual([]);
    expect(highlightsBetween(releases, '0.1.1', '0.1.2')).toEqual([]);
    // Not past the running build: a newer changelog entry is not shown early.
    expect(highlightsBetween(releases, '0.1.1', '0.2.0').map((one) => one.version)).toEqual(['0.2.0']);
  });

  it('orders versions numerically, a pre-release before its release', () => {
    expect(compareVersions('0.10.0', '0.9.9')).toBeGreaterThan(0);
    expect(compareVersions('0.2.0-rc.1', '0.2.0')).toBeLessThan(0);
    expect(compareVersions('0.2.0', '0.2.0')).toBe(0);
    expect(compareVersions('1.0.0', '0.99.99')).toBeGreaterThan(0);
    // Pre-releases as semver orders them: numbers by value, before words, and
    // a shorter list before a longer one it begins.
    expect(compareVersions('0.2.0-rc.10', '0.2.0-rc.9')).toBeGreaterThan(0);
    expect(compareVersions('0.2.0-1', '0.2.0-alpha')).toBeLessThan(0);
    expect(compareVersions('0.2.0-rc.1', '0.2.0-rc.1.1')).toBeLessThan(0);
    expect(compareVersions('0.2.0-beta', '0.2.0-alpha')).toBeGreaterThan(0);
    expect(compareVersions('0.2.0-rc.2', '0.2.0-rc.2')).toBe(0);
  });

  it("reads this repository's own CHANGELOG.md", () => {
    expect(RELEASES.some((one) => one.version === '0.1.0')).toBe(true);
    // 0.1.2, released: its Added entries by their bold lead, five at most.
    const released = RELEASES.find((one) => one.version === '0.1.2');
    expect(released?.highlights.slice(0, 2).map((one) => one.title)).toEqual(['Releases are signed', 'The desktop updates itself']);
    expect(released?.highlights).toHaveLength(MAX_HIGHLIGHTS);
    for (const one of RELEASES) expect(one.highlights.length).toBeLessThanOrEqual(MAX_HIGHLIGHTS);
    // 0.2.1 introduced Storage Inspector, marked for the dialog's New extra line.
    expect(RELEASES.find((one) => one.version === '0.2.1')?.highlights.find((one) => one.extra !== undefined)).toEqual({
      title: 'Storage Inspector',
      kind: 'added',
      extra: 'storage',
    });
  });
});

describe('the release pattern', () => {
  it('is the one the API contract spells, which a Go test pins to the store', () => {
    // The walk to go.mod, as TerminalPane.test.tsx reads a file outside web/.
    let root = resolve(process.cwd());
    while (!existsSync(join(root, 'go.mod')) && dirname(root) !== root) root = dirname(root);
    const contract = readFileSync(join(root, 'api', 'openapi.yaml'), 'utf8');
    expect(contract).toContain(`pattern: '${RELEASE.source}'`);
  });
});
