import { useQuery } from '@tanstack/react-query';
import { useMemo, useState, type ReactNode } from 'react';
import { Link, useParams, useSearchParams } from 'react-router';
import {
  Failure,
  HEAD_ROW,
  PageHeader,
  BackArrow,
  Pending,
  ROW,
  TABLE,
  bodyCell,
  btnSecondary,
  focusRing,
  headCell,
  useDeclarePageFacts,
} from '@kubetower/ui';

import {
  ago,
  detailQuery,
  filterCves,
  n,
  objectHref,
  pageHref,
  pageOf,
  total,
  type Check,
  type Cve,
  type Detail,
  type ExposedSecret,
  type Severity,
  type Source,
  type SourceKind,
  type WorkloadRef,
} from './data';
import { PolicyResults } from './PoliciesView';
import { Pager, SEV, SevBar, SevChip, SevCounts, SourcePills, levelOf, sourceState } from './parts';
import { SbomTab } from './SbomTab';

/**
 * One workload, at `/c/:cluster/x/security/:namespace/:kind/:name`: its
 * containers and images, then three tabs - the CVEs (50 at a time, filtered by
 * severity and by whether a fix exists), the failed config checks, and the
 * exposed secrets (rule, category, severity, file; never the match).
 */

const TABS = ['cve', 'config', 'secrets', 'policies', 'sbom'] as const;
type TabKey = (typeof TABS)[number];

export function WorkloadPage(): ReactNode {
  const { cluster = '', namespace = '', kind = '', name = '' } = useParams<{ cluster: string; namespace: string; kind: string; name: string }>();
  const ref: WorkloadRef = useMemo(() => ({ namespace, kind, name }), [namespace, kind, name]);
  useDeclarePageFacts({ title: `Security · ${name}` });
  const query = useQuery(detailQuery(cluster, ref));
  const data = query.data;
  const open = objectHref(cluster, ref);

  return (
    <div className="flex flex-col gap-4 p-4" data-security-workload="" aria-busy={query.isFetching}>
      <PageHeader
        heading="h1"
        back={<BackArrow to={pageHref(cluster)} />}
        kind={kind}
        name={name}
        namespace={namespace}
        cluster={cluster}
        badges={data === undefined ? null : <SourcePills sources={data.sources} />}
        actions={
        <span className="flex items-center gap-2">
          {open === undefined ? null : (
            <Link to={open} className={`text-xs text-accent hover:underline ${focusRing}`}>
              Open
            </Link>
          )}
          <button type="button" className={btnSecondary} onClick={() => void query.refetch()} disabled={query.isFetching}>
            {query.isFetching ? 'Reading…' : 'Read again'}
          </button>
        </span>
        }
        className=""
      />
      {data === undefined ? (
        query.isError ? (
          <Failure error={query.error} onRetry={() => void query.refetch()} />
        ) : (
          <Pending what="this workload's reports" />
        )
      ) : (
        <>
          {query.isError ? <Failure error={query.error} onRetry={() => void query.refetch()} /> : null}
          <Body detail={data} cluster={cluster} />
        </>
      )}
    </div>
  );
}

function Body({ detail, cluster }: { detail: Detail; cluster: string }): ReactNode {
  const [params, setParams] = useSearchParams();
  const raw = params.get('tab') ?? 'cve';
  const tab: TabKey = (TABS as readonly string[]).includes(raw) ? (raw as TabKey) : 'cve';
  const pick = (t: TabKey) =>
    setParams(
      (p) => {
        const out = new URLSearchParams(p);
        if (t === 'cve') out.delete('tab');
        else out.set('tab', t);
        return out;
      },
      { replace: true },
    );
  const source = (kind: SourceKind) => detail.sources.find((s) => s.kind === kind);
  // The SBOM's count is known once its tab reads it: none is drawn before.
  const labels: Readonly<Record<TabKey, { label: string; count?: number }>> = {
    cve: { label: 'CVE', count: detail.cvesTotal },
    config: { label: 'Config', count: detail.checks.length },
    secrets: { label: 'Secrets', count: detail.secrets.length },
    policies: { label: 'Policies', count: detail.policies?.total ?? 0 },
    sbom: { label: 'SBOM' },
  };
  // The Policies tab only where a policy engine answered.
  const tabs = TABS.filter((t) => t !== 'policies' || detail.policies !== undefined);

  return (
    <div className="flex flex-col gap-4">
      <Head detail={detail} />
      <div role="tablist" aria-label="Findings" className="flex gap-1 border-b border-line">
        {tabs.map((t) => (
          <button
            key={t}
            type="button"
            role="tab"
            aria-selected={tab === t}
            data-tab={t}
            onClick={() => pick(t)}
            className={`-mb-px border-b-2 px-3 py-1.5 text-sm ${tab === t ? 'border-accent text-fg' : 'border-transparent text-muted hover:text-fg'} ${focusRing}`}
          >
            {labels[t].label} {labels[t].count === undefined ? null : <span className="tabular-nums text-muted">{n(labels[t].count)}</span>}
          </button>
        ))}
      </div>
      <div role="tabpanel">
        {tab === 'cve' ? (
          <CveTab detail={detail} source={source('vulnerabilityreports')} />
        ) : tab === 'policies' ? (
          detail.policies === undefined ? null : (
            <>
              <SourcePills sources={detail.policies.sources.filter((s) => s.state !== 'absent')} />
              <div className="mt-3">
                <PolicyResults results={detail.policies.results} total={detail.policies.total} showPolicy />
              </div>
            </>
          )
        ) : tab === 'sbom' ? (
          <SbomTab cluster={cluster} workload={detail.workload} signature={detail.signature} />
        ) : tab === 'config' ? (
          <ConfigTab checks={detail.checks} source={source('configauditreports')} />
        ) : (
          <SecretsTab secrets={detail.secrets} source={source('exposedsecretreports')} />
        )}
      </div>
    </div>
  );
}

/** The containers, their images and OS, the scan, and how it is reached. */
function Head({ detail }: { detail: Detail }): ReactNode {
  const max = Math.max(0, ...detail.containers.map((c) => total(c.counts)));
  return (
    <section aria-label="Containers" className="flex min-w-0 flex-col gap-2 rounded-lg border border-line bg-panel px-3.5 py-3">
      <div className="flex flex-wrap items-center gap-2 text-xs" data-exposure="">
        {detail.exposure.length === 0 ? (
          <span className="text-muted">not exposed</span>
        ) : (
          detail.exposure.map((e) => (
            <span key={e} className="rounded-full border border-warn/50 px-2 text-2xs text-warn">
              {e}
            </span>
          ))
        )}
      </div>
      <div className="min-w-0 overflow-x-auto">
        <table className={`${TABLE} w-full`} data-containers="">
          <thead>
            <tr className={HEAD_ROW}>
              <th className={`${headCell()} w-32`}>Container</th>
              <th className={headCell()}>Image</th>
              <th className={`${headCell()} w-36`}>OS</th>
              <th className={`${headCell()} w-40`}>Severity</th>
              <th className={`${headCell()} w-40 text-right`} title="Critical · High · Medium · Low">
                C · H · M · L
              </th>
              <th className={`${headCell()} w-16 text-right`}>Scan</th>
            </tr>
          </thead>
          <tbody>
            {detail.containers.map((c) => (
              <tr key={c.container} className={ROW} data-container={c.container}>
                <td className={bodyCell(true)}>{c.container}</td>
                <td className={`${bodyCell(true)} max-w-96 truncate text-xs`} title={c.image}>
                  {c.image}
                </td>
                <td className={`${bodyCell(true)} text-xs`}>
                  <span className="text-muted">{c.os ?? '—'}</span>
                  {c.eosl === true ? (
                    <span data-eosl="" title="The OS is past its end of service: no more fixes" className="ml-1.5 rounded-full border border-bad/50 px-1.5 text-2xs text-bad">
                      EOSL
                    </span>
                  ) : null}
                </td>
                <td className={bodyCell(true)}>
                  {c.scanned === undefined ? (
                    <span data-unscanned="" className="text-xs text-muted" title="No vulnerability report covers this container">
                      not scanned
                    </span>
                  ) : total(c.counts) === 0 ? (
                    <span data-clean="" className="text-xs text-ok">
                      clean
                    </span>
                  ) : (
                    <SevBar counts={c.counts} scale={Math.log1p(total(c.counts)) / Math.log1p(max)} />
                  )}
                </td>
                <td className={`${bodyCell(true)} text-right text-xs`}>{c.scanned === undefined ? <span className="text-muted">—</span> : <SevCounts counts={c.counts} />}</td>
                <td className={`${bodyCell(true)} text-right text-xs tabular-nums text-muted`} title={c.scanned}>
                  {c.scanned === undefined ? '—' : ago(c.scanned, detail.read)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

/** A tab whose source was not read draws its state, never an empty list. */
function Unread({ source }: { source: Source | undefined }): ReactNode {
  if (source === undefined) return <p className="text-xs text-muted">—</p>;
  const state = sourceState(source);
  if (state === 'read' || state === 'partial') return null;
  return (
    <p data-unread={state} title={source.note} className={`text-xs ${state === 'unreadable' ? 'text-bad' : state === 'denied' ? 'text-warn' : 'text-muted'}`}>
      {state}
    </p>
  );
}

const readable = (s: Source | undefined) => s !== undefined && s.state === 'read';

const SEVERITIES: readonly Severity[] = ['CRITICAL', 'HIGH', 'MEDIUM', 'LOW', 'UNKNOWN'];

function CveTab({ detail, source }: { detail: Detail; source: Source | undefined }): ReactNode {
  const [severity, setSeverity] = useState<Severity | ''>('');
  const [fixable, setFixable] = useState(false);
  const [at, setAt] = useState(0);
  const filtered = useMemo(() => filterCves(detail.cves, { severity, fixable }), [detail.cves, severity, fixable]);
  const { rows, page, pages } = useMemo(() => pageOf(filtered, at), [filtered, at]);
  if (!readable(source)) return <Unread source={source} />;
  const unscanned = detail.containers.every((c) => c.scanned === undefined);
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <div role="group" aria-label="Severity" className="flex gap-1">
          {SEVERITIES.map((s) => {
            const l = levelOf(s);
            const on = severity === s;
            return (
              <button
                key={s}
                type="button"
                data-sev-filter={l}
                aria-pressed={on}
                onClick={() => {
                  setSeverity(on ? '' : s);
                  setAt(0);
                }}
                className={`rounded-full border px-2 py-0.5 ${on ? 'border-accent bg-panel-2' : 'border-line hover:bg-panel-2'} ${focusRing}`}
              >
                <span className={SEV[l].text}>{SEV[l].label}</span>
              </button>
            );
          })}
        </div>
        <label className="flex items-center gap-1.5">
          <input
            type="checkbox"
            data-fixable=""
            checked={fixable}
            onChange={(e) => {
              setFixable(e.target.checked);
              setAt(0);
            }}
          />
          Fixable only
        </label>
        {detail.cvesTotal > detail.cves.length ? (
          <span data-cut="" className="rounded-full border border-warn/45 px-2 text-2xs text-warn" title={`The ${n(detail.cves.length)} worst of ${n(detail.cvesTotal)}`}>
            {n(detail.cves.length)} / {n(detail.cvesTotal)}
          </span>
        ) : null}
        <span className="ml-auto">
          <Pager page={page} pages={pages} count={filtered.length} onPage={setAt} />
        </span>
      </div>
      {rows.length === 0 ? (
        <p className="text-xs text-muted" data-none="">
          {unscanned ? 'not scanned' : 'None'}
        </p>
      ) : (
        <div className="min-w-0 overflow-x-auto">
          <table className={`${TABLE} w-full`} data-cves="">
            <thead>
              <tr className={HEAD_ROW}>
                <th className={`${headCell()} w-40`}>ID</th>
                <th className={`${headCell()} w-24`}>Severity</th>
                <th className={`${headCell()} w-14 text-right`}>Score</th>
                <th className={headCell()}>Package</th>
                <th className={headCell()}>Installed</th>
                <th className={headCell()}>Fixed</th>
                <th className={`${headCell()} w-28`}>Container</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((c) => (
                <CveRow key={`${c.container}/${c.id}/${c.pkg}/${c.installed}`} c={c} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

/** Only an http(s) link leaves the console. */
const safeLink = (raw: string | undefined) => (raw !== undefined && /^https?:\/\//.test(raw) ? raw : undefined);

function CveRow({ c }: { c: Cve }): ReactNode {
  const link = safeLink(c.link);
  return (
    <tr className={ROW} data-cve-row={c.id} title={c.title}>
      <td className={`${bodyCell(true)} font-mono text-xs`}>
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
      <td className={`${bodyCell(true)} text-right text-xs tabular-nums`}>{c.score === undefined ? '—' : c.score.toFixed(1)}</td>
      <td className={`${bodyCell(true)} max-w-56 truncate text-xs`} title={c.pkg}>
        {c.pkg}
      </td>
      <td className={`${bodyCell(true)} max-w-40 truncate font-mono text-xs text-muted`} title={c.installed}>
        {c.installed}
      </td>
      <td className={`${bodyCell(true)} max-w-40 truncate font-mono text-xs ${c.fixed === '' ? 'text-muted' : 'text-ok'}`} title={c.fixed}>
        {c.fixed === '' ? '—' : c.fixed}
      </td>
      <td className={`${bodyCell(true)} truncate text-xs text-muted`}>{c.container}</td>
    </tr>
  );
}

function ConfigTab({ checks, source }: { checks: readonly Check[]; source: Source | undefined }): ReactNode {
  if (!readable(source)) return <Unread source={source} />;
  if (checks.length === 0) return <p className="text-xs text-muted" data-none="">None</p>;
  return (
    <div className="min-w-0 overflow-x-auto">
      <table className={`${TABLE} w-full`} data-checks="">
        <thead>
          <tr className={HEAD_ROW}>
            <th className={`${headCell()} w-24`}>Severity</th>
            <th className={`${headCell()} w-28`}>ID</th>
            <th className={headCell()}>Check</th>
            <th className={`${headCell()} w-40`}>Category</th>
          </tr>
        </thead>
        <tbody>
          {checks.map((c, i) => (
            <tr key={`${c.id}/${String(i)}`} className={ROW} data-check={c.id}>
              <td className={bodyCell(true)}>
                <SevChip severity={c.severity} />
              </td>
              <td className={`${bodyCell(true)} font-mono text-xs`}>{c.id}</td>
              <td className={`${bodyCell(true)} truncate text-xs`} title={c.title}>
                {c.title}
              </td>
              <td className={`${bodyCell(true)} text-xs text-muted`}>{c.category ?? '—'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function SecretsTab({ secrets, source }: { secrets: readonly ExposedSecret[]; source: Source | undefined }): ReactNode {
  if (!readable(source)) return <Unread source={source} />;
  if (secrets.length === 0) return <p className="text-xs text-muted" data-none="">None</p>;
  return (
    <div className="min-w-0 overflow-x-auto">
      <table className={`${TABLE} w-full`} data-secrets="">
        <thead>
          <tr className={HEAD_ROW}>
            <th className={headCell()}>Rule</th>
            <th className={`${headCell()} w-40`}>Category</th>
            <th className={`${headCell()} w-24`}>Severity</th>
            <th className={headCell()}>File</th>
          </tr>
        </thead>
        <tbody>
          {secrets.map((s, i) => (
            <tr key={`${s.rule}/${s.target}/${String(i)}`} className={ROW} data-secret={s.rule}>
              <td className={`${bodyCell(true)} text-xs`} title={s.title}>
                {s.rule}
              </td>
              <td className={`${bodyCell(true)} text-xs text-muted`}>{s.category}</td>
              <td className={bodyCell(true)}>
                <SevChip severity={s.severity} />
              </td>
              <td className={`${bodyCell(true)} truncate font-mono text-xs`} title={s.target}>
                {s.target}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
