import type { ReactNode } from 'react';

import type { ListPage } from '../../api';
import type { useOpenInDrawer } from './useOpenInDrawer';

/**
 * What is said over the table about the question rather than the rows: a
 * namespace the selection names that the cluster does not have, an object an
 * arrival named that the list does not hold, and a row verb whose preview
 * failed before there was a dialog to say so.
 */
export function ListNotes({
  page,
  gone,
  failure,
}: {
  page: ListPage | undefined;
  gone: ReturnType<typeof useOpenInDrawer>['gone'];
  /** The row verb's failure, `null` where there is none. */
  failure: string | null;
}): ReactNode {
  return (
    <>
      {/*
        A namespace the selection names and this cluster does not have.

        **Drawn on the panel and deliberately not among the rows.** It is a fact about the *selection*, the rows are
        replaced every two seconds, and **it has to appear even when there are
        rows** — a selection of three where one does not exist gives a short table
        that looks complete. An empty-state arm could not carry it for that reason
        alone.

        An empty answer and a failed answer are drawn differently, and this is
        the same case for the namespace control. The namespace can be typed into,
        so it can be told a name that is not one, and the table's own answer to
        that would be *"No X in namespace Y. The cluster answered; there are none
        here"* — a confident claim about what a namespace contains, made about
        something that is not a namespace. A reassuring wrong answer.

        The sentence is the server's whole contribution and is not composed here:
        `namespaceUnknownNote` words it differently depending on whether the
        cluster's namespace list was read, was kept from before it stopped
        answering, or could not be had at all, and those are three different
        things to conclude from the same missing name.
      */}
      {page?.unknownNamespace === undefined || page.unknownNamespace === '' ? null : (
        <p data-note="unknown-namespace" className="border-b border-line px-3 py-1.5 text-xs text-warn">
          {page.unknownNamespace}
        </p>
      )}

      {/*
        The object an arrival named and this list does not have.

        Beside the unknown-namespace note and for the same reason: it is a
        fact about what was asked, not about the rows, and it has to be drawn
        over a table that looks complete. Worded in the page's own vocabulary
        - the cluster answered; the thing is not among what it listed - so
        that it cannot be read as the list having failed.
      */}
      {gone === undefined || page === undefined ? null : (
        <p data-note="open-gone" role="status" className="border-b border-line px-3 py-1.5 text-xs text-warn">
          {gone.several
            ? `${gone.name} names more than one of the ${page.label} this cluster listed, so none was opened. Pick one from the list.`
            : gone.outside && gone.namespace !== undefined
              ? `${gone.namespace}/${gone.name} is in ${gone.namespace}, where you may not list ${page.label}, so it is not shown to you. Nothing was opened.`
              : gone.filtered
              ? `${gone.namespace === undefined ? '' : `${gone.namespace}/`}${gone.name} is not among the ${page.label} this filter shows. Nothing was opened; clear the filter or the namespace selection to find it.`
              : `${gone.namespace === undefined ? '' : `${gone.namespace}/`}${gone.name} is no longer among the ${page.label} this cluster listed. Nothing was opened.`}
        </p>
      )}

      {/*
        A verb chosen from a row's menu whose preview failed. The dialog says
        everything after that; this is the one failure that happens before
        there is a dialog to say it in.
      */}
      {failure === null ? null : (
        <p data-note="row-act" role="alert" className="border-b border-line px-3 py-1.5 text-xs text-bad">
          {failure}
        </p>
      )}
    </>
  );
}

/** Copy name's confirmation, floated over the panel's corner
 *  (`useRowActions` says why it is out of the flow). */
export function CopiedNote({ copied }: { copied: { text: string; written: boolean } }): ReactNode {
  return (
    <p
      data-note="copied"
      role="status"
      className={`absolute top-2 right-4 z-10 max-w-[calc(100%-var(--spacing)*8)] truncate rounded-md border border-line bg-panel-2 px-2 py-1 text-xs shadow-lg ${
        copied.written ? 'text-fg' : 'text-warn'
      }`}
    >
      {copied.written
        ? `Copied ${copied.text}.`
        : `This browser would not let the page write to the clipboard: ${copied.text}`}
    </p>
  );
}
