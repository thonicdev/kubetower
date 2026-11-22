import type { ReactNode } from 'react';

/**
 * # How this console draws a reading. Decided once, here.
 *
 * A **reading** is a measured quantity: a node's CPU, a cluster's requested
 * memory, a claim's disk. Several surfaces draw one — the resource grid
 * (`pages/resources/ResourceCell.tsx`), the overview's capacity tiles
 * (`pages/overview/Donut.tsx`, `pages/overview/Reading.tsx`) and the cluster
 * chooser (`clusters/ClusterList.tsx`). Several answers is how a console ends
 * up looking like several consoles, so the answer is written down here and
 * they import it: *one style for a reading, decided once*. **Read it before
 * drawing a number anywhere**, and change it here rather than beside the next
 * component.
 *
 * ## The anatomy, in seven parts
 *
 * A percentage beside every bar, and the used and total quantities on hover. A
 * usage cell is read for **pressure**; an absolute figure (`1163m`,
 * `12.4 GiB`) is the one nobody needs at a glance.
 *
 * **1. Where there is a proportion, the proportion leads: the bar and its
 * percentage, on one line.** The percentage is the rounded `percent` the caller
 * was given — on the grid `Cell.gauge.percent`, which "arrives already divided"
 * on the server (`api/openapi.yaml`) — in tabular numerals, because a column of
 * readings is read by running the eye down it and proportional digits make `6%`
 * and `58%` two different widths of the same fact. No quantity, no unit, in the
 * cell. Where there is **no** proportion — nothing measured, or a reading with
 * no whole — the line draws the reading itself (`value`):
 * the server's `text`, or its em dash and sentence. The hover's quantity is
 * part 2's, and part 2 names the one surface whose hover the browser composes.
 *
 * **2. The quantity moves to the hover, written `used / total`, and both halves
 * are the server's.** Nothing here formats a quantity: `Reading.text` is "the
 * rendered form, computed once on the server so that a table cannot report
 * three different units for the same quantity", so a client that composed its
 * own unit would be the second opinion that contract exists to prevent.
 *
 * **Both halves are in one unit, the whole's, to one precision rule**:
 * `0.37 / 8 cores`, `3.2 / 16 GiB`, never
 * `368m / 8.0 cores`, which is a sum left to the reader. CPU is always in
 * cores. The server writes it (`resources.Fraction`), and the ways in are:
 *
 * - a grid cell's `Cell.gauge` is `{percent, tone}` and carries no whole, so the
 *   server writes the whole string into `Cell.hint` (`0.37 / 8 cores
 *   allocatable`) and the cell passes it as `title`;
 * - the overview's `CapacityCell` carries each line's hover as
 *   `requestedOf` and `usedOf`. Only where a server sends neither does
 *   `fraction()` below join the two readings' own texts, as a fallback;
 * - **the one stated exception: the cluster chooser's tiles**, whose hover
 *   (`2 / 8 cores`) the browser composes in `clusters/fleet.ts` `usage()`. It
 *   is acceptable on that surface and on no other because the chooser holds no
 *   figure of its own to format: it
 *   re-reads each cluster's overview, takes the unit word from the server's own
 *   capacity text (`8.0 cores`, `32.0 GiB`) and divides the wire's exact values
 *   by what that unit is worth, so it cannot invent a unit or disagree with the
 *   server about one. Its `figure()` is the same precision rule as
 *   `resources.Fraction`'s, ties included (Go's `toFixed`), so a chooser hover
 *   and the overview's hover read the same cluster alike. A second surface that
 *   composes a unit is a defect, not a second exception.
 *
 * **The separator is a slash** — in `resources.Fraction` on the server, in
 * `fraction()` here and in the chooser's tile, and nowhere else. The hover is
 * also
 * an `sr-only` span after the percentage, because a `title` is not reliably read
 * out: screen readers say the slash as "slash", which is legible.
 *
 * **3. The percentage sits at the bar's trailing end, in a box of fixed width,
 * and the bar takes the rest of the line.** A track at the cell's leading edge
 * with its digits right-aligned a cell-width away would attach, to the eye, to
 * the column before it. Beside its percentage with one `gap-1.5` between them
 * the bar ends where its own figure begins — nothing of its own is a
 * cell-width away.
 * `measure/table.spec.ts` check 2 holds it: the bar is nearer its own figure
 * than the previous column's, on the figure's line, inside its own cell.
 *
 * **4. The bar does not move with its digits, by the fixed box.** An inline
 * bar followed by a right-aligned number is positioned *by the number*, and
 * moves as a node crosses 999m. The percentage's box is `w-10` whatever it
 * holds, so the bar beside it is the cell less a constant: `6%` and `100%`
 * leave it exactly the same length, and no arrangement of digits moves it. The
 * figure sits at the box's **leading** edge, against its bar: right-aligned in
 * the box it would begin further from its bar than the previous column's
 * digits are. A column of percentages is
 * therefore ragged on the right by a digit; that is the price, and the bar it
 * belongs to is the thing read down the column.
 *
 * **5. A term labels the line wherever one box holds more than one reading.**
 * The overview's capacity tile holds *requested* and *used*, which are different
 * quantities from different sources, and without a term the tile does not say
 * which is which.
 * Where a box holds one reading the column heading is already the label and the
 * term is left off.
 *
 * **6. Nothing measured draws no shape at all, and no percentage.** Not an empty
 * track, not a ring at 0 %, not `0%`. An empty
 * track reads as 0 %, which reads as an idle machine, which is the opposite of
 * the truth when nothing could be measured. The em dash and the server's
 * sentence are the whole answer. `percent` undefined is how a caller says so,
 * and there is deliberately no "empty gauge" arm to reach for.
 *
 * **7. The shape's tone is the shape's alone.** `Gauge.tone` says *how full is
 * too full*; `Cell.tone` says *whether this reading is a fault*. A node at 91 %
 * memory is a well-packed machine: coloring the bar says "look here", promoting
 * the reading says "this is a fault", and only the first is true. The tone never
 * reaches the percentage beside it either.
 *
 * ## What this costs, stated rather than discovered
 *
 * **The quantity is a hover away**, taken knowingly: the absolute figure is still on the cell (its `title`, its
 * `sr-only` text) and still what a gauged column sorts by (`Cell.value`, on the
 * server), but it is no longer read at a glance.
 *
 * **A clamped percentage reads 100 % past the whole.** The grid's
 * `Gauge.percent` is clamped on the server (`resources.gauge`: metrics-server
 * samples usage and allocatable at different moments, so a node briefly over its
 * allocatable is ordinary), so such a node reads `100%` and its hover states the
 * real figures. The cluster chooser's tiles (`fleet.ts`) and the overview's
 * `Gauge` clamp the same way. The overview's capacity lines under the ring do
 * not: they print the real figure past 100 %, as the ring's center does, so one
 * cluster's `used` can read `104%` under its ring and `100%` on its chooser
 * card. Both hovers state the real quantities.
 *
 * **The row is not taller for the bar**: the bar is on the figure's line.
 */

/** The bar's own tones, and they are the bar's alone — see part 7. Full class
 *  strings, looked up: Tailwind finds utilities by scanning source text, so
 *  `bg-${tone}` is a class that exists in the DOM and in no stylesheet. */
const FILL: Readonly<Record<ReadingTone, string>> = {
  ok: 'bg-ok',
  warn: 'bg-warn',
  bad: 'bg-bad',
};

export type ReadingTone = 'ok' | 'warn' | 'bad';

export interface ReadingTrackProps {
  /** 0–100, already divided by the server. Clamped again here: a client that
   *  trusts a bound it did not enforce draws a bar out of its own cell the first
   *  time a source disagrees, and the second clamp costs nothing. */
  percent: number;
  /** Absent draws the accent — a measured proportion with no grading on it. */
  tone?: ReadingTone | undefined;
  /** The shape's accessible name: "CPU", "Memory". */
  label: string;
}

/**
 * The bar, beside its percentage and as long as the line leaves it.
 *
 * `h-1`: `--spacing` is 4px and left alone deliberately (`theme.css` — moving
 * it would rescale every `p-*` in the console as a side effect of a font
 * decision), so 4px is the scale's nearest step and an arbitrary 3px height
 * would be a design value outside the theme, which `check:styles` refuses. A
 * bar most of the width of its own cell does not need weight to be found.
 *
 * The trough is **light** (`bg-white/10`) and has no border. The sunken idiom
 * fails here: on this palette there is nothing darker than `--color-bg` to
 * sink into, so the track would be invisible and the fill's right edge would
 * become the reading, wandering every two seconds.
 *
 * ## The bar is announced, and the percentage beside it is not
 *
 * One style means one answer, and the answer is **announced**: the bar is the
 * shape the proportion is, and its name says it (`CPU: 58 percent`).
 *
 * The proportion is also drawn as digits beside it, so
 * `Measure` hides those digits from assistive technology where the track is
 * drawn: one phrase per gauged cell, as before, and not the same figure twice.
 */
export function ReadingTrack({ percent, tone, label }: ReadingTrackProps): ReactNode {
  const drawn = Math.min(100, Math.max(0, percent));
  const shown = Math.round(percent);

  return (
    <span
      role="img"
      aria-label={`${label}: ${String(shown)} percent`}
      data-percent={shown}
      data-gauged="true"
      className="block h-1 w-full overflow-hidden rounded-full bg-white/10"
    >
      {/* The one inline style on a reading, and it is arithmetic rather than
          styling: the width IS the measurement, so it cannot be a utility class —
          a computed `w-[${n}%]` is a class Tailwind never scanned and therefore a
          bar that silently does not draw. Hand-written stylesheets are banned,
          not data. */}
      <span
        className={`block h-full rounded-full ${tone === undefined ? 'bg-accent' : FILL[tone]}`}
        style={{ width: `${String(drawn)}%` }}
      />
    </span>
  );
}

/**
 * `used / total`, from two texts the server rendered — part 2's one line on
 * this side, and the server's twin is `resources.Fraction`. Nothing is
 * formatted here: each half is a `Reading.text` as it arrived, so the unit is
 * the server's. Either half missing is no hover at all rather than half of one.
 */
export function fraction(used: string | undefined, whole: string | undefined): string | undefined {
  // An absent half is no hover; the callers on the overview pass the value as
  // the server sent it (`readingText`) where `text` is missing.
  if (used === undefined || used === '' || whole === undefined || whole === '') return undefined;
  return `${used} / ${whole}`;
}

/** A reading's server text, or - where the optional `text` is absent - its
 *  value as sent. Never formatted here, and never absent while a value is
 *  there, so the quantity is never lost from the hover. */
export function readingText(reading: { value: number | null; text?: string | undefined } | undefined): string | undefined {
  if (reading === undefined) return undefined;
  if (reading.text !== undefined && reading.text !== '') return reading.text;
  return reading.value === null ? undefined : String(reading.value);
}

export interface MeasureProps {
  /** The reading as the server rendered it, or its em dash and sentence. Part
   *  1: drawn on the line only where there is **no** `percent` — with one, the
   *  proportion leads and the quantity is the hover. */
  value: ReactNode;
  /** Part 2: the hover, `used / total` — the grid's `Cell.hint`, or
   *  `fraction()` of two server texts. Absent, a string `value` is the hover,
   *  so a quantity the line no longer draws is never lost from the cell. */
  title?: string | undefined;
  /** Part 5: a muted word in front, where one box holds several readings. */
  term?: ReactNode;
  /** 0–100 and beyond: the proportion, drawn rounded. Part 6: absent draws no
   *  track and no percentage. Not an empty one, not `0%`. */
  percent?: number | undefined;
  tone?: ReadingTone | undefined;
  /** The shape's accessible name: "CPU", "Memory". */
  label: string;
  /** Which edge the line sits against. `end` in a numeric grid column, whose
   *  `<td>` is right-aligned; `start` where the reading is prose. */
  align?: 'start' | 'end';
  /** `false` where another shape in the same box already draws this proportion
   *  — the capacity ring, whose lines are named under it. The percentage is
   *  still the line's figure; only the bar is left off, because a bar under a
   *  ring would be one measurement drawn twice. */
  track?: boolean;
}

/**
 * One reading, drawn to the anatomy above: where there is a proportion, the bar
 * and its percentage on one line with the quantity on hover; where there is
 * none, the reading itself.
 *
 * `min-w-0` on the boxes and `truncate` on the reading: the `<td>` clips
 * (`theme.css`, the operator's thirteenth report) and a flex child that will not
 * shrink below its content defeats that by growing the cell instead.
 */
export function Measure({
  value,
  title,
  term,
  percent,
  tone,
  label,
  align = 'end',
  track = true,
}: MeasureProps): ReactNode {
  const hover =
    title !== undefined && title !== '' ? title : typeof value === 'string' && value !== '' ? value : undefined;
  const line = `flex min-w-0 items-center gap-1.5 ${align === 'end' ? 'justify-end' : ''}`;
  const named = term === undefined ? null : <span className="shrink-0 text-xs text-muted">{term}</span>;

  if (percent === undefined) {
    return (
      <span className={line} {...(hover === undefined || hover === value ? {} : { title: hover })}>
        {named}
        <span className="min-w-0 truncate tabular-nums">{value}</span>
      </span>
    );
  }

  return (
    <span data-measure="true" className={line} {...(hover === undefined ? {} : { title: hover })}>
      {named}
      {track ? (
        <span className="min-w-0 flex-1">
          <ReadingTrack percent={percent} tone={tone} label={label} />
        </span>
      ) : null}
      {/* Part 4: a box of fixed width beside a track, so that the bar is the
          line less a constant whatever the digits are — and the figure against
          the box's **leading** edge, so it starts one gap after the bar ends
          whatever its width. Right-aligned in the box it would begin further
          from the bar than the previous column's digits
          (`measure/table.spec.ts` check 2).
          Hidden from assistive technology where the track's own name already
          says the figure. */}
      <span
        data-share={Math.round(percent)}
        className={`shrink-0 tabular-nums ${track ? 'w-10 text-left' : ''}`}
        {...(track ? { 'aria-hidden': true } : {})}
      >
        {Math.round(percent)}%
      </span>
      {hover === undefined ? null : <span className="sr-only">{hover}</span>}
    </span>
  );
}
