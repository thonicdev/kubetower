import { useEffect, useState, type ReactNode } from 'react';
import { Pill, focusRing } from '@kubetower/ui';

import { windowAge, type Note, type Bar as BarData, type Kind, type NodeResource, type PodTrack as PodTrackData, type Status } from './data';

/**
 * The drawing primitives - a container's bar, a node's resource bar and a
 * pod's track - as components that place what the server computed: every
 * width, every offset and every band arrives in the answer, and nothing here
 * rescales.
 *
 * Class strings are looked up whole, never built by interpolation: Tailwind
 * finds utilities by reading source text, so `bg-${status}` is a class in the
 * DOM and in no stylesheet.
 */

/** A severity as the console's pill tones. Over-provisioned is money and never
 *  an alarm, so it is the neutral pill. */
export const KIND_TONE: Readonly<Record<Kind, string>> = { danger: 'bad', warning: 'warn', overkill: 'muted' };

/** A suggestion's or a card's hairline down its left edge. */
export const KIND_EDGE: Readonly<Record<string, string>> = {
  danger: 'border-l-bad',
  warning: 'border-l-warn',
  overkill: 'border-l-muted',
};

/** A bar's fill. Over-provisioned is the accent on a container bar;
 *  `none` never reaches here (an unmeasured bar draws no fill). */
const BAR_FILL: Readonly<Record<string, string>> = {
  danger: 'bg-bad',
  warning: 'bg-warn',
  healthy: 'bg-ok',
  overkill: 'bg-accent',
};

const BAR_TEXT: Readonly<Record<string, string>> = {
  danger: 'text-bad',
  warning: 'text-warn',
  healthy: 'text-ok',
  overkill: 'text-accent',
};

/** A gauge's, a pod track's and a volume's band: the fourth band, nobody using
 *  it, is `--ka-over`, blue so that it never reads as fine and never as an alarm. */
export const BAND_FILL: Readonly<Record<string, string>> = {
  danger: 'bg-bad',
  warning: 'bg-warn',
  healthy: 'bg-ok',
  overkill: 'bg-ka-over',
};

export const BAND_TEXT: Readonly<Record<string, string>> = {
  danger: 'text-bad',
  warning: 'text-warn',
  healthy: 'text-ok',
  overkill: 'text-ka-over',
};

const percent = (v: number) => `${v}%`;

/**
 * One container's resource: the track is the ceiling, the fill is what one pod
 * uses, two marks say where the request sits and where the worst replica
 * reached. **An unmeasured bar draws no fill and says so**: a fill at zero would
 * make a container nothing reported on look like an idle one.
 */
export function Bar({ bar }: { bar: BarData }): ReactNode {
  return (
    <div data-bar={bar.label} data-measured={bar.measured} className="flex flex-col gap-1">
      <div className="flex items-baseline gap-2 text-xs">
        <span className="min-w-12 font-semibold">{bar.label}</span>
        {bar.measured ? (
          <>
            <span>
              {bar.mean} <span className="text-muted">avg/pod</span>
            </span>
            {bar.pct === null ? null : (
              <span className={`ml-auto tabular-nums ${BAR_TEXT[bar.status] ?? 'text-muted'}`}>{bar.pct}% of limit</span>
            )}
          </>
        ) : (
          <span className="text-muted">not measured</span>
        )}
      </div>
      <div className="relative h-2.5 rounded-full bg-white/10" title={`Track is the ${bar.scale}`}>
        {bar.measured ? (
          <div
            data-fill=""
            className={`h-full rounded-full ${BAR_FILL[bar.status] ?? 'bg-muted'}`}
            style={{ width: percent(bar.usePct) }}
          />
        ) : null}
        {bar.request === '' ? null : (
          <span
            data-mark="request"
            className="absolute -top-0.5 -ml-px h-3.5 w-0.5 bg-fg"
            style={{ left: percent(bar.reqPct) }}
            title={`Request: ${bar.request}`}
          />
        )}
        {bar.measured ? (
          <span
            data-mark="peak"
            className="absolute -top-0.5 -ml-px h-3.5 w-0.5 bg-muted opacity-75"
            style={{ left: percent(bar.peakPct) }}
            title={`Worst replica: ${bar.peak}`}
          />
        ) : null}
      </div>
      <div className="text-2xs tabular-nums text-muted">
        request {bar.request === '' ? 'none' : bar.request} · limit {bar.limit === '' ? 'none' : bar.limit}
        {bar.measured ? <> · peak {bar.peak}</> : null}
        {bar.headroom === '' ? null : (
          <span title="Limit minus the worst replica's usage — what is left at the worst moment observed">
            {' '}
            · {bar.headroom} headroom
          </span>
        )}
        {bar.unbounded ? (
          <span className="text-warn" title="Measured, with no limit at all: nothing stops this container taking the node's">
            {' '}
            · no limit
          </span>
        ) : null}
      </div>
    </div>
  );
}

/**
 * One resource of one node against what it has, in the node tab and on the
 * Nodes view alike: the pale fill is what the pods reserved, the solid one what
 * they use, the mark where their limits reach - at the end of the track when
 * they pass allocatable, which is said in red. **The figure sits beside the
 * track, never inside a shape**: a percentage drawn inside a ring overflows it
 * once it grows, and beside a bar it has room whatever its width.
 */
export function ResourceBar({ resource: r }: { resource: NodeResource }): ReactNode {
  const used = r.measured && r.usedPct !== null;
  return (
    <div data-node-resource={r.key} data-measured={r.measured} className="flex flex-col gap-1">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 text-xs">
        <span className="min-w-28 font-semibold">{r.label}</span>
        {r.noAllocatable ? (
          <span className="text-muted">the node declares no allocatable figure for it</span>
        ) : (
          <span className="text-muted">{r.allocatable} allocatable</span>
        )}
        <span className="ml-auto tabular-nums">
          requested {r.requested}
          {r.noAllocatable ? null : <span className="text-muted"> ({r.requestedPct}%)</span>}
          {' · '}
          <span className={r.limitedPct > 100 ? 'text-bad' : ''}>
            limits {r.limited}
            {r.noAllocatable ? null : ` (${r.limitedPct}%)`}
          </span>
          {' · '}
          {r.measured ? <>used {r.used}</> : <span className="text-muted">usage not measured</span>}
        </span>
      </div>
      {r.noAllocatable ? null : (
        <div className="flex items-center gap-2">
          <div className="relative h-2.5 min-w-0 flex-1 rounded-full bg-white/10" title={`Track is the node's allocatable ${r.label.toLowerCase()}`}>
            <div data-fill="requested" className="absolute inset-y-0 left-0 rounded-full bg-accent opacity-35" style={{ width: percent(r.requestBar) }} />
            {r.measured ? (
              <div data-fill="used" className={`absolute inset-y-0 left-0 rounded-full ${BAND_FILL[r.status] ?? 'bg-muted'}`} style={{ width: percent(r.useBar) }} />
            ) : null}
            <span
              data-mark="limits"
              className={`absolute -top-0.5 -ml-px h-3.5 w-0.5 ${r.limitedPct > 100 ? 'bg-bad' : 'bg-fg'}`}
              style={{ left: percent(r.limitBar) }}
              title={`Limits: ${r.limited}`}
            />
          </div>
          {/* Usage where it was measured, the reservation where it was not -
              muted and named, so the two never read alike. */}
          <span data-figure="" className={`w-24 shrink-0 text-right text-xs tabular-nums ${used ? 'font-semibold' : 'text-muted'}`}>
            {used ? `${r.usedPct}% used` : `${r.requestedPct}% reserved`}
          </span>
        </div>
      )}
      {r.coverage === '' && r.gap === '' ? null : (
        <span className="text-2xs text-muted">{[r.coverage, r.gap].filter((t) => t !== '').join(' · ')}</span>
      )}
    </div>
  );
}

/**
 * One pod's share of one node: the pale fill is what it reserved, the solid one
 * what it is doing. The figure beside it is the measurement where there is one
 * and the reservation where there is not, muted so the two never read alike.
 */
export function PodTrack({ track }: { track: PodTrackData }): ReactNode {
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      <span className="relative block h-1.5 min-w-0 flex-1 rounded-full bg-white/10" title={track.title}>
        <span className="absolute inset-y-0 left-0 rounded-full bg-accent opacity-30" style={{ width: percent(track.reqPct) }} />
        {track.measured ? (
          <span
            data-use=""
            className={`absolute inset-y-0 left-0 rounded-full ${BAND_FILL[track.status] ?? 'bg-muted'}`}
            style={{ width: percent(track.usePct) }}
          />
        ) : null}
      </span>
      <span
        data-measured={track.measured}
        className={`w-8 shrink-0 text-right text-2xs tabular-nums ${track.measured ? 'text-fg' : 'text-muted'}`}
      >
        {track.pct}
      </span>
    </span>
  );
}

/** A status as its band's text color, for a figure. */
export function bandText(status: Status | ''): string {
  return BAND_TEXT[status] ?? 'text-muted';
}

/**
 * What the answer was built from, always - in one line, each source a pill,
 * with every sentence behind it a click away. What failed is spelled out
 * under the line, because that is the part that changes what the page can
 * say; what worked does not need a paragraph each time.
 */
export function Sources({
  notes,
  partial = '',
  windowReadAt,
}: {
  notes: Note[];
  partial?: string;
  /** When the usage history was read: said in words once it is a minute old. */
  windowReadAt?: string | undefined;
}): ReactNode {
  const failed = notes.filter((n) => !n.ok);
  const now = useMinuteClock(windowReadAt !== undefined && windowReadAt !== '');
  const age = windowAge(windowReadAt, now);
  return (
    <section aria-label="What this page was built from" data-sources="" className="flex flex-col gap-1">
      <details className="text-xs text-muted">
        <summary className={`flex cursor-pointer flex-wrap items-center gap-1.5 ${focusRing}`}>
          <span>Built from</span>
          {notes.map((n) => (
            <Pill key={n.label} {...(n.ok ? { neutral: 'muted' as const } : { tone: 'warn' })}>
              {n.label}
            </Pill>
          ))}
        </summary>
        <ul className="mt-1.5 flex flex-col gap-1">
          {notes.map((note) => (
            <li key={note.label} data-note={note.label} data-ok={note.ok} className="flex min-w-0 items-baseline gap-1.5">
              <span className={`font-semibold whitespace-nowrap ${note.ok ? 'text-fg' : 'text-warn'}`}>{note.label}</span>
              <span className="min-w-0 break-words">{note.detail}</span>
            </li>
          ))}
        </ul>
      </details>
      {age === '' ? null : (
        <p data-window-age="" className="text-xs text-muted">
          {age}
        </p>
      )}
      {partial === '' ? null : (
        <p data-partial="" className="text-xs break-words text-warn">
          <span className="font-semibold">Partial:</span> {partial}
        </p>
      )}
      {failed.map((note) => (
        <p key={note.label} data-note-failed={note.label} className="text-xs break-words text-warn">
          <span className="font-semibold">{note.label}:</span> {note.detail}
        </p>
      ))}
    </section>
  );
}

/** Now, moved on every half minute while on, so an age said in minutes keeps up. */
function useMinuteClock(on: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!on) return undefined;
    // Read again at once: the time this was mounted may be long past when a
    // read time first arrives.
    const first = setTimeout(() => {
      setNow(Date.now());
    }, 0);
    const timer = setInterval(() => {
      setNow(Date.now());
    }, 30_000);
    return () => {
      clearTimeout(first);
      clearInterval(timer);
    };
  }, [on]);
  return now;
}
