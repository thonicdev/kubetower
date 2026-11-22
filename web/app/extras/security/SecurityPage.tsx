import { keepPreviousData, useIsFetching, useQuery, useQueryClient } from '@tanstack/react-query';
import { memo, useMemo, useState, type ReactNode } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router';
import {
  Failure,
  HEAD_ROW,
  Pending,
  ROW,
  TABLE,
  bodyCell,
  btnSecondary,
  focusRing,
  headCell,
  useDebounced,
  useDeclarePageFacts,
  useUrlParameter,
  Windowed,
} from '@kubetower/ui';

import {
  ago,
  complianceQuery,
  rbacQuery,
  configKey,
  configQuery,
  filterWorkloads,
  n,
  overviewQuery,
  pageOf,
  tileOf,
  total,
  withTile,
  workloadHref,
  type Counts,
  type Overview,
  type Tile,
  type Workload,
} from './data';
import { ClusterReports, RbacReports } from './ClusterReports';
import { ComplianceView } from './ComplianceView';
import { PoliciesView } from './PoliciesView';
import { policiesQuery } from './policyData';
import { Install } from './Install';
import { Pager, SEV, SevBar, SevCounts, SourcePills, hint, scaleOf, tabLook } from './parts';
import { ComponentsCell, SignatureCell, supplySources } from './SbomCells';

/**
 * Security Center, at `/c/:cluster/x/security`: what trivy-operator found,
 * read top-down - the sources, the tiles that filter, the namespaces and the
 * images as bars, then the workloads, worst first, 50 at a time.
 *
 * Three views (`?view=`): the workloads, with the cluster's own reports
 * below them; RBAC; Compliance. Each is read only while it is open.
 *
 * The address carries every filter (`?sev=`, `?images=1`, `?exposed=1`,
 * `?secrets=1`, `?image=`, `?ns=`, `?q=`), written in place. `ns` scopes the
 * read itself; the others filter the rows in hand.
 *
 * One read per scope, no interval.
 */
export function SecurityPage(): ReactNode {
  const { cluster = '' } = useParams<{ cluster: string }>();
  const [params, setParams] = useSearchParams();
  const ns = params.get('ns') ?? '';
  const view = viewOf(params.get('view'));
  useDeclarePageFacts({ title: 'Security Center' });
  const query = useQuery({ ...overviewQuery(cluster, ns), placeholderData: keepPreviousData, enabled: view === 'workloads' });
  const client = useQueryClient();
  const data = query.data;
  const other =
    view === 'workloads'
      ? undefined
      : view === 'rbac'
        ? rbacQuery(cluster)
        : view === 'policies'
          ? policiesQuery(cluster, params.get('pns') ?? '')
          : complianceQuery(cluster);
  const fetching = useIsFetching(other === undefined ? { queryKey: ['__none__'] } : { queryKey: other.queryKey }) > 0;
  const busy = view === 'workloads' ? query.isFetching : fetching;
  const again = () => {
    if (other === undefined) void query.refetch();
    else void client.refetchQueries({ queryKey: other.queryKey });
  };
  const open = (v: View) => {
    const out = new URLSearchParams();
    if (v !== 'workloads') out.set('view', v);
    setParams(out, { replace: true });
  };

  return (
    <div className="flex flex-col gap-4 p-4" data-security-page="" aria-busy={busy}>
      <header className="flex flex-wrap items-center gap-3">
        <h1 className="text-xl font-medium whitespace-nowrap">Security Center</h1>
        <nav aria-label="Views" className="flex gap-1" data-views="">
          {VIEWS.map(([v, label]) => (
            <button
              key={v}
              type="button"
              data-view={v}
              aria-pressed={view === v}
              onClick={() => open(v)}
              className={`${tabLook(view === v)} ${focusRing}`}
            >
              {label}
            </button>
          ))}
        </nav>
        {view !== 'workloads' || data === undefined || !data.operator ? null : <SourcePills sources={[...data.sources, ...supplySources(data)]} />}
        <button type="button" className={`${btnSecondary} ml-auto`} onClick={again} disabled={busy}>
          {busy ? 'Reading…' : 'Read again'}
        </button>
      </header>
      {view === 'policies' ? (
        <PoliciesView cluster={cluster} />
      ) : view === 'rbac' ? (
        <RbacReports cluster={cluster} />
      ) : view === 'compliance' ? (
        <ComplianceView cluster={cluster} />
      ) : data === undefined ? (
        query.isError ? (
          <Failure error={query.error} onRetry={() => void query.refetch()} />
        ) : (
          <Pending what="trivy-operator's reports" />
        )
      ) : (
        <>
          {query.isError ? <Failure error={query.error} onRetry={() => void query.refetch()} /> : null}
          {data.operator ? (
            <Board overview={data} cluster={cluster} />
          ) : (
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              <p
                data-operator="absent"
                title="No aquasecurity.github.io resource is served on this cluster"
                className="self-start rounded-full border border-line px-3 py-1 text-sm text-muted"
              >
                trivy-operator absent
              </p>
              {/* The console's own install route, linked: this page writes nothing. */}
              <Install cluster={cluster} onInstalled={() => void query.refetch()} />
            </div>
          )}
        </>
      )}
    </div>
  );
}

type View = 'workloads' | 'policies' | 'rbac' | 'compliance';
const VIEWS: ReadonlyArray<readonly [View, string]> = [
  ['workloads', 'Workloads'],
  ['policies', 'Policies'],
  ['rbac', 'RBAC'],
  ['compliance', 'Compliance'],
];
const viewOf = (v: string | null): View => (v === 'rbac' || v === 'compliance' || v === 'policies' ? v : 'workloads');

function Board({ overview, cluster }: { overview: Overview; cluster: string }): ReactNode {
  const [params, setParams] = useSearchParams();
  const tile = tileOf(params);
  const image = params.get('image') ?? '';
  const ns = params.get('ns') ?? '';
  const [typed, setTyped] = useUrlParameter('q');
  const q = useDebounced(typed, 200);

  const set = (next: URLSearchParams) => setParams(next, { replace: true });
  const toggle = (name: string, value: string) => {
    const out = new URLSearchParams(params);
    if (out.get(name) === value) out.delete(name);
    else out.set(name, value);
    set(out);
  };
  const press = (t: Tile) => set(withTile(params, tile === t ? '' : t));

  const rows = useMemo(() => filterWorkloads(overview.workloads, { tile, image, q }), [overview.workloads, tile, image, q]);

  return (
    <div className="flex flex-col gap-4">
      <Tiles overview={overview} tile={tile} onPress={press} />
      <div className="grid gap-4 xl:grid-cols-2">
        <Namespaces overview={overview} ns={ns} onPick={(v) => toggle('ns', v)} />
        <Images overview={overview} image={image} all={tile === 'images'} onPick={(v) => toggle('image', v)} />
      </div>
      <section aria-label="Workloads" className="flex min-w-0 flex-col gap-2 rounded-lg border border-line bg-panel px-3.5 py-3">
        <div className="flex flex-wrap items-center gap-2">
          <h2 className="text-sm font-semibold">
            Workloads <span className="font-normal text-muted" data-shown="">{n(rows.length)}</span>
          </h2>
          {overview.capped ? (
            <span data-capped="" className="rounded-full border border-warn/45 px-2 text-2xs text-warn" title={`The ${n(overview.workloads.length)} worst of ${n(overview.total)}; filter by namespace for the rest`}>
              {n(overview.workloads.length)} / {n(overview.total)}
            </span>
          ) : null}
          {ns === '' ? null : <Chip label={`ns ${ns}`} onClear={() => toggle('ns', ns)} />}
          {image === '' ? null : <Chip label={image} onClear={() => toggle('image', image)} />}
          <input
            type="search"
            aria-label="Filter workloads"
            placeholder="Filter"
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            className={`ml-auto w-56 rounded-md border border-line bg-bg px-2 py-1 text-xs ${focusRing}`}
          />
        </div>
        <WorkloadTable key={`${tile}|${image}|${q}|${ns}`} rows={rows} cluster={cluster} read={overview.read} />
      </section>
      <ClusterReports cluster={cluster} />
    </div>
  );
}

function Chip({ label, onClear }: { label: string; onClear: () => void }): ReactNode {
  return (
    <button type="button" onClick={onClear} data-chip="" className={`flex max-w-72 items-center gap-1 rounded-full border border-accent/50 px-2 text-2xs ${focusRing}`} title="Remove the filter">
      <span className="truncate">{label}</span>
      <span aria-hidden="true">×</span>
    </button>
  );
}

/* -------------------------------------------------------------------------- */
/* Tiles                                                                       */
/* -------------------------------------------------------------------------- */

function Tiles({ overview, tile, onPress }: { overview: Overview; tile: Tile; onPress: (t: Tile) => void }): ReactNode {
  const o = overview;
  const one = (t: Tile, count: number, label: string, legend: string, bar: string, title: string) => {
    const on = tile === t;
    return (
      <li key={t} className="min-w-0">
        <button
          type="button"
          data-tile={t}
          data-count={count}
          aria-pressed={on}
          title={title}
          onClick={() => onPress(t)}
          className={`flex w-full min-w-0 flex-col items-start rounded-md border border-l-4 px-3 py-1.5 text-left ${count === 0 ? 'border-l-line' : bar} ${
            on ? 'border-accent bg-panel-2 ring-1 ring-accent' : 'border-line bg-panel hover:bg-panel-2'
          } ${focusRing}`}
        >
          <span className="flex items-baseline gap-2">
            <span className={`text-2xl font-semibold tabular-nums ${count === 0 ? 'text-muted' : ''}`}>{n(count)}</span>
            <span className="text-2xs tracking-wide text-muted uppercase">{label}</span>
          </span>
          <span className="text-2xs text-muted" data-legend="">
            {legend}
          </span>
        </button>
      </li>
    );
  };
  const tiles = 6 + (o.signatures === undefined || o.signatures.state === 'absent' ? 0 : 2);
  return (
    <ul aria-label="Filter the workloads" className={`grid grid-cols-2 gap-2 ${tiles % 4 === 0 ? 'md:grid-cols-4' : 'md:grid-cols-3'}`} data-tiles="">
      {one('critical', o.totals.critical, 'Critical', `${n(o.affected.critical)} workloads`, 'border-l-bad', 'Critical CVEs, each counted once per image; workloads with at least one')}
      {one('high', o.totals.high, 'High', `${n(o.affected.high)} workloads`, 'border-l-y-tag', 'High CVEs, each counted once per image; workloads with at least one')}
      {one('medium', o.totals.medium, 'Medium', `${n(o.affected.medium)} workloads`, 'border-l-warn', 'Medium CVEs, each counted once per image; workloads with at least one')}
      {one('images', o.imageCount, 'Images', 'high or critical', 'border-l-bad', 'Distinct images with at least one high or critical CVE')}
      {one('exposed', o.exposedCount, 'Exposed', 'high or critical', 'border-l-bad', 'Workloads reachable through a LoadBalancer, a NodePort, an Ingress, a Gateway API route or the host network, with at least one high or critical CVE')}
      {one('secrets', o.secretCount, 'Secrets', 'in images', 'border-l-bad', 'Workloads whose image holds an exposed secret')}
      {/* The admission's verdicts, where a pod carries one: never a tile of the unsigned. */}
      {o.signatures === undefined || o.signatures.state === 'absent' ? null : (
        <>
          {one('unsigned', o.signatureFailed ?? 0, 'Signature failed', 'workloads', 'border-l-bad', "Workloads with an image the admission's verification failed")}
          {one('verified', o.verified ?? 0, 'Verified', 'workloads', 'border-l-ok', "Workloads whose every image the admission's verification passed")}
        </>
      )}
    </ul>
  );
}

/* -------------------------------------------------------------------------- */
/* Namespaces and images                                                       */
/* -------------------------------------------------------------------------- */

const severity = (c: Counts) => c.critical + c.high + c.medium + c.low;

function Namespaces({ overview, ns, onPick }: { overview: Overview; ns: string; onPick: (ns: string) => void }): ReactNode {
  const max = useMemo(() => Math.max(0, ...overview.namespaces.map((x) => severity(x.counts))), [overview.namespaces]);
  return (
    <section aria-label="Namespaces" className="flex min-w-0 flex-col gap-2 rounded-lg border border-line bg-panel px-3.5 py-3">
      <h2 className="text-sm font-semibold">Namespaces</h2>
      {overview.namespaces.length === 0 ? <p className="text-xs text-muted">None</p> : null}
      <ul className="flex max-h-72 flex-col gap-0.5 overflow-y-auto" data-namespaces="">
        <Windowed items={overview.namespaces} more={(button) => <li className="text-center">{button}</li>}>
          {(page) =>
            page.map((x) => (
              <li key={x.namespace}>
                <BarRow
                  data={{ 'data-ns': x.namespace }}
                  label={x.namespace}
                  counts={x.counts}
                  scale={scaleOf(severity(x.counts), max)}
                  pressed={ns === x.namespace}
                  aside={`${n(x.workloads)}`}
                  asideTitle={`${n(x.workloads)} workloads`}
                  onClick={() => onPick(x.namespace)}
                />
              </li>
            ))
          }
        </Windowed>
      </ul>
    </section>
  );
}

function Images({ overview, image, all, onPick }: { overview: Overview; image: string; all: boolean; onPick: (image: string) => void }): ReactNode {
  const shown = all ? overview.images : overview.images.slice(0, 10);
  const max = useMemo(() => Math.max(0, ...overview.images.map((x) => severity(x.counts))), [overview.images]);
  return (
    <section aria-label="Images" className="flex min-w-0 flex-col gap-2 rounded-lg border border-line bg-panel px-3.5 py-3">
      <h2 className="text-sm font-semibold">
        Images <span className="font-normal text-muted">{n(shown.length)}</span>
      </h2>
      {shown.length === 0 ? <p className="text-xs text-muted">None</p> : null}
      <ul className="flex max-h-72 flex-col gap-0.5 overflow-y-auto" data-images="">
        <Windowed items={shown} more={(button) => <li className="text-center">{button}</li>}>
          {(page) =>
            page.map((x) => (
              <li key={x.image}>
                <BarRow
                  data={{ 'data-image': x.image }}
                  label={x.image}
                  counts={x.counts}
                  scale={scaleOf(severity(x.counts), max)}
                  pressed={image === x.image}
                  aside={`×${n(x.workloads)}`}
                  asideTitle={`${n(x.workloads)} workloads`}
                  onClick={() => onPick(x.image)}
                />
              </li>
            ))
          }
        </Windowed>
      </ul>
    </section>
  );
}

function BarRow({
  data,
  label,
  counts,
  scale,
  pressed,
  aside,
  asideTitle,
  onClick,
}: {
  data: Readonly<Record<`data-${string}`, string>>;
  label: string;
  counts: Counts;
  scale: number;
  pressed: boolean;
  aside: string;
  asideTitle: string;
  onClick: () => void;
}): ReactNode {
  return (
    <button
      type="button"
      {...data}
      aria-pressed={pressed}
      onClick={onClick}
      title={`${label}\n${hint(counts)}`}
      className={`flex w-full items-center gap-2 rounded px-1.5 py-0.5 text-left text-xs ${
        pressed ? 'bg-panel-2 ring-1 ring-accent' : 'hover:bg-panel-2'
      } ${focusRing}`}
    >
      <span className="w-56 shrink-0 truncate" title={label}>{label}</span>
      <SevBar counts={counts} scale={scale} />
      <span className="flex gap-1.5 tabular-nums">
        <span className={counts.critical === 0 ? 'text-muted/50' : SEV.critical.text}>{n(counts.critical)}</span>
        <span className={counts.high === 0 ? 'text-muted/50' : SEV.high.text}>{n(counts.high)}</span>
      </span>
      <span className="w-10 text-right text-muted tabular-nums" title={asideTitle}>
        {aside}
      </span>
    </button>
  );
}

/** A row's failed checks: each check ID once, as the workload's page lists them. */
function ConfigCell({ audited, checks, failed }: { audited: boolean; checks: Counts | null | undefined; failed: boolean }): ReactNode {
  if (!audited) {
    return (
      <span className="text-muted" title="No config audit">
        —
      </span>
    );
  }
  if (checks === undefined && !failed) {
    return (
      <span data-config-pending="" className="text-muted/50" title="Reading">
        ·
      </span>
    );
  }
  if (checks === undefined || checks === null) {
    return (
      <span data-config-unread="" className="text-muted" title="Config checks not read">
        —
      </span>
    );
  }
  const count = total(checks);
  return (
    <span data-config={count} title={`Failed checks: ${hint(checks)}`} className={checks.critical + checks.high > 0 ? 'text-bad' : count > 0 ? 'text-warn' : 'text-ok'}>
      {n(count)}
    </span>
  );
}

/* -------------------------------------------------------------------------- */
/* The workloads                                                               */
/* -------------------------------------------------------------------------- */

/** The table, one page at a time; remounted (page 1) whenever a filter moves. */
function WorkloadTable({ rows, cluster, read }: { rows: readonly Workload[]; cluster: string; read: string }): ReactNode {
  const [at, setAt] = useState(0);
  const { rows: shown, page, pages } = useMemo(() => pageOf(rows, at), [rows, at]);
  const max = useMemo(() => Math.max(0, ...shown.map((w) => severity(w.counts))), [shown]);
  const audited = useMemo(() => shown.filter((w) => w.config !== null), [shown]);
  const config = useQuery({ ...configQuery(cluster, audited), enabled: audited.length > 0 });
  if (rows.length === 0) return <p className="text-xs text-muted" data-none="">None</p>;
  return (
    <>
      {/* The table's own width decides which columns show: a narrow one drops
          the namespace, the image and the scan's age, which the workload's
          title still carries, and narrows the severity bar. */}
      <div className="@container min-w-0 overflow-x-auto">
        <table className={`${TABLE} w-full`} data-workloads="">
          <thead>
            <tr className={HEAD_ROW}>
              <th className={`${headCell()} ${WIDE} w-28`}>Namespace</th>
              <th className={headCell()}>Workload</th>
              <th className={`${headCell()} ${WIDER}`}>Image</th>
              <th className={`${headCell()} w-28 @6xl:w-40`}>Severity</th>
              <th className={`${headCell()} w-40 text-right`} title="Critical · High · Medium · Low">
                C · H · M · L
              </th>
              <th className={`${headCell()} w-16`}>Config</th>
              <th className={`${headCell()} w-16`}>Secrets</th>
              <th className={`${headCell()} w-20`}>Exposed</th>
              <th className={`${headCell()} w-20`}>Signature</th>
              <th className={`${headCell()} w-24 text-right`}>Components</th>
              <th className={`${headCell()} ${WIDE} w-16 text-right`}>Scan</th>
            </tr>
          </thead>
          <tbody>
            {shown.map((w) => (
              <WorkloadRow
                key={`${w.namespace}/${w.kind}/${w.name}`}
                w={w}
                cluster={cluster}
                read={read}
                max={max}
                checks={config.data?.[configKey(w)]}
                checksFailed={config.isError}
              />
            ))}
          </tbody>
        </table>
      </div>
      <Pager page={page} pages={pages} count={rows.length} onPage={setAt} />
    </>
  );
}

/** Columns shown only where the table is at least 64rem, or 80rem, wide. */
const WIDE = 'hidden @5xl:table-cell';
const WIDER = 'hidden @7xl:table-cell';

const WorkloadRow = memo(function WorkloadRow({
  w,
  cluster,
  read,
  max,
  checks,
  checksFailed,
}: {
  w: Workload;
  cluster: string;
  read: string;
  max: number;
  /** The distinct failed checks; undefined while read, null where none could be. */
  checks: Counts | null | undefined;
  checksFailed: boolean;
}): ReactNode {
  const navigate = useNavigate();
  const href = workloadHref(cluster, w);
  const scanned = w.scanned !== undefined;
  const sum = total(w.counts);
  const first = w.images[0] ?? '';
  return (
    <tr data-workload-row={`${w.namespace}/${w.kind}/${w.name}`} className={`${ROW} cursor-pointer hover:bg-panel-2`} onClick={() => void navigate(href)}>
      <td className={`${bodyCell(true)} ${WIDE} text-muted`}>{w.namespace}</td>
      <td className={`${bodyCell(true)} max-w-72 truncate`}>
        <Link
          to={href}
          title={[`${w.namespace}/${w.kind}/${w.name}`, ...w.images].join('\n')}
          className={`text-accent hover:underline ${focusRing}`}
          onClick={(e) => e.stopPropagation()}
        >
          <span className="text-muted">{w.kind}/</span>
          {w.name}
        </Link>
      </td>
      <td className={`${bodyCell(true)} ${WIDER} max-w-72 truncate text-xs`} title={w.images.join('\n')}>
        {first}
        {w.images.length > 1 ? <span className="text-muted"> +{w.images.length - 1}</span> : null}
      </td>
      <td className={bodyCell(true)}>
        {!scanned ? (
          <span data-unscanned="" className="text-xs text-muted" title="No vulnerability report covers this workload">
            Not scanned
          </span>
        ) : sum === 0 ? (
          <span data-clean="" className="text-xs text-ok">
            Clean
          </span>
        ) : (
          <SevBar counts={w.counts} scale={scaleOf(severity(w.counts), max)} />
        )}
      </td>
      <td className={`${bodyCell(true)} text-right text-xs`}>{scanned ? <SevCounts counts={w.counts} /> : <span className="text-muted">—</span>}</td>
      <td className={`${bodyCell(true)} text-xs tabular-nums`}>
        <ConfigCell audited={w.config !== null} checks={checks} failed={checksFailed} />
      </td>
      <td className={`${bodyCell(true)} text-xs tabular-nums`}>
        {w.secrets === null ? (
          <span className="text-muted" title="No exposed-secret report">
            —
          </span>
        ) : (
          <span className={w.secrets > 0 ? 'text-bad' : 'text-muted'}>{n(w.secrets)}</span>
        )}
      </td>
      <td className={`${bodyCell(true)} text-xs`}>
        {w.exposed ? (
          <span data-exposed="" className="rounded-full border border-warn/50 px-1.5 text-2xs text-warn" title={w.exposure}>
            Exposed
          </span>
        ) : null}
      </td>
      <td className={`${bodyCell(true)} text-xs`}>
        <SignatureCell signature={w.signature} />
      </td>
      <td className={`${bodyCell(true)} text-right text-xs tabular-nums`}>
        <ComponentsCell components={w.components} />
      </td>
      <td className={`${bodyCell(true)} ${WIDE} text-right text-xs tabular-nums text-muted`} title={w.scanned}>
        {w.scanned === undefined ? '—' : ago(w.scanned, read)}
      </td>
    </tr>
  );
});
