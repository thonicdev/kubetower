import { useMemo, useState, type ReactNode } from 'react';
import { Failure, Pending, btnSecondary } from '@kubetower/ui';

import { InspectProblem, NO_JDK_TITLE, blocksJcmd, threadDump, type Deadlock, type JavaThread, type ThreadDump as Dump, type ThreadState } from './data';

/**
 * The thread dump of the JVM tab: `jcmd <pid> Thread.print -l` in the
 * container, on a click of its own button, never polled. The route parses the
 * dump (`extras/runtimeinspect/threads.go`); this draws it: the deadlocks the JVM found
 * first, a ring of the threads' states, and the threads grouped by state with
 * their stacks folded.
 *
 * **A dump of thousands of threads must not freeze the page.** A group draws
 * nothing of its threads until it is opened, an open group draws them
 * `THREAD_STEP` at a time, and a thread's stack is drawn only once it is
 * opened. The filter is matched against text folded once per dump.
 */

/** How many threads an open group draws at once; "Show more" adds as many. */
export const THREAD_STEP = 200;

/** The order the groups are drawn in: what needs a look first. */
const GROUP_ORDER: ThreadState[] = ['BLOCKED', 'RUNNABLE', 'WAITING', 'TIMED_WAITING', 'NEW', 'TERMINATED', 'VM'];

const STATE_LABEL: Record<ThreadState, string> = {
  RUNNABLE: 'Runnable',
  BLOCKED: 'Blocked',
  WAITING: 'Waiting',
  TIMED_WAITING: 'Timed waiting',
  NEW: 'New',
  TERMINATED: 'Terminated',
  VM: 'JVM’s own',
};

/** The ring's color of each state: stroke for the arc, fill for the legend. */
const STATE_STROKE: Record<ThreadState, string> = {
  RUNNABLE: 'stroke-plate-green',
  BLOCKED: 'stroke-bad',
  WAITING: 'stroke-plate-blue',
  TIMED_WAITING: 'stroke-plate-teal',
  NEW: 'stroke-plate-violet',
  TERMINATED: 'stroke-plate-pink',
  VM: 'stroke-muted',
};

const STATE_FILL: Record<ThreadState, string> = {
  RUNNABLE: 'bg-plate-green',
  BLOCKED: 'bg-bad',
  WAITING: 'bg-plate-blue',
  TIMED_WAITING: 'bg-plate-teal',
  NEW: 'bg-plate-violet',
  TERMINATED: 'bg-plate-pink',
  VM: 'bg-muted',
};

type Run =
  | { status: 'idle' }
  | { status: 'running' }
  | { status: 'done'; answer: Dump }
  | { status: 'failed'; error: unknown };

/** The thread dump's button, its sentence, and what it answered. */
export function ThreadDumpSection({
  cluster,
  namespace,
  name,
  container,
  failureTitle,
  noJdk,
  offTitle = NO_JDK_TITLE,
  onNoJcmd,
}: {
  cluster: string;
  namespace: string;
  name: string;
  container: string;
  failureTitle: Record<string, string>;
  noJdk: boolean;
  offTitle?: string;
  onNoJcmd: (error?: unknown) => void;
}): ReactNode {
  const [run, setRun] = useState<Run>({ status: 'idle' });
  const go = async (pid?: number) => {
    setRun({ status: 'running' });
    try {
      setRun({ status: 'done', answer: await threadDump(cluster, namespace, name, container, pid) });
    } catch (error) {
      if (blocksJcmd(error)) onNoJcmd(error);
      setRun({ status: 'failed', error });
    }
  };
  return (
    <section data-jvm-threads="" className="flex flex-col gap-3 border-t border-line pt-3">
      <div className="flex flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="text-xs font-semibold">Threads</h3>
          <button
            type="button"
            data-jvm-thread-dump=""
            className={btnSecondary}
            disabled={run.status === 'running' || container === '' || noJdk}
            title={
              noJdk
                ? offTitle
                : `Runs jcmd Thread.print -l in container ${container} (pods/exec, recorded in the audit log). The JVM pauses while it writes the dump, longer the more threads it has.`
            }
            onClick={() => void go()}
          >
            {run.status === 'running' ? 'Dumping…' : run.status === 'idle' ? 'Thread dump' : 'Dump again'}
          </button>
        </div>
      </div>
      {run.status === 'running' ? <Pending what="the thread dump" /> : null}
      {run.status === 'failed' ? (
        run.error instanceof InspectProblem ? (
          <div data-jvm-failure={run.error.code} className="flex flex-col gap-1">
            <p className="font-medium text-warn" title={run.error.message}>
              {failureTitle[run.error.code] ?? 'The thread dump failed'}
            </p>
          </div>
        ) : (
          <Failure error={run.error} onRetry={() => void go()} />
        )
      ) : null}
      {run.status === 'done' && run.answer.state === 'choose' ? (
        <div data-jvm-choose="" className="flex flex-col gap-2">
          <p>jcmd lists several JVMs in this container. Which one?</p>
          <ul className="flex flex-col gap-1">
            {(run.answer.jvms ?? []).map((j) => (
              <li key={j.pid}>
                <button type="button" className={btnSecondary} onClick={() => void go(j.pid)}>
                  {j.mainClass} <span className="text-muted">(pid {j.pid})</span>
                </button>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {run.status === 'done' && run.answer.state === 'dumped' ? <DumpView dump={run.answer} /> : null}
    </section>
  );
}

/** One thread with its text folded for the filter, once per dump. */
interface Indexed {
  thread: JavaThread;
  key: string;
  text: string;
}

/** A dump as the tab draws it. Exported for its test and the picture. */
export function DumpView({ dump }: { dump: Dump }): ReactNode {
  const indexed = useMemo<Indexed[]>(
    () =>
      (dump.threads ?? []).map((t, i) => ({
        thread: t,
        key: `${String(i)}:${t.name}`,
        text: [t.name, t.status ?? '', ...t.frames.map((f) => f.at)].join('\n').toLowerCase(),
      })),
    [dump.threads],
  );
  const [query, setQuery] = useState('');
  const [only, setOnly] = useState<ThreadState | null>(null);
  const q = query.trim().toLowerCase();
  const shown = useMemo(
    () => indexed.filter((x) => (only === null || x.thread.state === only) && (q === '' || x.text.includes(q))),
    [indexed, q, only],
  );
  const groups = useMemo(() => {
    const by = new Map<ThreadState, Indexed[]>();
    for (const x of shown) {
      const list = by.get(x.thread.state);
      if (list === undefined) by.set(x.thread.state, [x]);
      else list.push(x);
    }
    return GROUP_ORDER.flatMap((s) => {
      const list = by.get(s);
      return list === undefined ? [] : [{ state: s, threads: list }];
    });
  }, [shown]);
  const deadlocks = dump.deadlocks ?? [];
  const blocked = (dump.counts ?? []).find((c) => c.state === 'BLOCKED')?.count ?? 0;

  return (
    <div data-jvm-dump="" className="flex flex-col gap-3">
      {dump.notes.map((n) => (
        <p key={n} data-jvm-dump-note="" className="truncate text-xs text-warn" title={n}>
          {n}
        </p>
      ))}
      {deadlocks.map((d, i) => (
        <DeadlockBox key={String(i)} deadlock={d} />
      ))}
      <div className="flex flex-wrap items-start gap-6">
        <StateRing dump={dump} only={only} onPick={setOnly} />
        <div className="flex min-w-64 flex-1 flex-col gap-2 text-xs">
          <p className="tabular-nums">
            <span className="text-sm font-semibold">{dump.total ?? 0}</span> threads
            {dump.daemon === undefined ? null : <span className="text-muted"> · {dump.daemon} daemon</span>}
            {deadlocks.length === 0 ? (
              <span className="text-muted"> · no deadlock</span>
            ) : (
              <span className="font-semibold text-bad">
                {' '}
                · {deadlocks.length} deadlock{deadlocks.length === 1 ? '' : 's'}
              </span>
            )}
            {blocked > 0 ? <span className="text-bad"> · {blocked} blocked</span> : null}
          </p>
          <p className="text-muted">
            {dump.vm ?? ''}
            {dump.taken === undefined ? null : <> · taken {dump.taken}</>}
            {dump.pid === undefined ? null : <> · pid {dump.pid}</>}
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <input
              type="search"
              data-jvm-thread-filter=""
              aria-label="Filter the threads by name or frame"
              placeholder="Filter by name or frame…"
              className="w-64 rounded border border-line bg-panel px-2 py-1 text-xs text-fg"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
            {only === null ? null : (
              <button type="button" className="text-2xs text-accent hover:underline" onClick={() => setOnly(null)}>
                {STATE_LABEL[only]} only · show all
              </button>
            )}
            <Download dump={dump} />
          </div>
        </div>
      </div>
      {groups.length === 0 ? (
        <p data-jvm-no-thread="" className="text-xs text-muted">
          No match
        </p>
      ) : (
        <div className="flex flex-col gap-1">
          {groups.map((g) => (
            <Group
              key={`${g.state}:${q}:${only ?? ''}`}
              state={g.state}
              threads={g.threads}
              open={g.state === 'BLOCKED' || q !== '' || only !== null}
            />
          ))}
        </div>
      )}
    </div>
  );
}

/** A deadlock the JVM found: the cycle, each thread waiting for the next. */
function DeadlockBox({ deadlock }: { deadlock: Deadlock }): ReactNode {
  return (
    <section data-jvm-deadlock="" title={`${deadlock.kind} deadlock reported by the JVM`} className="flex flex-col gap-2 rounded-md border border-bad bg-bad/10 px-3 py-2">
      <h4 className="text-sm font-semibold text-bad">
        Deadlock · {deadlock.cycle.length} threads
      </h4>
      <ol className="flex flex-col gap-1 text-xs">
        {deadlock.cycle.map((l) => (
          <li key={l.thread} data-jvm-deadlock-link={l.thread} className="flex flex-wrap items-baseline gap-x-1">
            <code className="font-semibold">{l.thread}</code>
            <span className="text-muted">waits {l.waits}</span>
            <span aria-hidden="true">→</span>
            <span className="text-muted">held by</span>
            <code className="font-semibold">{l.heldBy}</code>
          </li>
        ))}
      </ol>
      {deadlock.cycleCut === true ? <p className="text-2xs text-muted">cycle cut</p> : null}
    </section>
  );
}

/**
 * The threads by state as a ring: one arc per state, in the order of the
 * counts, each as long as its share of the threads. A legend item filters the
 * list to its state.
 */
function StateRing({
  dump,
  only,
  onPick,
}: {
  dump: Dump;
  only: ThreadState | null;
  onPick: (s: ThreadState | null) => void;
}): ReactNode {
  const counts = (dump.counts ?? []).filter((c) => c.count > 0);
  const total = counts.reduce((n, c) => n + c.count, 0);
  const arcs = spans(counts.map((c) => (total === 0 ? 0 : (c.count / total) * 100))).map((s, i) => ({
    state: counts[i]?.state ?? 'VM',
    start: s.start,
    len: s.width,
  }));
  return (
    <figure data-jvm-state-ring="" className="flex items-center gap-4">
      <div className="relative h-32 w-32 shrink-0">
        <svg
          viewBox="0 0 120 120"
          className="absolute inset-0 h-full w-full -rotate-90"
          role="img"
          aria-label={counts.map((c) => `${STATE_LABEL[c.state]}: ${String(c.count)}`).join(', ')}
        >
          <circle cx="60" cy="60" r="48" fill="none" strokeWidth="14" className="stroke-white/10" />
          {arcs.map((a) => (
            <circle
              key={a.state}
              data-arc={a.state}
              cx="60"
              cy="60"
              r="48"
              fill="none"
              strokeWidth="14"
              pathLength={100}
              strokeDasharray={`${String(Math.max(0, a.len - (arcs.length > 1 ? 0.6 : 0)))} 100`}
              strokeDashoffset={String(-a.start)}
              className={`${STATE_STROKE[a.state]} ${only !== null && only !== a.state ? 'opacity-25' : ''}`}
            />
          ))}
        </svg>
        <div className="absolute inset-0 flex flex-col items-center justify-center text-center leading-tight">
          <span className="text-lg font-semibold tabular-nums">{dump.total ?? 0}</span>
          <span className="text-2xs text-muted">threads</span>
        </div>
      </div>
      <ul className="flex flex-col gap-0.5 text-xs tabular-nums">
        {counts.map((c) => (
          <li key={c.state}>
            <button
              type="button"
              data-jvm-state={c.state}
              aria-pressed={only === c.state}
              className={`flex items-center gap-1.5 rounded px-1 hover:bg-white/5 ${only === c.state ? 'bg-white/10' : ''}`}
              onClick={() => onPick(only === c.state ? null : c.state)}
            >
              <span className={`inline-block h-2 w-2 rounded-sm ${STATE_FILL[c.state]}`} />
              <span className={c.state === 'BLOCKED' ? 'text-bad' : ''}>{STATE_LABEL[c.state]}</span>
              <span className="text-muted">{c.count}</span>
            </button>
          </li>
        ))}
      </ul>
    </figure>
  );
}

/** One state's threads, folded: nothing of them is drawn until opened. */
function Group({ state, threads, open: initially }: { state: ThreadState; threads: Indexed[]; open: boolean }): ReactNode {
  const [open, setOpen] = useState(initially);
  const [limit, setLimit] = useState(THREAD_STEP);
  return (
    <section data-jvm-group={state} className="rounded border border-line">
      <button
        type="button"
        aria-expanded={open}
        className="flex w-full items-center gap-2 px-2 py-1 text-left text-xs hover:bg-white/5"
        onClick={() => setOpen(!open)}
      >
        <span aria-hidden="true" className="w-3 text-muted">
          {open ? '▾' : '▸'}
        </span>
        <span className={`inline-block h-2 w-2 rounded-sm ${STATE_FILL[state]}`} />
        <span className={`font-medium ${state === 'BLOCKED' ? 'text-bad' : ''}`}>{STATE_LABEL[state]}</span>
        <span className="text-muted tabular-nums">{threads.length}</span>
      </button>
      {open ? (
        <ul className="flex flex-col border-t border-line">
          {threads.slice(0, limit).map((x) => (
            <ThreadRow key={x.key} thread={x.thread} />
          ))}
          {threads.length > limit ? (
            <li className="px-2 py-1">
              <button
                type="button"
                data-jvm-more=""
                className="text-2xs text-accent hover:underline"
                onClick={() => setLimit(limit + THREAD_STEP)}
              >
                Show {Math.min(THREAD_STEP, threads.length - limit)} more of {threads.length - limit}
              </button>
            </li>
          ) : null}
        </ul>
      ) : null}
    </section>
  );
}

/** One thread: its name, what it waits for, its top frame; its stack on a click. */
function ThreadRow({ thread: t }: { thread: JavaThread }): ReactNode {
  const [open, setOpen] = useState(false);
  const top = t.frames[0]?.at;
  return (
    <li data-jvm-thread={t.name} className="border-b border-line/50 px-2 py-1 text-xs last:border-b-0">
      <button
        type="button"
        aria-expanded={open}
        disabled={t.frames.length === 0}
        className="flex w-full min-w-0 flex-wrap items-baseline gap-x-2 text-left"
        onClick={() => setOpen(!open)}
      >
        <span aria-hidden="true" className="w-3 text-muted">
          {t.frames.length === 0 ? '' : open ? '▾' : '▸'}
        </span>
        <span className="font-mono font-medium break-all">{t.name}</span>
        {t.daemon ? <span className="text-2xs text-muted">daemon</span> : null}
        {t.detail === undefined ? null : <span className="text-2xs text-muted">{t.detail}</span>}
        {t.cpu === undefined ? null : <span className="text-2xs text-muted tabular-nums">cpu {t.cpu}</span>}
        {top === undefined ? (
          t.status === undefined ? null : <span className="text-2xs text-muted">{t.status}</span>
        ) : (
          <span className="min-w-0 truncate font-mono text-2xs text-muted">at {top}</span>
        )}
      </button>
      {t.waitingFor === undefined ? null : (
        <p className={`ml-5 text-2xs ${t.state === 'BLOCKED' ? 'text-bad' : 'text-muted'}`}>
          {t.waitingFor.kind} <code>{t.waitingFor.id}</code>
          {t.waitingFor.class === undefined ? null : <> ({t.waitingFor.class})</>}
          {t.waitingFor.heldBy === undefined ? null : (
            <>
              {' '}
              held by <code className="font-semibold">{t.waitingFor.heldBy}</code>
            </>
          )}
        </p>
      )}
      {open ? (
        <ol data-jvm-stack="" className="mt-1 ml-5 flex flex-col font-mono text-2xs">
          {t.frames.map((f, i) => (
            <li key={String(i)} className="break-all">
              <span className="text-muted">at </span>
              {f.at}
              {(f.locks ?? []).map((l, j) => (
                <span key={String(j)} className={`block pl-4 ${l.kind === 'waiting to lock' ? 'text-bad' : 'text-muted'}`}>
                  - {l.kind} {l.id}
                  {l.class === undefined ? '' : ` (a ${l.class})`}
                </span>
              ))}
            </li>
          ))}
          {t.framesCut === undefined ? null : (
            <li className="text-muted">… {t.framesCut} more frames</li>
          )}
          {(t.holds ?? []).filter((h) => h.kind === 'owns').length === 0 ? null : (
            <li className="mt-1 text-muted">
              Owns{' '}
              {(t.holds ?? [])
                .filter((h) => h.kind === 'owns')
                .map((h) => `${h.id}${h.class === undefined ? '' : ` (${h.class})`}`)
                .join(', ')}
            </li>
          )}
        </ol>
      ) : null}
    </li>
  );
}

/** The dump as a text file, as the JVM wrote it. */
function Download({ dump }: { dump: Dump }): ReactNode {
  const [failed, setFailed] = useState(false);
  if (dump.raw === undefined || dump.raw === '') return null;
  const save = () => {
    if (typeof URL.createObjectURL !== 'function') {
      setFailed(true);
      return;
    }
    const url = URL.createObjectURL(new Blob([dump.raw ?? ''], { type: 'text/plain' }));
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = dumpFileName(dump);
    anchor.rel = 'noopener';
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    // Revoked a minute later: revoking at once can cancel the download.
    setTimeout(() => URL.revokeObjectURL(url), 60_000);
  };
  return (
    <>
      <button
        type="button"
        data-jvm-thread-download=""
        className={btnSecondary}
        title="The dump as the JVM wrote it; it may hold the application’s thread names"
        onClick={save}
      >
        Download
      </button>
      {failed ? <span className="text-2xs text-bad">cannot save</span> : null}
    </>
  );
}

/** `threads-<pod>-<container>-<taken>.txt`, with nothing a file name refuses. */
export function dumpFileName(dump: Dump): string {
  const taken = (dump.taken ?? '').replace(/[^0-9]/g, '');
  const parts = ['threads', dump.pod, dump.container, taken].filter((p) => p !== '');
  return `${parts.join('-').replace(/[^A-Za-z0-9._-]/g, '_')}.txt`;
}

/** Lays widths end to end: each one's start is the sum of those before it. */
export function spans(widths: number[]): { start: number; width: number }[] {
  return widths.reduce<{ start: number; width: number }[]>((out, width) => {
    const last = out[out.length - 1];
    return [...out, { start: last === undefined ? 0 : last.start + last.width, width }];
  }, []);
}
