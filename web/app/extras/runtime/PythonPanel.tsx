import { useQuery } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { Failure, HEAD_ROW, Pending, Pill, ROW, TABLE, bodyCell, btnSecondary, formatInstant, headCell } from '@kubetower/ui';

import { pythonMetricsQuery, pythonProcessesQuery, type PythonMetrics, type PythonProcess } from './data';

/**
 * Python's diagnostics on a pod's Python tab (`python.go`), with nothing
 * installed in the pod and no ptrace: the container's Python processes read
 * out of /proc by a fixed script, and the application's own prometheus_client
 * series, through a tunnel, where it serves them. No signal reaches the
 * process; no stack is read.
 */
export function PythonPanel({ cluster, namespace, name, container }: { cluster: string; namespace: string; name: string; container: string }): ReactNode {
  return (
    <div data-python="" className="flex flex-col gap-4">
      <Processes cluster={cluster} namespace={namespace} name={name} container={container} />
      <Metrics cluster={cluster} namespace={namespace} name={name} container={container} />
    </div>
  );
}

const READER_TITLE: Readonly<Record<string, string>> = {
  sh: 'Read out of /proc by sh',
  python3: "Read out of /proc by the container's python3: it has no sh",
};

function Processes({ cluster, namespace, name, container }: { cluster: string; namespace: string; name: string; container: string }): ReactNode {
  const read = useQuery(pythonProcessesQuery(cluster, namespace, name, container));
  return (
    <section data-python-processes="" className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-sm font-medium">Processes</h3>
        {read.data === undefined ? null : (
          <>
            <Pill neutral="muted" title={READER_TITLE[read.data.reader]}>
              /proc
            </Pill>
            {read.data.others > 0 ? (
              <Pill neutral="muted" title="Processes of this container that are not Python">
                {`+${String(read.data.others)} other`}
              </Pill>
            ) : null}
            {read.data.truncated ? <Pill tone="warn">truncated</Pill> : null}
          </>
        )}
        <button type="button" className={`ml-auto ${btnSecondary}`} onClick={() => void read.refetch()} disabled={read.isFetching}>
          Refresh
        </button>
      </div>
      {read.data === undefined ? (
        read.isError ? (
          <Failure error={read.error} onRetry={() => void read.refetch()} />
        ) : (
          <Pending what="the container's processes" />
        )
      ) : read.data.processes.length === 0 ? (
        <p data-python-none="" className="text-xs text-muted">
          No Python process
        </p>
      ) : (
        <div className="overflow-x-auto">
          <table className={`${TABLE} w-full`}>
            <thead>
              <tr className={HEAD_ROW}>
                <th className={headCell()}>PID</th>
                <th className={headCell()}>Python</th>
                <th className={headCell()}>Program</th>
                <th className={`${headCell()} text-right`}>Memory</th>
                <th className={`${headCell()} text-right`}>Peak</th>
                <th className={`${headCell()} text-right`}>Threads</th>
                <th className={headCell()}>Started</th>
              </tr>
            </thead>
            <tbody>
              {read.data.processes.map((p) => (
                <ProcessRow key={p.pid} p={p} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function ProcessRow({ p }: { p: PythonProcess }): ReactNode {
  return (
    <tr data-python-pid={p.pid} className={ROW}>
      <td className={`${bodyCell(true)} font-mono tabular-nums`}>{p.pid}</td>
      <td className={bodyCell(true)}>{p.version === '' ? <span className="text-muted">—</span> : <Pill neutral="normal">{p.version}</Pill>}</td>
      <td className={`${bodyCell(true)} truncate font-mono`} title={p.executable}>
        {p.comm}
      </td>
      <td className={`${bodyCell(true)} text-right tabular-nums`}>{bytes(p.rss)}</td>
      <td className={`${bodyCell(true)} text-right tabular-nums`}>{bytes(p.peak)}</td>
      <td className={`${bodyCell(true)} text-right tabular-nums`} title={threadTitle(p)}>
        {p.threads}
      </td>
      <td className={`${bodyCell(true)} whitespace-nowrap`}>{p.started === undefined ? <span className="text-muted">—</span> : formatInstant(p.started)}</td>
    </tr>
  );
}

/** The kernel's thread names, grouped; equal to the process's own name until Python 3.14 names them. */
function threadTitle(p: PythonProcess): string | undefined {
  if (p.threadNames.length === 0) return undefined;
  const counts = new Map<string, number>();
  for (const n of p.threadNames) counts.set(n, (counts.get(n) ?? 0) + 1);
  return [...counts].map(([n, c]) => (c > 1 ? `${n} ×${String(c)}` : n)).join('\n');
}

const SOURCE_TITLE: Readonly<Record<string, string>> = {
  annotation: 'From kubetower.io/metrics-port',
  port: 'From the container port named metrics',
  default: "prometheus_client's start_http_server default; kubetower.io/metrics-port or a port named metrics sets another",
};

function Metrics({ cluster, namespace, name, container }: { cluster: string; namespace: string; name: string; container: string }): ReactNode {
  const state = useQuery(pythonMetricsQuery(cluster, namespace, name, container));
  return (
    <section data-python-metrics="" className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-sm font-medium">prometheus_client</h3>
        {state.data === undefined ? null : (
          <>
            <Pill neutral="muted" title={SOURCE_TITLE[state.data.source]}>
              {`/metrics :${String(state.data.port)}`}
            </Pill>
            {state.data.reachable ? (
              <Pill tone="ok">reachable</Pill>
            ) : (
              <span data-python-metrics-unreachable="">
                <Pill tone="warn" title={state.data.detail}>
                  not served
                </Pill>
              </span>
            )}
          </>
        )}
        <button type="button" className={`ml-auto ${btnSecondary}`} onClick={() => void state.refetch()} disabled={state.isFetching}>
          Check again
        </button>
      </div>
      {state.data === undefined ? (
        state.isError ? (
          <Failure error={state.error} onRetry={() => void state.refetch()} />
        ) : (
          <Pending what="/metrics" />
        )
      ) : state.data.metrics === null ? null : (
        <MetricsView m={state.data.metrics} />
      )}
    </section>
  );
}

function MetricsView({ m }: { m: PythonMetrics }): ReactNode {
  const facts: { label: string; value: string; title?: string }[] = [];
  if (m.rss !== null) facts.push({ label: 'Memory', value: bytes(m.rss) });
  if (m.virtual !== null) facts.push({ label: 'Virtual', value: bytes(m.virtual) });
  if (m.cpuSeconds !== null) facts.push({ label: 'CPU', value: `${m.cpuSeconds.toFixed(1)} s` });
  if (m.openFds !== null)
    facts.push({ label: 'Open files', value: String(m.openFds), ...(m.maxFds === null ? {} : { title: `of ${String(m.maxFds)}` }) });
  if (m.startTime !== null) facts.push({ label: 'Started', value: formatInstant(new Date(m.startTime * 1000)) });
  return (
    <div data-python-metrics-read="" className="flex flex-col gap-3">
      {m.version === undefined ? null : (
        <div className="flex flex-wrap items-center gap-2">
          <Pill tone="info">{`${m.implementation ?? 'Python'} ${m.version}`}</Pill>
        </div>
      )}
      {facts.length === 0 ? null : (
        <dl className="grid grid-cols-2 gap-x-6 gap-y-1 text-xs sm:grid-cols-5">
          {facts.map((f) => (
            <div key={f.label} className="flex flex-col" title={f.title}>
              <dt className="text-muted">{f.label}</dt>
              <dd className="tabular-nums">{f.value}</dd>
            </div>
          ))}
        </dl>
      )}
      {m.gc.length === 0 ? null : (
        <div className="overflow-x-auto">
          <table data-python-gc="" className={`${TABLE} w-full`}>
            <thead>
              <tr className={HEAD_ROW}>
                <th className={headCell()}>GC generation</th>
                <th className={`${headCell()} text-right`}>Collections</th>
                <th className={`${headCell()} text-right`}>Collected</th>
                <th className={`${headCell()} text-right`}>Uncollectable</th>
              </tr>
            </thead>
            <tbody>
              {m.gc.map((g) => (
                <tr key={g.generation} data-python-gc-generation={g.generation} className={ROW}>
                  <td className={`${bodyCell(true)} font-mono`}>{g.generation}</td>
                  <td className={`${bodyCell(true)} text-right tabular-nums`}>{count(g.collections)}</td>
                  <td className={`${bodyCell(true)} text-right tabular-nums`}>{count(g.collected)}</td>
                  <td className={`${bodyCell(true)} text-right tabular-nums`}>
                    {g.uncollectable !== null && g.uncollectable > 0 ? <Pill tone="warn">{count(g.uncollectable)}</Pill> : count(g.uncollectable)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function count(n: number | null): string {
  return n === null ? '—' : Math.round(n).toLocaleString('en-US');
}

function bytes(n: number): string {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 || v >= 100 ? 0 : 1)} ${units[i] ?? 'B'}`;
}
