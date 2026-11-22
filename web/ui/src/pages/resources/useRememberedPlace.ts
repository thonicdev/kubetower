import { useEffect } from 'react';

import { rememberNamespaces, useMenuSaysNamespaced } from '../../shell/namespaceMemory';
import { rememberKind } from '../../shell/viewMemory';

/**
 * Where the person is - the kind and the namespaces - remembered for the
 * cluster, and the setter that writes a namespace choice through to that
 * memory. Returns `chooseNamespaces`.
 */
export function useRememberedPlace(
  cluster: string | undefined,
  kind: string | undefined,
  namespaced: boolean,
  namespaces: readonly string[],
  setNamespaces: (next: readonly string[]) => void,
): (next: readonly string[]) => void {
  /*
    Where the user is, remembered for this cluster.

    In an effect and not during render: this **writes** to a module store that
    other components subscribe to, and a write during render is a render with a
    side effect - React may call it twice, and a listener notified mid-render is
    the update-during-render warning at best.

    Keyed on the kind alone, so re-rendering the same list for a new namespace
    or a new filter does not re-commit: `rememberKind` is idempotent for the
    same value and returns before touching storage.
  */
  useEffect(() => {
    if (cluster === undefined || kind === undefined) return;
    rememberKind(cluster, kind);
  }, [cluster, kind]);

  /*
    **A namespace this address names becomes the cluster's choice**,
    which the menu, the jump box and the rail tile then carry to the next
    namespaced list. `namespaceMemory.ts` has the rules; the two halves here are
    what they mean for this page.

    **An address that names a namespace is remembered**, whoever wrote it - a
    pasted link, a bookmark, a carried menu row - once the kind is known to be
    namespaced: from this list's answer, or from the menu's `Ref.namespaced`
    where the read was refused, because the address still asked for that
    namespace on a kind that has them. **An address that names none writes
    nothing**: a node's *Pods* link or an Overview card asks for every
    namespace on purpose, and the page follows it without undoing the choice.

    In an effect for `rememberKind`'s reason above: a write to a store other
    components subscribe to. Keyed on the joined selection, which is what
    `useUrlList` memoizes on.
  */
  const menuSaysNamespaced = useMenuSaysNamespaced(cluster, kind);
  const scoped = namespaced || menuSaysNamespaced;
  const namespacesKey = namespaces.join(',');
  useEffect(() => {
    if (cluster === undefined || kind === undefined || !scoped || namespacesKey === '') return;
    rememberNamespaces(cluster, kind, namespacesKey.split(','));
  }, [cluster, kind, scoped, namespacesKey]);
  /**
   * **"All namespaces" is cleared where it is chosen**: the picker and the
   * namespace cell write through this, so the choice moves at the moment the
   * person makes it. Not inferred from the address afterwards - a bare address
   * is also every link another page built without a namespace, and those must
   * leave the choice alone. Only drawn on a namespaced kind, so the write
   * needs no scope check of its own.
   */
  const chooseNamespaces = (next: readonly string[]): void => {
    setNamespaces(next);
    if (cluster !== undefined && kind !== undefined) rememberNamespaces(cluster, kind, next);
  };
  return chooseNamespaces;
}
