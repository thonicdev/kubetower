import { useState } from 'react';

import { useResourceList } from '../../api';
import type { ListFilters } from './useListFilters';

/**
 * The list read for what the filters ask, and what the page needs to know
 * about the answer beyond the answer itself: which filter the rows on screen
 * answer, and whether the kind has namespaces.
 */
export function useListQuery(
  cluster: string | undefined,
  kind: string | undefined,
  {
    namespaces,
    chosen,
    settledSearch,
    settledSelector,
  }: Pick<ListFilters, 'namespaces' | 'chosen' | 'settledSearch' | 'settledSelector'>,
) {
  const query = useResourceList(cluster, kind, {
    namespace: namespaces.length === 0 ? undefined : namespaces,
    selector: settledSelector === '' ? undefined : settledSelector,
    search: settledSearch === '' ? undefined : settledSearch,
    filter: chosen.length === 0 ? undefined : chosen,
  });

  const answer = query.data;
  const page = answer !== undefined && answer.state === 'ok' ? answer.data : undefined;

  /*
    **The rows on screen may be the previous filter's**.

    `useResourceList` keeps the last answer for this same list on screen while
    a new search or selector is read, rather than dropping the table to its
    waiting state once per settled word. So "the filter typed" and "the filter
    these rows answer" are two values for the length of one request, and
    anything on this page that *names* the filter must name the second: an
    empty table left over from `ngin` must not say *nothing matches nginx-a*
    before anybody has asked the cluster about `nginx-a`. Adjusted during
    render, the idiom `useListSort` uses, so no frame draws the pair apart.
  */
  const asked = { selector: settledSelector, search: settledSearch };
  const standingIn = query.isPlaceholderData;
  const [answered, setAnswered] = useState(asked);
  if (
    !standingIn &&
    answer !== undefined &&
    (answered.selector !== asked.selector || answered.search !== asked.search)
  ) {
    setAnswered(asked);
  }
  const answersFilter = standingIn ? answered : asked;

  /**
   * Whether this kind has namespaces, **remembered across a re-read**.
   *
   * `page` is `undefined` whenever there is no answer for the current query key,
   * and picking a namespace changes that key — so a control drawn on
   * `page?.namespaced === true` unmounts itself the instant it is used and comes
   * back when the cluster answers, so a second click finds nothing to click.
   *
   * That is the same failure `useSelection` guards in its own domain and the
   * same distinction as everywhere else on this page: **"the answer says this
   * kind is cluster-scoped" and "there is no answer yet" are not one state.**
   * Only the first may take a control off the screen.
   *
   * Adjusted during render rather than in an effect, which is the idiom
   * `useListSort` uses and for the same reason: an effect paints once with the
   * previous kind's answer applied to the new kind.
   */
  const [known, setKnown] = useState<{ kind: string; namespaced: boolean } | undefined>(undefined);
  if (page !== undefined && (known === undefined || known.kind !== kind || known.namespaced !== page.namespaced)) {
    setKnown({ kind: kind ?? '', namespaced: page.namespaced });
  }
  const remembered = known !== undefined && known.kind === kind ? known.namespaced : false;
  const namespaced = page?.namespaced ?? remembered;

  return { query, answer, page, standingIn, answered, answersFilter, namespaced };
}
