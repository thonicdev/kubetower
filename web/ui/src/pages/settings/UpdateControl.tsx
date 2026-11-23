import type { ReactNode } from 'react';

import { btnPrimary, btnSecondary } from '../../components/buttons';
import { Pill } from '../../components/Pill';
import { useUpdateInstall, type UpdateInstall } from './updateInstall';

/**
 * The update's control, in the place it is drawn: the banner atop Settings and
 * Settings › Infos' version line (#48). One component, so the two always show
 * the same step:
 *
 *   idle        Download
 *   downloading a bar and the megabytes
 *   verifying   Verifying
 *   ready       Ready, what the restart ends, Restart to update
 *   installing  Installing…
 *   failed      the reason, Retry
 *
 * Two clicks and never one: Download only fetches and verifies; Restart to
 * update is a second request, because it ends the terminals and port-forwards
 * it counts. Nothing is drawn where installing is off or not supported here.
 */
export function UpdateControl({ prominent = false }: { prominent?: boolean }): ReactNode {
  const { latest, install, offered, download, restart } = useUpdateInstall();
  if (install === undefined || !install.enabled || !install.supported) return null;
  // A download in any state is drawn; idle only while a newer release exists.
  if (install.phase === 'idle' && !offered) return null;
  const refused = download.error ?? restart.error;
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
    case 'ready':
      step = (
        <>
          <span data-update-step="ready">
            <Pill tone="ok" title={install.asset}>
              Ready {install.version}
            </Pill>
          </span>
          <Ends install={install} />
          <button
            type="button"
            className={btnPrimary}
            disabled={restart.isPending}
            onClick={() => restart.mutate()}
            data-update-restart=""
          >
            Restart to update
          </button>
        </>
      );
      break;
    case 'installing':
      step = (
        <span data-update-step="installing" role="status">
          <Pill tone="info">Installing…</Pill>
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
            disabled={download.isPending}
            onClick={() => download.mutate()}
            data-update-retry=""
          >
            Retry
          </button>
        </>
      );
      break;
    default:
      step = (
        <button
          type="button"
          className={primary}
          disabled={download.isPending}
          onClick={() => download.mutate()}
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

const MB = 1024 * 1024;

function megabytes(n: number): string {
  return (n / MB).toFixed(1);
}

/** The download's bar and its figures. */
function Progress({ install }: { install: UpdateInstall }): ReactNode {
  const done = install.done ?? 0;
  const total = install.total ?? 0;
  const share = total > 0 ? Math.min(100, Math.round((done / total) * 100)) : 0;
  return (
    <span className="flex items-center gap-2" data-update-step="downloading">
      <span
        role="progressbar"
        aria-label="Download"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={share}
        className="relative inline-block h-1.5 w-40 overflow-hidden rounded-full bg-panel-2"
      >
        <span className="absolute inset-y-0 left-0 rounded-full bg-accent" style={{ width: `${String(share)}%` }} />
      </span>
      <span className="tabular-nums text-muted" title={install.asset}>
        {megabytes(done)} / {megabytes(total)} MB
      </span>
    </span>
  );
}

/** What the restart ends, as one warn badge; nothing when nothing is open. */
function Ends({ install }: { install: UpdateInstall }): ReactNode {
  const parts: string[] = [];
  const { terminals, forwards } = install.ends;
  if (terminals > 0) parts.push(`${String(terminals)} ${terminals === 1 ? 'terminal' : 'terminals'}`);
  if (forwards > 0) parts.push(`${String(forwards)} ${forwards === 1 ? 'forward' : 'forwards'}`);
  if (parts.length === 0) return null;
  return (
    <span data-update-ends="">
      <Pill tone="warn" title="Closed by the restart">
        {parts.join(' · ')}
      </Pill>
    </span>
  );
}

/** Settings' banner: shown while a newer release can be installed, or one is
 *  on its way. */
export function UpdateBanner(): ReactNode {
  const { latest, install, offered } = useUpdateInstall();
  if (install === undefined || !install.enabled || !install.supported) return null;
  if (!offered && install.phase === 'idle') return null;
  const version = install.version ?? latest;
  return (
    <section
      aria-label="Update"
      data-update-banner=""
      className="flex flex-wrap items-center gap-3 rounded-lg border border-accent/40 bg-accent/10 px-3 py-2 text-sm"
    >
      <span className="font-medium text-fg">Update available</span>
      {version === undefined ? null : <Pill tone="info">{version}</Pill>}
      <span className="ml-auto">
        <UpdateControl prominent />
      </span>
    </section>
  );
}

/** "Install updates", where this console can install. */
export function InstallUpdatesSetting(): ReactNode {
  const { install, allow } = useUpdateInstall();
  if (install === undefined) return null;
  return (
    <label className="mt-1 flex items-center gap-2 text-xs text-fg" data-install-updates="">
      <input
        type="checkbox"
        checked={install.enabled}
        disabled={allow.isPending}
        onChange={(event) => allow.mutate(event.target.checked)}
      />
      Install updates
      {allow.error === null ? null : (
        <span className="text-bad" role="alert">
          {allow.error.message}
        </span>
      )}
    </label>
  );
}

/** The badge on every Settings entry while an update can be installed. */
export function useUpdateBadge(): { shown: boolean; title: string } {
  const { latest, offered } = useUpdateInstall();
  return { shown: offered, title: latest === undefined ? 'Update available' : `Update available: ${latest}` };
}
