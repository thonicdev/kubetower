import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  acquireHandle,
  closeHandle,
  liveHandleKeys,
  releaseHandle,
  resetLiveHandles,
} from './handles';

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  resetLiveHandles();
  vi.useRealTimers();
});

function pty(opened: string[], disposed: string[], name: string) {
  return () => {
    opened.push(name);
    return { value: { name }, dispose: () => disposed.push(name) };
  };
}

describe('live handles', () => {
  it('opens one PTY across StrictMode mount, unmount, remount', () => {
    const opened: string[] = [];
    const disposed: string[] = [];

    // Exactly what React 19 does to an effect in development.
    acquireHandle('pty:prod/pod-a', pty(opened, disposed, 'a'));
    releaseHandle('pty:prod/pod-a');
    acquireHandle('pty:prod/pod-a', pty(opened, disposed, 'a'));

    expect(opened).toEqual(['a']);
    expect(disposed).toEqual([]);

    // And the grace period passing changes nothing while it is still held.
    vi.advanceTimersByTime(10_000);
    expect(disposed).toEqual([]);
  });

  it('closes once the last holder has let go and the grace period passes', () => {
    const opened: string[] = [];
    const disposed: string[] = [];

    acquireHandle('pty:a', pty(opened, disposed, 'a'));
    acquireHandle('pty:a', pty(opened, disposed, 'a'));
    releaseHandle('pty:a');

    vi.advanceTimersByTime(10_000);
    expect(disposed).toEqual([]); // one holder left

    releaseHandle('pty:a');
    expect(disposed).toEqual([]); // still inside the grace period
    vi.advanceTimersByTime(10_000);
    expect(disposed).toEqual(['a']);
    expect(liveHandleKeys()).toEqual([]);
  });

  it('keys by what it connects to, so two dock tabs on one pod share a PTY', () => {
    const opened: string[] = [];
    const disposed: string[] = [];

    const first = acquireHandle('pty:prod/pod-a', pty(opened, disposed, 'a'));
    const second = acquireHandle('pty:prod/pod-a', pty(opened, disposed, 'a'));

    expect(first).toBe(second);
    expect(opened).toEqual(['a']);
  });

  it('keeps two clusters apart', () => {
    const opened: string[] = [];
    const disposed: string[] = [];

    acquireHandle('pty:prod/pod-a', pty(opened, disposed, 'prod'));
    acquireHandle('pty:staging/pod-a', pty(opened, disposed, 'staging'));

    expect(opened).toEqual(['prod', 'staging']);
    expect(liveHandleKeys()).toEqual(['pty:prod/pod-a', 'pty:staging/pod-a']);
  });

  it('closes now when the operator shuts a terminal', () => {
    const opened: string[] = [];
    const disposed: string[] = [];

    acquireHandle('pty:a', pty(opened, disposed, 'a'));
    closeHandle('pty:a');

    expect(disposed).toEqual(['a']);
    expect(liveHandleKeys()).toEqual([]);
  });
});
