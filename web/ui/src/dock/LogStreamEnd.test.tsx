import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, render, screen } from '@testing-library/react';
import { useEffect, type ReactNode } from 'react';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { LogEvent } from '../api';
import { resetLiveHandles } from '../live/handles';
import { Dock } from './Dock';
import { DockProvider, useDock, type DockApi } from './DockContext';
import type * as Buffers from './logBuffer';

/**
 * The log panel at the end of its stream, against an `EventSource` that
 * reconnects the way the spec says a browser does.
 *
 * `LogPane.test.tsx` stubs a source that never reconnects and whose `close` does
 * nothing, so a panel that never closed its stream would pass every test there
 * while a real browser asked again every three seconds and appended the
 * replayed tail each time. This file's fake asks again three seconds after a
 * response finishes unless `close()` was called, and each request is answered
 * by `server`, as `/api/logs` would.
 *
 * The buffer's cap is lowered to 25 here, so the cap's notice can be seen
 * without pushing a hundred thousand lines through React.
 */

const CAP = 25;

vi.mock('./logBuffer', async (importOriginal) => {
  const actual: typeof Buffers = await importOriginal();
  class Small extends actual.LogBuffer {
    constructor() {
      super(CAP);
    }
  }
  return { ...actual, LogBuffer: Small };
});

const requests: string[] = [];
let server: (source: SpecEventSource) => void = () => {};

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
    this.request();
  }

  close(): void {
    this.readyState = SpecEventSource.CLOSED;
  }

  send(event: LogEvent): void {
    if (this.readyState === SpecEventSource.CLOSED) return;
    this.readyState = SpecEventSource.OPEN;
    this.onmessage?.({ data: JSON.stringify(event) });
  }

  /** The response finished: `error`, then the same object asks again after
   *  three seconds, unless closed. A normal close and a dropped connection are
   *  the same thing to the browser. */
  finish(): void {
    if (this.readyState === SpecEventSource.CLOSED) return;
    this.readyState = SpecEventSource.CONNECTING;
    this.onerror?.();
    setTimeout(() => {
      if (this.readyState === SpecEventSource.CLOSED) return;
      this.request();
    }, 3_000);
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

const probe: { api: DockApi | null } = { api: null };
const take = (api: DockApi): void => {
  probe.api = api;
};

function Probe({ take: sink }: { take: (api: DockApi) => void }): ReactNode {
  const api = useDock();
  useEffect(() => {
    sink(api);
  }, [api, sink]);
  return null;
}

function mount(): void {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  vi.stubGlobal('fetch', () =>
    Promise.resolve(
      new Response(JSON.stringify({ state: 'empty', items: [] }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    ));
  const router = createMemoryRouter(
    [
      {
        path: '/c/:cluster',
        element: (
          <DockProvider>
            <Probe take={take} />
            <Dock />
          </DockProvider>
        ),
      },
    ],
    { initialEntries: ['/c/prod'] },
  );
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  act(() => {
    probe.api?.openLogs({ cluster: 'prod', kind: 'pods', namespace: 'default', name: 'api-1' });
  });
}

/** Time passing, a second at a time so every timer along the way fires and the
 *  panel's 120 ms repaint runs. */
function seconds(count: number): void {
  for (let i = 0; i < count; i++) {
    act(() => {
      vi.advanceTimersByTime(1_000);
    });
  }
}

function rows(): number {
  return document.querySelectorAll('[data-line]').length;
}

function texts(): string[] {
  return Array.from(document.querySelectorAll('[data-line]'), (row) => row.textContent ?? '');
}

/** Which container a request asked for, or `''` for the server's default. */
function containerOf(url: string): string {
  return new URL(url, 'http://x').searchParams.get('container') ?? '';
}

function tabId(): string {
  return probe.api?.state.tabs[0]?.id ?? '';
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  requests.length = 0;
  server = () => {};
  vi.stubGlobal('EventSource', SpecEventSource);
});

afterEach(() => {
  probe.api = null;
  resetLiveHandles();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('a stream the server ended', () => {
  it('stays ended: one request, and the buffer does not grow over a minute', () => {
    // The measured case in the issue: a `previous` log, replayed whole on every
    // request - ten requests and twenty thousand line events in thirty seconds
    // from one panel.
    server = (source) => {
      lines(source, 10);
      source.send({ type: 'end', atStart: true });
      source.finish();
    };
    mount();
    seconds(2);
    expect(rows()).toBe(10);

    seconds(60);
    expect(requests).toHaveLength(1);
    expect(rows()).toBe(10);
    expect(screen.getByText(/End of log\./)).toBeTruthy();
    expect(probe.api?.state.tabs[0]?.live).toBe(false);
  });

  it('does not ask again after a refusal', () => {
    server = (source) => {
      source.send({
        type: 'state',
        state: 'forbidden',
        reason: { code: 'verb_denied', message: 'you may not read the logs of this pod' },
      });
      source.send({ type: 'end' });
      source.finish();
    };
    mount();
    seconds(60);

    expect(requests).toHaveLength(1);
    // The server's own sentence, not "the connection went away".
    expect(screen.getByText(/you may not read the logs of this pod/)).toBeTruthy();
    expect(screen.queryByText(/Reconnecting/)).toBeNull();
  });
});

describe('a connection that dropped', () => {
  it('says it is reconnecting, then keeps its lines and adds only the new ones', () => {
    let request = 0;
    server = (source) => {
      request += 1;
      if (request === 1) {
        // More than the tail a reconnect replays, as a long follow holds.
        source.send({ type: 'line', text: 'older than the tail' });
        lines(source, 3);
        source.finish();
        return;
      }
      // The replay is the tail: it repeats the last three lines held, then what
      // was printed in the gap; and the connection stays open, as a follow does.
      lines(source, 3);
      source.send({ type: 'line', text: 'printed in the gap' });
    };
    mount();
    act(() => {
      vi.advanceTimersByTime(200);
    });
    expect(rows()).toBe(4);
    expect(screen.getByText(/Reconnecting in 1 s \(attempt 1 of 5\)/)).toBeTruthy();
    expect(probe.api?.state.tabs[0]?.live).toBe(false);

    seconds(3);
    expect(requests).toHaveLength(2);
    // The scrollback older than the tail is kept, and the replayed lines it
    // already held are not appended again.
    expect(texts()).toEqual(['older than the tail', 'line 0', 'line 1', 'line 2', 'printed in the gap']);
    expect(screen.queryByText(/went away/)).toBeNull();
    expect(probe.api?.state.tabs[0]?.live).toBe(true);
  });

  it('says so when the re-read does not continue what it held', () => {
    let request = 0;
    server = (source) => {
      request += 1;
      if (request === 1) {
        source.send({ type: 'line', text: 'before the restart' });
        source.finish();
        return;
      }
      source.send({ type: 'line', text: 'after the restart' });
    };
    mount();
    seconds(3);

    expect(texts()).toEqual(['after the restart']);
    expect(screen.getByText(/earlier lines were discarded/)).toBeTruthy();
  });

  it('gives up after its attempts and says only that the connection went', () => {
    server = (source) => source.finish();
    mount();
    seconds(120);

    expect(requests).toHaveLength(6);
    expect(screen.getByText(/The connection to the stream went away\. —/)).toBeTruthy();
    expect(screen.queryByText(/Reconnecting/)).toBeNull();
    expect(probe.api?.state.tabs[0]?.live).toBe(false);
  });
});

/**
 * A released handle lingers for two seconds (`live/handles.ts`), so the old
 * stream outlives a key change. Before its callbacks were bound to their key, a
 * reconnect it had scheduled emptied the new container's pane and refilled it
 * with the old container's tail.
 */
describe('a stream the pane has moved away from', () => {
  it('cannot write into the pane after a container switch, nor reconnect', () => {
    server = (source) => {
      if (containerOf(source.url) === 'b') {
        source.send({ type: 'line', text: 'from b' });
        return;
      }
      source.send({ type: 'line', text: 'from a' });
      source.finish();
    };
    mount();
    act(() => {
      vi.advanceTimersByTime(200);
    });
    expect(screen.getByText(/Reconnecting in 1 s/)).toBeTruthy();

    act(() => {
      probe.api?.dispatch({ type: 'setContainer', id: tabId(), container: 'b', previous: false });
    });
    seconds(5);

    expect(texts()).toEqual(['from b']);
    // The old container's stream asked once and never again.
    expect(requests.filter((url) => containerOf(url) !== 'b')).toHaveLength(1);
    expect(screen.queryByText(/Reconnecting/)).toBeNull();
    expect(probe.api?.state.tabs[0]?.live).toBe(true);
  });

  it('makes no further request once its tab is closed', () => {
    server = (source) => source.finish();
    mount();
    act(() => {
      vi.advanceTimersByTime(200);
    });
    act(() => {
      probe.api?.dispatch({ type: 'close', id: tabId() });
    });
    seconds(30);

    expect(requests).toHaveLength(1);
  });

  it('does not carry "End of log" over to the next stream', () => {
    server = (source) => {
      if (containerOf(source.url) === 'b') {
        source.send({ type: 'line', text: 'from b' });
        return;
      }
      source.send({ type: 'end' });
      source.finish();
    };
    mount();
    seconds(1);
    expect(screen.getByText(/End of log\./)).toBeTruthy();

    act(() => {
      probe.api?.dispatch({ type: 'setContainer', id: tabId(), container: 'b', previous: false });
    });
    seconds(1);
    expect(texts()).toEqual(['from b']);
    expect(screen.queryByText(/End of log/)).toBeNull();
  });
});

describe('the cap', () => {
  it('holds at most its cap and says that earlier lines were let go', () => {
    server = (source) => lines(source, 40);
    mount();
    seconds(1);

    expect(rows()).toBeLessThanOrEqual(CAP);
    expect(screen.getByText(/keeps only its newest 25 lines/)).toBeTruthy();
    // The live region is the sentence, not the count that changes on each trim.
    const region = screen.getAllByRole('status').find((one) => /keeps only/.test(one.textContent ?? ''));
    expect(region?.textContent).not.toMatch(/so far/);
    // Paging backwards is not offered: the buffer no longer starts where the
    // stream did.
    expect(screen.queryByRole('button', { name: 'Read earlier lines' })).toBeNull();
  });
});
