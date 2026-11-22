import { useCallback, useEffect, useRef, type Dispatch, type RefObject, type SetStateAction } from 'react';

import type { LogBuffer, LogLine } from './logBuffer';
import type { Marker } from './LogNotes';

/**
 * How long a reconnect's replayed tail can be: the server's default
 * `tailLines` (`internal/api/handlers_dock.go`), which this panel does not
 * override. Once this many lines are held the replay is complete.
 */
const REPLAY_TAIL = 2_000;
/** A replay that pauses this long is over: the tail arrives in one burst. */
const REPLAY_QUIET_MS = 300;
/** A reconnect that has replayed nothing after this long replays nothing. */
const REPLAY_WAIT_MS = 5_000;

/**
 * A reconnect's replayed tail, and the throttled repaint that also times it.
 *
 * The tail a reconnect is replaying is held back until it can be lined up with
 * what the buffer already has (`LogBuffer.appendReplay`). Nothing is held
 * outside a reconnect. It is resolved when the tail is complete (`REPLAY_TAIL`
 * lines), when the stream goes quiet for `REPLAY_QUIET_MS`, or when it stops;
 * and given up after `REPLAY_WAIT_MS` with nothing arriving, so a reconnect
 * that replays nothing does not hold the first live line back.
 *
 * The repaint is throttled: a pod printing a thousand lines a second is a
 * thousand appends and roughly eight renders. **Only while the stream is
 * live.** An ended pane receives nothing, so a timer per ended pane would be a
 * wake-up every 120 ms for each of up to sixteen panes with nothing to paint.
 * Every way a pane ends also sets its marker, which renders what the buffer
 * holds; every way it comes back (`revive`, a container switch, the Time
 * toggle, a reconnect) is `live` again, which starts the timer.
 */
export function useLogReplay({
  buffer, bump, live, dirtyRef, generationRef, setMarker, resetPaging,
}: {
  buffer: LogBuffer;
  bump: Dispatch<SetStateAction<number>>;
  live: boolean;
  dirtyRef: RefObject<boolean>;
  generationRef: RefObject<number>;
  setMarker: Dispatch<SetStateAction<Marker | null>>;
  resetPaging: () => void;
}): {
  /** A scheduled reconnect is opening: hold what it replays. */
  readonly startReplay: () => void;
  /** Hold a line if a replay is running; `false` when there is none. */
  readonly holdLine: (line: LogLine) => boolean;
  /** Line the held tail up with the buffer, and stop holding. */
  readonly flushReplay: () => void;
  /** Forget the held tail without drawing it: the buffer starts again. */
  readonly dropReplay: () => void;
} {
  const replay = useRef<{ lines: LogLine[]; started: number; last: number } | null>(null);

  const flushReplay = useCallback(() => {
    const held = replay.current;
    replay.current = null;
    if (held === null || held.lines.length === 0) return;
    const overlap = buffer.appendReplay(held.lines);
    if (overlap === null) {
      // The re-read does not continue what the pane holds: the gap was longer
      // than the tail, or the log was rotated or replaced. Appending would put
      // an invisible seam in the middle of the pane, so the old lines go - and
      // the pane says so rather than letting the scrollback vanish unexplained.
      buffer.reset();
      generationRef.current += 1;
      for (const line of held.lines) buffer.push(line);
      resetPaging();
      setMarker({
        text:
          'Reconnected. The re-read tail did not continue the lines this panel held, ' +
          'so the earlier lines were discarded.',
        bad: false,
      });
    }
    dirtyRef.current = true;
  }, [buffer, dirtyRef, generationRef, resetPaging, setMarker]);

  useEffect(() => {
    if (!live) return undefined;
    const timer = setInterval(() => {
      const held = replay.current;
      if (held !== null) {
        const now = Date.now();
        if (held.lines.length > 0 && now - held.last >= REPLAY_QUIET_MS) flushReplay();
        else if (held.lines.length === 0 && now - held.started >= REPLAY_WAIT_MS) replay.current = null;
      }
      if (!dirtyRef.current) return;
      dirtyRef.current = false;
      bump((n) => n + 1);
    }, 120);
    return () => clearInterval(timer);
  }, [bump, dirtyRef, flushReplay, live]);

  const holdLine = useCallback((line: LogLine): boolean => {
    const held = replay.current;
    if (held === null) return false;
    held.lines.push(line);
    held.last = Date.now();
    if (held.lines.length >= REPLAY_TAIL) flushReplay();
    return true;
  }, [flushReplay]);

  const startReplay = useCallback(() => {
    const now = Date.now();
    replay.current = { lines: [], started: now, last: now };
  }, []);

  const dropReplay = useCallback(() => {
    replay.current = null;
  }, []);

  return { startReplay, holdLine, flushReplay, dropReplay };
}
