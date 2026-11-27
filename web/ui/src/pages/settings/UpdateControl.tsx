import type { ReactNode } from 'react';
import { useLocation } from 'react-router';

import { btnPrimary, btnSecondary } from '../../components/buttons';
import { focusRingTight } from '../../focus';
import { Icon } from '../../components/Icon';
import { Pill } from '../../components/Pill';
import { ExtraSwitch } from './extraParts';
import { needsPerson, PHASES, useForgetRefusals, useUpdateInstall, waitsOn, type UpdateInstall } from './updateInstall';

/**
 * The update's control, in the place it is drawn: the banner atop Settings and
 * Settings › Infos' version line (#48). One component, so the two always show
 * the same step:
 *
 *   idle        Download
 *   downloading a bar and the megabytes
 *   verifying   Verifying
 *   ready       Ready, then by hand what the restart ends and Restart to
 *               update; automatic (#122), On quit, the wait (open work or a
 *               password) and Restart now, or Install for a password
 *   installing  Installing…, then Failed and Retry if the console is not back
 *               within INSTALL_TIMEOUT_MS
 *   failed      the reason, Retry
 *   manual      Not writable, Open folder on the verified file
 *   (any other) nothing
 *
 * Two clicks and never one: Download only fetches and verifies; Restart to
 * update is a second request, because it ends the terminals and port-forwards
 * it counts. Nothing is drawn where installing is not supported here.
 */
export function UpdateControl({ prominent = false }: { prominent?: boolean }): ReactNode {
  const { latest, install, offered, busy, timedOut, waitAgain, refused, download, restart, openFolder } =
    useUpdateInstall();
  if (install === undefined || !install.supported) return null;
  // A download in any state is drawn; idle only while a newer release exists.
  if (install.phase === 'idle' && !offered) return null;
  if (!PHASES.has(install.phase)) return null;
  const start = (): void => {
    if (!busy) download.mutate();
  };
  const primary = prominent ? btnPrimary : btnSecondary;

  let step: ReactNode;
  switch (install.phase) {
    case 'downloading':
      step = <Progress install={install} />;
      break;
    case 'verifying':
      step = (
        <span data-update-step="verifying" role="status">
          <Pill tone="info" title="Signature and sha256">
            Verifying
          </Pill>
        </span>
      );
      break;
    case 'ready': {
      const auto = install.enabled;
      const waits = waitsOn(install);
      const ends = endsText(install);
      step = (
        <>
          <span data-update-step="ready">
            <Pill tone="ok" title={install.asset}>
              Ready {install.version}
            </Pill>
          </span>
          {auto && waits === undefined && install.when === 'quit' ? (
            <span data-update-on-quit="">
              <Pill tone="info" title="Installed when KubeTower closes">
                <span className="inline-flex items-center gap-1">
                  <Icon name="clock" />
                  On quit
                </span>
              </Pill>
            </span>
          ) : null}
          {waits === 'open' && ends !== undefined ? (
            <span data-update-waits="open">
              <Pill tone="warn" title="Installs once these are closed">
                <span className="inline-flex items-center gap-1">
                  <Icon name="hourglass" />
                  {ends}
                </span>
              </Pill>
            </span>
          ) : null}
          {waits === 'password' ? (
            <span data-update-waits="password">
              <Pill tone="warn" title="Your password is asked on Install">
                <span className="inline-flex items-center gap-1">
                  <Icon name="lock" />
                  Password
                </span>
              </Pill>
            </span>
          ) : null}
          {auto ? null : <Ends install={install} />}
          {install.reason === undefined || install.reason === '' ? null : (
            <span className="min-w-0 max-w-full truncate text-bad" data-update-reason="" title={install.reason}>
              {install.reason}
            </span>
          )}
          <button
            type="button"
            className={!auto || waits === 'password' ? btnPrimary : btnSecondary}
            disabled={busy}
            title={auto && ends !== undefined ? `Closes ${ends}` : undefined}
            onClick={() => {
              if (!busy) restart.mutate();
            }}
            data-update-restart=""
          >
            {!auto ? 'Restart to update' : waits === 'password' ? 'Install' : 'Restart now'}
          </button>
        </>
      );
      break;
    }
    case 'installing':
      step = timedOut ? (
        <>
          <span className="text-bad" data-update-step="timed-out" title="No answer from the restarted console">
            Failed
          </span>
          <button type="button" className={btnSecondary} onClick={waitAgain} data-update-wait="">
            Retry
          </button>
        </>
      ) : (
        <span data-update-step="installing" role="status">
          <Pill tone="info" title="KubeTower closes, installs and starts again">
            Installing…
          </Pill>
        </span>
      );
      break;
    case 'failed':
      step = (
        <>
          <span className="min-w-0 truncate text-bad" data-update-step="failed" title={install.reason}>
            {install.reason === undefined || install.reason === '' ? 'Failed' : install.reason}
          </span>
          <button
            type="button"
            className={btnSecondary}
            disabled={busy}
            onClick={start}
            data-update-retry=""
          >
            Retry
          </button>
        </>
      );
      break;
    case 'manual':
      step = (
        <>
          <span data-update-step="manual">
            <Pill tone="warn" title={install.asset}>
              Not writable
            </Pill>
          </span>
          <button
            type="button"
            className={primary}
            disabled={busy}
            onClick={() => {
              if (!busy) openFolder.mutate();
            }}
            title={install.folder}
            data-update-open-folder=""
          >
            Open folder
          </button>
        </>
      );
      break;
    default:
      step = (
        <button
          type="button"
          className={primary}
          disabled={busy}
          onClick={start}
          title={latest === undefined ? undefined : `KubeTower ${latest}`}
          data-update-download=""
        >
          Download
        </button>
      );
  }
  return (
    <span className="flex min-w-0 flex-wrap items-center gap-2 text-xs" data-update-control={install.phase}>
      {step}
      {refused === null ? null : (
        <span className="text-bad" role="alert" data-update-refused="">
          {refused.message}
        </span>
      )}
    </span>
  );
}

const MIB = 1024 * 1024;

function mebibytes(n: number): string {
  return (n / MIB).toFixed(1);
}

/** The download's bar and its figures. */
function Progress({ install }: { install: UpdateInstall }): ReactNode {
  const done = install.done ?? 0;
  const total = install.total ?? 0;
  // An unknown size has no share: the bar is indeterminate and only what came is counted.
  const share = total > 0 ? Math.min(100, Math.round((done / total) * 100)) : undefined;
  return (
    <span className="flex items-center gap-2" data-update-step="downloading">
      <span
        role="progressbar"
        aria-label="Download"
        aria-valuemin={0}
        aria-valuemax={100}
        {...(share === undefined ? {} : { 'aria-valuenow': share })}
        className="relative inline-block h-1.5 w-40 overflow-hidden rounded-full bg-panel-2"
      >
        {share === undefined ? (
          <span className="absolute inset-0 animate-pulse rounded-full bg-accent/50" />
        ) : (
          <span className="absolute inset-y-0 left-0 rounded-full bg-accent" style={{ width: `${String(share)}%` }} />
        )}
      </span>
      {share === undefined && done === 0 ? null : (
        <span className="tabular-nums text-muted" title={install.asset} data-update-size="">
          {share === undefined ? `${mebibytes(done)} MiB` : `${mebibytes(done)} / ${mebibytes(total)} MiB`}
        </span>
      )}
    </span>
  );
}

/** What the restart ends, as words; nothing when nothing is open. */
function endsText(install: UpdateInstall): string | undefined {
  const parts: string[] = [];
  const { terminals, forwards } = install.ends;
  if (terminals > 0) parts.push(`${String(terminals)} ${terminals === 1 ? 'terminal' : 'terminals'}`);
  if (forwards > 0) parts.push(`${String(forwards)} ${forwards === 1 ? 'forward' : 'forwards'}`);
  return parts.length === 0 ? undefined : parts.join(' · ');
}

/** What the restart ends, as one warn badge; nothing when nothing is open. */
function Ends({ install }: { install: UpdateInstall }): ReactNode {
  const ends = endsText(install);
  if (ends === undefined) return null;
  return (
    <span data-update-ends="">
      <Pill tone="warn" title="Closed by the restart">
        {ends}
      </Pill>
    </span>
  );
}

/** Settings' banner: by hand, while a newer release can be installed or one is
 *  on its way; automatic (#122), only while it waits on a person. */
export function UpdateBanner(): ReactNode {
  const { latest, install, offered } = useUpdateInstall();
  // The banner is on every Settings tab: it forgets a refusal for all of them.
  useForgetRefusals(useLocation().pathname);
  if (install === undefined || !install.supported) return null;
  if (!needsPerson(install, offered)) return null;
  if (!PHASES.has(install.phase)) return null;
  const version = install.version ?? latest;
  return (
    <section
      aria-label="Update"
      data-update-banner=""
      className="flex flex-wrap items-center gap-3 rounded-lg border border-accent/40 bg-accent/10 px-3 py-2 text-sm"
    >
      <span className="font-medium text-fg">Update available</span>
      {version === undefined ? null : <Pill tone="info">{version}</Pill>}
      <span className="ml-auto min-w-0 max-w-full">
        <UpdateControl prominent />
      </span>
    </section>
  );
}

/** When an automatic update installs, as the two choices draw it. */
const WHEN = [
  { value: 'quit', label: 'On quit', title: 'Installed when KubeTower closes' },
  { value: 'now', label: 'At once', title: 'Installed as soon as no terminal or forward is open' },
] as const;

/**
 * "Install updates automatically" (#122), where this console can install: a
 * switch, and when it installs - on quit (the default) or at once. Off, the
 * two choices are drawn but not offered, and Download stays by hand.
 */
export function InstallUpdatesSetting(): ReactNode {
  const { install, allow } = useUpdateInstall();
  if (install?.supported !== true) return null;
  const on = install.enabled;
  return (
    <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-fg" data-install-updates={on ? 'on' : 'off'}>
      <ExtraSwitch
        on={on}
        label="Install updates automatically"
        disabled={false}
        pending={allow.isPending}
        onToggle={() => allow.mutate({ enabled: !on })}
      />
      <span>Install updates automatically</span>
      <span role="radiogroup" aria-label="When" className={`inline-flex overflow-hidden rounded-md border border-line ${on ? '' : 'opacity-50'}`}>
        {WHEN.map((choice) => {
          const picked = install.when === choice.value;
          return (
            <button
              key={choice.value}
              type="button"
              role="radio"
              aria-checked={picked}
              disabled={!on}
              aria-disabled={allow.isPending || undefined}
              title={choice.title}
              onClick={() => {
                if (!picked && !allow.isPending) allow.mutate({ enabled: true, when: choice.value });
              }}
              data-install-when={choice.value}
              className={`px-2 py-0.5 disabled:cursor-not-allowed aria-disabled:cursor-wait ${focusRingTight} ${picked ? 'bg-accent text-fg' : 'text-muted hover:text-fg'}`}
            >
              {choice.label}
            </button>
          );
        })}
      </span>
      {allow.error === null ? null : (
        <span className="text-bad" role="alert">
          {allow.error.message}
        </span>
      )}
    </div>
  );
}

/** The badge on every Settings entry while an update can be installed. */
export function useUpdateBadge(): { shown: boolean; title: string } {
  const { running, latest, offered } = useUpdateInstall();
  return { shown: offered, title: badgeTitle(running, latest) };
}

/** The badge's tooltip: the running build and the release it would become (#48). */
export function badgeTitle(running: string | undefined, latest: string | undefined): string {
  const lines = ['Update available'];
  if (running !== undefined && running !== '') lines.push(`Running: ${running}`);
  if (latest !== undefined && latest !== '') lines.push(`New: ${latest}`);
  return lines.join('\n');
}
