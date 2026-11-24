import { useQuery } from '@tanstack/react-query';
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { Link, useParams, useSearchParams } from 'react-router';
import {
  CopyButton,
  FilterTile,
  Pending,
  Pill,
  Tile,
  TileGrid,
  focusRing,
  useDeclarePageFacts,
  btnSecondary,
} from '@kubetower/ui';

import { STATES, RedisProblem, getProbe, inventoryQuery, type Backend, type Deployment, type Inventory, type ProbeState } from './data';
import { ExternalTile, ProblemNotice, instanceHref, inventoryHref, plural, serviceHref } from './parts';
import {
  AXES,
  FILTER_PARAMETER,
  MODES,
  PILL_TONE,
  SAID_WORDS,
  STATUS_HINT,
  STATUS_TONE,
  STATUS_WORD,
  UNKNOWN,
  filterParams,
  mainMember,
  parseFilters,
  passes,
  readingOf,
  shapeOf,
  value,
  type Axis,
  type Filters,
  type TileReading,
} from './tiles';

/**
 * The Redis viewer's inventory, at `/c/:cluster/x/redis`: **one tile per Redis
 * or Valkey deployment** (as the operator asked, on the model of the tile
 * Settings → Extra draws). The primary, its replicas, the headless Service and
 * the Sentinels of one installation are one tile; the
 * tile opens the member that answers for it, and the instance page switches
 * between the members.
 *
 * **It opens no connection on load.** Every tile is drawn from the API server
 * through the route. The engine and the status are what a probe finds, and the
 * probe is the person's to press - one tunnel to one server.
 *
 * Four states, drawn differently: none found, could not list, narrowed, list.
 * A narrowed list's counts are lower bounds and say so (R5 §5).
 */
export function RedisViewer(): ReactNode {
  const { cluster = '' } = useParams<{ cluster: string }>();
  const [params, setParams] = useSearchParams();
  const scope = (params.get('ns') ?? '').trim();
  const filters = parseFilters(params.getAll(FILTER_PARAMETER));
  useDeclarePageFacts({ title: 'Redis viewer' });

  const inventory = useQuery(inventoryQuery(cluster, scope));
  const { probes, probe, probeEach, probing, stop } = useProbes(cluster);

  const pick = (next: Filters) =>
    setParams(
      (p) => {
        const out = new URLSearchParams(p);
        out.delete(FILTER_PARAMETER);
        for (const v of filterParams(next)) out.append(FILTER_PARAMETER, v);
        return out;
      },
      { replace: true },
    );

  const data = inventory.data;
  const rows = data === undefined ? [] : data.namespaces.flatMap((ns) => ns.deployments.map((d) => ({ namespace: ns.name, d })));
  // The tile probes the member it opens: the one that answers for it.
  const unprobed = rows
    .map(({ d }) => mainMember(d))
    .filter((m): m is NonNullable<typeof m> => m !== undefined && m.reachable && probes[m.key] === undefined);

  return (
    <div className="flex flex-col gap-3 p-4">
      <header className="flex flex-wrap items-center gap-2">
        <h1 className="text-xl font-medium">Redis</h1>
        <p className="text-xs text-muted" data-counts="">
          {cluster}
          {scope === '' ? null : <> · {scope}</>}
          {data === undefined || data.redises === 0 ? null : (
            <>
              {' '}
              · {plural(data.redises, 'deployment')}, {plural(data.services, 'Service')}
            </>
          )}
          {data === undefined || (data.external ?? []).length === 0 ? null : <> · {(data.external ?? []).length} external</>}
        </p>
        <span className="flex-1" />
        {scope === '' ? null : (
          <Link to={inventoryHref(cluster, '')} className={`text-xs text-accent underline ${focusRing}`}>
            All namespaces
          </Link>
        )}
        {probing ? (
          <button type="button" data-probe-stop="" onClick={stop} className={btnSecondary}>
            Stop probing
          </button>
        ) : unprobed.length === 0 ? null : (
          <button
            type="button"
            data-probe-each=""
            onClick={() => probeEach(unprobed)}
            title={`Probe the ${plural(unprobed.length, 'deployment')} not probed yet, one tunnel at a time.`}
            className={btnSecondary}
          >
            Probe all ({unprobed.length})
          </button>
        )}
        <button
          type="button"
          onClick={() => void inventory.refetch()}
          disabled={inventory.isFetching}
          className={btnSecondary}
        >
          {inventory.isFetching ? 'Reading…' : 'Refresh'}
        </button>
      </header>

      {data === undefined ? (
        inventory.isError ? (
          <ProblemNotice error={inventory.error} onRetry={() => void inventory.refetch()} />
        ) : (
          <Pending what="the Services that look like a Redis" />
        )
      ) : (
        <Listed cluster={cluster} data={data} rows={rows} probes={probes} probe={probe} filters={filters} pick={pick} />
      )}
    </div>
  );
}

/** The probes the person asked for, by member, and the one-at-a-time walk. */
function useProbes(cluster: string) {
  const [probes, setProbes] = useState<Record<string, ProbeState>>({});
  const [probing, setProbing] = useState(false);
  const abort = useRef<AbortController | undefined>(undefined);
  useEffect(() => () => abort.current?.abort(), []);

  const run = useCallback(
    async (backend: Backend, signal?: AbortSignal) => {
      const door = backend.doors[0];
      if (door === undefined) return;
      setProbes((p) => ({ ...p, [backend.key]: { status: 'running' } }));
      try {
        const answer = await getProbe(cluster, door, signal);
        setProbes((p) => ({ ...p, [backend.key]: { status: 'answered', answer } }));
      } catch (error) {
        // Stopped, or the request ended before the instance answered: the tile
        // goes back to not probed, rather than counting as an answer.
        if (signal?.aborted === true || (error instanceof RedisProblem && error.code === 'cancelled')) {
          setProbes((p) => Object.fromEntries(Object.entries(p).filter(([key]) => key !== backend.key)));
          return;
        }
        setProbes((p) => ({ ...p, [backend.key]: { status: 'failed', error } }));
      }
    },
    [cluster],
  );

  const probe = useCallback((backend: Backend) => void run(backend), [run]);

  // **One at a time**, never all at once: every probe is a tunnel against the
  // extensions' shared cap, and a burst of them would take the slots other
  // pages and other people are using.
  const probeEach = useCallback(
    async (backends: readonly Backend[]) => {
      abort.current?.abort();
      const controller = new AbortController();
      abort.current = controller;
      setProbing(true);
      try {
        for (const backend of backends) {
          if (controller.signal.aborted) return;
          await run(backend, controller.signal);
        }
      } finally {
        if (abort.current === controller) setProbing(false);
      }
    },
    [run],
  );

  // Stop abandons the walk: the probe in flight is aborted, and the tiles not
  // yet reached stay not probed.
  const stop = useCallback(() => {
    abort.current?.abort();
    abort.current = undefined;
    setProbing(false);
  }, []);

  return { probes, probe, probeEach: (b: readonly Backend[]) => void probeEach(b), probing, stop };
}

type Row = { namespace: string; d: Deployment };

function Listed({
  cluster,
  data,
  rows,
  probes,
  probe,
  filters,
  pick,
}: {
  cluster: string;
  data: Inventory;
  rows: readonly Row[];
  probes: Readonly<Record<string, ProbeState>>;
  probe: (b: Backend) => void;
  filters: Filters;
  pick: (f: Filters) => void;
}): ReactNode {
  const external = data.external ?? [];
  if (data.services === 0 && data.complete && external.length === 0) {
    // A fact about the search, not about the cluster: discovery is a guess over
    // Service ports, so "none matched" is this page's claim and "there is none"
    // is not.
    return (
      <div role="status" data-state="empty" className="rounded-lg border border-line bg-panel p-3 text-sm">
        <p className="font-medium">No Redis found{data.scope === '' ? '' : ` in ${data.scope}`}</p>
        <p className="mt-1 text-xs text-muted">
          Looked for Service ports named <code>redis</code> or <code>valkey</code>, or numbered <code>6379</code>.
        </p>
      </div>
    );
  }

  const read = rows.map((row) => ({ ...row, reading: readingOf(row.d, probes) }));
  const shown = read.filter((row) => passes(row.reading, filters));
  const filtered = AXES.some((a) => filters[a] !== undefined);
  return (
    <>
      {data.complete ? null : (
        <p role="status" data-state="narrowed" className="text-xs text-warn">
          <span className="font-medium">Incomplete list</span>
          {data.note === undefined || data.note === '' ? '' : `: ${data.note}`}. Counts are lower bounds.
        </p>
      )}
      {data.slicesNote === undefined || data.slicesNote === '' ? null : (
        <p data-slices="unread" className="text-xs text-warn">
          {data.slicesNote.charAt(0).toUpperCase() + data.slicesNote.slice(1)}.
        </p>
      )}
      {(data.podsNotes ?? []).map((note) => (
        <p key={note} data-pods="unread" className="text-xs text-warn">
          {note.charAt(0).toUpperCase() + note.slice(1)}.
        </p>
      ))}

      {rows.length === 0 ? null : <Filtering read={read} filters={filters} pick={pick} complete={data.complete} />}

      {shown.length === 0 ? (
        filtered ? (
          <p className="text-xs text-muted">
            Nothing matches.{' '}
            <button type="button" onClick={() => pick({})} className={`text-accent underline ${focusRing}`}>
              Show all
            </button>
          </p>
        ) : null
      ) : (
        <TileGrid label="Redis deployments">
          {shown.map(({ namespace, d, reading }) => (
            <DeploymentTile
              key={d.key}
              cluster={cluster}
              scope={data.scope}
              namespace={namespace}
              deployment={d}
              reading={reading}
              probes={probes}
              onProbe={probe}
            />
          ))}
        </TileGrid>
      )}
      {external.length === 0 || filtered ? null : (
        <TileGrid label="Outside the cluster">
          {external.map((door) => (
            <ExternalTile key={`${door.namespace}/${door.service}`} door={door} to={serviceHref(cluster, door.namespace, door.service)} />
          ))}
        </TileGrid>
      )}
    </>
  );
}

const AXIS_LABEL: Readonly<Record<Axis, string>> = { engine: 'Engine', mode: 'Mode', state: 'Status' };

/** A value's word on its filter tile. */
function word(axis: Axis, v: string): string {
  if (axis === 'state') return STATUS_WORD[v as keyof typeof STATUS_WORD] ?? v;
  return v === UNKNOWN ? 'not known' : v;
}

/**
 * The filter tiles: a count per engine, mode and status, each one narrowing
 * the grid on its axis; the three combine. A value nothing has is not drawn,
 * unless it is the one picked. Under a narrowed list a count is "at least".
 */
function Filtering({
  read,
  filters,
  pick,
  complete,
}: {
  read: readonly { reading: TileReading }[];
  filters: Filters;
  pick: (f: Filters) => void;
  complete: boolean;
}): ReactNode {
  const order: Readonly<Record<Axis, readonly string[]>> = {
    engine: ['Redis', 'Valkey', ...new Set(read.map((r) => r.reading.engine.value).filter((e) => e !== 'Redis' && e !== 'Valkey' && e !== UNKNOWN)), UNKNOWN],
    mode: [...MODES, UNKNOWN],
    state: STATES,
  };
  return (
    <div data-filters="" className="flex flex-wrap items-end gap-x-4 gap-y-2">
      {AXES.map((axis) => {
        const counts = new Map<string, number>();
        for (const r of read) counts.set(value(r.reading, axis), (counts.get(value(r.reading, axis)) ?? 0) + 1);
        const values = order[axis].filter((v) => (counts.get(v) ?? 0) > 0 || filters[axis] === v);
        if (values.length === 0) return null;
        return (
          <div key={axis} role="group" aria-label={AXIS_LABEL[axis]} className="flex max-w-full min-w-0 flex-none flex-col gap-1">
            <span className="text-2xs text-muted">{AXIS_LABEL[axis]}</span>
            <TileGrid density="compact" label={AXIS_LABEL[axis]}>
              {values.map((v) => {
                const n = counts.get(v) ?? 0;
                const on = filters[axis] === v;
                return (
                  <FilterTile
                    key={v}
                    data-filter={`${axis}.${v}`}
                    data-count={String(n)}
                    count={complete ? n : `${n}+`}
                    label={word(axis, v)}
                    tone={axis === 'state' ? STATUS_TONE[v as keyof typeof STATUS_TONE] : undefined}
                    pressed={on}
                    onClick={() => pick({ ...filters, [axis]: on ? undefined : v })}
                    hint={
                      axis === 'state'
                        ? STATUS_HINT[v as keyof typeof STATUS_HINT]
                        : v === UNKNOWN
                          ? `${AXIS_LABEL[axis]} not known until probed.`
                          : `Only ${v}.`
                    }
                  />
                );
              })}
            </TileGrid>
          </div>
        );
      })}
      {AXES.some((a) => filters[a] !== undefined) ? (
        <button type="button" onClick={() => pick({})} className={btnSecondary}>
          All
        </button>
      ) : null}
    </div>
  );
}

/** Where a pill's reading came from, as its hover sentence. */
function from(what: string, r: TileReading['engine']): string {
  if (r.from === undefined) return `${what} not known: probe to ask the server.`;
  return `${r.value}, from ${SAID_WORDS[r.from]}.`;
}

/**
 * One deployment: its name; engine, mode and status as pills; namespace and
 * shape on the second line; the tone from the status. The whole tile opens the
 * member that answers for it. One action: Probe before it has been asked, the
 * in-cluster address to copy after.
 */
function DeploymentTile({
  cluster,
  scope,
  namespace,
  deployment,
  reading,
  probes,
  onProbe,
}: {
  cluster: string;
  scope: string;
  namespace: string;
  deployment: Deployment;
  reading: TileReading;
  probes: Readonly<Record<string, ProbeState>>;
  onProbe: (b: Backend) => void;
}): ReactNode {
  const main = mainMember(deployment);
  const door = main?.doors[0];
  const probe = main === undefined ? undefined : probes[main.key];
  const running = probe?.status === 'running';
  const { engine, mode, state } = reading;
  const keys = probe?.status === 'answered' && probe.answer.overview !== undefined ? plural(probe.answer.overview.keys, 'key') : '';
  const detail = [namespace, shapeOf(deployment, probes, mode.value), keys].filter((p) => p !== '').join(' · ');

  let action: ReactNode;
  if (main !== undefined && main.reachable && door !== undefined) {
    if (probe === undefined || running) {
      action = (
        <button
          type="button"
          data-probe=""
          disabled={running}
          onClick={() => onProbe(main)}
          title={`Open one tunnel to ${door.service}:${door.port} and ask what it is.`}
          className={btnSecondary}
        >
          {running ? 'Probing…' : 'Probe'}
        </button>
      );
    } else {
      action = <CopyButton value={`${door.service}.${door.namespace}.svc:${door.port}`} label="Copy address" />;
    }
  }

  return (
    <Tile
      data-deployment={deployment.key}
      data-status={state}
      data-engine={engine.value}
      data-mode={mode.value}
      title={deployment.name}
      tone={running ? 'info' : STATUS_TONE[state]}
      {...(door === undefined || main?.reachable !== true
        ? {}
        : { to: instanceHref(cluster, door, scope), openLabel: `Open ${deployment.name} (${door.service}:${door.port})` })}
      badges={
        <>
          <Pill neutral={engine.from === 'info' ? 'normal' : 'muted'} title={from('Engine', engine)}>
            {engine.value === UNKNOWN ? 'engine ?' : engine.value}
          </Pill>
          <Pill neutral="muted" title={from('Mode', mode)}>
            {mode.value === UNKNOWN ? 'mode ?' : mode.value}
          </Pill>
          <Pill tone={PILL_TONE[state]} neutral="muted" title={STATUS_HINT[state]}>
            {running ? 'probing' : STATUS_WORD[state]}
          </Pill>
        </>
      }
      detail={detail}
      action={action}
    >
      {probe?.status === 'failed' && main !== undefined ? (
        <ProblemNotice error={probe.error} compact onRetry={() => onProbe(main)} />
      ) : undefined}
    </Tile>
  );
}
