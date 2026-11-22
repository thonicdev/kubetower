import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef, useState, type ReactNode } from 'react';

import { probeCluster, queryKeys, useCapabilities, type Reason, type Remedy, type State } from '../../api';
import { btnPrimary, btnSecondary } from '../../components/buttons';
import { CopyButton } from '../../components/CopyButton';
import { formatClock } from '../../components/instant';
import { useDockIfPresent } from '../../dock/DockContext';
import { stateWord } from '../../shell/clusterState';
import { CREDENTIAL_CODES } from '../../shell/downCluster';
import { overviewKey } from './data';

/**
 * An unreachable cluster's whole page: one warning, in the server's words, and
 * one Refresh that works.
 *
 * A single warning replaces a dashboard of eleven panels each repeating the same
 * refusal, beside a kinds menu that stayed drawn and inert. Refresh is a real
 * probe with a busy state, not a bare `refetch()` that dials for up to ten
 * seconds and brings the identical notice back.
 *
 * ## Refresh asks the probe, about this cluster only
 *
 * `POST /api/c/{cluster}/probe` runs the probe against this one cluster, now,
 * and the button says *Checking…* while it does. Then the clusters list, this
 * overview and this menu are invalidated together, and the mutation stays
 * pending until they are re-read - so the button does not come back before the
 * page and the rail agree on the new verdict. A cluster that answers takes the
 * page back to the dashboard; one that does not leaves this warning, with the
 * probe's fresh words.
 *
 * ## `check`: a verdict too old to stand alone is asked again, once
 *
 * A verdict older than thirty seconds is a reason to look again
 * (`shell/downCluster.ts`). With `check`, the warning is drawn at once and the
 * same probe runs on its own, once per mount, showing *Checking…* - never a
 * loading dashboard, and never a loop: after that the background round's own
 * backoff (once a minute, up to once every ten) is what keeps the verdict
 * current, and Refresh is there for whoever will not wait.
 */
export function ClusterDown({
  cluster,
  state,
  reason,
  check = false,
  remedy,
}: {
  cluster: string;
  state: State;
  reason: Reason | undefined;
  check?: boolean;
  /** The server's remedy; its `command` is offered to copy. */
  remedy?: Remedy | undefined;
}): ReactNode {
  const client = useQueryClient();
  const probe = useMutation({
    mutationFn: () => probeCluster(cluster),
    onSettled: () =>
      Promise.all([
        client.invalidateQueries({ queryKey: queryKeys.clusters }),
        client.invalidateQueries({ queryKey: overviewKey(cluster) }),
        client.invalidateQueries({ queryKey: queryKeys.nav(cluster) }),
      ]),
  });
  const checking = probe.isPending;

  // Decided on arrival and only then: a fresh verdict that ages past thirty
  // seconds while the page is open is not a second reason to probe - the
  // background round is already keeping it. Once per mount, StrictMode's second
  // effect included: the ref survives it. The caller keys this component by
  // cluster, so another cluster is another mount and gets its own check.
  const [onArrival] = useState(check);
  const checked = useRef(false);
  const { mutate } = probe;
  useEffect(() => {
    if (!onArrival || checked.current) return;
    checked.current = true;
    mutate();
  }, [onArrival, mutate]);

  // When Retry's probe settled: the time the screen says it looked, while the
  // session is still lapsed. Cleared as another Retry starts, so the line never
  // stands beside *Checking…* describing the previous check.
  const [retriedAt, setRetriedAt] = useState<Date | null>(null);
  const retry = (): void => {
    setRetriedAt(null);
    probe.mutate(undefined, { onSuccess: () => setRetriedAt(new Date()) });
  };

  const lapsed = state === 'unreadable' && reason?.code !== undefined && CREDENTIAL_CODES.includes(reason.code);
  const command = remedy?.command === undefined || remedy.command === '' ? undefined : remedy.command;
  if (lapsed) {
    return (
      <SignInAgain
        cluster={cluster}
        refused={reason.code === 'credentials_refused'}
        sentence={reason.message}
        command={command}
        retry={retry}
        checking={checking}
        failed={probe.error}
        retriedAt={retriedAt}
      />
    );
  }

  return (
    <section
      data-cluster-down={state}
      aria-labelledby="cluster-down-title"
      className="flex max-w-3xl flex-col gap-3 rounded-lg border border-bad bg-panel-2 p-4"
    >
      <h1 id="cluster-down-title" className="text-sm font-semibold text-bad">
        This cluster&apos;s API {stateWord(state)}
      </h1>
      {reason?.message === undefined || reason.message === '' ? null : (
        <p className="text-sm wrap-anywhere">{reason.message}</p>
      )}
      <CheckFailed error={probe.error} />
      <div>
        <button
          type="button"
          onClick={() => probe.mutate()}
          disabled={checking}
          aria-busy={checking}
          className={btnSecondary}
        >
          {checking ? 'Checking…' : 'Refresh'}
        </button>
      </div>
    </section>
  );
}

/** The check itself did not complete - the console, not the cluster. Said
 *  apart from the cluster's own words, which still stand above it. */
function CheckFailed({ error }: { error: Error | null }): ReactNode {
  if (error === null) return null;
  return (
    <p role="status" className="text-xs text-warn">
      The check could not be made: {error.message}
    </p>
  );
}

/**
 * A cluster whose credentials are what every read fails on: one screen in the
 * page's place, saying what happened, what to run, and Retry.
 *
 * **The console does not run the sign-in itself.** `aws sso login` is a
 * person signing in through a browser, as themselves; this process holds no
 * credential of its own and starts no command a page asks for. Where this
 * console has a terminal on this machine (`capabilities.localShell`, the
 * desktop), *Open a terminal* opens the dock's shell on this cluster, where the
 * copied command is pasted and run - the same door as the top bar's Shell, so
 * nothing new is reachable from a browser. Elsewhere - a container, a shared
 * console - the command is the host's to run, and the screen says so.
 *
 * Retry asks the probe again, now, and says *Checking…* while it does. Once the
 * session answers, the cluster list says so and the page comes back where the
 * person left it. When it does not, the screen would look exactly as before the
 * click, so it says that it looked, and when: a Retry with no visible effect
 * reads as a button that does nothing.
 */
function SignInAgain({
  cluster,
  refused,
  sentence,
  command,
  retry,
  checking,
  failed,
  retriedAt,
}: {
  cluster: string;
  refused: boolean;
  sentence: string;
  command: string | undefined;
  retry: () => void;
  checking: boolean;
  failed: Error | null;
  /** When the last Retry's probe answered; the screen is still drawn, so the
   *  session is still lapsed. */
  retriedAt: Date | null;
}): ReactNode {
  const capabilities = useCapabilities();
  const dock = useDockIfPresent();
  const terminal = capabilities.data?.localShell === true && dock !== null;
  const title =
    command !== undefined
      ? 'The AWS SSO session behind this cluster has expired'
      : refused
        ? 'The credentials behind this cluster were refused'
        : 'The session behind this cluster has lapsed';
  return (
    <section
      data-cluster-down="expired"
      aria-labelledby="cluster-down-title"
      className="mx-auto mt-8 flex w-full max-w-2xl flex-col gap-4 rounded-lg border border-warn bg-panel-2 p-6"
    >
      <h1 id="cluster-down-title" className="text-base font-semibold text-warn">
        {title}
      </h1>
      <p className="text-sm">
        Every read of this cluster is refused until you sign in again. The cluster itself may be fine: KubeTower
        signs in the way your kubeconfig does and keeps no credentials of its own.
      </p>
      {command !== undefined ? (
        // The command alone and not the server's sentence too: it says the
        // same thing, and one message is clearer than two.
        <div className="flex flex-col gap-2 text-sm">
          <p>{terminal ? 'Run this in a terminal on this machine, then Retry:' : 'Run this where KubeTower runs, then Retry:'}</p>
          <div className="flex items-center gap-2">
            <code data-command className="rounded-md border border-line bg-panel px-2 py-1 font-mono text-xs wrap-anywhere">
              {command}
            </code>
            <CopyButton value={command} label="Copy the command" />
          </div>
        </div>
      ) : sentence === '' ? null : (
        <p className="text-sm wrap-anywhere text-muted">{sentence}</p>
      )}
      <CheckFailed error={failed} />
      {retriedAt === null || checking ? null : (
        <p role="status" data-still-lapsed className="text-xs text-muted">
          Checked at {formatClock(retriedAt)}: still signed out.
        </p>
      )}
      <div className="flex flex-wrap items-center gap-2">
        {terminal ? (
          <button
            type="button"
            title={`Open a shell on this machine, for ${cluster}`}
            onClick={() => dock.openShell({ kind: 'terminal', cluster, label: cluster })}
            className={btnSecondary}
          >
            Open a terminal
          </button>
        ) : null}
        <button type="button" onClick={retry} disabled={checking} aria-busy={checking} className={btnPrimary}>
          {checking ? 'Checking…' : 'Retry'}
        </button>
      </div>
    </section>
  );
}
