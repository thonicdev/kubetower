import type { paths } from './schema';
import type { LogEvent } from './types';

/** Checked against the contract the same way `endpoints.ts` checks its
 *  templates: rename the operation in `api/openapi.yaml`, regenerate, and the
 *  type-check fails here rather than the panel 404-ing at run time. */
const LOGS = '/api/logs' satisfies keyof paths;

/**
 * The one place a log stream is opened.
 *
 * `EventSource` is banned outside this directory by `no-restricted-globals`, for
 * the same reason as `fetch`: one network module is what
 * makes the session cookie and the error mapping properties of the application
 * rather than of whichever component was written most recently.
 *
 * ## Why the frames are JSON and not the line itself
 *
 * Putting the line in the SSE `data:` field directly, with the end conditions
 * as *named events* - `end`, `truncated`, `error` - works and cannot say
 * anything mid-stream: a follow that stops because the pod was deleted and one
 * that stops because nothing is being written are the same silence.
 *
 * `/api/logs` frames every event as one JSON object, so a `state` frame carries
 * the envelope's own five-state vocabulary in the middle of a stream. What that
 * buys, concretely, is the one refusal a log panel can actually hit - sixteen
 * live follows already open - arriving as a sentence the panel can draw, where
 * the alternative is to smuggle it inside an `error` event, because **an
 * EventSource cannot see a status code at all**, not on the handshake and not
 * afterwards.
 *
 * That blindness is why the two callbacks below are not one. `onEvent` is the
 * server having said something; `onBroken` is the browser's own `error`, which
 * carries nothing - no status, no body, not even whether the server was reached.
 * A panel that renders both the same way tells the operator "the stream stopped"
 * for a refusal that named its reason.
 */
export interface LogStreamQuery {
  readonly cluster: string;
  readonly kind: string;
  readonly namespace: string;
  readonly name: string;
  readonly container?: string;
  readonly follow?: boolean;
  readonly tailLines?: number;
  readonly previous?: boolean;
  /** A property of the request and not of the rendering: the apiserver either
   *  prefixes the instant or it does not, and there is no way to add one
   *  afterwards to lines that arrived without. Turning it on therefore re-opens
   *  the stream and the buffer starts again from the tail, which is why the
   *  panel's button has to say so. */
  readonly timestamps?: boolean;
}

export interface LogStreamHandlers {
  /** One frame the server sent. */
  readonly onEvent: (event: LogEvent) => void;
  /** The transport failed. **Distinct from a `state` frame**: this one carries
   *  nothing at all, so a panel may only say that the connection went, never why.
   *
   *  `retry` says what happens next: a reconnect is scheduled (`LogRetry`), or
   *  `null` - there will be no further connection and the stream is over. */
  readonly onBroken: (retry: LogRetry | null) => void;
  /** A scheduled reconnect is about to open. The new connection replays the
   *  tail from the start, so a caller that keeps what it already holds has to
   *  expect lines it has seen.
   *
   *  **Optional**, because this interface is public `@kubetower/ui` API
   *  (`ui/src/index.ts` re-exports `./api`) and a caller written before it must
   *  keep compiling. Returning `false` cancels the reconnect and ends the stream
   *  without a further `onBroken`: the caller has stopped listening, so a
   *  request made on its behalf would be one nobody reads. */
  readonly onReopen?: () => boolean | void;
  /** The stream is waiting for one of this page's log connections
   *  (`MAX_OPEN_LOG_STREAMS`) and has not asked the server anything yet.
   *  `why` is the sentence to draw. It opens on its own as soon as another log
   *  stream ends or is closed; the first frame it then receives is the end of
   *  the wait. Optional, for `onReopen`'s reason. */
  readonly onQueued?: (why: string) => void;
}

/** A reconnect that has been scheduled after a dropped connection. */
export interface LogRetry {
  /** Which attempt this is, from 1. */
  readonly attempt: number;
  /** How many attempts there will be before the stream is given up. */
  readonly of: number;
  /** How long until it opens. */
  readonly inMs: number;
}

/**
 * The reconnect budget after a dropped connection: this many attempts, waiting
 * `RETRY_BASE_MS` doubled each time - 1, 2, 4, 8 and 16 seconds.
 *
 * Bounded on purpose. A stream the server ended never comes here at all (see
 * `openLogStream`), so this only spends itself on a connection that went with
 * no `end`: a proxy restart, a laptop waking up, the server being redeployed.
 * Past half a minute of that, a further attempt is somebody's dock hammering a
 * server that is not coming back, and the panel says the connection went.
 */
export const RETRY_ATTEMPTS = 5;
export const RETRY_BASE_MS = 1_000;

/** A connection that stayed up this long before dropping has earned a fresh
 *  budget: a follow that loses its connection once an hour must not run out of
 *  attempts on the sixth hour. Shorter than that and the attempts keep counting,
 *  which is what bounds a server that answers and then drops every time - a
 *  budget reset by any frame would let that server loop the panel forever.
 *
 *  The known cost: behind a proxy whose idle timeout is under a minute, a quiet
 *  follow is cut, reconnects, is cut again, and gives up after its attempts
 *  (about two and a half minutes). The cure is a server heartbeat (an SSE `:`
 *  comment line every 30 s or so), which is a server change and not this one. */
export const RETRY_STABLE_MS = 60_000;

/**
 * How many log streams this page holds open at once. **Four, below six on
 * purpose.**
 *
 * Every following log pane is one `EventSource`, which is one long-lived HTTP
 * request, and a browser speaking HTTP/1.1 holds **six connections per
 * origin** - the desktop listener is plain HTTP/1.1. Hidden panes stay mounted
 * and keep following (`dock/Dock.tsx`: a follow left on cluster A is still
 * running when the operator comes back), and the dock holds sixteen tabs, so
 * six following panes would take every connection the browser allows and the
 * console would stop answering, with nothing saying why.
 *
 * Capped rather than paused when hidden, and the argument is the dock's own
 * promise: a hidden follow is one the operator left running to collect what
 * arrives meanwhile, and pausing it would trade a stalled console for a log
 * with a silent gap in it. Capped rather than multiplexed because one stream
 * carrying every pane is a server change and a protocol change for the same
 * guarantee. Four leaves two connections for everything else the page asks -
 * the requests a list, a drawer and a panel make - which is what "the console
 * keeps answering" needs.
 *
 * A fifth stream **waits, and says so** (`onQueued`), and opens by itself the
 * moment another log stream ends or is closed: nothing is dropped, and the
 * order is the order they were asked for.
 */
export const MAX_OPEN_LOG_STREAMS = 4;

/** Why a stream is waiting, drawn by the panel as it is. */
export const QUEUED_SENTENCE =
  `Waiting to start: ${MAX_OPEN_LOG_STREAMS} logs are already streaming in this window, and a browser ` +
  'keeps only six connections open to the console - the rest are needed for the pages. This log ' +
  'starts by itself as soon as one of the others ends or is closed.';

/** The page's log connections: how many are held, and who is waiting. */
const slots = { held: 0, waiting: [] as Array<() => void> };

/** Takes a slot now, or queues `start` for the next free one, which then
 *  calls it. Returns what gives the slot back - or leaves the queue - once, and
 *  `true` when it had to wait. */
function takeSlot(start: () => void): { release: () => void; queued: boolean } {
  let state: 'waiting' | 'held' | 'done' = 'waiting';
  const granted = (): void => {
    state = 'held';
    start();
  };
  const release = (): void => {
    if (state === 'held') {
      slots.held -= 1;
      const next = slots.waiting.shift();
      if (next !== undefined) {
        slots.held += 1;
        next();
      }
    } else if (state === 'waiting') {
      const at = slots.waiting.indexOf(granted);
      if (at >= 0) slots.waiting.splice(at, 1);
    }
    state = 'done';
  };
  if (slots.held < MAX_OPEN_LOG_STREAMS) {
    // Held at once; the caller connects, after it has stored `release`.
    slots.held += 1;
    state = 'held';
    return { release, queued: false };
  }
  slots.waiting.push(granted);
  return { release, queued: true };
}

/** How many log streams hold a connection and how many wait. For tests. */
export function logStreamSlots(): { held: number; waiting: number } {
  return { held: slots.held, waiting: slots.waiting.length };
}

/** Forgets every slot: the test setup's seam, after each test. */
export function resetLogStreamSlots(): void {
  slots.held = 0;
  slots.waiting.length = 0;
}

/** `EventSource.CLOSED`, written out: the constant is a static of a global this
 *  module may be loaded without, in tests that stub it. */
const CLOSED = 2;

export interface LogStream {
  /** Closes the stream. Safe to call more than once. */
  readonly close: () => void;
}

export function logStreamPath(query: LogStreamQuery): string {
  const params = new URLSearchParams({
    cluster: query.cluster,
    kind: query.kind,
    namespace: query.namespace,
    name: query.name,
  });
  if (query.container !== undefined && query.container !== '') {
    params.set('container', query.container);
  }
  // Written out rather than left to the default, because the default differs
  // between the two halves of a re-read: a panel switching to a previous run
  // wants `follow=false` and would otherwise inherit `true`.
  params.set('follow', String(query.follow ?? true));
  if (query.tailLines !== undefined) params.set('tailLines', String(query.tailLines));
  if (query.previous === true) params.set('previous', 'true');
  if (query.timestamps === true) params.set('timestamps', 'true');
  return `${LOGS}?${params.toString()}`;
}

/**
 * Open the stream. The caller closes it.
 *
 * Held through `live/handles.ts` by everything that calls this, so StrictMode's
 * second mount finds the first stream rather than taking a second of the
 * server's sixteen follow slots.
 *
 * ## An ended stream stays ended
 *
 * **An `EventSource` reconnects on its own** whenever a `200` response simply
 * finishes - that is the spec, and it is exactly how the server ends every
 * stream: a finished container, a `previous` log, the 30-minute deadline, the
 * 8 MiB ceiling, and every refusal (`state` then `end`, at 200). Left alone, the
 * browser comes back every three seconds, the server replays the 2000-line tail
 * each time, and the panel appends all of it: thousands of duplicate lines in
 * seconds.
 *
 * So the source is closed here, not by the panel, the moment the stream
 * stops, and there are four ways for it to stop that are kept apart:
 *
 *   - **`end`** - the server said so. Closed on the spot, no further request.
 *   - **an `error` after a `state` frame** with no `end` behind it - a refusal
 *     or a stop whose reason has already been drawn. Closed, no retry: asking
 *     again gets the same refusal. Not after a `pending` frame, which says the
 *     follow is waiting for its container and not why it stopped.
 *   - **an `error` on a handshake the server refused** (not a 200) - final, as
 *     the browser itself treats it.
 *   - **an `error` with neither** - the connection dropped and nobody said why.
 *     The browser's own retry is canceled and replaced by a bounded one with a
 *     backoff (`RETRY_ATTEMPTS`), and `onReopen` runs before each, because the
 *     new request replays the tail from the start.
 *
 * Re-reading after an end is an explicit act - the container switch, the Time
 * toggle, a reopen - and each of those already makes a new handle.
 */
export function openLogStream(query: LogStreamQuery, handlers: LogStreamHandlers): LogStream {
  const path = logStreamPath(query);
  let closed = false;
  let source: EventSource | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let attempts = 0;
  // The page's log connection this stream holds, or waits for. Given back on
  // every way the stream ends - `end`, a final error, `close()` - so a
  // waiting stream opens the moment it can (MAX_OPEN_LOG_STREAMS).
  let slot: { release: () => void } = { release: () => {} };
  const stop = (): void => {
    closed = true;
    slot.release();
  };

  const connect = (): void => {
    const current = new EventSource(path);
    source = current;
    const opened = Date.now();
    let heard = false;
    let stated = false;

    current.onmessage = (message: MessageEvent<string>) => {
      if (closed || source !== current) return;
      let event: LogEvent;
      try {
        event = JSON.parse(message.data) as LogEvent;
      } catch {
        // A frame this build cannot read is not a line, and inventing one would
        // put text in the pane the container did not print. Dropped, and the
        // stream carries on: one unreadable frame is not a reason to lose the
        // rest of a log.
        return;
      }
      heard = true;
      // A `pending` frame is the one `state` that is not a reason for stopping:
      // a follow waiting for its container to start, whose lines come next on
      // this same connection. Counted as one, a drop while
      // it waited would be taken for an end the server had explained, and the
      // stream given up instead of reconnected.
      if (event.type === 'state' && event.state !== 'pending') stated = true;
      if (event.type === 'end') {
        // Before the handler, so a handler that throws cannot leave the browser
        // reconnecting behind it.
        stop();
        current.close();
      }
      handlers.onEvent(event);
    };

    current.onerror = () => {
      // The browser's own error. It fires on a failed handshake, on a dropped
      // connection, and on the server closing the stream normally - and it
      // carries nothing that tells the three apart, which is the whole reason
      // the server sends an explicit `end` frame before it closes.
      if (closed || source !== current) return;
      // A handshake the server refused - not a 200, or not an event stream: an
      // expired session, a 5xx before the first frame. The browser has already
      // "failed the connection" and will not retry, and neither does this:
      // asking again gets the same answer, and "the log will be read again" for
      // a session that has expired would be a promise nothing can keep. Read
      // before `close()`, which would make every error look like this one.
      const refused = current.readyState === CLOSED;
      // Whatever happens next, it is not the browser's three-second retry.
      current.close();
      if (refused) {
        stop();
        handlers.onBroken(null);
        return;
      }

      if (stated) {
        // The server already said why it stopped. Asking again gets the same
        // answer, so this is an end the `end` frame did not arrive for.
        stop();
        handlers.onBroken(null);
        return;
      }
      if (heard && Date.now() - opened >= RETRY_STABLE_MS) attempts = 0;
      if (attempts >= RETRY_ATTEMPTS) {
        stop();
        handlers.onBroken(null);
        return;
      }
      attempts += 1;
      const inMs = RETRY_BASE_MS * 2 ** (attempts - 1);
      handlers.onBroken({ attempt: attempts, of: RETRY_ATTEMPTS, inMs });
      timer = setTimeout(() => {
        timer = null;
        if (closed) return;
        if (handlers.onReopen?.() === false) {
          stop();
          return;
        }
        connect();
      }, inMs);
    };
  };

  const taken = takeSlot(() => {
    if (!closed) connect();
  });
  slot = taken;
  if (taken.queued) handlers.onQueued?.(QUEUED_SENTENCE);
  else connect();

  return {
    close: () => {
      stop();
      if (timer !== null) clearTimeout(timer);
      timer = null;
      source?.close();
    },
  };
}
