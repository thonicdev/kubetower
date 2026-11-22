import { useQuery } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { Link, useParams, useSearchParams } from 'react-router';
import {
  ApiError,
  DataGrid,
  Failure,
  Pending,
  Pill,
  focusRing,
  formatInstant,
  useDeclarePageFacts,
  Windowed,
  type GridColumn,
  btnSecondary,
} from '@kubetower/ui';

import { RefreshApplication } from './RefreshApplication';
import {
  applicationQuery,
  consoleObjectPath,
  Refused,
  type AppEvent,
  type ApplicationDetail,
  type Deployment,
  type Operation,
  type Resource,
} from './application';
import { ResourceWhy } from './ArgoTab';
import { unknownClusterOf } from './data';
import { PAGE_PATH } from './extension';
import { GrantRead } from './NextSteps';
import { NoSuchCluster } from './NoSuchCluster';
import { ago, sourceLine } from './summary';
import { trackedQuery, type Tracked } from './tracked';
import { HEALTH, SYNC, Word } from './words';

/**
 * One Application, at `/c/:cluster/x/argocd/applications/:namespace/:name`,
 * read-only: its sync and health, the last sync
 * operation, the resources it manages, and its deployment history.
 *
 * **Which resource differs from Git is Argo CD's answer, and the page says it
 * in Argo's word, `OutOfSync`.** The line diff and the rendered manifests Argo
 * compared are in Argo CD's repo-server, behind its own sign-in; this page does
 * not read them and says so, rather than drawing a diff it does not have. Each
 * resource opens in the console's own page - its manifest, and for a pod its
 * logs - which reads it for the person signed in.
 *
 * No button here writes: an extra has no write door of its own, so a sync or
 * a refresh is left to Argo CD.
 *
 * ## Resources first
 *
 * A reader lands on the resources and the events; the sync history waits
 * behind a History tab; a resource's click keeps to its page; an OutOfSync one
 * says quickly why. So the page is a one-line head (the two words, then project,
 * destination, source, auto-sync and the last sync in one row, an error
 * condition as one line), then three views kept in the address (`?view=`):
 * **Resources** - what is out of sync first, each opening the difference under
 * it (`ResourceWhy`), the table, and the Events beside them - **History** and
 * **Last sync**.
 */
export function ApplicationPage(): ReactNode {
  const { cluster = '', namespace = '', name = '' } = useParams<{ cluster: string; namespace: string; name: string }>();
  const query = useQuery(applicationQuery(cluster, namespace, name));
  // A cluster the console does not know is an answer about the address, drawn
  // as the Delivery Center draws it: one notice, the sentence once, no retry.
  const unknown = query.isError ? unknownClusterOf(query.error, cluster) : undefined;
  useDeclarePageFacts({ title: `Delivery Center / ${namespace} / ${name}` });

  return (
    <div className="flex flex-col gap-4 p-4">
      <header className="flex flex-wrap items-baseline gap-2">
        <Link
          to={`/c/${encodeURIComponent(cluster)}/${PAGE_PATH}`}
          className={`text-xs text-muted hover:text-fg ${focusRing}`}
        >
          Delivery Center
        </Link>
        <span aria-hidden="true" className="text-xs text-muted">
          /
        </span>
        <h1 className="text-xl font-medium">{name}</h1>
        <span className="text-xs text-muted">{namespace}</span>
        <span className="flex-1" />
        <RefreshApplication cluster={cluster} namespace={namespace} name={name} label="Refresh Application" />
        <button
          type="button"
          onClick={() => void query.refetch()}
          disabled={query.isFetching}
          className={btnSecondary}
        >
          {query.isFetching ? 'Reading…' : 'Refresh'}
        </button>
      </header>

      {query.isPending ? (
        <Pending what="this Application" />
      ) : query.isError ? (
        unknown !== undefined ? (
          <NoSuchCluster unknown={unknown} />
        ) : query.error instanceof Refused ? (
          <div data-refusal="list_forbidden" className="flex flex-col gap-3">
            <p className="text-sm text-muted">{query.error.message}</p>
            <GrantRead subject={query.error.subject} />
          </div>
        ) : (
          <Failure error={query.error} onRetry={() => void query.refetch()} />
        )
      ) : (
        <Detail cluster={cluster} app={query.data} />
      )}
    </div>
  );
}

type View = 'resources' | 'history' | 'sync';
const VIEWS: readonly { key: View; label: string }[] = [
  { key: 'resources', label: 'Resources' },
  { key: 'history', label: 'History' },
  { key: 'sync', label: 'Last sync' },
];
const VIEW_PARAMETER = 'view';

function Detail({ cluster, app }: { cluster: string; app: ApplicationDetail }): ReactNode {
  const [params, setParams] = useSearchParams();
  const raw = params.get(VIEW_PARAMETER);
  const view: View = raw === 'history' || raw === 'sync' ? raw : 'resources';
  const show = (next: View) =>
    setParams(
      (p) => {
        const out = new URLSearchParams(p);
        if (next === 'resources') out.delete(VIEW_PARAMETER);
        else out.set(VIEW_PARAMETER, next);
        return out;
      },
      { replace: true },
    );
  // Argo CD 3 keeps each resource's health out of the Application unless
  // `controller.resource.health.persist` is set (measured on v3.5.3): a
  // column of "not reported" would read as Argo CD having no opinion, when it has one and keeps it elsewhere.
  const healthReported = app.resources.some((r) => r.health !== undefined && r.health !== '');
  const outOfSync = app.resources.filter((r) => r.status === 'OutOfSync');
  // What differs first, then the rest in Argo CD's order.
  const rows = [...outOfSync, ...app.resources.filter((r) => r.status !== 'OutOfSync')];
  return (
    <>
      <Head cluster={cluster} app={app} />

      <div role="tablist" aria-label="Application views" className="flex gap-1 border-b border-line">
        {VIEWS.map((v) => (
          <button
            key={v.key}
            type="button"
            role="tab"
            aria-selected={view === v.key}
            data-view={v.key}
            id={`argocd-tab-${v.key}`}
            aria-controls={`argocd-panel-${v.key}`}
            onClick={() => show(v.key)}
            className={`-mb-px border-b-2 px-3 py-1.5 text-sm ${
              view === v.key ? 'border-accent text-fg' : 'border-transparent text-muted hover:text-fg'
            } ${focusRing}`}
          >
            {v.label}
            {v.key === 'resources' ? <span className="ml-1.5 text-xs text-muted tabular-nums">{app.resources.length}</span> : null}
            {v.key === 'history' ? <span className="ml-1.5 text-xs text-muted tabular-nums">{app.history.length}</span> : null}
          </button>
        ))}
      </div>

      {view === 'history' ? (
        <section id="argocd-panel-history" aria-labelledby="argocd-tab-history" role="tabpanel" className="flex flex-col gap-2">
          {app.history.length === 0 ? (
            <p className="text-sm text-muted">Argo CD has recorded no deployment of this Application.</p>
          ) : (
            <DataGrid
              columns={HISTORY_COLUMNS}
              rows={app.history}
              rowKey={(d) => String(d.id)}
              label="Deployments of this Application, newest first"
              empty={null}
            />
          )}
        </section>
      ) : view === 'sync' ? (
        <section id="argocd-panel-sync" aria-labelledby="argocd-tab-sync" role="tabpanel" className="flex flex-col gap-2">
          {app.operation === undefined ? (
            <p className="text-sm text-muted">Argo CD records no sync of this Application.</p>
          ) : (
            <LastOperation op={app.operation} />
          )}
        </section>
      ) : (
        <div id="argocd-panel-resources" role="tabpanel" aria-labelledby="argocd-tab-resources" className="flex flex-col gap-4 xl:flex-row xl:items-start">
          <section aria-labelledby="argocd-resources" className="flex min-w-0 flex-1 flex-col gap-3">
            <h2 id="argocd-resources" className="sr-only">
              Resources
            </h2>
            {outOfSync.length === 0 ? null : <OutOfSync cluster={cluster} resources={outOfSync} />}
            {app.resourcesNote === undefined ? null : <p className="text-xs text-warn">{app.resourcesNote}</p>}
            {healthReported || app.resources.length === 0 ? null : (
              <p data-health-note="" className="text-xs text-muted">
                Argo CD does not keep each resource&apos;s health in the Application (Argo CD 3 default); the health above
                is its verdict over all of them.
              </p>
            )}
            {app.resources.length === 0 ? (
              <p className="text-sm text-muted">Argo CD reports no resource for this Application.</p>
            ) : (
              <DataGrid
                columns={resourceColumns(cluster, healthReported)}
                rows={rows}
                rowKey={(r) => `${r.group ?? ''}/${r.kind}/${r.namespace ?? ''}/${r.name}`}
                label="Resources this Application manages"
                empty={null}
              />
            )}
          </section>
          <Events events={app.events ?? []} note={app.eventsNote} />
        </div>
      )}
    </>
  );
}

const PHASE_TONE: Readonly<Record<string, string>> = {
  Succeeded: 'text-muted',
  Running: 'text-accent',
  Failed: 'text-bad',
  Error: 'text-bad',
};

/**
 * The head: the two words, then everything else in one row - project,
 * destination, source and revision, auto-sync, the last sync - and an error
 * condition as one line under it.
 */
function Head({ cluster, app }: { cluster: string; app: ApplicationDetail }): ReactNode {
  const source = sourceLine({
    namespace: app.namespace,
    name: app.name,
    sync: app.sync,
    health: app.health,
    deleting: app.deleting,
    source: app.source,
  });
  const op = app.operation;
  const at = op?.finishedAt ?? op?.startedAt;
  const destination = [app.destination.name ?? app.destination.server, app.destination.namespace]
    .filter((part) => part !== undefined && part !== '')
    .join(' / ');
  const sep = <span aria-hidden="true">·</span>;
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-line bg-panel px-4 py-3" data-app-head="">
      <div className="flex flex-wrap items-center gap-2">
        <span title={app.healthMessage ?? 'Health'}>
          <Word word={app.health} grades={HEALTH} />
        </span>
        <Word word={app.sync} grades={SYNC} />
        {app.deleting ? <Pill tone="warn">Deleting</Pill> : null}
        {app.healthMessage === undefined ? null : <span className="text-xs text-muted">{app.healthMessage}</span>}
      </div>
      <p className="flex flex-wrap items-baseline gap-x-2 gap-y-1 text-xs text-muted" data-app-facts="">
        {app.project === undefined ? null : (
          <>
            <span>
              project{' '}
              {app.projectNamespace === undefined ? (
                <span className="text-fg">{app.project}</span>
              ) : (
                <Link
                  to={`/c/${encodeURIComponent(cluster)}/appprojects.argoproj.io/${encodeURIComponent(app.projectNamespace)}/${encodeURIComponent(app.project)}`}
                  className={`text-accent hover:underline ${focusRing}`}
                >
                  {app.project}
                </Link>
              )}
            </span>
            {sep}
          </>
        )}
        {destination === '' ? null : (
          <>
            <span className="font-mono">{destination}</span>
            {sep}
          </>
        )}
        {source === undefined ? null : (
          <>
            {/* The repository opens where it lives, when it is an https address:
                an ssh or oci source has no page to open. */}
            {app.source.repoURL?.startsWith('https://') === true ? (
              <a
                href={app.source.repoURL.replace(/\.git$/, '')}
                target="_blank"
                rel="noopener noreferrer"
                title={source.title}
                className={`text-accent hover:underline ${focusRing}`}
              >
                {source.text}
              </a>
            ) : (
              <span title={source.title}>{source.text}</span>
            )}
            {sep}
          </>
        )}
        {app.syncRevision === undefined ? null : (
          <>
            <Revision value={app.syncRevision} />
            {sep}
          </>
        )}
        {app.automatedSync ? (
          <>
            <span>auto-sync</span>
            {sep}
          </>
        ) : null}
        {op === undefined ? (
          <span>no sync recorded</span>
        ) : (
          <span data-last-sync={op.phase} className={PHASE_TONE[op.phase] ?? 'text-warn'} title={at === undefined ? op.phase : moment(at)}>
            {op.phase === 'Succeeded' ? 'synced' : op.phase === 'Running' ? 'syncing…' : `sync ${op.phase.toLowerCase()}`}
            {at === undefined || op.phase === 'Running' ? '' : ` ${ago(at)}`}
            {op.automated ? ' by the sync policy' : op.initiatedBy === undefined ? '' : ` by ${op.initiatedBy}`}
          </span>
        )}
      </p>
      {app.conditions.length === 0 ? null : (
        <ul aria-label="Conditions" className="flex flex-col gap-0.5">
          {app.conditions.map((c, i) => (
            <li
              key={`${c.type}-${i}`}
              data-condition={c.type}
              className={`line-clamp-2 text-xs [overflow-wrap:anywhere] ${c.error ? 'text-bad' : 'text-muted'}`}
              title={c.message}
            >
              <strong className="font-medium">{c.type}</strong> {c.message}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** How many OutOfSync rows open at once: their reasons are read with the
 *  page, the rest when opened. */
const OPEN_AT_ONCE = 5;

/**
 * Why one OutOfSync resource differs, in a few words: from the Application
 * alone where it says (pruning, a health of Missing), else from the
 * resource's own read once it is in - missing from the cluster, or how many
 * fields differ.
 */
export function reasonOf(r: Resource, read: { missing: boolean; fields: number | undefined } | undefined): string {
  if (r.requiresPruning) return 'not in git - would be pruned';
  if (r.health === 'Missing' || read?.missing === true) return 'missing from the cluster';
  if (read?.fields !== undefined && read.fields > 0) {
    return `${read.fields} ${read.fields === 1 ? 'field differs' : 'fields differ'}`;
  }
  return 'differs from git';
}

/**
 * The resources that differ, first, each a fold: its reason in a word, then -
 * read when opened - the difference itself (`ResourceWhy`, the Argo CD tab's
 * own read and table). The name still opens the resource's page.
 */
function OutOfSync({ cluster, resources }: { cluster: string; resources: readonly Resource[] }): ReactNode {
  return (
    <section aria-labelledby="argocd-outofsync" data-outofsync="" className="flex flex-col gap-1.5">
      <h3 id="argocd-outofsync" className="text-sm font-medium">
        Out of sync <span className="font-normal text-muted tabular-nums">{resources.length}</span>
      </h3>
      <ul className="flex flex-col gap-1.5">
        <Windowed items={resources} more={(button) => <li className="text-center">{button}</li>}>
          {(page) =>
            page.map((r, i) => (
              <OutOfSyncRow
                key={`${r.group ?? ''}/${r.kind}/${r.namespace ?? ''}/${r.name}`}
                cluster={cluster}
                resource={r}
                opened={i < OPEN_AT_ONCE}
              />
            ))
          }
        </Windowed>
      </ul>
    </section>
  );
}

function OutOfSyncRow({
  cluster,
  resource: r,
  opened,
}: {
  cluster: string;
  resource: Resource;
  opened: boolean;
}): ReactNode {
  const [open, setOpen] = useState(opened);
  const to = consoleObjectPath(cluster, r);
  const text = `${r.namespace === undefined || r.namespace === '' ? '' : `${r.namespace}/`}${r.name}`;
  // The same read as ResourceWhy's, shared through the query cache: the row's
  // word follows what the read found, once it is open.
  const readable = to !== undefined && r.path !== undefined;
  const query = useQuery({ ...trackedQuery(cluster, r.path ?? '', r.namespace, r.name), enabled: open && readable });
  const read =
    query.data !== undefined
      ? { missing: false, fields: fieldsOf(query.data) }
      : query.error instanceof ApiError && (query.error.body as { code?: unknown } | undefined)?.code === 'object_not_found'
        ? { missing: true, fields: undefined }
        : undefined;
  const reason = reasonOf(r, read);
  return (
    <li
      data-outofsync-row={`${r.kind}/${text}`}
      className="rounded-md border border-l-3 border-line border-l-warn bg-panel"
    >
      <div className="flex flex-wrap items-center gap-2 px-3 py-1.5 text-sm">
        <button
          type="button"
          aria-expanded={open}
          aria-label={`${open ? 'Hide' : 'Show'} why ${r.kind} ${text} is out of sync`}
          onClick={() => setOpen(!open)}
          className={`w-4 text-muted hover:text-fg ${focusRing}`}
        >
          {open ? '▾' : '▸'}
        </button>
        <span className="text-xs text-muted">{r.kind}</span>
        {to === undefined ? (
          <span>{text}</span>
        ) : (
          <Link to={`${to}?tab=x.argocd.sync`} className={`text-accent hover:underline ${focusRing}`}>
            {text}
          </Link>
        )}
        <span data-reason={reason} className="text-xs text-warn">
          {reason}
        </span>
      </div>
      {open ? (
        <div className="border-t border-line px-3 py-2">
          {!readable || r.path === undefined ? (
            <p className="text-xs text-muted">This cluster does not serve its kind, so its difference cannot be read here.</p>
          ) : (
            <ResourceWhy cluster={cluster} kind={r.path} namespace={r.namespace} name={r.name} />
          )}
        </div>
      ) : null}
    </li>
  );
}

function LastOperation({ op }: { op: Operation }): ReactNode {
  const tone = op.phase === 'Succeeded' ? 'text-ok' : op.phase === 'Failed' || op.phase === 'Error' ? 'text-bad' : 'text-warn';
  return (
    <section aria-labelledby="argocd-operation" data-operation={op.phase} className="flex flex-col gap-2">
      <h2 id="argocd-operation" className="text-sm font-medium">
        Last sync
      </h2>
      <p className="flex flex-wrap items-baseline gap-2 text-sm">
        <span className={`font-semibold ${tone}`}>{op.phase}</span>
        {op.revision === undefined ? null : <Revision value={op.revision} />}
        <span className="text-xs text-muted">
          {op.automated ? 'by the sync policy' : op.initiatedBy === undefined ? '' : `by ${op.initiatedBy}`}
          {op.startedAt === undefined ? '' : `, started ${moment(op.startedAt)}`}
          {op.finishedAt === undefined ? '' : `, finished ${moment(op.finishedAt)}`}
        </span>
      </p>
      {op.message === undefined ? null : <p className="text-sm text-muted">{op.message}</p>}
      {op.results.length === 0 ? null : (
        <ul className="flex flex-col gap-1 text-xs">
          {op.results.map((r, i) => (
            <li key={`${r.kind}/${r.namespace ?? ''}/${r.name}/${i}`}>
              <span className="font-medium">{r.status ?? r.hookPhase}</span>{' '}
              <span className="text-muted">
                {r.kind} {r.namespace === undefined ? '' : `${r.namespace}/`}
                {r.name}
              </span>
              {r.message === undefined ? null : <span className="text-muted"> - {r.message}</span>}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function resourceColumns(cluster: string, withHealth: boolean): readonly GridColumn<Resource>[] {
  const columns: GridColumn<Resource>[] = [
    { key: 'kind', label: 'Kind', measure: (r) => r.kind, cell: (r) => <span className="text-muted">{r.kind}</span> },
    {
      key: 'name',
      label: 'Name',
      wrap: 'wrap',
      measure: (r) => `${r.namespace ?? ''}/${r.name}`,
      cell: (r) => {
        const to = consoleObjectPath(cluster, r);
        const text = `${r.namespace === undefined || r.namespace === '' ? '' : `${r.namespace}/`}${r.name}`;
        // An OutOfSync resource opens on its Argo CD tab, where the drift is.
        return to === undefined ? (
          text
        ) : (
          <Link to={r.status === 'OutOfSync' ? `${to}?tab=x.argocd.sync` : to} className={`text-accent hover:underline ${focusRing}`}>
            {text}
          </Link>
        );
      },
    },
    {
      key: 'status',
      label: 'Sync',
      measure: (r) => `${r.status ?? ''}${r.requiresPruning ? ' prune' : ''}`,
      cell: (r) => (
        <>
          <Word word={r.status ?? ''} grades={SYNC} />
          {r.requiresPruning ? (
            <span className="ml-2 text-xs text-warn" title="Argo CD would delete this: it is no longer in Git.">
              prune
            </span>
          ) : null}
        </>
      ),
    },
    {
      key: 'health',
      label: 'Health',
      measure: (r) => r.health ?? '',
      cell: (r) => (
        <span title={r.healthMessage}>
          <Word word={r.health ?? ''} grades={HEALTH} />
        </span>
      ),
    },
    {
      key: 'manifest',
      label: 'Manifest',
      measure: () => 'Manifest',
      cell: (r) => {
        const to = consoleObjectPath(cluster, r);
        return to === undefined ? null : (
          <Link to={`${to}?tab=manifest`} className={`text-xs text-accent hover:underline ${focusRing}`}>
            Manifest
          </Link>
        );
      },
    },
  ];
  return withHealth ? columns : columns.filter((c) => c.key !== 'health');
}

const HISTORY_COLUMNS: readonly GridColumn<Deployment>[] = [
  { key: 'id', label: '#', measure: (d) => String(d.id), cell: (d) => <span className="text-muted">{d.id}</span> },
  {
    key: 'revision',
    label: 'Revision',
    measure: (d) => d.revision ?? '',
    cell: (d) => <span className="font-mono text-xs">{short(d.revision)}</span>,
  },
  {
    key: 'deployedAt',
    label: 'Deployed',
    measure: (d) => d.deployedAt ?? '',
    cell: (d) => (d.deployedAt === undefined ? '' : moment(d.deployedAt)),
  },
  {
    key: 'source',
    label: 'Source',
    wrap: 'wrap',
    measure: (d) => d.source.path ?? d.source.chart ?? '',
    cell: (d) => <span className="text-muted">{d.source.path ?? d.source.chart}</span>,
  },
];

/** The fields the tab would draw: Argo CD's own diff when it answered, else
 *  the drift since the last apply; undefined when neither compared. */
function fieldsOf(t: Tracked): number | undefined {
  if (t.argoDiff?.compared === true && t.argoDiff.fields.length > 0) return t.argoDiff.fields.length;
  return t.drift.compared ? t.drift.fields.length : undefined;
}

// --- small pieces -------------------------------------------------------------

function Revision({ value }: { value: string }): ReactNode {
  return (
    <span title={value} className="font-mono text-xs text-muted">
      {short(value)}
    </span>
  );
}

/** A Git revision as people read one: the first seven characters of a SHA,
 *  anything else (a tag, a chart version) whole. */
function short(revision: string | undefined): string {
  if (revision === undefined) return '';
  return /^[0-9a-f]{40}$/.test(revision) ? revision.slice(0, 7) : revision;
}

function moment(iso: string): string {
  const at = new Date(iso);
  return Number.isNaN(at.getTime()) ? iso : formatInstant(at, { seconds: true });
}

/**
 * The Application's Kubernetes Events, newest first: Argo CD records a
 * sync started, a health that changed, a resource it updated. Kubernetes keeps
 * them about an hour, so an empty list says so rather than looking like a
 * quiet Application.
 */
function Events({ events, note }: { events: readonly AppEvent[]; note: string | undefined }): ReactNode {
  return (
    <section aria-labelledby="argocd-events" className="flex min-w-0 flex-col gap-2 rounded-lg border border-line bg-panel p-3 xl:w-88 xl:shrink-0">
      <h2 id="argocd-events" className="text-sm font-medium">
        Events
      </h2>
      {note === undefined ? null : <p className="text-xs text-warn">{note}</p>}
      {events.length === 0 ? (
        note === undefined ? (
          <p className="text-xs text-muted">No Event about this Application: Kubernetes keeps them about an hour.</p>
        ) : null
      ) : (
        <ul className="flex flex-col gap-2 text-xs" aria-label="Events of this Application">
          {events.map((e, i) => (
            <li key={`${e.reason ?? ''}-${e.at ?? ''}-${i}`} data-event={e.reason} className="flex flex-col gap-0.5">
              <span className="flex items-baseline gap-2">
                <span className={`font-medium ${e.type === 'Warning' ? 'text-bad' : 'text-fg'}`}>{e.reason}</span>
                {e.count !== undefined && e.count > 1 ? <span className="text-muted">×{e.count}</span> : null}
                <span className="flex-1" />
                <span className="text-muted tabular-nums" title={e.at === undefined ? undefined : formatInstant(e.at, { seconds: true })}>
                  {e.at === undefined ? '' : ago(e.at)}
                </span>
              </span>
              <span className="text-muted [overflow-wrap:anywhere]">{e.message}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
