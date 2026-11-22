import type { ReactNode } from 'react';

import { btnSecondary } from '../components/buttons';

/** The clock part of an RFC 3339 instant, or the text as it came. */
function clock(at: string): string {
  const match = /T(\d\d:\d\d:\d\d)/.exec(at);
  return match?.[1] ?? at;
}

/**
 * The foot of the log panel: whether it follows, how much it holds, and the
 * way back to the newest line. Labels only; it re-renders with the panel's
 * throttled repaint and holds no per-line state.
 */
export function LogStatusBar({
  following, ended, fresh, lines, first, last, onJump,
}: {
  following: boolean;
  /** The stream has finished: nothing is being followed. */
  ended: boolean;
  /** Lines that arrived since the reader left the newest one. */
  fresh: number;
  /** Lines the panel shows. */
  lines: number;
  /** Instants of the oldest and newest held line, when stamped. */
  first?: string | undefined;
  last?: string | undefined;
  /** `null` while the view is at the newest line. */
  onJump: (() => void) | null;
}): ReactNode {
  const span =
    first !== undefined && last !== undefined ? `${clock(first)}–${clock(last)}` : null;
  return (
    <div
      className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-line px-2 py-1 text-xs text-muted"
    >
      {following && !ended ? (
        <span className="flex items-center gap-1 text-fg">
          <span aria-hidden className="size-1.5 rounded-full bg-accent" />
          Following
        </span>
      ) : (
        <span className="text-fg">{ended && following ? 'Ended' : 'Paused'}</span>
      )}
      {fresh > 0 && (
        <span className="text-accent">+{fresh.toLocaleString('en-US')} new</span>
      )}
      <span>{lines.toLocaleString('en-US')} {lines === 1 ? 'line' : 'lines'}</span>
      {span !== null && <span className="tabular-nums">{span}</span>}
      {onJump !== null && (
        <button type="button" className={`${btnSecondary} ml-auto`} onClick={onJump}>
          ↓ Jump to latest
        </button>
      )}
    </div>
  );
}
