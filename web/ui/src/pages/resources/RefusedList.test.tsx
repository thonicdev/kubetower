import { cleanup, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../../keyboard/stack';
import { resetMenuMode, resetOpenGroups } from '../../shell/menuMemory';
import { resetNavMemory } from '../../shell/navMemory';
import { resetViewMemory } from '../../shell/viewMemory';
import { PANEL_BOX } from '../../state/StateNotice';
import { mountApp, stubFetch } from '../../testing/harness';

/**
 * A list whose read the cluster refused, at application altitude: the menu is
 * what knows the kind's name when the answer carries none, so the page is
 * mounted with it.
 */

function forget(): void {
  resetKeyboardStack();
  resetOpenGroups();
  resetMenuMode();
  resetNavMemory();
  resetViewMemory();
  localStorage.clear();
}

beforeEach(forget);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  forget();
});

function refused(): void {
  stubFetch({
    '/api/session': {
      body: { identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' },
    },
    '/api/clusters': { body: { state: 'ok', items: [{ name: 'prod', label: 'prod', state: 'ok' }] } },
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    '/api/c/prod/nav': {
      body: {
        state: 'ok',
        data: {
          groups: [
            {
              key: 'cluster',
              label: 'Cluster',
              items: [
                { text: 'Nodes', href: '/c/prod/nodes' },
                { text: 'Service CIDRs', href: '/c/prod/servicecidrs.networking.k8s.io' },
              ],
            },
          ],
        },
      },
    },
    '/api/c/prod/r/nodes': {
      body: {
        state: 'forbidden',
        reason: {
          code: 'verb_denied',
          message: 'nodes is forbidden for this account.',
          scope: { cluster: 'prod', resource: 'nodes', verb: 'list' },
        },
      },
    },
  });
}

function heading(): string | null | undefined {
  return document.querySelector('h1')?.textContent;
}

describe('a refused list', () => {
  it('is titled with the kind’s name, in the heading and the breadcrumb', async () => {
    refused();
    mountApp('/c/prod/nodes');

    await waitFor(() => {
      expect(document.querySelector('[data-state="forbidden"]')).not.toBeNull();
    });
    await waitFor(() => {
      expect(heading()).toBe('Nodes');
    });
    const crumbs = document.querySelector('nav[aria-label="Breadcrumb"]');
    expect(crumbs?.textContent).toContain('Nodes');
    expect(crumbs?.textContent).not.toContain('nodes');
  });

  it('keeps the address’s segment where the menu has no row for the kind', async () => {
    refused();
    mountApp('/c/prod/widgets.example.io');

    await waitFor(() => {
      expect(document.querySelector('#panel [data-box]')).not.toBeNull();
    });
    expect(heading()).toBe('widgets.example.io');
  });

  it('insets both of its boxed notices with the panel’s one margin', async () => {
    refused();
    mountApp('/c/prod/nodes');

    await waitFor(() => {
      expect(document.querySelector('[data-remedy="forbidden"]')).not.toBeNull();
    });
    const panel = document.getElementById('panel');
    if (panel === null) throw new Error('the list has no panel');
    expect(panel.className).toContain(PANEL_BOX);
    // `PANEL_BOX` reaches the panel's boxed children by `data-box`: the notice
    // and its remedy both carry it, so neither runs edge to edge.
    const notice = document.querySelector('[data-state="forbidden"]');
    const remedy = document.querySelector('[data-remedy="forbidden"]');
    expect(notice?.parentElement).toBe(panel);
    expect(remedy?.parentElement).toBe(panel);
    expect(notice?.hasAttribute('data-box')).toBe(true);
    expect(remedy?.hasAttribute('data-box')).toBe(true);
  });
});
