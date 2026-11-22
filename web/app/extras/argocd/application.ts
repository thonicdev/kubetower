import { queryOptions } from '@tanstack/react-query';
import { ApiError, request } from '@kubetower/ui';

import { said, subjectOf, type Problem } from './data';

/**
 * What `GET /x/{cluster}/argocd/applications/{namespace}/{name}` answers
 * (`application.go`): one Application, read whole, read-only.
 */

export interface Source {
  repoURL?: string;
  path?: string;
  chart?: string;
  targetRevision?: string;
}

export interface OperationResult {
  kind: string;
  namespace?: string;
  name: string;
  status?: string;
  hookPhase?: string;
  message?: string;
}

export interface Operation {
  phase: string;
  message?: string;
  startedAt?: string;
  finishedAt?: string;
  initiatedBy?: string;
  automated: boolean;
  revision?: string;
  results: OperationResult[];
}

export interface Deployment {
  id: number;
  revision?: string;
  deployedAt?: string;
  startedAt?: string;
  source: Source;
}

export interface Condition {
  type: string;
  message?: string;
  error: boolean;
}

export interface Resource {
  group?: string;
  version?: string;
  kind: string;
  namespace?: string;
  name: string;
  /** Argo's sync word for this resource: `Synced`, or `OutOfSync`. */
  status?: string;
  health?: string;
  healthMessage?: string;
  requiresPruning: boolean;
  /** The kind's segment in the console's own address, or absent. */
  path?: string;
}

/** `spec.destination`: where Argo CD applies an Application. */
export interface Destination {
  server?: string;
  name?: string;
  namespace?: string;
}

export interface ApplicationDetail {
  namespace: string;
  name: string;
  project?: string;
  deleting: boolean;
  source: Source;
  destination: Destination;
  sync: string;
  syncRevision?: string;
  health: string;
  healthMessage?: string;
  reconciledAt?: string;
  automatedSync: boolean;
  operation?: Operation;
  history: Deployment[];
  conditions: Condition[];
  resources: Resource[];
  resourcesNote?: string;
  /** Where the AppProject lives, so the page opens it. */
  projectNamespace?: string;
  /** The Application's Kubernetes Events, newest first. */
  events?: AppEvent[];
  eventsNote?: string;
}

export interface AppEvent {
  type?: string;
  reason?: string;
  message?: string;
  count?: number;
  at?: string;
}

/** A refusal the page draws as what to do (the grant panel), carried as an
 *  error so `ValueView`-less code can branch on it by its code. */
export class Refused extends Error {
  readonly subject: string | undefined;
  constructor(message: string, subject: string | undefined) {
    super(message);
    this.subject = subject;
  }
}

export function applicationAPI(cluster: string, namespace: string, name: string): string {
  return `/x/${encodeURIComponent(cluster)}/argocd/applications/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

export async function getApplication(
  cluster: string,
  namespace: string,
  name: string,
  signal?: AbortSignal,
): Promise<ApplicationDetail> {
  try {
    return await request<ApplicationDetail>(applicationAPI(cluster, namespace, name), signal === undefined ? {} : { signal });
  } catch (error) {
    if (error instanceof ApiError) {
      const problem = error.body as Problem | undefined;
      if (problem?.code === 'list_forbidden') {
        throw new Refused(typeof problem.message === 'string' ? problem.message : 'The read was refused.', subjectOf(problem));
      }
    }
    throw said(error);
  }
}

export function applicationQuery(cluster: string, namespace: string, name: string) {
  return queryOptions({
    queryKey: ['argocd', 'application', cluster, namespace, name] as const,
    queryFn: ({ signal }) => getApplication(cluster, namespace, name, signal),
  });
}

/** Where the console shows one object: its own page, which reads it for the
 *  signed-in person and carries its Manifest tab and its Logs. */
export function consoleObjectPath(cluster: string, resource: Resource): string | undefined {
  if (resource.path === undefined || resource.path === '') return undefined;
  const parts = [resource.path];
  if (resource.namespace !== undefined && resource.namespace !== '') parts.push(resource.namespace);
  parts.push(resource.name);
  return `/c/${encodeURIComponent(cluster)}/${parts.map((p) => encodeURIComponent(p)).join('/')}`;
}
