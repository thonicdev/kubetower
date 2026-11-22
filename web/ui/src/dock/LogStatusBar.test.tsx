import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { useEffect, type ReactNode } from 'react';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { LogEvent } from '../api';
import { resetLiveHandles } from '../live/handles';
import { Dock } from './Dock';
import { DockProvider, useDock, type DockApi } from './DockContext';
import { LogStatusBar } from './LogStatusBar';

describe('the status bar', () => {
  const base = { following: true, ended: false, fresh: 0, lines: 1234, onJump: null };

  it('says Following with the line count and the time window', () => {
    render(
      <LogStatusBar {...base} first="2026-11-17T10:00:01Z" last="2026-11-17T10:05:09Z" />,
    );
    expect(screen.getByText('Following')).toBeTruthy();
    expect(screen.getByText('1,234 lines')).toBeTruthy();
    expect(screen.getByText('10:00:01–10:05:09')).toBeTruthy();
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('says Paused with the new lines and offers the way back', () => {
    const jump = vi.fn();
    render(<LogStatusBar {...base} following={false} fresh={42} onJump={jump} />);
    expect(screen.getByText('Paused')).toBeTruthy();
    expect(screen.getByText('+42 new')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: '↓ Jump to latest' }));
    expect(jump).toHaveBeenCalledOnce();
  });

  it('says Ended, not Following, once the stream is over', () => {
    render(<LogStatusBar {...base} ended />);
    expect(screen.getByText('Ended')).toBeTruthy();
    expect(screen.queryByText('Following')).toBeNull();
  });
});

const probe: { api: DockApi | null } = { api: null };
function Probe(): ReactNode {
  const api = useDock();
  useEffect(() => {
    probe.api = api;
  }, [api]);
  return null;
}
const streams: { onmessage: ((event: { data: string }) => void) | null }[] = [];
const emit = (event: LogEvent): void => {
  act(() => {
    streams[streams.length - 1]?.onmessage?.({ data: JSON.stringify(event) });
  });
};
const repaint = (): void => {
  act(() => {
    vi.advanceTimersByTime(200);
  });
};

function mountPanel(): void {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  streams.length = 0;
  vi.stubGlobal(
    'EventSource',
    class {
      onmessage: ((event: { data: string }) => void) | null = null;
      onerror: (() => void) | null = null;
      constructor() {
        streams.push(this);
      }
      close(): void {}
    },
  );
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
            <Probe />
            <Dock />
          </DockProvider>
        ),
      },
    ],
    { initialEntries: ['/c/prod'] },
  );
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  act(() => {
    probe.api?.openLogs({ cluster: 'prod', kind: 'pods', namespace: 'default', name: 'api-1' });
  });
}

/** The scroller, with the geometry jsdom does not have. */
function scroller(): HTMLElement {
  const element = document.querySelector<HTMLElement>('.overflow-auto.font-mono');
  if (element === null) throw new Error('no log scroller');
  Object.defineProperty(element, 'scrollHeight', { configurable: true, value: 1000 });
  Object.defineProperty(element, 'clientHeight', { configurable: true, value: 100 });
  return element;
}

describe('the status bar in the log panel', () => {
  beforeEach(mountPanel);
  afterEach(() => {
    probe.api = null;
    resetLiveHandles();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('follows, pauses off the bottom with the lines that came since, and resumes on Jump', () => {
    emit({ type: 'line', text: 'one' });
    emit({ type: 'line', text: 'two' });
    repaint();
    const host = scroller();
    expect(screen.getByText('Following')).toBeTruthy();
    expect(screen.queryByRole('button', { name: /Jump to latest/ })).toBeNull();

    host.scrollTop = 300;
    fireEvent.scroll(host);
    expect(screen.getByText('Paused')).toBeTruthy();
    expect(screen.queryByText(/new$/)).toBeNull();

    emit({ type: 'line', text: 'three' });
    emit({ type: 'line', text: 'four' });
    emit({ type: 'line', text: 'five' });
    repaint();
    expect(screen.getByText('+3 new')).toBeTruthy();
    expect(screen.getByText('5 lines')).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: /Jump to latest/ }));
    expect(host.scrollTop).toBe(1000);
    expect(screen.getByText('Following')).toBeTruthy();
    expect(screen.queryByText(/new$/)).toBeNull();
    expect(screen.queryByRole('button', { name: /Jump to latest/ })).toBeNull();
  });

  it('is following again once the reader scrolls back to the bottom', () => {
    emit({ type: 'line', text: 'one' });
    repaint();
    const host = scroller();
    host.scrollTop = 300;
    fireEvent.scroll(host);
    expect(screen.getByText('Paused')).toBeTruthy();
    host.scrollTop = 900;
    fireEvent.scroll(host);
    expect(screen.getByText('Following')).toBeTruthy();
  });
});
