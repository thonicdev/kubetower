import { useRef, useState } from 'react';

import type { ListPage, Row } from '../../api';
import { copyText } from '../../components/CopyButton';
import type { RowMenuAnchor } from '../../components/DataGrid';
import { useUrlParameters } from '../../components/useUrlParameter';
import { identityOf, OPEN_PARAMETER, type ObjectIdentity } from '../object/address';
import type { useOpenInDrawer } from './useOpenInDrawer';

/** The two parameters a row click writes together. Module-level, because
 *  `useUrlParameters` keys its memo on the array's identity. */
const OPEN_AND_TAB = [OPEN_PARAMETER, 'tab'] as const;

type Drawer = ReturnType<typeof useOpenInDrawer>;

/**
 * What a row does: the drawer a click opens, the menu it offers, and Copy
 * name's confirmation.
 */
export function useRowActions({
  cluster,
  kind,
  showInDrawer,
  gone,
  setGone,
}: {
  cluster: string | undefined;
  kind: string | undefined;
} & Pick<Drawer, 'showInDrawer' | 'gone' | 'setGone'>) {
  const [, setOpenAndTab] = useUrlParameters(OPEN_AND_TAB);

  /*
    The object panel's chosen tab, forgotten by a row click (`setOpenAndTab`).

    The tab lives in the address and a row click is a query-only change of it,
    so the tab would survive from one object to the next: open pod A on
    Manifest, click pod B, and B would open on Manifest. A fresh object lands on
    its summary instead. Keeping the tab across objects is what comparing five
    manifests wants, and that is the trade taken here: **a row click forgets the tab; an address that arrives with one
    still lands on it**, because the panel reads whatever the address says. A
    modifier on the click could keep it later; today nothing does.

    It is cleared in the same navigation that writes `open`, unconditionally:
    clearing an absent parameter changes nothing in the address.
  */

  /**
   * What opening one row does, or nothing where the row names no single object.
   *
   * The resource opens in a panel beside the table, the table keeps refreshing
   * behind it, and closing the panel returns you to exactly where you were. So
   * this does **not** navigate - the object's own route exists and is reached by a link or
   * a reload, not by clicking a row, or the list under the panel would be gone
   * and the two-second refresh with it.
   *
   * The identity is built from the payload's `namespaced` flag and the row's own
   * namespace, never from this page's `namespace` parameter: that parameter is a
   * *selection* in the address and a single optional string in the contract,
   * and handing either to something that wants one namespace would address the
   * wrong object.
   *
   * **Returning `undefined` rather than a function that does nothing** is what
   * keeps the affordance honest: `DataGrid` gives a row a tabstop and a pointer
   * only where there is something to open, so an unaddressable row does not
   * advertise a panel it cannot show.
   */
  const rowActionFor = (listPage: ListPage) => (row: Row) => {
    const identity = identityOf(cluster, listPage, row);
    // An ambiguous identity opens nothing. Not an error and not a guess: nothing
    // here knows which object the user meant.
    if (identity === undefined) return undefined;
    return () => {
      // Only when there is one to drop: writing the same address again is a
      // navigation with nothing in it, and `useUrlParameter` replaces rather
      // than pushes, so it would be invisible - but it is still a render.
      // A notice about an arrival that opened nothing is answered by opening
      // something.
      if (gone !== undefined) setGone(undefined);
      // The identity is also what `viewMemory` remembers for the rail tile
      // the drawer that was open here is where the tile returns to,
      // unless the user closes it.
      const value = showInDrawer(identity);
      // The object into the address and the tab out of it, in one navigation:
      // two setters in one handler would each start from the same stale query
      // and the second would undo the first. Replaced, never pushed, like every
      // parameter here - Back is the page before the list, not the row before.
      setOpenAndTab({ open: value, tab: '' });
    };
  };

  /*
    **The row's menu**: which row it is open on, and where it was asked for.
    One at a time, held here rather than in the grid, because the menu reads the object and its verb opens the plan
    dialog - neither of which the grid knows anything about.

    Offered on exactly the rows `rowActionFor` opens: a row that names no single
    object has no drawer, so it has no drawer's verbs either, and the browser's
    own menu stays on it.
  */
  const [menuState, setRowMenu] = useState<
    | { id: string; identity: ObjectIdentity; anchor: RowMenuAnchor; list: string; open: () => void }
    | undefined
  >(undefined);
  // Another list is another set of rows: a menu left open over it would be
  // about an object this page no longer draws, so it is the menu of the list
  // it was opened on, and of no other.
  const listNow = `${cluster ?? ''}|${kind ?? ''}`;
  const rowMenu = menuState?.list === listNow ? menuState : undefined;
  const rowMenuFor = (listPage: ListPage) => (row: Row) => {
    const identity = identityOf(cluster, listPage, row);
    // Offered on exactly the rows a click opens, and Open is that click.
    const open = rowActionFor(listPage)(row);
    if (identity === undefined || open === undefined) return undefined;
    return (anchor: RowMenuAnchor) => {
      setRowMenu({ id: row.id, identity, anchor, list: listNow, open });
    };
  };
  /*
    **Copy name's confirmation**, brief and out of the flow: a line that pushed
    the table down for a second and a half would move the row under the pointer.
    What the browser refused is said with the text, which can then be selected.
  */
  const [copied, setCopied] = useState<{ text: string; written: boolean } | undefined>(undefined);
  const copiedTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const copyName = (text: string): void => {
    copyText(text, (written) => {
      setCopied({ text, written });
      clearTimeout(copiedTimer.current);
      copiedTimer.current = setTimeout(
        () => {
          setCopied(undefined);
        },
        written ? 1400 : 6000,
      );
    });
  };

  return { rowActionFor, rowMenu, setRowMenu, rowMenuFor, copied, copyName };
}
