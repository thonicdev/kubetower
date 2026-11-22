import { useEffect, useEffectEvent, useRef, useState } from 'react';

import type { ListPage, ValueResult } from '../../api';
import { useUrlParameter } from '../../components/useUrlParameter';
import { forgetOpen } from '../../shell/viewMemory';
import { useShell } from '../../shell/ShellContext';
import {
  identityKey,
  identityOf,
  OPEN_PARAMETER,
  openValue,
  parseOpen,
  type ObjectIdentity,
  type OpenTarget,
} from '../object/address';
import { ObjectDetail } from '../object/ObjectDetail';
import type { ListFilters } from './useListFilters';

/**
 * The drawer a resource list puts up, and `open` in the address that names it:
 * written when a row opens, taken on arrival and matched against the answer,
 * dropped when the list narrows or the drawer is closed.
 */
export function useOpenInDrawer({
  cluster,
  kind,
  page,
  answer,
  standingIn,
  filters: { selector, search, settledSearch, settledSelector, namespaces, chosen },
}: {
  cluster: string | undefined;
  kind: string | undefined;
  page: ListPage | undefined;
  answer: ValueResult<ListPage> | undefined;
  standingIn: boolean;
  filters: Pick<
    ListFilters,
    'selector' | 'search' | 'settledSearch' | 'settledSelector' | 'namespaces' | 'chosen'
  >;
}) {
  const { drawer, openDrawer, closeDrawer } = useShell();
  /*
    **The open object is part of the address.** A drawer that added only
    `?tab=` would lose the object on a reload, on Back and in a copied link. So
    `open` stays in the address for as long as this page's drawer is up, in the
    namespaced form `parseOpen` reads, and leaves it when the user closes the
    drawer:

    - a row click writes it, with the tab cleared in the same navigation;
    - an arrival with it opens the row as before, and writes it back once the
      drawer is up (the arrival still drops it first: that second commit is
      what lets the drawer open after `Drawer.tsx` has emptied it, see below);
    - closing the drawer, by the × or by Escape, removes it, so a reload after
      a close does not bring back what the user put away;
    - a navigation elsewhere leaves it on the entry it was written to, which is
      exactly what makes Back return to the list *with* the drawer.

    `drawerShows` is the `open` value of the drawer this page put up, or `''`. A ref,
    because nothing draws it: it is how the effects below tell the address this
    page wrote from an address that arrived.
  */
  const drawerShows = useRef('');
  const showInDrawer = (identity: ObjectIdentity): string => {
    const value = openValue(identity);
    drawerShows.current = value;
    // Keyed on the object, so another row is another panel and nothing the
    // last one was holding - a failure, a node shell's plan - carries over.
    openDrawer(
      <ObjectDetail key={identityKey(identity)} identity={identity} chrome="drawer" onClose={closeDrawer} />,
      identity,
    );
    return value;
  };

  /*
    Arriving with `?open=<name>`: the named row's drawer, opened by this page.

    A relation followed from the drawer is
    a navigation to the related kind's list, narrowed to the namespace, and the
    object is opened *here* rather than carried across - `Drawer.tsx`'s rule is
    that a navigation empties the drawer, and it does; this is the destination
    re-opening it, which is the only way the two rules coexist.

    **Not on the commit that arrives.** The `Drawer` is the Outlet's later
    sibling, so its effect - the one that empties it on a new path - runs
    *after* this page's effects in the same commit. A drawer opened on the
    arriving commit is closed on the arriving commit, and only when the list
    was already in the cache, which is the kind of defect that passes every
    test with a cold cache; the warm-cache test in `ObjectDrawer.test.tsx` is
    the one that saw it. So the arriving commit only takes the name and drops
    the parameter, and the drop is itself a navigation - a second commit, on a
    path the `Drawer` has already seen - on which the drawer opens, or later
    still if the list has not answered yet.

    **The parameter is dropped on arrival, with `replace`, and written back
    once the drawer is up** (`drawerShows` above says the rest).
    Dropping it for good would lose the object being read on a reload, on Back
    and in a copied link; leaving it in the address is safe because closing the
    drawer takes it out, so a reload does not reopen a drawer the user closed.
    Replaced rather than pushed either way, so Back from here is the page the relation was followed from.

    **The row, and only the row.** `identityOf` on the row the list drew is the
    one validated way to an identity here, and the list is not capped, so a
    name that is not among the rows is an object that is not in this list -
    deleted since the relation was drawn, or filtered out by a `search` the
    pasted address also carried. A drawer about it would contradict the page
    behind it; the list saying it is not there is the honest answer. Nothing
    is built from the parameters: the namespace parameter is a selection and
    may name several, which a single-name lookup cannot resolve.

    A ref and not state, because nothing draws the pending name: it is a note
    between two effects, and state for it would be a render per step that
    `react-hooks/set-state-in-effect` refuses for good reason.

    **Two forms of the name.** A relation writes the name alone and
    narrows the list to the namespace, so one name is one row. The rail tile
    returns to the list *as the memory left it* - no namespace selection - and
    writes `<namespace>/<name>`, because a bare name on a namespaced kind can
    be several rows. `parseOpen` tells them apart; a namespace given is matched
    as well as the name, and a namespace not given is what it always was.

    **A name the list does not hold is said, not swallowed.** An
    arrival naming a row that is not there must not open nothing and say
    nothing: the object may have been deleted or rescheduled since a tile that
    promised to bring a drawer back was drawn, so the list has to say so ("opened"
    and "found nothing to open" are different answers, as an empty answer and
    a failed answer are). The note is
    about the arrival and not about the list, so it sits with the
    unknown-namespace note above the rows rather than among them, and the
    memory of the object is forgotten with it, so the next return does not
    repeat it. This one is state, because something draws it.
  */
  const [open, setOpen] = useUrlParameter(OPEN_PARAMETER);
  const pendingOpen = useRef('');
  const [gone, setGone] = useState<
    (OpenTarget & { kind: string; several: boolean; filtered: boolean; outside: boolean }) | undefined
  >(undefined);
  useEffect(() => {
    // A name pending for one kind must not be opened on the next. This page
    // stays mounted from `/pods` to `/deployments` (same route, new params), so
    // a name taken on a list that never answered would otherwise be matched
    // against whichever list answers first. Declared before the effect that
    // takes the name, so that an arrival with `open` on a new kind ends with
    // the name pending and not with it cleared - effects run in order.
    pendingOpen.current = '';
  }, [cluster, kind]);
  useEffect(() => {
    // A new kind is a new list: what was shown was shown on the last one, and
    // `Drawer.tsx` has emptied it.
    drawerShows.current = '';
  }, [cluster, kind]);
  useEffect(() => {
    // The address this page wrote for its own drawer, not an arrival.
    if (open === '' || open === drawerShows.current) return;
    pendingOpen.current = open;
    setOpen('');
  }, [open, setOpen]);
  /*
    **A narrower list takes `open` out of the address.** The filter box and the
    namespace picker keep every other parameter, so narrowing the list while a
    drawer is up would leave `open` naming a row the list may no longer hold. A
    reload of that address would then match against the narrowed answer and say
    the object was gone - and forget the rail tile's memory of it - for an
    object that was only filtered out. Dropped on any
    change of the filter or the selection, not only one that hides the object:
    which rows a half-typed filter keeps is not known until the answer comes,
    and an address that may name a hidden row is the defect. The drawer itself
    stays up; only the address stops promising it.

    Compared with the values this effect last saw rather than run on mount, so
    an arrival that carries both `open` and a filter is left to the arrival
    path below.
  */
  const narrowing = JSON.stringify([selector, search, namespaces]);
  const lastNarrowing = useRef(narrowing);
  useEffect(() => {
    if (lastNarrowing.current === narrowing) return;
    lastNarrowing.current = narrowing;
    if (drawerShows.current === '') return;
    drawerShows.current = '';
    setOpen('');
  }, [narrowing, setOpen]);
  useEffect(() => {
    // Closed by the user - the × or Escape - while this page's object was
    // in the address: take it out, so a reload does not reopen it.
    if (drawerShows.current === '' || drawer !== null) return;
    drawerShows.current = '';
    setOpen('');
  }, [drawer, setOpen]);
  /* Effect events: what the arrival does once matched reads the drawer's
     callbacks and the narrowing as they are now, without re-running the
     effect below when they change - it re-runs on its own list, as before. */
  const noteGone = useEffectEvent((target: OpenTarget, listPage: ListPage, several: boolean, on: string) => {
    // Not here, or not one row. Said on the page, and forgotten by the
    // memory so the tile does not promise it again. State written from an
    // effect, because it is the outcome of an event - the arrival, matched
    // against the answer that came - and nothing paints wrongly before it:
    // the list without a notice is a true page.
    //
    // **Not forgotten when the list was narrowed**: a filter or a namespace selection that leaves the object out says
    // nothing about whether it exists, so the notice says the filter hides
    // it and the rail tile keeps its memory.
    const filtered =
      search !== '' ||
      selector !== '' ||
      chosen.length > 0 ||
      (namespaces.length > 0 &&
        (target.namespace === undefined || !namespaces.includes(target.namespace)));
    // **Outside the person's namespaces**: the list was narrowed by the
    // access review, and the server named every namespace where this person
    // may list the kind. One not among them was never theirs to see, which is
    // a different sentence from "no longer listed". Only where the server
    // named them: its absence says nothing about any namespace.
    const outside =
      !several &&
      target.namespace !== undefined &&
      listPage.allowedNamespaces !== undefined &&
      !listPage.allowedNamespaces.includes(target.namespace);
    setGone({ ...target, kind: listPage.kind, several, filtered: filtered && !outside, outside });
    if (!filtered || outside) forgetOpen(on);
  });
  const reopen = useEffectEvent((identity: ObjectIdentity) => {
    // And back into the address, now that the drawer is up (see `drawerShows`).
    setOpen(showInDrawer(identity));
  });
  useEffect(() => {
    // Still the arriving commit while the parameter is in the address: the
    // effect above has only just asked for it to go.
    if (open !== '' || pendingOpen.current === '' || answer === undefined) return;
    // Not the list the name was meant for, yet, in either of two ways:
    // the previous filter's rows standing in while this one is read,
    // or - the common arrival, a link from elsewhere carrying `search` and
    // `open` together - a filter typed into the address that has not settled
    // into a request, so the answer on screen is still the unfiltered one. A
    // name either lacks may be in the answer on its way. Wait for it.
    if (standingIn || search !== settledSearch || selector !== settledSelector) return;
    // Consumed on the first answer, whichever arm: a list that could not be
    // read has no row to open, and keeping the name for a later retry would
    // open a drawer over a page the user has since moved on inside.
    const target = parseOpen(pendingOpen.current);
    pendingOpen.current = '';
    if (page === undefined || cluster === undefined) return;
    // Exactly one row by that name. A namespaced kind read without a namespace
    // selection can hold the same name in several namespaces, and picking the
    // first would be a guess, which is why the tile's form carries the namespace.
    const [row, ...others] = page.rows.filter(
      (one) =>
        one.name === target.name &&
        (target.namespace === undefined || one.namespace === target.namespace),
    );
    if (row === undefined || others.length > 0) {
      noteGone(target, page, others.length > 0, cluster);
      return;
    }
    // A row that is there and cannot be addressed is the payload contradicting
    // itself (`identityOf` says how), not an object that is gone: it opens
    // nothing, as it always did, and no notice claims otherwise.
    const identity = identityOf(cluster, page, row);
    if (identity === undefined) return;
    // `openDrawer`/`closeDrawer` are re-made whenever the drawer changes, so
    // `showInDrawer` is read through the effect event above: listing it would
    // re-run this on every open. What this effect is about is the answer
    // arriving, or the parameter leaving, while a name is pending.
    reopen(identity);
  }, [open, answer, page, cluster, standingIn, search, settledSearch, selector, settledSelector]);
  // A note about one kind's arrival does not outlive that kind on this page,
  // which stays mounted from `/pods` to `/deployments`. Adjusted during render,
  // React's own answer for state that follows a prop.
  if (gone !== undefined && gone.kind !== page?.kind && page !== undefined) setGone(undefined);

  return { showInDrawer, gone, setGone };
}
