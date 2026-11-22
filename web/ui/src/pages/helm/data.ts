import { queryOptions, useQuery, type UseQueryResult } from '@tanstack/react-query';

import { asList, asValue, request, type Column, type ListResult, type ValueResult } from '../../api';
import type { ListEnvelopeWire, ValueEnvelopeWire } from '../../api/envelope';
import type { components, paths } from '../../api/schema';
import { clusterPath } from '../../clusters/clusterPath';

/**
 * The Helm releases on one cluster, and one release's own history.
 *
 * **Helm is part of the core console.** There is no extras gate on it: a
 * release is an ordinary Secret and the console reads it directly.
 *
 * ## The namespace selection is repeatable
 *
 * `?namespace=` is a **repeatable** parameter - `?namespace=argocd&namespace=ingress`
 * narrows to both - because the control it feeds is a multiple selection, and
 * narrowing to the four namespaces a thirty-namespace cluster is actually read
 * through is how the page is used.
 *
 * The server reads every value, so a two-namespace selection is answered as
 * two. This is the client half, and it is why `selection` below appends one
 * parameter per namespace rather than joining them.
 *
 * ## The address carries `namespace=a,b`, and the request carries two parameters
 *
 * Two spellings of one selection, and the split is deliberate. The **address**
 * has to be one parameter a person can type and a bookmark can hold, a
 * `namespace=a,b` form that works with every script off. The **request** has to be the repeatable shape the contract declares.
 * Joining them in the URL and splitting them at the fetch is the one place the
 * two meet.
 *
 * The parameter is named `namespace` and not `ns` because `pages/resources`
 * already chose that spelling for the same fact, and two names for one selection
 * is how a page comes to keep a filter the page beside it drops.
 */

type Schemas = components['schemas'];

export type HelmRelease = Schemas['HelmRelease'];
export type HelmRevision = Schemas['HelmRevision'];
export type HelmReleaseDetail = Schemas['HelmReleaseDetail'];
export type HelmValues = Schemas['HelmValues'];
export type HelmNotes = Schemas['HelmNotes'];

/** The same helper as the other pages'; see the note in `pages/overview/data.ts`
 *  about why it is copied rather than shared while several are being ported. */
function path<T extends keyof paths & string>(
  template: T,
  params: Readonly<Record<string, string>> = {},
): string {
  return template.replace(/\{(\w+)\}/g, (_match, name: string) => {
    const value = params[name];
    if (value === undefined) throw new Error(`${template}: no value for {${name}}`);
    return encodeURIComponent(value);
  });
}

/**
 * `a, b ,` becomes `['a','b']`.
 *
 * Empty pieces are dropped and nothing else is transformed: the user's
 * typing is the authority on what they meant, and a console that lower-cased a
 * namespace would be asking for a different one. An empty selection is **every
 * namespace the caller may read**, which is not `default`.
 */
export function selectionOf(value: string): string[] {
  return value
    .split(',')
    .map((piece) => piece.trim())
    .filter((piece) => piece !== '');
}

function withNamespaces(base: string, namespaces: readonly string[]): string {
  if (namespaces.length === 0) return base;
  const query = new URLSearchParams();
  for (const namespace of namespaces) query.append('namespace', namespace);
  return `${base}?${query.toString()}`;
}

/**
 * The release list, and the sentence that says the access review narrowed it:
 * the person may not list Secrets in every namespace, so the releases are those
 * of the namespaces where they may. `accessNote` is absent on a list that is the
 * cluster's whole answer.
 */
export type HelmReleases = ListResult<HelmRelease> & {
  readonly accessNote?: string;
  /** The list's columns as the console arranged them - the same store and the
   *  same writes as a resource list's, under the kind `helm`. Absent on a
   *  failure. */
  readonly columns?: Column[];
  readonly columnsCustomised?: boolean;
};

export async function getHelmReleases(
  cluster: string,
  namespaces: readonly string[],
  signal?: AbortSignal,
): Promise<HelmReleases> {
  const wire = await request<
    ListEnvelopeWire<HelmRelease> & { accessNote?: string; columns?: Column[]; columnsCustomised?: boolean }
  >(withNamespaces(path('/api/c/{cluster}/helm', { cluster }), namespaces), signal ? { signal } : {});
  const list = asList(wire);
  return {
    ...list,
    ...(wire.accessNote === undefined || wire.accessNote === '' ? {} : { accessNote: wire.accessNote }),
    ...(wire.columns === undefined ? {} : { columns: wire.columns }),
    ...(wire.columnsCustomised === undefined ? {} : { columnsCustomised: wire.columnsCustomised }),
  };
}

export async function getHelmRelease(
  cluster: string,
  namespace: string,
  name: string,
  signal?: AbortSignal,
): Promise<ValueResult<HelmReleaseDetail>> {
  const wire = await request<ValueEnvelopeWire<HelmReleaseDetail>>(
    path('/api/c/{cluster}/helm/{namespace}/{name}', { cluster, namespace, name }),
    signal ? { signal } : {},
  );
  return asValue(wire);
}

/**
 * Reveal the values supplied to one revision of a release.
 *
 * **The only way the page gets them.** Supplied values routinely carry
 * passwords, so the release's own answer says only whether there are any
 * (`valuesSupplied`), and this is the reveal: reviewed for the person, recorded
 * on the server - who, which release, which revision - and never cached. That
 * is why it is a plain call the page makes on a gesture and not a query: a
 * query would sit in the cache, be refetched on focus and be written again on
 * every refetch, each one a record of a reveal nobody asked for.
 *
 * `revision` is the one the page was drawn from. A release that has moved on is
 * answered `absent`, and the page says so rather than showing a newer
 * revision's values under an older revision's facts.
 */
export async function revealHelmValues(
  cluster: string,
  namespace: string,
  name: string,
  revision: number,
  signal?: AbortSignal,
): Promise<ValueResult<HelmValues>> {
  const base = path('/api/c/{cluster}/helm/{namespace}/{name}/values', { cluster, namespace, name });
  const wire = await request<ValueEnvelopeWire<HelmValues>>(
    `${base}?${new URLSearchParams({ revision: String(revision) }).toString()}`,
    signal ? { signal } : {},
  );
  return asValue(wire);
}

/**
 * Reveal the notes Helm rendered for one revision of a release.
 *
 * The values' door, under the key `notes`, for the values' reason: charts print
 * a generated admin password or a token in their `NOTES.txt`. The release's own
 * answer says only whether there are any (`notesRendered`); this is reviewed and
 * recorded on the server, and a plain call on a gesture rather than a query for
 * the same reason as `revealHelmValues`.
 */
export async function revealHelmNotes(
  cluster: string,
  namespace: string,
  name: string,
  revision: number,
  signal?: AbortSignal,
): Promise<ValueResult<HelmNotes>> {
  const base = path('/api/c/{cluster}/helm/{namespace}/{name}/notes', { cluster, namespace, name });
  const wire = await request<ValueEnvelopeWire<HelmNotes>>(
    `${base}?${new URLSearchParams({ revision: String(revision) }).toString()}`,
    signal ? { signal } : {},
  );
  return asValue(wire);
}

/**
 * A release's own page, carrying the list's namespace selection so the way
 * back returns to the list as it was narrowed. Repeated `namespace`, the
 * spelling `useUrlList` writes on the list.
 */
export function releasePath(
  cluster: string,
  namespace: string,
  name: string,
  selection: readonly string[] = [],
): string {
  return (
    `${clusterPath(cluster)}/helm/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}` +
    (selection.length === 0
      ? ''
      : `?${new URLSearchParams(selection.map((one) => ['namespace', one])).toString()}`)
  );
}

export const helmKey = (cluster: string, namespaces: readonly string[]) =>
  ['helm', cluster, [...namespaces].join(',')] as const;

export const helmReleaseKey = (cluster: string, namespace: string, name: string) =>
  ['helm-release', cluster, namespace, name] as const;

/**
 * No `refetchInterval` on either, and that is a decision rather than an
 * omission.
 *
 * A Helm release changes when somebody runs `helm upgrade`, which is minutes
 * apart at best and usually days. The dashboard polls because a cluster's own
 * state moves under the reader; this does not, and a poll here would be one
 * Secret list per open tab per interval for an answer that is the same every
 * time.
 */
export function helmQuery(cluster: string, namespaces: readonly string[]) {
  return queryOptions({
    queryKey: helmKey(cluster, namespaces),
    queryFn: ({ signal }) => getHelmReleases(cluster, namespaces, signal),
  });
}

export function helmReleaseQuery(cluster: string, namespace: string, name: string) {
  return queryOptions({
    queryKey: helmReleaseKey(cluster, namespace, name),
    queryFn: ({ signal }) => getHelmRelease(cluster, namespace, name, signal),
  });
}

export function useHelmReleases(
  cluster: string | undefined,
  namespaces: readonly string[],
): UseQueryResult<HelmReleases> {
  return useQuery({
    ...helmQuery(cluster ?? '', namespaces),
    enabled: cluster !== undefined,
  });
}

export function useHelmRelease(
  cluster: string | undefined,
  namespace: string | undefined,
  name: string | undefined,
): UseQueryResult<ValueResult<HelmReleaseDetail>> {
  return useQuery({
    ...helmReleaseQuery(cluster ?? '', namespace ?? '', name ?? ''),
    enabled: cluster !== undefined && namespace !== undefined && name !== undefined,
  });
}
