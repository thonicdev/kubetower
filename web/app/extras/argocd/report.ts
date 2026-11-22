import { queryOptions } from '@tanstack/react-query';
import { request, type ApiError } from '@kubetower/ui';

import { refusalOf, said, sentenceOf, subjectOf, type Problem, type Refusal } from './data';

/**
 * What `GET /x/{cluster}/argocd/report` answers (`overview.go`, `report/`): the
 * whole Delivery Center, every sentence decided on the Go side.
 */

export interface Tile {
  label: string;
  key: string;
  count: number;
}

export interface AreaView {
  name: string;
  kind: string;
  unavailable?: string;
  count: number;
  summary?: string;
  note?: string;
}

export interface Since {
  of: string;
  value: string;
  hint: string;
  missing: boolean;
}

export interface ConditionView {
  type: string;
  error: boolean;
  message?: string;
}

export interface Row {
  namespace: string;
  name: string;
  project?: string;
  projectNamespace?: string;
  projectMessage?: string;
  sync: string;
  syncKey: string;
  health: string;
  healthKey: string;
  healthMessage?: string;
  deleting?: string;
  since: Since[];
  source: string;
  sourceHint?: string;
  revision: string;
  operation: string;
  operationTone: 'ok' | 'bad' | 'muted' | string;
  operationMessage?: string;
  deployed?: string;
  deployedHint?: string;
  conditions: ConditionView[];
  conditionsNote?: string;
  reason: string;
}

export interface CauseRow {
  type: string;
  error: boolean;
  message: string;
  failing: number;
  objects: { namespace: string; name: string }[];
  objectsNote?: string;
}

export interface SetRow {
  namespace: string;
  name: string;
  type: string;
  status: string;
  message: string;
}

export interface ProjectRow {
  namespace: string;
  name: string;
  description?: string;
  applications: number;
  offending: number;
}

export interface View {
  collected: string;
  state: 'absent' | 'pending' | 'present' | 'unreadable';
  detail?: string;
  where?: string;
  whereUnavailable?: string;
  install?: string;
  summary?: string;
  headline?: string;
  headlineKey?: string;
  allWell: boolean;
  sync: Tile[];
  health: Tile[];
  areas: AreaView[];
  offendersUnavailable?: string;
  offenders: Row[];
  offendersNote?: string;
  causes: CauseRow[];
  causesNote?: string;
  setsUnavailable?: string;
  sets: SetRow[];
  setsNote?: string;
  setTotal: number;
  setFailing: number;
  projectsUnavailable?: string;
  projects: ProjectRow[];
  projectsNote?: string;
  projectTotal: number;
  notes: string[];
}

export type ReportAnswer =
  | { view: View; refusal?: undefined; subject?: undefined }
  | { view?: undefined; refusal: Refusal; message: string; subject: string | undefined };

export async function getReport(cluster: string, signal?: AbortSignal): Promise<ReportAnswer> {
  try {
    const view = await request<View>(`/x/${encodeURIComponent(cluster)}/argocd/report`, signal === undefined ? {} : { signal });
    return { view };
  } catch (error) {
    const refusal = refusalOf(error);
    if (refusal !== undefined) {
      const problem = (error as ApiError).body as Problem | undefined;
      const message = sentenceOf(problem, refusal === 'cluster_unknown') ?? '';
      return { refusal, message, subject: subjectOf(problem) };
    }
    throw said(error);
  }
}

export function reportQuery(cluster: string) {
  return queryOptions({
    queryKey: ['argocd', 'report', cluster] as const,
    queryFn: ({ signal }) => getReport(cluster, signal),
  });
}
