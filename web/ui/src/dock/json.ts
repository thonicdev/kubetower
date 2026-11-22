/**
 * JSON inside a log line.
 *
 * Structured logging is what server output mostly is now - Go's slog, Logback,
 * pino, zap - and a record of twenty fields is unreadable in a pane 200 columns
 * wide: the one field being looked for is somewhere in the middle of one dense
 * line. The JSON toggle in the toolbar indents it.
 *
 * **Everything here is about not touching a line it does not fully understand,
 * because the failure mode is not a missing feature. It is a log the operator
 * can no longer trust to be what the container printed.**
 *
 * Four rules, with the reasoning, because the reasoning is what stops the next reader "simplifying"
 * one of them:
 *
 *   - **A log line is rarely pure JSON.** It is a timestamp, then a level, then
 *     the record; or a record with a suffix after it. So what is looked for is a
 *     *span* inside the line and whatever sits either side of it is kept
 *     verbatim - the timestamp and the level are how a reader scans a pane, and
 *     a formatter that swallowed them would be worse than none.
 *   - **Almost-JSON comes out character for character.** A line the kubelet cut
 *     at its own limit, a Go map printed with `%v` - which starts with a bracket
 *     and is not JSON - a Java stack trace full of braces, a half-written record
 *     from a process that died mid-line. Every one leaves by `null`, and the
 *     caller then draws the raw line exactly as the toggle-off pane does.
 *     **Nothing here ever puts a parse error where a log line was**: the
 *     operator came for the line, and it is not written down anywhere else in
 *     the browser.
 *   - **The span is found by scanning, not by a regular expression.** Braces
 *     occur inside JSON strings - `{"msg":"unexpected } here"}` is one ordinary
 *     record - and no regular expression counts nesting.
 *   - **Two bounds.** Only the first `TRIES` candidate positions are tried, so a
 *     line whose prose contains a bracket before its record is left alone rather
 *     than paid for on every brace-carrying line in a buffer of a hundred
 *     thousand. And a value that would spread past `MAX_ROWS` rows is left
 *     alone, because the window's cost is counted in rows and one line becoming
 *     four hundred is how a pane the browser could lay out becomes one it
 *     cannot.
 */

/** Candidate opening positions tried before the line is given up on. */
const TRIES = 4;

/** Rows one line may become. Past this the line is left dense - visibly still
 *  one line - rather than made to disappear into the window's budget. */
const MAX_ROWS = 200;

/** Where a JSON value could begin. `g` so the search resumes rather than slicing
 *  a 16 KiB line four times over; every use sets `lastIndex` first. */
const OPEN = /[[{]/g;

/**
 * The index one past the balanced value beginning at `start`, or -1 when there
 * is not one. String- and escape-aware.
 */
export function jsonEnd(text: string, start: number): number {
  const close = text[start] === '{' ? '}' : ']';
  let depth = 0;
  let inString = false;
  let escaped = false;
  for (let i = start; i < text.length; i++) {
    const ch = text[i];
    if (inString) {
      if (escaped) escaped = false;
      else if (ch === '\\') escaped = true;
      else if (ch === '"') inString = false;
      continue;
    }
    if (ch === '"') {
      inString = true;
      continue;
    }
    if (ch === '{' || ch === '[') {
      depth++;
    } else if (ch === '}' || ch === ']') {
      depth--;
      // Closed by the wrong bracket, or closed more than it ever opened: not a
      // value, and finding that out here costs less than JSON.parse finding it
      // out over the same characters.
      if (depth < 0) return -1;
      if (depth === 0) return ch === close ? i + 1 : -1;
    }
  }
  return -1; // never closed, which is what a line cut short looks like
}

/**
 * The line with its first JSON object or array indented, or `null` when there is
 * nothing in it to indent.
 *
 * Pure - the same string in gives the same string out, always - which is what
 * lets the panel cache the answer against a line index for the life of the
 * buffer and flip the toggle as often as anybody likes for the price of the
 * first pass.
 */
export function indentJSON(text: string): string | null {
  let at = 0;
  for (let tries = 0; tries < TRIES; tries++) {
    OPEN.lastIndex = at;
    const found = OPEN.exec(text);
    if (found === null) return null;
    const start = found.index;
    at = start + 1;

    const end = jsonEnd(text, start);
    if (end < 0) continue;

    let value: unknown;
    try {
      value = JSON.parse(text.slice(start, end));
    } catch {
      // Bindingless on purpose. A caught error is the raw material of the defect
      // this module exists to prevent: the moment it has a name, somebody can
      // return it as the line, and "SyntaxError: Unexpected end of JSON input"
      // where a log line was has destroyed the one thing the pane was asked to
      // show.
      continue;
    }
    // A bare number or string is not what this is for and has nothing to spread.
    if (value === null || typeof value !== 'object') continue;

    const indented = JSON.stringify(value, null, 2);
    const rows = indented.split('\n').length;
    // Too big to spread: give up on the whole line rather than move on to the
    // next candidate, which would be a value *inside* this one - half a record
    // indented under the other half left dense reads as a rendering bug.
    if (rows > MAX_ROWS) return null;
    // An empty object or array is already one row. Formatting it would change
    // nothing except to mark the line as formatted, so it does not count as a
    // find and the search moves on.
    if (rows < 2) continue;

    return text.slice(0, start) + indented + text.slice(end);
  }
  return null;
}
