import changelog from 'virtual:kubetower-changelog';

/**
 * What's new (#80): the highlights of each release, read from CHANGELOG.md,
 * which the build imports into the bundle. Nothing is fetched.
 *
 * A highlight is an entry of a release's `### Added`, or of its `### Fixed`
 * when it carries `<!-- highlight -->`. It is drawn as its bold lead (the
 * changelog's own short sentence) and at most MAX_HIGHLIGHTS per release. An
 * entry carrying `<!-- extra: <key> -->` introduces that extra: the dialog
 * draws it first, as a New extra line (#121), and it is kept before the cut to
 * MAX_HIGHLIGHTS, wherever the release lists it (#125). Only an Added entry
 * does; the marker on a Fixed entry is ignored. Its key is read in
 * lower case with `_` as `-`; `app/extras/changelog.test.ts` refuses a marker
 * naming no shipped extra.
 */

/** One highlight: its bold lead, the section it came from (#107), and the
 *  extra it introduces, by key (#121). */
export interface Highlight {
  title: string;
  kind: 'added' | 'fixed';
  extra?: string | undefined;
}

export interface Release {
  version: string;
  highlights: Highlight[];
}

export const MAX_HIGHLIGHTS = 5;

const HEADING = /^## +([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)(?:\s|$)/;
const MARK = '<!-- highlight -->';
const EXTRA = /<!--\s*extra:\s*([^\s>]+?)\s*-->/;
/** Any HTML comment: a marker, never a word of the title. */
const COMMENT = /<!--[\s\S]*?-->/g;

/** The releases of a changelog, newest first as it lists them. */
export function parseChangelog(text: string): Release[] {
  const releases: Release[] = [];
  let release: Release | undefined;
  let section = '';
  let item: string[] | undefined;
  const close = (): void => {
    if (release === undefined || item === undefined) return;
    const whole = item.join(' ');
    item = undefined;
    const counts = section === 'added' || (section === 'fixed' && whole.includes(MARK));
    if (!counts) return;
    const title = titleOf(whole);
    // Only an Added entry introduces an extra: a Fixed one marked so stays a
    // fix, never promoted before an Added extra (#182).
    const extra = section === 'added' ? extraKey(whole) : undefined;
    if (title !== '') {
      release.highlights.push({ title, kind: section === 'added' ? 'added' : 'fixed', ...(extra === undefined ? {} : { extra }) });
    }
  };
  for (const line of text.split(/\r?\n/)) {
    const heading = HEADING.exec(line);
    if (heading !== null) {
      close();
      release = { version: heading[1] ?? '', highlights: [] };
      releases.push(release);
      section = '';
      continue;
    }
    if (line.startsWith('### ')) {
      close();
      section = line.slice(4).trim().toLowerCase();
      continue;
    }
    if (line.startsWith('## ')) {
      close();
      release = undefined;
      continue;
    }
    if (/^[-*] /.test(line)) {
      close();
      item = [line.slice(2)];
      continue;
    }
    if (item !== undefined && /^\s+\S/.test(line)) {
      item.push(line.trim());
      continue;
    }
    if (line.trim() === '' || !/^\s/.test(line)) close();
  }
  close();
  // The extras first, then the cut: an extra listed sixth is still shown.
  for (const one of releases) {
    const extras = one.highlights.filter((highlight) => highlight.extra !== undefined);
    const rest = one.highlights.filter((highlight) => highlight.extra === undefined);
    one.highlights = [...extras, ...rest].slice(0, MAX_HIGHLIGHTS);
  }
  return releases;
}

/** The key an entry's `<!-- extra: KEY -->` names, in lower case with `_` as `-`. */
function extraKey(entry: string): string | undefined {
  const key = EXTRA.exec(entry)?.[1];
  return key === undefined ? undefined : key.toLowerCase().replaceAll('_', '-');
}

/** An entry's short title: its bold lead, else its first sentence, as text. */
function titleOf(entry: string): string {
  const text = entry.replace(COMMENT, '').trim();
  const bold = /^\*\*(.+?)\*\*/.exec(text);
  const lead = bold?.[1] ?? text.split(/(?<=\.)\s/)[0] ?? text;
  return plain(lead)
    .replace(/\s*\((?:#\d+(?:,\s*)?)+\)/g, '')
    .replace(/\.$/, '')
    .trim();
}

/** Markdown links, code and emphasis reduced to their text. */
function plain(text: string): string {
  return text
    .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/[`*_]/g, '')
    .replace(/\s+/g, ' ');
}

/** A version's numbers, and its pre-release identifiers when it has some. */
function split(version: string): { core: number[]; pre: string[] | undefined } {
  const dash = version.indexOf('-');
  const core = (dash < 0 ? version : version.slice(0, dash)).split('.').map(Number);
  return { core, pre: dash < 0 ? undefined : version.slice(dash + 1).split('.') };
}

const NUMERIC = /^[0-9]+$/;

/** Pre-release identifiers as semver orders them: numbers by value, and before words. */
function comparePre(a: readonly string[], b: readonly string[]): number {
  for (let i = 0; i < Math.min(a.length, b.length); i++) {
    const x = a[i] ?? '';
    const y = b[i] ?? '';
    const numberX = NUMERIC.test(x);
    const numberY = NUMERIC.test(y);
    if (numberX && numberY) {
      const diff = Number(x) - Number(y);
      if (diff !== 0) return diff;
    } else if (numberX !== numberY) {
      return numberX ? -1 : 1;
    } else if (x !== y) {
      return x < y ? -1 : 1;
    }
  }
  return a.length - b.length;
}

/** Compares two release versions: negative when a is older than b. */
export function compareVersions(a: string, b: string): number {
  const one = split(a);
  const other = split(b);
  for (let i = 0; i < 3; i++) {
    const diff = (one.core[i] ?? 0) - (other.core[i] ?? 0);
    if (diff !== 0) return diff;
  }
  // A pre-release comes before its release.
  if (one.pre === undefined) return other.pre === undefined ? 0 : 1;
  if (other.pre === undefined) return -1;
  return comparePre(one.pre, other.pre);
}

/**
 * The releases newer than `seen`, up to and including `current`, that have
 * highlights, newest first. Every release up to `current` when nothing was seen.
 */
export function highlightsBetween(releases: readonly Release[], seen: string | undefined, current: string): Release[] {
  return releases
    .filter((one) => compareVersions(one.version, current) <= 0)
    .filter((one) => seen === undefined || compareVersions(one.version, seen) > 0)
    .filter((one) => one.highlights.length > 0)
    .sort((a, b) => compareVersions(b.version, a.version));
}

/** This build's changelog. */
export let RELEASES: readonly Release[] = parseChangelog(changelog);

/** The layout harness's seam: draws the dialog from fake releases. */
export function replaceReleases(releases: readonly Release[]): void {
  RELEASES = releases;
}
