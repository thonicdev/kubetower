import type { ReactNode } from 'react';
import { Link, useParams } from 'react-router';
import { HEAD_ROW, Pill, ROW, TABLE, bodyCell, btnSecondary, focusRing, headCell } from '@kubetower/ui';

import type { Ask, Flows, Overview, PolicyRow } from './data';
import { IsolationChips } from './parts';

/** The tables under the graph: `pods` (one page from the server), `policies`,
 *  `idle` (policies selecting nothing), `namespaces` and `deny` (default-deny
 *  namespaces). */
export type ListKey = 'pods' | 'policies' | 'idle' | 'namespaces' | 'deny' | 'flows';

/** A policy's kind, short. */
const KIND: Readonly<Record<string, string>> = { NetworkPolicy: 'NP', CiliumNetworkPolicy: 'CNP', CiliumClusterwideNetworkPolicy: 'CCNP',
  'Calico NetworkPolicy': 'Calico NP',
  'Calico GlobalNetworkPolicy': 'Calico GNP',
  AdminNetworkPolicy: 'ANP',
  BaselineAdminNetworkPolicy: 'BANP',
};

/** Rows a client-side table draws at most; the server caps its own lists too. */
export const ROWS = 200;

/** Pod rows drawn at most: the server's page. */
export const POD_ROWS = 300;

type Go = (next: Record<string, string>) => void;

export function Lists({ overview, flows, ask, list, go }: { overview: Overview; flows?: Flows | undefined; ask: Ask; list: ListKey; go: Go }): ReactNode {
  const tab = (key: ListKey, label: string, count: number) => {
    const on = list === key || (key === 'policies' && list === 'idle') || (key === 'namespaces' && list === 'deny');
    return (
      <button
        type="button"
        data-list-tab={key}
        aria-pressed={on}
        onClick={() => go({ list: key === 'pods' ? '' : key })}
        className={`rounded-md px-2.5 py-1 text-sm ${on ? 'bg-panel text-fg' : 'text-muted hover:text-fg'} ${focusRing}`}
      >
        {label} <span className="text-muted tabular-nums">{count}</span>
      </button>
    );
  };
  return (
    <section aria-label="Lists" className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-1">
        {tab('pods', 'Pods', overview.pods.total)}
        {tab('policies', 'Policies', overview.policiesTotal)}
        {tab('namespaces', 'Namespaces', overview.namespacesTotal)}
        {flows !== undefined && flows.state === 'read' ? tab('flows', 'Flows', flows.rows.length) : null}
        {list === 'pods' ? (
          <label className="ml-auto flex items-center gap-1.5 text-xs text-muted">
            <span>Find</span>
            <input
              type="search"
              defaultValue={ask.q}
              placeholder="namespace/pod or workload"
              onKeyDown={(event) => {
                if (event.key === 'Enter') go({ q: event.currentTarget.value, offset: '' });
              }}
              className={`rounded-md border border-line bg-panel-2 px-2 py-1 text-xs text-fg ${focusRing}`}
            />
          </label>
        ) : null}
      </div>
      {list === 'flows' && flows !== undefined ? (
        <FlowTable flows={flows} />
      ) : list === 'policies' || list === 'idle' ? (
        <Policies rows={list === 'idle' ? overview.policies.filter((p) => p.selects === 0) : overview.policies} total={overview.policiesTotal} idle={list === 'idle'} go={go} />
      ) : list === 'namespaces' || list === 'deny' ? (
        <Namespaces overview={overview} onlyDeny={list === 'deny'} go={go} />
      ) : (
        <Pods overview={overview} ask={ask} go={go} />
      )}
    </section>
  );
}

// The console's table look (`tableLook.ts`), named in each cell so its test
// reads it: one body size, heading and row height with every list. A cell
// whose content is several lines is `wrap`.

function Pods({ overview, ask, go }: { overview: Overview; ask: Ask; go: Go }): ReactNode {
  const { cluster = '' } = useParams<{ cluster: string }>();
  const page = overview.pods;
  // The server pages at 300; the cap holds here too, whatever it sent.
  const rows = page.rows.slice(0, POD_ROWS);
  const end = page.offset + rows.length;
  return (
    <div className="flex flex-col gap-2">
      {ask.show !== '' ? (
        <p className="flex items-center gap-2 text-xs">
          <Pill>{ask.show === 'in' ? 'isolated in' : ask.show === 'out' ? 'isolated out' : ask.show === 'open' ? 'not isolated' : 'host network'}</Pill>
          <button type="button" className={`text-accent underline ${focusRing}`} onClick={() => go({ show: '', offset: '' })}>
            all pods
          </button>
        </p>
      ) : null}
      {rows.length === 0 ? (
        <p data-no-pods="" className="text-sm text-muted">
          No pod
        </p>
      ) : (
        <table className={`${TABLE} w-full`} data-pods="">
          <thead>
            <tr className={HEAD_ROW}>
              <th className={headCell()}>Pod</th>
              <th className={headCell()}>Workload</th>
              <th className={headCell()}>Isolation</th>
              <th className={headCell()}>Policies</th>
              <th className={headCell()}>Ask</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => {
              const id = `${row.namespace}/${row.name}`;
              return (
                <tr key={id} data-pod={id} className={ROW}>
                  <td className={bodyCell()}>
                    <Link to={`/c/${encodeURIComponent(cluster)}/pods/${encodeURIComponent(row.namespace)}/${encodeURIComponent(row.name)}`} className={`text-accent hover:underline ${focusRing}`}>
                      <span className="text-muted">{row.namespace}/</span>
                      {row.name}
                    </Link>
                  </td>
                  <td className={`${bodyCell()} text-muted`}>{row.workload}</td>
                  <td className={bodyCell()}>
                    <IsolationChips isolatedIn={row.in} isolatedOut={row.out} hostNetwork={row.hostNetwork} />
                  </td>
                  <td className={`${bodyCell()} tabular-nums`}>{row.policies}</td>
                  <td className={bodyCell()}>
                    <span className="inline-flex gap-1">
                      <button type="button" className={`text-accent underline ${focusRing}`} onClick={() => go({ from: id })} title="Ask the verdict from this pod">
                        from
                      </button>
                      <button type="button" className={`text-accent underline ${focusRing}`} onClick={() => go({ to: id })} title="Ask the verdict to this pod">
                        to
                      </button>
                    </span>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
      {page.total > rows.length ? (
        <div className="flex items-center gap-2 text-xs text-muted" data-paging="">
          <span>
            {page.offset + 1}–{end} of {page.total}
          </span>
          <button type="button" className={btnSecondary} disabled={page.offset === 0} onClick={() => go({ offset: String(Math.max(0, page.offset - POD_ROWS)) })}>
            Previous
          </button>
          <button type="button" className={btnSecondary} disabled={end >= page.total} onClick={() => go({ offset: String(end) })}>
            Next
          </button>
        </div>
      ) : null}
    </div>
  );
}

function Policies({ rows, total, idle, go }: { rows: readonly PolicyRow[]; total: number; idle: boolean; go: Go }): ReactNode {
  const shown = rows.slice(0, ROWS);
  return (
    <div className="flex flex-col gap-2">
      {idle ? (
        <button type="button" className={`self-start text-xs text-accent underline ${focusRing}`} onClick={() => go({ list: 'policies' })}>
          all policies
        </button>
      ) : null}
      {shown.length === 0 ? (
        <p className="text-sm text-muted">No policy</p>
      ) : (
        <table className={`${TABLE} w-full`} data-policies="">
          <thead>
            <tr className={HEAD_ROW}>
              <th className={headCell()}>Policy</th>
              <th className={headCell()}>Selects</th>
              <th className={headCell()}>Types</th>
              <th className={headCell()}>Rules</th>
            </tr>
          </thead>
          <tbody>
            {shown.map((p) => (
              <tr key={`${p.namespace}/${p.name}`} data-policy={`${p.namespace}/${p.name}`} className={ROW}>
                <td className={`${bodyCell()} wrap`}>
                  <span className="mr-1 text-2xs text-muted" title={p.kind}>
                    {KIND[p.kind] ?? p.kind}
                  </span>
                  <span className="text-muted">{p.namespace === '' ? '' : `${p.namespace}/`}</span>
                  {p.name}
                  <div className="text-2xs text-muted">{p.selector === '' ? 'all pods' : p.selector}</div>
                </td>
                <td className={`${bodyCell()} tabular-nums ${p.selects === 0 ? 'text-warn' : ''}`}>{p.selects === 0 ? 'nothing' : `${String(p.selects)} pods`}</td>
                <td className={bodyCell()}>
                  <span className="inline-flex gap-1">
                    {p.types.map((t) => (
                      <Pill key={t}>{t}</Pill>
                    ))}
                  </span>
                </td>
                <td className={`${bodyCell()} wrap`}>
                  <RuleLines words={p.ingress} empty={p.types.includes('Ingress') ? 'deny all ingress' : ''} />
                  <RuleLines words={p.egress} empty={p.types.includes('Egress') ? 'deny all egress' : ''} />
                  {p.broken.length > 0 ? <p className="text-bad" title={p.broken.join('; ')}>unreadable part</p> : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {rows.length > shown.length || total > rows.length ? (
        <p className="text-xs text-muted">
          {shown.length} / {idle ? rows.length : total}
        </p>
      ) : null}
    </div>
  );
}

function RuleLines({ words, empty }: { words: readonly string[]; empty: string }): ReactNode {
  if (words.length === 0) return empty === '' ? null : <p className="text-muted">{empty}</p>;
  return (
    <ul className="flex flex-col gap-0.5">
      {words.map((w, i) => (
        <li key={i}>{w}</li>
      ))}
    </ul>
  );
}

function Namespaces({ overview, onlyDeny, go }: { overview: Overview; onlyDeny: boolean; go: Go }): ReactNode {
  const rows = onlyDeny ? overview.namespaces.filter((n) => n.defaultDenyIn || n.defaultDenyOut) : overview.namespaces;
  const shown = rows.slice(0, ROWS);
  return (
    <div className="flex flex-col gap-2">
      {onlyDeny ? (
        <button type="button" className={`self-start text-xs text-accent underline ${focusRing}`} onClick={() => go({ list: 'namespaces' })}>
          all namespaces
        </button>
      ) : null}
      {shown.length === 0 ? (
        <p className="text-sm text-muted">No namespace</p>
      ) : (
        <table className={`${TABLE} w-full`} data-namespaces="">
          <thead>
            <tr className={HEAD_ROW}>
              <th className={headCell()}>Namespace</th>
              <th className={headCell()}>Pods</th>
              <th className={headCell()}>Isolated in</th>
              <th className={headCell()}>Isolated out</th>
              <th className={headCell()}>Policies</th>
              <th className={headCell()}>Default deny</th>
            </tr>
          </thead>
          <tbody>
            {shown.map((n) => (
              <tr key={n.name} data-namespace={n.name} className={ROW}>
                <td className={bodyCell()}>
                  <button type="button" className={`text-accent hover:underline ${focusRing}`} onClick={() => go({ ns: n.name, list: '', show: '', offset: '', node: '' })}>
                    {n.name}
                  </button>
                </td>
                <td className={`${bodyCell()} tabular-nums`}>{n.pods}</td>
                <td className={bodyCell()}>
                  <Fraction part={n.isolatedIn} whole={n.pods} />
                </td>
                <td className={bodyCell()}>
                  <Fraction part={n.isolatedOut} whole={n.pods} />
                </td>
                <td className={`${bodyCell()} tabular-nums`}>{n.policies}</td>
                <td className={bodyCell()}>
                  <span className="inline-flex gap-1">
                    {n.defaultDenyIn ? <Pill tone="ok">ingress</Pill> : null}
                    {n.defaultDenyOut ? <Pill tone="ok">egress</Pill> : null}
                    {!n.defaultDenyIn && !n.defaultDenyOut ? <span className="text-muted">—</span> : null}
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {rows.length > shown.length ? (
        <p className="text-xs text-muted">
          {shown.length} / {rows.length}
        </p>
      ) : null}
    </div>
  );
}

/** "12 / 40" with a bar: a namespace's isolated share. */
function Fraction({ part, whole }: { part: number; whole: number }): ReactNode {
  const pct = whole === 0 ? 0 : Math.round((part / whole) * 100);
  return (
    <span className="inline-flex items-center gap-1.5" title={`${String(pct)}%`}>
      <svg viewBox="0 0 100 4" preserveAspectRatio="none" className="h-1 w-12" aria-hidden="true">
        <rect width="100" height="4" rx="2" fill="var(--color-line)" />
        <rect width={pct} height="4" rx="2" fill="var(--color-ok)" />
      </svg>
      <span className="tabular-nums">
        {part} / {whole}
      </span>
    </span>
  );
}

/** What the network plugin saw, one row per (from, to, port, verdict): solid facts, the
 *  verdict as a pill and the dropping policy when it names one. */
function FlowTable({ flows }: { flows: Flows }): ReactNode {
  const shown = flows.rows.slice(0, ROWS);
  return (
    <div className="flex flex-col gap-2">
      <table className={`${TABLE} w-full`} data-flow-table="">
        <thead>
          <tr className={HEAD_ROW}>
            <th className={headCell()}>From</th>
            <th className={headCell()}>To</th>
            <th className={headCell()}>Port</th>
            <th className={headCell()}>Verdict</th>
            <th className={headCell()}>Dropped by</th>
            <th className={headCell()}>Flows</th>
          </tr>
        </thead>
        <tbody>
          {shown.map((f) => (
            <tr key={`${f.from}>${f.to}:${String(f.port)}/${f.protocol}:${f.verdict}:${f.reason ?? ''}`} data-flow-row={f.verdict} className={ROW}>
              <td className={bodyCell()}>{f.from}</td>
              <td className={bodyCell()}>{f.to}</td>
              <td className={`${bodyCell()} tabular-nums`}>
                {f.port === 0 ? f.protocol : `${String(f.port)}/${f.protocol}`}
              </td>
              <td className={bodyCell()}>
                <span title={f.reason}>
                  <Pill tone={f.verdict === 'DROPPED' ? 'bad' : f.verdict === 'FORWARDED' ? 'ok' : undefined}>{f.verdict.toLowerCase()}</Pill>
                </span>
              </td>
              <td className={`${bodyCell()} text-muted`}>{f.deniedBy.join(', ')}</td>
              <td className={`${bodyCell()} tabular-nums`} title={f.last}>
                {f.count}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {flows.rows.length > shown.length || flows.truncated ? (
        <p className="text-xs text-muted">
          {shown.length} / {flows.rows.length}
          {flows.truncated ? '+' : ''}
        </p>
      ) : null}
    </div>
  );
}
