import type { ReactNode } from 'react';

import { headCell, HEAD_ROW, PAD, TABLE } from '../../components/tableLook';
import type { MetricsPanel, PhaseEntry } from './data';

/**
 * The small shapes the dashboard's panels are built from.
 *
 * Everything in this file is a **token**, never a literal. The one place a number
 * reaches the DOM is a computed geometry - a bar's width, an SVG's points - and
 * that is arithmetic rather than styling: a `w-[${n}%]` is a class Tailwind never
 * scanned and therefore a bar that silently does not draw.
 */

const FILL: Readonly<Record<string, string>> = {
  ok: 'bg-ok',
  warn: 'bg-warn',
  bad: 'bg-bad',
  muted: 'bg-muted',
  normal: 'bg-accent',
};

/**
 * A graded word.
 *
 * `tone` is the server's, and an absent one is `normal` rather than a guess:
 * nothing here re-derives a grade from a number, because the thresholds are a
 * scheduling fact (75 % and 90 %: past 90 % requested, the next pod of any size
 * has nowhere to go) and they live where the figures do.
 *
 * It is the console's one pill: the shape, the tints and the neutral arm live in
 * `components/Pill.tsx`, and this re-export keeps every caller's `<Pill tone=...>`
 * unchanged. `data-tone` is preserved by the shared component; `Panels.test.tsx`
 * reads it.
 */
export { Pill } from '../../components/Pill';

/**
 * The mark that says a row is an init container, and nothing where it is not.
 *
 * ## Why there is a mark at all
 *
 * The restarting panel reads `initContainerStatuses` as well as
 * `containerStatuses`. An init container and an app container restarting are
 * **different facts** - `kubectl logs -c` goes to a different place, and a plain
 * init container that will not start is why a pod never reaches Running at all -
 * so the two must not draw an identical row.
 *
 * ## Why it is neutral and not a tone
 *
 * A shape's tone says *how full is too full* and a cell's tone says *whether this
 * is a fault*; conflating the two is a defect (see `components/Reading.tsx`).
 * Being an init container is **neither**. It is a taxonomy - which of the
 * kubelet's two lists the row came from - and the fault on this row is already
 * graded, by the `Restarts` pill carrying the server's `tone`. A warn-colored
 * mark one cell to its left would read as a second grading of the same row and
 * say "this is worse", which is a claim nothing here can support: the sort
 * deliberately does not promote init containers.
 *
 * So: the panel's `muted` token, which is the same one an absent reading uses.
 * It is legible and it is not a color anybody scans for.
 *
 * **This is a deliberate departure from `pages/object/Containers.tsx`**, whose
 * own init mark is `text-warn`. That one sits alone in a card header with no
 * grading anywhere near it; this one sits in a dense table next to a pill that
 * is warn or bad for a real reason.
 *
 * ## Why a mark rather than a column
 *
 * `PanelTable` below records that some tables lose width at 1024px, and a column
 * carrying one boolean is the first thing that reflow should not have to spend.
 * It rides on the Container cell instead, where the word it qualifies already is.
 *
 * ## Why nothing is drawn for an app container
 *
 * The panel draws a dense list a page at a time, and a word on every row of it
 * is ink spent to say "ordinary". `init` is the marked case and its absence is
 * the unmarked one - which holds because the contract makes the member
 * **required**, so a blank cell cannot mean "the server did not say".
 */
export function InitMark(): ReactNode {
  return (
    <span
      data-init-mark
      title="an init container - its logs are under this name, not the pod's app container"
      className="rounded-sm border border-line px-1 text-2xs text-muted uppercase"
    >
      init
    </span>
  );
}

/**
 * A table wider than its panel scrolls, rather than being clipped.
 *
 * Without it, a table wider than its panel (the hottest-pods memory table at a
 * 1280px window, the restarting and recent-warnings tables at 1024px) has its
 * rightmost columns outside the panel **with no scrollbar anywhere**: not merely
 * cramped but unreachable by any gesture.
 *
 * `.table-scroll` and `.kt-grid` are the names `theme.css` matches on, and
 * `kt-grid` and not `grid` because Tailwind emits `.grid { display: grid }`,
 * which would take the table out of table layout entirely.
 *
 * `pad` is the headings' horizontal padding, and it must be the cells' own: a
 * table whose rows pad tighter than its headings draws every heading off its
 * column. The overview's tables pad 4px a side below `2xl`.
 */
export function PanelTable({
  label,
  head,
  pad = PAD,
  children,
}: {
  label: string;
  head: readonly { key: string; text: ReactNode; numeric?: boolean }[];
  pad?: string;
  children: ReactNode;
}): ReactNode {
  return (
    <div className="table-scroll min-w-0 overflow-x-auto">
      <table className={`${TABLE} w-full`} aria-label={label}>
        <thead>
          <tr className={HEAD_ROW}>
            {head.map((column) => (
              <th
                key={column.key}
                scope="col"
                className={`${headCell(pad)}${column.numeric === true ? ' text-right' : ''}`}
              >
                {column.text}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

/**
 * The pod phase breakdown, as one bar.
 *
 * **Data and not a drawing.** The server sends the counts and the markup is JSX;
 * there is no arithmetic here beyond a share of a total, which is the one sum a
 * client may do because it has every term.
 *
 * **The whole bar leads to the pod list and no band leads anywhere narrower.**
 * Nothing narrower is addressable: the filter above a table matches a name and a
 * namespace, not a phase, so a "Failed 3" that quietly landed on the unfiltered
 * list would be a link that lies about what it did. That is why the server sends
 * no `ref` on a band and the destination is the panel's own.
 */
export function PhaseBar({ phases }: { phases: readonly PhaseEntry[] }): ReactNode {
  const total = phases.reduce((sum, phase) => sum + phase.count, 0);
  if (total === 0) {
    return <p className="text-xs text-muted">This cluster reported no pods to break down.</p>;
  }

  return (
    <div className="flex flex-col gap-1.5">
      <span className="flex h-2 w-full overflow-hidden rounded-full bg-white/10">
        {phases.map((phase) => (
          <span
            key={phase.phase}
            title={`${phase.phase}: ${String(phase.count)}`}
            data-phase={phase.phase}
            className={`block h-full ${FILL[phase.tone ?? 'normal'] ?? 'bg-accent'}`}
            style={{ width: `${String((phase.count / total) * 100)}%` }}
          />
        ))}
      </span>
      {/* The figures repeated as text: nothing on this page depends on the
          reader's ability to judge an angle or on hovering. */}
      <ul className="flex flex-wrap gap-x-3 gap-y-1">
        {phases.map((phase) => (
          <li key={phase.phase} className="flex items-center gap-1 text-xs">
            <span
              aria-hidden="true"
              className={`h-2 w-2 shrink-0 rounded-xs ${FILL[phase.tone ?? 'normal'] ?? 'bg-accent'}`}
            />
            <span className="text-muted">{phase.phase}</span>
            <span className="tabular-nums">{phase.count}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/**
 * Whether a line has nothing this Prometheus could ever chart: no sample, and
 * the server's reason is `not_installed` - no per-container and no pod cgroup
 * series of its counter. Not a failed read, which is a different code,
 * and not an empty window over series that exist, which is `empty_scope`.
 */
export function unchartable(series: MetricsPanel['series'][number]): boolean {
  return (
    series.reason?.code === 'not_installed' &&
    !series.data.values.some((value) => value !== null && Number.isFinite(value))
  );
}

/**
 * One series over time, drawn from the numbers the server sent.
 *
 * ## The gaps
 *
 * `Series.values` carries `null` for a missing sample and **never omits one**,
 * because omitting one shifts every later sample onto the wrong moment and the
 * chart still draws. So a null breaks the line into a new run rather than being
 * interpolated across: a five-minute hole in the scrape has to look like a
 * five-minute hole and not like a steeper line.
 *
 * ## What this deliberately does not do
 *
 * **No crosshair, no readout, no keyboard walk.** Those belong to the overlay,
 * whose sample table is computed on the server so that the client never holds a
 * second copy of the scale - a second copy puts the crosshair a few pixels off,
 * and the only symptom is a number that reads slightly wrong. A sparkline has no
 * crosshair, so scaling a polyline here is safe; the moment a readout is added it
 * stops being safe, and the overlay is where that is answered.
 */
export function Sparkline({ panel, label }: { panel: MetricsPanel; label: string }): ReactNode {
  const series = panel.series[0];
  if (series === undefined) {
    return <p className="text-xs text-muted">No metrics</p>;
  }

  const values = series.data.values;
  const finite = values.filter((value): value is number => value !== null && Number.isFinite(value));
  if (unchartable(series)) {
    // A line this Prometheus cannot chart - it holds neither the container nor
    // the pods' cgroup series - draws nothing at all. "No samples in this
    // window." over it reads as an idle cluster, and a filler sentence is noise.
    // Where no line can be charted the Overview draws no history section either
    // (`unchartable`).
    return null;
  }
  if (finite.length === 0 && series.reason !== undefined && series.reason.code !== 'empty_scope') {
    // A line whose read failed is not an absence, and it stays: two words in
    // the shape `Panel.tsx`'s `NotRead` gives a spoilt panel, with the line's
    // name since two of them sit side by side, and the reason on hover rather
    // than a paragraph under the chart. Different does not mean louder.
    return (
      <p data-state="incomplete" title={series.reason.message} className="text-xs text-muted">
        {label} <span aria-hidden="true">&mdash;</span> Metrics unavailable
      </p>
    );
  }
  if (finite.length < 2) {
    // One point is not a line, and a line drawn through one point is a flat
    // line - which is read as "nothing is happening" rather than as "there is
    // one sample".
    return (
      <p className="text-xs text-muted">
        {finite.length === 0 ? 'No metrics' : 'One sample in this window.'}
      </p>
    );
  }

  const low = Math.min(...finite);
  const high = Math.max(...finite);
  const span = high - low === 0 ? 1 : high - low;
  const step = values.length > 1 ? 100 / (values.length - 1) : 100;

  // Runs, split on every gap. Each run is its own polyline.
  const runs: string[][] = [];
  let run: string[] = [];
  values.forEach((value, index) => {
    if (value === null || !Number.isFinite(value)) {
      if (run.length > 1) runs.push(run);
      run = [];
      return;
    }
    const x = index * step;
    const y = 28 - ((value - low) / span) * 24;
    run.push(`${String(x)},${String(y)}`);
  });
  if (run.length > 1) runs.push(run);

  return (
    <figure className="flex flex-col gap-1">
      <svg
        viewBox="0 0 100 32"
        preserveAspectRatio="none"
        role="img"
        aria-label={`${label}: ${String(finite.length)} samples`}
        className="h-8 w-full text-accent"
      >
        {runs.map((points) => (
          <polyline
            key={points[0]}
            points={points.join(' ')}
            fill="none"
            stroke="currentColor"
            strokeWidth="1"
            vectorEffect="non-scaling-stroke"
          />
        ))}
      </svg>
      <figcaption className="flex items-baseline justify-between text-xs text-muted">
        <span>{series.label}</span>
        {/* The figures beside the shape, because a chart nobody can read aloud
            is a chart some users cannot use. */}
        <span className="tabular-nums">
          {low.toFixed(2)} – {high.toFixed(2)}
          {series.unit === undefined ? '' : ` ${series.unit}`}
        </span>
      </figcaption>
    </figure>
  );
}
