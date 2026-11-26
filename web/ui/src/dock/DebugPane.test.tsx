import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { StrictMode, useEffect, type ReactNode } from 'react';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';

import { knownNoShell } from '../debug/noShellMemory';
import { resetLiveHandles } from '../live/handles';
import { stubFetch, type StubRoutes } from '../testing/harness';
import { patientWithALoadedRunner } from '../testing/loadedRunner';
import { Dock } from './Dock';
import { DockProvider, useDock, type DockApi } from './DockContext';
import { terminalChunk } from './TerminalPane';

/**
 * A debug session in the dock (#147): the channel it opens, the refusals it
 * draws by code, a pod shell that found no shell offering Debug, and a reload
 * that must never run it again by itself - each run adds a container to the
 * pod that nothing can remove.
 */

patientWithALoadedRunner();

beforeAll(async () => {
  await terminalChunk.real();
});

const probe: { api: DockApi | null } = { api: null };

function Probe(): ReactNode {
  const api = useDock();
  useEffect(() => {
    probe.api = api;
  }, [api]);
  return null;
}

interface FakeSocket {
  url: string;
  onmessage: ((event: { data: string }) => void) | null;
  onclose: (() => void) | null;
}

const sockets: FakeSocket[] = [];

async function socket(index: number): Promise<FakeSocket> {
  return waitFor(() => {
    const found = sockets[index];
    if (found === undefined) throw new Error(`no socket ${String(index)} yet`);
    return found;
  });
}

function mount(): void {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
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
    <StrictMode>
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </StrictMode>,
  );
}

const POD = '/api/c/prod/r/pods/pay/api-0';
const running = { state: 'running', ready: true, restarts: 0 };

function routes(debug: unknown): StubRoutes {
  return {
    '/api/capabilities': {
      body: {
        mode: 'desktop',
        localShell: true,
        podExec: true,
        nodeShell: false,
        assistant: false,
        portForward: true,
        readOnly: { writesRefused: false, detailWritesRefused: false, measured: true },
      },
    },
    [POD]: {
      body: {
        state: 'ok',
        data: {
          kind: 'pods',
          namespace: 'pay',
          name: 'api-0',
          title: 'Pod',
          tabs: [],
          sections: [],
          containers: [
            { name: 'web', image: 'shop/web:1', init: false, status: running },
            { name: 'envoy', image: 'envoyproxy/envoy:v1.31.2', init: false, status: running },
          ],
          ...(debug === undefined ? {} : { debug }),
        },
      },
    },
  };
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  sockets.length = 0;
  sessionStorage.clear();
  vi.stubGlobal(
    'WebSocket',
    class {
      static readonly CONNECTING = 0;
      static readonly OPEN = 1;
      readyState = 0;
      onopen: (() => void) | null = null;
      onmessage: ((event: { data: string }) => void) | null = null;
      onerror: (() => void) | null = null;
      onclose: (() => void) | null = null;
      url: string;
      constructor(url: string) {
        this.url = url;
        sockets.push(this as unknown as FakeSocket);
      }
      send(): void {}
      close(): void {}
    },
  );
  vi.stubGlobal('matchMedia', () => ({
    matches: false,
    media: '',
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
    onchange: null,
  }));
  vi.stubGlobal(
    'ResizeObserver',
    class {
      observe(): void {}
      unobserve(): void {}
      disconnect(): void {}
    },
  );
  stubFetch(routes({ permitted: true }));
});

afterEach(() => {
  probe.api = null;
  resetLiveHandles();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  sessionStorage.clear();
});

function openDebug(target?: string): void {
  act(() => {
    probe.api?.openShell({
      kind: 'debug',
      cluster: 'prod',
      label: 'debug · api-0 · busybox',
      namespace: 'pay',
      name: 'api-0',
      image: 'busybox:1.36',
      ...(target === undefined ? {} : { target }),
    });
  });
}

function refuse(on: FakeSocket, code: string, message: string): void {
  act(() => {
    on.onmessage?.({ data: JSON.stringify({ type: 'state', state: 'forbidden', reason: { code, message } }) });
  });
}

describe('a debug session in the dock', () => {
  it('opens on the debug channel with the image and the target', async () => {
    mount();
    openDebug('web');
    const url = new URL((await socket(0)).url);
    expect(url.pathname).toBe('/api/c/prod/ws/debug');
    expect(Object.fromEntries(url.searchParams)).toEqual({ namespace: 'pay', pod: 'api-0', image: 'busybox:1.36', target: 'web' });
    // Its tab wears the bug, not a shell's `❯`.
    expect(screen.getByRole('tab').querySelector('use')?.getAttribute('href')).toBe('#i-debug');
  });

  it.each([
    ['verb_denied', 'not permitted'],
    ['pod_security', 'Refused by Pod Security'],
    ['admission_denied', 'refused by an admission policy'],
    ['not_supported_here', 'no ephemeral containers on this cluster'],
    ['container_not_started', 'debug container did not start'],
    ['timeout', 'debug container not running after 2 min'],
    ['no_such_object', 'pod or container not found'],
    ['no_shell', 'no shell in this image'],
  ])('says %s in a few words, with the server\'s sentence on hover', async (code, word) => {
    mount();
    openDebug();
    refuse(await socket(0), code, `the server's sentence for ${code}`);
    const note = screen.getByText(new RegExp(word));
    expect(note.getAttribute('title')).toBe(`the server's sentence for ${code}`);
  });

  it('draws the sentence whole for a code it has no words for', async () => {
    mount();
    openDebug();
    refuse(await socket(0), 'unclassified', 'the debug container was not added: boom');
    expect(screen.getByText(/the debug container was not added: boom/)).toBeTruthy();
  });

  it('says it was not opened when the socket closes before any answer', async () => {
    // A read-only cluster refuses `ws/debug` with 403 before the upgrade,
    // which a browser reports as a bare close.
    mount();
    openDebug();
    const gone = await socket(0);
    act(() => {
      gone.onclose?.();
    });
    const note = screen.getByText(/not opened/);
    expect(note.getAttribute('title')).toContain('read-only');
  });
});

describe('a pod shell that found no shell', () => {
  function openExec(): void {
    act(() => {
      probe.api?.openShell({
        kind: 'exec', cluster: 'prod', label: 'api-0/web', namespace: 'pay', name: 'api-0', container: 'web', image: 'shop/web:1',
      });
    });
  }

  it('offers Debug on that container, remembers the fact, and Start opens a debug tab', async () => {
    mount();
    openExec();
    refuse(await socket(0), 'no_shell', 'executable file not found in $PATH');
    expect(screen.getByText(/no shell in this image/)).toBeTruthy();
    expect(knownNoShell({ cluster: 'prod', namespace: 'pay', pod: 'api-0', container: 'web', image: 'shop/web:1' })).toBe(true);

    const debug = await screen.findByRole('button', { name: 'Debug' });
    expect(debug).toHaveProperty('disabled', false);
    fireEvent.click(debug);
    const pressed = [...document.querySelectorAll('[data-debug-form] button[aria-pressed="true"]')].map((one) => one.textContent);
    expect(pressed).toEqual(['busybox', 'web']);
    fireEvent.click(screen.getByRole('button', { name: 'Start' }));

    const url = new URL((await socket(1)).url);
    expect(url.pathname).toBe('/api/c/prod/ws/debug');
    expect(url.searchParams.get('target')).toBe('web');
  });

  it('grays Debug with the reason where the page refuses it', async () => {
    stubFetch(routes({ permitted: false, reason: { code: 'verb_denied', message: 'prod is marked read-only' } }));
    mount();
    openExec();
    refuse(await socket(0), 'no_shell', 'executable file not found in $PATH');
    const debug = await screen.findByRole('button', { name: 'Debug' });
    expect(debug).toHaveProperty('disabled', true);
    expect(debug.getAttribute('title')).toBe('prod is marked read-only');
  });

  it('offers nothing where the page withholds Debug', async () => {
    stubFetch(routes(undefined));
    mount();
    openExec();
    refuse(await socket(0), 'no_shell', 'executable file not found in $PATH');
    await act(async () => {
      await vi.advanceTimersByTimeAsync(200);
    });
    expect(screen.queryByRole('button', { name: 'Debug' })).toBeNull();
  });

  it('does not offer Debug on any other refusal', async () => {
    mount();
    openExec();
    refuse(await socket(0), 'verb_denied', 'prod is marked read-only');
    await act(async () => {
      await vi.advanceTimersByTimeAsync(200);
    });
    expect(screen.queryByRole('button', { name: 'Debug' })).toBeNull();
    // An exec refusal keeps the server's sentence whole, as before.
    expect(screen.getByText(/prod is marked read-only/)).toBeTruthy();
  });
});

describe('a debug tab across a reload', () => {
  function reload(): void {
    cleanup();
    resetLiveHandles();
    probe.api = null;
    mount();
  }

  it('comes back ended and never runs again by itself; only Start again does', async () => {
    mount();
    openDebug('web');
    await socket(0);

    for (let i = 0; i < 3; i++) {
      reload();
      await act(async () => {
        await vi.advanceTimersByTimeAsync(300);
      });
    }
    expect(sockets).toHaveLength(1);
    expect(probe.api?.state.tabs[0]).toMatchObject({ kind: 'debug', live: false, dormant: true, image: 'busybox:1.36', target: 'web' });
    expect(screen.getByText(/ended with the reload/)).toBeTruthy();

    act(() => {
      screen.getByRole('button', { name: 'Start again' }).click();
    });
    const again = new URL((await socket(1)).url);
    expect(again.pathname).toBe('/api/c/prod/ws/debug');
    expect(again.searchParams.get('target')).toBe('web');
  });
});
