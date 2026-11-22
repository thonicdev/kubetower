import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

/**
 * `@kubetower/ui`'s peer ranges admit only the release line the workspace
 * installs.
 *
 * The package is private and compiled from source by the app, so its
 * `peerDependencies` are never resolved against anything but this workspace.
 * That is exactly why nothing else holds them: `pnpm install --frozen-lockfile`
 * and `pnpm typecheck` both pass with a range that still admits a major the
 * code was never built or tested against (`react-router: ^7.18.3 || ^8.4.0`
 * after the move to 8). A range is a claim about compatibility; this
 * test makes the claim match what the lockfile installs.
 *
 * A release line is the major for `1.0.0` and above, and `0.<minor>` below
 * it, the way a caret reads them. Only caret ranges joined by `||` are
 * understood; any other syntax fails the test rather than being skipped, so
 * the parser grows with the ranges instead of quietly reading nothing.
 */
// `import.meta.dirname` rather than a `URL`: under jsdom `import.meta.url` is
// not a `file:` URL.
const web = resolve(import.meta.dirname, '..');

const CARET = /^\^(\d+)\.(\d+)\.(\d+)$/;
const VERSION = /^(\d+)\.(\d+)\.(\d+)$/;

type Version = [number, number, number];

function line([major, minor]: Version): string {
  return major > 0 ? `${major}` : `0.${minor}`;
}

function atLeast(a: Version, b: Version): boolean {
  for (let i = 0; i < 3; i++) {
    if (a[i] !== b[i]) return a[i]! > b[i]!;
  }
  return true;
}

/** The lower bound of each `||` alternative of a caret range. */
function parseRange(range: string): Version[] {
  return range.split('||').map((part) => {
    const m = CARET.exec(part.trim());
    if (m === null) {
      throw new Error(`peer range "${range}": "${part.trim()}" is not a caret range this test reads`);
    }
    return [Number(m[1]), Number(m[2]), Number(m[3])];
  });
}

function parseVersion(version: string): Version {
  const m = VERSION.exec(version);
  if (m === null) throw new Error(`installed version "${version}" is not x.y.z`);
  return [Number(m[1]), Number(m[2]), Number(m[3])];
}

/**
 * Why a range does not fit what is installed, or `null` when it fits: every
 * release line it admits is one the workspace installs, and every installed
 * version satisfies it.
 */
function rangeProblem(range: string, installed: readonly string[]): string | null {
  const bounds = parseRange(range);
  const versions = installed.map(parseVersion);
  const installedLines = new Set(versions.map(line));
  const extra = bounds.map(line).filter((l) => !installedLines.has(l));
  if (extra.length > 0) {
    return `admits ${extra.map((l) => `${l}.x`).join(', ')}, which the workspace does not install (installed: ${installed.join(', ')})`;
  }
  const unsatisfied = versions.filter(
    (v) => !bounds.some((b) => line(b) === line(v) && atLeast(v, b)),
  );
  if (unsatisfied.length > 0) {
    return `does not admit the installed ${unsatisfied.map((v) => v.join('.')).join(', ')}`;
  }
  return null;
}

/**
 * The version each importer of `pnpm-lock.yaml` installs, by package name.
 * Read line by line: the `importers:` block is a fixed, two-space-indented
 * shape, and a YAML dependency for one test is not worth it.
 */
function lockedVersions(lockfile: string): Map<string, Set<string>> {
  const out = new Map<string, Set<string>>();
  let inImporters = false;
  let name: string | null = null;
  for (const raw of lockfile.split('\n')) {
    if (/^\S/.test(raw)) {
      inImporters = raw.startsWith('importers:');
      continue;
    }
    if (!inImporters) continue;
    const dep = /^ {6}'?([^':]+)'?:\s*$/.exec(raw);
    if (dep !== null) {
      name = dep[1]!;
      continue;
    }
    const version = /^ {8}version: ([^(\s]+)/.exec(raw);
    if (version !== null && name !== null) {
      const set = out.get(name) ?? new Set<string>();
      set.add(version[1]!);
      out.set(name, set);
      name = null;
    }
  }
  return out;
}

const manifest = JSON.parse(readFileSync(resolve(web, 'ui/package.json'), 'utf8')) as {
  peerDependencies?: Record<string, string>;
};
const peers = Object.entries(manifest.peerDependencies ?? {});
const locked = lockedVersions(readFileSync(resolve(web, 'pnpm-lock.yaml'), 'utf8'));

describe('reading a peer range', () => {
  it('refuses a range that admits a major nothing installs', () => {
    expect(rangeProblem('^7.18.3 || ^8.4.0', ['8.5.0'])).toMatch(/admits 7\.x/);
    expect(rangeProblem('^8.4.0', ['8.5.0'])).toBeNull();
  });

  it('reads a 0.x caret as one minor line', () => {
    expect(rangeProblem('^0.11.0', ['0.11.0'])).toBeNull();
    expect(rangeProblem('^0.10.0 || ^0.11.0', ['0.11.0'])).toMatch(/admits 0\.10\.x/);
  });

  it('refuses a range the installed version does not satisfy', () => {
    expect(rangeProblem('^8.6.0', ['8.5.0'])).toMatch(/does not admit the installed 8\.5\.0/);
  });

  it('fails on syntax it does not read, rather than passing it', () => {
    expect(() => rangeProblem('>=8', ['8.5.0'])).toThrow(/not a caret range/);
  });

  it('finds the importers in the lockfile', () => {
    // A parser that matched nothing would make every peer below "not
    // installed" - a failure, but not the one that explains itself.
    expect(locked.get('react-router')?.size).toBeGreaterThan(0);
  });
});

describe("@kubetower/ui's peer ranges", () => {
  it('declares some', () => {
    expect(peers.length).toBeGreaterThan(0);
  });

  it.each(peers)('%s %s admits only what the workspace installs', (name, range) => {
    const installed = [...(locked.get(name) ?? [])];
    expect(installed, `${name} is not installed by any importer of pnpm-lock.yaml`).not.toEqual([]);
    expect(rangeProblem(range, installed), `${name} ${range}`).toBeNull();
  });
});
