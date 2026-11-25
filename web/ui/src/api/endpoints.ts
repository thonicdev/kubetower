import { asList, asValue, type ListResult, type ValueResult } from './envelope';
import { ApiError } from './errors';
import { request } from './request';
import type { paths } from './schema';
import type {
  ActOutcome,
  Capabilities,
  Cluster,
  ConfirmationPlan,
  CreateTemplate,
  PlanTarget,
  Prereq,
  Container,
  DetailPage,
  EnvSource,
  EnvValue,
  ExtensionInfo,
  ExtensionSuggestion,
  Forward,
  ForwardRequest,
  ForwardWindowRequest,
  ListPage,
  LogPage,
  LoginRequest,
  ManifestPanel,
  DataPanel,
  DataWrite,
  MetricsPanel,
  ChangesPanel,
  ManifestWrite,
  Namespace,
  NavGroup,
  KindPreferences,
  NavMenu,
  NavOccupancy,
  Preferences,
  RailNameplateWrite,
  Session,
  SignInMethods,
} from './types';

/**
 * Every address this SPA knows, in one file.
 *
 * ## The template is checked against the contract
 *
 * `ApiPath` is `keyof paths` from the generated `schema.ts`, so every template
 * below is a string `api/openapi.yaml` actually defines. Rename an operation's
 * path there, regenerate, and `pnpm run typecheck` fails here rather than the
 * console 404-ing at runtime - which is the whole reason the paths are written
 * as templates and substituted instead of being assembled with `+`.
 *
 * ## Every parameter is encoded
 *
 * A context name is whatever a `KUBECONFIG` says it is. `api/openapi.yaml`
 * already reserves a one-letter segment for exactly this reason - "a segment
 * the cluster names never sits beside a segment this console names" - and the
 * encoding here is the client half of it.
 */
type ApiPath = keyof paths & string;

function url<T extends ApiPath>(template: T, params: Readonly<Record<string, string>> = {}): string {
  return template.replace(/\{(\w+)\}/g, (_match, name: string) => {
    const value = params[name];
    if (value === undefined) throw new Error(`${template}: no value for {${name}}`);
    return encodeURIComponent(value);
  });
}

// ---- Session ------------------------------------------------------------

/**
 * Exchange a credential for a session cookie.
 *
 * 204 and no body: `api/openapi.yaml` is explicit that no token is returned and
 * none is readable by the browser, so there is nothing here to store and
 * nothing this function can hand back. What signals success is that it did not
 * throw.
 */
export function login(body: LoginRequest, signal?: AbortSignal): Promise<void> {
  return request<void>(url('/api/auth/login'), { method: 'POST', body, ...(signal ? { signal } : {}) });
}

/** Answers 204 whether or not a session was live, so a front end with a lapsed
 *  session can still reach its own sign-in screen. */
export function logout(): Promise<void> {
  return request<void>(url('/api/auth/logout'), { method: 'POST' });
}

/**
 * How a person may sign in here, asked before anybody has. Public on the
 * server, and `no-store`: the identity provider's state changes while the
 * sign-in view is open.
 */
export function getSignInMethods(signal?: AbortSignal): Promise<SignInMethods> {
  return request<SignInMethods>(url('/api/auth/methods'), signal ? { signal } : {});
}

/**
 * Where "Sign in with SSO" goes: an address, **not a request**.
 *
 * The flow is a navigation - the browser leaves for the identity provider and
 * comes back to the callback - so it is an anchor's `href` and nothing in this
 * module fetches it. A fetch would follow the redirect into another origin,
 * which the Content-Security-Policy refuses, and there would be no page for the
 * provider to draw its sign-in on anyway. The path is still built from the
 * contract, so a rename there fails the typecheck here.
 */
export function singleSignOnHref(next: string): string {
  return `${url('/api/auth/oidc/start')}?${new URLSearchParams({ next }).toString()}`;
}

export function getSession(signal?: AbortSignal): Promise<Session> {
  return request<Session>(url('/api/session'), signal ? { signal } : {});
}

export function getCapabilities(signal?: AbortSignal): Promise<Capabilities> {
  return request<Capabilities>(url('/api/capabilities'), signal ? { signal } : {});
}

// ---- Clusters, navigation, extensions -----------------------------------

export async function listClusters(signal?: AbortSignal): Promise<ListResult<Cluster>> {
  return asList(await request(url('/api/clusters'), signal ? { signal } : {}));
}

/**
 * One cluster's namespaces, for the selector every list carries.
 *
 * A **list** result and not a value one, and the four arms are the point: a
 * cluster with nothing in it is `empty`, one that could not be asked is
 * `unreadable` or `forbidden`, one nobody has asked yet is `pending`, and a list
 * kept from before the cluster stopped answering is `ok` carrying
 * `reason.code: stale_reading`. `api/openapi.yaml` notes that a real cluster
 * always has `default` and `kube-system`, so "no namespaces" is nearly always a
 * failure wearing an empty answer's clothes — which is why it may not be drawn
 * as one.
 */
export async function listNamespaces(
  cluster: string,
  signal?: AbortSignal,
): Promise<ListResult<Namespace>> {
  return asList(
    await request(url('/api/c/{cluster}/namespaces', { cluster }), signal ? { signal } : {}),
  );
}

/**
 * The extensions this build carries, each with its state, its words and its
 * settings on one cluster - read by the Settings page's Extra tab, and by
 * a list whose extension column is gated on a setting (`ExtensionColumn.setting`).
 * Not by the menu, which reads what is on from `getNav`.
 */
export async function listExtensions(
  cluster: string,
  signal?: AbortSignal,
): Promise<ListResult<ExtensionInfo>> {
  return asList(
    await request(url('/api/c/{cluster}/extensions', { cluster }), signal ? { signal } : {}),
  );
}

/**
 * The extensions that are off on one cluster and that it gives a reason to turn
 * on, each with its own sentence of evidence - the banner above the page.
 * Registered exactly where `setExtensionEnabled` is, since
 * what it offers is that switch; elsewhere it answers 404.
 */
export async function listExtensionSuggestions(
  cluster: string,
  signal?: AbortSignal,
): Promise<ListResult<ExtensionSuggestion>> {
  return asList(
    await request(url('/api/c/{cluster}/extensions/suggestions', { cluster }), signal ? { signal } : {}),
  );
}

/**
 * Ask one cluster again, now, and answer its entry as `listClusters` draws it -
 * the overview's Refresh on a cluster the probe found down.
 * A POST, so it carries the mutation marker like every other non-GET; slow by
 * construction against a cluster that is down, which is why the caller shows
 * that it is working.
 */
export async function probeCluster(cluster: string): Promise<ValueResult<Cluster>> {
  return asValue(await request(url('/api/c/{cluster}/probe', { cluster }), { method: 'POST' }));
}

export async function getNav(cluster: string, signal?: AbortSignal): Promise<ValueResult<NavMenu>> {
  return asValue(await request(url('/api/c/{cluster}/nav', { cluster }), signal ? { signal } : {}));
}

/**
 * One menu group `getNav` answered with `deferred: true`, filled.
 *
 * A **value** result: the group is a single thing, and a cluster serving no
 * custom resource kinds is `ok` with an empty `items` and a `note` saying so,
 * never `empty`. That distinction is the contract's and it is load-bearing here -
 * a heading that drew "serves none" and "could not be listed" the same way is the
 * founding defect wearing a navigation's clothes.
 */
export async function getNavGroup(
  cluster: string,
  group: string,
  signal?: AbortSignal,
): Promise<ValueResult<NavGroup>> {
  return asValue(
    await request(url('/api/c/{cluster}/nav/{group}', { cluster, group }), signal ? { signal } : {}),
  );
}

/**
 * Which kinds of the menu hold an object the person may see,
 * keyed by slug - `kindOfHref` of the entry's href. A value result: a cluster
 * that could not be asked is the envelope's failure arm. **`unknown`, and a kind
 * the answer does not name, is never hidden**: an unknown count is not zero.
 */
export async function getNavOccupancy(cluster: string, signal?: AbortSignal): Promise<ValueResult<NavOccupancy>> {
  return asValue(await request(url('/api/c/{cluster}/nav/occupancy', { cluster }), signal ? { signal } : {}));
}

/**
 * The person's choices about the kinds column, kept on the server only where
 * `Capabilities.kindPreferences` is true; elsewhere this is a 403
 * `not_available_in_this_profile` and the browser keeps them.
 */
export function getKindPreferences(signal?: AbortSignal): Promise<KindPreferences> {
  return request<KindPreferences>(url('/api/me/kinds'), signal ? { signal } : {});
}

/** Replace them, both members; answers what the server kept. */
export function setKindPreferences(prefs: KindPreferences): Promise<KindPreferences> {
  return request<KindPreferences>(url('/api/me/kinds'), { method: 'PUT', body: prefs });
}

/**
 * Turn one extension on or off on one cluster - the Extra tab's switch.
 * Registered only where `Capabilities.preferences` is
 * true: not under the server profile, and not on one of several replicas.
 */
export function setExtensionEnabled(cluster: string, key: string, enabled: boolean): Promise<void> {
  return request<void>(url('/api/c/{cluster}/extensions/{key}', { cluster, key }), {
    method: 'PUT',
    body: { enabled },
  });
}

/**
 * Write one setting of one extension on one cluster - a checkbox in the
 * extension's tile. Registered exactly where `setExtensionEnabled` is.
 */
export function setExtensionSetting(cluster: string, key: string, setting: string, value: boolean): Promise<void> {
  return request<void>(url('/api/c/{cluster}/extensions/{key}/settings/{setting}', { cluster, key, setting }), {
    method: 'PUT',
    body: { value },
  });
}

/**
 * Write one text setting of one extension on one cluster,
 * from the Edit form of the extension's tile. An empty text returns it to not
 * set. Registered exactly where `setExtensionEnabled` is.
 */
export function setExtensionText(cluster: string, key: string, setting: string, text: string): Promise<void> {
  return request<void>(url('/api/c/{cluster}/extensions/{key}/settings/{setting}', { cluster, key, setting }), {
    method: 'PUT',
    body: { text },
  });
}

// ---- Preferences: the rail ----------------------------------------------

export function getPreferences(signal?: AbortSignal): Promise<Preferences> {
  return request<Preferences>(url('/api/preferences'), signal ? { signal } : {});
}

/**
 * One cluster's nameplate: what the tile says, its initials, its color.
 *
 * **Only the members named are written** (`RailNameplateWrite`): an absent
 * member leaves that part alone and an empty one returns it to the derived form,
 * and `reset` clears all three. So a caller passes what changed and nothing
 * else - a body carrying every member would re-assert two fields as a side
 * effect of editing the third.
 */
export function setRailNameplate(
  cluster: string,
  change: Partial<Omit<RailNameplateWrite, 'cluster'>>,
): Promise<void> {
  return request<void>(url('/api/preferences/rail/nameplate'), {
    method: 'POST',
    body: { cluster, ...change },
  });
}

/**
 * Several preference writes that make one gesture, sent one after another.
 *
 * Sequential so that a refusal part-way leaves a prefix rather than an arbitrary
 * subset, and **a refusal stops the rest**: it changed nothing, and the writes
 * after it were planned against an arrangement that did not happen.
 *
 * **A write the server applied and could not save does not stop them**
 * (`ApiError.isNotSaved`). It changed the arrangement, so stopping there is
 * what leaves half a gesture standing - a swap with one tile moved and the
 * other on top of it. The remaining writes are sent and the first such error is
 * thrown at the end, so the caller still says the arrangement will not survive
 * a restart.
 */
export async function writeInOrder(writes: readonly (() => Promise<void>)[]): Promise<void> {
  let unsaved: ApiError | undefined;
  for (const write of writes) {
    try {
      await write();
    } catch (error) {
      if (!(error instanceof ApiError) || !error.isNotSaved) throw error;
      unsaved ??= error;
    }
  }
  if (unsaved !== undefined) throw unsaved;
}

export function setRailMove(cluster: string, order: number): Promise<void> {
  return request<void>(url('/api/preferences/rail/move'), { method: 'POST', body: { cluster, order } });
}

/**
 * Send one cluster's tile to one hotbar. `RailAssignWrite` is `{cluster, bar}`,
 * `bar` 1-based and bounded by `preferences.rail.barCount`.
 *
 * The arrange dialog's Bar field is the only caller. It is not the same gesture
 * as `setRailPlace` below, which places a tile within a bar.
 */
export function setRailAssign(cluster: string, bar: number): Promise<void> {
  return request<void>(url('/api/preferences/rail/assign'), {
    method: 'POST',
    body: { cluster, bar },
  });
}

/**
 * Drop one cluster's tile beside another.
 *
 * `RailPlaceWrite` is `additionalProperties: false` and requires `target`.
 * Nothing type-checks these bodies (they are plain object literals behind
 * `request<void>`), so a contract change surfaces here as a failed write.
 *
 * **Nothing calls this yet.** A drop target needs a drag, and the arrange dialog
 * moves tiles with the two arrow buttons through `setRailMove`. A neighbor and
 * an edge is deliberately not an index: an index is a fact about the list the
 * browser last saw.
 */
export function setRailPlace(
  cluster: string,
  target: string,
  edge: 'before' | 'after' = 'before',
): Promise<void> {
  return request<void>(url('/api/preferences/rail/place'), {
    method: 'POST',
    body: { cluster, target, edge },
  });
}

/** Deliberately not `hide` with a boolean: a client with a stale view of what is
 *  hidden flips the wrong way. `api/openapi.yaml` keeps them as two routes and
 *  so does this. */
export function setRailHide(cluster: string): Promise<void> {
  return request<void>(url('/api/preferences/rail/hide'), { method: 'POST', body: { cluster } });
}

export function setRailReveal(cluster: string): Promise<void> {
  return request<void>(url('/api/preferences/rail/reveal'), { method: 'POST', body: { cluster } });
}

/**
 * Put one hotbar on screen.
 *
 * `RailBarWrite` is `{bar}`: the route shows no bar and hides none, it selects
 * which of several is drawn. Called by `shell/Rail.tsx`'s `BarSwitcher`.
 */
export function setRailBar(bar: number): Promise<void> {
  return request<void>(url('/api/preferences/rail/bar'), { method: 'POST', body: { bar } });
}

// ---- Resources ----------------------------------------------------------

/**
 * Query parameters, appended to a path whose segments are already encoded.
 *
 * Written here rather than at the call site for the reason `api/openapi.yaml`
 * gives about `search`: a free-text filter is "sent as a query parameter and
 * never interpolated into a path", because a name containing an ampersand must
 * not be able to send the request somewhere else — a bug this product has
 * already paid for once in its template layer. `URLSearchParams` is the
 * encoding, and an empty value is dropped rather than sent as `&search=`, so
 * that a cleared filter box and a filter box that was never touched produce the
 * same request and therefore share one cache entry.
 */
function withQuery(
  path: string,
  params: Readonly<Record<string, string | readonly string[] | undefined>>,
): string {
  const search = new URLSearchParams();
  for (const [name, value] of Object.entries(params)) {
    // An array is **appended once per value**, which is what `explode: true` on
    // a repeatable parameter means: `?namespace=a&namespace=b`. Joining it into
    // one value would be a different request, which the server would read as
    // a single namespace.
    if (Array.isArray(value)) {
      for (const one of value) if (one !== '') search.append(name, one);
    } else if (typeof value === 'string' && value !== '') {
      search.set(name, value);
    }
  }
  const query = search.toString();
  return query === '' ? path : `${path}?${query}`;
}

export interface ListResourcesParams {
  /**
   * The namespaces to narrow to. **Several, not one.**
   *
   * The parameter is repeatable (`api/openapi.yaml`): the selector it feeds is
   * a multiple selection, and a list narrowed to four namespaces is how a
   * cluster with thirty is actually read.
   *
   * Empty means every namespace the caller may read. That is not the same as
   * `default`, and a client that substitutes one for the other shows a fraction
   * of a cluster with no sign that it did.
   */
  namespace?: readonly string[] | undefined;
  selector?: string | undefined;
  search?: string | undefined;
  /**
   * The filter control's choices, each `<column label>:<value>`, repeated like
   * the namespace: several values of one column are either, several columns
   * are all. Server-side, so the count stays the server's.
   */
  filter?: readonly string[] | undefined;
}

/**
 * One kind, in the shape the grid draws.
 *
 * A **value** result: `asValue` and not `asList`. The page is one object whose
 * `rows` happens to be a list, so the five states are about the page and a kind
 * with nothing in it is `ok` with an empty `rows`.
 */
export async function listResources(
  cluster: string,
  kind: string,
  params: ListResourcesParams = {},
  signal?: AbortSignal,
): Promise<ValueResult<ListPage>> {
  const path = withQuery(url('/api/c/{cluster}/r/{kind}', { cluster, kind }), {
    namespace: params.namespace,
    selector: params.selector,
    search: params.search,
    filter: params.filter,
  });
  return asValue(await request(path, signal ? { signal } : {}));
}

// ---- Preferences: the grid ----------------------------------------------
//
// **One cell per request, and the schemas refuse the second field.** This is the
// endpoint set the whole-object rule was written for: a body carrying every
// column's visibility means that unticking one re-asserts the rest from whatever
// the page happened to hold, which freezes a grid that has since gained a column.
// `09-silent-losses.md` §2.4 records it as a loss that is a design comment rather
// than a line of code — a JSON API accepting the whole preferences object is
// tidier, passes review, and reintroduces the exact bug the shape prevents.

export function setColumnShow(kind: string, column: string, visible: boolean): Promise<void> {
  return request<void>(url('/api/preferences/columns/show'), {
    method: 'POST',
    body: { kind, column, visible },
  });
}

export function setColumnSize(kind: string, column: string, width: number | null): Promise<void> {
  return request<void>(url('/api/preferences/columns/size'), {
    method: 'POST',
    body: { kind, column, width },
  });
}

/**
 * Move one column **beside another**, which is the drop the store performs.
 *
 * `ColumnPlaceWrite` offers two forms and says which is which: `target` and
 * `edge` are "the drop the store actually performs", and `order` "remains for a
 * client holding a position and is the weaker of the two". This sends the
 * first: **an index is a fact about the list the browser last
 * saw**, and the panel is redrawn under the pointer every two seconds, so a
 * position resolved against a stale list lands somewhere nobody aimed.
 */
export function setColumnBeside(
  kind: string,
  column: string,
  target: string,
  edge: 'before' | 'after',
): Promise<void> {
  return request<void>(url('/api/preferences/columns/place'), {
    method: 'POST',
    body: { kind, column, target, edge },
  });
}

/** The weaker form, kept because the contract keeps it. Nothing calls it: every
 *  gesture in this console is "beside that one, on this side". */
export function setColumnPlace(kind: string, column: string, order: number): Promise<void> {
  return request<void>(url('/api/preferences/columns/place'), {
    method: 'POST',
    body: { kind, column, order },
  });
}

/**
 * Row height, **for every table**, which is why this is the one write here that
 * carries no `kind`.
 *
 * `api/openapi.yaml` says so on the route and `internal/columns/columns.go:758`
 * is where the store keeps it: it is a statement about the operator's screen
 * rather than about pods, and somebody who wants more rows at once wants them
 * everywhere.
 */
export function setColumnDensity(density: 'comfortable' | 'compact'): Promise<void> {
  return request<void>(url('/api/preferences/columns/density'), {
    method: 'POST',
    body: { density },
  });
}

/** The one write here that is deliberately not a single cell, because forgetting
 *  is the operation. It names the kind it forgets, so it can never be a
 *  whole-console reset by accident. */
export function resetColumns(kind: string): Promise<void> {
  return request<void>(url('/api/preferences/columns/reset'), { method: 'POST', body: { kind } });
}

// ---- One object ---------------------------------------------------------

/**
 * One object, in the shape the drawer draws.
 *
 * **Two operations and not one with an optional segment**, because the contract
 * has two: `/r/{kind}/{name}` is a cluster-scoped object
 * and `/r/{kind}/{namespace}/{name}` a namespaced one, and *"one trailing
 * segment is a name, never a namespace"*. Choosing between them by whether a
 * string is empty is the arithmetic that reported a node's name as a namespace;
 * choosing by which template was written is checked by `keyof paths` above.
 *
 * A **value** result: an object that is not there is `absent`.
 */
export async function getResource(
  cluster: string,
  kind: string,
  namespace: string | undefined,
  name: string,
  signal?: AbortSignal,
): Promise<ValueResult<DetailPage>> {
  const path =
    namespace === undefined || namespace === ''
      ? url('/api/c/{cluster}/r/{kind}/{name}', { cluster, kind, name })
      : url('/api/c/{cluster}/r/{kind}/{namespace}/{name}', { cluster, kind, namespace, name });
  return asValue(await request(path, signal ? { signal } : {}));
}

/**
 * The object's manifest, as tokens.
 *
 * **Tokens and never markup.** The coloring is `internal/web/yamlcolour.go` -
 * one colouriser, so a custom resource's manifest and a built-in one do not read
 * differently - and it returns `{text, class}` pairs that the client renders as
 * spans. The contract says why in as many words: *a contract that ships HTML
 * ships an injection surface with it*, and `react/no-danger` is an error on this
 * side of it.
 *
 * **Secret values arrive masked**, by `detail.Trimmed`, which is also the base of
 * the edit diff - so the mask is identical on both sides and cannot be written
 * back over a value. `raw` is that same masked document, for the editor.
 *
 * A **value** result: an object that is not there is `absent`, never an empty
 * manifest.
 */
export async function getManifest(
  cluster: string,
  kind: string,
  namespace: string | undefined,
  name: string,
  signal?: AbortSignal,
): Promise<ValueResult<ManifestPanel>> {
  // One route with the object in the query string, and not the `/r/{kind}/...`
  // pair: that is what the contract declares, and `keyof paths` is what refuses
  // an invention here.
  const path = withQuery(url('/api/c/{cluster}/manifest', { cluster }), {
    kind,
    namespace,
    name,
  });
  return asValue(await request(path, signal ? { signal } : {}));
}

/**
 * Reveal one key of an object this client is already looking at.
 *
 * **The same door as `revealEnvValue`, entered with an address rather than a
 * Kind.** That function exists for a *container's environment row*, which names
 * its source as a Kind - "Secret" - and has to map it to a resource before it
 * can ask. A panel drawn from `/data` is past that: its `kind` is the discovery
 * plural the route carried, which is the address itself.
 *
 * Mapping it back through `resourceOfSource` is what the first version did, and
 * it threw `no reveal address for a secrets source` on every click - the map is
 * keyed by Kind and was handed a plural. Two names for one thing, and the
 * conversion in the direction nobody needed.
 */
export async function revealDataValue(
  cluster: string,
  kind: string,
  namespace: string | undefined,
  name: string,
  key: string,
  signal?: AbortSignal,
): Promise<ValueResult<EnvValue>> {
  const path = withQuery(url('/api/c/{cluster}/env', { cluster }), { kind, namespace, name, key });
  return asValue(await request(path, signal ? { signal } : {}));
}

/**
 * A ConfigMap's or a Secret's own key/value block.
 *
 * **A Secret's values are not in here.** `DataEntry.value` is absent while
 * `masked` is true, and the one door is `revealEnvValue` - the same door a
 * container's environment row uses, addressed at the source object, which for
 * this panel is the object itself. Nothing in this client is allowed a second
 * way to obtain a masked value, which is what makes the audit line in a shared
 * profile complete.
 *
 * `resourceVersion` comes back on the panel and rides on every write as a
 * precondition. Without it two tabs overwrite each other and the loser never
 * learns - the contract states it in one line and it is the reason the write
 * takes the panel rather than a key.
 */
export async function getDataPanel(
  cluster: string,
  kind: string,
  namespace: string | undefined,
  name: string,
  signal?: AbortSignal,
): Promise<ValueResult<DataPanel>> {
  const path = withQuery(url('/api/c/{cluster}/data', { cluster }), { kind, namespace, name });
  return asValue(await request(path, signal ? { signal } : {}));
}

/**
 * What writing these entries would do. **Touches nothing.**
 *
 * The same split as the manifest editor and for the same reason: this validates
 * and returns a plan, and only the dialog that plan draws posts the token to
 * `applyData`. One call that previewed and applied would be a confirmation the
 * operator reads afterwards.
 */
export function previewData(cluster: string, write: DataWrite): Promise<ConfirmationPlan> {
  return request<ConfirmationPlan>(url('/api/c/{cluster}/data/preview', { cluster }), {
    method: 'POST',
    body: write,
  });
}

/**
 * What opening a shell on this node would do. **Creates nothing.**
 *
 * The one plan whose act is not an operation in the contract: the shell is a
 * WebSocket upgrade, so the plan carries a `channel` beside its token and **that
 * token crosses protocols**. The upgrade re-derives the facts before creating
 * anything, and an upgrade with no token, an expired one, or one minted for
 * another cluster is refused with 403 and no socket is opened.
 *
 * Which is why this is a preview and not a button: the pod is created *inside*
 * the socket, and closing the socket is what deletes it. An operator agreeing to
 * that has to have been told it.
 */
export function previewNodeShell(cluster: string, node: string): Promise<ConfirmationPlan> {
  return request<ConfirmationPlan>(url('/api/c/{cluster}/nodeshell/preview', { cluster }), {
    method: 'POST',
    body: { node },
  });
}

/**
 * What running this verb on this object would do. **Touches nothing.**
 *
 * The single-object half of the same pair the bulk bar uses, and the same rule:
 * this validates and returns a plan, and only the dialog that plan draws posts
 * its token. The verb and the target are all it takes: a replica count is a
 * value rather than a proof of intent, and the plan asks for it as
 * `confirmation.kind: number`; the precondition the act carries is stated on the
 * plan by the server and is never a client's to send.
 */
export function previewAction(
  cluster: string,
  request_: { verb: string; target: PlanTarget; includeUnowned?: boolean },
): Promise<ConfirmationPlan> {
  return request<ConfirmationPlan>(url('/api/c/{cluster}/actions/preview', { cluster }), {
    method: 'POST',
    body: request_,
  });
}

/** Run the verb the plan was minted for: the token, and the gesture it asked
 *  for. The facts were re-derived server-side when the plan was made, so the
 *  target is not sent again. `force` is the "Force delete" box - a grace
 *  period of zero - and is sent only when ticked, on a plan that says
 *  `forceable`; the server refuses it anywhere else. */
export function runAction(
  cluster: string,
  token: string,
  confirmed?: string,
  force?: boolean,
): Promise<ActOutcome> {
  return request<ActOutcome>(url('/api/c/{cluster}/actions', { cluster }), {
    method: 'POST',
    body: {
      token,
      ...(confirmed === undefined ? {} : { confirmed }),
      ...(force === true ? { force: true } : {}),
    },
  });
}

/**
 * What discarding this store would destroy. **Touches nothing.**
 *
 * The one destructive act with no cluster behind it, and it takes a plan like
 * every other. `archive` is the scope that empties this console's own kept
 * window of events and log lines - deliberately not part of `all`, because the
 * other scopes are preferences and losing those costs a column width, while the
 * archive is the only store here holding something no other system has.
 */
export function previewClear(scope: 'preferences' | 'clusterState' | 'archive' | 'all'): Promise<ConfirmationPlan> {
  return request<ConfirmationPlan>('/api/settings/clear', {
    method: 'POST',
    body: { scope, preview: true },
  });
}

/** Discard it, with the token and the gesture the plan asked for. The scope
 *  rides again because the server re-derives the plan from it and refuses a
 *  token whose facts have changed since. */
export function runClear(
  scope: 'preferences' | 'clusterState' | 'archive' | 'all',
  token: string,
  confirmed?: string,
): Promise<ActOutcome> {
  return request<ActOutcome>('/api/settings/clear', {
    method: 'POST',
    body: confirmed === undefined ? { scope, preview: false, token } : { scope, preview: false, token, confirmed },
  });
}

/**
 * What running this verb over this selection would do. **Touches nothing.**
 *
 * The plan names every target the *server* resolved, and `facts.count` is what
 * the operator agrees to - not the number this client has ticked. The contract
 * says why in one line: between a preview and a click, a cluster changes. If the
 * two figures differ, the front end is about to confirm the wrong thing, and the
 * dialog shows the server's.
 */
export function previewBulk(
  cluster: string,
  request_: { verb: string; kind: string; targets: readonly PlanTarget[] },
): Promise<ConfirmationPlan> {
  return request<ConfirmationPlan>(url('/api/c/{cluster}/bulk/preview', { cluster }), {
    method: 'POST',
    body: request_,
  });
}

/**
 * Run the verb the plan was minted for.
 *
 * The **token**, and the gesture the plan asked for. The server re-resolves the
 * selection from the token and answers 409 if the set is no longer the set that
 * was shown - *a confirmation that is not about the set being acted on is not a
 * confirmation*. That 409 is a re-read here, never a retry: retrying it would be
 * acting on a set nobody has seen.
 *
 * Partial success is the normal case and comes back as such: `succeeded`,
 * `failed`, and a result per target.
 */
export function runBulk(cluster: string, token: string, confirmed?: string): Promise<ActOutcome> {
  return request<ActOutcome>(url('/api/c/{cluster}/bulk', { cluster }), {
    method: 'POST',
    body: confirmed === undefined ? { token } : { token, confirmed },
  });
}

/** Write the entries the plan was minted for. The **token** and nothing else:
 *  the facts the operator agreed to were re-derived server-side when the plan
 *  was made, so sending the document again would be sending a second one. */
export function applyData(cluster: string, token: string): Promise<ActOutcome> {
  return request<ActOutcome>(url('/api/c/{cluster}/data', { cluster }), {
    method: 'POST',
    body: { token },
  });
}

/**
 * The metrics panel for one object.
 *
 * **The address is composed here and never taken from the payload**, although
 * the object's own `tabs[].endpoint` carries a ready-made one. Two reasons, and
 * the second is the one that matters: `url()` is keyed on `keyof paths`, so a
 * route that leaves the contract stops compiling here - a string from a response
 * is checked by nothing; and `RefLink.tsx` already argues the general form, that
 * a server-supplied href is accepted only where this client can say what it is.
 * The `endpoint` field stays useful as the server's statement that the tab has
 * something behind it, which is what `tabs` is read for.
 *
 * `range` and `points` are the zoom. The contract made them parameters rather
 * than a second operation precisely so that a wider window is this call with a
 * different argument - see the operation's own description.
 */
export async function getMetricsPanel(
  cluster: string,
  kind: string,
  namespace: string | undefined,
  name: string,
  window?: { range?: string; points?: number },
  signal?: AbortSignal,
): Promise<ValueResult<MetricsPanel>> {
  const path = withQuery(url('/api/c/{cluster}/metrics', { cluster }), {
    kind,
    namespace,
    name,
    range: window?.range,
    // A number has to become a string for the query, and `undefined` has to stay
    // `undefined` rather than becoming "undefined" - which `String()` would do,
    // and which the server would refuse as a malformed integer.
    points: window?.points === undefined ? undefined : String(window.points),
  });
  return asValue(await request(path, signal ? { signal } : {}));
}

/**
 * The change history for one object.
 *
 * **What this returns is the field managers' record** - who last wrote which
 * part of the object. It is not the archive this console keeps after the
 * cluster has forgotten its events, which no operation exposes yet; this
 * comment exists so that nobody reads the name and assumes the archive is
 * being drawn.
 */
export async function getChangesPanel(
  cluster: string,
  kind: string,
  namespace: string | undefined,
  name: string,
  signal?: AbortSignal,
): Promise<ValueResult<ChangesPanel>> {
  const path = withQuery(url('/api/c/{cluster}/changes', { cluster }), {
    kind,
    namespace,
    name,
  });
  return asValue(await request(path, signal ? { signal } : {}));
}

/**
 * What applying this document would do. **Touches nothing.**
 *
 * Two requests and not one, and the split is the whole safety of the path: this
 * validates against the cluster with a dry run and changes nothing, and only the
 * dialog it draws posts to `applyManifest`. A single call that both previewed and
 * applied would be a confirmation the operator reads afterwards.
 *
 * The kind that is authorized is read out of the **submitted document**, after
 * parsing, and never from a route or a sibling field - checking the route's kind
 * and writing the body's kind is a full bypass that looks correct in both halves
 * of the code. Nothing on this side can influence that, which is the point.
 *
 * Not an envelope: a plan or a problem.
 */
export function previewManifest(
  cluster: string,
  write: ManifestWrite,
  signal?: AbortSignal,
): Promise<ConfirmationPlan> {
  return request<ConfirmationPlan>(url('/api/c/{cluster}/manifest/preview', { cluster }), {
    method: 'POST',
    body: write,
    ...(signal ? { signal } : {}),
  });
}

/**
 * Apply the document that was previewed.
 *
 * **The token is the whole request.** It binds the act to the plan the operator
 * agreed to, it is single-use and short-lived, and the server re-derives the
 * facts before acting - so a confirmation about an object that has since changed
 * is refused with a 409 rather than applied. Sending the document again here
 * would be sending a second document, and the one that was agreed to would be
 * the one not applied.
 */
export function applyManifest(
  cluster: string,
  token: string,
  signal?: AbortSignal,
): Promise<ActOutcome> {
  return request<ActOutcome>(url('/api/c/{cluster}/manifest/apply', { cluster }), {
    method: 'POST',
    body: { token },
    ...(signal ? { signal } : {}),
  });
}

/**
 * A starting document for a new object of one kind: what
 * the list's "+" opens. The client knows the list's slug only, so the server
 * resolves `apiVersion` and `kind` and fills `metadata.namespace` with
 * `namespace` on a namespaced kind.
 *
 * **Reading a template is not permission to create.** `ListPage.create` is the
 * verdict the button is drawn from, and `previewCreate` reviews it again.
 */
export async function getCreateTemplate(
  cluster: string,
  kind: string,
  namespace: string | undefined,
  signal?: AbortSignal,
): Promise<ValueResult<CreateTemplate>> {
  const path = withQuery(url('/api/c/{cluster}/create/template', { cluster }), { kind, namespace });
  return asValue(await request(path, signal ? { signal } : {}));
}

/**
 * What creating this document would do. **Touches nothing.** The twin of
 * `previewManifest` for an object that does not exist yet: a dry run of a
 * create, never of a patch, so an object that already exists is refused rather
 * than overwritten. What is reviewed is read out of the document, after
 * parsing, as for an edit. No `resourceVersion`: a new object has none.
 */
export function previewCreate(
  cluster: string,
  document: string,
  signal?: AbortSignal,
): Promise<ConfirmationPlan> {
  return request<ConfirmationPlan>(url('/api/c/{cluster}/create/preview', { cluster }), {
    method: 'POST',
    body: { document } satisfies ManifestWrite,
    ...(signal ? { signal } : {}),
  });
}

/**
 * Create the object that was previewed. The token is the whole request, for
 * `applyManifest`'s reason; the outcome's `next` names the object created.
 */
export function applyCreate(
  cluster: string,
  token: string,
  signal?: AbortSignal,
): Promise<ActOutcome> {
  return request<ActOutcome>(url('/api/c/{cluster}/create/apply', { cluster }), {
    method: 'POST',
    body: { token },
    ...(signal ? { signal } : {}),
  });
}

// ---- Prerequisites ------------------------------------------------------

/**
 * An operator an extra page reads from, as the console would install it:
 * whether Argo CD is there to install through, the chart pinned, whether the
 * Application exists, and whether this person may create it. **Touches
 * nothing**, and decides what a page draws, never what happens.
 */
export async function getPrereq(cluster: string, key: string, signal?: AbortSignal): Promise<ValueResult<Prereq>> {
  return asValue(await request(url('/api/c/{cluster}/prereq/{key}', { cluster, key }), signal ? { signal } : {}));
}

/**
 * The install's plan: one Argo CD Application, its YAML as `facts.request`,
 * validated by a dry run. **Touches nothing.** No body: the key is the request.
 */
export function previewPrereq(cluster: string, key: string, signal?: AbortSignal): Promise<ConfirmationPlan> {
  return request<ConfirmationPlan>(url('/api/c/{cluster}/prereq/{key}/preview', { cluster, key }), {
    method: 'POST',
    ...(signal ? { signal } : {}),
  });
}

/** Create the Application that was previewed: the token and the typed name. */
export function installPrereq(cluster: string, key: string, token: string, confirmed: string): Promise<ActOutcome> {
  return request<ActOutcome>(url('/api/c/{cluster}/prereq/{key}/install', { cluster, key }), {
    method: 'POST',
    body: { token, confirmed },
  });
}

// ---- The one door that reveals ------------------------------------------

/**
 * The discovery plural of the two kinds a source can be, and there are two.
 *
 * `EnvSource.kind` is written as a person writes it - `ConfigMap`, `Secret` -
 * and the API takes a discovery plural (`kindPattern` is
 * `^[a-z0-9][a-z0-9.-]{0,126}$`, so `ConfigMap` is refused outright). Something
 * has to bridge the two.
 *
 * **A table of the two, and not `kind.toLowerCase() + 's'`.** The producer's set
 * is closed at two by construction - `internal/detail` fills a source only for a
 * `configMapKeyRef`, a `secretKeyRef` or an `envFrom`, and nothing else can reach
 * this - so a table is exhaustive where a rule is a guess that happens to be
 * right twice. A kind this does not know returns nothing rather than a plausible
 * URL: a wrong plural here is a request that 404s, and a reveal control whose
 * failure reads as "that key is not there" is worse than one that is absent.
 */
const SOURCE_RESOURCE: Readonly<Record<string, string>> = {
  ConfigMap: 'configmaps',
  Secret: 'secrets',
};

export function resourceOfSource(kind: string): string | undefined {
  return SOURCE_RESOURCE[kind];
}

/**
 * Reveal one masked value.
 *
 * **The one door**, and four of its properties are the client's half to keep:
 *
 * - The **key** travels in the query and the **value** only in the body. A secret
 *   in a URL is a secret in an access log.
 * - `kind` and `name` are the **source object** - the ConfigMap or the Secret the
 *   row names - and never the workload. Several containers import the same
 *   ConfigMap under different prefixes, so the variable's name as a container
 *   sees it is not the key in the source; addressing the workload would mean
 *   re-deriving that mapping on every reveal, from a pod spec that may have moved
 *   since the panel was drawn. It is also what keeps this from being a general
 *   object reader: the server refuses any kind carrying no data block, which is
 *   one condition rather than a guard rebuilt somewhere easy to forget.
 * - **One key, one request.** There is no control anywhere that opens more than
 *   one, and this function cannot express one.
 * - `container` and `variable` are for the audit line and for the response's own
 *   labeling. Neither decides what is read.
 *
 * `namespace` is the **object's** namespace - the source lives beside the
 * workload that names it - and is passed by the caller rather than dug out of the
 * source, which does not carry one.
 *
 * A **value** result: a key that is not there is `absent`, and an `ok` carrying
 * the empty string is a revealed empty value, which is a real and confusing thing
 * to meet in a Secret and is distinguishable only because the envelope says so.
 */
export async function revealEnvValue(
  cluster: string,
  namespace: string | undefined,
  source: EnvSource,
  labelling: { container?: string | undefined; variable?: string | undefined } = {},
  signal?: AbortSignal,
): Promise<ValueResult<EnvValue>> {
  const resource = resourceOfSource(source.kind);
  if (resource === undefined) {
    throw new Error(`no reveal address for a ${source.kind} source`);
  }
  const path = withQuery(url('/api/c/{cluster}/env', { cluster }), {
    kind: resource,
    namespace,
    name: source.name,
    key: source.key,
    container: labelling.container,
    variable: labelling.variable,
  });
  return asValue(await request(path, signal ? { signal } : {}));
}

// ---- The dock ------------------------------------------------------------

/**
 * What can be followed on a pod, so the panel offers a choice rather than making
 * one.
 *
 * A **list** result, and the arms are the point: a build with no log reader is
 * `absent` and not an empty array. And the failure policy above this call is
 * deliberate: *"the stream starts whether or not this succeeds,
 * because the server picks a default container on its own and a switcher we
 * could not draw must not cost the logs themselves"*.
 */
export async function listLogContainers(
  ref: { cluster: string; kind: string; namespace: string; name: string },
  signal?: AbortSignal,
): Promise<ListResult<Container>> {
  const path = withQuery(url('/api/logs/containers'), {
    cluster: ref.cluster, kind: ref.kind, namespace: ref.namespace, name: ref.name,
  });
  return asList(await request(path, signal ? { signal } : {}));
}

/**
 * One page backwards through the same log.
 *
 * Same address as the stream and a different shape, and the asymmetry is the
 * point rather than an inconsistency: `before` turns `/api/logs` into a bounded
 * JSON read. A `fetch` may read the status **and** the sentence; the
 * `EventSource` a follow runs on may read neither, which is why a refusal
 * mid-stream has to be smuggled into a frame and a refusal here is an ordinary
 * 403 the caller can retry.
 *
 * `before` is how many lines the pane already holds - not an offset and not a
 * cursor, because the Kubernetes log API has neither. The server grows the tail
 * to `before + page` and keeps the front of it. Overstating it buys nothing
 * (the window is clamped either way) and understating it only costs lines the
 * caller already has, so there is nothing here for a client to get dangerously
 * wrong.
 *
 * A **value** result. The three ways paging stops live *inside* the payload as
 * `stop`, and they answer a different question from the envelope's state: the
 * envelope says whether the page arrived, and `stop` says whether there will be
 * another one. A panel that folded the two together would render "this account
 * may not read this log" and "there is nothing older" identically.
 */
export async function fetchEarlierLogs(
  query: EarlierLogsQuery,
  signal?: AbortSignal,
): Promise<ValueResult<LogPage>> {
  const path = withQuery(url('/api/logs'), {
    cluster: query.cluster, kind: query.kind, namespace: query.namespace, name: query.name,
    ...(query.container !== undefined && query.container !== '' ? { container: query.container } : {}),
    before: String(query.before),
    ...(query.page !== undefined ? { page: String(query.page) } : {}),
    ...(query.previous === true ? { previous: 'true' } : {}),
    // Carried even on a page backwards, and deliberately: a page arriving
    // without stamps beside a buffer that has them would have the wrong first
    // word dimmed.
    ...(query.timestamps === true ? { timestamps: 'true' } : {}),
  });
  return asValue(await request(path, signal ? { signal } : {}));
}

export interface EarlierLogsQuery {
  readonly cluster: string;
  readonly kind: string;
  readonly namespace: string;
  readonly name: string;
  readonly container?: string;
  /** How many lines the pane already holds. */
  readonly before: number;
  /** How many earlier lines to ask for. Omitted means the server's own figure. */
  readonly page?: number;
  readonly previous?: boolean;
  readonly timestamps?: boolean;
}

/** The port-forwards this process holds. Process state rather than a cluster
 *  read, so it is a plain array and not an envelope. */
export function listForwards(signal?: AbortSignal): Promise<Forward[]> {
  return request<Forward[]>(url('/api/forwards'), signal ? { signal } : {});
}

/** Open one. `portName` and `appProtocol` are sent so the **server** decides the
 *  scheme; a copy of its table here would be a second answer to the same
 *  question, drifting the first time one of them learned a new port. */
export function openForward(body: ForwardRequest): Promise<Forward> {
  return request<Forward>(url('/api/forwards'), { method: 'POST', body });
}

export function closeForward(id: string): Promise<void> {
  return request<void>(url('/api/forwards/{id}', { id }), { method: 'DELETE' });
}

/** Open a forward's flow UI in a window of the desktop's own, beside the
 *  console's. Only where `capabilities.toolWindow` is true; the server builds
 *  the address from the forward, so only the path on it is sent. */
export function openForwardWindow(id: string, body: ForwardWindowRequest): Promise<void> {
  return request<void>(url('/api/forwards/{id}/window', { id }), { method: 'POST', body });
}
