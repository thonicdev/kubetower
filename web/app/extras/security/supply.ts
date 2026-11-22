import { queryOptions } from '@tanstack/react-query';
import { EnvelopeError, request } from '@kubetower/ui';

import { KEEP_MS, KEY, STALE_MS, n, said, type Source, type WorkloadRef } from './data';

/**
 * The supply chain, as `extras/security` answers it: the SBOMs trivy-operator
 * writes and the verdict the admission wrote on each pod about its images.
 * Nothing here, nor in the Go, asks a registry: a verdict is the admission's,
 * and a workload with none is unknown (`—`), never unsigned.
 */

/** As Kyverno spells it, and `unreadable` for an annotation that could not be read. */
export type Verdict = 'pass' | 'fail' | 'skip' | 'unreadable';

export interface ImageVerdict {
  image: string;
  verdict: Verdict;
}

/** The admission's verdict on a workload's images; null where no pod carries one. */
export interface Signature {
  verdict: Verdict;
  images: ImageVerdict[];
  unreadable?: number;
}

export interface ImageComponents {
  container: string;
  image: string;
  components: number;
  dependencies: number;
}

/** A workload's SBOM as the table tells it; null where no SBOM report covers it. */
export interface Components {
  total: number;
  dependencies: number;
  images: ImageComponents[];
}

export interface SbomImage {
  container: string;
  image: string;
  digest?: string;
  components: number;
  dependencies: number;
  /** Scanned, but no SbomReport: its components are unknown, not zero. */
  absent?: boolean;
}

export interface Component {
  name: string;
  version: string;
  type: string;
  license: string;
  purl?: string;
  container: string;
}

export interface Tally {
  name: string;
  count: number;
}

/** `GET /workload/{namespace}/{kind}/{name}/sbom`. */
export interface Sbom {
  cluster: string;
  read: string;
  workload: WorkloadRef;
  source: Source;
  images: SbomImage[];
  components: Component[];
  total: number;
  types: Tally[];
  licenses: Tally[];
  notes: string[];
}

export function sbomPath(cluster: string, w: WorkloadRef): string {
  return `/x/${encodeURIComponent(cluster)}/${KEY}/workload/${encodeURIComponent(w.namespace)}/${encodeURIComponent(w.kind)}/${encodeURIComponent(w.name)}/sbom`;
}

export async function getSbom(cluster: string, w: WorkloadRef, signal?: AbortSignal): Promise<Sbom> {
  let body: Sbom;
  try {
    body = await request<Sbom>(sbomPath(cluster, w), signal === undefined ? {} : { signal });
  } catch (error) {
    throw said(error);
  }
  if (typeof body.source !== 'object' || !Array.isArray(body.components) || !Array.isArray(body.licenses)) {
    throw new EnvelopeError('Security Center answered an SBOM in a shape this page does not know', body);
  }
  return body;
}

/** Read only while the SBOM tab is open; no interval. */
export function sbomQuery(cluster: string, w: WorkloadRef) {
  return queryOptions({
    queryKey: [KEY, 'sbom', cluster, w.namespace, w.kind, w.name] as const,
    queryFn: ({ signal }) => getSbom(cluster, w, signal),
    staleTime: STALE_MS,
    gcTime: KEEP_MS,
    refetchOnWindowFocus: false,
  });
}

/** No SbomReport for any of the workload's containers: absent, not an SBOM of 0 components. */
export const sbomAbsent = (s: Sbom): boolean => s.images.every((i) => i.absent === true);

/** The licenses shown in the bar. */
export const TOP_LICENSES = 10;

export interface ComponentFilters {
  q: string;
  /** A license name; '' for any. A component with several matches any of them. */
  license: string | null;
}

/** The components the filters keep, in the server's order. `license` null is no filter; '' a component that names none. */
export function filterComponents(all: readonly Component[], f: ComponentFilters): Component[] {
  const q = f.q.trim().toLowerCase();
  if (q === '' && f.license === null) return [...all];
  return all.filter(
    (c) =>
      (f.license === null || (f.license === '' ? c.license === '' : c.license.split(', ').includes(f.license))) &&
      (q === '' ||
        c.name.toLowerCase().includes(q) ||
        c.version.toLowerCase().includes(q) ||
        c.type.toLowerCase().includes(q) ||
        c.license.toLowerCase().includes(q) ||
        (c.purl ?? '').toLowerCase().includes(q)),
  );
}

/** The cell's word: Pass, Fail, Skipped, Unreadable, or — where the admission said nothing. */
export function verdictMark(s: Signature | null | undefined): string {
  switch (s?.verdict) {
    case 'pass':
      return 'Pass';
    case 'fail':
      return 'Fail';
    case 'skip':
      return 'Skipped';
    case 'unreadable':
      return 'Unreadable';
    default:
      return '—';
  }
}

/** Image by image, for a `title`. */
export function verdictTitle(s: Signature | null | undefined): string {
  if (s === null || s === undefined) return 'No admission verdict';
  const lines = s.images.map((i) => `${i.verdict}  ${i.image}`);
  if ((s.unreadable ?? 0) > 0) lines.push(`${n(s.unreadable ?? 0)} pod annotations unreadable`);
  return lines.join('\n');
}

export function componentsTitle(c: Components): string {
  return [`${n(c.dependencies)} dependencies`, ...c.images.map((i) => `${i.container}  ${n(i.components)}  ${i.image}`)].join('\n');
}
