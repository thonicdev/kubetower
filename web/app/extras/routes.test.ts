import { describe, expect, it } from 'vitest';

import { buildRoutes } from '../../ui/src/router/routes';
import { extras } from './index';

/**
 * Every extra this build carries passes the core's `x/<key>` check:
 * `buildRoutes` throws at start-up on a route outside it,
 * so an extra that broke the rule would be a console that never draws. The
 * list is the one `app/main.tsx` hands to `createApp`, imported rather than
 * copied, so a new extra is checked here the day it ships.
 */
describe('the in-tree extras', () => {
  it('keep every route under x/<key>, each under a key of its own', () => {
    expect(extras.length).toBeGreaterThan(0);
    expect(() => buildRoutes(extras)).not.toThrow();
  });
});
