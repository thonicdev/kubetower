import { queryOptions } from '@tanstack/react-query';
import { EnvelopeError, request, type ApiError, type ListResult } from '@kubetower/ui';

import { refusalOf, refused, said, subjectOf, type Engine, type Problem, type Refusal } from './data';

/**
 * What `GET /x/{cluster}/argocd/flux` answers (`flux.go`), and what the page
 * makes of it: Flux CD's Kustomizations and HelmReleases, beside Argo CD's
 * Applications and never folded into them.
 *
 * The same four refusals as Argo CD's list, read the same way and drawn per
 * engine, so one engine absent or refused never hides the other.
 */

/** One row, as `flux.go`'s `FluxObject` sends it. */
export interface FluxObject {
  kind: 'Kustomization' | 'HelmRelease' | string;
  namespace: string;
  name: string;
  /** The `Ready` condition's status as Flux writes it: `True`, `False`,
   *  `Unknown`, or `''` when Flux has not reported one. */
  ready: string;
  reason?: string;
  message?: string;
  reconciling: boolean;
  stalled: boolean;
  suspended: boolean;
  revision?: string;
  deleting: boolean;
}

export const FLUX_CD: Engine = {
  resource: 'kustomizations.kustomize.toolkit.fluxcd.io, helmreleases.helm.toolkit.fluxcd.io',
  product: 'Flux CD',
};

export interface Flux {
  list: ListResult<FluxObject>;
  /** A sentence beside the rows: a reconciler that is absent, a read that stopped. */
  note: string | undefined;
  refusal: Refusal | undefined;
  subject?: string | undefined;
}

export function fluxPath(cluster: string): string {
  return `/x/${encodeURIComponent(cluster)}/argocd/flux`;
}

export async function getFlux(cluster: string, signal?: AbortSignal): Promise<Flux> {
  let body: { objects?: unknown; complete?: unknown; note?: unknown };
  try {
    body = await request(fluxPath(cluster), signal === undefined ? {} : { signal });
  } catch (error) {
    const refusal = refusalOf(error);
    if (refusal !== undefined) {
      const problem = (error as ApiError).body as Problem | undefined;
      return { list: refused(refusal, problem, cluster, FLUX_CD), note: undefined, refusal, subject: subjectOf(problem) };
    }
    throw said(error);
  }
  if (!Array.isArray(body.objects) || typeof body.complete !== 'boolean') {
    throw new EnvelopeError('the Delivery Center answered Flux without an objects array and a complete flag', body);
  }
  const items = (body.objects as FluxObject[])
    .slice()
    .sort((a, b) => a.kind.localeCompare(b.kind) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name));
  const note = typeof body.note === 'string' && body.note !== '' ? body.note : undefined;
  if (items.length > 0) return { list: { state: 'ok', items }, note, refusal: undefined };
  return {
    list: {
      state: 'empty',
      items: [],
      reason: {
        code: 'empty_scope',
        message: 'Flux CD answered on this cluster and has no Kustomization or HelmRelease this console may read.',
      },
    },
    note,
    refusal: undefined,
  };
}

export function fluxQuery(cluster: string) {
  return queryOptions({
    queryKey: ['argocd', 'flux', cluster] as const,
    queryFn: ({ signal }) => getFlux(cluster, signal),
  });
}
