import { afterEach, describe, expect, it } from 'vitest';

import {
  forgetOpen,
  lastKind,
  lastOpen,
  lastSort,
  rememberKind,
  rememberOpen,
  rememberSort,
  resetViewMemory,
  viewMemory,
} from './viewMemory';

afterEach(() => {
  resetViewMemory();
});

/**
 * Two memories, one store: *where a person was, per cluster* and *the sort a
 * kind was left with*.
 */
describe('what was I looking at', () => {
  it('remembers a kind per cluster, and keeps them apart', () => {
    rememberKind('prod', 'pods');
    rememberKind('staging', 'deployments');

    expect(lastKind('prod')).toBe('pods');
    expect(lastKind('staging')).toBe('deployments');
    // A cluster nobody has opened has no answer, and that is not an error: it
    // is what makes a rail tile land on the overview the first time.
    expect(lastKind('edge')).toBeUndefined();
  });

  it('keys a sort by kind, so it is not carried to a table without that column', () => {
    rememberSort('prod', 'pods', { key: 'restarts', direction: 'desc' });

    expect(lastSort('prod', 'pods')).toEqual({ key: 'restarts', direction: 'desc' });
    // The rule the old behavior was right about: a sort belongs to one kind's
    // columns, and `restarts` is not on the Services table. Keying per kind is
    // what keeps that true while still remembering the answer.
    expect(lastSort('prod', 'services')).toBeUndefined();
    // And the same kind in another cluster is another question.
    expect(lastSort('staging', 'pods')).toBeUndefined();
  });

  it('forgets a sort that was cleared, rather than keeping the last one', () => {
    rememberSort('prod', 'pods', { key: 'restarts', direction: 'desc' });
    rememberSort('prod', 'pods', undefined);

    // Unsorted is a state somebody chose. Keeping the previous sort would make
    // the clear control do nothing on the next visit.
    expect(lastSort('prod', 'pods')).toBeUndefined();
  });

  it('keeps the kind and the sorts of one cluster in one entry', () => {
    rememberKind('prod', 'pods');
    rememberSort('prod', 'pods', { key: 'age', direction: 'asc' });

    // Writing one must not drop the other: they share an entry, and a naive
    // spread would replace it.
    expect(lastKind('prod')).toBe('pods');
    expect(lastSort('prod', 'pods')).toEqual({ key: 'age', direction: 'asc' });
  });
});

/**
 * The drawer that was open on a cluster, so that the tile returns to it.
 *
 * The store only holds and forgets; which close path forgets and which keeps is
 * the Shell's wiring and is asserted at application altitude in
 * `pages/object/ObjectDrawer.test.tsx` ("what the drawer remembers across a
 * cluster switch"). What is pinned here is the shape: one object per cluster,
 * beside the kind, and gone the moment the cluster is looked at through
 * another kind.
 */
describe('which object was open', () => {
  it('remembers one object per cluster, beside the kind', () => {
    rememberKind('prod', 'pods');
    rememberOpen('prod', { kind: 'pods', namespace: 'shop', name: 'web-1' });
    rememberOpen('staging', { kind: 'nodes', name: 'worker-2' });

    expect(lastOpen('prod')).toEqual({ kind: 'pods', namespace: 'shop', name: 'web-1' });
    // A cluster-scoped object carries no namespace, and absent is not the
    // empty string - the two produce different addresses.
    expect(lastOpen('staging')).toEqual({ kind: 'nodes', name: 'worker-2' });
    expect(lastOpen('edge')).toBeUndefined();
    // Writing the object did not drop the kind it sits beside.
    expect(lastKind('prod')).toBe('pods');
  });

  it('forgets it on request, and only for that cluster', () => {
    rememberOpen('prod', { kind: 'pods', namespace: 'shop', name: 'web-1' });
    rememberOpen('staging', { kind: 'pods', namespace: 'shop', name: 'web-1' });
    rememberSort('prod', 'pods', { key: 'age', direction: 'asc' });

    forgetOpen('prod');

    expect(lastOpen('prod')).toBeUndefined();
    expect(lastOpen('staging')).toEqual({ kind: 'pods', namespace: 'shop', name: 'web-1' });
    // Forgetting the drawer is not forgetting the page: the sort stays.
    expect(lastSort('prod', 'pods')).toEqual({ key: 'age', direction: 'asc' });
  });

  it('leaves the object behind when the cluster is looked at through another kind', () => {
    // The tile returns to one page - the kind - and a drawer belongs to the
    // list it was opened beside. An operator who left Pods for Deployments
    // left the pod's drawer on Pods; the tile that returns to Deployments
    // must not carry a pod with it.
    rememberKind('prod', 'pods');
    rememberOpen('prod', { kind: 'pods', namespace: 'shop', name: 'web-1' });

    rememberKind('prod', 'deployments');

    expect(lastKind('prod')).toBe('deployments');
    expect(lastOpen('prod')).toBeUndefined();
  });

  it('keeps the object when the same kind is remembered again', () => {
    // The return through the tile lands on the same kind, and that arrival
    // re-remembers it. Dropping the object there would drop it on exactly the
    // navigation that exists to bring it back.
    rememberKind('prod', 'pods');
    rememberOpen('prod', { kind: 'pods', namespace: 'shop', name: 'web-1' });

    rememberKind('prod', 'pods');

    expect(lastOpen('prod')).toEqual({ kind: 'pods', namespace: 'shop', name: 'web-1' });
  });

  it('survives a re-read from storage', () => {
    rememberOpen('prod', { kind: 'pods', namespace: 'shop', name: 'web-1' });
    const stored = localStorage.getItem('kubetower.view');
    resetViewMemory();
    if (stored !== null) localStorage.setItem('kubetower.view', stored);

    expect(lastOpen('prod')).toEqual({ kind: 'pods', namespace: 'shop', name: 'web-1' });
  });
});

/**
 * What is in `localStorage` was written by a build that may be older than this
 * one, by a user who can edit it. A shape that does not match has to read as
 * nothing rather than crash the first render of the shell.
 */
describe('what is stored is not trusted', () => {
  it('reads a garbled entry as nothing at all', () => {
    localStorage.setItem('kubetower.view', 'not json');
    resetViewMemory();
    localStorage.setItem('kubetower.view', 'not json');

    expect(viewMemory()).toEqual({});
    expect(lastKind('prod')).toBeUndefined();
  });

  it('drops the members it cannot recognize and keeps the ones it can', () => {
    localStorage.setItem(
      'kubetower.view',
      JSON.stringify({
        prod: {
          kind: 'pods',
          sorts: {
            // Good.
            pods: { key: 'restarts', direction: 'desc' },
            // A direction nothing can mean.
            nodes: { key: 'cpu', direction: 'sideways' },
            // No key.
            services: { direction: 'asc' },
          },
        },
        // A cluster whose whole entry is the wrong shape.
        staging: 'everything',
        // An open object with no name, and one whose namespace is not a string.
        edge: { kind: 'pods', open: { kind: 'pods', namespace: 'shop' } },
        lab: { kind: 'pods', open: { kind: 'pods', namespace: 7, name: 'web-1' } },
      }),
    );
    resetViewMemory();
    localStorage.setItem(
      'kubetower.view',
      JSON.stringify({
        prod: {
          kind: 'pods',
          sorts: {
            pods: { key: 'restarts', direction: 'desc' },
            nodes: { key: 'cpu', direction: 'sideways' },
            services: { direction: 'asc' },
          },
        },
        staging: 'everything',
        edge: { kind: 'pods', open: { kind: 'pods', namespace: 'shop' } },
        lab: { kind: 'pods', open: { kind: 'pods', namespace: 7, name: 'web-1' } },
      }),
    );

    expect(lastKind('prod')).toBe('pods');
    expect(lastSort('prod', 'pods')).toEqual({ key: 'restarts', direction: 'desc' });
    // Neither of the two malformed sorts survives, and neither takes the good
    // one with it.
    expect(lastSort('prod', 'nodes')).toBeUndefined();
    expect(lastSort('prod', 'services')).toBeUndefined();
    expect(lastKind('staging')).toBeUndefined();
    // A malformed open object is dropped and takes nothing else with it.
    expect(lastOpen('edge')).toBeUndefined();
    expect(lastKind('edge')).toBe('pods');
    expect(lastOpen('lab')).toBeUndefined();
  });
});
