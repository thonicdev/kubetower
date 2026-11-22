import { useQuery } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { Link, useParams, useSearchParams } from 'react-router';
import {
  HEAD_ROW,
  ROW,
  TABLE,
  bodyCell,
  headCell,
  FilterTile,
  Pending,
  Pill,
  Tile,
  TileGrid,
  btnSecondary,
  focusRing,
  useDeclarePageFacts,
  type TileTone,
} from '@kubetower/ui';

import {
  overviewQuery,
  passes,
  plural,
  reasonWords,
  showOf,
  type Acme,
  type Certificate,
  type Issuer,
  type Overview,
  type Show,
} from './data';
import { DaysBar, ExpiryStrip, ProblemNotice, ReadyRing } from './parts';

/** Rows drawn at once; "Show more" adds as many again. */
export const PAGE_ROWS = 100;

/**
 * The Cert Manager page, at `/c/:cluster/x/certmanager`: **what expires in the
 * next 30 days**, then what is failing.
 *
 * Tiles that filter (the one pressed is `?show=` in the address), the next 90
 * days as a heat strip, the Ready states as a ring, then the list the tile
 * picked: certificates with a days-left bar, ACME orders and challenges in
 * trouble with their reason, or the issuers that are not ready.
 *
 * One read per scope, no interval: the page reads again on Refresh, or when it
 * is opened after a minute. Three answers stay three - not installed, refused
 * or unreadable, and empty.
 */
export function CertManager(): ReactNode {
  const { cluster = '' } = useParams<{ cluster: string }>();
  const [params, setParams] = useSearchParams();
  const scope = (params.get('ns') ?? '').trim();
  const show = showOf(params.get('show'));
  const focus = params.get('cert') ?? '';
  useDeclarePageFacts({ title: 'Cert Manager' });
  const overview = useQuery(overviewQuery(cluster, scope));

  const pick = (next: Show) =>
    setParams(
      (p) => {
        const out = new URLSearchParams(p);
        if (next === 'all') out.delete('show');
        else out.set('show', next);
        out.delete('cert');
        return out;
      },
      { replace: true },
    );

  const data = overview.data;
  return (
    <div className="flex flex-col gap-4 p-4">
      <header className="flex flex-wrap items-center gap-2">
        <h1 className="text-xl font-medium">Cert Manager</h1>
        <p className="text-xs text-muted" data-counts="">
          {cluster}
          {scope === '' ? null : <> · {scope}</>}
          {data?.installed === true ? <> · {plural(data.counts.total, 'certificate')}</> : null}
        </p>
        <span className="flex-1" />
        {scope === '' ? null : (
          <Link to={`/c/${encodeURIComponent(cluster)}/x/certmanager`} className={`text-xs text-accent underline ${focusRing}`}>
            All namespaces
          </Link>
        )}
        <button type="button" onClick={() => void overview.refetch()} disabled={overview.isFetching} className={btnSecondary}>
          {overview.isFetching ? 'Reading…' : 'Refresh'}
        </button>
      </header>

      {data === undefined ? (
        overview.isError ? (
          <ProblemNotice error={overview.error} onRetry={() => void overview.refetch()} />
        ) : (
          <Pending what="cert-manager's certificates" />
        )
      ) : !data.installed ? (
        <div role="status" data-state="absent" className="rounded-lg border border-line bg-panel p-3 text-sm">
          <p className="font-medium" title={data.message}>
            cert-manager is not installed
          </p>
        </div>
      ) : (
        <Answer cluster={cluster} data={data} show={show} focus={focus} pick={pick} />
      )}
    </div>
  );
}

interface TileSpec {
  show: Show;
  label: string;
  count: number;
  tone: TileTone;
  hint: string;
}

function tiles(data: Overview): TileSpec[] {
  const c = data.counts;
  const issuersRead = data.issuers.state === 'read';
  const acmeRead = data.acme.state === 'read';
  return [
    { show: 'expired', label: 'Expired', count: c.expired, tone: c.expired > 0 ? 'bad' : 'off', hint: 'Certificates past their notAfter.' },
    { show: 'week', label: '< 7 days', count: c.week, tone: c.week > 0 ? 'bad' : 'off', hint: 'Expiring within a week.' },
    { show: 'month', label: '< 30 days', count: c.month, tone: c.month > 0 ? 'warn' : 'off', hint: 'Expiring within 30 days, the week included.' },
    { show: 'notready', label: 'Not ready', count: c.notReady, tone: c.notReady > 0 ? 'bad' : 'off', hint: 'Ready is not True: never issued, or failing.' },
    {
      show: 'overdue',
      label: 'Renewal late',
      count: c.renewalOverdue,
      tone: c.renewalOverdue > 0 ? 'warn' : 'off',
      hint: 'Past their renewal time and not renewed: a renewal that is failing, before the expiry.',
    },
    {
      show: 'acme',
      label: 'ACME failing',
      count: acmeRead ? c.acmeFailing : -1,
      tone: !acmeRead ? 'off' : c.acmeFailing > 0 ? 'bad' : 'off',
      hint: 'Orders and challenges that failed or are stuck, with their reason.',
    },
    {
      show: 'issuers',
      label: 'Issuers broken',
      count: issuersRead ? c.issuersBroken : -1,
      tone: !issuersRead ? 'off' : c.issuersBroken > 0 ? 'bad' : 'off',
      hint: 'Issuers and ClusterIssuers whose Ready is not True.',
    },
  ];
}

function Answer({
  cluster,
  data,
  show,
  focus,
  pick,
}: {
  cluster: string;
  data: Overview;
  show: Show;
  focus: string;
  pick: (s: Show) => void;
}): ReactNode {
  return (
    <>
      {data.notes.length === 0 ? null : (
        <div role="status" data-state="narrowed" className="flex flex-col gap-0.5 text-xs text-warn">
          {data.notes.map((note) => (
            <p key={note}>{note}</p>
          ))}
        </div>
      )}

      <div role="group" aria-label="Filters" className="flex flex-wrap items-center gap-2">
        <TileGrid density="compact" label="What to show">
          {tiles(data).map((t) => (
            <FilterTile
              key={t.show}
              data-filter={t.show}
              data-count={String(t.count)}
              count={t.count < 0 ? '–' : data.complete ? t.count : `${String(t.count)}+`}
              label={t.label}
              tone={t.tone}
              pressed={show === t.show}
              onClick={() => pick(show === t.show ? 'all' : t.show)}
              hint={t.count < 0 ? 'Could not be read: see below.' : t.hint}
            />
          ))}
        </TileGrid>
        {focus === '' ? null : (
          <span data-focus={focus} className="text-xs text-muted">
            Certificate {focus}
          </span>
        )}
        {show === 'all' && focus === '' ? null : (
          <button type="button" onClick={() => pick('all')} className={btnSecondary}>
            All
          </button>
        )}
      </div>

      <section aria-label="At a glance" className="flex flex-wrap items-center gap-6 rounded-lg border border-line bg-panel p-3">
        <ReadyRing {...data.ring} />
        <div className="min-w-64 flex-1">
          <ExpiryStrip strip={data.strip} now={data.now} />
        </div>
      </section>

      {show === 'acme' ? (
        <AcmeSection cluster={cluster} data={data} />
      ) : show === 'issuers' ? (
        <IssuerSection data={data} />
      ) : (
        <Certificates cluster={cluster} data={data} show={show} focus={focus} />
      )}
    </>
  );
}

function objectHref(cluster: string, kind: string, namespace: string, name: string): string {
  return `/c/${encodeURIComponent(cluster)}/${kind}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

const CERTIFICATES = 'certificates.cert-manager.io';

/** The certificates the tile picked, soonest first, a page of rows at a time. */
export function Certificates({ cluster, data, show, focus }: { cluster: string; data: Overview; show: Show; focus: string }): ReactNode {
  const [limit, setLimit] = useState(PAGE_ROWS);
  const rows = data.certificates.filter((c) => passes(c, show) && (focus === '' || `${c.namespace}/${c.name}` === focus));
  if (data.counts.total === 0) {
    return (
      <div role="status" data-state="empty" className="rounded-lg border border-line bg-panel p-3 text-sm">
        <p className="font-medium">No certificate{data.namespace === '' ? '' : ` in ${data.namespace}`}</p>
      </div>
    );
  }
  if (rows.length === 0) {
    return (
      <p data-state="none-match" className="text-xs text-muted">
        No match
      </p>
    );
  }
  const drawn = rows.slice(0, limit);
  return (
    <section aria-label="Certificates" className="flex flex-col gap-2">
      <div className="overflow-x-auto rounded-lg border border-line">
        <table className={`${TABLE} w-full`} data-rows={String(drawn.length)}>
          <thead className="bg-panel">
            <tr className={HEAD_ROW}>
              <th className={headCell()}>Certificate</th>
              <th className={headCell()}>Expires</th>
              <th className={headCell()}>Ready</th>
              <th className={headCell()}>Issuer</th>
              <th className={headCell()}>Names</th>
            </tr>
          </thead>
          <tbody>
            {drawn.map((c) => (
              <Row key={`${c.namespace}/${c.name}`} cluster={cluster} c={c} />
            ))}
          </tbody>
        </table>
      </div>
      <p
        className="flex items-center gap-2 text-xs text-muted"
        title={data.shown < data.counts.total ? `The first ${String(data.shown)} of ${String(data.counts.total)} read, alarms first` : undefined}
      >
        {String(drawn.length)} of {String(rows.length)}
        {drawn.length < rows.length ? (
          <button type="button" data-more="" onClick={() => setLimit((n) => n + PAGE_ROWS)} className={btnSecondary}>
            Show {String(Math.min(PAGE_ROWS, rows.length - drawn.length))} more
          </button>
        ) : null}
      </p>
    </section>
  );
}

function Row({ cluster, c }: { cluster: string; c: Certificate }): ReactNode {
  const names = c.dnsNames.join(', ') + (c.moreNames > 0 ? ` +${String(c.moreNames)}` : '');
  return (
    <tr data-cert={`${c.namespace}/${c.name}`} data-band={c.band} className={ROW}>
      <td className={bodyCell()}>
        <Link to={objectHref(cluster, CERTIFICATES, c.namespace, c.name)} className={`text-fg hover:underline ${focusRing}`}>
          {c.name}
        </Link>
        <span className="ml-1.5 text-muted">{c.namespace}</span>
      </td>
      <td className={bodyCell()} title={c.notAfter === '' ? 'Never issued' : `Not after ${c.notAfter}`}>
        <DaysBar c={c} />
      </td>
      <td className={bodyCell()}>
        <span className="flex items-center gap-1">
          <Pill tone={c.ready === 'True' ? 'ok' : 'bad'} title={c.message === '' ? undefined : c.message}>
            {c.ready === 'True' ? 'Ready' : c.reason === '' ? 'Not ready' : reasonWords(c.reason)}
          </Pill>
          {c.renewalOverdue ? (
            <Pill tone="warn" title={`Renewal was due ${c.renewalTime}`}>
              renewal late
            </Pill>
          ) : null}
        </span>
      </td>
      <td className={`${bodyCell()} text-muted`}>
        {c.issuerKind === 'ClusterIssuer' ? 'cluster/' : ''}
        {c.issuerName}
      </td>
      <td className={`${bodyCell()} text-muted`} title={names}>
        {names}
      </td>
    </tr>
  );
}

function SectionNotice({ state, message }: { state: string; message: string | undefined }): ReactNode {
  return (
    <div role="status" data-state={state} className="rounded-lg border border-line bg-panel p-3 text-sm">
      <p className={`font-medium ${state === 'refused' ? 'text-warn' : state === 'failed' ? 'text-bad' : ''}`} title={message}>
        {state === 'refused' ? 'Refused' : state === 'failed' ? 'Could not read' : 'Not installed'}
      </p>
    </div>
  );
}

function AcmeSection({ cluster, data }: { cluster: string; data: Overview }): ReactNode {
  const s = data.acme;
  if (s.state !== 'read') return <SectionNotice state={s.state} message={s.message} />;
  if (s.items.length === 0) {
    return (
      <p role="status" data-state="empty" className="text-xs text-muted">
        None failing
      </p>
    );
  }
  return (
    <section aria-label="ACME failing" className="flex flex-col gap-2">
      {s.items.slice(0, PAGE_ROWS).map((a: Acme) => (
        <div key={`${a.kind}/${a.namespace}/${a.name}`} data-acme={a.kind} className="rounded-lg border border-l-3 border-line border-l-bad bg-panel px-3 py-2 text-xs">
          <p className="flex flex-wrap items-center gap-1.5">
            <Pill tone="bad">{a.kind}</Pill>
            {a.type === '' ? null : <Pill>{a.type}</Pill>}
            <Link
              to={objectHref(cluster, a.kind === 'Order' ? 'orders.acme.cert-manager.io' : 'challenges.acme.cert-manager.io', a.namespace, a.name)}
              className={`text-sm text-fg hover:underline ${focusRing}`}
            >
              {a.dnsName === '' ? a.name : a.dnsName}
            </Link>
            <span className="text-muted">
              {a.namespace}
              {a.certificate === '' ? '' : ` · for ${a.certificate}`} · {a.state === '' ? 'no state' : a.state}
            </span>
          </p>
          {a.reason === '' ? null : <p className="mt-1 break-words text-muted">{reasonWords(a.reason)}</p>}
        </div>
      ))}
      {s.items.length > PAGE_ROWS ? <p className="text-xs text-muted">and {String(s.items.length - PAGE_ROWS)} more</p> : null}
    </section>
  );
}

function IssuerSection({ data }: { data: Overview }): ReactNode {
  const s = data.issuers;
  const broken = s.items.filter((i) => i.broken);
  return (
    <section aria-label="Issuers" className="flex flex-col gap-2">
      {s.state === 'read' ? null : <SectionNotice state={s.state} message={s.message} />}
      {broken.length === 0 ? (
        s.state === 'read' ? (
          <p role="status" data-state="empty" className="text-xs text-muted">
            All ready · {String(s.total)}
          </p>
        ) : null
      ) : (
        <TileGrid label="Broken issuers">
          {broken.slice(0, PAGE_ROWS).map((i: Issuer) => (
            <Tile
              key={`${i.kind}/${i.namespace ?? ''}/${i.name}`}
              data-issuer={i.name}
              title={i.name}
              tone="bad"
              badges={
                <>
                  <Pill tone="bad">{i.reason === '' ? 'Not ready' : reasonWords(i.reason)}</Pill>
                  <Pill>{i.type === '' ? '?' : i.type}</Pill>
                </>
              }
              detail={i.kind === 'ClusterIssuer' ? 'ClusterIssuer' : `Issuer · ${i.namespace ?? ''}`}
            >
              <p className="text-xs break-words text-muted">{i.message === '' ? 'No message.' : i.message}</p>
            </Tile>
          ))}
        </TileGrid>
      )}
    </section>
  );
}

