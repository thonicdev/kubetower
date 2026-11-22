import { useQuery } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { Link } from 'react-router';
import { ApiError, HEAD_ROW, Pending, ROW, TABLE, bodyCell, focusRing, headCell, type ExtensionTabProps } from '@kubetower/ui';

import { briefQuery, detailQuery, n, workloadHref, type Brief, type Counts, type Cve, type WorkloadRef } from './data';
import type { PolicyCounts } from './policyData';
import { briefHref } from './panel';
import { SEV, SevBar, SevChip, hint, sourceState } from './parts';

/**
 * The Security tab in the panel of a pod or of a workload that runs pods: the
 * severity bar, the four counts, the image, and the ten worst CVEs of the
 * workload the report was bound to, with the way to its page. Labels, values
 * and short states only; what a value means is in its `title`.
 */

/** The CVEs the tab lists, the worst first as the route sorts them. */
export const TAB_CVES = 10;

type Short = 'unreadable' | 'denied' | 'not scanned' | 'absent' | 'partial' | 'read' | 'off';

function stateOf(error: unknown): Short {
  return error instanceof ApiError && error.status === 403 ? 'denied' : 'unreadable';
}

const message = (error: unknown) => (error instanceof Error ? error.message : undefined);

function State({ state, title }: { state: Short; title?: string | undefined }): ReactNode {
  const tone = state === 'unreadable' ? 'text-bad' : state === 'denied' ? 'text-warn' : 'text-muted';
  return (
    <p data-state={state} title={title} className={`text-xs ${tone}`}>
      {state}
    </p>
  );
}

export default function PanelTab({ cluster, kind, namespace, name }: ExtensionTabProps): ReactNode {
  const brief = useQuery({ ...briefQuery(cluster, kind, namespace ?? '', name), enabled: namespace !== undefined && namespace !== '' });
  let body: ReactNode;
  if (brief.data !== undefined) {
    body = brief.data.scanned ? <Scanned cluster={cluster} brief={brief.data} /> : <State state="not scanned" />;
  } else if (brief.isError) {
    body = <State state={stateOf(brief.error)} title={message(brief.error)} />;
  } else {
    body = <Pending compact what="the report" />;
  }
  const policies = brief.data?.policies;
  return (
    <div data-security-tab="" className="flex flex-col gap-3">
      {body}
      {policies === undefined ? null : <PoliciesLine cluster={cluster} brief={brief.data} counts={policies} />}
    </div>
  );
}

/** "Policies 3 fail · 1 warn", from the brief; a link to the workload's policies. */
function PoliciesLine({ cluster, brief, counts }: { cluster: string; brief: Brief | undefined; counts: PolicyCounts }): ReactNode {
  const parts = [
    counts.fail > 0 ? { text: `${n(counts.fail)} fail`, tone: SEV.high.text } : undefined,
    counts.error > 0 ? { text: `${n(counts.error)} error`, tone: SEV.critical.text } : undefined,
    counts.warn > 0 ? { text: `${n(counts.warn)} warn`, tone: SEV.medium.text } : undefined,
  ].filter((x) => x !== undefined);
  const to = brief?.workload === undefined ? undefined : `${workloadHref(cluster, brief.workload)}?tab=policies`;
  return (
    <p data-tab-policies="" className="flex items-baseline gap-2 text-xs" title={`${n(counts.audit)} let through by audit`}>
      <span className="text-2xs tracking-eyebrow text-muted uppercase">Policies</span>
      {parts.length === 0 ? (
        <span className="text-ok">pass</span>
      ) : (
        parts.map((x, i) => (
          <span key={x.text} className={x.tone}>
            {i === 0 ? '' : '· '}
            {x.text}
          </span>
        ))
      )}
      {to === undefined ? null : (
        <Link to={to} className={`ml-auto text-accent hover:underline ${focusRing}`}>
          →
        </Link>
      )}
    </p>
  );
}

function Scanned({ cluster, brief }: { cluster: string; brief: Brief }): ReactNode {
  const counts = brief.counts;
  const to = briefHref(cluster, brief);
  return (
    <>
      <header className="flex min-w-0 items-center gap-2 text-xs">
        {brief.workload === undefined ? null : (
          <span className="min-w-0 truncate text-muted" data-workload="" title={`${brief.workload.namespace}/${brief.workload.kind}/${brief.workload.name}`}>
            {brief.workload.kind}/<span className="text-fg">{brief.workload.name}</span>
          </span>
        )}
        <span className="rounded-full border border-line px-1.5 text-2xs leading-4 text-muted">Trivy</span>
        {to === undefined ? null : (
          <Link to={to} data-open="" className={`ml-auto shrink-0 text-accent hover:underline ${focusRing}`}>
            Security Center →
          </Link>
        )}
      </header>
      {counts === undefined ? null : <Summary counts={counts} />}
      {brief.image === undefined || brief.image === '' ? null : (
        <p data-image="" className="truncate font-mono text-xs text-muted" title={brief.image}>
          {brief.image}
        </p>
      )}
      {brief.workload === undefined ? null : <WorstCves cluster={cluster} workload={brief.workload} />}
    </>
  );
}

/** The bar and the four counts under it, each labeled. */
function Summary({ counts }: { counts: Counts }): ReactNode {
  return (
    <section aria-label="Severity" className="flex flex-col gap-2 rounded-lg border border-line bg-panel-2/40 px-3 py-2.5">
      <div className="flex">
        <SevBar counts={counts} className="h-2.5" />
      </div>
      <dl className="grid grid-cols-4 gap-2" title={hint(counts)}>
        {(['critical', 'high', 'medium', 'low'] as const).map((l) => (
          <div key={l} data-counter={l} className="flex flex-col">
            <dt className="text-2xs tracking-eyebrow text-muted uppercase">{SEV[l].label}</dt>
            <dd className={`text-lg tabular-nums ${counts[l] === 0 ? 'text-muted/50' : SEV[l].text}`}>{n(counts[l])}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

/** The ten worst CVEs, read from the workload's own route on its first page. */
function WorstCves({ cluster, workload }: { cluster: string; workload: WorkloadRef }): ReactNode {
  const detail = useQuery(detailQuery(cluster, workload));
  if (detail.data === undefined) {
    return detail.isError ? <State state={stateOf(detail.error)} title={message(detail.error)} /> : <Pending compact what="the CVEs" />;
  }
  const source = detail.data.sources.find((s) => s.kind === 'vulnerabilityreports');
  if (source !== undefined && source.state !== 'read') return <State state={sourceState(source)} title={source.note} />;
  const worst = detail.data.cves.slice(0, TAB_CVES);
  if (worst.length === 0) {
    return (
      <p data-none="" className="text-xs text-muted">
        None
      </p>
    );
  }
  return (
    <section aria-label="CVE" className="flex min-w-0 flex-col gap-1.5">
      <h3 className="flex items-baseline gap-2 text-2xs tracking-eyebrow text-muted uppercase">
        CVE
        <span data-shown="" className="tabular-nums normal-case" title={`The ${n(worst.length)} worst of ${n(detail.data.cvesTotal)}`}>
          {n(worst.length)} / {n(detail.data.cvesTotal)}
        </span>
      </h3>
      <div className="min-w-0 overflow-x-auto">
        <table className={`${TABLE} w-full table-fixed`} data-tab-cves="">
          <thead>
            <tr className={HEAD_ROW}>
              <th className={`${headCell()} w-36`}>ID</th>
              <th className={`${headCell()} w-20`}>Severity</th>
              <th className={headCell()}>Package</th>
              <th className={`${headCell()} w-24`}>Fixed</th>
            </tr>
          </thead>
          <tbody>
            {worst.map((c) => (
              <CveRow key={`${c.container}/${c.id}/${c.pkg}/${c.installed}`} c={c} />
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

/** Only an http(s) link leaves the console. */
const safeLink = (raw: string | undefined) => (raw !== undefined && /^https?:\/\//.test(raw) ? raw : undefined);

function CveRow({ c }: { c: Cve }): ReactNode {
  const link = safeLink(c.link);
  return (
    <tr className={ROW} data-tab-cve={c.id} title={c.title}>
      <td className={`${bodyCell(true)} truncate font-mono text-xs`}>
        {link === undefined ? (
          c.id
        ) : (
          <a href={link} target="_blank" rel="noreferrer" className={`text-accent hover:underline ${focusRing}`}>
            {c.id}
          </a>
        )}
      </td>
      <td className={bodyCell(true)}>
        <SevChip severity={c.severity} />
      </td>
      <td className={`${bodyCell(true)} truncate text-xs`} title={`${c.pkg} ${c.installed}`}>
        {c.pkg}
      </td>
      <td className={`${bodyCell(true)} truncate font-mono text-xs ${c.fixed === '' ? 'text-muted' : 'text-ok'}`} title={c.fixed}>
        {c.fixed === '' ? '—' : c.fixed}
      </td>
    </tr>
  );
}
