/**
 * Design proposals for 0.1.3 (#121, #122): pictures for Thomas to choose
 * from, drawn with the console's own parts. Not shipped: nothing under `ui/`
 * imports this file.
 *
 *   proposal-news-a|b|c           What's new, one sentence per change
 *   proposal-updates-a|b|c-<step> Settings › Infos with the automatic setting
 *   proposal-updates-a|b|c-sheet  every step of one variant, one row each
 */
import { useEffect, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';

import { btnPrimary, btnSecondary } from '../ui/src/components/buttons';
import { Dialog } from '../ui/src/components/Dialog';
import { Icon, type IconName } from '../ui/src/components/Icon';
import { Pill } from '../ui/src/components/Pill';
import { KeyLayer } from '../ui/src/keyboard/layers';
import { ExtraSwitch } from '../ui/src/pages/settings/extraParts';
import storagePicture from '../app/extras/storage/assets/preview.png';

/* -------------------------------------------------------------------------- */
/* What's new                                                                 */
/* -------------------------------------------------------------------------- */

type Kind = 'extra' | 'added' | 'fixed';
interface Entry {
  kind: Kind;
  /** The CHANGELOG bullet's bold lead: one short sentence. */
  text: string;
  picture?: string;
  icon?: IconName;
}

/** 0.2.1's bullets, as their bold leads would read under the proposed format. */
const ENTRIES: readonly Entry[] = [
  { kind: 'extra', text: 'Storage Inspector', picture: storagePicture, icon: 'disk' },
  { kind: 'added', text: 'Welcome and What’s new open in a dialog' },
  { kind: 'added', text: 'Mark an extra as a favorite' },
  { kind: 'added', text: 'An image’s version is always visible' },
  { kind: 'fixed', text: 'Installing from Windows finishes' },
  { kind: 'fixed', text: 'Updates on Linux and macOS say what happened' },
];

const BADGE: Readonly<Record<Kind, { label: string; tone: string; icon: IconName; box: string }>> = {
  extra: { label: 'New extra', tone: 'info', icon: 'extra', box: 'border-accent/40 bg-accent/10 text-accent' },
  added: { label: 'New', tone: 'info', icon: 'plus', box: 'border-accent/40 bg-accent/10 text-accent' },
  fixed: { label: 'Fixed', tone: 'ok', icon: 'check', box: 'border-ok/40 bg-ok/10 text-ok' },
};

function Glyph({ kind, icon }: { kind: Kind; icon?: IconName | undefined }): ReactNode {
  return (
    <span aria-hidden="true" className={`grid size-7 shrink-0 place-items-center rounded-md border ${BADGE[kind].box}`}>
      <Icon name={icon ?? BADGE[kind].icon} />
    </span>
  );
}

/** The top left of the extra's preview, enlarged: its title and its counters. */
function Thumb({ src, className }: { src: string; className: string }): ReactNode {
  return (
    <span
      aria-hidden="true"
      className={`block rounded-md border border-line bg-no-repeat ${className}`}
      style={{ backgroundImage: `url(${src})`, backgroundSize: '280% auto', backgroundPosition: '0 0' }}
    />
  );
}

/** A: today's rows, the badge first, the sentence after it, the picture at the end. */
function NewsA(): ReactNode {
  return (
    <ul className="flex flex-col gap-1.5">
      {ENTRIES.map((entry) => (
        <li key={entry.text} className="flex min-w-0 items-center gap-2 rounded-md border border-line px-2.5 py-2">
          <Glyph kind={entry.kind} icon={entry.icon} />
          <span className="flex w-20 shrink-0">
            <Pill tone={BADGE[entry.kind].tone}>{BADGE[entry.kind].label}</Pill>
          </span>
          <span className="min-w-0 flex-1 text-fg">{entry.text}</span>
          {entry.picture === undefined ? null : <Thumb src={entry.picture} className="h-14 w-24 shrink-0 sm:h-16 sm:w-28" />}
        </li>
      ))}
    </ul>
  );
}

/** B: a new extra as a card with its picture, then one line per change. */
function NewsB(): ReactNode {
  const extras = ENTRIES.filter((entry) => entry.kind === 'extra');
  const rest = ENTRIES.filter((entry) => entry.kind !== 'extra');
  return (
    <div className="flex flex-col gap-3">
      {extras.map((entry) => (
        <figure key={entry.text} className="overflow-hidden rounded-lg border border-accent/40 bg-accent/5">
          {entry.picture === undefined ? null : (
            <img src={entry.picture} alt="" className="h-36 w-full object-cover object-left-top sm:h-44" />
          )}
          <figcaption className="flex items-center gap-2 border-t border-line px-2.5 py-2">
            <Glyph kind="extra" icon={entry.icon} />
            <span className="min-w-0 flex-1 font-medium text-fg">{entry.text}</span>
            <Pill tone="info">New extra</Pill>
          </figcaption>
        </figure>
      ))}
      <ul className="flex flex-col divide-y divide-line rounded-md border border-line">
        {rest.map((entry) => (
          <li key={entry.text} className="flex min-w-0 items-center gap-2 px-2.5 py-2">
            <Glyph kind={entry.kind} />
            <span className="min-w-0 flex-1 text-fg">{entry.text}</span>
            <Pill tone={BADGE[entry.kind].tone}>{BADGE[entry.kind].label}</Pill>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** C: grouped under New and Fixed; new extras as picture tiles in New. */
function NewsC(): ReactNode {
  const groups: { kind: 'added' | 'fixed'; label: string; entries: Entry[] }[] = [
    { kind: 'added', label: 'New', entries: ENTRIES.filter((entry) => entry.kind !== 'fixed') },
    { kind: 'fixed', label: 'Fixed', entries: ENTRIES.filter((entry) => entry.kind === 'fixed') },
  ];
  return (
    <div className="flex flex-col gap-4">
      {groups.map((group) => {
        const tiles = group.entries.filter((entry) => entry.picture !== undefined);
        const lines = group.entries.filter((entry) => entry.picture === undefined);
        return (
          <section key={group.kind} className="flex flex-col gap-2">
            <h3 className="flex items-center gap-2 text-sm font-medium text-fg">
              <Glyph kind={group.kind} />
              {group.label}
              <Pill neutral="muted">{String(group.entries.length)}</Pill>
            </h3>
            {tiles.length === 0 ? null : (
              <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                {tiles.map((entry) => (
                  <figure key={entry.text} className="flex flex-col gap-1.5 rounded-lg border border-line p-1.5">
                    <Thumb src={entry.picture ?? ''} className="h-28 w-full" />
                    <figcaption className="flex items-center gap-2 px-1">
                      <Icon name={entry.icon ?? 'extra'} />
                      <span className="min-w-0 flex-1 truncate font-medium text-fg">{entry.text}</span>
                      <Pill tone="info">New extra</Pill>
                    </figcaption>
                  </figure>
                ))}
              </div>
            )}
            <ul className="flex flex-col gap-1 pl-9">
              {lines.map((entry) => (
                <li key={entry.text} className="flex min-w-0 items-baseline gap-2 text-fg">
                  <span aria-hidden="true" className={group.kind === 'fixed' ? 'text-ok' : 'text-accent'}>
                    {group.kind === 'fixed' ? '✓' : '+'}
                  </span>
                  <span className="min-w-0">{entry.text}</span>
                </li>
              ))}
            </ul>
          </section>
        );
      })}
    </div>
  );
}

export function NewsProposal({ variant }: { variant: string }): ReactNode {
  const body = variant === 'a' ? <NewsA /> : variant === 'b' ? <NewsB /> : <NewsC />;
  return (
    <Dialog title="What's new 0.2.1" onClose={() => undefined} layer={KeyLayer.Dialog} size="wide" showClose={false}>
      <div className="flex flex-col gap-4">
        {body}
        <div className="flex flex-wrap items-center gap-3">
          <a href="#changelog" className="inline-flex items-center gap-1 text-accent underline underline-offset-2">
            Changelog
            <Icon name="external" />
          </a>
          <button type="button" className={`ml-auto ${btnPrimary}`}>
            Close
          </button>
        </div>
      </div>
    </Dialog>
  );
}

/* -------------------------------------------------------------------------- */
/* Settings › Updates                                                         */
/* -------------------------------------------------------------------------- */

/** What the automatic setting can be doing. */
export type AutoStep = 'off' | 'current' | 'downloading' | 'waiting' | 'password' | 'onquit' | 'installing' | 'failed';
export const AUTO_STEPS: readonly AutoStep[] = ['off', 'current', 'downloading', 'waiting', 'password', 'onquit', 'installing', 'failed'];

function Bar({ share }: { share: number }): ReactNode {
  return (
    <span className="relative inline-block h-1.5 w-28 overflow-hidden rounded-full bg-panel-2" aria-hidden="true">
      <span className="absolute inset-y-0 left-0 rounded-full bg-accent" style={{ width: `${String(share)}%` }} />
    </span>
  );
}

/** The automatic update's state, as badges and at most one button. */
function AutoState({ step, variant }: { step: AutoStep; variant: string }): ReactNode {
  switch (step) {
    case 'off':
      return <Pill neutral="muted" title="Download stays by hand">Manual</Pill>;
    case 'current':
      return (
        <Pill tone="ok" title="Checked 12 min ago">
          Up to date
        </Pill>
      );
    case 'downloading':
      return (
        <span className="flex items-center gap-2">
          <Pill tone="info">0.1.3</Pill>
          <Bar share={42} />
          <span className="tabular-nums text-muted">16.0 / 38.0 MiB</span>
        </span>
      );
    case 'waiting':
      return (
        <span className="flex flex-wrap items-center gap-2">
          <Pill tone="ok" title="Signature and sha256 verified">
            0.1.3 ready
          </Pill>
          <Pill tone="warn" title="Installs once these are closed">
            <span className="inline-flex items-center gap-1">
              <Icon name="hourglass" />2 terminals · 1 forward
            </span>
          </Pill>
          <button type="button" className={btnSecondary}>
            Restart now
          </button>
        </span>
      );
    case 'password':
      return (
        <span className="flex flex-wrap items-center gap-2">
          <Pill tone="ok" title="Signature and sha256 verified">
            0.1.3 ready
          </Pill>
          <Pill tone="warn" title="The .deb installs through pkexec: your password is asked on Install">
            <span className="inline-flex items-center gap-1">
              <Icon name="lock" />
              Password
            </span>
          </Pill>
          <button type="button" className={btnPrimary}>
            Install
          </button>
        </span>
      );
    case 'onquit':
      return (
        <span className="flex flex-wrap items-center gap-2">
          <Pill tone="ok" title="Signature and sha256 verified">
            0.1.3 ready
          </Pill>
          <Pill tone="info" title="Installed when KubeTower closes; the next start is 0.1.3">
            <span className="inline-flex items-center gap-1">
              <Icon name="clock" />
              On quit
            </span>
          </Pill>
          {variant === 'c' ? (
            <button type="button" className={btnSecondary}>
              Restart now
            </button>
          ) : null}
        </span>
      );
    case 'installing':
      return (
        <Pill tone="info" title="KubeTower closes, installs and starts again">
          Installing 0.1.3…
        </Pill>
      );
    case 'failed':
      return (
        <span className="flex flex-wrap items-center gap-2">
          <span className="min-w-0 truncate text-bad" title="kubetower-0.1.3-x86_64.AppImage does not match its signed sha256">
            Failed
          </span>
          <button type="button" className={btnSecondary}>
            Retry
          </button>
        </span>
      );
  }
}

/** The setting itself, per variant. */
function Setting({ variant, on }: { variant: string; on: boolean }): ReactNode {
  if (variant === 'b') {
    const choices = ['Automatic', 'Ask', 'Off'];
    const picked = on ? 'Automatic' : 'Ask';
    return (
      <span className="flex flex-wrap items-center gap-2">
        <span className="text-fg">Updates</span>
        <span role="radiogroup" aria-label="Updates" className="inline-flex overflow-hidden rounded-md border border-line">
          {choices.map((choice) => (
            <button
              key={choice}
              type="button"
              role="radio"
              aria-checked={choice === picked}
              title={
                choice === 'Automatic' ? 'Download, verify and install' : choice === 'Ask' ? 'Download and Install by hand' : 'Check only'
              }
              className={`px-2.5 py-1 ${choice === picked ? 'bg-accent text-fg' : 'text-muted hover:text-fg'}`}
            >
              {choice}
            </button>
          ))}
        </span>
      </span>
    );
  }
  return (
    <span className="flex flex-wrap items-center gap-2">
      <ExtraSwitch on={on} label="Install updates automatically" disabled={false} onToggle={() => undefined} />
      <span className="text-fg">Install updates automatically</span>
      {variant === 'c' ? (
        <span className={`inline-flex overflow-hidden rounded-md border border-line ${on ? '' : 'opacity-50'}`}>
          {['On quit', 'At once'].map((choice, index) => (
            <span
              key={choice}
              title={index === 0 ? 'Installed when KubeTower closes' : 'Installed as soon as nothing is open'}
              className={`px-2 py-0.5 ${index === 0 ? 'bg-accent text-fg' : 'text-muted'}`}
            >
              {choice}
            </span>
          ))}
        </span>
      ) : null}
    </span>
  );
}

/** The update block of Settings › Infos, in one step of one variant. */
export function UpdatesBlock({ variant, step }: { variant: string; step: AutoStep }): ReactNode {
  return (
    <div className="mt-2 flex flex-col gap-2 text-xs" data-proposal-updates={variant}>
      <Setting variant={variant} on={step !== 'off'} />
      <span className="flex min-w-0 flex-wrap items-center gap-2">
        <AutoState step={step} variant={variant} />
      </span>
    </div>
  );
}

/** Settings' band, only where the automatic update waits for a person. */
function Band({ step, variant }: { step: AutoStep; variant: string }): ReactNode {
  if (step !== 'waiting' && step !== 'password' && step !== 'failed') return null;
  return (
    <section className="mb-3 flex flex-wrap items-center gap-3 rounded-lg border border-accent/40 bg-accent/10 px-3 py-2 text-sm">
      <span className="font-medium text-fg">Update</span>
      <span className="ml-auto flex min-w-0 max-w-full flex-wrap items-center gap-2 text-xs">
        <AutoState step={step} variant={variant} />
      </span>
    </section>
  );
}

/**
 * Puts the proposal into the real Settings › Infos, drawn by the console
 * scene beside it: the current Download, checkbox and band are hidden, and the
 * variant is drawn where they were.
 */
export function UpdatesProposal({ variant, step }: { variant: string; step: AutoStep }): ReactNode {
  const [hosts, setHosts] = useState<{ block: HTMLElement; band: HTMLElement } | null>(null);
  useEffect(() => {
    const timer = window.setInterval(() => {
      const action = document.querySelector('[data-update-action]');
      const infos = document.querySelector('[data-infos]');
      if (action === null || infos === null) return;
      window.clearInterval(timer);
      const block = document.createElement('div');
      action.after(block);
      const band = document.createElement('div');
      infos.before(band);
      setHosts({ block, band });
    }, 50);
    return () => window.clearInterval(timer);
  }, []);
  return (
    <>
      <style>{'[data-update-action],[data-install-updates],[data-update-banner]{display:none!important}'}</style>
      {hosts === null ? null : (
        <>
          {createPortal(<UpdatesBlock variant={variant} step={step} />, hosts.block)}
          {createPortal(<Band step={step} variant={variant} />, hosts.band)}
          <span data-proposal-ready="" hidden />
        </>
      )}
    </>
  );
}

/** Every step of one variant, one row each, for comparing. */
export function UpdatesSheet({ variant }: { variant: string }): ReactNode {
  const steps = AUTO_STEPS.filter((step) => variant === 'c' || step !== 'onquit');
  return (
    <div className="min-h-screen bg-bg p-4 text-fg" data-proposal-ready="">
      <table className="w-full max-w-4xl border-separate border-spacing-y-1.5 text-xs">
        <tbody>
          {steps.map((step) => (
            <tr key={step}>
              <td className="w-28 pr-3 align-top font-mono text-muted">{step}</td>
              <td className="rounded-lg border border-line bg-panel px-3 pb-2">
                <UpdatesBlock variant={variant} step={step} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
