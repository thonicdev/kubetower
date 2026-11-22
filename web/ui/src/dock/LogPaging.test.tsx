import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, render, screen } from '@testing-library/react';
import { useEffect, type ReactNode } from 'react';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { LogEvent } from '../api';
import { resetLiveHandles } from '../live/handles';
import { patientWithALoadedRunner } from '../testing/loadedRunner';
import { LogBuffer, dockLogBudget } from './logBuffer';
import { Dock } from './Dock';
import { DockProvider, useDock, type DockApi } from './DockContext';

/**
 * Looking backwards: the half of the log panel that talks to a different shape
 * of the same route.
 *
 * ## What this does not test
 *
 * The window arithmetic. `internal/logs/earlier_test.go` has eleven test
 * functions on it, including `TestPagingNeverCrossesIntoThePreviousRun` and
 * `TestAPageBackwardsTakesNoFollowSlot`, and it is the server's answer. A second
 * copy of it in the browser would be a second answer, drifting the first time
 * one of them was fixed.
 *
 * ## What it does test
 *
 * **Five outcomes rendered from one place, so that they cannot drift apart** - and the two that are
 * silently wrong if nobody looks:
 *
 *   - a stop is a stop, and the pane has to stop asking. A pane that kept asking
 *     would re-read the same window on every scroll to the top;
 *   - a refusal is not an ending. "The cluster did not answer" can be retried
 *     and "there is nothing earlier" cannot, and a pane that draws them the same
 *     way tells an operator a log has no history when what happened is that
 *     their account may not read it.
 *
 * Its own file rather than an eighth `describe` in `LogPane.test.tsx`, because
 * the fixture is different: this one routes `fetch` by what was asked for, since
 * the panel now makes two different calls to `/api/logs`.
 */

// Two tests here draw 2 000 to 10 000 rows in jsdom, three times over, and
// nothing else: no wait, no timer that is not driven by hand. On an idle
// machine that is a quarter of a second; on a loaded CI runner it has run past
// vitest's 5 s. The budget is for the work.
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

const streams: {
  url: string;
  onmessage: ((event: { data: string }) => void) | null;
  onerror: (() => void) | null;
}[] = [];

/** The pages this fixture hands back, in order, and every page the pane asked
 *  for. */
const pages: unknown[] = [];
const asked: string[] = [];

function emit(event: LogEvent): void {
  const stream = streams[streams.length - 1];
  act(() => {
    stream?.onmessage?.({ data: JSON.stringify(event) });
  });
}

/** The panel repaints on a 120 ms timer rather than per line - `logBuffer.ts`
 *  carries the measurement that decides that. */
function repaint(): void {
  act(() => {
    vi.advanceTimersByTime(200);
  });
}

function mount(): void {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  vi.stubGlobal('fetch', (path: string) => {
    const url = new URL(path, 'http://x');
    // `before` is what turns this operation from a stream into a bounded read,
    // so it is also what tells the two calls apart here.
    if (url.searchParams.get('before') !== null) {
      asked.push(path);
      const page = pages.shift() ?? { state: 'ok', data: { lines: [] } };
      return Promise.resolve(
        new Response(JSON.stringify(page), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      );
    }
    return Promise.resolve(
      new Response(JSON.stringify({ state: 'empty', items: [] }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
  });
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

/**
 * The control exists **as well as** the scroll gesture, and that is not
 * belt-and-braces: a pane showing fewer lines than fit it has no scrollbar, so
 * there is no "scroll to the top" to perform and a gesture-only trigger would
 * simply not exist on a quiet pod - which is most pods, most of the time.
 *
 * The scroll branch of the same call is driven by `scrollToTop` below. jsdom
 * lays nothing out, so every geometry number it reports is zero - which is
 * exactly the shape of a hidden pane, and is why `scrollToTop` can reach the
 * branch at all. What it cannot show is the *compensation*, which is a pixel
 * arithmetic over heights jsdom does not have.
 */
async function readEarlier(): Promise<void> {
  await act(async () => {
    screen.getByRole('button', { name: 'Read earlier lines' }).click();
    await Promise.resolve();
    await Promise.resolve();
  });
  repaint();
}

/** The pane, scrolled to the top. The container is the only scrollable element
 *  the panel draws. */
function scrollToTop(): void {
  const pane = document.querySelector('.overflow-auto');
  if (pane === null) throw new Error('the log pane has no scrollable container');
  act(() => {
    pane.dispatchEvent(new Event('scroll', { bubbles: true }));
  });
}

beforeEach(() => {
  // The clock moves when `repaint()` moves it, and only then. With
  // `shouldAdvanceTime` it also followed the wall clock, so the pane's 120 ms
  // repaint could fire on its own at any `await` in a test - more often the
  // slower the machine - and a test about what one repaint draws was reading
  // whatever the scheduler had let through. Nothing here waits on a real timer:
  // the fetch stub answers in microtasks, which fake timers do not hold.
  vi.useFakeTimers();
  streams.length = 0;
  pages.length = 0;
  asked.length = 0;
  vi.stubGlobal(
    'EventSource',
    class {
      onmessage: ((event: { data: string }) => void) | null = null;
      onerror: (() => void) | null = null;
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

describe('looking backwards', () => {
  it('offers nothing to read earlier than nothing', () => {
    mount();
    repaint();
    // Asking how many lines precede zero is a question with no answer, and a
    // control offering it would spend one of the server's reads to be told so.
    expect(screen.queryByRole('button', { name: 'Read earlier lines' })).toBeNull();
  });

  it('asks for the lines before the ones it holds', async () => {
    mount();
    emit({ type: 'line', text: 'newest' });
    repaint();
    pages.push({ state: 'ok', data: { lines: ['older'] } });

    await readEarlier();

    expect(asked).toHaveLength(1);
    const url = new URL(asked[0] ?? '', 'http://x');
    expect(url.pathname).toBe('/api/logs');
    // `before` is how many lines the pane holds - not an offset and not a
    // cursor, because the Kubernetes log API has neither.
    expect(url.searchParams.get('before')).toBe('1');
    expect(url.searchParams.get('name')).toBe('api-1');
    expect(screen.getByText(/older/)).toBeTruthy();
  });

  it('says which of the three ways it stopped, in the words the server sent', async () => {
    mount();
    emit({ type: 'line', text: 'newest' });
    repaint();
    pages.push({
      state: 'ok',
      data: {
        lines: [],
        stop: 'beginning',
        // Never "the container started here": the kubelet rotates, so the oldest
        // line the API returns is not the first the process wrote. The sentence
        // is the server's and this panel draws it rather than composing one.
        note: 'this is as far back as the kubelet has kept',
      },
    });

    await readEarlier();

    expect(screen.getByText(/as far back as the kubelet has kept/)).toBeTruthy();
  });

  it('stops asking once it has been told there is no more', async () => {
    mount();
    emit({ type: 'line', text: 'newest' });
    repaint();
    pages.push({ state: 'ok', data: { lines: [], stop: 'beginning', note: 'nothing older' } });

    await readEarlier();

    // The control is gone, which is the only thing that stops a scroll to the
    // top re-asking for ever. `beginning`, `ceiling` and `dense` are all "do not
    // ask again"; only a failed fetch may be retried.
    expect(screen.queryByRole('button', { name: 'Read earlier lines' })).toBeNull();
    expect(asked).toHaveLength(1);
  });

  it('keeps a refusal apart from an ending, and offers to try again', async () => {
    mount();
    emit({ type: 'line', text: 'newest' });
    repaint();
    // The envelope says the page did not arrive and why. Distinct from a stop,
    // which says it arrived and there will not be another.
    pages.push({
      state: 'forbidden',
      reason: { code: 'rbac_denied', message: 'this account may not read this log' },
    });

    await readEarlier();

    expect(screen.getByText(/may not read this log/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy();
  });

  it('does not claim a beginning when there is more behind the page', async () => {
    mount();
    emit({ type: 'line', text: 'newest' });
    repaint();
    // No `stop`, so there is more. A pane rendering this as an ending would stop
    // offering to page back after one gesture, and the operator would see a log
    // claiming to have no history.
    pages.push({ state: 'ok', data: { lines: ['older'] } });

    await readEarlier();

    expect(screen.getByRole('button', { name: 'Read earlier lines' })).toBeTruthy();
  });

  it('keeps drawing arriving lines after a page backwards', async () => {
    mount();
    emit({ type: 'line', text: 'newest' });
    repaint();
    pages.push({ state: 'ok', data: { lines: ['older'] } });

    await readEarlier();

    // Looking backwards stops the pane *following*, which is a scroll property;
    // what it must not do is stop it accepting. A stream that kept arriving into
    // an array and never appearing is a panel that has quietly stopped for no
    // reason the reader can see.
    emit({ type: 'line', text: 'newer still' });
    repaint();
    expect(screen.getByText(/older/)).toBeTruthy();
    expect(screen.getByText(/newer still/)).toBeTruthy();
  });

  it('asks once for a scroll to the top, and never again after a stop', async () => {
    mount();
    emit({ type: 'line', text: 'newest' });
    repaint();
    pages.push({ state: 'ok', data: { lines: [], stop: 'ceiling', note: 'use kubectl' } });

    // The gesture half of the trigger. The control is the other half and they
    // reach the same call, so the guard against asking twice has to live in the
    // call rather than in whether a button is on screen.
    scrollToTop();
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    repaint();
    expect(asked).toHaveLength(1);
    expect(screen.getByText(/use kubectl/)).toBeTruthy();

    // `ceiling` is "there is more and this console will not fetch it", which is
    // still "do not ask again". Without the guard inside the call, every
    // subsequent scroll to the top would re-read the same window.
    scrollToTop();
    scrollToTop();
    await act(async () => {
      await Promise.resolve();
    });
    expect(asked).toHaveLength(1);
  });

  it('starts again from nothing when the container changes', async () => {
    mount();
    emit({ type: 'line', text: 'newest' });
    repaint();
    pages.push({ state: 'ok', data: { lines: [], stop: 'beginning', note: 'nothing older' } });

    await readEarlier();
    expect(screen.queryByRole('button', { name: 'Read earlier lines' })).toBeNull();

    // A new stream is a new buffer. Carrying the previous container's
    // `beginning` across would make the pane say there is nothing older about a
    // log it has not looked at.
    // Time is behind the toolbar's `⋯`.
    act(() => {
      screen.getByRole('button', { name: 'More log options' }).click();
    });
    act(() => {
      screen.getByRole('menuitemcheckbox', { name: 'Time' }).click();
    });
    emit({ type: 'line', text: 'first line of the new stream' });
    repaint();
    expect(screen.getByRole('button', { name: 'Read earlier lines' })).toBeTruthy();
  });
});

/**
 * Two costs a pane must stop paying once it stops earning them: a drawn window
 * that grew upward and never came back down would leave thousands of rows in
 * the document, re-rendered on every arrival; and a repaint timer per pane
 * would run on after its stream had ended.
 */
describe('a pane left open', () => {
  /** Many lines in one act, so the test is not ten thousand renders. */
  function emitLines(count: number): void {
    const stream = streams[streams.length - 1];
    act(() => {
      for (let i = 0; i < count; i++) {
        stream?.onmessage?.({ data: JSON.stringify({ type: 'line', text: `line ${String(i)}` }) });
      }
    });
  }

  /** jsdom lays nothing out; this gives the pane the geometry of one that
   *  scrolls, and puts the reader at `top`. */
  function scrollPaneTo(top: number): void {
    const pane = document.querySelector<HTMLElement>('.overflow-auto');
    if (pane === null) throw new Error('the log pane has no scrollable container');
    Object.defineProperty(pane, 'scrollHeight', { configurable: true, get: () => 100_000 });
    Object.defineProperty(pane, 'clientHeight', { configurable: true, get: () => 400 });
    Object.defineProperty(pane, 'scrollTop', { configurable: true, writable: true, value: top });
    act(() => {
      pane.dispatchEvent(new Event('scroll', { bubbles: true }));
    });
  }

  function drawn(): number {
    return document.querySelectorAll('[data-line]').length;
  }

  it('draws the newest window again once the reader is back at the bottom', () => {
    mount();
    emitLines(2_500);
    repaint();
    expect(drawn()).toBe(2_000);

    // Near the top: the window grows upward over what the buffer holds.
    scrollPaneTo(0);
    repaint();
    expect(drawn()).toBe(2_500);

    // Back at the newest line: the window that grew goes back to its size.
    scrollPaneTo(100_000 - 400);
    repaint();
    expect(drawn()).toBe(2_000);
  });

  it('holds a share of the dock budget while mounted, and gives it back when closed', () => {
    // The budget at the pane, not at the class: with `join` and `leave` made
    // no-ops every other test here would still pass.
    const before = dockLogBudget.panes;
    mount();
    expect(dockLogBudget.panes).toBe(before + 1);
    act(() => {
      probe.api?.openLogs({ cluster: 'prod', kind: 'pods', namespace: 'default', name: 'api-2' });
    });
    expect(dockLogBudget.panes).toBe(before + 2);
    act(() => {
      probe.api?.dispatch({ type: 'closeAll' });
    });
    expect(dockLogBudget.panes).toBe(before);
  });

  it('says so at once when the budget trims it after its stream has ended', async () => {
    // An ended pane has no repaint timer, so a trim nobody announced would
    // leave it drawing lines it no longer holds, with no "let go" sentence -
    // and Download, which writes what is held, would save fewer lines than it
    // showed.
    mount();
    emitLines(10_500);
    emit({ type: 'end' });
    repaint();
    expect(screen.queryByText(/keeps only its newest/)).toBeNull();

    const before = dockLogBudget.panes;
    const others = Array.from({ length: 40 - before }, () => new LogBuffer());
    try {
      act(() => {
        for (const other of others) dockLogBudget.join(other);
      });
      // Forty panes share 400 000 lines: 10 000 each, so 500 went.
      expect(dockLogBudget.panes).toBe(40);
      expect(screen.getByText(/keeps only its newest 10,000 lines/)).toBeTruthy();
      expect(screen.getByText(/500 so far/)).toBeTruthy();

      // And Download writes exactly what is held, which the sentence says.
      const saved: Blob[] = [];
      const create = Object.getOwnPropertyDescriptor(URL, 'createObjectURL');
      const revoke = Object.getOwnPropertyDescriptor(URL, 'revokeObjectURL');
      Object.defineProperty(URL, 'createObjectURL', {
        configurable: true,
        value: (blob: Blob) => {
          saved.push(blob);
          return 'blob:test';
        },
      });
      Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: () => {} });
      try {
        // Download is behind the toolbar's `⋯`.
        act(() => {
          screen.getByRole('button', { name: 'More log options' }).click();
        });
        act(() => {
          screen.getByRole('menuitem', { name: 'Download' }).click();
        });
      } finally {
        if (create === undefined) Reflect.deleteProperty(URL, 'createObjectURL');
        else Object.defineProperty(URL, 'createObjectURL', create);
        if (revoke === undefined) Reflect.deleteProperty(URL, 'revokeObjectURL');
        else Object.defineProperty(URL, 'revokeObjectURL', revoke);
      }
      const text = (await saved[0]?.text()) ?? '';
      const lines = text.split('\n');
      expect(lines).toHaveLength(10_000);
      expect(lines[0]).toBe('line 500');
      expect(document.querySelector('[data-line]')?.textContent).not.toBe('line 0');
    } finally {
      for (const other of others) dockLogBudget.leave(other);
    }
  });

  it('stops its repaint timer once the stream has ended', () => {
    mount();
    emit({ type: 'line', text: 'only' });
    repaint();
    const following = vi.getTimerCount();
    emit({ type: 'end' });
    repaint();
    expect(screen.getByText(/End of log/)).toBeTruthy();
    expect(vi.getTimerCount()).toBe(following - 1);
  });
});
