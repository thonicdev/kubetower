import {
  hashKey,
  keepPreviousData,
  queryOptions,
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
  type UseMutationResult,
  type QueryClient,
  type QueryKey,
  type UseQueryResult,
} from '@tanstack/react-query';

import {
  getCapabilities,
  getKindPreferences,
  getNav,
  getNavGroup,
  getNavOccupancy,
  getPreferences,
  getManifest,
  getDataPanel,
  getMetricsPanel,
  getChangesPanel,
  getCreateTemplate,
  getPrereq,
  getResource,
  getSession,
  getSignInMethods,
  listClusters,
  listExtensions,
  listForwards,
  listNamespaces,
  listResources,
  setKindPreferences,
  type ListResourcesParams,
} from './endpoints';
import type { ListResult, ValueResult } from './envelope';
import { ApiError } from './errors';
import type {
  Capabilities,
  Cluster,
  DetailPage,
  ListPage,
  ManifestPanel,
  DataPanel,
  ExtensionInfo,
  MetricsPanel,
  ChangesPanel,
  CreateTemplate,
  Prereq,
  Namespace,
  KindPreferences,
  NavGroup,
  NavMenu,
  NavOccupancy,
  Preferences,
  Session,
  SignInMethods,
} from './types';

/**
 * Query keys and options, in one file, because a key written twice is a cache
 * that invalidates half of itself.
 *
 * Everything here already returns the *narrowed* result from `./envelope` - the
 * five states are decided in the network layer, once, rather than by whichever
 * component unwrapped the envelope. A component receives a `ListResult` and has
 * no `items` to reach for until it has said which arm it is in.
 */
export const queryKeys = {
  session: ['session'] as const,
  signInMethods: ['signInMethods'] as const,
  capabilities: ['capabilities'] as const,
  clusters: ['clusters'] as const,
  preferences: ['preferences'] as const,
  /** The port-forwards this process holds. Process state, not a cluster read,
   *  so no cluster in the key: one list, read by every Ports panel, and one
   *  entry for a close to take a forward out of. */
  forwards: ['forwards'] as const,
  nav: (cluster: string) => ['nav', cluster] as const,
  /** Which of one cluster's kinds hold an object the person may see.
   *  Not under `nav`: it lists, and invalidating the menu
   *  must not re-ask every kind of the cluster. */
  navOccupancy: (cluster: string) => ['navOccupancy', cluster] as const,
  /** The person's choices about the kinds column, where the server keeps them. */
  kindPreferences: ['kindPreferences'] as const,
  /** What the extensions are on one cluster: on or off, and their settings.
   *  The console's own file, so a switch invalidates it and nothing polls it. */
  extensions: (cluster: string) => ['extensions', cluster] as const,
  /** Which extensions that are off one cluster gives a reason to turn on
   *  Under `extensions`, so a switch invalidates it. */
  extensionSuggestions: (cluster: string) => ['extensions', cluster, 'suggestions'] as const,
  /** One deferred group of one cluster's menu. Under `nav` so that anything
   *  invalidating the menu invalidates the group it defers, which is the same
   *  cluster's discovery answered in two calls. */
  navGroup: (cluster: string, group: string) => ['nav', cluster, group] as const,
  namespaces: (cluster: string) => ['namespaces', cluster] as const,
  resourceList: (cluster: string, kind: string, namespace: string, selector: string, search: string, filter = '') =>
    ['resources', cluster, kind, namespace, selector, search, filter] as const,
  /** Everything read for one kind on one cluster, whatever the filter. What a
   *  column preference write invalidates: the arrangement is a property of the
   *  kind, so the filtered and unfiltered entries are both stale. */
  resourceKind: (cluster: string, kind: string) => ['resources', cluster, kind] as const,
  /** One object's manifest. **Not** under `resource`: invalidating the drawer
   *  after a write must not silently re-read a document the operator may be
   *  editing, and the two reads answer different questions about the same
   *  object. A manifest write invalidates both, by hand, which is the one
   *  place that relationship is stated. */
  manifest: (cluster: string, kind: string, namespace: string, name: string) =>
    ['manifest', cluster, kind, namespace, name] as const,
  /** One ConfigMap's or Secret's data block. Beside `manifest`: a write to it
   *  invalidates both, and a drawer refresh must not re-read either. */
  data: (cluster: string, kind: string, namespace: string, name: string) =>
    ['data', cluster, kind, namespace, name] as const,
  /** One object's metrics panel, at one window. The window is in the key because
   *  it is what was asked for: widening the range is a different question about
   *  the same object, and a shared key would draw the old window's samples
   *  under the new one's caption *as if they were the answer*. The old samples
   *  do stay on screen while the new window loads - `useMetricsPanel` says how
   *  and why - but as a placeholder the query flags and the span row marks,
   *  never as a cache entry claiming to be the new window. */
  metrics: (cluster: string, kind: string, namespace: string, name: string, window: string) =>
    ['metrics', cluster, kind, namespace, name, window] as const,
  /** One object's change history. Beside `manifest` rather than under `resource`
   *  for the same reason: it answers a different question about the object, and
   *  a drawer refresh has no business re-reading it. */
  changes: (cluster: string, kind: string, namespace: string, name: string) =>
    ['changes', cluster, kind, namespace, name] as const,
  /**
   * One object.
   *
   * The namespace is in the key as the empty string on a cluster-scoped kind,
   * and that is not a tidying detail: `undefined` in a query key is dropped by
   * the structural hash, so a node called `api` and a pod called `api` in no
   * namespace would share one cache entry. One trailing segment is a name, never
   * a namespace, here as in the routes.
   */
  resource: (cluster: string, kind: string, namespace: string, name: string) =>
    ['resource', cluster, kind, namespace, name] as const,
  /** The document the list's "+" opens, per kind and per
   *  namespace it is filled with - the empty string for none, for `resource`'s
   *  reason. Not under `resources`: a list refresh has no business re-reading it. */
  createTemplate: (cluster: string, kind: string, namespace: string) =>
    ['createTemplate', cluster, kind, namespace] as const,
  prereq: (cluster: string, key: string) => ['prereq', cluster, key] as const,
};

/**
 * Every read that answers *what is this object now*.
 *
 * Four keys and not one prefix: `resource`, `manifest`, `data` and `changes` are
 * siblings on purpose - each says above why it is not under the others - so
 * nothing about the key layout makes them reachable together, and a caller that
 * wants them all has to name them all. This is the list, in the file that
 * defines them, so that adding a fifth per-object read is one edit here rather
 * than four edits nobody makes.
 *
 * **`metrics` is deliberately not in it.** It answers *how much has this used*,
 * which a write does not change: yesterday's samples are still yesterday's
 * samples after somebody edits a label. And its key carries the window, so
 * reaching it would mean a prefix match, which would throw away every span the
 * operator has open - the same objection `SelectionBar.tsx` makes to the broad
 * `['resource', cluster, kind]` form.
 */
export function objectReadKeys(
  cluster: string,
  kind: string,
  namespace: string | undefined,
  name: string,
): QueryKey[] {
  return [
    queryKeys.resource(cluster, kind, namespace ?? '', name),
    queryKeys.manifest(cluster, kind, namespace ?? '', name),
    queryKeys.data(cluster, kind, namespace ?? '', name),
    queryKeys.changes(cluster, kind, namespace ?? '', name),
  ];
}

/**
 * What acting on an object makes stale: **every read of it, and the row it lives
 * in.**
 *
 * The object's own read is what the drawer draws, and the kind's list is where
 * the row lives; a manifest is a third reader of the same object, its data block
 * a fourth, its field-manager record a fifth, and none of the five carries a
 * `refetchInterval`. Only `resourceListQuery` does, so a caller that invalidated
 * the list and stopped would *look* correct: the list heals itself and the
 * panels do not.
 *
 * ## Why this is one function rather than four copies of a sentence
 *
 * Four components commit a plan - `Verbs.tsx`, `SelectionBar.tsx`, `Data.tsx`
 * and `ManifestEditor.tsx`. Each holding a private copy of *what reads this
 * object* goes wrong as soon as that answer gains a member: a bulk-deleted row's
 * drawer goes on offering verbs on it, or the Manifest tab keeps the document
 * from before a save. What reads an object is a property of the key layout
 * above, so it belongs beside the keys. What a caller decides for itself is the
 * only thing it actually knows: **which objects it acted on**, and whether it
 * needs to wait.
 *
 * ## One pass over the cache, whatever the arity
 *
 * Forty targets would be a hundred and sixty `invalidateQueries` calls, each a
 * full scan. A predicate against a pre-hashed set is a single scan and an O(1)
 * lookup per query, and the set is built from the key builders above, so no key
 * is ever spelled a second time. One target takes the same path: two scans in
 * all, which is what the single-object callers already cost.
 *
 * Returns a promise that settles when the refetches do, for the one caller that
 * has to wait - `ManifestEditor.tsx` closes its dialog onto the new document.
 * The others let it run.
 */
export function invalidateActedObjects(
  client: QueryClient,
  cluster: string,
  kind: string,
  targets: readonly { kind: string; namespace?: string; name: string }[],
): Promise<void> {
  const acted = new Set(
    targets.flatMap((one) =>
      objectReadKeys(cluster, one.kind, one.namespace, one.name).map((key) => hashKey(key)),
    ),
  );
  // The kind rather than one filtered read: the arrangement is a property of the
  // kind, and an object that moved moved in every filter of it.
  const waiting = [client.invalidateQueries({ queryKey: queryKeys.resourceKind(cluster, kind) })];
  // The namespace selector lists the cluster's namespaces under a key of its own,
  // kept for a minute: a namespace deleted here would otherwise go on being
  // offered by it until that minute ends.
  if (kind === 'namespaces' || targets.some((one) => one.kind === 'namespaces')) {
    waiting.push(client.invalidateQueries({ queryKey: queryKeys.namespaces(cluster) }));
  }
  if (acted.size > 0) {
    waiting.push(
      client.invalidateQueries({ predicate: (query) => acted.has(hashKey(query.queryKey)) }),
    );
  }
  return Promise.all(waiting).then(() => undefined);
}

/**
 * A query that must not send the session lapse signal when it 401s.
 *
 * `query/client.ts` routes a 401 from anywhere to the sign-in view. The sign-in
 * view's own calls are the exception, and the exception is declared on the query
 * rather than inferred from the current route, because a route is a place and
 * this is a property of the request.
 */
export const ANONYMOUS = { anonymous: true } as const;

export function sessionQuery() {
  return queryOptions({
    queryKey: queryKeys.session,
    queryFn: ({ signal }) => getSession(signal),
    // The one query whose 401 is ordinary rather than exceptional: it is what
    // the console asks to find out whether there is a session at all. It still
    // reports the lapse - that is how a tab left open overnight reaches the
    // sign-in view without the operator clicking anything.
    retry: false,
  });
}

/**
 * How often the shared profile's capabilities and object pages are re-read:
 * the minute the server holds an access review's answer for (`access.TTL` in
 * internal/access). Asking faster reads the same cached answer.
 */
export const SHARED_CAPABILITIES_MS = 60_000;

const isShared = (data: Capabilities | undefined): boolean => data?.mode === 'shared';

/**
 * What the sign-in view may offer. Asked with no session, so it is anonymous:
 * a failure here is not a lapse, and must not route the sign-in view to itself.
 *
 * **Re-asked every few seconds while the provider is not answering**, because
 * an unreachable provider is a state the page reports and then leaves - a view
 * that asked once would say "unavailable" until reloaded, long after the
 * console itself had noticed the provider was back.
 */
export function signInMethodsQuery() {
  return queryOptions({
    queryKey: queryKeys.signInMethods,
    queryFn: ({ signal }) => getSignInMethods(signal),
    meta: ANONYMOUS,
    retry: false,
    refetchInterval: (query) => {
      const state = query.state.data?.sso.state;
      return state === 'pending' || state === 'unavailable' ? 5_000 : false;
    },
  });
}

export function capabilitiesQuery() {
  return queryOptions({
    queryKey: queryKeys.capabilities,
    queryFn: ({ signal }) => getCapabilities(signal),
    /*
      **Two profiles, two answers to "can this change under me".**

      Everywhere but `shared` the capabilities describe the build and its
      routes, which do not change under a running process, so they are read once:
      re-asking on every window focus is noise in an audit log.

      Under `shared` they are still re-read on the server's own minute, so a
      member that depends on the signed-in person does not need a tab reloaded
      to change. Whether a pod offers a shell is per pod, on the pod's page
      (`DetailPage.shell`), which `resourceDetailQuery` re-reads on the same
      minute under `shared`. Derived from the answer, because the first read is
      what says which profile this is.
    */
    staleTime: (query) => (isShared(query.state.data) ? SHARED_CAPABILITIES_MS : Infinity),
    refetchInterval: (query) => (isShared(query.state.data) ? SHARED_CAPABILITIES_MS : false),
  });
}

export function clustersQuery() {
  return queryOptions({
    queryKey: queryKeys.clusters,
    queryFn: ({ signal }) => listClusters(signal),
    // Per-query, deliberately: each entry carries its own reachability, so this
    // list going stale is a rail that says a cluster is up after it went down.
    // 30 seconds and not a list's two - a rail is glanced at, not watched.
    refetchInterval: 30_000,
  });
}

export function preferencesQuery() {
  return queryOptions({
    queryKey: queryKeys.preferences,
    queryFn: ({ signal }) => getPreferences(signal),
  });
}

/**
 * What this process is forwarding, for the panel whose row is also the control.
 *
 * Read on mount by every Ports panel so a forward opened from another drawer, or
 * before a reload, is drawn beside its port and is stoppable there - `closeForward`
 * was written and nothing called it, so a forward outlived the drawer and the tab
 * until the process exited, and the cap is per process, so stopping is also
 * what frees a slot.
 */
export function forwardsQuery(enabled = true) {
  return queryOptions({
    queryKey: queryKeys.forwards,
    queryFn: ({ signal }) => listForwards(signal),
    enabled,
  });
}

/**
 * How long the menu is believed before it is asked again: the ten seconds the
 * server holds an access review it could not make (`access.FailedTTL` in
 * internal/access), and no longer.
 *
 * The menu carries the extras the person may use, reviewed on the server and
 * held there; a menu kept longer here than the server keeps its review shows a
 * grant, a revocation or a review that came back late by the difference.
 * `TestTheMenuIsKeptNoLongerThanTheServerKeepsItsReview` in internal/web reads
 * this declaration and holds it to `access.FailedTTL`: neither language can
 * import the other's constant. Discovery under the menu is held by the server
 * (the catalog), so a re-read here costs no cluster read.
 */
export const MENU_FRESH_MS = 10_000;

/** How soon the menu asks again while discovery is still running, and after it
 *  failed: the first is a wait on the cluster, the second a retry. */
export const MENU_DISCOVERY_PENDING_MS = 2_000;
export const MENU_DISCOVERY_FAILED_MS = 30_000;

/** The poll an answer asks for: none once discovery is complete. */
export function navRefetchInterval(data: ValueResult<NavMenu> | undefined): number | false {
  if (data?.state !== 'ok') return false;
  switch (data.data.discovery?.state) {
    case 'pending':
      return MENU_DISCOVERY_PENDING_MS;
    case 'failed':
      return MENU_DISCOVERY_FAILED_MS;
    default:
      return false;
  }
}

export function navQuery(cluster: string) {
  return queryOptions({
    queryKey: queryKeys.nav(cluster),
    queryFn: ({ signal }) => getNav(cluster, signal),
    staleTime: MENU_FRESH_MS,
    refetchInterval: (query) => navRefetchInterval(query.state.data),
  });
}

/**
 * Which kinds of one cluster's menu hold an object the person may see.
 * The server holds its reads for three minutes and
 * answers a narrowed person within bounds; a minute here keeps the column from
 * asking on every page, and a kind's first object reaches it within a few.
 */
export function navOccupancyQuery(cluster: string) {
  return queryOptions({
    queryKey: queryKeys.navOccupancy(cluster),
    queryFn: ({ signal }) => getNavOccupancy(cluster, signal),
    staleTime: 60_000,
  });
}

/** The person's choices about the kinds column, read only where the server
 *  keeps them (`useKeepsKindPreferences`). Changed only by this tab's own
 *  writes, which set it. */
export function kindPreferencesQuery() {
  return queryOptions({
    queryKey: queryKeys.kindPreferences,
    queryFn: ({ signal }) => getKindPreferences(signal),
    staleTime: Infinity,
  });
}

/**
 * One deferred menu group, asked for only when its disclosure is open.
 *
 * `enabled` is the whole mechanism and it is the caller's, because only the menu
 * knows whether the operator has opened the heading. Filling it lists the
 * cluster's CustomResourceDefinitions - 23.7 MB and 603-668 ms on one production
 * cluster - which is why the server answers `getNav` without it in the first
 * place, and why asking for it here on every page would put that cost straight
 * back.
 *
 * Kept for five minutes rather than the menu's one. A CRD is installed by an
 * operator running `helm install`, not by the cluster doing its work, so the list
 * is near-static - and this is the read whose cost the whole arrangement exists
 * to avoid paying twice. A newly installed kind appears on the next reload.
 *
 * **`snapshot` is the answer this browser was last given, and it outlives the
 * tab.** Five minutes in this cache alone would be five minutes per tab lifetime:
 * F5 would forget the answer and the next opening pay the full read again. The
 * snapshot is handed in as `initialData`, stamped with
 * `initialDataUpdatedAt`, so the heading is drawn from it at once and **the same
 * `staleTime` decides whether to ask again** - a snapshot older than five
 * minutes is revalidated in the background on the next opening, a younger one is
 * not asked about at all. Where it lives, what is written and why only an `ok`
 * answer is, is `shell/navMemory.ts`'s; this file only knows the shape.
 */
export function navGroupQuery(
  cluster: string,
  group: string,
  enabled: boolean,
  snapshot?: NavGroupSnapshot,
) {
  return queryOptions({
    queryKey: queryKeys.navGroup(cluster, group),
    queryFn: ({ signal }) => getNavGroup(cluster, group, signal),
    staleTime: 300_000,
    enabled,
    // A function, so the query's data type stays `ValueResult<NavGroup>` and
    // not that-or-undefined: TanStack types a plain `undefined` here as the
    // data being optional everywhere it is read.
    initialData: (): ValueResult<NavGroup> | undefined =>
      snapshot === undefined ? undefined : { state: 'ok', data: snapshot.group },
    initialDataUpdatedAt: () => snapshot?.at,
  });
}

/** A deferred group's last `ok` answer and when it was received, as
 *  `navGroupQuery` seeds itself from it. `at` is the query's own
 *  `dataUpdatedAt`, so the store and the cache agree on the answer's age. */
export interface NavGroupSnapshot {
  readonly at: number;
  readonly group: NavGroup;
}

/**
 * The namespaces of one cluster.
 *
 * Kept for a minute, deliberately. It is a cluster read behind a control that is
 * opened and closed, and namespaces are created rarely — a list that was right a
 * minute ago is right enough to pick from, and a list page rendered every two
 * seconds costs one listing per hold rather than one per render.
 */
export function namespacesQuery(cluster: string) {
  return queryOptions({
    queryKey: queryKeys.namespaces(cluster),
    queryFn: ({ signal }) => listNamespaces(cluster, signal),
    staleTime: 60_000,
  });
}


export function useSession(): UseQueryResult<Session> {
  return useQuery(sessionQuery());
}

export function useCapabilities(): UseQueryResult<Capabilities> {
  return useQuery(capabilitiesQuery());
}

/**
 * Whether the server has said this console offers **no way to sign in**:
 * neither the shared password nor an identity provider (`SignInMethods`). That
 * is the loopback desktop, where the window arrives
 * signed in from the application, and nowhere else - `KT_PASSWORD_HASH` on a
 * desktop offers a password again, and any configured provider, answering or
 * not, is a way in.
 *
 * One predicate, read by the sign-in view (which then says to open KubeTower
 * again) and by the session controls (which then draw no Log out: there is
 * nothing to come back to). Two copies of it would disagree on the edit that
 * matters. Nothing said yet is `false`.
 */
export function opensFromTheApp(methods: SignInMethods | undefined): boolean {
  return methods !== undefined && !methods.password && methods.sso.state === 'off';
}

/**
 * Whether a person who signs out here can sign back in, which is what decides
 * whether Log out and the identity beside it are drawn at all: on the
 * desktop they would be of no use.
 *
 * `undefined` until the server has answered, so a desktop does not draw the
 * tile for a moment and take it away. **A failed read is `true`**: an older
 * server has no `/api/auth/methods` and does have a sign-in page, and a console
 * with no way out is the worse of the two mistakes.
 *
 * **Asked once, without the sign-in view's poll.** That view re-asks every five
 * seconds while a provider is `pending` or `unavailable`, to say when it is
 * back; here every one of those states is "a way in", so the answer this reads
 * cannot change while the process runs, and a shell left open on a console
 * whose provider is down would otherwise ask twelve times a minute for ever.
 * Both options are the observer's, so the sign-in view keeps its own.
 *
 * **Except after a failure that may pass.** A read that
 * never reached the server, or that a 5xx answered - the process behind the
 * desktop's window restarting, say - is asked again every
 * `SIGN_IN_METHODS_RETRY_MS`, `SIGN_IN_METHODS_RETRIES` asks in all, the first
 * one included. Without it the loopback desktop would draw Log out for the rest
 * of the window's life on one lost request, and pressing it would land on "Open
 * KubeTower again". Log out stays drawn while it re-asks,
 * and after the last try: failing open is the rule above. A 4xx is an answer -
 * an older server's 404 - and is not re-asked.
 */
export function useSignsBackIn(): boolean | undefined {
  const methods = useQuery({
    ...signInMethodsQuery(),
    refetchInterval: (query) =>
      query.state.status === 'error' &&
      mayPass(query.state.error) &&
      query.state.errorUpdateCount < SIGN_IN_METHODS_RETRIES
        ? SIGN_IN_METHODS_RETRY_MS
        : false,
    staleTime: Infinity,
  });
  if (methods.isError) return true;
  if (methods.data === undefined) return undefined;
  return !opensFromTheApp(methods.data);
}

/** How often `useSignsBackIn` asks after a failure that may pass, and how many
 *  asks in all, the first included: a minute, long enough for a desktop
 *  process to restart. */
const SIGN_IN_METHODS_RETRY_MS = 5_000;
const SIGN_IN_METHODS_RETRIES = 12;

/** A failure that is not an answer: no status at all, or the server's 5xx.
 *  The global `retry`'s rule (`query/client.ts`), so the two cannot disagree:
 *  anything that is not an `ApiError` - a body that did not parse, a bug - is
 *  final, and a lost request is an `ApiError` with status 0 (`request.ts`). */
function mayPass(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 0 || error.status >= 500);
}

export function useClusters(): UseQueryResult<ListResult<Cluster>> {
  return useQuery(clustersQuery());
}

/**
 * Whether cluster is read-only, as an extension's tab is told it
 * (`ExtensionTabProps.readOnly`, #157): undefined until the clusters are
 * read; a list that could not be read, or a cluster missing from it, is
 * false.
 */
export function useClusterReadOnly(cluster: string): boolean | undefined {
  const clusters = useClusters();
  if (clusters.data?.state === 'ok') return clusters.data.items.find((c) => c.name === cluster)?.readOnly === true;
  return clusters.isError || clusters.data !== undefined ? false : undefined;
}

/**
 * Whether this console keeps what a person arranges - the rail, the column
 * layouts, the density. **False only where the server says so**: under the
 * server profile, where what it kept would be everybody's, and where it
 * runs as several replicas behind one address. There the preference writes are
 * not registered at all, so the rail draws no arrangement and the tables keep
 * each person's columns in this browser (`columnMemory`). Unknown - the
 * capabilities not answered yet, or a server older than the member - is `true`,
 * which is what every console before this member did.
 */
export function useKeepsPreferences(): boolean {
  return useCapabilities().data?.preferences !== false;
}

/**
 * Whether this console runs as several replicas behind one address, where the
 * settings writes are not registered. Not read off `preferences`, which the
 * server profile also turns off on one replica, where the settings writes
 * stay. Unknown is `false`, one replica.
 */
export function useRunsAsReplicas(): boolean {
  return useCapabilities().data?.replicated === true;
}

/**
 * Why this console does not keep its settings here, or `null` where it does.
 * `replicas`: several replicas share one address, and each write is a file in
 * one pod. `shared`: the server profile (`mode: shared`), where the archive's
 * settings and emptying it are every person's, so the server registers none of
 * them on one replica either. Unknown is `null`, kept: what every
 * console before these members drew.
 */
export function useSettingsNotKept(): 'replicas' | 'shared' | null {
  const data = useCapabilities().data;
  if (data?.replicated === true) return 'replicas';
  if (isShared(data)) return 'shared';
  return null;
}

/**
 * Whether the server keeps each person's choices about the kinds column.
 * **Unknown is `false`**: a server older than the member,
 * or not answered yet, keeps nothing, and the browser does.
 */
export function useKeepsKindPreferences(): boolean {
  return useCapabilities().data?.kindPreferences === true;
}

/** `enabled` false withholds the read, as `useNav`'s does. */
export function useNavOccupancy(
  cluster: string | undefined,
  enabled = true,
): UseQueryResult<ValueResult<NavOccupancy>> {
  return useQuery({ ...navOccupancyQuery(cluster ?? ''), enabled: enabled && cluster !== undefined });
}

/** The server's copy of the choices; asked only where it keeps them. */
export function useKindPreferences(enabled = true): UseQueryResult<KindPreferences> {
  return useQuery({ ...kindPreferencesQuery(), enabled });
}

/**
 * Replace the choices on the server. Drawn at once from what was sent, then
 * from what the server kept; a refusal re-reads the server's copy, so the
 * column never keeps drawing a choice that was not kept.
 */
export function useSaveKindPreferences(): UseMutationResult<
  KindPreferences,
  Error,
  KindPreferences,
  { previous: KindPreferences | undefined }
> {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (prefs: KindPreferences) => setKindPreferences(prefs),
    onMutate: async (prefs) => {
      await client.cancelQueries({ queryKey: queryKeys.kindPreferences });
      const previous = client.getQueryData<KindPreferences>(queryKeys.kindPreferences);
      client.setQueryData(queryKeys.kindPreferences, prefs);
      return { previous };
    },
    onSuccess: (kept) => {
      client.setQueryData(queryKeys.kindPreferences, kept);
    },
    onError: () => {
      void client.invalidateQueries({ queryKey: queryKeys.kindPreferences });
    },
  });
}

export function usePreferences(): UseQueryResult<Preferences> {
  return useQuery(preferencesQuery());
}

/** `enabled` false withholds the read: a cluster the probe found down moments
 *  ago is not asked for its menu. */
export function useNav(cluster: string | undefined, enabled = true): UseQueryResult<ValueResult<NavMenu>> {
  return useQuery({ ...navQuery(cluster ?? ''), enabled: enabled && cluster !== undefined });
}

/**
 * The extensions on one cluster, as the Settings page's Extra tab and a
 * setting-gated list column read them. **No poll and no staleness**: it is the
 * console's own file, which only a switch on this page changes, and the switch
 * invalidates it (`queryKeys.extensions`). Under the server profile the
 * deployment's `KT_EXTRAS` decides, read once at start, so a new answer is a new
 * pod - and a new pod is a new page load.
 */
export function extensionsQuery(cluster: string) {
  return queryOptions({
    queryKey: queryKeys.extensions(cluster),
    queryFn: ({ signal }) => listExtensions(cluster, signal),
    staleTime: Infinity,
  });
}

export function useExtensions(cluster: string | undefined, enabled = true): UseQueryResult<ListResult<ExtensionInfo>> {
  return useQuery({ ...extensionsQuery(cluster ?? ''), enabled: enabled && cluster !== undefined });
}

/** The contents of a deferred group. `open` is the disclosure's state: a group
 *  nobody has opened is not read, which is the point of it being deferred.
 *  `snapshot` is what this browser was last answered, if anything - see
 *  `navGroupQuery`. */
export function useNavGroup(
  cluster: string | undefined,
  group: string,
  open: boolean,
  snapshot?: NavGroupSnapshot,
): UseQueryResult<ValueResult<NavGroup>> {
  return useQuery(navGroupQuery(cluster ?? '', group, open && cluster !== undefined, snapshot));
}

/** What the deferred groups already hold, without asking for any of them: the
 *  cache's answer, or the browser's snapshot, per group and in order; `undefined`
 *  where nothing `ok` is in hand. The jump box counts its matches from this, so
 *  the observers are disabled - reading must never be the fetch that opening
 *  the heading is. Each carries the same snapshot `useNavGroup` does, because
 *  whichever observer creates the cache entry seeds it. */
export function useNavGroupsInHand(
  cluster: string,
  groups: readonly { readonly key: string; readonly snapshot: NavGroupSnapshot | undefined }[],
): readonly (NavGroup | undefined)[] {
  return useQueries({
    queries: groups.map(({ key, snapshot }) => navGroupQuery(cluster, key, false, snapshot)),
    combine: (results) => results.map((result) => (result.data?.state === 'ok' ? result.data.data : undefined)),
  });
}

export function useNamespaces(
  cluster: string | undefined,
): UseQueryResult<ListResult<Namespace>> {
  return useQuery({ ...namespacesQuery(cluster ?? ''), enabled: cluster !== undefined });
}


// ---- Resources ----------------------------------------------------------

/**
 * One kind's list, keyed by everything that changes the request.
 *
 * The key carries the namespace and the search term **because they are part of
 * the question**, not because they are convenient: a cache entry shared between
 * "pods in prod" and "pods everywhere" is a page that shows one and claims the
 * other. `selector` is in the key for the same reason.
 */
/** The fastest of `kinds.go`'s three bands, used only until the answer says
 *  which band this kind is in. Being wrong in this direction costs one extra
 *  read of a slow kind; the other direction leaves a live one a minute stale. */
const LIVE_SECONDS = 2;

/** One canonical string for a set of filters: order does not change the
 *  question, nor does case (the server compares both halves without it), and
 *  the separator is one no value can hold. */
function filtersKey(filters: readonly string[]): string {
  return [...filters].map((one) => one.toLowerCase()).sort().join('\n');
}

export function resourceListQuery(cluster: string, kind: string, params: ListResourcesParams = {}) {
  /*
    The selection, canonicalized into the key.

    Sorted and joined, because the key has to answer "is this the same question"
    and `[b, a]` is the same question as `[a, b]`. `parseNamespaces` canonicalizes
    on the server for exactly this reason — "two URLs naming the same two
    namespaces in different orders are the same page, and without this they would
    be two different cache keys" — and a client that did not would hold two
    entries for one answer and refetch on every reorder of a selection.
  */
  const namespace = [...(params.namespace ?? [])].sort().join(',');
  const selector = params.selector ?? '';
  const search = params.search ?? '';
  // The filters, canonicalized for the namespace's reason: the same choices in
  // another order are the same question.
  const filter = filtersKey(params.filter ?? []);
  return queryOptions({
    queryKey: queryKeys.resourceList(cluster, kind, namespace, selector, search, filter),
    queryFn: ({ signal }) => listResources(cluster, kind, params, signal),
    /*
      **The interval is the kind's, and it is read out of the answer.**

      `internal/resources/kinds.go` bands it — 2s live, 5s reconciling, 60s
      declared — and `ListPage.refreshSeconds` carries the kind's own figure.
      Nothing looks wrong at a uniform two seconds, which is why it matters: a
      Secrets list at that rate pushes megabytes of plaintext through the
      process every minute to render a Keys column.

      A function and not a number, because the first fetch has no answer to read
      it from — TanStack calls this with the query, so the value is re-derived
      after every read and a kind whose band changes is honored on the next tick
      rather than at the next navigation. Two seconds until there is an answer,
      which is the fastest band and therefore the safe way to be wrong: it costs
      one extra read of a slow kind, where the other direction leaves a live kind
      a minute stale.

      Keeping the refresh at all is also the fastest way an expired session
      is discovered — which is why `query/client.ts` routes
      a 401 from anywhere rather than each page handling its own.
    */
    refetchInterval: (query) => listRefreshMs(query.state.data),
  });
}

/**
 * How long until a list is asked again, or `false` for not until something
 * else changes. The answer's own band where it gives one, two seconds
 * otherwise - except the console's own ceiling, `not_supported_here` and not
 * retryable (a list too large to narrow to the person's namespaces):
 * it is the same answer until the selection changes, and each ask repeats the
 * whole read behind it, so it is asked once.
 */
export function listRefreshMs(answer: ValueResult<ListPage> | undefined): number | false {
  if (answer !== undefined && answer.state !== 'ok' && answer.reason.code === 'not_supported_here' &&
    answer.reason.retryable !== true) {
    return false;
  }
  const seconds = answer !== undefined && answer.state === 'ok' ? answer.data.refreshSeconds : undefined;
  return (seconds === undefined || seconds <= 0 ? LIVE_SECONDS : seconds) * 1_000;
}

/**
 * Whether two list keys ask about **the same list** - one cluster, one kind, one
 * namespace selection - and differ at most in how it is narrowed.
 *
 * The four leading members of `queryKeys.resourceList`, read by position because
 * that is the key's shape: `['resources', cluster, kind, namespace, selector,
 * search, filter]`. A key of any other shape is never the same list. A filter
 * chosen in the filter control narrows the same list, as a search does.
 */
export function sameListAsked(one: QueryKey, other: QueryKey): boolean {
  if (one.length !== 7 || other.length !== 7) return false;
  return one[0] === 'resources' && other[0] === 'resources' &&
    one[1] === other[1] && one[2] === other[2] && one[3] === other[3];
}

export function useResourceList(
  cluster: string | undefined,
  kind: string | undefined,
  params: ListResourcesParams = {},
): UseQueryResult<ValueResult<ListPage>> {
  const options = resourceListQuery(cluster ?? '', kind ?? '', params);
  return useQuery({
    ...options,
    enabled: cluster !== undefined && kind !== undefined,
    /*
      **The previous rows stay on screen while only the filter changed.**

      The filter is part of the key and a new key starts empty, so without this
      the table would drop to its waiting state and redraw once per settled
      word while typing in a search box - a reload, as far as anybody watching
      could tell.

      `router/prefetch.ts` refuses a placeholder across a change of list, and
      that refusal stands: the previous *kind's* rows under a new heading would
      draw pods under *Ingresses*. A new search or selector on the same cluster,
      kind and namespaces is not a change of list. The rows on screen are a
      superset or a sibling of what is coming, under the right heading, and
      they stay - as the last true answer, with `isPlaceholderData` set, which
      is what the page reads to mark the box *filtering* until the answer for
      what was typed arrives. The metrics panel's span change is the same
      argument, `useMetricsPanel` below.

      **Decided against the query that actually answered, not the one before.**
      TanStack hands this the last query *that had data*, so typing `ngin` and
      then `nginx-a` before `ngin` answers keeps the unfiltered rows, and moving
      to another kind and typing before it answers keeps nothing: that kind has
      not answered, and the one that did is not this list.

      **Nothing is added to the cache by this.** A placeholder lives on the
      observer and is never written under the new key, so the cache holds one
      entry per settled filter, each collected `gcTime` after its last observer
      leaves.

      **A new function on every render, and that is load-bearing.** TanStack
      re-uses a placeholder it is already showing, without asking again,
      whenever `placeholderData` is the *same* function as last render. Hoisted
      or memoized, this would keep showing pods that were standing in for a
      filter after the person moved to Ingresses - the one thing it exists to
      refuse. `ResourceList.test.tsx` moves kind while a placeholder is up.
    */
    placeholderData: (previous, previousQuery) =>
      previousQuery !== undefined && sameListAsked(previousQuery.queryKey, options.queryKey)
        ? previous
        : undefined,
  });
}

/**
 * One object's detail, for the drawer and for the object's own route.
 *
 * **One query for both**, which is the whole reason this is not two. The route
 * and the drawer are two ways to the same object, and two query keys would
 * be two answers a second apart — a drawer saying `Running` beside a page saying
 * `Terminating`, with no way to tell which is older.
 *
 * No `refetchInterval`. The list refreshes every two seconds because a list is a
 * live view of a cluster; a drawer is a thing being read, and re-reading the
 * object under a reader's eyes is deliberately not done: the table keeps
 * refreshing behind it, and the panel does not.
 *
 * **Except under the shared profile, where the page carries an answer about the
 * person and not only about the object.** `actions` is which verbs the person
 * behind the tab may perform on this object, and `shell` whether they may open
 * a shell in this pod - the server asks the cluster, for the person and for the
 * console's own ServiceAccount, and holds the answer a minute - so a grant
 * given or revoked changes the page without the object changing at all.
 * `shared` re-reads on that same minute, which is what brings a newly granted
 * Delete onto an open page without a reload, and takes a revoked one away. The
 * desktop answers about the kubeconfig's own credentials and is not re-read.
 */
export function resourceDetailQuery(
  cluster: string,
  kind: string,
  namespace: string | undefined,
  name: string,
  shared = false,
) {
  return queryOptions({
    queryKey: queryKeys.resource(cluster, kind, namespace ?? '', name),
    queryFn: ({ signal }) => getResource(cluster, kind, namespace, name, signal),
    // **An object being deleted is read again until it is gone.** Nothing else
    // re-reads one object, so a pod in its grace period, or anything a
    // finalizer holds, would otherwise stay on screen as it was - "Running",
    // its verbs drawn - long after the list has dropped it.
    refetchInterval: (query) => {
      const answer = query.state.data;
      if (answer?.state === 'ok' && answer.data.deleting === true) return DELETING_REREAD_MS;
      return shared ? SHARED_CAPABILITIES_MS : false;
    },
  });
}

/** How often an object being deleted is read again: the list's own pace. */
export const DELETING_REREAD_MS = 2_000;

/**
 * One object's manifest.
 *
 * Its own query and not a member of the detail page's, because it is a second
 * read of the same object and the drawer opens without it: the manifest is a tab
 * somebody chooses, and fetching a hundred lines of YAML for every row a person
 * clicks is a cost paid for the tab they did not open.
 *
 * No `refetchInterval`, for `resourceDetailQuery`'s reason and one more of its
 * own: this document is what the editor diffs against, and re-reading it under a
 * reader's eyes would move the base of an edit they are part-way through
 * writing.
 */
export function manifestQuery(
  cluster: string,
  kind: string,
  namespace: string | undefined,
  name: string,
) {
  return queryOptions({
    queryKey: queryKeys.manifest(cluster, kind, namespace ?? '', name),
    queryFn: ({ signal }) => getManifest(cluster, kind, namespace, name, signal),
  });
}

export function useManifest(
  cluster: string | undefined,
  kind: string | undefined,
  namespace: string | undefined,
  name: string | undefined,
  enabled = true,
): UseQueryResult<ValueResult<ManifestPanel>> {
  return useQuery({
    ...manifestQuery(cluster ?? '', kind ?? '', namespace, name ?? ''),
    // `enabled` is the tab, not the route: the panel is mounted only when the
    // operator picks it, and a query that ran anyway would defeat the reason it
    // is a separate read.
    enabled: enabled && cluster !== undefined && kind !== undefined && name !== undefined,
  });
}

/**
 * The template the list's "+" opens, read only once it is pressed: `enabled` is
 * the dialog, as it is the tab for `useManifest`.
 */
export function useCreateTemplate(
  cluster: string | undefined,
  kind: string | undefined,
  namespace: string | undefined,
  enabled: boolean,
): UseQueryResult<ValueResult<CreateTemplate>> {
  return useQuery({
    queryKey: queryKeys.createTemplate(cluster ?? '', kind ?? '', namespace ?? ''),
    queryFn: ({ signal }) => getCreateTemplate(cluster ?? '', kind ?? '', namespace, signal),
    enabled: enabled && cluster !== undefined && kind !== undefined,
  });
}

/**
 * An operator an extra page reads from, as the console would install it. Read
 * once, with no interval: a page reads it again on demand, after an install.
 */
export function prereqQuery(cluster: string, key: string) {
  return queryOptions<ValueResult<Prereq>>({
    queryKey: queryKeys.prereq(cluster, key),
    queryFn: ({ signal }) => getPrereq(cluster, key, signal),
  });
}

export function dataQuery(
  cluster: string,
  kind: string,
  namespace: string | undefined,
  name: string,
) {
  return queryOptions({
    queryKey: queryKeys.data(cluster, kind, namespace ?? '', name),
    queryFn: ({ signal }) => getDataPanel(cluster, kind, namespace, name, signal),
  });
}

export function useDataPanel(
  cluster: string | undefined,
  kind: string | undefined,
  namespace: string | undefined,
  name: string | undefined,
  enabled: boolean,
): UseQueryResult<ValueResult<DataPanel>> {
  return useQuery({
    ...dataQuery(cluster ?? '', kind ?? '', namespace, name ?? ''),
    enabled: enabled && cluster !== undefined && kind !== undefined && name !== undefined,
  });
}

export function metricsQuery(
  cluster: string,
  kind: string,
  namespace: string | undefined,
  name: string,
  window: { range?: string; points?: number },
) {
  return queryOptions({
    queryKey: queryKeys.metrics(cluster, kind, namespace ?? '', name, window.range ?? ''),
    queryFn: ({ signal }) => getMetricsPanel(cluster, kind, namespace, name, window, signal),
  });
}

export function useMetricsPanel(
  cluster: string | undefined,
  kind: string | undefined,
  namespace: string | undefined,
  name: string | undefined,
  window: { range?: string; points?: number },
  enabled: boolean,
): UseQueryResult<ValueResult<MetricsPanel>> {
  return useQuery({
    ...metricsQuery(cluster ?? '', kind ?? '', namespace, name ?? '', window),
    // The tab, not the route - the same argument `useManifest` makes. Two
    // Prometheus range queries over a week take a second or more, and through a
    // port-forwarded tunnel the tunnel is opened first: every pod anybody
    // clicked would pay that before showing a name.
    enabled: enabled && cluster !== undefined && kind !== undefined && name !== undefined,
    // The previous window's chart stays on screen while the next one loads.
    //
    // The second or more is Prometheus's and does not move; dropping the
    // whole panel to its waiting state for that second would be slowness in
    // the shape that looks broken.
    //
    // `router/prefetch.ts` refuses `placeholderData` for lists, and the reason
    // it gives is the reason this is allowed here: keeping the previous kind's
    // rows would draw pods under a heading that says ingresses. A metrics panel
    // holds the same object and the same measurement; only the span changes,
    // and `isPlaceholderData` is what the span row reads to say *settling* on
    // the button that was pressed. The old chart is on screen as the last true
    // answer, marked as not yet the new one - not as the new one.
    //
    // It also keeps `Panel` mounted across the refetch, so the measurement the
    // operator picked survives a span change rather than snapping back to CPU
    // with the remount.
    placeholderData: keepPreviousData,
  });
}

export function changesQuery(
  cluster: string,
  kind: string,
  namespace: string | undefined,
  name: string,
) {
  return queryOptions({
    queryKey: queryKeys.changes(cluster, kind, namespace ?? '', name),
    queryFn: ({ signal }) => getChangesPanel(cluster, kind, namespace, name, signal),
  });
}

export function useChangesPanel(
  cluster: string | undefined,
  kind: string | undefined,
  namespace: string | undefined,
  name: string | undefined,
  enabled: boolean,
): UseQueryResult<ValueResult<ChangesPanel>> {
  return useQuery({
    ...changesQuery(cluster ?? '', kind ?? '', namespace, name ?? ''),
    enabled: enabled && cluster !== undefined && kind !== undefined && name !== undefined,
  });
}

export function useResourceDetail(
  cluster: string | undefined,
  kind: string | undefined,
  namespace: string | undefined,
  name: string | undefined,
): UseQueryResult<ValueResult<DetailPage>> {
  const shared = isShared(useCapabilities().data);
  return useQuery({
    ...resourceDetailQuery(cluster ?? '', kind ?? '', namespace, name ?? '', shared),
    enabled: cluster !== undefined && kind !== undefined && name !== undefined,
  });
}
