import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useMemo, type ReactNode } from 'react';
import { useParams, useSearchParams } from 'react-router';
import { Pending as Waiting, useDeclarePageFacts } from '@kubetower/ui';

import { DEFAULT_SPAN, SPANS, isRefused, statsQuery, type Span, type Stats } from './data';
import { EnableMetrics } from './EnableMetrics';
import { TILE_KEYS, podBars, stateWord, upstreamBars, type TileKey } from './model';
import { Bars, CacheChart, LatencyChart, RcodeChart, SpanControl, Tiles } from './parts';

/** A short state in place of figures; the reason is the hover. */
function State({ word, title }: { word: string; title: string }): ReactNode {
  return (
    <p data-state={word} title={title} className={`text-sm ${word === 'Refused' ? 'text-warn' : 'text-muted'}`}>
      {word}
    </p>
  );
}

/** The figures of one read, under the filter `show` names. */
export function DNSBody({ stats, show, onShow }: { stats: Stats; show: TileKey | ''; onShow: (key: TileKey | '') => void }): ReactNode {
  const pods = useMemo(() => podBars(stats, show === '' ? 'qps' : show, show !== ''), [stats, show]);
  const upstreams = useMemo(() => upstreamBars(stats, show === '' ? 'qps' : show, show !== ''), [stats, show]);
  const series = stats.series;
  return (
    <div data-dns="" className="flex flex-col gap-3">
      <Tiles now={stats.now} show={show} onShow={onShow} />
      {stats.partial ? (
        <p data-partial="" title={stats.partial} className="text-xs text-warn">
          Partial
        </p>
      ) : null}
      {series === undefined ? null : (
        <div className="flex flex-wrap gap-2">
          <RcodeChart series={series} />
          <LatencyChart series={series} />
          <CacheChart series={series} />
        </div>
      )}
      <div className="flex flex-wrap gap-4">
        <Bars title="CoreDNS pods" rows={pods} none={show === '' ? 'No CoreDNS' : 'None over'} />
        <Bars title="Upstreams" rows={upstreams} none={show === '' ? 'No upstreams' : 'None over'} />
      </div>
    </div>
  );
}

/**
 * DNS, at `/c/:cluster/x/dns`: CoreDNS's health from its Prometheus. The
 * address carries `span` and `show`. Asked again only while the page is on
 * screen, at the span's step and never faster than every 15 s. Nothing here
 * changes a cluster.
 */
export function DNSPage(): ReactNode {
  const { cluster = '' } = useParams<{ cluster: string }>();
  useDeclarePageFacts({ title: 'DNS' });
  const [params, setParams] = useSearchParams();
  const asked = params.get('span') ?? '';
  const span: Span = (SPANS as readonly string[]).includes(asked) ? (asked as Span) : DEFAULT_SPAN;
  const showAsked = params.get('show') ?? '';
  const show: TileKey | '' = (TILE_KEYS as readonly string[]).includes(showAsked) ? (showAsked as TileKey) : '';
  const query = useQuery({ ...statsQuery(cluster, span), placeholderData: keepPreviousData });
  const stats = query.data;

  const write = (name: string, value: string, fallback: string) =>
    setParams((current) => {
      const out = new URLSearchParams(current);
      if (value === '' || value === fallback) out.delete(name);
      else out.set(name, value);
      return out;
    });

  const word = stats === undefined ? null : stateWord(stats);
  return (
    <div id="dns" className="flex flex-col gap-4 p-4" aria-busy={query.isFetching}>
      <header className="flex flex-wrap items-center gap-3">
        <h1 className="text-xl font-semibold">DNS</h1>
        {stats?.version ? <span className="text-xs text-muted">CoreDNS {stats.version}</span> : null}
        {stats !== undefined && word === null ? (
          <div className="ml-auto">
            <SpanControl span={span} onSpan={(s) => write('span', s, DEFAULT_SPAN)} />
          </div>
        ) : null}
      </header>
      {stats === undefined ? (
        query.isError ? (
          <State
            word={isRefused(query.error) ? 'Refused' : 'Unreadable'}
            title={query.error instanceof Error ? query.error.message : 'The read failed'}
          />
        ) : (
          <Waiting what="DNS" />
        )
      ) : word !== null ? (
        <>
          <State word={word} title={stats.detail ?? ''} />
          {word === 'No metrics' ? <EnableMetrics /> : null}
        </>
      ) : (
        <DNSBody stats={stats} show={show} onShow={(key) => write('show', key, '')} />
      )}
    </div>
  );
}
