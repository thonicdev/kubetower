import type { ReactNode } from 'react';
import { Link } from 'react-router';
import { Pill, Tile, TileGrid, focusRing } from '@kubetower/ui';

import type { View } from './report';

/**
 * The AppProjects and the failing ApplicationSets, as tiles under the
 * Applications, part of the page and split by project. Drawn from the report
 * route, whose
 * every sentence is decided in Go (`report/`).
 *
 * A project tile filters the page to that project; the AppProject object
 * itself is one link away, on its own line. An ApplicationSet tile opens the
 * object. There are no tables of the worst offenders, the conditions by cause
 * or what was read: Needs attention and the Application tiles say that.
 *
 * Nothing here writes, syncs or offers to.
 */
export function ProjectsAndSets({
  cluster,
  view,
  active,
  onProject,
}: {
  cluster: string;
  view: View;
  active: string | undefined;
  onProject: (p: string | undefined) => void;
}): ReactNode {
  if (view.state === 'absent') return null;
  return (
    <div className="flex flex-col gap-4" data-argo-catalogue="">
      {view.detail === undefined ? null : (
        <p className={`text-xs ${view.state === 'unreadable' ? 'text-bad' : 'text-muted'}`}>{view.detail}</p>
      )}

      <section aria-labelledby="argo-projects" className="flex flex-col gap-2">
        <h3 id="argo-projects" className="border-b border-line pb-1 text-sm font-medium">
          Projects <span className="font-normal text-muted tabular-nums">{view.projectTotal}</span>
        </h3>
        {view.projectsUnavailable !== undefined ? (
          <p className="text-xs text-muted">{view.projectsUnavailable}</p>
        ) : view.projects.length === 0 ? (
          <p className="text-xs text-muted">No AppProject on this cluster.</p>
        ) : (
          <TileGrid label="Argo CD projects">
            {view.projects.map((p) => (
              <Tile
                key={`${p.namespace}/${p.name}`}
                data-project={p.name}
                title={p.name}
                tone={p.offending > 0 ? 'warn' : p.applications === 0 ? 'off' : 'ok'}
                onOpen={() => onProject(p.name)}
                openLabel={active === p.name ? `Show every project` : `Show project ${p.name} only`}
                badges={
                  <>
                    <Pill tone="normal">
                      {p.applications} {p.applications === 1 ? 'application' : 'applications'}
                    </Pill>
                    {p.offending > 0 ? (
                      <Pill tone="warn">
                        {p.offending} {p.offending === 1 ? 'needs' : 'need'} attention
                      </Pill>
                    ) : null}
                    {active === p.name ? <Pill tone="normal">shown</Pill> : null}
                  </>
                }
                detail={p.description === undefined || p.description === '' ? undefined : <span title={p.description}>{p.description}</span>}
              >
                <Link
                  to={objectPath(cluster, 'appprojects.argoproj.io', p.namespace, p.name)}
                  className={`self-start text-xs text-accent hover:underline ${focusRing}`}
                >
                  AppProject {p.namespace}/{p.name}
                </Link>
              </Tile>
            ))}
          </TileGrid>
        )}
        {view.projectsNote === undefined ? null : <p className="text-xs text-muted">{view.projectsNote}</p>}
      </section>

      <section aria-labelledby="argo-sets" className="flex flex-col gap-2">
        <h3 id="argo-sets" className="border-b border-line pb-1 text-sm font-medium">
          ApplicationSets{' '}
          <span className="font-normal text-muted tabular-nums">
            {view.setTotal}
            {view.setFailing === 0 ? null : <span className="text-bad"> · {view.setFailing} failing</span>}
          </span>
        </h3>
        {view.setsUnavailable !== undefined ? (
          <p className="text-xs text-muted">{view.setsUnavailable}</p>
        ) : view.sets.length === 0 ? (
          <p className="text-xs text-muted">{view.setTotal === 0 ? 'No ApplicationSet on this cluster.' : 'None failing.'}</p>
        ) : (
          <TileGrid label="Failing ApplicationSets">
            {view.sets.map((s) => (
              <Tile
                key={`${s.namespace}/${s.name}`}
                data-set={`${s.namespace}/${s.name}`}
                title={s.name}
                tone="bad"
                to={objectPath(cluster, 'applicationsets.argoproj.io', s.namespace, s.name)}
                openLabel={`Open ApplicationSet ${s.name}`}
                badges={<Pill tone="bad">{s.type}</Pill>}
                detail={<span title={s.message}>{s.message}</span>}
              />
            ))}
          </TileGrid>
        )}
        {view.setsNote === undefined ? null : <p className="text-xs text-muted">{view.setsNote}</p>}
      </section>

      <ArgoNotes view={view} />
    </div>
  );
}

function objectPath(cluster: string, kind: string, namespace: string, name: string): string {
  return `/c/${encodeURIComponent(cluster)}/${kind}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

/**
 * What the page did not read, folded: it is for the operator who wonders why a
 * number is not there, not for every reading of the page.
 */
function ArgoNotes({ view }: { view: View }): ReactNode {
  if (view.notes.length === 0) return null;
  return (
    <details className="text-xs text-muted" data-argo-notes="">
      <summary className={`cursor-pointer text-fg ${focusRing}`}>What this page did not read</summary>
      <ul className="mt-2 list-disc pl-5">
        {view.notes.map((n) => (
          <li key={n}>{n}</li>
        ))}
      </ul>
    </details>
  );
}
