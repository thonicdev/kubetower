import { useRef, useState } from 'react';

import { useDebounced } from '../../components/useDebounced';
import { useUrlList } from '../../components/useUrlList';
import { useUrlParameters } from '../../components/useUrlParameter';
import { composeFilter, FILTER_PARAMETERS, splitFilter } from './filterText';
import { useChosenFilters } from './listFilters';

/**
 * What narrows a resource list: the filter box (a selector and a search in the
 * address), the namespace selection and the filter control's choices, and the
 * settled values the cluster is asked with. Held apart from `ResourceList` so
 * the page reads what is asked rather than how the box keeps its text.
 */
export function useListFilters() {
  /*
    The two narrowings of this list, in the address like every other one.

    `selector` is accepted by `listResources`, keyed in the query cache and
    passed through by the Go read; the filter box is what sends it, beside the
    namespace and the search.

    Unparsed here on purpose: `app=api,tier!=cache` is the API server's grammar,
    it is what an operator already types into `kubectl -l`, and a client that
    validated it would be a second implementation of a parser whose errors the
    server states better.

    **Two parameters, one box.** The address keeps `selector` and
    `search` apart because they narrow at different layers - the selector asks
    the cluster for less, the search looks through what came back - and because
    a pasted link carries them apart. The
    toolbar does not: a user should not have to know which layer they are
    typing at. So one box holds the composition
    of the two and `filterText.ts` decides, token by token, which parameter
    each edit goes to. Written together in one navigation, because two
    `useUrlParameter` writes in one handler lose one of the two — the hook says
    why.
  */
  const [{ selector, search }, setFilter] = useUrlParameters(FILTER_PARAMETERS);
  /*
    What the box shows, and it is **not** re-derived from the address on every
    keystroke.

    The box is a composition of two parameters and each edit decomposes back to
    them, so composing again would seem to be free. It is not: `app=api ` (a
    user about to type the second word) decomposes to `app=api` and composes
    back without the space, and `nginx app=api` comes back reordered under the
    caret. The value is verbatim — `useUrlParameter`'s third decision — so the
    typed text is held here and re-read from the address **only when the
    address says something the text does not**: a pasted link, Back, the empty
    state's *Clear the filter*. While the address is the text's own
    decomposition, the text stands.

    Seeded during render rather than in an effect, for the reason
    `NamespaceMenu` gives: a `setState` in an effect paints once with the stale
    text, and that paint is long enough to type into.
  */
  const [held, setHeld] = useState(() => ({
    seen: { selector, search },
    text: composeFilter({ selector, search }),
  }));
  let filterText = held.text;
  if (held.seen.selector !== selector || held.seen.search !== search) {
    const own = splitFilter(held.text);
    filterText =
      own.selector === selector && own.search === search
        ? held.text
        : composeFilter({ selector, search });
    setHeld({ seen: { selector, search }, text: filterText });
  }
  /* The search the box last wrote. A search that arrived any other way - a
     bookmark, a link, Back - is an address, and only an address is translated
     from the withdrawn `column:value` grammar (see the rewrite below): typing
     `node:x` into the box is a search for `node:x`, which is what was decided. */
  const typedSearch = useRef<string | undefined>(undefined);
  const typeFilter = (text: string) => {
    setHeld((previous) => ({ ...previous, text }));
    const parts = splitFilter(text);
    typedSearch.current = parts.search;
    // A keystroke that changes the text but not what it asks for — a trailing
    // space — is not a navigation.
    if (parts.selector !== selector || parts.search !== search) setFilter(parts);
  };
  /*
    **A selection, not a name**, and the plural hook is the whole difference.

    A list's namespace is a selection, so the parameter is repeatable:
    `?namespace=a&namespace=b` is expressible in the address bar, in the
    request, and in the control that writes both.
  */
  const [namespaces, setNamespaces] = useUrlList('namespace');
  /*
    **The filter control's choices**: `?filter=Node:worker-2`,
    repeated. Not typed, so not settled: a tick is a finished statement.
    `listFilters.ts` says why the column is named by its label.
  */
  const { chosen, setChosen, rewrite } = useChosenFilters();

  // The address bar keeps up with every keystroke; the cluster does not.
  const settledSearch = useDebounced(search, 250);
  // The same settling, and for a sharper reason: a half-typed selector is not
  // just a narrower list, it is a **syntax error**, and sending one per
  // keystroke means the page spends the whole time drawing refusals of things
  // nobody finished saying.
  const settledSelector = useDebounced(selector, 250);

  return {
    selector,
    search,
    filterText,
    typeFilter,
    typedSearch,
    namespaces,
    setNamespaces,
    chosen,
    setChosen,
    rewrite,
    settledSearch,
    settledSelector,
  };
}

export type ListFilters = ReturnType<typeof useListFilters>;
