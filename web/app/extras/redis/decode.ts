import { asJson } from './keys';

/**
 * A value made readable before it is drawn, and nothing drawn that is not.
 *
 * Applications store their values in whatever form suits them: JSON, JSON in
 * base64, a gzip of either, or bytes no person can read. Drawn as they come,
 * the last three are a wall of noise in a table meant to be read. So each
 * value goes through the steps below, in order, and the page says which ones
 * it took:
 *
 * 1. **Bytes the server sent** (`raw`, set only on an element that was not
 *    UTF-8) are the starting point when there are some: the text rendering of
 *    such an element has lost them to U+FFFD, and nothing can be inflated or
 *    shown in hex from a rendering.
 * 2. **gzip**, recognized by its magic number (1f 8b), is inflated in the
 *    browser (`DecompressionStream`), bounded, and what comes out goes through
 *    these steps again.
 * 3. **base64** is decoded only on a strict reading - the standard alphabet,
 *    padded to a multiple of four, at least 12 characters - **and only kept
 *    when what it decodes to is readable** or is a gzip: plenty of ordinary
 *    words are valid base64, and decoding one into noise would replace a
 *    readable value with an unreadable one.
 * 4. **JSON** (an object or an array) is drawn indented, under the same
 *    lossless rule as a string's formatted view (`asJson`).
 * 5. **Readable text** is UTF-8 that decodes without error and holds no NUL
 *    and almost no other control character.
 *
 * Whatever yields no readable text is **binary**: the page draws its size and,
 * on a click, a bounded hex view - never the bytes as characters.
 *
 * Pure apart from the inflation, which the browser only offers as a stream, so
 * every rule here is tested on its own.
 */

/** A value that reads as text, after the steps it took. */
export interface Readable {
  kind: 'text';
  text: string;
  /** Drawn indented as JSON. */
  json: boolean;
  /** The decodings applied, in order: `base64`, `gzip`. */
  via: string[];
  /** The inflation stopped before the end: the input was cut, or the output
   *  reached the bound. */
  cut?: boolean;
  /** Drawn from the server's U+FFFD rendering, with no bytes to decode. */
  lossy?: boolean;
}

/** A value that yields no readable text: drawn as its size and in hex. */
export interface Binary {
  kind: 'binary';
  bytes: Uint8Array;
  via: string[];
  cut?: boolean;
}

export type Decoded = Readable | Binary;

/** A step still to take: bytes that are a gzip, to inflate. */
export interface Gzip {
  kind: 'gzip';
  bytes: Uint8Array;
  via: string[];
}

/** The most inflated output kept: a value small on the wire can be huge once inflated. */
export const INFLATE_LIMIT = 1 << 20;

/** The decodings followed in a row, so a value that decodes to itself cannot loop. */
const DEPTH = 4;

const STRICT_BASE64 = /^(?:[A-Za-z0-9+/]{4})+(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/;

/** Bytes of a base64 text, any alphabet and padding, as the server writes `raw`. */
export function fromBase64(text: string): Uint8Array | undefined {
  const standard = text.replace(/-/g, '+').replace(/_/g, '/').replace(/=+$/, '');
  if (!/^[A-Za-z0-9+/]*$/.test(standard)) return undefined;
  try {
    const binary = atob(standard + '='.repeat((4 - (standard.length % 4)) % 4));
    const out = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
    return out;
  } catch {
    return undefined;
  }
}

/** The bytes of a text that is base64 on the strict reading of step 3, or nothing. */
export function strictBase64(text: string): Uint8Array | undefined {
  if (text.length < 12 || text.length % 4 !== 0 || !STRICT_BASE64.test(text)) return undefined;
  return fromBase64(text);
}

/** The bytes start with gzip's magic number. */
export function isGzip(bytes: Uint8Array): boolean {
  return bytes.length >= 2 && bytes[0] === 0x1f && bytes[1] === 0x8b;
}

/** The bytes as text when they read as text (step 5), else nothing. */
export function readableText(bytes: Uint8Array): string | undefined {
  let text: string;
  try {
    text = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  } catch {
    return undefined;
  }
  let controls = 0;
  for (let i = 0; i < text.length; i++) {
    const c = text.charCodeAt(i);
    if (c === 0) return undefined;
    if ((c < 0x20 && c !== 0x09 && c !== 0x0a && c !== 0x0d) || c === 0x7f) controls++;
  }
  return controls * 100 > text.length ? undefined : text;
}

function fromBytes(bytes: Uint8Array, via: string[], depth: number): Decoded | Gzip {
  if (isGzip(bytes) && depth < DEPTH) return { kind: 'gzip', bytes, via };
  const text = readableText(bytes);
  if (text === undefined) return { kind: 'binary', bytes, via };
  return fromText(text, via, depth);
}

function fromText(text: string, via: string[], depth: number): Readable | Gzip {
  const json = asJson(text);
  if (json !== undefined) return { kind: 'text', text: json, json: true, via };
  const decoded = depth < DEPTH ? strictBase64(text) : undefined;
  if (decoded !== undefined) {
    const next = fromBytes(decoded, [...via, 'base64'], depth + 1);
    // Base64 whose bytes read as nothing was a word, not an encoding.
    if (next.kind !== 'binary') return next;
  }
  return { kind: 'text', text, json: false, via };
}

/**
 * Every step that needs no stream: the value read, or a gzip still to
 * inflate. `raw` is the element's bytes when the server sent them; `binary`
 * without `raw` is a rendering with nothing behind it, drawn as it is.
 */
export function decodeNow(value: string, raw?: string, binary?: boolean): Decoded | Gzip {
  if (raw !== undefined && raw !== '') {
    const bytes = fromBase64(raw);
    if (bytes !== undefined) return fromBytes(bytes, [], 0);
  }
  if (binary === true) return { kind: 'text', text: value, json: false, via: [], lossy: true };
  return fromText(value, [], 0);
}

/**
 * Inflate a gzip, bounded by `limit`. What came out before an error is kept
 * and said to be cut: an element the server clipped is a gzip with no end,
 * and its start is still worth reading. Nothing at all is `undefined`.
 */
export async function inflate(bytes: Uint8Array, limit = INFLATE_LIMIT): Promise<{ bytes: Uint8Array; cut: boolean } | undefined> {
  if (typeof DecompressionStream !== 'function') return undefined;
  const input = new ReadableStream<BufferSource>({
    start(controller) {
      // A copy on its own buffer: the stream's type wants an ArrayBuffer,
      // and a view may sit on a shared one.
      controller.enqueue(new Uint8Array(bytes));
      controller.close();
    },
  });
  const reader = input.pipeThrough(new DecompressionStream('gzip')).getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  let cut = false;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      chunks.push(value);
      size += value.length;
      if (size >= limit) {
        cut = true;
        void reader.cancel().catch(() => undefined);
        break;
      }
    }
  } catch {
    cut = true;
  }
  if (size === 0) return undefined;
  const out = new Uint8Array(Math.min(size, limit));
  let at = 0;
  for (const chunk of chunks) {
    const take = Math.min(chunk.length, out.length - at);
    out.set(chunk.subarray(0, take), at);
    at += take;
    if (at >= out.length) break;
  }
  return { bytes: out, cut };
}

/** Every step, the inflation included. */
export async function decode(value: string, raw?: string, binary?: boolean): Promise<Decoded> {
  let step = decodeNow(value, raw, binary);
  let cut = false;
  let depth = 0;
  while (step.kind === 'gzip') {
    const out = depth < DEPTH ? await inflate(step.bytes) : undefined;
    if (out === undefined) return { kind: 'binary', bytes: step.bytes, via: step.via };
    cut ||= out.cut;
    depth++;
    // A cut inflation may end inside a character: its last bytes are dropped
    // until the rest reads, rather than the whole value turning binary.
    let bytes = out.bytes;
    if (out.cut && readableText(bytes) === undefined) {
      for (let drop = 1; drop <= 3 && drop < out.bytes.length; drop++) {
        const shorter = out.bytes.subarray(0, out.bytes.length - drop);
        if (readableText(shorter) !== undefined) {
          bytes = shorter;
          break;
        }
      }
    }
    step = fromBytes(bytes, [...step.via, 'gzip'], depth);
  }
  return cut ? { ...step, cut } : step;
}

/** What the small mark beside a decoded value says, or nothing when it was drawn as it came. */
export function decodedMark(d: Decoded): string | undefined {
  const steps = [...d.via];
  if (d.kind === 'text' && d.json) steps.push('JSON');
  else if (d.kind === 'text' && d.via.length > 0) steps.push('text');
  if (steps.length === 0) return undefined;
  return steps.join(' → ') + (d.cut === true ? ' (cut)' : '');
}

/** The most bytes the hex view draws: past that it is a dump nobody reads on a page. */
export const HEX_LIMIT = 4096;

/** One line of the hex view: the offset, sixteen bytes in hex, the same bytes as ASCII. */
export interface HexRow {
  offset: string;
  hex: string;
  ascii: string;
}

export function hexRows(bytes: Uint8Array, limit = HEX_LIMIT): HexRow[] {
  const rows: HexRow[] = [];
  const end = Math.min(bytes.length, limit);
  for (let at = 0; at < end; at += 16) {
    const line = bytes.subarray(at, Math.min(at + 16, end));
    const pairs = [...line].map((b) => b.toString(16).padStart(2, '0'));
    rows.push({
      offset: at.toString(16).padStart(8, '0'),
      hex: [pairs.slice(0, 8).join(' '), pairs.slice(8).join(' ')].filter((p) => p !== '').join('  '),
      ascii: [...line].map((b) => (b >= 0x20 && b < 0x7f ? String.fromCharCode(b) : '.')).join(''),
    });
  }
  return rows;
}

/** The characters and lines a long value shows before it is unfolded. */
export const PREVIEW_CHARS = 200;
export const PREVIEW_LINES = 2;

/** A long text's start, or nothing when the text is short enough to draw whole. */
export function preview(text: string, chars = PREVIEW_CHARS, lines = PREVIEW_LINES): string | undefined {
  let end = Math.min(text.length, chars);
  let at = -1;
  for (let n = 0; n < lines; n++) {
    at = text.indexOf('\n', at + 1);
    if (at === -1) break;
  }
  if (at !== -1 && at < end) end = at;
  return end >= text.length ? undefined : text.slice(0, end);
}
