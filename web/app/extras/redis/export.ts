import type { Door, KeyDetail, KeyRow, Scan } from './data';
import { isPartial } from './keys';

/**
 * The quick exports of the instance page: the key list
 * of the scan round on screen, as CSV or JSON, and an opened key's value - the
 * pages of it loaded so far - as JSON or as its raw text.
 *
 * **Built in the browser from what the page already holds**, and nothing
 * else: no route is asked for more, nothing is written anywhere but the
 * person's own download. So an export is exactly as bounded as the page - one
 * scan round, 64 KiB of a string, the pages of elements loaded - and says so in the file
 * (`partial`, `note`) as well as on the button.
 *
 * **No credential is ever in one.** Every builder takes the fields it writes,
 * by name; none spreads an answer of the route into a file, so the
 * credential's provenance (Secret, key, pod) - which the page shows - is not
 * exported either, and a password is not in any shape this page receives.
 */

/** Where the data came from, written at the head of every JSON export. */
export interface Origin {
  cluster: string;
  instance: Pick<Door, 'namespace' | 'service' | 'port'>;
  db: number;
}

/** Whether the keys on screen are every key there is: one round that went all the way round from the start. */
export function scanIsWhole(scan: Pick<Scan, 'done' | 'cursor'>): boolean {
  return scan.done && (scan.cursor === '' || scan.cursor === '0');
}

/** The sentence a partial key export carries, and the button shows. */
export function scanPartialNote(scan: Pick<Scan, 'done' | 'cursor' | 'match'>): string | undefined {
  if (scanIsWhole(scan)) return undefined;
  return scan.done
    ? 'The last round of a scan read over several rounds: the keys of the earlier rounds are not in it.'
    : 'One round of a scan that is not finished: the keyspace holds keys this export does not.';
}

/**
 * One CSV cell. Quoted when it holds a comma, a quote or a line break, its
 * quotes doubled (RFC 4180). A cell a spreadsheet would run as a formula -
 * starting `=`, `+`, `-`, `@`, a tab or a carriage return - is written with a
 * leading `'`, because a key name is data somebody else's application chose,
 * and opening an export must not execute it. The JSON export is exact.
 *
 * `guard` is false for the raw column only: base64url is letters, digits, `-`
 * and `_`, so nothing in it can call a function, and a leading `'` would make
 * the one column that exists to be decoded undecodable (the review of
 * da9da8f).
 */
export function csvCell(value: string, guard = true): string {
  const safe = guard && /^[=+\-@\t\r]/.test(value) ? `'${value}` : value;
  return /[",\r\n]/.test(safe) ? `"${safe.replaceAll('"', '""')}"` : safe;
}

export function keysCsv(keys: readonly KeyRow[]): string {
  const lines = ['key,type,ttl,memory_bytes,raw_base64url'];
  for (const k of keys) {
    const cells = [k.name, k.type, k.ttl, k.memory === undefined ? '' : String(k.memory)].map((v) => csvCell(v));
    lines.push([...cells, csvCell(k.raw ?? '', false)].join(','));
  }
  return lines.join('\r\n') + '\r\n';
}

export function keysJson(origin: Origin, scan: Scan): string {
  const note = scanPartialNote(scan);
  return (
    JSON.stringify(
      {
        cluster: origin.cluster,
        instance: { namespace: origin.instance.namespace, service: origin.instance.service, port: origin.instance.port },
        db: origin.db,
        match: scan.match,
        cursor: scan.cursor,
        ...(scan.next === undefined ? {} : { next: scan.next }),
        partial: note !== undefined,
        ...(note === undefined ? {} : { note }),
        keys: scan.keys.map((k) => ({
          name: k.name,
          ...(k.raw === undefined ? {} : { raw: k.raw }),
          type: k.type,
          ttl: k.ttl,
          ...(k.memory === undefined ? {} : { memoryBytes: k.memory }),
        })),
      },
      null,
      2,
    ) + '\n'
  );
}

/** A value as JSON: a string as a string, a hash as an object, the rest as arrays. */
export function valueJson(origin: Origin, detail: KeyDetail): string {
  let value: unknown;
  switch (detail.type) {
    case 'string':
      value = detail.entries[0]?.value ?? null;
      break;
    case 'hash': {
      // An object, unless two fields read the same - two fields that are not
      // text both drawn with U+FFFD - which an object would fold into one:
      // then the pairs, every one of them.
      const fields = detail.entries.map((e) => e.field ?? '');
      value =
        new Set(fields).size === fields.length
          ? Object.fromEntries(detail.entries.map((e) => [e.field ?? '', e.value]))
          : detail.entries.map((e) => ({ field: e.field ?? '', value: e.value }));
      break;
    }
    case 'zset':
      value = detail.entries.map((e) => ({ member: e.value, score: e.score ?? null }));
      break;
    case 'stream':
      // An entry's pairs arrive as JSON text; written as the object they are,
      // or as the text when it does not parse (a pair that was not UTF-8).
      value = detail.entries.map((e) => ({ id: e.field ?? '', fields: parsedOr(e.value) }));
      break;
    default:
      value = detail.entries.map((e) => e.value);
  }
  const binary = detail.entries.some((e) => e.binary === true);
  // What is held, against what the key holds, while pages are left to read:
  // the file is then a sample, and says of how much.
  const more = detail.next !== undefined && detail.next !== '';
  const loaded = !more ? undefined : detail.length === undefined ? String(detail.entries.length) : `${String(detail.entries.length)} of ${String(detail.length)}`;
  return (
    JSON.stringify(
      {
        cluster: origin.cluster,
        instance: { namespace: origin.instance.namespace, service: origin.instance.service, port: origin.instance.port },
        db: origin.db,
        key: detail.name,
        ...(detail.raw === undefined ? {} : { raw: detail.raw }),
        type: detail.type,
        ttl: detail.ttl,
        ...(detail.length === undefined ? {} : { length: detail.length }),
        ...(detail.encoding === undefined ? {} : { encoding: detail.encoding }),
        ...(detail.memory === undefined ? {} : { memory: detail.memory }),
        partial: isPartial(detail),
        ...(loaded === undefined ? {} : { loaded }),
        ...(detail.note === undefined || detail.note === '' ? {} : { note: detail.note }),
        ...(binary ? { binary: 'some bytes were not text and are drawn as U+FFFD' } : {}),
        value,
      },
      null,
      2,
    ) + '\n'
  );
}

function parsedOr(text: string): unknown {
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return text;
  }
}

/**
 * A value as raw text: a string as itself; a hash as `field<TAB>value`
 * lines; a sorted set as `member<TAB>score`; a stream as `id<TAB>fields`;
 * a list or a set one element a line. It holds what is loaded, and a raw file
 * has nowhere to say it is partial, so the menu says it instead.
 */
export function valueRaw(detail: KeyDetail): string {
  switch (detail.type) {
    case 'string':
      return detail.entries[0]?.value ?? '';
    case 'hash':
      return detail.entries.map((e) => `${e.field ?? ''}\t${e.value}`).join('\n') + '\n';
    case 'zset':
      return detail.entries.map((e) => `${e.value}\t${e.score ?? ''}`).join('\n') + '\n';
    case 'stream':
      return detail.entries.map((e) => `${e.field ?? ''}\t${e.value}`).join('\n') + (detail.entries.length === 0 ? '' : '\n');
    default:
      return detail.entries.map((e) => e.value).join('\n') + (detail.entries.length === 0 ? '' : '\n');
  }
}

/**
 * A file name the browser will accept: the parts joined, everything outside
 * a conservative set replaced, kept short. A name the browser refuses is a
 * download that silently does not happen.
 */
export function fileName(parts: readonly (string | number)[], extension: string): string {
  const stem = parts
    .map((p) => String(p).replace(/[^A-Za-z0-9._-]+/g, '_'))
    .filter((p) => p !== '')
    .join('-')
    .slice(0, 120);
  return `${stem === '' ? 'redis' : stem}.${extension}`;
}

/**
 * Hand a text to the browser as a download. The object URL is revoked a
 * minute later rather than at once: revoking straight away can cancel the
 * download it was made for (the same call `dock/LogPane` makes). Answers
 * false where the browser cannot make one, so the page can say so.
 */
export function download(text: string, name: string, type: string): boolean {
  if (typeof URL.createObjectURL !== 'function') return false;
  const url = URL.createObjectURL(new Blob([text], { type }));
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = name;
  anchor.rel = 'noopener';
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
  return true;
}
