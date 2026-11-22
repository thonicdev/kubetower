import { useRef, type ReactNode } from 'react';
import { Link } from 'react-router';
import { CopyButton, Pill, Windowed, focusRing, btnSecondary, TABLE, HEAD_ROW, ROW, headCell, bodyCell } from '@kubetower/ui';

import {
  shownSuggestions,
  type Category,
  type Container,
  type NamespaceCard,
  type NodeCard,
  type NodeState,
  type Page,
  type Row,
  type Volume,
  type WorkloadCard,
} from './data';
import { reasonLabel } from './reasons';
import { BAND_FILL, BAND_TEXT, Bar, KIND_EDGE, KIND_TONE, PodTrack, ResourceBar, bandText } from './parts';

// --- Namespaces: what to act on, then where the waste is ---------------------

/** A namespace's band as the color of its word. */
const BAND_WORD_TEXT: Readonly<Record<string, string>> = {
  overcommit: 'text-bad',
  high: 'text-warn',
  ok: 'text-ok',
  norequests: 'text-warn',
  '': 'text-muted',
};

/** Three answers where it is tempting to draw one: a band, "no requests set"
 *  (measured, and the scheduler places these pods blind) and "not measured".
 *  The bands are request ÷ usage, so they are over-requesting, not
 *  "overcommit" - which in Kubernetes means limits past what a node has. */
function bandWord(card: NamespaceCard): string {
  switch (card.band) {
    case 'overcommit':
      return 'far over-requested';
    case 'high':
      return 'over-requested';
    case 'ok':
      return 'about right';
    case 'norequests':
      return 'no requests set';
    default:
      return 'not measured';
  }
}

/**
 * The overview: what to act on first, across the cluster, then the namespaces
 * as one table ranked by how far their requests are from their use, with what
 * each reserves and leaves idle, so the reader need not open each namespace to
 * find a single finding.
 */
export function OverviewView({ cluster, page }: { cluster: string; page: Page }): ReactNode {
  const cards = page.namespaceCards ?? [];
  return (
    <div className="flex flex-col gap-4" data-body="overview">
      <TopFindings rows={page.top ?? []} />
      <section aria-label="Namespaces" className="flex flex-col gap-2">
        <h2 className="text-sm font-medium">Namespaces</h2>
        {cards.length === 0 ? (
          <p className="rounded-lg border border-line bg-panel p-4 text-sm text-muted">No namespace in this cluster reported any usage.</p>
        ) : (
          <div className="overflow-x-auto rounded-lg border border-line bg-panel">
            <table className={`${TABLE} w-full tabular-nums`}>
              <thead>
                <tr className={HEAD_ROW}>
                  <th className={headCell()}>Namespace</th>
                  <th className={headCell()} title="How far the requests are from the use, the furthest first">Requests vs use</th>
                  <th className={`${headCell()} text-right`} title="CPU requested ÷ CPU used, over the containers that reported usage">CPU ratio</th>
                  <th className={`${headCell()} text-right`}>CPU requested</th>
                  <th className={`${headCell()} text-right`} title="Requested and not used, over the containers that reported usage">CPU idle</th>
                  <th className={`${headCell()} text-right`} title="Memory requested ÷ memory used, over the containers that reported usage">Memory ratio</th>
                  <th className={`${headCell()} text-right`}>Memory requested</th>
                  <th className={`${headCell()} text-right`} title="Requested and not used, over the containers that reported usage">Memory idle</th>
                  <th className={`${headCell()} text-right`}>Findings</th>
                </tr>
              </thead>
              <tbody>
                {cards.map((card) => (
                  <tr key={card.name} data-namespace-card={card.name} data-band={card.band} className={`${ROW} last:border-b-0 hover:bg-panel-2`}>
                    <td className={`${bodyCell()} wrap`}>
                      <Link
                        to={{ search: `?ns=${encodeURIComponent(card.name)}` }}
                        aria-label={`${card.name} on ${cluster}`}
                        className={`font-semibold break-all text-accent hover:underline ${focusRing}`}
                      >
                        {card.name}
                      </Link>
                      <span className="ml-2 text-2xs text-muted">
                        {card.workloads} workloads · {card.pods} pods
                      </span>
                      {card.coverage === '' ? null : (
                        <span
                          className="block text-2xs text-muted"
                          title="The figures beside are over the containers that reported usage. The rest are counted in what this namespace asks for and cannot be counted in what it uses."
                        >
                          {card.coverage}
                        </span>
                      )}
                    </td>
                    <td data-band-word="" className={`${bodyCell()} ${BAND_WORD_TEXT[card.band] ?? 'text-muted'}`}>
                      {bandWord(card)}
                    </td>
                    <td className={`${bodyCell()} text-right`}>{card.cpuRatio}</td>
                    <td className={`${bodyCell()} text-right`}>{card.cpuRequest}</td>
                    <td data-col="cpu-idle" className={`${bodyCell()} text-right text-muted`}>
                      {card.cpuIdle === '' ? '—' : card.cpuIdle}
                    </td>
                    <td className={`${bodyCell()} text-right`}>{card.memRatio}</td>
                    <td className={`${bodyCell()} text-right`}>{card.memRequest}</td>
                    <td data-col="mem-idle" className={`${bodyCell()} text-right text-muted`}>
                      {card.memIdle === '' ? '—' : card.memIdle}
                    </td>
                    <td className={`${bodyCell()} text-right`}>
                      {card.findings > 0 && card.worst !== '' ? (
                        <Pill tone={KIND_TONE[card.worst]} neutral="muted">
                          {card.findings}
                        </Pill>
                      ) : (
                        <span className="text-muted">0</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  );
}

/** What to act on first: the risks, then the reductions that give back the
 *  most, each with the namespace where its card is. */
function TopFindings({ rows }: { rows: Row[] }): ReactNode {
  return (
    <section aria-label="Act on first" data-top="" className="flex flex-col gap-2">
      <h2 className="text-sm font-medium">Act on first</h2>
      {rows.length === 0 ? (
        <p className="rounded-lg border border-line bg-panel p-3 text-sm text-muted">Nothing to change in this cluster.</p>
      ) : (
        <ul className="flex flex-col divide-y divide-line rounded-lg border border-line bg-panel">
          {rows.map((row, i) => (
            <li
              key={`${row.namespace}/${row.workload}/${row.container}/${row.resource}/${row.action}/${i}`}
              data-top-row={row.kind}
              className={`flex flex-wrap items-baseline gap-x-3 gap-y-1 border-l-2 px-3 py-2 text-xs ${KIND_EDGE[row.kind] ?? 'border-l-line'}`}
            >
              <span className="w-32 shrink-0">
                <Pill tone={KIND_TONE[row.kind]} neutral="muted">
                  {row.action}
                </Pill>
              </span>
              <span className="min-w-0 break-words">
                <strong className="font-semibold">{row.workload}</strong> · {row.container} · {row.resource}
              </span>
              <Link to={{ search: `?ns=${encodeURIComponent(row.namespace)}` }} className={`text-accent hover:underline ${focusRing}`}>
                {row.namespace}
              </Link>
              <span className="ml-auto" />
              <Basis row={row} />
              <span className="w-40 shrink-0 text-right tabular-nums">
                {row.current} → <strong>{suggestedOf(row)}</strong>
              </span>
              <span className="basis-full text-muted" title={row.message}>
                {reasonLabel(row)}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

// --- Nodes: what the machines have --------------------------------------------

/**
 * The count tiles, each a filter: one per state the server places a node in.
 * Clicking narrows the cards to that state; clicking it again clears. A zero
 * is drawn as a zero; a count nobody could establish is drawn as an em dash
 * and cannot be picked.
 */
function Tiles({
  states,
  picked,
  onPick,
  partial,
}: {
  states: NodeState[];
  picked: string;
  onPick: (state: string) => void;
  /** The page's partial sentence: when set, an unknown count was withheld,
   *  not unread, and the tooltip says which. */
  partial: string;
}): ReactNode {
  return (
    <section aria-label="Nodes by state" data-tiles="" className="flex flex-col gap-2 rounded-lg border border-line bg-panel p-3">
      <div className="flex items-center gap-2">
        <h2 className="flex-1 text-sm font-medium">Nodes by state</h2>
        {picked === '' ? null : (
          <button
            type="button"
            onClick={() => onPick('')}
            className={btnSecondary}
          >
            All
          </button>
        )}
      </div>
      <ul className="flex flex-wrap gap-2">
        {states.map((tile) => {
          const on = picked === tile.key;
          const known = tile.count !== null;
          return (
            <li key={tile.key}>
              <button
                type="button"
                data-tile={tile.key}
                data-count={known ? String(tile.count) : 'unknown'}
                aria-pressed={on}
                disabled={!known}
                title={
                  known
                    ? `Show only the nodes that are ${tile.label.toLowerCase()}`
                    : partial !== ''
                      ? 'Withheld: you see only some of the pods on each node, so no node is placed in a state'
                      : 'Nobody could say: no node was measured, or none could be read - see the notes above'
                }
                onClick={() => onPick(on ? '' : tile.key)}
                className={`flex items-baseline gap-1.5 rounded-md border px-2.5 py-1 disabled:cursor-not-allowed ${
                  on ? 'border-accent bg-panel-2' : 'border-line hover:bg-panel-2'
                } ${focusRing}`}
              >
                <span className={`text-2xl font-semibold tabular-nums ${known && tile.count !== 0 ? (BAND_TEXT[tile.key] ?? 'text-fg') : 'text-muted'}`}>
                  {known ? tile.count : '—'}
                </span>
                <span className="text-xs text-muted">{tile.label}</span>
              </button>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

export function NodesView({ page, picked, onPick }: { page: Page; picked: string; onPick: (state: string) => void }): ReactNode {
  const all = page.nodeCards ?? [];
  const states = page.nodeStates ?? [];
  // A pick is applied only on a tile whose count is known. A bookmarked
  // `state=danger` on a cluster nobody could measure would otherwise say "No
  // node is near capacity" under an unknown - an absence claimed where nobody
  // could look.
  const pickedTile = states.find((s) => s.key === picked);
  const applied = pickedTile !== undefined && pickedTile.count !== null ? picked : '';
  const shown = applied === '' ? all : all.filter((card) => card.state === applied);
  return (
    <div className="flex flex-col gap-3" data-body="nodes">
      {/* Narrowed, the partial sentence is in the sources line just above
          this view (once, not twice); the tiles say their counts are withheld. */}
      {states.length === 0 ? null : <Tiles states={states} picked={applied} onPick={onPick} partial={page.partial} />}
      {picked !== '' && applied === '' ? (
        <p data-filter-ignored={picked} className="text-xs text-muted">
          The filter {pickedTile === undefined ? `“${picked}”` : `“${pickedTile.label}”`} is not applied: nobody could say which nodes
          are in that state, so every node read is shown.{' '}
          <button
            type="button"
            onClick={() => onPick('')}
            className={btnSecondary}
          >
            Clear
          </button>
        </p>
      ) : null}
      <div className="grid grid-cols-1 gap-3 xl:grid-cols-2">
        <Windowed items={shown} more={(button) => <div className="col-span-full text-center">{button}</div>}>
          {(cards) => cards.map((card) => <NodeCardView key={card.name} card={card} />)}
        </Windowed>
        {page.nodeCards === null ? (
          <section data-nodes="unread" className="col-span-full rounded-lg border border-line bg-panel p-4 text-sm text-muted">
            No node capacity could be read — see the Node capacity note above.
          </section>
        ) : all.length === 0 ? (
          <section data-nodes="none" className="col-span-full rounded-lg border border-line bg-panel p-4 text-sm text-muted">
            This cluster answered with no node.
          </section>
        ) : shown.length === 0 ? (
          <section className="col-span-full rounded-lg border border-line bg-panel p-4 text-sm text-muted">
            No node is {pickedTile === undefined ? 'in that state' : pickedTile.label.toLowerCase()}.
          </section>
        ) : null}
      </div>
    </div>
  );
}

const TAINT: Readonly<Record<string, string>> = {
  NoSchedule: 'border-bad/45 bg-bad/10 text-bad',
  NoExecute: 'border-bad bg-bad/20 text-bad',
  PreferNoSchedule: 'border-warn/40 bg-warn/10 text-warn',
};

function NodeCardView({ card }: { card: NodeCard }): ReactNode {
  return (
    <section
      data-node={card.name}
      data-state={card.state}
      className={`flex flex-col gap-2.5 rounded-lg border bg-panel px-4 py-3.5 ${card.ready ? 'border-line' : 'border-bad/45'} ${
        card.controlPlane ? 'border-l-4 border-l-accent' : ''
      }`}
    >
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <h2 className="text-base font-bold break-all">{card.name}</h2>
        {card.ready ? <Pill tone="ok">Ready</Pill> : <Pill tone="bad">NotReady</Pill>}
        {card.roles.map((role) => (
          <span key={role} className={`rounded-sm bg-panel-2 px-1.5 py-px text-2xs font-semibold tracking-wide uppercase ${card.controlPlane ? 'text-accent' : 'text-muted'}`}>
            {role}
          </span>
        ))}
        {card.maxPods > 0 ? (
          <span title="Running pods against this node's pod ceiling">
            <Pill tone={card.podsTight ? 'warn' : undefined} neutral="muted">
              {card.pods} / {card.maxPods} pods
            </Pill>
          </span>
        ) : (
          <Pill neutral="muted">{card.pods} pods</Pill>
        )}
      </div>
      <div className="flex flex-wrap items-baseline gap-x-3.5 gap-y-1 text-2xs text-muted">
        {card.age === '' ? null : <span title="How long this node has existed">{card.age}</span>}
        {card.kubelet === '' ? null : <span title="kubelet version">{card.kubelet}</span>}
        {card.osImage === '' ? null : (
          <span className="max-w-64 truncate" title={card.osImage}>
            {card.osImage}
          </span>
        )}
        {card.kernel === '' ? null : (
          <span className="max-w-64 truncate" title={`kernel ${card.kernel}`}>
            kernel {card.kernel}
          </span>
        )}
      </div>
      {card.pressures.length === 0 && card.taints.length === 0 ? null : (
        <div className="flex flex-wrap gap-1.5">
          {card.pressures.map((p) => (
            <span key={p} className="rounded-sm border border-bad/45 bg-bad/10 px-1.5 py-px text-2xs font-bold tracking-wide text-bad uppercase">
              {p}
            </span>
          ))}
          {card.taints.map((t) => (
            <span
              key={t.title}
              data-taint={t.effect}
              title={t.title}
              className={`max-w-80 truncate rounded-sm border px-1.5 py-px text-2xs font-semibold ${TAINT[t.effect] ?? 'border-line bg-panel-2 text-muted'}`}
            >
              {t.text} · {t.effect}
            </span>
          ))}
        </div>
      )}
      {card.ready ? (
        <>
          <div className="flex flex-col gap-3 border-t border-line pt-2.5">
            {card.resources.map((r) => (
              <ResourceBar key={r.key} resource={r} />
            ))}
          </div>
          {card.topPods.length === 0 ? null : (
            <details className="border-t border-line pt-2.5">
              <summary className={`cursor-pointer text-2xs text-muted hover:text-fg ${focusRing}`}>
                Busiest pods{card.more === '' ? null : <span className="text-muted"> — {card.more}</span>}
              </summary>
              <div className="mt-2 flex flex-col gap-1">
                <div aria-hidden="true" className="grid grid-cols-[minmax(0,1fr)_minmax(--spacing(20),0.6fr)_minmax(--spacing(20),0.6fr)] gap-x-3 border-b border-line pb-0.5 text-2xs font-bold tracking-wide text-muted uppercase">
                  <span>pod</span>
                  <span>CPU</span>
                  <span>memory</span>
                </div>
                {card.topPods.map((pod) => (
                  <div key={pod.title} title={pod.title} data-top-pod={pod.name} className="grid grid-cols-[minmax(0,1fr)_minmax(--spacing(20),0.6fr)_minmax(--spacing(20),0.6fr)] items-center gap-x-3 text-2xs">
                    <span className="min-w-0 truncate text-muted">{pod.name}</span>
                    <PodTrack track={pod.cpu} />
                    <PodTrack track={pod.mem} />
                  </div>
                ))}
              </div>
            </details>
          )}
        </>
      ) : (
        <p className="border-t border-line pt-2.5 text-xs text-muted">
          This node is not ready, so nothing on it was measured and its capacity is not available to anything.
        </p>
      )}
    </section>
  );
}

// --- One namespace: its workloads, and the advice beside them ------------------

/** `asked` is the filter in the address, already checked: the page applies it
 *  to the answer it holds, so a chip reads nothing from the cluster. */
export function NamespaceView({ page, asked, onCategory }: { page: Page; asked: Category; onCategory: (cat: Category) => void }): ReactNode {
  const cards = page.cards ?? [];
  return (
    <div className="flex flex-col items-start gap-4 xl:flex-row" data-body="namespace">
      <div className="flex w-full min-w-0 flex-1 flex-col gap-3.5">
        {cards.map((card) => (
          <WorkloadCardView key={`${card.kind}/${card.workload}`} card={card} />
        ))}
        {cards.length === 0 ? (
          <section className="rounded-lg border border-line bg-panel p-4 text-sm text-muted">No running workload in this namespace reported any usage.</section>
        ) : null}
      </div>
      <Suggestions page={page} asked={asked} onCategory={onCategory} />
    </div>
  );
}

/** One workload, drawn whole: the namespace view's card, and the workload tab's. */
export function WorkloadCardView({ card }: { card: WorkloadCard }): ReactNode {
  return (
    <section
      data-workload={card.workload}
      data-worst={card.worst}
      className={`rounded-lg border border-line bg-panel ${card.worst === 'danger' || card.worst === 'warning' ? `border-l-4 ${KIND_EDGE[card.worst]}` : ''}`}
    >
      <div className="flex flex-wrap items-center gap-2 border-b border-line px-4 py-2.5">
        <h2 className="text-base font-semibold break-all">{card.workload}</h2>
        <span className="text-xs text-muted">{card.kind}</span>
        {card.replicas > 1 ? <Pill neutral="muted">{card.replicas} replicas</Pill> : null}
        {card.qos === '' ? null : (
          <span title="QoS class assigned by the API server">
            <Pill neutral="muted">{card.qos}</Pill>
          </span>
        )}
        {card.worst === '' ? (
          <Pill tone="ok">sized about right</Pill>
        ) : (
          <Pill tone={KIND_TONE[card.worst]} neutral="muted">
            {card.findings.length} finding(s)
          </Pill>
        )}
      </div>
      {card.managed.length === 0 ? null : (
        <ul data-managed="" className="flex flex-col gap-0.5 border-b border-line px-4 py-2 text-xs text-muted">
          {card.managed.map((m) => (
            <li key={m}>{m}</li>
          ))}
        </ul>
      )}
      <div className="flex flex-wrap gap-4.5 px-4 py-3.5">
        {card.containers.map((c) => (
          <ContainerView key={c.name} container={c} />
        ))}
      </div>
      {card.volumes.length > 0 ? (
        <div className="border-t border-dashed border-line px-4 pb-3.5">
          <div className="mt-2.5 mb-2 text-2xs font-bold tracking-wider text-muted uppercase">Volumes</div>
          {card.volumes.map((v) => (
            <VolumeView key={`${v.pod}/${v.name}`} volume={v} />
          ))}
        </div>
      ) : card.storageOK ? null : (
        <p data-disks="unreadable" className="border-t border-dashed border-line px-4 pt-2.5 pb-3.5 text-xs text-muted">
          Disks were not readable on this cluster, so nothing is said about this workload&apos;s volumes.
        </p>
      )}
    </section>
  );
}

function ContainerView({ container: c }: { container: Container }): ReactNode {
  return (
    <div data-container={c.name} className="flex min-w-64 max-w-xl flex-1 basis-80 flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2 text-sm font-semibold">
        <span>{c.name}</span>
        {c.oomKilled ? (
          <span title="Killed for memory — no reduction will be suggested for it">
            <Pill tone="bad">OOMKilled</Pill>
          </span>
        ) : null}
        {c.oomKilledEarlier ? (
          <span title="Killed for memory more than a day ago. No alarm for that now, but no memory reduction is suggested either.">
            <Pill tone="warn">OOMKilled earlier</Pill>
          </span>
        ) : null}
        {c.memoryRaised ? (
          <span
            data-memory-raised
            title={`The memory limit was raised from ${c.memoryRaised.from} to ${c.memoryRaised.to} at revision ${c.memoryRaised.revision}. The pods killed before that rollout are gone, so no reduction of this limit is suggested while that raise is its newest change.`}
          >
            <Pill tone="warn">limit raised from {c.memoryRaised.from}</Pill>
          </span>
        ) : null}
        {c.crashLooping ? (
          <span title="In CrashLoopBackOff, or restarted in the last ten minutes. A dying container's usage says what it managed to allocate before it died, so no reduction is suggested for it.">
            <Pill tone="bad">crash-looping · {c.restarts} restart(s), no advice</Pill>
          </span>
        ) : c.restarts > 0 ? (
          <span title="Restarts observed. Not currently a crash loop, so the suggestions below still apply.">
            <Pill tone="warn">{c.restarts} restart(s)</Pill>
          </span>
        ) : null}
      </div>
      <Bar bar={c.cpu} />
      <Bar bar={c.memory} />
      {/* Disk only when the kubelet answered for it: an empty bar here would be a
          third thing that looks like a container using nothing. */}
      {c.ephemeral.measured ? <Bar bar={c.ephemeral} /> : null}
      {c.pods.length > 1 ? (
        <details>
          <summary className={`cursor-pointer text-xs text-muted hover:text-fg ${focusRing}`}>{c.pods.length} pods</summary>
          <table className={`${TABLE} mt-1.5 w-full`}>
            <thead>
              <tr className={HEAD_ROW}>
                <th className={headCell()}>Pod</th>
                <th className={`${headCell()} text-right`}>CPU</th>
                <th className={`${headCell()} text-right`}>Memory</th>
              </tr>
            </thead>
            <tbody>
              {c.pods.map((p) => (
                <tr key={p.pod} data-pod={p.pod} className={ROW}>
                  <td className={`${bodyCell(true)} wrap`}>{p.pod}</td>
                  {p.measured ? (
                    <>
                      <td className={`${bodyCell(true)} text-right tabular-nums`}>{p.cpu}</td>
                      <td className={`${bodyCell(true)} text-right tabular-nums`}>{p.memory}</td>
                    </>
                  ) : (
                    <td colSpan={2} className={`${bodyCell(true)} text-right text-muted`}>
                      not measured
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      ) : null}
    </div>
  );
}

function VolumeView({ volume: v }: { volume: Volume }): ReactNode {
  return (
    <div data-volume={v.name} data-measured={v.measured} className="mb-2.5">
      <div className="flex flex-wrap items-baseline gap-1.5 text-xs">
        <span className="rounded-xs bg-accent/20 px-1 py-px text-2xs font-bold tracking-wide text-ka-over uppercase">{v.kind}</span>
        <span className="font-semibold break-all">{v.name}</span>
        {v.claim === '' ? null : (
          <span className="text-2xs break-all text-muted" title="The claim this volume binds to">
            {v.claim}
          </span>
        )}
        <span className="text-2xs break-all text-muted">{v.pod}</span>
        {v.unbounded ? (
          <span title="An emptyDir with no sizeLimit can fill the node's disk and evict every pod on it, whatever it is using today">
            <Pill tone="warn">no sizeLimit</Pill>
          </span>
        ) : null}
        {v.pct === '' ? null : (
          <span className={`ml-auto font-semibold tabular-nums ${bandText(v.status)}`} title={v.pctOf}>
            {v.pct}
          </span>
        )}
      </div>
      <div className="relative mt-1 h-2 overflow-hidden rounded-full bg-white/10">
        {v.usePct === null ? null : (
          <div data-fill="" className={`h-full ${BAND_FILL[v.status] ?? 'bg-muted'}`} style={{ width: `${v.usePct}%` }} />
        )}
      </div>
      <div className="mt-1 text-2xs tabular-nums text-muted">
        {v.measured ? (
          <>
            used {v.used}
            {v.free === '' ? null : <> · {v.free} free</>}
          </>
        ) : (
          <span className="text-warn">usage not measured — see the Disk note above</span>
        )}
        {v.capacity === '' ? null : (
          <>
            {' '}
            · {v.ceilingWord} {v.capacity}
          </>
        )}
      </div>
    </div>
  );
}

/**
 * The findings, beside the numbers they are about. Three filters, counted over
 * everything; the suggestions grouped by severity; and every command in one
 * block to review and copy - one block and not a button per row, because what
 * an operator wants before running any of this is the whole change in front of
 * them, and each line restarts every pod of its workload.
 */
function Suggestions({ page, asked, onCategory }: { page: Page; asked: Category; onCategory: (cat: Category) => void }): ReactNode {
  // Every suggestion, before the filter, as the counts are.
  const total = page.counts.reduce((n, c) => n + (c.count ?? 0), 0);
  const block = useRef<HTMLPreElement | null>(null);
  const { groups, commands } = shownSuggestions(page, asked);
  const text = commands.join('\n');
  return (
    <aside aria-label="Suggestions" className="w-full shrink-0 xl:sticky xl:top-3 xl:w-96">
      <section className="flex flex-col rounded-lg border border-line bg-panel">
        <div className="flex items-center gap-2 border-b border-line px-3.5 py-2.5">
          <h2 className="flex-1 text-base font-semibold">Suggestions</h2>
          <span className="text-xs tabular-nums text-muted">{total}</span>
        </div>
        <div className="flex flex-wrap gap-1.5 px-3.5 pt-2.5" data-categories="">
          {page.categories.map((c) => {
            const on = asked === c.key;
            return (
              <button
                key={c.key}
                type="button"
                aria-pressed={on}
                data-category={c.key}
                onClick={() => onCategory(on ? '' : c.key)}
                className={`inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-2xs font-semibold ${
                  on ? 'border-accent bg-accent/20 text-fg' : 'border-line bg-panel-2 text-muted hover:border-accent hover:text-fg'
                } ${c.count === null || c.count === 0 ? 'opacity-45' : ''} ${focusRing}`}
              >
                {c.label}
                <span className="tabular-nums opacity-80">{c.count ?? '—'}</span>
              </button>
            );
          })}
        </div>
        {groups.length > 0 ? (
          <div className="flex flex-col pb-3">
            {groups.map((g) => (
              <div key={g.kind} data-group={g.kind} className="mt-3">
                <div className={`flex items-baseline gap-2 px-3.5 text-xs font-bold tracking-wide uppercase ${BAND_TEXT[g.kind] ?? ''}`}>
                  <span aria-hidden="true">{g.mark}</span>
                  <span className="flex-1">{g.label}</span>
                  <span className="tabular-nums">{g.rows.length}</span>
                </div>
                <ul className="flex flex-col gap-3.5 px-3.5 pt-2">
                  {g.rows.map((row, i) => (
                    <Finding key={`${row.workload}/${row.container}/${row.resource}/${row.action}/${i}`} row={row} />
                  ))}
                </ul>
              </div>
            ))}
            {commands.length > 0 ? (
              <details data-commands="" className="mx-3.5 mt-4 rounded-md border border-line bg-panel-2 p-2.5">
                <summary
                  className={`cursor-pointer text-xs font-semibold ${focusRing}`}
                  title="To review and run yourself"
                >
                  All {commands.length} {commands.length === 1 ? 'command' : 'commands'}
                </summary>
                <pre ref={block} className="mt-2 overflow-x-auto font-mono text-2xs whitespace-pre select-all">
                  {text}
                </pre>
                <div className="mt-2 flex items-center gap-2">
                  <CopyButton value={text} label="Copy all" selectRef={block} />
                  <span data-restarts="" className="text-2xs text-warn" title="Each of these restarts every pod of its workload. KubeTower does not run them.">
                    Restarts pods
                  </span>
                </div>
              </details>
            ) : null}
          </div>
        ) : asked !== '' ? (
          <p
            className="px-3.5 py-3 text-xs text-muted"
            title="Other categories may still have findings: the counts above are over all of them."
          >
            Nothing under this filter
          </p>
        ) : (
          <p className="px-3.5 py-3 text-xs text-muted">Nothing to change</p>
        )}
      </section>
    </aside>
  );
}

/** The confidence pill's own column: one width, so the pills line up down a list. */
const CONFIDENCE_CELL = 'inline-block w-40 shrink-0';

const CONFIDENCE_TITLE: Record<Exclude<Row['confidence'], ''>, string> = {
  high: 'Sized on a week of usable usage from Prometheus: every daily peak, and one of each weekday.',
  medium: 'Sized on a day to a week of usable usage from Prometheus: a weekly peak may not be in it.',
  low: 'Sized on a single metrics-server scrape per replica: one instant says nothing about the hours around it.',
};

/** How much a row's figure rests on: its confidence, or that there is too little to name one. */
function Basis({ row }: { row: Row }): ReactNode {
  if (row.insufficientData) {
    return (
      <span data-confidence="insufficient" className={CONFIDENCE_CELL}>
        <Pill tone="warn" title="Under a day of usable usage: the finding stands, and no value is recommended until there is a day of it.">
          insufficient data
        </Pill>
      </span>
    );
  }
  if (row.confidence === '') {
    return null;
  }
  return (
    <span data-confidence={row.confidence} className={CONFIDENCE_CELL}>
      <Pill neutral="muted" title={CONFIDENCE_TITLE[row.confidence]}>
        {row.confidence} confidence
      </Pill>
    </span>
  );
}

/** The value a row recommends, or a word for its absence: never a blank after the arrow. */
function suggestedOf(row: Row): string {
  return row.suggested === '' ? 'no value' : row.suggested;
}

/** `command` false leaves the kubectl out, for a list that gathers a
 *  workload's commands under its own "How to fix". */
export function Finding({ row, command = true }: { row: Row; command?: boolean }): ReactNode {
  return (
    <li data-finding={row.kind} className={`border-l-2 pl-2.5 ${KIND_EDGE[row.kind] ?? 'border-l-line'}`}>
      <div className="flex flex-wrap items-baseline gap-2">
        <Pill tone={KIND_TONE[row.kind]} neutral="muted">
          {row.action}
        </Pill>
        <Basis row={row} />
        <span className="ml-auto text-xs tabular-nums">
          {row.current} → <strong>{suggestedOf(row)}</strong>
        </span>
      </div>
      <div className="mt-1 text-xs break-words text-muted">
        {row.workload} · {row.container} · {row.resource}
      </div>
      <div className="mt-1 text-xs" title={row.message}>
        {reasonLabel(row)}
      </div>
      {row.warnings.map((w) => (
        <div key={w} className="mt-1 text-xs text-warn">
          {w}
        </div>
      ))}
      {!command || row.kubectl === '' ? null : (
        <code className="mt-1.5 block overflow-x-auto rounded-md border border-line bg-panel-2 px-1.5 py-1 font-mono text-2xs whitespace-pre">
          {row.kubectl}
        </code>
      )}
    </li>
  );
}
