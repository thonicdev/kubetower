import type { ReactNode } from 'react';

import type { ListPage, useNamespaces } from '../../api';
import { ColumnMenu } from '../../components/ColumnMenu';
import { FilterBox } from '../../components/FilterBox';
import { DroppedFilters, FilterChips, FilterMenu } from '../../components/FilterMenu';
import { NamespaceMenu, namespaceNote, namespacesOf } from '../../components/NamespaceMenu';
import { TableMenu } from '../../components/TableMenu';
import type { ContributedColumn } from './extensionColumns';
import { filterColumnsOf, type FilterChip } from './filterChips';
import { FILTER_SYNTAX } from './filterText';
import { sameChosen, toggledChosen } from './listFilters';
import type { useColumnControls } from './useColumnControls';
import type { useDroppedFilters } from './useDroppedFilters';
import type { ListFilters } from './useListFilters';

type Columns = ReturnType<typeof useColumnControls>;

/**
 * The list's heading row: the kind's name, the namespace it is narrowed to,
 * the count, then the filters, Columns and the `⋯` at the far right, and the
 * server's notes about what the table holds under them.
 */
export function ListToolbar({
  title,
  kind,
  page,
  namespaced,
  namespaceList,
  namespaces,
  chooseNamespaces,
  filters: { selector, search, filterText, typeFilter, chosen, setChosen, settledSearch, settledSelector },
  standingIn,
  chips,
  droppedHere,
  dismissDropped,
  shown,
  compact,
  onShow,
  onReset,
  onDensity,
  contributed = [],
}: {
  title: string | undefined;
  kind: string | undefined;
  page: ListPage | undefined;
  /** The server's answer to whether the kind has namespaces at all. */
  namespaced: boolean;
  /** The namespaces to pick from, read apart from the list. */
  namespaceList: ReturnType<typeof useNamespaces>;
  namespaces: readonly string[];
  chooseNamespaces: (next: readonly string[]) => void;
  filters: Pick<
    ListFilters,
    | 'selector'
    | 'search'
    | 'filterText'
    | 'typeFilter'
    | 'chosen'
    | 'setChosen'
    | 'settledSearch'
    | 'settledSelector'
  >;
  /** The rows are the previous filter's while the next is read. */
  standingIn: boolean;
  chips: readonly FilterChip[];
  droppedHere: ReturnType<typeof useDroppedFilters>['droppedHere'];
  dismissDropped: ReturnType<typeof useDroppedFilters>['dismissDropped'];
  shown: Columns['shown'];
  compact: Columns['compact'];
  onShow: Columns['onShow'];
  onReset: Columns['onReset'];
  onDensity: Columns['onDensity'];
  /** The columns extensions add to this list, offered in the filter control. */
  contributed?: readonly ContributedColumn[];
}): ReactNode {
  const filterColumns = filterColumnsOf(page, chosen, shown?.columns, contributed);

  return (
    <header className="flex flex-wrap items-center gap-3 border-b border-line px-3 py-2">
      <h1 className="text-lg font-medium">{title}</h1>

      {/*
        The namespace, immediately after the heading and before everything
        else. The heading says what you are looking at, the
        namespace says which of them, and everything that changes *how* the rows
        are drawn stays at the far right.

        Drawn only where the kind has namespaces at all. The server applies
        the same guard to the selector itself: a Node has no namespace, and a
        control that cannot change anything about this table is one more thing
        to read past. `page.namespaced` is the server's answer and the front end
        does not guess it from whether a row happens to carry one.
      */}
      {namespaced ? (
        <NamespaceMenu
          available={namespacesOf(namespaceList.data)}
          {...(namespaceNote(namespaceList.data) === undefined
            ? {}
            : { note: namespaceNote(namespaceList.data) })}
          selected={namespaces}
          onChange={chooseNamespaces}
        />
      ) : null}

      {/* The count comes from the answer, never from the table. */}
      <p aria-live="polite" className="text-xs text-muted">
        {page === undefined ? '' : `${String(page.rows.length)} listed`}
      </p>

      {/*
        **`min-w-0` and `max-w-full`, and the search box gives way.** At 1024px
        with a drawer open the page is about 360px wide; a group that stayed
        its full width there would run Columns and the `⋯` on under the
        drawer. Held to the line: what does not fit wraps onto a line of its
        own, the field gives way where even that is too narrow (`FilterBox`),
        and every button is drawn whole.
      */}
      <div className="ml-auto flex min-w-0 max-w-full flex-wrap items-center justify-end gap-2">
        {/*
          The filters in effect, beside the box.
        */}
        <FilterChips
          chips={chips}
          onRemove={(parameter) => {
            setChosen(chosen.filter((one) => !sameChosen(one, parameter)));
          }}
        />
        <DroppedFilters
          filters={droppedHere}
          onDismiss={dismissDropped}
        />

        {/* The box and the three controls, which wrap as a group after the
            chips, and the controls after the box: a box squeezed to 60px
            to keep one line is not one anybody can type a name into. */}
        <div className="flex min-w-0 max-w-full flex-wrap items-center justify-end gap-2">
          {/*
            One box for both narrowings, and the placeholder teaches it: a word
            is searched in every column of what came back, `app=api` asks the
            cluster for less. `filterText.ts` tells them apart; the address
            still carries them apart. Settling while either half has not
            reached a request.

            **And nothing else.** A `column:value` grammar for one column's exact
            value would be one nobody can find, could not address a column
            titled `Controlled by`, and would make the box do two jobs. A
            column's value is the filter icon's, beside the box, and the
            placeholder says what the box is.
          */}
          {/* The field, the funnel and Columns are one segmented control: no
              gap between them. The `⋯` is a separate control and keeps its gap. */}
          <div className="flex min-w-0 items-center">
            <FilterBox
              name="filter"
              value={filterText}
              onChange={typeFilter}
              label={`Filter ${page?.label ?? kind ?? 'this list'}`}
              placeholder="search all columns, or app=api"
              title={FILTER_SYNTAX}
              /* Settling for as long as the rows are not the answer to what is
                 typed: while the typing settles, and then while the previous
                 answer stands in for the one being read. */
              settling={selector !== settledSelector || search !== settledSearch || standingIn}
            >
            {/* The columns the server marks filterable. Where it marks none - a
                custom resource's printer columns, Deployments, ConfigMaps - the
                funnel is still drawn, inert, and says why: absent, it read as
                missing beside the lists that have it. Not before the page answers, when nothing is known either way. */}
            {page === undefined ? null : (
              <FilterMenu
                columns={filterColumns}
                onToggle={(column, value) => {
                  setChosen(toggledChosen(chosen, column, value));
                }}
                onClear={() => {
                  setChosen([]);
                }}
              />
            )}
            </FilterBox>

            {shown === undefined ? null : (
              <ColumnMenu
                columns={shown.columns}
                labelOf={(column) => column.label}
                onShow={onShow}
                /*
                  **Whether anything is stored is the server's answer, not an
                  inference from the payload.** `ListPage.columnsCustomised` says
                  "this kind has an arrangement saved" (`internal/columns`).

                  Reconstructing it from the payload - a column whose visibility
                  differs from its default, or one carrying a stored width - is
                  wrong in both directions at once. It cannot see a
                  *re-ordering*, because the server sends only the order it
                  resolved, so a table arranged by dragging would offer no way
                  back; and it reads a *default* as an arrangement, because
                  `column.visible === column.secondary` is true of a secondary
                  column that is off exactly as the descriptor asks, so any kind
                  declaring one would offer Reset for ever on a fresh install. A
                  control that is always offered is one that stops being read.
                */
              />
            )}
          </div>

          {/*
            The `⋯`, at the far end, holding what is done **to** the table rather
            than read from it. Drawn whether or not the page has answered: the
            row height is a console preference, so there is something to set even
            while a cluster is being asked, and a control that disappears while a
            list loads is one a person learns not to reach for.
          */}
          <TableMenu
            compact={compact}
            onDensity={onDensity}
            onReset={onReset}
            arranged={shown?.columnsCustomised === true}
          />
        </div>
      </div>

      {/* No line here about empty usage columns: the dashes say it, and the
          server's sentence is on the empty columns' headings (`usageNote`). */}
      {page?.readOnlyNote !== undefined ? (
        <p className="basis-full text-xs text-warn">{page.readOnlyNote}</p>
      ) : null}
      {/* The access review narrowed this list: the person may not list this
          kind in every namespace, so the rows are those of the namespaces
          they may. A narrowed table and a complete one look alike, so it is
          said where the table is read. The sentence is the server's, built
          from the question and never from the rows. */}
      {page?.accessNote !== undefined ? (
        <p className="basis-full text-xs text-warn" data-note="access">
          {page.accessNote}
        </p>
      ) : null}
    </header>
  );
}
