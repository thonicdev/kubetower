import type { ReactNode } from 'react';

import { fraction, Measure, readingText } from '../../components/Reading';

import type { CapacityCell } from './data';
import { measured, ReadingText } from './Reading';

/**
 * The capacity ring.
 *
 * It is the dashboard's **capacity donut**, drawn in one place: CPU, memory and
 * pods, at the top of the cluster overview. A list row's usage columns carry
 * bars, not rings.
 *
 * ## Geometry and behavior
 *
 * **The geometry:** 120 units, radius 46, stroke 13, the arc starting at twelve
 * o'clock. The track is drawn first and the arc over it, so the ring reads as a
 * whole with a part colored in rather than as a fragment floating in space.
 *
 * **A fraction over 1 fills the ring completely and still prints its real
 * percentage.** An over-committed cluster is a normal state, and capping the
 * *number* as well as the shape would hide the only reading that matters.
 *
 * **The grading is the server's**, not re-derived here: the thresholds are a
 * scheduling fact - past 90 % requested, the next pod of any size has nowhere to
 * go - and they live where the figures are computed. `tone` comes down on the
 * cell.
 *
 * The SVG is JSX rather than a string the server wrote. The reading beside the
 * shape, the grading, the accessible sentence and the over-commit rule all live
 * here.
 *
 * ## The sub-label is not squeezed
 *
 * The figure is drawn **under** the ring in ordinary text, which cannot overlap
 * anything, so no text-length estimate is needed to keep it out of the hole.
 *
 * ## Which arc is which, decided once and written on the panel
 *
 * Nothing on the tile may leave the reader guessing which of the two quantities
 * the ring is:
 *
 * - **the outer ring is `requested`**. It is the figure that decides whether the
 *   next pod schedules, and it is available with no metrics backend at all. The
 *   server's `tone` grades it, because past 90 % requested the next pod of
 *   any size has nowhere to go — a scheduling fact, computed where the figures
 *   are;
 * - **the thin inner arc is `used`**, in the accent rather than in a grade. A
 *   grade on `used` would say the same threshold means the same thing for a
 *   quantity the scheduler never looks at, which it does not. It is drawn
 *   **before** the requested arc so that the graded one is the last circle in
 *   the group, which is what `Panels.test.tsx` reads when it asserts the arc is
 *   clamped at an over-commit;
 * - **and both are named underneath**, each with a swatch in its own color and
 *   its own line drawn to the console's reading anatomy
 *   (`components/Reading.tsx`): the term and the percentage, with
 *   `used / total` on hover. The swatch is never the only distinction - the word
 *   is there beside it, because color alone is a distinction some readers cannot
 *   see.
 *
 * `used` against the **same** `capacity` is deliberate: two denominators on one
 * tile would be two wholes, and the pair is only comparable because it is not.
 */

/** Full class strings, looked up. Tailwind scans source text. */
const ARC: Readonly<Record<string, string>> = {
  ok: 'text-ok',
  warn: 'text-warn',
  bad: 'text-bad',
  muted: 'text-muted',
};

/** The ring's geometry: 120 units, radius 46, stroke 13, the arc starting at
 *  twelve o'clock. The rendered size is a utility on the `<svg>`, so the ring
 *  keeps its proportions at any size. */
const SIZE = 120;
const RADIUS = 46;
const STROKE = 13;
const CIRCUMFERENCE = 2 * Math.PI * RADIUS;

/** The inner arc: `used`, inside the ring's hole and thinner, so it reads as a
 *  second series rather than as a second ring. 32 and 6 leave the hole 29 units
 *  of radius, which is what the centered percentage needs at this font size. */
const USED_RADIUS = 32;
const USED_STROKE = 6;
const USED_CIRCUMFERENCE = 2 * Math.PI * USED_RADIUS;

export function CapacityDonut({ cell }: { cell: CapacityCell }): ReactNode {
  // **Requested against allocatable**, which is the figure that decides whether
  // the next pod schedules and is available without any metrics backend at all.
  // `used` is a different quantity from a different source: it is the thin inner
  // arc and its own named line, never the ring.
  const requested = cell.requested;
  const capacity = cell.capacity;
  const drawable = measured(requested) && measured(capacity) && capacity.value > 0;
  const whole = measured(capacity) && capacity.value > 0 ? capacity : undefined;

  return (
    // `w-full min-w-0`: a figure centered in a flex column is otherwise as wide as
    // its widest line, so a long reading under the ring widened the figure past
    // its tile instead of being cut inside it.
    <figure data-donut={cell.label} className="flex w-full min-w-0 flex-col items-center gap-3">
      {drawable ? (
        <Ring
          fraction={requested.value / capacity.value}
          used={measured(cell.used) ? cell.used.value / capacity.value : undefined}
          tone={cell.tone}
          label={`${cell.label}: ${requested.text ?? ''} of ${capacity.text ?? ''}`}
        />
      ) : (
        // **The reason goes inside the frame.** A chart-shaped blank with an
        // explanation somewhere below it is how "this cluster has no capacity
        // reading" gets read as "this chart is broken".
        <Empty label={cell.label} why={capacity.text ?? requested.text} />
      )}

      {/* The readings under the shape, always, and each one **named**: nothing on
          this page depends on the reader's ability to judge an angle or on
          guessing which quantity an angle is. */}
      <figcaption className="flex w-full flex-col gap-1">
        <Line
          term="requested"
          swatch={SWATCH[cell.tone ?? 'muted'] ?? 'bg-accent'}
          reading={requested}
          whole={whole}
          of={cell.requestedOf}
        />
        <Line term="used" swatch="bg-accent" reading={cell.used} whole={whole} of={cell.usedOf} />
      </figcaption>
    </figure>
  );
}

/** The tone's swatch, matching the arc it names. Full class strings, looked up:
 *  Tailwind scans source text. */
const SWATCH: Readonly<Record<string, string>> = {
  ok: 'bg-ok',
  warn: 'bg-warn',
  bad: 'bg-bad',
  muted: 'bg-muted',
  normal: 'bg-accent',
};

/**
 * One named quantity under the ring, drawn to the console's reading anatomy:
 * its percentage of the tile's whole, and `used / total` on hover, as the server
 * wrote it with both halves in the capacity's unit (`requestedOf`, `usedOf`).
 *
 * No track: the ring above **is** this line's shape, and a bar under the words
 * as well would be the same measurement drawn twice — so `track={false}`, and
 * the percentage is the line's figure. It is the real one past 100 %, as the
 * ring's center is: an over-committed cluster reads `118%`, never a clamped
 * `100%`. A line whose reading or whole was not measured carries no percentage
 * and draws the dash and *not measured*, which is the same call an unmeasured
 * grid cell makes.
 */
function Line({
  term,
  swatch,
  reading,
  whole,
  of,
}: {
  term: string;
  swatch: string;
  reading: CapacityCell['requested'] | undefined;
  whole: CapacityCell['capacity'] | undefined;
  /** The server's hover for this line, both halves in the capacity's unit
   *  (`CapacityCell.requestedOf`, `usedOf`). When absent, it
   *  falls back to the two readings' texts joined. */
  of?: string | undefined;
}): ReactNode {
  const share =
    measured(reading) && measured(whole) && whole.value > 0
      ? (reading.value / whole.value) * 100
      : undefined;
  return (
    <Measure
      align="start"
      label={term}
      term={
        <span className="flex items-center gap-1.5">
          <span aria-hidden="true" className={`size-2 shrink-0 rounded-full ${swatch}`} />
          {term}
        </span>
      }
      value={<ReadingText reading={reading} />}
      track={false}
      {...(share === undefined
        ? {}
        : { percent: share, title: of !== undefined && of !== '' ? of : fraction(readingText(reading), readingText(whole)) })}
    />
  );
}

function Ring({
  fraction,
  used,
  tone,
  label,
}: {
  fraction: number;
  /** `used` over the same capacity, where the metrics backend answered. */
  used: number | undefined;
  tone: string | undefined;
  label: string;
}): ReactNode {
  // Clamped for the **shape** and not for the number: the arc cannot be drawn
  // past the ring, and the percentage below is the real one.
  const drawn = Math.min(1, Math.max(0, fraction));
  const filled = drawn * CIRCUMFERENCE;
  const percent = Math.round(fraction * 100);
  const usedFilled =
    used === undefined ? undefined : Math.min(1, Math.max(0, used)) * USED_CIRCUMFERENCE;

  return (
    // **`size-48`**: the capacity ring is the first and largest thing on the page.
    // The viewBox is fixed, so every proportion survives the size.
    <svg
      viewBox={`0 0 ${String(SIZE)} ${String(SIZE)}`}
      role="img"
      aria-label={`${label}, ${String(percent)} percent`}
      data-percent={percent}
      className={`size-48 max-w-full ${ARC[tone ?? 'muted'] ?? 'text-accent'}`}
    >
      {/* The track first, the arc over it. */}
      <circle
        cx={SIZE / 2}
        cy={SIZE / 2}
        r={RADIUS}
        fill="none"
        stroke="var(--color-line)"
        strokeWidth={STROKE}
      />
      {/* **`used`, before the requested arc and inside it.** Before, because the
          graded arc has to be the group's last `<circle>`: `Panels.test.tsx`
          reads `circles.at(-1)` to assert the over-commit is clamped, and a test
          that silently starts measuring a different arc is worse than no test.
          Inside and thinner, because it is a second series on the same tile and
          not a second ring. The accent rather than a grade — the thresholds are a
          scheduling fact about requests, and `used` is not what the scheduler
          reads. */}
      {usedFilled === undefined ? null : (
        <circle
          data-arc="used"
          cx={SIZE / 2}
          cy={SIZE / 2}
          r={USED_RADIUS}
          fill="none"
          stroke="var(--color-accent)"
          strokeWidth={USED_STROKE}
          strokeDasharray={`${String(usedFilled)} ${String(USED_CIRCUMFERENCE - usedFilled)}`}
          transform={`rotate(-90 ${String(SIZE / 2)} ${String(SIZE / 2)})`}
        />
      )}
      <circle
        data-arc="requested"
        cx={SIZE / 2}
        cy={SIZE / 2}
        r={RADIUS}
        fill="none"
        stroke="currentColor"
        strokeWidth={STROKE}
        strokeDasharray={`${String(filled)} ${String(CIRCUMFERENCE - filled)}`}
        // Twelve o'clock, so a small fraction is read as a small fraction rather
        // than as a slice starting somewhere arbitrary.
        transform={`rotate(-90 ${String(SIZE / 2)} ${String(SIZE / 2)})`}
      />
      <text
        x={SIZE / 2}
        y={SIZE / 2}
        textAnchor="middle"
        dominantBaseline="central"
        fontSize="24"
        fontWeight="600"
        fill="var(--color-fg)"
      >
        {percent}%
      </text>
    </svg>
  );
}

/**
 * A ring with nothing to draw, and the reason **inside** the frame.
 *
 * The empty track is deliberately not drawn: a full-size ring at 0 % reads as an
 * idle cluster, which is the opposite of the truth when nothing could be
 * measured.
 */
function Empty({ label, why }: { label: string; why: string | undefined }): ReactNode {
  return (
    <div
      role="img"
      aria-label={`${label}: ${why ?? 'not measured'}`}
      data-donut-empty="true"
      className="flex size-48 max-w-full items-center justify-center rounded-full border border-dashed border-line p-6 text-center text-xs text-muted"
    >
      {why ?? 'no capacity reported'}
    </div>
  );
}
