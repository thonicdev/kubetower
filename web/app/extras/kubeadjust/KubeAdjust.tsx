import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useRef, type ReactNode } from 'react';
import { Link, useParams, useSearchParams } from 'react-router';
import { CloseButton, CopyButton, Failure, NoMetrics, Pending, focusRing, useDeclarePageFacts, btnSecondary } from '@kubetower/ui';

import { categoryIn, pageQuery, viewOf, type Ask, type Headline as HeadlineData, type Page, type View } from './data';
import { NodeWorkloadsView } from './NodeFilter';
import { Sources } from './parts';
import { NamespaceView, NodesView, OverviewView } from './views';

/**
 * KubeAdjust, at `/c/:cluster/x/kubeadjust`: is this cluster asking for what it
 * uses, and if not, what should it ask for.
 *
 * Three views and not one page, because four hundred cards is not an answer:
 * **Namespaces** (where the waste is), **Nodes** (what the machines have), and
 * **one namespace** (its workloads, and the advice beside the numbers). The
 * address carries `ns`, `view` and `cat`, so a link and a reload land on the
 * same view; `ns` wins over `view`. Only `ns` is a read: the cluster's answer
 * holds both of its views and every suggestion, so a tab or a category chip is
 * drawn from the answer already here.
 *
 * **Nothing here changes a cluster.** A recommendation is a `kubectl set
 * resources`, which restarts every pod of its workload: it is drawn as text to
 * review and copy, and run by the operator: there is no Apply.
 *
 * **An empty page is a claim, and it has to be earned.** The four notes are
 * always there, and a cluster nobody could measure says so in place of every
 * body - never "nothing to change".
 *
 * **Read top-down**: a headline saying what is at risk,
 * what the over-sized requests would give back and how much of what is
 * requested is used; then the sources, one line, with only what failed
 * spelled out; then the view.
 *
 * `node`, in the address, narrows the page to the workloads with a pod on one
 * node: where a node's panel sends its reader for what it has no room for. It
 * is the page's own filter and not the server's question, so the page above
 * the list stays the cluster's.
 */
export function KubeAdjust(): ReactNode {
  const { cluster = '' } = useParams<{ cluster: string }>();
  useDeclarePageFacts({ title: 'KubeAdjust' });
  const [params, setParams] = useSearchParams();
  const ask: Ask = { ns: params.get('ns') ?? '', view: params.get('view') ?? '', cat: params.get('cat') ?? '' };
  const node = params.get('node') ?? '';
  // The previous answer stays on screen while another namespace is read,
  // dimmed, so the menu does not blank the page; the key is the scope, so an
  // older answer can never be drawn as the newer one.
  const query = useQuery({ ...pageQuery(cluster, ask.ns), placeholderData: keepPreviousData });
  const page = query.data;
  const view: View | '' = page === undefined ? '' : viewOf(page, ask.view);

  /** Moves the page to another question, keeping what the move does not change. */
  const go = (next: Partial<Ask> & { state?: string; node?: string }) =>
    setParams((current) => {
      const out = new URLSearchParams(current);
      for (const [name, value] of Object.entries(next)) {
        if (value === '' || value === undefined) out.delete(name);
        else out.set(name, value);
      }
      return out;
    });

  return (
    <div id="kubeadjust" className="flex flex-col gap-4 p-4" data-view={view} aria-busy={query.isFetching}>
      <Toolbar page={page} fetching={query.isFetching} onRefresh={() => void query.refetch()} go={go} ask={ask} node={node} />

      {page === undefined ? (
        query.isError ? (
          <Failure error={query.error} onRetry={() => void query.refetch()} />
        ) : (
          <Pending what="this cluster's requests, limits and usage" />
        )
      ) : (
        <div className={query.isPlaceholderData ? 'flex flex-col gap-4 opacity-60' : 'flex flex-col gap-4'}>
          {query.isError ? <Failure error={query.error} onRetry={() => void query.refetch()} /> : null}
          {page.window === 'no-prometheus' ? <PrometheusBanner /> : null}
          {page.ready && page.headline !== null ? <Headline headline={page.headline} /> : null}
          <Sources notes={page.notes} partial={page.partial} windowReadAt={page.windowReadAt} />
          {node !== '' && page.ready ? (
            <NodeWorkloadsView cluster={cluster} node={node} />
          ) : (
            <Body cluster={cluster} page={page} view={viewOf(page, ask.view)} go={go} picked={params.get('state') ?? ''} asked={ask.cat} />
          )}
        </div>
      )}
    </div>
  );
}

type Go = (next: Partial<Ask> & { state?: string; node?: string }) => void;

/** The official chart's page, and the two lines that install it. */
const PROMETHEUS_DOCS = 'https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack';
const PROMETHEUS_INSTALL =
  'helm repo add prometheus-community https://prometheus-community.github.io/helm-charts && ' +
  'helm install prometheus prometheus-community/kube-prometheus-stack -n monitoring --create-namespace';

/**
 * Said above everything when no usage history was read: every suggestion then
 * rests on one sample per replica, which a reader skimming the sources line
 * would not weigh. The History note under the headline says why it was not
 * read; this says what it costs and how to get one. Shown only when the
 * core found no Prometheus (`window` is `no-prometheus`): a Prometheus that
 * could not be asked, or answered with nothing, is not fixed by installing
 * one, and the History note explains each of those.
 */
function PrometheusBanner(): ReactNode {
  const block = useRef<HTMLElement | null>(null);
  return (
    <section
      aria-label="Prometheus"
      data-prometheus-banner=""
      className="flex flex-col gap-1.5 rounded-lg border border-warn/45 bg-warn/10 px-3.5 py-2.5 text-xs"
    >
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <strong className="text-sm font-semibold text-warn">Recommendations are not reliable without Prometheus</strong>
        <span className="text-muted">With no usage history, every suggestion rests on a single sample per pod: no P95, no bursts, no trend.</span>
        <a href={PROMETHEUS_DOCS} target="_blank" rel="noreferrer" className={`ml-auto text-accent underline ${focusRing}`}>
          How to install it
        </a>
      </div>
      <div className="flex min-w-0 items-center gap-2">
        <code ref={block} data-install="" className="min-w-0 flex-1 overflow-x-auto rounded-md border border-line bg-panel-2 px-1.5 py-1 font-mono text-2xs whitespace-pre select-all">
          {PROMETHEUS_INSTALL}
        </code>
        <CopyButton value={PROMETHEUS_INSTALL} label="Copy" selectRef={block} />
      </div>
    </section>
  );
}

/**
 * The tabs, the counts, the namespace menu and "Check again" - outside every
 * panel, which is a fix and not a layout: a menu inside a box that clips
 * opens behind the page.
 */
function Toolbar({
  page,
  fetching,
  onRefresh,
  go,
  ask,
  node,
}: {
  page: Page | undefined;
  fetching: boolean;
  onRefresh: () => void;
  go: Go;
  ask: Ask;
  node: string;
}): ReactNode {
  const shown = viewOf(page ?? { namespace: ask.ns }, ask.view);
  const tab = (label: string, view: 'overview' | 'nodes') => {
    const on = node === '' && shown === view;
    return (
      <Link
        to={{ search: view === 'nodes' ? '?view=nodes' : '' }}
        aria-current={on ? 'page' : undefined}
        data-tab={view}
        className={`-mb-px border-b-2 px-3 py-1.5 text-sm ${on ? 'border-accent text-fg' : 'border-transparent text-muted hover:text-fg'} ${focusRing}`}
      >
        {label}
      </Link>
    );
  };
  const namespaces = page?.namespaces ?? (ask.ns === '' ? [] : [ask.ns]);
  return (
    <header className="flex flex-col gap-2">
      <h1 className="text-xl font-medium">KubeAdjust</h1>
      <div className="flex flex-wrap items-end gap-2 border-b border-line" data-toolbar="">
        <nav aria-label="KubeAdjust views" className="flex items-end gap-1">
          {tab('Namespaces', 'overview')}
          {tab('Nodes', 'nodes')}
          {node !== '' ? (
            <>
              <span className="text-muted" aria-hidden="true">
                ›
              </span>
              <span aria-current="page" data-node-chip={node} className="inline-flex items-center gap-1.5 rounded-md border border-line bg-panel px-2.5 py-1 text-sm">
                Node {node}
                <CloseButton onClick={() => go({ node: '' })} label={`Clear the node filter ${node}`} size="inline" />
              </span>
            </>
          ) : shown === 'namespace' ? (
            <>
              <span className="text-muted" aria-hidden="true">
                ›
              </span>
              <span aria-current="page" className="rounded-md border border-line bg-panel px-2.5 py-1 text-sm">
                {page?.namespace ?? ask.ns}
              </span>
            </>
          ) : null}
        </nav>
        <div className="ml-auto flex flex-wrap items-center gap-2 pb-1.5">
          {page !== undefined && !page.ready ? (
            <span data-counts="unmeasured">
              <NoMetrics variant="badge" detail={usageNote(page)} />
            </span>
          ) : null}
          <label className="flex items-center gap-1.5 text-xs text-muted">
            <span>Namespace</span>
            <select
              data-namespace-menu=""
              value={ask.ns}
              title={page?.namespacesNote ?? 'Show only one namespace, or all of them'}
              // Another namespace is another question: the filter goes with the
              // one it was asked of.
              onChange={(event) => go({ ns: event.target.value, view: '', cat: '', state: '' })}
              className={`rounded-md border border-line bg-panel-2 px-2 py-1 text-xs text-fg ${focusRing}`}
            >
              <option value="">All namespaces</option>
              {namespaces.map((name) => (
                <option key={name} value={name}>
                  {name}
                </option>
              ))}
            </select>
          </label>
          <button
            type="button"
            onClick={onRefresh}
            disabled={fetching}
            title="Read this cluster's requests, limits and usage again"
            className={btnSecondary}
          >
            {fetching ? 'Reading…' : 'Check again'}
          </button>
        </div>
      </div>
    </header>
  );
}

/**
 * The first line: what is at risk, what the reductions give back, and how much
 * of what is requested is used. Drawn only on a measured page - a cluster
 * nobody could measure has no headline, never one of zeros. A severity at zero
 * is a fact, and drawn as one.
 */
function Headline({ headline: h }: { headline: HeadlineData }): ReactNode {
  const tile = 'flex min-w-36 flex-col gap-0.5 rounded-lg border border-line bg-panel px-3 py-2';
  const label = 'text-2xs font-bold tracking-wide text-muted uppercase';
  const reclaim = [h.reclaimCPU, h.reclaimMemory].filter((v) => v !== '');
  const share = (v: number | null) => (v === null ? '—' : `${v}%`);
  return (
    <section aria-label={`In ${h.scope}`} data-headline="" className="flex flex-wrap gap-2">
      {(
        [
          ['danger', 'At risk', h.atRisk, 'text-bad'],
          ['warning', 'Worth a look', h.worth, 'text-warn'],
          ['overkill', 'Over-provisioned', h.over, 'text-fg'],
        ] as const
      ).map(([kind, text, count, tone]) => (
        <div key={kind} data-headline-count={kind} className={tile}>
          <span className={label}>{text}</span>
          <span className={`text-2xl font-semibold tabular-nums ${count === 0 ? 'text-muted' : tone}`}>{count}</span>
        </div>
      ))}
      <div data-headline="reclaim" className={tile} title="What the request reductions below would give back, over every replica. Requests only: a limit reserves nothing.">
        <span className={label}>Requests given back</span>
        {reclaim.length === 0 ? (
          <span className="text-sm text-muted">nothing to give back</span>
        ) : (
          <span className="text-base font-semibold tabular-nums">{reclaim.join(' · ')}</span>
        )}
        {h.reclaimWorkloads > 0 ? (
          <span className="text-2xs text-muted">
            from {h.reclaimWorkloads} workload{h.reclaimWorkloads === 1 ? '' : 's'}
          </span>
        ) : null}
      </div>
      <div data-headline="used" className={tile} title="Usage as a share of the requests, over the containers that reported usage">
        <span className={label}>Used of requested</span>
        <span className="text-base font-semibold tabular-nums">
          CPU {share(h.usedOfRequestedCPU)} · memory {share(h.usedOfRequestedMemory)}
        </span>
        <span className="text-2xs text-muted">in {h.scope}</span>
      </div>
    </section>
  );
}

function Body({
  cluster,
  page,
  view,
  go,
  picked,
  asked,
}: {
  cluster: string;
  page: Page;
  view: View;
  go: Go;
  picked: string;
  asked: string;
}): ReactNode {
  if (!page.ready) {
    return (
      <section data-unmeasured="" className="px-1 py-2">
        <NoMetrics detail={usageNote(page)} />
      </section>
    );
  }
  switch (view) {
    case 'nodes':
      return <NodesView page={page} picked={picked} onPick={(state) => go({ state })} />;
    case 'namespace':
      return <NamespaceView page={page} asked={categoryIn(asked)} onCategory={(cat) => go({ cat })} />;
    default:
      return <OverviewView cluster={cluster} page={page} />;
  }
}

/** Why nothing was measured: the failing usage note's line, for the hover of
 *  the one *No metrics available* the page draws (#127). */
function usageNote(page: Page): string | undefined {
  return page.notes.find((note) => !note.ok)?.detail;
}
