import type { ListPage } from '../../api';
import type { ReactNode } from 'react';
import { useParams } from 'react-router';

import { ApiError, isFailedState, useNamespaces } from '../../api';
import { namespacesOf } from '../../components/NamespaceMenu';
import { Remedy } from '../../components/Remedy';
import { useDeclarePageFacts } from '../../shell/pageFacts';
import { useMenuTitle } from '../../shell/namespaceMemory';
import { useKindsOnPage } from '../../shell/KindsToggle';
import { ValueView } from '../../state/Result';
import { PANEL_BOX } from '../../state/StateNotice';
import { useObjectAct } from '../object/useObjectAct';
import { CreateObject } from './CreateObject';
import { EmptyList } from './EmptyList';
import { useExtensionColumns } from './extensionColumns';
import { chipsOf, narrowByExtensionColumns } from './filterChips';
import { composeFilter } from './filterText';
import { useLegacyListParameters } from './legacyParameters';
import { CopiedNote, ListNotes } from './ListNotes';
import { ListSelectionBar } from './ListSelectionBar';
import { ListToolbar } from './ListToolbar';
import { Grid, nextSort } from './ResourceGrid';
import { RowMenu } from './RowMenu';
import { SidewaysCue } from './SidewaysCue';
import { useColumnControls } from './useColumnControls';
import { useDroppedFilters } from './useDroppedFilters';
import { useListFilters } from './useListFilters';
import { useListQuery } from './useListQuery';
import { useListSelection } from './useListSelection';
import { useListSort } from './useListSort';
import { useOpenInDrawer } from './useOpenInDrawer';
import { useRememberedPlace } from './useRememberedPlace';
import { useRowActions } from './useRowActions';
import { useScrollReset } from './useScrollReset';

/**
 * One kind, listed.
 *
 * ## The six states
 *
 * **An empty answer and a failed answer are drawn differently, and so is each
 * kind of failure.** Collapsing `unreadable`, `absent`, `unreachable` and
 * `forbidden` into one error sentence would hide what the person can do next.
 *
 * The shortcut that creates the collapse is
 * `if (state !== 'ok') return <Error message={reason.message}/>`. It renders, it
 * says something true, and it passes every test anybody would think to write.
 * What is drawn instead:
 *
 * | State | Where it comes from | What is drawn |
 * |---|---|---|
 * | `ok`, rows | the kind listed | the grid |
 * | `ok`, `rows: []` | **the empty state, and it lives inside `ok`** — `ListPageEnvelope` is a *value* envelope, so a kind with nothing in it is `ok` with `columns` filled | the grid, **headings and all**, with one full-width cell saying what was asked and found nothing. Three sub-shapes: a filter that matched nothing, a namespace with nothing in it, a kind with nothing in it — and the filter one offers to clear the filter |
 * | `unreadable` | the console could not make sense of the answer | the notice, plus a remedy saying the fault is **this console**, not the cluster (no link: there is no Cluster page) |
 * | `absent` | the kind is not in discovery | the notice, plus a remedy saying nothing here serves it |
 * | `unreachable` | the cluster did not answer | the notice, plus a remedy saying to try again; the retry button itself is `reason.retryable`'s and is drawn by the notice |
 * | `forbidden` | the API server refused | the notice, plus the **verb, resource and namespace to ask for**, read out of `reason.scope` |
 * | `pending` | **nobody has asked this cluster yet** — the console probes on its own loop and answered out of what it holds | the notice, and **no remedy and no retry**. There is nothing to fix and pressing anything cannot make the probe arrive sooner |
 * | 4xx/5xx | a problem object, not an envelope — a cluster this console does not serve | `Failure`, a different component with the problem `code`: it is not one of the six and drawing it as one sends a person to look at a cluster |
 *
 * `reason.code` is what anything branches on and `reason.message` is what is
 * rendered. Nothing here reads the message except to print it.
 *
 * ## The column declares what its cells cost
 *
 * `Column` carries `secondary`, `gauged`, `copyable`, `prose` and `role`, and
 * every one of them is read here rather than guessed:
 *
 * | Member | Read as | Why it cannot be derived |
 * |---|---|---|
 * | `prose` | `GridColumn.wrap` | what marks a column as a sentence is a property of the view model, not of its label |
 * | `gauged`, `copyable` | `GridColumn.reserve` | the first render has no bar and no control to measure, and no later render is allowed to resize around one |
 * | `role` | how the cell is drawn | *after a drag, `first` is whatever was dragged there, and without this an age is styled as a name on every row* |
 * | `secondary` | whether Reset is offered | it separates *the user hid this* from *it is off by default*, which is what decides whether there is anything to forget |
 *
 * ## Clicking a row opens the drawer, and an ambiguous row opens none
 *
 * **An ambiguous selection opens no drawer.** The rule is `../object/address.ts`,
 * which answers `undefined` for a row that does not name exactly one object, and
 * a row that cannot be addressed is not given a tabstop, a pointer or a click. A
 * list's `namespace` is a selection, so it is never read raw as one name: a
 * namespace called `argocd,kube-system` must not be addressed.
 *
 * **The row is the control and the name inside it is not**: a name that was
 * also a link would fire both, so the drawer opened and the page navigated out
 * from under it. The namespace control, the addresses, the copy button and the
 * selection tick decline the click by carrying `data-no-open`, which `DataGrid`
 * reads, and each of the four is asserted: `ObjectDrawer.test.tsx` for the
 * namespace, the addresses and the tick, `CopyButton.test.tsx` for the copy.
 *
 * A `role: 'namespace'` cell re-asks this list for that one namespace. The
 * namespace is otherwise read from the address bar and honored, so a bookmark
 * works and the empty state can say which namespace it means.
 */
export function ResourceList(): ReactNode {
  const { cluster, kind } = useParams<{ cluster: string; kind: string }>();
  // What extensions add to this list, read once per list.
  const contributed = useExtensionColumns(cluster, kind);
  // The older `?ns=` and `?q=`, moved to the names read below
  // (`legacyParameters.ts`): an old link narrows the list it names.
  useLegacyListParameters();

  const filters = useListFilters();
  const { namespaces, setNamespaces, chosen, rewrite } = filters;

  const { query, answer, page, standingIn, answered, answersFilter, namespaced } = useListQuery(
    cluster,
    kind,
    filters,
  );

  /*
    The namespaces to pick from, and they are read separately from the list.

    `GET /api/c/{cluster}/namespaces` is its own endpoint with its own four arms,
    and that separation is what lets the menu say *why* it is short rather than
    drawing a cluster with two namespaces as a cluster with two namespaces. A
    real cluster always has `default` and `kube-system`, so "none" is nearly
    always a failure wearing an empty answer's clothes.
  */
  const namespaceList = useNamespaces(cluster);

  const { scope, selection, offersSelection } = useListSelection(cluster, kind, page);
  // Rows are ticked and the selection bar holds the panel's bottom-right corner.
  const ticked = offersSelection && selection.count > 0;
  const kindsOnPage = useKindsOnPage();
  const [sort, setSort] = useListSort(cluster, kind, scope);

  // Where the user is, remembered for this cluster (`useRememberedPlace`).
  const chooseNamespaces = useRememberedPlace(cluster, kind, namespaced, namespaces, setNamespaces);

  // Back to the top on a new kind, and when a new filter's answer lands.
  const panel = useScrollReset(cluster, kind, answered);

  const { showInDrawer, gone, setGone } = useOpenInDrawer({
    cluster,
    kind,
    page,
    answer,
    standingIn,
    filters,
  });
  const { rowActionFor, rowMenu, setRowMenu, rowMenuFor, copied, copyName } = useRowActions({
    cluster,
    kind,
    showInDrawer,
    gone,
    setGone,
  });
  /* The verb chosen from it goes through the drawer's own flow - preview, the
     plan dialog, the act, the re-reads - and not a copy of it. */
  const rowAct = useObjectAct();

  const { arranged, shown, compact, onShow, onReset, onDensity, onResize, onPlace } =
    useColumnControls(cluster, kind, page);

  const { droppedHere, dismissDropped } = useDroppedFilters(cluster, kind, page, standingIn, filters, contributed);
  const chips = chipsOf(page, chosen);

  // The rows an extension column's filter leaves: the console applies it, as
  // the server cannot see those columns.
  const narrowed = (listPage: ListPage): ListPage => ({
    ...listPage,
    rows: [...narrowByExtensionColumns(listPage.rows, chosen, contributed)],
  });

  const failed = answer !== undefined && answer.state !== 'ok' ? answer : undefined;
  const refusedSelector = invalidSelector(query.error, filters.settledSelector);

  // The kind's name: the answer's label, else the menu's for a read that was
  // refused or failed and so carries none. The address's segment only where
  // neither is in hand.
  const menuTitle = useMenuTitle(cluster, kind);
  const title = page?.label ?? menuTitle ?? kind;

  // What the topbar's breadcrumb and the tab title say. The list refreshes on
  // this page's `refreshSeconds` interval, through `useResourceList`.
  useDeclarePageFacts({ title: title ?? null });

  return (
    <div className="flex h-full min-h-0 flex-col">
      <ListToolbar
        title={title}
        kind={kind}
        page={page}
        namespaced={namespaced}
        namespaceList={namespaceList}
        namespaces={namespaces}
        chooseNamespaces={chooseNamespaces}
        filters={filters}
        standingIn={standingIn}
        chips={chips}
        droppedHere={droppedHere}
        dismissDropped={dismissDropped}
        shown={shown}
        compact={compact}
        onShow={onShow}
        onReset={onReset}
        onDensity={onDensity}
        contributed={contributed}
      />

      <ListNotes page={page} gone={gone} failure={rowAct.failure} />

      {rowMenu === undefined ? null : (
        <RowMenu
          identity={rowMenu.identity}
          anchor={rowMenu.anchor}
          onClose={() => {
            setRowMenu(undefined);
          }}
          // One confirmation at a time: while a preview is in flight or its
          // dialog is open, the menu draws its verbs refused and says why, so
          // a choice is never dropped in silence.
          busy={rowAct.busy}
          onChoose={(action) => {
            rowAct.start(rowMenu.identity, action);
          }}
          onOpen={rowMenu.open}
          onCopy={copyName}
        />
      )}
      {rowAct.dialog}

      {/*
        `relative` is the positioning context the selection bar needs. The bar
        is out of the flow so that ticking a row moves nothing — a bar that
        takes height on appearing pushes the table down at the instant the
        user clicks, and the next tick of a run lands on the wrong row.
      */}
      <div className="relative flex min-h-0 flex-1 flex-col">
        {copied === undefined ? null : <CopiedNote copied={copied} />}
        {offersSelection && cluster !== undefined && kind !== undefined ? (
          <ListSelectionBar selection={selection} cluster={cluster} kind={kind} page={page} />
        ) : null}

        {/*
          `id="panel"` and nothing between it and the grid: `theme.css` scopes
          the grid's fixed layout to `#panel > .table-scroll > .kt-grid`,
          so an extra wrapper here silently takes the whole rule with it.
          `ValueView` returns a fragment, which is why it can sit in between.
        */}
        <div
          ref={panel}
          id="panel"
          // `pb-16` while rows are ticked or the "+" is drawn: the selection
          // bar and the "+" float over the panel's foot, deliberately out of
          // the flow so a tick moves nothing and scrolling never hides the
          // "+", and without room either would sit over the last row with no
          // way to scroll that row clear. Room after the rows moves none of
          // them.
          // `relative`, so a row's copy button's `sr-only` live region is
          // clipped and scrolled by this panel: positioned only by the wrapper
          // above, it would escape the panel and scroll `main` beside it
          // (`Shell.tsx` says why).
          // No padding of its own: the heading row meets the toolbar and both
          // edges. `PANEL_BOX` gives a boxed notice its margin.
          className={`relative min-h-0 flex-1 overflow-auto ${PANEL_BOX}${ticked || page?.create !== undefined ? ' pb-16' : ''}`}
          // The rows are the previous filter's while the next is read.
          // Said to assistive technology here and to the eye by the
          // box's marker; the rows themselves are left exactly as they were,
          // because dimming them on every settled word is a flicker of its own.
          aria-busy={standingIn ? true : undefined}
        >
          {refusedSelector !== undefined ? (
            /* A selector the server refused: one line, the server's sentence on
               hover. Never the rows of another filter, never an empty list. */
            <p role="alert" data-invalid-selector className="p-3 text-sm text-bad" title={refusedSelector}>
              Invalid label selector
            </p>
          ) : (
          <ValueView query={query} what={listLabel(kind, cluster)}>
            {(listPage) => (
              <Grid
                page={narrowed(arranged(listPage))}
                contributed={contributed}
                rowAction={rowActionFor(listPage)}
                rowMenu={rowMenuFor(listPage)}
                rowMenuOpen={rowMenu?.id}
                compact={compact}
                onResize={onResize}
                onPlace={onPlace}
                sort={sort}
                onSort={(key) => {
                  setSort(nextSort(sort, key));
                }}
                {...(offersSelection ? { selection } : {})}
                selected={namespaces}
                onPickNamespace={(picked) => {
                  /*
                    A namespace cell **replaces** the selection rather than
                    widening it . The
                    menu is where a selection is built up; a click in the table
                    is "show me these", which is the gesture somebody makes after
                    finding one interesting row.
                  */
                  chooseNamespaces([picked]);
                }}
                empty={
                  <EmptyList
                    page={listPage}
                    namespace={namespaces.join(', ')}
                    filter={composeFilter(answersFilter)}
                    chosen={chips.map((chip) => `${chip.column}: ${chip.value}`)}
                    reason={answer?.reason}
                    onClearFilter={() => {
                      // One navigation for the box and the control: two writes
                      // in one handler lose one of the two.
                      rewrite({ selector: '', search: '' }, []);
                    }}
                  />
                }
              />
            )}
          </ValueView>
          )}

          {/*
            A remedy for the four failures, and **deliberately none for
            `pending`**.

            `failed.state` is `ReasonOnlyState`, which is the four plus
            `pending`, and `isFailedState` narrows it so that a new state stops
            this line compiling — the forcing function.
            `Remedy` answers *what does the user do next*, and for a cluster
            nobody has asked yet the answer is *nothing*: there is no fault, no
            role to request and nothing to install. An arm there would be the
            console inventing a next step for a question it has not put yet, and
            `StateNotice` already says the true one.
          */}
          {failed !== undefined && isFailedState(failed.state) ? (
            <Remedy state={failed.state} reason={failed.reason} />
          ) : null}
        </div>
        {/*
          The Kinds button, folded with no rail, sits over this row
          (`KindsToggle.tsx`): a row of its own under the panel, so it covers
          no cell at any scroll. Only for that button: the "+" floats.
        */}
        {kindsOnPage ? (
          <div data-kinds-row className="min-h-13 shrink-0 border-t border-line pointer-coarse:min-h-14" />
        ) : null}
        {/*
          The "+", floating over the list's bottom-right corner: outside the
          panel, so scrolling never moves it; inside this column, so a drawer
          opens beside it and the dock below it. The panel keeps room after its
          last row for it. Not while rows are ticked: the selection bar takes
          the same corner and the person is acting on what exists.
          `CreateObject.tsx` says the rest.
        */}
        {ticked || page?.create === undefined || cluster === undefined || kind === undefined ? null : (
          <CreateObject
            cluster={cluster}
            kind={kind}
            label={page.label}
            create={page.create}
            namespaced={page.namespaced}
            available={namespacesOf(namespaceList.data)}
            current={namespaces}
            className="absolute right-4 bottom-3 z-10"
          />
        )}
        <SidewaysCue
          panel={panel}
          answer={answer}
          columns={`${compact ? 'compact' : 'comfortable'} ${(shown?.columns ?? [])
            .map((column) => `${column.key}:${String(column.visible)}:${String(column.width ?? '')}`)
            .join(',')}`}
        />
      </div>
    </div>
  );
}

/** The server's sentence when it refused the selector the list was asked with
 *  (its own code, `invalid_selector`), else undefined. Any other bad request -
 *  a search over its bound, a filter too long - is drawn as the failure it is. */
function invalidSelector(error: unknown, selector: string): string | undefined {
  if (selector === '' || !(error instanceof ApiError)) return undefined;
  return error.code === 'invalid_selector' ? error.message : undefined;
}

function listLabel(kind: string | undefined, cluster: string | undefined): string {
  if (kind === undefined) return 'this list';
  return cluster === undefined ? kind : `${kind} on ${cluster}`;
}
