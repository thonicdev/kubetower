import type { ReactNode } from 'react';

import { fraction, Measure, readingText, type ReadingTone } from '../../components/Reading';

import type { Reading } from './data';

/**
 * A measurement, drawn only where there is one.
 *
 * `Reading` carries `value` **nullable** beside `trustworthy`, and
 * `api/openapi.yaml` says exactly why: a bare float is a complete-looking value,
 * and an untrustworthy reading drawn as a number is a failed answer drawn as a
 * good one, in a gauge, where it looks entirely fine.
 *
 * So there is one predicate, `measured`, and nothing in this directory reads
 * `value` without going through it.
 *
 * ## Non-finite, even though JSON cannot carry it
 *
 * `strconv.ParseFloat` accepts `"NaN"` and `"+Inf"`, the upstream writes both,
 * and the Go encoder refuses both - which is why the contract makes `value`
 * nullable in the first place. `Number.isFinite` is still checked here, because
 * the guarantee is upstream of this file and one `1e999` in a hand-written
 * fixture, a proxy, or a future encoder would otherwise draw `Infinity` as a
 * full bar. A gap is a gap: it is drawn as a gap or it is not drawn.
 */

/** The only way a number reaches the screen on this page. */
export function measured(reading: Reading | undefined): reading is Reading & { value: number } {
  return (
    reading !== undefined &&
    reading.trustworthy &&
    reading.value !== null &&
    Number.isFinite(reading.value)
  );
}

/**
 * What was measured, or why nothing was.
 *
 * `text` is the server's rendered form, computed once so that a table cannot
 * report three different units for the same quantity - so it is preferred over
 * anything formatted here. On an unmeasured reading the server puts its sentence
 * in the same field ("the cluster reported no capacity").
 *
 * **The dash and a short word, and the sentence on hover.** The longest of the
 * server's sentences is a hundred characters: drawn beside the dash under a
 * capacity ring on a cluster with no metrics backend, it ran past its tile and
 * over the next one at 1280px. The dash says there is no number, *not measured*
 * says it is not a zero, and the server's sentence is still one hover away - and
 * still read out, so a screen reader loses nothing.
 * Every reading on the dashboard comes through here, so the node table and the
 * top boards take the same shape rather than a second one.
 */
export function ReadingText({ reading }: { reading: Reading | undefined }): ReactNode {
  if (measured(reading)) {
    return <span className="tabular-nums">{reading.text ?? String(reading.value)}</span>;
  }

  const why = reading?.text !== undefined && reading.text !== '' ? reading.text : undefined;
  return (
    <span data-unmeasured="true" className="text-muted" {...(why === undefined ? {} : { title: why })}>
      <span aria-hidden="true">—</span>
      <span className="ml-1 text-xs">not measured</span>
      {why === undefined ? null : <span className="sr-only">: {why}</span>}
    </span>
  );
}

function toneFor(fraction: number): ReadingTone {
  if (fraction >= 0.9) return 'bad';
  if (fraction >= 0.75) return 'warn';
  return 'ok';
}

export interface GaugeProps {
  used: Reading | undefined;
  capacity: Reading | undefined;
  /** For the accessible name: "CPU", "Memory". */
  label: string;
}

/**
 * One bar, and the arithmetic behind whether it is drawn at all.
 *
 * **An unmeasured cell draws no track**, which is theme.css's own note: an empty
 * track reads as 0%, which reads as an idle machine, which is the opposite of
 * the truth when nothing could be measured. The same goes for a capacity of zero - dividing by it gives `Infinity` or `NaN`, and
 * both would paint something.
 *
 * **How the bar is drawn is not decided here.** `components/Reading.tsx` holds
 * the console's one anatomy for a measured quantity, and the whole of this
 * function is the arithmetic above plus one call into it: one style for a
 * reading, decided once.
 *
 * **This has no caller.** It is an exported component nothing
 * imports; `Donut` draws the overview's readings and
 * `pages/resources/ResourceCell` the grid's. It is kept rather than deleted
 * because deleting a public export is a decision about the surface and not about
 * the style, and it is folded into the shared anatomy rather than left as a
 * third private copy of it - a dead second answer is still a second answer the
 * next reader copies.
 */
export function Gauge({ used, capacity, label }: GaugeProps): ReactNode {
  // Written as an early return rather than as one boolean, because a type
  // predicate narrows the parameter only where control flow reaches it: a
  // `const drawable = measured(used) && …` would leave `used.value` possibly
  // null below, and the shortest way past that compiler error is a `!` - which
  // is this whole file's rule, deleted in one keypress.
  if (!measured(used) || !measured(capacity) || capacity.value <= 0) {
    return <Measure value={<ReadingText reading={used} />} label={label} />;
  }

  const share = Math.min(1, Math.max(0, used.value / capacity.value));

  return (
    <Measure
      value={<ReadingText reading={used} />}
      title={fraction(readingText(used), readingText(capacity))}
      percent={share * 100}
      tone={toneFor(share)}
      label={label}
    />
  );
}
