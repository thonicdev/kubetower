import { useQuery } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { Failure, Pending, Pill, ROW, TABLE, bodyCell, btnPrimary, btnSecondary, type ExtensionTabProps } from '@kubetower/ui';

import {
  InspectProblem,
  inspect,
  blocksJcmd,
  podQuery,
  reveal,
  useJdkTools,
  type Arg,
  type GCWork,
  type Inspection,
  type MemoryBar,
  type PropertyRow,
  type Section,
} from './data';

import { FlightRecordingSection } from './FlightRecording';
import { HeapDumpSection } from './HeapDump';
import { HistogramSection } from './Histogram';
import { HistorySection } from './History';
import { ThreadDumpSection, spans } from './ThreadDump';

/**
 * The JVM tab on a pod: what the JVM says about itself, read with `jcmd` in
 * its container.
 *
 * **Nothing runs when the tab opens.** The tab reads the pod's spec (the same
 * route as the page) to offer its Java containers, and runs jcmd only on a
 * click of Inspect, through the console's exec door: reviewed as `create
 * pods/exec` for the person and recorded with the command. The button's `title`
 * says so before the click; the line beside it names what Inspect gives.
 *
 * The memory bar's geometry is the server's: the tab draws the percentages it
 * is given and never rescales.
 */
export default function JVMTab({ cluster, namespace, name }: ExtensionTabProps): ReactNode {
  if (namespace === undefined || namespace === '') {
    return <p className="text-sm text-muted">A pod is namespaced; this one came without its namespace.</p>;
  }
  return <OnPod cluster={cluster} namespace={namespace} name={name} />;
}

type Run =
  | { status: 'idle' }
  | { status: 'running' }
  | { status: 'done'; answer: Inspection }
  | { status: 'failed'; error: unknown };

function OnPod({ cluster, namespace, name }: { cluster: string; namespace: string; name: string }): ReactNode {
  const pod = useQuery(podQuery(cluster, namespace, name));
  const [picked, setPicked] = useState<string | undefined>(undefined);
  const [run, setRun] = useState<Run>({ status: 'idle' });
  // The container the tab shows, the same as below; empty until the pod is read.
  const shown = pod.data?.containers ?? [];
  const javaNow = shown.filter((c) => c.java);
  const { noJdk, offTitle, attachOff, markNoJdk } = useJdkTools(cluster, namespace, name, picked ?? (javaNow.length > 0 ? javaNow : shown)[0]?.name ?? '');

  if (pod.data === undefined) {
    return pod.isError ? (
      <Failure error={pod.error} onRetry={() => void pod.refetch()} />
    ) : (
      <Pending what="this pod's containers" />
    );
  }
  const java = pod.data.containers.filter((c) => c.java);
  const offered = java.length > 0 ? java : pod.data.containers;
  const container = picked ?? offered[0]?.name ?? '';

  const go = async (pid?: number) => {
    setRun({ status: 'running' });
    try {
      setRun({ status: 'done', answer: await inspect(cluster, namespace, name, container, pid) });
    } catch (error) {
      if (blocksJcmd(error)) markNoJdk(error);
      setRun({ status: 'failed', error });
    }
  };

  return (
    <div data-jvm-tab="" className="flex flex-col gap-4 text-sm">
      <div className="flex flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2">
          {offered.length > 1 ? (
            <label className="flex items-center gap-1 text-xs text-muted">
              Container
              <select
                className="rounded border border-line bg-panel px-1 py-0.5 text-fg"
                value={container}
                onChange={(e) => {
                  setPicked(e.target.value);
                  setRun({ status: 'idle' });
                }}
              >
                {offered.map((c) => (
                  <option key={c.name} value={c.name}>
                    {c.name}
                  </option>
                ))}
              </select>
            </label>
          ) : null}
          <button
            type="button"
            data-jvm-inspect=""
            className={btnPrimary}
            disabled={run.status === 'running' || container === '' || noJdk}
            title={noJdk ? offTitle : `Runs jcmd in container ${container} (pods/exec, recorded in the audit log)`}
            onClick={() => void go()}
          >
            {run.status === 'running' ? 'Inspecting…' : run.status === 'idle' ? 'Inspect' : 'Inspect again'}
          </button>
        </div>
        {attachOff === undefined ? null : (
          <div className="flex">
            <Pill tone="warn" title={attachOff}>
              Attach disabled
            </Pill>
          </div>
        )}
        {java.length === 0 ? (
          <p className="text-xs text-warn" title="Nothing in this pod’s spec says it runs Java; jcmd may find no JVM.">
            Not Java by its spec
          </p>
        ) : null}
      </div>
      {run.status === 'idle' ? (
        <p data-jvm-hint="" className="text-xs text-muted">
          Inspect → memory, GC and threads
        </p>
      ) : null}
      {run.status === 'running' ? <Pending what="the JVM's answers" /> : null}
      {run.status === 'failed' ? <Failed error={run.error} onRetry={() => void go()} /> : null}
      {run.status === 'done' && run.answer.state === 'choose' ? (
        <Choose answer={run.answer} onPick={(pid) => void go(pid)} />
      ) : null}
      {run.status === 'done' && run.answer.state === 'inspected' ? <Result answer={run.answer} /> : null}
      {container === '' ? null : (
        <ThreadDumpSection
          key={container}
          cluster={cluster}
          namespace={namespace}
          name={name}
          container={container}
          failureTitle={FAILURE_TITLE}
          noJdk={noJdk}
          offTitle={offTitle}
          onNoJcmd={markNoJdk}
        />
      )}
      {container === '' ? null : (
        <HeapDumpSection
          key={`heap:${container}`}
          cluster={cluster}
          namespace={namespace}
          name={name}
          container={container}
          failureTitle={FAILURE_TITLE}
          noJdk={noJdk}
          offTitle={offTitle}
          onNoJcmd={markNoJdk}
        />
      )}
      {container === '' ? null : (
        <HistogramSection
          key={`histogram:${container}`}
          cluster={cluster}
          namespace={namespace}
          name={name}
          container={container}
          failureTitle={FAILURE_TITLE}
          noJdk={noJdk}
          offTitle={offTitle}
          onNoJcmd={markNoJdk}
        />
      )}
      {container === '' ? null : (
        <FlightRecordingSection
          key={`jfr:${container}`}
          cluster={cluster}
          namespace={namespace}
          name={name}
          container={container}
          failureTitle={FAILURE_TITLE}
          noJdk={noJdk}
          offTitle={offTitle}
          onNoJcmd={markNoJdk}
        />
      )}
      {container === '' ? null : <HistorySection cluster={cluster} namespace={namespace} name={name} />}
    </div>
  );
}

/** The headings of the failures the route names; the sentence is the route's. */
const FAILURE_TITLE: Record<string, string> = {
  no_jcmd: 'No jcmd: use a JDK image',
  attach_refused: 'The JVM refused the attach',
  attach_disabled: 'Attach disabled',
  no_jvm: 'No JVM found',
  exec_forbidden: 'You may not run commands in this pod',
  read_only: 'Read-only cluster',
  exec_unreviewed: 'Your access could not be checked',
  exec_busy: 'Too many commands running',
  exec_timed_out: 'jcmd timed out',
  not_running: 'The pod is not running',
  unknown_pod: 'Pod no longer exists',
};

function Failed({ error, onRetry }: { error: unknown; onRetry: () => void }): ReactNode {
  if (!(error instanceof InspectProblem)) return <Failure error={error} onRetry={onRetry} />;
  // The badge beside Inspect says it.
  if (error.code === 'attach_disabled') return null;
  return (
    <div data-jvm-failure={error.code} className="flex flex-col gap-1">
      <p className="font-medium text-warn" title={error.message}>
        {FAILURE_TITLE[error.code] ?? 'The inspection failed'}
      </p>
    </div>
  );
}

function Choose({ answer, onPick }: { answer: Inspection; onPick: (pid: number) => void }): ReactNode {
  return (
    <div data-jvm-choose="" className="flex flex-col gap-2">
      <p>jcmd lists several JVMs in this container. Which one?</p>
      <ul className="flex flex-col gap-1">
        {(answer.jvms ?? []).map((j) => (
          <li key={j.pid}>
            <button type="button" className={btnSecondary} onClick={() => onPick(j.pid)}>
              {j.mainClass} <span className="text-muted">(pid {j.pid})</span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

function Result({ answer }: { answer: Inspection }): ReactNode {
  const s = answer.summary;
  const other = (answer.sections ?? []).filter((x) => x.state !== 'ok' || x.truncated === true);
  const max = answer.memory?.heapMax ?? null;
  const limit = answer.memory?.markers.find((m) => m.key === 'limit');
  return (
    <div data-jvm-result="" className="flex flex-col gap-4">
      {answer.notes.map((n) => (
        <p key={n} data-jvm-note="" className="truncate text-xs text-warn" title={n}>
          {n}
        </p>
      ))}
      {s === undefined ? null : (
        <ul data-jvm-cards="" className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <Card label="JVM" value={s.version === '' ? 'unknown' : s.version.replace(/^JDK /, 'JDK ')} hint={s.vm} />
          <Card label="Garbage collector" value={s.gc === '' ? 'not named' : s.gc} />
          <Card label="Uptime" value={s.uptime === '' ? 'unknown' : s.uptime} hint={`pid ${String(answer.pid ?? '')}${answer.pidFrom === 'pid1' ? ' · not listed' : ''}`} />
          <Card
            label="Max heap"
            value={max === null ? 'unknown' : max.text}
            hint={limit === undefined ? 'no memory limit' : `limit ${limit.text}`}
          />
        </ul>
      )}
      {answer.memory === undefined ? null : (
        <div className="flex flex-wrap items-start gap-6">
          {answer.memory.heapGauge === null ? null : <HeapRing bar={answer.memory} />}
          <div className="min-w-64 flex-1">
            <Memory bar={answer.memory} />
          </div>
        </div>
      )}
      {answer.gc === undefined ? null : <GCGauge gc={answer.gc} />}
      {s === undefined || s.flags.length === 0 ? null : (
        <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-xs">
          {s.flags.map((f) => (
            <FlagRow key={f.name} name={f.name} value={f.text ?? f.value} />
          ))}
        </dl>
      )}
      {other.length === 0 ? null : <Sections sections={other} />}
      <Details answer={answer} />
    </div>
  );
}

function Card({ label, value, hint }: { label: string; value: string; hint?: string }): ReactNode {
  return (
    <li className="flex min-w-0 flex-col rounded-md border border-line bg-panel-2 px-3 py-2" title={hint}>
      <span className="text-2xs tracking-eyebrow text-muted uppercase">{label}</span>
      <span className="truncate text-sm font-medium tabular-nums">{value}</span>
      {hint === undefined || hint === '' ? null : <span className="truncate text-2xs text-muted">{hint}</span>}
    </li>
  );
}

/**
 * The heap as a ring: the whole circle is the maximum heap, the pale arc what
 * the JVM committed, the strong arc what it uses. The arcs are the server's
 * percentages (`heapGauge`), drawn as given.
 */
function HeapRing({ bar }: { bar: MemoryBar }): ReactNode {
  const g = bar.heapGauge;
  if (g === null) return null;
  const arc = (pct: number) => `${String(Math.min(100, Math.max(0, pct)))} 100`;
  return (
    <figure data-jvm-heap-ring="" className="flex flex-col items-center gap-1">
      <div className="relative h-36 w-36">
        <svg
          viewBox="0 0 120 120"
          className="absolute inset-0 h-full w-full -rotate-90"
          role="img"
          aria-label={`Heap: ${bar.heapUsed?.text ?? '?'} used, ${bar.heapCommitted?.text ?? '?'} committed, of ${bar.heapMax?.text ?? '?'}`}
        >
          <circle cx="60" cy="60" r="50" fill="none" strokeWidth="10" className="stroke-white/10" />
          <circle cx="60" cy="60" r="50" fill="none" strokeWidth="10" pathLength={100} strokeDasharray={arc(g.committedPct)} className="stroke-plate-blue-pale" />
          <circle cx="60" cy="60" r="50" fill="none" strokeWidth="10" pathLength={100} strokeDasharray={arc(g.usedPct)} className="stroke-plate-blue" />
        </svg>
        <div className="absolute inset-0 flex flex-col items-center justify-center text-center leading-tight">
          <span className="text-sm font-semibold tabular-nums">{bar.heapUsed?.text ?? '?'}</span>
          <span className="text-2xs text-muted">of {bar.heapMax?.text ?? '?'}</span>
        </div>
      </div>
      <figcaption className="text-2xs text-muted">Heap · {bar.heapCommitted?.text ?? '?'} committed</figcaption>
    </figure>
  );
}

/**
 * The garbage collectors' work since the JVM started, from its performance
 * counters: the share of the uptime spent in each collector, on a scale of
 * 10 % (100 % once the share is above it), and how many collections each ran.
 */
function GCGauge({ gc }: { gc: GCWork }): ReactNode {
  const scale = gc.share > 10 ? 100 : 10;
  const laid = spans(gc.collectors.map((c) => (c.share / scale) * 100));
  const parts = gc.collectors.map((c, i) => {
    const start = Math.min(100, laid[i]?.start ?? 0);
    return { c, start, width: Math.min(100 - start, laid[i]?.width ?? 0) };
  });
  const fill = (young: boolean) => (young ? 'bg-plate-green' : 'bg-plate-amber');
  return (
    <section data-jvm-gc="" className="flex flex-col gap-2">
      <h3 className="text-xs font-semibold" title="From PerfCounter.print: pause time as a share of the uptime">
        GC{' '}
        <span className="font-normal text-muted">
          {gc.share} % of uptime · scale {scale} %
        </span>
      </h3>
      <div className="relative h-3 rounded bg-white/10" role="img" aria-label={`${String(gc.share)} % of the uptime in GC pauses`}>
        {parts.map((p) => (
          <div
            key={p.c.name}
            data-gc-part={p.c.young ? 'young' : 'old'}
            className={`absolute inset-y-0 ${fill(p.c.young)}`}
            style={{ left: percent(p.start), width: percent(Math.max(p.width, p.c.seconds > 0 ? 0.5 : 0)) }}
            title={`${p.c.name}: ${String(p.c.share)} %`}
          />
        ))}
      </div>
      <ul className="flex flex-wrap gap-x-4 gap-y-1 text-2xs tabular-nums">
        {gc.collectors.map((c) => (
          <li key={c.name} className="flex items-center gap-1">
            <span className={`inline-block h-2 w-2 rounded-sm ${fill(c.young)}`} />
            <span title={c.name}>{c.young ? 'Young' : 'Other'}</span>
            <span className="text-muted">
              {c.invocations} × · {c.seconds} s
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function FlagRow({ name, value }: { name: string; value: string }): ReactNode {
  return (
    <>
      <dt className="text-muted">{name}</dt>
      <dd className="tabular-nums">{value}</dd>
    </>
  );
}

function Sections({ sections }: { sections: Section[] }): ReactNode {
  return (
    <ul data-jvm-sections="" className="flex flex-col gap-1 text-xs">
      {sections.map((x) => (
        <li key={x.command} data-jvm-section={x.state} title={x.sentence}>
          <code>{x.command}</code>{' '}
          <Pill tone={x.state === 'failed' ? 'bad' : x.state === 'truncated' ? 'warn' : undefined} neutral="muted">
            {x.state === 'not-enabled'
              ? x.command.includes('native_memory')
                ? 'NMT off: add -XX:NativeMemoryTracking=summary to JAVA_TOOL_OPTIONS'
                : 'not enabled'
              : x.state === 'unavailable'
                ? 'not on this JDK'
                : x.state === 'truncated'
                  ? 'cut, not read'
                  : x.state === 'skipped'
                    ? 'not run'
                    : x.state}
          </Pill>
        </li>
      ))}
    </ul>
  );
}

/** The color of each part of the bar, by the key the server gives it. */
const SEGMENT_FILL: Record<string, string> = {
  heap: 'bg-plate-blue-pale',
  metaspace: 'bg-plate-violet',
  code: 'bg-plate-teal',
  threads: 'bg-plate-amber',
  gc: 'bg-plate-green',
  cds: 'bg-plate-lime',
  other: 'bg-plate-pink',
  outside: 'bg-muted opacity-50',
};

const MARKER_FILL: Record<string, string> = {
  request: 'bg-fg',
  limit: 'bg-bad',
  workingSet: 'bg-warn',
  heapMax: 'bg-accent',
};

const percent = (v: number) => `${String(v)}%`;

/**
 * The memory bar: the parts the JVM committed end to end, the heap's used
 * part inside its own, and what the working set holds beyond them, against
 * the request, the limit, the working set and the maximum heap.
 */
function Memory({ bar }: { bar: MemoryBar }): ReactNode {
  return (
    <section data-jvm-memory={bar.source} className="flex flex-col gap-2">
      <h3 className="text-xs font-semibold" title={[bar.workingSetNote ?? '', ...bar.notes].filter((n) => n !== '').join('\n')}>
        Memory{' '}
        <span className="font-normal text-muted">
          {bar.source === 'nmt' ? 'NMT' : 'heap · metaspace · code cache'} · scale{' '}
          {bar.track.text}
        </span>
      </h3>
      <div className="relative h-6 rounded bg-white/10">
        {bar.segments.map((s) => (
          <div
            key={s.key}
            data-segment={s.key}
            className={`absolute inset-y-0 ${SEGMENT_FILL[s.key] ?? 'bg-plate-pink'}`}
            style={{ left: percent(s.start), width: percent(s.width) }}
            title={`${s.label}: ${s.text}`}
          >
            {s.used === undefined ? null : (
              <div className="h-full bg-plate-blue" style={{ width: percent(s.width === 0 ? 0 : (s.used.width / s.width) * 100) }} />
            )}
          </div>
        ))}
        {bar.markers.map((m) => (
          <span
            key={m.key}
            data-marker={m.key}
            className={`absolute -top-1 -ml-px h-8 w-0.5 ${MARKER_FILL[m.key] ?? 'bg-fg'}`}
            style={{ left: percent(m.at) }}
            title={`${m.label}: ${m.text}`}
          />
        ))}
      </div>
      <ul className="flex flex-wrap gap-x-4 gap-y-1 text-2xs tabular-nums">
        {bar.segments.map((s) => (
          <li key={s.key} className="flex items-center gap-1">
            <span className={`inline-block h-2 w-2 rounded-sm ${SEGMENT_FILL[s.key] ?? 'bg-plate-pink'}`} />
            {s.label} {s.text}
            {s.used === undefined ? null : <span className="text-muted"> ({s.used.text} used)</span>}
          </li>
        ))}
      </ul>
      <ul className="flex flex-wrap gap-x-4 gap-y-1 text-2xs tabular-nums">
        {bar.markers.map((m) => (
          <li key={m.key} className="flex items-center gap-1">
            <span className={`inline-block h-2 w-0.5 ${MARKER_FILL[m.key] ?? 'bg-fg'}`} />
            {m.label} {m.text}
          </li>
        ))}
      </ul>
    </section>
  );
}

/** The flags, the command line and the system properties, folded. */
function Details({ answer }: { answer: Inspection }): ReactNode {
  const [shown, setShown] = useState<Record<string, string | { error: unknown } | 'loading'>>({});
  const pid = answer.pid ?? 1;
  const doReveal = async (kind: 'property' | 'option', key: string) => {
    const id = `${kind}:${key}`;
    setShown((s) => ({ ...s, [id]: 'loading' }));
    try {
      const value = await reveal(answer.cluster, answer.namespace, answer.pod, answer.container, pid, kind, key);
      setShown((s) => ({ ...s, [id]: value }));
    } catch (error) {
      setShown((s) => ({ ...s, [id]: { error } }));
    }
  };
  const revealed = (kind: 'property' | 'option', key: string, maskedText: ReactNode): ReactNode => {
    const v = shown[`${kind}:${key}`];
    if (typeof v === 'string' && v !== 'loading') return <span data-revealed={key}>{v}</span>;
    return (
      <>
        {maskedText}{' '}
        <button
          type="button"
          data-reveal={key}
          className="text-2xs text-accent hover:underline"
          disabled={v === 'loading'}
          title="Runs jcmd again in the container to read this value; the run is recorded in the audit log"
          onClick={() => void doReveal(kind, key)}
        >
          {v === 'loading' ? 'reading…' : 'Reveal'}
        </button>
        {typeof v === 'object' ? (
          <span className="text-2xs text-bad"> {v.error instanceof Error ? v.error.message : 'could not be read'}</span>
        ) : null}
      </>
    );
  };
  const args = (list: Arg[]) =>
    list.map((a, i) => (
      <span key={`${String(i)}:${a.text}`} className="mr-1 break-all">
        {a.secret === undefined ? a.text : <>{a.text}{revealed('option', a.secret, '••••••')}</>}
      </span>
    ));
  const property = (p: PropertyRow) => (p.masked ? revealed('property', p.key, p.value) : p.value);

  return (
    <div className="flex flex-col gap-2 text-xs">
      {answer.allFlags === undefined ? null : (
        <details>
          <summary className="cursor-pointer">Flags ({answer.allFlags.length})</summary>
          <ul className="mt-1 font-mono">
            {answer.allFlags.map((f) => (
              <li key={f.name}>
                {f.name} = {f.value}
              </li>
            ))}
          </ul>
        </details>
      )}
      {answer.commandLine === undefined ? null : (
        <details>
          <summary className="cursor-pointer">Command line</summary>
          <p className="mt-1 font-mono">
            <span className="text-muted">JVM options: </span>
            {args(answer.commandLine.jvmArgs)}
          </p>
          <p className="font-mono">
            <span className="text-muted">Command: </span>
            {args(answer.commandLine.javaCommand)}
          </p>
        </details>
      )}
      {answer.properties === undefined ? null : (
        <details>
          <summary className="cursor-pointer">System properties ({answer.properties.length})</summary>
          <table className={`${TABLE} mt-1 font-mono`}>
            <tbody>
              {answer.properties.map((p) => (
                <tr key={p.key} className={ROW}>
                  <td className={`${bodyCell(true)} align-top text-muted`}>{p.key}</td>
                  <td className={`${bodyCell(true)} break-all`}>{property(p)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      )}
      {answer.memory?.nmt === undefined ? null : (
        <details>
          <summary className="cursor-pointer">Native memory tracking ({answer.memory.nmt.length} categories)</summary>
          <table className={`${TABLE} mt-1 tabular-nums`}>
            <tbody>
              {answer.memory.nmt.map((c) => (
                <tr key={c.name} className={ROW}>
                  <td className={bodyCell(true)}>{c.name}</td>
                  <td className={`${bodyCell(true)} text-right`}>{c.committed.text}</td>
                  <td className={`${bodyCell(true)} text-right text-muted`}>{c.reserved.text} reserved</td>
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      )}
    </div>
  );
}
