import { useQuery } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { Failure, Pending, Pill, btnPrimary, btnSecondary, type Downloaded } from '@kubetower/ui';

import { PPROF_SECONDS, goroutines, pprofDownload, pprofQuery, type GoroutineGroup, type Goroutines, type PprofDownload } from './data';

/**
 * Go's diagnostics on a pod's Go tab, read from net/http/pprof through a
 * tunnel to the pod (`pprof.go`): the goroutines grouped by state and stack,
 * and the heap, CPU and trace profiles as files for `go tool pprof` and
 * `go tool trace`. Nothing but a GET of pprof's own endpoints reaches the
 * process; no signal, no debugger.
 */

const SOURCE_TITLE: Readonly<Record<string, string>> = {
  annotation: 'From kubetower.io/pprof-port',
  port: 'From the container port named pprof',
  default: 'The net/http/pprof convention; kubetower.io/pprof-port or a port named pprof sets another',
};

function save(file: Downloaded): void {
  const url = URL.createObjectURL(file.blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = file.filename;
  a.click();
  setTimeout(() => {
    URL.revokeObjectURL(url);
  }, 0);
}

type Run = { status: 'idle' } | { status: 'running'; what: string } | { status: 'failed'; error: unknown };

export function GoPprof({ cluster, namespace, name, container }: { cluster: string; namespace: string; name: string; container: string }): ReactNode {
  const state = useQuery(pprofQuery(cluster, namespace, name, container));
  const [run, setRun] = useState<Run>({ status: 'idle' });
  const [dump, setDump] = useState<Goroutines | undefined>(undefined);
  const [cpu, setCpu] = useState<number>(PPROF_SECONDS.profile[1]);
  const [trace, setTrace] = useState<number>(PPROF_SECONDS.trace[0]);

  if (state.data === undefined) {
    return state.isError ? <Failure error={state.error} onRetry={() => void state.refetch()} /> : <Pending what="pprof" />;
  }
  const { port, source, reachable, detail } = state.data;
  const busy = run.status === 'running';
  const off = !reachable || busy;

  const fetchProfile = async (what: PprofDownload, seconds?: number) => {
    setRun({ status: 'running', what });
    try {
      save(await pprofDownload(cluster, namespace, name, container, what, seconds));
      setRun({ status: 'idle' });
    } catch (error) {
      setRun({ status: 'failed', error });
    }
  };
  const readGoroutines = async () => {
    setRun({ status: 'running', what: 'goroutines' });
    try {
      setDump(await goroutines(cluster, namespace, name, container));
      setRun({ status: 'idle' });
    } catch (error) {
      setRun({ status: 'failed', error });
    }
  };

  return (
    <section data-pprof="" className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Pill neutral="muted" title={SOURCE_TITLE[source]}>
          pprof :{port}
        </Pill>
        {reachable ? (
          <Pill tone="ok">reachable</Pill>
        ) : (
          <span data-pprof-unreachable="">
            <Pill tone="warn" title={detail === undefined ? undefined : `${detail}. Serve net/http/pprof on this port.`}>
              unreachable
            </Pill>
          </span>
        )}
        <button type="button" className={btnSecondary} onClick={() => void state.refetch()} disabled={state.isFetching}>
          Check again
        </button>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <button type="button" data-pprof-goroutines="" className={btnPrimary} disabled={off} onClick={() => void readGoroutines()}>
          {busy && run.what === 'goroutines' ? 'Reading…' : 'Goroutines'}
        </button>
        <button type="button" data-pprof-heap="" className={btnSecondary} disabled={off} onClick={() => void fetchProfile('heap')}>
          Heap profile
        </button>
        <span className="flex items-center gap-1">
          <button type="button" data-pprof-cpu="" className={btnSecondary} disabled={off} onClick={() => void fetchProfile('profile', cpu)}>
            {busy && run.what === 'profile' ? 'Profiling…' : 'CPU profile'}
          </button>
          <Seconds label="CPU profile seconds" options={PPROF_SECONDS.profile} value={cpu} onChange={setCpu} disabled={off} />
        </span>
        <span className="flex items-center gap-1">
          <button type="button" data-pprof-trace="" className={btnSecondary} disabled={off} onClick={() => void fetchProfile('trace', trace)}>
            {busy && run.what === 'trace' ? 'Tracing…' : 'Trace'}
          </button>
          <Seconds label="Trace seconds" options={PPROF_SECONDS.trace} value={trace} onChange={setTrace} disabled={off} />
        </span>
      </div>
      {run.status === 'failed' ? <Failure error={run.error} onRetry={() => setRun({ status: 'idle' })} /> : null}
      {dump === undefined ? null : <GoroutineView dump={dump} filename={`${name}-${container}-goroutines.txt`} />}
    </section>
  );
}

function Seconds({
  label,
  options,
  value,
  onChange,
  disabled,
}: {
  label: string;
  options: readonly number[];
  value: number;
  onChange: (n: number) => void;
  disabled: boolean;
}): ReactNode {
  return (
    <select
      aria-label={label}
      className="rounded border border-line bg-panel px-1 py-0.5 text-xs text-fg"
      value={value}
      disabled={disabled}
      onChange={(e) => onChange(Number(e.target.value))}
    >
      {options.map((n) => (
        <option key={n} value={n}>
          {n} s
        </option>
      ))}
    </select>
  );
}

function GoroutineView({ dump, filename }: { dump: Goroutines; filename: string }): ReactNode {
  const [filter, setFilter] = useState('');
  const needle = filter.trim().toLowerCase();
  const shown = needle === '' ? dump.groups : dump.groups.filter((g) => matches(g, needle));
  return (
    <div data-goroutines="" className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <Pill>{dump.total} goroutines</Pill>
        <Pill neutral="muted">{dump.groups.length} stacks</Pill>
        {dump.truncated ? (
          <Pill tone="warn" title="The dump was cut at 16 MiB">
            cut
          </Pill>
        ) : null}
        <input
          type="search"
          aria-label="Filter goroutines"
          placeholder="Filter"
          className="rounded border border-line bg-panel px-2 py-0.5 text-xs"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
        />
        <button
          type="button"
          className={btnSecondary}
          onClick={() => save({ blob: new Blob([dump.raw], { type: 'text/plain' }), filename })}
        >
          Download
        </button>
      </div>
      <ul className="flex flex-col gap-1">
        {shown.map((g) => (
          <li key={`${g.state}|${g.frames.join('|')}`} data-goroutine-group={g.state} className="rounded border border-line px-2 py-1">
            <div className="flex flex-wrap items-center gap-2">
              <Pill>{g.count}</Pill>
              <Pill neutral="muted">{g.state}</Pill>
              {g.longest === undefined ? null : <span className="text-xs text-muted">{g.longest}</span>}
              <code className="text-xs">{g.frames[0] ?? ''}</code>
            </div>
            {g.frames.length > 1 ? (
              <details>
                <summary className="cursor-pointer text-xs text-muted">{g.frames.length} frames</summary>
                <pre className="text-xs">{g.frames.join('\n')}</pre>
              </details>
            ) : null}
          </li>
        ))}
      </ul>
    </div>
  );
}

function matches(g: GoroutineGroup, needle: string): boolean {
  return g.state.toLowerCase().includes(needle) || g.frames.some((f) => f.toLowerCase().includes(needle));
}
