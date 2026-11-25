import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { Suspense, type ReactNode } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Container, Inspection, PodAnswer } from './data';
import { podQuery } from './data';
import { runtimeInspector as jvm } from './index';
import JVMTab from './JVMTab';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

/** The JVM tab on a pod, with a fetch stub standing in for `extras/runtimeinspect`. */

interface Asked {
  url: string;
  method: string;
  marked: boolean;
  body: string | null;
}

function stub(replies: Record<string, { status?: number; body: unknown }>): Asked[] {
  const asked: Asked[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
      const method = init?.method ?? 'GET';
      asked.push({
        url,
        method,
        marked: new Headers(init?.headers).get('X-KubeTower-Request') === '1',
        body: typeof init?.body === 'string' ? init.body : null,
      });
      // The tab probes for jcmd on display; a JDK container answers it.
      const reply = replies[`${method} ${url}`] ?? (method === 'POST' && url.endsWith('/tools') ? { body: { jcmd: true } } : undefined);
      if (reply === undefined) return Promise.resolve(new Response(`no stub for ${method} ${url}`, { status: 599 }));
      return Promise.resolve(
        new Response(JSON.stringify(reply.body), { status: reply.status ?? 200, headers: { 'Content-Type': 'application/json' } }),
      );
    }),
  );
  return asked;
}

function mount(element: ReactNode): void {
  const router = createMemoryRouter([{ path: '/c/:cluster/*', element: <Suspense>{element}</Suspense> }], {
    initialEntries: ['/c/kind/pods'],
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

const app: Container = {
  name: 'app',
  image: 'eclipse-temurin:21-jdk',
  java: true,
  hints: [{ source: 'image', reason: 'a Java image' }],
  request: { bytes: 268435456, text: '256Mi' },
  limit: { bytes: 536870912, text: '512Mi' },
  heap: { bytes: 134217728, text: '128.0 MiB', from: 'default', source: '25 % of the limit' },
  risk: { level: 'ok', percent: 25, sentence: 'room' },
  notes: [],
};

const POD = '/x/kind/runtime/pod/shop/billing-0';
const INSPECT = `${POD}/app/inspect`;

const pod = (java: boolean): PodAnswer => ({
  cluster: 'kind',
  namespace: 'shop',
  name: 'billing-0',
  phase: 'Running',
  java,
  containers: [{ ...app, java, hints: java ? app.hints : [] }],
});

const fig = (bytes: number, text: string) => ({ bytes, text });

const inspection: Inspection = {
  cluster: 'kind',
  namespace: 'shop',
  pod: 'billing-0',
  container: 'app',
  state: 'inspected',
  pid: 1,
  pidFrom: 'listed',
  sections: [
    { command: 'VM.version', state: 'ok' },
    {
      command: 'VM.native_memory summary',
      state: 'not-enabled',
      sentence: 'Native memory tracking is not enabled in this JVM. -XX:NativeMemoryTracking=summary',
    },
  ],
  summary: {
    vm: 'OpenJDK 64-Bit Server VM version 21.0.13+9-LTS',
    version: 'JDK 21.0.13',
    major: 21,
    uptime: '1 min 0 s',
    gc: 'G1',
    flags: [{ name: 'MaxHeapSize', value: '536870912', text: '512.0 MiB' }],
  },
  memory: {
    source: 'jvm',
    track: fig(536870912, '512.0 MiB'),
    segments: [
      { key: 'heap', label: 'Java heap', bytes: 35651584, text: '34.0 MiB', start: 0, width: 6.6, used: { bytes: 3045376, text: '2.9 MiB', width: 0.6 } },
      { key: 'outside', label: 'Outside the JVM or not tracked', bytes: 100000000, text: '95.4 MiB', start: 6.6, width: 18.6, unattributed: true },
    ],
    markers: [
      { key: 'limit', label: 'Limit', bytes: 536870912, text: '512.0 MiB', at: 100 },
      { key: 'workingSet', label: 'Working set', bytes: 167772160, text: '160.0 MiB', at: 31.3 },
    ],
    committed: fig(35651584, '34.0 MiB'),
    workingSet: fig(167772160, '160.0 MiB'),
    heapUsed: fig(3045376, '2.9 MiB'),
    heapCommitted: fig(35651584, '34.0 MiB'),
    heapMax: fig(536870912, '512.0 MiB'),
    metaspaceUsed: null,
    metaspaceCommitted: null,
    codeCacheUsed: null,
    codeCacheSize: null,
    heapGauge: { usedPct: 0.6, committedPct: 6.6 },
    notes: [],
  },
  commandLine: { jvmArgs: [{ text: '-Dspring.datasource.password=', secret: 'spring.datasource.password' }, { text: '-Xmx512m' }], javaCommand: [{ text: 'D' }] },
  properties: [
    { key: 'java.version', value: '21.0.13', masked: false },
    { key: 'spring.datasource.password', value: '••••••', masked: true },
  ],
  notes: [],
};

describe('the JVM tab', () => {
  it('is declared on pods and drawn where the spec says a container is probably Java, without running anything', async () => {
    const tab = jvm.tabs?.[0];
    expect(tab?.key).toBe('jvm');
    expect(tab?.kinds).toEqual(['pods']);
    const signal = new AbortController().signal;
    const object = { cluster: 'kind', kind: 'pods', namespace: 'shop', name: 'billing-0' };

    let asked = stub({ [`GET ${POD}`]: { body: pod(true) } });
    await expect(tab?.probe?.(object, signal)).resolves.toBe(true);
    expect(asked.map((a) => `${a.method} ${a.url}`)).toEqual([`GET ${POD}`]);

    asked = stub({ [`GET ${POD}`]: { body: pod(false) } });
    await expect(tab?.probe?.(object, signal)).resolves.toBe(false);
    await expect(tab?.probe?.({ ...object, namespace: undefined }, signal)).resolves.toBe(false);
    expect(asked.every((a) => a.method === 'GET')).toBe(true);
  });

  it('runs nothing when it opens, says what Inspect runs in its title, and posts with the request header on the click', async () => {
    const asked = stub({ [`GET ${POD}`]: { body: pod(true) }, [`POST ${INSPECT}`]: { body: inspection } });
    mount(<JVMTab cluster="kind" kind="pods" namespace="shop" name="billing-0" />);
    const button = await screen.findByRole('button', { name: 'Inspect' });
    expect(button.getAttribute('title')).toBe('Runs jcmd in container app (pods/exec, recorded in the audit log)');
    // No explanatory sentence is drawn under it.
    expect(document.body.textContent).not.toMatch(/Nothing runs until you click|recorded in the audit log/);
    // Nothing but the reads and the jcmd probe, a POST since #82.
    expect(asked.filter((a) => a.method !== 'GET' && !a.url.endsWith('/tools'))).toEqual([]);

    fireEvent.click(screen.getByRole('button', { name: 'Inspect' }));
    await screen.findByText('JDK 21.0.13');
    const post = asked.find((a) => a.method === 'POST' && !a.url.endsWith('/tools'));
    expect(post?.url).toBe(INSPECT);
    expect(post?.marked).toBe(true);

    // The server's geometry, drawn as given.
    const outside = document.querySelector<HTMLElement>('[data-segment="outside"]');
    expect(outside?.style.left).toBe('6.6%');
    expect(outside?.style.width).toBe('18.6%');
    expect(document.querySelector<HTMLElement>('[data-marker="workingSet"]')?.style.left).toBe('31.3%');
    expect(document.body.textContent).not.toContain('not attributed to anything');
    // NMT off is said in a word, not drawn as zero; the how-to is in the title.
    const nmt = document.querySelector('[data-jvm-section="not-enabled"]');
    expect(nmt?.textContent).toBe('VM.native_memory summary NMT off: add -XX:NativeMemoryTracking=summary to JAVA_TOOL_OPTIONS');
    expect(nmt?.getAttribute('title')).toContain('-XX:NativeMemoryTracking=summary');
  });

  it('draws the garbage collectors’ share of the uptime, young and other, on a scale of 10 %', async () => {
    stub({
      [`GET ${POD}`]: { body: pod(true) },
      [`POST ${INSPECT}`]: {
        body: {
          ...inspection,
          gc: {
            collectors: [
              { name: 'G1 young collection pauses', young: true, invocations: 18402, seconds: 1520.4, share: 0.6 },
              { name: 'G1 full collection pauses', young: false, invocations: 2, seconds: 3.1, share: 0 },
            ],
            policy: 'GarbageFirst',
            uptime: 273600,
            share: 0.6,
          },
        },
      },
    });
    mount(<JVMTab cluster="kind" kind="pods" namespace="shop" name="billing-0" />);
    fireEvent.click(await screen.findByRole('button', { name: 'Inspect' }));
    await screen.findByText('JDK 21.0.13');
    const gauge = document.querySelector('[data-jvm-gc]');
    expect(gauge?.textContent).toContain('GC 0.6 % of uptime · scale 10 %');
    expect(gauge?.textContent).toContain('Young18402 × · 1520.4 s');
    expect(document.querySelector<HTMLElement>('[data-gc-part="young"]')?.style.width).toBe('6%');
    // A collector that ran but rounds to 0 % still shows a sliver.
    expect(document.querySelector<HTMLElement>('[data-gc-part="old"]')?.style.width).toBe('0.5%');
  });

  it('reveals a masked value on a click, by running the one command again', async () => {
    const asked = stub({
      [`GET ${POD}`]: { body: pod(true) },
      [`POST ${INSPECT}`]: { body: inspection },
      [`POST ${INSPECT}/reveal`]: { body: { kind: 'property', name: 'spring.datasource.password', value: 'hunter2' } },
    });
    mount(<JVMTab cluster="kind" kind="pods" namespace="shop" name="billing-0" />);
    fireEvent.click(await screen.findByRole('button', { name: 'Inspect' }));
    await screen.findByText('JDK 21.0.13');
    expect(document.body.textContent).not.toContain('hunter2');
    const buttons = document.querySelectorAll<HTMLButtonElement>('[data-reveal="spring.datasource.password"]');
    const propertyButton = buttons[buttons.length - 1];
    if (propertyButton === undefined) throw new Error('no reveal button');
    fireEvent.click(propertyButton);
    await waitFor(() => expect(document.querySelector('[data-revealed="spring.datasource.password"]')?.textContent).toBe('hunter2'));
    const call = asked.find((a) => a.url === `${INSPECT}/reveal`);
    expect(call?.marked).toBe(true);
    expect(JSON.parse(call?.body ?? '{}')).toEqual({ pid: 1, kind: 'property', name: 'spring.datasource.password' });
  });

  it('names a failure the route named', async () => {
    stub({
      [`GET ${POD}`]: { body: pod(true) },
      [`POST ${INSPECT}`]: {
        status: 422,
        body: { code: 'no_jcmd', message: 'There is no jcmd in this image: a JRE or a distroless image carries none.' },
      },
    });
    mount(<JVMTab cluster="kind" kind="pods" namespace="shop" name="billing-0" />);
    fireEvent.click(await screen.findByRole('button', { name: 'Inspect' }));
    await screen.findByText('No jcmd: use a JDK image');
    const failure = document.querySelector('[data-jvm-failure="no_jcmd"]');
    expect(failure?.textContent).toBe('No jcmd: use a JDK image');
    expect(failure?.querySelector('[title]')?.getAttribute('title')).toContain('a JRE or a distroless image');
  });

  it('titles a read-only cluster\'s refusal, the line in its tooltip', async () => {
    const line = 'kind is read-only: no command, tunnel or dump.';
    stub({
      [`GET ${POD}`]: { body: pod(true) },
      [`POST ${INSPECT}`]: { status: 403, body: { code: 'read_only', message: line } },
    });
    mount(<JVMTab cluster="kind" kind="pods" namespace="shop" name="billing-0" />);
    fireEvent.click(await screen.findByRole('button', { name: 'Inspect' }));
    const title = await screen.findByText('Read-only cluster');
    expect(title.closest('[data-jvm-failure="read_only"]')).not.toBeNull();
    expect(title.getAttribute('title')).toBe(line);
  });

  it('draws an Attach disabled badge with the cause in its tooltip and greys the actions', async () => {
    const cause = 'The JVM does not support the attach mechanism.';
    stub({
      [`GET ${POD}`]: { body: pod(true) },
      [`POST ${INSPECT}`]: { status: 422, body: { code: 'attach_disabled', message: cause } },
    });
    mount(<JVMTab cluster="kind" kind="pods" namespace="shop" name="billing-0" />);
    fireEvent.click(await screen.findByRole('button', { name: 'Inspect' }));
    const badge = await screen.findByText('Attach disabled');
    expect(badge.getAttribute('title')).toBe(cause);
    expect(document.querySelector('[data-jvm-failure="attach_disabled"]')).toBeNull();
    const inspect = screen.getByRole('button', { name: 'Inspect again' });
    expect(inspect).toHaveProperty('disabled', true);
    expect(inspect.getAttribute('title')).toBe(cause);
    expect(screen.getByRole('button', { name: 'Thread dump' })).toHaveProperty('disabled', true);
  });

  it('lets the person pick a JVM when there are several, by main class and pid only', async () => {
    const asked = stub({
      [`GET ${POD}`]: { body: pod(true) },
      [`POST ${INSPECT}`]: {
        body: { ...inspection, state: 'choose', jvms: [{ pid: 1, mainClass: 'app.Main' }, { pid: 7, mainClass: 'worker.jar' }], sections: undefined, summary: undefined, memory: undefined },
      },
      [`POST ${INSPECT}?pid=7`]: { body: { ...inspection, pid: 7, pidFrom: 'chosen' } },
    });
    mount(<JVMTab cluster="kind" kind="pods" namespace="shop" name="billing-0" />);
    fireEvent.click(await screen.findByRole('button', { name: 'Inspect' }));
    fireEvent.click(await screen.findByRole('button', { name: /worker\.jar/ }));
    await screen.findByText('JDK 21.0.13');
    expect(asked.map((a) => a.url)).toContain(`${INSPECT}?pid=7`);
  });
});

describe('the JVM tab holds nothing open', () => {
  it('reads the pod once, caches it for a bounded time, and asks nothing after it is closed', async () => {
    const options = podQuery('kind', 'shop', 'billing-0');
    expect(options.gcTime).toBeLessThanOrEqual(60_000);
    expect(options.refetchOnWindowFocus).toBe(false);
    expect('refetchInterval' in options).toBe(false);

    const asked = stub({ [`GET ${POD}`]: { body: pod(true) }, [`POST ${INSPECT}`]: { body: inspection } });
    mount(<JVMTab cluster="kind" kind="pods" namespace="shop" name="billing-0" />);
    fireEvent.click(await screen.findByRole('button', { name: 'Inspect' }));
    await screen.findByText('JDK 21.0.13');
    const before = asked.length;
    cleanup();
    // A window regaining focus and time passing ask nothing once the tab is gone.
    window.dispatchEvent(new Event('focus'));
    document.dispatchEvent(new Event('visibilitychange'));
    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(asked.length).toBe(before);
    expect(asked.filter((a) => a.method === 'POST' && a.url === INSPECT)).toHaveLength(1);
    expect(asked.filter((a) => a.method === 'POST' && a.url.endsWith('/tools'))).toHaveLength(1);
  });

  it('draws a large answer without hanging', async () => {
    const memory = inspection.memory;
    if (memory === undefined) throw new Error('the fixture has a memory bar');
    const big: Inspection = {
      ...inspection,
      allFlags: Array.from({ length: 500 }, (_, i) => ({ name: `Flag${String(i)}`, value: String(i) })),
      properties: Array.from({ length: 1000 }, (_, i) => ({ key: `p.${String(i)}`, value: 'v'.repeat(200), masked: i % 50 === 0 })),
      memory: {
        ...memory,
        nmt: Array.from({ length: 64 }, (_, i) => ({ name: `Category ${String(i)}`, reserved: fig(1024, '1.0 KiB'), committed: fig(512, '512 B') })),
      },
    };
    stub({ [`GET ${POD}`]: { body: pod(true) }, [`POST ${INSPECT}`]: { body: big } });
    mount(<JVMTab cluster="kind" kind="pods" namespace="shop" name="billing-0" />);
    fireEvent.click(await screen.findByRole('button', { name: 'Inspect' }));
    const start = performance.now();
    await screen.findByText('JDK 21.0.13', undefined, { timeout: 10_000 });
    expect(performance.now() - start).toBeLessThan(8_000);
    expect(document.querySelectorAll('[data-jvm-tab] tr').length).toBe(1000 + 64);
  });
});
