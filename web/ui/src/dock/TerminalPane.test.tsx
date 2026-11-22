/// <reference types="node" />
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';

import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { StrictMode, useEffect, type ReactNode } from 'react';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';

import { liveHandleKeys, resetLiveHandles } from '../live/handles';
import { patientWithALoadedRunner } from '../testing/loadedRunner';
import { Dock } from './Dock';
import { DockProvider, useDock, type DockApi } from './DockContext';
import { terminalChunk, TERMINAL_FONT } from './TerminalPane';

/**
 * The terminal pane, and the two things a test in jsdom can actually establish.
 *
 * ## What this proves
 *
 * **That one tab opens one session, under StrictMode.** That is the trap that
 * costs half a day: React 19
 * double-invokes effects in development, so a terminal opened in a bare
 * `useEffect` opens two sockets and two pseudo-terminals - and
 * `internal/terminal/limit.go` caps live sessions at sixteen for the whole
 * process, so it presents as a session limit reached at half the sessions, in
 * development only. Every test below therefore renders inside `<StrictMode>`,
 * because a test that does not is a test that cannot see it.
 *
 * And **that two shells on one pod are two sessions.** The handle key contains
 * `tab.seq`; without it the deduplication finds the first tab's handle and
 * defeats the counter that exists to keep them apart, so the operator's second
 * shell is the first shell.
 *
 * ## What it cannot prove, and is not written as though it does
 *
 * Anything about xterm. jsdom lays nothing out, reports every dimension as zero,
 * and implements neither `ResizeObserver` nor the canvas the renderer measures
 * with - so the fit, the cursor, the colors and the scrollback are a browser's
 * job and nothing here speaks to them.
 *
 * ## Why every socket is awaited
 *
 * The emulator is a chunk of its own, asked for inside the handle's `open`:
 * the socket is constructed *after* `import()` has
 * resolved, so it is not there on the line after the click. The awaits below
 * are that import landing, and a test that read `sockets` synchronously would
 * pass for a pane that opened nothing.
 */

// Every test here mounts xterm in jsdom and waits two or three times for it.
// The file has taken 20 s and 40 s on a loaded CI runner against a few seconds
// idle, and its tests ran past vitest's 5 s.
patientWithALoadedRunner();

// The emulator's chunk, compiled once before the first test rather than inside
// that test's wait for its socket. Tests still reach it through
// `terminalChunk.load` and its `import()`, which now finds the module there.
beforeAll(async () => {
  await terminalChunk.real();
});

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

/** Every socket the pane opened, as the instance itself, so a test can both
 *  read what was asked for and deliver a frame back into it. */
interface FakeSocket {
  url: string;
  closes: number;
  readyState: number;
  onopen: (() => void) | null;
  onmessage: ((event: { data: string }) => void) | null;
  onclose: (() => void) | null;
}

const sockets: FakeSocket[] = [];
/** Every frame any socket sent, in order. */
const sent: string[] = [];

/** The n-th socket the pane opened, once the emulator's chunk has landed and
 *  the pane has got as far as opening it. */
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
            <Probe take={take} />
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

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  sockets.length = 0;
  sent.length = 0;
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
      closes = 0;
      constructor(url: string) {
        this.url = url;
        sockets.push(this as unknown as FakeSocket);
      }
      send(frame: string): void {
        sent.push(frame);
      }
      close(): void {
        this.closes += 1;
      }
    },
  );
  // Two things xterm needs that jsdom does not have, and neither stub makes this
  // test say anything about the terminal - they only stop it throwing before it
  // reaches what it does measure. `matchMedia` is how xterm watches the device
  // pixel ratio; `ResizeObserver` is what the pane watches its host with, because
  // the panel is resized by its own grip and by folding, neither of which resizes
  // the window.
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
});

afterEach(() => {
  probe.api = null;
  terminalChunk.load = terminalChunk.real;
  resetLiveHandles();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('a shell in the dock', () => {
  it('opens one session per tab under StrictMode', async () => {
    mount();
    act(() => {
      probe.api?.openShell({ kind: 'terminal', cluster: 'prod', label: 'prod' });
    });

    // One, not two. The second mount finds the first handle inside its grace
    // period; without `live/handles.ts` this is two sockets and two PTYs, and the
    // sixteen-session cap is reached at eight.
    await waitFor(() => {
      expect(sockets).toHaveLength(1);
    });
    expect(liveHandleKeys()).toHaveLength(1);
  });

  it('points the cluster shell at the channel the contract advertises', async () => {
    mount();
    act(() => {
      probe.api?.openShell({ kind: 'terminal', cluster: 'prod', label: 'prod' });
    });

    const url = new URL((await socket(0)).url);
    expect(url.pathname).toBe('/api/c/prod/ws/terminal');
    // Same origin, so the session cookie is attached and `connect-src 'self'`
    // covers it. A socket at another origin would carry no session at all.
    expect(url.host).toBe(window.location.host);
  });

  it('opens a pod shell on exec and the assistant on its own address', async () => {
    mount();
    act(() => {
      probe.api?.openShell({
        kind: 'exec', cluster: 'prod', label: 'api-1',
        namespace: 'default', name: 'api-1', container: 'app',
      });
    });
    const exec = new URL((await socket(0)).url);
    expect(exec.pathname).toBe('/api/c/prod/ws/exec');
    expect(exec.searchParams.get('pod')).toBe('api-1');
    expect(exec.searchParams.get('container')).toBe('app');

    act(() => {
      probe.api?.openShell({ kind: 'claude', cluster: 'prod', label: 'assistant' });
    });
    // Not under /c/{cluster}: it is a control channel rather than a cluster read,
    // and `x-channels` addresses it at /api/ws/assistant.
    expect(new URL((await socket(1)).url).pathname).toBe('/api/ws/assistant');
  });

  it('carries the plan token on a node shell, and no node', async () => {
    mount();
    act(() => {
      probe.api?.openShell({
        kind: 'nodeshell', cluster: 'prod', label: 'node-3', token: 'abc123',
      });
    });
    const url = new URL((await socket(0)).url);
    expect(url.pathname).toBe('/api/c/prod/ws/nodeshell');
    expect(url.searchParams.get('token')).toBe('abc123');
    // The server re-derives the node from the plan. Nothing here can name a
    // different one, which is what binds the confirmation to what happens.
    expect(url.searchParams.get('node')).toBeNull();
  });

  it('gives two shells on one pod two sessions', async () => {
    // The counter the tab id carries is defeated by the deduplication unless the
    // handle key carries it too - which is the one line of `live/handles.ts`'s
    // documentation that is easy to leave out, and the one whose absence looks
    // like a feature (the second click "reuses" the first shell).
    mount();
    act(() => {
      probe.api?.openShell({
        kind: 'exec', cluster: 'prod', label: 'api-1', namespace: 'default', name: 'api-1',
      });
      probe.api?.openShell({
        kind: 'exec', cluster: 'prod', label: 'api-1', namespace: 'default', name: 'api-1',
      });
    });

    await waitFor(() => {
      expect(sockets).toHaveLength(2);
    });
    expect(new Set(liveHandleKeys()).size).toBe(2);
  });

  it('keeps a hidden tab running', async () => {
    // The single promise the whole partition rests on: a `kubectl get pods -w`
    // left on one cluster is still running, with its scrollback, when the
    // operator comes back on Friday. Only `hidden` moves, and nothing on the
    // hiding path disposes anything.
    mount();
    act(() => {
      probe.api?.openShell({ kind: 'terminal', cluster: 'prod', label: 'first' });
      probe.api?.openShell({ kind: 'terminal', cluster: 'prod', label: 'second' });
    });
    await waitFor(() => {
      expect(sockets).toHaveLength(2);
    });
    expect(sockets[0]?.closes).toBe(0);
  });

  it('puts the keyboard in the terminal it opens from a button', async () => {
    // The Shell button keeps the focus after its press; without the pane
    // taking it, the first keys typed go nowhere.
    mount();
    const button = document.createElement('button');
    button.textContent = 'Shell';
    document.body.append(button);
    button.focus();
    act(() => {
      probe.api?.openShell({ kind: 'terminal', cluster: 'prod', label: 'prod' });
    });
    const area = await waitFor(() => {
      const found = document.querySelector<HTMLElement>('.xterm-helper-textarea');
      if (found === null) throw new Error('no terminal yet');
      return found;
    });
    await waitFor(() => {
      expect(document.activeElement).toBe(area);
    });
    button.remove();
  });

  it('draws the server’s own sentence when a session is refused', async () => {
    mount();
    act(() => {
      probe.api?.openShell({ kind: 'exec', cluster: 'prod', label: 'api-1' });
    });
    const refused = await socket(0);
    // The refusal arrives as a frame rather than as a red line inside the
    // terminal output, which is the whole reason this wire is framed: a client
    // can draw it, and can tell it from something a command printed.
    act(() => {
      refused.onmessage?.({
        data: JSON.stringify({
          type: 'state',
          state: 'forbidden',
          reason: { code: 'verb_denied', message: 'prod is marked read-only' },
        }),
      });
    });
    expect(screen.getByText(/prod is marked read-only/)).toBeTruthy();
  });

  // The server says `exit` before it closes, and the close
  // that follows must not overwrite it with the sentence kept for a socket the
  // network took. A bare close, with no frame ahead of it, still says that.
  it('says the session ended when the exit frame arrives ahead of the close', async () => {
    mount();
    act(() => {
      probe.api?.openShell({ kind: 'terminal', cluster: 'prod', label: 'prod' });
    });
    const ended = await socket(0);
    act(() => {
      ended.onmessage?.({ data: JSON.stringify({ type: 'exit' }) });
      ended.onclose?.();
    });
    expect(screen.getByText(/The session ended\./)).toBeTruthy();
    expect(screen.queryByText(/went away/)).toBeNull();
  });

  it('names the status a pod exec ended with', async () => {
    mount();
    act(() => {
      probe.api?.openShell({ kind: 'exec', cluster: 'prod', label: 'api-1' });
    });
    const ended = await socket(0);
    act(() => {
      ended.onmessage?.({ data: JSON.stringify({ type: 'exit', code: 3 }) });
      ended.onclose?.();
    });
    expect(screen.getByText(/The session ended \(3\)\./)).toBeTruthy();
  });

  it('says the connection went away when the socket closes with no frame', async () => {
    mount();
    act(() => {
      probe.api?.openShell({ kind: 'terminal', cluster: 'prod', label: 'prod' });
    });
    const gone = await socket(0);
    act(() => {
      gone.onclose?.();
    });
    expect(screen.getByText(/The connection to the session went away/)).toBeTruthy();
  });

  it('closes the socket and disposes the emulator when its tab is closed', async () => {
    // Closing a tab unmounts its pane, and the pane only releases the handle:
    // it is the handle's `dispose`, after the grace period, that closes the
    // socket and frees xterm. A tab closed with its socket still open is a session the server keeps counting
    // against the sixteen, with nothing on screen to close it from.
    const { Terminal } = await terminalChunk.real();
    const dispose = vi.spyOn(Terminal.prototype, 'dispose');
    try {
      mount();
      act(() => {
        probe.api?.openShell({ kind: 'terminal', cluster: 'prod', label: 'prod' });
      });
      const opened = await socket(0);
      // The emulator exists once the session has been shown on the pane.
      await waitFor(() => {
        expect(document.querySelector('.xterm')).not.toBeNull();
      });
      expect(liveHandleKeys()).toHaveLength(1);
      expect(opened.closes).toBe(0);

      fireEvent.click(screen.getByRole('button', { name: 'Close prod' }));
      // Past the handle's grace period, which spans a StrictMode remount.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2_500);
      });

      expect(opened.closes).toBe(1);
      expect(dispose).toHaveBeenCalledTimes(1);
      expect(liveHandleKeys()).toHaveLength(0);
    } finally {
      dispose.mockRestore();
    }
  });
});

/**
 * The chunk that does not arrive.
 *
 * A tab left open across an upgrade asks for a terminal chunk the server no
 * longer has, and unlike a page this import is not a route: `RouteError` never
 * sees it. So the pane does what the boundary does - one reload, through the
 * same guard - and, where that reload has already been spent, says what
 * happened rather than drawing an empty pane over a socket that was never
 * opened.
 */
describe('the emulator’s chunk does not arrive', () => {
  const MISSING = 'Failed to fetch dynamically imported module: http://console/assets/terminal-x1.js';

  beforeEach(() => {
    sessionStorage.clear();
    vi.stubGlobal('location', { host: window.location.host, reload: vi.fn() });
    terminalChunk.load = () => Promise.reject(new TypeError(MISSING));
  });

  afterEach(() => {
    sessionStorage.clear();
  });

  it('opens no socket and reloads the tab once', async () => {
    mount();
    act(() => {
      probe.api?.openShell({ kind: 'terminal', cluster: 'prod', label: 'prod' });
    });
    await waitFor(() => {
      expect(vi.mocked(window.location.reload).mock.calls).toHaveLength(1);
    });
    expect(sockets).toHaveLength(0);
  });

  it('says so, and does not reload again, when the reload has been spent', async () => {
    sessionStorage.setItem('kubetower.chunk-reloaded', '1');
    mount();
    act(() => {
      probe.api?.openShell({ kind: 'terminal', cluster: 'prod', label: 'prod' });
    });
    // By its text and not by `role="status"`: the dock has a live region of
    // its own, and the pane's note is the second such element in the tree.
    const note = await screen.findByText(/The terminal could not be loaded/);
    expect(note.textContent).toContain(MISSING);
    expect(vi.mocked(window.location.reload).mock.calls).toHaveLength(0);
    expect(sockets).toHaveLength(0);
  });
});

/**
 * The terminal's font and the theme's `--font-mono` are one ladder written
 * twice, and this is what keeps them one.
 *
 * They cannot be one spelling: xterm measures a cell on a canvas, and a canvas
 * resolves no CSS custom property - `ctx.font = '12.5px var(--font-mono)'` is an
 * invalid declaration, so the context silently keeps its own 10px sans-serif and
 * every column in the terminal is measured against a font nobody is drawing in.
 *
 * So the pane carries a literal, and this reads the stylesheet rather than
 * trusting a comment to be obeyed. What it asserts is **containment, not
 * equality**: the terminal is allowed to prefer a console face the rest of the
 * console has no use for, and is not allowed to drop the fallbacks that make it
 * land somewhere sensible where that face is absent.
 */
describe('the terminal font', () => {
  // The stylesheet is read rather than imported: `?raw` on a file Tailwind
  // itself processes gives back the processed sheet, where the token has been
  // consumed. `node:fs` and the walk to `go.mod` is the route `kindGlyph.test.ts`
  // already established for reading a source file from a test, with the same
  // cost - it reaches outside `web/`, and fails loudly rather than passing on an
  // empty read.
  const themeCss = (): string => {
    let at = resolve(process.cwd());
    for (;;) {
      if (existsSync(join(at, 'go.mod'))) break;
      const up = dirname(at);
      if (up === at) { at = resolve(process.cwd(), '..'); break; }
      at = up;
    }
    return readFileSync(join(at, 'web', 'app', 'theme.css'), 'utf8');
  };

  const declared = (): string => {
    const line = /--font-mono:\s*([^;]+);/.exec(themeCss());
    if (line === null) throw new Error('--font-mono is not in app/theme.css - has it been renamed?');
    return line[1] ?? '';
  };

  const families = (list: string): string[] =>
    list.split(',').map((one) => one.trim().replace(/^["']|["']$/g, ''));

  it('is a real stack and not a custom property', () => {
    // The defect in one assertion: a `var()` here reaches a canvas that cannot
    // read it.
    expect(TERMINAL_FONT).not.toContain('var(');
    expect(families(TERMINAL_FONT).at(-1)).toBe('monospace');
  });

  it('keeps every family the theme names, in the order the theme names them', () => {
    const theme = families(declared());
    const terminal = families(TERMINAL_FONT);

    for (const family of theme) {
      expect(terminal).toContain(family);
    }

    // Order matters as much as membership: a fallback that jumps ahead of the
    // face it is a fallback for is a different rendering on every machine.
    const kept = terminal.filter((one) => theme.includes(one));
    expect(kept).toEqual(theme);
  });
});

/**
 * A reload, and a second browser tab.
 *
 * Neither may open a session by itself: tabs in `localStorage`, which every
 * tab of the origin shares, would let a second browser tab open shells of its
 * own, and a restored shell tab that opened its session the moment it was
 * drawn would open one on every reload. Each of those holds one of the
 * server's sixteen slots.
 *
 * jsdom cannot reload a page or open a second tab, but the dock cannot tell
 * either from this: the tree unmounted, the live registry gone with the page,
 * and the tree mounted again. A reload keeps `sessionStorage`; a second tab
 * starts with its own, empty, and shares only `localStorage`.
 */
describe('a reload and a second browser tab', () => {
  function reload(): void {
    cleanup();
    resetLiveHandles();
    probe.api = null;
    mount();
  }

  function secondTab(): void {
    cleanup();
    resetLiveHandles();
    probe.api = null;
    sessionStorage.clear();
    mount();
  }

  beforeEach(() => {
    sessionStorage.clear();
  });

  it('opens one session across five reloads, and offers the shell back instead', async () => {
    mount();
    act(() => {
      probe.api?.openShell({ kind: 'terminal', cluster: 'prod', label: 'prod' });
    });
    await socket(0);

    for (let i = 0; i < 5; i++) {
      reload();
      // Long enough for a restored tab's emulator chunk to land and its
      // socket to open, were it going to - so each reload is a whole one.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(300);
      });
    }
    expect(sockets).toHaveLength(1);

    // The tab is back, and says what it is rather than being a shell.
    expect(screen.getAllByRole('tab')).toHaveLength(1);
    expect(probe.api?.state.tabs[0]?.live).toBe(false);
    expect(screen.getByText(/was open before the page was reloaded/)).toBeTruthy();

    // One press is the operator asking, and only that opens a session.
    act(() => {
      screen.getByRole('button', { name: 'Open a new session' }).click();
    });
    await socket(1);
    expect(sockets).toHaveLength(2);
    expect(probe.api?.state.tabs[0]?.live).toBe(true);
  });

  it('opens nothing, and shows nothing, in a second browser tab', async () => {
    mount();
    act(() => {
      probe.api?.openShell({ kind: 'terminal', cluster: 'prod', label: 'prod' });
      probe.api?.openShell({ kind: 'claude', cluster: 'prod', label: 'Claude · prod' });
    });
    await socket(1);

    secondTab();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(500);
    });
    expect(sockets).toHaveLength(2);
    expect(probe.api?.state.tabs).toHaveLength(0);
  });
});

/**
 * Copying out of a terminal, through the real emulator. The branches are in `terminalClipboard.test.ts`; what
 * only a mounted pane can show is that the keys reach the right place - Ctrl+C
 * to the shell as an interrupt, Ctrl+Shift+C nowhere near it - and that a
 * right-click pastes into the session.
 *
 * jsdom gives xterm no selection to make, so the copy itself is asserted there
 * and not here.
 */
describe('the clipboard in a terminal', () => {
  /** What the session received as input, decoded out of its data frames. */
  function typed(): string {
    return sent
      .map((frame) => JSON.parse(frame) as { type: string; data?: string })
      .filter((frame) => frame.type === 'data' && frame.data !== undefined)
      .map((frame) => atob(frame.data ?? ''))
      .join('');
  }

  async function openShell(): Promise<HTMLElement> {
    mount();
    act(() => {
      probe.api?.openShell({ kind: 'terminal', cluster: 'prod', label: 'prod' });
    });
    const live = await socket(0);
    act(() => {
      live.readyState = 1;
      live.onopen?.();
    });
    return waitFor(() => {
      const area = document.querySelector<HTMLElement>('.xterm-helper-textarea');
      if (area === null) throw new Error('no terminal yet');
      return area;
    });
  }

  function chord(target: HTMLElement, key: string, shift: boolean): void {
    const init = { key, code: `Key${key.toUpperCase()}`, keyCode: key.toUpperCase().charCodeAt(0), ctrlKey: true, shiftKey: shift, bubbles: true, cancelable: true };
    act(() => {
      target.dispatchEvent(new KeyboardEvent('keydown', init));
      target.dispatchEvent(new KeyboardEvent('keyup', init));
    });
  }

  it('still sends Ctrl+C to the shell as an interrupt', async () => {
    const area = await openShell();
    chord(area, 'c', false);
    expect(typed()).toBe('\u0003');
  });

  it('keeps Ctrl+Shift+C out of the shell', async () => {
    const area = await openShell();
    chord(area, 'C', true);
    expect(typed()).toBe('');
  });

  /** The clipboard a right-click reads. Defined on the real `navigator` and
   *  not by replacing it: a spread copy has no `userAgent`, and xterm reads
   *  that when its module first loads, so a test that ran alone failed. */
  function clipboard(readText: () => Promise<string>): void {
    vi.stubGlobal('isSecureContext', true);
    Object.defineProperty(navigator, 'clipboard', { value: { readText }, configurable: true });
  }

  afterEach(() => {
    Reflect.deleteProperty(navigator, 'clipboard');
  });

  function host(): HTMLElement {
    const found = document.querySelector<HTMLElement>('.xterm')?.parentElement;
    if (found === null || found === undefined) throw new Error('no terminal host');
    return found;
  }

  function rightClick(): MouseEvent {
    const menu = new MouseEvent('contextmenu', { bubbles: true, cancelable: true });
    act(() => {
      host().dispatchEvent(menu);
    });
    return menu;
  }

  /** What the browser does on Ctrl+Shift+V: a `paste` event on xterm's
   *  textarea, carrying the clipboard. jsdom has no `ClipboardEvent`. */
  function pasteEvent(area: HTMLElement, text: string): void {
    const event = new Event('paste', { bubbles: true, cancelable: true });
    Object.defineProperty(event, 'clipboardData', {
      value: { getData: (type: string) => (type === 'text/plain' ? text : '') },
    });
    act(() => {
      area.dispatchEvent(event);
    });
  }

  /** The review's payload: three commands, one hidden behind an
   *  end-of-bracketed-paste sequence. */
  const PAYLOAD = 'kubectl get pods\nrm -rf /tmp/x\n\u001b[201~whoami\n';

  it('pastes the clipboard on a right-click with nothing selected, and draws no menu', async () => {
    clipboard(() => Promise.resolve('kubectl get pods'));
    await openShell();
    const menu = rightClick();
    expect(menu.defaultPrevented).toBe(true);
    await waitFor(() => {
      expect(typed()).toBe('kubectl get pods');
    });
  });

  it('asks before a right-click pastes several lines, and sends nothing until it is confirmed', async () => {
    clipboard(() => Promise.resolve(PAYLOAD));
    await openShell();
    rightClick();
    const confirm = await screen.findByRole('button', { name: 'Paste 3 lines' });
    expect(typed()).toBe('');

    // Refusing sends nothing.
    act(() => {
      screen.getByRole('button', { name: 'Cancel' }).click();
    });
    expect(screen.queryByRole('button', { name: 'Paste 3 lines' })).toBeNull();
    expect(typed()).toBe('');
    expect(confirm.isConnected).toBe(false);

    // Asked again and confirmed: the lines, without the escape sequence. The
    // question is awaited before it is answered - a `findBy*` left floating
    // inside `act` raced the `waitFor` below, and a timeout in it surfaced as
    // an unhandled rejection rather than as this test's failure.
    rightClick();
    const again = await screen.findByRole('button', { name: 'Paste 3 lines' });
    act(() => {
      again.click();
    });
    await waitFor(() => {
      expect(typed()).toBe('kubectl get pods\rrm -rf /tmp/x\rwhoami\r');
    });
    expect(typed()).not.toContain('\u001b');
  });

  it('strips an embedded end-of-paste sequence from a single line', async () => {
    clipboard(() => Promise.resolve('echo hi\u001b[201~'));
    await openShell();
    rightClick();
    await waitFor(() => {
      expect(typed()).toBe('echo hi');
    });
  });

  it('asks the same before Ctrl+Shift+V pastes several lines', async () => {
    const area = await openShell();
    pasteEvent(area, PAYLOAD);
    await screen.findByRole('button', { name: 'Paste 3 lines' });
    expect(typed()).toBe('');
    act(() => {
      screen.getByRole('button', { name: 'Paste 3 lines' }).click();
    });
    expect(typed()).toBe('kubectl get pods\rrm -rf /tmp/x\rwhoami\r');
  });

  it('pastes one line by Ctrl+Shift+V straight through, stripped', async () => {
    const area = await openShell();
    pasteEvent(area, 'echo hi\u001b[201~');
    expect(typed()).toBe('echo hi');
    expect(screen.queryByRole('button', { name: /^Paste \d+ lines$/ })).toBeNull();
  });

  it('says the browser refused, and not HTTPS, when reading is refused on a secure page', async () => {
    clipboard(() => Promise.reject(new DOMException('denied', 'NotAllowedError')));
    await openShell();
    rightClick();
    expect(await screen.findByText(/browser refused clipboard access/)).toBeTruthy();
    expect(screen.queryByText(/HTTPS/)).toBeNull();
    expect(typed()).toBe('');
  });

  /** Bytes from the session, as the server frames them. */
  function output(text: string): void {
    const live = sockets[0];
    if (live === undefined) throw new Error('no socket');
    act(() => {
      live.onmessage?.({ data: JSON.stringify({ type: 'data', data: btoa(text) }) });
    });
  }

  it('asks before one line ending in a newline runs (follow-up review)', async () => {
    const area = await openShell();
    pasteEvent(area, 'curl x | sh\r');
    await screen.findByRole('button', { name: 'Paste 1 line' });
    expect(typed()).toBe('');
  });

  it('asks for several lines even after the session switched bracketed paste on', async () => {
    // Anything the session prints can switch the mode on - a `cat` of a file,
    // a motd - and a plain `sh` knows nothing of brackets, so the mode is not
    // trusted to make a paste safe.
    const area = await openShell();
    output('$ \u001b[?2004h');
    // xterm parses what it is given on a timer, not inside `write`.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100);
    });
    pasteEvent(area, 'a\nb\nc');
    await screen.findByRole('button', { name: 'Paste 3 lines' });
    expect(typed()).toBe('');
  });

  it('is answered from the keyboard alone, and no key reaches the shell meanwhile', async () => {
    const area = await openShell();
    pasteEvent(area, PAYLOAD);
    const prompt = await screen.findByRole('alertdialog');
    const cancel = screen.getByRole('button', { name: 'Cancel' });
    // Cancel, the safe answer, has the focus.
    await waitFor(() => {
      expect(document.activeElement).toBe(cancel);
    });

    // Tab moves within the prompt and never reaches the shell.
    act(() => {
      fireEvent.keyDown(cancel, { key: 'Tab' });
    });
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Paste 3 lines' }));
    act(() => {
      fireEvent.keyDown(document.activeElement as HTMLElement, { key: 'Tab', shiftKey: true });
    });
    expect(document.activeElement).toBe(cancel);
    expect(typed()).toBe('');

    // Escape cancels, sends nothing, and gives the terminal its keys back.
    act(() => {
      fireEvent.keyDown(cancel, { key: 'Escape' });
    });
    expect(prompt.isConnected).toBe(false);
    expect(typed()).toBe('');
    expect(document.activeElement).toBe(area);

    // Asked again and confirmed from the keyboard: Tab to the paste button,
    // then activate it (jsdom does not turn Enter into a click; the browser
    // does, so the click stands for it).
    pasteEvent(area, PAYLOAD);
    await screen.findByRole('alertdialog');
    await waitFor(() => {
      expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Cancel' }));
    });
    act(() => {
      fireEvent.keyDown(document.activeElement as HTMLElement, { key: 'Tab' });
    });
    act(() => {
      (document.activeElement as HTMLElement).click();
    });
    expect(typed()).toBe('kubectl get pods\rrm -rf /tmp/x\rwhoami\r');
    expect(document.activeElement).toBe(area);
  });

  it('keeps keys typed at the terminal from the shell while the prompt is up', async () => {
    const area = await openShell();
    pasteEvent(area, PAYLOAD);
    await screen.findByRole('alertdialog');
    const press = (key: string): void => {
      const init = { key, code: key, bubbles: true, cancelable: true };
      act(() => {
        area.dispatchEvent(new KeyboardEvent('keydown', init));
        area.dispatchEvent(new KeyboardEvent('keyup', init));
      });
    };
    press('Tab');
    expect(typed()).toBe('');
    press('Escape');
    expect(typed()).toBe('');
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });

  it('refuses a second paste while the first is waiting, rather than swapping it in', async () => {
    const area = await openShell();
    pasteEvent(area, PAYLOAD);
    await screen.findByRole('button', { name: 'Paste 3 lines' });
    pasteEvent(area, 'single');
    pasteEvent(area, 'x\ny\n');
    expect(typed()).toBe('');
    expect(screen.getByText(/already waiting/)).toBeTruthy();
    act(() => {
      screen.getByRole('button', { name: 'Paste 3 lines' }).click();
    });
    expect(typed()).toBe('kubectl get pods\rrm -rf /tmp/x\rwhoami\r');
  });

  it('drops a waiting paste when the session ends', async () => {
    const area = await openShell();
    pasteEvent(area, PAYLOAD);
    await screen.findByRole('alertdialog');
    const live = sockets[0];
    act(() => {
      live?.onmessage?.({ data: JSON.stringify({ type: 'exit' }) });
    });
    expect(screen.queryByRole('alertdialog')).toBeNull();
    expect(typed()).toBe('');
  });

  it('says the browser does not offer reading, where readText is missing on a secure page', async () => {
    vi.stubGlobal('isSecureContext', true);
    Object.defineProperty(navigator, 'clipboard', { value: {}, configurable: true });
    await openShell();
    rightClick();
    expect(await screen.findByText(/does not let the page read the clipboard/)).toBeTruthy();
    expect(typed()).toBe('');
  });

  it('says to paste by key where the clipboard cannot be read, over plain HTTP', async () => {
    vi.stubGlobal('isSecureContext', false);
    await openShell();
    rightClick();
    expect(await screen.findByText(/only be read over HTTPS.*Paste with Ctrl\+Shift\+V instead/)).toBeTruthy();
    expect(typed()).toBe('');
  });

  /**
   * The footer says what a right-click does, and a copy is not silent.
   */
  it('says what a right-click and the keys do, in a footer line that stays', async () => {
    await openShell();
    expect(screen.getByText('Right-click: copy selection / paste · Ctrl+Shift+C / Ctrl+Shift+V')).toBeTruthy();
    // Still there after the session has had its say: it is permanent.
    act(() => {
      sockets[0]?.onmessage?.({ data: JSON.stringify({ type: 'exit' }) });
    });
    expect(screen.getByText(/^Right-click: copy selection \/ paste/)).toBeTruthy();
  });

  /**
   * jsdom gives xterm no selection to make with a pointer, so the emulator is
   * the real one with a selection already in it - through the chunk seam,
   * which is what it is for - and a clipboard that takes what it is given.
   */
  function selecting(): { written: string[]; cleared: ReturnType<typeof vi.fn> } {
    const cleared = vi.fn();
    terminalChunk.load = () =>
      terminalChunk.real().then((module) => ({
        ...module,
        Terminal: class extends module.Terminal {
          override hasSelection(): boolean {
            return true;
          }
          override getSelection(): string {
            return 'pod/api-1';
          }
          override clearSelection(): void {
            cleared();
          }
        },
      }));
    const written: string[] = [];
    vi.stubGlobal('isSecureContext', true);
    Object.defineProperty(navigator, 'clipboard', {
      value: {
        writeText: (text: string) => {
          written.push(text);
          return Promise.resolve();
        },
      },
      configurable: true,
    });
    return { written, cleared };
  }

  /** The footer's live region: always in the document, or a reader may never
   *  announce what appears in it. */
  function copiedRegion(): Element {
    const region = screen.getByText(/^Right-click:/).parentElement?.querySelector('[role="status"]');
    if (region === null || region === undefined) throw new Error('no live region in the footer');
    return region;
  }

  it('says "Copied" after a right-click copy, politely, and then stops saying it', async () => {
    const { written, cleared } = selecting();
    await openShell();
    const region = copiedRegion();
    expect(region.textContent).toBe('');

    rightClick();
    await waitFor(() => {
      expect(region.textContent).toBe('Copied');
    });
    expect(written).toEqual(['pod/api-1']);
    expect(cleared).toHaveBeenCalled();
    expect(typed()).toBe('');

    // The copy resolved outside `act`, so the timer that clears it may not be
    // set when "Copied" is first seen: advancing the clock once and asserting
    // at once raced it on CI. Advance, then wait.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    await waitFor(() => {
      expect(region.textContent).toBe('');
    });
  });

  it('says "Copied" again for a second copy inside the window, by emptying the region first', async () => {
    // A live region is announced when its text changes. A second copy that
    // only restarted the timer would leave "Copied" as it was, and a reader
    // would hear nothing.
    selecting();
    await openShell();
    const region = copiedRegion();
    rightClick();
    await waitFor(() => {
      expect(region.textContent).toBe('Copied');
    });

    const seen: string[] = [];
    const watch = new MutationObserver(() => seen.push(region.textContent ?? ''));
    watch.observe(region, { childList: true, characterData: true, subtree: true });
    rightClick();
    await waitFor(() => {
      expect(seen).toEqual(['', 'Copied']);
    });
    watch.disconnect();
  });

  it("turns xterm's right-click word selection off, which a Mac has on by default (review M1)", async () => {
    // xterm 6 defaults `rightClickSelectsWord` to `isMac`, and its own
    // listener selects the word under the pointer before the pane's runs: on
    // a Mac a right-click over text copied that word and never pasted, which
    // is not what the footer says. The option is asserted as it is *asked
    // for*, since jsdom is not a Mac and the default here is already off.
    const asked: unknown[] = [];
    terminalChunk.load = () =>
      terminalChunk.real().then((module) => ({
        ...module,
        Terminal: class extends module.Terminal {
          constructor(...options: ConstructorParameters<typeof module.Terminal>) {
            super(...options);
            asked.push(options[0]);
          }
        },
      }));
    await openShell();
    expect(asked).not.toHaveLength(0);
    for (const options of asked) expect(options).toMatchObject({ rightClickSelectsWord: false });
  });
});
