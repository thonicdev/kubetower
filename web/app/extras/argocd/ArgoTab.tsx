import { useQuery } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { Link } from 'react-router';
import { ApiError, Failure, Pending, Pill, focusRing, type ExtensionTabProps } from '@kubetower/ui';

import { unknownClusterOf, type Problem } from './data';
import { PAGE_PATH } from './extension';
import { ago } from './summary';
import { NoSuchCluster } from './NoSuchCluster';
import { RefreshApplication } from './RefreshApplication';
import { trackedQuery, type DriftField, type Tracked } from './tracked';

/**
 * Argo CD in an object's panel, on any object
 * Argo CD marks as its own (the core's `kinds: ['*']` and `when`).
 *
 * **One line, then the drift**: Synced, Not synced, or Unknown when Argo CD's word for this resource could
 * not be read; and when something differs, the difference itself, at once.
 *
 * One diff, never two. Argo CD's own (git rendered against live, through
 * argocd-server and a read-only token) when it
 * answered; otherwise the object against the `last-applied-configuration`
 * Argo CD wrote, which shows what changed since the last sync and not a commit
 * still in git - so its heading says which one it is.
 *
 * A refusal and a failure stay two drawings, because an empty answer and a
 * failed one are different answers: an answer
 * about the cluster - not allowed, no such object, no such kind - is a muted
 * sentence with no retry; a cluster the console does not know is the Delivery
 * Center's one notice (`NoSuchCluster`), also with no retry; a request that
 * failed is the console's `Failure`.
 */
export default function ArgoTab({ cluster, kind, namespace, name }: ExtensionTabProps): ReactNode {
  const query = useQuery(trackedQuery(cluster, kind, namespace, name));
  if (query.isPending) return <Pending what="what Argo CD says about this object" />;
  if (query.isError) {
    const unknown = unknownClusterOf(query.error, cluster);
    if (unknown !== undefined) return <NoSuchCluster unknown={unknown} />;
    const refused = answerOf(query.error);
    if (refused !== undefined) {
      return (
        <p data-argo-tab="refused" data-code={refused.code} className="text-sm text-muted">
          {refused.message}
        </p>
      );
    }
    return <Failure error={query.error} onRetry={() => void query.refetch()} />;
  }
  return <Answer cluster={cluster} tracked={query.data} />;
}

/**
 * Why one resource of an Application is OutOfSync, under its row on the
 * Application's page, so a reader sees quickly why: missing from the cluster,
 * a field not applied, a readable diff. The same read and
 * the same diff as this tab, without the verdict line the row already says;
 * read only once the row is opened.
 */
export function ResourceWhy({
  cluster,
  kind,
  namespace,
  name,
}: {
  cluster: string;
  /** The kind's segment in the console's address. */
  kind: string;
  namespace: string | undefined;
  name: string;
}): ReactNode {
  const query = useQuery(trackedQuery(cluster, kind, namespace, name));
  if (query.isPending) return <Pending what="the difference" />;
  if (query.isError) {
    const unknown = unknownClusterOf(query.error, cluster);
    if (unknown !== undefined) return <NoSuchCluster unknown={unknown} />;
    const refused = answerOf(query.error);
    if (refused?.code === 'object_not_found') {
      return (
        <p
          data-why="missing"
          title="In git and not in the cluster: Argo CD has not created it yet, or somebody deleted it. The next sync creates it."
          className="text-xs text-warn"
        >
          Missing
        </p>
      );
    }
    if (refused !== undefined) {
      return (
        <p data-why="refused" data-code={refused.code} className="text-xs text-muted">
          {refused.message}
        </p>
      );
    }
    return <Failure error={query.error} onRetry={() => void query.refetch()} />;
  }
  const t = query.data;
  return (
    <div data-why="drift" className="flex flex-col gap-2 text-sm">
      {t.resource?.requiresPruning === true || t.argoDiff?.notInGit === true ? (
        <p data-not-in-git="" title="A sync with pruning deletes it." className="text-xs text-warn">
          Not in git
        </p>
      ) : null}
      <Drift tracked={t} verdict={verdictOf(t)} />
    </div>
  );
}

/** How long ago Argo CD last compared the Application, on the console's
 *  scale: `34m ago`, `3h ago`. */
export function comparedAgo(at: string, now: number = Date.now()): string {
  return Number.isNaN(Date.parse(at)) ? at : ago(at, now);
}

/** The route's answers about the cluster, as opposed to failures to ask it. */
const ANSWERS = new Set(['list_forbidden', 'kind_unknown', 'object_not_found', 'bad_request']);

function answerOf(error: unknown): { code: string; message: string } | undefined {
  if (!(error instanceof ApiError)) return undefined;
  const problem = error.body as Problem | undefined;
  if (typeof problem?.code !== 'string' || !ANSWERS.has(problem.code)) return undefined;
  return { code: problem.code, message: typeof problem.message === 'string' && problem.message !== '' ? problem.message : error.message };
}

type Verdict = 'synced' | 'outofsync' | 'unknown';

const VERDICT: Readonly<Record<Verdict, { label: string; tone: string }>> = {
  synced: { label: 'Synced', tone: 'ok' },
  outofsync: { label: 'Out of sync', tone: 'warn' },
  unknown: { label: 'Unknown', tone: 'muted' },
};

/** Argo CD's word for this resource, in `status.resources`. Nothing else
 *  decides it: the Application's own word is about all of its resources. */
function verdictOf(t: Tracked): Verdict {
  if (t.resource?.status === 'Synced') return 'synced';
  if (t.resource?.status === 'OutOfSync') return 'outofsync';
  return 'unknown';
}

function Answer({ cluster, tracked: t }: { cluster: string; tracked: Tracked }): ReactNode {
  if (!t.managed) {
    return (
      <div data-argo-tab="unmanaged" className="flex flex-col gap-1 text-sm">
        <p title={t.note}>
          <Pill tone="muted" neutral="muted">
            Not managed by Argo CD
          </Pill>
        </p>
      </div>
    );
  }

  const verdict = verdictOf(t);
  const app = t.application;
  const owner = t.through?.[t.through.length - 1];
  return (
    <div data-argo-tab="managed" data-verdict={verdict} className="flex flex-col gap-3 text-sm">
      <p className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span data-argo-verdict="">
          <Pill tone={VERDICT[verdict].tone} neutral="muted">
            {VERDICT[verdict].label}
          </Pill>
        </span>
        {t.resource?.requiresPruning === true || t.argoDiff?.notInGit === true ? (
          <span data-not-in-git="" className="text-xs text-warn">
            Not in git
          </span>
        ) : null}
        {app?.reconciledAt === undefined ? null : (
          <span data-argo-compared="" title={app.reconciledAt} className="text-xs text-muted">
            compared {comparedAgo(app.reconciledAt)}
          </span>
        )}
        {app === undefined ? null : <RefreshApplication cluster={cluster} namespace={app.namespace} name={app.name} />}
        <span className="text-xs text-muted">
          {owner === undefined ? null : (
            <>
              via{' '}
              {owner.path === undefined ? (
                `${owner.kind} ${owner.name}`
              ) : (
                <Link
                  to={`/c/${encodeURIComponent(cluster)}/${owner.path}/${owner.namespace === undefined ? '' : `${encodeURIComponent(owner.namespace)}/`}${encodeURIComponent(owner.name)}?tab=x.argocd.sync`}
                  title={(t.through ?? []).map((o) => `${o.kind} ${o.name}`).join(' → ')}
                  className={`text-accent hover:underline ${focusRing}`}
                >
                  {owner.kind} {owner.name}
                </Link>
              )}
              {' · '}
            </>
          )}
          {app === undefined ? null : (
            <Link
              to={`/c/${encodeURIComponent(cluster)}/${PAGE_PATH}/applications/${encodeURIComponent(app.namespace)}/${encodeURIComponent(app.name)}`}
              className={`text-accent hover:underline ${focusRing}`}
            >
              {app.name}
            </Link>
          )}
        </span>
      </p>

      {/* Why the word is not known: the Application could not be read, or does
          not list this object. A refusal, with how to grant the read one fold
          away. */}
      {t.applicationNote === undefined ? null : (
        <p data-argo-why="" data-code={t.applicationCode} title={t.applicationNote} className="text-xs text-muted">
          {t.applicationCode === 'list_forbidden' || t.applicationCode === 'list_unreviewed'
            ? 'Application not readable'
            : 'Application not found'}
        </p>
      )}

      <Drift tracked={t} verdict={verdict} />
    </div>
  );
}

/** The one diff this tab draws, or the one line saying why there is none. */
function Drift({ tracked: t, verdict }: { tracked: Tracked; verdict: Verdict }): ReactNode {
  if (t.secret === true) return <SecretDrift tracked={t} verdict={verdict} />;
  // Argo CD's own diff wins, except where it finds nothing and the object did
  // change since Argo CD applied it (ignoreDifferences on a field edited by
  // hand): then that drift is drawn, marked as one Argo CD does not count,
  // rather than hidden behind "no difference".
  const answered = t.argoDiff?.compared === true ? t.argoDiff : undefined;
  const hiddenDrift = answered !== undefined && answered.fields.length === 0 && t.drift.compared && t.drift.fields.length > 0;
  const own = hiddenDrift ? undefined : answered;
  const lastApplied = own === undefined && t.drift.compared ? t.drift : undefined;
  const fields = own?.fields ?? lastApplied?.fields;

  // Why Argo CD's own diff is not the one drawn, when it is not: off (a setup,
  // folded), or failed (said, in warn). Git no longer having the object is an
  // answer, already said beside the verdict.
  const ownState =
    answered !== undefined ||
    t.argoDiff === undefined ||
    t.argoDiff.notInGit === true ||
    t.argoDiff.setup ||
    t.argoDiff.note === undefined ? null : (
      <p data-argo-diff="failed" title={t.argoDiff.note} className="text-xs text-warn">
        Argo CD diff unavailable
      </p>
    );

  if (fields === undefined) {
    // Nothing to compare against. Synced needs no more words; otherwise say why.
    return (
      <>
        {verdict === 'synced' || t.drift.note === undefined ? null : (
          <p data-drift="none-compared" title={t.drift.note} className="text-xs text-muted">
            Not compared
          </p>
        )}
        {ownState}
      </>
    );
  }

  if (fields.length === 0) {
    return (
      <>
        {verdict !== 'outofsync' ? null : lastApplied !== undefined ? (
          <p data-drift="in-git" className="text-xs text-muted">
            Commit not synced{revisions(t)}
          </p>
        ) : (
          <p data-drift="none" className="text-xs text-muted">
            No field difference
          </p>
        )}
        {ownState}
      </>
    );
  }

  return (
    <>
      <DiffTable
        fields={fields}
        source={own !== undefined ? 'git' : 'last-applied'}
        ignored={verdict === 'synced' || hiddenDrift}
      />
      {ownState}
    </>
  );
}

/**
 * A Secret is never read: "Secret differs" and the keys Argo CD's masked
 * comparison names, never a value.
 */
function SecretDrift({ tracked: t, verdict }: { tracked: Tracked; verdict: Verdict }): ReactNode {
  const keys = t.argoDiff?.compared === true ? t.argoDiff.fields.map((f) => secretKey(f.path)) : [];
  if (verdict !== 'outofsync' && keys.length === 0) return null;
  return (
    <div data-drift="secret" className="flex flex-wrap items-center gap-1.5 text-xs">
      <span className="text-warn">Secret differs</span>
      {keys.map((key) => (
        <span key={key} data-secret-key={key} className="rounded-sm bg-panel-2 px-1.5 py-0.5 font-mono text-fg">
          {key}
        </span>
      ))}
    </div>
  );
}

/** `data.password` → `password`, `data["tls.crt"]` → `tls.crt`. */
function secretKey(path: string): string {
  const quoted = /^(?:data|stringData)\["(.*)"\]$/.exec(path);
  if (quoted?.[1] !== undefined) return quoted[1];
  const plain = /^(?:data|stringData)\.(.+)$/.exec(path);
  return plain?.[1] ?? path;
}

/**
 * The difference, field by field: what Argo CD wants, what is live. A Secret's
 * values never travel, only which fields differ.
 */
function DiffTable({
  fields,
  source,
  ignored,
}: {
  fields: readonly DriftField[];
  source: 'git' | 'last-applied';
  /** Argo CD calls the resource Synced and these still differ from what it
   *  applied: ignoreDifferences, or not compared since. */
  ignored: boolean;
}): ReactNode {
  const wanted = source === 'git' ? 'Git' : 'Last applied';
  return (
    <div className="flex flex-col gap-1" data-diff={source}>
      {ignored ? (
        <p className="text-xs text-muted" title="Ignored by Argo CD (ignoreDifferences), or changed since it last compared.">
          Ignored by Argo CD
        </p>
      ) : null}
      <div className="overflow-x-auto rounded-md border border-line">
        <table className="w-full border-collapse font-mono text-xs" aria-label={`${wanted} against live`}>
          <thead>
            <tr className="bg-panel-2 text-left font-sans text-2xs tracking-eyebrow text-muted uppercase">
              <th className="px-2 py-1 font-medium">Field</th>
              <th className="px-2 py-1 font-medium">{wanted}</th>
              <th className="px-2 py-1 font-medium">Live</th>
            </tr>
          </thead>
          <tbody>
            {fields.map((f) => (
              <tr key={f.path} data-change={f.change} className="border-t border-line align-top">
                <td className="px-2 py-1 text-fg [overflow-wrap:anywhere]">{f.path}</td>
                <td className="bg-ok/10 px-2 py-1 text-ok [overflow-wrap:anywhere]">
                  {f.change === 'added' ? <Absent /> : <span>{f.applied}</span>}
                </td>
                <td className="bg-warn/10 px-2 py-1 text-warn [overflow-wrap:anywhere]">
                  {f.change === 'missing' ? <Absent /> : <span>{f.live}</span>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function Absent(): ReactNode {
  return <span className="font-sans text-muted italic">absent</span>;
}

function revisions(t: Tracked): string {
  const now = t.application?.syncRevision;
  const applied = t.syncedRevision;
  if (now === undefined || applied === undefined || now === applied) return '';
  return ` (git ${short(now)}, applied ${short(applied)})`;
}

function short(revision: string): string {
  return revision.length > 7 ? revision.slice(0, 7) : revision;
}
