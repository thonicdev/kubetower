import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef, useState, type ReactNode } from 'react';
import {
  ApiError,
  CopyButton,
  PlanDialog,
  btnSecondary,
  focusRing,
  installPrereq,
  prereqQuery,
  previewPrereq,
  type ActOutcome,
  type ConfirmationPlan,
  type Prereq,
  type PrereqHelm,
} from '@kubetower/ui';

/**
 * Installing trivy-operator from the Security Center, beside its "absent"
 * pill. **The page writes nothing**: the console's own route plans one Argo
 * CD Application, checks it for the person, and creates it once the
 * Application's name is typed back. This draws what that route answers.
 *
 * Four shapes, each a label and a value, the detail in a `title`:
 *
 * - `offer` - an Install button; the dialog shows the chart, its version and
 *   the Application exactly as it will be sent, then the server's gesture.
 * - `helm` - no Argo CD to install through: the helm command, copyable,
 *   composed from the route's structured `helm`.
 * - `installing` - the Application exists (or was just created): Argo CD
 *   reconciles it, which takes minutes.
 * - `blocked` - the button, disabled, the reason in its `title`.
 */

/** The key of the console's own prerequisite table. */
export const OPERATOR = 'trivy-operator';

/** After an install, the page's reports are read again once, this much later:
 *  one bounded re-read, never an interval. */
export const REREAD_AFTER_MS = 30_000;

/** The helm command for a cluster with no Argo CD, as one line. */
export function helmCommand(h: PrereqHelm): string {
  return `helm upgrade --install ${h.release} ${h.chart} --repo ${h.repo} --version ${h.version} --namespace ${h.namespace} --create-namespace`;
}

const pill = 'self-start rounded-full border px-3 py-1 text-sm';

export function Install({ cluster, onInstalled }: { cluster: string; onInstalled: () => void }): ReactNode {
  const client = useQueryClient();
  const facts = useQuery(prereqQuery(cluster, OPERATOR));
  const [plan, setPlan] = useState<ConfirmationPlan | undefined>(undefined);
  const [previewing, setPreviewing] = useState(false);
  const [refused, setRefused] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const [outcome, setOutcome] = useState<ActOutcome | undefined>(undefined);
  const [installed, setInstalled] = useState(false);
  const later = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (later.current !== null) clearTimeout(later.current);
    },
    [],
  );

  const result = facts.data;
  if (result === undefined) {
    return facts.isError ? (
      <span data-install="unread" className={`${pill} border-line text-muted`} title={facts.error.message}>
        install —
      </span>
    ) : null;
  }
  if (result.state !== 'ok') {
    return (
      <span data-install="unread" className={`${pill} border-line text-muted`} title={result.reason.message}>
        install —
      </span>
    );
  }
  const p: Prereq = result.data;
  const chart = `${p.chart.name} ${p.chart.version}`;

  const open = async (): Promise<void> => {
    setPreviewing(true);
    setRefused(null);
    try {
      setPlan(await previewPrereq(cluster, OPERATOR));
    } catch (error) {
      setRefused(error instanceof Error ? error.message : String(error));
    } finally {
      setPreviewing(false);
    }
  };

  const apply = async (confirmed?: string): Promise<void> => {
    if (plan === undefined) return;
    setPending(true);
    setFailure(null);
    try {
      const done = await installPrereq(cluster, OPERATOR, plan.token, confirmed ?? '');
      setOutcome(done);
      setInstalled(true);
      void client.invalidateQueries({ queryKey: prereqQuery(cluster, OPERATOR).queryKey });
      if (later.current !== null) clearTimeout(later.current);
      later.current = setTimeout(onInstalled, REREAD_AFTER_MS);
    } catch (error) {
      setFailure(error instanceof Error ? error.message : String(error));
      // A 409 carries the plan as it stands now; the person reads that one.
      if (error instanceof ApiError && error.plan !== undefined) setPlan(error.plan);
    } finally {
      setPending(false);
    }
  };

  const close = (): void => {
    setPlan(undefined);
    setOutcome(undefined);
    setFailure(null);
  };

  const via = p.argo.namespace === undefined ? '' : ` · Argo CD ${p.argo.namespace}`;
  const where = p.application === undefined ? '' : `${p.application.namespace}/${p.application.name}`;
  const command = p.helm === undefined ? '' : helmCommand(p.helm);

  // The dialog stays drawn whatever the face becomes: once the install has
  // run, the face says installing while the dialog reports the outcome.
  const face =
    installed || p.application?.state === 'present' ? (
      <span data-install="installing" className={`${pill} border-accent/50 text-accent`} title={`Argo CD Application ${where} · ${chart}`}>
        installing
      </span>
    ) : p.helm !== undefined ? (
      <span data-install="helm" className="flex min-w-0 max-w-full items-center gap-2" title={p.reason?.message}>
        <span className="text-2xs tracking-wide text-muted uppercase">helm</span>
        <code data-helm-command="" className="min-w-0 truncate rounded border border-line bg-panel px-2 py-0.5 font-mono text-2xs" title={command}>
          {command}
        </code>
        <CopyButton value={command} label="Copy" />
      </span>
    ) : (
      <>
        <button
          type="button"
          data-install={p.installable ? 'offer' : 'blocked'}
          className={btnSecondary}
          disabled={!p.installable || previewing}
          onClick={() => void open()}
          title={p.installable ? `${chart}${via}` : (p.reason?.message ?? chart)}
        >
          {previewing ? 'Install…' : 'Install'}
        </button>
        {refused === null ? null : (
          <span data-install-refused="" className={`${pill} border-bad/50 text-bad`} title={refused}>
            refused
          </span>
        )}
      </>
    );

  return (
    <>
      {face}
      {plan === undefined ? null : (
        <PlanDialog
          title={`Install ${p.label}`}
          plan={plan}
          pending={pending}
          error={failure}
          outcome={outcome}
          onCancel={close}
          onApply={(confirmed) => void apply(confirmed)}
        >
          <dl data-install-chart="" className="grid grid-cols-[max-content_1fr] gap-x-3 gap-y-0.5">
            <dt className="text-muted">Chart</dt>
            <dd className="font-mono">{p.chart.name}</dd>
            <dt className="text-muted">Version</dt>
            <dd className="font-mono">
              {p.chart.version}
              {p.chart.appVersion === undefined ? null : <span className="text-muted"> · app {p.chart.appVersion}</span>}
            </dd>
            <dt className="text-muted">Namespace</dt>
            <dd className="font-mono">{p.chart.namespace}</dd>
            <dt className="text-muted">Repository</dt>
            <dd className="truncate font-mono" title={p.chart.repo}>
              {p.chart.repo}
            </dd>
          </dl>
          <pre
            data-install-yaml=""
            tabIndex={0}
            className={`max-h-72 overflow-auto rounded border border-line bg-bg p-2 font-mono text-2xs leading-snug ${focusRing}`}
          >
            {plan.facts.request}
          </pre>
        </PlanDialog>
      )}
    </>
  );
}
