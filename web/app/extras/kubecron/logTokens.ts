/**
 * A log line, cut into what a reader scans for:
 * timestamps, levels, `key=value` keys, quoted strings and a JSON record.
 *
 * **The line's text is never changed**, only cut: joining the tokens gives the
 * line back, ANSI escapes aside, so what is drawn is what the container
 * printed. Anything not recognized is `plain`, and a JSON-looking span that
 * does not parse is prose like the rest of the line - the same rule the
 * console's own log pane keeps (`ui/src/dock/json.ts`).
 */

export type TokenKind =
  | 'plain'
  | 'time'
  | 'error'
  | 'warn'
  | 'info'
  | 'debug'
  | 'key'
  | 'string'
  | 'number'
  | 'literal'
  | 'punct';

export interface Token {
  text: string;
  kind: TokenKind;
}

export type Level = 'error' | 'warn' | 'info' | 'debug';

export interface Line {
  tokens: Token[];
  /** The line's level, when it says one: it tints the whole row. */
  level?: Level;
}

// eslint-disable-next-line no-control-regex -- the escape is what is matched
const ANSI = /\x1b\[[0-9;:?]*[ -/]*[@-~]/g;

export function stripAnsi(text: string): string {
  return text.replace(ANSI, '');
}

/** A level word, in any of the spellings loggers use, or undefined. */
export function levelOf(word: string): Level | undefined {
  switch (word.toLowerCase()) {
    case 'fatal':
    case 'panic':
    case 'crit':
    case 'critical':
    case 'emerg':
    case 'alert':
    case 'error':
    case 'err':
    case 'e':
    case 'f':
      return 'error';
    case 'warn':
    case 'warning':
    case 'w':
      return 'warn';
    case 'info':
    case 'notice':
    case 'i':
      return 'info';
    case 'debug':
    case 'trace':
    case 'd':
      return 'debug';
    default:
      return undefined;
  }
}

/**
 * The prose rules, tried at each position in this order:
 *
 *   1. an ISO-8601 / RFC 3339 timestamp, or a clock time `12:00:01.123`;
 *   2. a level written in capitals (`ERROR`, `WARN`, …) - capitals only, or
 *      "for more info" would be painted as a level;
 *   3. a `key=` of logfmt, whose value is a level when the key is `level`;
 *   4. a double-quoted string.
 */
const PROSE =
  /(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(?::\d{2}(?:[.,]\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?|\b\d{2}:\d{2}:\d{2}(?:[.,]\d+)?\b)|\b(FATAL|PANIC|CRITICAL|ERROR|ERR|WARNING|WARN|INFO|NOTICE|DEBUG|TRACE)\b|(?<![\w.-])([A-Za-z_][\w.-]*)(=)("(?:[^"\\]|\\.)*"|[^\s"]*)|("(?:[^"\\]|\\.)*")/g;

/** klog's header, `I1023 12:00:00.000000 …`: its first letter is the level. */
const KLOG = /^([IWEF])(\d{4}) /;

const LEVEL_KEYS = new Set(['level', 'lvl', 'severity', 'loglevel', 'log.level']);

function prose(text: string, out: Token[]): Level | undefined {
  let level: Level | undefined;
  let at = 0;
  const push = (t: string, kind: TokenKind) => {
    if (t === '') return;
    const last = out[out.length - 1];
    if (kind === 'plain' && last?.kind === 'plain') last.text += t;
    else out.push({ text: t, kind });
  };
  PROSE.lastIndex = 0;
  for (let m = PROSE.exec(text); m !== null; m = PROSE.exec(text)) {
    push(text.slice(at, m.index), 'plain');
    const [whole, time, word, key, eq, value, quoted] = m;
    if (time !== undefined) push(time, 'time');
    else if (word !== undefined) {
      const l = levelOf(word) ?? 'info';
      level ??= l;
      push(word, l);
    } else if (key !== undefined && eq !== undefined && value !== undefined) {
      push(key, 'key');
      push(eq, 'punct');
      const l = LEVEL_KEYS.has(key.toLowerCase()) ? levelOf(value.replace(/"/g, '')) : undefined;
      if (l !== undefined) {
        level = l;
        push(value, l);
      } else push(value, value.startsWith('"') ? 'string' : 'plain');
    } else if (quoted !== undefined) push(quoted, 'string');
    at = m.index + whole.length;
    if (whole === '') PROSE.lastIndex++;
  }
  push(text.slice(at), 'plain');
  return level;
}

/** The index one past the balanced JSON value opening at `start`, or -1. */
export function jsonEnd(text: string, start: number): number {
  let depth = 0;
  let inString = false;
  for (let i = start; i < text.length; i++) {
    const c = text[i];
    if (inString) {
      if (c === '\\') i++;
      else if (c === '"') inString = false;
      continue;
    }
    if (c === '"') inString = true;
    else if (c === '{' || c === '[') depth++;
    else if (c === '}' || c === ']') {
      depth--;
      if (depth === 0) return i + 1;
    }
  }
  return -1;
}

/** Where a parseable JSON object or array is in the line, trying the first
 *  four openings only, as the dock does. */
function jsonSpan(text: string): { start: number; end: number; value: unknown } | undefined {
  let tries = 0;
  for (let i = 0; i < text.length && tries < 4; i++) {
    if (text[i] !== '{' && text[i] !== '[') continue;
    tries++;
    const end = jsonEnd(text, i);
    if (end < 0) continue;
    try {
      const value: unknown = JSON.parse(text.slice(i, end));
      if (typeof value === 'object' && value !== null) return { start: i, end, value };
    } catch {
      // Not JSON: a Go map printed with %v, a stack trace's braces. Prose.
    }
  }
  return undefined;
}

const JSON_TOKEN = /("(?:[^"\\]|\\.)*")(\s*:)?|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)|\b(true|false|null)\b|([{}[\],:])|(\s+)/g;

function json(text: string, out: Token[]): void {
  JSON_TOKEN.lastIndex = 0;
  let at = 0;
  for (let m = JSON_TOKEN.exec(text); m !== null; m = JSON_TOKEN.exec(text)) {
    if (m.index > at) out.push({ text: text.slice(at, m.index), kind: 'plain' });
    const [whole, str, colon, num, lit, punct] = m;
    if (str !== undefined) {
      out.push({ text: str, kind: colon === undefined ? 'string' : 'key' });
      if (colon !== undefined) out.push({ text: colon, kind: 'punct' });
    } else if (num !== undefined) out.push({ text: num, kind: 'number' });
    else if (lit !== undefined) out.push({ text: lit, kind: 'literal' });
    else if (punct !== undefined) out.push({ text: punct, kind: 'punct' });
    else out.push({ text: whole, kind: 'plain' });
    at = m.index + whole.length;
  }
  if (at < text.length) out.push({ text: text.slice(at), kind: 'plain' });
}

function jsonLevel(value: unknown): Level | undefined {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return undefined;
  for (const [k, v] of Object.entries(value)) {
    if (LEVEL_KEYS.has(k.toLowerCase()) && typeof v === 'string') return levelOf(v);
  }
  return undefined;
}

export function tokenize(raw: string): Line {
  const text = stripAnsi(raw);
  const tokens: Token[] = [];
  // klog's letter is the logger's own word for the level, so it wins over
  // anything the message itself says.
  let header: Level | undefined;
  const klog = KLOG.exec(text);
  let rest = text;
  if (klog !== null) {
    header = levelOf(klog[1] ?? '');
    tokens.push({ text: klog[1] ?? '', kind: header ?? 'plain' }, { text: `${klog[2] ?? ''} `, kind: 'time' });
    rest = text.slice(klog[0].length);
  }
  let level: Level | undefined;
  const span = jsonSpan(rest);
  if (span === undefined) {
    level = prose(rest, tokens);
  } else {
    const before = prose(rest.slice(0, span.start), tokens);
    json(rest.slice(span.start, span.end), tokens);
    const after = prose(rest.slice(span.end), tokens);
    level = jsonLevel(span.value) ?? before ?? after;
  }
  level = header ?? level;
  return level === undefined ? { tokens } : { tokens, level };
}
