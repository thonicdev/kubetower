import { describe, expect, it } from 'vitest';

import { envFoldIsOpen, envFoldKey, setEnvFoldOpen } from './envFolds';

/**
 * The memory behind a folded variable list. The panel's behavior is in
 * `Containers.test.tsx`; this is the two properties a rendering cannot show:
 * that two lists never share an address, and that the set does not grow for the
 * life of the page.
 */
describe('which variable lists are open', () => {
  const pod = { cluster: 'prod', kind: 'pods', namespace: 'shop', name: 'api-1' };

  it('keeps an init container apart from an app container of the same name', () => {
    const init = envFoldKey(pod, { name: 'setup', init: true }, 'environment');
    const app = envFoldKey(pod, { name: 'setup', init: false }, 'environment');
    setEnvFoldOpen(init, true);

    expect(envFoldIsOpen(init)).toBe(true);
    expect(envFoldIsOpen(app)).toBe(false);
  });

  it('cannot be made to confuse two addresses by a name holding a separator', () => {
    // Joined with `/`, these two would be one string.
    const a = envFoldKey({ ...pod, namespace: 'a/b', name: 'c' }, { name: 'x', init: false }, 'environment');
    const b = envFoldKey({ ...pod, namespace: 'a', name: 'b/c' }, { name: 'x', init: false }, 'environment');
    expect(a).not.toBe(b);
  });

  it('forgets the oldest opened list past its bound', () => {
    const key = (at: number): string =>
      envFoldKey({ ...pod, name: `api-${String(at)}` }, { name: 'app', init: false }, 'environment');
    for (let at = 0; at <= 200; at += 1) setEnvFoldOpen(key(at), true);

    expect(envFoldIsOpen(key(0))).toBe(false);
    expect(envFoldIsOpen(key(1))).toBe(true);
    expect(envFoldIsOpen(key(200))).toBe(true);
  });
});
