import { useQuery } from '@tanstack/react-query';
import { useRef, type ReactNode } from 'react';
import { Link } from 'react-router';
import { CopyButton, Failure, Pending, focusRing, type ExtensionTabProps } from '@kubetower/ui';

import { workloadTabQuery, type WorkloadTab as WorkloadTabData } from './data';
import { Sources } from './parts';
import { Finding, WorkloadCardView } from './views';

/**
 * KubeAdjust in a Deployment's, StatefulSet's, DaemonSet's or CronJob's panel: the card
 * the page draws for this workload - each container's CPU and memory against
 * its request and limit, the kills for memory it remembers - then its
 * recommendations, each with how sure it is, and the commands that would apply
 * them.
 *
 * Every figure is the server's (`workload.go`), computed exactly as the page
 * computes them for the workload's namespace; this places them. Nothing
 * measured is said in words, never drawn as a workload sized right.
 */
export default function WorkloadTab({ cluster, kind, namespace, name }: ExtensionTabProps): ReactNode {
  const query = useQuery(workloadTabQuery(cluster, kind, namespace ?? '', name));
  if (query.data === undefined) {
    return query.isError ? (
      <Failure error={query.error} onRetry={() => void query.refetch()} />
    ) : (
      <Pending what="this workload's requests, limits and usage" />
    );
  }
  return <Body cluster={cluster} tab={query.data} />;
}

const heading = 'text-xs font-bold tracking-wide text-muted uppercase';

function Body({ cluster, tab }: { cluster: string; tab: WorkloadTabData }): ReactNode {
  const page = `/c/${encodeURIComponent(cluster)}/x/kubeadjust?ns=${encodeURIComponent(tab.namespace)}`;
  const block = useRef<HTMLPreElement | null>(null);
  const text = tab.commands.join('\n');
  const findings = tab.card?.findings ?? [];
  return (
    <div className="flex flex-col gap-4 text-sm" data-kubeadjust-tab="workload">
      <div className="flex flex-wrap items-center gap-2">
        <span className="flex-1" />
        <Link to={page} data-see-page="" className={`text-xs text-accent underline ${focusRing}`}>
          Namespace {tab.namespace} in KubeAdjust →
        </Link>
      </div>

      <Sources notes={tab.notes} partial={tab.partial} windowReadAt={tab.windowReadAt} />

      {!tab.ready ? (
        <p data-unmeasured="" className="text-xs text-warn">
          No usage could be read, so this {tab.kind} could not be sized — see the notes above.
        </p>
      ) : tab.card === null ? (
        <p data-no-card="" className="text-xs text-muted">
          No running pod of this {tab.kind} reported any usage, so there is nothing to size yet.
          {tab.kind === 'CronJob' ? ' A CronJob is measured only while one of its Jobs runs.' : null}
        </p>
      ) : (
        <>
          <WorkloadCardView card={tab.card} />

          <section aria-label="Recommendations" className="flex flex-col gap-2">
            <h3 className={heading}>Recommendations</h3>
            {findings.length === 0 ? (
              <p className="text-xs text-muted">Nothing to change: this {tab.kind} asks for about what it uses.</p>
            ) : (
              <ul className="flex flex-col gap-3.5">
                {findings.map((row, i) => (
                  <Finding key={`${row.container}/${row.resource}/${row.action}/${i}`} row={row} command={false} />
                ))}
              </ul>
            )}
          </section>

          {tab.commands.length === 0 ? null : (
            <section aria-label="How to apply" data-commands="" className="rounded-md border border-line bg-panel-2 p-2.5">
              <h3 className={heading}>How to apply</h3>
              <pre ref={block} className="mt-2 overflow-x-auto font-mono text-2xs whitespace-pre select-all">
                {text}
              </pre>
              <div className="mt-2 flex flex-wrap items-center gap-2">
                <CopyButton value={text} label={tab.commands.length === 1 ? 'Copy' : 'Copy all'} selectRef={block} />
<span
                  data-restarts=""
                  className="text-2xs text-warn"
                  title={
                    tab.kind === 'CronJob'
                      ? 'Each of these changes the runs of this CronJob from the next one on. KubeTower does not run them.'
                      : `Each of these restarts every pod of this ${tab.kind}. KubeTower does not run them.`
                  }
                >
                  {tab.kind === 'CronJob' ? 'Next runs' : 'Restarts pods'}
                </span>
              </div>
            </section>
          )}
        </>
      )}
    </div>
  );
}
