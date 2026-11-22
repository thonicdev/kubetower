import { queryOptions } from '@tanstack/react-query';
import { request } from '@kubetower/ui';

/**
 * The page's own settings (`settings.go`'s `SettingsAnswer`): which tool to
 * look for, and where each tool's own UI lives. The addresses arrive already
 * validated and trimmed; the front end still builds links only from an
 * http(s) address.
 */
export type Tool = 'auto' | 'argocd' | 'flux' | 'both';

export interface DeliverySettings {
  tool: Tool;
  argoUrl: string | undefined;
  fluxUrl: string | undefined;
}

export const DEFAULT_SETTINGS: DeliverySettings = { tool: 'auto', argoUrl: undefined, fluxUrl: undefined };

export function settingsPath(cluster: string): string {
  return `/x/${encodeURIComponent(cluster)}/argocd/settings`;
}

function web(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined;
  try {
    const url = new URL(value);
    return (url.protocol === 'http:' || url.protocol === 'https:') && url.hostname !== '' && url.username === ''
      ? value.replace(/\/+$/, '')
      : undefined;
  } catch {
    return undefined;
  }
}

export async function getSettings(cluster: string, signal?: AbortSignal): Promise<DeliverySettings> {
  const body = await request<{ tool?: unknown; argoUrl?: unknown; fluxUrl?: unknown }>(
    settingsPath(cluster),
    signal === undefined ? {} : { signal },
  );
  const tool = body.tool === 'argocd' || body.tool === 'flux' || body.tool === 'both' ? body.tool : 'auto';
  return { tool, argoUrl: web(body.argoUrl), fluxUrl: web(body.fluxUrl) };
}

export function settingsQuery(cluster: string) {
  return queryOptions({
    queryKey: ['argocd', 'settings', cluster] as const,
    queryFn: ({ signal }) => getSettings(cluster, signal),
  });
}

/** Where Argo CD's UI shows one Application. */
export function argoLink(base: string, namespace: string, name: string): string {
  return `${base}/applications/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

/**
 * Where Weave GitOps shows a Flux object: `/kustomization/details` or
 * `/helm_release/details`, with the object named in the query. `clusterName`
 * is Weave GitOps' own name for the local cluster, `Default`.
 */
export function fluxLink(base: string, kind: string, namespace: string, name: string): string | undefined {
  const page = kind === 'Kustomization' ? 'kustomization' : kind === 'HelmRelease' ? 'helm_release' : undefined;
  if (page === undefined) return undefined;
  const query = new URLSearchParams({ clusterName: 'Default', name, namespace });
  return `${base}/${page}/details?${query.toString()}`;
}
