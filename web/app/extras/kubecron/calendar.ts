import type { Day } from './data';

/**
 * The heatmap's arithmetic, apart from its drawing so it can be tested
 * alone: days into a grid of weeks, GitHub's way - one
 * column a week, Monday at the top - and each day into a kind and a shade.
 *
 * Every day is a UTC day, as the collector counts them (`parseDay` in
 * `runs.go`).
 */

/** What a square says. **`unrecorded` is not `none`**: a day before the
 *  collector watched, or older than it keeps runs, is a day nobody knows about,
 *  and drawing it as a day without runs is a wall of alarms the morning after
 *  an install. */
export type Square = 'unrecorded' | 'none' | 'ok' | 'failed' | 'running';

/** Which runs a square counts. */
export type StateFilter = 'all' | 'failed' | 'succeeded' | 'running';

export const STATE_FILTERS: readonly StateFilter[] = ['all', 'failed', 'succeeded', 'running'];

export interface Counts {
  total: number;
  succeeded: number;
  running: number;
  failed: number;
}

export interface Cell {
  day: string;
  kind: Square;
  /** The runs counted under the filter. */
  count: number;
  /** The shade, 1 to 4 for a day with runs, 0 for one without. */
  level: 0 | 1 | 2 | 3 | 4;
  counts?: Counts;
}

export interface Grid {
  /** Columns of seven, Monday first; null outside the window. */
  weeks: (Cell | null)[][];
  /** A month's short name over the first week holding its 1st. */
  months: { week: number; label: string }[];
  /** The busiest day's count, which the shades are relative to. */
  max: number;
  /** The runs counted, which is also the scale the legend draws. */
  state: StateFilter;
}

const DAY_MS = 86_400_000;

/** A YYYY-MM-DD as the UTC midnight it names. */
export function dayMs(day: string): number {
  return Date.parse(`${day}T00:00:00Z`);
}

export function dayOf(ms: number): string {
  return new Date(ms).toISOString().slice(0, 10);
}

/** The UTC day of an instant, as the collector files a run under it. */
export function dayOfInstant(iso: string): string | undefined {
  const ms = Date.parse(iso);
  return Number.isNaN(ms) ? undefined : dayOf(ms);
}

/** Several calendars into one: the page's, across the CronJobs filtered in. */
export function sumDays(calendars: readonly { days: readonly Day[] }[]): Map<string, Counts> {
  const out = new Map<string, Counts>();
  for (const c of calendars) {
    for (const d of c.days) {
      const was = out.get(d.day) ?? { total: 0, succeeded: 0, running: 0, failed: 0 };
      out.set(d.day, {
        total: was.total + d.total,
        succeeded: was.succeeded + d.succeeded,
        running: was.running + d.running,
        failed: was.failed + d.failed,
      });
    }
  }
  return out;
}

export function countFor(c: Counts | undefined, state: StateFilter): number {
  if (c === undefined) return 0;
  if (state === 'failed') return c.failed;
  if (state === 'succeeded') return c.succeeded;
  if (state === 'running') return c.running;
  return c.total;
}

/** A failure anywhere in the day colors it, then a run still going. */
export function kindOf(c: Counts | undefined, state: StateFilter): Square {
  if (countFor(c, state) === 0 || c === undefined) return 'none';
  if (state === 'failed') return 'failed';
  if (state === 'succeeded') return 'ok';
  if (state === 'running') return 'running';
  if (c.failed > 0) return 'failed';
  if (c.running > 0 && c.succeeded === 0) return 'running';
  return 'ok';
}

/**
 * The first day anything is known about: the later of when the collector began
 * watching and how far back it keeps runs. Undefined when it said neither, and
 * then every day of the window is drawn as known.
 */
export function knownFrom(today: string, observedSince: string | undefined, runDays: number | undefined): string | undefined {
  const since = observedSince === undefined ? undefined : dayOfInstant(observedSince);
  const kept = runDays === undefined || runDays <= 0 ? undefined : dayOf(dayMs(today) - (runDays - 1) * DAY_MS);
  if (since === undefined) return kept;
  if (kept === undefined) return since;
  return since > kept ? since : kept;
}

export function grid({
  byDay,
  windowDays,
  today,
  known,
  state,
}: {
  byDay: ReadonlyMap<string, Counts>;
  windowDays: number;
  today: string;
  known: string | undefined;
  state: StateFilter;
}): Grid {
  const end = dayMs(today);
  const start = end - (Math.max(1, windowDays) - 1) * DAY_MS;
  // Monday is 0: getUTCDay has Sunday at 0.
  const offset = (new Date(start).getUTCDay() + 6) % 7;
  let max = 0;
  for (let at = start; at <= end; at += DAY_MS) max = Math.max(max, countFor(byDay.get(dayOf(at)), state));

  const weeks: (Cell | null)[][] = [];
  const months: { week: number; label: string }[] = [];
  let week: (Cell | null)[] = Array.from({ length: offset }, () => null);
  for (let at = start; at <= end; at += DAY_MS) {
    const day = dayOf(at);
    const counts = byDay.get(day);
    const count = countFor(counts, state);
    let kind = kindOf(counts, state);
    if (counts === undefined && known !== undefined && day < known) kind = 'unrecorded';
    const level = count === 0 || max === 0 ? 0 : (Math.min(4, Math.max(1, Math.ceil((4 * count) / max))) as 1 | 2 | 3 | 4);
    week.push({ day, kind, count, level, ...(counts === undefined ? {} : { counts }) });
    const date = new Date(at);
    if (date.getUTCDate() === 1 || at === start) {
      const label = date.toLocaleDateString(undefined, { month: 'short', timeZone: 'UTC' });
      const index = weeks.length;
      // Two labels over one week, or one over each of two neighboring weeks,
      // collide; the newer month keeps its place.
      const last = months[months.length - 1];
      if (last !== undefined && index - last.week < 2) months.pop();
      months.push({ week: index, label });
    }
    if (week.length === 7) {
      weeks.push(week);
      week = [];
    }
  }
  if (week.length > 0) {
    while (week.length < 7) week.push(null);
    weeks.push(week);
  }
  return { weeks, months, max, state };
}

/** Today as the collector's calendar names it. */
export function todayUTC(now: Date = new Date()): string {
  return now.toISOString().slice(0, 10);
}
