import type { ListPage, Row } from '../../api';
import { clusterPath } from '../../clusters/clusterPath';

/**
 * Which object, and how it is addressed.
 *
 * ## Two URL shapes, and the trailing segment is a name
 *
 * `/c/{cluster}/{kind}/{name}` is a cluster-scoped object and
 * `/c/{cluster}/{kind}/{namespace}/{name}` is a namespaced one. Reading them all
 * as the second would report **a node's name as a namespace**, which passes
 * every validator, reaches `kubectl -n`, and is then stated as fact.
 *
 * The API keeps the two apart on its own side
 * (`/api/c/{cluster}/r/{kind}/{name}` and
 * `/api/c/{cluster}/r/{kind}/{namespace}/{name}`). The client route is the second
 * place the same distinction has to hold, and it holds here rather than at each
 * call site.
 *
 * ## Which form is a fact the payload carries, never a guess
 *
 * `api/openapi.yaml` on `ListPage.namespaced`: *"Which detail form to address.
 * The front end reads this rather than guessing from whether a row has a
 * namespace."* So `identityOf` reads the flag and refuses the row where the two
 * disagree, instead of inferring the kind's scope from one row that happened to
 * carry a namespace.
 *
 * ## An ambiguous identity opens nothing
 *
 * A list's namespace parameter is a *selection* and may hold several names, so
 * reading it raw could address a namespace called `argocd,kube-system` - a value
 * that passes every validator, reaches the API server, and comes back "not
 * found" for an object plainly visible behind the drawer. **An ambiguous
 * selection opens no drawer at all, because nothing here knows which of the
 * several the user meant.**
 *
 * The rule: an identity is built only where the row names **one** object. A
 * namespaced kind whose row carries no namespace, a row with no name, or a
 * namespace that is a selection rather than a name are each `undefined` here —
 * and `undefined` is what the caller must draw nothing for. It is deliberately
 * not a thrown error and not a best guess: a row that cannot be addressed is a
 * row that does not open.
 *
 * A comma cannot occur inside a DNS-1123 label, which is what makes the joined
 * form detectable at all.
 */
export interface ObjectIdentity {
  readonly cluster: string;
  readonly kind: string;
  /** Absent on a cluster-scoped kind, and absent is not the empty string: the
   *  two produce different URLs and the wrong one addresses a different object. */
  readonly namespace?: string | undefined;
  readonly name: string;
}

/**
 * One string per object, for a React `key`.
 *
 * The drawer is one element whose content is swapped when another row is
 * clicked, and without a key React keeps the same `ObjectDetail` instance for
 * the next object: a failure under Verbs, Terminals or Ports would survive the
 * re-target, and with it a node shell's plan, so the dialog and the dock tab
 * would name node B while the token opened node A. Keyed on this, another object is
 * another panel. All four parts, because two clusters hold a `default/api`
 * each, and a JSON array so no name can be read as two parts.
 */
export function identityKey(identity: ObjectIdentity): string {
  return JSON.stringify([identity.cluster, identity.kind, identity.namespace ?? '', identity.name]);
}

/** A name that addresses exactly one object. Empty is not one, and neither is a
 *  comma-joined selection — see the header. */
function namesOne(value: string): boolean {
  return value !== '' && !value.includes(',') && !value.includes('/');
}

/**
 * The identity a row addresses, or `undefined` where it addresses no single
 * object.
 *
 * `page` is read for `namespaced` alone. Passing the page rather than a boolean
 * is deliberate: the flag has one producer and a caller that had to extract it
 * would be a caller that could extract the wrong thing.
 */
export function identityOf(
  cluster: string | undefined,
  page: ListPage,
  row: Row,
): ObjectIdentity | undefined {
  // The cluster is **not** put through `namesOne`, and that is deliberate rather
  // than an omission: it arrives from a route segment that has already matched,
  // and a context name is whatever a `KUBECONFIG` says it is - an EKS one is
  // `arn:aws:eks:eu-west-1:1234:cluster/prod` and holds a slash. Judging it again
  // here would refuse to open an object on a cluster whose list drew perfectly
  // well. What has to name one thing is the row's own identity, which is what
  // the rest of this function is about.
  if (cluster === undefined || cluster === '') return undefined;

  const name = row.name ?? '';
  if (!namesOne(name)) return undefined;

  const namespace = row.namespace ?? '';

  if (page.namespaced) {
    // A namespaced kind whose row does not say which namespace. Nothing here can
    // supply one: the list's own parameter is a *selection* and may name
    // several, which is the exact value that must not be passed onward.
    if (!namesOne(namespace)) return undefined;
    return { cluster, kind: page.kind, namespace, name };
  }

  // A cluster-scoped kind whose row carries a namespace is the payload
  // contradicting itself. Dropping the namespace would produce a URL that works
  // and answers about a different object than the one the row describes, which
  // is the failure mode this file exists for, so it opens nothing instead.
  if (namespace !== '') return undefined;
  return { cluster, kind: page.kind, name };
}

/**
 * Where this object lives in the address bar.
 *
 * Every segment is encoded. A context name is whatever a `KUBECONFIG` says it
 * is, and a kind can be `certificates.cert-manager.io`; the API client makes the
 * same argument about its own templates. The cluster goes through `clusterPath`,
 * the one spelling every link to a cluster uses.
 */
export function objectPath(id: ObjectIdentity): string {
  const parts = [id.kind];
  if (id.namespace !== undefined && id.namespace !== '') parts.push(id.namespace);
  parts.push(id.name);
  return `${clusterPath(id.cluster)}/${parts.map((part) => encodeURIComponent(part)).join('/')}`;
}

/**
 * The parameter that names the row a list has open in its drawer.
 *
 * Named once, here, because it has writers and a reader in different files.
 * `Relations.tsx` and the rail tile write it into the address they point at;
 * `ResourceList.tsx` reads it whenever it arrives, and also writes
 * it itself for as long as its drawer is up - on a row click, and back after
 * an arrival has opened the row - and takes it out on close or when the list
 * is narrowed. Two spellings would be a link that arrives and opens nothing,
 * with no error anywhere.
 */
export const OPEN_PARAMETER = 'open';

/**
 * What `open` names: a row, by name - and by namespace too where the list it
 * lands on is not narrowed to one.
 *
 * Two forms, and the slash tells them apart. `listPath` below writes the name
 * alone, because it also narrows the list to the object's namespace and one
 * name is one row there. The rail tile lands on the list *as the
 * memory left it*, with no namespace selection, and a bare name on a
 * namespaced kind can be several rows - so it writes `<namespace>/<name>`.
 * A slash cannot occur in a DNS-1123 name, which is what makes the two forms
 * distinguishable at all, and the same argument `namesOne` already makes about
 * a comma.
 */
export interface OpenTarget {
  /** Absent means "whatever the list's selection is": match on the name. */
  readonly namespace?: string | undefined;
  readonly name: string;
}

/** The `open` value for an object: namespaced form where it has a namespace. */
export function openValue(target: OpenTarget): string {
  return target.namespace === undefined || target.namespace === ''
    ? target.name
    : `${target.namespace}/${target.name}`;
}

/** Read an `open` value. Both forms; a second slash is nobody's name and reads
 *  as the whole string, which then matches no row and is reported as such. */
export function parseOpen(value: string): OpenTarget {
  const slash = value.indexOf('/');
  if (slash <= 0 || slash === value.length - 1 || value.indexOf('/', slash + 1) !== -1) {
    return { name: value };
  }
  return { namespace: value.slice(0, slash), name: value.slice(slash + 1) };
}

/**
 * Where this object's **list** is, narrowed to its namespace, with the object
 * named for opening.
 *
 * A relation followed from the drawer lands on the related kind's page
 * rather than re-targeting the drawer, because the list is the context the
 * drawer alone does not give: which other ReplicaSets there are, how old this
 * one is beside them. The namespace goes through the list's own `namespace`
 * parameter, which is a selection that here names one; the object goes through
 * `open`, which the list keeps in the address while the drawer is up in
 * the `<namespace>/<name>` form `openValue` writes.
 *
 * `objectPath` above is the other address the same identity has, and the two
 * are not interchangeable: that one is the object's own page (what a
 * reload lands on), this one is the object in its list.
 */
export function listPath(id: ObjectIdentity): string {
  const query = new URLSearchParams();
  if (id.namespace !== undefined && id.namespace !== '') query.set('namespace', id.namespace);
  query.set(OPEN_PARAMETER, id.name);
  return `${clusterPath(id.cluster)}/${encodeURIComponent(id.kind)}?${query.toString()}`;
}

/**
 * Where a link to another object leads from an object's panels: in the drawer,
 * that object's list with it opened (`listPath`); on the object's own page, that
 * object's own page (`objectPath`). One choice, shared by Related and by the
 * Events rows that name an owned object, so the two cannot lead to different places.
 */
export function relatedPath(id: ObjectIdentity, chrome: 'drawer' | 'page'): string {
  return chrome === 'drawer' ? listPath(id) : objectPath(id);
}

/**
 * Where the objects a selector matches are listed: the kind's list, narrowed to
 * the namespace and filtered by the selector.
 *
 * A Deployment's, a ReplicaSet's or a Job's "Selects app=api" row names the pods
 * it controls, and the way down from a controller to its pods is this link
 * rather than retyping the selector into the list. `Relation.selector`'s own
 * contract says why it is there: *"`kind` is set alongside it, so a client can
 * open a filtered list."* The list's `selector` parameter is the one the filter
 * box writes (`FILTER_PARAMETERS`), so this lands on exactly the page the
 * user would have typed, and the namespace is the target's, never this
 * object's - the same rule `listPath` follows.
 */
export function selectorPath(
  cluster: string,
  kind: string,
  namespace: string | undefined,
  selector: string,
): string {
  const query = new URLSearchParams();
  if (namespace !== undefined && namespace !== '') query.set('namespace', namespace);
  query.set('selector', selector);
  return `${clusterPath(cluster)}/${encodeURIComponent(kind)}?${query.toString()}`;
}

/**
 * A list's address, narrowed to one namespace where one is given: the place a
 * breadcrumb segment names. The object page's kind and
 * namespace crumbs point here, and so do the Helm release page's, whose list
 * is `helm`.
 */
export function kindListPath(cluster: string, kind: string, namespace?: string): string {
  const base = `${clusterPath(cluster)}/${encodeURIComponent(kind)}`;
  return namespace === undefined || namespace === ''
    ? base
    : `${base}?${new URLSearchParams({ namespace }).toString()}`;
}
