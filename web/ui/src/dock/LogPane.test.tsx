import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { useEffect, type ReactNode } from 'react';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { LogEvent } from '../api';
import { resetLiveHandles } from '../live/handles';
import { Dock } from './Dock';
import { DockProvider, useDock, type DockApi } from './DockContext';

/**
 * The log panel, driven by real frames.
 *
 * A `state` frame really produces a marker, an escape sequence really becomes a
 * `<span>`, and `Download` really writes the raw line.
 */

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

/** Every stream opened by the panel, so a test can push frames into it. */
const streams: {
  url: string;
  onmessage: ((event: { data: string }) => void) | null;
  onerror: (() => void) | null;
}[] = [];

function emit(event: LogEvent): void {
  const stream = streams[streams.length - 1];
  act(() => {
    stream?.onmessage?.({ data: JSON.stringify(event) });
  });
}

/** The panel repaints on a 120 ms timer rather than per line - see
 *  `logBuffer.ts` for the measurement that decides that. */
function repaint(): void {
  act(() => {
    vi.advanceTimersByTime(200);
  });
}

function mount(containers: unknown = { state: 'empty', items: [] }): void {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  vi.stubGlobal('fetch', () =>
    Promise.resolve(
      new Response(JSON.stringify(containers), {
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

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  streams.length = 0;
  vi.stubGlobal(
    'EventSource',
    class {
      onmessage: ((event: { data: string }) => void) | null = null;
      onerror: (() => void) | null = null;
      // Assigned in the body rather than declared as a parameter property:
      // `erasableSyntaxOnly` is on, and a parameter property is syntax that
      // cannot be erased.
      url: string;
      constructor(url: string) {
        this.url = url;
        streams.push(this);
      }
      close(): void {}
    },
  );
});

afterEach(() => {
  probe.api = null;
  resetLiveHandles();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('the stream', () => {
  it('asks for the object it was opened on', () => {
    mount();
    expect(streams).toHaveLength(1);
    const asked = new URL(streams[0]?.url ?? '', 'http://x');
    expect(asked.pathname).toBe('/api/logs');
    expect(asked.searchParams.get('cluster')).toBe('prod');
    expect(asked.searchParams.get('namespace')).toBe('default');
    expect(asked.searchParams.get('name')).toBe('api-1');
    expect(asked.searchParams.get('follow')).toBe('true');
  });

  it('draws the lines it is sent', () => {
    mount();
    emit({ type: 'line', text: 'listening on :8080' });
    emit({ type: 'line', text: 'ready' });
    repaint();
    expect(screen.getByText(/listening on :8080/)).toBeTruthy();
    expect(screen.getByText(/ready/)).toBeTruthy();
  });

  it('draws the color a container printed and never the escapes', () => {
    mount();
    emit({ type: 'line', text: '\x1b[31mERR\x1b[0m db refused' });
    repaint();
    const coloured = document.querySelector('[style*="--color-ansi-red"]');
    expect(coloured?.textContent).toBe('ERR');
    expect(document.body.textContent).not.toContain('\x1b');
  });
});

/**
 * The end of a stream. Four things can stop one, and an empty answer and a
 * failed one are different answers: a panel that renders them the same way is
 * the defect.
 */
describe('how a stream ends', () => {
  it('says end of log, in a marker outside the buffer', () => {
    // A marker pushed in as a line would be hidden by the next filter that does
    // not happen to match it, and would land in the middle of a downloaded file
    // as though the container had printed it.
    mount();
    emit({ type: 'line', text: 'one' });
    emit({ type: 'end' });
    repaint();
    expect(screen.getByText(/End of log\./)).toBeTruthy();
  });

  it('says how far back the log goes, and does not say "the beginning of time"', () => {
    mount();
    emit({ type: 'end', atStart: true });
    repaint();
    // The kubelet rotates, so the beginning of what the cluster still holds is
    // not the beginning of the container's life. The Go test that pinned this
    // sentence grepped Go strings and would have stayed green over a string
    // nobody read once it moved here.
    expect(screen.getByText(/rotated away/)).toBeTruthy();
  });

  it('draws the server\'s own sentence for a refusal, rather than "the stream stopped"', () => {
    // The one refusal a log panel can actually hit: sixteen live follows already
    // open. An EventSource cannot see a status, which is why the contract sends
    // it as a framed `state` and why this must not be flattened into the
    // transport error below.
    mount();
    emit({
      type: 'state',
      state: 'unreadable',
      reason: {
        code: 'truncated',
        message: 'stopped after 8 MiB — narrow the tail or filter in the terminal',
      },
    });
    repaint();
    expect(screen.getByText(/stopped after 8 MiB/)).toBeTruthy();
  });

  it('draws a waiting notice for a container not started yet, and its first line takes it down', () => {
    // A pod created and its log opened at once. The server
    // says the follow is waiting, then sends the container's lines on the same
    // stream - so the notice is not a stop, the tab stays live, and once the
    // container talks the notice has nothing left to say.
    mount();
    emit({
      type: 'state',
      state: 'pending',
      reason: {
        code: 'container_not_started',
        message: 'waiting for the container to start: PodInitializing',
      },
    });
    emit({
      type: 'state',
      state: 'pending',
      reason: {
        code: 'container_not_started',
        message: 'waiting for the container to start: ContainerCreating',
      },
    });
    repaint();
    const notice = screen.getByText(/waiting for the container to start: ContainerCreating/);
    // A notice, not a warning: nothing has gone wrong yet.
    expect(notice.className).toContain('text-muted');
    expect(notice.className).not.toContain('text-warn');
    // The later reason replaced the earlier one rather than stacking under it.
    expect(screen.queryByText(/PodInitializing/)).toBeNull();
    expect(probe.api?.state.tabs[0]?.live).toBe(true);

    emit({ type: 'line', text: 'listening on :8080' });
    repaint();
    expect(screen.queryByText(/waiting for the container to start/)).toBeNull();
    expect(screen.getByText(/listening on :8080/)).toBeTruthy();
    expect(probe.api?.state.tabs[0]?.live).toBe(true);
  });

  it('replaces a waiting notice with the sentence the wait ended on', () => {
    mount();
    emit({
      type: 'state',
      state: 'pending',
      reason: {
        code: 'container_not_started',
        message: 'waiting for the container to start: ContainerCreating',
      },
    });
    emit({
      type: 'state',
      state: 'unreadable',
      reason: {
        code: 'container_not_started',
        message: 'container "app" is not starting: ImagePullBackOff — reopen the panel once that is fixed',
        retryable: false,
      },
    });
    emit({ type: 'end' });
    repaint();
    expect(screen.queryByText(/waiting for the container to start/)).toBeNull();
    const ended = screen.getByText(/is not starting: ImagePullBackOff/);
    expect(ended.className).toContain('text-warn');
    expect(probe.api?.state.tabs[0]?.live).toBe(false);
  });

  it('says only what it knows when the transport goes', () => {
    // The browser's own error carries no status, no body, and not even whether
    // the server was reached. So the sentence names the connection and claims no
    // cause.
    mount();
    act(() => {
      streams[0]?.onerror?.();
    });
    repaint();
    expect(screen.getByText(/connection to the stream went away/)).toBeTruthy();
  });

  it('grays the tab out and stops counting it, keeping every line', () => {
    mount();
    emit({ type: 'line', text: 'one' });
    emit({ type: 'end' });
    repaint();
    expect(probe.api?.state.tabs[0]?.live).toBe(false);
    // The scrollback is the reason the operator opened it.
    expect(screen.getByText(/one/)).toBeTruthy();
  });
});

describe('the toolbar', () => {
  const twoContainers = {
    state: 'ok',
    items: [
      { name: 'wait-for-db', init: true, ready: false, restarts: 0 },
      { name: 'api', init: false, ready: true, restarts: 3 },
    ],
  };

  it('offers a chip per container, with the restart count on it', async () => {
    mount(twoContainers);
    expect(await screen.findByRole('button', { name: /wait-for-db/ })).toBeTruthy();
    // `restarts` is the member that was declared and never emitted until this
    // branch; without it the chip cannot say this, and Prev run cannot be drawn.
    expect(screen.getByText('↺3')).toBeTruthy();
  });

  it('refuses Prev run on a container that has never restarted, and says why', async () => {
    // The trap: an EventSource never sees the status or the body of a failed
    // handshake, so the server's 404 for "previous terminated container not
    // found" arrives as nothing at all and the pane sits empty looking like a
    // stream that has not started. The restart count is the only thing in the
    // browser that can tell the difference.
    mount({
      state: 'ok',
      items: [{ name: 'api', init: false, ready: true, restarts: 0 }],
    });
    const button = await screen.findByRole('button', { name: 'Prev run' });
    expect(button.hasAttribute('disabled')).toBe(true);
    expect(button.getAttribute('title')).toContain('no previous run');
  });

  it('offers Prev run where there is one', async () => {
    mount(twoContainers);
    await screen.findByRole('button', { name: /wait-for-db/ });
    expect(screen.getByRole('button', { name: 'Prev run' }).hasAttribute('disabled')).toBe(false);
  });

  it('draws no switcher on a single-container pod', async () => {
    mount({ state: 'ok', items: [{ name: 'api', init: false, ready: true, restarts: 0 }] });
    await screen.findByRole('button', { name: 'Prev run' });
    expect(screen.queryByRole('button', { name: /^api$/ })).toBeNull();
  });

  it('starts the stream even when the container list could not be read', async () => {
    // "The server picks a default container on its own and a switcher we could
    // not draw must not cost the logs themselves."
    mount({ state: 'unreachable', items: [], reason: { code: 'timeout', message: 'no' } });
    await screen.findByRole('button', { name: MORE });
    openLogMenu();
    expect(screen.getByRole('menuitem', { name: 'Download' })).toBeTruthy();
    expect(streams).toHaveLength(1);
  });

  it('does not wrap until asked', () => {
    // The mirror of the tables' one-line rows: a log is read by running the eye
    // down the left edge, and one wrapped stack trace turns forty lines into
    // four screens.
    mount();
    emit({ type: 'line', text: 'x' });
    repaint();
    const pre = document.querySelector('pre');
    expect(pre?.className).toContain('whitespace-pre');
    expect(pre?.className).not.toContain('whitespace-pre-wrap');

    fromLogMenu('Wrap');
    expect(document.querySelector('pre')?.className).toContain('whitespace-pre-wrap');
  });

  it('indents a record only when asked, and never puts a parse error in the pane', () => {
    mount();
    emit({ type: 'line', text: '08:15 ERROR {"msg":"db refused","n":1}' });
    emit({ type: 'line', text: 'map[a:1 b:2]' });
    repaint();

    fromLogMenu('JSON');
    expect(document.body.textContent).toContain('"msg": "db refused"');
    // Almost-JSON comes out character for character. What must never happen is a
    // parse error where a log line was.
    expect(document.body.textContent).toContain('map[a:1 b:2]');
    expect(document.body.textContent).not.toMatch(/SyntaxError|Unexpected/);
  });

  /**
   * The right-hand group would crowd the filter, so four of its five controls
   * are behind a `⋯`.
   */
  it('keeps Time, JSON, Wrap and Download behind the ⋯, each with its state', () => {
    mount();
    for (const name of ['Time', 'JSON', 'Wrap', 'Download']) {
      expect(screen.queryByRole('button', { name }), `${name} is not in the bar`).toBeNull();
    }

    openLogMenu();
    for (const name of ['Time', 'JSON', 'Wrap']) {
      expect(screen.getByRole('menuitemcheckbox', { name }).getAttribute('aria-checked')).toBe('false');
    }
    expect(screen.getByRole('menuitem', { name: 'Download' })).toBeTruthy();

    // Choosing closes the menu; opening it again shows the state it left.
    act(() => {
      fireEvent.click(screen.getByRole('menuitemcheckbox', { name: 'Wrap' }));
    });
    expect(screen.queryByRole('menu')).toBeNull();
    openLogMenu();
    expect(screen.getByRole('menuitemcheckbox', { name: 'Wrap' }).getAttribute('aria-checked')).toBe('true');
    expect(screen.getByRole('menuitemcheckbox', { name: 'JSON' }).getAttribute('aria-checked')).toBe('false');
  });

  it('re-opens the stream when Time is turned on from the menu, and puts the dot on the ⋯', () => {
    mount();
    expect(streams).toHaveLength(1);
    expect(screen.queryByTestId('overflow-flag')).toBeNull();

    fromLogMenu('Time');

    // A new read, asked for with stamps: the item says it re-opens the stream
    // and it does, as the bar's button did.
    expect(streams).toHaveLength(2);
    expect(new URL(streams[1]?.url ?? '', 'http://x').searchParams.get('timestamps')).toBe('true');

    // The dot, and the sentence a reader gets for it, naming what is on.
    expect(screen.getByTestId('overflow-flag')).toBeTruthy();
    const more = screen.getByRole('button', { name: MORE });
    expect(document.getElementById(more.getAttribute('aria-describedby') ?? '')?.textContent).toBe('Time on');

    // Off again, and nothing hidden is on: no dot.
    fromLogMenu('Time');
    expect(streams).toHaveLength(3);
    expect(screen.queryByTestId('overflow-flag')).toBeNull();
  });

  it('keeps Prev run in the bar, beside the restart badge, and out of the menu', async () => {
    mount(twoContainers);
    const prev = await screen.findByRole('button', { name: 'Prev run' });
    // Beside the chip that carries the restart count - the fact that says there
    // is a previous run to read.
    expect(prev.previousElementSibling?.textContent).toContain('↺3');

    openLogMenu();
    expect(screen.queryByRole('menuitemcheckbox', { name: 'Prev run' })).toBeNull();
  });

  it('still refuses Prev run, in the bar, on a container that has never restarted', async () => {
    mount({ state: 'ok', items: [{ name: 'api', init: false, ready: true, restarts: 0 }] });
    const prev = await screen.findByRole('button', { name: 'Prev run' });
    expect(prev.hasAttribute('disabled')).toBe(true);
    expect(prev.closest('[role="menu"]')).toBeNull();
  });

  it('filters, counts, and keeps the stream when the pattern will not compile', () => {
    mount();
    for (const text of ['alpha', 'beta', 'alphabet']) emit({ type: 'line', text });
    repaint();

    act(() => {
      screen.getByRole('button', { name: '.*' }).click();
    });
    typeFilter('alpha');
    // Two of the three lines match. The counter is `n/m` and its parts are
    // separate text nodes, so it is read off the toolbar rather than matched as
    // one string.
    expect(screen.getByLabelText('Next match').parentElement?.textContent).toMatch(/1\s*\/\s*2/);

    // A half-typed bracket must not cost the logs: the previous filter stays,
    // the box is marked, and the useful half of the browser's message is shown.
    typeFilter('[unclosed');
    expect(screen.getByLabelText('Filter the log').className).toContain('border-warn');
    expect(streams).toHaveLength(1);
  });
});

describe('the filter, settling like every search box', () => {
  /** A keystroke, and no time after it. */
  function type(text: string): void {
    const box = screen.getByLabelText('Filter the log') as HTMLInputElement;
    act(() => {
      const setter = Object.getOwnPropertyDescriptor(
        window.HTMLInputElement.prototype, 'value',
      )?.set;
      setter?.call(box, text);
      box.dispatchEvent(new Event('input', { bubbles: true }));
    });
  }

  function lines(): void {
    mount();
    for (const text of ['GET /a 200', 'GET /b 500', 'boot complete']) emit({ type: 'line', text });
    repaint();
  }

  it('keeps every line on screen until the typing settles, and announces nothing', () => {
    lines();

    type('500');
    // The lines as they were: nothing on screen is emptied or swapped while
    // the typing has not settled. And no `filtering…` marker: 140 ms over the
    // buffer in hand is not a wait, and a live region per keystroke is noise.
    expect(document.body.textContent).toContain('GET /a 200');
    expect(document.body.textContent).toContain('boot complete');
    expect(document.querySelector('[data-settling]')).toBeNull();

    act(() => {
      vi.advanceTimersByTime(200);
    });
    expect(document.body.textContent).toContain('GET /b 500');
    expect(document.body.textContent).not.toContain('GET /a 200');
  });

  it('settles at once on Enter', () => {
    lines();
    type('boot');
    expect(document.body.textContent).toContain('GET /a 200');

    act(() => {
      fireEvent.keyDown(screen.getByLabelText('Filter the log'), { key: 'Enter' });
    });
    expect(document.body.textContent).toContain('boot complete');
    expect(document.body.textContent).not.toContain('GET /a 200');
  });
});

/** The toolbar's `⋯`, found by the name it keeps in every state. */
const MORE = 'More log options';

/** Open the toolbar's `⋯`. */
function openLogMenu(): void {
  act(() => {
    fireEvent.click(screen.getByRole('button', { name: MORE }));
  });
}

/** Time, JSON, Wrap and Download live behind the `⋯`: open it and choose. */
function fromLogMenu(name: string): void {
  openLogMenu();
  act(() => {
    fireEvent.click(
      screen.getByRole(name === 'Download' ? 'menuitem' : 'menuitemcheckbox', { name }),
    );
  });
}

function typeFilter(text: string): void {
  const box = screen.getByLabelText('Filter the log') as HTMLInputElement;
  act(() => {
    const setter = Object.getOwnPropertyDescriptor(
      window.HTMLInputElement.prototype, 'value',
    )?.set;
    setter?.call(box, text);
    box.dispatchEvent(new Event('input', { bubbles: true }));
  });
  // Past the 140 ms debounce: typing "error" is one pass over the buffer, not
  // five.
  act(() => {
    vi.advanceTimersByTime(200);
  });
}

describe('download', () => {
  it('writes what the container printed and not what is on screen', () => {
    // "A saved log is the file `kubectl logs` would have written, and an
    // indented copy is not that file - it cannot be diffed against one, grepped
    // with the patterns the operator's other tools use, or handed to anything
    // that parses a record per line."
    const written: string[] = [];
    vi.stubGlobal('URL', {
      ...URL,
      createObjectURL: (blob: Blob) => {
        void blob.text().then((text) => written.push(text));
        return 'blob:x';
      },
      revokeObjectURL: () => {},
    });

    mount();
    emit({ type: 'line', text: '{"msg":"db refused"}' });
    repaint();
    fromLogMenu('JSON');
    // Indented on screen…
    expect(document.body.textContent).toContain('"msg": "db refused"');

    fromLogMenu('Download');
    return Promise.resolve().then(() => {
      // …and dense in the file.
      expect(written[0]).toBe('{"msg":"db refused"}');
    });
  });
});

/**
 * Following panes do not take every connection the browser keeps to the
 * console. The fifth waits, says so, and
 * opens on its own when another ends - `api/logstream.test.ts` holds the
 * counting; this is the pane's half.
 */
describe('a pane waiting for a connection', () => {
  it('says why it is waiting, and its first line takes the notice down', () => {
    mount();
    for (const name of ['api-2', 'api-3', 'api-4', 'api-5']) {
      act(() => {
        probe.api?.openLogs({ cluster: 'prod', kind: 'pods', namespace: 'default', name });
      });
    }
    // Five panes, four requests: the fifth (api-5, the one shown) waits.
    expect(streams).toHaveLength(4);
    repaint();
    expect(screen.getByText(/Waiting to start: 4 logs are already streaming/)).toBeTruthy();

    // The first pane's log ends: the fifth opens, and talks.
    act(() => {
      streams[0]?.onmessage?.({ data: JSON.stringify({ type: 'end' }) });
    });
    expect(streams).toHaveLength(5);
    emit({ type: 'line', text: 'api-5 is up' });
    repaint();
    expect(screen.getByText(/api-5 is up/)).toBeTruthy();
    expect(screen.queryByText(/Waiting to start/)).toBeNull();
  });
});
