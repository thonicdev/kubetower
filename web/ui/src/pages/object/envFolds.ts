import { useCallback, useSyncExternalStore } from 'react';

/**
 * Which of a pod's long variable lists the user opened, remembered while
 * they move around the object.
 *
 * ## The defect this exists for
 *
 * A pod's Containers section listing every variable open pushes everything
 * under it off the screen when there are dozens of them (one Argo CD server pod
 * declares 51). So a list past `ENV_FOLD_AFTER` rows is folded behind its count
 * (`Containers.tsx`, `Fold`), and opening one is a gesture that has to last.
 *
 * ## Why a module and not a `useState`
 *
 * The panel that draws the lists is **unmounted by a tab move**: `ObjectDetail`
 * draws the Manifest or Activity panel *instead of* the details body, not
 * on top of it. A `useState` in `Containers` is therefore destroyed by the very
 * gesture this exists for - open the environment, glance at the Manifest, come
 * back to a folded list. A `useState` in a route component is destroyed on
 * navigation; a module-level set is not. `menuMemory.ts` makes the same
 * argument for the menu's groups, at a smaller scale.
 *
 * ## In memory, and never in storage
 *
 * Unlike the menu's arrangement this is not a preference to carry across a
 * reload: it is *what is open on this object now*. A reload reading a list back
 * open would be the visit outliving itself, and a key per pod per container
 * written to `localStorage` is a list of every workload this browser looked at,
 * kept for nobody. So the set lives as long as the page does, and a reload
 * starts folded again - which is also the one rule `testing/setup.ts` needs no
 * prefix for.
 *
 * ## Keyed by the object, then the container, then the list
 *
 * A drawer re-targeted at another pod must not open that pod's lists because
 * this pod's were - the same address discipline the reveal keeps, for the
 * same reason: the cell is the same component at the same position, and
 * the only thing that tells the two pods apart is the address. So the key is the
 * cluster, the kind, the namespace and the name, then the container with its
 * init mark (an init and an app container may share a name), then which list.
 *
 * ## Bounded
 *
 * Each key is one short string, and a long session opening lists on hundreds of
 * pods is the only way the set grows. It is cut at `REMEMBERED`, oldest first -
 * a `Set` keeps insertion order - so a page left open for a week does not hold
 * every list it ever opened. Losing the oldest costs a click on a pod nobody has
 * looked at for a hundred lists.
 */

/** How many opened lists are kept before the oldest is forgotten. */
const REMEMBERED = 200;

/** The keys of the lists drawn open. Replaced, never mutated:
 *  `useSyncExternalStore` compares snapshots by identity, and a set changed in
 *  place is a change nobody re-renders for. */
let opened: ReadonlySet<string> = new Set();

const listeners = new Set<() => void>();

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function snapshot(): ReadonlySet<string> {
  return opened;
}

/** Open or close one list. A no-op when it is already so, because notifying
 *  every subscriber for nothing re-renders every container card for nothing. */
export function setEnvFoldOpen(key: string, isOpen: boolean): void {
  if (opened.has(key) === isOpen) return;
  const next = new Set(opened);
  if (isOpen) {
    next.add(key);
    for (const oldest of next) {
      if (next.size <= REMEMBERED) break;
      next.delete(oldest);
    }
  } else {
    next.delete(key);
  }
  opened = next;
  for (const listener of listeners) listener();
}

/** Whether a list is remembered open. Exported for the tests. */
export function envFoldIsOpen(key: string): boolean {
  return opened.has(key);
}

/** Forget every opened list. `testing/setup.ts` calls it after each test,
 *  since a module-level set outlives the tree that wrote it. */
export function resetEnvFolds(): void {
  if (opened.size === 0) return;
  opened = new Set();
  for (const listener of listeners) listener();
}

/**
 * The address of one list: the object, the container and which list. A JSON
 * array rather than a joined string, so no name containing the separator can
 * make two addresses one.
 */
export function envFoldKey(
  object: { cluster: string; kind: string; namespace: string | undefined; name: string },
  container: { name: string; init: boolean },
  list: string,
): string {
  return JSON.stringify([
    object.cluster,
    object.kind,
    object.namespace ?? '',
    object.name,
    container.init ? 'init' : 'app',
    container.name,
    list,
  ]);
}

/** Whether the list at `key` is open, and the gesture that changes it. */
export function useEnvFold(key: string): [boolean, (isOpen: boolean) => void] {
  const set = useSyncExternalStore(subscribe, snapshot, snapshot);
  const change = useCallback(
    (isOpen: boolean) => {
      setEnvFoldOpen(key, isOpen);
    },
    [key],
  );
  return [set.has(key), change];
}
