import { useState, type ReactNode } from 'react';
import { Failure, Pending, Pill, btnPrimary, btnSecondary } from '@kubetower/ui';

import { InspectProblem, NO_JDK_TITLE, blocksJcmd, classHistogram, type ClassHistogram, type ClassRow } from './data';

/**
 * The class histogram of the JVM tab: `jcmd <pid> GC.class_histogram` in the
 * container, which **runs a full GC** and pauses the application. So the
 * button only asks: a confirmation row names the cost, and nothing is sent
 * until Run. Never polled, never run when the tab opens.
 *
 * The route parses the output and keeps the top classes by bytes
 * (`extras/jvm/histogram.go`); this draws them as bars whose length is the
 * share of the largest row, in the server's own figures.
 */

type Run =
  | { status: 'idle' }
  | { status: 'confirm' }
  | { status: 'running' }
  | { status: 'done'; answer: ClassHistogram }
  | { status: 'failed'; error: unknown };

export function HistogramSection({
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
      setRun({ status: 'done', answer: await classHistogram(cluster, namespace, name, container, pid) });
    } catch (error) {
      if (blocksJcmd(error)) onNoJcmd(error);
      setRun({ status: 'failed', error });
    }
  };
  const busy = run.status === 'running';
  return (
    <section data-jvm-histogram="" className="flex flex-col gap-3 border-t border-line pt-3">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-xs font-semibold">Classes</h3>
        <button
          type="button"
          data-jvm-histogram-start=""
          className={btnSecondary}
          disabled={busy || container === '' || noJdk}
          title={noJdk ? offTitle : `Runs jcmd GC.class_histogram in container ${container} (pods/exec, recorded in the audit log)`}
          onClick={() => setRun({ status: 'confirm' })}
        >
          {run.status === 'idle' || run.status === 'confirm' ? 'Class histogram' : busy ? 'Counting…' : 'Count again'}
        </button>
        {run.status === 'confirm' ? (
          <span data-jvm-histogram-confirm="" className="flex flex-wrap items-center gap-2">
            <Pill tone="warn" title="The JVM runs a full garbage collection before it counts: the application pauses, longer the larger the heap">
              Full GC · pauses the application
            </Pill>
            <button type="button" data-jvm-histogram-run="" className={btnPrimary} onClick={() => void go()}>
              Run
            </button>
            <button type="button" className={btnSecondary} onClick={() => setRun({ status: 'idle' })}>
              Cancel
            </button>
          </span>
        ) : null}
      </div>
      {busy ? <Pending what="the class histogram" /> : null}
      {run.status === 'failed' ? (
        run.error instanceof InspectProblem ? (
          <div data-jvm-failure={run.error.code} className="flex flex-col gap-1">
            <p className="font-medium text-warn" title={run.error.message}>
              {failureTitle[run.error.code] ?? 'The class histogram failed'}
            </p>
          </div>
        ) : (
          <Failure error={run.error} onRetry={() => setRun({ status: 'confirm' })} />
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
      {run.status === 'done' && run.answer.state === 'histogram' ? <HistogramView histogram={run.answer} /> : null}
    </section>
  );
}

/** The JDK's own classes and the primitive arrays, against the application's. */
const JDK_CLASS = /^(?:java|javax|jdk|sun|com\.sun)\.|^(?:byte|char|short|int|long|float|double|boolean)(?:\[\])+$/;

const isJDK = (r: ClassRow): boolean => JDK_CLASS.test(r.name);

const count = (n: number) => n.toLocaleString('en-US');

/** A histogram as the tab draws it. Exported for its test and the picture. */
export function HistogramView({ histogram }: { histogram: ClassHistogram }): ReactNode {
  const rows = histogram.rows ?? [];
  const biggest = rows.reduce((m, r) => Math.max(m, r.share), 0);
  return (
    <div data-jvm-histogram-result="" className="flex flex-col gap-3">
      <ul className="flex flex-wrap items-center gap-x-4 gap-y-1 text-2xs tabular-nums">
        <li>
          <span className="text-muted">Classes</span> {count(histogram.classes ?? 0)}
        </li>
        <li>
          <span className="text-muted">Instances</span> {count(histogram.instances ?? 0)}
        </li>
        <li>
          <span className="text-muted">Live bytes</span> {formatBytes(histogram.bytes ?? 0)}
        </li>
        {histogram.cut === true ? (
          <li>
            <Pill neutral="muted" title={`The ${String(rows.length)} largest of ${String(histogram.classes ?? 0)} classes`}>
              top {rows.length}
            </Pill>
          </li>
        ) : null}
        {histogram.outputCut === true ? (
          <li>
            <Pill tone="warn" title="jcmd’s output reached the exec’s limit and was cut: the totals are short">
              output cut
            </Pill>
          </li>
        ) : null}
        <li className="flex items-center gap-1">
          <span className="inline-block h-2 w-2 rounded-sm bg-plate-blue" /> JDK
          <span className="ml-2 inline-block h-2 w-2 rounded-sm bg-plate-green" /> application
        </li>
      </ul>
      <ol data-jvm-class-rows="" className="flex flex-col gap-1 text-xs">
        {rows.map((r) => (
          <li
            key={r.rank}
            data-jvm-class={r.name}
            className="flex items-center gap-2 tabular-nums"
            title={`${r.name}${r.module === undefined || r.module === '' ? '' : ` (${r.module})`}\n${count(r.instances)} instances · ${count(r.bytes)} bytes`}
          >
            <span className="w-8 shrink-0 text-right text-muted">{r.rank}</span>
            <span className="relative flex h-5 min-w-0 flex-1 items-center rounded bg-white/10">
              <span
                className={`absolute inset-y-0 left-0 rounded ${isJDK(r) ? 'bg-plate-blue' : 'bg-plate-green'}`}
                style={{ width: `${String(biggest === 0 ? 0 : Math.max(0.5, (r.share / biggest) * 100))}%` }}
              />
              <span className="relative truncate px-1.5 font-mono">{r.name}</span>
            </span>
            <span className="w-20 shrink-0 text-right">{formatBytes(r.bytes)}</span>
            <span className="w-24 shrink-0 text-right text-muted">{count(r.instances)} ×</span>
          </li>
        ))}
      </ol>
    </div>
  );
}

function formatBytes(n: number): string {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  return `${i === 0 ? String(v) : v.toFixed(1)} ${units[i] ?? 'B'}`;
}
