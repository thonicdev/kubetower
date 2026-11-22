import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, render, screen } from '@testing-library/react';
import { useEffect, type ReactNode } from 'react';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { KeyboardStack } from '../keyboard/KeyboardStack';
import { resetKeyboardStack } from '../keyboard/stack';
import { resetLiveHandles } from '../live/handles';
import { patientWithALoadedRunner } from '../testing/loadedRunner';
import { Dock } from './Dock';
import { DockProvider, useDock, type DockApi } from './DockContext';
import { LOG_WINDOW } from './logBuffer';

/**
 * Copying lines out of a log pane.
 *
 * No claimant in the keyboard stack takes Ctrl+C or ⌘C: the browser's own
 * `copy` runs. **What is at risk is the selection, on a pod that is
 * printing.** The pane draws its newest `LOG_WINDOW` rows and follows the
 * stream, so without a hold the rows under the selection would slide out of
 * the drawn window and be taken out of the document, and the selection with
 * them. On a slower stream the pinning would scroll the selected lines out of
 * sight first, and a drag still in progress would grow over lines that move
 * under the pointer.
 *
 * So the first block here is the shortcut reaching the browser - jsdom does not
 * perform a copy, so what is asserted is that nothing in the dock or in the
 * keyboard stack it registers with called `preventDefault` on it (the whole
 * console's claimants are covered by §20 only as far as the harness mounts
 * them) - and the rest is the selection surviving the stream.
 * The browser half, the copy itself, is `measure/dock-log.spec.ts` §20.
 */

// Up to two and a half thousand rows drawn in jsdom, as `LogPaging` does; the
// budget is for the work (see `loadedRunner.ts`).
patientWithALoadedRunner();

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

const streams: { onmessage: ((event: { data: string }) => void) | null }[] = [];
/** Pages handed back for a look backwards, in order. */
const pages: unknown[] = [];

/** Lines `from` to `to - 1`, each naming its own number. */
function lines(from: number, to: number): void {
  const stream = streams[streams.length - 1];
  act(() => {
    for (let n = from; n < to; n++) {
      stream?.onmessage?.({ data: JSON.stringify({ type: 'line', text: `line ${String(n)} ok` }) });
    }
  });
}

/** The panel repaints on a 120 ms timer rather than per line. */
function repaint(): void {
  act(() => {
    vi.advanceTimersByTime(200);
  });
}

function mount(): void {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  vi.stubGlobal('fetch', (path: string) => {
    const url = new URL(path, 'http://x');
    const body =
      url.searchParams.get('before') !== null
        ? pages.shift() ?? { state: 'ok', data: { lines: [] } }
        : { state: 'empty', items: [] };
    return Promise.resolve(
      new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    );
  });
  const router = createMemoryRouter(
    [
      {
        path: '/c/:cluster',
        element: (
          // The application's one keyboard listener, so every claimant the
          // dock registers is asked about the key exactly as in the console.
          <KeyboardStack>
            <DockProvider>
              <Probe take={take} />
              <Dock />
            </DockProvider>
          </KeyboardStack>
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

/** The drawn row for line `n` of the stream, as the pane numbers it. */
function row(text: string): HTMLElement {
  const found = screen.getByText(text, { exact: false }).closest<HTMLElement>('[data-line]');
  if (found === null) throw new Error(`no row draws ${text}`);
  return found;
}

/**
 * Select from the start of one row to the end of another, as a drag would, and
 * tell the page so. jsdom keeps a `Selection` but does not fire
 * `selectionchange` when one is made by script; every browser does, for a drag
 * and for a script alike.
 */
function select(first: HTMLElement, last: HTMLElement): void {
  const range = document.createRange();
  range.setStartBefore(first);
  range.setEndAfter(last);
  act(() => {
    const selection = document.getSelection();
    selection?.removeAllRanges();
    selection?.addRange(range);
    document.dispatchEvent(new Event('selectionchange'));
  });
}

function letGo(): void {
  act(() => {
    document.getSelection()?.removeAllRanges();
    document.dispatchEvent(new Event('selectionchange'));
  });
}

/** The newest row in the document. */
function lastDrawn(): string {
  const rows = document.querySelectorAll('[data-line]');
  return rows[rows.length - 1]?.textContent ?? '';
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  streams.length = 0;
  pages.length = 0;
  resetKeyboardStack();
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
});

afterEach(() => {
  letGo();
  probe.api = null;
  resetLiveHandles();
  resetKeyboardStack();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('the copy shortcut', () => {
  // Both spellings, because the platform decides which one a person presses
  // and the page cannot tell a Mac keyboard from a remapped one.
  for (const [name, modifier] of [
    ['Ctrl+C', { ctrlKey: true }],
    ['⌘C', { metaKey: true }],
  ] as const) {
    it(`${name} over a selection in the log is left to the browser`, () => {
      mount();
      lines(0, 20);
      repaint();
      select(row('line 4 ok'), row('line 6 ok'));

      const key = new KeyboardEvent('keydown', { key: 'c', code: 'KeyC', bubbles: true, cancelable: true, ...modifier });
      // `dispatchEvent` is false when a listener canceled the event, which is
      // the one thing that stops the browser's own copy.
      const reached = row('line 5 ok').dispatchEvent(key);
      expect(reached, `${name} was canceled on its way to the browser`).toBe(true);
      expect(document.getSelection()?.toString()).toContain('line 5 ok');
    });
  }
});

describe('a selection in a pane that is following', () => {
  it('is still in the document after the stream has moved the window past it', () => {
    mount();
    lines(0, 40);
    repaint();
    select(row('line 10 ok'), row('line 12 ok'));
    const chosen = document.getSelection()?.toString() ?? '';
    expect(chosen).toContain('line 11 ok');

    // A busy pod: more lines than the drawn window holds, so without the hold
    // the rows under the selection are the first to leave the document.
    lines(40, 40 + LOG_WINDOW + 500);
    repaint();

    expect(document.querySelector('[data-line="10"]'), 'the first selected row was taken out').not.toBeNull();
    expect(document.getSelection()?.toString()).toBe(chosen);
    // Held back, not lost: drawn once the selection is let go.
    expect(lastDrawn()).toBe('line 39 ok');
  });

  it('says that following is paused, and how many lines are waiting', () => {
    mount();
    lines(0, 40);
    repaint();
    select(row('line 10 ok'), row('line 12 ok'));
    lines(40, 65);
    repaint();

    const paused = screen.getByText(/Following is paused while text in this panel is selected/);
    expect(paused.closest('p')?.textContent).toContain('25 newer lines');
  });

  it('follows again once the selection is let go', () => {
    mount();
    lines(0, 40);
    repaint();
    select(row('line 10 ok'), row('line 12 ok'));
    lines(40, 100);
    repaint();
    expect(lastDrawn()).toBe('line 39 ok');

    letGo();
    repaint();
    expect(lastDrawn()).toBe('line 99 ok');
    expect(screen.queryByText(/Following is paused/)).toBeNull();
  });

  it('is not held by a selection somewhere else on the page', () => {
    mount();
    lines(0, 40);
    repaint();
    const elsewhere = document.createElement('p');
    elsewhere.textContent = 'a page heading';
    document.body.append(elsewhere);
    select(elsewhere, elsewhere);

    lines(40, 60);
    repaint();
    expect(lastDrawn()).toBe('line 59 ok');
    elsewhere.remove();
  });

  it('keeps its text when earlier lines are put in front of it', async () => {
    mount();
    lines(0, 30);
    repaint();
    select(row('line 10 ok'), row('line 12 ok'));
    const chosen = document.getSelection()?.toString() ?? '';

    // A page backwards puts lines in front of every index the pane drew. Rows
    // keyed by their index would each be handed another line's text, and the
    // selection would then cover text nobody chose.
    pages.push({ state: 'ok', data: { lines: ['earlier a', 'earlier b', 'earlier c'] } });
    act(() => {
      screen.getByRole('button', { name: 'Read earlier lines' }).click();
    });
    await act(async () => {
      await vi.runOnlyPendingTimersAsync();
    });
    repaint();

    expect(screen.getByText('earlier a')).toBeTruthy();
    expect(document.getSelection()?.toString()).toBe(chosen);
  });
});
