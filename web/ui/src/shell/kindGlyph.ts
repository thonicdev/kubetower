import type { IconName } from '../components/Icon';
import { EXTENSION, FORWARDS } from '../router/pages';

/**
 * Which glyph a kind and a group draw in the menu.
 *
 * ## Why this is a module and not a `Record` inside `Menu.tsx`
 *
 * It is the one thing in the menu that can be wrong without anything failing.
 * A kind that falls through to `resource` looks deliberate, and a kind that
 * resolved to the wrong shape looks deliberate too - so the question "does every
 * kind the server can send have an answer here" has to be askable by a test,
 * against the server's own list rather than against a copy of it. That is
 * `kindGlyph.test.ts`, which reads `internal/resources/kinds.go`.
 *
 * ## One shape per concept, not one per kind
 *
 * Not one symbol per kind: 36 kinds are answered by 22 shapes,
 * and the difference is deliberate rather than saved effort:
 *
 * - `replicasets` and `replicationcontrollers` are the same idea spelt two years
 *   apart. Two hand-drawn variants of "several copies of a thing" would differ
 *   by accident rather than by meaning, and an operator would read a difference
 *   that is not there.
 * - `roles` and `clusterroles`, `rolebindings` and `clusterrolebindings`,
 *   `endpoints` and `endpointslices`, the two webhook configurations: each pair
 *   differs in *scope*, which the name already says and a 15px glyph cannot.
 * - `ingressclasses`, `priorityclasses`, `runtimeclasses` and `storageclasses`
 *   are the same noun. They sit in three different groups, so the shared shape
 *   is what makes "this is a class of something" visible across the column.
 *
 * What is **not** shared is any pair an operator picks between under time
 * pressure. `secrets` and `configmaps` are a lock and a document; `pods`,
 * `deployments`, `statefulsets` and `daemonsets` are four different shapes,
 * because that group is where the eye goes first and most often.
 *
 * ## The fallback is `resource`, and it is a rule rather than a gap
 *
 * `Metrics.tsx`'s `GLYPH` already works this way: a key the server added and the
 * map has not is still drawn, with its label, rather than vanishing. Here the
 * stake is higher - a custom resource kind is a name this product has never seen
 * and cannot have a shape for, and there can be 140 of them on one cluster.
 * Drawing nothing would leave the custom rows a pixel-ragged column beside the
 * built-in ones.
 */

/**
 * The built-in kinds, by the slug that is in their href.
 *
 * Keyed on the slug and **not** on the label, for `menuMemory`'s reason one
 * layer along: a label is a rendering and is free to change - a rename, a
 * translation, a count - while the slug is `Descriptor.Kind` in
 * `internal/resources/kinds.go` and is what the route is built from.
 */
const KIND: Readonly<Record<string, IconName>> = {
  // Workloads
  pods: 'pod',
  deployments: 'deployment',
  statefulsets: 'statefulset',
  daemonsets: 'daemonset',
  replicasets: 'replicaset',
  replicationcontrollers: 'replicaset',
  jobs: 'job',
  cronjobs: 'cronjob',

  // Network
  services: 'service',
  ingresses: 'ingress',
  endpoints: 'endpoint',
  endpointslices: 'endpoint',
  ingressclasses: 'class',
  networkpolicies: 'policy',

  // Configuration
  configmaps: 'config',
  secrets: 'secret',
  resourcequotas: 'quota',
  limitranges: 'quota',
  horizontalpodautoscalers: 'scale',
  poddisruptionbudgets: 'policy',
  priorityclasses: 'class',
  runtimeclasses: 'class',
  leases: 'lease',
  mutatingwebhookconfigurations: 'webhook',
  validatingwebhookconfigurations: 'webhook',

  // Access Control
  serviceaccounts: 'account',
  clusterroles: 'role',
  roles: 'role',
  clusterrolebindings: 'binding',
  rolebindings: 'binding',

  // Storage
  persistentvolumeclaims: 'volume',
  persistentvolumes: 'disk',
  storageclasses: 'class',

  // The three the server folds into a "Cluster" group of its own.
  nodes: 'node',
  namespaces: 'namespace',
  events: 'event',
};

/**
 * The API groups whose owner this console recognizes.
 *
 * Custom resources get the group glyph of their API group's owner where it is
 * known (cert-manager, Argo, Calico) and `resource` otherwise. So the
 * answer is one of the **group** shapes and never a kind's: this map says what
 * kind of thing an operator installed, which is all that can honestly be claimed
 * about a name this product has never seen. cert-manager issues credentials, so
 * it answers with Access Control's shield; Calico is networking; Argo runs
 * things.
 *
 * Matched on the suffix, because one vendor owns several groups -
 * `cert-manager.io` and `acme.cert-manager.io`, `argoproj.io` and
 * `workflows.argoproj.io` - and listing them exhaustively is a list that is
 * wrong the next time one is added.
 *
 * It is an open map: a fourth vendor worth recognizing is one more line, and
 * until then a
 * kind it does not match draws `resource`, which is correct rather than absent.
 */
const OWNER: readonly (readonly [string, IconName])[] = [
  ['cert-manager.io', 'access'],
  ['argoproj.io', 'workloads'],
  ['projectcalico.org', 'network'],
  ['tigera.io', 'network'],
];

/**
 * The five groups, plus the three the server makes itself.
 *
 * `cluster` is `apimount.go`'s own heading for the kinds `kinds.go` leaves
 * ungrouped, `custom-resources` is `navKey("Custom Resources")`, and `extra` is
 * `navKey("Extra")` - `extranav.go`'s heading at the foot of the menu. The keys
 * are `navKey(label)` - lower case, spaces hyphenated - so "Access Control" is
 * `access-control` and a heading renamed on the server draws `resource` here
 * rather than nothing.
 *
 * `extra` is the one group whose entries draw its own shape too (see
 * `glyphForHref`): an extra is a page plugged into the console, not a kind, and
 * the custom resources' cube would make the Extra group read as more custom
 * resources.
 */
const GROUP: Readonly<Record<string, IconName>> = {
  workloads: 'workloads',
  network: 'network',
  configuration: 'settings',
  'access-control': 'access',
  storage: 'disk',
  cluster: 'cluster',
  'custom-resources': 'resource',
  extra: 'extra',
};

/** The glyph for a group heading, by the key the server chose. */
export function glyphForGroup(key: string): IconName {
  return GROUP[key] ?? 'resource';
}

/**
 * The five headings `kinds.go` declares, by `navKey`: where the server also
 * files the Kubernetes project's own kinds.
 */
const SECTIONS: ReadonlySet<string> = new Set([
  'workloads',
  'network',
  'configuration',
  'access-control',
  'storage',
]);

/**
 * The glyph for one entry, by its href and the heading it is under.
 *
 * As `glyphForHref`, except that a kind it has no shape for draws **the shape
 * of the heading it is filed under**, where that heading is one of the five.
 * The server files the Kubernetes project's own kinds there - a Gateway under
 * Network, a VolumeAttachment under Storage - and a cube among them would read
 * as a custom resource that wandered in. Asked of the heading rather than of
 * the API group, so this file does not keep a second copy of the server's
 * section table (`internal/customresources/project.go`) to drift from it. The
 * Cluster heading and Custom Resources keep `glyphForHref`'s answer: Helm is
 * not a kind of the cluster, and a vendor's kind is what the cube is for.
 */
export function glyphForEntry(href: string | undefined, heading: string | undefined): IconName {
  const glyph = glyphForHref(href);
  if (glyph !== 'resource' || heading === undefined || !SECTIONS.has(heading)) return glyph;
  // A row with no kind route keeps the generic shape: it names nothing filed
  // anywhere, and Menu's dead row is drawn as one.
  if (kindOfHref(href) === undefined) return glyph;
  return glyphForGroup(heading);
}

/**
 * The glyph for one kind, by the slug in its href.
 *
 * A built-in kind first, then its API group's owner, then `resource`. The order
 * is what matters: a custom kind cannot collide with a built-in slug, because a
 * custom slug is `plural.group` and every built-in one is a bare plural - which
 * is `customresources.go`'s own distinction and the reason this asks the map
 * rather than testing for a dot.
 */
export function glyphForKind(slug: string): IconName {
  const known = KIND[slug];
  if (known !== undefined) return known;

  const dot = slug.indexOf('.');
  if (dot < 0) return 'resource';
  const apiGroup = slug.slice(dot + 1);
  for (const [suffix, glyph] of OWNER) {
    if (apiGroup === suffix || apiGroup.endsWith(`.${suffix}`)) return glyph;
  }
  return 'resource';
}

/**
 * The kind slug inside a menu href, or `undefined` where there is not one.
 *
 * `/c/{cluster}/{kind}`, and **the query string is cut off first** for
 * `menuMemory.groupHoldsPath`'s reason, quoted there: `viewMemory.remembered`
 * appends the sort an operator last used, so a menu item's href is
 * `/c/prod/pods?sort=age` as often as `/c/prod/pods`. A slug read with the query
 * still on it matches nothing in the map, and every sorted kind would silently
 * drop back to `resource` - a failure that never errors and looks like a design.
 */
export function kindOfHref(href: string | undefined): string | undefined {
  if (href === undefined) return undefined;
  const path = href.split(/[?#]/)[0] ?? '';
  // Unanchored, and the same expression `menuMemory.KIND_ROUTE` uses: the two
  // are asking the same question of the same strings, and a stricter one here
  // would be a second rule about what a kind route looks like.
  return /^\/c\/[^/]+\/([^/]+)/.exec(path)?.[1];
}

/**
 * The glyph for one menu entry, by its href.
 *
 * A kind route answers as its kind (`glyphForKind`). An extra's page answers
 * with the Extra heading's own shape: its href is `extraPageURL` in
 * `extranav.go`, `/c/{cluster}/x/{key}`, so the segment `kindOfHref` reads is
 * `x` - `EXTENSION`, the segment `router/pages.ts` reserves for extras - and
 * not a kind at all. Asked of the map it would fall through to `resource`, and
 * every extra would be drawn as one more custom resource.
 *
 * **One shape for every extra, and it is the heading's.** The extension
 * contract carries no icon, so nothing an extra could say would tell this function which shape to
 * draw. It reads the heading's answer rather than naming the glyph a second
 * time, so the heading and its entries cannot be redrawn apart.
 *
 * Compared against the whole segment: a custom kind whose plural begins with an
 * `x` is a kind, and `kindOfHref` returns `xwidgets.example.com`, not `x`.
 */
export function glyphForHref(href: string | undefined): IconName {
  const segment = kindOfHref(href);
  if (segment === EXTENSION) return glyphForGroup('extra');
  // The open port-forwards: a page under Network and not
  // a kind, so not in the kinds' map; the shape the Ports panel's control is.
  if (segment === FORWARDS) return 'forward';
  return glyphForKind(segment ?? '');
}
