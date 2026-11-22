import { useQuery } from '@tanstack/react-query';
import { useMemo, useRef, useState, type ReactNode, type UIEvent } from 'react';

import { listLogContainers } from '../api';
import { useDock } from './DockContext';
import { LOG_CHUNK, compileFilter, type LogLine } from './logBuffer';
import { downloadLog } from './logDownload';
import { Backward, HeldBack, MarkerLine } from './LogNotes';
import { LogRow, rowKey } from './LogRow';
import { LogStatusBar } from './LogStatusBar';
import { LogToolbar } from './LogToolbar';
import type { Tab } from './tabs';
import { useLogBuffer } from './useLogBuffer';
import { useLogFind } from './useLogFind';
import { useLogPaging } from './useLogPaging';
import { useLogScroll } from './useLogScroll';
import { useLogSelection } from './useLogSelection';
import { useLogStream, type LogTarget } from './useLogStream';
import { useLogView } from './useLogView';

/**
 * A container's log, followed.
 *
 * ## The one thing this component must not do
 *
 * **Re-render per arriving line.** The buffer is a `LogBuffer` held in a ref and
 * mutated by the stream; the component re-renders on a throttle. A state update
 * per line is a render per line - milliseconds each, seconds for a few
 * thousand lines. `logBuffer.ts` carries the reasoning.
 *
 * ## The stream is held, not opened
 *
 * `useLiveHandle` keys the stream by the tab, so React 19's StrictMode
 * double-invoke finds the first stream rather than taking a second of the
 * server's sixteen follow slots. **The key contains the tab's sequence number**,
 * or two log panels on one pod collapse into one - which is the deduplication
 * defeating the counter that exists to keep them apart.
 *
 * ## Looking backwards is four states, and only one of them is an ending
 *
 * `back` is `{busy, stop, note, fail}` and the four are kept apart deliberately.
 * **`busy` always resolves into one of the others** - a spinner that ends in
 * nothing is the worst of the four. `fail` is kept separate from `stop` because
 * a failed fetch can be retried and a stop cannot, and because "the cluster did
 * not answer" and "there is nothing earlier" are different answers. `stop` is
 * itself three - `beginning`, `ceiling`, `dense` - and the server writes the
 * sentence for each, so curl and this panel say the same words.
 *
 * The trigger is **both** a scroll to the top and a button, because a pane
 * showing fewer lines than fit it has no scrollbar: there is no "scroll to the
 * top" to perform, and a gesture-only trigger would simply not exist on a quiet
 * pod.
 *
 * ## Ended is not closed
 *
 * When the stream finishes the tab grays out and stops being counted, and keeps
 * every line: the scrollback is the reason it was opened. Changing container or
 * turning timestamps on **revives** it, because it is following again - one
 * line, and the kind of line a port drops because `ended` reads like a terminal
 * state.
 *
 * ## A selection holds the pane still
 *
 * Copying is the browser's own: a selection, Ctrl+C or ⌘C, and nothing here
 * listens for either. So it needs no clipboard API, and over plain HTTP, where
 * `navigator.clipboard` is undefined, there is nothing to degrade. What it does
 * need is for the selected rows to stay in the document until the key is
 * pressed, and on a following pane that is not free: the window is the newest
 * `LOG_WINDOW` rows, so a busy pod would slide the selected rows out of it and
 * take them out of the document with the selection on them; on a slower
 * stream the pinning would scroll the selection out of sight first, and a drag
 * still in progress would grow over lines that move under the pointer.
 *
 * So while a selection is inside the pane, the window ends at the line that
 * was newest when it began, and the pane does not scroll. Lines go on arriving
 * into the buffer, and a sentence below the rows says how many are held back,
 * because a pane that silently stops moving reads as a stream that ended. Let
 * go of the selection and it draws them and follows again.
 *
 * Rows are keyed by their line rather than by their index for the same reason:
 * a page backwards or a trim moves every index, and a row keyed by its index is
 * then handed another line's text - under a selection, text nobody chose.
 */
export function LogPane({ tab }: { tab: Tab }): ReactNode {
  const { dispatch } = useDock();
  // The buffer, read during render and mutated by the stream (`useLogBuffer`).
  const { buffer, bump } = useLogBuffer();
  const { timestamps, wrap, json, toggleTimestamps, toggleWrap, toggleJSON } = useLogView();
  // The find box (see `useLogFind`).
  const { typed, query, regex, matchCase, at, settleQuery, setAt, onQuery, onRegex, onMatchCase } =
    useLogFind();
  const host = useRef<HTMLDivElement>(null);
  /** Whether the reader is at the newest line: remembered, never measured on
   *  arrival (see `useLogScroll`). */
  const pinned = useRef(true);
  /** The distance to the bottom before the window grew upward, for the scroll
   *  compensation after it has (see `useLogScroll`). */
  const grew = useRef<number | null>(null);
  /** Lines arrived since the last repaint, which is throttled (`useLogReplay`). */
  const dirty = useRef(false);
  /**
   * Which read the buffer belongs to. Bumped whenever the buffer is emptied for
   * a different read, so that a page backwards still in flight from the old one
   * is dropped on arrival instead of being prepended to lines it was not
   * counted against.
   */
  const generation = useRef(0);
  /**
   * The newest line drawn when a selection inside the pane began, or `null`
   * while nothing in it is selected. The window ends there while it is held
   * (see the header). State and not a ref, because the render reads it.
   */
  const [held, setHeld] = useState<LogLine | null>(null);
  /**
   * Set while the reader is off the newest line: the arrival count when they
   * left it, so the status bar can say how many came since. A mirror of
   * `pinned` for the render, written only when that flips (`onScroll`).
   */
  const [paused, setPaused] = useState<{ mark: number } | null>(null);

  const target: LogTarget = {
    cluster: tab.cluster,
    namespace: tab.namespace ?? '',
    name: tab.name ?? tab.label,
    container: tab.container ?? '',
    previous: tab.previous ?? false,
  };
  const { cluster, namespace, name, container, previous } = target;

  // The switcher's list. **Its failure does not block the stream**: the server
  // picks a default container on its own, and a switcher we could not draw must
  // not cost the logs themselves.
  const containers = useQuery({
    queryKey: ['logContainers', cluster, namespace, name],
    queryFn: ({ signal }) =>
      listLogContainers({ cluster, kind: 'pods', namespace, name }, signal),
    retry: false,
  });

  const { windowSize, setWindowSize, back, resetPaging, loadEarlier } = useLogPaging({
    buffer, target, timestamps,
    hostRef: host, pinnedRef: pinned, grewRef: grew, dirtyRef: dirty, generationRef: generation,
  });

  const { marker } = useLogStream({
    tab, target, timestamps, buffer, bump, dispatch,
    dirtyRef: dirty, generationRef: generation, resetPaging,
    // A new read: nothing learned about looking backwards in the old one
    // holds, nor is a selection over its lines a reason to hold the new one
    // still.
    onNewRead: () => {
      resetPaging();
      setHeld(null);
    },
  });

  // --- The view --------------------------------------------------------------

  const filter = useMemo(() => compileFilter(query, regex, matchCase), [query, regex, matchCase]);

  // Read during render, deliberately. `LogBuffer.view` is a cache and applying a
  // filter is idempotent, so this is a derived value and not a side effect - and
  // the alternative, an effect that writes the result into state, is a cascading
  // render on every keystroke of a filter box over a hundred thousand lines.
  const shown = buffer.view(filter.pattern, json);
  // The decoded copies worth keeping are the rows drawn, and a chunk of growth
  // above them; the buffer lets go of the rest.
  buffer.keepDecoded(windowSize + LOG_CHUNK);
  // Where the window ends: the newest line, or, while a selection is held, the
  // line that was newest when it began. A held line the cap has since let go
  // of holds nothing - the rows it would keep are gone already.
  const heldEnd = held === null ? null : buffer.shownThrough(held);
  const end = heldEnd ?? shown.length;
  const waiting = shown.length - end;
  const from = Math.max(0, end - windowSize);
  const rows = shown.slice(from, end);
  // The window may only grow **upward**, so a filter that cuts the buffer down
  // must not leave a window larger than there is anything to put in it - which
  // would make "am I at the top of the drawn window" trivially true and fire a
  // page backwards on every scroll.
  const atWindowTop = from === 0;

  const scrolled = useLogScroll({
    hostRef: host, pinnedRef: pinned, grewRef: grew, held: heldEnd !== null,
    windowSize, setWindowSize, atWindowTop, loadEarlier,
  });
  useLogSelection({ hostRef: host, buffer, rows, setHeld });
  const onScroll = (event: UIEvent<HTMLDivElement>): void => {
    scrolled(event);
    const away = !pinned.current;
    if (away !== (paused !== null)) setPaused(away ? { mark: buffer.pushed } : null);
  };
  const jump = (): void => {
    pinned.current = true;
    setPaused(null);
    setHeld(null);
    window.getSelection()?.removeAllRanges();
    const element = host.current;
    if (element !== null) element.scrollTop = element.scrollHeight;
  };
  const following = paused === null && heldEnd === null;
  const fresh = paused !== null ? buffer.pushed - paused.mark : waiting;

  const chosen = containers.data?.state === 'ok' ? containers.data.items : [];
  const active = container === '' ? chosen.find((c) => c.init !== true)?.name ?? '' : container;

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <LogToolbar
        containers={chosen}
        active={active}
        previous={previous}
        onContainer={(picked) =>
          dispatch({ type: 'setContainer', id: tab.id, container: picked, previous: false })
        }
        onPrevious={(want) =>
          dispatch({ type: 'setContainer', id: tab.id, container: active, previous: want })
        }
        query={typed}
        onQuery={onQuery}
        onForce={settleQuery}
        regex={regex}
        onRegex={onRegex}
        matchCase={matchCase}
        onMatchCase={onMatchCase}
        error={filter.error}
        matches={filter.pattern === null ? 0 : shown.length}
        at={at}
        onStep={(step) => {
          const count = shown.length;
          if (count === 0) return;
          const next = ((at + step) % count + count) % count;
          setAt(next);
          // Walking the matches moves the window off the end, so following stops
          // - without which new lines would arrive into the buffer and never
          // appear, a panel that has quietly stopped for no reason the reader can
          // see. Reaching the bottom again re-attaches it.
          pinned.current = false;
          const index = shown[next];
          if (index !== undefined) {
            host.current?.querySelector(`[data-line="${index}"]`)?.scrollIntoView({ block: 'center' });
          }
        }}
        timestamps={timestamps}
        onTimestamps={() => {
          toggleTimestamps();
          dispatch({ type: 'revive', id: tab.id });
        }}
        wrap={wrap}
        onWrap={toggleWrap}
        json={json}
        onJSON={() => { toggleJSON(); setAt(0); }}
        onDownload={() => downloadLog(buffer, name, active)}
      />

      <div
        ref={host}
        className="relative min-h-0 flex-1 overflow-auto px-2 pb-2 font-mono text-xs"
        onScroll={onScroll}
      >
        <Backward
          state={back}
          onMore={loadEarlier}
          empty={buffer.length === 0}
          dropped={buffer.dropped}
          cap={buffer.cap}
        />
        <pre className={wrap ? 'whitespace-pre-wrap break-words' : 'whitespace-pre'}>
          {rows.map((index) => (
            <LogRow
              key={rowKey(buffer.lines[index], index)}
              buffer={buffer}
              index={index}
              matcher={filter.pattern}
              stamped={timestamps}
              current={shown[at] === index}
            />
          ))}
        </pre>
        <HeldBack waiting={waiting} />
        <MarkerLine marker={marker} />
      </div>
      <LogStatusBar
        following={following}
        ended={!tab.live}
        fresh={Math.max(0, fresh)}
        lines={shown.length}
        first={buffer.lines[0]?.at}
        last={buffer.lines[buffer.length - 1]?.at}
        onJump={following ? null : jump}
      />
    </div>
  );
}
