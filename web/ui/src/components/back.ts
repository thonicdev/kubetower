/**
 * The way back from a panel opened as a page.
 *
 * The drawer's *Open as page* is a link, and without a control of its own the
 * page's way out would be the browser's Back - which the desktop window does
 * not have, having no browser bar. So the link carries, in the history state of the
 * entry it pushes, the address it was followed from - the list, with the
 * drawer's object and tab in its query - and the page's *Back* returns there,
 * where the list opens the drawer again. A page reached any other way (a typed
 * address, a link from outside, a tab of its own) has no such state, and its
 * *Back* goes to the object's list instead.
 *
 * The drawer's tab in that address is the one the page was opened on; the page
 * may change it, and Back opens the drawer on the tab last chosen on the page,
 * so the page hands its tab to `withTab` on the way back. A page reached directly has no tab to carry back: its list has no
 * drawer to open.
 *
 * In the history state and not in the query, so a copied link does not carry
 * somebody else's way back. The state survives a reload of the same entry,
 * and the page's own query writes keep it (`useUrlParameter`, `useUrlList`).
 */
const BACK = 'kubetower.back';

/** The history state an *Open as page* link carries: where it was followed from. */
export function backHere(address: string): Record<string, string> {
  return { [BACK]: address };
}

/** Where a page returns to, when the console opened it from somewhere. */
export function backOf(state: unknown): string | undefined {
  if (typeof state !== 'object' || state === null) return undefined;
  const back = (state as Record<string, unknown>)[BACK];
  // Only an address inside the console: a path, never a scheme or a host.
  return typeof back === 'string' && back.startsWith('/') && !back.startsWith('//') && !back.includes('\\')
    ? back
    : undefined;
}

/**
 * `address` with its `tab` set to the page's `tab`, the rest of its query kept.
 * Details is the empty address, so `''` (and `'details'`) removes it, as the
 * drawer itself writes it.
 */
export function withTab(address: string, tab: string): string {
  const at = address.indexOf('?');
  const path = at === -1 ? address : address.slice(0, at);
  const query = new URLSearchParams(at === -1 ? '' : address.slice(at + 1));
  if (tab === '' || tab === 'details') query.delete('tab');
  else query.set('tab', tab);
  const search = query.toString();
  return search === '' ? path : `${path}?${search}`;
}
