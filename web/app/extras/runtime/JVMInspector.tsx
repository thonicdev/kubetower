import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link, useParams } from 'react-router';
import {
  Failure,
  FilterBox,
  ImageRef,
  Pending,
  Pill,
  TABLE,
  HEAD_ROW,
  ROW,
  bodyCell,
  btnSecondary,
  btnToggle,
  focusRing,
  headCell,
  useDeclarePageFacts,
  useUrlParameter,
} from '@kubetower/ui';

import {
  groupPods,
  heapBar,
  inventoryQuery,
  POLL_MS,
  ROW_STEP,
  matches,
  riskTone,
  riskWord,
  worstLevel,
  type Container,
  type Group,
  type Grouping,
  type Inventory,
  type Pod,
  type RiskLevel,
  type Usage,
} from './data';
import JVMTab from './JVMTab';

/**
 * The Runtime Inspector's page, at `/c/:cluster/x/runtime` (`x/jvm` before 0.2.0, redirected): the pods that probably run Java,
 * recognized from their spec, and for each Java container how close its
 * maximum heap comes to its memory limit.
 *
 * **Read from the spec alone**: nothing is run in a container, so the heap is
 * an estimate from the options the JVM would read and the limit, and a pod
 * whose image starts Java with nothing in its spec is not on this page. The
 * page says both, so an empty table never reads as "no Java here". The
 * working set is metrics-server's, drawn as a mark when it was measured and
 * said "not measured" - with why - when it was not.
 *
 * The address holds every choice: `ns` (the namespace read, empty for the
 * whole cluster), `risk` (the tile picked), `group` (`workload`, or the
 * namespace by default) and `q` (the search). The list column's links land
 * here with `ns` and `q`.
 *
 * Class strings are looked up whole, never built by interpolation: Tailwind
 * finds utilities by reading source text.
 */
export function JVMInspector(): ReactNode {
  const { cluster = '' } = useParams<{ cluster: string }>();
  useDeclarePageFacts({ title: 'Runtime Inspector' });
  const [ns, setNs] = useUrlParameter('ns');
  const [risk, setRisk] = useUrlParameter('risk');
  const [group, setGroup] = useUrlParameter('group');
  const [search, setSearch] = useUrlParameter('q');
  // The pod whose JVM tab is open beside the list, as `namespace/name`.
  const [open, setOpen] = useUrlParameter('pod');
  // Polled for the working sets, and only while the page is open and its
  // tab visible: TanStack stops the interval when the page unmounts and
  // pauses it while the document is hidden (refetchIntervalInBackground).
  const query = useQuery({
    ...inventoryQuery(cluster, ns),
    placeholderData: keepPreviousData,
    refetchInterval: POLL_MS,
    refetchIntervalInBackground: false,
  });
  const inventory = query.data;

  return (
    <div id="jvm" className="flex flex-col gap-4 p-4" aria-busy={query.isFetching}>
      <header className="flex flex-wrap items-baseline gap-3">
        <h1
          className="text-lg font-semibold"
          title="Probable Java pods, read from their spec. The risk compares the estimated maximum heap with the memory limit; the 75 % line is a heuristic, not a JDK rule."
        >
          Runtime Inspector
        </h1>
        <button
          type="button"
          className={`ml-auto ${btnSecondary}`}
          disabled={query.isFetching}
          onClick={() => void query.refetch()}
        >
          Refresh
        </button>
      </header>
      {ns === '' ? null : (
        <p data-jvm-scope={ns} className="flex items-center gap-2 text-xs text-muted">
          Namespace <span className="font-semibold text-fg">{ns}</span>
          <button type="button" className={btnSecondary} onClick={() => setNs('')}>
            Whole cluster
          </button>
        </p>
      )}
      {inventory === undefined ? (
        query.isError ? (
          <Failure error={query.error} onRetry={() => void query.refetch()} />
        ) : (
          <Pending what="this cluster's pods" />
        )
      ) : (
        <>
          {query.isError ? <Failure error={query.error} onRetry={() => void query.refetch()} /> : null}
          <Body
            cluster={cluster}
            inventory={inventory}
            risk={risk}
            onRisk={setRisk}
            grouping={group === 'workload' ? 'workload' : 'namespace'}
            onGrouping={(g) => setGroup(g === 'namespace' ? '' : g)}
            search={search}
            onSearch={setSearch}
            open={open}
            onOpen={setOpen}
          />
        </>
      )}
    </div>
  );
}

/** A tile's risk, its label and the class its count is drawn in. */
const TILES: readonly { key: RiskLevel; label: string; text: string; hint: string }[] = [
  { key: 'ok', label: 'Room', text: 'text-ok', hint: 'the heap stays under 75 % of the memory limit' },
  { key: 'warning', label: 'Tight', text: 'text-warn', hint: 'the heap may pass 75 % of the memory limit' },
  { key: 'error', label: 'OOMKill risk', text: 'text-bad', hint: 'an explicit heap at or above the memory limit' },
  { key: 'nolimit', label: 'No limit', text: 'text-fg', hint: 'no memory limit: the heap is sized on the node' },
  { key: 'unknown', label: 'Unknown', text: 'text-fg', hint: 'a limit, and a heap sized on what the spec does not say' },
];

function Body({
  cluster,
  inventory,
  risk,
  onRisk,
  grouping,
  onGrouping,
  search,
  onSearch,
  open,
  onOpen,
}: {
  cluster: string;
  inventory: Inventory;
  risk: string;
  onRisk: (risk: string) => void;
  grouping: Grouping;
  onGrouping: (grouping: Grouping) => void;
  search: string;
  onSearch: (search: string) => void;
  open: string;
  onOpen: (pod: string) => void;
}): ReactNode {
  const counts = useMemo(() => {
    const out = new Map<RiskLevel, number>();
    for (const pod of inventory.pods) {
      const level = worstLevel(pod);
      out.set(level, (out.get(level) ?? 0) + 1);
    }
    return out;
  }, [inventory.pods]);
  const picked = TILES.find((t) => t.key === risk);
  const shown = inventory.pods.filter((pod) => (picked === undefined || worstLevel(pod) === picked.key) && matches(pod, search));
  const groups = groupPods(shown, grouping);
  const rows = shown.reduce((n, pod) => n + pod.containers.length, 0);
  const [budget, setBudget] = useState(ROW_STEP);
  const drawn = budgeted(groups, budget);
  const drawnRows = Math.min(budget, rows);
  const counted = `${String(inventory.pods.length)} of ${String(inventory.read)} ${inventory.read === 1 ? 'pod' : 'pods'}`;

  return (
    <div className="flex flex-col gap-3 text-sm">
      <section aria-label="Java pods by risk" data-jvm-tiles="" className="flex flex-wrap items-stretch gap-2">
        <button
          type="button"
          data-jvm-tile="all"
          data-count={String(inventory.pods.length)}
          aria-pressed={picked === undefined}
          title="Show every Java pod"
          onClick={() => onRisk('')}
          className={`flex min-w-32 flex-col items-start gap-0.5 rounded-lg border px-3 py-2 ${
            picked === undefined ? 'border-accent bg-panel-2' : 'border-line bg-panel hover:bg-panel-2'
          } ${focusRing}`}
        >
          <span className="text-2xl font-semibold tabular-nums">{inventory.pods.length}</span>
          <span className="text-xs text-muted">Java pods</span>
        </button>
        {TILES.map((tile) => {
          const count = counts.get(tile.key) ?? 0;
          // Unknown is drawn only when some pod is there: it is a gap in the
          // spec's reading, not a grade every cluster has.
          if (tile.key === 'unknown' && count === 0) return null;
          const on = picked?.key === tile.key;
          return (
            <button
              key={tile.key}
              type="button"
              data-jvm-tile={tile.key}
              data-count={String(count)}
              aria-pressed={on}
              title={`Show only the pods whose worst Java container is ${tile.label.toLowerCase()}: ${tile.hint}`}
              onClick={() => onRisk(on ? '' : tile.key)}
              className={`flex min-w-32 flex-col items-start gap-0.5 rounded-lg border px-3 py-2 ${
                on ? 'border-accent bg-panel-2' : 'border-line bg-panel hover:bg-panel-2'
              } ${focusRing}`}
            >
              <span className={`text-2xl font-semibold tabular-nums ${count === 0 ? 'text-muted' : tile.text}`}>{count}</span>
              <span className="text-xs text-muted">{tile.label}</span>
            </button>
          );
        })}
      </section>
      <p
        data-jvm-count
        className="text-xs text-muted"
        title="Running or pending pods read. A pod whose image starts Java with nothing in its spec is not recognized; a pod is counted under its worst Java container."
      >
        {counted}
      </p>
      {inventory.complete ? null : (
        <p data-jvm-partial className="text-xs text-warn">
          {inventory.note ?? 'Partial list.'}
        </p>
      )}
      {(inventory.unreadable ?? 0) > 0 ? <p className="text-xs text-warn">{String(inventory.unreadable)} pods unreadable</p> : null}
      <UsageLine usage={inventory.usage} />
      {inventory.pods.length === 0 ? null : (
        <>
          <div className="flex flex-wrap items-center gap-3">
            <div role="group" aria-label="Group by" className="flex items-center gap-1 text-xs text-muted">
              Group by
              <button type="button" aria-pressed={grouping === 'namespace'} className={btnToggle(grouping === 'namespace')} onClick={() => onGrouping('namespace')}>
                Namespace
              </button>
              <button type="button" aria-pressed={grouping === 'workload'} className={btnToggle(grouping === 'workload')} onClick={() => onGrouping('workload')}>
                Workload
              </button>
            </div>
            <FilterBox name="q" value={search} onChange={onSearch} label="Search the Java pods" placeholder="pod, workload, image" />
          </div>
          <Legend />
          {groups.length === 0 ? (
            <p data-jvm-none className="rounded-lg border border-line bg-panel p-4 text-sm text-muted">
              No match.{' '}
              <button
                type="button"
                className={btnSecondary}
                onClick={() => {
                  onRisk('');
                  onSearch('');
                }}
              >
                Show all
              </button>
            </p>
          ) : (
            <div className="overflow-x-auto">
              <table className={`${TABLE} w-full tabular-nums`}>
                <thead>
                  <tr className={HEAD_ROW}>
                    <th className={headCell()}>Pod</th>
                    <th className={headCell()}>Container</th>
                    <th className={`${headCell()} w-2/5`}>Memory: heap, room outside it, limit</th>
                    <th className={headCell()}>Risk</th>
                    <th className={headCell()}>Why Java</th>
                  </tr>
                </thead>
                {drawn.map((g) => (
                  <tbody key={g.key} data-jvm-group={g.key}>
                    <tr>
                      <th colSpan={5} scope="colgroup" className="border-t border-line bg-panel px-2 py-1.5 text-left text-xs font-semibold">
                        <span className="text-fg">{g.title}</span>
                        {g.sub === '' ? null : <span className="ml-2 font-normal text-muted">{g.sub}</span>}
                        <span className="ml-2 font-normal text-muted">
                          {g.pods.length} {g.pods.length === 1 ? 'pod' : 'pods'}
                        </span>
                        <span className="ml-2">
                          <Pill tone={riskTone(g.worst)} neutral="muted">
                            {TILES.find((t) => t.key === g.worst)?.label ?? g.worst}
                          </Pill>
                        </span>
                      </th>
                    </tr>
                    {g.shownPods.flatMap((pod) =>
                      pod.containers.map((c, i) => (
                        <Row key={`${pod.namespace}/${pod.name}/${c.name}`} cluster={cluster} pod={pod} c={c} first={i === 0} grouping={grouping} picked={open === podId(pod)} onOpen={onOpen} />
                      )),
                    )}
                  </tbody>
                ))}
              </table>
              {drawnRows < rows ? (
                <p data-jvm-more="" className="mt-2 flex items-center justify-end gap-2 text-xs text-muted">
                  <span className="tabular-nums">
                    {drawnRows} of {rows} containers shown
                  </span>
                  <button type="button" className={btnSecondary} onClick={() => setBudget(budget + ROW_STEP)}>
                    Show {Math.min(ROW_STEP, rows - drawnRows)} more
                  </button>
                </p>
              ) : null}
            </div>
          )}
        </>
      )}
      <PodPanel cluster={cluster} open={open} onClose={() => onOpen('')} />
    </div>
  );
}

const podId = (pod: Pod) => `${pod.namespace}/${pod.name}`;

/**
 * The pod's JVM tab beside the list, as a list opens a drawer. Closing it
 * unmounts the tab, so nothing it reads goes on: the page itself polls only
 * the inventory.
 */
function PodPanel({ cluster, open, onClose }: { cluster: string; open: string; onClose: () => void }): ReactNode {
  useEffect(() => {
    if (open === '') return undefined;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [open, onClose]);
  const at = open.indexOf('/');
  if (open === '' || at < 1) return null;
  const namespace = open.slice(0, at);
  const name = open.slice(at + 1);
  const href = `/c/${encodeURIComponent(cluster)}/pods/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
  return (
    <aside
      data-jvm-panel={open}
      aria-label={`JVM of ${name}`}
      className="fixed inset-y-0 right-0 z-30 flex w-full max-w-2xl flex-col gap-3 overflow-y-auto border-l border-line bg-panel p-3 shadow-lg"
    >
      <header className="flex items-center gap-2">
        <Link to={href} className={`min-w-0 truncate font-semibold text-accent hover:underline ${focusRing}`} title={`Open the pod ${open}`}>
          {name}
        </Link>
        <span className="text-xs text-muted">{namespace}</span>
        <button type="button" data-jvm-panel-close="" className={`ml-auto ${btnSecondary}`} onClick={onClose}>
          Close
        </button>
      </header>
      <JVMTab key={open} kind="pods" cluster={cluster} namespace={namespace} name={name} />
    </aside>
  );
}

/** Each group with as many of its rows as the budget has left, in order;
 *  a group with none left is not drawn. */
function budgeted(groups: readonly Group[], budget: number): (Group & { shownPods: Pod[] })[] {
  const out: (Group & { shownPods: Pod[] })[] = [];
  let left = budget;
  for (const g of groups) {
    const pods: Pod[] = [];
    for (const pod of g.pods) {
      if (left <= 0) break;
      const containers = pod.containers.slice(0, left);
      left -= containers.length;
      pods.push(containers.length === pod.containers.length ? pod : { ...pod, containers });
    }
    if (pods.length > 0) out.push({ ...g, shownPods: pods });
  }
  return out;
}

/** Why no working set is drawn, where none is: each failure its own words. */
const USAGE_SAID: Readonly<Record<string, string>> = {
  absent: 'Working set: no metrics-server',
  refused: 'Working set: metrics-server read refused',
  unreachable: 'Working set: metrics-server unreachable',
  forbidden: 'Working set: not allowed',
  failed: 'Working set: unreadable',
};

function UsageLine({ usage }: { usage: Usage | undefined }): ReactNode {
  if (usage === undefined) return null;
  if (usage.state === 'read') {
    return usage.complete || usage.note === undefined ? null : (
      <p data-jvm-usage="partial" className="text-xs text-muted">
        Working sets: {usage.note}
      </p>
    );
  }
  return (
    <p data-jvm-usage={usage.state} className="text-xs text-muted" title={usage.detail}>
      {USAGE_SAID[usage.state] ?? USAGE_SAID.failed}
    </p>
  );
}

/** The bar's key, once above the table. */
function Legend(): ReactNode {
  const swatch = 'inline-block h-2.5 w-3 rounded-sm';
  return (
    <p data-jvm-legend="" className="flex flex-wrap items-center gap-x-3 gap-y-1 text-2xs text-muted">
      <span className="flex items-center gap-1">
        <span className={`${swatch} bg-ok`} /> max heap
      </span>
      <span className="flex items-center gap-1">
        <span className={`${swatch} border border-line bg-white/10`} /> room outside the heap
      </span>
      <span className="flex items-center gap-1">
        <span className="inline-block h-3 w-0.5 bg-fg" /> limit
      </span>
      <span className="flex items-center gap-1">
        <span className="inline-block h-3 w-0 border-l border-dashed border-warn" /> 75 %
      </span>
      <span className="flex items-center gap-1">
        <span className="inline-block size-2 rotate-45 bg-accent" /> working set
      </span>
      <span className="flex items-center gap-1">
        <span className="inline-block h-1.5 w-0.5 bg-muted" /> request
      </span>
    </p>
  );
}

/** The heap's fill, by its grade. */
const HEAP_FILL: Readonly<Record<string, string>> = {
  ok: 'bg-ok/70',
  warning: 'bg-warn/80',
  error: 'bg-bad/80',
  nolimit: 'bg-muted/60',
  unknown: 'bg-muted/60',
};

const pct = (v: number) => `${String(v)}%`;

/**
 * One Java container's memory, on one track: the heap's fill from zero, the
 * room the JVM has outside it up to the limit, the limit itself, the 75 %
 * line, the request as a tick under the track and the working set as a mark
 * when metrics-server measured it. A heap past the limit pushes the limit line
 * in from the end and is drawn red past it: the picture of an OOMKill.
 */
function MemoryBar({ c }: { c: Container }): ReactNode {
  const bar = heapBar(c);
  const level = c.risk?.level ?? 'unknown';
  const used = c.workingSet ?? null;
  const margin = c.heap?.bytes != null && c.limit !== null ? c.limit.bytes - c.heap.bytes : null;
  return (
    <div data-jvm-bar="" className="flex min-w-64 flex-col gap-1">
      <div className="relative h-3 rounded-sm bg-panel-2">
        {bar.limit === null ? null : (
          <span data-jvm-room="" className="absolute inset-y-0 left-0 rounded-sm border border-line bg-white/10" style={{ width: pct(bar.limit) }} />
        )}
        {bar.heap === null ? null : (
          <span
            data-jvm-heap=""
            className={`absolute inset-y-0 left-0 rounded-l-sm ${HEAP_FILL[level] ?? 'bg-muted/60'}`}
            style={{ width: pct(bar.heap) }}
            title={c.heap?.source}
          />
        )}
        {bar.limit !== null && bar.heap !== null && bar.heap > bar.limit ? (
          <span
            data-jvm-over=""
            className="absolute inset-y-0 bg-bad"
            style={{ left: pct(bar.limit), width: pct(bar.heap - bar.limit) }}
            title="The heap may grow past the memory limit"
          />
        ) : null}
        {bar.threshold === null ? null : (
          <span
            data-jvm-mark="threshold"
            className="absolute -inset-y-0.5 w-0 border-l border-dashed border-warn"
            style={{ left: pct(bar.threshold) }}
            title="75 % of the memory limit"
          />
        )}
        {bar.limit === null ? null : (
          <span
            data-jvm-mark="limit"
            className="absolute -inset-y-1 -ml-px w-0.5 bg-fg"
            style={{ left: pct(bar.limit) }}
            title={`Memory limit: ${c.limit?.text ?? ''}`}
          />
        )}
        {bar.request === null ? null : (
          <span
            data-jvm-mark="request"
            className="absolute -bottom-1.5 -ml-px h-1.5 w-0.5 bg-muted"
            style={{ left: pct(bar.request) }}
            title={`Memory request: ${c.request?.text ?? ''}`}
          />
        )}
        {bar.workingSet === null ? null : (
          <span
            data-jvm-mark="working-set"
            className="absolute top-1/2 -ml-1 size-2 -translate-y-1/2 rotate-45 border border-bg bg-accent"
            style={{ left: pct(bar.workingSet) }}
            title={`Working set now: ${used?.text ?? ''}`}
          />
        )}
      </div>
      <div className="flex flex-wrap gap-x-2 text-2xs text-muted">
        <span title={c.heap?.source}>
          heap{' '}
          {c.heap === null || c.heap.text === '' ? (
            'sized on the node'
          ) : (
            <span className="text-fg">{c.heap.text}</span>
          )}
        </span>
        {margin === null ? null : margin > 0 ? (
          <span>· {formatBytes(margin)} outside it</span>
        ) : margin === 0 ? (
          <span className="text-bad">· no room outside it</span>
        ) : (
          <span className="text-bad">· {formatBytes(-margin)} over the limit</span>
        )}
        <span>· limit {c.limit === null ? 'none' : <span className="text-fg">{c.limit.text}</span>}</span>
        {c.request === null ? null : <span>· request {c.request.text}</span>}
        <span data-jvm-used={used === null ? 'none' : String(used.bytes)}>
          · used{' '}
          {used === null ? (
            'not measured'
          ) : (
            <span className="text-accent">
              {used.text}
              {c.limit === null ? '' : ` (${String(Math.round((used.bytes / c.limit.bytes) * 100))} %)`}
            </span>
          )}
        </span>
      </div>
    </div>
  );
}

/** Bytes in binary units, one decimal, as the Go half's textutil.Bytes. */
function formatBytes(n: number): string {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return i === 0 ? `${String(v)} B` : `${v.toFixed(1)} ${units[i] ?? ''}`;
}

/** The hint's source as one word on its pill. */
const SOURCE_WORD: Readonly<Record<string, string>> = { image: 'image', command: 'command', env: 'env' };

function Row({
  cluster,
  pod,
  c,
  first,
  grouping,
  picked,
  onOpen,
}: {
  cluster: string;
  pod: Pod;
  c: Container;
  first: boolean;
  grouping: Grouping;
  picked: boolean;
  onOpen: (pod: string) => void;
}): ReactNode {
  const href = `/c/${encodeURIComponent(cluster)}/pods/${encodeURIComponent(pod.namespace)}/${encodeURIComponent(pod.name)}`;
  const owner = pod.owner ?? null;
  return (
    <tr
      data-jvm-container={`${pod.namespace}/${pod.name}/${c.name}`}
      aria-selected={picked}
      className={`${ROW} cursor-pointer ${picked ? 'bg-panel-2' : ''}`}
      onClick={(e) => {
        if (!(e.target instanceof Element) || e.target.closest('a,button') === null) onOpen(podId(pod));
      }}
    >
      <td className={`${bodyCell(true)} align-top`}>
        {first ? (
          <>
            <span className="flex items-center gap-1">
              <Link to={href} title={pod.name} className={`block max-w-48 truncate text-accent hover:underline ${focusRing}`}>
                {pod.name}
              </Link>
              <button
                type="button"
                data-jvm-open=""
                className={`shrink-0 rounded-full border border-line bg-white/5 px-1.5 py-px text-2xs font-semibold text-muted hover:text-fg ${focusRing}`}
                title={`Open the JVM tab of ${pod.name} beside the list`}
                onClick={() => onOpen(podId(pod))}
              >
                JVM
              </button>
            </span>
            <span className="block max-w-48 truncate text-2xs text-muted">
              {grouping === 'namespace' ? (owner === null ? 'no owner' : `${owner.kind} ${owner.name}`) : pod.namespace}
            </span>
          </>
        ) : null}
      </td>
      <td className={`${bodyCell(true)} align-top`}>
        <span className="block max-w-48 truncate" title={c.name}>
          {c.name}
        </span>
        <ImageRef image={c.image} of={c.name} className="max-w-56" />
      </td>
      <td className={`${bodyCell(true)} wrap align-top`}>
        <MemoryBar c={c} />
      </td>
      <td className={`${bodyCell(true)} wrap align-top text-xs`}>
        {c.risk === null ? null : (
          <>
            <Pill tone={riskTone(c.risk.level)} neutral="muted" title={c.risk.sentence}>
              {c.risk.percent === null ? riskWord(c.risk) : `${riskWord(c.risk)} · ${String(c.risk.percent)} %`}
            </Pill>
            {c.notes.length === 0 ? null : (
              <span data-jvm-notes="" className="ml-1 cursor-help text-muted underline decoration-dotted" title={c.notes.join('\n')}>
                {c.notes.length === 1 ? 'note' : `${String(c.notes.length)} notes`}
              </span>
            )}
          </>
        )}
      </td>
      <td className={`${bodyCell(true)} wrap align-top text-xs`}>
        <span className="flex flex-wrap gap-1">
          {c.hints.map((h) => (
            <Pill key={h.reason} neutral="muted" title={h.reason}>
              {SOURCE_WORD[h.source] ?? h.source}
            </Pill>
          ))}
        </span>
      </td>
    </tr>
  );
}
