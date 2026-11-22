import { QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { KeyboardStack } from '../../keyboard/KeyboardStack';
import { KeyLayer } from '../../keyboard/layers';
import { claimKey, resetKeyboardStack } from '../../keyboard/stack';
import { metricsQuery } from '../../api';
import { createQueryClient } from '../../query/client';
import { stubFetch, type StubReply } from '../../testing/harness';
import { Metrics } from './Metrics';

/**
 * The metrics panel on its own, answered by a stubbed `/api/c/prod/metrics`.
 *
 * The first concern is the one this file exists for: **Network must not show
 * CPU.** A `series.find(key === wanted) ?? series[0]` fallback would let a
 * chosen measurement with no line under it fall through to the first series,
 * which is CPU, drawn under the Network icon with nothing on screen to say so.
 * A chart of the wrong thing that looks right is the defect this whole console
 * is organized against.
 */

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
});

const METRICS = '/api/c/prod/metrics';

const cpu = {
  key: 'cpu',
  chart: 'cpu',
  label: 'CPU',
  unit: 'mcores',
  data: { startedAt: '2026-09-12T10:51:08Z', stepSeconds: 30, values: [264.2, 324.8, 372.7, 324.6] },
};

const networkIn = {
  key: 'network_rx',
  chart: 'network',
  label: 'Network in',
  unit: 'bytes/s',
  data: { startedAt: '2026-09-12T10:51:08Z', stepSeconds: 30, values: [1000, 2000, 1500, 9000] },
};

const networkOut = {
  key: 'network_tx',
  chart: 'network',
  label: 'Network out',
  unit: 'bytes/s',
  data: { startedAt: '2026-09-12T10:51:08Z', stepSeconds: 30, values: [100, 200, 150, 400] },
};

const choices = [
  { key: 'cpu', label: 'CPU', available: true, selected: true },
  { key: 'network', label: 'Network', available: true },
];

const windows = [
  { key: '1h', label: '1h', selected: true },
  { key: '24h', label: '24h' },
];

function panel(series: unknown[], extra: Record<string, unknown> = {}): StubReply {
  return {
    body: {
      state: 'ok',
      data: { series, metrics: choices, windows, ...extra },
    },
  };
}

function mount(): void {
  render(
    <QueryClientProvider client={createQueryClient()}>
      <KeyboardStack>
        <Metrics cluster="prod" kind="pods" namespace="argo" name="api-0" />
      </KeyboardStack>
    </QueryClientProvider>,
  );
}

function control(text: string): HTMLButtonElement {
  const found = [...document.querySelectorAll('button')].find((one) => one.textContent?.includes(text));
  if (found === undefined) throw new Error(`no control saying ${text}`);
  return found;
}

function polylines(): string[] {
  return [...document.querySelectorAll('polyline')].map((one) => one.getAttribute('points') ?? '');
}

async function drawn(): Promise<void> {
  await waitFor(() => {
    expect(document.querySelector('svg[role="img"]')).not.toBeNull();
  });
}

function chartButton(): HTMLButtonElement {
  const found = document.querySelector('button[aria-label^="Open"]');
  if (!(found instanceof HTMLButtonElement)) throw new Error('the chart is not clickable');
  return found;
}

function dialog(): HTMLElement | null {
  const found = document.querySelector('[role="dialog"]');
  return found instanceof HTMLElement ? found : null;
}

describe('which measurement is drawn', () => {
  it('draws the network as two lines in two tones, with a legend', async () => {
    stubFetch({ [METRICS]: panel([cpu, networkIn, networkOut]) });
    mount();
    await drawn();
    expect(polylines()).toHaveLength(1);

    fireEvent.click(control('Network'));

    await waitFor(() => {
      expect(polylines()).toHaveLength(2);
    });
    // Two tones, so the legend can be read back onto the lines.
    const [first, second] = [...document.querySelectorAll('polyline')];
    expect(first?.getAttribute('class')).not.toBe(second?.getAttribute('class'));
    const text = document.body.textContent ?? '';
    expect(text).toContain('Network in');
    expect(text).toContain('Network out');
    // And nothing of CPU's: neither its peak nor its caption.
    expect(text).not.toContain('373');
    expect(text).not.toContain('mcores');
  });

  it('draws a chosen measurement with no line as its own absence, never another series', async () => {
    // The icon row offers the network and the answer carries no network line:
    // an out-of-contract answer, and one the client must not paper over by
    // drawing series[0].
    stubFetch({ [METRICS]: panel([cpu]) });
    mount();
    await drawn();
    const cpuLine = polylines()[0];

    fireEvent.click(control('Network'));

    await waitFor(() => {
      expect(document.querySelector('p[role="status"]')).not.toBeNull();
    });
    // No line at all - and in particular not CPU's.
    expect(polylines()).toEqual([]);
    expect(document.body.innerHTML).not.toContain(cpuLine ?? 'never');
    // The absence names the measurement it is the absence of, in one muted
    // line and not as a "Nothing here" notice.
    const notice = document.querySelector('p[role="status"]');
    expect(notice?.textContent).toContain('Network');
    expect(document.body.textContent).not.toContain('Nothing here');
  });

  it("draws an empty line as a short state, with the server's sentence on hover", async () => {
    // The contract's shape for a line that could not be read: sent, empty,
    // carrying why. The sentence is the server's and is drawn verbatim.
    const why = 'this cluster\'s Prometheus does not export container_network_receive_bytes_total, so there is nothing to chart';
    const emptyIn = {
      ...networkIn,
      data: { ...networkIn.data, values: [], trustworthy: false },
      reason: { code: 'not_installed', message: why },
    };
    const emptyOut = {
      ...networkOut,
      data: { ...networkOut.data, values: [], trustworthy: false },
      reason: { code: 'not_installed', message: why.replace('receive', 'transmit') },
    };
    stubFetch({ [METRICS]: panel([cpu, emptyIn, emptyOut]) });
    mount();
    await drawn();

    fireEvent.click(control('Network'));

    await waitFor(() => {
      expect(document.body.textContent).toContain('No metrics');
    });
    // The sentence is on hover only, never on screen.
    expect(document.body.textContent).not.toContain('container_network_receive_bytes_total');
    expect(document.querySelector('[title*="container_network_receive_bytes_total"]')).not.toBeNull();
    expect(document.querySelector('[title*="container_network_transmit_bytes_total"]')).not.toBeNull();
    expect(polylines()).toEqual([]);
  });

  it('groups lines by chart and not by matching the key against the icon row', async () => {
    // A pod's filesystem line: keyed `disk`, drawn under the `disk` icon. The
    // server must not key it by its lowered title (`container filesystem`),
    // which matches no icon and would leave the Filesystem tab drawing CPU.
    const disk = {
      key: 'disk',
      chart: 'disk',
      label: 'Container filesystem',
      unit: 'bytes',
      data: { startedAt: '2026-09-12T10:51:08Z', stepSeconds: 30, values: [10, 20, 30] },
    };
    stubFetch({
      [METRICS]: panel([cpu, disk], {
        metrics: [...choices, { key: 'disk', label: 'Filesystem', available: true }],
      }),
    });
    mount();
    await drawn();

    fireEvent.click(control('Filesystem'));

    await waitFor(() => {
      expect(document.body.textContent).toContain('Container filesystem');
    });
    expect(polylines()).toHaveLength(1);
    expect(polylines()[0]?.split(' ')).toHaveLength(3);
  });
});

/**
 * Changing the span is slow, and must not look broken by sending the whole
 * panel to its waiting state until the new window lands. Two mechanisms, both
 * asserted on the request or the DOM and never on a feeling: the previous
 * chart stays until the new one is drawn, with the span
 * row saying so; and a pointer resting on a span button asks for that span
 * before the click.
 */
describe('changing the span', () => {
  /** Answer the first window at once and hold every `range=24h` answer until the
   *  test releases it, so the moment between the click and the answer can be
   *  looked at. The two answers are told apart by their sample count. */
  function gated(first: StubReply, wider: StubReply): { release: () => void; asked: string[] } {
    const asked: string[] = [];
    let release: () => void = () => undefined;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    stubFetch({ [METRICS]: first });
    const answerFirst = globalThis.fetch;
    stubFetch({ [METRICS]: wider });
    const answerWider = globalThis.fetch;
    vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url);
      asked.push(url);
      if (!url.includes('range=24h')) return answerFirst(input, init);
      await gate;
      return answerWider(input, init);
    });
    return { release, asked };
  }

  const wider = { ...cpu, data: { ...cpu.data, values: [1, 2, 3, 4, 5, 6] } };

  it('keeps the previous chart on screen, saying so, until the new one lands', async () => {
    const { release } = gated(panel([cpu]), panel([wider]));
    mount();
    await drawn();
    const before = polylines()[0];
    expect(before?.split(' ')).toHaveLength(4);

    fireEvent.click(control('24h'));

    // Between the click and the answer: the old line is still there, and the
    // span row says a newer answer is on its way. Not a blank, not a spinner
    // over the chart - the answer on screen is still the last true answer.
    await waitFor(() => {
      expect(document.body.textContent).toContain('settling');
    });
    expect(polylines()).toEqual([before]);
    // And the strip already shows the span that was asked for, so the marker
    // is read against the right button.
    expect(control('24h').getAttribute('aria-pressed')).toBe('true');

    release();

    await waitFor(() => {
      expect(polylines()[0]?.split(' ')).toHaveLength(6);
    });
    expect(document.body.textContent).not.toContain('settling');
  });

  it('asks for a span the pointer has rested on, before any click', async () => {
    const { asked } = gated(panel([cpu]), panel([wider]));
    mount();
    await drawn();
    expect(asked.filter((one) => one.includes('range=24h'))).toEqual([]);

    // `pointerOver`, not `pointerEnter`: React derives `onPointerEnter` from the
    // bubbling `pointerover`, and dispatching `pointerenter` reaches nothing.
    fireEvent.pointerOver(control('24h'));

    await waitFor(() => {
      expect(asked.filter((one) => one.includes('range=24h'))).toHaveLength(1);
    });
    // And the panel still shows the span it was asked for: reading ahead is
    // not choosing.
    expect(control('1h').getAttribute('aria-pressed')).toBe('true');
    expect(control('24h').getAttribute('aria-pressed')).toBe('false');
    expect(polylines()[0]?.split(' ')).toHaveLength(4);
  });
});

/**
 * Zoom: clicking a chart opens it large. `components/Dialog` owns Escape, the veil and
 * the focus handover, so the assertions on those are that the chart is inside
 * one.
 */
describe('click to zoom', () => {
  it('opens the same lines large, with the axes labeled and every sample readable', async () => {
    stubFetch({ [METRICS]: panel([cpu, networkIn, networkOut]) });
    mount();
    await drawn();
    fireEvent.click(control('Network'));
    await waitFor(() => {
      expect(polylines()).toHaveLength(2);
    });
    const small = polylines();

    fireEvent.click(chartButton());

    await waitFor(() => {
      expect(dialog()).not.toBeNull();
    });
    const box = dialog();
    if (box === null) throw new Error('unreachable');
    // The same lines: same sample space, same scale, so nothing is recomputed
    // between the small chart and the large one.
    const large = [...box.querySelectorAll('polyline')].map((one) => one.getAttribute('points'));
    expect(large).toEqual(small);
    // Named for what it shows.
    expect(box.textContent).toContain('Network');
    // The axes: the peak at the top, zero at the bottom, and the two ends of
    // the window - all three in one multiple, the peak's, so the steps are the
    // same size (not `9000 bytes/s`).
    expect(box.textContent).toContain('8.8 KiB/s');
    expect(box.textContent).toContain('4.4 KiB/s');
    expect(box.textContent).toContain('0 KiB/s');
    // Every sample is hoverable and says what it is: one readout per sample
    // column, carrying the time and the value of each line at that moment.
    const readouts = [...box.querySelectorAll('svg title')].map((one) => one.textContent ?? '');
    expect(readouts).toHaveLength(4);
    // Each figure in its own multiple, which is what keeps the small line
    // readable under the large one: 9000 B/s is 8.8 KiB/s and 400 B/s is not
    // 0.4 KiB/s.
    expect(readouts[3]).toContain('8.8 KiB/s');
    expect(readouts[3]).toContain('400 B/s');
    expect(readouts[3]).toContain('Network in');
  });

  it('closes on Escape and gives the keyboard back to the chart', async () => {
    stubFetch({ [METRICS]: panel([cpu]) });
    mount();
    await drawn();

    const opener = chartButton();
    opener.focus();
    fireEvent.click(opener);
    await waitFor(() => {
      expect(dialog()).not.toBeNull();
    });
    // The keyboard is inside the dialog while it is up.
    expect(dialog()?.contains(document.activeElement)).toBe(true);

    fireEvent.keyDown(window, { key: 'Escape' });

    await waitFor(() => {
      expect(dialog()).toBeNull();
    });
    // Dismissing the dialog returns focus to the icon that was clicked rather
    // than to the top of the document.
    expect(document.activeElement).toBe(chartButton());
    // And the small chart is still there.
    expect(polylines()).toHaveLength(1);
  });
});

/**
 * The enlarged chart can change its span and its measurement: the veil covers
 * the panel's buttons, so without controls in the dialog changing the span
 * would mean closing it first. The span is the panel's, so the small chart
 * behind the dialog follows it.
 */
describe('the enlarged chart carries the span and the measurement', () => {
  const memory = {
    key: 'memory',
    chart: 'memory',
    label: 'Memory',
    unit: 'bytes',
    data: { startedAt: '2026-09-12T10:51:08Z', stepSeconds: 30, values: [1048576, 2097152, 3145728] },
  };
  const sixHours = { ...cpu, data: { ...cpu.data, stepSeconds: 180, values: [1, 2, 3, 4, 5, 6] } };
  const spans = [
    { key: '1h', label: '1h', selected: true },
    { key: '6h', label: '6h' },
  ];
  const offered = [
    { key: 'cpu', label: 'CPU', available: true, selected: true },
    { key: 'memory', label: 'Memory', available: true },
  ];

  /** One answer per span, and every URL asked for. */
  function serve(): string[] {
    const asked: string[] = [];
    vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
      const url = String(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url);
      asked.push(url);
      const wide = url.includes('range=6h');
      const body = {
        state: 'ok',
        data: {
          series: [wide ? sixHours : cpu, memory],
          metrics: offered,
          windows: spans.map((one) => ({ ...one, selected: one.key === (wide ? '6h' : '1h') })),
        },
      };
      return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }));
    });
    return asked;
  }

  function inDialog(text: string): HTMLButtonElement {
    const found = [...(dialog()?.querySelectorAll('button') ?? [])].find((one) => one.textContent === text);
    if (!(found instanceof HTMLButtonElement)) throw new Error(`no ${text} in the dialog`);
    return found;
  }

  function dialogLine(): string[] {
    return [...(dialog()?.querySelectorAll('polyline') ?? [])].map((one) => one.getAttribute('points') ?? '');
  }

  it('changes the span from inside the dialog, which stays open and redraws, and the panel follows', async () => {
    const asked = serve();
    mount();
    await drawn();
    fireEvent.click(chartButton());
    await waitFor(() => {
      expect(dialog()).not.toBeNull();
    });
    expect(dialogLine()[0]?.split(' ')).toHaveLength(4);
    expect(inDialog('1h').getAttribute('aria-pressed')).toBe('true');

    fireEvent.click(inDialog('6h'));

    // The request carries the span, and the dialog redraws with its samples
    // without having closed.
    await waitFor(() => {
      expect(asked.some((one) => one.includes('range=6h'))).toBe(true);
    });
    await waitFor(() => {
      expect(dialogLine()[0]?.split(' ')).toHaveLength(6);
    });
    expect(dialog()).not.toBeNull();
    expect(inDialog('6h').getAttribute('aria-pressed')).toBe('true');

    fireEvent.keyDown(window, { key: 'Escape' });
    await waitFor(() => {
      expect(dialog()).toBeNull();
    });
    // The span was the panel's all along.
    expect(control('6h').getAttribute('aria-pressed')).toBe('true');
    expect(control('1h').getAttribute('aria-pressed')).toBe('false');
    expect(polylines()[0]?.split(' ')).toHaveLength(6);
  });

  it('switches to Memory inside the dialog and redraws it with Memory', async () => {
    serve();
    mount();
    await drawn();
    fireEvent.click(chartButton());
    await waitFor(() => {
      expect(dialog()).not.toBeNull();
    });

    fireEvent.click(inDialog('Memory'));

    await waitFor(() => {
      expect(dialogLine()[0]?.split(' ')).toHaveLength(3);
    });
    const box = dialog();
    expect(box?.querySelector('h2, [role="heading"]')?.textContent ?? box?.textContent).toContain('Memory');
    expect(inDialog('Memory').getAttribute('aria-pressed')).toBe('true');
    // Memory's own axis, in its own unit, and nothing of CPU's.
    expect(box?.textContent).toContain('MiB');
    expect(box?.textContent).not.toContain('mcores');
  });

  /** The same, except that the 6h span cannot be read: a refusal in the
   *  envelope, or the request itself failing. */
  function serveFailingWide(how: 'refusal' | 'http'): string[] {
    const asked: string[] = [];
    vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
      const url = String(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url);
      asked.push(url);
      const json = (body: unknown, status = 200) =>
        Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }));
      if (url.includes('range=6h')) {
        return how === 'refusal'
          ? json({ state: 'unreachable', reason: { code: 'timeout', message: 'Prometheus did not answer in time' } })
          : json({ status: 502, title: 'Bad Gateway' }, 502);
      }
      return json({
        state: 'ok',
        data: {
          series: [cpu, memory],
          metrics: offered,
          windows: spans.map((one) => ({ ...one, selected: one.key === '1h' })),
        },
      });
    });
    return asked;
  }

  /**
   * `Panel` - which holds the picked measurement and the open dialog - must
   * not sit under `ValueView`, or a span whose read failed or was refused
   * would unmount it: the dialog would close and the pick fall back to CPU.
   */
  it.each(['refusal', 'http'] as const)(
    'a span that cannot be read (%s) keeps the dialog open, the measurement picked, and the chart drawn',
    async (how) => {
      const asked = serveFailingWide(how);
      mount();
      await drawn();
      fireEvent.click(chartButton());
      await waitFor(() => {
        expect(dialog()).not.toBeNull();
      });
      fireEvent.click(inDialog('Memory'));
      await waitFor(() => {
        expect(dialogLine()[0]?.split(' ')).toHaveLength(3);
      });

      fireEvent.click(inDialog('6h'));

      // A 5xx is retried twice by the query client before it is a failure.
      await waitFor(
        () => {
          expect(dialog()?.querySelector('[data-span-failed]')?.textContent).toMatch(/^6h could not be read: /);
        },
        { timeout: 8_000 },
      );
      if (how === 'refusal') {
        expect(dialog()?.querySelector('[data-span-failed]')?.textContent).toContain('Prometheus did not answer in time');
      }
      expect(asked.some((one) => one.includes('range=6h'))).toBe(true);
      // Still open, still Memory, still the span that was drawn.
      expect(dialog()).not.toBeNull();
      expect(inDialog('Memory').getAttribute('aria-pressed')).toBe('true');
      expect(inDialog('1h').getAttribute('aria-pressed')).toBe('true');
      expect(inDialog('6h').getAttribute('aria-pressed')).toBe('false');
      expect(dialogLine()[0]?.split(' ')).toHaveLength(3);

      // Picking the drawn span again clears the line.
      fireEvent.click(inDialog('1h'));
      await waitFor(() => {
        expect(dialog()?.querySelector('[data-span-failed]')).toBeNull();
      });
      expect(inDialog('Memory').getAttribute('aria-pressed')).toBe('true');
    },
    // The 5xx case waits out the query client's two retries, about 3 s.
    15_000,
  );

  /**
   * The span drawn before a pick must not be read back only from the cache,
   * which lets go of a read nothing observes after its `gcTime`: a pick that
   * failed after that would find nothing to draw, and the panel - with the
   * dialog and the measurement picked - would go.
   */
  it('keeps the dialog and the chart when the drawn span has left the cache before a pick fails', async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    vi.stubGlobal('fetch', async (input: RequestInfo | URL) => {
      const url = String(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url);
      const json = (body: unknown) =>
        new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
      if (url.includes('range=6h')) {
        await gate;
        return json({ state: 'unreachable', reason: { code: 'timeout', message: 'Prometheus did not answer in time' } });
      }
      return json({
        state: 'ok',
        data: {
          series: [cpu, memory],
          metrics: offered,
          windows: spans.map((one) => ({ ...one, selected: one.key === '1h' })),
        },
      });
    });
    const client = createQueryClient();
    render(
      <QueryClientProvider client={client}>
        <KeyboardStack>
          <Metrics cluster="prod" kind="pods" namespace="argo" name="api-0" />
        </KeyboardStack>
      </QueryClientProvider>,
    );
    await drawn();
    fireEvent.click(chartButton());
    await waitFor(() => {
      expect(dialog()).not.toBeNull();
    });
    fireEvent.click(inDialog('Memory'));
    await waitFor(() => {
      expect(dialogLine()[0]?.split(' ')).toHaveLength(3);
    });

    fireEvent.click(inDialog('6h'));
    // The drawn span's read is collected while the pick is in flight - what
    // `gcTime` does to a read nothing observes any more.
    client.removeQueries({ predicate: (one) => !JSON.stringify(one.queryKey).includes('6h') });
    release();

    await waitFor(() => {
      expect(dialog()?.querySelector('[data-span-failed]')?.textContent).toMatch(/^6h could not be read: /);
    });
    expect(dialog()).not.toBeNull();
    expect(inDialog('Memory').getAttribute('aria-pressed')).toBe('true');
    expect(inDialog('1h').getAttribute('aria-pressed')).toBe('true');
    expect(dialogLine()[0]?.split(' ')).toHaveLength(3);
  });

  /** Every read answered, except the spans and objects `refuse` names, whose
   *  read waits for `gate` and is then refused. */
  function serveRefusing(refuse: (url: string) => boolean, gate: Promise<void> = Promise.resolve()): void {
    vi.stubGlobal('fetch', async (input: RequestInfo | URL) => {
      const url = String(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url);
      const json = (body: unknown) =>
        new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });
      if (refuse(url)) {
        await gate;
        return json({ state: 'unreachable', reason: { code: 'timeout', message: 'Prometheus did not answer in time' } });
      }
      const wide = url.includes('range=6h');
      return json({
        state: 'ok',
        data: {
          series: [wide ? sixHours : cpu, memory],
          metrics: offered,
          windows: spans.map((one) => ({ ...one, selected: one.key === (wide ? '6h' : '1h') })),
        },
      });
    });
  }

  /**
   * The drawer reuses one `Metrics` for the next object, so the answer kept
   * for a failed pick must be tied to the object it was read for: pod B's
   * failed read must not draw pod A's chart under B's name. Nothing of A's
   * may be drawn on B.
   */
  it("never draws another object's kept chart when this object's read fails", async () => {
    serveRefusing((url) => url.includes('api-1'));
    const client = createQueryClient();
    const at = (name: string) => (
      <QueryClientProvider client={client}>
        <KeyboardStack>
          <Metrics cluster="prod" kind="pods" namespace="argo" name={name} />
        </KeyboardStack>
      </QueryClientProvider>
    );
    const view = render(at('api-0'));
    await drawn();
    fireEvent.click(control('6h'));
    await waitFor(() => {
      expect(polylines()[0]?.split(' ')).toHaveLength(6);
    });

    view.rerender(at('api-1'));
    await waitFor(() => {
      expect(document.body.textContent).toContain('Prometheus did not answer in time');
    });
    expect(polylines()).toEqual([]);
    expect(document.querySelector('[data-span-failed]')).toBeNull();
  });

  it("prefers the cache's answer for the drawn span over the one kept with it", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    serveRefusing((url) => url.includes('range=6h'), gate);
    const client = createQueryClient();
    render(
      <QueryClientProvider client={client}>
        <KeyboardStack>
          <Metrics cluster="prod" kind="pods" namespace="argo" name="api-0" />
        </KeyboardStack>
      </QueryClientProvider>,
    );
    await drawn();
    expect(polylines()[0]?.split(' ')).toHaveLength(4);

    fireEvent.click(control('6h'));
    // A fresher answer for the drawn span lands in the cache meanwhile.
    const fresher = { ...cpu, data: { ...cpu.data, values: [1, 2, 3, 4, 5] } };
    client.setQueryData(metricsQuery('prod', 'pods', 'argo', 'api-0', {}).queryKey, {
      state: 'ok',
      data: { series: [fresher, memory], metrics: offered, windows: spans },
    } as never);
    release();

    await waitFor(() => {
      expect(document.querySelector('[data-span-failed]')?.textContent).toMatch(/^6h could not be read: /);
    });
    expect(polylines()[0]?.split(' ')).toHaveLength(5);
  });

  /**
   * The dialog draws the same controls as the panel behind its veil, so while
   * it is open there must not be two groups named "Measurement", and the panel
   * behind must not take focus.
   */
  it('makes the panel behind the dialog inert and names the two copies of the controls apart', async () => {
    serve();
    mount();
    await drawn();
    const behind = document.querySelector('[data-metrics-behind]');
    expect(behind?.hasAttribute('inert')).toBe(false);

    fireEvent.click(chartButton());
    await waitFor(() => {
      expect(dialog()).not.toBeNull();
    });

    expect(document.querySelector('[data-metrics-behind]')?.hasAttribute('inert')).toBe(true);
    expect(document.querySelector('[data-metrics-behind]')?.contains(dialog())).toBe(false);
    const names = [...document.querySelectorAll('[role="group"]')].map((one) => one.getAttribute('aria-label'));
    expect(names).toEqual(['Measurement', 'Window', 'Measurement, enlarged chart', 'Window, enlarged chart']);
    expect(new Set(names).size).toBe(names.length);

    fireEvent.keyDown(window, { key: 'Escape' });
    await waitFor(() => {
      expect(dialog()).toBeNull();
    });
    expect(document.querySelector('[data-metrics-behind]')?.hasAttribute('inert')).toBe(false);
  });
});

/**
 * The full-size chart is `layers.ts`'s `Overlay` row, not the `Dialog`
 * default.
 *
 * The table in `keyboard/layers.ts` says `Overlay` yields to nothing. Without a
 * `layer` the chart would claim `Dialog` - the layer the manifest editor is
 * on. That is harmless while nothing raises the chart from inside a dialog and
 * the tie-break covers the rest.
 *
 * **Both registration orders, because one of them passes on the wrong
 * numbers.** With the dialog registered first the chart is the newest claimant
 * in its own layer, so "whatever opened last" hands it the key whether the two
 * share a layer or not - that assertion is green on the defect. Only the
 * reverse order asks the question this file means to ask;
 * `keyboard/stack.test.ts` holds the same pair for a confirmation against a
 * dialog.
 */
describe('the full-size chart claims the overlay layer, not the dialog one', () => {
  async function zoomed(): Promise<void> {
    stubFetch({ [METRICS]: panel([cpu]) });
    mount();
    await drawn();
    fireEvent.click(chartButton());
    await waitFor(() => {
      expect(dialog()).not.toBeNull();
    });
  }

  it('takes Escape from a dialog registered before it', async () => {
    const under = vi.fn(() => true);
    const release = claimKey(KeyLayer.Dialog, under);
    await zoomed();

    fireEvent.keyDown(window, { key: 'Escape' });

    await waitFor(() => {
      expect(dialog()).toBeNull();
    });
    expect(under).not.toHaveBeenCalled();
    release();
  });

  it('takes Escape from a dialog registered after it', async () => {
    await zoomed();
    // The order that tells the two mechanisms apart: the thing underneath is
    // now the newest claimant, so a shared layer would give it the key and the
    // chart would stay open over a dialog that had just closed behind it.
    const under = vi.fn(() => true);
    const release = claimKey(KeyLayer.Dialog, under);

    fireEvent.keyDown(window, { key: 'Escape' });

    await waitFor(() => {
      expect(dialog()).toBeNull();
    });
    expect(under).not.toHaveBeenCalled();
    release();
  });
});

/**
 * A network reading is not printed in raw bytes, six digits at a time
 * (`402210 bytes/s now · peak 476917 bytes/s`): nobody reads six digits, and
 * the memory column already prints `14.2 GiB`. One rule for every family the panel draws, so there is a case
 * here per family and one at each boundary - a special case for the network
 * would be the same defect wearing better clothes.
 *
 * Binary, because `textutil.BytesCompact` is what prints the memory column and
 * a second convention for one unit is how a third spelling gets added.
 */
describe('the figures are scaled to a multiple a human reads', () => {
  /** One panel carrying one line, mounted on its own. Mounted on its own
   *  because two renders in one case leave two charts in the body and the
   *  assertion then reads the first one. */
  const legend = async (unit: string, values: readonly number[]): Promise<string> => {
    cleanup();
    stubFetch({
      [METRICS]: panel(
        [
          {
            key: 'net',
            chart: 'network',
            label: 'Network in',
            unit,
            data: { startedAt: '2026-09-12T10:51:08Z', stepSeconds: 30, values },
          },
        ],
        { metrics: [{ key: 'network', label: 'Network', available: true, selected: true }] },
      ),
    });
    mount();
    await drawn();
    return document.body.textContent ?? '';
  };

  it("prints the operator's own reading in kibibytes per second", async () => {
    const text = await legend('bytes/s', [402210, 476917]);
    // 476917 B/s is the last sample and the peak, and three significant
    // figures of 465.7 KiB/s is 466, in the console's binary convention.
    expect(text).toContain('466 KiB/s now · peak 466 KiB/s');
    expect(text).not.toContain('402210');
    expect(text).not.toContain('476917');
    // The caption still names what was measured; the figures say how much.
    expect(text).toContain('in bytes/s');
  });

  it('leaves a small rate alone rather than rounding it to nothing', async () => {
    const text = await legend('bytes/s', [40, 40]);
    expect(text).toContain('40 B/s');
    expect(text).not.toContain('KiB/s');
  });

  it('scales a level in the same family and the same convention', async () => {
    expect(await legend('bytes', [15_246_565_376])).toContain('14.2 GiB');
  });

  it('stays under a kibibyte up to 1023 bytes', async () => {
    expect(await legend('bytes', [1023])).toContain('1023 B');
  });

  it('crosses to kibibytes at 1024 and not at 1000', async () => {
    expect(await legend('bytes', [1024])).toContain('1 KiB');
  });

  it('stays in kibibytes up to a mebibyte', async () => {
    expect(await legend('bytes', [1_048_575])).toContain('1024 KiB');
  });

  it('crosses to mebibytes at 1048576', async () => {
    expect(await legend('bytes', [1_048_576])).toContain('1 MiB');
  });

  it('prints millicores under one core, the way a request is written', async () => {
    expect(await legend('mcores', [402])).toContain('402m');
  });

  it('stays in millicores at 999', async () => {
    expect(await legend('mcores', [999])).toContain('999m');
  });

  it('crosses to cores at 1000', async () => {
    expect(await legend('mcores', [1000])).toContain('1 cores');
  });

  it('prints a whole number of cores without a trailing zero', async () => {
    expect(await legend('mcores', [8000])).toContain('8 cores');
  });

  it('keeps one decimal where a core is not whole', async () => {
    expect(await legend('mcores', [3200])).toContain('3.2 cores');
  });

  it('leaves a family it has never heard of exactly as the server sent it', async () => {
    // Scaling a unit whose steps are unknown would invent a conversion. The number is rounded; the label is untouched.
    expect(await legend('widgets', [1234])).toContain('1234 widgets');
  });
});

/**
 * Usage and history are two questions.
 *
 * On a cluster with metrics-server and no Prometheus the panel must not draw
 * "Nothing here" over a pod whose CPU the Pods list is showing, nor say one
 * sentence three times - under the chart, in the caption ("read from
 * docker-desktop has metrics-server and no Prometheus, so it holds no history
 * to query over the last 1h") and as "Read from ...". The server sends what the
 * line is *now* beside the reason it has no series, and says the reason once;
 * these pin how each state draws.
 */
describe('usage now, and history', () => {
  const NO_HISTORY = 'docker-desktop has metrics-server and no Prometheus, so it holds no history to query';

  const cpuNow = {
    key: 'cpu',
    chart: 'cpu',
    label: 'CPU',
    unit: 'mcores',
    data: { startedAt: '2026-10-10T10:00:00Z', stepSeconds: 30, values: [], trustworthy: false },
    reason: { code: 'not_installed', message: NO_HISTORY },
    current: { value: 12, trustworthy: true, text: '12m' },
  };

  function count(text: string): number {
    return (document.body.textContent ?? '').split(text).length - 1;
  }

  function spans(): HTMLButtonElement[] {
    return [...document.querySelectorAll('[role="group"][aria-label="Window"] button')].filter(
      (one): one is HTMLButtonElement => one instanceof HTMLButtonElement,
    );
  }

  it('instant only: draws the reading of now, and grays the spans with "no Prometheus detected"', async () => {
    // Metrics-server and no Prometheus. CPU stays
    // offered and shows the figure; no span of history can be chosen, so the
    // span buttons are grayed and say why on hover - which is where the
    // reason there is no series is said, once, and not again under the figure.
    stubFetch({ [METRICS]: panel([cpuNow]) });
    mount();

    await waitFor(() => {
      expect(document.querySelector('[data-reading="now"]')).not.toBeNull();
    });
    const now = document.querySelector('[data-reading="now"]');
    expect(now?.textContent).toContain('12m');
    expect(now?.textContent).toContain('now');
    // Not a chart of one point: a single reading implies no history.
    expect(document.querySelector('svg[role="img"]')).toBeNull();
    expect(control('CPU').disabled).toBe(false);

    expect(spans()).toHaveLength(windows.length);
    for (const span of spans()) {
      expect(span.disabled).toBe(true);
      expect(span.title).toBe(`No Prometheus detected: ${NO_HISTORY}`);
    }
    // Said once - on the spans - and never as "Nothing here".
    expect(count(NO_HISTORY)).toBe(0);
    expect(document.body.textContent).not.toContain('Nothing here');
    expect(document.body.textContent).not.toContain('Read from');
  });

  it('an empty window on a Prometheus leaves the spans to be chosen', async () => {
    // `empty_scope` is a window with nothing in it, and a wider one is the
    // control that might find samples: graying it would take that away.
    const young = {
      ...cpuNow,
      reason: { code: 'empty_scope', message: 'no samples in this window' },
      current: undefined,
    };
    stubFetch({ [METRICS]: panel([young]) });
    mount();

    await waitFor(() => {
      expect(document.querySelector('p[role="status"]')).not.toBeNull();
    });
    for (const span of spans()) expect(span.disabled).toBe(false);
    expect(document.body.textContent).not.toContain('Nothing here');
  });

  it('history: draws the series and no reading of now', async () => {
    stubFetch({ [METRICS]: panel([cpu], { caption: 'the last 1h' }) });
    mount();
    await drawn();

    expect(document.querySelector('[data-reading="now"]')).toBeNull();
    expect(polylines()).toHaveLength(1);
    expect(document.body.textContent).toContain('the last 1h');
  });

  it('neither: CPU and memory grayed with the reason, and nothing drawn beneath', async () => {
    // Neither Prometheus nor metrics-server.
    // The server refuses CPU and memory like the network, so the row is four
    // grayed controls carrying the reason, nothing is selected, and nothing at
    // all is drawn under them - no "Nothing here", no sentence, no figure.
    const NONE = 'the cluster answered: no Prometheus service matched, and metrics.k8s.io is not registered';
    const refused = (key: string, label: string) => ({
      key,
      label,
      available: false,
      reason: { code: 'not_installed', message: NONE },
    });
    const untrusted = { ...cpuNow, reason: { code: 'not_installed', message: NONE }, current: undefined };
    const memory = { ...untrusted, key: 'memory', chart: 'memory', label: 'Memory', unit: 'bytes' };
    stubFetch({
      [METRICS]: panel([untrusted, memory], {
        metrics: [refused('cpu', 'CPU'), refused('memory', 'Memory'), refused('network', 'Network'), refused('disk', 'Filesystem')],
      }),
    });
    mount();

    await waitFor(() => {
      expect(control('Memory').disabled).toBe(true);
    });
    for (const label of ['CPU', 'Memory', 'Network', 'Filesystem']) {
      expect(control(label).disabled).toBe(true);
      expect(control(label).title).toBe(`${label}: ${NONE}`);
    }
    expect(document.querySelector('[data-reading="now"]')).toBeNull();
    expect(document.querySelector('p[role="status"]')).toBeNull();
    expect(document.querySelector('svg[role="img"]')).toBeNull();
    expect(document.body.textContent).not.toContain('Nothing here');
    expect(count(NONE)).toBe(0);
    for (const span of spans()) expect(span.title).toBe(`No Prometheus detected: ${NONE}`);
  });
});

/**
 * A finished pod's empty chart: the sentence names the
 * period that holds the run, and the panel draws it as a button that selects
 * it. The period the person chose is never changed without that press.
 */
describe("a finished pod's run", () => {
  const sentence = 'this pod ran 8s, from … to …, 21h56m ago — before this 1h window; the 24h period holds it';
  const empty = { startedAt: '2026-10-19T21:30:00Z', stepSeconds: 30, values: [] };
  const ranCPU = {
    key: 'cpu',
    chart: 'cpu',
    label: 'CPU',
    unit: 'mcores',
    data: empty,
    reason: { code: 'empty_scope', message: sentence },
    period: '24h',
  };
  const ranIn = {
    ...networkIn,
    data: empty,
    reason: { code: 'empty_scope', message: sentence },
    period: '24h',
    runTotal: { value: 3145728, trustworthy: true, text: '3 MiB' },
  };
  const ranOut = { ...networkOut, data: empty, reason: { code: 'empty_scope', message: sentence }, period: '24h' };

  function recorded(first: StubReply, wide: StubReply): string[] {
    const asked: string[] = [];
    stubFetch({ [METRICS]: first });
    const answerFirst = globalThis.fetch;
    stubFetch({ [METRICS]: wide });
    const answerWide = globalThis.fetch;
    vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url);
      asked.push(url);
      return url.includes('range=24h') ? answerWide(input, init) : answerFirst(input, init);
    });
    return asked;
  }

  function offer(): HTMLButtonElement | null {
    const found = document.querySelector('button[data-period-offer]');
    return found instanceof HTMLButtonElement ? found : null;
  }

  it('offers the period that holds the run as a button, and selects it only when pressed', async () => {
    const asked = recorded(panel([ranCPU, ranIn, ranOut]), panel([cpu]));
    mount();
    await waitFor(() => {
      expect(offer()).not.toBeNull();
    });
    expect(offer()?.textContent).toBe('Show the 24h period');
    expect(document.body.textContent).toContain('No metrics');
    expect(document.body.textContent).not.toContain(sentence);
    expect(document.querySelector(`[title="${sentence}"]`)).not.toBeNull();

    // Nothing but the press changes the period: the answer drawn, the
    // pointer resting on the button (which reads ahead, as the row does).
    fireEvent.pointerOver(offer() as HTMLButtonElement);
    await waitFor(() => {
      expect(asked.filter((one) => one.includes('range=24h'))).toHaveLength(1);
    });
    expect(control('1h').getAttribute('aria-pressed')).toBe('true');
    expect(control('24h').getAttribute('aria-pressed')).toBe('false');
    expect(polylines()).toEqual([]);

    fireEvent.click(offer() as HTMLButtonElement);

    // Exactly as pressing 24h in the row: the row says so, and its chart is drawn.
    await drawn();
    expect(control('24h').getAttribute('aria-pressed')).toBe('true');
    expect(control('1h').getAttribute('aria-pressed')).toBe('false');
    expect(offer()).toBeNull();
  });

  it("draws the network's run totals as one compact line, with no sentence and no box", async () => {
    // The server drops the period of a run read from one sample: no period
    // draws a rate from it, so a button naming one would change nothing.
    const once = { ...ranIn, period: undefined };
    const outOnce = { ...ranOut, period: undefined, runTotal: { value: 348160, trustworthy: true, text: '340 KiB' } };
    stubFetch({ [METRICS]: panel([ranCPU, once, outOnce]) });
    mount();
    await waitFor(() => {
      expect(offer()).not.toBeNull();
    });
    fireEvent.click(control('Network'));

    await waitFor(() => {
      expect(document.querySelectorAll('[data-reading="run"]')).toHaveLength(1);
    });
    const figure = document.querySelector('[data-reading="run"]');
    expect(figure?.textContent).toBe('Network in 3 MiB · Network out 340 KiB');
    expect(figure?.className).not.toMatch(/border|rounded|p-3/);
    expect(figure?.querySelector('.text-lg')).toBeNull();
    // Nothing else: not the sentence, not "over the whole run", not a button.
    expect(document.body.textContent).not.toContain(sentence);
    expect(document.body.textContent).not.toContain('over the whole run');
    expect(offer()).toBeNull();
  });

  it('keeps the sentence of a network line with no total, and one button for both lines', async () => {
    stubFetch({ [METRICS]: panel([ranCPU, ranIn, ranOut]) });
    mount();
    await waitFor(() => {
      expect(offer()).not.toBeNull();
    });
    fireEvent.click(control('Network'));

    await waitFor(() => {
      expect(document.querySelector('[data-reading="run"]')).not.toBeNull();
    });
    expect(document.querySelector('[data-reading="run"]')?.textContent).toBe('Network in 3 MiB');
    expect(document.body.textContent).toContain('Network out: No metrics');
    expect(document.body.textContent).not.toContain(sentence);
    expect(document.body.textContent).not.toContain(`Network in: ${sentence}`);
    expect(document.querySelectorAll('button[data-period-offer]')).toHaveLength(1);
  });

  it('offers no button where the sentence names no period, or the row does not offer it', async () => {
    const unnamed = { ...ranCPU, period: undefined };
    stubFetch({ [METRICS]: panel([unnamed, { ...ranIn, period: '30d' }, ranOut]) });
    mount();
    await waitFor(() => {
      expect(document.body.textContent).toContain('No metrics');
    });
    expect(offer()).toBeNull();
    fireEvent.click(control('Network'));
    await waitFor(() => {
      expect(document.querySelector('[data-reading="run"]')).not.toBeNull();
    });
    // network_tx still names 24h, which the row offers.
    expect(offer()?.textContent).toBe('Show the 24h period');
  });
});
