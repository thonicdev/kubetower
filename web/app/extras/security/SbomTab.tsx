import { useQuery } from '@tanstack/react-query';
import { useMemo, useState, type ReactNode } from 'react';
import { Failure, HEAD_ROW, Pending, ROW, TABLE, bodyCell, focusRing, headCell, useDebounced } from '@kubetower/ui';

import { n, pageOf, type WorkloadRef } from './data';
import { Pager, sourceState } from './parts';
import { TOP_LICENSES, filterComponents, sbomAbsent, sbomQuery, verdictMark, verdictTitle, type Component, type Sbom, type Signature, type Tally } from './supply';

/**
 * A workload's SBOM tab: its images and digests, a bar of the component types
 * and one of the ten most frequent licenses (a license filters the table), and
 * the components 50 at a time with a text filter. Read only while the tab is
 * open. Labels, values and short states; the rest in `title`.
 */
export function SbomTab({ cluster, workload, signature }: { cluster: string; workload: WorkloadRef; signature: Signature | null | undefined }): ReactNode {
  const query = useQuery(sbomQuery(cluster, workload));
  if (query.data === undefined) {
    return query.isError ? <Failure error={query.error} onRetry={() => void query.refetch()} /> : <Pending what="the SBOM" />;
  }
  return <Body sbom={query.data} signature={signature} />;
}

function Body({ sbom, signature }: { sbom: Sbom; signature: Signature | null | undefined }): ReactNode {
  const state = sourceState(sbom.source);
  const [typed, setTyped] = useState('');
  const q = useDebounced(typed, 200);
  const [license, setLicense] = useState<string | null>(null);
  const [at, setAt] = useState(0);
  const filtered = useMemo(() => filterComponents(sbom.components, { q, license }), [sbom.components, q, license]);
  const { rows, page, pages } = useMemo(() => pageOf(filtered, at), [filtered, at]);
  if (state !== 'read' && state !== 'partial') {
    const tone = state === 'unreadable' ? 'text-bad' : state === 'denied' ? 'text-warn' : 'text-muted';
    return (
      <p data-sbom-state={state} title={sbom.source.note} className={`text-xs ${tone}`}>
        {state === 'absent' ? 'SBOM absent' : state === 'off' ? 'SBOM off' : state}
      </p>
    );
  }
  const pick = (name: string) => {
    setLicense(license === name ? null : name);
    setAt(0);
  };
  return (
    <div className="flex flex-col gap-3" data-sbom="">
      <Images sbom={sbom} signature={signature} />
      {sbomAbsent(sbom) ? null : sbom.total === 0 ? (
        <p className="text-xs text-muted" data-none="">
          None
        </p>
      ) : (
        <>
          <div className="grid gap-3 xl:grid-cols-2">
            <TypesBar types={sbom.types} total={sbom.total} />
            <Licenses licenses={sbom.licenses} total={sbom.total} picked={license} onPick={pick} />
          </div>
          <div className="flex flex-wrap items-center gap-2 text-xs">
            {license === null ? null : (
              <button
                type="button"
                data-license-chip=""
                onClick={() => pick(license)}
                title="Remove the filter"
                className={`flex max-w-72 items-center gap-1 rounded-full border border-accent/50 px-2 text-2xs ${focusRing}`}
              >
                <span className="truncate">{license === '' ? '—' : license}</span>
                <span aria-hidden="true">×</span>
              </button>
            )}
            {sbom.total > sbom.components.length ? (
              <span data-cut="" className="rounded-full border border-warn/45 px-2 text-2xs text-warn" title={`The first ${n(sbom.components.length)} of ${n(sbom.total)}, by name`}>
                {n(sbom.components.length)} / {n(sbom.total)}
              </span>
            ) : null}
            <input
              type="search"
              aria-label="Filter components"
              placeholder="Filter"
              value={typed}
              onChange={(e) => {
                setTyped(e.target.value);
                setAt(0);
              }}
              className={`w-56 rounded-md border border-line bg-bg px-2 py-1 text-xs ${focusRing}`}
            />
            <span className="ml-auto">
              <Pager page={page} pages={pages} count={filtered.length} onPage={setAt} />
            </span>
          </div>
          {rows.length === 0 ? (
            <p className="text-xs text-muted" data-none="">
              None
            </p>
          ) : (
            <div className="min-w-0 overflow-x-auto">
              <table className={`${TABLE} w-full`} data-components="">
                <thead>
                  <tr className={HEAD_ROW}>
                    <th className={headCell()}>Name</th>
                    <th className={headCell()}>Version</th>
                    <th className={`${headCell()} w-32`}>Type</th>
                    <th className={headCell()}>License</th>
                    <th className={`${headCell()} w-28`}>Container</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((c, i) => (
                    <ComponentRow key={`${c.container}/${c.name}/${c.version}/${String(i)}`} c={c} />
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </div>
  );
}

function ComponentRow({ c }: { c: Component }): ReactNode {
  return (
    <tr className={ROW} data-component={c.name} title={c.purl}>
      <td className={`${bodyCell(true)} max-w-72 truncate text-xs`}>{c.name}</td>
      <td className={`${bodyCell(true)} max-w-48 truncate font-mono text-xs text-muted`} title={c.version}>
        {c.version === '' ? '—' : c.version}
      </td>
      <td className={`${bodyCell(true)} text-xs text-muted`}>{c.type === '' ? '—' : c.type}</td>
      <td className={`${bodyCell(true)} max-w-64 truncate text-xs`} title={c.license}>
        {c.license === '' ? <span className="text-muted">—</span> : c.license}
      </td>
      <td className={`${bodyCell(true)} truncate text-xs text-muted`}>{c.container}</td>
    </tr>
  );
}

/** Each container's image and digest, its component count, and the admission's verdict. */
function Images({ sbom, signature }: { sbom: Sbom; signature: Signature | null | undefined }): ReactNode {
  const mark = verdictMark(signature);
  const tone = signature?.verdict === 'pass' ? 'border-ok/45 text-ok' : signature?.verdict === 'fail' ? 'border-bad/45 text-bad' : 'border-line text-muted';
  return (
    <section aria-label="Images" className="flex min-w-0 flex-col gap-2 rounded-lg border border-line bg-panel px-3.5 py-3">
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <span data-signature={signature?.verdict ?? 'none'} title={verdictTitle(signature)} className={`rounded-full border px-2 text-2xs ${tone}`}>
          Signature {mark}
        </span>
        <span className="text-muted tabular-nums" data-total={sbomAbsent(sbom) ? 'absent' : sbom.total}>
          {sbomAbsent(sbom) ? '—' : n(sbom.total)} components
        </span>
      </div>
      <div className="min-w-0 overflow-x-auto">
        <table className={`${TABLE} w-full`} data-sbom-images="">
          <thead>
            <tr className={HEAD_ROW}>
              <th className={`${headCell()} w-32`}>Container</th>
              <th className={headCell()}>Image</th>
              <th className={headCell()}>Digest</th>
              <th className={`${headCell()} w-28 text-right`}>Components</th>
            </tr>
          </thead>
          <tbody>
            {sbom.images.map((i) => (
              <tr key={i.container} className={ROW} data-sbom-image={i.container}>
                <td className={bodyCell(true)}>{i.container}</td>
                <td className={`${bodyCell(true)} max-w-96 truncate text-xs`} title={i.image}>
                  {i.image}
                </td>
                <td className={`${bodyCell(true)} max-w-64 truncate font-mono text-xs text-muted`} title={i.digest}>
                  {i.digest === undefined || i.digest === '' ? '—' : i.digest}
                </td>
                {i.absent === true ? (
                  <td className={`${bodyCell(true)} text-right text-xs text-muted`} data-absent="">
                    —
                  </td>
                ) : (
                  <td className={`${bodyCell(true)} text-right text-xs tabular-nums`} title={`${n(i.dependencies)} dependencies`}>
                    {n(i.components)}
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

/** Full class strings, never assembled: Tailwind reads them from the source. */
const SHADES = ['bg-accent', 'bg-ok', 'bg-warn', 'bg-y-tag', 'bg-bad', 'bg-muted/60', 'bg-line'] as const;
const shade = (i: number) => SHADES[Math.min(i, SHADES.length - 1)] ?? 'bg-line';

function TypesBar({ types, total }: { types: readonly Tally[]; total: number }): ReactNode {
  return (
    <section aria-label="Types" className="flex min-w-0 flex-col gap-2 rounded-lg border border-line bg-panel px-3.5 py-3">
      <h3 className="text-sm font-semibold">Types</h3>
      <span className="flex h-2.5 overflow-hidden rounded-full bg-white/5" data-types-bar="">
        {types.map((t, i) => (
          <span key={t.name} data-type-seg={t.name} title={`${t.name === '' ? '—' : t.name}: ${n(t.count)}`} className={`h-full ${shade(i)}`} style={{ width: `${String((t.count / Math.max(1, total)) * 100)}%` }} />
        ))}
      </span>
      <ul className="flex flex-wrap gap-x-3 gap-y-1 text-2xs">
        {types.map((t, i) => (
          <li key={t.name} data-type={t.name} className="flex items-center gap-1">
            <span aria-hidden="true" className={`inline-block h-2 w-2 rounded-sm ${shade(i)}`} />
            <span>{t.name === '' ? '—' : t.name}</span>
            <span className="text-muted tabular-nums">{n(t.count)}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function Licenses({ licenses, total, picked, onPick }: { licenses: readonly Tally[]; total: number; picked: string | null; onPick: (name: string) => void }): ReactNode {
  const shown = licenses.slice(0, TOP_LICENSES);
  const max = Math.max(1, ...shown.map((l) => l.count));
  return (
    <section aria-label="Licenses" className="flex min-w-0 flex-col gap-1 rounded-lg border border-line bg-panel px-3.5 py-3">
      <h3 className="text-sm font-semibold">
        Licenses <span className="font-normal text-muted">{licenses.length > TOP_LICENSES ? `top ${String(TOP_LICENSES)}` : n(licenses.length)}</span>
      </h3>
      <ul className="flex flex-col gap-0.5" data-licenses="">
        {shown.map((l) => (
          <li key={l.name}>
            <button
              type="button"
              data-license={l.name}
              aria-pressed={picked === l.name}
              onClick={() => onPick(l.name)}
              title={`${l.name === '' ? 'No license named' : l.name}: ${n(l.count)} of ${n(total)}`}
              className={`flex w-full items-center gap-2 rounded px-1.5 py-0.5 text-left text-xs ${picked === l.name ? 'bg-panel-2 ring-1 ring-accent' : 'hover:bg-panel-2'} ${focusRing}`}
            >
              <span className="w-56 shrink-0 truncate">{l.name === '' ? '—' : l.name}</span>
              <span className="flex h-2 min-w-0 flex-1 overflow-hidden rounded-full bg-white/5">
                <span className="h-full bg-accent" style={{ width: `${String((l.count / max) * 100)}%` }} />
              </span>
              <span className="w-12 text-right text-muted tabular-nums">{n(l.count)}</span>
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}
