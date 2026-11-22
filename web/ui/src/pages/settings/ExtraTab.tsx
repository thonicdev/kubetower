import { useMutation, useQuery } from '@tanstack/react-query';
import { useId, useState, type ReactNode } from 'react';

import {
  listExtensionSuggestions,
  queryKeys,
  setExtensionEnabled,
  type Cluster,
  type ExtensionInfo,
} from '../../api';
import { Icon } from '../../components/Icon';
import { Pill } from '../../components/Pill';
import { Tile, TileGrid } from '../../components/Tile';
import { focusRingTight } from '../../focus';
import { useShellIfPresent } from '../../shell/ShellContext';
import { ExtraPreview } from './ExtraPreview';
import { ExtraSettingsModal } from './ExtraSettingsModal';
import { AboutBadges, badgesOf, lockState, useExtraReread, WriteFailure } from './extraParts';

/**
 * Extra: one tile per extension the build carries (`extras.Catalogue()`,
 * nothing it does not carry), for one cluster, drawn with the console's shared
 * `Tile`: the name, one line of purpose, on or off, and the button.
 *
 * **Every extension is off until it is turned on, on every profile, and every
 * one is listed with a button**. The
 * server says, per row, whether the person looking may press it (`writable`)
 * and, where not, why (`locked`, `reason`) - the page decides nothing about who
 * may, and the writes refuse on their own:
 *
 * - **writable**: a switch, and the extension's settings as checkboxes. A
 *   switch and a checkbox write one thing each, one request each
 *   (`setExtensionEnabled`, `setExtensionSetting`), and the listing is read
 *   again after - the tile draws what the console keeps, never what was
 *   pressed.
 * - **`values`** (a cluster whose chart's values do not name it),
 *   **`pinned`** (`KT_EXTRAS` on the desktop or in a container) and
 *   **`unregistered`** (several replicas, or a cluster with no state volume):
 *   the switch, disabled, at its state, and the reason under it - every extra
 *   is listed with its button, and the reason says why it cannot move.
 * - **`use`** (a cluster where the person does not hold the `use` permission on it):
 *   no switch - the button is not offered - and the reason.
 *
 * A non-writable tile draws its settings at their value, with nothing to press.
 * The cluster the tiles are about is the picker's, beside the heading.
 *
 * **A tile never grows.** It draws the name, the state, the switch (which
 * works without opening anything), the server's reason where the switch cannot
 * move, a refused write, and an Edit button. Edit - or a click on the tile -
 * opens `ExtraSettingsModal`: the extra's switch, settings, columns and status
 * in a window with Save and Cancel. The grid keeps its shape.
 *
 * **About** opens the extra's preview window (`ExtraPreview.tsx`): what it does,
 * reads and needs - the `about` its UI half carries - and the button that turns
 * it on; for deciding, and for the settings step before turning one on.
 * The window is drawn from the row the listing holds now, so it follows every
 * re-read, and closes on its own when the row leaves the listing (another
 * cluster picked). A switch pressed in it that the console accepted closes it,
 * and its sentence (« Delta Center is now on for cluster prod. ») is said
 * above the tiles, in a live region that is always there so a screen reader
 * hears it.
 *
 */
export function ExtraTab({
  cluster,
  clusters,
  items,
}: {
  cluster: string;
  clusters: readonly Cluster[];
  items: readonly ExtensionInfo[];
}): ReactNode {
  // The extras this build carries, for what each says about itself. Absent
  // outside a Shell (a test that renders the page alone): the window then says
  // what the server's row says, and nothing more.
  const shipped = useShellIfPresent()?.extensions;
  // Which window is open, and on which cluster: one opened on another cluster
  // is not reopened by coming back to it.
  const [open, setOpen] = useState<{ cluster: string; key: string } | null>(null);
  const opened = open?.cluster === cluster ? items.find((item) => item.key === open.key) : undefined;
  // Which settings window is open, on which cluster; one per extra and cluster.
  const [editing, setEditing] = useState<{ cluster: string; key: string } | null>(null);
  const editedItem = editing?.cluster === cluster ? items.find((item) => item.key === editing.key) : undefined;
  // What the last switch pressed in a window did, on the cluster it was
  // pressed on: another cluster picked does not carry it along.
  const [said, setSaid] = useState<{ cluster: string; text: string } | null>(null);
  const absent = useAbsent(cluster, items);
  const suggested = useSuggested(cluster, items);

  return (
    <section className="flex flex-col gap-3" data-extras="">
      <header className="flex flex-wrap items-end gap-x-6 gap-y-2">
        <div className="min-w-0 flex-1">
          <h2 className="text-base font-medium">Extra pages</h2>
          {/* Always drawn, empty until a window's switch takes: a live region
              that appears with its sentence is not reliably read out. */}
          <p role="status" className="text-xs text-ok" data-extras-said="">
            {said?.cluster === cluster ? said.text : ''}
          </p>
        </div>
        <ClusterPicker cluster={cluster} clusters={clusters} />
      </header>

      <TileGrid label="Extensions" align="start">
        {absentLast(byLabel(items), absent).map((item) => (
          <ExtensionTile
            key={item.key}
            cluster={cluster}
            item={item}
            absent={absent.get(item.key)}
            tools={badgesOf(shipped?.find((extension) => extension.key === item.key))}
            suggested={suggested.get(item.key)}
            onEdit={() => {
              setSaid(null);
              setEditing({ cluster, key: item.key });
            }}
          />
        ))}
      </TileGrid>
      {editedItem === undefined ? null : (
        <ExtraSettingsModal
          key={`${cluster}/${editedItem.key}`}
          cluster={cluster}
          item={editedItem}
          absent={absent.get(editedItem.key)}
          own={shipped?.find((extension) => extension.key === editedItem.key)}
          onClose={() => setEditing(null)}
          onAbout={() => {
            setSaid(null);
            setEditing(null);
            setOpen({ cluster, key: editedItem.key });
          }}
        />
      )}
      {opened === undefined ? null : (
        <ExtraPreview
          // A new window per extra and per cluster: the settings step and a
          // failure belong to the one they were met on.
          key={`${cluster}/${opened.key}`}
          cluster={cluster}
          item={opened}
          about={shipped?.find((extension) => extension.key === opened.key)?.about}
          onClose={() => setOpen(null)}
          onDone={(text) => {
            setOpen(null);
            setSaid({ cluster, text });
          }}
        />
      )}
    </section>
  );
}

/** The extras alphabetically by label, case-insensitively: the order the menu
 *  draws them in, whatever order the listing arrived in. */
export function byLabel(items: readonly ExtensionInfo[]): ExtensionInfo[] {
  return [...items].sort((a, b) => a.label.toLowerCase().localeCompare(b.label.toLowerCase()));
}

/**
 * The extras whose component the cluster does not run, by key, with the short
 * state the tile draws ("cert-manager absent"). The detection is the Suggesters'
 * (`extension.Suggester`), the banner's own query: asked only while some extra
 * is off and could be pressed, and a read that failed is no answer - the tile
 * then says nothing, never "absent". An extra that is on is never absent.
 */
function useAbsent(cluster: string, items: readonly ExtensionInfo[]): ReadonlyMap<string, string> {
  const asks = items.some((item) => !item.enabled && item.writable);
  const suggestions = useQuery({
    queryKey: queryKeys.extensionSuggestions(cluster),
    queryFn: ({ signal }) => listExtensionSuggestions(cluster, signal),
    enabled: asks,
    retry: false,
    staleTime: 5 * 60_000,
  });
  const out = new Map<string, string>();
  if (!asks || suggestions.data?.state !== 'ok') return out;
  const off = new Set(items.filter((item) => !item.enabled).map((item) => item.key));
  for (const one of suggestions.data.items) {
    if (one.absent === true && off.has(one.key)) out.set(one.key, one.evidence);
  }
  return out;
}

/**
 * The sentence the suggestion banner says for each off extra the cluster gives
 * a reason for, by key. The banner's own query; a failed read gives none.
 */
function useSuggested(cluster: string, items: readonly ExtensionInfo[]): ReadonlyMap<string, string> {
  const asks = items.some((item) => !item.enabled && item.writable);
  const suggestions = useQuery({
    queryKey: queryKeys.extensionSuggestions(cluster),
    queryFn: ({ signal }) => listExtensionSuggestions(cluster, signal),
    enabled: asks,
    retry: false,
    staleTime: 5 * 60_000,
  });
  const out = new Map<string, string>();
  if (!asks || suggestions.data?.state !== 'ok') return out;
  const off = new Set(items.filter((item) => !item.enabled).map((item) => item.key));
  for (const one of suggestions.data.items) {
    if (one.absent !== true && off.has(one.key)) out.set(one.key, `${one.evidence} on this cluster.`);
  }
  return out;
}

/** The absent extras after the others, each group in the order it came. */
export function absentLast(items: readonly ExtensionInfo[], absent: ReadonlyMap<string, string>): ExtensionInfo[] {
  return [...items.filter((item) => !absent.has(item.key)), ...items.filter((item) => absent.has(item.key))];
}

/**
 * Which cluster the tiles are about, written beside them so the tile's blast
 * radius is always visible. With several clusters the choice is the rail's
 * icons (`Rail.tsx`), which are always drawn then, so there is no second control.
 */
function ClusterPicker({ cluster, clusters }: { cluster: string; clusters: readonly Cluster[] }): ReactNode {
  const known = clusters.find((one) => one.name === cluster);
  return (
    <p className="text-xs text-muted" data-extras-cluster={cluster}>
      Cluster <span className="font-mono text-fg">{known?.label ?? cluster}</span>
    </p>
  );
}

function ExtensionTile({
  cluster,
  item,
  absent,
  tools,
  suggested,
  onEdit,
}: {
  cluster: string;
  item: ExtensionInfo;
  /** Its short state where the cluster does not run its component. */
  absent: string | undefined;
  /** The tools it works with, from its `about.badges`. */
  tools: readonly string[] | undefined;
  /** The banner's sentence where the cluster gives a reason to turn it on. */
  suggested: string | undefined;
  onEdit: () => void;
}): ReactNode {
  const reread = useExtraReread(cluster);
  const toggle = useMutation({
    mutationFn: (enabled: boolean) => setExtensionEnabled(cluster, item.key, enabled),
    onSettled: reread,
  });
  // Strictly: a row from a server older than `writable` is not one the person
  // may change.
  const writable = item.writable;
  const { shownDisabled, reason } = lockState(item);
  const reasonId = useId();
  const busy = toggle.isPending;
  const failed = toggle.error;

  const action = writable || shownDisabled ? (
    <button
      type="button"
      role="switch"
      aria-checked={item.enabled}
      aria-label={`${item.label}, cluster ${cluster}`}
      {...(reason === undefined ? {} : { 'aria-describedby': reasonId })}
      disabled={busy || !writable}
      onClick={() => toggle.mutate(!item.enabled)}
      className={`flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs disabled:opacity-50 ${focusRingTight} ${
        item.enabled ? 'border-ok/40 bg-ok/10 text-ok' : 'border-line text-muted'
      }`}
    >
      <span aria-hidden="true" className={`inline-block h-2 w-2 rounded-full ${item.enabled ? 'bg-ok' : 'bg-line'}`} />
      {item.enabled ? 'On' : 'Off'}
    </button>
  ) : (
    <span className={`text-xs font-medium ${item.enabled ? 'text-ok' : 'text-muted'}`} data-extra-state="">
      {item.enabled ? 'On' : 'Off'}
    </span>
  );

  const detail = item.summary === undefined || item.summary === '' ? undefined : <span>{item.summary}</span>;

  return (
    <Tile
      data-extra={item.key}
      data-on={item.enabled ? 'true' : 'false'}
      title={item.label}
      onOpen={onEdit}
      tone={item.enabled ? 'ok' : 'off'}
      dim={absent !== undefined}
      {...(absent === undefined ? {} : { 'data-extra-absent': absent })}
      badges={
        item.pinned || absent !== undefined ? (
          <>
            {item.pinned ? (
              <span data-extra-pinned="">
                <Pill neutral="muted">pinned</Pill>
              </span>
            ) : null}
            {absent === undefined ? null : (
              <span data-extra-absent-state="">
                <Pill neutral="muted">{absent}</Pill>
              </span>
            )}
          </>
        ) : undefined
      }
      detail={detail}
      action={action}
    >
      {/* The tile never grows. Under its summary, in this order: the
          suggestion, the server's reason, a refused write, the tool badges,
          then the Edit button. */}
      {suggested === undefined ? null : (
        <p className="text-xs text-muted" data-extra-suggested="">
          {suggested}
        </p>
      )}
      {reason === undefined ? null : (
        <p id={reasonId} className="text-xs text-muted" data-extra-reason={item.locked ?? ''}>
          {reason}
        </p>
      )}
      {failed === null ? null : <WriteFailure error={failed} />}
      <AboutBadges names={tools} />
      <button
        type="button"
        onClick={onEdit}
        aria-label={`Edit ${item.label}`}
        data-extra-edit=""
        className={`flex items-center gap-1.5 self-start rounded-md border border-line px-2 py-0.5 text-xs text-fg ${focusRingTight}`}
      >
        <span aria-hidden="true" className="size-3.5">
          <Icon name="settings" />
        </span>
        Edit
      </button>
    </Tile>
  );
}
