import { useEffect, useRef, useState, type ReactNode } from 'react';
import { useParams } from 'react-router';

import { useNamespaces, type Column } from '../../api';
import { ColumnMenu } from '../../components/ColumnMenu';
import { arrange } from '../../components/columns';
import { DataGrid, type GridColumn, type GridSort } from '../../components/DataGrid';
import { FilterBox } from '../../components/FilterBox';
import { DroppedFilters, FilterChips, FilterMenu } from '../../components/FilterMenu';
import { NamespaceMenu, namespaceNote, namespacesOf } from '../../components/NamespaceMenu';
import { TableMenu } from '../../components/TableMenu';
import { useUrlList } from '../../components/useUrlList';
import { useUrlParameter } from '../../components/useUrlParameter';
import { rememberNamespaces } from '../../shell/namespaceMemory';
import { useDeclarePageFacts } from '../../shell/pageFacts';
import { useShell } from '../../shell/ShellContext';
import { ListView } from '../../state/Result';
import { PANEL_BOX } from '../../state/StateNotice';
import { OPEN_PARAMETER, parseOpen } from '../object/address';
import { parseChosen, sameChosen, toggledChosen, useChosenFilters, type ChosenFilter } from '../resources/listFilters';
import { useColumnControls } from '../resources/useColumnControls';
import { helmKey, useHelmReleases, type HelmRelease } from './data';
import { ReleasePanel } from './HelmRelease';
import { ReleaseStatus, Updated } from './Parts';

/**
 * The Helm releases on one cluster, at `/c/:cluster/helm`.
 *
 * ## A resource list, drawn as one
 *
 * The same panel head as `ResourceList` - heading, namespace menu, count, then
 * the search box, the filter funnel, Columns and the `⋯` at the far end - and
 * the same `DataGrid`, so the headings, the widths, the sort and the row
 * affordance are the ones every other list has. A row opens the release in the
 * drawer (`ReleasePanel`, chrome `drawer`), whose "Open as page" control is the
 * release's own route.
 *
 * **The columns are arranged as a resource list's are.** The answer carries
 * them (`columns`, `columnsCustomised`) as the console's column store keeps
 * them under the kind `helm`, and `useColumnControls` - the resource lists'
 * own - posts every gesture to that store, or keeps it in this browser where
 * the console keeps no preferences. The server declares the columns and which
 * of them the funnel offers; this page draws each cell, matched by the
 * column's label.
 *
 * **The funnel filters in the browser**, as the search box does here: the whole
 * answer is in hand, so the values offered for a column are those of the rows
 * the search and the other columns' filters leave, and the address carries the
 * choices as a resource list's does (`?filter=Status:failed`).
 *
 * What a resource list has and this does not: the label selector (a release is
 * a Secret's payload, not an object whose labels this list reads), row
 * selection and the row menu (no Helm action is offered here).
 *
 * **Two version columns and not one**, deliberately: a release has a chart
 * version and an application version, they are different numbers, and a single
 * column labeled "Version" is a question rather than an answer.
 *
 * ## The empty arm is the one that matters
 *
 * An empty table here would say this cluster installs nothing with Helm, which
 * is the most reassuring possible way to be wrong. So `empty` names the
 * selection it was asked under, and the four failure arms each draw their own -
 * `unreachable`, `unreadable`, `undecodable` and `absent` are mapped apart on
 * the server and must not arrive at one paragraph here.
 */
export function HelmList(): ReactNode {
  const { cluster } = useParams<{ cluster: string }>();
  // The repeatable `namespace` parameter, as on a resource list; a
  // comma-joined one from an older link reads the same (`useUrlList`).
  const [namespaces, setNamespaces] = useUrlList('namespace');
  const [search, setSearch] = useUrlParameter('search');
  const { chosen, setChosen } = useChosenFilters();
  const [sort, setSort] = useState<GridSort | undefined>(undefined);
  const releases = useHelmReleases(cluster, namespaces);
  const namespaceList = useNamespaces(cluster);
  const { openDrawer, closeDrawer } = useShell();
  // The tab title and the breadcrumb's last segment: "Helm releases", the
  // menu's word for it (`helmNavLabel`).
  useDeclarePageFacts({ title: 'Helm releases' });

  /*
    The namespace chosen here is the cluster's choice, as
    on a resource list: an address naming namespaces is remembered, and "All
    namespaces" chosen in the picker clears it, at the moment it is chosen. So
    a namespace picked on the releases carries to the next resource list, and
    the menu's Helm entry, which the server marks `namespaced` like a namespaced
    kind's (`TestTheMenuSaysWhichEntriesOpenANamespacedList`), carries a choice
    made on another list here.
  */
  const namespacesKey = namespaces.join(',');
  useEffect(() => {
    if (cluster === undefined || namespacesKey === '') return;
    rememberNamespaces(cluster, HELM_KIND, namespacesKey.split(','));
  }, [cluster, namespacesKey]);
  const chooseNamespaces = (next: readonly string[]): void => {
    setNamespaces(next);
    if (cluster !== undefined) rememberNamespaces(cluster, HELM_KIND, next);
  };

  const answer = releases.data;
  const accessNote = answer?.accessNote;

  /*
    The columns, arranged. An answer with rows (or none) carries them; a failed
    one carries none and draws no Columns menu, as a resource list that did not
    answer draws none. An answer from a console that sent no columns is drawn
    with the declared ones, unarranged.
  */
  const columnPage =
    answer === undefined || (answer.state !== 'ok' && answer.state !== 'empty')
      ? undefined
      : { columns: answer.columns ?? DEFAULT_COLUMNS, columnsCustomised: answer.columnsCustomised ?? false };
  const { shown, compact, onShow, onReset, onDensity, onResize, onPlace } = useColumnControls(
    cluster,
    HELM_KIND,
    columnPage,
    cluster === undefined ? undefined : helmKey(cluster, []).slice(0, 2),
  );
  const columns = shown?.columns ?? DEFAULT_COLUMNS;

  /*
    The filters: the columns the server marks filterable, the choices the
    address carries on them. A choice naming a column this list cannot be
    filtered by narrows nothing; it is dropped from the address, and said once
    beside the chips, as a resource list says it.
  */
  const filterable = columns.filter((column) => column.filterable && byLabel(column.label) !== undefined);
  const applied = chosen.flatMap((one) => {
    const parsed = parseChosen(one);
    if (parsed === undefined) return [];
    const column = filterable.find((each) => each.label.toLowerCase() === parsed.column.toLowerCase());
    return column === undefined ? [] : [{ parameter: one, column: column.label, value: parsed.value }];
  });
  const [dropped, setDropped] = useState<readonly ChosenFilter[]>([]);
  const known = shown !== undefined;
  const chosenKey = JSON.stringify(chosen);
  const appliedKey = JSON.stringify(applied.map((one) => one.parameter));
  useEffect(() => {
    if (!known || chosenKey === appliedKey) return;
    const kept = JSON.parse(appliedKey) as string[];
    const all = JSON.parse(chosenKey) as string[];
    // The outcome of this rewrite, known only here: the address is about to
    // lose these filters, so they cannot be read back from it at render
    // (`useDroppedFilters` makes the same call on a resource list).
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setDropped(
      all.filter((one) => !kept.includes(one)).flatMap((one) => {
        const parsed = parseChosen(one);
        return parsed === undefined ? [] : [parsed];
      }),
    );
    setChosen(kept);
  }, [known, chosenKey, appliedKey, setChosen]);

  const all = answer !== undefined && answer.state === 'ok' ? answer.items : undefined;
  const searched = all === undefined ? undefined : matching(all, search);
  const filtered = searched === undefined ? undefined : narrowed(searched, applied);
  const note = namespaceNote(namespaceList.data);

  const filterColumns = filterable.map((column) => {
    const read = byLabel(column.label)?.read;
    // The values of the rows the search and every *other* column's filters
    // leave, so choosing one keeps the others on offer: the second value of an
    // OR is chosen from here.
    const others = applied.filter((one) => one.column !== column.label);
    const rows = searched === undefined ? [] : narrowed(searched, others);
    const values = read === undefined ? [] : [...new Set(rows.map((release) => valueOf(read, release)))];
    values.sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
    return {
      label: column.label,
      values,
      chosen: applied.filter((one) => one.column === column.label).map((one) => one.value),
    };
  });

  const open = (release: Pick<HelmRelease, 'namespace' | 'name'>): void => {
    // Keyed on the release, so another row is another panel and nothing the
    // last one held - a revealed value above all - carries over.
    openDrawer(
      <ReleasePanel
        key={`${release.namespace}/${release.name}`}
        cluster={cluster ?? ''}
        namespace={release.namespace}
        name={release.name}
        chrome="drawer"
        onClose={closeDrawer}
      />,
    );
  };

  /*
    **Arriving with `?open=<namespace>/<name>`: that release in the drawer**.
    It is what a release page's Back returns to when the
    page was opened from this drawer, so Back lands on the list with the
    release open again.

    The resource list's arrival, in short (`ResourceList.tsx` says why): the
    arriving commit only takes the name and drops the parameter, because the
    `Drawer` empties itself on a new path *after* this page's effects in that
    same commit; the drawer opens on the next one. Unlike a resource list the
    parameter is not written back: this list never kept its drawer in the
    address. The panel reads the release itself, so a release gone since is
    said by the panel, not by a guess here; a value without a namespace names
    no release and opens nothing.
  */
  const [openParameter, setOpenParameter] = useUrlParameter(OPEN_PARAMETER);
  const pendingOpen = useRef('');
  useEffect(() => {
    if (openParameter === '') return;
    pendingOpen.current = openParameter;
    setOpenParameter('');
  }, [openParameter, setOpenParameter]);
  useEffect(() => {
    if (openParameter !== '' || pendingOpen.current === '') return;
    const target = parseOpen(pendingOpen.current);
    pendingOpen.current = '';
    if (target.namespace === undefined) return;
    open({ namespace: target.namespace, name: target.name });
    // `open` is re-made on every render; what this is about is the parameter
    // leaving the address while a name is pending.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [openParameter]);

  // The grid's columns: the arrangement's, each drawn by this page's own cell.
  const drawn = gridColumns(columns);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="flex flex-wrap items-center gap-3 border-b border-line px-3 py-2">
        <h1 className="text-lg font-medium">Helm releases</h1>
        <NamespaceMenu
          available={namespacesOf(namespaceList.data)}
          {...(note === undefined ? {} : { note })}
          selected={namespaces}
          onChange={chooseNamespaces}
        />
        {/* The count comes from the answer, never from the table. */}
        <p aria-live="polite" className="text-xs text-muted">
          {all === undefined || filtered === undefined
            ? ''
            : filtered.length === all.length
              ? `${String(all.length)} listed`
              : `${String(filtered.length)} of ${String(all.length)} listed`}
        </p>
        <div className="ml-auto flex min-w-0 max-w-full flex-wrap items-center justify-end gap-2">
          <FilterChips
            chips={applied}
            onRemove={(parameter) => {
              setChosen(chosen.filter((one) => !sameChosen(one, parameter)));
            }}
          />
          <DroppedFilters
            filters={dropped}
            onDismiss={() => {
              setDropped([]);
            }}
          />
          <div className="flex min-w-0 max-w-full flex-wrap items-center justify-end gap-2">
            {/* The field, the funnel and Columns are one segmented control, as
                on a resource list; the `⋯` keeps its gap. */}
            <div className="flex min-w-0 items-center">
              <FilterBox
                name="search"
                value={search}
                onChange={setSearch}
                label="Filter Helm releases"
                placeholder="search all columns"
              >
                {/* Not before the list answers, when nothing is known either
                    way; inert, and saying why, where no column is offered. */}
                {shown === undefined ? null : (
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
                <ColumnMenu columns={shown.columns} labelOf={(column) => column.label} onShow={onShow} />
              )}
            </div>
            <TableMenu
              compact={compact}
              onDensity={onDensity}
              onReset={onReset}
              arranged={shown?.columnsCustomised === true}
            />
          </div>
        </div>
        {/* The access review narrowed this list, as a resource list says it:
            a narrowed table and a complete one look alike. The sentence is the
            server's, built from the question and never from the rows. */}
        {accessNote === undefined ? null : (
          <p className="basis-full text-xs text-warn" data-note="access">
            {accessNote}
          </p>
        )}
      </header>

      {/* `id="panel"` and nothing between it and the grid: `theme.css` scopes
          the grid's fixed layout to `#panel > .table-scroll > .kt-grid`.
          `ListView` returns a fragment, which is why it can sit in between. */}
      <div id="panel" className={`relative min-h-0 flex-1 overflow-auto ${PANEL_BOX}`}>
        <ListView
          query={releases}
          what="the Helm releases"
          empty={
            // Named, because "Nothing here." would be a claim about what a
            // namespace contains rather than about whether it is one.
            <p data-empty="helm" className="p-3 text-sm text-muted">
              {namespaces.length > 0
                ? `No Helm release in ${namespaces.join(', ')}.`
                : accessNote === undefined
                  ? 'No Helm release on this cluster.'
                  : 'No Helm release in the namespaces where you may list Secrets.'}
            </p>
          }
        >
          {(items) => (
            <DataGrid
              columns={drawn}
              rows={ordered(narrowed(matching(items, search), applied), sort, drawn)}
              rowKey={(release) => `${release.namespace}/${release.name}`}
              label="Helm releases"
              stickyHead
              compact={compact}
              onResize={onResize}
              onPlace={onPlace}
              sort={sort}
              onSort={(key) => {
                setSort(nextSort(sort, key));
              }}
              empty={
                <p data-empty="filtered" className="text-sm text-muted">
                  {search === ''
                    ? 'No Helm release matched the filters.'
                    : `No Helm release matched “${search}”.`}
                </p>
              }
              // A release whose stored payload could not be decoded is
              // **listed** and marked, never dropped, and opens nothing: there
              // is no panel for a release that would not read.
              rowAction={(release) =>
                release.state === 'ok'
                  ? () => {
                      open(release);
                    }
                  : undefined
              }
            />
          )}
        </ListView>
      </div>
    </div>
  );
}

/** The list's segment, under which the namespace memory records it, and the
 *  kind the console's column store files its arrangement under. */
const HELM_KIND = 'helm';

/** What a column sorts, searches, filters and measures by: the value drawn, or
 *  `undefined` for a dash. */
type Read = (release: HelmRelease) => string | number | undefined;

/** How this page draws one of the columns the server declares, by label. */
interface HelmCell {
  label: string;
  read: Read;
  /** Whether the search box looks in this column: not where the cell draws
   *  something other than what `read` returns. */
  searched: boolean;
  grid: Omit<GridColumn<HelmRelease>, 'key' | 'label'>;
}

function cellOf(
  label: string,
  read: Read,
  cell: (release: HelmRelease) => ReactNode,
  extra: Partial<GridColumn<HelmRelease>> & { searched?: boolean } = {},
): HelmCell {
  const { searched = true, ...grid } = extra;
  return {
    label,
    read,
    searched,
    grid: { measure: (release) => String(read(release) ?? '—'), cell, ...grid },
  };
}

function dash(value: string | number | undefined): ReactNode {
  return value ?? '—';
}

/** Every column the list declares, in the server's default order; the labels
 *  are `helmColumns`' titles in `internal/web`. */
const CELLS: readonly HelmCell[] = [
  cellOf(
    'Name',
    (release) => release.name,
    (release) => <span data-release={`${release.namespace}/${release.name}`}>{release.name}</span>,
  ),
  cellOf(
    'Namespace',
    (release) => release.namespace,
    (release) => <span className="text-muted">{release.namespace}</span>,
  ),
  cellOf('Revision', (release) => release.revision, (release) => dash(release.revision), { align: 'end' }),
  // `message` on the **cell**: `white-space` inherits, so an `overflow-wrap`
  // on a child is inert. The note under the pill is drawn only
  // where the highest revision is not the running one.
  cellOf(
    'Status',
    (release) => (release.state === 'ok' ? release.status : 'could not be read'),
    (release) => <ReleaseStatus release={release} />,
    { wrap: 'message' },
  ),
  cellOf(
    'Chart',
    (release) => release.chart,
    (release) => (
      <>
        {dash(release.chart)}
        {release.deprecated === true ? (
          <span
            title="Deprecated in its Chart.yaml"
            className="ml-1 rounded-sm bg-warn/15 px-1.5 py-0.5 text-2xs text-warn"
          >
            deprecated
          </span>
        ) : null}
      </>
    ),
  ),
  cellOf('Chart version', (release) => release.chartVersion, (release) => dash(release.chartVersion)),
  cellOf('App version', (release) => release.appVersion, (release) => dash(release.appVersion)),
  cellOf(
    'Updated',
    (release) => release.updatedAt,
    (release) => (
      <span className="text-muted">
        <Updated at={release.updatedAt} age={release.age} />
      </span>
    ),
    // The cell draws an age that moves with the clock, not the instant it
    // sorts by, so the search box leaves this column out rather than match a
    // string nobody sees.
    { measure: (release) => release.age ?? '—', searched: false },
  ),
  // Helm's own sentence, and on a failed upgrade it is the API server's whole
  // refusal. Prose, so the cell wraps.
  cellOf('Description', (release) => release.description, (release) => dash(release.description), {
    wrap: 'prose',
  }),
];

function byLabel(label: string): HelmCell | undefined {
  const wanted = label.toLowerCase();
  return CELLS.find((each) => each.label.toLowerCase() === wanted);
}

/**
 * The declared columns, unarranged, for an answer that carries none: the
 * server's keys (`columnKey`: the label in lower case, spaces as dashes, and
 * its index), Name locked, Status and Chart offered to the funnel.
 */
const DEFAULT_COLUMNS: Column[] = CELLS.map((each, index) => ({
  key: `${each.label.toLowerCase().replace(/ /g, '-')}-${String(index)}`,
  label: each.label,
  visible: true,
  order: index,
  width: null,
  sortable: true,
  secondary: false,
  gauged: false,
  copyable: false,
  prose: false,
  role: index === 0 ? 'name' : 'other',
  filterable: each.label === 'Status' || each.label === 'Chart',
  ...(index === 0 ? { lockedNote: 'always shown — it is what says which object each row is' } : {}),
  ...(each.label === 'Revision' ? { align: 'end' as const } : {}),
}));

/** The visible columns in their order, each with this page's cell; a column
 *  this page cannot draw is left out rather than drawn empty. */
function gridColumns(columns: readonly Column[]): GridColumn<HelmRelease>[] {
  return arrange(columns).flatMap((column) => {
    const drawn = byLabel(column.label);
    if (drawn === undefined) return [];
    return [
      {
        ...drawn.grid,
        key: column.key,
        label: column.label,
        sortable: true,
        ...(column.width === undefined ? {} : { width: column.width }),
      },
    ];
  });
}

/** A cell's value as the funnel offers and compares it: the server's dash for
 *  nothing, which the control draws as "(none)". */
function valueOf(read: Read, release: HelmRelease): string {
  const value = read(release);
  return value === undefined || value === '' ? '—' : String(value);
}

/** The rows whose values hold every word of `search`, case aside. */
function matching(releases: readonly HelmRelease[], search: string): readonly HelmRelease[] {
  const words = search
    .toLowerCase()
    .split(/\s+/)
    .filter((word) => word !== '');
  if (words.length === 0) return releases;
  return releases.filter((release) => {
    const text = CELLS.filter((each) => each.searched)
      .map((each) => String(each.read(release) ?? ''))
      .join(' ')
      .toLowerCase();
    return words.every((word) => text.includes(word));
  });
}

/**
 * The rows the chosen filters keep: any of a column's chosen values, and every
 * column with a choice - the server's rule for a resource list
 * (`internal/resources/match.go`). Case aside, as the server compares.
 */
function narrowed(
  releases: readonly HelmRelease[],
  filters: readonly { column: string; value: string }[],
): readonly HelmRelease[] {
  if (filters.length === 0) return releases;
  const perColumn = new Map<string, string[]>();
  for (const one of filters) {
    perColumn.set(one.column, [...(perColumn.get(one.column) ?? []), one.value.toLowerCase()]);
  }
  return releases.filter((release) =>
    [...perColumn].every(([label, values]) => {
      const read = byLabel(label)?.read;
      return read === undefined || values.includes(valueOf(read, release).toLowerCase());
    }),
  );
}

/** A copy in the chosen order; a dash sorts last either way. */
function ordered(
  releases: readonly HelmRelease[],
  sort: GridSort | undefined,
  columns: readonly GridColumn<HelmRelease>[],
): readonly HelmRelease[] {
  if (sort === undefined) return releases;
  const column = columns.find((each) => each.key === sort.key);
  const by = column === undefined ? undefined : byLabel(column.label);
  if (by === undefined) return releases;
  const sign = sort.direction === 'asc' ? 1 : -1;
  return [...releases].sort((left, right) => {
    const a = by.read(left);
    const b = by.read(right);
    if (a === undefined && b === undefined) return 0;
    if (a === undefined) return 1;
    if (b === undefined) return -1;
    if (typeof a === 'number' && typeof b === 'number') return sign * (a - b);
    return sign * String(a).localeCompare(String(b), undefined, { numeric: true });
  });
}

/** Ascending, then descending, then the server's order - `ResourceList`'s. */
function nextSort(current: GridSort | undefined, key: string): GridSort | undefined {
  if (current === undefined || current.key !== key) return { key, direction: 'asc' };
  if (current.direction === 'asc') return { key, direction: 'desc' };
  return undefined;
}
