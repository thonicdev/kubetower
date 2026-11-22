import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';

import {
  ApiError,
  queryKeys,
  setRailAssign,
  setRailHide,
  setRailMove,
  setRailNameplate,
  setRailReveal,
  useClusters,
  usePreferences,
  writeInOrder,
  type Cluster,
  type ClusterColour,
} from '../api';
import { ClusterName } from '../components/ClusterName';
import { btnSecondary } from '../components/buttons';
import { Dialog } from '../components/Dialog';
import { focusRingTight } from '../focus';
import { ListView } from '../state/Result';
import { announce } from './announce';
import { iconOf, recolour } from './iconRecipe';
import { ClusterIcon } from './ClusterIcon';
import { arrange, colourLabel, MAX_INITIALS, MAX_NAME, stateSentence } from './clusterState';
import { ColourButton, SwatchGrid } from './Swatches';

/**
 * Arranging the rail: the nameplate editor for every cluster at once, with the
 * hotbar, the order and whether the tile is shown.
 *
 * ## One row per cluster, and no table
 *
 * A table column is as wide as its widest cell: one EKS ARN in a Context
 * column would set the dialog's width and put a horizontal scrollbar under it.
 * A row is a grid whose middle
 * column is `minmax(0, 1fr)` - the tile, then the name, the initials and the
 * color over the context name (cut in the middle, `ClusterName`), then the
 * hotbar, the order, Hide and Reset - so nothing in it can be wider than the
 * dialog.
 *
 * **The color is the inline sixteen where they fit and one button where they
 * do not**: the grid from `md`
 * up, where the dialog is wide enough for it beside the fields, and below that
 * a button in the chosen color that opens the same grid. Both are drawn and
 * the breakpoint chooses, so the switch costs no measurement in JavaScript.
 *
 * ## Four separate writes, and that is the contract rather than a preference
 *
 * `api/openapi.yaml` gives each of these its own route with a body naming one
 * cluster, and says why in the one place it would be tempting to collapse:
 * `hide` and `reveal` are two routes and not one toggle, because "a client with
 * a stale view of what is hidden flips the wrong way". The same argument applies
 * to every field here - a body carrying the whole arrangement lets a panel that
 * has been open for ten minutes re-assert nine tiles as a side effect of
 * renaming one.
 *
 * ## What the nameplate is not
 *
 * It is a person's word for a cluster and it never reaches a path. Contexts
 * are named in URLs; a nameplate that got into one would make two clusters
 * indistinguishable to the router. Every link in the rail is built from
 * `cluster.name` and every label from `cluster.label`.
 *
 * ## Escape, the veil and the keyboard
 *
 * All three come from `components/Dialog`, which is `confirm.js` ported: Escape
 * at `KeyLayer.Dialog` from the one keyboard stack, a click on the veil and not
 * on the box, and the keyboard handed into the dialog on open and back to the
 * Arrange button on close. Nothing here adds a listener of its own, which is the
 * arrangement that stopped a keystroke dismissing the covered dialog and leaving
 * the visible one open.
 */
export function RailArrange({ onClose }: { onClose: () => void }): ReactNode {
  const clusters = useClusters();

  return (
    <Dialog title="Arrange the rail" onClose={onClose} size="wide">
      <ListView query={clusters} what="clusters">
        {(items) => <Rows clusters={arrange(items)} />}
      </ListView>
    </Dialog>
  );
}

function Rows({ clusters }: { clusters: readonly Cluster[] }): ReactNode {
  const queryClient = useQueryClient();
  const plates = usePreferences().data?.rail?.nameplates;

  const refresh = async (): Promise<void> => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: queryKeys.clusters }),
      queryClient.invalidateQueries({ queryKey: queryKeys.preferences }),
    ]);
  };

  /**
   * Every change to the rail says what it did.
   *
   * The sentence is composed where the gesture is, so no attribute has to be
   * read back and deleted to keep the table refresh from announcing it a
   * second time.
   *
   * Only on success, and the failure is not announced here: the error below is a
   * `role="alert"`, which a screen reader reads on its own. Saying it twice is
   * the same defect as not saying it at all, arriving from the other side.
   */
  const write = useMutation({
    mutationFn: (change: { run: () => Promise<void>; said: string }) => change.run(),
    onSuccess: async (_result, change) => {
      announce(change.said);
      await refresh();
    },
    // A change the server applied and could not save is on the rail now, so
    // the rail is read again; the alert below says it will not survive a
    // restart. A refusal changed nothing and leaves the rail as it is.
    onError: async (error) => {
      if (error instanceof ApiError && error.isNotSaved) await refresh();
    },
  });

  /**
   * A swap is two writes and not one, because the route takes one cluster and
   * one order. Posting only the moved tile would leave two tiles claiming the
   * same position, and `arrange()` would then break the tie by label - which
   * reads, to the person who just pressed the arrow, as the move not happening.
   *
   * `writeInOrder`, so a first write the server applied and could not save
   * still sends the second: stopping there would leave exactly the half-swap above.
   */
  function swap(index: number, other: number): void {
    const a = clusters[index];
    const b = clusters[other];
    if (a === undefined || b === undefined) return;
    write.mutate({
      run: () =>
        writeInOrder([() => setRailMove(a.name, other), () => setRailMove(b.name, index)]),
      // Named rather than positional. "Moved to position 3" is a number the
      // person cannot see; "moved above staging" is the arrangement they were
      // looking at - which is the same argument `hotbars.Place` makes about
      // posting a neighbor and a side rather than an index.
      said: other < index ? `${a.label} moved above ${b.label}.` : `${a.label} moved below ${b.label}.`,
    });
  }

  return (
    <>
      <ul className="flex flex-col gap-2">
        {clusters.map((cluster, index) => {
          const stored = plates?.[cluster.name];
          // The swatch drawn as chosen is the person's, or the automatic one
          // the server sent; picking writes the key and only the key.
          const pick = (colour: ClusterColour): void => {
            write.mutate({
              // A stored icon's background follows the color, as from the
              // tile's own menu (`recolour`).
              run: () => setRailNameplate(cluster.name, recolour(colour, stored?.icon)),
              // The swatch's own name, as the grid and its tooltip say it.
              said: `${cluster.label} is now ${colourLabel(colour)}.`,
            });
          };
          return (
            <li
              key={cluster.name}
              data-cluster={cluster.name}
              className={`grid grid-cols-[auto_minmax(0,1fr)] items-center gap-x-3 gap-y-2 rounded-lg border border-line p-2 md:grid-cols-[auto_minmax(0,1fr)_auto] ${
                cluster.hidden === true ? 'opacity-60' : ''
              }`}
            >
              <span
                aria-hidden="true"
                title={stateSentence(cluster)}
                className="flex size-9 shrink-0 overflow-hidden rounded-lg bg-panel-2"
              >
                {/* The tile's icon, drawn as the rail draws
                    it; it is edited from the tile's own editor. The color
                    below is what the default icon is drawn on. */}
                <ClusterIcon recipe={iconOf(cluster.name, cluster.colour, stored?.icon)} fallback={null} />
              </span>

              <div className="min-w-0">
                <div className="flex min-w-0 flex-wrap items-center gap-2">
                  <TextWrite
                    label={`Name for ${cluster.name}`}
                    value={stored?.name ?? ''}
                    placeholder="Name"
                    width="w-40"
                    // The server's own cut (`nameplates.MaxName`): a longer
                    // name would be announced whole and stored cut.
                    maxLength={MAX_NAME}
                    onCommit={(label) => {
                      write.mutate({
                        run: () => setRailNameplate(cluster.name, { label }),
                        said:
                          label === ''
                            ? `${cluster.name} is called by its context name again.`
                            : `${cluster.name} is now called ${label}.`,
                      });
                    }}
                  />
                  <TextWrite
                    label={`Initials for ${cluster.name}`}
                    value={stored?.initials ?? ''}
                    placeholder={cluster.initials}
                    width="w-12"
                    maxLength={MAX_INITIALS}
                    onCommit={(initials) => {
                      write.mutate({
                        run: () => setRailNameplate(cluster.name, { initials }),
                        said:
                          initials === ''
                            ? `${cluster.label} has its derived initials again.`
                            : `${cluster.label} now shows ${initials}.`,
                      });
                    }}
                  />
                  <span className="hidden md:inline-flex">
                    <SwatchGrid value={cluster.colour} onPick={pick} small label={`Color for ${cluster.name}`} />
                  </span>
                  <span className="inline-flex md:hidden">
                    <ColourButton value={cluster.colour} onPick={pick} label={`Color for ${cluster.name}`} />
                  </span>
                  {/*
                    The color alone back to automatic: the contract's empty
                    key. Reset clears the name and the initials with it, and a
                    swatch pick pins one - neither is this.
                  */}
                  <button
                    type="button"
                    disabled={stored?.colour === undefined}
                    aria-label={`Automatic color for ${cluster.name}`}
                    title={
                      stored?.colour === undefined
                        ? 'Already the color derived from the context name'
                        : 'Back to the color derived from the context name'
                    }
                    onClick={() => {
                      write.mutate({
                        run: () => setRailNameplate(cluster.name, { colour: '' }),
                        said: `${cluster.label} has its automatic color again.`,
                      });
                    }}
                    className={btnSecondary}
                  >
                    Auto
                  </button>
                </div>
                <p className="mt-1 flex min-w-0 font-mono text-2xs text-muted">
                  <ClusterName name={cluster.name} />
                </p>
              </div>

              {/*
                Which hotbar this tile is on: a number, because `RailAssignWrite`
                is `{cluster, bar}`, 1-based. The upper bound is
                `preferences.rail.barCount`, and a number past it is refused by
                the server and drawn by the alert below rather than guarded at
                the keystroke - guessing a maximum here would be this client
                deciding a server constant.
              */}
              <div className="col-span-2 flex flex-wrap items-center justify-end gap-1 text-xs md:col-span-1">
                <span className="text-muted">Bar</span>
                <TextWrite
                  label={`Bar for ${cluster.name}`}
                  value={cluster.bar === undefined ? '' : String(cluster.bar)}
                  width="w-8"
                  onCommit={(typed) => {
                    const bar = Number.parseInt(typed, 10);
                    if (!Number.isInteger(bar) || bar < 1) {
                      // Said rather than swallowed: a field that silently
                      // discards what was typed is a control that did nothing.
                      announce(`${typed} is not a bar number. Bars are counted from 1.`);
                      return;
                    }
                    write.mutate({
                      run: () => setRailAssign(cluster.name, bar),
                      said: `${cluster.label} moved to bar ${String(bar)}.`,
                    });
                  }}
                />
                <button
                  type="button"
                  aria-label={`Move ${cluster.name} up`}
                  disabled={index === 0}
                  title={index === 0 ? 'Already first' : undefined}
                  onClick={() => swap(index, index - 1)}
                  className={`${btnSecondary} min-w-6`}
                >
                  &uarr;
                </button>
                <button
                  type="button"
                  aria-label={`Move ${cluster.name} down`}
                  disabled={index === clusters.length - 1}
                  title={index === clusters.length - 1 ? 'Already last' : undefined}
                  onClick={() => swap(index, index + 1)}
                  className={`${btnSecondary} min-w-6`}
                >
                  &darr;
                </button>
                {cluster.hidden === true ? (
                  <button
                    type="button"
                    onClick={() => {
                      write.mutate({
                        run: () => setRailReveal(cluster.name),
                        said: `${cluster.label} is back on the rail.`,
                      });
                    }}
                    className={btnSecondary}
                  >
                    Reveal
                  </button>
                ) : (
                  <button
                    type="button"
                    onClick={() => {
                      write.mutate({
                        run: () => setRailHide(cluster.name),
                        said: `${cluster.label} taken off the rail.`,
                      });
                    }}
                    className={btnSecondary}
                  >
                    Hide
                  </button>
                )}
                <button
                  type="button"
                  disabled={stored === undefined}
                  title={
                    stored === undefined
                      ? 'Nothing set: the name, initials and color are already derived from the context name'
                      : 'Back to the name, initials and color derived from the context name'
                  }
                  aria-label={`Reset the nameplate of ${cluster.name}`}
                  onClick={() => {
                    write.mutate({
                      run: () => setRailNameplate(cluster.name, { reset: true }),
                      said: `${cluster.name} is back to its derived name, initials and color.`,
                    });
                  }}
                  className={btnSecondary}
                >
                  Reset
                </button>
              </div>
            </li>
          );
        })}
      </ul>

      {write.isError ? (
        <p role="alert" className="mt-2 text-xs text-bad">
          {write.error instanceof Error ? write.error.message : 'The change was not saved.'}
        </p>
      ) : null}

      <p className="mt-3 text-xs text-muted">
        A nameplate is the operator&rsquo;s word for a cluster. It never replaces the context name in
        an address. Right-click a tile on the rail to edit one cluster.
      </p>
    </>
  );
}

/**
 * A field that writes when it is left, not on every keystroke.
 *
 * Every character would be a request, and the server's answer to the third one
 * can arrive after the answer to the fifth. Committing on blur and on Enter is
 * one write per decision.
 *
 * **It follows the stored value while nobody is typing in it.** The row is
 * keyed by the context name, so it never remounts: a draft copied from `value`
 * once would still hold the old name after Reset, and leaving it would post
 * that name back - undoing the Reset in silence. So a stored value that moves
 * replaces the draft whenever the draft is still the value it replaces - an
 * untouched field, focused or not, follows the store - and leaves alone a draft
 * somebody has typed something else into.
 */
function TextWrite({
  label,
  value,
  onCommit,
  placeholder,
  width,
  maxLength,
}: {
  label: string;
  value: string;
  onCommit: (value: string) => void;
  placeholder?: string;
  /** A step on the spacing scale: the field's width in its row. */
  width: string;
  maxLength?: number;
}): ReactNode {
  const [draft, setDraft] = useState(value);
  const [seen, setSeen] = useState(value);
  // Adjusted during render rather than in an effect: React's documented way to
  // follow a prop, and the field never paints the stale draft in between.
  if (value !== seen) {
    setSeen(value);
    if (draft === seen) setDraft(value);
  }

  function commit(): void {
    if (draft !== value) onCommit(draft);
  }

  return (
    <input
      aria-label={label}
      value={draft}
      placeholder={placeholder}
      maxLength={maxLength}
      onChange={(event) => setDraft(event.target.value)}
      onBlur={commit}
      onKeyDown={(event) => {
        if (event.key === 'Enter') {
          event.preventDefault();
          commit();
        }
      }}
      // `.tile-name-input`, app.css:3466.
      className={`${width} min-w-0 rounded-sm border border-line bg-panel-2 px-1 py-0.5 text-xs ${focusRingTight}`}
    />
  );
}
