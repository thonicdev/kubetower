import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { Suspense, type ReactNode } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { openedFromConsole } from './data';
import { CELL } from './Heatmap';
import { kubecron } from './index';
import KubeCronTab from './KubeCronTab';

// The stubbed `fetch` goes with the test that stubbed it. The extra's own
// setup file did this for every test before the extras came into the core;
// the core's (`ui/src/testing/setup.ts`) unmounts but restores no global.
afterEach(() => {
  vi.unstubAllGlobals();
});

/**
 * KubeCron in an object's panel: the owning CronJob's heatmap, the run's figures and a
 * Log button. Each stub is the body its Go route writes.
 */

/** The size utilities on each heatmap square and legend swatch, as drawn: the
 *  page's, a CronJob's and the tab's are one size. */
function drawnSizes(): Set<string> {
  const drawn = [...document.querySelectorAll('[data-day] > *, [data-legend] span.inline-block')];
  return new Set(drawn.flatMap((e) => [...e.classList].filter((c) => /^(size|w|h|aspect|min-w|basis)-/.test(c))));
}

type Replies = Record<string, { status?: number; body: unknown }>;

function stub(replies: Replies): ReturnType<typeof vi.fn> {
  const fetch = vi.fn((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
    const reply = replies[url];
    if (reply === undefined) return Promise.resolve(new Response(`no stub for ${url}`, { status: 599 }));
    return Promise.resolve(
      new Response(JSON.stringify(reply.body), { status: reply.status ?? 200, headers: { 'Content-Type': 'application/json' } }),
    );
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

function withProviders(element: ReactNode, path = '/c/docker-desktop/pods'): void {
  const router = createMemoryRouter([{ path: '/c/:cluster/*', element: <Suspense>{element}</Suspense> }], { initialEntries: [path] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

const run = (id: string, pod: string, status = 'failed') => ({
  id,
  pod,
  trigger: 'scheduled',
  startedAt: '2026-10-20T11:00:00Z',
  status,
  exitCode: 3,
  retryCount: 0,
  logSizeBytes: 40,
  durationMs: 5000,
});

const runAnswer = (podExists: boolean | undefined) => ({
  run: run('r2', 'nightly-29190-x7'),
  namespace: 'ops',
  cronJob: 'nightly',
  collector: { state: 'ok' },
  log: { lines: [{ at: '2026-10-20T11:00:01Z', text: 'ERROR: connection refused' }], sizeBytes: 40, expired: false, truncated: false, running: false },
  samples: { samples: [], intervalSeconds: 15, metricsEnabled: true },
  ...(podExists === undefined ? {} : { podExists }),
});

describe('the KubeCron tab', () => {
  it('is declared for CronJobs, Jobs and Pods, and lazy', () => {
    const tab = kubecron.tabs?.[0];
    expect(tab?.kinds).toEqual(['cronjobs', 'jobs']);
    expect(tab?.label).toBe('KubeCron');
    // On a Pod, only when a Job made it.
    const onPod = kubecron.tabs?.[1];
    expect(onPod?.kinds).toEqual(['pods']);
    expect(onPod?.key).toBe('runs');
    expect(onPod?.when?.({ labels: { 'job-name': 'nightly-29190' }, annotations: {} })).toBe(true);
    expect(onPod?.when?.({ labels: { app: 'guestbook-ui' }, annotations: {} })).toBe(false);
  });

  const DETAIL = '/x/docker-desktop/kubecron/cronjobs/ops/nightly';
  const today = new Date().toISOString().slice(0, 10);
  const at = (hour: string) => `${today}T${hour}:00:00Z`;
  const detail = (runs: unknown[], over: Record<string, unknown> = {}) => ({
    body: {
      cronJob: { namespace: 'ops', name: 'nightly', schedule: '0 * * * *', suspended: false, verdict: 'ok', activeCount: 0, runs: [] },
      collector: { state: 'ok', runDays: 90 },
      daily: { windowDays: 90, observedSince: '2020-01-01T00:00:00Z', days: [{ day: today, total: 2, succeeded: 1, running: 0, failed: 1 }] },
      runs: { runs, nextCursor: '' },
      ...over,
    },
  });
  const r2 = { ...run('r2', 'nightly-29190-x7'), startedAt: at('11') };
  const r3 = { ...run('r3', 'nightly-29191-y8', 'succeeded'), startedAt: at('12'), exitCode: 0 };

  it("on a Pod, draws its CronJob's heatmap with its own day and run picked, its figures and a Log button, and no log", async () => {
    const fetch = stub({
      '/x/docker-desktop/kubecron/pods/ops/nightly-29190-x7': {
        body: { cronJob: { namespace: 'ops', name: 'nightly' }, job: 'nightly-29190', collector: { state: 'ok' }, runs: [r2] },
      },
      [`${DETAIL}?day=${today}`]: detail([r3, r2]),
      '/x/docker-desktop/kubecron/runs/r2': {
        body: { ...runAnswer(false), run: r2, samples: { samples: [], intervalSeconds: 15, avgCpuMillicores: 12, maxCpuMillicores: 40, avgMemoryBytes: 20971520, maxMemoryBytes: 33554432, metricsEnabled: true } },
      },
    });
    withProviders(<KubeCronTab cluster="docker-desktop" kind="pods" namespace="ops" name="nightly-29190-x7" />);
    expect((await screen.findByRole('link', { name: 'ops/nightly' })).getAttribute('href')).toBe('/c/docker-desktop/x/kubecron/ops/nightly');
    // The whole CronJob's calendar, its day picked: asked for that day.
    expect(screen.getByRole('button', { name: new RegExp(`^Runs of ${today}:`) }).getAttribute('aria-pressed')).toBe('true');
    expect(fetch.mock.calls.map(([input]) => String(input))).toContain(`${DETAIL}?day=${today}`);
    // Every run of that day, this pod's marked and open.
    expect(await screen.findByText('· this pod')).toBeTruthy();
    expect(document.querySelectorAll('[data-run]')).toHaveLength(2);
    expect(document.querySelector('[data-run="r2"] button')?.getAttribute('aria-pressed')).toBe('true');
    const stats = await waitFor(() => {
      const s = document.querySelector('[data-run-stats]');
      if (s === null) throw new Error('no figures yet');
      return s;
    });
    expect(stats.textContent).toContain('Exit code3');
    expect(stats.textContent).toContain('12m · peak 40m');
    expect(stats.textContent).toContain('20 MiB · peak 32 MiB');
    // No log until Log is pressed; then it unfolds under the run, in the
    // tab, with full-page as its own button.
    expect(screen.queryByText('ERROR: connection refused')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Log' }));
    expect(await screen.findByText('connection refused', { exact: false })).toBeTruthy();
    expect(document.querySelector('[data-log-full]')?.getAttribute('href')).toBe('/c/docker-desktop/x/kubecron/runs/r2/log');
    fireEvent.click(screen.getByRole('button', { name: 'Close the log' }));
    await waitFor(() => expect(document.querySelector('[data-inline-log]')).toBeNull());
  });

  it('says a figure nobody recorded is missing, and why, never 0', async () => {
    stub({
      '/x/docker-desktop/kubecron/pods/ops/nightly-29190-x7': {
        body: { cronJob: { namespace: 'ops', name: 'nightly' }, job: 'nightly-29190', collector: { state: 'ok' }, runs: [r2] },
      },
      [`${DETAIL}?day=${today}`]: detail([r2]),
      '/x/docker-desktop/kubecron/runs/r2': { body: { ...runAnswer(false), run: r2, samples: { samples: [], intervalSeconds: 15, metricsEnabled: false } } },
    });
    withProviders(<KubeCronTab cluster="docker-desktop" kind="pods" namespace="ops" name="nightly-29190-x7" />);
    expect(await screen.findByText('This collector records no CPU or memory on this cluster.')).toBeTruthy();
    expect([...document.querySelectorAll('[data-missing]')].map((e) => e.textContent)).toEqual(['not recorded', 'not recorded']);
  });

  it("reads a running pod's CPU and memory from metrics-server when the collector has none yet", async () => {
    const running = { ...r2, status: 'running', exitCode: undefined, durationMs: undefined };
    stub({
      '/x/docker-desktop/kubecron/pods/ops/nightly-29190-x7': {
        body: { cronJob: { namespace: 'ops', name: 'nightly' }, job: 'nightly-29190', collector: { state: 'ok' }, runs: [running] },
      },
      [`${DETAIL}?day=${today}`]: detail([running]),
      '/x/docker-desktop/kubecron/runs/r2': {
        body: { ...runAnswer(true), run: running, livePod: 'nightly-29190-x7', samples: { samples: [], intervalSeconds: 15, metricsEnabled: true } },
      },
      '/api/c/docker-desktop/metrics?kind=pods&namespace=ops&name=nightly-29190-x7': {
        body: {
          state: 'ok',
          data: {
            series: [
              { key: 'cpu', label: 'CPU', unit: 'mcores', data: { startedAt: at('11'), stepSeconds: 60, values: [], trustworthy: true }, current: { value: 7, trustworthy: true, text: '7m' } },
              { key: 'memory', label: 'Memory', unit: 'bytes', data: { startedAt: at('11'), stepSeconds: 60, values: [1048576, null], trustworthy: true } },
            ],
          },
        },
      },
    });
    withProviders(<KubeCronTab cluster="docker-desktop" kind="pods" namespace="ops" name="nightly-29190-x7" />);
    expect(await screen.findByText('7m now')).toBeTruthy();
    expect(screen.getByText('1.0 MiB now')).toBeTruthy();
    expect(document.querySelector('[data-run-stats]')?.textContent).toContain('Exit coderunning');
  });

  it('on a Job, marks each attempt and opens the newest', async () => {
    const r4 = { ...run('r4', 'nightly-29190-z9'), startedAt: at('13') };
    stub({
      '/x/docker-desktop/kubecron/jobs/ops/nightly-29190': {
        body: { cronJob: { namespace: 'ops', name: 'nightly' }, job: 'nightly-29190', collector: { state: 'ok' }, runs: [r4, r2] },
      },
      // A day longer than one page: the rest is on the CronJob's page.
      [`${DETAIL}?day=${today}`]: detail([], { runs: { runs: [r4, r3, r2], nextCursor: '2026-10-23T00:00:00Z' } }),
      '/x/docker-desktop/kubecron/runs/r4': { body: { ...runAnswer(undefined), run: r4 } },
    });
    withProviders(<KubeCronTab cluster="docker-desktop" kind="jobs" namespace="ops" name="nightly-29190" />);
    await screen.findByRole('list', { name: 'Runs' });
    expect([...document.querySelectorAll('[data-own]')].map((e) => e.getAttribute('data-run'))).toEqual(['r4', 'r2']);
    expect(screen.getAllByText('· this job')).toHaveLength(2);
    expect(document.querySelector('[data-more-runs]')?.getAttribute('href')).toBe(`/c/docker-desktop/x/kubecron/ops/nightly?day=${today}`);
    expect((await screen.findByRole('button', { name: 'Log' })).getAttribute('data-log-toggle')).toBe('r4');
  });

  it('on a CronJob, opens on the newest runs, and a picked day narrows to its runs, one of which opens', async () => {
    const fetch = stub({
      [DETAIL]: detail([r3, r2]),
      [`${DETAIL}?day=${today}`]: detail([r2]),
      '/x/docker-desktop/kubecron/runs/r2': { body: { ...runAnswer(undefined), run: r2 } },
    });
    withProviders(<KubeCronTab cluster="docker-desktop" kind="cronjobs" namespace="ops" name="nightly" />);
    await screen.findByText('Newest');
    expect(document.querySelectorAll('[data-run]')).toHaveLength(2);
    // The page's squares and legend, not a smaller variant.
    expect(drawnSizes()).toEqual(new Set([CELL]));
    expect(document.querySelector('[data-legend]')).not.toBeNull();
    expect(document.querySelector('[data-run-stats]')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: new RegExp(`^Runs of ${today}:`) }));
    await waitFor(() => expect(document.querySelectorAll('[data-run]')).toHaveLength(1));
    expect(fetch.mock.calls.map(([input]) => String(input))).toContain(`${DETAIL}?day=${today}`);
    expect(document.querySelector('[data-more-runs]')).toBeNull();
    fireEvent.click(document.querySelector('[data-run="r2"] button') as HTMLElement);
    expect((await screen.findByRole('button', { name: 'Log' })).getAttribute('data-log-toggle')).toBe('r2');
  });

  it('keeps the picked day, run and open log when the reader comes back from the full-page log', async () => {
    stub({
      [DETAIL]: detail([r3, r2]),
      [`${DETAIL}?day=${today}`]: detail([r2]),
      '/x/docker-desktop/kubecron/runs/r2': { body: { ...runAnswer(undefined), run: r2 } },
    });
    const tab = <KubeCronTab cluster="docker-desktop" kind="cronjobs" namespace="ops" name="nightly" />;
    const router = createMemoryRouter(
      [
        { path: '/c/:cluster/cronjobs/:namespace/:name', element: <Suspense>{tab}</Suspense> },
        { path: '/c/:cluster/x/kubecron/runs/:id/log', element: <p>the log</p> },
      ],
      { initialEntries: ['/c/docker-desktop/cronjobs/ops/nightly?tab=x.kubecron.runs'] },
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );
    await screen.findByText('Newest');
    fireEvent.click(screen.getByRole('button', { name: new RegExp(`^Runs of ${today}:`) }));
    await waitFor(() => expect(document.querySelectorAll('[data-run]')).toHaveLength(1));
    fireEvent.click(document.querySelector('[data-run="r2"] button') as HTMLElement);
    fireEvent.click(await screen.findByRole('button', { name: 'Log' }));
    fireEvent.click(await screen.findByRole('link', { name: /Full page/ }));
    await screen.findByText('the log');
    // Opened from inside the console, so the log page will offer Back.
    expect(openedFromConsole(router.state.location.state)).toBe(true);
    // The core's own address is untouched.
    await router.navigate(-1);
    expect(router.state.location.search).toBe('?tab=x.kubecron.runs');
    await waitFor(() => expect(document.querySelector(`[data-day="${today}"] button`)?.getAttribute('aria-pressed')).toBe('true'));
    expect(document.querySelector('[data-run="r2"] button')?.getAttribute('aria-pressed')).toBe('true');
    expect((await screen.findByRole('button', { name: 'Log' })).getAttribute('aria-pressed')).toBe('true');
    expect(await waitFor(() => document.querySelector('[data-inline-log="r2"]'))).toBeTruthy();
    // A drawer's address is rewritten on the way back, without the entry's
    // state: the tab takes its pick back all the same.
    fireEvent.click(await screen.findByRole('link', { name: /Full page/ }));
    await screen.findByText('the log');
    await router.navigate('/c/docker-desktop/cronjobs/ops/nightly?tab=x.kubecron.runs&open=ops%2Fnightly');
    await waitFor(() => expect(document.querySelector(`[data-day="${today}"] button`)?.getAttribute('aria-pressed')).toBe('true'));
    expect(document.querySelector('[data-run="r2"] button')?.getAttribute('aria-pressed')).toBe('true');
    expect(await waitFor(() => document.querySelector('[data-inline-log="r2"]'))).toBeTruthy();
    // Taken once: the tab opened afresh later starts from its own default.
    await router.navigate('/c/docker-desktop/x/kubecron/runs/r2/log');
    await screen.findByText('the log');
    await router.navigate('/c/docker-desktop/cronjobs/ops/nightly?tab=x.kubecron.runs');
    await screen.findByText('Newest');
    expect(document.querySelector('[data-inline-log]')).toBeNull();
    // A ticket nobody came back for within two minutes is not handed out.
    fireEvent.click(screen.getByRole('button', { name: new RegExp(`^Runs of ${today}:`) }));
    await waitFor(() => expect(document.querySelectorAll('[data-run]')).toHaveLength(1));
    fireEvent.click(document.querySelector('[data-run="r2"] button') as HTMLElement);
    fireEvent.click(await screen.findByRole('button', { name: 'Log' }));
    fireEvent.click(await screen.findByRole('link', { name: /Full page/ }));
    await screen.findByText('the log');
    const later = Date.now() + 180_000;
    const clock = vi.spyOn(Date, 'now').mockReturnValue(later);
    await router.navigate('/c/docker-desktop/cronjobs/ops/nightly?tab=x.kubecron.runs&open=ops%2Fnightly');
    await screen.findByText('Newest');
    expect(document.querySelector('[data-inline-log]')).toBeNull();
    clock.mockRestore();
  });

  it('tells a day with no run from a history that could not be read', async () => {
    stub({ [DETAIL]: detail([], { runs: undefined, runsNote: 'the collector at kubecron/kubecron answered 500' }) });
    withProviders(<KubeCronTab cluster="docker-desktop" kind="cronjobs" namespace="ops" name="nightly" />);
    expect(await screen.findByText('the collector at kubecron/kubecron answered 500')).toBeTruthy();
    expect(screen.queryByText('No run recorded yet.')).toBeNull();
  });

  it('says in a sentence when no CronJob started the object, never as an empty history', async () => {
    stub({
      '/x/docker-desktop/kubecron/pods/ops/lonely': {
        body: { note: 'this Pod was not started by a Job, so no CronJob ran it', collector: { state: '' }, runs: [] },
      },
    });
    withProviders(<KubeCronTab cluster="docker-desktop" kind="pods" namespace="ops" name="lonely" />);
    expect(await screen.findByText('This Pod was not started by a Job, so no CronJob ran it.')).toBeTruthy();
    expect(screen.queryByRole('list', { name: 'Runs' })).toBeNull();
  });

  it('offers the install when the cluster has no collector', async () => {
    stub({
      '/x/docker-desktop/kubecron/jobs/ops/nightly-29190': {
        body: {
          cronJob: { namespace: 'ops', name: 'nightly' },
          job: 'nightly-29190',
          collector: { state: 'absent', detail: 'no KubeCron collector runs in this cluster.' },
          runs: [],
        },
      },
    });
    withProviders(<KubeCronTab cluster="docker-desktop" kind="jobs" namespace="ops" name="nightly-29190" />);
    expect(await screen.findByRole('heading', { name: 'Run history needs the KubeCron collector' })).toBeTruthy();
  });

  it("titles an object Kubernetes already deleted Not found, never Not installed", async () => {
    stub({
      '/x/docker-desktop/kubecron/jobs/kc-test/always-fails-29875149': {
        status: 404,
        body: { code: 'object_not_found', message: 'There is no Job kc-test/always-fails-29875149 on this cluster that this console may read.' },
      },
    });
    withProviders(<KubeCronTab cluster="docker-desktop" kind="jobs" namespace="kc-test" name="always-fails-29875149" />);
    expect(await screen.findByText('Not found')).toBeTruthy();
    expect(screen.queryByText('Not installed')).toBeNull();
  });

  it("links the run's Job to its page in the console while it is there", async () => {
    stub({
      '/x/docker-desktop/kubecron/pods/ops/nightly-29190-x7': {
        body: { cronJob: { namespace: 'ops', name: 'nightly' }, job: 'nightly-29190', collector: { state: 'ok' }, runs: [r2] },
      },
      [`${DETAIL}?day=${today}`]: detail([r2]),
      '/x/docker-desktop/kubecron/runs/r2': { body: { ...runAnswer(true), run: r2, job: 'nightly-29190' } },
    });
    withProviders(<KubeCronTab cluster="docker-desktop" kind="pods" namespace="ops" name="nightly-29190-x7" />);
    const link = await screen.findByRole('link', { name: 'Job ops/nightly-29190' });
    expect(link.getAttribute('href')).toBe('/c/docker-desktop/jobs/ops/nightly-29190');
  });
});
