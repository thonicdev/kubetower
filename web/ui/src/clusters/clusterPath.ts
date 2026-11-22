/**
 * Where a cluster lives in the console's address bar, spelt one way.
 *
 * Two spellings of one cluster would both resolve to the same context on the
 * server, so nothing would error; but React Router's `NavLink` compares
 * pathnames as strings, so a page reached as
 * `/c/kubernetes-admin%40kubernetes/pods` would match no menu row written as
 * `/c/kubernetes-admin@kubernetes/pods`, and `menuMemory.ts` would see the same
 * mismatch.
 *
 * **The spelling is Go's `url.PathEscape` of the real name**, and this is that
 * function, byte for byte. `internal/web/list.go`'s `clusterPath` is the other
 * half; both are held to `clusterPath.cases.json` by a test in each language,
 * so a change to one that is not made to the other fails loudly.
 *
 * - **The real name, not the Headlamp rewrite.** `--` for `/` is not reversible
 *   (a context may be called `a--b`), and a name the router hands back as
 *   `useParams().cluster` should be the name the rest of the console compares
 *   against. `Manager.Resolve` still accepts the rewritten form, for the links
 *   people saved before.
 * - **`@`, `:`, `$`, `&`, `+` and `=` stay as they are**, because RFC 3986
 *   allows them in a path segment and Go does not escape them. kubeadm's default
 *   `kubernetes-admin@kubernetes` reads as itself in the address bar.
 * - **Everything else outside the unreserved set is escaped**, `/` included -
 *   which is what keeps an EKS ARN one segment - and `!`, `'`, `(`, `)` and `*`
 *   too, which `encodeURIComponent` would have left alone and Go does not.
 */
export function clusterSegment(name: string): string {
  let out = '';
  for (const byte of new TextEncoder().encode(name)) {
    const char = String.fromCharCode(byte);
    out += KEPT.test(char) ? char : `%${byte.toString(16).toUpperCase().padStart(2, '0')}`;
  }
  return out;
}

/** `/c/<cluster>`: the cluster's own page, and the prefix of every page on it. */
export function clusterPath(name: string): string {
  return `/c/${clusterSegment(name)}`;
}

/**
 * The canonical address for one that names a cluster in another spelling, or
 * `undefined` when the address is already canonical or cannot be made so.
 *
 * **Why it exists.** Before #94 the rail and a node's Pods link wrote
 * `encodeURIComponent(name)`, and the server's menu wrote the Headlamp form
 * (`/` as `--`, a space as `__`). Bookmarks and history hold both. The server
 * still resolves them (`Manager.Resolve`), but `NavLink` compares the address
 * with its `to` as a string, so on such an address neither the rail tile nor any
 * menu row is marked current. `CanonicalCluster` replaces the address with this
 * one, so the comparison is made on the one spelling.
 *
 * - `pathname` is the location's, raw: the segment as it was written.
 * - `param` is the router's `:cluster`, decoded.
 * - `names` are the contexts the server lists. The cluster is resolved the way
 *   `Manager.Resolve` does it: the name itself, else the context whose Headlamp
 *   form it is. Nothing is resolved against a list that is not there.
 *
 * **It cannot loop, and the reason is one comparison:** `segment === raw`. The
 * address proposed is `/c/<clusterSegment(name)>`, the router reads it back as
 * `name`, and on arriving there the same name gives the same segment, which is
 * then the raw one, so nothing more is proposed.
 *
 * The two checks after it - `routerParam(segment) !== name`, and a segment of
 * `.` or `..` - are **defense only**, and not reachable from the real router:
 * React Router turns every `%2F` in a param into `/`, so no param, and no name
 * resolved from one, holds the literal `%2F` that would fail the first; and a
 * browser's URL parser resolves `.` and `..` (and `%2E%2E`) away before the
 * router sees the path.
 * They are kept because they are what would stop a loop if a future router
 * decoded differently: an address that the router would not read back as the
 * same name is never proposed. Their unit test feeds inputs the router does not
 * produce, and says so.
 */
export function canonicalClusterAddress(
  pathname: string,
  param: string,
  names: readonly string[],
): string | undefined {
  const parts = splitClusterPath(pathname);
  if (parts === undefined) return undefined;
  // `find`, the first match: `/api/clusters` lists contexts in
  // `Manager.Contexts()` order, sorted, and `Manager.Resolve` takes the first
  // context whose Headlamp form matches, so a collision resolves the same way
  // on both sides.
  const name = names.includes(param) ? param : names.find((one) => headlampForm(one) === param);
  if (name === undefined) return undefined;
  const segment = clusterSegment(name);
  if (segment === parts.raw) return undefined;
  if (segment === '.' || segment === '..' || routerParam(segment) !== name) return undefined;
  return `/c/${segment}${parts.rest}`;
}

/**
 * Whether `canonicalClusterAddress` could return an address once the cluster
 * list is known - asked before it is, so that `CanonicalCluster` can hold the
 * page back only where a redirect is possible, and draw a canonical address at
 * once.
 *
 * It is a necessary condition for a redirect, computed from the address alone:
 *
 * - **resolved as itself** (`name === param`): a redirect needs
 *   `clusterSegment(param) !== raw`;
 * - **resolved through the Headlamp form** (`name !== param`):
 *   `headlampForm(name) === param` with `name !== param` needs a `/` or a space
 *   in `name`, so a `--` or a `__` in `param`.
 *
 * So the answer is false exactly when the segment is already `param`'s own
 * canonical spelling and `param` holds neither `--` nor `__`: such an address
 * can only resolve to itself. A canonical address whose name does hold `--` or
 * `__` waits for the list on a cold load, because it might be another context's
 * Headlamp form and only the list can say.
 */
export function clusterAddressMayMove(pathname: string, param: string): boolean {
  const parts = splitClusterPath(pathname);
  if (parts === undefined) return false;
  return clusterSegment(param) !== parts.raw || param.includes('--') || param.includes('__');
}

/** `/c/<raw>` and whatever follows it, with the segment as it was written. */
function splitClusterPath(pathname: string): { raw: string; rest: string } | undefined {
  const match = /^\/c\/([^/]*)(.*)$/.exec(pathname);
  if (match === null) return undefined;
  return { raw: match[1] ?? '', rest: match[2] ?? '' };
}

/** `cluster.MakeDNSFriendly`, which the server's menu wrote until #94. */
function headlampForm(name: string): string {
  return name.replaceAll('/', '--').replaceAll(' ', '__');
}

/** What React Router 8.4.0 hands back as `:cluster` for a segment: `decodePath`
 *  decodes it and re-escapes `/` as `%2F`, then `matchPath` turns every `%2F`
 *  into `/` (`lib/router/utils.js`). The second step is why a name holding a
 *  literal `%2F` does not survive the trip. */
function routerParam(segment: string): string {
  try {
    return decodeURIComponent(segment).replace(/%2F/g, '/');
  } catch {
    return segment;
  }
}

/** What `url.PathEscape` leaves as it is: RFC 3986's unreserved characters, and
 *  the sub-delimiters Go allows in a segment (`shouldEscape`, `encodePathSegment`). */
const KEPT = /^[A-Za-z0-9\-._~$&+:=@]$/;
