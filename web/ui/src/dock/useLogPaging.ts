import { useCallback, useRef, useState, type Dispatch, type RefObject, type SetStateAction } from 'react';

import { fetchEarlierLogs, type LogPage } from '../api';
import { LOG_CHUNK, LOG_WINDOW, type LogBuffer } from './logBuffer';
import type { LogTarget } from './useLogStream';

/**
 * Looking backwards, as four states that cannot be two.
 *
 * `busy` is a page in flight and **always resolves into one of the others**:
 * a spinner that ends in nothing is the worst of the four, because it is the one
 * the reader waits on. `fail` is a fetch that did not answer and is kept apart
 * from `stop` for two reasons - it can be retried and a stop cannot, and "the
 * cluster did not answer" and "there is nothing earlier" are different answers.
 * `stop` is the server's, and is three.
 */
export interface Backwards {
  readonly busy: boolean;
  /** Which of the three ways paging ended. Absent while there may be more. */
  readonly stop?: LogPage['stop'];
  /** The server's sentence for that stop. Never composed here: it is written
   *  server-side so that curl and this panel say the same words, and the
   *  beginning one is careful never to say "the container started here" - the
   *  kubelet rotates. */
  readonly note?: string;
  /** The fetch itself failed. Retryable, which is what distinguishes it. */
  readonly fail?: string;
}

/**
 * The drawn window and the pages backwards: how many rows of the buffer are in
 * the document, and where looking backwards stands.
 */
export function useLogPaging({
  buffer, target, timestamps, hostRef, pinnedRef, grewRef, dirtyRef, generationRef,
}: {
  buffer: LogBuffer;
  target: LogTarget;
  timestamps: boolean;
  hostRef: RefObject<HTMLDivElement | null>;
  pinnedRef: RefObject<boolean>;
  grewRef: RefObject<number | null>;
  dirtyRef: RefObject<boolean>;
  generationRef: RefObject<number>;
}): {
  readonly windowSize: number;
  readonly setWindowSize: Dispatch<SetStateAction<number>>;
  readonly back: Backwards;
  /** Back to the newest window, with nothing learned about looking backwards. */
  readonly resetPaging: () => void;
  readonly loadEarlier: () => void;
} {
  const { cluster, namespace, name, container, previous } = target;
  /**
   * How many rows of the buffer are in the document.
   *
   * Grows upward by `LOG_CHUNK` when the reader scrolls near the top, and the
   * scroll position is compensated by exactly the height that arrived - **the
   * single most common way an upward infinite scroll ships broken** is to grow
   * the list and let the browser keep `scrollTop`, which walks the reader
   * backwards through the content they were reading.
   */
  const [windowSize, setWindowSize] = useState(LOG_WINDOW);
  /** Looking backwards. Four states, and `busy` always resolves into one of the
   *  other three. */
  const [back, setBack] = useState<Backwards>({ busy: false });

  const resetPaging = useCallback(() => {
    setBack({ busy: false });
    setWindowSize(LOG_WINDOW);
  }, []);

  /**
   * One page backwards.
   *
   * Guarded on `busy` through a ref rather than through the state, because two
   * scroll events land in one frame and a state read inside a handler is the
   * value from the last render - which is how a scroll to the top fires three
   * identical requests and three of the server's read budgets.
   *
   * The scroll compensation is the whole of the correctness here: the lines are
   * prepended, then `scrollTop` is moved by exactly the height that arrived, so
   * the line the reader was looking at stays where it was. It is done
   * synchronously against the live element after React has painted the new rows,
   * which is what `flushSync`-free ordering costs - the effect in `useLogScroll`
   * does it instead, keyed on the prepend having happened.
   */
  const busy = useRef(false);
  const loadEarlier = useCallback(() => {
    if (busy.current) return;
    // A stop is a stop: `beginning`, `ceiling` and `dense` are all "do not ask
    // again", and only `fail` may be retried.
    if (back.stop !== undefined) return;
    if (buffer.length === 0) return;
    // Lines were let go to stay within the cap, so the buffer no longer starts
    // where the stream did and `before` would count from the wrong place.
    if (buffer.dropped > 0) return;
    busy.current = true;
    setBack({ busy: true });
    const asked = generationRef.current;

    const element = hostRef.current;
    const before = element === null ? 0 : element.scrollHeight - element.scrollTop;

    fetchEarlierLogs({
      cluster, kind: 'pods', namespace, name,
      ...(container === '' ? {} : { container }),
      before: buffer.length,
      page: LOG_CHUNK,
      previous,
      timestamps,
    })
      .then((answer) => {
        // The buffer was emptied for another read while this was in flight, so
        // the page was counted against lines that are no longer here.
        if (asked !== generationRef.current) {
          setBack({ busy: false });
          return;
        }
        if (answer.state !== 'ok') {
          // The envelope says the page did not arrive and why. Distinct from a
          // stop, which says it did arrive and there will not be another.
          setBack({ busy: false, fail: answer.reason.message });
          return;
        }
        const page: LogPage = answer.data;
        const added = buffer.prepend(page.lines.map((text) => ({ text })));
        // Looking backwards is not following. Without this the next arriving
        // line would yank the reader back to the bottom of a log they had
        // deliberately scrolled away from.
        pinnedRef.current = false;
        if (added > 0) {
          setWindowSize((was) => was + added);
          grewRef.current = before;
        }
        dirtyRef.current = true;
        setBack(
          page.stop === undefined
            ? { busy: false }
            : { busy: false, stop: page.stop, note: page.note ?? '' },
        );
      })
      .catch((cause: unknown) => {
        setBack({
          busy: false,
          fail: cause instanceof Error ? cause.message : 'the page could not be fetched',
        });
      })
      .finally(() => {
        busy.current = false;
      });
  }, [
    back.stop, buffer, cluster, container, dirtyRef, generationRef, grewRef, hostRef, name,
    namespace, pinnedRef, previous, timestamps,
  ]);

  return { windowSize, setWindowSize, back, resetPaging, loadEarlier };
}
