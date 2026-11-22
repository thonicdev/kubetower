import { useQuery } from '@tanstack/react-query';
import { useMemo, useState, type ReactNode } from 'react';
import { useSearchParams } from 'react-router';
import { Failure, HEAD_ROW, ROW, TABLE, bodyCell, focusRing, headCell } from '@kubetower/ui';

import { ago, findingsQuery, n, pageOf, total, type Counts, type FindingRow, type Source } from './data';
import { Pager, SevBar, SevCounts, scaleOf, sourceLabel, sourceState, tabLook } from './parts';

const severity = (c: Counts) => c.critical + c.high + c.medium + c.low;

/**
 * The reports about the cluster itself - its components, its cluster-scoped
 * objects, its control plane and its nodes - below the workloads: one chip
 * per kind that filters (`?cr=` in the address), then the rows, worst first,
 * 50 at a time. Read once with the page; nothing drawn where the cluster
 * serves none of the kinds. The RBAC view is the same section on the RBAC
 * assessment reports, read when the view opens.
 */
export function ClusterReports({ cluster }: { cluster: string }): ReactNode {
  return <FindingsSection query={findingsQuery(cluster, 'cluster')} label="Cluster" param="cr" hideWhenAbsent />;
}

/** The RBAC view: Roles and ClusterRoles as trivy-operator assessed them. */
export function RbacReports({ cluster }: { cluster: string }): ReactNode {
  return <FindingsSection query={findingsQuery(cluster, 'rbac')} label="RBAC" param="rr" hideWhenAbsent={false} />;
}

function FindingsSection({
  query: options,
  label,
  param,
  hideWhenAbsent,
}: {
  query: ReturnType<typeof findingsQuery>;
  label: string;
  param: string;
  hideWhenAbsent: boolean;
}): ReactNode {
  const query = useQuery(options);
  const [params, setParams] = useSearchParams();
  const pick = params.get(param) ?? '';
  const data = query.data;
  const rows = useMemo(() => (data === undefined ? [] : pick === '' ? data.rows : data.rows.filter((r) => r.source === pick)), [data, pick]);

  if (data === undefined) {
    return query.isError ? <Failure error={query.error} onRetry={() => void query.refetch()} /> : null;
  }
  if (hideWhenAbsent && data.sources.every((s) => s.state === 'absent')) return null;

  const press = (kind: string) => {
    const out = new URLSearchParams(params);
    if (pick === kind) out.delete(param);
    else out.set(param, kind);
    setParams(out, { replace: true });
  };

  return (
    <section aria-label={label} className="flex min-w-0 flex-col gap-2 rounded-lg border border-line bg-panel px-3.5 py-3" data-cluster-reports={param}>
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-sm font-semibold">
          {label} <span className="font-normal text-muted">{n(rows.length)}</span>
        </h2>
        {data.capped ? (
          <span data-capped="" className="rounded-full border border-warn/45 px-2 text-2xs text-warn" title={`The ${n(data.rows.length)} worst of ${n(data.total)}`}>
            {n(data.rows.length)} / {n(data.total)}
          </span>
        ) : null}
        <ul aria-label={`Filter the ${label} reports`} className="flex flex-wrap gap-1.5">
          {data.sources.map((s) => (
            <KindChip key={s.kind} source={s} count={data.byKind[s.kind] ?? 0} pressed={pick === s.kind} onPress={() => press(s.kind)} />
          ))}
        </ul>
      </div>
      <FindingTable key={pick} rows={rows} read={data.read} />
    </section>
  );
}

function KindChip({ source, count, pressed, onPress }: { source: Source; count: number; pressed: boolean; onPress: () => void }): ReactNode {
  const state = sourceState(source);
  const usable = state === 'read' || state === 'partial';
  const title = [`${n(source.reports)} reports`, source.note].filter((x) => x !== undefined && x !== '').join(' · ');
  return (
    <li>
      <button
        type="button"
        data-cluster-kind={source.kind}
        data-state={state}
        aria-pressed={pressed}
        disabled={!usable}
        title={title}
        onClick={onPress}
        className={`flex items-center gap-1.5 ${tabLook(pressed)} disabled:cursor-default disabled:hover:bg-transparent ${focusRing}`}
      >
        <span className={usable ? 'text-fg' : 'text-muted'}>{sourceLabel(source.kind)}</span>
        <span className={state === 'denied' || state === 'partial' ? 'text-warn' : state === 'unreadable' ? 'text-bad' : 'text-muted'}>
          {usable ? n(count) : state}
        </span>
      </button>
    </li>
  );
}

function FindingTable({ rows, read }: { rows: readonly FindingRow[]; read: string }): ReactNode {
  const [at, setAt] = useState(0);
  const { rows: shown, page, pages } = useMemo(() => pageOf(rows, at), [rows, at]);
  const max = useMemo(() => Math.max(0, ...shown.map((r) => severity(r.counts))), [shown]);
  const images = useMemo(() => rows.some((r) => r.image !== undefined && r.image !== ''), [rows]);
  if (rows.length === 0) return <p className="text-xs text-muted" data-none="">None</p>;
  return (
    <>
      <div className="min-w-0 overflow-x-auto">
        <table className={`${TABLE} w-full`} data-findings="">
          <thead>
            <tr className={HEAD_ROW}>
              <th className={`${headCell()} w-28`}>Report</th>
              <th className={headCell()}>Object</th>
              {images ? <th className={headCell()}>Image</th> : null}
              <th className={`${headCell()} w-40`}>Severity</th>
              <th className={`${headCell()} w-40 text-right`} title="Critical · High · Medium · Low">
                C · H · M · L
              </th>
              <th className={`${headCell()} w-16 text-right`}>Scan</th>
            </tr>
          </thead>
          <tbody>
            {shown.map((r, i) => (
              <tr key={`${r.source}/${r.namespace ?? ''}/${r.kind}/${r.name}/${String(i)}`} className={ROW} data-finding={`${r.source}/${r.kind}/${r.name}`}>
                <td className={`${bodyCell(true)} text-xs text-muted`}>{sourceLabel(r.source)}</td>
                <td className={`${bodyCell(true)} max-w-96 truncate`} title={[r.kind, r.namespace, r.name].filter((x) => x !== undefined && x !== '').join('/')}>
                  {r.kind === '' ? null : <span className="text-muted">{r.kind}/</span>}
                  {r.namespace === undefined || r.namespace === '' ? null : <span className="text-muted">{r.namespace}/</span>}
                  {r.name}
                </td>
                {images ? (
                  <td className={`${bodyCell(true)} max-w-72 truncate text-xs`} title={r.image}>
                    {r.image ?? ''}
                  </td>
                ) : null}
                <td className={bodyCell(true)}>
                  {total(r.counts) === 0 ? (
                    <span data-clean="" className="text-xs text-ok">
                      Clean
                    </span>
                  ) : (
                    <SevBar counts={r.counts} scale={scaleOf(severity(r.counts), max)} />
                  )}
                </td>
                <td className={`${bodyCell(true)} text-right text-xs`}>
                  <SevCounts counts={r.counts} />
                </td>
                <td className={`${bodyCell(true)} text-right text-xs text-muted tabular-nums`} title={r.scanned}>
                  {r.scanned === undefined ? '—' : ago(r.scanned, read)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <Pager page={page} pages={pages} count={rows.length} onPage={setAt} />
    </>
  );
}

