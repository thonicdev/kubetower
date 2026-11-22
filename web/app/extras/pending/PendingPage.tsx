import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useMemo, useState, type ReactNode } from 'react';
import { Link, useParams, useSearchParams } from 'react-router';
import { BackArrow, Failure, PageHeader, Pending as Waiting, btnSecondary, focusRing, useDeclarePageFacts } from '@kubetower/ui';

import {
  CATEGORIES,
  CATEGORY_HINT,
  CATEGORY_LABEL,
  PAGE_ROWS,
  ago,
  format,
  pageQuery,
  podHref,
  type Category,
  type Failure as FailureData,
  type PageAnswer,
  type Row,
} from './data';
import { CATEGORY_DOT, CategoryChip, NodeBars, QuotaBar, SectionNote } from './parts';
import { leadReason } from './reasons';
import { PodView } from './PodView';

/** The tile that shows the workloads whose pods could not be created. */
const CREATE = 'create';
type Filter = Category | typeof CREATE | '';

/**
 * Pending, at `/c/:cluster/x/pending`: every pod that waits, filed by why.
 *
 * **Read top-down**: tiles that count each reason and filter the list; the
 * pods, longest waiting first, each with its reason and the node that comes
 * closest; the nodes' room, with the picked pod's request laid over it; the
 * workloads that cannot even create their pods (a quota refuses a pod before
 * it exists, so it is never Pending). The address carries `cat` and `ns`, so a
 * link lands on the same view.
 *
 * Nothing here changes a cluster.
 */
export function PendingPage(): ReactNode {
  const { cluster = '' } = useParams<{ cluster: string }>();
  useDeclarePageFacts({ title: 'Pod Debugging' });
  const [params, setParams] = useSearchParams();
  const filter = (params.get('cat') ?? '') as Filter;
  const ns = params.get('namespace') ?? params.get('ns') ?? '';
  const query = useQuery({ ...pageQuery(cluster, ''), placeholderData: keepPreviousData });
  const page = query.data;
  const set = (name: string, value: string) =>
    setParams(
      (current) => {
        const out = new URLSearchParams(current);
        if (name === 'namespace') out.delete('ns');
        if (value === '') out.delete(name);
        else out.set(name, value);
        return out;
      },
      { replace: true },
    );

  return (
    <div id="pending" className="flex flex-col gap-4 p-4" aria-busy={query.isFetching}>
      <header className="flex flex-wrap items-center gap-3">
        <h1 className="text-xl font-semibold">Pod Debugging</h1>
        <span className="ml-auto flex items-center gap-2">
          {page === undefined ? null : <NamespacePicker page={page} ns={ns} onPick={(v) => set('namespace', v)} />}
          <button type="button" className={btnSecondary} onClick={() => void query.refetch()} disabled={query.isFetching}>
            {query.isFetching ? 'Reading…' : 'Read again'}
          </button>
        </span>
      </header>
      {page === undefined ? (
        query.isError ? (
          <Failure error={query.error} onRetry={() => void query.refetch()} />
        ) : (
          <Waiting what="the pods that wait" />
        )
      ) : (
        <>
          {query.isError ? <Failure error={query.error} onRetry={() => void query.refetch()} /> : null}
          <Body cluster={cluster} page={page} filter={filter} ns={ns} onFilter={(v) => set('cat', v)} />
        </>
      )}
    </div>
  );
}

function NamespacePicker({ page, ns, onPick }: { page: PageAnswer; ns: string; onPick: (ns: string) => void }): ReactNode {
  const namespaces = useMemo(() => [...new Set([...page.rows.map((r) => r.namespace), ...page.failures.map((f) => f.namespace)])].sort(), [page]);
  return (
    <select
      aria-label="Namespace"
      value={ns}
      onChange={(e) => onPick(e.target.value)}
      className={`rounded-md border border-line bg-panel px-2 py-1 text-xs ${focusRing}`}
    >
      <option value="">All namespaces</option>
      {namespaces.map((n) => (
        <option key={n} value={n}>
          {n}
        </option>
      ))}
    </select>
  );
}

function Body({ cluster, page, filter, ns, onFilter }: { cluster: string; page: PageAnswer; filter: Filter; ns: string; onFilter: (f: string) => void }): ReactNode {
  const [picked, setPicked] = useState('');
  const inScope = useMemo(() => (ns === '' ? page.rows : page.rows.filter((r) => r.namespace === ns)), [page.rows, ns]);
  const failures = useMemo(() => (ns === '' ? page.failures : page.failures.filter((f) => f.namespace === ns)), [page.failures, ns]);
  const counts = useMemo(() => {
    const out: Partial<Record<Category, number>> = {};
    for (const r of inScope) out[r.main] = (out[r.main] ?? 0) + 1;
    return out;
  }, [inScope]);
  const rows = filter === '' || filter === CREATE ? inScope : inScope.filter((r) => r.main === filter);
  const pickedRow = inScope.find((r) => `${r.namespace}/${r.name}` === picked);
  const nothing = page.total === 0 && page.failures.length === 0 && page.pods.state === 'read';

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-1">
        <SectionNote what="Pods" section={page.pods} />
        {page.capped ? <p className="text-xs text-warn">First {page.total} pending pods only</p> : null}
        <SectionNote what="Nodes" section={page.nodes} />
        <SectionNote what="Pods on the nodes" section={page.placed} />
        <SectionNote what="Workloads that cannot create pods" section={page.failuresRead} />
      </div>

      {nothing ? (
        <section data-nothing-pending="" className="flex items-center gap-3 rounded-lg border border-ok/40 bg-ok/10 px-3.5 py-3 text-sm">
          <span className="text-ok">✓</span>
          <span>{page.pods.complete ? 'Nothing pending' : 'Nothing pending in the namespaces you may read'}</span>
        </section>
      ) : (
        <>
          <Tiles counts={counts} failures={failures.length} filter={filter} onFilter={onFilter} total={inScope.length} />
          {filter === CREATE ? (
            <Failures failures={failures} />
          ) : (
            <div className="grid gap-4 xl:grid-cols-[3fr_2fr]">
              <PodList cluster={cluster} rows={rows} read={page.read} picked={picked} onPick={setPicked} />
              <section aria-label="Nodes" className="flex min-w-0 flex-col gap-2 rounded-lg border border-line bg-panel px-3.5 py-3">
                <h2 className="flex items-baseline gap-2 text-sm font-semibold" title="Requested against allocatable; a picked pod's request is laid over each node, green where it fits, red where not">
                  Room on the nodes
                  {pickedRow === undefined ? null : (
                    <span data-picked="" className="truncate text-2xs font-normal text-muted">
                      + {pickedRow.name}: {format('cpu', pickedRow.request['cpu'] ?? 0)} CPU, {format('memory', pickedRow.request['memory'] ?? 0)}
                    </span>
                  )}
                </h2>
                {page.nodes.state === 'read' ? (
                  <NodeBars bars={page.bars} {...(pickedRow === undefined ? {} : { request: pickedRow.request })} />
                ) : (
                  <p className="text-xs text-warn">Nodes not readable</p>
                )}
              </section>
            </div>
          )}
          {filter !== CREATE && failures.length > 0 ? <Failures failures={failures} /> : null}
        </>
      )}
    </div>
  );
}

function Tiles({
  counts,
  failures,
  filter,
  onFilter,
  total,
}: {
  counts: Partial<Record<Category, number>>;
  failures: number;
  filter: Filter;
  onFilter: (f: string) => void;
  total: number;
}): ReactNode {
  const shown = CATEGORIES.filter((c) => (counts[c] ?? 0) > 0);
  const tile = (key: Filter, count: number, label: string, hint: string, dot: string) => {
    const on = filter === key;
    return (
      <li key={key || 'all'} className="min-w-0">
        <button
          type="button"
          data-tile={key || 'all'}
          data-count={count}
          aria-pressed={on}
          title={hint}
          onClick={() => onFilter(on ? '' : key)}
          className={`flex w-full items-center gap-2 rounded-md border px-3 py-1.5 ${on ? 'border-accent bg-panel-2' : 'border-line bg-panel hover:bg-panel-2'} ${focusRing}`}
        >
          {dot === '' ? null : <span className={`size-2.5 rounded-full ${dot}`} />}
          <span className={`text-2xl font-semibold tabular-nums ${count === 0 ? 'text-muted' : ''}`}>{count}</span>
          <span className="text-xs text-muted">{label}</span>
        </button>
      </li>
    );
  };
  return (
    <ul aria-label="Why pods wait" className="grid grid-cols-2 gap-2 md:grid-cols-4 xl:grid-cols-6">
      {tile('', total, 'Pending', 'Every pending pod', '')}
      {shown.map((c) => tile(c, counts[c] ?? 0, CATEGORY_LABEL[c], CATEGORY_HINT[c], CATEGORY_DOT[c]))}
      {failures > 0 ? tile(CREATE, failures, 'Cannot create pods', 'Workloads whose pods were refused before they existed: quota, admission policy', 'bg-bad') : null}
    </ul>
  );
}

/**
 * The pods, PAGE_ROWS at a time: a cluster with thousands waiting draws one
 * screen of rows, never all of them.
 */
function PodList({ cluster, rows, read, picked, onPick }: { cluster: string; rows: readonly Row[]; read: string; picked: string; onPick: (key: string) => void }): ReactNode {
  const [pages, setPages] = useState(1);
  const shown = rows.slice(0, pages * PAGE_ROWS);
  return (
    <section aria-label="Pending pods" className="flex min-w-0 flex-col gap-2 rounded-lg border border-line bg-panel px-3.5 py-3">
      <h2 className="text-sm font-semibold">
        Pods <span className="font-normal text-muted">{rows.length}</span>
      </h2>
      {rows.length === 0 ? <p className="text-xs text-muted">None</p> : null}
      <ul className="flex flex-col divide-y divide-line">
        {shown.map((r) => {
          const key = `${r.namespace}/${r.name}`;
          const on = key === picked;
          return (
            <li key={key} data-row={key} data-main={r.main} className={`flex flex-col gap-1 py-2 ${on ? 'bg-panel-2' : ''}`}>
              <div className="flex flex-wrap items-center gap-2" title={r.stage === 'gated' ? r.message : undefined}>
                <button
                  type="button"
                  aria-pressed={on}
                  title="Lay its request over the nodes"
                  onClick={() => onPick(on ? '' : key)}
                  className={`size-3 shrink-0 rounded-full border ${on ? 'border-accent bg-accent' : 'border-line'} ${focusRing}`}
                />
                <Link to={podHref(cluster, r.namespace, r.name)} className={`min-w-0 truncate text-sm text-accent hover:underline ${focusRing}`}>
                  <span className="text-muted">{r.namespace}/</span>
                  {r.name}
                </Link>
                <CategoryChip category={r.main} main />
                <span className="ml-auto shrink-0 text-2xs tabular-nums text-muted">{ago(r.since ?? r.created, read)}</span>
              </div>
              {r.stage === 'gated' ? null : (
                <p className="truncate pl-5 text-xs text-muted" title={r.message}>
                  {summary(r)}
                </p>
              )}
              {r.closest === undefined ? null : (
                <p className="pl-5 text-2xs text-muted">
                  closest <span className="text-fg">{r.closest.node}</span> · {r.closest.failing} failed
                </p>
              )}
            </li>
          );
        })}
      </ul>
      {rows.length > shown.length ? (
        <button type="button" className={`self-start text-xs text-accent underline ${focusRing}`} onClick={() => setPages((p) => p + 1)}>
          Show {Math.min(PAGE_ROWS, rows.length - shown.length)} more of {rows.length - shown.length}
        </button>
      ) : null}
    </section>
  );
}

/** The reason in one line: the most significant reason with its count. */
function summary(r: Row): string {
  const first = leadReason(r.verdict.reasons);
  if (r.stage !== 'unschedulable' || first === undefined) return r.message;
  const more = (r.verdict.reasons?.length ?? 1) - 1;
  const nodes = first.nodes < 0 ? '' : r.verdict.total > 0 ? ` (${first.nodes}/${r.verdict.total} nodes)` : '';
  return `${first.text}${nodes}${more > 0 ? ` + ${more} more` : ''}`;
}

function Failures({ failures }: { failures: readonly FailureData[] }): ReactNode {
  return (
    <section aria-label="Workloads that cannot create pods" className="flex flex-col gap-2 rounded-lg border border-bad/40 bg-panel px-3.5 py-3">
      <h2 className="text-sm font-semibold" title="Pods refused before they existed, from the FailedCreate events of the last hour">
        Workloads that cannot create pods
      </h2>
      <ul className="flex flex-col gap-2">
        {failures.slice(0, PAGE_ROWS).map((f) => (
          <li key={`${f.namespace}/${f.kind}/${f.name}`} data-failure={f.category} className="flex flex-col gap-1 border-t border-line pt-2 text-xs">
            <div className="flex flex-wrap items-baseline gap-2">
              <span className="font-semibold">
                <span className="text-muted">
                  {f.namespace}/{f.kind}/
                </span>
                {f.name}
              </span>
              <span className="rounded-full border border-bad/50 px-2 text-2xs text-bad">{f.category}</span>
              <span className="ml-auto text-2xs text-muted">×{f.count}</span>
            </div>
            <p className="text-muted">{f.message}</p>
            {f.quota?.hard === undefined || f.quota.used === undefined ? null : <QuotaBar quota={f.quota} />}
          </li>
        ))}
      </ul>
    </section>
  );
}

/** One pod's own page, at `/c/:cluster/x/pending/:namespace/:name`. */
export function PodPage(): ReactNode {
  const { cluster = '', namespace = '', name = '' } = useParams<{ cluster: string; namespace: string; name: string }>();
  useDeclarePageFacts({ title: `Pod Debugging · ${name}` });
  return (
    <div className="flex flex-col gap-4 p-4">
      <PageHeader
        heading="h1"
        back={<BackArrow to={`/c/${encodeURIComponent(cluster)}/x/pending`} />}
        kind="Pod"
        name={name}
        namespace={namespace}
        cluster={cluster}
        actions={
          <Link to={`/c/${encodeURIComponent(cluster)}/pods/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`} className={`text-xs text-accent hover:underline ${focusRing}`}>
            Open the pod
          </Link>
        }
        className=""
      />
      <PodView cluster={cluster} namespace={namespace} name={name} />
    </div>
  );
}

