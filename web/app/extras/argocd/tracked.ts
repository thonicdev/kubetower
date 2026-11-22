import { queryOptions } from '@tanstack/react-query';
import { request, type ExtensionCell, type ExtensionTabObject } from '@kubetower/ui';

import type { ApplicationDetail, Resource } from './application';
import { said } from './data';

/**
 * What `GET /x/{cluster}/argocd/resource` answers (`tracked.go`): one
 * object as Argo CD sees it - the Application that manages it, Argo's sync
 * word for it, and what changed on it since Argo CD last applied it.
 */

/** Argo CD 3's mark on what it manages. The tab is offered on an object
 *  carrying it, or the instance label (`when` in `extension.tsx`). */
export const TRACKING_ANNOTATION = 'argocd.argoproj.io/tracking-id';
export const INSTANCE_LABEL = 'app.kubernetes.io/instance';
/** What `helm install` writes on its objects and Argo CD's own rendering of a
 *  chart never does: an instance label beside it is Helm's release name. */
export const HELM_RELEASE = 'meta.helm.sh/release-name';

/**
 * Whether an object carries Argo CD's mark, as the panel holds it: the tracking
 * annotation, or the instance label on an object no Helm release installed.
 * Without that last condition every object `helm install` made - the console's
 * own Deployment among them - would be offered an Argo CD tab that says "not
 * Argo CD's" (seen on docker-desktop).
 */
export function marked({ annotations, labels }: ExtensionTabObject): boolean {
  return TRACKING_ANNOTATION in annotations || (INSTANCE_LABEL in labels && !(HELM_RELEASE in annotations));
}

export interface DriftField {
  path: string;
  applied?: string;
  live?: string;
  /** `changed`; `missing`, Argo CD set it and it is gone; `added`, an item
   *  a list Argo CD replaces whole has and its manifest did not. */
  change: 'changed' | 'missing' | 'added';
}

export interface Drift {
  /** False when there was nothing to compare against; `note` says why. */
  compared: boolean;
  /** A Secret: the paths show, the values never. */
  masked: boolean;
  fields: DriftField[];
  note?: string;
}

export interface ObjectRef {
  kind: string;
  namespace?: string;
  name: string;
  /** The kind's segment in the console's address. */
  path?: string;
}

/** Argo CD's own diff, through argocd-server and a read-only token. */
export interface ArgoDiff {
  compared: boolean;
  masked: boolean;
  fields: DriftField[];
  /** No Secret holds the token: the tab says how to turn the diff on. */
  setup: boolean;
  /** Where the token is looked for, `namespace/name`. */
  secret?: string;
  note?: string;
  /** argocd-server answered that git no longer has the object: an answer,
   *  not a failed comparison. */
  notInGit?: boolean;
}

export interface Tracked {
  kind: string;
  namespace?: string;
  name: string;
  /** The object asked about, when the answer is its owner's. */
  of?: ObjectRef;
  /** The chain from the object's owner up to the one Argo CD manages. */
  through?: ObjectRef[];
  managed: boolean;
  via?: 'annotation' | 'label';
  mark?: string;
  copied: boolean;
  /** A Secret, answered without being read: only the keys that differ. */
  secret?: boolean;
  application?: ApplicationDetail;
  applicationCode?: string;
  applicationNote?: string;
  subject?: string;
  resource?: Resource;
  syncedRevision?: string;
  drift: Drift;
  argoDiff?: ArgoDiff;
  note?: string;
}

export function trackedAPI(cluster: string, kind: string, namespace: string | undefined, name: string): string {
  const query = new URLSearchParams({ kind, name });
  if (namespace !== undefined && namespace !== '') query.set('namespace', namespace);
  return `/x/${encodeURIComponent(cluster)}/argocd/resource?${query.toString()}`;
}

/** Whether Argo CD manages an object through its owner: the tab's probe on a
 *  Pod or a ReplicaSet, the same route the tab reads. */
export async function managedThroughOwner(
  object: { cluster: string; kind: string; namespace: string | undefined; name: string },
  signal: AbortSignal,
): Promise<boolean> {
  const answer = await request<Tracked>(trackedAPI(object.cluster, object.kind, object.namespace, object.name), { signal });
  return answer.managed && answer.of !== undefined;
}

/** Whether an Application really manages this object - the server's
 *  `managed`, which is false where the instance label names no Application
 *  listing it, or the tracking annotation was copied from another object.
 *  A mark is a hint and this is the proof: the tab is
 *  drawn on neither a hint alone nor a failed read. An answer through the
 *  owner counts - a ReplicaSet carries its Deployment's mark, and the tab
 *  draws the owner's answer, as it does for the unmarked case. */
export async function managedHere(
  object: { cluster: string; kind: string; namespace: string | undefined; name: string },
  signal: AbortSignal,
): Promise<boolean> {
  const answer = await request<Tracked>(trackedAPI(object.cluster, object.kind, object.namespace, object.name), { signal });
  return answer.managed;
}

/** The Argo CD column of a list: one read for the whole list, from the
 *  Applications alone. A refusal or an absent Argo CD rejects, and the list
 *  then draws no column. */
export async function argoColumn(
  list: { cluster: string; kind: string },
  signal: AbortSignal,
): Promise<Readonly<Record<string, ExtensionCell>>> {
  const query = new URLSearchParams({ kind: list.kind });
  return request<Record<string, ExtensionCell>>(`/x/${encodeURIComponent(list.cluster)}/argocd/column?${query.toString()}`, { signal });
}

export function trackedQuery(cluster: string, kind: string, namespace: string | undefined, name: string) {
  return queryOptions({
    queryKey: ['argocd', 'resource', cluster, kind, namespace ?? '', name] as const,
    queryFn: async ({ signal }) => {
      try {
        return await request<Tracked>(trackedAPI(cluster, kind, namespace, name), { signal });
      } catch (error) {
        throw said(error);
      }
    },
    // Read again each time the tab opens and while it stays open, so the
    // console adds no delay of its own to Argo CD's word.
    staleTime: 0,
    refetchInterval: 15_000,
  });
}
