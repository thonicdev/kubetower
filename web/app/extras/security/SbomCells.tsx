import type { ReactNode } from 'react';

import { n, type Overview, type Source } from './data';
import { componentsTitle, verdictMark, verdictTitle, type Components, type Signature } from './supply';

/**
 * The supply chain's two cells of the workloads' table, and its two sources'
 * pills. A workload the admission said nothing about is `—`, never unsigned.
 */

/** The SBOM and signatures sources, as pills beside trivy-operator's own. */
export function supplySources(o: Overview): Source[] {
  return [o.sbom, o.signatures].filter((s): s is Source => s !== undefined);
}

const TONE: Readonly<Record<string, string>> = {
  pass: 'text-ok',
  fail: 'text-bad',
  skip: 'text-muted',
  unreadable: 'text-warn',
};

export function SignatureCell({ signature }: { signature: Signature | null | undefined }): ReactNode {
  const verdict = signature?.verdict;
  return (
    <span data-signature={verdict ?? 'none'} title={verdictTitle(signature)} className={verdict === undefined ? 'text-muted' : TONE[verdict]}>
      {verdictMark(signature)}
    </span>
  );
}

export function ComponentsCell({ components }: { components: Components | null | undefined }): ReactNode {
  if (components === null || components === undefined) {
    return (
      <span data-component-count="none" className="text-muted" title="No SBOM report">
        —
      </span>
    );
  }
  return (
    <span data-component-count={components.total} title={componentsTitle(components)}>
      {n(components.total)}
    </span>
  );
}
