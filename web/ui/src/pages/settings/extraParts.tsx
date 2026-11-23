import { useQueryClient } from '@tanstack/react-query';
import type { ReactNode } from 'react';

import { ApiError, queryKeys, type ExtensionInfo } from '../../api';
import { Icon, type IconName } from '../../components/Icon';
import { Pill } from '../../components/Pill';
import { focusRingTight } from '../../focus';

/**
 * What the Extra tab's tile and its preview window share: the reading of a
 * row's settings, the re-read after a write, and how a write that failed is
 * said. One copy, so the window and the tile cannot disagree about either.
 */

/**
 * An extension's settings, or none: the contract says the member is always
 * sent and that its absence, from a server older than it, reads as none.
 */
export function settingsOf(item: ExtensionInfo): ExtensionInfo['settings'] {
  return (item.settings as ExtensionInfo['settings'] | undefined) ?? [];
}

/** A line of text, as opposed to a checkbox. A server older than `kind` sent
 *  checkboxes only. */
export function isText(setting: ExtensionInfo['settings'][number]): boolean {
  const kind = setting.kind as ExtensionInfo['settings'][number]['kind'] | undefined;
  return kind === 'text' || kind === 'choice';
}

/**
 * Whether an extension's switch is drawn disabled, and the sentence under it.
 * Locked for a reason other than the person's own rights: the button is
 * drawn, disabled. Without `use` it is not offered at all. A row the person
 * may change carries no reason, nor does one whose reason is empty.
 */
export function lockState(item: ExtensionInfo): { shownDisabled: boolean; reason: string | undefined } {
  const writable = item.writable;
  return {
    shownDisabled: !writable && item.locked !== undefined && item.locked !== 'use',
    reason: writable || item.reason === undefined || item.reason === '' ? undefined : item.reason,
  };
}

/** What a write that failed says: the server's own sentence. */
export function failure(error: unknown): string {
  return error instanceof Error ? error.message : 'No reason was given.';
}

/**
 * Read again what a write on an extension changes. The menu's Extra group, the
 * object tabs and the list columns all read what is on from the cluster's
 * menu, and a gated column from the listing: both are read again after any
 * write here, so what is drawn after is what the console kept.
 */
export function useExtraReread(cluster: string): () => Promise<unknown> {
  const cache = useQueryClient();
  return () =>
    Promise.all([
      cache.invalidateQueries({ queryKey: queryKeys.extensions(cluster) }),
      cache.invalidateQueries({ queryKey: queryKeys.nav(cluster) }),
    ]);
}

/**
 * A write that failed, in the server's words. A write that took effect and
 * could not be kept (`not_saved`) is not a refusal: what is drawn already
 * shows it applied, and the sentence says it will not survive a restart.
 */
export function WriteFailure({ error }: { error: unknown }): ReactNode {
  return (
    <p
      role="alert"
      className={`text-xs ${error instanceof ApiError && error.isNotSaved ? 'text-warn' : 'text-bad'}`}
      data-extra-failure=""
    >
      {failure(error)}
    </p>
  );
}

/** A setting the deployment pinned (KT_EXTRA_SETTINGS), where the others can
 *  be changed: its value, a "pinned" badge, and nothing to press. */
export function PinnedSetting({ setting }: { setting: ExtensionInfo['settings'][number] }): ReactNode {
  return (
    <div className="flex items-center gap-2 text-xs" data-extra-setting={setting.key} data-extra-setting-pinned="">
      <span className="min-w-0 flex-1 text-fg" {...(setting.summary === undefined ? {} : { title: setting.summary })}>
        {setting.label}
      </span>
      <Pill neutral="muted" title="Set by the deployment (KT_EXTRA_SETTINGS)">
        pinned
      </Pill>
      <span className={setting.value ? 'text-ok' : 'text-muted'}>{setting.value ? 'On' : 'Off'}</span>
    </div>
  );
}

/** What an extra says about the tools it works with, as small neutral pills
 *  that wrap. Nothing is drawn where it says none. */
export function AboutBadges({ names }: { names: readonly string[] | undefined }): ReactNode {
  if (names === undefined || names.length === 0) return null;
  return (
    <span className="flex flex-wrap items-center gap-1" data-extra-about-badges="">
      {names.map((name) => (
        <Pill key={name} neutral="muted">
          {name}
        </Pill>
      ))}
    </span>
  );
}

/** An extra's `about.badges`, read defensively: the field is optional in the
 *  contract, and a build that predates it says none. */
export function badgesOf(extension: { about?: object | undefined } | undefined): readonly string[] | undefined {
  return (extension?.about as { badges?: readonly string[] } | undefined)?.badges;
}

/**
 * An extra's glyph in a rounded square, tinted while it is on. `name` is the
 * extension's own `icon`; absent, the generic `extra` glyph.
 */
export function ExtraGlyph({ name, on, size = 'card' }: { name: IconName | undefined; on: boolean; size?: 'card' | 'title' }): ReactNode {
  const box = size === 'card' ? 'size-10 rounded-lg [--icon:calc(var(--spacing)*5)]' : 'size-8 rounded-md [--icon:calc(var(--spacing)*4)]';
  return (
    <span
      aria-hidden="true"
      data-extra-icon={name ?? 'extra'}
      className={`grid shrink-0 place-items-center border ${box} ${
        on ? 'border-ok/40 bg-ok/10 text-ok' : 'border-line bg-white/5 text-muted'
      }`}
    >
      <Icon name={name ?? 'extra'} />
    </span>
  );
}

/**
 * The On/Off switch: a track and a knob. `role="switch"`, so `aria-checked`
 * is what a screen reader says; the visible word is in the tooltip.
 */
export function ExtraSwitch({
  on,
  label,
  disabled,
  onToggle,
  describedBy,
  focusFirst = false,
}: {
  on: boolean;
  label: string;
  disabled: boolean;
  onToggle: () => void;
  describedBy?: string | undefined;
  focusFirst?: boolean;
}): ReactNode {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={on}
      aria-label={label}
      title={on ? 'On' : 'Off'}
      {...(describedBy === undefined ? {} : { 'aria-describedby': describedBy })}
      {...(focusFirst ? { 'data-dialog-focus': '' } : {})}
      disabled={disabled}
      onClick={onToggle}
      data-extra-switch={on ? 'on' : 'off'}
      className={`relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${focusRingTight} ${
        on ? 'border-ok bg-ok/60' : 'border-line bg-panel-2'
      }`}
    >
      <span
        aria-hidden="true"
        className={`inline-block size-3.5 rounded-full bg-fg shadow transition-transform ${on ? 'translate-x-4' : 'translate-x-0.5'}`}
      />
    </button>
  );
}

/** On or Off for the picked cluster, as a badge. */
export function StateBadge({ on, cluster }: { on: boolean; cluster: string }): ReactNode {
  return (
    <span data-extra-state={on ? 'on' : 'off'}>
      {on ? (
        <Pill tone="ok" title={`On for cluster ${cluster}`}>
          On
        </Pill>
      ) : (
        <Pill neutral="muted" title={`Off for cluster ${cluster}`}>
          Off
        </Pill>
      )}
    </span>
  );
}
