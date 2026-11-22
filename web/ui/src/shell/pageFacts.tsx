import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react';

/**
 * What the page inside the Outlet says about itself, for the regions outside it.
 *
 * ## Why a page has to declare this rather than the shell deriving it
 *
 * The topbar needs to know what this page is called, and the route cannot
 * answer it. It is close to derivable - the route knows it is a list of `pods` -
 * but the *label* is the server's (`ListPage.label`), and re-deriving it in the
 * shell would be a second copy of a name that already travels in the response.
 */
export interface PageFacts {
  /** What this page is, for the breadcrumb and the tab title. `null` while no
   *  page has said — which is a page that has not spoken, not a page with no
   *  name. The topbar draws the two differently.
   *
   *  **Where a `trail` is given, it wins**: `useDeclarePageFacts` declares its
   *  labels joined as the title, whatever was passed here, so the window title
   *  and the breadcrumb cannot say two different things. */
  readonly title: string | null;
  /**
   * The breadcrumb's page part cut into segments, each with the place it names.
   * Absent, the breadcrumb draws `title` as one segment,
   * as it always did. `trail(...)` below builds both.
   *
   * A page whose title is a path - `Pod / default / api-0` - declares this so
   * the kind and the namespace link back to the list they belong to. The
   * topbar never links the last segment, whatever it is given: that is where
   * you are, and a link to the page under the pointer is a link that does
   * nothing.
   */
  readonly trail?: readonly Crumb[] | undefined;
}

/** One segment of the breadcrumb: what it says, and where it goes if it is a
 *  place one can go back to. */
export interface Crumb {
  readonly label: string;
  /** An in-app address. Absent is a segment that names no page of its own. */
  readonly to?: string | undefined;
}

const silent: PageFacts = { title: null };

/**
 * A page's facts from its breadcrumb segments, for `useDeclarePageFacts`.
 *
 * An absent segment, or one with an empty label, is left out - and its link
 * with it - rather than drawn as an empty segment between two slashes: a
 * cluster-scoped object has no namespace, and `Node /  / worker-1` would read
 * as a namespace that could not be read. The title is the labels joined the
 * way the topbar joins its own, so the window title reads `Pod / default /
 * api-0` as it did before the segments linked.
 */
export function trail(...parts: readonly (Crumb | undefined)[]): PageFacts {
  const present = parts.filter((part): part is Crumb => part !== undefined && part.label !== '');
  return { title: titleOf(present), trail: present };
}

function titleOf(segments: readonly Crumb[]): string {
  return segments.map((one) => one.label).join(' / ');
}

/** One string for a trail, so two declarations of the same segments compare
 *  equal however often a page rebuilds the array. Only what a crumb says and
 *  where it goes, field by field: a crumb built from a richer object (extra
 *  fields, a cycle) neither throws here nor reads as changed. The crumbs
 *  themselves are passed on whole; a field added to `Crumb` that the topbar
 *  draws is added here too. */
function trailKey(segments: readonly Crumb[] | undefined): string {
  return segments === undefined ? '' : JSON.stringify(segments.map((one) => [one.label, one.to ?? null]));
}

interface PageFactsApi {
  readonly facts: PageFacts;
  readonly declare: (facts: PageFacts) => void;
}

const PageFactsContext = createContext<PageFactsApi | null>(null);

export function PageFactsProvider({ children }: { children: ReactNode }): ReactNode {
  const [facts, setFacts] = useState<PageFacts>(silent);

  // Stable, so `useDeclarePageFacts`'s effect depends on the values it was given
  // and not on this function's identity — otherwise every render of the provider
  // re-runs every page's declaration, and the state update re-renders the
  // provider.
  const declare = useCallback((next: PageFacts) => {
    setFacts((current) =>
      current.title === next.title && trailKey(current.trail) === trailKey(next.trail) ? current : next,
    );
  }, []);

  const value = useMemo<PageFactsApi>(() => ({ facts, declare }), [facts, declare]);
  return <PageFactsContext value={value}>{children}</PageFactsContext>;
}

/** What the shell draws from. `silent` where there is no provider, so a region
 *  rendered in isolation in a test draws the "nothing declared" case rather than
 *  throwing. */
export function usePageFacts(): PageFacts {
  return useContext(PageFactsContext)?.facts ?? silent;
}

/**
 * Said by a page, once per render, about itself.
 *
 * The cleanup resets to `silent` rather than leaving the last page's facts
 * standing. Without it a navigation from a list to a page that declares nothing
 * would leave the list's title in the breadcrumb over a page it does not name.
 */
export function useDeclarePageFacts(facts: PageFacts): void {
  const api = useContext(PageFactsContext);
  const declare = api?.declare;
  // A page builds its trail afresh on every render. The array is kept until a
  // render brings different segments (React's "storing information from
  // previous renders"), so the effect re-runs on what the trail says rather
  // than on its identity, and the crumbs are passed on whole.
  // An empty trail is no trail: the topbar draws the title for it, so the title
  // has to stay the page's own rather than become the empty join.
  const given = facts.trail !== undefined && facts.trail.length > 0 ? facts.trail : undefined;
  const key = trailKey(given);
  const [kept, setKept] = useState({ key, segments: given });
  if (kept.key !== key) setKept({ key, segments: given });
  const segments = kept.key === key ? kept.segments : given;
  const title = segments === undefined ? facts.title : titleOf(segments);

  useEffect(() => {
    if (declare === undefined) return;
    declare(segments === undefined ? { title } : { title, trail: segments });
    return () => declare(silent);
  }, [declare, title, segments]);
}
