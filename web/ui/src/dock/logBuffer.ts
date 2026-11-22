import { decodeANSI, type Decoded } from './ansi';
import { indentJSON } from './json';

/**
 * The log buffer, and the window over it.
 *
 * ## The shape follows from one number
 *
 * The server bounds a stream at 8 MiB, so a chatty pod can put the better part
 * of a hundred thousand lines through this panel. A pane that holds every line
 * it has ever received as a text node is fine while it only appends and
 * ruinous the moment anything has to walk them.
 *
 * ## And in React the hazard is worse, not better
 *
 * A state update per arriving line is a **render** per line. So the buffer is
 * not React state: it is this object, mutated imperatively by the stream, and
 * the component re-renders on a throttle. That is why this file has no
 * `useState` in it and no React import at all.
 *
 * ## The caches are keyed by the line itself, and bounded
 *
 * Arrays parallel to `lines` by index would have two costs. One that forgot to
 * move with `lines` would hand every line the answer belonging to some other
 * line - this line's text with the previous line's colors, which reads as a
 * rendering bug and is a cache bug. And a filter that decoded **every** held
 * line and kept the result would hold a pane's hundred thousand lines two or
 * three times over until its tab was closed.
 *
 * So the decoded and indented copies are kept in maps keyed by the `LogLine`
 * object - which moves with its line through every trim and prepend by
 * construction - and only for lines that were **drawn**, at most `keepDecoded`
 * of them, the oldest let go first. A filter decodes what it tests and keeps
 * none of it.
 *
 * ## And the lines are bounded twice
 *
 * Per pane by `LOG_CAP`, and across the dock by a `LogBudget` the panes share:
 * sixteen panes at a hundred thousand lines each would be about a gigabyte.
 */

/** Rows kept in the document at once. */
export const LOG_WINDOW = 2_000;

/** Rows pulled in when the reader scrolls towards the top of the window. */
export const LOG_CHUNK = 1_000;

/**
 * The most lines one panel holds. **A hard bound**: `length` never exceeds it.
 *
 * The server's own bounds are per *stream* - 8 MiB, a 5 000-line tail, a
 * 10 000-line rewind - and a panel is not a stream: a panel that reconnects
 * and appends the whole tail again each time is bounded by nothing else. 8 MiB of ordinary log
 * lines is the better part of a hundred thousand, so this is the figure at which
 * one honest stream starts to be trimmed and past which nothing honest goes.
 *
 * Trimmed from the oldest end, a tenth of the cap (at most `LOG_CHUNK`) at a
 * time rather than one line at a time: every index in `shown` moves on a trim,
 * and doing that per arriving line on a busy pod is a hundred thousand writes
 * per line.
 */
export const LOG_CAP = 100_000;

/**
 * The most lines every log pane in the dock holds together.
 *
 * Four panes at the full `LOG_CAP` each; past four, the budget is shared out
 * equally, so sixteen panes hold a quarter of the cap each rather than sixteen
 * caps. A figure chosen, not measured: what a line costs in a
 * browser depends on the line. A pane that already holds more than its share
 * when another joins lets go of its oldest lines at once, ended or not, and
 * says so as it does for the per-pane cap.
 */
export const DOCK_LOG_BUDGET = 4 * LOG_CAP;

/** Decoded copies kept when nobody has said how many rows are drawn: the
 *  window, and one chunk of growth above it. */
const DECODED_DEFAULT = LOG_WINDOW + LOG_CHUNK;

/** How far into an incoming page to look for lines this buffer already holds.
 *  See `LogBuffer.prepend` for why the trim errs towards dropping a duplicate. */
export const OVERLAP = 64;

/** One line as it arrived, with what the server said about it. */
export interface LogLine {
  readonly text: string;
  readonly at?: string;
  readonly container?: string;
}

export interface Filter {
  readonly pattern: RegExp | null;
  /** The message the browser gave, trimmed to its useful half, when the pattern
   *  would not compile. The previous filter stays in force. */
  readonly error: string | null;
}

/**
 * One budget of lines, shared by every pane that joins it.
 *
 * A pane's share is the budget divided by the panes holding one, never more than
 * the pane's own cap. Joining is what makes the others let go - an ended pane
 * receives nothing, so waiting for its next line would be waiting for ever -
 * and leaving gives the share back for what arrives next.
 *
 * **A pane that lets go is told.** An ended pane has no repaint timer, so a
 * trim nobody announced left its rows and its "earlier lines were let go"
 * sentence stale until somebody touched it - and Download, which writes what
 * is held, would have saved fewer lines than the pane appeared to hold, with
 * nothing on screen saying so. `trimmed` is the pane's repaint.
 */
export class LogBudget {
  private readonly members = new Map<LogBuffer, () => void>();

  readonly lines: number;

  constructor(lines: number = DOCK_LOG_BUDGET) {
    this.lines = Math.max(1, Math.floor(lines));
  }

  /** Each member's share, now. */
  get share(): number {
    return Math.max(1, Math.floor(this.lines / Math.max(1, this.members.size)));
  }

  /** How many panes hold a share now. */
  get panes(): number {
    return this.members.size;
  }

  join(buffer: LogBuffer, trimmed: () => void = () => {}): void {
    this.members.set(buffer, trimmed);
    buffer.within(this);
    for (const [member, told] of this.members) {
      if (member.fit()) told();
    }
  }

  leave(buffer: LogBuffer): void {
    this.members.delete(buffer);
    buffer.within(null);
  }
}

/** The dock's own budget, which every `LogPane` joins while it is mounted. */
export const dockLogBudget = new LogBudget();

export class LogBuffer {
  /** The record. What `Download` writes, and never what is on screen. */
  readonly lines: LogLine[] = [];

  /** `indentJSON` per drawn line, `null` for "nothing to indent here". Absent
   *  is "not drawn lately". See the header for why a map and not an array. */
  private readonly pretty = new Map<LogLine, string | null>();

  /** `decodeANSI` per drawn line. */
  private readonly styled = new Map<LogLine, Decoded>();

  /** How many lines may keep decoded copies. See `keepDecoded`. */
  private decodedLimit = DECODED_DEFAULT;

  /** The budget this pane shares, while it has joined one. */
  private budget: LogBudget | null = null;

  /** The indices passing the filter. With no filter this is `0…n-1` in full
   *  **rather than a special case**: one number per line, a few hundred
   *  kilobytes at the server's ceiling, and it buys a single code path
   *  everywhere else. */
  shown: number[] = [];

  private matcher: RegExp | null = null;
  private json = false;

  /** Lines this panel has let go of to stay within `cap`, since the last
   *  `reset`. Non-zero means the front of the buffer is no longer the front of
   *  what was read, which the panel says, and which ends paging backwards: the
   *  server counts `before` from the lines the client holds, and those no
   *  longer start where the stream did. */
  dropped = 0;

  /** Lines pushed since the last `reset`: only ever grows, so the difference
   *  between two readings is how many lines arrived between them, whatever was
   *  trimmed or paged in meanwhile. */
  pushed = 0;

  /** This pane's own ceiling, before any budget is shared out. */
  private readonly own: number;

  constructor(cap: number = LOG_CAP) {
    this.own = Math.max(1, Math.floor(cap));
  }

  /** The most lines this pane holds now: its own cap, or its share of the
   *  budget it has joined, whichever is smaller. */
  get cap(): number {
    return this.budget === null ? this.own : Math.min(this.own, this.budget.share);
  }

  /** How many lines one trim lets go of. */
  private get step(): number {
    return Math.max(1, Math.min(LOG_CHUNK, Math.floor(this.cap / 10)));
  }

  /** Called by `LogBudget` only. */
  within(budget: LogBudget | null): void {
    this.budget = budget;
  }

  /** Let go of whatever is over the cap now. The budget calls it when a pane
   *  joins, which is what shrinks every share. */
  fit(): boolean {
    const over = this.lines.length - this.cap;
    if (over <= 0) return false;
    this.trim(over);
    return true;
  }

  /**
   * How many rows are drawn, so how many decoded copies are worth keeping. The
   * pane says so on each render; a draw past it re-decodes the oldest rather
   * than keeping a copy of every line it ever showed.
   */
  keepDecoded(rows: number): void {
    this.decodedLimit = Math.max(1, Math.floor(rows));
    this.evict(this.styled);
    this.evict(this.pretty);
  }

  /** How many lines hold a decoded copy now. For the tests, and for nothing
   *  that draws. */
  get decodedLines(): number {
    return this.styled.size;
  }

  get length(): number {
    return this.lines.length;
  }

  /**
   * Append. The only thing the arrival path does, and it does nothing else on
   * purpose: **nothing here reads the layout.**
   *
   * The obvious version reads `scrollHeight`, `scrollTop` and `clientHeight` as
   * each line arrives to see whether the reader is at the bottom. It is also
   * **3.4 milliseconds per line, measured, on a two-thousand line window** -
   * five thousand lines take seventeen seconds and the tab is unusable
   * throughout, and it looks perfectly reasonable.
   */
  push(line: LogLine): void {
    const cap = this.cap;
    if (this.lines.length >= cap) this.trim(Math.max(this.step, this.lines.length - cap + 1));
    const index = this.lines.length;
    this.lines.push(line);
    this.pushed += 1;
    if (this.passes(index)) this.shown.push(index);
  }

  /**
   * Put a block of earlier lines in front of everything, oldest first.
   *
   * The other end of `push`, and the only other thing that changes the array's
   * front - so it is the one operation that has to move both structures
   * together. `shown` holds indices *into* `lines`, so every index already in
   * `shown` shifts by the number of lines that arrived. Getting that wrong does not throw: it draws
   * the wrong lines, which reads as a container that printed something it did
   * not.
   *
   * ## The overlap is trimmed, erring towards dropping a duplicate
   *
   * A page backwards is `before + page` lines with the front kept, and `before`
   * is what the client *said* it held. A line rotated away between the two reads
   * makes those disagree by one or two, so the block can overlap what is already
   * here. Up to `OVERLAP` lines of the incoming tail are dropped where they
   * match the buffer's own head exactly.
   *
   * The asymmetry is deliberate: **a repeated line reads as the container having
   * printed twice**, which is a fact about the workload and is worse than losing
   * a line to an over-eager trim. The trim only fires on an exact match of text,
   * so it cannot drop a line the container really did print twice in a row
   * unless it also happens to sit exactly at the seam.
   *
   * Returns how many lines were actually added, which is what the caller
   * compensates the scroll position by.
   */
  prepend(incoming: readonly LogLine[]): number {
    if (incoming.length === 0) return 0;

    let overlap = 0;
    const reach = Math.min(OVERLAP, incoming.length, this.lines.length);
    for (let run = reach; run > 0; run--) {
      let same = true;
      for (let i = 0; i < run; i++) {
        if (incoming[incoming.length - run + i]?.text !== this.lines[i]?.text) {
          same = false;
          break;
        }
      }
      if (same) {
        overlap = run;
        break;
      }
    }
    // Within the cap, keeping the newest of the block - the end that meets the
    // buffer's head - so what is held stays contiguous. What does not fit is
    // counted as dropped, which ends paging: there are earlier lines and this
    // panel will not hold them.
    const room = Math.max(0, this.cap - this.lines.length);
    const fits = incoming.length - overlap;
    const kept = Math.min(fits, room);
    this.dropped += fits - kept;
    const added = incoming.slice(fits - kept, fits);
    if (added.length === 0) return 0;

    this.lines.unshift(...added);
    // Every index already in `shown` now points one block further along.
    for (let i = 0; i < this.shown.length; i++) {
      this.shown[i] = (this.shown[i] ?? 0) + added.length;
    }
    // And the new lines take their place at the front, filtered like any other.
    const front: number[] = [];
    for (let index = 0; index < added.length; index++) {
      if (this.passes(index)) front.push(index);
    }
    this.shown.unshift(...front);
    return added.length;
  }

  /**
   * Append a tail that a reconnect replayed, without the lines already held.
   *
   * After a dropped connection the new request replays the log's tail from its
   * start, so its front repeats the end of this buffer and its rest is what was
   * printed in the gap. The overlap taken is the **longest** run where the
   * block's head equals the buffer's last lines exactly: the longest, because
   * on a log that repeats itself a short match is the likelier coincidence, and
   * the trim errs towards dropping a duplicate for the reason `prepend` gives.
   *
   * Returns how many lines of the block were already held, or `null` when the
   * block does not continue this buffer at all - the gap was longer than the
   * tail, or the log was rotated - and the caller has to decide what to keep,
   * because appending would put a seam nobody can see in the middle of the pane.
   */
  appendReplay(incoming: readonly LogLine[]): number | null {
    if (incoming.length === 0) return 0;
    if (this.lines.length === 0) {
      for (const line of incoming) this.push(line);
      return 0;
    }
    const end = this.lines.length;
    let overlap = 0;
    for (let run = Math.min(incoming.length, end); run > 0; run--) {
      let same = true;
      for (let i = 0; i < run; i++) {
        if (incoming[i]?.text !== this.lines[end - run + i]?.text) {
          same = false;
          break;
        }
      }
      if (same) {
        overlap = run;
        break;
      }
    }
    if (overlap === 0) return null;
    for (let i = overlap; i < incoming.length; i++) {
      const line = incoming[i];
      if (line !== undefined) this.push(line);
    }
    return overlap;
  }

  /**
   * Drop everything before `keep`, which is what a re-read does.
   *
   * `lines` and `shown` empty together, and the kept copies with them.
   */
  reset(): void {
    this.lines.length = 0;
    this.pretty.clear();
    this.styled.clear();
    this.shown = [];
    this.dropped = 0;
    this.pushed = 0;
  }

  /**
   * Let go of the oldest `count` lines, to stay within `cap`.
   *
   * `shown` moves with `lines`, as in `prepend` and for the same reason: it
   * holds indices into them, so every index it keeps moves down by `count`, and
   * the ones that pointed at a dropped line go. The caches are keyed by the line
   * and need no moving; a dropped line's copies go with it.
   */
  private trim(count: number): void {
    const gone = Math.min(count, this.lines.length);
    if (gone === 0) return;
    for (const line of this.lines.splice(0, gone)) {
      this.pretty.delete(line);
      this.styled.delete(line);
    }
    const shown: number[] = [];
    for (const index of this.shown) {
      if (index >= gone) shown.push(index - gone);
    }
    this.shown = shown;
    this.dropped += gone;
  }

  /**
   * The one string a line has as far as the rest of the panel is concerned:
   * **what the filter tests, what the counter counts, what the marks are placed
   * in, and what is drawn.**
   *
   * With the JSON toggle on that is the indented form, deliberately. A search
   * for `"level":"error"` stops matching once the toggle is on - the pane now
   * reads `"level": "error"`, with the space `JSON.stringify` puts there - and
   * that is the honest answer: the alternative is a counter claiming twelve
   * matches over twelve lines with no mark on any of them.
   *
   * And it is the line with the escapes taken out, for the same reason one layer
   * down.
   */
  shownText(index: number): string {
    return this.textOf(index, true);
  }

  /** The color the container asked for, kept for a drawn line. */
  decoded(index: number): Decoded {
    return this.decodeOf(index, true);
  }

  /** `shownText`, with `keep` saying whether the answer is worth holding: a
   *  draw keeps it, a filter pass does not (see the header). */
  private textOf(index: number, keep: boolean): string {
    const decoded = this.decodeOf(index, keep);
    if (!this.json) return decoded.text;
    const line = this.lines[index];
    if (line === undefined) return decoded.text;
    let indented = this.pretty.get(line);
    if (indented === undefined) {
      indented = indentJSON(decoded.text);
      if (keep) this.remember(this.pretty, line, indented);
    } else if (keep) {
      this.touch(this.pretty, line, indented);
    }
    return indented ?? decoded.text;
  }

  private decodeOf(index: number, keep: boolean): Decoded {
    const line = this.lines[index];
    if (line === undefined) return decodeANSI('');
    let found = this.styled.get(line);
    if (found === undefined) {
      found = decodeANSI(line.text);
      if (keep) this.remember(this.styled, line, found);
    } else if (keep) {
      this.touch(this.styled, line, found);
    }
    return found;
  }

  /** A drawn line moves to the newest end, so eviction is by last use and not
   *  by first draw: after the window shrinks, the rows still drawn are the
   *  last to go rather than the first. A delete and a set, on a hit. */
  private touch<T>(cache: Map<LogLine, T>, line: LogLine, value: T): void {
    cache.delete(line);
    cache.set(line, value);
  }

  /** Keep one copy, letting go of the least recently drawn past the limit. A
   *  map iterates in insertion order and `touch` re-inserts on use, so the
   *  first key is the one drawn longest ago. */
  private remember<T>(cache: Map<LogLine, T>, line: LogLine, value: T): void {
    cache.set(line, value);
    this.evict(cache);
  }

  private evict<T>(cache: Map<LogLine, T>): void {
    while (cache.size > this.decodedLimit) {
      const oldest = cache.keys().next();
      if (oldest.done === true) return;
      cache.delete(oldest.value);
    }
  }

  /**
   * The indices to draw, for this filter and this view.
   *
   * **Safe to call during a render**, which is why it is one function rather
   * than two setters and an effect. It is a cache: the same pattern and the same
   * toggle give the same array without work, and StrictMode calling it twice
   * costs nothing because re-filtering is idempotent. The alternative - applying
   * the filter in a `useEffect` and keeping the result in state - is a cascading
   * render per keystroke and is what `react-hooks/set-state-in-effect` refuses.
   *
   * Every cached indent stays valid across a toggle, because `indentJSON` is
   * pure. What has to run again is the filter, since what a line *is* has
   * changed: with JSON on, a search for `"level":"error"` stops matching, and
   * that is the honest answer rather than a counter claiming matches it cannot
   * mark.
   */
  view(matcher: RegExp | null, json: boolean): readonly number[] {
    if (matcher !== this.matcher || json !== this.json) {
      this.matcher = matcher;
      this.json = json;
      this.refilter();
    }
    return this.shown;
  }

  get jsonOn(): boolean {
    return this.json;
  }

  /**
   * How many of `shown` come at or before `line`, or `null` once that line has
   * been let go.
   *
   * What a pane holding a selection draws up to (`LogPane`):
   * its window ends at the last row it drew when the selection began, and that
   * row is named by the line itself rather than by its index, because a trim or
   * a page backwards moves every index while the reader is still selecting.
   * Read after `view`, which is what `shown` answers for.
   *
   * Searched from the newest end, where the line is: it was the last row drawn,
   * and only what arrived since lies beyond it.
   */
  shownThrough(line: LogLine): number | null {
    const index = this.lines.lastIndexOf(line);
    if (index < 0) return null;
    // `shown` ascends, so the answer is its upper bound for `index`.
    let low = 0;
    let high = this.shown.length;
    while (low < high) {
      const middle = (low + high) >>> 1;
      if ((this.shown[middle] ?? Infinity) <= index) low = middle + 1;
      else high = middle;
    }
    return low;
  }

  private refilter(): void {
    if (this.matcher === null) {
      this.shown = this.lines.map((_line, index) => index);
      return;
    }
    const shown: number[] = [];
    for (let index = 0; index < this.lines.length; index++) {
      if (this.passes(index)) shown.push(index);
    }
    this.shown = shown;
  }

  private passes(index: number): boolean {
    if (this.matcher === null) return true;
    // **Reset `lastIndex` first.** Reusing a `/g` pattern without it tests from
    // halfway through the previous line and skips roughly every other match -
    // which presents as a filter that drops lines it should have kept,
    // intermittently, and only on a busy pod.
    this.matcher.lastIndex = 0;
    return this.matcher.test(this.textOf(index, false));
  }
}

/**
 * Compile what was typed into the box.
 *
 * Plain text is escaped into a pattern rather than tested with `includes`: one
 * code path for hit-finding beats two, and the marks and the counter then read
 * the same matcher as the filter.
 *
 * An unfinished regular expression **keeps the previous filter** and says why,
 * having stripped the useless half of the browser's message: for
 * `/[unclosed/gi` Chrome says *"Invalid regular expression: /[unclosed/gi:
 * Unterminated character class"* and the useful half is the last clause.
 */
export function compileFilter(text: string, regex: boolean, caseSensitive: boolean): Filter {
  if (text === '') return { pattern: null, error: null };
  const flags = caseSensitive ? 'g' : 'gi';
  const source = regex ? text : text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  try {
    return { pattern: new RegExp(source, flags), error: null };
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    const clause = message.split(':').pop()?.trim() ?? message;
    return { pattern: null, error: clause };
  }
}

/** One piece of a drawn line: a stretch of text, whether it is a match, and the
 *  style over it. */
export interface Piece {
  readonly text: string;
  readonly match: boolean;
  readonly style: Decoded['runs'][number]['style'] | null;
}

/**
 * Cut a line at every boundary either the hit list or the run list has, and emit
 * the fewest pieces that can be right.
 *
 * On the ordinary line - no filter, no color - that is the single piece it was.
 *
 * Two details are load-bearing.
 *
 *   - **A zero-length match ends the loop.** A pattern that can match nothing -
 *     `.*`, `a*`, an empty group - returns a zero-length match and leaves
 *     `lastIndex` where it was. The loop then never ends and the tab is gone,
 *     with no error anywhere to say why.
 *   - **The mark goes inside the colored span**, which is a rendering rule and
 *     is enforced by the caller: color is inherited, so a span carrying `color`
 *     inside a mark would override the mark's own, and the current match on a
 *     red ERR line would be red on amber.
 */
export function pieces(text: string, runs: Decoded['runs'], matcher: RegExp | null): Piece[] {
  const cuts = new Set<number>([0, text.length]);
  for (const run of runs) {
    if (run.from < text.length) cuts.add(run.from);
    if (run.to < text.length) cuts.add(run.to);
  }

  const hits: [number, number][] = [];
  if (matcher !== null) {
    matcher.lastIndex = 0;
    let found = matcher.exec(text);
    while (found !== null) {
      if (found[0].length === 0) break;
      hits.push([found.index, found.index + found[0].length]);
      cuts.add(found.index);
      if (found.index + found[0].length < text.length) cuts.add(found.index + found[0].length);
      found = matcher.exec(text);
    }
  }

  const edges = [...cuts].sort((a, b) => a - b);
  const out: Piece[] = [];
  for (let i = 0; i < edges.length - 1; i++) {
    const from = edges[i] ?? 0;
    const to = edges[i + 1] ?? 0;
    if (to <= from) continue;
    out.push({
      text: text.slice(from, to),
      match: hits.some(([start, end]) => from >= start && to <= end),
      style: runs.find((run) => from >= run.from && to <= run.to)?.style ?? null,
    });
  }
  return out.length > 0 ? out : [{ text, match: false, style: null }];
}
