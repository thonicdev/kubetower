import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';

import { KeyLayer } from '../keyboard/layers';
import { useEscape } from '../keyboard/useKeyLayer';
import { focusRing, focusRingInside } from '../focus';
import { MenuScrim } from './MenuScrim';
import type { ListResult, Namespace as ApiNamespace } from '../api';
import { toggled } from './useUrlList';

/**
 * Which namespaces this list is narrowed to.
 *
 * The largest single control on the panel: without it the only way to narrow
 * a list would be to edit the address bar.
 *
 * ## Where it sits
 *
 * In the panel head, **immediately after the `<h1>`**. Not inside the right-hand
 * tools group, which carries `margin-left: auto` — leftmost within a group
 * pinned to the right edge is not left at all; and not before the `<h1>`, where
 * the page would lose its name to a control. The heading says what you are
 * looking at, the namespace says which of them, and everything that changes
 * *how* the rows are drawn stays at the far right.
 *
 * ## It is a selection, and that is why the parameter had to change
 *
 * Several namespaces can be on at once, so each entry is a toggle and carries
 * `aria-pressed` rather than `aria-current`: a reader announcing three entries as
 * "current page" describes a page that does not exist. Each click is *"the
 * selection I have now, with this one flipped"* (`toggled`) — an index or a
 * whole new list would be a claim about the page the browser last drew.
 *
 * `?namespace=` carries a list: a picker that can express a selection against
 * a parameter that cannot carry one silently narrows what the operator asked
 * for.
 *
 * ## The typed box
 *
 * Forty namespaces are not found by scrolling. Three decisions, and each is a
 * thing that goes wrong without it:
 *
 * 1. **The box is not the address bar.** This value is *a selection that has not been applied
 *    yet*, and reflecting it would put `?namespace=kube-sys` in the address while
 *    the table below showed something else — and F5 believes the address. Here
 *    the box is local state and only Enter or a tick writes anything.
 * 2. **The term is the fragment after the last comma.** The box holds the whole
 *    selection, `argocd,kube-system,`, and the operator types the next name onto
 *    the end of it. Matching the whole value against each row would hide every
 *    row the moment anything was selected — a control that breaks as soon as it
 *    is used once.
 * 3. **"Nothing matched" is said.** A list that has silently gone empty under a
 *    search reads as a cluster with no namespaces: empty and failed are
 *    different answers, inside a menu too. And the line offers Enter, because a namespace the cluster's list
 *    does not carry may still exist — the list may be stale, or the caller may
 *    not be allowed to enumerate.
 *
 * **"All namespaces" is never hidden by a search** (`data-ns` is on the toggles
 * and on nothing else), because a search that hides the way out is a
 * menu you have to reload to escape.
 *
 * ## The way out of the box
 *
 * *A control you type into needs a way out of the box that is not
 * Tab-and-count.*
 * `↓`/`↑` move the focus between the box and the matching entries, and Enter on
 * an entry ticks it with the menu left open. `arrows` below carries the whole of
 * it — which mechanism, which it refuses, and where it stops.
 *
 * ## What is composed here rather than sent
 *
 * The summary — "All namespaces", one name, two names, or "3 namespaces" — has
 * no contract member. It is composed here, and that is the same license
 * `EmptyList` takes: it restates **this client's own request** rather than
 * guessing at an answer. With three selected, printing
 * all three names run together grows a control in a flex row and pushes the
 * filter box off the end of the panel. So it counts past two, and the whole list
 * is on the `title`.
 */

export interface NamespaceMenuProps {
  /** Every namespace this cluster has, or none while they are being read. */
  available: readonly string[];
  /** A sentence about the list itself rather than an entry in it — "could not be
   *  asked", "answered with none", "read four minutes ago". Drawn as its own row
   *  and never as a toggle, because it has no tick and is not pressed, and a
   *  reader announcing it as an unpressed toggle describes a control that does
   *  not exist. */
  note?: string | undefined;
  /** The current selection. Empty is every namespace, which is not the same as
   *  `default`. */
  selected: readonly string[];
  onChange: (selection: readonly string[]) => void;
}

export function NamespaceMenu({
  available,
  note,
  selected,
  onChange,
}: NamespaceMenuProps): ReactNode {
  const [open, setOpen] = useState(false);
  /*
    The box's text, and **which opening of the menu it belongs to**.

    Every opening starts from what the selection actually is, with the caret at
    the end, so the next keystroke continues it rather than landing in the
    middle. The box is a view of the selection and a stale one is a control
    arguing with the table under it.

    Seeded **during render** and not in an effect, which is the same rule the
    sort on `ResourceList` follows and for the same reason: a `setState` in an
    effect paints once with the previous opening's text in the box, and that
    paint is long enough to type into. `react-hooks/set-state-in-effect` says so
    as a lint error.

    A session counter rather than `open` alone, so that closing and reopening on
    the same selection still re-seeds.
  */
  const [session, setSession] = useState(0);
  const [box, setBox] = useState<{ session: number; text: string }>({ session: 0, text: '' });
  const panelId = useId();
  const hintId = useId();
  const input = useRef<HTMLInputElement>(null);
  const panel = useRef<HTMLDivElement>(null);

  useEscape(
    KeyLayer.Menu,
    () => {
      setOpen(false);
    },
    open,
  );

  /*
    The trailing comma is deliberate: `parseNamespaces` drops an empty part, so
    `argocd,` *is* `argocd`, and without it the next thing typed is glued onto the
    last name.
  */
  let typed = box.text;
  if (box.session !== session) {
    typed = selected.length === 0 ? '' : `${selected.join(',')},`;
    setBox({ session, text: typed });
  }
  const setTyped = (text: string) => {
    setBox({ session, text });
  };

  /*
    The focus, which is the half that genuinely is an effect: it touches the DOM
    and nothing about it is state. Keyed on the session so a reopening focuses
    again.
  */
  useEffect(() => {
    if (!open) return;
    const node = input.current;
    if (node === null) return;
    try {
      node.focus({ preventScroll: true });
      node.setSelectionRange(node.value.length, node.value.length);
    } catch {
      // A browser that refuses either — focus during an unrelated gesture, or
      // setSelectionRange on an input type it does not support it on. The box is
      // still typeable; only the convenience is lost.
    }
  }, [open, session]);

  const term = lastTerm(typed);
  const matching = term === '' ? available : available.filter((name) => name.includes(term));

  /*
    The way out of the box, and what it costs.

    **Real focus, moved with `.focus()` — not a roving tabindex and not
    `aria-activedescendant`.** Both of those exist to give one tab stop to a list
    of things that are not separately focusable, and these entries are ordinary
    `<button>`s that already are one each. A roving tabindex would take them
    *out* of the tab order to solve a problem this menu does not have; and
    `aria-activedescendant` would leave the focus in the box and merely *claim* a
    row was active, which makes Enter the box's Enter — the box's Enter shows
    what the box says and closes the menu, so a row would be impossible to tick
    with the keyboard while the whole point is to tick it. Moving the focus for
    real means Enter on an entry is the browser activating a button, which is why
    **nothing here handles Enter on a row**: a handler that did would fire
    alongside the native click and toggle the namespace twice, which reads as the
    key doing nothing at all.

    **The rows are read from the document under this panel** (`[data-ns]`),
    rather than from a ref per entry: what is rendered is the
    answer to "what can be focused", and it cannot go stale behind an entry React
    has already unmounted. It is also why focus can never land on a filtered-out
    row — a row that does not match is not rendered, so it is not in this list.

    **`data-ns` is on the namespace toggles and on nothing else**, which buys
    two things: "All namespaces" and the
    note rows are never walked into, and `↓` from the box lands on the first
    *match*. Focus sitting on "All namespaces" is not in the list either, so it
    behaves as an off-list row — `↓` to the first match, `↑` back to the box.

    **It stops at both ends; it never wraps**, deliberately: past the last match `list[at + 1]` is undefined and
    the handler returns *without* preventing the default, so the menu scrolls
    rather than the focus jumping silently back to the top. Up from the first
    match returns to the box, because "a dead key at the top of a list is how an
    operator concludes the keyboard is not supported here". Up *in* the box
    refocuses the box and prevents the default, which is the one line here that
    looks like a no-op and is not: `↑` in a text field otherwise throws the caret
    to position 0, and the caret staying at the end is what lets the next name
    continue the selection.
  */
  const arrows = (event: KeyboardEvent<HTMLElement>): void => {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
    const list = [...(panel.current?.querySelectorAll<HTMLElement>('[data-ns]') ?? [])];
    if (list.length === 0) return;
    // -1 in the box, and -1 on "All namespaces" or a note, which is what makes
    // both of those behave as off-list rows.
    const at = list.indexOf(event.currentTarget);
    const next = event.key === 'ArrowDown' ? list[at + 1] : at <= 0 ? input.current : list[at - 1];
    if (next === undefined || next === null) return;
    event.preventDefault();
    try {
      next.focus({ preventScroll: false });
    } catch {
      // Same bargain as the opening focus above: a browser that refuses leaves
      // the control usable and loses only the convenience.
    }
  };

  return (
    <div className="relative">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={panelId}
        title={selected.length === 0 ? 'Every namespace' : selected.join(', ')}
        onClick={() => {
          setOpen((was) => {
            // A new session on every opening, which is what re-seeds the box.
            if (!was) setSession((n) => n + 1);
            return !was;
          });
        }}
        // The toolbar's height (`toolbar.ts`), as the search box beside it.
        className={`h-8 max-w-56 truncate rounded-md border border-line px-2 text-xs text-muted ${focusRing}`}
      >
        {summary(selected)}
      </button>

      {open ? (
        <>
          {/* A real element in the tree, so the click that dismisses this is an
              ordinary React handler and nothing is bound to `document` -
              `MenuScrim` carries the reason, once, for every menu. */}
          <MenuScrim
            onDismiss={() => {
              setOpen(false);
            }}
          />

          {/* `left-0`, not `right-0`: this control sits at the near end of the
              panel head, and a panel anchored right would drop leftwards over the
              table. */}
          <div
            id={panelId}
            ref={panel}
            className="absolute left-0 z-20 mt-1 max-h-96 w-72 overflow-y-auto rounded-lg border border-line bg-panel-2 p-2 shadow-lg"
          >
            {/*
              A real label and not a placeholder. A placeholder is gone the moment
              anything is typed, which is precisely when "comma-separated" is the
              thing the operator needs to be told — and a box whose only name is a
              placeholder is announced by a reader as an unlabeled text field.
            */}
            <label className="block text-xs text-muted" htmlFor={`${panelId}-box`}>
              Namespaces, comma-separated
            </label>
            <input
              id={`${panelId}-box`}
              ref={input}
              type="text"
              value={typed}
              autoComplete="off"
              spellCheck={false}
              aria-describedby={hintId}
              placeholder="type a namespace…"
              onChange={(event) => {
                setTyped(event.target.value);
              }}
              onKeyDown={(event) => {
                arrows(event);
                if (event.key !== 'Enter') return;
                event.preventDefault();
                // Enter shows what the box says, whatever the list below thinks:
                // the cluster's namespace list may be stale, or the caller may
                // not be allowed to enumerate, and the name may still be real.
                onChange(splitTyped(typed));
                setOpen(false);
              }}
              // The ring inside, and the border goes transparent under it, exactly as the filter box does.
              className={`mt-1 w-full rounded-md border border-line bg-bg px-2 py-1 text-sm ${focusRingInside} focus-visible:border-transparent`}
            />
            <p id={hintId} className="mt-1 text-xs text-muted">
              Press Enter to list the namespaces typed here. Separate several with commas.
            </p>

            {term !== '' && matching.length === 0 ? (
              <p data-note="no-match" className="mt-2 text-xs text-warn">
                No namespace here matches <code>{term}</code>. Press Enter to ask this cluster for
                it anyway.
              </p>
            ) : null}

            {note === undefined || note === '' ? null : (
              // First among the entries, because a note under forty namespaces is
              // a note nobody reads — and above it is the box, which is the
              // control rather than a remark about the list.
              <p data-note="namespaces" className="mt-2 text-xs whitespace-normal text-muted">
                {note}
              </p>
            )}

            <ul className="mt-2">
              {/* The way back to no filter, and a search never hides it. */}
              <li>
                <Entry
                  label="All namespaces"
                  pressed={selected.length === 0}
                  onKeyDown={arrows}
                  onClick={() => {
                    onChange([]);
                    setOpen(false);
                  }}
                />
              </li>
              {matching.map((name) => (
                <li key={name}>
                  <Entry
                    label={name}
                    name={name}
                    pressed={selected.includes(name)}
                    onKeyDown={arrows}
                    onClick={() => {
                      // The selection with this one flipped, and the menu stays
                      // open: picking two namespaces is one gesture, not two.
                      onChange(toggled(selected, name));
                    }}
                  />
                </li>
              ))}
            </ul>
          </div>
        </>
      ) : null}
    </div>
  );
}

/** One toggle. `aria-pressed` and not `aria-current`, because several can be on
 *  at once and a reader announcing three of them as the current page describes a
 *  page that does not exist. */
function Entry({
  label,
  name,
  pressed,
  onClick,
  onKeyDown,
}: {
  label: string;
  /** The namespace this entry is, which becomes `data-ns` — and is absent on
   *  "All namespaces". `arrows` walks `[data-ns]`, so
   *  what this attribute really decides is which entries the keyboard visits. */
  name?: string;
  pressed: boolean;
  onClick: () => void;
  onKeyDown?: (event: KeyboardEvent<HTMLElement>) => void;
}): ReactNode {
  return (
    <button
      type="button"
      data-ns={name}
      aria-pressed={pressed}
      onClick={onClick}
      // No `onKeyDown` case for Enter: a focused `<button>` is activated by the
      // browser, and a handler here would fire beside that native click and
      // toggle twice. See `arrows`.
      onKeyDown={onKeyDown}
      className={`flex w-full items-center gap-2 rounded-sm px-1 py-1 text-left text-sm ${focusRing}`}
    >
      {/* The tick keeps its width when empty so the labels stay in a column. */}
      <span aria-hidden="true" className="inline-block w-3 shrink-0">
        {pressed ? '✓' : ' '}
      </span>
      <span className="min-w-0 flex-1 truncate">{label}</span>
    </button>
  );
}

/**
 * The label on the control.
 *
 * It counts past two rather than naming every one, and that is a measured
 * decision rather than brevity: printing each selected name run together
 * grows the control inside a flex row and pushes the
 * filter box off the end of the panel. The whole list is on the `title`.
 */
export function summary(selected: readonly string[]): string {
  if (selected.length === 0) return 'All namespaces';
  if (selected.length <= 2) return selected.join(', ');
  return `${String(selected.length)} namespaces`;
}

/** The piece after the last comma, lowercased. Namespaces are DNS-1123 labels so
 *  they are lower case already; what is *typed* need not be. */
export function lastTerm(value: string): string {
  const parts = value.split(',');
  return (parts[parts.length - 1] ?? '').trim().toLowerCase();
}

/** What the box means when Enter is pressed. The trailing comma is deliberate in
 *  the box — it keeps the next name from being glued onto the last — and is
 *  dropped here, exactly as `parseNamespaces` drops an empty part. */
export function splitTyped(value: string): string[] {
  const seen = new Set<string>();
  for (const part of value.split(',')) {
    const name = part.trim();
    if (name !== '') seen.add(name);
  }
  return [...seen].sort();
}

/**
 * The names the menu offers, and **only from an arm that has names**.
 *
 * `asList` narrows before this sees it, so an `unreadable` cluster has no `items`
 * to reach for and cannot be drawn as a cluster with no namespaces. That
 * distinction is the whole reason the endpoint has four arms rather than
 * returning `[]` on a failure.
 */
export function namespacesOf(answer: ListResult<ApiNamespace> | undefined): readonly string[] {
  if (answer === undefined || answer.state !== 'ok') return [];
  return answer.items.map((one) => one.name);
}

/**
 * What the menu says about its own list, where there is something to say.
 *
 * Three cases and they are three different things to conclude, which is why one
 * sentence for all of them is the defect: the cluster **could not be asked**, it
 * **answered with none**, or these were **read earlier** and kept because it has
 * since stopped answering. The third is `ok` carrying `reason.code:
 * stale_reading`, and drawing it as a failure would empty a selector that is
 * still perfectly usable.
 *
 * The server's own sentence wins wherever there is one: `Reason.message` is
 * permitted on a successful arm precisely so a qualified answer can word itself.
 */
export function namespaceNote(answer: ListResult<ApiNamespace> | undefined): string | undefined {
  if (answer === undefined) return undefined;
  if (answer.state === 'ok') {
    return answer.reason?.message;
  }
  if (answer.state === 'empty') {
    return answer.reason?.message ?? 'No namespaces.';
  }
  return answer.reason.message;
}
