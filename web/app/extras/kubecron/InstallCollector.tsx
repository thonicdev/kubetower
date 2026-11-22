import type { ReactNode } from 'react';
import { Link } from 'react-router';
import { CopyButton } from '@kubetower/ui';

import type { CollectorCheck, CollectorState } from './data';

import howItWorks from './assets/how-it-works.svg';
import pageWithHistory from './assets/page-with-history.png';

/**
 * What to install before the history columns can fill: the KubeCron collector,
 * one per cluster. Drawn where somebody meets the absence, on this page, rather
 * than in a document they would have to know to look for.
 *
 * **No collector is the ordinary case and not an error.** The verdicts above
 * this panel need nothing; what is missing is history, and the panel says so in
 * those words rather than in a warning color.
 */

export const COLLECTOR_REPO = 'https://github.com/schmitech-fr/kubecron';

/**
 * The chart's own install, in server mode. `image.tag=main` because the chart's
 * default tag (its appVersion) has no published image yet; drop it the day the
 * project publishes release images.
 */
export function installCommand(cluster: string): string {
  return [
    `git clone ${COLLECTOR_REPO}.git`,
    'helm install kubecron kubecron/charts/kubecron -n kubecron --create-namespace \\',
    '  --set mode=server \\',
    `  --set clusterID=${cluster} \\`,
    '  --set api.token="$(openssl rand -hex 32)" \\',
    '  --set image.tag=main',
  ].join('\n');
}

const CHECK = 'kubectl get svc -A -l kubecron.io/collector=true';

/** Where this cluster's collector is set: the Settings page's Extra tab, on
 *  this cluster. */
export function settingsHref(cluster: string): string {
  return `/settings/extra?cluster=${encodeURIComponent(cluster)}`;
}

export function SettingsLink({ cluster }: { cluster: string }): ReactNode {
  return (
    <Link to={settingsHref(cluster)} className="underline" data-collector-settings="">
      Settings › Extra › KubeCron
    </Link>
  );
}

const MARK: Record<CollectorCheck['state'], { mark: string; tone: string; said: string }> = {
  ok: { mark: '✓', tone: 'text-ok', said: 'ok' },
  skipped: { mark: '–', tone: 'text-muted', said: 'not needed' },
  missing: { mark: '✕', tone: 'text-warn', said: 'not found' },
  several: { mark: '!', tone: 'text-warn', said: 'several' },
  denied: { mark: '✕', tone: 'text-bad', said: 'refused' },
  refused: { mark: '✕', tone: 'text-bad', said: 'refused' },
  failed: { mark: '✕', tone: 'text-bad', said: 'failed' },
};

const STEP: Record<CollectorCheck['step'], string> = {
  collector: 'Collector',
  secret: 'Token Secret',
  reach: 'Reached',
  token: 'Token',
};

const SOURCE: Record<NonNullable<CollectorCheck['source']>, string> = {
  environment: 'environment',
  setting: 'this cluster’s setting',
  default: 'default',
};

/**
 * Each step of finding and reading the collector, one line each
 *: found or not, its Secret found, refused (with the right
 * that is missing) or not needed, the collector reached, its token accepted.
 * Where a value came from - the environment, this cluster's setting or the
 * default - is said beside it.
 */
export function CollectorChecks({ state }: { state: CollectorState }): ReactNode {
  const checks = state.checks ?? [];
  if (checks.length === 0) return null;
  return (
    <ul className="flex flex-col gap-1 text-xs" data-collector-checks="">
      {checks.map((one, i) => {
        const mark = MARK[one.state] ?? MARK.failed;
        return (
          <li key={`${one.step}-${i}`} className="flex items-baseline gap-2" data-check={one.step} data-check-state={one.state}>
            <span aria-hidden="true" className={`w-3 shrink-0 text-center ${mark.tone}`}>
              {mark.mark}
            </span>
            <span className="w-24 shrink-0 text-muted">
              {STEP[one.step] ?? one.step}
              <span className="sr-only">: {mark.said}</span>
            </span>
            <span className="min-w-0 flex-1 break-words">
              {sentence(one.detail)}
              {one.source === undefined ? null : (
                <span className="ml-1 text-muted" data-check-source={one.source}>
                  ({SOURCE[one.source] ?? one.source})
                </span>
              )}
            </span>
          </li>
        );
      })}
    </ul>
  );
}

/**
 * What a collector that is not read needs from the person, under the sentence
 * that says why: each step, and where to set it - the cluster's settings, or
 * the deployment's variables when they are what decided. The install for none.
 */
export function CollectorHelp({ cluster, state }: { cluster: string; state: CollectorState }): ReactNode {
  if (state.state === 'absent') {
    const fromEnvironment = state.checks?.some((one) => one.step === 'collector' && one.source === 'environment') === true;
    return (
      <InstallCollector
        cluster={cluster}
        detail={state.detail}
        configured={state.configured}
        fromEnvironment={fromEnvironment}
      />
    );
  }
  if (state.state === 'ok') return null;
  return (
    <section data-collector-help={state.state} className="flex flex-col gap-2 rounded-lg border border-line bg-panel-2 p-4 text-sm">
      <CollectorChecks state={state} />
      <p className="text-xs text-muted">
        Set where the collector and its token are in <SettingsLink cluster={cluster} />, with no restart. The
        deployment&apos;s <code>KT_KUBECRON_COLLECTOR</code> and <code>KT_KUBECRON_TOKEN_SECRET</code>, where set, win
        over it.
      </p>
    </section>
  );
}

/** Why a collector is not read, in the route's words: one that is here, or
 *  KubeTower's own settings for it. Nothing for `ok` and `absent`, which the
 *  history line and the install panel say. `after` follows the sentence. */
export function CollectorSentence({ state, after }: { state: CollectorState; after?: string }): ReactNode {
  const tail = after === undefined ? null : <>. {after}</>;
  if (state.state === 'unusable') {
    return (
      <span className="text-warn">
        The KubeCron collector could not be read: {state.detail}
        {tail}
      </span>
    );
  }
  if (state.state === 'misconfigured') {
    return (
      <span className="text-warn">
        KubeTower&apos;s KubeCron configuration is invalid: {state.detail}
        {tail}
      </span>
    );
  }
  return null;
}

/** How the collector was reached, when the route says. */
export function CollectorSource({ state, className }: { state: CollectorState; className?: string }): ReactNode {
  if (state.source === undefined || state.state === 'absent') return null;
  return (
    <p data-collector-source={state.via} className={className ?? 'text-xs text-muted'}>
      {sentence(state.source)}
    </p>
  );
}

/** The collector's standing where it is not read: the sentence, and the help. */
export function CollectorStanding({ cluster, state }: { cluster: string; state: CollectorState }): ReactNode {
  if (state.state === 'ok') return null;
  return (
    <>
      {state.state === 'absent' ? null : (
        <p className="text-sm">
          <CollectorSentence state={state} />
        </p>
      )}
      <CollectorSource state={state} />
      <CollectorHelp cluster={cluster} state={state} />
    </>
  );
}

/** The route's sentence as the opening of a paragraph: Go writes it lower-case
 *  and unterminated, to sit inside other sentences. */
export function sentence(text: string): string {
  const t = text.trim();
  if (t === '') return t;
  const capital = t.charAt(0).toUpperCase() + t.slice(1);
  return /[.!?]$/.test(capital) ? capital : `${capital}.`;
}

export function InstallCollector({
  cluster,
  detail,
  configured,
  fromEnvironment = false,
}: {
  cluster: string;
  detail?: string | undefined;
  /** `configured` is the environment's, not this cluster's setting. */
  fromEnvironment?: boolean;
  /** The Service the environment or this cluster's setting names, which this
   *  cluster does not have. */
  configured?: string | undefined;
}): ReactNode {
  const command = installCommand(cluster);
  return (
    <section data-collector="absent" aria-labelledby="kc-install" className="rounded-lg border border-line bg-panel-2 p-4 text-sm">
      <h2 id="kc-install" className="text-base font-medium">
        Run history needs the KubeCron collector
      </h2>
      <p className="mt-1 text-muted">
        {sentence(detail ?? 'No KubeCron collector runs in this cluster')} The verdicts on this page need nothing more: they come
        from the CronJobs themselves. What the collector adds is what Kubernetes throws away within minutes of a run
        ending — outcomes, durations, exit codes, logs and resource use, kept for weeks.
      </p>

      <figure className="mt-4">
        <img src={howItWorks} alt="" className="w-full max-w-3xl" width={720} height={220} />
        <figcaption className="mt-1 text-xs text-muted">
          The collector runs in the cluster, read-only, and KubeTower reads it through a port-forward. Its token is read
          from a Secret in the collector&apos;s own namespace, and sent only to that collector.
        </figcaption>
      </figure>

      <ol className="mt-4 flex list-decimal flex-col gap-3 pl-5">
        <li>
          <p>
            Install the collector from the{' '}
            <a href={COLLECTOR_REPO} target="_blank" rel="noreferrer" className="underline">
              KubeCron project
            </a>{' '}
            (Apache-2.0), with its chart, in <strong>server</strong> mode:
          </p>
          <div className="mt-2 flex items-start gap-2">
            <pre className="flex-1 overflow-x-auto rounded-md bg-panel p-2 font-mono text-xs">{command}</pre>
            <CopyButton value={command} label="Copy the install command" />
          </div>
          <p className="mt-1 text-xs text-muted">
            Server mode serves only the read-only API and the chart grants it no write.
          </p>
        </li>
        {configured === undefined ? (
          <li data-install-step="configure">
            <p>
              Nothing to configure: KubeTower reads the Service labeled <code>kubecron.io/collector=true</code>, and its
              token from the <code>api-token</code> key of the Secret <code>kubecron-api</code> in that Service&apos;s
              namespace - what the command above creates. Installed under another name, or with several collectors in
              the cluster, set it in <SettingsLink cluster={cluster} />.
            </p>
          </li>
        ) : (
          <li data-install-step="configured">
            <p>
              KubeTower is set to read <code>{configured}</code>, which this cluster does not have: install the collector
              so that its Service is <code>{configured}</code>, or{' '}
              {fromEnvironment ? (
                // The variable decides it on every cluster, and the Settings
                // page refuses the write while it is set.
                <>
                  change <code>KT_KUBECRON_COLLECTOR</code> in KubeTower&apos;s environment.
                </>
              ) : (
                <>
                  change it in <SettingsLink cluster={cluster} />.
                </>
              )}
            </p>
          </li>
        )}
        <li>
          <p>Check that its Service is there:</p>
          <div className="mt-2 flex items-start gap-2">
            <pre className="flex-1 overflow-x-auto rounded-md bg-panel p-2 font-mono text-xs">{CHECK}</pre>
            <CopyButton value={CHECK} label="Copy the check" />
          </div>
        </li>
        <li>
          <p>
            Press <strong>Refresh</strong>. History starts from the moment the collector starts watching: it does not
            invent the past, and days before it are drawn as not watched rather than as days nothing ran.
          </p>
        </li>
      </ol>

      <figure className="mt-4">
        <img
          src={pageWithHistory}
          alt="The KubeCron page once a collector answers: each CronJob with its verdict, its failure rate and its durations."
          className="w-full max-w-3xl rounded-md border border-line"
        />
        <figcaption className="mt-1 text-xs text-muted">What this page shows once the collector answers.</figcaption>
      </figure>
    </section>
  );
}
