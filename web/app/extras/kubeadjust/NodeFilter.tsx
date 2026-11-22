import { useQuery } from '@tanstack/react-query';
import { useRef, type ReactNode } from 'react';
import { CopyButton, Failure, Pending, Pill, focusRing } from '@kubetower/ui';

import { nodeTabQuery, type Row } from './data';
import { KIND_EDGE, KIND_TONE } from './parts';
import { Finding } from './views';

/** One workload's suggestions, in the order the server ranked them. */
interface WorkloadRows {
  key: string;
  workload: string;
  kind: string;
  namespace: string;
  rows: Row[];
}

/** Groups the node's suggestions by workload, keeping the server's order:
 *  the first row of a workload is its loudest. */
function byWorkload(rows: Row[]): WorkloadRows[] {
  const out: WorkloadRows[] = [];
  const at = new Map<string, WorkloadRows>();
  for (const row of rows) {
    const key = `${row.namespace}/${row.workloadKind}/${row.workload}`;
    let group = at.get(key);
    if (group === undefined) {
      group = { key, workload: row.workload, kind: row.workloadKind, namespace: row.namespace, rows: [] };
      at.set(key, group);
      out.push(group);
    }
    group.rows.push(row);
  }
  return out;
}

/**
 * The page narrowed to one node (`?node=`): every workload with a pod there
 * and something to change, each with its suggestions and, under "How to fix",
 * the commands to review and run. It is where the node tab's "See the N
 * workloads" leads, so the panel can stay short and nothing it left out is
 * lost. The list is the node tab's own answer, so the two count the same
 * workloads.
 */
export function NodeWorkloadsView({ cluster, node }: { cluster: string; node: string }): ReactNode {
  const query = useQuery(nodeTabQuery(cluster, node));
  const tab = query.data;
  if (tab === undefined) {
    return query.isError ? (
      <Failure error={query.error} onRetry={() => void query.refetch()} />
    ) : (
      <Pending what={`the workloads on ${node}`} />
    );
  }
  const groups = byWorkload(tab.suggestions);
  return (
    <section aria-label={`Workloads on ${node}`} data-body="node" data-node-filter={node} className="flex flex-col gap-3">
      <h2 className="text-sm font-medium">
        {tab.workloads === null
          ? `Workloads with a pod on ${node}`
          : `${groups.length} workload${groups.length === 1 ? '' : 's'} with a pod on ${node} to change`}
      </h2>
      {tab.workloads === null ? (
        <p className="rounded-lg border border-line bg-panel p-4 text-sm text-warn">
          No usage could be read, so no workload on this node could be judged — see the notes above.
        </p>
      ) : groups.length === 0 ? (
        <p className="rounded-lg border border-line bg-panel p-4 text-sm text-muted">
          Nothing to change in the {tab.workloads} workload{tab.workloads === 1 ? '' : 's'} with a pod on {node}.
        </p>
      ) : (
        <div className="grid grid-cols-1 gap-3 xl:grid-cols-2">
          {groups.map((g) => (
            <WorkloadFix key={g.key} group={g} />
          ))}
        </div>
      )}
    </section>
  );
}

function WorkloadFix({ group: g }: { group: WorkloadRows }): ReactNode {
  const block = useRef<HTMLPreElement | null>(null);
  const commands = [...new Set(g.rows.map((r) => r.kubectl).filter((c) => c !== ''))];
  const worst = g.rows[0]?.kind;
  return (
    <section
      data-workload={g.workload}
      className={`flex flex-col gap-2.5 rounded-lg border border-line bg-panel px-4 py-3 ${worst === undefined ? '' : `border-l-4 ${KIND_EDGE[worst] ?? ''}`}`}
    >
      <div className="flex flex-wrap items-baseline gap-2">
        <h3 className="text-base font-semibold break-all">{g.workload}</h3>
        <span className="text-xs text-muted">
          {g.kind} · {g.namespace}
        </span>
        {worst === undefined ? null : (
          <Pill tone={KIND_TONE[worst]} neutral="muted">
            {g.rows.length} finding{g.rows.length === 1 ? '' : 's'}
          </Pill>
        )}
      </div>
      <ul className="flex flex-col gap-3">
        {g.rows.map((row, i) => (
          <Finding key={`${row.container}/${row.resource}/${row.action}/${i}`} row={row} command={false} />
        ))}
      </ul>
      {commands.length === 0 ? null : (
        <details data-how-to-fix="" className="rounded-md border border-line bg-panel-2 p-2.5">
          <summary className={`cursor-pointer text-xs font-semibold ${focusRing}`}>How to fix</summary>
          <pre ref={block} className="mt-2 overflow-x-auto font-mono text-2xs whitespace-pre select-all">
            {commands.join('\n')}
          </pre>
          <div className="mt-2 flex items-center gap-2">
            <CopyButton value={commands.join('\n')} label={commands.length === 1 ? 'Copy' : 'Copy all'} selectRef={block} />
            <span data-restarts="" className="text-2xs text-warn" title="Each of these restarts every pod of the workload, on every node. KubeTower does not run them.">
              Restarts pods
            </span>
          </div>
        </details>
      )}
    </section>
  );
}
