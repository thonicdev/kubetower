/**
 * Where the operator was, per cluster, and how a kind was sorted.
 *
 * ## Two memories, one store
 *
 * *Where a person was, per cluster*: without it every rail tile would link to
 * the bare overview, so switching cluster and coming back would lose the page.
 * *The sort a kind was left with*: in component state it would be discarded
 * whenever the scope changed, surviving exactly as long as nobody navigated.
 *
 * They are one store because they are one question — *what was I looking at* —
 * and because the discarding rule that made the second necessary is the thing
 * this file fixes: a sort is dropped on a scope change **because a sort belongs
 * to one kind's columns**, and carrying it to the next kind sorts by a column
 * that is not there. Remembering it *per kind* keeps that rule and stops losing
 * the answer.
 *
 * ## And the drawer that was open there
 *
 * Back on a cluster, a person is back on the drawer they were on. The drawer
 * is component state and `Drawer.tsx` empties it on every navigation - which
 * is right: a drawer must never carry one cluster's object, with its verbs,
 * onto another cluster's page.
 *
 * So the object is remembered **here** and never carried: the rail tile puts it
 * in the address as `?open=`, and the destination list re-opens it for its own
 * cluster, exactly as a relation followed from the drawer already arrives.
 * `open` is one object per cluster, beside the kind, and it is **the drawer as
 * the page was left**:
 *
 * - **kept** when a navigation empties the drawer - that is the case the memory
 *   exists for;
 * - **forgotten** when the operator shuts it - × or Escape - because coming
 *   back to a drawer somebody deliberately closed is worse than forgetting.
 *   `ShellContext.tsx` is where the two paths are told apart;
 * - **left behind** when this cluster is looked at through another kind: a
 *   drawer belongs to the list it was opened beside, and the tile that returns
 *   to Deployments must not bring a pod along.
 *
 * ## localStorage, per viewer, and it may not be there
 *
 * The same shape as `menuMemory.ts`, which does this for open menu groups: a
 * module-level snapshot, a listener list, and every read and write wrapped.
 * A private window, blocked site data or a locked-down profile makes this last
 * as long as the tab — which is the memory being lost and never the page.
 * Nothing is reported: there is nothing the operator could do about it and
 * nothing on screen is wrong.
 *
 * ## What it deliberately does not remember
 *
 * The namespace selection and the filter box. Those are in the **address**,
 * which is where a thing somebody can paste to a colleague belongs, and
 * a store that also held them would be a second answer to the same question —
 * the one that wins on a reload and loses on a pasted link. The namespace that
 * follows the operator from list to list is
 * `namespaceMemory.ts`, and it keeps that rule: it writes the next link's
 * address and never overrides the one on screen.
 */

const KEY = 'kubetower.view';

/** What is remembered for one cluster. */
interface ClusterView {
  /** The last kind opened there, as the route spells it. */
  readonly kind?: string;
  /** The sort each kind was left with, by kind. Per kind, because a sort is
   *  about one kind's columns. */
  readonly sorts?: Readonly<Record<string, Sort>>;
  /** The object whose drawer was open, on `kind`'s list. One and not one per
   *  kind: the tile returns to one page. */
  readonly open?: OpenObject;
}

/** The object a drawer was open on. Structural for the same reason `Sort` is,
 *  and `cluster` is the key rather than a member: the identity the object page
 *  uses carries it, and this store is already keyed by it. */
export interface OpenObject {
  readonly kind: string;
  /** Absent on a cluster-scoped kind, and absent is not the empty string: the
   *  two make different addresses. */
  readonly namespace?: string | undefined;
  readonly name: string;
}

/** A sort, in the shape the grid uses. Stored structurally rather than as the
 *  grid's own type so that a change to that type is a compile error here rather
 *  than a shape in somebody's browser that no longer parses. */
export interface Sort {
  /** `key` and not `column`, because that is what `GridSort` calls it: one
   *  spelling for one fact, and this file is the one storing it. */
  readonly key: string;
  readonly direction: 'asc' | 'desc';
}

type Memory = Readonly<Record<string, ClusterView>>;

let snapshot: Memory | undefined;
const listeners = new Set<() => void>();

/** Subscribe, for `useSyncExternalStore`. */
export function watchViewMemory(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** Everything remembered. One object, so a component reading two facts about a
 *  cluster subscribes once. */
export function viewMemory(): Memory {
  snapshot ??= readStorage();
  return snapshot;
}

/** The kind this cluster was last looked at, or undefined. */
export function lastKind(cluster: string): string | undefined {
  return viewMemory()[cluster]?.kind;
}

/** The sort this kind was left with in this cluster, or undefined. */
export function lastSort(cluster: string, kind: string): Sort | undefined {
  return viewMemory()[cluster]?.sorts?.[kind];
}

/** The object whose drawer was open on this cluster, or undefined. */
export function lastOpen(cluster: string): OpenObject | undefined {
  return viewMemory()[cluster]?.open;
}

/** Remember that this cluster is being looked at through this kind. A
 *  different kind leaves the open object behind - see the header. */
export function rememberKind(cluster: string, kind: string): void {
  const current = viewMemory();
  if (current[cluster]?.kind === kind) return;
  commit({ ...current, [cluster]: { ...withoutOpen(current[cluster]), kind } });
}

/** Remember which object's drawer is open on this cluster. */
export function rememberOpen(cluster: string, open: OpenObject): void {
  const current = viewMemory();
  const held = current[cluster]?.open;
  if (
    held !== undefined &&
    held.kind === open.kind &&
    held.name === open.name &&
    (held.namespace ?? '') === (open.namespace ?? '')
  ) {
    return;
  }
  const stored: OpenObject =
    open.namespace === undefined || open.namespace === ''
      ? { kind: open.kind, name: open.name }
      : { kind: open.kind, namespace: open.namespace, name: open.name };
  commit({ ...current, [cluster]: { ...current[cluster], open: stored } });
}

/** Forget it: the operator closed the drawer, or the object is no longer
 *  there. The kind and the sorts stay - the page is still remembered. */
export function forgetOpen(cluster: string): void {
  const current = viewMemory();
  if (current[cluster]?.open === undefined) return;
  commit({ ...current, [cluster]: withoutOpen(current[cluster]) });
}

/** The entry without its open object. Spelled out rather than destructured
 *  away, so that no unused binding has to be named. */
function withoutOpen(view: ClusterView | undefined): ClusterView {
  const out: { kind?: string; sorts?: Readonly<Record<string, Sort>> } = {};
  if (view?.kind !== undefined) out.kind = view.kind;
  if (view?.sorts !== undefined) out.sorts = view.sorts;
  return out;
}

/** Remember how this kind was sorted — or that it was unsorted, which is a
 *  state somebody chose and not the absence of one. */
export function rememberSort(cluster: string, kind: string, sort: Sort | undefined): void {
  const current = viewMemory();
  const sorts = { ...current[cluster]?.sorts };
  if (sort === undefined) {
    if (!(kind in sorts)) return;
    delete sorts[kind];
  } else {
    if (sorts[kind]?.key === sort.key && sorts[kind]?.direction === sort.direction) return;
    sorts[kind] = sort;
  }
  commit({ ...current, [cluster]: { ...current[cluster], sorts } });
}

function commit(next: Memory): void {
  snapshot = next;
  try {
    localStorage.setItem(KEY, JSON.stringify(next));
  } catch {
    // See the header: the memory is what is lost, never the page.
  }
  for (const listener of [...listeners]) listener();
}

/**
 * Read what is stored, defensively.
 *
 * Every field is checked rather than trusted. What is in `localStorage` was
 * written by a build that may be older than this one, and by a user who can
 * edit it — so a shape that does not match is treated as nothing rather than as
 * a crash on the first render of the shell.
 */
function readStorage(): Memory {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw === null) return {};
    const parsed: unknown = JSON.parse(raw);
    if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) return {};

    const out: Record<string, ClusterView> = {};
    for (const [cluster, value] of Object.entries(parsed as Record<string, unknown>)) {
      if (value === null || typeof value !== 'object') continue;
      const entry = value as { kind?: unknown; sorts?: unknown; open?: unknown };
      const view: { kind?: string; sorts?: Record<string, Sort>; open?: OpenObject } = {};
      if (typeof entry.kind === 'string') view.kind = entry.kind;
      const open = readOpen(entry.open);
      if (open !== undefined) view.open = open;
      if (entry.sorts !== null && typeof entry.sorts === 'object' && !Array.isArray(entry.sorts)) {
        const sorts: Record<string, Sort> = {};
        for (const [kind, sort] of Object.entries(entry.sorts as Record<string, unknown>)) {
          if (sort === null || typeof sort !== 'object') continue;
          const candidate = sort as { key?: unknown; direction?: unknown };
          if (typeof candidate.key !== 'string') continue;
          if (candidate.direction !== 'asc' && candidate.direction !== 'desc') continue;
          sorts[kind] = { key: candidate.key, direction: candidate.direction };
        }
        view.sorts = sorts;
      }
      out[cluster] = view;
    }
    return out;
  } catch {
    return {};
  }
}

/** One stored open object, or nothing where any member is not what it says. A
 *  namespace that is present must be a string; absent is a cluster-scoped
 *  object and is kept absent. */
function readOpen(value: unknown): OpenObject | undefined {
  if (value === null || typeof value !== 'object') return undefined;
  const candidate = value as { kind?: unknown; namespace?: unknown; name?: unknown };
  if (typeof candidate.kind !== 'string' || typeof candidate.name !== 'string') return undefined;
  if (candidate.namespace === undefined) return { kind: candidate.kind, name: candidate.name };
  if (typeof candidate.namespace !== 'string') return undefined;
  return { kind: candidate.kind, namespace: candidate.namespace, name: candidate.name };
}

/** Test seam. Leaves the module as a fresh import would — the cached snapshot
 *  included, or one test's memory is still in memory for the next. */
export function resetViewMemory(): void {
  snapshot = undefined;
  try {
    localStorage.removeItem(KEY);
  } catch {
    // Nothing to clear where nothing could be written.
  }
}
