import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { Suspense, type ReactNode } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { JavaThread, PodAnswer, ThreadDump, ThreadState } from './data';
import JVMTab from './JVMTab';
import { DumpView, THREAD_STEP, dumpFileName } from './ThreadDump';
import { withinCeiling } from '../../../ui/src/testing/ceiling';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

/** The thread dump of the JVM tab, with a fetch stub standing in for `extras/runtimeinspect`. */

const POD = '/x/kind/runtime/pod/shop/billing-0';
const THREADS = `${POD}/app/threads`;

const pod: PodAnswer = {
  cluster: 'kind',
  namespace: 'shop',
  name: 'billing-0',
  phase: 'Running',
  java: true,
  owner: null,
  containers: [
    {
      name: 'app',
      image: 'eclipse-temurin:21-jdk',
      java: true,
      hints: [{ source: 'image', reason: 'a Java image' }],
      request: null,
      limit: null,
      heap: null,
      risk: null,
      notes: [],
      workingSet: null,
    },
  ],
};

const worker = (name: string, other: string, lock: string, held: string): JavaThread => ({
  name,
  id: 18,
  daemon: false,
  prio: 5,
  state: 'BLOCKED',
  detail: 'on object monitor',
  status: 'waiting for monitor entry',
  cpu: '3.08ms',
  frames: [
    {
      at: 'D.lambda$main$0(D.java:4)',
      locks: [
        { kind: 'waiting to lock', id: lock, class: 'java.lang.Object' },
        { kind: 'locked', id: held, class: 'java.lang.Object' },
      ],
    },
    { at: 'java.lang.Thread.run(java.base@21.0.13/Thread.java:1583)' },
  ],
  waitingFor: { kind: 'waiting to lock', id: lock, class: 'java.lang.Object', heldBy: other },
  holds: [{ kind: 'locked', id: held, class: 'java.lang.Object' }],
});

const deadlocked: ThreadDump = {
  cluster: 'kind',
  namespace: 'shop',
  pod: 'billing-0',
  container: 'app',
  state: 'dumped',
  pid: 1,
  pidFrom: 'listed',
  taken: '2026-11-11 11:04:15',
  vm: 'OpenJDK 64-Bit Server VM (21.0.13+9-LTS mixed mode, sharing)',
  total: 3,
  daemon: 0,
  counts: [
    { state: 'RUNNABLE', count: 0 },
    { state: 'BLOCKED', count: 2 },
    { state: 'WAITING', count: 0 },
    { state: 'TIMED_WAITING', count: 1 },
    { state: 'NEW', count: 0 },
    { state: 'TERMINATED', count: 0 },
    { state: 'VM', count: 0 },
  ],
  threads: [
    { name: 'main', id: 1, daemon: false, prio: 5, state: 'TIMED_WAITING', detail: 'sleeping', frames: [{ at: 'D.main(D.java:6)' }] },
    worker('worker-a', 'worker-b', '0x50', '0x40'),
    worker('worker-b', 'worker-a', '0x40', '0x50'),
  ],
  deadlocks: [
    {
      kind: 'Java-level',
      cycle: [
        { thread: 'worker-a', waits: 'to lock monitor 0x1 (object 0x50, a java.lang.Object)', lock: '0x50', heldBy: 'worker-b' },
        { thread: 'worker-b', waits: 'to lock monitor 0x2 (object 0x40, a java.lang.Object)', lock: '0x40', heldBy: 'worker-a' },
      ],
    },
  ],
  raw: '2026-11-11 11:04:15\nFull thread dump …\n"main" #1 prio=5\n',
  notes: [],
};

function stub(replies: Record<string, unknown>): { url: string; method: string; marked: boolean }[] {
  const asked: { url: string; method: string; marked: boolean }[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url;
      const method = init?.method ?? 'GET';
      asked.push({ url, method, marked: new Headers(init?.headers).get('X-KubeTower-Request') === '1' });
      // The tab probes for jcmd on display; a JDK container answers it.
      const reply = replies[`${method} ${url}`] ?? (method === 'POST' && url.endsWith('/tools') ? { jcmd: true } : undefined);
      if (reply === undefined) return Promise.resolve(new Response(`no stub for ${method} ${url}`, { status: 599 }));
      return Promise.resolve(new Response(JSON.stringify(reply), { status: 200, headers: { 'Content-Type': 'application/json' } }));
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

const STATES: ThreadState[] = ['RUNNABLE', 'WAITING', 'TIMED_WAITING', 'RUNNABLE', 'WAITING', 'TIMED_WAITING', 'RUNNABLE', 'BLOCKED'];

/** A dump of n threads, a quarter of them RUNNABLE, an eighth BLOCKED. */
function big(n: number): ThreadDump {
  const threads: JavaThread[] = [];
  for (let i = 0; i < n; i += 1) {
    const state = STATES[i % STATES.length] ?? 'RUNNABLE';
    threads.push({
      name: `http-nio-8080-exec-${String(i)}`,
      id: i + 20,
      daemon: true,
      prio: 5,
      state,
      frames: Array.from({ length: 20 }, (_, f) => ({ at: `com.example.Service.call${String(f)}(Service.java:${String(f + 1)})` })),
      ...(state === 'BLOCKED'
        ? { waitingFor: { kind: 'waiting to lock', id: `0x${String(i - 1)}`, heldBy: `http-nio-8080-exec-${String(i - 1)}` } }
        : {}),
    });
  }
  const counts = (['RUNNABLE', 'BLOCKED', 'WAITING', 'TIMED_WAITING', 'NEW', 'TERMINATED', 'VM'] as ThreadState[]).map((s) => ({
    state: s,
    count: threads.filter((t) => t.state === s).length,
  }));
  return { ...deadlocked, total: n, daemon: n, counts, threads, deadlocks: [] };
}

describe('the thread dump', () => {
  it('runs nothing until its own button, says in its title that the JVM pauses, and posts with the request header', async () => {
    const asked = stub({ [`GET ${POD}`]: pod, [`POST ${THREADS}`]: deadlocked });
    mount(<JVMTab cluster="kind" kind="pods" namespace="shop" name="billing-0" />);
    const button = await screen.findByRole('button', { name: 'Thread dump' });
    const said = button.getAttribute('title') ?? '';
    expect(said).toContain('Runs jcmd Thread.print -l in container app (pods/exec, recorded in the audit log)');
    expect(said).toContain('The JVM pauses while it writes the dump');
    // No sentence is drawn under it.
    expect(document.querySelector('[data-jvm-threads]')?.textContent).not.toMatch(/pauses|audit log/);
    // Nothing but the reads and the jcmd probe, a POST since #82.
    expect(asked.filter((a) => a.method !== 'GET' && !a.url.endsWith('/tools'))).toEqual([]);

    fireEvent.click(screen.getByRole('button', { name: 'Thread dump' }));
    await screen.findByText('Deadlock · 2 threads');
    const post = asked.filter((a) => a.method === 'POST' && !a.url.endsWith('/tools'));
    expect(post.map((p) => p.url)).toEqual([THREADS]);
    expect(post[0]?.marked).toBe(true);
    // Nothing is polled.
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(asked.filter((a) => a.method === 'POST' && a.url === THREADS)).toHaveLength(1);
    expect(asked.filter((a) => a.method === 'POST' && a.url.endsWith('/tools'))).toHaveLength(1);
  });

  it('shows the deadlock first, with its cycle, and the blocked threads open with who holds their lock', () => {
    render(<DumpView dump={deadlocked} />);
    const view = document.querySelector('[data-jvm-dump]');
    const box = document.querySelector('[data-jvm-deadlock]');
    if (view === null || box === null) throw new Error('no deadlock drawn');
    // The deadlock comes before the ring and the threads.
    expect(view.firstElementChild).toBe(box);
    expect(box.querySelector('[data-jvm-deadlock-link="worker-a"]')?.textContent).toMatch(/worker-a.*waits to lock monitor.*held by.*worker-b/);
    expect(box.querySelector('[data-jvm-deadlock-link="worker-b"]')?.textContent).toMatch(/held by.*worker-a/);
    // BLOCKED is open; the others are folded and draw no thread.
    const blocked = document.querySelector('[data-jvm-group="BLOCKED"]');
    expect(blocked?.querySelectorAll('[data-jvm-thread]')).toHaveLength(2);
    expect(within(blocked as HTMLElement).getAllByText(/held by/)).toHaveLength(2);
    expect(document.querySelector('[data-jvm-group="TIMED_WAITING"] [data-jvm-thread]')).toBeNull();
    // A stack is drawn on a click, with its locks.
    expect(document.querySelector('[data-jvm-stack]')).toBeNull();
    const row = document.querySelector('[data-jvm-thread="worker-a"] > button');
    if (row === null) throw new Error('no row');
    fireEvent.click(row);
    expect(document.querySelector('[data-jvm-stack]')?.textContent).toContain('- waiting to lock 0x50 (a java.lang.Object)');
    // The ring has an arc per state present, as long as its share.
    expect(Array.from(document.querySelectorAll('[data-arc]')).map((a) => a.getAttribute('data-arc'))).toEqual(['BLOCKED', 'TIMED_WAITING']);
  });

  it('filters by name and by frame, and by state from the ring’s legend', () => {
    render(<DumpView dump={deadlocked} />);
    const filter = screen.getByRole('searchbox', { name: 'Filter the threads by name or frame' });
    fireEvent.change(filter, { target: { value: 'D.MAIN(' } });
    expect(Array.from(document.querySelectorAll('[data-jvm-thread]')).map((t) => t.getAttribute('data-jvm-thread'))).toEqual(['main']);
    fireEvent.change(filter, { target: { value: 'Thread.java:1583' } });
    expect(document.querySelectorAll('[data-jvm-thread]')).toHaveLength(2);
    fireEvent.change(filter, { target: { value: 'nothing-like-this' } });
    expect(document.querySelector('[data-jvm-no-thread]')).not.toBeNull();
    fireEvent.change(filter, { target: { value: '' } });
    const timed = document.querySelector('[data-jvm-state="TIMED_WAITING"]');
    if (timed === null) throw new Error('no legend item');
    fireEvent.click(timed);
    expect(document.querySelector('[data-jvm-group="BLOCKED"]')).toBeNull();
    expect(Array.from(document.querySelectorAll('[data-jvm-thread]')).map((t) => t.getAttribute('data-jvm-thread'))).toEqual(['main']);
  });

  it('downloads the dump as the JVM wrote it, and says in its title it may hold thread names', () => {
    const created: Blob[] = [];
    vi.stubGlobal('URL', Object.assign(URL, {
      createObjectURL: vi.fn((b: Blob) => {
        created.push(b);
        return 'blob:x';
      }),
      revokeObjectURL: vi.fn(),
    }));
    const clicked = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined);
    render(<DumpView dump={deadlocked} />);
    expect(screen.getByRole('button', { name: 'Download' }).getAttribute('title')).toContain('may hold the application’s thread names');
    expect(document.body.textContent).not.toContain('may hold');
    fireEvent.click(screen.getByRole('button', { name: 'Download' }));
    expect(created).toHaveLength(1);
    expect(clicked).toHaveBeenCalledTimes(1);
    clicked.mockRestore();
    expect(created[0]?.size).toBe(new Blob([deadlocked.raw ?? '']).size);
    expect(dumpFileName(deadlocked)).toBe('threads-billing-0-app-20261111110415.txt');
    const untimed: ThreadDump = { ...deadlocked, pod: 'a/b c' };
    delete untimed.taken;
    expect(dumpFileName(untimed)).toBe('threads-a_b_c-app.txt');
  });

  it('draws a 2,000-thread dump without drawing every thread, and opens a group within a time ceiling', () => {
    const dump = big(2000);
    const start = performance.now();
    render(<DumpView dump={dump} />);
    const drawn = performance.now() - start;
    // BLOCKED is open, at most one step of it; the other groups draw nothing.
    expect(document.querySelectorAll('[data-jvm-thread]')).toHaveLength(Math.min(250, THREAD_STEP));
    expect(document.querySelectorAll('[data-jvm-stack]')).toHaveLength(0);
    withinCeiling(drawn, 3000);

    const runnable = document.querySelector('[data-jvm-group="RUNNABLE"] > button');
    if (runnable === null) throw new Error('no group');
    const opened = performance.now();
    fireEvent.click(runnable);
    const took = performance.now() - opened;
    expect(document.querySelectorAll('[data-jvm-group="RUNNABLE"] [data-jvm-thread]')).toHaveLength(THREAD_STEP);
    withinCeiling(took, 1500);
    fireEvent.click(within(document.querySelector('[data-jvm-group="RUNNABLE"]') as HTMLElement).getByRole('button', { name: /Show 200 more of 550/ }));
    expect(document.querySelectorAll('[data-jvm-group="RUNNABLE"] [data-jvm-thread]')).toHaveLength(2 * THREAD_STEP);

    // A filter over every thread's frames answers quickly too.
    const filtering = performance.now();
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'exec-1999' } });
    withinCeiling(performance.now() - filtering, 1500);
    expect(Array.from(document.querySelectorAll('[data-jvm-thread]')).map((t) => t.getAttribute('data-jvm-thread'))).toEqual(['http-nio-8080-exec-1999']);
    // The bounds above are the check; building 2,000 threads of fixture on a
    // loaded runner can take longer than the default five seconds.
  }, 20_000);

  it('says what was cut', () => {
    render(<DumpView dump={{ ...deadlocked, outputCut: true, notes: ["jcmd's output reached the exec's limit of 4.0 MiB and was cut."] }} />);
    expect(document.querySelector('[data-jvm-dump-note]')?.textContent).toContain('4.0 MiB');
  });
});
