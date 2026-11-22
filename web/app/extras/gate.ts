import { ApiError, NotJsonError } from '@kubetower/ui';

/**
 * The console's sentence for a cluster the kubeconfig does not hold, when the
 * console's gate refused an extra's address for it; `undefined` for any other
 * answer.
 *
 * The gate resolves the cluster before an extra's route runs, and answers a
 * name it cannot resolve as the console's API does (`api.UnknownCluster`,
 * `internal/web/extras.go`): 404, a problem object, `code` `not_found`, the
 * sentence as `detail`. No extra's route writes `not_found`, so at an extra's
 * address that code is the gate's and means this.
 */
export function gateUnknownCluster(error: unknown): string | undefined {
  if (!(error instanceof ApiError) || error instanceof NotJsonError || error.status !== 404) return undefined;
  const body = error.body as { code?: unknown; detail?: unknown } | undefined;
  if (body?.code !== 'not_found' || typeof body.detail !== 'string' || body.detail === '') return undefined;
  return body.detail;
}
