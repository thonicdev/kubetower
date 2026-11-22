import { useQuery } from '@tanstack/react-query';
import { useMemo, useState, type ReactNode } from 'react';
import { useSearchParams } from 'react-router';
import { Failure, HEAD_ROW, Pending, ROW, TABLE, bodyCell, focusRing, headCell } from '@kubetower/ui';

import { ago, complianceQuery, n, pageOf, type ComplianceReport } from './data';
import { Pager, SevChip, sourceLabel, sourceState } from './parts';

/**
 * The Compliance view: one card per standard trivy-operator checks the
 * cluster against (NSA, CIS, PSS), its controls passed and failed as a bar,
 * then its controls, the failed first. `?cf=all` shows every control;
 * otherwise only the failed ones. Read when the view opens.
 */
export function ComplianceView({ cluster }: { cluster: string }): ReactNode {
  const query = useQuery(complianceQuery(cluster));
  const [params, setParams] = useSearchParams();
  const all = params.get('cf') === 'all';
  const data = query.data;
  if (data === undefined) {
    return query.isError ? <Failure error={query.error} onRetry={() => void query.refetch()} /> : <Pending what="the compliance reports" />;
  }
  const state = sourceState(data.source);
  if (state !== 'read' && state !== 'partial') {
    return (
      <p
        data-compliance={state}
        title={data.source.note}
        className={`self-start rounded-full border border-line px-3 py-1 text-sm ${state === 'denied' ? 'text-warn' : state === 'unreadable' ? 'text-bad' : 'text-muted'}`}
      >
        {sourceLabel(data.source.kind)} {state}
      </p>
    );
  }
  const toggle = () => {
    const out = new URLSearchParams(params);
    if (all) out.delete('cf');
    else out.set('cf', 'all');
    setParams(out, { replace: true });
  };
  return (
    <div className="flex flex-col gap-4" data-compliance="read">
      <div className="flex items-center gap-2">
        <button
          type="button"
          aria-pressed={!all}
          data-failed-only=""
          onClick={toggle}
          className={`rounded-full border px-2 py-0.5 text-2xs ${!all ? 'border-accent bg-panel-2 ring-1 ring-accent' : 'border-line hover:bg-panel-2'} ${focusRing}`}
        >
          Failed only
        </button>
      </div>
      {data.reports.map((r) => (
        <Standard key={r.name} report={r} all={all} read={data.read} />
      ))}
    </div>
  );
}

function Standard({ report, all, read }: { report: ComplianceReport; all: boolean; read: string }): ReactNode {
  const controls = useMemo(() => (all ? report.controls : report.controls.filter((c) => c.fail !== null && c.fail > 0)), [report.controls, all]);
  const [at, setAt] = useState(0);
  const { rows, page, pages } = useMemo(() => pageOf(controls, at), [controls, at]);
  const sum = report.pass + report.fail;
  const passed = sum === 0 ? 0 : (report.pass / sum) * 100;
  return (
    <section aria-label={report.title} className="flex min-w-0 flex-col gap-2 rounded-lg border border-line bg-panel px-3.5 py-3" data-standard={report.name}>
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-sm font-semibold">
          {report.title}
          {report.version === undefined || report.version === '' ? null : <span className="font-normal text-muted"> {report.version}</span>}
        </h2>
        <span className="ml-auto text-xs tabular-nums" data-counts="">
          <span className="text-ok">{n(report.pass)} pass</span>
          <span className="text-muted"> · </span>
          <span className={report.fail > 0 ? 'text-bad' : 'text-muted'}>{n(report.fail)} fail</span>
        </span>
        <span className="text-xs text-muted tabular-nums" title={report.updated}>
          {report.updated === undefined ? '—' : ago(report.updated, read)}
        </span>
      </div>
      <div className="flex h-2 overflow-hidden rounded-full bg-panel-2" aria-hidden="true">
        <span className="bg-ok" style={{ width: `${String(passed)}%` }} />
        <span className="bg-bad" style={{ width: `${String(sum === 0 ? 0 : 100 - passed)}%` }} />
      </div>
      {controls.length === 0 ? (
        <p className="text-xs text-muted" data-none="">
          None
        </p>
      ) : (
        <>
          <div className="min-w-0 overflow-x-auto">
            <table className={`${TABLE} w-full`} data-controls="">
              <thead>
                <tr className={HEAD_ROW}>
                  <th className={`${headCell()} w-20`}>ID</th>
                  <th className={headCell()}>Control</th>
                  <th className={`${headCell()} w-24`}>Severity</th>
                  <th className={`${headCell()} w-16 text-right`}>Failed</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((c) => (
                  <tr key={c.id} className={ROW} data-control={c.id}>
                    <td className={`${bodyCell(true)} font-mono text-xs text-muted`}>{c.id}</td>
                    <td className={bodyCell(true)}>{c.name}</td>
                    <td className={bodyCell(true)}>
                      <SevChip severity={c.severity} />
                    </td>
                    <td className={`${bodyCell(true)} text-right text-xs tabular-nums`}>
                      {c.fail === null ? (
                        <span className="text-muted" title="Manual">
                          —
                        </span>
                      ) : (
                        <span className={c.fail > 0 ? 'text-bad' : 'text-ok'}>{n(c.fail)}</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <Pager page={page} pages={pages} count={controls.length} onPage={setAt} />
          {report.controlsTotal > report.controls.length ? (
            <span className="text-2xs text-muted" data-capped="">
              {n(report.controls.length)} / {n(report.controlsTotal)}
            </span>
          ) : null}
        </>
      )}
    </section>
  );
}
