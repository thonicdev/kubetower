import { ApiError, NotJsonError } from '@kubetower/ui';
import { describe, expect, it } from 'vitest';

import { gateUnknownCluster } from './gate';

const SENTENCE = 'There is no cluster called "gone" in the kubeconfig this KubeTower reads.';

describe('gateUnknownCluster', () => {
  it("reads the gate's 404 not_found as an unknown cluster, with its sentence", () => {
    const error = new ApiError(404, SENTENCE, { title: 'Not found', status: 404, code: 'not_found', detail: SENTENCE });
    expect(gateUnknownCluster(error)).toBe(SENTENCE);
  });

  it('reads nothing else as one', () => {
    expect(gateUnknownCluster(new ApiError(404, 'x', { code: 'cluster_unknown', message: SENTENCE }))).toBeUndefined();
    expect(gateUnknownCluster(new ApiError(404, 'x', { code: 'not_found' }))).toBeUndefined();
    expect(gateUnknownCluster(new ApiError(403, 'x', { code: 'not_found', detail: SENTENCE }))).toBeUndefined();
    expect(gateUnknownCluster(new NotJsonError(404, 'text/plain', SENTENCE))).toBeUndefined();
    expect(gateUnknownCluster(new Error(SENTENCE))).toBeUndefined();
  });
});
