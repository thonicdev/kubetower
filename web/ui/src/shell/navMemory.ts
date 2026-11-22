import type { NavGroup, NavGroupSnapshot } from '../api';

/**
 * What a deferred menu group answered, per cluster, remembered across reloads.
 *
 * ## The half of the deferral that did not hold
 *
 * The custom-resource heading is fetched only when its disclosure opens, because
 * filling it lists the cluster's CustomResourceDefinitions - tens of megabytes
 * and over half a second on a large cluster - and `Menu.tsx` carries that
 * argument. Having paid once, a person should not pay again on F5: TanStack's
 * in-memory cache alone makes the five-minute `staleTime` on `navGroupQuery`
 * per tab lifetime, not per browser, and every reload with the heading open
 * would be a fresh discovery read.
 *
 * ## What is stored is kilobytes, and the megabytes never cross the wire
 *
 * The 23.7 MB is what the **server** reads to produce the answer. What arrives
 * here, and what is written down, is the menu entries - kind names, paths, the
 * API groups they sit under, and the server's `note` when there are none. One
 * key per cluster and group, `kubetower.nav.<cluster>.<group>`, because the
 * custom kinds installed on prod are not the ones installed on staging and a
 * snapshot that crossed over would draw a link to a kind the cluster does not
 * serve.
 *
 * ## It seeds the query; it does not replace it
 *
 * `Menu.tsx` hands the snapshot to `useNavGroup` as TanStack's `initialData`
 * with `initialDataUpdatedAt`, so the heading is drawn from it at once and the
 * same five-minute `staleTime` decides whether to ask again in the background.
 * A newly installed CRD therefore still appears without an F5, and nobody waits
 * on a heading they opened before the reload. **Not an idle prefetch**: a
 * cluster where the heading was never opened has no snapshot and asks nothing,
 * which is the case the deferral exists for.
 *
 * Only an `ok` answer is written, `note` included. A cluster that could not be
 * asked is a fact about that moment, and drawing *Could not be asked* from a
 * snapshot on the next visit would report an outage that ended. The three
 * answers stay three: a snapshot of *serves none* carries the sentence and is
 * drawn as it, never as an empty heading.
 *
 * ## The same shape as `menuMemory.ts` and `viewMemory.ts`
 *
 * A module-level cache, every read and write wrapped, absence tolerated. A
 * private window, blocked site data or a locked-down profile makes the answer
 * last as long as the tab - which is the memory being lost and never the menu,
 * and it is the bargain the other two strike in the same words. What is read
 * back is checked field by field rather than trusted: it was written by a build
 * that may be older than this one, and by a user who can edit it.
 */

const PREFIX = 'kubetower.nav';

/** Read once per key; replaced by a write. `null` is "read, and nothing there",
 *  so storage is not re-parsed on every render of the menu. */
const cache = new Map<string, NavGroupSnapshot | null>();

function keyOf(cluster: string, group: string): string {
  return `${PREFIX}.${cluster}.${group}`;
}

/** The last `ok` answer for this group on this cluster, and when it was given -
 *  or `undefined` where the heading was never filled in this browser. */
export function rememberedGroup(cluster: string, group: string): NavGroupSnapshot | undefined {
  const key = keyOf(cluster, group);
  let snapshot = cache.get(key);
  if (snapshot === undefined) {
    snapshot = readStorage(key);
    cache.set(key, snapshot);
  }
  return snapshot ?? undefined;
}

/**
 * Write an answer down, stamped with when the query received it.
 *
 * `at` is the query's own `dataUpdatedAt` rather than `Date.now()` here, so the
 * snapshot and TanStack agree on the answer's age. A query seeded from the
 * snapshot hands back the snapshot itself on its first render, stamped with the
 * snapshot's time - the same moment, not a new fact, and it is not re-written.
 */
export function rememberGroup(cluster: string, group: string, answer: NavGroup, at: number): void {
  const key = keyOf(cluster, group);
  if (cache.get(key)?.at === at) return;
  const snapshot: NavGroupSnapshot = { at, group: answer };
  cache.set(key, snapshot);
  try {
    localStorage.setItem(key, JSON.stringify(snapshot));
  } catch {
    // See the header: the memory is what is lost, never the menu.
  }
}

/**
 * Let go of every snapshot for a cluster that is not in `clusters` - the list
 * the server just answered.
 *
 * Nothing else removes one, so without this a desktop that cycles through
 * short-lived clusters would keep every one it had opened a heading on, for
 * ever, in an origin
 * whose quota the dock's own tab list shares: the write refused when it fills
 * is that one, and silently. The caller hands over an `ok` list only - a list
 * that could not be read says nothing about which clusters exist.
 *
 * A cluster is matched as a whole prefix of the key, `<prefix>.<cluster>.`,
 * because a context name may hold a dot and the key cannot be split back into
 * its two halves. That errs towards keeping: a snapshot of `prod.eu` survives
 * while `prod` is listed. Never towards removing one that is in use.
 */
export function forgetOtherClusters(clusters: readonly string[]): void {
  const kept = clusters.map((cluster) => `${PREFIX}.${cluster}.`);
  const stale = (key: string): boolean =>
    key.startsWith(`${PREFIX}.`) && !kept.some((prefix) => key.startsWith(prefix));
  for (const key of [...cache.keys()]) {
    if (stale(key)) cache.delete(key);
  }
  try {
    const keys: string[] = [];
    for (let at = 0; at < localStorage.length; at++) {
      const key = localStorage.key(at);
      if (key !== null && stale(key)) keys.push(key);
    }
    for (const key of keys) localStorage.removeItem(key);
  } catch {
    // See the header: storage that will not be read holds nothing to let go of.
  }
}

function readStorage(key: string): NavGroupSnapshot | null {
  try {
    const raw = localStorage.getItem(key);
    if (raw === null) return null;
    const parsed: unknown = JSON.parse(raw);
    if (!isRecord(parsed)) return null;
    if (typeof parsed['at'] !== 'number' || !Number.isFinite(parsed['at'])) return null;
    const group = readGroup(parsed['group'], true);
    if (group === undefined) return null;
    return { at: parsed['at'], group };
  } catch {
    return null;
  }
}

/**
 * One group, checked field by field. `nested` is the one level `api/openapi.yaml`
 * allows - "one nested level, and no more" - so a snapshot cannot smuggle a
 * deeper tree into the menu than the server would ever send.
 */
function readGroup(value: unknown, nested: boolean): NavGroup | undefined {
  if (!isRecord(value)) return undefined;
  if (typeof value['key'] !== 'string' || typeof value['label'] !== 'string') return undefined;
  if (!Array.isArray(value['items'])) return undefined;

  const items = value['items'].flatMap((item): NavGroup['items'] => {
    if (!isRecord(item) || typeof item['text'] !== 'string') return [];
    const href = typeof item['href'] === 'string' ? item['href'] : undefined;
    const hint = typeof item['hint'] === 'string' ? item['hint'] : undefined;
    return [{ text: item['text'], ...(href === undefined ? {} : { href }), ...(hint === undefined ? {} : { hint }) }];
  });

  const group: NavGroup = {
    key: value['key'],
    label: value['label'],
    // Never deferred: a snapshot is, by construction, the filled answer.
    deferred: false,
    items,
    ...(typeof value['note'] === 'string' ? { note: value['note'] } : {}),
    // Carried like the note, or after F5 the heading drawn from the snapshot
    // would have quietly lost the line saying which kinds it leaves out.
    ...(typeof value['accessNote'] === 'string' ? { accessNote: value['accessNote'] } : {}),
  };
  return withGroups(group, value, nested);
}

function withGroups(group: NavGroup, value: Record<string, unknown>, nested: boolean): NavGroup {
  if (!nested || !Array.isArray(value['groups'])) return group;
  const groups = value['groups'].flatMap((sub) => {
    const read = readGroup(sub, false);
    return read === undefined ? [] : [read];
  });
  return { ...group, groups };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

/** Test seam. Leaves the module as a fresh import would - the cache included,
 *  or one test's snapshot is still in memory for the next. */
export function resetNavMemory(): void {
  cache.clear();
}
