import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Capabilities, PodContainer } from '../api';
import { debugOn, debugRequest, debugTargets, probablyNoShell, shortImage, validImage } from './debug';
import { knownNoShell, rememberNoShell } from './noShellMemory';

/**
 * The rules every way into a debug container shares: which image probably has
 * no shell, which containers one may join, when it is offered, and the tab a
 * Start opens.
 */

describe('an image that probably has no shell, read from its reference alone', () => {
  it.each([
    'gcr.io/distroless/static-debian12:nonroot',
    'gcr.io/distroless/base',
    'gcr.io/distroless/java21-debian12@sha256:0123456789abcdef',
    'cgr.dev/chainguard/static:latest',
    'registry.example.com/chainguard/nginx:1.27',
    'scratch',
    'docker.io/library/scratch:latest',
  ])('%s', (image) => {
    expect(probablyNoShell(image)).toBe(true);
  });

  it.each([
    // The variants of the same families that carry a shell.
    'gcr.io/distroless/base:debug',
    'gcr.io/distroless/static-debian12:debug-nonroot',
    'cgr.dev/chainguard/static:latest-dev',
    'cgr.dev/chainguard/python:3.12-dev',
    // Ordinary images.
    'docker.io/library/nginx:1.27.2',
    'busybox:1.36',
    'localhost:5000/scratchpad:1',
    '',
  ])('not %s', (image) => {
    expect(probablyNoShell(image)).toBe(false);
  });

  it('asks nothing of an image it was not given', () => {
    expect(probablyNoShell(undefined)).toBe(false);
  });
});

describe('a free image reference', () => {
  it('is not empty and holds no space', () => {
    expect(validImage('alpine:3.20')).toBe(true);
    expect(validImage('')).toBe(false);
    expect(validImage('alpine 3.20')).toBe(false);
    expect(validImage('alpine:3.20\t')).toBe(false);
  });

  it('names a tab by its own short name', () => {
    expect(shortImage('nicolaka/netshoot:v0.13')).toBe('netshoot');
    expect(shortImage('busybox:1.36')).toBe('busybox');
    expect(shortImage('localhost:5000/tools/kit@sha256:abc')).toBe('kit');
  });
});

describe('the containers a debug container may join', () => {
  const running = { state: 'running', ready: true } as NonNullable<PodContainer['status']>;
  it('are the running app containers, never an init or an ephemeral one', () => {
    const containers: PodContainer[] = [
      { name: 'wait', image: 'busybox', init: true, status: running },
      { name: 'api', image: 'gcr.io/distroless/static', init: false, status: running },
      { name: 'crashing', image: 'x', init: false, status: { state: 'waiting', ready: false, reason: 'CrashLoopBackOff' } },
      { name: 'kt-debug-abcde', image: 'busybox:1.36', init: false, ephemeral: true, target: 'api', status: running },
    ];
    expect(debugTargets(containers).map((one) => one.name)).toEqual(['api']);
    expect(debugTargets(undefined)).toEqual([]);
  });
});

describe('whether Debug is drawn', () => {
  const desktop = { mode: 'desktop', podExec: true } as Capabilities;
  it('follows the page on both profiles, behind podExec', () => {
    expect(debugOn(desktop, { permitted: true }, true)).toEqual({ drawn: true, refused: undefined });
    expect(debugOn({ ...desktop, mode: 'shared' } as Capabilities, { permitted: true }, true).drawn).toBe(true);
    // Withheld: no offer on the page.
    expect(debugOn(desktop, undefined, true).drawn).toBe(false);
    // Not before the page has answered.
    expect(debugOn(desktop, { permitted: true }, false).drawn).toBe(false);
    // A build that cannot open an exec has nothing to open a debug shell with.
    expect(debugOn({ ...desktop, podExec: false } as Capabilities, { permitted: true }, true).drawn).toBe(false);
  });

  it('is grayed with the server\'s sentence where it is refused', () => {
    const refused = debugOn(
      desktop,
      { permitted: false, reason: { code: 'verb_denied', message: 'prod is marked read-only' } },
      true,
    );
    expect(refused).toEqual({ drawn: true, refused: 'prod is marked read-only' });
  });
});

describe('the tab a Start opens', () => {
  it('is a debug tab on the pod, with the image and the target', () => {
    expect(debugRequest({ cluster: 'prod', namespace: 'pay', pod: 'api-0' }, { image: 'nicolaka/netshoot:v0.13', target: 'api' })).toEqual({
      kind: 'debug',
      cluster: 'prod',
      label: 'debug · api-0 · netshoot',
      namespace: 'pay',
      name: 'api-0',
      image: 'nicolaka/netshoot:v0.13',
      target: 'api',
    });
    expect(debugRequest({ cluster: 'prod', namespace: 'pay', pod: 'api-0' }, { image: 'busybox:1.36' })).not.toHaveProperty('target');
  });
});

describe('what a Shell learned about a container', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    sessionStorage.clear();
  });

  const fact = { cluster: 'prod', namespace: 'pay', pod: 'api-0', container: 'api', image: 'shop/api:1' };

  it('is kept for the session, per image', () => {
    expect(knownNoShell(fact)).toBe(false);
    rememberNoShell(fact);
    expect(knownNoShell(fact)).toBe(true);
    // The same container with another image is another fact.
    expect(knownNoShell({ ...fact, image: 'shop/api:2' })).toBe(false);
  });

  it('is kept for the page where storage refuses it', () => {
    vi.stubGlobal('sessionStorage', {
      getItem: () => {
        throw new Error('blocked');
      },
      setItem: () => {
        throw new Error('blocked');
      },
    });
    const other = { ...fact, pod: 'api-1' };
    expect(knownNoShell(other)).toBe(false);
    rememberNoShell(other);
    expect(knownNoShell(other)).toBe(true);
  });
});
