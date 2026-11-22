import { useQuery } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { Link } from 'react-router';
import {
  Failure,
  Pending,
  Pill,
  focusRing,
  type ExtensionTabProps,
  TABLE,
  HEAD_ROW,
  ROW,
  headCell,
  bodyCell,
} from '@kubetower/ui';

import { nodeTabQuery, type NodeTab as NodeTabData } from './data';
import { KIND_EDGE, KIND_TONE, ResourceBar, Sources } from './parts';

/**
 * KubeAdjust in a Node's panel: for this node, what it can give, what its pods
 * reserve and cap, what they use where measured, what the machine is, and the
 * few workloads here that are furthest from what they reserve.
 *
 * **A panel is read at a glance, so it holds no commands and no long lists.**
 * A command restarts every pod of its workload, on every node, and wants the
 * room to be reviewed: the KubeAdjust page has it, narrowed to this node by the
 * link under the workloads. The node's own findings and its pods are folded,
 * there for whoever opens them.
 *
 * Every figure is the server's (`nodetab.go`); this places them. Usage nobody
 * measured is said in words, never drawn as an empty bar.
 */
export default function NodeTab({ cluster, name }: ExtensionTabProps): ReactNode {
  const query = useQuery(nodeTabQuery(cluster, name));
  if (query.data === undefined) {
    return query.isError ? (
      <Failure error={query.error} onRetry={() => void query.refetch()} />
    ) : (
      <Pending what="this node's requests, limits and usage" />
    );
  }
  return <Body cluster={cluster} tab={query.data} />;
}

/** How many workloads the panel names; the server sends no more. */
const SHOWN = 5;

const heading = 'text-xs font-medium text-muted uppercase';

function Body({ cluster, tab }: { cluster: string; tab: NodeTabData }): ReactNode {
  const page = `/c/${encodeURIComponent(cluster)}/x/kubeadjust`;
  return (
    <div className="flex flex-col gap-4 text-sm" data-kubeadjust-tab="node">
      <div className="flex flex-wrap items-center gap-2">
        {tab.ready ? <Pill tone="ok">Ready</Pill> : <Pill tone="bad">NotReady</Pill>}
        <Pill neutral="muted">{tab.maxPods > 0 ? `${tab.pods} / ${tab.maxPods} pods` : `${tab.pods} pods`}</Pill>
        <span className="flex-1" />
        <Link to={`${page}?view=nodes`} className={`text-xs text-accent underline ${focusRing}`}>
          Every node in KubeAdjust
        </Link>
      </div>

      {/* The page's sources line, and above every failure the one thing a
          note marked OK can still change: totals over some of the pods. */}
      <Sources notes={tab.notes} partial={tab.partial} windowReadAt={tab.windowReadAt} />

      <section aria-label="Resources" className="flex flex-col gap-3">
        {tab.resources.map((r) => (
          <ResourceBar key={r.key} resource={r} />
        ))}
      </section>

      <About tab={tab} />

      <section aria-label="Workloads on this node" className="flex flex-col gap-1.5">
        <h3 className={heading}>Workloads with a pod here</h3>
        {tab.workloads === null ? (
          <p className="text-xs text-warn">No usage could be read, so no workload here could be judged — see the notes above.</p>
        ) : tab.flagged === 0 ? (
          <p className="text-xs text-muted">
            Nothing to change in the {tab.workloads} workload{tab.workloads === 1 ? '' : 's'} with a pod here.
          </p>
        ) : (
          <>
            <ul className="flex flex-col gap-1.5">
              {tab.topWorkloads.slice(0, SHOWN).map((w) => (
                <li
                  key={`${w.namespace}/${w.kind}/${w.workload}`}
                  data-node-workload={w.workload}
                  className={`flex flex-wrap items-baseline gap-x-2 border-l-2 pl-2.5 text-xs ${w.worst === '' ? 'border-l-line' : (KIND_EDGE[w.worst] ?? 'border-l-line')}`}
                >
                  {w.worst === '' ? null : (
                    <Pill tone={KIND_TONE[w.worst]} neutral="muted">
                      {w.findings}
                    </Pill>
                  )}
                  <span className="min-w-0 break-all">
                    <strong className="font-semibold">{w.workload}</strong> <span className="text-muted">· {w.namespace}</span>
                  </span>
                  <span className="ml-auto text-muted tabular-nums">{w.gap === '' ? 'not measured here' : w.gap}</span>
                </li>
              ))}
            </ul>
            <Link
              to={`${page}?node=${encodeURIComponent(tab.node)}`}
              data-see-all=""
              className={`self-start text-xs text-accent underline ${focusRing}`}
            >
              See the {tab.flagged} workload{tab.flagged === 1 ? '' : 's'} in KubeAdjust →
            </Link>
          </>
        )}
      </section>

      <details data-node-pods="" className="text-xs">
        <summary className={`cursor-pointer ${heading} ${focusRing}`}>Pods, the largest reservation first ({tab.podList.length})</summary>
        {tab.podList.length === 0 ? (
          <p className="mt-1.5 text-muted">No pod holds anything on this node.</p>
        ) : (
          <div className="mt-1.5 overflow-x-auto">
            <table className={`${TABLE} w-full tabular-nums`}>
              <thead>
                <tr className={HEAD_ROW}>
                  <th className={headCell()}>Pod</th>
                  <th className={`${headCell()} text-right`} title="The larger of its CPU and memory requests, as a share of the node">Share</th>
                  <th className={`${headCell()} text-right`}>CPU req / lim / used</th>
                  <th className={`${headCell()} text-right`}>Memory req / lim / used</th>
                </tr>
              </thead>
              <tbody>
                {tab.podList.map((p) => (
                  <tr key={`${p.namespace}/${p.name}`} data-node-pod={p.name} className={`${ROW} last:border-b-0`}>
                    <td className={`${bodyCell(true)} wrap`}>
                      <span className="break-all">{p.name}</span>
                      <span className="block text-2xs text-muted">
                        {p.namespace}
                        {p.workload === '' ? null : <> · {p.workload}</>}
                        {p.findings > 0 && p.worst !== '' ? (
                          <>
                            {' '}
                            <Pill tone={KIND_TONE[p.worst]} neutral="muted">
                              {p.findings}
                            </Pill>
                          </>
                        ) : null}
                      </span>
                    </td>
                    <td className={`${bodyCell(true)} text-right`}>{p.share}%</td>
                    <td className={`${bodyCell(true)} text-right`}>
                      {p.cpuRequest} / {p.cpuLimit} / {p.measured ? p.cpuUsed : <span className="text-muted">not measured</span>}
                    </td>
                    <td className={`${bodyCell(true)} text-right`}>
                      {p.memRequest} / {p.memLimit} / {p.measured ? p.memUsed : <span className="text-muted">not measured</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </details>
    </div>
  );
}

/**
 * What the machine is, in the five facts a reader looks for first; the rest -
 * kernel, architecture, roles, taints and the node's own findings - folded.
 * A fact the node did not report is said, never left blank.
 */
function About({ tab }: { tab: NodeTabData }): ReactNode {
  const a = tab.about;
  const facts: [string, string][] = [
    ['Version', a.kubelet === '' ? '' : `kubelet ${a.kubelet}`],
    ['OS', a.os],
    ['Container runtime', a.runtime],
    ['Age', a.age],
    ['Pods', tab.maxPods > 0 ? `${tab.pods} / ${tab.maxPods}` : String(tab.pods)],
  ];
  const more = tab.findings.length + (a.kernel === '' ? 0 : 1) + (a.architecture === '' ? 0 : 1) + a.roles.length + a.taints.length;
  return (
    <section aria-label="About this node" className="flex flex-col gap-1.5">
      <h3 className={heading}>About this node</h3>
      <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-0.5 text-xs" data-node-facts="">
        {facts.map(([label, value]) => (
          <div key={label} className="contents" data-fact={label}>
            <dt className="text-muted">{label}</dt>
            <dd className={`break-words ${value === '' ? 'text-muted' : ''}`}>{value === '' ? 'not reported' : value}</dd>
          </div>
        ))}
      </dl>
      {more === 0 ? null : (
        <details data-node-more="" className="text-xs">
          <summary className={`cursor-pointer text-muted hover:text-fg ${focusRing}`}>
            More about this node{tab.findings.length === 0 ? null : ` · ${tab.findings.length} finding${tab.findings.length === 1 ? '' : 's'}`}
          </summary>
          <div className="mt-1.5 flex flex-col gap-1.5">
            <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-0.5">
              {a.kernel === '' ? null : (
                <>
                  <dt className="text-muted">Kernel</dt>
                  <dd className="break-words">{a.kernel}</dd>
                </>
              )}
              {a.architecture === '' ? null : (
                <>
                  <dt className="text-muted">Architecture</dt>
                  <dd>{a.architecture}</dd>
                </>
              )}
              {a.roles.length === 0 ? null : (
                <>
                  <dt className="text-muted">Roles</dt>
                  <dd>{a.roles.join(', ')}</dd>
                </>
              )}
              {a.taints.length === 0 ? null : (
                <>
                  <dt className="text-muted">Taints</dt>
                  <dd className="break-all">
                    {a.taints.map((t) => (
                      <span key={t.title} title={t.title} className="mr-2 inline-block">
                        {t.text} · {t.effect}
                      </span>
                    ))}
                  </dd>
                </>
              )}
            </dl>
            {tab.findings.length === 0 ? null : (
              <ul className="flex flex-col gap-1.5">
                {tab.findings.map((f) => (
                  <li key={f.title} data-node-finding={f.kind} className={`border-l-2 pl-2.5 ${KIND_EDGE[f.kind] ?? 'border-l-line'}`}>
                    <Pill tone={KIND_TONE[f.kind]} neutral="muted">
                      {f.title}
                    </Pill>
                    <p className="mt-0.5 text-muted">{f.detail}</p>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </details>
      )}
    </section>
  );
}
