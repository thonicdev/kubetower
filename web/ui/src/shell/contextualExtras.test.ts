import { describe, expect, it } from 'vitest';

import type { NavGroup } from '../api';
import type { Extension } from '../extension';
import { withoutContextual } from './contextualExtras';

const extra = (...keys: string[]): NavGroup =>
  ({ key: 'extra', label: 'Extra', deferred: false, items: keys.map((key) => ({ text: key, href: `/c/prod/x/${key}` })) }) as NavGroup;
const workloads = { key: 'workloads', label: 'Workloads', deferred: false, items: [{ text: 'Pods', href: '/c/prod/pods' }] } as NavGroup;
const ext = (key: string, menu?: false): Extension => ({ key, nav: { label: key, path: `x/${key}`, ...(menu === undefined ? {} : { menu }) }, routes: [] });

describe('the menu without contextual extras', () => {
  it('drops the entry of an extra that declares no menu entry, and only that one', () => {
    const out = withoutContextual([workloads, extra('argocd', 'pending')], [ext('argocd'), ext('pending', false)]);
    expect(out[0]).toBe(workloads);
    expect(out[1]?.items.map((item) => item.text)).toEqual(['argocd']);
  });

  it('drops the Extra group when nothing is left in it', () => {
    expect(withoutContextual([workloads, extra('pending')], [ext('pending', false)]).map((g) => g.key)).toEqual(['workloads']);
  });

  it('returns the groups as they are when no extra is contextual', () => {
    const groups = [workloads, extra('argocd')];
    expect(withoutContextual(groups, [ext('argocd')])).toBe(groups);
    expect(withoutContextual(groups, undefined)).toBe(groups);
  });
});
