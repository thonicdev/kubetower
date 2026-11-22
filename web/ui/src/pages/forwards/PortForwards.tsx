import { useState, type ReactNode } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useParams } from 'react-router';

import { closeForward, forwardsQuery, queryKeys, useCapabilities, type Capabilities, type Forward } from '../../api';
import { btnSecondary } from '../../components/buttons';
import { DataGrid, type GridColumn, type GridSort } from '../../components/DataGrid';
import { FilterBox } from '../../components/FilterBox';
import { FilterMenu } from '../../components/FilterMenu';
import { formatInstant, shortDuration } from '../../components/instant';
import { useNow } from '../../components/useNow';
import { useUrlParameter } from '../../components/useUrlParameter';
import { externalHref } from '../../externalHref';
import { focusRing } from '../../focus';
import { useDeclarePageFacts } from '../../shell/pageFacts';
import { Failure, PANEL_BOX, Pending } from '../../state/StateNotice';
import { objectPath } from '../object/address';
import { forwardAddress, forwardLink } from './address';

/**
 * The port-forwards this console holds for one cluster, at
 * `/c/:cluster/port-forwards`.
 *
 * ## Why there is a page
 *
 * A forward is opened from a pod's or a service's Ports panel. Without this
 * page it would be visible and stoppable only there, so finding or stopping one
 * means remembering which object it came from. The menu's Network
 * heading ends with an entry for this list - added by the server, in the
 * one place the menu is composed (`internal/web/forwardsnav.go`), and only
 * where the console opens forwards at all.
 *
 * ## Process state, not a kind - drawn as a list all the same
 *
 * `GET /api/forwards` is a plain array of what this process holds, for every
 * cluster, not a cluster read in an envelope. So this page is not a
 * `ResourceList`: it asks the one list the Ports panels ask, keeps the rows of
 * the cluster in the address, and draws every column from the `Forward` the
 * server sent. Nothing is derived here but the age, which is `openedAt` against
 * the clock.
 *
 * Not being a `ResourceList` is no reason to look different from one. The page
 * is drawn as `HelmList` is: the list's panel head - heading, count, then the
 * search box and its funnel at the far end - and the same `DataGrid` in an
 * `id="panel"` box, so the headings, fonts, widths, sort and empty state are
 * the ones every other list has. What a resource list has and this does not:
 * the namespace menu, the column chooser and stored widths (the server keeps
 * them per kind, and forwards are not a kind), and row selection.
 *
 * **One cache entry with the Ports panels** (`queryKeys.forwards`): a stop here
 * takes the forward out of the list they read too, so a pod's panel opened
 * afterwards offers Forward again rather than an address that no longer
 * answers - and a forward opened there is listed here on arrival.
 *
 * ## Where the console does not forward
 *
 * `capabilities.portForward` false - the in-cluster console, or several
 * replicas behind one address - and the forward operations are not registered:
 * the menu has no entry and nothing is asked. Somebody who types the address
 * gets a sentence saying so, and where the command is instead.
 */
export function PortForwards(): ReactNode {
  const { cluster = '' } = useParams<{ cluster: string }>();
  useDeclarePageFacts({ title: 'Port forwards' });
  const [search, setSearch] = useUrlParameter('search');
  const [sort, setSort] = useState<GridSort | undefined>(undefined);

  const capabilities = useCapabilities();
  const offered = capabilities.data?.portForward === true;
  // Re-read while the page is open: a tunnel can end without the user -
  // the pod it reached deleted, the connection lost - and a list of live
  // tunnels that keeps one the process has dropped is the list lying.
  const live = useQuery({ ...forwardsQuery(offered), refetchInterval: REFRESH_MS });

  // The process holds every cluster's forwards; this page is one cluster's.
  const here = live.data?.filter((one) => one.cluster === cluster);
  const shown = here === undefined ? undefined : matching(here, search);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="flex flex-wrap items-center gap-3 border-b border-line px-3 py-2">
        <h1 className="text-lg font-medium">Port forwards</h1>
        {/* The count comes from the answer, never from the table. */}
        <p aria-live="polite" className="text-xs text-muted">
          {here === undefined || shown === undefined
            ? ''
            : shown.length === here.length
              ? `${String(here.length)} listed`
              : `${String(shown.length)} of ${String(here.length)} listed`}
        </p>
        {offered ? (
          <div className="ml-auto flex min-w-0 max-w-full flex-wrap items-center justify-end gap-2">
            <div className="flex min-w-0 items-center">
              <FilterBox
                name="search"
                value={search}
                onChange={setSearch}
                label="Filter port forwards"
                placeholder="search all columns"
              >
                {/* Inert, as on every list with no column to filter by: absent,
                    it read as missing beside the lists that have it. */}
                <FilterMenu columns={[]} onToggle={() => undefined} onClear={() => undefined} />
              </FilterBox>
            </div>
          </div>
        ) : null}
        {/* A re-read that failed over a list already drawn: the list stays,
            and it is said that it may be behind. */}
        {live.isError && live.data !== undefined ? (
          <p role="status" className="basis-full text-xs text-warn">
            The list could not be read again; it may no longer match what is open.
          </p>
        ) : null}
      </header>

      {/* `id="panel"` and nothing between it and the grid: `theme.css` scopes
          the grid's fixed layout to `#panel > .table-scroll > .kt-grid`. */}
      <div id="panel" className={`relative min-h-0 flex-1 overflow-auto ${PANEL_BOX}`}>
        {capabilities.data === undefined ? (
          capabilities.isError ? (
            <Failure error={capabilities.error} onRetry={() => void capabilities.refetch()} />
          ) : (
            <Pending what="what this console offers" />
          )
        ) : !offered ? (
          <p
            data-note="not-offered"
            data-box
            className="rounded-lg border border-line bg-panel-2 p-3 text-sm text-muted"
          >
            This console does not open port-forwards{whyNoForwards(capabilities.data)}. The Ports section of a
            pod&rsquo;s or a service&rsquo;s Details shows the <code>kubectl port-forward</code> command to run on
            your own machine instead.
          </p>
        ) : shown === undefined ? (
          live.isError ? (
            <Failure error={live.error} onRetry={() => void live.refetch()} />
          ) : (
            <Pending what="the port-forwards" />
          )
        ) : (
          <DataGrid
            columns={COLUMNS}
            rows={ordered(shown, sort)}
            rowKey={(forward) => forward.id}
            label="Port forwards on this cluster"
            stickyHead
            sort={sort}
            onSort={(key) => {
              setSort(nextSort(sort, key));
            }}
            empty={
              search === '' ? (
                <div data-empty="kind">
                  <p className="text-sm text-muted">No port-forwards on this cluster.</p>
                </div>
              ) : (
                <div data-empty="filtered">
                  <p className="text-sm text-muted">No port-forwards matched “{search}”.</p>
                  <button
                    type="button"
                    onClick={() => {
                      setSearch('');
                    }}
                    className={`mt-2 ${btnSecondary}`}
                  >
                    Clear the filter
                  </button>
                </div>
              )
            }
          />
        )}
      </div>
    </div>
  );
}

/**
 * Why forwards are off, from the profile the server described - the two
 * reasons it turns them off for - as a clause to end the sentence with, or
 * nothing where the profile says neither.
 */
function whyNoForwards(capabilities: Capabilities): string {
  if (capabilities.mode === 'shared') {
    return ': it runs in the cluster for everyone who signs in, so a forward would open inside its own pod, and the address it gave you would not be on your machine';
  }
  if (capabilities.replicated === true) {
    return ': it runs as several replicas behind one address, so a forward would be a listener inside whichever one answered';
  }
  return '';
}

/** How often the list is asked again while the page is open. */
const REFRESH_MS = 15_000;

/** What a column sorts and searches by: the value drawn, or `undefined` where
 *  there is none. */
type Read = (forward: Forward) => string | number | undefined;

type ForwardColumn = GridColumn<Forward> & {
  read: Read;
  /** Whether the search box looks in this column: not where the cell draws
   *  something other than what `read` returns. */
  searched: boolean;
};

function column(
  key: string,
  label: string,
  read: Read,
  cell: (forward: Forward) => ReactNode,
  extra: Partial<ForwardColumn> = {},
): ForwardColumn {
  return {
    key,
    label,
    read,
    searched: true,
    measure: (forward) => String(read(forward) ?? '—'),
    cell,
    ...extra,
  };
}

function serviceOf(forward: Forward): string | undefined {
  return forward.service === undefined || forward.service === '' ? undefined : forward.service;
}

/** The target as it is drawn: `svc/web → pod/web-0`, or the pod alone. */
function targetText(forward: Forward): string {
  const service = serviceOf(forward);
  return service === undefined ? `pod/${forward.pod}` : `svc/${service} → pod/${forward.pod}`;
}

/** Where the forward listens, as the cell draws it. */
function addressText(forward: Forward): string {
  return forward.scheme === undefined ? forwardAddress(forward) : forwardLink(forward, forward.scheme);
}

const COLUMNS: readonly ForwardColumn[] = [
  // In the foreground, as a resource list draws its namespace column: the two
  // tables are read the same way.
  column(
    'namespace',
    'Namespace',
    (forward) => forward.namespace,
    (forward) => forward.namespace,
  ),
  column('target', 'Target', targetText, (forward) => <TargetCell forward={forward} />, {
    headTitle: 'The pod the tunnel reached, and the service it was opened on where it was opened from one.',
  }),
  column(
    'remotePort',
    'Remote port',
    (forward) => forward.remotePort,
    (forward) => <span className="tabular-nums">{forward.remotePort}</span>,
    { align: 'end' },
  ),
  column('address', 'Local address', addressText, (forward) => <Address forward={forward} />),
  column(
    'age',
    'Age',
    (forward) => forward.openedAt,
    (forward) => (
      <span className="text-muted">
        <Age at={forward.openedAt} />
      </span>
    ),
    // The cell draws an age that moves with the clock, not the instant it
    // sorts by, so the search box leaves this column out rather than match a
    // string nobody sees.
    { searched: false, measure: () => '59m' },
  ),
  // A refused stop's sentence goes under the button, so the cell wraps.
  column('stop', 'Stop', () => undefined, (forward) => <Stop forward={forward} />, {
    searched: false,
    sortable: false,
    measure: () => 'Stopping…',
    wrap: 'message',
  }),
];

/** The rows whose values hold every word of `search`, case aside. */
function matching(forwards: readonly Forward[], search: string): readonly Forward[] {
  const words = search
    .toLowerCase()
    .split(/\s+/)
    .filter((word) => word !== '');
  if (words.length === 0) return forwards;
  return forwards.filter((forward) => {
    const text = COLUMNS.filter((each) => each.searched)
      .map((each) => String(each.read(forward) ?? ''))
      .join(' ')
      .toLowerCase();
    return words.every((word) => text.includes(word));
  });
}

/** A copy in the chosen order; a missing value sorts last either way. */
function ordered(forwards: readonly Forward[], sort: GridSort | undefined): readonly Forward[] {
  if (sort === undefined) return forwards;
  const by = COLUMNS.find((each) => each.key === sort.key);
  if (by === undefined) return forwards;
  const sign = sort.direction === 'asc' ? 1 : -1;
  return [...forwards].sort((left, right) => {
    const a = by.read(left);
    const b = by.read(right);
    if (a === undefined && b === undefined) return 0;
    if (a === undefined) return 1;
    if (b === undefined) return -1;
    if (typeof a === 'number' && typeof b === 'number') return sign * (a - b);
    return sign * String(a).localeCompare(String(b), undefined, { numeric: true });
  });
}

/** Ascending, then descending, then the server's order - every list's. */
function nextSort(current: GridSort | undefined, key: string): GridSort | undefined {
  if (current === undefined || current.key !== key) return { key, direction: 'asc' };
  if (current.direction === 'asc') return { key, direction: 'desc' };
  return undefined;
}

/** The service the forward was opened on, where there is one, then the pod.
 *  `data-forward` names the row's forward. */
function TargetCell({ forward }: { forward: Forward }): ReactNode {
  const service = serviceOf(forward);
  return (
    <span data-forward={forward.id} className="flex flex-wrap items-baseline gap-1">
      {service === undefined ? null : (
        <>
          <Target
            cluster={forward.cluster}
            kind="services"
            namespace={forward.namespace}
            name={service}
            title="The service the forward was opened on"
          />
          {/* A service forward is not load balanced: it picked one ready
              pod and stays on it, so the pod is the fact. */}
          <span className="text-muted" aria-hidden="true">
            &rarr;
          </span>
        </>
      )}
      <Target
        cluster={forward.cluster}
        kind="pods"
        namespace={forward.namespace}
        name={forward.pod}
        title="The pod the tunnel reached"
      />
    </span>
  );
}

function Stop({ forward }: { forward: Forward }): ReactNode {
  const client = useQueryClient();
  const [failure, setFailure] = useState<string | null>(null);

  // The Ports panel's own stop, on the same cache entry: the row goes from the
  // list every panel reads, and a re-read follows so what is drawn is what the
  // server holds.
  const stop = useMutation({
    mutationFn: () => closeForward(forward.id),
    onSuccess: () => {
      setFailure(null);
      client.setQueryData<Forward[]>(queryKeys.forwards, (before) =>
        (before ?? []).filter((one) => one.id !== forward.id),
      );
      void client.invalidateQueries({ queryKey: queryKeys.forwards });
    },
    // The row stays and says why: a stop the server refused is a forward still
    // open. A forward already gone answers 404, and the re-read takes it away.
    onError: (error: Error) => {
      setFailure(error.message);
      void client.invalidateQueries({ queryKey: queryKeys.forwards });
    },
  });

  return (
    <>
      <button
        type="button"
        disabled={stop.isPending}
        title={`Stop forwarding ${String(forward.remotePort)}; the address stops answering and the slot is freed`}
        onClick={() => stop.mutate()}
        className={btnSecondary}
      >
        {stop.isPending ? 'Stopping…' : 'Stop'}
      </button>
      {failure === null ? null : (
        // The server's own sentence, drawn rather than paraphrased.
        <span role="alert" className="block text-xs text-bad">
          {failure}
        </span>
      )}
    </>
  );
}

/** One end of the tunnel, as a link to that object's page. */
function Target({
  cluster,
  kind,
  namespace,
  name,
  title,
}: {
  cluster: string;
  kind: 'pods' | 'services';
  namespace: string;
  name: string;
  title: string;
}): ReactNode {
  return (
    <Link
      to={objectPath({ cluster, kind, namespace, name })}
      title={title}
      className={`rounded-xs text-accent underline-offset-2 hover:underline ${focusRing}`}
    >
      {kind === 'services' ? 'svc' : 'pod'}/{name}
    </Link>
  );
}

/**
 * Where the forward listens, as a link where the server said which scheme it
 * speaks and as the address alone where it did not. Written through
 * `./address.ts`, so the loopback spelling that keeps the console's cookie from
 * the tunnel is the one the Ports panel uses, and through `externalHref`, the
 * one guard every anchor that leaves the console passes.
 */
function Address({ forward }: { forward: Forward }): ReactNode {
  const scheme = forward.scheme;
  if (scheme === undefined) {
    return (
      <span title="The server did not say which scheme this port speaks" className="tabular-nums">
        {forwardAddress(forward)}
      </span>
    );
  }
  const href = externalHref(forwardLink(forward, scheme));
  if (href === undefined) return <span className="tabular-nums">{forwardLink(forward, scheme)}</span>;
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      className={`rounded-xs text-accent underline-offset-2 tabular-nums hover:underline ${focusRing}`}
    >
      {forwardLink(forward, scheme)}
    </a>
  );
}

/** How long the forward has been open, on the server's scale, with the instant
 *  on hover. A forward the server gave no time for says so rather than "0s". */
function Age({ at }: { at: string | undefined }): ReactNode {
  const now = useNow();
  const opened = at === undefined ? Number.NaN : Date.parse(at);
  if (Number.isNaN(opened)) return <span title="The server gave no time for this forward">—</span>;
  return (
    <span title={formatInstant(new Date(opened), { seconds: true })}>{shortDuration(now - opened)}</span>
  );
}
