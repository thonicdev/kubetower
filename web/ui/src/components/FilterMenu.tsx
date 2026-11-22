import { useId, useRef, useState, type ReactNode } from 'react';

import { KeyLayer } from '../keyboard/layers';
import { useEscape } from '../keyboard/useKeyLayer';
import { focusRing, focusRingTight } from '../focus';
import { btnSecondary } from './buttons';
import { CloseButton } from './CloseButton';
import { Icon } from './Icon';
import { toolbarBase, toolbarControl } from './toolbar';
import { MenuScrim } from './MenuScrim';

/**
 * The list's filter control: an icon beside the search box, opening the columns
 * the server marks filterable, each with the values present in the list.
 *
 * The search box still searches every column; filtering by a column's value
 * is found by looking rather than typed into the box: a funnel, and under it
 * the columns that can be filtered and what they hold.
 *
 * **Which columns, and which values, are the server's answer.** `Column
 * .filterable` says a column holds a category worth choosing among, and
 * `Column.filterValues` is what its cells hold across the rows the search and
 * every *other* column's filters leave - so choosing one node keeps the other
 * nodes here, which is the only way a second value of an OR is ever chosen. No
 * counts.
 *
 * The menu stays open while values are ticked, as the namespace menu does:
 * picking two nodes is one gesture, not two.
 *
 * Presentational: it holds whether it is open and nothing else. The page owns
 * the address.
 */
export interface FilterMenuColumn {
  label: string;
  /** What the server offers, sorted. */
  values: readonly string[];
  /** What is chosen on this column, as the address spells it. A chosen value
   *  the server no longer offers is still drawn, ticked, so it can be unticked. */
  chosen: readonly string[];
}

export interface FilterMenuProps {
  columns: readonly FilterMenuColumn[];
  onToggle: (column: string, value: string) => void;
  onClear: () => void;
}

/** What the funnel says on a built-in list that has nothing to offer it. */
export const NOTHING_TO_FILTER = 'Nothing to filter by on this list.';

export function FilterMenu({ columns, onToggle, onClear }: FilterMenuProps): ReactNode {
  const [open, setOpen] = useState(false);
  // The column whose values are showing: the panel opens on the columns alone,
  // and choosing one drops its values beneath it.
  const [expanded, setExpanded] = useState<string | null>(null);
  // One filterable column has nothing to choose between: its values are shown
  // at once, one click and not two.
  const shown = columns.length === 1 ? (columns[0]?.label ?? null) : expanded;
  const panelId = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  // Escape gives the focus back to the funnel: closing the panel unmounts the
  // value that held it, and the focus would otherwise be nowhere (review nit).
  useEscape(
    KeyLayer.Menu,
    () => {
      setOpen(false);
      trigger.current?.focus();
    },
    open,
  );

  const active = columns.reduce((sum, column) => sum + column.chosen.length, 0);
  const label = active === 0 ? 'Filter by column' : `Filter by column, ${String(active)} chosen`;

  /*
    **No column to offer is drawn, and says why.** A funnel left out of a list
    with no filterable column reads as "missing" on Deployments beside Pods
    that have it: an absence nobody can tell from a fault. The rule is the
    server's - a column is offered when it holds a category, a status or a node, never a name, a
    figure or a date (`resources.Column.Filterable`) - so this is where the
    rule is written down, on the control it decides about. `aria-disabled`
    and not `disabled`, so it keeps its place in the tab order and its title
    can be reached.
  */
  if (columns.length === 0) {
    return (
      <button
        type="button"
        aria-disabled="true"
        aria-label={`${label}: ${NOTHING_TO_FILTER}`}
        title={NOTHING_TO_FILTER}
        data-control="filter"
        className={`-ml-px ${toolbarControl} rounded-l-none rounded-r-none cursor-default text-muted opacity-50 ${focusRing}`}
      >
        <Icon name="filter" />
      </button>
    );
  }

  return (
    <div className="relative -ml-px">
      <button
        ref={trigger}
        type="button"
        aria-expanded={open}
        aria-controls={panelId}
        aria-label={label}
        title={label}
        data-control="filter"
        onClick={() => {
          setOpen((was) => !was);
        }}
        className={`${toolbarBase} rounded-l-none rounded-r-none px-2 ${
          active === 0 ? 'border-line text-muted' : 'border-accent text-fg'
        } ${focusRing}`}
      >
        <Icon name="filter" />
        {active === 0 ? null : <span>{active}</span>}
      </button>

      {open ? (
        <>
          <MenuScrim
            onDismiss={() => {
              setOpen(false);
            }}
          />
          {/* `right-0`: this control sits at the far end of the panel head, and
              a panel anchored left would run off the window. */}
          <div
            id={panelId}
            role="group"
            aria-label="Filter by column"
            className="absolute right-0 z-20 mt-1 max-h-96 w-72 overflow-y-auto rounded-lg border border-line bg-panel-2 p-2 shadow-lg"
          >
            {columns.map((one) => (
              <section key={one.label} data-filter-column={one.label}>
                {/* A single column is always open, so its name is a title and
                    not a disclosure: a chevron and `aria-expanded` on a button
                    that does nothing when clicked would lie. */}
                {columns.length === 1 ? (
                  <h3 className="flex w-full items-center gap-2 px-1 py-1 text-sm">
                    <span className="min-w-0 flex-1 truncate">{one.label}</span>
                    <ChosenCount count={one.chosen.length} />
                  </h3>
                ) : (
                  <h3>
                    <button
                      type="button"
                      aria-expanded={shown === one.label}
                      onClick={() => {
                        setExpanded((was) => (was === one.label ? null : one.label));
                      }}
                      className={`flex w-full items-center gap-2 rounded-sm px-1 py-1 text-left text-sm ${focusRing}`}
                    >
                      <span className="min-w-0 flex-1 truncate">{one.label}</span>
                      <ChosenCount count={one.chosen.length} />
                      <Icon name="chevron" />
                    </button>
                  </h3>
                )}
                {shown === one.label ? (
                <>
                <ul className="mb-1 ml-2 border-l border-line pl-1">
                  {offered(one).map((value) => {
                    const pressed = one.chosen.some((one) => one.toLowerCase() === value.toLowerCase());
                    return (
                      <li key={value}>
                        <button
                          type="button"
                          aria-pressed={pressed}
                          onClick={() => {
                            onToggle(one.label, value);
                          }}
                          className={`flex w-full items-center gap-2 rounded-sm px-1 py-1 text-left text-sm ${focusRing}`}
                        >
                          <span aria-hidden="true" className="inline-block w-3 shrink-0">
                            {pressed ? '✓' : ' '}
                          </span>
                          <span className="min-w-0 flex-1 truncate" title={value}>
                            {shownValue(value)}
                          </span>
                        </button>
                      </li>
                    );
                  })}
                </ul>
                {offered(one).length === 0 ? (
                  // Empty because nothing is left, not because the read failed:
                  // a failed list draws no control at all.
                  <p className="text-xs text-muted">No value in the rows listed.</p>
                ) : null}
                </>
                ) : null}
              </section>
            ))}
            {active === 0 ? null : (
              <button
                type="button"
                onClick={() => {
                  onClear();
                  setOpen(false);
                }}
                className={`mt-2 ${btnSecondary}`}
              >
                Clear the filters
              </button>
            )}
          </div>
        </>
      ) : null}
    </div>
  );
}

/**
 * How many values are chosen on a column, beside its name. The figure is drawn
 * and hidden from a reader; the reader is given ", 1 chosen" instead, which the
 * bare figure ran into the name as "Status1" (review nit).
 */
function ChosenCount({ count }: { count: number }): ReactNode {
  if (count === 0) return null;
  return (
    <>
      <span aria-hidden="true" className="shrink-0 text-xs text-accent">
        {count}
      </span>
      <span className="sr-only">, {count} chosen</span>
    </>
  );
}

/**
 * A value as the control draws it. The server's em dash is the cell of a row
 * with nothing in that column - a pod no node has taken - and as an entry of
 * its own it read as a glyph rather than as a choice (review nit), so it is
 * drawn as "(none)". The value sent is still the dash, which is what the cell
 * holds and what the server compares.
 */
function shownValue(value: string): string {
  return value === '—' ? '(none)' : value;
}

/** The server's values, plus any chosen value it no longer offers. */
function offered(column: FilterMenuColumn): string[] {
  const values = [...column.values];
  for (const one of column.chosen) {
    if (!values.some((value) => value.toLowerCase() === one.toLowerCase())) values.push(one);
  }
  return values;
}

/**
 * The filters in effect, as removable chips beside the search box: what
 * narrows the list is on screen, and one click takes
 * any of it away.
 */
export function FilterChips({
  chips,
  onRemove,
}: {
  chips: readonly { parameter: string; column: string; value: string }[];
  onRemove: (parameter: string) => void;
}): ReactNode {
  if (chips.length === 0) return null;
  return (
    <ul aria-label="Filters" className="flex min-w-0 flex-wrap items-center gap-1">
      {chips.map((chip) => (
        <li key={chip.parameter}>
          <button
            type="button"
            data-chip={chip.parameter}
            aria-label={`Remove the filter ${chip.column}: ${shownValue(chip.value)}`}
            title={`Remove the filter ${chip.column}: ${shownValue(chip.value)}`}
            onClick={() => {
              onRemove(chip.parameter);
            }}
            // The toolbar's height (`toolbar.ts`): the chips sit on the line
            // of the search box and its controls.
            className={`flex h-8 max-w-56 items-center gap-1 rounded-md border border-line bg-panel-2 px-2 text-xs ${focusRingTight}`}
          >
            <span className="text-muted">{chip.column}:</span>
            <span className="min-w-0 truncate">{shownValue(chip.value)}</span>
            <Icon name="close" />
          </button>
        </li>
      ))}
    </ul>
  );
}

/**
 * Filters the address asked for and the server did not apply, said once beside
 * the chips until dismissed. The page drops them from the address - a chip
 * would claim a narrowing that did not happen - and without this an old
 * bookmark's filter on a column that is no longer one would draw the whole
 * list with nothing saying why. A label the kind never had is
 * the same case, so the sentence says "not a filter on this list" rather than
 * "no longer".
 */
export function DroppedFilters({
  filters,
  onDismiss,
}: {
  filters: readonly { column: string; value: string }[];
  onDismiss: () => void;
}): ReactNode {
  if (filters.length === 0) return null;
  const named = filters.map((one) => `${one.column}: ${shownValue(one.value)}`).join(', ');
  const sentence =
    filters.length === 1
      ? `Not a filter on this list: ${named}. The list is not filtered by it.`
      : `Not filters on this list: ${named}. The list is not filtered by them.`;
  return (
    <p
      role="status"
      title={sentence}
      className="flex max-w-80 items-center gap-1 rounded-md border border-line px-2 py-1 text-xs text-warn"
    >
      <span className="min-w-0 truncate">{sentence}</span>
      <CloseButton onClick={onDismiss} label="Dismiss" size="inline" />
    </p>
  );
}
