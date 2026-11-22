import { describe, expect, it } from 'vitest';

import { groupName, groupRules, summarize, verbRisk } from './rulesModel';

describe('verbRisk', () => {
  it('leaves reading neutral, warns on changing, and marks * and escalation red', () => {
    expect(['get', 'list', 'watch'].map(verbRisk)).toEqual(['neutral', 'neutral', 'neutral']);
    expect(['create', 'update', 'patch', 'delete', 'deletecollection'].map(verbRisk)).toEqual([
      'warn',
      'warn',
      'warn',
      'warn',
      'warn',
    ]);
    expect(['*', 'escalate', 'bind', 'impersonate'].map(verbRisk)).toEqual(['bad', 'bad', 'bad', 'bad']);
  });

  it('does not invent a tone for a verb it does not know', () => {
    expect(verbRisk('use')).toBe('neutral');
    expect(verbRisk('approve')).toBe('neutral');
  });
});

describe('groupName', () => {
  it('calls the empty group core and leaves the rest alone', () => {
    expect(groupName('')).toBe('core');
    expect(groupName('apps')).toBe('apps');
    expect(groupName('*')).toBe('*');
  });
});

describe('groupRules', () => {
  it('joins rules that differ only in resources, in the order first met', () => {
    const lines = groupRules([
      { apiGroups: [''], resources: ['pods'], verbs: ['get', 'list'] },
      { apiGroups: ['apps'], resources: ['deployments'], verbs: ['get', 'list'] },
      { apiGroups: [''], resources: ['services', 'pods'], verbs: ['get', 'list'] },
    ]);
    expect(lines.length).toBe(2);
    expect(lines[0]?.resources).toEqual(['pods', 'services']);
    expect(lines[0]?.rules).toBe(2);
    expect(lines[1]?.resources).toEqual(['deployments']);
  });

  it('keeps a rule that names objects or URLs apart from one that does not', () => {
    const lines = groupRules([
      { apiGroups: [''], resources: ['secrets'], verbs: ['get'] },
      { apiGroups: [''], resources: ['secrets'], verbs: ['get'], resourceNames: ['one'] },
      { nonResourceURLs: ['/healthz'], verbs: ['get'] },
      { nonResourceURLs: ['/metrics'], verbs: ['get'] },
    ]);
    expect(lines.length).toBe(4);
  });

  it('does not mutate its input', () => {
    const rules = [
      { apiGroups: [''], resources: ['pods'], verbs: ['get'] },
      { apiGroups: [''], resources: ['services'], verbs: ['get'] },
    ];
    groupRules(rules);
    expect(rules[0]?.resources).toEqual(['pods']);
  });
});

describe('summarize', () => {
  it('counts the stored rules and finds a * in any of the three lists', () => {
    expect(summarize([{ apiGroups: ['*'], resources: ['pods'], verbs: ['get'] }]).wildcard).toBe(true);
    expect(summarize([{ apiGroups: [''], resources: ['*'], verbs: ['get'] }]).wildcard).toBe(true);
    expect(summarize([{ apiGroups: [''], resources: ['pods'], verbs: ['*'] }]).wildcard).toBe(true);
    const plain = summarize([
      { apiGroups: [''], resources: ['pods'], verbs: ['get'] },
      { apiGroups: [''], resources: ['pods'], verbs: ['list'] },
    ]);
    expect(plain).toEqual({ count: 2, wildcard: false, escalation: [] });
  });

  it('names the escalation verbs present, in a fixed order', () => {
    const found = summarize([
      { verbs: ['impersonate'] },
      { verbs: ['bind', 'get'] },
    ]);
    expect(found.escalation).toEqual(['bind', 'impersonate']);
  });
});
