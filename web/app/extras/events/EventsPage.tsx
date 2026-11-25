import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useMemo, useState, type ReactNode } from 'react';
import { useParams, useSearchParams } from 'react-router';
import { Failure, Pending as Waiting, btnSecondary, useDeclarePageFacts } from '@kubetower/ui';

import { DEFAULT_WINDOW, WINDOWS, namespaceQuery, pageQuery, type Answer, type Tiles as TilesData, type Window } from './data';
import { TILE_KEYS, narrow, normalCount, shownTypes, type Filters, type TileKey } from './model';
import { Causes, Controls, Correlations, Sources, Tiles, Timeline, useHighlight } from './parts';

/**
 * The body both the page and an object's tab draw: the tiles (the page only),
 * the controls, the correlations, the timeline and the causes under the filters
 * given, Normal Events only once asked for.
 */
export function EventsBody({
  cluster,
  answer,
  filters,
  onFilters,
  window,
  onWindow,
  tiles = true,
  tilesOf = answer.tiles,
}: {
  cluster: string;
  answer: Answer;
  filters: Filters;
  onFilters: (next: Filters) => void;
  window: Window;
  onWindow: (w: Window) => void;
  tiles?: boolean;
  /** The figures the tiles show; the answer's own when omitted, null while a namespace's are read. */
  tilesOf?: TilesData | null | undefined;
}): ReactNode {
  const narrowed = useMemo(() => narrow(answer, filters), [answer, filters]);
  // Normal Events wait behind a toggle, unless the Type filter chose a type.
  const [normal, setNormal] = useState(false);
  const causes = useMemo(() => shownTypes(narrowed, filters, normal), [narrowed, filters, normal]);
  const highlight = useHighlight();
  return (
    <div data-events="" className="flex flex-col gap-3">
      {tiles ? <Tiles tiles={tilesOf ?? undefined} filters={filters} onTile={(tile) => onFilters({ ...filters, tile })} /> : null}
      <Controls answer={answer} filters={filters} onFilters={onFilters} window={window} onWindow={onWindow} />
      <Sources answer={answer} />
      <Correlations answer={answer} causes={causes} highlight={highlight} />
      <Timeline
        answer={answer}
        causes={causes}
        highlight={highlight}
        normal={filters.type === '' ? { on: normal, count: normalCount(narrowed), onToggle: () => setNormal(!normal) } : undefined}
        scope={filters.namespace === '' ? undefined : { namespace: filters.namespace, tiles: tilesOf }}
      />
      <Causes cluster={cluster} answer={answer} causes={causes} highlight={highlight} hidden={normal || filters.type !== '' ? 0 : normalCount(narrowed)} />
    </div>
  );
}

/**
 * Event Intelligence, at `/c/:cluster/x/events`: the tiles, the timeline of
 * what changed with the Events under it, and the causes. The address carries
 * `window`, `tile`, `namespace`, `type` and `kind`, so a link lands on the same view.
 * Asked again every 30 s only while the page is on screen. Nothing here
 * changes a cluster.
 */
export function EventsPage(): ReactNode {
  const { cluster = '' } = useParams<{ cluster: string }>();
  useDeclarePageFacts({ title: 'Event Intelligence' });
  const [params, setParams] = useSearchParams();
  const asked = params.get('window') ?? '';
  const window: Window = (WINDOWS as readonly string[]).includes(asked) ? (asked as Window) : DEFAULT_WINDOW;
  const tile = params.get('tile') ?? '';
  const filters: Filters = {
    tile: (TILE_KEYS as readonly string[]).includes(tile) ? (tile as TileKey) : '',
    namespace: params.get('namespace') ?? params.get('ns') ?? '',
    type: params.get('type') ?? '',
    kind: params.get('kind') ?? '',
  };
  const query = useQuery({ ...pageQuery(cluster, window), placeholderData: keepPreviousData });
  const answer = query.data;
  // The tiles follow the namespace: the route reads that namespace alone, so
  // its figures are the namespace's, where the whole answer's are the cluster's.
  const scoped = useQuery({ ...namespaceQuery(cluster, window, filters.namespace), enabled: filters.namespace !== '', placeholderData: keepPreviousData });
  const tilesOf = filters.namespace === '' ? undefined : scoped.data?.namespace === filters.namespace ? scoped.data.tiles : null;

  const write = (next: Filters, w: Window) =>
    setParams(
      (current) => {
      const out = new URLSearchParams(current);
      const put = (name: string, value: string) => (value === '' ? out.delete(name) : out.set(name, value));
      put('window', w === DEFAULT_WINDOW ? '' : w);
      put('tile', next.tile);
      out.delete('ns');
      put('namespace', next.namespace);
      put('type', next.type);
      put('kind', next.kind);
      return out;
      },
      { replace: true },
    );

  return (
    <div id="events" className="flex flex-col gap-4 p-4" aria-busy={query.isFetching}>
      <header className="flex flex-wrap items-center gap-3">
        <h1 className="text-xl font-semibold">Event Intelligence</h1>
        <button type="button" className={`ml-auto ${btnSecondary}`} onClick={() => void query.refetch()} disabled={query.isFetching}>
          {query.isFetching ? 'Reading…' : 'Read again'}
        </button>
      </header>
      {answer === undefined ? (
        query.isError ? (
          <Failure error={query.error} onRetry={() => void query.refetch()} />
        ) : (
          <Waiting what="the events" />
        )
      ) : (
        <>
          {query.isError ? <Failure error={query.error} onRetry={() => void query.refetch()} /> : null}
          <EventsBody cluster={cluster} answer={answer} tilesOf={tilesOf} filters={filters} onFilters={(next) => write(next, window)} window={window} onWindow={(w) => write(filters, w)} />
        </>
      )}
    </div>
  );
}
