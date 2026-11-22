import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { grid, sumDays, type StateFilter } from './calendar';
import { CELL, Heatmap } from './Heatmap';

/** The heatmap by keyboard: one Tab stop, the
 *  arrows inside, and each square named with its counts. */

const day = (d: string, total: number, failed = 0) => ({ day: d, total, succeeded: total - failed, running: 0, failed });

function draw(onPick = vi.fn()) {
  // 2026-10-21 is a Wednesday; 2026-10-13 and before are not recorded.
  const g = grid({
    byDay: sumDays([{ days: [day('2026-10-21', 3, 1), day('2026-10-14', 2)] }]),
    windowDays: 21,
    today: '2026-10-21',
    known: '2026-10-14',
    state: 'all',
  });
  render(<Heatmap grid={g} onPick={onPick} label="Runs per day" />);
  return onPick;
}

describe('the heatmap by keyboard', () => {
  it('holds one Tab stop, on the newest day', () => {
    draw();
    const stops = [...document.querySelectorAll('button')].filter((b) => b.tabIndex === 0);
    expect(stops.map((b) => b.closest('[data-day]')?.getAttribute('data-day'))).toEqual(['2026-10-21']);
    // A day nobody recorded is no button at all.
    expect(document.querySelector('[data-day="2026-10-13"] button')).toBeNull();
  });

  it('names each square with its counts', () => {
    draw();
    expect(screen.getByRole('button', { name: 'Runs of 2026-10-21: 3 runs, 1 failed' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Runs of 2026-10-20: no run' })).toBeTruthy();
  });

  it('moves a day with Up and Down, a week with Left and Right, and stops at what was not recorded', () => {
    const onPick = draw();
    const newest = document.querySelector('[data-day="2026-10-21"] button') as HTMLButtonElement;
    newest.focus();
    fireEvent.keyDown(newest, { key: 'ArrowUp' });
    expect(document.activeElement?.closest('[data-day]')?.getAttribute('data-day')).toBe('2026-10-20');
    fireEvent.keyDown(document.activeElement as Element, { key: 'ArrowLeft' });
    expect(document.activeElement?.closest('[data-day]')?.getAttribute('data-day')).toBe('2026-10-20');
    fireEvent.keyDown(document.activeElement as Element, { key: 'Home' });
    expect(document.activeElement?.closest('[data-day]')?.getAttribute('data-day')).toBe('2026-10-14');
    fireEvent.keyDown(document.activeElement as Element, { key: 'ArrowRight' });
    expect(document.activeElement?.closest('[data-day]')?.getAttribute('data-day')).toBe('2026-10-21');
    // Still one Tab stop, which followed the focus.
    expect([...document.querySelectorAll('button')].filter((b) => b.tabIndex === 0)).toHaveLength(1);
    fireEvent.click(document.activeElement as Element);
    expect(onPick).toHaveBeenCalledWith('2026-10-21');
  });
});

/** The size utilities on each square and each legend swatch, as drawn. */
function drawnSizes(): Set<string> {
  const drawn = [...document.querySelectorAll('[data-day] > *, [data-legend] span.inline-block')];
  return new Set(drawn.flatMap((e) => [...e.classList].filter((c) => /^(size|w|h|aspect|min-w|basis)-/.test(c))));
}

describe('one size, wherever it is drawn', () => {
  const year = (state: StateFilter = 'all') =>
    grid({ byDay: sumDays([{ days: [day('2026-10-21', 9, 2), day('2026-03-02', 1)] }]), windowDays: 367, today: '2026-10-21', known: undefined, state });

  it("draws a year's squares, a quarter's and the legend's swatches at the one size, never stretched to the width", () => {
    render(
      <>
        <Heatmap grid={year()} onPick={vi.fn()} label="The page's year" />
        <Heatmap grid={grid({ byDay: new Map(), windowDays: 90, today: '2026-10-21', known: '2026-10-01', state: 'all' })} label="A CronJob's quarter" />
      </>,
    );
    expect(document.querySelectorAll('[data-day]').length).toBe(367 + 90);
    expect(drawnSizes()).toEqual(new Set([CELL]));
    // Nothing is laid out from the width it is given.
    expect(document.querySelector('[style*="grid-template-columns"]')).toBeNull();
    // A year wider than its room scrolls inside its own box, not the page.
    expect(document.querySelectorAll('[data-heatmap-scroll].overflow-x-auto')).toHaveLength(2);
  });

  it('draws its legend in the scale its squares are shaded in', () => {
    render(<Heatmap grid={year('failed')} label="Failed runs" />);
    expect(document.querySelector('[data-legend-scale]')?.getAttribute('data-legend-scale')).toBe('failed');
    const swatches = [...document.querySelectorAll('[data-legend-scale] span')].map((e) => e.className);
    expect(swatches.some((c) => c.includes('bg-bad/40'))).toBe(true);
    expect(swatches.some((c) => c.includes('bg-ok'))).toBe(false);
    // The busiest failed day is drawn in a shade the legend shows.
    const busiest = document.querySelector('[data-day="2026-10-21"] > *')?.className ?? '';
    expect(swatches.some((c) => c.split(' ').some((k) => k.startsWith('bg-bad') && busiest.split(' ').includes(k)))).toBe(true);
  });
});
