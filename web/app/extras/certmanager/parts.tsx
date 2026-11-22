import type { ReactNode } from 'react';
import { Failure, btnSecondary } from '@kubetower/ui';

import { CertManagerProblem, daysWord, plural, type Band, type Certificate } from './data';

/**
 * The drawings of the Cert Manager page, hand-made: a heat strip, a ring and a
 * bar. Every color is a theme token through a utility, every number the
 * server's.
 */

/** A band's fill, written out whole so Tailwind finds each class. */
export const BAND_FILL: Readonly<Record<Band, string>> = {
  expired: 'bg-bad',
  week: 'bg-bad',
  month: 'bg-warn',
  later: 'bg-ok',
  none: 'bg-muted',
};

export const BAND_TEXT: Readonly<Record<Band, string>> = {
  expired: 'text-bad',
  week: 'text-bad',
  month: 'text-warn',
  later: 'text-ok',
  none: 'text-muted',
};

/** The tone a pill takes for a band: the console's `ok`, `warn`, `bad`. */
export const BAND_TONE: Readonly<Record<Band, string>> = {
  expired: 'bad',
  week: 'bad',
  month: 'warn',
  later: 'ok',
  none: 'neutral',
};

/** A count's weight on the strip: four steps of the same fill. */
const WEIGHT = ['opacity-60', 'opacity-75', 'opacity-90', 'opacity-100'] as const;

/** Where the strip's scale is labeled, by day. */
const MARKS = [
  { day: 0, label: 'Today' },
  { day: 7, label: '+7 d' },
  { day: 30, label: '+30 d' },
  { day: 60, label: '+60 d' },
  { day: 90, label: '+90 d' },
] as const;

function weight(n: number, most: number): string {
  if (most <= 1) return WEIGHT[3];
  return WEIGHT[Math.min(3, Math.floor(((n - 1) / most) * 4))] ?? WEIGHT[3];
}

function dayFill(day: number): string {
  return day < 7 ? 'bg-bad' : day < 30 ? 'bg-warn' : 'bg-accent';
}

/**
 * The next 90 days, one cell a day, tinted by how many certificates expire on
 * it: red the first week, amber to 30 days, blue after. A day nothing expires
 * on is an empty cell, never a zero drawn as a color. A reading, not a
 * control: the tiles above it filter.
 */
export function ExpiryStrip({ strip, now }: { strip: readonly number[]; now: string }): ReactNode {
  const most = Math.max(0, ...strip);
  const start = Date.parse(now);
  const total = strip.reduce((a, b) => a + b, 0);
  return (
    <figure data-strip="" className="flex min-w-0 flex-col gap-1">
      <figcaption className="text-2xs text-muted">
        Next {String(strip.length)} days · {String(total)}
      </figcaption>
      <div role="list" aria-label="Certificates expiring per day" className="flex h-8 min-w-0 items-stretch gap-px">
        {strip.map((n, day) => {
          const date = Number.isNaN(start) ? '' : new Date(start + day * 86_400_000).toISOString().slice(0, 10);
          const label = `${date === '' ? `day ${String(day)}` : date}: ${n === 0 ? 'nothing expires' : `${plural(n, 'certificate')} expire`}`;
          return (
            <div
              key={day}
              role="listitem"
              title={label}
              aria-label={label}
              data-day={String(day)}
              data-count={String(n)}
              className={`min-w-0 flex-1 rounded-xs ${n === 0 ? 'bg-panel-2' : `${dayFill(day)} ${weight(n, most)}`} ${
                day === 7 || day === 30 ? 'ml-0.5' : ''
              }`}
            />
          );
        })}
      </div>
      <div className="relative h-4 text-2xs text-muted" aria-hidden="true">
        {MARKS.filter((m) => m.day <= strip.length).map((m) => (
          <span
            key={m.day}
            className={`absolute top-0 whitespace-nowrap ${m.day === 0 ? '' : m.day === strip.length ? '-translate-x-full' : '-translate-x-1/2'}`}
            style={{ left: `${String((m.day / strip.length) * 100)}%` }}
          >
            {m.label}
          </span>
        ))}
      </div>
    </figure>
  );
}

/**
 * The Ready states as a ring: ready, not ready, unknown, with the share of
 * ready in the middle. A cluster with no certificate draws an empty ring.
 */
export function ReadyRing({ ready, notReady, unknown }: { ready: number; notReady: number; unknown: number }): ReactNode {
  const total = ready + notReady + unknown;
  const parts = [
    { n: ready, stroke: 'stroke-ok', word: 'ready' },
    { n: notReady, stroke: 'stroke-bad', word: 'not ready' },
    { n: unknown, stroke: 'stroke-muted', word: 'unknown' },
  ];
  let offset = 25; // start at twelve o'clock
  const share = total === 0 ? 0 : Math.round((ready / total) * 100);
  const label = total === 0 ? 'No certificate' : parts.map((p) => `${String(p.n)} ${p.word}`).join(', ');
  return (
    <figure data-ring="" className="flex items-center gap-3">
      <svg viewBox="0 0 36 36" role="img" aria-label={label} className="h-20 w-20 shrink-0">
        <circle cx="18" cy="18" r="15.915" fill="none" strokeWidth="4" className="stroke-panel-2" />
        {total === 0
          ? null
          : parts.map((p) => {
              if (p.n === 0) return null;
              const length = (p.n / total) * 100;
              const dash = `${String(length)} ${String(100 - length)}`;
              const at = offset;
              offset = (offset - length + 100) % 100;
              return (
                <circle
                  key={p.word}
                  data-part={p.word}
                  cx="18"
                  cy="18"
                  r="15.915"
                  fill="none"
                  strokeWidth="4"
                  strokeDasharray={dash}
                  strokeDashoffset={String(at)}
                  className={p.stroke}
                />
              );
            })}
        <text x="18" y="20.5" textAnchor="middle" fontSize="8" className="fill-fg font-medium">
          {total === 0 ? '–' : `${String(share)}%`}
        </text>
      </svg>
      <figcaption className="flex flex-col gap-0.5 text-xs">
        <span className="text-2xs text-muted">Ready</span>
        <span>
          <span className="text-ok">{String(ready)}</span> ready
        </span>
        <span>
          <span className={notReady === 0 ? 'text-muted' : 'text-bad'}>{String(notReady)}</span> not ready
        </span>
        {unknown === 0 ? null : (
          <span>
            <span className="text-muted">{String(unknown)}</span> unknown
          </span>
        )}
      </figcaption>
    </figure>
  );
}

/** How far ahead the bar's scale reaches: the strip's 90 days. */
export const BAR_DAYS = 90;

/**
 * Days left as a bar over 90 days: full at 90 or more, empty at expiry; a
 * certificate never issued draws a dashed empty track.
 */
export function DaysBar({ c }: { c: Pick<Certificate, 'daysLeft' | 'band'> }): ReactNode {
  const pct = c.daysLeft === null ? 0 : Math.max(0, Math.min(100, (c.daysLeft / BAR_DAYS) * 100));
  return (
    <div className="flex min-w-0 items-center gap-2" data-days={c.daysLeft === null ? 'none' : String(c.daysLeft)}>
      <div
        className={`relative h-1.5 w-24 shrink-0 overflow-hidden rounded-full ${
          c.daysLeft === null ? 'border border-dashed border-line' : 'bg-panel-2'
        }`}
        aria-hidden="true"
      >
        {c.band === 'expired' ? (
          <div className="absolute inset-0 bg-bad opacity-40" />
        ) : (
          <div className={`absolute inset-y-0 left-0 rounded-full ${BAND_FILL[c.band]}`} style={{ width: `${String(pct)}%` }} />
        )}
      </div>
      <span className={`truncate text-xs ${BAND_TEXT[c.band]}`}>{daysWord(c)}</span>
    </div>
  );
}

/** A refusal is said as one, a failure as one; the gate's sentence as is. */
export function ProblemNotice({ error, onRetry }: { error: unknown; onRetry?: () => void }): ReactNode {
  if (!(error instanceof CertManagerProblem)) return <Failure error={error} onRetry={onRetry} />;
  const refused = error.refused || error.problem === 'gate';
  return (
    <div role="status" data-problem={error.problem} className="flex flex-col gap-1 rounded-lg border border-line bg-panel-2 p-3 text-sm">
      <p className={`font-medium ${refused ? 'text-warn' : 'text-bad'}`}>{refused ? 'Refused' : 'Could not read'}</p>
      <p title={error.detail}>{error.message}</p>
      {onRetry === undefined || refused ? null : (
        <button type="button" onClick={onRetry} className={`self-start ${btnSecondary}`}>
          Try again
        </button>
      )}
    </div>
  );
}
