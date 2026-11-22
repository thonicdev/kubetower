import { useQuery } from '@tanstack/react-query';
import { useMemo, useState, type ReactNode } from 'react';
import { Link, useSearchParams } from 'react-router';
import { BackArrow, Failure, HEAD_ROW, PageHeader, Pending, ROW, TABLE, bodyCell, focusRing, headCell, useDebounced, useUrlParameter } from '@kubetower/ui';

import { n, pageOf, workloadHref } from './data';
import { Pager, SEV, SevChip, SourcePills } from './parts';
import {
  filterPolicies,
  filterResources,
  policiesQuery,
  policyOf,
  policyQuery,
  policyTileOf,
  policyTotal,
  type PolicyCounts,
  type PolicyOverview,
  type PolicyResource,
  type PolicyResult,
  type PolicyRow,
  type PolicyTile,
} from './policyData';

/**
 * The Policies view (`?view=policies`): what Kyverno and Gatekeeper found.
 * Sources, tiles that filter (`?pt=`), namespaces as bars (`?pns=` scopes the
 * read), then the policies and the resources, worst first, 50 at a time; a
 * policy opens its results (`?policy=engine/name`). Read while the view is
 * open, once a minute at most, never on an interval.
 */
export function PoliciesView({ cluster }: { cluster: string }): ReactNode {
  const [params] = useSearchParams();
  const ns = params.get('pns') ?? '';
  const query = useQuery(policiesQuery(cluster, ns));
  const picked = policyOf(params);
  const data = query.data;
  if (data === undefined) {
    return query.isError ? <Failure error={query.error} onRetry={() => void query.refetch()} /> : <Pending what="the policy reports" />;
  }
  if (!data.engines) {
    return (
      <p data-policies="none" title="No PolicyReport, Gatekeeper constraint or Kyverno policy is served on this cluster" className="self-start rounded-full border border-line px-3 py-1 text-sm text-muted">
        no policy engine
      </p>
    );
  }
  return (
    <div className="flex flex-col gap-4" data-policies="read">
      {query.isError ? <Failure error={query.error} onRetry={() => void query.refetch()} /> : null}
      <SourcePills sources={data.sources} />
      {picked === undefined ? <Board overview={data} cluster={cluster} /> : <OnePolicy cluster={cluster} engine={picked.engine} name={picked.name} row={data.policies.find((p) => p.engine === picked.engine && p.name === picked.name)} />}
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/* The bar                                                                     */
/* -------------------------------------------------------------------------- */

const OUTCOMES = [
  ['error', SEV.critical.bar],
  ['fail', SEV.high.bar],
  ['warn', SEV.medium.bar],
] as const;

export function policyHint(c: PolicyCounts): string {
  return `${n(c.fail)} fail · ${n(c.error)} error · ${n(c.warn)} warn · ${n(c.audit)} audit`;
}

/** One stacked bar, error, fail then warn; `scale` against the longest beside it. */
export function PolicyBar({ counts, scale = 1 }: { counts: PolicyCounts; scale?: number }): ReactNode {
  const sum = policyTotal(counts);
  return (
    <span data-policybar="" title={policyHint(counts)} className="flex h-2 min-w-0 flex-1 overflow-hidden rounded-full bg-white/5">
      {sum === 0 ? null : (
        <span className="flex h-full" style={{ width: `${String(Math.max(2, Math.min(1, scale) * 100))}%` }}>
          {OUTCOMES.map(([k, bar]) =>
            counts[k] === 0 ? null : <span key={k} data-seg={k} className={`h-full ${bar}`} style={{ width: `${String((counts[k] / sum) * 100)}%` }} />,
          )}
        </span>
      )}
    </span>
  );
}

/** "3 · 1 · 2": fail, error, warn, each in its color, zero grayed. */
function OutcomeCounts({ counts }: { counts: PolicyCounts }): ReactNode {
  return (
    <span className="flex justify-end gap-2 tabular-nums" title={policyHint(counts)}>
      <span className={counts.fail === 0 ? 'text-muted/50' : SEV.high.text}>{n(counts.fail)}</span>
      <span className={counts.error === 0 ? 'text-muted/50' : SEV.critical.text}>{n(counts.error)}</span>
      <span className={counts.warn === 0 ? 'text-muted/50' : SEV.medium.text}>{n(counts.warn)}</span>
    </span>
  );
}

const scaleOf = (v: number, max: number) => (max <= 0 ? 0 : v / max);

/* -------------------------------------------------------------------------- */
/* The board                                                                   */
/* -------------------------------------------------------------------------- */

function Board({ overview, cluster }: { overview: PolicyOverview; cluster: string }): ReactNode {
  const [params, setParams] = useSearchParams();
  const tile = policyTileOf(params);
  const ns = params.get('pns') ?? '';
  const [typed, setTyped] = useUrlParameter('pq');
  const q = useDebounced(typed, 200);
  const set = (name: string, value: string) => {
    const out = new URLSearchParams(params);
    if (value === '' || out.get(name) === value) out.delete(name);
    else out.set(name, value);
    setParams(out, { replace: true });
  };
  const policies = useMemo(() => filterPolicies(overview.policies, tile, q), [overview.policies, tile, q]);
  const resources = useMemo(() => filterResources(overview.resources, tile, q), [overview.resources, tile, q]);
  const t = overview.totals;

  return (
    <div className="flex flex-col gap-4">
      <ul aria-label="Filter the policies" className="flex flex-wrap gap-2" data-policy-tiles="">
        <Tile id="fail" tile={tile} count={t.fail} label="Fail" bar="border-l-y-tag" title="Failed results" onPress={set} />
        <Tile id="error" tile={tile} count={t.error} label="Error" bar="border-l-bad" title="Results the engine could not evaluate" onPress={set} />
        <Tile id="warn" tile={tile} count={t.warn} label="Warn" bar="border-l-warn" title="Warnings" onPress={set} />
        <Tile id="audit" tile={tile} count={t.audit} label="Audit" bar="border-l-warn" title="Failures a policy lets through: Kyverno in Audit, Gatekeeper dryrun or warn" onPress={set} />
        <Tile id="policies" tile={tile} count={overview.policiesTotal} label="Policies" bar="border-l-accent" title="Policies with a result that does not pass" onPress={set} />
        <Tile id="resources" tile={tile} count={overview.failing} label="Resources" bar="border-l-accent" title="Resources with a failure or an error" onPress={set} />
      </ul>
      {overview.namespaces.length === 0 ? null : <Namespaces overview={overview} ns={ns} onPick={(v) => set('pns', v)} />}
      <div className="flex items-center gap-2">
        {ns === '' ? null : (
          <button type="button" data-chip="" onClick={() => set('pns', ns)} className={`rounded-full border border-accent/50 px-2 text-2xs ${focusRing}`} title="Remove the filter">
            ns {ns} ×
          </button>
        )}
        <input
          type="search"
          aria-label="Filter policies and resources"
          placeholder="Filter"
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          className={`ml-auto w-56 rounded-md border border-line bg-bg px-2 py-1 text-xs ${focusRing}`}
        />
      </div>
      {tile === 'resources' ? null : <Policies key={`p|${tile}|${q}|${ns}`} rows={policies} total={overview.policiesTotal} />}
      {tile === 'policies' ? null : <Resources key={`r|${tile}|${q}|${ns}`} rows={resources} total={overview.resourcesTotal} cluster={cluster} />}
    </div>
  );
}

function Tile({
  id,
  tile,
  count,
  label,
  bar,
  title,
  onPress,
}: {
  id: PolicyTile;
  tile: PolicyTile;
  count: number;
  label: string;
  bar: string;
  title: string;
  onPress: (name: string, value: string) => void;
}): ReactNode {
  const on = tile === id;
  return (
    <li>
      <button
        type="button"
        data-tile={id}
        data-policy-tile={id}
        data-count={count}
        aria-pressed={on}
        title={title}
        onClick={() => onPress('pt', id)}
        className={`flex min-w-28 flex-col items-start rounded-md border border-l-4 px-3 py-1.5 text-left ${count === 0 ? 'border-l-line' : bar} ${
          on ? 'border-accent bg-panel-2 ring-1 ring-accent' : 'border-line bg-panel hover:bg-panel-2'
        } ${focusRing}`}
      >
        <span className="flex items-baseline gap-2">
          <span className={`text-2xl font-semibold tabular-nums ${count === 0 ? 'text-muted' : ''}`}>{n(count)}</span>
          <span className="text-2xs tracking-wide text-muted uppercase">{label}</span>
        </span>
      </button>
    </li>
  );
}

function Namespaces({ overview, ns, onPick }: { overview: PolicyOverview; ns: string; onPick: (ns: string) => void }): ReactNode {
  const max = useMemo(() => Math.max(0, ...overview.namespaces.map((x) => policyTotal(x.counts))), [overview.namespaces]);
  return (
    <section aria-label="Namespaces" className="flex min-w-0 flex-col gap-2 rounded-lg border border-line bg-panel px-3.5 py-3">
      <h2 className="text-sm font-semibold">Namespaces</h2>
      <ul className="flex max-h-60 flex-col gap-0.5 overflow-y-auto" data-policy-namespaces="">
        {overview.namespaces.map((x) => (
          <li key={x.namespace}>
            <button
              type="button"
              data-pns={x.namespace}
              aria-pressed={ns === x.namespace}
              onClick={() => onPick(x.namespace)}
              title={`${x.namespace}\n${policyHint(x.counts)}`}
              className={`flex w-full items-center gap-2 rounded px-1.5 py-0.5 text-left text-xs ${ns === x.namespace ? 'bg-panel-2 ring-1 ring-accent' : 'hover:bg-panel-2'} ${focusRing}`}
            >
              <span className="w-56 shrink-0 truncate">{x.namespace}</span>
              <PolicyBar counts={x.counts} scale={scaleOf(policyTotal(x.counts), max)} />
              <span className="w-28">
                <OutcomeCounts counts={x.counts} />
              </span>
              <span className="w-10 text-right text-muted tabular-nums" title={`${n(x.resources)} resources`}>
                {n(x.resources)}
              </span>
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}

function EngineChip({ engine }: { engine: string }): ReactNode {
  return <span className="rounded-full border border-line px-1.5 text-2xs text-muted">{engine}</span>;
}

function ModeChip({ mode }: { mode: string }): ReactNode {
  if (mode === '') {
    return (
      <span className="text-muted" title="Mode not read">
        —
      </span>
    );
  }
  const lets = mode === 'Audit' || mode === 'dryrun' || mode === 'warn';
  return <span className={`rounded-full border px-1.5 text-2xs ${lets ? 'border-warn/50 text-warn' : 'border-line text-muted'}`}>{mode}</span>;
}

function Policies({ rows, total }: { rows: readonly PolicyRow[]; total: number }): ReactNode {
  const cut = total > rows.length;
  const [at, setAt] = useState(0);
  const { rows: shown, page, pages } = useMemo(() => pageOf(rows, at), [rows, at]);
  return (
    <section aria-label="Policies" className="flex min-w-0 flex-col gap-2 rounded-lg border border-line bg-panel px-3.5 py-3">
      <h2 className="text-sm font-semibold">
        Policies <span className="font-normal text-muted">{n(rows.length)}</span>
        {cut ? <span className="font-normal text-muted" title="Only the worst are listed"> / {n(total)}</span> : null}
      </h2>
      {rows.length === 0 ? (
        <p className="text-xs text-muted" data-none="">
          None
        </p>
      ) : (
        <>
          <div className="min-w-0 overflow-x-auto">
            <table className={`${TABLE} w-full`} data-policy-table="">
              <thead>
                <tr className={HEAD_ROW}>
                  <th className={`${headCell()} w-24`}>Engine</th>
                  <th className={headCell()}>Policy</th>
                  <th className={`${headCell()} w-20`}>Mode</th>
                  <th className={headCell()}>Category</th>
                  <th className={`${headCell()} w-24`}>Severity</th>
                  <th className={`${headCell()} w-28 text-right`} title="Fail · Error · Warn">
                    F · E · W
                  </th>
                  <th className={`${headCell()} w-20 text-right`}>Resources</th>
                </tr>
              </thead>
              <tbody>
                {shown.map((p) => (
                  <tr key={`${p.engine}/${p.name}`} className={`${ROW} hover:bg-panel-2`} data-policy-row={`${p.engine}/${p.name}`}>
                    <td className={bodyCell(true)}>
                      <EngineChip engine={p.engine} />
                    </td>
                    <td className={bodyCell(true)}>
                      <Link to={`?view=policies&policy=${encodeURIComponent(`${p.engine}/${p.name}`)}`} className={`text-accent hover:underline ${focusRing}`} title={p.kind}>
                        {p.name}
                      </Link>
                    </td>
                    <td className={bodyCell(true)}>
                      <ModeChip mode={p.mode} />
                    </td>
                    <td className={`${bodyCell(true)} text-xs text-muted`}>{p.category ?? ''}</td>
                    <td className={bodyCell(true)}>{p.severity === undefined || p.severity === '' ? <span className="text-muted">—</span> : <SevChip severity={p.severity} />}</td>
                    <td className={`${bodyCell(true)} text-xs`}>
                      <OutcomeCounts counts={p.counts} />
                    </td>
                    <td
                      className={`${bodyCell(true)} text-right text-xs tabular-nums`}
                      title={p.violations !== undefined && p.listed !== undefined && p.violations > p.listed ? `${n(p.listed)} / ${n(p.violations)} written` : undefined}
                    >
                      {n(p.resources)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <Pager page={page} pages={pages} count={rows.length} onPage={setAt} />
        </>
      )}
    </section>
  );
}

function Resources({ rows, total, cluster }: { rows: readonly PolicyResource[]; total: number; cluster: string }): ReactNode {
  const cut = total > rows.length;
  const [at, setAt] = useState(0);
  const { rows: shown, page, pages } = useMemo(() => pageOf(rows, at), [rows, at]);
  const max = useMemo(() => Math.max(0, ...shown.map((r) => policyTotal(r.counts))), [shown]);
  return (
    <section aria-label="Resources" className="flex min-w-0 flex-col gap-2 rounded-lg border border-line bg-panel px-3.5 py-3">
      <h2 className="text-sm font-semibold">
        Resources <span className="font-normal text-muted">{n(rows.length)}</span>
        {cut ? <span className="font-normal text-muted" title="Only the worst are listed"> / {n(total)}</span> : null}
      </h2>
      {rows.length === 0 ? (
        <p className="text-xs text-muted" data-none="">
          None
        </p>
      ) : (
        <>
          <div className="min-w-0 overflow-x-auto">
            <table className={`${TABLE} w-full`} data-resource-table="">
              <thead>
                <tr className={HEAD_ROW}>
                  <th className={`${headCell()} w-28`}>Namespace</th>
                  <th className={headCell()}>Resource</th>
                  <th className={`${headCell()} w-40`}>Results</th>
                  <th className={`${headCell()} w-28 text-right`} title="Fail · Error · Warn">
                    F · E · W
                  </th>
                  <th className={`${headCell()} w-20 text-right`}>Policies</th>
                </tr>
              </thead>
              <tbody>
                {shown.map((r) => (
                  <tr key={`${r.namespace ?? ''}/${r.kind}/${r.name}`} className={ROW} data-resource-row={`${r.namespace ?? ''}/${r.kind}/${r.name}`}>
                    <td className={`${bodyCell(true)} text-muted`}>{r.namespace ?? ''}</td>
                    <td className={bodyCell(true)}>
                      {r.workload === undefined ? (
                        <>
                          <span className="text-muted">{r.kind}/</span>
                          {r.name}
                        </>
                      ) : (
                        <Link to={`${workloadHref(cluster, r.workload)}?tab=policies`} className={`text-accent hover:underline ${focusRing}`} title={`${r.workload.kind}/${r.workload.name}`}>
                          <span className="text-muted">{r.kind}/</span>
                          {r.name}
                        </Link>
                      )}
                    </td>
                    <td className={bodyCell(true)}>
                      <PolicyBar counts={r.counts} scale={scaleOf(policyTotal(r.counts), max)} />
                    </td>
                    <td className={`${bodyCell(true)} text-xs`}>
                      <OutcomeCounts counts={r.counts} />
                    </td>
                    <td className={`${bodyCell(true)} text-right text-xs tabular-nums`}>{n(r.policies)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <Pager page={page} pages={pages} count={rows.length} onPage={setAt} />
        </>
      )}
    </section>
  );
}

/* -------------------------------------------------------------------------- */
/* One policy                                                                  */
/* -------------------------------------------------------------------------- */

function OnePolicy({ cluster, engine, name, row }: { cluster: string; engine: string; name: string; row: PolicyRow | undefined }): ReactNode {
  const query = useQuery(policyQuery(cluster, engine, name));
  const policy = query.data?.policy ?? row ?? null;
  return (
    <section aria-label={name} className="flex min-w-0 flex-col gap-2 rounded-lg border border-line bg-panel px-3.5 py-3" data-policy-detail={`${engine}/${name}`}>
      <PageHeader
        back={<BackArrow to="?view=policies" />}
        kind={policy?.kind ?? 'Policy'}
        name={name}
        badges={
          <>
            <EngineChip engine={engine} />
            {policy === null ? null : (
              <>
                <ModeChip mode={policy.mode} />
                {policy.severity === undefined || policy.severity === '' ? null : <SevChip severity={policy.severity} />}
              </>
            )}
          </>
        }
        actions={
          policy === null ? undefined : (
            <span className="w-28 text-xs">
              <OutcomeCounts counts={policy.counts} />
            </span>
          )
        }
        className=""
      />
      {query.data === undefined ? (
        query.isError ? (
          <Failure error={query.error} onRetry={() => void query.refetch()} />
        ) : (
          <Pending what="this policy's results" />
        )
      ) : (
        <PolicyResults results={query.data.results} total={query.data.total} showPolicy={false} />
      )}
    </section>
  );
}

/** A list of results, 50 at a time: the message is in the row's title. */
export function PolicyResults({ results, total, showPolicy }: { results: readonly PolicyResult[]; total: number; showPolicy: boolean }): ReactNode {
  const [at, setAt] = useState(0);
  const { rows, page, pages } = useMemo(() => pageOf(results, at), [results, at]);
  if (results.length === 0) {
    return (
      <p className="text-xs text-muted" data-none="">
        None
      </p>
    );
  }
  return (
    <>
      <div className="min-w-0 overflow-x-auto">
        <table className={`${TABLE} w-full`} data-policy-results="">
          <thead>
            <tr className={HEAD_ROW}>
              {showPolicy ? <th className={headCell()}>Policy</th> : null}
              <th className={headCell()}>Rule</th>
              <th className={`${headCell()} w-20`}>Result</th>
              <th className={`${headCell()} w-24`}>Severity</th>
              {showPolicy ? null : <th className={`${headCell()} w-28`}>Namespace</th>}
              {showPolicy ? null : <th className={headCell()}>Resource</th>}
            </tr>
          </thead>
          <tbody>
            {rows.map((r, i) => (
              <tr key={`${r.policy}/${r.rule ?? ''}/${r.namespace ?? ''}/${r.kind}/${r.name}/${String(i)}`} className={ROW} title={r.message} data-policy-result={r.result}>
                {showPolicy ? (
                  <td className={bodyCell(true)}>
                    <span className="text-muted">{r.engine} · </span>
                    {r.policy}
                  </td>
                ) : null}
                <td className={`${bodyCell(true)} text-xs`}>{r.rule ?? '—'}</td>
                <td className={bodyCell(true)}>
                  <span className={`text-xs ${r.result === 'error' ? SEV.critical.text : r.result === 'fail' ? SEV.high.text : SEV.medium.text}`}>
                    {r.result}
                    {r.audit === true ? <span className="text-muted"> · audit</span> : null}
                  </span>
                </td>
                <td className={bodyCell(true)}>{r.severity === undefined || r.severity === '' ? <span className="text-muted">—</span> : <SevChip severity={r.severity} />}</td>
                {showPolicy ? null : <td className={`${bodyCell(true)} text-muted`}>{r.namespace ?? ''}</td>}
                {showPolicy ? null : (
                  <td className={bodyCell(true)}>
                    <span className="text-muted">{r.kind}/</span>
                    {r.name}
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <Pager page={page} pages={pages} count={results.length} onPage={setAt} />
      {total > results.length ? (
        <span className="text-2xs text-muted" data-capped="">
          {n(results.length)} / {n(total)}
        </span>
      ) : null}
    </>
  );
}
