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
      { title: "What's new", kind: 'added' },
      { title: 'aws eks get-token arguments read as the CLI reads them', kind: 'fixed' },
    ]);
    expect(releases[2]?.highlights).toEqual([]);
    expect(releases[3]?.highlights).toEqual([]);
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
