import { useQueryClient, type QueryClient } from '@tanstack/react-query';
import { useCallback, useEffect, useRef } from 'react';

import { resourceListQuery } from '../api/queries';
import { overviewQuery } from '../pages/overview/data';
import { pageFor, warmPage } from './pages';

/**
 * Reading ahead, so that clicking a menu entry draws rather than waits.
 *
 * # Why
 *
 * Moving from one page to another shows a load. It is not a page that refuses
 * to cache: **the first visit to a kind has nothing in the cache**, so the
 * list query is `pending` and the page draws its waiting state. Coming back
 * within five seconds is already instant; the wait is the first time, every
 * time - and `gcTime` throws the entry away after five minutes, so "the first
 * time" comes round again.
 *
 * The fix is to make the first visit not be the first fetch. A pointer resting
 * on a menu entry is a statement of intent that arrives several hundred
 * milliseconds before the click, and that is enough.
 *
 * **The first visit also has code to fetch**: every page is a `lazy` route in
 * `router/routes.tsx`, and a hover that warmed only the data would land on
 * a page whose chunk has not arrived - the wait moved rather than removed. So
 * the same intent warms the chunk too, through the table in `./pages.ts`: one
 * rest on a link, one data request where there is one, one chunk request where
 * the chunk is not already there. A dynamic import is idempotent, so a chunk
 * that has arrived costs nothing to ask for again.
 *
 * # Why an intent delay rather than firing on `pointerenter`
 *
 * A cluster's menu carries dozens of kinds. Sweeping the pointer down it to
 * reach the bottom one crosses every entry above, and firing on entry would send
 * one request per crossing - a menu that asks the cluster for everything it has
 * because somebody moved the mouse. The delay is what separates *traveling
 * over* from *stopping on*, and 120 ms is short enough to still be ahead of the
 * click.
 *
 * Leaving before the delay elapses cancels it. Nothing is sent, so a sweep costs
 * nothing at all.
 *
 * # Why it is safe to fetch what has not been asked for
 *
 * Every prefetch here is a **read**, and it is the same read the page itself
 * would issue a moment later. `prefetchQuery` does nothing at all when the entry
 * is still fresh, so resting on the same entry twice sends one request.
 *
 * It is not a free action, and the place that matters is the audit log: a
 * refused read is refused once more than it would have been. That is the
 * honest cost, and it is bounded by the intent delay rather than by politeness
 * - which is why the delay is not a tuning knob to be lowered later without
 * reading this paragraph.
 *
 * # What is deliberately not here
 *
 * **No prefetch on render.** Warming every entry when the menu draws is the same
 * request storm the delay exists to prevent, minus the intent.
 *
 * **No `placeholderData` across a change of list.** Keeping the previous kind's
 * rows on screen while the next kind loads would draw pods under a heading that
 * says ingresses. This whole console exists because a view that shows one thing
 * and claims another is worse than a view that says it is working. A *list* is
 * one cluster, one kind and one namespace selection; a change of any of the
 * three starts from the waiting state, whatever is in the cache for the last.
 *
 * Two places do keep the previous answer, and both because what changed is not
 * the subject. The metrics panel's span change - `api/queries.ts`,
 * `useMetricsPanel` - holds the same object and measurement, and the span row
 * says *settling* while it does. And a list whose **filter** alone
 * changed - a search or a label selector on the same list - keeps its rows
 * while the narrower answer is read, and the box says *filtering* while it does:
 * `useResourceList` and `sameListAsked` are the rule, in code.
 */

/** How long a pointer must rest before it counts as intent. */
const INTENT_MS = 120;

/**
 * Warm the query behind one internal path, where there is one.
 *
 * Keyed off the path rather than off a route object because the menu is served
 * by the API: `Ref.href` is a string the server chose, and the router's own
 * table is not addressable by it. The three shapes here are the three the
 * navigation surfaces actually offer.
 *
 * **The query string is cut off first**, and that is load-bearing rather than
 * tidy. `viewMemory.remembered` appends the sort an operator last used to a
 * kind's bare path, so a menu href is `/c/prod/pods?sort=age` as often as it is
 * `/c/prod/pods`. The sort is client-side ordering and is deliberately not in
 * the query key, so the entry warmed here is the entry the page reads. A
 * search term *is* in the key, and a menu href never carries one.
 *
 * **A namespace is in the key too, and a menu href may carry one**: the
 * namespace chosen on the last namespaced list rides on the rows that open
 * another (`shell/namespaceMemory.ts`). So it is read back out of the href and
 * handed to the query, or the entry warmed would be "All namespaces" and the
 * page, arriving at `?namespace=default`, would read nothing of it. Read with
 * `getAll`, the repeated spelling those links write.
 */
function warm(client: QueryClient, href: string): void {
  const pathname = href.split(/[?#]/)[0] ?? '';
  const query = new URLSearchParams(/\?([^#]*)/.exec(href)?.[1] ?? '');
  const namespace = query.getAll('namespace').filter((one) => one !== '');
  const segments = pathname.split('/').filter((segment) => segment !== '');

  // The code first and for every page, the data below for the pages that
  // have a query here. A detail route warms its chunk and no data; that is
  // still the half of the wait that was there before.
  warmPage(pathname);

  if (segments.length === 2 && segments[0] === 'c') {
    void client.prefetchQuery(overviewQuery(decodeURIComponent(segments[1] ?? '')));
    return;
  }
  // A kind's list, and only that: Helm, the port-forwards and an extra's page
  // are three segments long too, and none of them is a kind the list read
  // could answer for - warming one would request a list that does not exist.
  // The chunk table already knows which is which.
  if (pageFor(pathname) === 'resourceList') {
    void client.prefetchQuery(
      resourceListQuery(
        decodeURIComponent(segments[1] ?? ''),
        decodeURIComponent(segments[2] ?? ''),
        namespace.length === 0 ? {} : { namespace },
      ),
    );
  }
  // Anything else - a detail route, an extra's page - has no query here yet and
  // silently warms nothing. Deliberately not an error: this is an optimization,
  // and an optimization that throws is worse than one that does not apply.
}

export interface IntentHandlers {
  onPointerEnter: () => void;
  onPointerLeave: () => void;
  onFocus: () => void;
  onBlur: () => void;
}

/**
 * Handlers that call `fire` once a pointer has rested, or focus has landed, for
 * the intent delay - the mechanism under `useIntentPrefetch`, on its own so a
 * control that is not a link can read ahead the same way. The metrics panel's
 * span buttons are the first: resting on `24h` asks for that window before the
 * click, through the same delay and the same cancellation, so the argument
 * above about sweeps costing nothing holds there too.
 *
 * Focus counts as intent as well as hover, and not as an afterthought: a
 * keyboard operator tabbing through the menu is stating exactly what a pointer
 * resting states, and a page that is instant with a mouse and slow with a
 * keyboard has chosen who it is fast for.
 *
 * The timer is cleared on unmount. A menu group that closes while a pointer
 * rests in it would otherwise fetch for a link that no longer exists - harmless
 * here, because the answer lands in a cache rather than in a component, but it
 * is a request nobody asked for and the cleanup costs one line.
 *
 * `fire` is read at the moment the delay elapses, not at the moment the pointer
 * arrived, so a caller may pass a new closure every render without restarting
 * the timer. `undefined` means there is nothing to read ahead for.
 */
export function useIntent(fire: (() => void) | undefined): IntentHandlers {
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  // Written in an effect and not during render: a render may be discarded, and
  // React's own lint refuses a ref updated from one for that reason.
  const latest = useRef(fire);
  useEffect(() => {
    latest.current = fire;
  }, [fire]);

  const cancel = useCallback(() => {
    if (timer.current !== undefined) clearTimeout(timer.current);
    timer.current = undefined;
  }, []);

  useEffect(() => cancel, [cancel]);

  const begin = useCallback(() => {
    if (latest.current === undefined) return;
    cancel();
    timer.current = setTimeout(() => {
      timer.current = undefined;
      latest.current?.();
    }, INTENT_MS);
  }, [cancel]);

  return { onPointerEnter: begin, onPointerLeave: cancel, onFocus: begin, onBlur: cancel };
}

/**
 * Handlers to spread onto a navigation link: `useIntent` with `warm` as the
 * thing to do, keyed off the link's path.
 */
export function useIntentPrefetch(href: string | undefined): IntentHandlers {
  const client = useQueryClient();
  return useIntent(href === undefined ? undefined : () => warm(client, href));
}
