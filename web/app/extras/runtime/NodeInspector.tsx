import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { Dialog, Failure, KeyLayer, Pending, Pill, btnPrimary, btnSecondary, type Downloaded } from '@kubetower/ui';

import { NODE_PROFILE_SECONDS, nodeDownload, nodeQuery, nodeReport, openInspector, type NodeReport } from './data';

/**
 * Node.js's diagnostics on a pod's Node.js tab, through its inspector reached
 * by a tunnel to the pod (`node.go`). Opening the inspector is confirmed and
 * stays until the process restarts; the reads are a heap snapshot (confirmed),
 * a CPU profile and the diagnostic report.
 */

const OPEN_RISK = 'Stays open until the process restarts; anyone reaching the pod’s port can run code in the process. Recorded in the audit log.';
const SNAPSHOT_RISK = 'Stops the process while the snapshot is written, and can double its memory.';

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

type Run =
  | { status: 'idle' }
  | { status: 'confirm-open'; pid?: number }
  | { status: 'choose'; pids: number[]; gone?: number | undefined }
  | { status: 'confirm-snapshot' }
  | { status: 'running'; what: string }
  | { status: 'failed'; error: unknown };

export function NodeInspector({ cluster, namespace, name, container, readOnly }: { cluster: string; namespace: string; name: string; container: string; readOnly: boolean }): ReactNode {
  const client = useQueryClient();
  const query = nodeQuery(cluster, namespace, name, container, readOnly);
  const state = useQuery(query);
  const [run, setRun] = useState<Run>({ status: 'idle' });
  const [seconds, setSeconds] = useState<number>(NODE_PROFILE_SECONDS[0]);
  const [report, setReport] = useState<NodeReport | undefined>(undefined);

  if (state.data === undefined) {
    return state.isError ? <Failure error={state.error} onRetry={() => void state.refetch()} /> : <Pending what="the inspector" />;
  }
  const { port, source, started, sigusr1Disabled, open, detail } = state.data;
  const busy = run.status === 'running';

  const doOpen = async (pid?: number) => {
    setRun({ status: 'running', what: 'open' });
    try {
      const opened = await openInspector(cluster, namespace, name, container, pid);
      if (opened.state === 'choose') {
        setRun({ status: 'choose', pids: opened.pids, gone: pid });
        return;
      }
      client.setQueryData(query.queryKey, opened.answer);
      setRun({ status: 'idle' });
    } catch (error) {
      setRun({ status: 'failed', error });
    }
  };
  const doDownload = async (read: 'heapsnapshot' | 'profile') => {
    setRun({ status: 'running', what: read });
    try {
      save(await nodeDownload(cluster, namespace, name, container, read, seconds));
      setRun({ status: 'idle' });
    } catch (error) {
      setRun({ status: 'failed', error });
    }
  };
  const doReport = async () => {
    setRun({ status: 'running', what: 'report' });
    try {
      setReport(await nodeReport(cluster, namespace, name, container));
      setRun({ status: 'idle' });
    } catch (error) {
      setRun({ status: 'failed', error });
    }
  };

  return (
    <section data-node-inspector="" className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Pill neutral="muted" title={`Port from ${source === 'default' ? 'the default' : source === 'env' ? 'NODE_OPTIONS' : 'the command line'}`}>
          inspector :{port}
        </Pill>
        <span data-node-state={open ? 'open' : 'closed'}>
          {open ? <Pill tone="warn">open</Pill> : <Pill neutral="muted" title={detail}>closed</Pill>}
        </span>
        {started ? <Pill neutral="muted">--inspect</Pill> : null}
        {sigusr1Disabled ? <Pill tone="bad">--disable-sigusr1</Pill> : null}
        <button type="button" className={btnSecondary} onClick={() => void state.refetch()} disabled={state.isFetching}>
          Check again
        </button>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        {open ? null : (
          <button
            type="button"
            data-node-open=""
            className={btnSecondary}
            disabled={busy || sigusr1Disabled}
            title={OPEN_RISK}
            onClick={() => setRun({ status: 'confirm-open' })}
          >
            {busy && run.what === 'open' ? 'Opening…' : 'Open inspector'}
          </button>
        )}
        <button
          type="button"
          data-node-snapshot=""
          className={btnSecondary}
          disabled={!open || busy}
          title={SNAPSHOT_RISK}
          onClick={() => setRun({ status: 'confirm-snapshot' })}
        >
          {busy && run.what === 'heapsnapshot' ? 'Writing…' : 'Heap snapshot'}
        </button>
        <span className="flex items-center gap-1">
          <button type="button" data-node-profile="" className={btnSecondary} disabled={!open || busy} onClick={() => void doDownload('profile')}>
            {busy && run.what === 'profile' ? 'Profiling…' : 'CPU profile'}
          </button>
          <select
            aria-label="CPU profile seconds"
            className="rounded border border-line bg-panel px-1 py-0.5 text-xs text-fg"
            value={seconds}
            disabled={!open || busy}
            onChange={(e) => setSeconds(Number(e.target.value))}
          >
            {NODE_PROFILE_SECONDS.map((n) => (
              <option key={n} value={n}>
                {n} s
              </option>
            ))}
          </select>
        </span>
        <button type="button" data-node-report="" className={btnPrimary} disabled={!open || busy} onClick={() => void doReport()}>
          {busy && run.what === 'report' ? 'Reading…' : 'Diagnostic report'}
        </button>
      </div>
      {run.status === 'confirm-open' ? (
        <Confirm title={`Open the inspector of ${container}?`} risks={['until restart', `code execution on :${String(port)}`]} go="Open" onGo={() => void doOpen(run.pid)} onCancel={() => setRun({ status: 'idle' })} />
      ) : null}
      {run.status === 'confirm-snapshot' ? (
        <Confirm title={`Heap snapshot of ${container}?`} risks={['process paused', 'memory ×2', 'holds secrets']} go="Snapshot and download" onGo={() => void doDownload('heapsnapshot')} onCancel={() => setRun({ status: 'idle' })} />
      ) : null}
      {run.status === 'choose' ? (
        <ul data-node-choose="" className="flex flex-wrap items-center gap-1">
          {run.gone === undefined ? null : (
            <li data-node-gone="">
              <Pill tone="warn">pid {run.gone} exited</Pill>
            </li>
          )}
          {run.pids.map((pid) => (
            <li key={pid}>
              <button type="button" className={btnSecondary} onClick={() => setRun({ status: 'confirm-open', pid })}>
                pid {pid}
              </button>
            </li>
          ))}
        </ul>
      ) : null}
      {run.status === 'failed' ? <Failure error={run.error} onRetry={() => setRun({ status: 'idle' })} /> : null}
      {report === undefined ? null : <ReportView report={report} filename={`${name}-${container}-report.json`} />}
    </section>
  );
}

function Confirm({ title, risks, go, onGo, onCancel }: { title: string; risks: string[]; go: string; onGo: () => void; onCancel: () => void }): ReactNode {
  return (
    <Dialog title={title} layer={KeyLayer.Confirm} onClose={onCancel}>
      <div data-node-confirm="" className="flex flex-col gap-3 text-sm">
        <div className="flex flex-wrap gap-1">
          {risks.map((r) => (
            <Pill key={r} tone="warn">
              {r}
            </Pill>
          ))}
        </div>
        <div className="flex justify-end gap-2">
          <button type="button" className={btnSecondary} onClick={onCancel}>
            Cancel
          </button>
          <button type="button" data-node-go="" className={btnPrimary} onClick={onGo}>
            {go}
          </button>
        </div>
      </div>
    </Dialog>
  );
}

function field(report: Record<string, unknown>, ...path: string[]): string | undefined {
  let at: unknown = report;
  for (const key of path) {
    if (typeof at !== 'object' || at === null) return undefined;
    at = (at as Record<string, unknown>)[key];
  }
  return typeof at === 'string' || typeof at === 'number' ? String(at) : undefined;
}

function ReportView({ report, filename }: { report: NodeReport; filename: string }): ReactNode {
  const facts: [string, string | undefined][] = [
    ['Node.js', field(report.report, 'header', 'nodejsVersion')],
    ['uptime (s)', field(report.report, 'resourceUsage', 'elapsed') ?? field(report.report, 'header', 'uptime')],
    ['rss', field(report.report, 'resourceUsage', 'rss')],
    ['heap used', field(report.report, 'javascriptHeap', 'usedMemory')],
    ['heap total', field(report.report, 'javascriptHeap', 'totalMemory')],
  ];
  const text = JSON.stringify(report.report, null, 2);
  return (
    <div data-node-report-view="" className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        {facts
          .filter((f): f is [string, string] => f[1] !== undefined)
          .map(([label, value]) => (
            <Pill key={label} neutral="muted">
              {label} {value}
            </Pill>
          ))}
        {report.masked > 0 ? (
          <Pill tone="info" title="Environment values whose name looks like a secret's">
            {report.masked} masked
          </Pill>
        ) : null}
        <button type="button" className={btnSecondary} onClick={() => save({ blob: new Blob([text], { type: 'application/json' }), filename })}>
          Download
        </button>
      </div>
      <details>
        <summary className="cursor-pointer text-xs text-muted">JSON</summary>
        <pre className="max-h-96 overflow-auto text-xs">{text}</pre>
      </details>
    </div>
  );
}
