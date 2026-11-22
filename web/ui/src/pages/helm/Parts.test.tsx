import { act, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { Updated } from './Parts';
import { formatInstant } from '../../components/instant';

/**
 * The `Updated` cell, against a clock it does not own.
 *
 * Two things a rendered age can get wrong: computing the age from `Date.now()`
 * once and never again, so a list left open says "4m" for an hour; and
 * clamping a negative delta to `0s`, so a laptop whose clock was behind the cluster's reported every release as
 * deployed this second. The faked clock is what makes both assertable.
 */

const NOW = new Date('2026-09-13T10:00:00Z');

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
});

afterEach(() => {
  vi.useRealTimers();
});

function cell(): HTMLElement {
  const el = screen.getByTitle(/./);
  return el;
}

describe('the updated cell', () => {
  it('ticks: the age changes without anything else re-rendering', () => {
    render(<Updated at={new Date(NOW.getTime() - 30_000).toISOString()} />);
    expect(cell().textContent).toBe('30s');

    // One tick of the shared clock. Nothing above this component re-renders;
    // the cell has to move on its own.
    act(() => {
      vi.advanceTimersByTime(30_000);
    });
    expect(cell().textContent).toBe('1m');
  });

  it('draws an instant ahead of this clock as the instant, never as "0s"', () => {
    const ahead = new Date(NOW.getTime() + 60 * 60 * 1000);
    render(<Updated at={ahead.toISOString()} />);

    expect(cell().textContent).not.toBe('0s');
    expect(cell().textContent).toBe(formatInstant(ahead, { year: true, seconds: true }));
  });

  it('still draws the em dash, and says why, when Helm recorded no time', () => {
    render(<Updated at={undefined} />);
    expect(screen.getByTitle('Helm recorded no time for this').textContent).toBe('—');
  });
});

/**
 * `HelmRelease.age` is the server's own wording,
 * as `EventEntry.age` is, *"so that two panels drawing the same event cannot
 * spell it two ways"*. It is a snapshot taken at the read, and this page does
 * not poll - so the cell draws the server's word while it is current and
 * carries the reading forward on the shared clock from the first tick, on the
 * server's own scale, so that the only thing that changes the word is time
 * passing.
 */
describe('the server’s age and the browser’s clock', () => {
  it('draws the server’s word at the read, and its own reading from the first tick', () => {
    // A hundred seconds ago by this browser; the server, whose clock is eighty
    // seconds ahead, said "3m". At the read the server's word is the one drawn -
    // it is the reading the rest of the console spells its ages with.
    render(<Updated at={new Date(NOW.getTime() - 100_000).toISOString()} age="3m" />);
    expect(cell().textContent).toBe('3m');

    // One tick later the snapshot is stale by definition, and the browser
    // ages `updatedAt` itself: 130 seconds, "2m" on the server's scale.
    act(() => {
      vi.advanceTimersByTime(30_000);
    });
    expect(cell().textContent).toBe('2m');
  });

  it('keeps the server’s word when the instant is ahead of this clock, and says whose it is', () => {
    // The browser's subtraction here is a clock disagreement, not an age. The
    // server's reading is the honest relative one, and the tooltip names the clock.
    const ahead = new Date(NOW.getTime() + 60 * 60 * 1000);
    render(<Updated at={ahead.toISOString()} age="4m" />);
    expect(cell().textContent).toBe('4m');
    expect(cell().getAttribute('title')).toContain(formatInstant(ahead, { year: true, seconds: true }));
    expect(cell().getAttribute('title')).toMatch(/server/);
  });

  it('spells its own reading on the server’s scale, so a tick cannot rename the cell', () => {
    // Thirty hours. The server's `ShortDuration` says "1d" from twenty-four hours
    // and truncates, where a scale that rounds would say "30h". Two spellings for
    // one release is the defect to avoid, and a fallback drawing the other one
    // would reintroduce it on the first tick.
    render(<Updated at={new Date(NOW.getTime() - 30 * 60 * 60 * 1000).toISOString()} />);
    expect(cell().textContent).toBe('1d');
  });
});
