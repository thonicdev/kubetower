import changelog from 'virtual:kubetower-changelog';

/**
 * What's new (#80): the highlights of each release, read from CHANGELOG.md,
 * which the build imports into the bundle. Nothing is fetched.
 *
 * A highlight is an entry of a release's `### Added`, or of its `### Fixed`
 * when it carries `<!-- highlight -->`. It is drawn as its bold lead (the
 * changelog's own short title) and at most MAX_HIGHLIGHTS per release.
 */

export interface Release {
  version: string;
  highlights: string[];
}

export const MAX_HIGHLIGHTS = 5;

const HEADING = /^## +([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)(?:\s|$)/;
const MARK = '<!-- highlight -->';

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
    if (!counts || release.highlights.length >= MAX_HIGHLIGHTS) return;
    const title = titleOf(whole);
    if (title !== '') release.highlights.push(title);
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
  return releases;
}

/** An entry's short title: its bold lead, else its first sentence, as text. */
function titleOf(entry: string): string {
  const text = entry.replace(MARK, '').trim();
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

/** Compares two release versions: negative when a is older than b. */
export function compareVersions(a: string, b: string): number {
  const [coreA = '', preA] = a.split('-', 2);
  const [coreB = '', preB] = b.split('-', 2);
  const partsA = coreA.split('.').map(Number);
  const partsB = coreB.split('.').map(Number);
  for (let i = 0; i < 3; i++) {
    const diff = (partsA[i] ?? 0) - (partsB[i] ?? 0);
    if (diff !== 0) return diff;
  }
  // A pre-release comes before its release.
  if (preA === undefined) return preB === undefined ? 0 : 1;
  if (preB === undefined) return -1;
  return preA < preB ? -1 : preA > preB ? 1 : 0;
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

/** The layout harness's seam: draws the banner from fake releases. */
export function replaceReleases(releases: readonly Release[]): void {
  RELEASES = releases;
}
