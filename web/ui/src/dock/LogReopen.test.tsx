import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { useEffect, type ReactNode } from 'react';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { LogEvent } from '../api';
import { resetLiveHandles } from '../live/handles';
import { Dock } from './Dock';
import { DockProvider, useDock, type DockApi } from './DockContext';

/**
 * A log panel re-opened by its own toggles.
 *
 * Time and Prev run each re-open the stream, and the tooltip promises that
 * *"the buffer starts again from the tail"*. The hazard is the grace period in
 * `live/handles.ts`: a toggle pressed back within two seconds would find the
 * previous read's handle still lingering and take it back instead of opening a
 * new one, so the pane's `open` - the one place the buffer is emptied - would
 * never run, and what the buffer held for the other read would stay in front
 * of the stream, the same line repeated with identical nanosecond stamps. Every assertion here is "each line once, and the lines of the read on
 * screen".
 *
 * The fake `EventSource` answers each request as `/api/logs` would for a
 * crash-looping container: the tail of the current run, stamped when the
 * request asks for stamps, still open (a follow); or the previous run, whole,
 * then `end`.
 */

const requests: string[] = [];

/** The two runs of the container, as the kubelet holds them. Same text in both,
 *  which is what a crash loop looks like and why a text-only comparison could
 *  never tell them apart. */
const CURRENT = [
  { at: '2026-10-10T10:00:03.000000001Z', text: 'starting api v2.1.0' },
  { at: '2026-10-10T10:00:03.000000002Z', text: 'listening on :8080' },
  { at: '2026-10-10T10:00:04.000000003Z', text: 'panic: no database' },
];
const PREVIOUS = [
  { at: '2026-10-10T09:59:58.000000001Z', text: 'starting api v2.1.0' },
  { at: '2026-10-10T09:59:58.000000002Z', text: 'listening on :8080' },
  { at: '2026-10-10T09:59:59.000000003Z', text: 'panic: no database' },
];

/**
 * The current run as the n-th request reads it: the same three lines, and then
 * one the container printed while that read was open. That last line is what
 * tells two reads apart, so a pane that re-attached to an old read instead of
 * reading again shows the old read's line and fails here - which a tail of
 * identical lines on its own never could.
 */
function current(read: number): { at: string; text: string }[] {
  return [...CURRENT, { at: `2026-10-10T10:00:05.00000000${read}Z`, text: `request ${read} still following` }];
}

/** What the pane should draw for the n-th read of the current run. */
function drawnCurrent(read: number, stamped: boolean): string[] {
  return current(read).map((line) => (stamped ? `${line.at}${line.text}` : line.text));
}

class FakeEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;

  url: string;
  readyState = FakeEventSource.CONNECTING;
  onmessage: ((event: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;

  constructor(url: string) {
    this.url = url;
    requests.push(url);
    this.read = requests.length;
    setTimeout(() => {
      this.answer();
    }, 0);
  }

  close(): void {
    this.readyState = FakeEventSource.CLOSED;
  }

  private send(event: LogEvent): void {
    if (this.readyState === FakeEventSource.CLOSED) return;
    this.readyState = FakeEventSource.OPEN;
    this.onmessage?.({ data: JSON.stringify(event) });
  }

  private answer(): void {
    const query = new URL(this.url, 'http://x').searchParams;
    const stamped = query.get('timestamps') === 'true';
    const previous = query.get('previous') === 'true';
    for (const line of previous ? PREVIOUS : current(this.read)) {
      this.send({ type: 'line', text: line.text, ...(stamped ? { at: line.at } : {}) });
    }
    // A previous run is read whole and ends; the current run is followed and
    // stays open.
    if (previous) this.send({ type: 'end' });
  }

  /** Which request this is, counting from 1. Set in the constructor, after the
   *  push: a field initializer runs before it. */
  private read = 0;
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
      new Response(
        JSON.stringify({ state: 'ok', items: [{ name: 'api', init: false, ready: false, restarts: 4 }] }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
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
    probe.api?.openLogs({ cluster: 'prod', kind: 'pods', namespace: 'demo', name: 'crashing-api' });
  });
}

/** A moment, well inside the two-second grace period of `live/handles.ts`, so
 *  the panel's 120 ms repaint runs and the fake answers. */
function moment(): void {
  act(() => {
    vi.advanceTimersByTime(300);
  });
}

/** Each drawn row, stamp included where one is drawn. */
function drawn(): string[] {
  return Array.from(document.querySelectorAll('[data-line]'), (row) => row.textContent ?? '');
}

function press(label: string): void {
  fireEvent.click(screen.getByRole('button', { name: label }));
  moment();
}

/** Time is behind the toolbar's `⋯`: open it, and choose. */
function pressInMenu(label: string): void {
  fireEvent.click(screen.getByRole('button', { name: 'More log options' }));
  fireEvent.click(screen.getByRole('menuitemcheckbox', { name: label }));
  moment();
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  requests.length = 0;
  vi.stubGlobal('EventSource', FakeEventSource);
});

afterEach(() => {
  probe.api = null;
  resetLiveHandles();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('a log panel re-opened by its own toggles', () => {
  it('shows each line once after three Time toggles, each inside the grace period', async () => {
    mount();
    moment();
    await waitFor(() => {
      expect(drawn()).toEqual(drawnCurrent(1, false));
    });

    // Each toggle is a new read, so each draws that read's lines once - and
    // not the lines of the read two presses ago, which is still lingering.
    pressInMenu('Time'); // on
    expect(drawn()).toEqual(drawnCurrent(2, true));
    pressInMenu('Time'); // off
    expect(drawn()).toEqual(drawnCurrent(3, false));
    pressInMenu('Time'); // on
    expect(drawn()).toEqual(drawnCurrent(4, true));
  });

  it('shows each line once after Prev run is turned on and off twice', async () => {
    mount();
    moment();
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Prev run' }).hasAttribute('disabled')).toBe(false);
    });
    pressInMenu('Time'); // stamps on, so a line that appears twice is the same instant twice

    for (let round = 0; round < 2; round++) {
      press('Prev run'); // the previous run, read whole
      expect(drawn()).toEqual(PREVIOUS.map((line) => `${line.at}${line.text}`));
      press('Prev run'); // back to following the current run
      // Requests: 1 the first read, 2 Time, then a previous and a current per
      // round.
      expect(drawn()).toEqual(drawnCurrent(4 + 2 * round, true));
    }
  });

  it('asks the server again on every re-open, rather than re-attaching to the read it left', async () => {
    mount();
    moment();
    await waitFor(() => {
      expect(requests).toHaveLength(1);
    });
    pressInMenu('Time');
    pressInMenu('Time');
    pressInMenu('Time');
    // One request per read: the first, and one per toggle. A re-attached
    // handle is a read the tooltip says happened and did not.
    expect(requests).toHaveLength(4);
  });
});
