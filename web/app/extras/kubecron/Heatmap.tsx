import { useLayoutEffect, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';
import { focusRingTight } from '@kubetower/ui';

import type { Cell, Grid, Square, StateFilter } from './calendar';

/**
 * The heatmap, drawn: one column a week, Monday at
 * the top, as GitHub draws contributions.
 *
 * **One size, wherever it is drawn**: a heatmap stretched to its room is far
 * too big on a wide page and tiny in a tab. The page's
 * year, a CronJob's page and the tab on a Pod, a Job or a CronJob all draw
 * this component, with the square in `CELL` and the gap in `GAP`, and nothing
 * stretches them. GitHub's contribution graph is the reference: a 12px square
 * 3px from the next, so a year is about 800px wide. Where that is wider than
 * the room it is given, the heatmap scrolls inside its own box, opened on the
 * newest week, and the page never scrolls sideways.
 *
 * A square is a button wherever there is something to show: a day nobody
 * recorded is not.
 *
 * **One Tab stop, and the arrows inside**: a year
 * is 371 squares, and 371 Tab stops is a page nobody can leave by keyboard.
 * The picked day, or the newest, holds the stop; Up and Down move a day, Left
 * and Right a week, Home and End to either end, skipping what was not
 * recorded. Each square is named with its counts, not only titled with them.
 *
 * The legend is drawn with it, from the same scale: a filter on failed runs
 * shades in red, so its legend does too.
 */

/** The square, and the legend's swatches: the one size token. */
export const CELL = 'size-3';
/** Between two squares, across and down. */
export const GAP = 'gap-0.75';

const SHADE: Readonly<Record<Exclude<Square, 'unrecorded' | 'none'>, readonly string[]>> = {
  ok: ['bg-ok/30', 'bg-ok/50', 'bg-ok/75', 'bg-ok'],
  failed: ['bg-bad/40', 'bg-bad/60', 'bg-bad/80', 'bg-bad'],
  running: ['bg-warn/40', 'bg-warn/60', 'bg-warn/80', 'bg-warn'],
};

const NONE = 'bg-panel-2';
const UNRECORDED = 'border border-dashed border-line';

export function squareClass(cell: Pick<Cell, 'kind' | 'level'>): string {
  if (cell.kind === 'unrecorded') return UNRECORDED;
  if (cell.kind === 'none' || cell.level === 0) return NONE;
  return SHADE[cell.kind][cell.level - 1] ?? NONE;
}

const WORD: Readonly<Record<Square, string>> = {
  unrecorded: 'not recorded',
  none: 'no run',
  ok: 'succeeded',
  failed: 'failed',
  running: 'running',
};

export function title(cell: Cell): string {
  const c = cell.counts;
  if (c === undefined) return `${cell.day}: ${WORD[cell.kind]}`;
  const parts = [`${c.total} run${c.total === 1 ? '' : 's'}`];
  if (c.failed > 0) parts.push(`${c.failed} failed`);
  if (c.running > 0) parts.push(`${c.running} running`);
  return `${cell.day}: ${parts.join(', ')}`;
}

const WEEKDAYS = ['Mon', '', 'Wed', '', 'Fri', '', ''];

export function Heatmap({
  grid,
  picked,
  onPick,
  label,
}: {
  grid: Grid;
  picked?: string | undefined;
  onPick?: ((day: string) => void) | undefined;
  label: string;
}): ReactNode {
  const days = grid.weeks.flat().filter((c): c is Cell => c !== null);
  const open = days.filter((c) => c.kind !== 'unrecorded').map((c) => c.day);
  const [active, setActive] = useState<string | undefined>(undefined);
  const holder = active !== undefined && open.includes(active) ? active : picked !== undefined && open.includes(picked) ? picked : open[open.length - 1];
  const cells = useRef<HTMLOListElement>(null);
  const box = useRef<HTMLDivElement>(null);
  // Opened on the newest week, as GitHub opens its graph, when it is wider
  // than its box.
  const weeks = grid.weeks.length;
  useLayoutEffect(() => {
    const el = box.current;
    if (el !== null) el.scrollLeft = el.scrollWidth;
  }, [weeks]);
  const move = (event: KeyboardEvent<HTMLOListElement>) => {
    const all = days.map((c) => c.day);
    const at = holder === undefined ? -1 : all.indexOf(holder);
    if (at < 0) return;
    const step = { ArrowUp: -1, ArrowDown: 1, ArrowLeft: -7, ArrowRight: 7 }[event.key];
    let to: string | undefined;
    if (event.key === 'Home') to = open[0];
    else if (event.key === 'End') to = open[open.length - 1];
    else if (step !== undefined) {
      for (let i = at + step; i >= 0 && i < all.length; i += step) {
        const day = all[i];
        if (day !== undefined && open.includes(day)) {
          to = day;
          break;
        }
      }
    } else return;
    event.preventDefault();
    if (to === undefined) return;
    setActive(to);
    cells.current?.querySelector<HTMLButtonElement>(`[data-day="${to}"] button`)?.focus();
  };
  const square = `${CELL} rounded-sm`;
  return (
    <div className="flex min-w-0 flex-col gap-2" data-heatmap-cell={CELL}>
      {/* The scroll box: a year is wider than a narrow window, and only the
          heatmap scrolls then, never the page. The padding keeps the picked
          square's outline inside it. */}
      <div ref={box} className="max-w-full overflow-x-auto p-1" data-heatmap-scroll="">
        <div role="group" aria-label={label} className={`grid w-max grid-cols-[auto_auto] ${GAP} text-2xs leading-none text-muted`}>
          <span />
          {/* One slot a week, so a month's name sits over its own week. */}
          <ol aria-hidden className={`grid auto-cols-max grid-flow-col ${GAP}`}>
            {grid.weeks.map((_, w) => (
              <li key={w} className="w-3 whitespace-nowrap">
                {grid.months.find((m) => m.week === w)?.label ?? ''}
              </li>
            ))}
          </ol>
          <ol aria-hidden className={`grid grid-rows-7 ${GAP} pr-1`}>
            {WEEKDAYS.map((d, i) => (
              <li key={i} className="flex items-center">
                {d}
              </li>
            ))}
          </ol>
          <ol ref={cells} onKeyDown={onPick === undefined ? undefined : move} className={`grid auto-cols-max grid-flow-col grid-rows-7 ${GAP}`}>
            {grid.weeks.flatMap((week, w) =>
              week.map((cell, d) =>
                cell === null ? (
                  <li key={`${w}-${d}`} className={square} />
                ) : (
                  <li key={cell.day} data-day={cell.day} data-square={cell.kind} className="flex">
                    {cell.kind === 'unrecorded' || onPick === undefined ? (
                      <span title={title(cell)} className={`${square} ${squareClass(cell)}`} />
                    ) : (
                      <button
                        type="button"
                        title={title(cell)}
                        aria-label={`Runs of ${title(cell)}`}
                        aria-pressed={picked === cell.day}
                        tabIndex={cell.day === holder ? 0 : -1}
                        onFocus={() => setActive(cell.day)}
                        onClick={() => onPick(cell.day)}
                        className={`${square} ${squareClass(cell)} ${picked === cell.day ? 'outline outline-2 outline-offset-1 outline-accent' : ''} ${focusRingTight}`}
                      />
                    )}
                  </li>
                ),
              ),
            )}
          </ol>
        </div>
      </div>
      <Legend state={grid.state} />
    </div>
  );
}

/** What the scale shades for each filter: a filter on one outcome is drawn in its color. */
const SCALE: Readonly<Record<StateFilter, Exclude<Square, 'unrecorded' | 'none'>>> = {
  all: 'ok',
  succeeded: 'ok',
  failed: 'failed',
  running: 'running',
};

/**
 * Less to more in the scale the squares are shaded in, from a day without a
 * run, then what the other squares mean, in as few words. The days are the
 * collector's, which are UTC days: a run at 00:30 in Paris is filed under the
 * day before.
 */
export function Legend({ state = 'all' }: { state?: StateFilter }): ReactNode {
  const swatch = `inline-block ${CELL} rounded-sm`;
  return (
    <ul className="flex flex-wrap items-center gap-x-3 gap-y-1 text-2xs text-muted" data-legend={state}>
      <li className="flex flex-wrap items-center gap-1" data-legend-scale={SCALE[state]}>
        less
        <span className={`${swatch} ${NONE}`} title="no run" />
        {SHADE[SCALE[state]].map((c) => (
          <span key={c} className={`${swatch} ${c}`} />
        ))}
        more
      </li>
      {state === 'all' ? (
        <>
          <li className="flex items-center gap-1">
            <span className={`${swatch} ${SHADE.failed[3] ?? ''}`} /> a run failed
          </li>
          <li className="flex items-center gap-1">
            <span className={`${swatch} ${SHADE.running[3] ?? ''}`} /> running
          </li>
        </>
      ) : null}
      <li className="flex items-center gap-1">
        <span className={`${swatch} ${UNRECORDED}`} /> not recorded
      </li>
      <li>UTC days</li>
    </ul>
  );
}
