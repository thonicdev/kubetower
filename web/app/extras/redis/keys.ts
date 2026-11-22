import type { KeyDetail, KeyRow } from './data';

/**
 * The key list's and the value view's arithmetic, kept apart from the pages so
 * it is tested on its own: the tree by `:` prefix, the
 * sizes, the type badges' words, a string read as JSON when it is JSON.
 *
 * Nothing here reaches a server. It shapes what one scan round or one opened
 * key already brought back.
 */

/** Redis's own convention for a key's namespace, `user:42:session`. */
export const SEPARATOR = ':';

export type TreeItem =
  | { kind: 'folder'; path: string; label: string; count: number; children: TreeItem[] }
  | { kind: 'key'; label: string; row: KeyRow };

interface Building {
  folders: Map<string, { label: string; path: string; node: Building }>;
  keys: { label: string; row: KeyRow }[];
}

/**
 * The keys of one scan round as a tree by `:` prefix: `user:42:name` is the
 * key `name` under the folder `user:` under the folder `user:42:`.
 *
 * A key that is also a prefix of others (`user` beside `user:1`) is a key and
 * a folder side by side, never one hiding the other. An empty segment
 * (`a::b`) is a folder of its own, drawn as such, so the tree never claims two
 * different keys are one. Folders before keys, each in the order a person
 * reads them. A key that is not text keeps its raw form on its row, so it can
 * still be opened.
 */
export function keyTree(rows: readonly KeyRow[], separator = SEPARATOR): TreeItem[] {
  const root: Building = { folders: new Map(), keys: [] };
  for (const row of rows) {
    const parts = row.name.split(separator);
    let node = root;
    let path = '';
    for (const part of parts.slice(0, -1)) {
      path += part + separator;
      let next = node.folders.get(part);
      if (next === undefined) {
        next = { label: part + separator, path, node: { folders: new Map(), keys: [] } };
        node.folders.set(part, next);
      }
      node = next.node;
    }
    // A key ending in the separator (`user:`) has an empty last segment: it is
    // drawn by its whole name rather than as a row with no name at all.
    const last = parts[parts.length - 1] ?? '';
    node.keys.push({ label: last === '' ? row.name : last, row });
  }
  return finish(root);
}

function finish(node: Building): TreeItem[] {
  const folders = [...node.folders.values()]
    .sort((a, b) => compare(a.label, b.label))
    .map((f) => {
      const children = finish(f.node);
      return { kind: 'folder' as const, path: f.path, label: f.label, count: countKeys(children), children };
    });
  const keys = [...node.keys]
    .sort((a, b) => compare(a.label, b.label))
    .map((k) => ({ kind: 'key' as const, label: k.label, row: k.row }));
  return [...folders, ...keys];
}

function countKeys(items: readonly TreeItem[]): number {
  return items.reduce((n, item) => n + (item.kind === 'key' ? 1 : item.count), 0);
}

/** Natural order, so `item:2` comes before `item:10`. */
const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });
function compare(a: string, b: string): number {
  return collator.compare(a, b) || (a < b ? -1 : a > b ? 1 : 0);
}

/** Every folder path in a tree, for "expand all". */
export function folderPaths(items: readonly TreeItem[]): string[] {
  return items.flatMap((item) => (item.kind === 'folder' ? [item.path, ...folderPaths(item.children)] : []));
}

/** Bytes as a person reads them: `72 B`, `1.5 KiB`, `3.2 MiB`. */
export function bytes(n: number | undefined): string {
  if (n === undefined || !Number.isFinite(n) || n < 0) return '';
  if (n < 1024) return `${n} B`;
  const units = ['KiB', 'MiB', 'GiB', 'TiB'];
  let value = n / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value >= 10 ? value.toFixed(0) : value.toFixed(1)} ${units[unit] ?? 'TiB'}`;
}

/** The short word a type badge shows. A module's type is shown as itself. */
export function typeWord(type: string): string {
  switch (type) {
    case 'string':
      return 'str';
    case 'hash':
      return 'hash';
    case 'list':
      return 'list';
    case 'set':
      return 'set';
    case 'zset':
      return 'zset';
    case 'stream':
      return 'stream';
    default:
      return type;
  }
}

/**
 * The badge's color, looked up and never built: Tailwind finds a class by
 * reading the source, so `text-${x}` would be a class in no stylesheet. The
 * hues are the theme's nameplate tokens, so a type keeps its color
 * across every page that draws one.
 */
export const TYPE_TONE: Readonly<Record<string, string>> = {
  string: 'text-plate-blue-pale',
  hash: 'text-plate-green-pale',
  list: 'text-plate-amber-pale',
  set: 'text-plate-teal-pale',
  zset: 'text-plate-violet-pale',
  stream: 'text-plate-pink-pale',
};

/**
 * A string's value read as JSON, pretty-printed - **only when it is an object
 * or an array**: `42` and `"x"` parse as JSON too, and printing them again
 * changes nothing but the claim. A value cut at 64 KiB does not parse, and is
 * shown as the text it is.
 *
 * **And only when formatting it loses nothing.** A browser's JSON is lossy:
 * `12345678901234567890` comes back `12345678901234567000`, `1.50` comes back
 * `1.5`, and of a key written twice only the last survives (the review of
 * da9da8f). The formatted view is the default and what its Copy copies, so a
 * value that would not survive the round trip - its re-serialized form not
 * equal to the stored text with the whitespace between tokens taken out - is
 * shown as stored. Conservative: an escaped `é` or `\/` also falls back
 * to the text, which is never wrong.
 */
export function asJson(text: string): string | undefined {
  const trimmed = text.trimStart();
  if (!trimmed.startsWith('{') && !trimmed.startsWith('[')) return undefined;
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return undefined;
  }
  if (parsed === null || typeof parsed !== 'object') return undefined;
  if (JSON.stringify(parsed) !== withoutWhitespace(text)) return undefined;
  return JSON.stringify(parsed, null, 2);
}

/** JSON text with the whitespace between its tokens removed, its strings untouched. */
function withoutWhitespace(text: string): string {
  let out = '';
  let inString = false;
  let escaped = false;
  for (const ch of text) {
    if (inString) {
      out += ch;
      if (escaped) escaped = false;
      else if (ch === '\\') escaped = true;
      else if (ch === '"') inString = false;
    } else if (ch === '"') {
      inString = true;
      out += ch;
    } else if (ch !== ' ' && ch !== '\n' && ch !== '\r' && ch !== '\t') {
      out += ch;
    }
  }
  return out;
}

/**
 * Whether what is held is the whole value: not when the server's note says
 * something was cut, nor while there are pages left to read.
 */
export function isPartial(detail: Pick<KeyDetail, 'note' | 'next'>): boolean {
  return (detail.note !== undefined && detail.note !== '') || (detail.next !== undefined && detail.next !== '');
}

/** "N of M loaded" while pages are left to read; nothing once the whole key is held. */
export function loadedNote(detail: Pick<KeyDetail, 'entries' | 'length' | 'next'>): string | undefined {
  if (detail.next === undefined || detail.next === '') return undefined;
  return detail.length === undefined ? `${String(detail.entries.length)} loaded` : `${String(detail.entries.length)} of ${String(detail.length)} loaded`;
}
