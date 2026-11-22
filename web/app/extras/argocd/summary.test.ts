import { describe, expect, it } from 'vitest';

import type { Application } from './data';
import { needsAttention, whyOf } from './DeliveryCenter';
import { ago, shortRevision, sourceLine } from './summary';

const app = (over: Partial<Application>): Application => ({
  namespace: 'argocd',
  name: 'guestbook',
  sync: 'Synced',
  health: 'Healthy',
  deleting: false,
  ...over,
});

describe("an Application tile's facts", () => {
  it('names a git source by its last two path segments, the path and the revision asked for', () => {
    expect(
      sourceLine(app({ source: { repoURL: 'https://github.com/argoproj/argocd-example-apps.git', path: 'guestbook', targetRevision: 'HEAD' } }))
        ?.text,
    ).toBe('argoproj/argocd-example-apps/guestbook @ HEAD');
    expect(sourceLine(app({ source: { repoURL: 'git@github.com:acme/infra.git', path: '.' } }))?.text).toBe('acme/infra');
  });

  it('names a chart by itself and its version, and counts the other sources', () => {
    const multi = app({ source: { repoURL: 'https://charts.example.com', chart: 'web', targetRevision: '1.2.3' }, sources: 3 });
    expect(sourceLine(multi)).toEqual({
      text: 'web 1.2.3 +2',
      title: 'https://charts.example.com · web · 1.2.3 (and 2 more)',
    });
    expect(sourceLine(app({}))).toBeUndefined();
  });

  it('shortens a git sha and leaves a chart version alone', () => {
    expect(shortRevision('8088f4c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7')).toBe('8088f4c');
    expect(shortRevision('6.7.1')).toBe('6.7.1');
  });

  it('says how long ago in the largest whole unit', () => {
    const now = Date.parse('2026-10-24T12:00:00Z');
    expect(ago('2026-10-24T11:59:30Z', now)).toBe('30s ago');
    expect(ago('2026-10-24T11:55:00Z', now)).toBe('5m ago');
    expect(ago('2026-10-24T09:00:00Z', now)).toBe('3h ago');
    expect(ago('2026-10-20T12:00:00Z', now)).toBe('4d ago');
    expect(ago('not a date', now)).toBe('');
  });
});

describe('needs attention, and why', () => {
  it('takes the warn and bad tiles only', () => {
    expect(needsAttention(app({ sync: 'OutOfSync' }))).toBe(true);
    expect(needsAttention(app({ health: 'Degraded' }))).toBe(true);
    expect(needsAttention(app({ deleting: true }))).toBe(true);
    expect(needsAttention(app({ health: 'Progressing' }))).toBe(false);
    expect(needsAttention(app({ sync: 'Unknown' }))).toBe(false);
    expect(needsAttention(app({}))).toBe(false);
  });

  it("says why in Argo CD's words: the error, then a failed sync, then the health's reason, then the count", () => {
    const failed = { phase: 'Failed', message: 'one or more objects failed to apply' };
    expect(
      whyOf(app({ condition: { type: 'ComparisonError', message: 'app path does not exist', error: true }, lastSync: failed })),
    ).toBe('ComparisonError: app path does not exist');
    expect(whyOf(app({ lastSync: failed, healthMessage: 'x' }))).toBe('Sync failed: one or more objects failed to apply');
    expect(whyOf(app({ health: 'Degraded', healthMessage: 'Deployment has 0 of 2 pods available' }))).toBe(
      'Deployment has 0 of 2 pods available',
    );
    expect(whyOf(app({ sync: 'OutOfSync', outOfSyncCount: 3 }))).toBe('3 resources differ from git.');
    expect(whyOf(app({}))).toBeUndefined();
  });
});
