import type { ReactNode } from 'react';

import type { ListPage, Reason } from '../../api';
import { btnSecondary } from '../../components/buttons';

/**
 * Nothing was found, and which question that answers.
 *
 * Three sub-shapes, because "Nothing here." is a claim and the three claims are
 * different: *the filter matched nothing*, *this namespace holds none*, *this
 * kind has none on this cluster*. The namespace control has a
 * fourth version of the same hazard — a typed name that is not a namespace at
 * all, where "Nothing here." becomes a claim about what a namespace contains
 * rather than about whether it is one.
 *
 * **The server's sentence wins when there is one.** `Reason` is permitted on a
 * successful arm precisely so that "nothing in this namespace" can say which
 * namespace, and a client that renders its own guess instead has thrown the
 * server's sentence away. What is composed below is composed only from **this
 * client's own request** — the kind it asked for, the namespace it sent, the
 * text it typed — which is restating the question rather than guessing at an
 * answer.
 */
export function EmptyList({
  page,
  namespace,
  filter,
  chosen,
  reason,
  onClearFilter,
}: {
  page: ListPage;
  namespace: string;
  /** What the one box holds, selector and search composed - the sentence
   *  quotes the box, because the box is what *Clear the filter* empties. */
  filter: string;
  /** The filter control's choices, `Node: worker-2`, which *Clear the filter*
   *  empties too. */
  chosen: readonly string[];
  reason: Reason | undefined;
  onClearFilter: () => void;
}): ReactNode {
  if (reason !== undefined) {
    return (
      <div data-empty="server">
        <p className="text-sm text-muted">{reason.message}</p>
      </div>
    );
  }

  if (filter !== '' || chosen.length > 0) {
    const what = [filter === '' ? '' : `“${filter}”`, chosen.join(', ')].filter((one) => one !== '');
    return (
      <div data-empty="filtered">
        <p className="text-sm text-muted">
          No {page.label} matched {what.join(' with ')}.
        </p>
        <button
          type="button"
          onClick={onClearFilter}
          className={`mt-2 ${btnSecondary}`}
        >
          Clear the filter
        </button>
      </div>
    );
  }

  // A list the access review narrowed has not established that there are none:
  // "the cluster has none" would be a claim about namespaces the person was
  // not shown. It says what is true - none where they may look - and why.
  if (page.accessNote !== undefined) {
    return (
      <div data-empty="narrowed">
        <p className="text-sm text-muted">
          No {page.label} in the namespaces you may list. {page.accessNote}
        </p>
      </div>
    );
  }

  if (namespace !== '') {
    return (
      <div data-empty="namespaced">
        <p className="text-sm text-muted">
          No {page.label} in namespace {namespace}.
        </p>
      </div>
    );
  }

  return (
    <div data-empty="kind">
      <p className="text-sm text-muted">
        No {page.label} on this cluster.
      </p>
    </div>
  );
}
