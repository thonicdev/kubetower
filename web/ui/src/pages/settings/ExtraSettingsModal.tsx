import { Suspense, useId, useState, type KeyboardEvent, type ReactNode } from 'react';
import { Link } from 'react-router';

import {
  setExtensionEnabled,
  setExtensionSetting,
  setExtensionText,
  type ExtensionInfo,
} from '../../api';
import { Dialog } from '../../components/Dialog';
import { Pill } from '../../components/Pill';
import type { Extension } from '../../extension';
import { focusRing, focusRingTight } from '../../focus';
import { KeyLayer } from '../../keyboard/layers';
import { extraColumnShown, setExtraColumnShown } from '../resources/extraColumnMemory';
import { AboutBadges, badgesOf, failure, isText, lockState, PinnedSetting, settingsOf, useExtraReread } from './extraParts';

type SettingInfo = ExtensionInfo['settings'][number];

/** The controls Tab stops on inside the window. */
const TABBABLE = 'button:not([disabled]), input:not([disabled]), select:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])';

/** Tab from the last control goes to the first and Shift+Tab from the first to
 *  the last, so the keyboard stays in the window while it is open. Escape and
 *  the veil still close it, whatever a save did. */
function keepTabInside(event: KeyboardEvent<HTMLElement>): void {
  if (event.key !== 'Tab') return;
  const box = event.currentTarget.closest<HTMLElement>('[role="dialog"]');
  if (box === null) return;
  const stops = [...box.querySelectorAll<HTMLElement>(TABBABLE)].filter((each) => each.tabIndex >= 0);
  const first = stops[0];
  const last = stops[stops.length - 1];
  if (first === undefined || last === undefined) return;
  const here = document.activeElement;
  if (event.shiftKey && (here === first || !box.contains(here))) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && (here === last || !box.contains(here))) {
    event.preventDefault();
    first.focus();
  }
}

/**
 * One extra's settings, in a window over the Extra tab: its switch, its
 * settings and columns, what the cluster says about its component, and a link
 * to its page. Nothing is written until Save; Cancel, Escape and a click on
 * the veil leave everything as it was. A refused write keeps the window open
 * with the server's sentence.
 *
 * The window draws a draft of the row it opened on and writes only what
 * differs from the row as it is now, in order (switch, checkboxes, lines of
 * text), one request each, so a second Save after a refusal sends only what is
 * left. The listing is read again after, whatever happened.
 */
export function ExtraSettingsModal({
  cluster,
  item,
  absent,
  own,
  onClose,
  onAbout,
}: {
  cluster: string;
  item: ExtensionInfo;
  /** Its short state where the cluster does not run its component. */
  absent: string | undefined;
  /** What the build carries for it: its page, its columns, its own status. */
  own: Extension | undefined;
  onClose: () => void;
  onAbout: () => void;
}): ReactNode {
  const reread = useExtraReread(cluster);
  const reasonId = useId();
  const writable = item.writable;
  const { shownDisabled, reason } = lockState(item);
  const all = settingsOf(item);
  const bools = all.filter((one) => !isText(one));
  const texts = all.filter(isText);
  const editableTexts = texts.filter((one) => one.pinned !== true);
  const columns = (own?.columns ?? []).filter((column) => column.setting === undefined);

  const [enabled, setEnabled] = useState(item.enabled);
  const [checks, setChecks] = useState<Record<string, boolean>>(() =>
    Object.fromEntries(bools.map((one) => [one.key, one.value])),
  );
  const [lines, setLines] = useState<Record<string, string>>(() =>
    Object.fromEntries(editableTexts.map((one) => [one.key, one.text ?? ''])),
  );
  const [shown, setShown] = useState<Record<string, boolean>>(() =>
    Object.fromEntries(columns.map((column) => [column.key, extraColumnShown(item.key, column.key)])),
  );
  const [saving, setSaving] = useState(false);
  const [refusal, setRefusal] = useState<string | null>(null);
  const Status = item.enabled ? own?.SettingsStatus : undefined;
  const [revision, setRevision] = useState(0);

  const save = async (): Promise<void> => {
    setSaving(true);
    setRefusal(null);
    try {
      if (writable) {
        for (const one of bools) {
          const wanted = checks[one.key] ?? one.value;
          if (one.pinned !== true && wanted !== one.value) await setExtensionSetting(cluster, item.key, one.key, wanted);
        }
        for (const one of editableTexts) {
          const wanted = (lines[one.key] ?? '').trim();
          if (wanted !== (one.text ?? '')) await setExtensionText(cluster, item.key, one.key, wanted);
        }
        // The switch last, as the preview does: what it turns on is already set.
        if (enabled !== item.enabled) await setExtensionEnabled(cluster, item.key, enabled);
      }
      for (const column of columns) {
        const wanted = shown[column.key] ?? false;
        if (wanted !== extraColumnShown(item.key, column.key)) setExtraColumnShown(item.key, column.key, wanted);
      }
      await reread();
      onClose();
    } catch (error) {
      setRefusal(failure(error));
      await reread();
      setRevision((n) => n + 1);
    } finally {
      setSaving(false);
    }
  };

  const hint = (setting: SettingInfo): { title?: string } => (setting.summary === undefined ? {} : { title: setting.summary });
  const field = `rounded-md border border-line bg-panel-2 px-2 py-1 text-xs text-fg disabled:opacity-50 ${focusRing}`;

  return (
    <Dialog title={item.label} onClose={onClose} layer={KeyLayer.Dialog} size="sheet" showClose={false}>
      <form
        className="flex flex-col gap-3"
        data-extra-modal={item.key}
        aria-label={`${item.label} settings`}
        onKeyDown={keepTabInside}
        onSubmit={(event) => {
          event.preventDefault();
          if (!saving) void save();
        }}
      >
        <div className="flex flex-wrap items-center gap-2">
          {writable || shownDisabled ? (
            <button
              type="button"
              role="switch"
              aria-checked={enabled}
              aria-label={`${item.label}, cluster ${cluster}`}
              {...(reason === undefined ? {} : { 'aria-describedby': reasonId })}
              disabled={saving || !writable}
              onClick={() => setEnabled((was) => !was)}
              data-dialog-focus=""
              className={`flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs disabled:opacity-50 ${focusRingTight} ${
                enabled ? 'border-ok/40 bg-ok/10 text-ok' : 'border-line text-muted'
              }`}
            >
              <span aria-hidden="true" className={`inline-block h-2 w-2 rounded-full ${enabled ? 'bg-ok' : 'bg-line'}`} />
              {enabled ? 'On' : 'Off'}
            </button>
          ) : (
            <span className={`text-xs font-medium ${item.enabled ? 'text-ok' : 'text-muted'}`} data-extra-state="">
              {item.enabled ? 'On' : 'Off'}
            </span>
          )}
          {item.pinned ? <Pill neutral="muted">pinned</Pill> : null}
          {absent === undefined ? null : (
            <span data-extra-absent-state="">
              <Pill neutral="muted">{absent}</Pill>
            </span>
          )}
          <button
            type="button"
            onClick={onAbout}
            aria-label={`About ${item.label}`}
            data-extra-about=""
            className={`ml-auto rounded-md border border-line px-2 py-0.5 text-xs text-fg ${focusRingTight}`}
          >
            About
          </button>
        </div>
        <AboutBadges names={badgesOf(own)} />
        {reason === undefined ? null : (
          <p id={reasonId} className="text-xs text-muted" data-extra-reason={item.locked ?? ''}>
            {reason}
          </p>
        )}

        {texts.length === 0 ? null : (
          <div className="flex flex-col gap-2" data-extra-texts="">
            {texts.map((one) =>
              one.pinned === true ? (
                <div key={one.key} className="flex items-baseline gap-2 text-xs" data-extra-setting={one.key} {...hint(one)}>
                  <span className="text-muted">{one.label}</span>
                  <Pill neutral="muted" title={`Set by the deployment: ${one.env ?? ''}`}>
                    {one.env ?? 'environment'}
                  </Pill>
                </div>
              ) : (
                <label key={one.key} className="flex flex-col gap-0.5 text-xs" data-extra-setting={one.key} {...hint(one)}>
                  <span className="text-fg">{one.label}</span>
                  {one.options === undefined ? (
                    <input
                      type="text"
                      className={`${field} font-mono`}
                      value={lines[one.key] ?? ''}
                      placeholder={one.placeholder ?? 'not set'}
                      disabled={saving || !writable}
                      spellCheck={false}
                      autoComplete="off"
                      onChange={(event) => setLines({ ...lines, [one.key]: event.target.value })}
                    />
                  ) : (
                    <select
                      className={field}
                      value={lines[one.key] || one.options[0]?.value || ''}
                      disabled={saving || !writable}
                      onChange={(event) => setLines({ ...lines, [one.key]: event.target.value })}
                    >
                      {one.options.map((option) => (
                        <option key={option.value} value={option.value}>
                          {option.label}
                        </option>
                      ))}
                    </select>
                  )}
                </label>
              ),
            )}
          </div>
        )}

        {bools.length === 0 ? null : (
          <fieldset className="flex flex-col gap-1.5">
            <legend className="sr-only">{item.label} settings</legend>
            {bools.map((one) =>
              one.pinned === true ? (
                <PinnedSetting key={one.key} setting={one} />
              ) : (
                <label key={one.key} className="flex items-center gap-2 text-xs text-fg" data-extra-setting={one.key} {...hint(one)}>
                  <input
                    type="checkbox"
                    className={focusRingTight}
                    checked={checks[one.key] ?? one.value}
                    disabled={saving || !writable}
                    onChange={(event) => setChecks({ ...checks, [one.key]: event.target.checked })}
                  />
                  <span className="min-w-0">{one.label}</span>
                </label>
              ),
            )}
          </fieldset>
        )}

        {columns.length === 0 ? null : (
          <fieldset className="flex flex-col gap-1.5" data-extra-columns="">
            <legend className="sr-only">{item.label} columns</legend>
            {columns.map((column) => (
              <label key={column.key} className="flex items-center gap-2 text-xs text-fg" data-extra-column={column.key}>
                <input
                  type="checkbox"
                  className={focusRingTight}
                  checked={shown[column.key] ?? false}
                  disabled={saving}
                  onChange={(event) => setShown({ ...shown, [column.key]: event.target.checked })}
                />
                <span className="min-w-0">Show the column</span>
                <span className="min-w-0 truncate text-muted">{column.label}</span>
              </label>
            ))}
          </fieldset>
        )}

        {Status === undefined ? null : (
          <Suspense fallback={<p className="text-xs text-muted">Checking…</p>}>
            <Status cluster={cluster} revision={revision} />
          </Suspense>
        )}

        {refusal === null ? null : (
          <p role="alert" className="text-xs text-bad" data-extra-failure="">
            {refusal}
          </p>
        )}

        <div className="flex items-center gap-2">
          {item.enabled && own !== undefined && own.nav.menu !== false ? (
            <Link
              to={`/c/${encodeURIComponent(cluster)}/${own.nav.path}`}
              onClick={onClose}
              data-extra-page=""
              className={`rounded-md text-xs text-accent underline-offset-2 hover:underline ${focusRingTight}`}
            >
              Open {own.nav.label}
            </Link>
          ) : null}
          <span className="flex-1" />
          <button
            type="button"
            onClick={onClose}
            className={`rounded-md border border-line px-3 py-1 text-xs text-muted ${focusRingTight}`}
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={saving || (!writable && columns.length === 0)}
            className={`rounded-md border border-accent/50 bg-accent/10 px-3 py-1 text-xs text-accent disabled:opacity-50 ${focusRingTight}`}
          >
            {saving ? 'Saving…' : 'Save'}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
