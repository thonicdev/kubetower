import { afterEach, describe, expect, it, vi } from 'vitest';

import { managedHere } from './argocd/tracked';
import { extras } from './index';

/**
 * An extra's tab is drawn only on an object the extra concerns. A tab offered on every kind cannot know that from the kind, so it
 * must ask (`probe`); a label anything can set is not an answer.
 */

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('every extra', () => {
  it.each(extras.flatMap((extra) => (extra.tabs ?? []).map((tab) => [`${extra.key}.${tab.key}`, tab] as const)))(
    '%s asks before it is drawn on every kind',
    (_, tab) => {
      if (tab.kinds.includes('*')) expect(tab.probe).toBeTypeOf('function');
    },
  );
});

describe('the Argo CD tab', () => {
  function answer(body: unknown, status = 200): void {
    vi.stubGlobal(
      'fetch',
      vi.fn(() =>
        Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })),
      ),
    );
  }
  const object = { cluster: 'kind', kind: 'configmaps', namespace: 'web', name: 'labelled' };
  const signal = new AbortController().signal;

  it('is drawn where an Application manages the object', async () => {
    answer({ kind: 'ConfigMap', name: 'labelled', managed: true, via: 'label', copied: false });
    await expect(managedHere(object, signal)).resolves.toBe(true);
  });

  it('is not drawn on an instance label no Application lists, nor on a copied annotation', async () => {
    answer({ kind: 'ConfigMap', name: 'labelled', managed: false, copied: false, note: 'no Application lists it' });
    await expect(managedHere(object, signal)).resolves.toBe(false);
    answer({ kind: 'ConfigMap', name: 'labelled', managed: false, copied: true });
    await expect(managedHere(object, signal)).resolves.toBe(false);
  });

  // A ReplicaSet carries its Deployment's mark, and the server answers through
  // the owner: the tab stays, drawing the owner's answer.
  it('is drawn where the answer comes through the owner Argo CD manages', async () => {
    answer({ kind: 'Deployment', name: 'web', managed: true, of: { kind: 'ReplicaSet', name: 'web-7c9' }, copied: false });
    await expect(managedHere(object, signal)).resolves.toBe(true);
  });

  it('is not drawn when Argo CD could not be asked', async () => {
    answer({ status: 503, title: 'unavailable' }, 503);
    await expect(managedHere(object, signal)).rejects.toBeDefined();
  });
});
