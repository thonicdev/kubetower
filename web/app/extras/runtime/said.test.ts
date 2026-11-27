import { ApiError } from '@kubetower/ui';
import { describe, expect, it } from 'vitest';

import { said } from './data';

describe("the route's refusal", () => {
  it('is drawn on one line, whatever the route or the cluster wrapped (#157)', () => {
    const refused = new ApiError(403, 'Forbidden', {
      code: 'exec_forbidden',
      message: 'You may not run commands in this pod:\n  pods/exec is not granted',
      detail: 'pods "billing-0" is forbidden:\r\n\tUser "frank" cannot create resource "pods/exec"',
    });
    const shown = said(refused);
    expect(shown).toBeInstanceOf(ApiError);
    expect((shown as ApiError).message).toBe(
      'You may not run commands in this pod: pods/exec is not granted pods "billing-0" is forbidden: User "frank" cannot create resource "pods/exec"',
    );
    expect((shown as ApiError).status).toBe(403);
  });
});
