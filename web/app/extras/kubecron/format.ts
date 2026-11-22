import { formatInstant, shortDuration } from '@kubetower/ui';

import type { Verdict } from './data';

/** A duration in milliseconds, to one or two units: 450ms, 27s, 1m40s, 3h05m, 2d4h. */
export function duration(ms: number): string {
  if (ms < 1000) return `${Math.max(0, Math.round(ms))}ms`;
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return s % 60 === 0 ? `${m}m` : `${m}m${String(s % 60).padStart(2, '0')}s`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h${String(m % 60).padStart(2, '0')}m`;
  return `${Math.floor(h / 24)}d${h % 24}h`;
}

/** How far a moment is from now, on the console's scale: "in 3m", "2h ago". */
export function relative(iso: string, now: number = Date.now()): string {
  const ms = Date.parse(iso) - now;
  const text = shortDuration(Math.abs(ms));
  return ms >= 0 ? `in ${text}` : `${text} ago`;
}

/** A moment as the console writes one: `Mar 12, 02:15`, in the reader's zone. */
export function moment(iso: string): string {
  return formatInstant(iso);
}

/** The calendar day of an instant in a zone, YYYY-MM-DD; the reader's own
 *  zone unless one is named. */
export function zoneDay(at: Date, timeZone?: string): string {
  return new Intl.DateTimeFormat('en-CA', { year: 'numeric', month: '2-digit', day: '2-digit', ...(timeZone === undefined ? {} : { timeZone }) }).format(at);
}

/**
 * A run's moment in a list of one UTC day's runs: the reader's own clock, and
 * **its date too whenever that is not the day the list names**. The collector
 * files runs by UTC day, so a run at 00:30 in Paris sits under the day before;
 * drawn as "00:30" alone under that day, it reads as a run from the wrong
 * night. Outside a day's list it is `moment`.
 */
export function momentOn(iso: string, day: string | undefined, timeZone?: string): string {
  if (day === undefined) return moment(iso);
  const at = new Date(iso);
  // Intl throws on an invalid instant: say what the collector sent instead.
  if (Number.isNaN(at.getTime())) return iso;
  const zone = timeZone === undefined ? {} : { timeZone };
  const time = new Intl.DateTimeFormat('en', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23', ...zone }).format(at);
  if (zoneDay(at, timeZone) === day) return time;
  return `${new Intl.DateTimeFormat('en', { day: 'numeric', month: 'short', ...zone }).format(at)}, ${time}`;
}

export function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ['KiB', 'MiB', 'GiB', 'TiB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

/**
 * The four verdicts, in words and in the console's tones. **Four and not
 * three**: "cannot say" has its own word and color, because folding it into on
 * time hides a broken schedule and folding it into missed pages somebody about a
 * typo in a time zone.
 */
export const VERDICT: Readonly<Record<Verdict, { word: string; tone: string; title: string }>> = {
  ok: { word: 'On time', tone: 'ok', title: 'The last occurrence that was due has fired.' },
  missed: { word: 'Missed', tone: 'bad', title: 'An occurrence was due and the controller did not fire it.' },
  suspended: { word: 'Suspended', tone: '', title: 'spec.suspend is true: a decision somebody made, not a fault.' },
  unknown: { word: 'Cannot say', tone: 'warn', title: 'The schedule could not be evaluated; the reason is beside it.' },
};
