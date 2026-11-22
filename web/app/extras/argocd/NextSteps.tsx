import type { ReactNode } from 'react';
import { CopyButton } from '@kubetower/ui';

import { APPLICATION_KIND } from './data';

/**
 * The two refusals somebody can act on, drawn where they meet them: the engine
 * is not installed, or it is and the read was refused, for Argo CD and
 * for Flux CD.
 *
 * The notice above each says what the cluster answered. These say what to do
 * about it, with the commands to copy, because "the request failed" over a
 * refusal that no retry can change sends the operator looking in the wrong
 * place.
 */

export type EngineKey = 'argocd' | 'flux';

interface EngineText {
  product: string;
  /** The install panel's heading: what the page lists. */
  heading: string;
  docs: string;
  docsLabel: string;
  install: string;
  check: string;
  /** What the page lists once it is installed, in a sentence. */
  lists: string;
  /** The ClusterRole's rules, as YAML lines under `rules:`. */
  rules: readonly string[];
  roleSuffix: string;
}

export const ARGOCD_DOCS = 'https://argo-cd.readthedocs.io/en/stable/getting_started/';
export const FLUX_DOCS = 'https://fluxcd.io/flux/installation/';

/** Argo CD's own Getting Started install, unchanged: the project's manifests,
 *  applied server-side because its CRDs are too large for a client-side apply. */
export const INSTALL_ARGOCD = [
  'kubectl create namespace argocd',
  'kubectl apply -n argocd --server-side --force-conflicts \\',
  '  -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml',
].join('\n');

/** Flux's own install without its CLI: the release's manifests. `flux install`
 *  and `flux bootstrap` are the documented alternatives once the CLI is there. */
export const INSTALL_FLUX = 'kubectl apply -f https://github.com/fluxcd/flux2/releases/latest/download/install.yaml';

const ENGINES: Readonly<Record<EngineKey, EngineText>> = {
  argocd: {
    product: 'Argo CD',
    heading: 'This page lists Argo CD Applications',
    docs: ARGOCD_DOCS,
    docsLabel: 'Getting Started',
    install: INSTALL_ARGOCD,
    check: `kubectl get crd ${APPLICATION_KIND}`,
    lists: "each Application appears here with its sync and health, in Argo CD's own words",
    // The three kinds the page reads: the Applications, and the
    // ApplicationSets and AppProjects its panels draw.
    rules: ['  - apiGroups: [argoproj.io]', '    resources: [applications, applicationsets, appprojects]', '    verbs: [get, list]'],
    roleSuffix: 'argocd-applications-read',
  },
  flux: {
    product: 'Flux CD',
    heading: 'This page lists Flux CD Kustomizations and HelmReleases',
    docs: FLUX_DOCS,
    docsLabel: 'installation guide',
    install: INSTALL_FLUX,
    check: 'kubectl get crd kustomizations.kustomize.toolkit.fluxcd.io helmreleases.helm.toolkit.fluxcd.io',
    lists: "each Kustomization and HelmRelease appears here with its Ready condition, in Flux's own words",
    rules: [
      '  - apiGroups: [kustomize.toolkit.fluxcd.io, helm.toolkit.fluxcd.io]',
      '    resources: [kustomizations, helmreleases]',
      '    verbs: [get, list]',
    ],
    roleSuffix: 'flux-read',
  },
};

export function Install({ engine = 'argocd' }: { engine?: EngineKey }): ReactNode {
  const e = ENGINES[engine];
  const id = `${engine}-install`;
  return (
    <section aria-labelledby={id} className="rounded-lg border border-line bg-panel-2 p-4 text-sm">
      <h2 id={id} className="text-base font-medium">
        {e.heading}
      </h2>
      <p className="mt-1 text-muted">
        Nothing is broken: there is nothing to list until {e.product} is installed on this cluster. Once it is, {e.lists}.
      </p>
      <ol className="mt-3 flex list-decimal flex-col gap-3 pl-5">
        <li>
          <p>
            Install {e.product}, as its{' '}
            <a href={e.docs} target="_blank" rel="noreferrer" className="underline">
              {e.docsLabel}
            </a>{' '}
            describes:
          </p>
          <Command value={e.install} label="Copy the install command" />
        </li>
        <li>
          <p>Check that the cluster now serves its kinds:</p>
          <Command value={e.check} label="Copy the check" />
        </li>
        <li>
          <p>
            Press <strong>Refresh</strong>. If the console runs in the cluster from its chart, it also needs the right to
            read them: the page says so then, and how.
          </p>
        </li>
      </ol>
    </section>
  );
}

/** Argo CD's install panel, under the name the first version exported. */
export function InstallArgoCD(): ReactNode {
  return <Install engine="argocd" />;
}

/**
 * The cluster names who it refused, `system:serviceaccount:<ns>:<name>` for a
 * ServiceAccount. The console's own account and a signed-in person are granted
 * the read in different places, so the panel starts by saying which.
 */
export function serviceAccountOf(subject: string | undefined): { namespace: string; name: string } | undefined {
  const parts = subject?.split(':');
  if (parts?.length !== 4 || parts[0] !== 'system' || parts[1] !== 'serviceaccount') return undefined;
  return { namespace: parts[2] ?? '', name: parts[3] ?? '' };
}

/** A ClusterRole that reads the engine's objects and nothing else, bound to the
 *  refused account. `get` and `list` only: the page never watches. */
export function grantManifest(subject: string | undefined, engine: EngineKey = 'argocd'): string {
  const e = ENGINES[engine];
  const sa = serviceAccountOf(subject);
  const name = sa === undefined ? e.roleSuffix : `${sa.name}-${e.roleSuffix}`;
  const who =
    sa !== undefined
      ? ['  - kind: ServiceAccount', `    name: ${sa.name}`, `    namespace: ${sa.namespace}`]
      : ['  - kind: User', '    apiGroup: rbac.authorization.k8s.io', `    name: ${subject ?? '<your user name>'}`];
  return [
    "kubectl apply -f - <<'EOF'",
    'apiVersion: rbac.authorization.k8s.io/v1',
    'kind: ClusterRole',
    'metadata:',
    `  name: ${name}`,
    'rules:',
    ...e.rules,
    '---',
    'apiVersion: rbac.authorization.k8s.io/v1',
    'kind: ClusterRoleBinding',
    'metadata:',
    `  name: ${name}`,
    'roleRef:',
    '  apiGroup: rbac.authorization.k8s.io',
    '  kind: ClusterRole',
    `  name: ${name}`,
    'subjects:',
    ...who,
    'EOF',
  ].join('\n');
}

export function GrantRead({ subject, engine = 'argocd' }: { subject: string | undefined; engine?: EngineKey }): ReactNode {
  const e = ENGINES[engine];
  const sa = serviceAccountOf(subject);
  const id = `${engine}-grant`;
  return (
    <section aria-labelledby={id} className="rounded-lg border border-line bg-panel-2 p-4 text-sm">
      <h2 id={id} className="text-base font-medium">
        {e.product} is here; {sa !== undefined ? 'the console may not read it' : 'the read was refused'}
      </h2>
      {sa !== undefined ? (
        <>
          <p className="mt-1 text-muted">
            The cluster refused <Code>{subject}</Code>, the ServiceAccount this console runs as. Retrying changes
            nothing: the read has to be granted, in one of two ways.
          </p>
          <ol className="mt-3 flex list-decimal flex-col gap-3 pl-5">
            <li>
              <p>
                <strong>With the KubeTower chart</strong>, set <Code>rbac.customResources: true</Code> and upgrade the
                release. It is a read-only wildcard over every custom resource, which the console&apos;s own
                custom-resource pages use too, and the chart leaves it off until somebody decides.
              </p>
            </li>
            <li>
              <p>
                <strong>Or narrower</strong>: read {e.product}&apos;s objects and nothing else.
              </p>
              <Command value={grantManifest(subject, engine)} label="Copy the role and its binding" />
            </li>
          </ol>
        </>
      ) : (
        <>
          <p className="mt-1 text-muted">
            {subject === undefined ? (
              <>The cluster refused this read for you, as the console reviewed it.</>
            ) : (
              <>
                The cluster refused <Code>{subject}</Code>.
              </>
            )}{' '}
            Retrying changes nothing: whoever administers the cluster grants{' '}
            {subject === undefined ? 'your account' : 'that account'} the read of {e.product}&apos;s objects, for
            example with:
          </p>
          <Command value={grantManifest(subject, engine)} label="Copy the role and its binding" />
        </>
      )}
      <p className="mt-3 text-xs text-muted">
        Then press <strong>Refresh</strong>.
      </p>
    </section>
  );
}

function Command({ value, label }: { value: string; label: string }): ReactNode {
  return (
    <div className="mt-2 flex items-start gap-2">
      <pre className="flex-1 overflow-x-auto rounded-md bg-panel p-2 font-mono text-xs">{value}</pre>
      <CopyButton value={value} label={label} />
    </div>
  );
}

function Code({ children }: { children: ReactNode }): ReactNode {
  return <code className="rounded-sm bg-panel px-1 font-mono">{children}</code>;
}
