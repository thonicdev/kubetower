import { useQuery } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { Link } from 'react-router';
import { Pending, Pill, focusRing, type ExtensionTabProps } from '@kubetower/ui';

import { certificateHref, daysWord, reasonWords, objectQuery, type Certificate, type Chain } from './data';
import { ChainView } from './Chain';
import { BAND_FILL, BAND_TEXT, BAR_DAYS, ProblemNotice } from './parts';

/**
 * The Certificate tab, in the panel of a Certificate, a Secret or an Ingress:
 * each certificate behind the object, its days left as a bar over 90 days, its
 * dates, whether it is ready and why not, and its issuer. One read, of the
 * object's namespace only; none on an interval.
 */
export default function CertificateTab({ cluster, kind, namespace, name }: ExtensionTabProps): ReactNode {
  const query = useQuery({ ...objectQuery(cluster, kind, namespace ?? '', name), enabled: namespace !== undefined });
  if (query.isError) return <ProblemNotice error={query.error} onRetry={() => void query.refetch()} />;
  if (query.data === undefined) return <Pending what="its certificate" />;
  if (query.data === null) {
    return (
      <p role="status" data-state="none" className="text-sm text-muted">
        No certificate
      </p>
    );
  }
  return (
    <div className="flex flex-col gap-3" data-cert-tab="">
      {query.data.certificates.map((c) => (
        <CertificateCard key={`${c.namespace}/${c.name}`} cluster={cluster} c={c} chain={query.data?.chains[c.name]} />
      ))}
    </div>
  );
}

function when(iso: string): string {
  return iso === '' ? '—' : iso.replace('T', ' ').replace(/:\d\dZ$/, ' UTC');
}

export function CertificateCard({ cluster, c, chain }: { cluster: string; c: Certificate; chain?: Chain | undefined }): ReactNode {
  const pct = c.daysLeft === null ? 0 : Math.max(0, Math.min(100, (c.daysLeft / BAR_DAYS) * 100));
  return (
    <section aria-label={`Certificate ${c.name}`} data-cert={`${c.namespace}/${c.name}`} className="flex flex-col gap-2 rounded-lg border border-line bg-panel p-3">
      <header className="flex flex-wrap items-center gap-2">
        <Link to={certificateHref(cluster, c)} className={`text-sm font-medium text-fg hover:underline ${focusRing}`}>
          {c.name}
        </Link>
        <Pill tone={c.ready === 'True' ? 'ok' : 'bad'} title={c.message === '' ? undefined : c.message}>
          {c.ready === 'True' ? 'Ready' : c.reason === '' ? 'Not ready' : reasonWords(c.reason)}
        </Pill>
        {c.renewalOverdue ? <Pill tone="warn">renewal late</Pill> : null}
        <span className="flex-1" />
        <span className={`text-lg font-medium ${BAND_TEXT[c.band]}`} data-days-word="">
          {daysWord(c)}
        </span>
      </header>
      <div
        role="meter"
        aria-label="Days left, over 90 days"
        aria-valuemin={0}
        aria-valuemax={BAR_DAYS}
        aria-valuenow={Math.max(0, Math.min(BAR_DAYS, c.daysLeft ?? 0))}
        className={`relative h-2.5 overflow-hidden rounded-full ${c.daysLeft === null ? 'border border-dashed border-line' : 'bg-panel-2'}`}
      >
        {c.band === 'expired' ? (
          <div className="absolute inset-0 bg-bad opacity-40" />
        ) : (
          <div className={`absolute inset-y-0 left-0 rounded-full ${BAND_FILL[c.band]}`} style={{ width: `${String(pct)}%` }} />
        )}
        {/* 7 and 30 days, where the color changes. */}
        <div className="absolute inset-y-0 w-px bg-bg" style={{ left: `${String((7 / BAR_DAYS) * 100)}%` }} />
        <div className="absolute inset-y-0 w-px bg-bg" style={{ left: `${String((30 / BAR_DAYS) * 100)}%` }} />
      </div>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs">
        <dt className="text-muted">Not after</dt>
        <dd>{when(c.notAfter)}</dd>
        <dt className="text-muted">Renewal</dt>
        <dd>{when(c.renewalTime)}</dd>
        <dt className="text-muted">Issuer</dt>
        <dd>
          {c.issuerName} <span className="text-muted">({c.issuerKind})</span>
        </dd>
        <dt className="text-muted">Secret</dt>
        <dd>
          {c.secretName} <span className="text-muted">in {c.namespace}</span>
        </dd>
        <dt className="text-muted">Names</dt>
        <dd className="break-words">
          {c.dnsNames.join(', ')}
          {c.moreNames > 0 ? ` +${String(c.moreNames)}` : ''}
        </dd>
        {c.ready === 'True' || c.message === '' ? null : (
          <>
            <dt className="text-muted">Why</dt>
            <dd className="break-words text-bad">{c.message}</dd>
          </>
        )}
      </dl>
      {chain === undefined ? null : <ChainView chain={chain} />}
    </section>
  );
}
