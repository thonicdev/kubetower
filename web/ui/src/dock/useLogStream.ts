import { useCallback, useEffect, useRef, useState, type Dispatch, type RefObject, type SetStateAction } from 'react';

import { openLogStream, type LogEvent, type LogRetry } from '../api';
import { useLiveHandle } from '../live/useLiveHandle';
import type { DockApi } from './DockContext';
import type { LogBuffer, LogLine } from './logBuffer';
import type { Marker } from './LogNotes';
import type { Tab } from './tabs';
import { useLogReplay } from './useLogReplay';

/** Which container's log a pane reads, and from which run. */
export interface LogTarget {
  readonly cluster: string;
  readonly namespace: string;
  readonly name: string;
  readonly container: string;
  readonly previous: boolean;
}

/**
 * A log pane's stream: opened, numbered, bound to the pane, reconnected, and
 * the sentence below the buffer that says what became of it.
 *
 * The stream is held, not opened: `useLiveHandle` keys it by the tab, so React
 * 19's StrictMode double-invoke finds the first stream rather than taking a
 * second of the server's sixteen follow slots. **The key contains the tab's
 * sequence number**, or two log panels on one pod collapse into one.
 *
 * `onNewRead` is called **during render** whenever the key changes, so the
 * pane can drop what it had learned about the old read in the same pass.
 */
export function useLogStream({
  tab, target, timestamps, buffer, bump, dispatch, dirtyRef, generationRef, resetPaging, onNewRead,
}: {
  tab: Tab;
  target: LogTarget;
  timestamps: boolean;
  buffer: LogBuffer;
  bump: Dispatch<SetStateAction<number>>;
  dispatch: DockApi['dispatch'];
  dirtyRef: RefObject<boolean>;
  generationRef: RefObject<number>;
  resetPaging: () => void;
  onNewRead: () => void;
}): { readonly marker: Marker | null } {
  const [marker, setMarker] = useState<Marker | null>(null);
  const { cluster, namespace, name, container, previous } = target;

  /**
   * Whether the marker on screen is a waiting notice - a follow opened on a
   * container that has not started yet. The first line
   * takes it down, since a pane that keeps saying "waiting for the container to
   * start" over the container's own output reads as a notice nobody updated.
   *
   * A ref beside the marker rather than a read of it, because the arrival path
   * runs once per line and must not render per line (see `LogPane`): it reads
   * this, and only the one line that ends the wait calls `setMarker`.
   */
  const waitingNotice = useRef(false);

  const { startReplay, holdLine, flushReplay, dropReplay } = useLogReplay({
    buffer, bump, live: tab.live, dirtyRef, generationRef, setMarker, resetPaging,
  });

  const onEvent = useCallback((event: LogEvent) => {
    if (event.type === 'line') {
      if (waitingNotice.current) {
        // The container started and this is its first line: the wait is over.
        waitingNotice.current = false;
        setMarker(null);
      }
      const line: LogLine = {
        text: event.text ?? '',
        ...(event.at === undefined ? {} : { at: event.at }),
        ...(event.container === undefined ? {} : { container: event.container }),
      };
      if (holdLine(line)) return;
      buffer.push(line);
      dirtyRef.current = true;
      return;
    }
    // Anything but a line means the replay, if one was running, is complete.
    flushReplay();
    if (event.type === 'state') {
      // The marker is a **separate element below the buffer**, never a line
      // pushed into it. A marker pushed in as a line would be hidden by the next
      // filter that does not happen to match it, and would land in the middle of
      // a downloaded file as though the container had printed it.
      if (event.state === 'pending') {
        // **Not a stop.** The follow is waiting for its container to start and
        // the lines come next on this same stream, so it
        // is drawn as a notice and not as a warning, the tab stays live, and
        // the first line takes it down. A later `pending` - the reason moved
        // from `PodInitializing` to `ContainerCreating` - replaces it.
        waitingNotice.current = true;
        setMarker({
          text: event.reason?.message ?? 'waiting for the container to start',
          bad: false,
          waiting: true,
        });
        return;
      }
      waitingNotice.current = false;
      setMarker({ text: event.reason?.message ?? `the stream stopped: ${event.state ?? ''}`, bad: true });
      return;
    }
    // `end`. `atStart` says the read reached the beginning of the log the
    // cluster still holds - which is **not** the beginning of time, because the
    // kubelet rotates. That wording is this side's to get right now, and the Go
    // test that enforced it by grepping Go strings has moved with it.
    // A waiting notice is not a reason for the end, and is replaced by one.
    setMarker((was) =>
      was !== null && was.waiting !== true ? was : {
        text:
          event.atStart === true
            ? 'End of log. This is as far back as the kubelet has kept — earlier lines, ' +
              'if there were any, have been rotated away.'
            : 'End of log.',
        bad: false,
      },
    );
    dispatch({ type: 'ended', id: tab.id });
  }, [buffer, dirtyRef, dispatch, flushReplay, holdLine, tab.id]);

  const onBroken = useCallback((retry: LogRetry | null) => {
    // The browser's own error carries nothing - no status, no body, not even
    // whether the server was reached. So the sentence names what is knowable and
    // does not claim a cause.
    //
    // Three answers and not one. An `end` never comes here:
    // the stream closed itself on it. A drop with a reconnect ahead says when.
    // A drop with nothing ahead says the connection went, and only overwrites a
    // marker that was itself a reconnect notice: a `state` frame's sentence is
    // the server having said why, and it stays.
    flushReplay();
    if (retry !== null) {
      const seconds = Math.max(1, Math.round(retry.inMs / 1000));
      setMarker({
        text:
          `The connection to the stream went away. Reconnecting in ${seconds} s ` +
          `(attempt ${retry.attempt} of ${retry.of}).`,
        bad: true,
        retrying: true,
      });
    } else {
      // A waiting notice goes too: the stream it said was waiting is gone.
      setMarker((was) =>
        was !== null && was.retrying !== true && was.waiting !== true
          ? was
          : { text: 'The connection to the stream went away.', bad: true });
    }
    dispatch({ type: 'ended', id: tab.id });
  }, [dispatch, flushReplay, tab.id]);

  // A scheduled reconnect is opening. **The new request replays the tail from
  // the start**, and appending it as it came would duplicate it. Nor is the
  // buffer emptied: that would throw away the
  // scrollback the reader was in, and the filter's place in it. The replay is
  // held back and lined up with what is already here instead (`flushReplay`).
  const onReopen = useCallback(() => {
    startReplay();
    waitingNotice.current = false;
    setMarker(null);
    dispatch({ type: 'revive', id: tab.id });
  }, [dispatch, startReplay, tab.id]);

  // Waiting for one of the page's log connections: drawn
  // as a notice, like a container that has not started, and taken down the
  // same way - by the first frame the stream then receives.
  const onQueued = useCallback((why: string) => {
    waitingNotice.current = true;
    setMarker({ text: why, bad: false, waiting: true });
  }, []);

  // The key is the identity of the *stream*, so changing container, previous or
  // timestamps closes the old one and opens a new one - which is what those
  // three do, and why the Time button has to say so. `tab.seq` keeps two panels
  // on one pod apart, or the deduplication defeats the counter.
  //
  // **And every re-open is a new read, numbered.** Without the number, a
  // toggle pressed back inside the two-second grace period of
  // `live/handles.ts` produces a key that is still held - Time on and off
  // again, Prev run on and off again - and the pane re-attaches to the read it
  // has just left instead of asking again: no request, and no `open`, which is
  // the one place the buffer is emptied, so what the other read put in the
  // buffer stays in front of the stream, duplicated. The tooltip says *"the buffer starts again from
  // the tail"*; the number is what makes that true. The read left behind still
  // lingers out its grace period, silenced by `mine()` below.
  const stream = `${cluster}/${namespace}/${name}:${container}:${previous}:${timestamps}`;
  const [read, setRead] = useState({ stream, n: 0 });
  const readNumber = read.stream === stream ? read.n : read.n + 1;
  if (read.stream !== stream) setRead({ stream, n: readNumber });
  const key = `logs:${tab.seq}:${readNumber}:${stream}`;

  // The handlers are read through a ref so that neither is in the handle's
  // dependency list: both are new functions on some renders, and depending on
  // them would close and reopen the stream - taking a second of the server's
  // sixteen follow slots each time. The key is the dependency, because the key
  // is the identity. `useLiveHandle` does the same with its own two callbacks.
  const latest = useRef({ onEvent, onBroken, onReopen, onQueued });
  useEffect(() => {
    latest.current = { onEvent, onBroken, onReopen, onQueued };
  });

  /**
   * Which stream this pane is listening to, and whether it is mounted at all.
   *
   * `live/handles.ts` keeps a released handle alive for a moment (StrictMode's
   * second mount must find it), so **the old stream outlives a key change**: a
   * container switch, the Time toggle, a closed tab. Its callbacks would still
   * reach `latest` - which by then is the *new* stream's pane state - and a
   * reconnect it had scheduled would pour the old container's tail into the new
   * container's buffer. So every callback is bound to the key it was opened for
   * and goes quiet once that is not the key being shown; and a reconnect asked
   * for by a stream nobody is listening to is canceled rather than made.
   */
  const bound = useRef({ key: '', mounted: false });

  // A new stream is a new buffer, so everything the old one had learned about
  // looking backwards goes with it. Without this, switching container carries a
  // `beginning` from the previous container's log across, and the pane says
  // there is nothing older about a log it has not looked at.
  //
  // **Adjusted during render rather than in an effect**, which is React's own
  // answer for state that derives from a prop: an effect would paint the old
  // pane's notice once with the new pane's lines, and `react-hooks/set-state-in-effect`
  // refuses it outright. React re-runs this component immediately and commits
  // nothing in between, so there is no flash and no cascading render.
  const [streamKey, setStreamKey] = useState(key);
  if (streamKey !== key) {
    setStreamKey(key);
    // The paging state, and a selection over the old lines (see `LogPane`).
    onNewRead();
    // The old stream's "End of log" is not a sentence about the new one.
    setMarker(null);
  }

  // Declared before `useLiveHandle` so that they run first: the handle's own
  // effect opens the new stream, and by then `bound` must already name it.
  useEffect(() => {
    bound.current.key = key;
  });
  useEffect(() => {
    const flags = bound.current;
    flags.mounted = true;
    return () => {
      flags.mounted = false;
    };
  }, []);

  useLiveHandle(key, () => {
    buffer.reset();
    generationRef.current += 1;
    dropReplay();
    waitingNotice.current = false;
    const opened = key;
    const mine = (): boolean => bound.current.mounted && bound.current.key === opened;
    const stream = openLogStream(
      {
        cluster, kind: 'pods', namespace, name,
        ...(container === '' ? {} : { container }),
        follow: !previous, previous, timestamps,
      },
      {
        onEvent: (event) => {
          if (mine()) latest.current.onEvent(event);
        },
        onBroken: (retry) => {
          if (mine()) latest.current.onBroken(retry);
        },
        onReopen: () => {
          if (!mine()) return false;
          latest.current.onReopen();
          return true;
        },
        onQueued: (why) => {
          if (mine()) latest.current.onQueued(why);
        },
      },
    );
    return { value: stream, dispose: () => stream.close() };
  });

  return { marker };
}
