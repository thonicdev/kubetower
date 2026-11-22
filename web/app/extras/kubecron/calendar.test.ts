import { describe, expect, it } from 'vitest';

import { countFor, dayOfInstant, grid, kindOf, knownFrom, sumDays } from './calendar';
import { MAX_NAMED, dayScope, filtered, ranOn, yearWindow } from './PageHeatmap';
import { figures } from './RunView';
import { momentOn, zoneDay } from './format';

/** The heatmap's arithmetic, apart from any drawing. */

const day = (d: string, total: number, failed = 0, running = 0) => ({ day: d, total, succeeded: total - failed - running, running, failed });

describe('the calendar grid', () => {
  it('puts each day under its week, Monday first, and pads the first and last weeks', () => {
    // 2026-10-21 is a Wednesday; eleven days back is Sunday 2026-10-11.
    const g = grid({ byDay: new Map(), windowDays: 11, today: '2026-10-21', known: undefined, state: 'all' });
    expect(g.weeks).toHaveLength(3);
    expect(g.weeks[0]?.map((c) => c?.day ?? null)).toEqual([null, null, null, null, null, null, '2026-10-11']);
    expect(g.weeks[1]?.[0]?.day).toBe('2026-10-12');
    expect(g.weeks[2]?.map((c) => c?.day ?? null)).toEqual(['2026-10-19', '2026-10-20', '2026-10-21', null, null, null, null]);
    expect(g.weeks.flat().filter((c) => c !== null)).toHaveLength(11);
  });

  it('draws a day nobody recorded as unrecorded and a recorded day without runs as none', () => {
    const g = grid({ byDay: new Map(), windowDays: 3, today: '2026-10-21', known: '2026-10-20', state: 'all' });
    expect(g.weeks.flat().filter((c) => c !== null).map((c) => c?.kind)).toEqual(['unrecorded', 'none', 'none']);
  });

  it('shades a day by its count against the busiest, and colors a failure over a success', () => {
    const byDay = sumDays([{ days: [day('2026-10-20', 8), day('2026-10-21', 2, 1)] }]);
    const cells = grid({ byDay, windowDays: 2, today: '2026-10-21', known: undefined, state: 'all' }).weeks.flat().filter((c) => c !== null);
    expect(cells.map((c) => [c?.kind, c?.level])).toEqual([
      ['ok', 4],
      ['failed', 1],
    ]);
  });

  it('counts only the runs the state filter keeps', () => {
    const counts = { total: 5, succeeded: 3, running: 1, failed: 1 };
    expect([countFor(counts, 'all'), countFor(counts, 'failed'), countFor(counts, 'succeeded'), countFor(counts, 'running')]).toEqual([5, 1, 3, 1]);
    expect(kindOf(counts, 'succeeded')).toBe('ok');
    expect(kindOf({ total: 2, succeeded: 2, running: 0, failed: 0 }, 'failed')).toBe('none');
    // A day with a run still going and nothing finished is running, not ok.
    expect(kindOf({ total: 1, succeeded: 0, running: 1, failed: 0 }, 'all')).toBe('running');
  });

  it('sums several CronJobs day by day', () => {
    const sum = sumDays([{ days: [day('2026-10-20', 2, 1)] }, { days: [day('2026-10-20', 3), day('2026-10-21', 1)] }]);
    expect(sum.get('2026-10-20')).toEqual({ total: 5, succeeded: 4, running: 0, failed: 1 });
    expect(sum.get('2026-10-21')?.total).toBe(1);
  });

  it('knows from the later of when the collector began and how far back it keeps runs', () => {
    expect(knownFrom('2026-10-21', '2026-10-01T08:00:00Z', 90)).toBe('2026-10-01');
    expect(knownFrom('2026-10-21', '2025-01-01T00:00:00Z', 90)).toBe('2026-07-24');
    expect(knownFrom('2026-10-21', undefined, undefined)).toBeUndefined();
  });

  it('names a month over its first week and files a run under its UTC day', () => {
    const g = grid({ byDay: new Map(), windowDays: 60, today: '2026-10-21', known: undefined, state: 'all' });
    expect(g.months.map((m) => m.week)).toEqual([...new Set(g.months.map((m) => m.week))]);
    expect(g.months.length).toBeGreaterThanOrEqual(2);
    expect(dayOfInstant('2026-10-20T23:30:00-02:00')).toBe('2026-10-21');
  });
});

describe("the page's year and filters", () => {
  it('draws fifty-two whole weeks and this one', () => {
    // A Wednesday: 364 days back plus Monday to Wednesday.
    expect(yearWindow('2026-10-21')).toBe(367);
    const g = grid({ byDay: new Map(), windowDays: yearWindow('2026-10-21'), today: '2026-10-21', known: undefined, state: 'all' });
    expect(g.weeks).toHaveLength(53);
    expect(g.weeks[0]?.[0]?.day).toBe('2025-10-20');
  });

  it('keeps the calendars of a namespace or one CronJob, and asks a day only of those that ran', () => {
    const answer = {
      collector: { state: 'ok' as const },
      complete: true,
      windowDays: 90,
      cronJobs: [
        { namespace: 'a', name: 'x', days: [day('2026-10-20', 1, 1)] },
        { namespace: 'a', name: 'y', days: [day('2026-10-20', 1)] },
        { namespace: 'b', name: 'x', days: [] },
      ],
      unread: [],
    };
    expect(filtered(answer, { ns: 'a', cronJob: undefined }).map((c) => c.name)).toEqual(['x', 'y']);
    expect(filtered(answer, { ns: undefined, cronJob: 'b/x' }).map((c) => c.namespace)).toEqual(['b']);
    expect(ranOn(answer.cronJobs, '2026-10-20', 'all')).toEqual(['a/x', 'a/y']);
    expect(ranOn(answer.cronJobs, '2026-10-20', 'failed')).toEqual(['a/x']);
  });
});

describe('what a day is asked for', () => {
  const cal = (n: number, ns = 'a') =>
    Array.from({ length: n }, (_, i) => ({ namespace: ns, name: `j${i}`, days: [day('2026-10-20', 1)] }));
  const f = { ns: undefined, cronJob: undefined, state: 'all' as const, day: '2026-10-20' };

  it('names the CronJobs that ran while they are few', () => {
    expect(dayScope(cal(3), f)).toEqual({ cronJobs: ['a/j0', 'a/j1', 'a/j2'] });
  });

  it('past that, asks by namespace or for everything, never with a thousand names', () => {
    expect(dayScope(cal(MAX_NAMED + 1), f)).toEqual({});
    expect(dayScope(cal(MAX_NAMED + 1), { ...f, ns: 'a' })).toEqual({ namespace: 'a' });
  });

  it('asks nothing when nothing ran', () => {
    expect(dayScope(cal(3), { ...f, day: '2026-10-21' })).toBeUndefined();
  });
});

describe("a run's figures", () => {
  const run = { id: 'r', pod: 'p', trigger: 'scheduled', startedAt: '2026-10-20T00:00:00Z', status: 'succeeded', retryCount: 0, logSizeBytes: 0 };

  it('leaves a measurement nobody recorded out, rather than drawing a dash for it', () => {
    expect(figures({ ...run, maxCpuMillicores: 40 }, undefined)).toEqual({ cpu: 'peak 40m' });
    expect(figures(run, { samples: [], intervalSeconds: 15, avgMemoryBytes: 2048, metricsEnabled: true })).toEqual({ memory: '2.0 KiB' });
    expect(figures(run, undefined)).toBeUndefined();
  });
});

describe('a run near midnight', () => {
  const dateIn = (iso: string, timeZone: string) => new Intl.DateTimeFormat('en', { day: 'numeric', month: 'short', timeZone }).format(new Date(iso));
  const timeIn = (iso: string, timeZone: string) => new Intl.DateTimeFormat('en', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone }).format(new Date(iso));

  it('is filed under its UTC day, as the collector files it', () => {
    expect(dayOfInstant('2026-10-23T22:30:00Z')).toBe('2026-10-23');
    expect(dayOfInstant('2026-10-24T00:30:00+02:00')).toBe('2026-10-23');
    expect(zoneDay(new Date('2026-10-23T22:30:00Z'), 'Europe/Paris')).toBe('2026-10-24');
  });

  it("in a day's list, says its own date where the reader's clock is on another day, and only the time otherwise", () => {
    // 00:30 in Paris on the 24th, filed under the 23rd: the date is said.
    const late = '2026-10-23T22:30:00Z';
    expect(momentOn(late, '2026-10-23', 'Europe/Paris')).toBe(`${dateIn(late, 'Europe/Paris')}, ${timeIn(late, 'Europe/Paris')}`);
    // 23:30 in Paris on the 23rd, under the 23rd: the time alone.
    const evening = '2026-10-23T21:30:00Z';
    expect(momentOn(evening, '2026-10-23', 'Europe/Paris')).toBe(timeIn(evening, 'Europe/Paris'));
    // West of UTC the other way round: 22:30 in New York on the 23rd, filed under the 24th.
    const early = '2026-10-24T02:30:00Z';
    expect(dayOfInstant(early)).toBe('2026-10-24');
    expect(momentOn(early, '2026-10-24', 'America/New_York')).toBe(`${dateIn(early, 'America/New_York')}, ${timeIn(early, 'America/New_York')}`);
    expect(momentOn(early, '2026-10-24', 'UTC')).toBe(timeIn(early, 'UTC'));
    // An instant that does not parse is said as sent, never thrown on.
    expect(momentOn('not a time', '2026-10-24', 'Europe/Paris')).toBe('not a time');
  });
});
