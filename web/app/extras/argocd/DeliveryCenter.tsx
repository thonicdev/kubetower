import { useQuery } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { useParams, useSearchParams } from 'react-router';
import {
  DataGrid,
  Failure,
  ListView,
  Pill,
  StateNotice,
  Tile,
  TileGrid,
  focusRing,
  formatClock,
  formatInstant,
  useDeclarePageFacts,
  Windowed,
  type GridColumn,
  type TileTone,
  btnSecondary,
  btnToggle,
} from '@kubetower/ui';

import type { Destination } from './application';
import { APPLICATION_KIND, deliveryQuery, type Application, type Delivery, type Refusal } from './data';
import { ProjectsAndSets } from './ProjectsAndSets';
import { fluxQuery, type Flux, type FluxObject } from './flux';
import { GrantRead, Install, type EngineKey } from './NextSteps';
import { reportQuery } from './report';
import { argoLink, DEFAULT_SETTINGS, fluxLink, settingsQuery } from './settings';
import { ago, sourceLine, shortRevision } from './summary';
import {
  HEALTH,
  HEALTH_TONE,
  NOT_REPORTED,
  READY,
  SYNC,
  SYNC_TONE,
  TEXT,
  Word,
  severity,
  spoken,
  tileToneOf,
  toneOf,
  worst,
  type Tone,
} from './words';

/**
 * The Delivery Center, at `/c/:cluster/x/argocd`: the Argo CD Applications and
 * the Flux CD Kustomizations and HelmReleases of one cluster, in two sections
 * that never answer for each other. An
 * Application's name opens its own page (`ApplicationPage.tsx`).
 *
 * ## Synced or not, first
 *
 * What is synced and what is out of sync is seen precisely, with little text,
 * and one Application per tile, as Argo CD's own UI draws them (the core's
 * `Tile`). So the Argo CD section opens
 * on compact filter tiles - all, the sync words, the health words present -
 * and the Applications as tiles under them, worst first: name, health and sync
 * as two chips, project and destination in one line. A tile opens its
 * Application's page, where each out-of-sync resource opens on its Argo CD
 * tab and the drift is.
 *
 * ## Closer to Argo CD's own UI
 *
 * The tiles carry a little more detail, a search bar sits at the top as in
 * Argo CD, and the projects and ApplicationSets are part of the page, split by
 * project. So:
 *
 * - a search box (`?q=`) over name, namespace, project, destination and
 *   source, for both engines;
 * - a project filter (`?project=`) beside the sync and health ones;
 * - **Needs attention** first: every Application whose tile is warn or bad,
 *   worst first, each with the one sentence that says why;
 * - then one section per project, worst project first;
 * - each tile adds the source, the revision, auto-sync and the last sync;
 * - under them, the AppProjects and the failing ApplicationSets as tiles,
 *   from the report route, read with the page. The report's tables - worst
 *   offenders, conditions by cause, what was read - are gone: Needs
 *   attention and the tiles say what they said.
 *
 * ## Every other answer drawn as itself
 *
 * - `complete: false` - the rows **and** a notice that they are not all of
 *   them (`extension.Page`);
 * - not installed, not served, ambiguous, refused - four refusals, four
 *   drawings (`data.ts`), the first and the last with what to do
 *   (`NextSteps.tsx`);
 * - a cluster the console does not know - one notice for the page, the
 *   console's sentence once and no retry, in place of both engines' sections:
 *   an answer about the address, not about Argo CD or Flux CD;
 * - a request that failed - the console's `Failure`, never a state.
 *
 * All of it goes through `ListView`, the only way a list reaches the screen in
 * this console.
 */
export function DeliveryCenter(): ReactNode {
  const { cluster = '' } = useParams<{ cluster: string }>();
  // The tool and the UI addresses. A read that fails or is not back yet is the
  // default (auto, no links), never a reason to hold the page.
  // Until the answer is in the tool is auto; a tool the setting leaves out is
  // not asked again after it.
  const settingsRead = useQuery(settingsQuery(cluster));
  const settings = settingsRead.data ?? DEFAULT_SETTINGS;
  const wantArgo = settings.tool !== 'flux';
  const wantFlux = settings.tool !== 'argocd';
  const options = deliveryQuery(cluster);
  // Two observers of one query: the whole answer, for the notices around the
  // list, and the list alone, for `ListView`. One request between them.
  const delivery = useQuery({ ...options, enabled: wantArgo });
  const list = useQuery({ ...options, select: listOf, enabled: wantArgo });

  const incomplete = delivery.data?.incomplete;
  const refusal = delivery.data?.refusal;

  const fluxOptions = fluxQuery(cluster);
  const flux = useQuery({ ...fluxOptions, enabled: wantFlux });
  const fluxList = useQuery({ ...fluxOptions, select: fluxListOf, enabled: wantFlux });

  // **One engine present, the other not mentioned**:
  // an absent engine's section is left out entirely while the other one is
  // there, and says nothing. With neither present, both installs are offered.
  // Refused, not served, ambiguous or failed is not absent and is always
  // drawn. The Tool setting narrows this: one tool alone is the only section,
  // and `both` draws both whatever is installed.
  const argoAbsent = refusal === 'kind_unknown';
  const fluxAbsent = flux.data?.refusal === 'kind_unknown';
  const argoPresent = delivery.data !== undefined && !argoAbsent;
  const fluxPresent = flux.data !== undefined && !fluxAbsent;
  // Neither found, with both asked for: one short state, no install hints.
  const neither = settings.tool === 'auto' && argoAbsent && fluxAbsent;
  const showArgo = !neither && settings.tool !== 'flux' && (settings.tool === 'both' || !(argoAbsent && fluxPresent));
  const showFlux = !neither && settings.tool !== 'argocd' && (settings.tool === 'both' || !(fluxAbsent && argoPresent));

  // **No such cluster is one answer for the whole page**. Both routes
  // resolve the name the same way, so whichever says it first speaks for both,
  // and the page draws it once rather than once per engine.
  const unknown =
    refusal === 'cluster_unknown' ? delivery.data?.list : flux.data?.refusal === 'cluster_unknown' ? flux.data.list : undefined;

  // The filter lives in the address, so a narrowed page can be reloaded and
  // sent.
  const [params, setParams] = useSearchParams();
  const filter = parseFilter(params.get(FILTER_PARAMETER));
  const project = params.get(PROJECT_PARAMETER) ?? undefined;
  const search = params.get(SEARCH_PARAMETER) ?? '';
  const pick = (next: StatusFilter | undefined) =>
    setParams((p) => {
      const out = new URLSearchParams(p);
      if (next === undefined || (filter !== undefined && filterValue(filter) === filterValue(next))) out.delete(FILTER_PARAMETER);
      else out.set(FILTER_PARAMETER, filterValue(next));
      return out;
    });
  const pickProject = (next: string | undefined) =>
    setParams((p) => {
      const out = new URLSearchParams(p);
      if (next === undefined || next === project) out.delete(PROJECT_PARAMETER);
      else out.set(PROJECT_PARAMETER, next);
      return out;
    });
  // Status and project in one navigation: two calls in one click would each
  // start from this render's address, and the second would undo the first.
  const clearFilters = () =>
    setParams((p) => {
      const out = new URLSearchParams(p);
      out.delete(FILTER_PARAMETER);
      out.delete(PROJECT_PARAMETER);
      return out;
    });
  // Typing replaces the entry rather than pushing one per key.
  const type = (next: string) =>
    setParams(
      (p) => {
        const out = new URLSearchParams(p);
        if (next === '') out.delete(SEARCH_PARAMETER);
        else out.set(SEARCH_PARAMETER, next);
        return out;
      },
      { replace: true },
    );

  // The AppProjects and ApplicationSets, read with the page since they are
  // drawn on it.
  const report = useQuery({ ...reportQuery(cluster), enabled: delivery.data !== undefined && refusal === undefined });
  const fluxRows = flux.data?.list.state === 'ok' ? flux.data.list.items : [];
  const argoApps = delivery.data?.list.state === 'ok' ? delivery.data.list.items : undefined;
  useDeclarePageFacts({ title: 'Delivery Center' });

  const fetching = delivery.isFetching || flux.isFetching || report.isFetching;

  return (
    <div className="flex flex-col gap-4 p-4">
      <header className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <div className="flex min-w-0 flex-1 flex-col">
          <h1 className="text-xl font-medium">Delivery Center</h1>
          <Summary apps={argoApps} complete={incomplete === undefined} at={delivery.dataUpdatedAt} />
        </div>
        <button
          type="button"
          onClick={() => {
            void delivery.refetch();
            void flux.refetch();
            if (delivery.data !== undefined && refusal === undefined) void report.refetch();
          }}
          disabled={fetching}
          className={btnSecondary}
        >
          <svg aria-hidden="true" viewBox="0 0 16 16" className={`h-3.5 w-3.5 ${fetching ? 'animate-spin' : ''}`} fill="none" stroke="currentColor" strokeWidth="1.5">
            <path d="M13.5 8a5.5 5.5 0 1 1-1.6-3.9M13.5 2.5v3h-3" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
          {fetching ? 'Reading…' : 'Refresh'}
        </button>
      </header>

      {unknown !== undefined && unknown.state !== 'ok' && unknown.state !== 'empty' ? (
        <div data-refusal="cluster_unknown">
          <StateNotice state={unknown.state} reason={unknown.reason} title="No such cluster" />
        </div>
      ) : (
        <>
          {neither ? null : <SearchBox value={search} onChange={type} />}

          {showArgo ? (
            <section aria-labelledby="engine-argocd" className="flex flex-col gap-3">
              <h2 id="engine-argocd" className="text-sm font-medium">
                Argo CD
              </h2>
              {incomplete === undefined ? null : <Incomplete note={incomplete} />}
              <ListView query={list} what="the Argo CD Applications">
                {(items) => (
                  <ArgoBoard
                    cluster={cluster}
                    apps={items}
                    filter={filter?.engine === 'argo' ? filter : undefined}
                    project={project}
                    search={search}
                    hidden={filter?.engine === 'flux'}
                    onPick={pick}
                    onProject={pickProject}
                    onClear={clearFilters}
                    argoUrl={settings.argoUrl}
                  />
                )}
              </ListView>
              {refusal === undefined ? null : <NextStep refusal={refusal} subject={delivery.data?.subject} />}
              {delivery.data === undefined || refusal !== undefined ? null : report.isPending ? null : report.isError ? (
                <Failure error={report.error} onRetry={() => void report.refetch()} />
              ) : report.data.view === undefined ? (
                <p className="text-xs text-muted">{report.data.message}</p>
              ) : (
                <ProjectsAndSets cluster={cluster} view={report.data.view} active={project} onProject={pickProject} />
              )}
            </section>
          ) : null}

          {showFlux ? (
            <section aria-labelledby="engine-flux" className="flex flex-col gap-3">
              <h2 id="engine-flux" className="text-sm font-medium">
                Flux CD
              </h2>
              {fluxRows.length === 0 ? null : <FluxOverview flux={fluxRows} active={filter} onPick={pick} />}
              {flux.data?.note === undefined ? null : <p className="text-xs text-muted">{flux.data.note}</p>}

              <ListView query={fluxList} what="the Flux CD Kustomizations and HelmReleases">
                {(items) => (
                  <FluxObjects
                    rows={
                      filter?.engine === 'argo'
                        ? []
                        : items.filter((row) => fluxMatches(row, filter) && fluxSearched(row, search))
                    }
                    picked={filter !== undefined}
                    onClear={() => pick(undefined)}
                    uiUrl={settings.fluxUrl}
                  />
                )}
              </ListView>

              {flux.data?.refusal === undefined ? null : (
                <NextStep refusal={flux.data.refusal} subject={flux.data.subject} engine="flux" />
              )}
            </section>
          ) : null}

          {neither ? (
            <p data-neither="" className="rounded-lg border border-line bg-panel px-3 py-2.5 text-sm text-muted">
              No Argo CD or Flux on this cluster.
            </p>
          ) : null}
        </>
      )}
    </div>
  );
}

function fluxListOf(flux: Flux): Flux['list'] {
  return flux.list;
}

function listOf(delivery: Delivery): Delivery['list'] {
  return delivery.list;
}

/**
 * The list is not all of it. Drawn above the counts, so it is read before
 * they are.
 */
function Incomplete({ note }: { note: string }): ReactNode {
  return (
    <div role="status" data-complete="false" className="rounded-lg border border-line bg-panel-2 p-3 text-sm">
      <p className="font-medium text-warn">Incomplete list</p>
      <p className="mt-1 text-muted">{note}</p>
    </div>
  );
}

/**
 * What to do about each refusal. The state and the sentence above it say what
 * the cluster answered; this says which question to ask next, and it keeps
 * *not served* and *ambiguous* - which share a state - from reading as one.
 */
const NEXT: Readonly<Record<'kind_not_served' | 'kind_ambiguous', ReactNode>> = {
  kind_not_served: (
    <>
      The definition is there with every version set to <Code>served: false</Code>, which an interrupted Argo CD
      upgrade or removal leaves behind. <Code>kubectl get crd {APPLICATION_KIND} -o yaml</Code> shows it.
    </>
  ),
  kind_ambiguous: (
    <>
      This page does not choose between two definitions. <Code>kubectl api-resources | grep applications</Code>{' '}
      names both; nothing can be listed here until one of them is gone.
    </>
  ),
};

function NextStep({
  refusal,
  subject,
  engine = 'argocd',
}: {
  refusal: Refusal;
  subject: string | undefined;
  engine?: EngineKey;
}): ReactNode {
  // Argo CD's keeps the attribute it always had; Flux's is its own, so one
  // engine's next step is never read as the other's.
  const marker = engine === 'argocd' ? { 'data-refusal': refusal } : { 'data-flux-refusal': refusal };
  switch (refusal) {
    case 'kind_unknown':
      return (
        <div {...marker}>
          <Install engine={engine} />
        </div>
      );
    case 'list_forbidden':
      return (
        <div {...marker}>
          <GrantRead subject={subject} engine={engine} />
        </div>
      );
    case 'cluster_unknown':
      // The page's own notice says it, once, in place of both engines.
      return null;
    default:
      return (
        <p {...marker} className="text-xs text-muted">
          {NEXT[refusal]}
        </p>
      );
  }
}

function Code({ children }: { children: ReactNode }): ReactNode {
  return <code className="rounded-sm bg-panel-2 px-1 font-mono">{children}</code>;
}

// --- Argo CD: the filters and the tiles ---------------------------------------

/**
 * The sync words, in the order they are read: what needs a look first, then
 * what cannot be told, then what is fine. A word Argo CD adds later lands
 * between the last two, drawn in its own spelling.
 */
const SYNC_ORDER: readonly string[] = ['OutOfSync', 'Unknown', NOT_REPORTED];

/** A filter's word, in plain words; the tiles keep Argo's spelling. `Unknown`
 *  and `not reported` say what they are about, because both rows carry them. */
const SYNC_LABEL: Readonly<Record<string, string>> = {
  OutOfSync: 'Out of sync',
  Synced: 'Synced',
  Unknown: 'Sync unknown',
  [NOT_REPORTED]: 'Sync not reported',
};

const HEALTH_LABEL: Readonly<Record<string, string>> = {
  Unknown: 'Health unknown',
  [NOT_REPORTED]: 'Health not reported',
};

const syncWord = (app: Application) => (app.sync === '' ? NOT_REPORTED : app.sync);
const healthWord = (app: Application) => (app.health === '' ? NOT_REPORTED : app.health);

function syncRank(word: string): number {
  if (word === 'Synced') return SYNC_ORDER.length + 1;
  const at = SYNC_ORDER.indexOf(word);
  return at === -1 ? SYNC_ORDER.length : at;
}

function grouped<T>(rows: readonly T[], key: (row: T) => string): [string, T[]][] {
  const groups = new Map<string, T[]>();
  for (const row of rows) {
    const k = key(row);
    groups.set(k, [...(groups.get(k) ?? []), row]);
  }
  return [...groups.entries()];
}

/** The tile's tone: the worst of its health, its sync, a deletion and an error
 *  condition. */
export function appTone(app: Application): TileTone {
  const tones: TileTone[] = [tileToneOf(app.health, HEALTH_TONE), tileToneOf(app.sync, SYNC_TONE)];
  if (app.deleting) tones.push('warn');
  if (app.condition !== undefined) tones.push('bad');
  return worst(tones);
}

/** Worst first, then by sync word, then by name: out of sync and degraded
 *  before healthy. */
export function byWorst(a: Application, b: Application): number {
  return (
    severity(appTone(b)) - severity(appTone(a)) ||
    syncRank(syncWord(a)) - syncRank(syncWord(b)) ||
    a.name.localeCompare(b.name) ||
    a.namespace.localeCompare(b.namespace)
  );
}

/** How Argo CD names the cluster it runs in. */
const IN_CLUSTER_SERVER = 'https://kubernetes.default.svc';

/** The destination's cluster, or nothing when it is the one Argo CD runs in. */
function remoteOf(d: Destination): string | undefined {
  const name = d.name ?? '';
  const server = d.server ?? '';
  if (name === 'in-cluster') return undefined;
  if (name !== '') return name;
  if (server === '' || server === IN_CLUSTER_SERVER) return undefined;
  return server.replace(/^https?:\/\//, '').replace(/\/$/, '');
}

/**
 * The tile's one line: the project and where Argo CD applies it - the
 * namespace, behind its cluster when that is not the one Argo CD runs in.
 */
export function placeOf(app: Application, withProject = true): string | undefined {
  const d = app.destination ?? {};
  const remote = remoteOf(d);
  const ns = d.namespace === undefined || d.namespace === '' ? undefined : d.namespace;
  const where = remote !== undefined && ns !== undefined ? `${remote}/${ns}` : (remote ?? ns);
  const parts = [withProject ? app.project : undefined, where].filter((p): p is string => p !== undefined && p !== '');
  return parts.length === 0 ? undefined : parts.join(' · ');
}

function ArgoBoard({
  cluster,
  apps,
  filter,
  project,
  search,
  hidden,
  onPick,
  onProject,
  onClear,
  argoUrl,
}: {
  cluster: string;
  apps: readonly Application[];
  filter: StatusFilter | undefined;
  project: string | undefined;
  search: string;
  /** A Flux status is picked: no Application matches it. */
  hidden: boolean;
  onPick: (f: StatusFilter | undefined) => void;
  onProject: (p: string | undefined) => void;
  /** Lets go of the status and the project at once. */
  onClear: () => void;
  /** Argo CD's own UI, where one is set: each Application links to it. */
  argoUrl: string | undefined;
}): ReactNode {
  // Each row of filters counts what the others and the search leave, so a
  // count is what pressing it would show.
  const searched = apps.filter((row) => searchMatches(row, search));
  const inProject = searched.filter((row) => projectMatches(row, project));
  const shown = hidden ? [] : inProject.filter((row) => argoMatches(row, filter)).sort(byWorst);
  const attention = shown.filter(needsAttention);
  // One project, or one picked: its heading would only repeat the filter, and
  // under no heading a tile already drawn under Needs attention would be
  // drawn twice in a row. Under project headings each project stays whole.
  const headed = byProject(shown).length > 1;
  const sections = byProject(headed ? shown : shown.filter((app) => !needsAttention(app)));
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-2 rounded-lg border border-line bg-panel px-3 py-2.5">
        <ArgoFilters apps={inProject} active={filter} onPick={onPick} />
        <ProjectFilter
          apps={searched.filter((row) => argoMatches(row, filter))}
          active={project}
          onProject={onProject}
        />
      </div>
      {shown.length === 0 ? (
        <Filtered picked={filter !== undefined || hidden || project !== undefined} onClear={onClear} />
      ) : (
        <>
          {attention.length === 0 ? null : (
            <section aria-labelledby="argo-attention" data-argo-attention="" className="flex flex-col gap-2">
              <h3 id="argo-attention" className="text-sm font-medium">
                Needs attention <span className="font-normal text-muted tabular-nums">{attention.length}</span>
              </h3>
              <TileGrid label="Applications that need attention">
                <Windowed items={attention} more={(button) => <div className="col-span-full text-center">{button}</div>}>
                  {(page) =>
                    page.map((app) => (
                      <AppTile key={`${app.namespace}/${app.name}`} cluster={cluster} app={app} argoUrl={argoUrl} why />
                    ))
                  }
                </Windowed>
              </TileGrid>
            </section>
          )}
          {sections.map(([name, rows]) => (
            <section
              key={name}
              aria-label={headed ? `Project ${name}` : 'Applications'}
              data-argo-project={name}
              className="flex flex-col gap-2"
            >
              {headed ? <ProjectHeading name={name} rows={rows} onProject={onProject} /> : null}
              <TileGrid label={`Applications of project ${name}`}>
                <Windowed items={rows} more={(button) => <div className="col-span-full text-center">{button}</div>}>
                  {(page) =>
                    page.map((app) => (
                      <AppTile key={`${app.namespace}/${app.name}`} cluster={cluster} app={app} argoUrl={argoUrl} inProject />
                    ))
                  }
                </Windowed>
              </TileGrid>
            </section>
          ))}
        </>
      )}
    </div>
  );
}

/** A tile that is warn or bad: out of sync, degraded, missing, failing,
 *  being deleted. Progressing and Unknown health are not on their own. */
export function needsAttention(app: Application): boolean {
  const tone = appTone(app);
  return tone === 'bad' || tone === 'warn';
}

const NO_PROJECT = '(no project)';
const projectOf = (app: Application) => (app.project === undefined || app.project === '' ? NO_PROJECT : app.project);

/** The Applications by project, worst project first, then by name. */
export function byProject(rows: readonly Application[]): [string, Application[]][] {
  const worstOf = (apps: readonly Application[]) => Math.max(...apps.map((a) => severity(appTone(a))));
  return grouped(rows, projectOf).sort(
    ([a, ra], [b, rb]) => worstOf(rb) - worstOf(ra) || a.localeCompare(b),
  );
}

function ProjectHeading({
  name,
  rows,
  onProject,
}: {
  name: string;
  rows: readonly Application[];
  onProject: (p: string | undefined) => void;
}): ReactNode {
  const attention = rows.filter(needsAttention).length;
  return (
    <h3 className="flex items-baseline gap-2 border-b border-line pb-1 text-sm">
      {name === NO_PROJECT ? (
        <span className="font-medium text-muted">{name}</span>
      ) : (
        <button
          type="button"
          onClick={() => onProject(name)}
          title={`Show project ${name} only`}
          className={`font-medium hover:underline ${focusRing}`}
        >
          {name}
        </button>
      )}
      <span className="text-xs text-muted tabular-nums">
        {rows.length} {rows.length === 1 ? 'application' : 'applications'}
        {attention === 0 ? null : (
          <span className="text-warn">
            {' '}
            · {attention} {attention === 1 ? 'needs' : 'need'} attention
          </span>
        )}
      </span>
    </h3>
  );
}

/** The projects present, each a filter, most Applications first. Drawn only
 *  where there is more than one, or one is picked. */
function ProjectFilter({
  apps,
  active,
  onProject,
}: {
  apps: readonly Application[];
  active: string | undefined;
  onProject: (p: string | undefined) => void;
}): ReactNode {
  const counts = new Map<string, number>();
  for (const [name, rows] of grouped(apps, projectOf)) counts.set(name, rows.length);
  if (active !== undefined && !counts.has(active)) counts.set(active, 0);
  if (counts.size < 2 && active === undefined) return null;
  const rows = [...counts.entries()].sort(([a, n], [b, m]) => m - n || a.localeCompare(b));
  return (
    <div data-project-filters="">
      <ChipRow title="Project" label="Applications by project">
        {rows.map(([name, n]) => (
          <Chip
            key={name}
            data-project-filter={name}
            count={n}
            label={name}
            tone={n === 0 ? 'off' : 'info'}
            pressed={active === name}
            onClick={() => onProject(name)}
          />
        ))}
      </ChipRow>
    </div>
  );
}

/**
 * The summary, as filters: all of them, then sync - out of sync and synced
 * always, zero included, since "0 out of sync" is the reassurance the page
 * exists to give - then the health words present, worst first. A picked word
 * stays drawn at 0, so a link sent before it cleared can be un-picked where it
 * was picked.
 */
function ArgoFilters({
  apps,
  active,
  onPick,
}: {
  apps: readonly Application[];
  active: StatusFilter | undefined;
  onPick: (f: StatusFilter | undefined) => void;
}): ReactNode {
  const counted = (key: (app: Application) => string, field: string, always: readonly string[]) => {
    const counts = new Map<string, number>(always.map((w) => [w, 0]));
    for (const [word, rows] of grouped(apps, key)) counts.set(word, rows.length);
    if (active?.engine === 'argo' && active.field === field && !counts.has(active.value)) counts.set(active.value, 0);
    return [...counts.entries()];
  };
  const tile = (f: StatusFilter, n: number, label: string, tone: TileTone) => (
    <Chip
      key={filterValue(f)}
      data-filter={filterValue(f)}
      count={n}
      label={label}
      tone={n === 0 ? 'off' : tone}
      pressed={active !== undefined && filterValue(active) === filterValue(f)}
      onClick={() => onPick(f)}
    />
  );

  const sync = counted(syncWord, 'sync', ['OutOfSync', 'Synced']).sort(
    ([a], [b]) => syncRank(a) - syncRank(b) || a.localeCompare(b),
  );
  const health = counted(healthWord, 'health', []).sort(
    ([a], [b]) =>
      severity(tileToneOf(b, HEALTH_TONE)) - severity(tileToneOf(a, HEALTH_TONE)) || a.localeCompare(b),
  );
  const deleting = apps.filter((a) => a.deleting).length;
  const deletingPicked = active?.engine === 'argo' && active.field === 'deleting';

  return (
    <div className="flex flex-col gap-2" data-argo-filters="">
      <ChipRow title="Sync" label="Applications by sync">
        <Chip
          data-filter="all"
          count={apps.length}
          label="All"
          tone="info"
          pressed={active === undefined}
          onClick={() => onPick(undefined)}
        />
        {sync.map(([word, n]) =>
          tile({ engine: 'argo', field: 'sync', value: word }, n, SYNC_LABEL[word] ?? spoken(word), tileToneOf(word, SYNC_TONE)),
        )}
      </ChipRow>
      <ChipRow title="Health" label="Applications by health">
        {health.map(([word, n]) =>
          tile(
            { engine: 'argo', field: 'health', value: word },
            n,
            HEALTH_LABEL[word] ?? spoken(word),
            tileToneOf(word, HEALTH_TONE),
          ),
        )}
        {deleting === 0 && !deletingPicked
          ? null
          : tile({ engine: 'argo', field: 'deleting', value: 'Deleting' }, deleting, 'Deleting', 'warn')}
      </ChipRow>
    </div>
  );
}

const DOT: Readonly<Record<TileTone, string>> = {
  ok: 'bg-ok',
  warn: 'bg-warn',
  bad: 'bg-bad',
  info: 'bg-accent',
  off: 'bg-line',
};

/** One row of filter chips under its name. */
function ChipRow({ title, label, children }: { title: string; label: string; children: ReactNode }): ReactNode {
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
      <span className="w-14 shrink-0 text-2xs font-medium tracking-eyebrow text-muted uppercase">{title}</span>
      <ul aria-label={label} className="flex flex-wrap gap-1.5">
        {children}
      </ul>
    </div>
  );
}

/**
 * A filter, as a chip: a dot in the status's color, the word, the count. A
 * pressed chip is the accent's; a word at 0 keeps a gray dot.
 */
function Chip({
  label,
  count,
  tone,
  pressed,
  onClick,
  ...data
}: {
  label: string;
  count: number;
  tone: TileTone;
  pressed: boolean;
  onClick: () => void;
} & { readonly [key: `data-${string}`]: string | undefined }): ReactNode {
  return (
    <li {...data}>
      <button
        type="button"
        aria-pressed={pressed}
        onClick={onClick}
        className={`inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs transition-colors ${
          pressed ? 'border-accent bg-accent/15 text-fg' : 'border-line bg-panel-2 text-fg hover:border-muted'
        } ${focusRing}`}
      >
        <span aria-hidden="true" className={`h-2 w-2 rounded-full ${DOT[tone]}`} />
        {label} <span className={`tabular-nums ${count === 0 ? 'text-muted' : 'font-medium'}`}>{count}</span>
      </button>
    </li>
  );
}

/** The sync chip's hover: what is behind the word. */
function syncHint(app: Application): string {
  if (app.sync === '') return 'Argo CD has not reported a sync status yet.';
  const n = app.outOfSyncCount ?? 0;
  return n === 0 ? 'Sync status' : `${n} ${n === 1 ? 'resource' : 'resources'} out of sync`;
}

/**
 * Why a tile needs attention, in Argo CD's own sentence: the error condition,
 * else a sync that did not succeed, else the health's reason.
 */
export function whyOf(app: Application): string | undefined {
  if (app.condition !== undefined) {
    return app.condition.message === undefined ? app.condition.type : `${app.condition.type}: ${app.condition.message}`;
  }
  const last = app.lastSync;
  if (last !== undefined && (last.phase === 'Failed' || last.phase === 'Error') && last.message !== undefined) {
    return `Sync ${last.phase.toLowerCase()}: ${last.message}`;
  }
  if (app.healthMessage !== undefined && app.healthMessage !== '') return app.healthMessage;
  if (app.sync === 'OutOfSync') {
    const n = app.outOfSyncCount ?? 0;
    return n === 0 ? 'Out of sync with git.' : `${n} ${n === 1 ? 'resource differs' : 'resources differ'} from git.`;
  }
  return undefined;
}

const SYNC_PHASE_TONE: Readonly<Record<string, string>> = {
  Succeeded: 'text-muted',
  Running: 'text-accent',
  Terminating: 'text-warn',
  Failed: 'text-bad',
  Error: 'text-bad',
};

/** The last sync, in a few words: what it did and how long ago. */
function LastSyncLine({ app }: { app: Application }): ReactNode {
  const last = app.lastSync;
  if (last === undefined) return <span className="text-muted">no sync recorded</span>;
  const at = last.finishedAt ?? last.startedAt;
  const when = at === undefined ? '' : ` ${ago(at)}`;
  const words =
    last.phase === 'Succeeded'
      ? `synced${when}`
      : last.phase === 'Running'
        ? 'syncing…'
        : `sync ${last.phase.toLowerCase()}${when}`;
  return (
    <span
      data-last-sync={last.phase}
      className={SYNC_PHASE_TONE[last.phase] ?? 'text-muted'}
      title={at === undefined ? last.phase : `${last.phase}, ${formatInstant(at, { seconds: true })}`}
    >
      {words}
    </span>
  );
}

/**
 * One Application, one tile, as Argo CD draws it: the name, health and sync as
 * two chips, the project and destination in one line, the worst of it as the
 * bar - then the source, the revision, auto-sync and the last sync. Under
 * Needs attention (`why`) it also says why. The whole tile opens the
 * Application's page, where the drift is.
 */
function AppTile({
  cluster,
  app,
  why = false,
  inProject = false,
  argoUrl,
}: {
  cluster: string;
  app: Application;
  argoUrl?: string | undefined;
  why?: boolean;
  /** Drawn under its project's heading: the project is not repeated. */
  inProject?: boolean;
}): ReactNode {
  const place = placeOf(app, !inProject);
  const source = sourceLine(app);
  const reason = why ? whyOf(app) : undefined;
  return (
    <Tile
      data-app={`${app.namespace}/${app.name}`}
      title={app.name}
      tone={appTone(app)}
      to={applicationPath(cluster, app.namespace, app.name)}
      openLabel={`Open Application ${app.name}`}
      action={
        argoUrl === undefined ? undefined : (
          <ExternalLink href={argoLink(argoUrl, app.namespace, app.name)} label={`Open ${app.name} in Argo CD`}>
            Open in Argo CD
          </ExternalLink>
        )
      }
      badges={
        <>
          <Word
            word={app.health}
            grades={HEALTH}
            title={app.health === '' ? 'Argo CD has not reported a health yet.' : (app.healthMessage ?? 'Health')}
          />
          <Word word={app.sync} grades={SYNC} title={syncHint(app)} />
          {app.deleting ? (
            <span data-deleting="true" title="Being deleted: a finalizer holds it while it stays here.">
              <Pill tone="warn">Deleting</Pill>
            </span>
          ) : null}
          {app.condition === undefined ? null : (
            <span
              data-condition={app.condition.type}
              title={
                app.condition.message === undefined
                  ? app.condition.type
                  : `${app.condition.type}: ${app.condition.message}`
              }
            >
              <Pill tone="bad">Error</Pill>
            </span>
          )}
        </>
      }
      detail={place === undefined ? undefined : <span title={place}>{place}</span>}
    >
      <div className="flex min-w-0 flex-col gap-0.5 text-xs" data-app-facts="">
        {source === undefined ? null : (
          <span className="truncate text-muted" title={source.title} data-app-source="">
            {source.text}
          </span>
        )}
        <span className="flex min-w-0 flex-wrap items-baseline gap-x-2">
          {app.revision === undefined || app.revision === '' ? null : (
            <span className="font-mono text-muted" title={app.revision} data-app-revision="">
              {shortRevision(app.revision)}
            </span>
          )}
          <LastSyncLine app={app} />
          {app.automatedSync === true ? (
            <span className="text-muted" title="The sync policy is automated: Argo CD syncs it on its own.">
              auto-sync
            </span>
          ) : null}
        </span>
        {reason === undefined ? null : (
          <span className="line-clamp-2 text-fg [overflow-wrap:anywhere]" title={reason} data-app-why="">
            {reason}
          </span>
        )}
      </div>
    </Tile>
  );
}

export function applicationPath(cluster: string, namespace: string, name: string): string {
  return `/c/${encodeURIComponent(cluster)}/x/argocd/applications/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

// --- Flux CD -------------------------------------------------------------------

const FLUX_COLUMNS: readonly GridColumn<FluxObject>[] = [
  {
    key: 'kind',
    label: 'Kind',
    measure: (row) => row.kind,
    cell: (row) => <span className="text-muted">{row.kind}</span>,
  },
  {
    key: 'namespace',
    label: 'Namespace',
    measure: (row) => row.namespace,
    cell: (row) => <span className="text-muted">{row.namespace}</span>,
  },
  { key: 'name', label: 'Name', wrap: 'wrap', measure: (row) => row.name, cell: (row) => row.name },
  {
    key: 'ready',
    label: 'Ready',
    measure: (row) =>
      `${row.deleting ? 'Deleting  ' : ''}${row.suspended ? 'Suspended  ' : ''}${row.stalled ? 'Stalled  ' : ''}${row.ready || NOT_REPORTED}`,
    cell: (row) => (
      <span className="flex flex-wrap gap-1">
        {row.deleting ? (
          <span data-deleting="true" title="metadata.deletionTimestamp is set.">
            <Word word="Deleting" grades={{ Deleting: 'warn' }} />
          </span>
        ) : null}
        {row.suspended ? (
          <span data-suspended="true" title="spec.suspend is true: Flux will not reconcile this until it is resumed.">
            <Word word="Suspended" grades={{ Suspended: 'warn' }} />
          </span>
        ) : null}
        {row.stalled ? (
          <span data-stalled="true" title="Flux's Stalled condition: it stopped trying until something changes.">
            <Word word="Stalled" grades={{ Stalled: 'bad' }} />
          </span>
        ) : null}
        <Word word={row.ready} grades={READY} />
      </span>
    ),
  },
  {
    key: 'reason',
    label: 'Reason',
    wrap: 'wrap',
    measure: (row) => `${row.reason ?? ''}${row.reconciling ? ' · reconciling' : ''}`,
    cell: (row) => (
      <span title={row.message} className="text-muted">
        {row.reason}
        {row.reconciling ? ' · reconciling' : ''}
      </span>
    ),
  },
  {
    key: 'revision',
    label: 'Revision',
    wrap: 'wrap',
    measure: (row) => row.revision ?? '',
    cell: (row) => <span className="font-mono text-xs text-muted">{row.revision}</span>,
  },
];

/** A link that leaves the console, for a tool's own UI. */
function ExternalLink({ href, label, children }: { href: string; label: string; children: ReactNode }): ReactNode {
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      aria-label={label}
      title={label}
      data-external=""
      className={`text-xs text-accent hover:underline ${focusRing}`}
    >
      {children}
    </a>
  );
}

function FluxObjects({
  rows,
  picked,
  onClear,
  uiUrl,
}: {
  rows: readonly FluxObject[];
  picked: boolean;
  onClear: () => void;
  /** The Weave GitOps UI, where one is set. */
  uiUrl: string | undefined;
}): ReactNode {
  if (rows.length === 0) return <Filtered picked={picked} onClear={onClear} />;
  const columns: readonly GridColumn<FluxObject>[] =
    uiUrl === undefined
      ? FLUX_COLUMNS
      : [
          ...FLUX_COLUMNS,
          {
            key: 'ui',
            label: 'UI',
            measure: () => 'Open in Flux',
            cell: (row) => {
              const href = fluxLink(uiUrl, row.kind, row.namespace, row.name);
              return href === undefined ? null : (
                <ExternalLink href={href} label={`Open ${row.name} in Flux`}>
                  Open in Flux
                </ExternalLink>
              );
            },
          },
        ];
  return (
    <DataGrid
      columns={columns}
      rows={rows}
      rowKey={(row) => `${row.kind}/${row.namespace}/${row.name}`}
      label="Flux CD Kustomizations and HelmReleases on this cluster"
      empty={null}
    />
  );
}

/** Flux's statuses at a glance, each count a filter on the table below. */
function FluxOverview({
  flux,
  active,
  onPick,
}: {
  flux: readonly FluxObject[];
  active: StatusFilter | undefined;
  onPick: (f: StatusFilter | undefined) => void;
}): ReactNode {
  const suspended = flux.filter((o) => o.suspended).length;
  const stalled = flux.filter((o) => o.stalled).length;
  const order = Object.keys(READY);
  const ready = grouped(flux, (o) => (o.ready === '' ? NOT_REPORTED : o.ready))
    .map(([word, rows]) => [word, rows.length] as const)
    .sort(([a], [b]) => (order.indexOf(a) + 1 || 99) - (order.indexOf(b) + 1 || 99) || a.localeCompare(b));
  const chip = (f: StatusFilter, count: number, tone: Tone) => {
    const on = active !== undefined && filterValue(active) === filterValue(f);
    return (
      <button
        key={filterValue(f)}
        type="button"
        aria-pressed={on}
        data-filter={filterValue(f)}
        onClick={() => onPick(f)}
        className={btnToggle(on)}
      >
        <span className={TEXT[tone]}>{f.value}</span>
        <span className="tabular-nums text-fg">{count}</span>
      </button>
    );
  };
  return (
    <div data-overview="flux" role="group" aria-label="Flux CD by status" className="flex flex-wrap items-center gap-2">
      <span className="text-xs whitespace-nowrap text-muted">
        {flux.length} {flux.length === 1 ? 'object' : 'objects'}
      </span>
      {ready.map(([w, n]) => chip({ engine: 'flux', field: 'ready', value: w }, n, toneOf(w, READY)))}
      {suspended === 0 ? null : chip({ engine: 'flux', field: 'suspended', value: 'Suspended' }, suspended, 'warn')}
      {stalled === 0 ? null : chip({ engine: 'flux', field: 'stalled', value: 'Stalled' }, stalled, 'bad')}
      {active?.engine === 'flux' ? <ShowAll onClear={() => onPick(undefined)} /> : null}
    </div>
  );
}

// --- the filter -------------------------------------

export interface StatusFilter {
  engine: 'argo' | 'flux';
  /** `sync`, `health` or `deleting` for Argo CD; `ready`, `suspended` or
   *  `stalled` for Flux CD. */
  field: string;
  value: string;
}

export const FILTER_PARAMETER = 'status';
export const PROJECT_PARAMETER = 'project';
export const SEARCH_PARAMETER = 'q';

/** Every word of the search is found somewhere in the Application: its name,
 *  namespace, project, destination or source. Case does not matter. */
export function searchMatches(row: Application, search: string): boolean {
  const words = search.toLowerCase().split(/\s+/).filter((w) => w !== '');
  if (words.length === 0) return true;
  const d = row.destination ?? {};
  const src = row.source ?? {};
  const hay = [row.name, row.namespace, row.project, d.name, d.server, d.namespace, src.repoURL, src.path, src.chart, src.targetRevision]
    .filter((v): v is string => v !== undefined && v !== '')
    .join(' ')
    .toLowerCase();
  return words.every((w) => hay.includes(w));
}

export function projectMatches(row: Application, project: string | undefined): boolean {
  return project === undefined || projectOf(row) === project;
}

function fluxSearched(row: FluxObject, search: string): boolean {
  const words = search.toLowerCase().split(/\s+/).filter((w) => w !== '');
  const hay = `${row.kind} ${row.namespace} ${row.name} ${row.revision ?? ''}`.toLowerCase();
  return words.every((w) => hay.includes(w));
}

/**
 * The page's answer in one line under its title: how many Applications, how
 * many need attention, how many projects, and when it was read. Nothing while
 * the list is not read: a count is never drawn for a list that was not.
 */
function Summary({
  apps,
  complete,
  at,
}: {
  apps: readonly Application[] | undefined;
  /** The list is all of them: "all synced and healthy" is a claim about all. */
  complete: boolean;
  at: number;
}): ReactNode {
  if (apps === undefined) return null;
  const attention = apps.filter(needsAttention).length;
  // Green only for what it says: every one Synced and Healthy, none being
  // deleted, over the whole list. Progressing or Unknown needs no attention
  // and is not that.
  const allWell =
    complete && apps.length > 0 && apps.every((a) => a.sync === 'Synced' && a.health === 'Healthy' && !a.deleting);
  const projects = new Set(apps.map(projectOf)).size;
  return (
    <p className="flex flex-wrap items-baseline gap-x-2 text-xs text-muted" data-argo-summary="">
      <span>
        Argo CD · <span className="text-fg tabular-nums">{apps.length}</span> {apps.length === 1 ? 'application' : 'applications'}
      </span>
      {allWell ? (
        <span className="text-ok" data-all-well="">
          · all synced and healthy
        </span>
      ) : attention === 0 ? (
        <span>· none needs attention</span>
      ) : (
        <span className="text-warn">
          · <span className="tabular-nums">{attention}</span> {attention === 1 ? 'needs' : 'need'} attention
        </span>
      )}
      <span>
        · <span className="tabular-nums">{projects}</span> {projects === 1 ? 'project' : 'projects'}
      </span>
      {at === 0 ? null : <span>· read at {formatClock(new Date(at))}</span>}
    </p>
  );
}

/** The search, as Argo CD's own UI puts it: one box over every Application. */
function SearchBox({ value, onChange }: { value: string; onChange: (next: string) => void }): ReactNode {
  return (
    <div className="relative w-full max-w-xl">
      <svg
        aria-hidden="true"
        viewBox="0 0 16 16"
        className="pointer-events-none absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2 text-muted"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
      >
        <circle cx="7" cy="7" r="4.5" />
        <path d="m10.5 10.5 3 3" strokeLinecap="round" />
      </svg>
      <input
        type="search"
        value={value}
        onChange={(event) => onChange(event.currentTarget.value)}
        placeholder="Search applications by name, project, namespace, repository…"
        aria-label="Search applications"
        data-argo-search=""
        className={`w-full rounded-lg border border-line bg-panel py-2 pr-3 pl-9 text-sm placeholder:text-muted ${focusRing}`}
      />
    </div>
  );
}

export function filterValue(f: StatusFilter): string {
  return `${f.engine}.${f.field}.${f.value}`;
}

export function parseFilter(raw: string | null): StatusFilter | undefined {
  if (raw === null) return undefined;
  const [engine, field, ...rest] = raw.split('.');
  if ((engine !== 'argo' && engine !== 'flux') || field === undefined || rest.length === 0) return undefined;
  return { engine, field, value: rest.join('.') };
}

const word = (w: string) => (w === '' ? NOT_REPORTED : w);

export function argoMatches(row: Application, f: StatusFilter | undefined): boolean {
  if (f?.engine !== 'argo') return true;
  if (f.field === 'sync') return word(row.sync) === f.value;
  if (f.field === 'health') return word(row.health) === f.value;
  if (f.field === 'deleting') return row.deleting;
  return true;
}

export function fluxMatches(row: FluxObject, f: StatusFilter | undefined): boolean {
  if (f?.engine !== 'flux') return true;
  if (f.field === 'ready') return word(row.ready) === f.value;
  if (f.field === 'suspended') return row.suspended;
  if (f.field === 'stalled') return row.stalled;
  return true;
}

/** Clears whatever status is picked: the way back from a filter, whichever
 *  chip set it and whether or not that chip is still drawn. */
function ShowAll({ onClear }: { onClear: () => void }): ReactNode {
  return (
    <button
      type="button"
      data-show-all=""
      onClick={onClear}
      className={`self-center ${btnSecondary}`}
    >
      Show all
    </button>
  );
}

/** A section the filters or the search narrowed to nothing, with the way
 *  back from the filters. The search box is its own way back. */
function Filtered({ picked, onClear }: { picked: boolean; onClear: () => void }): ReactNode {
  return (
    <p className="flex items-center gap-2 text-xs text-muted" data-filtered={picked ? 'filters' : 'search'}>
      {picked ? 'None here matches the filters picked.' : 'Nothing here matches the search.'}
      {picked ? <ShowAll onClear={onClear} /> : null}
    </p>
  );
}
