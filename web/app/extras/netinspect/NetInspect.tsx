import { keepPreviousData, useQuery } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { useParams, useSearchParams } from 'react-router';
import { Failure, Pending, btnSecondary, focusRing, useDeclarePageFacts } from '@kubetower/ui';

import { ENGINES, flowsQuery, overviewQuery, problemCode, showIn, type Ask, type Flows, type Overview, type Show } from './data';
import { FlowUIButtons } from './FlowUIs';
import { Lists, type ListKey } from './Lists';
import { Matrix } from './Matrix';
import { NamespaceView } from './NamespaceView';
import { FlowsBadge, ModeBadge, Notices } from './parts';
import { VerdictPanel } from './Verdict';

/**
 * The Network Inspector, at `/c/:cluster/x/netinspect`: which NetworkPolicies
 * select which pods, whom their rules let them talk to, and whether one pod
 * would reach another.
 *
 * **Everything here is the rules**, read from the NetworkPolicy objects: what
 * would be allowed, never what passed. The mode badge says so first, the
 * matrix says it again, and the verdict says "the rules allow
 * it", never "it passes".
 *
 * The address carries the question - `ns` (one namespace, or the cluster),
 * `show` (a tile's filter on the pods), `list` (which table), and the verdict's `from`, `to`, `port` - so a link and a
 * reload land on the same picture. Only `ns`, `show`, `q` and `offset` are
 * reads; the rest is drawn from the answer already here.
 */
/** The namespace menu's choices: every namespace the answer names, whatever
 *  the scope asked, so one namespace leads to another directly. */
function choicesOf(overview: Overview | undefined): readonly string[] {
  return overview?.choices ?? (overview?.namespaces ?? []).map((row) => row.name);
}

export function NetInspect(): ReactNode {
  const { cluster = '' } = useParams<{ cluster: string }>();
  useDeclarePageFacts({ title: 'Network Inspector' });
  const [params, setParams] = useSearchParams();
  const ask: Ask = {
    ns: params.get('ns') ?? '',
    show: showIn(params.get('show')),
    q: params.get('q') ?? '',
    offset: Math.max(0, Number(params.get('offset') ?? '0') || 0),
  };
  const query = useQuery({ ...overviewQuery(cluster, ask), placeholderData: keepPreviousData });
  const overview = query.data;
  // Observed traffic is its own read: a tunnel to the flow source, once per
  // namespace asked, and only once the rules are here.
  const flows = useQuery({ ...flowsQuery(cluster, ask.ns), enabled: overview !== undefined });
  const hidden = new Set((params.get('hide') ?? '').split(',').filter((x) => x !== ''));
  const toggle = (layer: string) => {
    const next = new Set(hidden);
    if (next.has(layer)) next.delete(layer);
    else next.add(layer);
    go({ hide: [...next].sort().join(',') });
  };

  const go = (next: Record<string, string>) =>
    setParams((current) => {
      const out = new URLSearchParams(current);
      for (const [name, value] of Object.entries(next)) {
        if (value === '') out.delete(name);
        else out.set(name, value);
      }
      return out;
    });

  return (
    <div id="netinspect" className="flex flex-col gap-4 p-4" aria-busy={query.isFetching || flows.isFetching}>
      <header className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-3">
          <h1 className="text-xl font-medium">Network Inspector</h1>
          <ul className="flex flex-wrap items-center gap-1" aria-label="Policy engines evaluated" data-engines="">
            {ENGINES.map((name) => (
              <li key={name} data-engine={name} className="rounded-sm border border-line px-1.5 py-0.5 text-2xs text-muted">
                {name}
              </li>
            ))}
          </ul>
          <ModeBadge mode={overview?.mode ?? 'rules'} on={!hidden.has('rules')} onToggle={() => toggle('rules')} />
          <FlowsBadge flows={flows.data} pending={flows.isFetching} failed={flows.isError} on={!hidden.has('observed')} onToggle={() => toggle('observed')} />
          <div className="ml-auto flex flex-wrap items-center gap-2">
            <FlowUIButtons cluster={cluster} namespace={ask.ns} />
            <label className="flex items-center gap-1.5 text-xs text-muted">
              <span>Namespace</span>
              <select
                data-namespace-menu=""
                value={ask.ns}
                onChange={(event) => go({ ns: event.target.value, show: '', offset: '', node: '' })}
                className={`rounded-md border border-line bg-panel-2 px-2 py-1 text-xs text-fg ${focusRing}`}
              >
                <option value="">All namespaces</option>
                {choicesOf(overview).map((name) => (
                  <option key={name} value={name}>
                    {name}
                  </option>
                ))}
                {ask.ns !== '' && !choicesOf(overview).includes(ask.ns) ? <option value={ask.ns}>{ask.ns}</option> : null}
              </select>
            </label>
            <button type="button" className={btnSecondary} disabled={query.isFetching} onClick={() => {
                void query.refetch();
                void flows.refetch();
              }}>
              {query.isFetching ? 'Reading…' : 'Check again'}
            </button>
          </div>
        </div>
      </header>

      {overview === undefined ? (
        query.isError ? (
          <Refused error={query.error} onRetry={() => void query.refetch()} />
        ) : (
          <Pending what="this cluster's NetworkPolicies and pods" />
        )
      ) : (
        <div className={query.isPlaceholderData ? 'flex flex-col gap-4 opacity-60' : 'flex flex-col gap-4'}>
          {query.isError ? <Refused error={query.error} onRetry={() => void query.refetch()} /> : null}
          <Notices complete={overview.complete} notes={overview.notes} cni={overview.cni} unevaluated={overview.unevaluated} cilium={overview.cilium} />
          <Body cluster={cluster} overview={overview} flows={flows.data} hidden={hidden} ask={ask} params={params} go={go} />
        </div>
      )}
    </div>
  );
}

/** A refusal to read the rules is said as one: not a cluster without rules. */
function Refused({ error, onRetry }: { error: unknown; onRetry: () => void }): ReactNode {
  const code = problemCode(error);
  if (code === 'list_forbidden') {
    return (
      <p role="alert" data-refused="" className="rounded-lg border border-warn/45 bg-warn/10 p-3 text-sm text-warn" title={error instanceof Error ? error.message : String(error)}>
        Not allowed to read the NetworkPolicies or the pods here
      </p>
    );
  }
  return <Failure error={error} onRetry={onRetry} />;
}

type Go = (next: Record<string, string>) => void;

function Body({
  cluster,
  overview,
  flows,
  hidden,
  ask,
  params,
  go,
}: {
  cluster: string;
  overview: Overview;
  flows: Flows | undefined;
  hidden: ReadonlySet<string>;
  ask: Ask;
  params: URLSearchParams;
  go: Go;
}): ReactNode {
  const list = (params.get('list') ?? 'pods') as ListKey;
  if (overview.summary.policies === 0 && ask.ns === '' && ask.show === '' && ask.q === '') {
    return (
      <>
        <Tiles overview={overview} ask={ask} list={list} go={go} />
        <p data-empty="" className="rounded-lg border border-line bg-panel p-4 text-sm">
          No NetworkPolicy: every pod is open
        </p>
        <VerdictPanel cluster={cluster} params={params} go={go} />
      </>
    );
  }
  return (
    <>
      <Tiles overview={overview} ask={ask} list={list} go={go} />
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
        <section aria-label="Rules" className="flex flex-col gap-2 xl:col-span-2">
          <h2 className="text-sm font-medium">
            {overview.graph.scope === 'cluster' ? 'Namespaces' : overview.namespace}
          </h2>
          {overview.view === undefined ? (
            <Matrix
              graph={overview.graph}
              flows={flows?.edges ?? []}
              showRules={!hidden.has('rules')}
              showFlows={!hidden.has('observed')}
              onNamespace={(ns) => go({ ns, node: '', show: '', offset: '' })}
            />
          ) : (
            <NamespaceView
              namespace={overview.namespace}
              view={overview.view}
              flows={flows?.edges ?? []}
              showFlows={!hidden.has('observed')}
              onWorkload={(name) => go({ q: name, list: '', offset: '' })}
            />
          )}
        </section>
        <VerdictPanel cluster={cluster} params={params} go={go} />
      </div>
      <Lists overview={overview} flows={flows} ask={ask} list={list} go={go} />
    </>
  );
}

/**
 * The tiles: each a count that filters. The first four filter the pods (the
 * server's `show`); the last two open the table they are about, filtered.
 */
function Tiles({ overview, ask, list, go }: { overview: Overview; ask: Ask; list: ListKey; go: Go }): ReactNode {
  const s = overview.summary;
  const tiles: { key: string; label: string; count: number; hint: string; on: boolean; act: () => void; tone: string }[] = [
    { key: 'in', label: 'Isolated in', count: s.isolatedIn, hint: 'Pods a policy selects for ingress: only what a rule allows reaches them.', on: ask.show === 'in', act: () => pickShow('in'), tone: 'text-ok' },
    { key: 'out', label: 'Isolated out', count: s.isolatedOut, hint: 'Pods a policy selects for egress: they reach only what a rule allows.', on: ask.show === 'out', act: () => pickShow('out'), tone: 'text-ok' },
    { key: 'open', label: 'Not isolated', count: s.open, hint: 'Pods no policy selects: every flow in and out is allowed.', on: ask.show === 'open', act: () => pickShow('open'), tone: 'text-fg' },
    { key: 'deny', label: 'Default-deny namespaces', count: s.defaultDeny, hint: 'Namespaces with a policy selecting every pod and allowing nothing in a direction.', on: list === 'deny', act: () => go({ list: list === 'deny' ? '' : 'deny' }), tone: 'text-fg' },
    { key: 'idle', label: 'Policies selecting nothing', count: s.idle, hint: 'Policies whose podSelector matches no running pod: often a label that does not exist.', on: list === 'idle', act: () => go({ list: list === 'idle' ? '' : 'idle' }), tone: s.idle > 0 ? 'text-warn' : 'text-fg' },
  ];
  if (s.hostNetwork > 0) {
    tiles.splice(3, 0, { key: 'host', label: 'Host network', count: s.hostNetwork, hint: 'Pods on their node’s network: NetworkPolicy applies to them as the network plugin decides.', on: ask.show === 'host', act: () => pickShow('host'), tone: 'text-muted' });
  }
  function pickShow(show: Show): void {
    go({ show: ask.show === show ? '' : show, offset: '', list: '' });
  }
  return (
    <section aria-label="Summary" data-tiles="" className="flex flex-wrap gap-2">
      <div className="flex min-w-32 flex-col gap-0.5 rounded-lg border border-line bg-panel px-3 py-2" data-tile="pods">
        <span className="text-2xs font-bold tracking-wide text-muted uppercase">Pods · policies</span>
        <span className="text-2xl font-semibold tabular-nums">
          {s.pods} <span className="text-base text-muted">· {s.policies}</span>
        </span>
      </div>
      {tiles.map((tile) => (
        <button
          key={tile.key}
          type="button"
          data-tile={tile.key}
          aria-pressed={tile.on}
          title={tile.hint}
          onClick={tile.act}
          className={`flex min-w-36 flex-col gap-0.5 rounded-lg border px-3 py-2 text-left ${tile.on ? 'border-accent bg-accent/10' : 'border-line bg-panel hover:border-muted'} ${focusRing}`}
        >
          <span className="text-2xs font-bold tracking-wide text-muted uppercase">{tile.label}</span>
          <span className={`text-2xl font-semibold tabular-nums ${tile.count === 0 ? 'text-muted' : tile.tone}`}>{tile.count}</span>
          <Share part={tile.key === 'in' || tile.key === 'out' || tile.key === 'open' || tile.key === 'host' ? tile.count : -1} whole={s.pods} />
        </button>
      ))}
    </section>
  );
}

/** A thin bar under a pod tile: its share of the pods. */
function Share({ part, whole }: { part: number; whole: number }): ReactNode {
  if (part < 0 || whole === 0) return null;
  const pct = Math.round((part / whole) * 100);
  return (
    <span className="flex items-center gap-1.5" aria-label={`${String(pct)}% of the pods`}>
      <svg viewBox="0 0 100 4" preserveAspectRatio="none" className="h-1 w-20" aria-hidden="true">
        <rect width="100" height="4" rx="2" fill="var(--color-line)" />
        <rect width={pct} height="4" rx="2" fill="var(--color-accent)" />
      </svg>
      <span className="text-2xs text-muted tabular-nums">{pct}%</span>
    </span>
  );
}

