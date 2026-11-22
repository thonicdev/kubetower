import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  MAX_OPEN_LOG_STREAMS,
  QUEUED_SENTENCE,
  RETRY_ATTEMPTS,
  RETRY_STABLE_MS,
  logStreamSlots,
  openLogStream,
  type LogRetry,
  type LogStream,
} from './logstream';
import type { LogEvent } from './types';

/**
 * The log stream's ending, against an `EventSource` that does what the spec
 * says.
 *
 * The fake below is the point of this file. The panel's other tests stub an
 * `EventSource` whose `close` does nothing and which never reconnects, so a
 * stream that was never closed looked exactly like one that was - and the
 * browser's real behavior, **reconnecting three seconds after a `200` response
 * simply finishes**, was the whole defect. Here a response that finishes fires
 * `error` and the same object asks again three seconds later, unless `close()`
 * was called; and each request is answered by `server`, as `/api/logs` would.
 */

/** Every request made, by every source - one entry per HTTP request. */
const requests: string[] = [];
/** Every source constructed. A reconnect by the browser reuses one; a reconnect
 *  by our code constructs another. */
const sources: SpecEventSource[] = [];
/** Answers one request. Runs on a timer, as a response would. */
let server: (source: SpecEventSource) => void = () => {};

/** The browser's reconnection time when the server sends no `retry:`. */
const BROWSER_RETRY_MS = 3_000;

class SpecEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;

  url: string;
  readyState = SpecEventSource.CONNECTING;
  onmessage: ((event: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;

  constructor(url: string) {
    this.url = url;
    sources.push(this);
    this.request();
  }

  close(): void {
    this.readyState = SpecEventSource.CLOSED;
  }

  /** One frame from the server. Dropped once closed, as the browser drops it. */
  send(event: LogEvent): void {
    if (this.readyState === SpecEventSource.CLOSED) return;
    this.readyState = SpecEventSource.OPEN;
    this.onmessage?.({ data: JSON.stringify(event) });
  }

  /**
   * The handshake was refused - not a 200, or not `text/event-stream`. The
   * browser "fails the connection": `CLOSED`, then `error`, and no retry.
   */
  refuse(): void {
    if (this.readyState === SpecEventSource.CLOSED) return;
    this.readyState = SpecEventSource.CLOSED;
    this.onerror?.();
  }

  /**
   * The response finished, normally or not: the browser cannot tell, and does
   * the same thing for both - "reestablish the connection": `error`, then a new
   * request on the same object after the reconnection time, unless closed.
   */
  finish(): void {
    if (this.readyState === SpecEventSource.CLOSED) return;
    this.readyState = SpecEventSource.CONNECTING;
    this.onerror?.();
    setTimeout(() => {
      if (this.readyState === SpecEventSource.CLOSED) return;
      this.request();
    }, BROWSER_RETRY_MS);
  }

  private request(): void {
    requests.push(this.url);
    setTimeout(() => {
      if (this.readyState === SpecEventSource.CLOSED) return;
      server(this);
    }, 0);
  }
}

function lines(source: SpecEventSource, count: number): void {
  for (let i = 0; i < count; i++) source.send({ type: 'line', text: `line ${i}` });
}

interface Seen {
  events: LogEvent[];
  broken: (LogRetry | null)[];
  reopened: number;
}

function open(): { stream: LogStream; seen: Seen } {
  const seen: Seen = { events: [], broken: [], reopened: 0 };
  const stream = openLogStream(
    { cluster: 'prod', kind: 'pods', namespace: 'default', name: 'api-1', previous: true, follow: false },
    {
      onEvent: (event) => seen.events.push(event),
      onBroken: (retry) => seen.broken.push(retry),
      onReopen: () => {
        seen.reopened += 1;
      },
    },
  );
  return { stream, seen };
}

/** A simulated minute, in steps so every timer along the way fires. */
function aMinute(): void {
  for (let i = 0; i < 60; i++) vi.advanceTimersByTime(1_000);
}

beforeEach(() => {
  vi.useFakeTimers();
  requests.length = 0;
  sources.length = 0;
  server = () => {};
  vi.stubGlobal('EventSource', SpecEventSource);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('a stream the server ended', () => {
  it('is closed on `end`, and asks for nothing more over a minute', () => {
    // The measured case: a `previous` log, replayed in full on every request.
    server = (source) => {
      lines(source, 2000);
      source.send({ type: 'end', atStart: true });
      source.finish();
    };
    const { seen } = open();
    aMinute();

    expect(requests).toHaveLength(1);
    expect(sources[0]?.readyState).toBe(SpecEventSource.CLOSED);
    expect(seen.events.filter((event) => event.type === 'line')).toHaveLength(2000);
    expect(seen.events.filter((event) => event.type === 'end')).toHaveLength(1);
    // An end is not a broken connection, and nothing is replayed.
    expect(seen.broken).toEqual([]);
    expect(seen.reopened).toBe(0);
  });

  it('does not loop on a refusal: `state` then `end`', () => {
    // Forbidden, pod gone, follow slots full - all of them `state` and `end` at
    // 200, which the browser reads as a stream to resume.
    server = (source) => {
      source.send({
        type: 'state',
        state: 'forbidden',
        reason: { code: 'verb_denied', message: 'you may not read the logs of this pod' },
      });
      source.send({ type: 'end' });
      source.finish();
    };
    const { seen } = open();
    aMinute();

    expect(requests).toHaveLength(1);
    expect(sources).toHaveLength(1);
    expect(seen.broken).toEqual([]);
  });

  it('does not retry a refusal whose `end` never arrived', () => {
    // The reason has been said. Asking again gets the same refusal, every few
    // seconds, for as long as the dock is open.
    server = (source) => {
      source.send({
        type: 'state',
        state: 'unreadable',
        reason: { code: 'truncated', message: 'stopped after 8 MiB' },
      });
      source.finish();
    };
    const { seen } = open();
    aMinute();

    expect(requests).toHaveLength(1);
    expect(sources).toHaveLength(1);
    expect(sources[0]?.readyState).toBe(SpecEventSource.CLOSED);
    expect(seen.broken).toEqual([null]);
  });
});

describe('a follow waiting for its container', () => {
  it('reconnects a drop after a `pending` frame, which is not a reason for stopping', () => {
    // The server says the container is being created and
    // keeps the stream open for its lines. A connection lost then is a drop
    // like any other, not an end the server explained.
    let request = 0;
    server = (source) => {
      request += 1;
      if (request === 1) {
        source.send({
          type: 'state',
          state: 'pending',
          reason: {
            code: 'container_not_started',
            message: 'waiting for the container to start: ContainerCreating',
          },
        });
        source.finish();
        return;
      }
      lines(source, 1);
      source.send({ type: 'end' });
      source.finish();
    };
    const { seen } = open();
    aMinute();

    expect(requests).toHaveLength(2);
    expect(seen.broken).toEqual([{ attempt: 1, of: RETRY_ATTEMPTS, inMs: 1_000 }]);
    expect(seen.events.map((event) => event.type)).toEqual(['state', 'line', 'end']);
  });
});

describe('a connection that dropped', () => {
  it('reconnects with a backoff, a bounded number of times, and then says so', () => {
    // No frame at all: a server that is not there. The browser's own retry is
    // every three seconds forever; ours is 1, 2, 4, 8, 16 seconds and done.
    server = (source) => source.finish();
    const { seen } = open();
    aMinute();
    aMinute();

    expect(requests).toHaveLength(1 + RETRY_ATTEMPTS);
    expect(seen.broken.map((retry) => retry?.inMs ?? null)).toEqual([
      1_000, 2_000, 4_000, 8_000, 16_000, null,
    ]);
    expect(seen.broken.map((retry) => retry?.attempt ?? null)).toEqual([1, 2, 3, 4, 5, null]);
    expect(seen.reopened).toBe(RETRY_ATTEMPTS);
    // Every source is closed: none of them is still retrying on its own.
    expect(sources.every((source) => source.readyState === SpecEventSource.CLOSED)).toBe(true);
  });

  it('tells the caller to drop what it holds before the tail is replayed', () => {
    let request = 0;
    server = (source) => {
      request += 1;
      lines(source, 3);
      // The first connection drops with no `end`; the second ends properly.
      if (request === 1) {
        source.finish();
        return;
      }
      source.send({ type: 'end' });
      source.finish();
    };
    const order: string[] = [];
    openLogStream(
      { cluster: 'prod', kind: 'pods', namespace: 'default', name: 'api-1' },
      {
        onEvent: (event) => order.push(event.type),
        onBroken: (retry) => order.push(retry === null ? 'gone' : 'retry'),
        onReopen: () => {
          order.push('reopen');
        },
      },
    );
    aMinute();

    expect(order).toEqual(['line', 'line', 'line', 'retry', 'reopen', 'line', 'line', 'line', 'end']);
    expect(requests).toHaveLength(2);
  });

  it('stays bounded against a server that answers and then drops, every time', () => {
    // The budget must not reset on a frame alone: this server sends lines on
    // every connection, and a reset per frame would reconnect it forever.
    server = (source) => {
      lines(source, 3);
      source.finish();
    };
    const { seen } = open();
    aMinute();
    aMinute();

    expect(requests).toHaveLength(1 + RETRY_ATTEMPTS);
    expect(seen.broken[seen.broken.length - 1]).toBeNull();
  });

  it('gives a connection that stayed up a fresh budget', () => {
    // One drop an hour must not exhaust the attempts on the sixth hour.
    server = (source) => {
      source.send({ type: 'line', text: 'ready' });
      setTimeout(() => source.finish(), RETRY_STABLE_MS);
    };
    const { seen } = open();
    for (let i = 0; i < RETRY_ATTEMPTS + 2; i++) {
      vi.advanceTimersByTime(RETRY_STABLE_MS);
      vi.advanceTimersByTime(1_000);
    }
    expect(seen.broken.length).toBeGreaterThan(RETRY_ATTEMPTS);
    expect(seen.broken.every((retry) => retry?.attempt === 1)).toBe(true);
  });

  it('takes a refused handshake as final, as the browser does', () => {
    // An expired session or a 5xx before the first frame. Asking again gets the
    // same answer, and promising to re-read the log would be a promise nothing
    // can keep.
    server = (source) => source.refuse();
    const { seen } = open();
    aMinute();

    expect(requests).toHaveLength(1);
    expect(sources).toHaveLength(1);
    expect(seen.broken).toEqual([null]);
    expect(seen.reopened).toBe(0);
  });

  it('reconnects without `onReopen`, which is optional public API', () => {
    let request = 0;
    server = (source) => {
      request += 1;
      if (request === 1) source.finish();
    };
    const broken: (LogRetry | null)[] = [];
    openLogStream(
      { cluster: 'prod', kind: 'pods', namespace: 'default', name: 'api-1' },
      { onEvent: () => {}, onBroken: (retry) => broken.push(retry) },
    );
    aMinute();

    expect(requests).toHaveLength(2);
    expect(broken).toHaveLength(1);
  });

  it('drops the reconnect when `onReopen` says nobody is listening', () => {
    server = (source) => source.finish();
    const broken: (LogRetry | null)[] = [];
    openLogStream(
      { cluster: 'prod', kind: 'pods', namespace: 'default', name: 'api-1' },
      { onEvent: () => {}, onBroken: (retry) => broken.push(retry), onReopen: () => false },
    );
    aMinute();

    expect(requests).toHaveLength(1);
    // Nobody is listening, so nobody is told either.
    expect(broken).toHaveLength(1);
  });

  it('cancels a scheduled reconnect when closed', () => {
    server = (source) => source.finish();
    const { stream, seen } = open();
    vi.advanceTimersByTime(10);
    expect(seen.broken).toHaveLength(1);

    stream.close();
    aMinute();
    expect(requests).toHaveLength(1);
    expect(seen.reopened).toBe(0);
  });
});

/**
 * **Seven following panes leave the console answering**. A browser speaking
 * HTTP/1.1 - the desktop listener - holds six connections per origin, and every
 * following pane is one `EventSource`, one long-lived request. Without the cap,
 * six panes would take all six and every other request of the page would wait
 * behind them, for as long as they followed.
 *
 * The connection is the `SpecEventSource` below: one constructed is one
 * request held open until it is closed or ends. The page's ordinary requests
 * are what is left of six.
 */
describe('the page keeps connections for its other requests', () => {
  const BROWSER_CONNECTIONS = 6;

  /** A follow that streams and never ends, which is what a following pane is. */
  function follow(): { stream: LogStream; queued: string[]; events: LogEvent[] } {
    const queued: string[] = [];
    const events: LogEvent[] = [];
    const stream = openLogStream(
      { cluster: 'prod', kind: 'pods', namespace: 'default', name: 'api-1', follow: true },
      {
        onEvent: (event) => events.push(event),
        onBroken: () => {},
        onQueued: (why) => queued.push(why),
      },
    );
    return { stream, queued, events };
  }

  /** Connections the log streams hold right now. */
  const held = (): number => sources.filter((each) => each.readyState !== SpecEventSource.CLOSED).length;

  it('holds at most four, so seven following panes leave two connections free', () => {
    server = (source) => source.send({ type: 'line', text: 'still running' });
    const panes = Array.from({ length: 7 }, follow);
    vi.advanceTimersByTime(0);

    expect(MAX_OPEN_LOG_STREAMS).toBeLessThan(BROWSER_CONNECTIONS);
    expect(held()).toBe(MAX_OPEN_LOG_STREAMS);
    expect(BROWSER_CONNECTIONS - held()).toBeGreaterThanOrEqual(2);
    expect(logStreamSlots()).toEqual({ held: 4, waiting: 3 });
    // The three that wait say so, in words, and asked the server nothing.
    expect(panes.slice(4).map((pane) => pane.queued)).toEqual([[QUEUED_SENTENCE], [QUEUED_SENTENCE], [QUEUED_SENTENCE]]);
    expect(panes.slice(4).every((pane) => pane.events.length === 0)).toBe(true);
    expect(panes.slice(0, 4).every((pane) => pane.queued.length === 0)).toBe(true);
    // A minute of following does not change it.
    aMinute();
    expect(held()).toBe(MAX_OPEN_LOG_STREAMS);
    expect(requests).toHaveLength(MAX_OPEN_LOG_STREAMS);
  });

  it('opens a waiting stream, in order, the moment another is closed or ends', () => {
    server = (source) => source.send({ type: 'line', text: 'running' });
    const panes = Array.from({ length: 6 }, follow);
    vi.advanceTimersByTime(0);
    expect(requests).toHaveLength(4);

    // Closed by the operator: the fifth asked for is the next to open.
    panes[1]?.stream.close();
    vi.advanceTimersByTime(0);
    expect(requests).toHaveLength(5);
    expect(panes[4]?.events.map((event) => event.text)).toEqual(['running']);
    expect(panes[5]?.events).toEqual([]);

    // Ended by the server: the sixth opens.
    sources[0]?.send({ type: 'end' });
    vi.advanceTimersByTime(0);
    expect(requests).toHaveLength(6);
    expect(panes[5]?.events.map((event) => event.text)).toEqual(['running']);
    expect(held()).toBe(MAX_OPEN_LOG_STREAMS);
    expect(logStreamSlots()).toEqual({ held: 4, waiting: 0 });
  });

  it('gives a waiting place back when the pane is closed before it opened', () => {
    server = (source) => source.send({ type: 'line', text: 'running' });
    const panes = Array.from({ length: 5 }, follow);
    vi.advanceTimersByTime(0);
    panes[4]?.stream.close();
    expect(logStreamSlots()).toEqual({ held: 4, waiting: 0 });

    // And a slot freed later opens nothing for a pane nobody is looking at.
    panes[0]?.stream.close();
    vi.advanceTimersByTime(0);
    expect(requests).toHaveLength(4);
    expect(logStreamSlots()).toEqual({ held: 3, waiting: 0 });
  });

  it('gives the slot back when a stream is given up after its retries', () => {
    // A connection that drops and never comes back: the stream gives up, and
    // the page's connection is free for the pane waiting behind it.
    server = (source) => source.finish();
    const panes = Array.from({ length: 5 }, follow);
    vi.advanceTimersByTime(0);
    expect(panes[4]?.queued).toEqual([QUEUED_SENTENCE]);
    aMinute();
    expect(logStreamSlots().waiting).toBe(0);
  });
});
