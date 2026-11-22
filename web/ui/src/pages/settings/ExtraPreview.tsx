import { useMutation } from '@tanstack/react-query';
import { useEffect, useId, useRef, useState, type ReactNode } from 'react';

import { setExtensionEnabled, setExtensionSetting, type ExtensionInfo } from '../../api';
import { btnPrimary, btnSecondary } from '../../components/buttons';
import { Dialog } from '../../components/Dialog';
import { Icon } from '../../components/Icon';
import type { ExtensionAbout } from '../../extension';
import { focusRingTight } from '../../focus';
import { KeyLayer } from '../../keyboard/layers';
import { glyphForGroup } from '../../shell/kindGlyph';
import { isText, lockState, PinnedSetting, settingsOf, useExtraReread, WriteFailure } from './extraParts';

/**
 * The preview window of one extra, opened from its tile in Settings → Extra:
 * what it does, what it reads and what that needs, what must already run in
 * the cluster, a picture of its page - and, bottom right, the button that
 * turns it on or off on the cluster the page is about.
 *
 * ## What it draws is the listing's, never what was pressed
 *
 * `item` is the row the page holds from `GET /api/c/{cluster}/extensions`, and
 * every write is followed by the same two re-reads the tile makes.
 *
 * ## A switch that took closes the window
 *
 * Once a « Turn on » or « Turn off » - or the settings step's « Save » - has
 * been accepted **and** the listing read again, the window closes and hands
 * `onDone` one sentence saying what is now true (`ExtraTab` draws it in a live
 * region above the tiles), so the person sees the tile already changed and is
 * told so. The button stays disabled while the write is in flight. A write
 * that failed leaves the window open, on the step it was on, with the server's
 * sentence in it - a `not_saved` answer included, since it is the one that
 * says the change will not survive a restart.
 *
 * ## Who may press it: the tile's rule
 *
 * Writable: the button. Not writable for a reason other than the person's own
 * rights (`values`, `pinned`, `unregistered`): the button, disabled, with the
 * server's reason under it. Without `use` on the cluster: no button - the
 * reason only. The page decides nothing about who may; the server's row does.
 *
 * ## Turning on an extra that has settings: a second step
 *
 * Turn on, on an extra that is off and declares settings, moves this window to
 * a « Settings » step: each setting at its current value, a pinned one shown
 * pinned with nothing to press. **Save** writes each setting that changed,
 * then turns the extra on - in that order, so the extra's first page is drawn
 * with the settings chosen for it. The server accepts a setting on an extra
 * that is off (it is how the page is drawn, not whether it is there), which is
 * what makes that order possible. **Cancel** writes nothing, and the extra
 * stays off. An extra with no settings is turned on at once.
 *
 * Writes stop at the first that fails: what was written before it stays
 * written, the listing is read again so the window shows it, and the failure is
 * said in the window, which stays on the step it was on.
 */
export function ExtraPreview({
  cluster,
  item,
  about,
  onClose,
  onDone,
}: {
  cluster: string;
  item: ExtensionInfo;
  about: ExtensionAbout | undefined;
  onClose: () => void;
  /** The switch was accepted and the listing read again: the window closes
   *  through this, with the sentence that says what changed. */
  onDone: (said: string) => void;
}): ReactNode {
  const reread = useExtraReread(cluster);
  // Checkboxes only: a line of text is edited on the tile, behind its Edit.
  const settings = settingsOf(item).filter((one) => !isText(one));
  const [step, setStep] = useState<'about' | 'settings'>('about');
  // What the settings step will write, by key: only the settings the person
  // changed. Absent, a setting is drawn at the value the listing holds.
  const [draft, setDraft] = useState<Readonly<Record<string, boolean>>>({});

  const toggle = useMutation({
    mutationFn: (enabled: boolean) => setExtensionEnabled(cluster, item.key, enabled),
    onSettled: reread,
  });
  const save = useMutation({
    mutationFn: async (changes: readonly (readonly [string, boolean])[]) => {
      // One at a time and in order: the settings, then the switch.
      for (const [setting, value] of changes) {
        await setExtensionSetting(cluster, item.key, setting, value);
      }
      await setExtensionEnabled(cluster, item.key, true);
    },
    onSettled: reread,
  });
  // Called per write, so it runs after the hook's `onSettled` re-read has
  // finished: the tile behind the window already draws what the console kept.
  const done = (enabled: boolean) => (): void => {
    onDone(`${item.label} is now ${enabled ? 'on' : 'off'} for cluster ${cluster}.`);
  };

  const writable = item.writable;
  const { shownDisabled, reason } = lockState(item);
  const reasonId = useId();
  const busy = toggle.isPending || save.isPending;
  const failed = toggle.error ?? save.error;

  const press = (): void => {
    save.reset();
    toggle.reset();
    if (!item.enabled && settings.length > 0) {
      setDraft({});
      setStep('settings');
      return;
    }
    const enabled = !item.enabled;
    toggle.mutate(enabled, { onSuccess: done(enabled) });
  };
  const cancel = (): void => {
    save.reset();
    setDraft({});
    setStep('about');
  };
  const commit = (): void => {
    toggle.reset();
    const changes = settings
      .filter((setting) => setting.pinned !== true)
      .flatMap((setting) => {
        const wanted = draft[setting.key];
        return wanted === undefined || wanted === setting.value ? [] : [[setting.key, wanted] as const];
      });
    save.mutate(changes, { onSuccess: done(true) });
  };

  // The keyboard follows the step: into the first setting when the settings
  // step opens, back onto the button when it closes. `Dialog` places it on
  // open only, and the control that had it is gone once the step changes.
  const stepBox = useRef<HTMLDivElement>(null);
  const opened = useRef(false);
  useEffect(() => {
    if (!opened.current) {
      opened.current = true;
      return;
    }
    stepBox.current
      ?.querySelector<HTMLElement>('input:not([disabled]), button[data-extra-primary]:not([disabled])')
      ?.focus({ preventScroll: true });
  }, [step]);

  const description =
    about?.description ?? (item.summary === undefined || item.summary === '' ? undefined : item.summary);
  const requires = about?.requires ?? (item.needs === undefined || item.needs === '' ? undefined : [item.needs]);

  return (
    <Dialog title={item.label} onClose={onClose} layer={KeyLayer.Dialog} size="wide">
      <div className="flex flex-col gap-4" data-extra-preview={item.key} data-on={item.enabled ? 'true' : 'false'}>
        <div className="flex items-center gap-2 text-xs text-muted">
          <span className="text-fg" data-extra-preview-icon="">
            <Icon name={glyphForGroup('extra')} />
          </span>
          <span className={item.enabled ? 'text-ok' : 'text-muted'} data-extra-preview-state="">
            {item.enabled ? 'On' : 'Off'}
          </span>
          <span>
            on cluster <span className="font-mono text-fg">{cluster}</span>
          </span>
        </div>

        <div ref={stepBox} className="flex flex-col gap-4">
          {step === 'about' ? (
            <>
              {description === undefined ? null : (
                <p className="text-sm text-fg" data-extra-preview-description="">
                  {description}
                </p>
              )}
              {about === undefined ? null : (
                <div className="grid gap-4 sm:grid-cols-2">
                  <Facts title="What it reads" items={about.reads} data="reads" />
                  <Facts title="Permissions" items={about.permissions} data="permissions" mono />
                </div>
              )}
              {requires === undefined ? null : (
                <Facts
                  title="Requires"
                  items={requires.length === 0 ? ['Nothing beyond the cluster itself.'] : requires}
                  data="requires"
                />
              )}
              {about?.screenshot === undefined ? null : <Screenshot src={about.screenshot} label={item.label} />}
              {(about?.gallery ?? []).map((picture) => (
                <figure key={picture.src} className="flex flex-col gap-1" data-extra-preview-picture="">
                  <Screenshot src={picture.src} label={item.label} alt={picture.caption} />
                  <figcaption className="text-xs text-muted">{picture.caption}</figcaption>
                </figure>
              ))}
            </>
          ) : (
            <SettingsStep item={item} draft={draft} busy={busy} onChange={(key, value) => setDraft({ ...draft, [key]: value })} />
          )}

          {failed === null ? null : <WriteFailure error={failed} />}

          <div className="flex flex-col items-end gap-1">
            {step === 'about' ? (
              writable || shownDisabled ? (
                <button
                  type="button"
                  className={btnPrimary}
                  disabled={busy || !writable}
                  onClick={press}
                  data-extra-primary=""
                  {...(reason === undefined ? {} : { 'aria-describedby': reasonId })}
                >
                  {item.enabled ? 'Turn off' : 'Turn on'}
                </button>
              ) : null
            ) : (
              <div className="flex gap-2">
                <button type="button" className={btnPrimary} disabled={busy} onClick={commit} data-extra-primary="">
                  Save
                </button>
                <button type="button" className={btnSecondary} disabled={busy} onClick={cancel}>
                  Cancel
                </button>
              </div>
            )}
            {reason === undefined ? null : (
              // The server's own sentence: why this person cannot press it here.
              <p id={reasonId} className="text-right text-xs text-muted" data-extra-reason={item.locked ?? ''}>
                {reason}
              </p>
            )}
          </div>
        </div>
      </div>
    </Dialog>
  );
}

/** One heading and its list: what it reads, the permissions, what it needs. */
function Facts({
  title,
  items,
  data,
  mono = false,
}: {
  title: string;
  items: readonly string[];
  data: string;
  mono?: boolean;
}): ReactNode {
  return (
    <section className="flex flex-col gap-1" data-extra-preview-facts={data}>
      <h3 className="text-xs font-medium tracking-wide text-muted uppercase">{title}</h3>
      <ul className={`flex list-disc flex-col gap-0.5 pl-4 text-xs text-fg ${mono ? 'font-mono' : ''}`}>
        {items.map((line) => (
          <li key={line}>{line}</li>
        ))}
      </ul>
    </section>
  );
}

/** The extra's page as a picture. An image that cannot be loaded draws
 *  nothing rather than the browser's broken-image box. */
function Screenshot({ src, label, alt }: { src: string; label: string; alt?: string }): ReactNode {
  const [broken, setBroken] = useState(false);
  if (broken) return null;
  return (
    <img
      src={src}
      alt={alt ?? `The ${label} page`}
      className="w-full rounded-md border border-line"
      data-extra-preview-screenshot=""
      onError={() => setBroken(true)}
    />
  );
}

/** The « Settings » step: each setting at its value as the step holds it, a
 *  pinned one with nothing to press. */
function SettingsStep({
  item,
  draft,
  busy,
  onChange,
}: {
  item: ExtensionInfo;
  draft: Readonly<Record<string, boolean>>;
  busy: boolean;
  onChange: (key: string, value: boolean) => void;
}): ReactNode {
  return (
    <fieldset className="flex flex-col gap-2" data-extra-preview-settings="">
      <legend className="mb-2 text-sm font-medium text-fg">Settings</legend>
      <p className="text-xs text-muted">Choose how {item.label} is drawn, then save to turn it on.</p>
      {settingsOf(item).filter((one) => !isText(one)).map((setting) =>
        setting.pinned === true ? (
          <PinnedSetting key={setting.key} setting={setting} />
        ) : (
          <label
            key={setting.key}
            className="flex items-center gap-2 text-xs text-fg"
            data-extra-setting={setting.key}
            {...(setting.summary === undefined ? {} : { title: setting.summary })}
          >
            <input
              type="checkbox"
              className={focusRingTight}
              checked={draft[setting.key] ?? setting.value}
              disabled={busy}
              onChange={(event) => onChange(setting.key, event.target.checked)}
            />
            <span className="min-w-0">{setting.label}</span>
          </label>
        ),
      )}
    </fieldset>
  );
}
