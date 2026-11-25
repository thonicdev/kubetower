import { fireEvent, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Extension } from '../../extension';
import { resetKeyboardStack } from '../../keyboard/stack';
import { mountApp, stubFetch, type StubReply, type StubRoutes } from '../../testing/harness';

/**
 * The settings page: its tabs, the archive's controls, the node shell's image,
 * the Infos tab and the Extra tab.
 *
 * Covered here:
 *
 *   - each tab draws its own sections and not the others;
 *   - an unknown tab is the General tab;
 *   - the retention dropdown can always show what is set;
 *   - the Infos tab names the build it is running, as one string;
 *   - Extra draws a switch for every extension, and the switch posts the wanted
 *     state and not the box.
 *
 * `/setup` has no equivalent and needs none: the SPA has never had that address,
 * so a redirect from it would be a route invented to satisfy a test.
 */

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
});

const session = {
  body: {
    identity: 'alice',
    idleExpiresAt: '2999-01-01T00:00:00Z',
    absoluteExpiresAt: '2999-01-01T00:00:00Z',
  },
};

const clusters = {
  body: {
    state: 'ok',
    items: [
      { name: 'prod', label: 'prod', state: 'ok' },
      { name: 'staging', label: 'staging', state: 'ok' },
    ],
  },
};

/**
 * Retention is 21, which is **not** one of the five offered.
 *
 * That is the fixture's whole point. A control that cannot represent its own
 * current state does not merely look wrong: with retention at 21 no option
 * carried `selected`, the browser submitted the first, and a Save opened to tick
 * an unrelated checkbox destroyed twenty days of archived events while saying
 * "Saved."
 */
const settings = {
  body: {
    general: { archiveEnabled: true, captureLogs: false, retentionDays: 21 },
    retentionChoices: [
      { days: 1, label: '1 day' },
      { days: 3, label: '3 days' },
      { days: 7, label: '7 days' },
      { days: 14, label: '14 days' },
      { days: 21, label: '21 days' },
      { days: 30, label: '30 days' },
    ],
    archive: {
      state: 'ok',
      data: {
        path: '/state/archive',
        bytes: 4194304,
        days: 6,
        oldestAt: '2026-09-06T00:00:00Z',
        writesRefused: false,
        note: '',
      },
    },
    nodeShell: {
      enabled: true,
      available: true,
      image: 'busybox:1.36',
      defaultImage: 'alpine:3.24',
    },
    info: {
      statePath: '/state',
      statePersistent: true,
      kubeconfigFiles: ['/kube/config', '/kube/work (not there)'],
      kubeconfigShadowed: [
        {
          context: 'prod',
          keptFrom: '/kube/config',
          shadowedIn: '/kube/work',
          sentence:
            'The context prod is taken from /kube/config; the one in /kube/work is not merged.',
        },
      ],
      listenAddress: '127.0.0.1:5823',
      withoutPassword: true,
      version: 'kubetower 0.7.2 (2026-09-11, windows/amd64)',
      development: false,
      stateWrittenBy: '0.8.0',
      stateIsNewer: true,
      repositoryUrl: 'https://github.com/thonicdev/kubetower',
      documentationUrl: 'https://thonicdev.github.io/kubetower-site/docs/',
      update: {
        state: 'newer',
        latest: '0.8.0',
        releaseUrl: 'https://github.com/thonicdev/kubetower/releases/tag/v0.8.0',
        checkedAt: '2026-09-11T09:00:00Z',
        note: 'KubeTower 0.8.0 has been released. This build is 0.7.2.',
      },
    },
  },
};

function shell(extra: StubRoutes = {}): StubRoutes {
  return {
    '/api/session': session,
    '/api/clusters': clusters,
    '/api/settings': settings,
    '/api/c/prod/extensions': { body: { state: 'empty', items: [] } },
    '/api/c/staging/extensions': { body: { state: 'empty', items: [] } },
    ...extra,
  };
}

function page(): HTMLElement {
  const element = document.getElementById('page');
  if (element === null) throw new Error('#page is missing');
  return element;
}

/** Opens the settings window of a tile of Settings > Extra, from its Edit button. */
async function edit(key: string): Promise<void> {
  const selector = `[data-extra="${key}"] [data-extra-edit]`;
  await waitFor(() => {
    expect(document.querySelector(selector)).not.toBeNull();
  });
  fireEvent.click(document.querySelector(selector) as HTMLElement);
  await waitFor(() => {
    expect(document.querySelector(`[role="dialog"] [data-extra-modal="${key}"]`)).not.toBeNull();
  });
}

/** The open settings window. */
function modal(): HTMLElement {
  const found = document.querySelector<HTMLElement>('[role="dialog"]');
  if (found === null) throw new Error('no window is open');
  return found;
}

describe('each tab draws its own sections and not the others', () => {
  it('draws General', async () => {
    stubFetch(shell());
    mountApp('/settings/general');

    await waitFor(() => {
      expect(page().textContent).toContain('What the archive keeps');
    });
    const text = page().textContent ?? '';
    expect(text).toContain('Node shell');
    // Not the other two.
    expect(text).not.toContain('Kubeconfig');
    expect(page().querySelector('[data-cluster="prod"]')).toBeNull();
  });

  // A build whose listing names no extra draws no Extra tab, and the address it
  // would answer behaves like any other stale bookmark rather than like a dead
  // link. Where there are extras the tab is drawn: see the describe blocks at
  // the end of this file.
  it('sends the Extra address to General where the build has no extra, like any stale bookmark', async () => {
    stubFetch(shell());
    const { router } = mountApp('/settings/extra');

    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/settings/general');
    });
    await waitFor(() => {
      expect(page().textContent).toContain('What the archive keeps');
    });
    // And no tab named for it, so nothing offers the address back.
    expect(page().textContent).not.toContain('Extra');
    expect(page().querySelector('[data-cluster="prod"]')).toBeNull();
  });

  it('draws Infos', async () => {
    stubFetch(shell());
    mountApp('/settings/infos');

    await waitFor(() => {
      expect(page().querySelector('[data-infos]')).not.toBeNull();
    });
    expect(page().textContent).not.toContain('What the archive keeps');
  });
});

describe('a tab is a real address', () => {
  it('sends an unknown tab to General rather than to an empty panel', async () => {
    stubFetch(shell());
    const { router } = mountApp('/settings/nosuchtab');

    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/settings/general');
    });
    // A stale bookmark is a stale bookmark, not an error page: it lands on a
    // working tab rather than on a 404 or an empty panel.
    await waitFor(() => {
      expect(page().textContent).toContain('What the archive keeps');
    });
  });

  it('sends the bare address to General, rather than being a second spelling of it', async () => {
    stubFetch(shell());
    const { router } = mountApp('/settings');

    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/settings/general');
    });
    // Two addresses for one place puts two entries in the history for one
    // navigation, and Back then does nothing visible.
  });

  it('survives a reload on a tab that is not the first', async () => {
    stubFetch(shell());
    mountApp('/settings/infos');

    // Which is what a `useState` tab strip loses, and this is the page somebody
    // is told to open by somebody else.
    await waitFor(() => {
      expect(page().querySelector('[data-infos]')).not.toBeNull();
    });
  });
});

describe('the retention dropdown can always show what is set', () => {
  it('offers the current value even though it is not one of the five', async () => {
    stubFetch(shell());
    mountApp('/settings/general');

    await waitFor(() => {
      expect(page().querySelector('select[name="retentionDays"]')).not.toBeNull();
    });

    const select = page().querySelector<HTMLSelectElement>('select[name="retentionDays"]');
    const values = [...(select?.options ?? [])].map((option) => Number(option.value));
    expect(values).toContain(21);

    // And it is the one selected. Without it the browser submits the first
    // option, which is 1 day - and a Save opened to tick an unrelated checkbox
    // destroys twenty days of archived events while saying "Saved."
    expect(select?.value).toBe('21');
  });

  it('sends only the field that changed', async () => {
    const bodies: unknown[] = [];
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
      const path = url.split('?')[0] ?? url;
      if (init?.method === 'PUT' && path === '/api/settings') {
        bodies.push(JSON.parse(String(init.body)));
        return Promise.resolve(
          new Response(JSON.stringify(settings.body), {
            headers: { 'Content-Type': 'application/json' },
          }),
        );
      }
      const replies: Record<string, unknown> = {
        '/api/session': session.body,
        '/api/clusters': clusters.body,
        '/api/settings': settings.body,
      };
      const reply = replies[path];
      if (reply === undefined) return Promise.resolve(new Response(null, { status: 404 }));
      return Promise.resolve(
        new Response(JSON.stringify(reply), { headers: { 'Content-Type': 'application/json' } }),
      );
    });

    mountApp('/settings/general');
    await waitFor(() => {
      expect(page().querySelector('input[name="captureLogs"]')).not.toBeNull();
    });

    const logs = page().querySelector<HTMLInputElement>('input[name="captureLogs"]');
    logs?.click();
    const save = [...page().querySelectorAll('button')].find(
      (each) => each.textContent === 'Save',
    );
    save?.click();

    await waitFor(() => {
      expect(bodies).toHaveLength(1);
    });

    // **Only what moved.** `false` and "not said" are different instructions
    // about keeping events, and a form that posted all three would turn one into
    // the other the first time a field failed to render.
    expect(bodies[0]).toEqual({ captureLogs: true });
  });
});

describe('a save that could not be written is not a failure', () => {
  it('says it is in effect and will not survive a restart', async () => {
    const applied = {
      ...settings.body,
      applied: {
        persisted: false,
        note: 'This is in effect now and will not survive a restart: read-only file system',
      },
    };
    stubFetch(shell({ '/api/settings': { body: applied } }));
    mountApp('/settings/general');

    await waitFor(() => {
      expect(page().querySelector('[data-persisted="false"]')).not.toBeNull();
    });

    // Reporting it as an error makes a user retype a setting that is
    // already working. What they need is the narrower true thing.
    expect(page().textContent).toContain('will not survive a restart');
    expect(page().querySelector('[role="alert"]')).toBeNull();
  });
});

describe('the archive says what it costs, and whether it is writing', () => {
  it('draws the figures and the state together', async () => {
    stubFetch(shell());
    mountApp('/settings/general');

    await waitFor(() => {
      expect(page().querySelector('[data-archive="ok"]')).not.toBeNull();
    });
    const text = page().textContent ?? '';
    expect(text).toContain('4.0 MiB');
    expect(text).toContain('/state/archive');
    expect(text).toContain('writing');
  });

  it('draws an archive with nowhere to write as a state and not as a zero', async () => {
    const nowhere = {
      ...settings.body,
      archive: {
        state: 'absent',
        reason: {
          code: 'not_installed',
          message: 'this archive has nowhere to write, so it keeps nothing',
        },
      },
    };
    stubFetch(shell({ '/api/settings': { body: nowhere } }));
    mountApp('/settings/general');

    await waitFor(() => {
      expect(page().querySelector('[data-archive="absent"]')).not.toBeNull();
    });
    // A page reporting 0 bytes over an archive that does not exist is the most
    // reassuring possible way to be wrong.
    expect(page().textContent).toContain('nowhere to write');
    expect(page().textContent).not.toContain('0 B');
  });

  it('offers the Empty control, and says what it destroys before it is pressed', async () => {
    stubFetch(shell());
    mountApp('/settings/general');

    // A user who came for a control and finds none concludes the page has not
    // finished loading, so the button is there with the sentence beside it.
    await waitFor(() => {
      expect(page().textContent).toContain('Empty the archive');
    });
    expect(page().textContent).toContain('this is the only copy');
    // And it asks first. The ellipsis is the promise, and a control that opened
    // nothing would be lying with it.
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });
});

describe('the Infos tab links the repository, its issues and the documentation', () => {
  // Three links, and no header sentence about what this console keeps.
  it('draws the three links with their addresses, leaving the console', async () => {
    stubFetch(shell());
    mountApp('/settings/infos');

    await waitFor(() => {
      expect(page().querySelector('[data-infos-links]')).not.toBeNull();
    });
    const links = [...page().querySelectorAll<HTMLAnchorElement>('[data-infos-links] a')];
    expect(links.map((one) => [one.textContent, one.getAttribute('href')])).toEqual([
      ['GitHub repository', 'https://github.com/thonicdev/kubetower'],
      ['Issues', 'https://github.com/thonicdev/kubetower/issues'],
      ['Documentation', 'https://thonicdev.github.io/kubetower-site/docs/'],
    ]);
    for (const one of links) {
      expect(one.target).toBe('_blank');
      expect(one.rel).toContain('noopener');
    }
    expect(page().textContent).not.toContain('What this console');
  });

  it('links no address it was not sent, nor one that is not http or https', async () => {
    const info = { ...settings.body.info, repositoryUrl: 'javascript:alert(1)', documentationUrl: undefined };
    stubFetch(shell({ '/api/settings': { body: { ...settings.body, info } } }));
    mountApp('/settings/infos');

    await waitFor(() => {
      expect(page().querySelector('[data-version]')).not.toBeNull();
    });
    // The refused repository is drawn as the text it is, with no issues
    // link built from it, and the documentation that was not sent is not drawn.
    expect(page().querySelector('[data-infos-links] [data-refused-href]')?.textContent).toBe('javascript:alert(1)');
    expect(page().querySelectorAll('[data-infos-links] a')).toHaveLength(0);
    expect(page().querySelector('a[href^="javascript:"]')).toBeNull();
    expect(page().textContent).not.toContain('Documentation');
  });
});

describe('the Infos tab says the version and whether a newer one exists, and nothing else', () => {
  // No explanations - address, signing in, cluster access, kubeconfig, stored
  // state, repository - on any profile.
  it('draws the version and the update line, and none of the other sections', async () => {
    stubFetch(shell());
    mountApp('/settings/infos');

    await waitFor(() => {
      expect(page().querySelector('[data-version]')?.textContent).toBe('kubetower 0.7.2 (2026-09-11, windows/amd64)');
    });
    expect(page().querySelectorAll('section')).toHaveLength(1);
    const text = page().textContent ?? '';
    for (const gone of ['Kubeconfig', 'Signing in', 'Stored state', 'Cluster access', 'KubeTower on GitHub', '127.0.0.1', '/kube/config']) {
      expect(text).not.toContain(gone);
    }
    for (const gone of ['[data-signing-in]', '[data-in-cluster-access]', '[data-shadowed]', '[data-state-newer]', '[data-state-in-cluster]']) {
      expect(page().querySelector(gone)).toBeNull();
    }
  });

  it('draws the same one section in a cluster', async () => {
    stubFetch(
      shell({
        '/api/capabilities': {
          body: {
            mode: 'shared',
            localShell: false,
            podExec: true,
            nodeShell: false,
            assistant: false,
            portForward: false,
            preferences: false,
            readOnly: { writesRefused: false, detailWritesRefused: false },
          },
        },
      }),
    );
    mountApp('/settings/infos');

    await waitFor(() => {
      expect(page().querySelector('[data-version]')).not.toBeNull();
    });
    expect(page().querySelectorAll('section')).toHaveLength(1);
    expect(page().textContent).not.toContain('ServiceAccount');
  });

  it('offers the release page when a newer release exists, in a few words', async () => {
    stubFetch(shell());
    mountApp('/settings/infos');

    await waitFor(() => {
      expect(page().querySelector('[data-update="newer"]')).not.toBeNull();
    });
    const line = page().querySelector('[data-update="newer"]');
    expect(line?.textContent).toContain('0.8.0 available');
    // The server's sentence - the start-up log's - is on hover, not on the page.
    expect(line?.getAttribute('title')).toContain('KubeTower 0.8.0 has been released.');
    // It offers; it does not update. The whole of the offer is this link, and it
    // leaves the console, so it leaves the tab, with `noopener`.
    const link = page().querySelector<HTMLAnchorElement>(
      'a[href="https://github.com/thonicdev/kubetower/releases/tag/v0.8.0"]',
    );
    // The release page (GitHub's `html_url`), not a file, and it says so.
    expect(link?.textContent).toBe('View release');
    expect(link?.target).toBe('_blank');
    expect(link?.rel).toContain('noopener');
  });

  it('does not link a release address that is not http or https', async () => {
    // `releaseUrl` is copied from the `html_url` of GitHub's answer,
    // unvalidated (`internal/update/update.go`).
    const infos = settings.body.info;
    stubFetch(
      shell({
        '/api/settings': {
          body: { ...settings.body, info: { ...infos, update: { ...infos.update, releaseUrl: 'javascript:alert(2)' } } },
        },
      }),
    );
    mountApp('/settings/infos');

    await waitFor(() => {
      expect(page().querySelector('[data-update="newer"]')).not.toBeNull();
    });
    expect(page().querySelector('a[href^="javascript:"]')).toBeNull();
    // Drawn as the text it is, not dropped: the page still says what it was sent.
    expect([...page().querySelectorAll('[data-refused-href]')].map((node) => node.textContent)).toEqual([
      'javascript:alert(2)',
    ]);
  });

  it('does not draw a failed check the way it draws an up-to-date one', async () => {
    const withUpdate = (update: unknown) => ({ ...settings.body, info: { ...settings.body.info, update } });
    stubFetch(
      shell({
        '/api/settings': {
          body: withUpdate({ state: 'failed', note: 'Could not reach GitHub, so whether a newer release exists is unknown.' }),
        },
      }),
    );
    mountApp('/settings/infos');

    await waitFor(() => {
      expect(page().querySelector('[data-update="failed"]')).not.toBeNull();
    });
    // The whole point of the line. A check that could not be made drawn as an
    // unannotated version reads as confirmation.
    const failed = page().querySelector('[data-update="failed"]');
    expect(failed?.firstElementChild?.textContent).toBe('Could not check for updates');
    expect(failed?.firstElementChild?.className).toContain('text-warn');
    // Why, on the page and so not again on hover: the one thing a user can act on.
    expect(failed?.querySelector('[data-update-why]')?.textContent).toBe(
      'Could not reach GitHub, so whether a newer release exists is unknown.',
    );
    expect(failed?.getAttribute('title')).toBeNull();
    expect(page().textContent).not.toContain('Up to date');
    expect(page().querySelector('[data-update] a')).toBeNull();
  });

  it('says up to date only when the check answered so', async () => {
    const current = { state: 'current', latest: '0.7.2', note: 'This is the latest release.' };
    stubFetch(shell({ '/api/settings': { body: { ...settings.body, info: { ...settings.body.info, update: current } } } }));
    mountApp('/settings/infos');

    await waitFor(() => {
      expect(page().querySelector('[data-update="current"]')).not.toBeNull();
    });
    const line = page().querySelector('[data-update="current"]');
    expect(line?.textContent).toBe('Up to date');
    expect(line?.firstElementChild?.className).toContain('text-ok');
    expect(line?.querySelector('[data-update-why]')).toBeNull();
    expect(page().textContent).not.toContain('Could not check');
  });

  it('never tells a development build it is out of date, and marks it', async () => {
    const dev = {
      ...settings.body,
      info: {
        ...settings.body.info,
        development: true,
        version: 'kubetower dev (go1.27.1, windows/amd64)',
        update: { state: 'notApplicable', note: 'A development build carries no version to compare against a release.' },
      },
    };
    stubFetch(shell({ '/api/settings': { body: dev } }));
    mountApp('/settings/infos');

    await waitFor(() => {
      expect(page().querySelector('[data-update="notApplicable"]')).not.toBeNull();
    });
    expect(page().textContent).toContain('development build');
    expect(page().querySelector('[data-update="notApplicable"]')?.getAttribute('title')).toContain('no version to compare');
    expect(page().textContent).not.toContain('available');
  });

  it('draws no update line at all where nothing checks', async () => {
    const nothing = { ...settings.body, info: { ...settings.body.info, update: undefined } };
    stubFetch(shell({ '/api/settings': { body: nothing } }));
    mountApp('/settings/infos');

    await waitFor(() => {
      expect(page().textContent).toContain('kubetower 0.7.2');
    });
    // An empty line where a state belongs reads as a field that failed to render.
    expect(page().querySelector('[data-update]')).toBeNull();
  });
});

/**
 * A state directory the deployment says is wiped at every restart.
 *
 * The console cannot see a volume and is told, and what it does with it is take
 * away what could not survive the next start: the archive - form, figures and
 * the Empty control - and the node-shell image. **Absent, not disabled**, which
 * is the same rule `nodeShell.available` already follows; and one sentence in
 * their place so the tab does not read as a page that failed to load.
 */
describe('a state directory that does not survive a restart', () => {
  const ephemeral = {
    ...settings.body,
    general: {},
    retentionChoices: undefined,
    archive: {
      state: 'absent',
      reason: {
        code: 'not_supported_here',
        message: "this console's state directory does not survive a restart, so it keeps no archive",
      },
    },
    nodeShell: { enabled: false, available: false, defaultImage: 'alpine:3.24' },
    info: { ...settings.body.info, statePersistent: false },
  };

  it('draws no archive section at all, and says why the tab is short', async () => {
    stubFetch(shell({ '/api/settings': { body: ephemeral } }));
    mountApp('/settings/general');

    await waitFor(() => {
      expect(page().querySelector('[data-state-persistent="false"]')).not.toBeNull();
    });
    const text = page().textContent ?? '';
    // Not the form, not the figures, not the destructive control - and not the
    // "this build has no archive" notice either, which is a different fact.
    expect(text).not.toContain('What the archive keeps');
    expect(text).not.toContain('Nothing is being kept');
    expect(text).not.toContain('Empty the archive');
    expect(text).not.toContain('This build has no archive');
    expect(page().querySelector('[data-archive]')).toBeNull();
    expect(page().querySelector('input[name="archiveEnabled"]')).toBeNull();
    expect(page().querySelector('select[name="retentionDays"]')).toBeNull();
    expect(text).not.toContain('Node shell');
    expect(text).toContain('does not survive a restart');
  });

  it('draws no clear control even where the archive envelope reads ok', async () => {
    // The address the Empty control calls is not registered on this server,
    // so the control must follow `statePersistent` itself and not only the
    // archive envelope: a server answering `ok` beside `statePersistent: false`
    // would otherwise draw a destructive button that answers 404.
    const contradictory = { ...settings.body, info: { ...settings.body.info, statePersistent: false } };
    stubFetch(shell({ '/api/settings': { body: contradictory } }));
    mountApp('/settings/general');

    // Waited on something drawn either way, so that what fails with the gate
    // removed is the clear control and not the wait.
    await waitFor(() => {
      expect(page().textContent).toContain('Node shell');
    });
    expect(page().textContent).not.toContain('Empty the archive');
    expect(page().querySelector('[data-archive]')).toBeNull();
    expect(page().querySelector('[data-state-persistent="false"]')).not.toBeNull();
  });

  it("leaves the Infos tab to the version: the warning is General's", async () => {
    stubFetch(shell({ '/api/settings': { body: ephemeral } }));
    mountApp('/settings/infos');

    await waitFor(() => {
      expect(page().querySelector('[data-version]')).not.toBeNull();
    });
    expect(page().querySelector('[data-state-persistent]')).toBeNull();
  });

  it('draws what it always drew where the server does not say', async () => {
    // A server from before the member: silence is not "ephemeral", and reading
    // it as a warning would put one on every console that predates it.
    const older: Record<string, unknown> = { ...settings.body.info };
    delete older.statePersistent;
    stubFetch(shell({ '/api/settings': { body: { ...settings.body, info: older } } }));
    mountApp('/settings/general');

    await waitFor(() => {
      expect(page().textContent).toContain('What the archive keeps');
    });
    expect(page().querySelector('[data-state-persistent]')).toBeNull();
  });
});

describe('the node shell', () => {
  it('shows the default so the user can see what they are moving away from', async () => {
    stubFetch(shell());
    mountApp('/settings/general');

    await waitFor(() => {
      expect(page().querySelector('input[name="image"]')).not.toBeNull();
    });
    const field = page().querySelector<HTMLInputElement>('input[name="image"]');
    expect(field?.value).toBe('busybox:1.36');
    expect(field?.placeholder).toBe('alpine:3.24');
    expect(page().textContent).toContain('Empty the field and save to go back to it');
  });

  it('is absent where the build has no terminal layer, rather than a field that refuses', async () => {
    const noShell = {
      ...settings.body,
      nodeShell: { enabled: false, available: false },
    };
    stubFetch(shell({ '/api/settings': { body: noShell } }));
    mountApp('/settings/general');

    await waitFor(() => {
      expect(page().textContent).toContain('What the archive keeps');
    });
    // A control that cannot work is worse than an absent one.
    expect(page().textContent).not.toContain('Node shell');
  });
});

/**
 * Emptying the archive, behind the same confirm-then-act plan every other
 * destructive verb uses, with no special case.
 */
describe('emptying the archive', () => {
  const plan = {
    body: {
      token: 'plan-archive-1',
      expiresAt: '2999-01-01T00:00:00Z',
      sentence: 'Empty this console’s archive.',
      facts: {
        verb: 'empty',
        // What `previewClearArchive` composes. Without it the control would
        // draw `facts.verb` - `empty`, the wire slug, on the act that destroys
        // the only copy of everything the archive holds.
        button: 'Empty this archive',
        targets: [{ kind: 'archive', name: "this console's archive", note: '4.0 MiB across 6 days' }],
        count: 1,
        destructive: true,
        irreversible: true,
        warnings: ['These events and log lines are gone from the cluster. This archive is the only copy.'],
        confirmation: {
          kind: 'count',
          expected: '6',
          label: 'Type 6 - the number of days this archive holds - to empty it.',
        },
      },
    },
  };

  it('previews before it touches anything, and draws the server’s own recap', async () => {
    const posted: { path: string; body: unknown }[] = [];
    stubFetch(shell({ '/api/settings/clear': plan }));
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if (init?.method === 'POST') posted.push({ path, body: JSON.parse(String(init.body)) });
      return stubbed(input, init);
    });

    mountApp('/settings/general');
    await waitFor(() => {
      expect(page().textContent).toContain('Empty the archive');
    });
    press('Empty the archive…');

    await waitFor(() => {
      expect(dialog().textContent).toContain('Empty this');
    });
    // The server's sentence and the server's warning, neither composed here.
    expect(dialog().textContent).toContain('the only copy');
    expect(dialog().textContent).toContain('4.0 MiB across 6 days');
    // And nothing has been emptied: the preview touches nothing.
    expect(posted).toHaveLength(1);
    expect(posted[0]?.body).toMatchObject({ scope: 'archive', preview: true });
  });

  it('sends the token and what was typed, and only once the gesture is made', async () => {
    const posted: { path: string; body: unknown }[] = [];
    stubFetch(shell({ '/api/settings/clear': plan }));
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if (init?.method === 'POST') posted.push({ path, body: JSON.parse(String(init.body)) });
      return stubbed(input, init);
    });

    mountApp('/settings/general');
    await waitFor(() => {
      expect(page().textContent).toContain('Empty the archive');
    });
    press('Empty the archive…');
    await waitFor(() => {
      expect(dialog().querySelector('input[type="text"]')).not.toBeNull();
    });

    // The gesture the **server** asked for: the number of days, not a word this
    // page chose. Until it is typed the act cannot be pressed. The control is
    // labeled with the plan's own verb, which is the server's word too.
    expect(apply().hasAttribute('disabled')).toBe(true);

    fireEvent.change(dialog().querySelector('input[type="text"]')!, { target: { value: '6' } });
    fireEvent.click(apply());

    await waitFor(() => {
      expect(posted).toHaveLength(2);
    });
    expect(posted[1]?.body).toMatchObject({
      scope: 'archive',
      preview: false,
      token: 'plan-archive-1',
      confirmed: '6',
    });
  });
});

/** The plan dialog, which is outside `#page`. */
function dialog(): HTMLElement {
  const element = document.querySelector('[role="dialog"]');
  if (element === null) throw new Error('no dialog is open');
  return element as HTMLElement;
}

/** The act control inside it, labeled with the phrase the *server* composed
 *  for this plan - `facts.button`, "Empty this archive", not the verb slug. */
function apply(): HTMLButtonElement {
  const found = [...dialog().querySelectorAll('button')].find(
    (one) =>
      one.textContent?.trim() === 'Empty this archive' || one.textContent?.trim() === 'Working…',
  );
  if (found === undefined) throw new Error('the dialog has no act control');
  return found;
}

function press(text: string): void {
  const found = [...page().querySelectorAll('button')].find((one) => one.textContent?.includes(text));
  if (found === undefined) throw new Error(`no control saying ${text}`);
  fireEvent.click(found);
}

/*
  Several replicas behind one address. The three settings writes and the clear
  are not registered there (Capabilities.replicated says so), so a form that
  posted to one would answer 404 with nothing saying why. Absent, with one
  sentence, and what is only read stays.

  **Not `preferences`**: the server profile turns that off on one replica too,
  where the settings writes are registered - so the third case below is the one
  this member exists for.
*/
describe('where several replicas share one address', () => {
  function capabilities(replicated: boolean, preferences = !replicated, mode = 'shared'): StubRoutes {
    return {
      '/api/capabilities': {
        body: {
          mode,
          localShell: false,
          podExec: true,
          nodeShell: true,
          assistant: false,
          portForward: false,
          preferences,
          replicated,
          readOnly: { writesRefused: false, detailWritesRefused: false },
        },
      },
    };
  }

  it('draws no settings write on one replica of the server profile, where they are everybody\'s', async () => {
    // The server profile registers none of them on one replica either.
    stubFetch(shell(capabilities(false, false)));
    mountApp('/settings/general');

    await waitFor(() => {
      expect(page().textContent).toContain('Settings are not kept here');
    });
    expect(page().textContent).toContain('shared by everybody who signs in');
    expect(page().querySelector('input[name="archiveEnabled"]')).toBeNull();
    expect(page().textContent).not.toContain('Empty the archive');
    expect(page().textContent).toContain('What it costs');
  });

  it('draws no write the server does not have', async () => {
    stubFetch(shell(capabilities(true)));
    mountApp('/settings/general');

    await waitFor(() => {
      expect(page().textContent).toContain('Settings are not kept here');
    });
    expect(page().querySelector('input[name="archiveEnabled"]')).toBeNull();
    expect(page().querySelector('input[name="image"]')).toBeNull();
    expect(page().textContent).not.toContain('Empty the archive');
    // What is only read is still drawn.
    expect(page().textContent).toContain('What it costs');
  });

  it('draws every form where the server keeps them', async () => {
    stubFetch(shell(capabilities(false, true, 'container')));
    mountApp('/settings/general');

    await waitFor(() => {
      expect(page().querySelector('input[name="archiveEnabled"]')).not.toBeNull();
    });
    expect(page().querySelector('input[name="image"]')).not.toBeNull();
    expect(page().textContent).toContain('Empty the archive');
    expect(page().textContent).not.toContain('Settings are not kept here');
  });
});

/*
  In a cluster, Settings › Extra lists every extra the build carries, each off
  until it is turned on, with a button. The chart's
  values (KT_EXTRAS) say which may be turned on, and a person holding the `use`
  permission on one turns it on per cluster; the listing says per row whether this
  person may (`writable`) and why not (`locked`, `reason`), and the tab draws
  exactly that. A build carrying no extra draws no tab, and neither does
  a listing that failed: a failure notice about a feature the build may not
  have would be wrong.

  The fixture keys are not real pages' keys, for `fixtureextension_test.go`'s
  reason: which pages exist is not this repository's to publish.
*/
describe('in a cluster, Extra lists every extra with a button, as the server says it may be pressed', () => {
  const inCluster = 'in-cluster';
  const profile = (mode: string, replicated = false): StubRoutes => ({
    '/api/capabilities': {
      body: {
        mode,
        localShell: false,
        podExec: true,
        nodeShell: false,
        assistant: false,
        portForward: false,
        preferences: false,
        replicated,
        readOnly: { writesRefused: false, detailWritesRefused: false },
      },
    },
    '/api/clusters': {
      body: { state: 'ok', items: [{ name: inCluster, label: inCluster, state: 'ok' }] },
    },
  });
  const notInValues = "Not enabled in this installation's chart values.";
  const noUse = 'You may not turn Charlie Center on or off on this cluster: it needs the verb use on extras.kubetower.dev/charlie, granted by a ClusterRole.';
  const carried: StubRoutes = {
    [`/api/c/${inCluster}/extensions`]: {
      body: {
        state: 'ok',
        items: [
          { key: 'alfa', label: 'Alfa Center', enabled: false, pinned: false, writable: true },
          {
            key: 'bravo', label: 'Bravo Center', enabled: false, pinned: false, writable: false,
            locked: 'values', reason: notInValues,
          },
          {
            key: 'charlie', label: 'Charlie Center', enabled: false, pinned: false, writable: false,
            locked: 'use', reason: noUse,
          },
        ],
      },
    },
  };

  function tabs(): string {
    return document.querySelector('nav[aria-label="Settings sections"]')?.textContent ?? '';
  }
  const tile = (key: string): Element | null => page().querySelector(`[data-extra="${key}"]`);

  it('lists every extra, each off with its button, disabled with the reason where the values do not name it', async () => {
    stubFetch(shell({ ...profile('shared'), ...carried }));
    mountApp('/settings/extra');

    await waitFor(() => {
      expect(tile('alfa')).not.toBeNull();
    });
    expect(tabs()).toContain('Extra');
    for (const key of ['alfa', 'bravo', 'charlie']) {
      expect(tile(key)?.getAttribute('data-on')).toBe('false');
    }
    // Writable: the button, enabled.
    const alfa = tile('alfa')?.querySelector<HTMLButtonElement>('[role="switch"]');
    expect(alfa?.disabled).toBe(false);
    expect(alfa?.getAttribute('aria-checked')).toBe('false');
    expect(tile('alfa')?.querySelector('[data-extra-reason]')).toBeNull();
    // Not in the chart's values: the button, disabled, and the reason it names.
    const bravo = tile('bravo')?.querySelector<HTMLButtonElement>('[role="switch"]') ?? null;
    expect(bravo).not.toBeNull();
    expect(bravo?.disabled).toBe(true);
    const reason = tile('bravo')?.querySelector('[data-extra-reason="values"]');
    expect(reason?.textContent).toBe(notInValues);
    expect(bravo?.getAttribute('aria-describedby')).toBe(reason?.id);
    // No `use`: the button is not offered, and the reason says why.
    // (Edit and About are still drawn: About offers no switch either.)
    // The star writes a person's favourite, not the cluster (#111): it stays.
    expect(tile('charlie')?.querySelector('[role="switch"], button:not([data-extra-about]):not([data-extra-edit]):not([data-extra-favourite])')).toBeNull();
    expect(tile('charlie')?.querySelector('[data-extra-reason="use"]')?.textContent).toBe(noUse);
    // Nothing says "per cluster", and no tile carries a hover.
    expect(page().querySelector('[data-extras]')?.textContent).not.toMatch(/per cluster/i);
    expect(page().querySelector('[data-extras] [data-tile-detail] [title]')).toBeNull();
  });

  it('turns an extra on through the write, for a person the server says may', async () => {
    const sent: { path: string; body: unknown }[] = [];
    stubFetch(shell({ ...profile('shared'), ...carried }));
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      if ((init?.method ?? 'GET') === 'PUT') {
        sent.push({ path: String(input), body: JSON.parse(String(init?.body ?? 'null')) as unknown });
        return Promise.resolve(new Response(null, { status: 204 }));
      }
      return stubbed(input, init);
    });
    mountApp('/settings/extra');
    const button = await waitFor(() => {
      const found = tile('alfa')?.querySelector('[role="switch"]') ?? null;
      expect(found).not.toBeNull();
      return found as Element;
    });

    fireEvent.click(button);
    await waitFor(() => {
      expect(sent).toEqual([{ path: `/api/c/${inCluster}/extensions/alfa`, body: { enabled: true } }]);
    });
  });

  it("draws the server's refusal when the write is refused", async () => {
    stubFetch(shell({ ...profile('shared'), ...carried }));
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      if ((init?.method ?? 'GET') === 'PUT') {
        const problem = {
          type: 'about:blank', title: 'Refused', status: 403, code: 'rbac_denied',
          detail: 'access denied: use on extras.kubetower.dev/alfa',
        };
        return Promise.resolve(
          new Response(JSON.stringify(problem), { status: 403, headers: { 'Content-Type': 'application/problem+json' } }),
        );
      }
      return stubbed(input, init);
    });
    mountApp('/settings/extra');
    const button = await waitFor(() => {
      const found = tile('alfa')?.querySelector('[role="switch"]') ?? null;
      expect(found).not.toBeNull();
      return found as Element;
    });

    fireEvent.click(button);
    await waitFor(() => {
      expect(tile('alfa')?.querySelector('[role="alert"]')?.textContent).toContain('use on extras.kubetower.dev/alfa');
    });
  });

  it('with several replicas, draws every row with its button disabled and the reason', async () => {
    const replicas = 'This console runs as several replicas, which have no per-cluster switch: extras are turned on with one replica and its state volume.';
    stubFetch(
      shell({
        ...profile('shared', true),
        [`/api/c/${inCluster}/extensions`]: {
          body: {
            state: 'ok',
            items: ['alfa', 'bravo'].map((key) => ({
              key, label: key, enabled: false, pinned: false, writable: false, locked: 'unregistered', reason: replicas,
            })),
          },
        },
      }),
    );
    mountApp('/settings/extra');
    await waitFor(() => {
      expect(tile('alfa')).not.toBeNull();
    });
    const tab = page().querySelector('[data-extras]');
    // Each extra keeps its button, disabled, with the
    // reason it cannot move; nothing can be pressed.
    const switches = [...(tab?.querySelectorAll<HTMLButtonElement>('[role="switch"]') ?? [])];
    expect(switches.length).toBe(2);
    expect(switches.every((one) => one.disabled)).toBe(true);
    // (Bar each card's Edit and About, which write nothing by themselves, and its
    // star, which writes a person's favourite and not the cluster: #111.)
    expect(
      tab?.querySelectorAll('button:not([disabled]):not([data-extra-about]):not([data-extra-edit]):not([data-extra-favourite]), input:not([disabled]), select, textarea').length,
    ).toBe(0);
    expect(tab?.querySelectorAll('[data-extra-reason="unregistered"]').length).toBe(2);
    expect(tile('alfa')?.textContent).toContain(replicas);
    expect(switches[0]?.getAttribute('aria-describedby')).toBe(
      tile('alfa')?.querySelector('[data-extra-reason="unregistered"]')?.id,
    );
  });

  it('draws no tab for a build that carries no extra, and sends the address to General', async () => {
    stubFetch(
      shell({ ...profile('shared'), [`/api/c/${inCluster}/extensions`]: { body: { state: 'empty', items: [] } } }),
    );
    const { router } = mountApp('/settings/extra');

    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/settings/general');
    });
    await waitFor(() => {
      expect(page().textContent).toContain('What the archive keeps');
    });
    expect(tabs()).not.toContain('Extra');
    expect(page().querySelector('[data-extras]')).toBeNull();
  });

  it('draws no tab, and no failure, when the listing cannot be read', async () => {
    // No stub for the listing: the harness answers 404.
    stubFetch(shell(profile('shared')));
    mountApp('/settings/general');

    await waitFor(() => {
      expect(page().textContent).toContain('What the archive keeps');
    });
    await waitFor(() => {
      expect(tabs()).toContain('Infos');
    });
    expect(tabs()).not.toContain('Extra');
    expect(page().textContent).not.toContain('Could not');
    expect(page().querySelector('[role="alert"]')).toBeNull();
  });

  it("draws an extension's settings at their value, with nothing to press, for a person who may not change it", async () => {
    stubFetch(
      shell({
        ...profile('shared'),
        [`/api/c/${inCluster}/extensions`]: {
          body: {
            state: 'ok',
            items: [
              {
                key: 'alfa', label: 'Alfa Center', enabled: true, pinned: false, writable: false,
                locked: 'use', reason: 'You may not turn Alfa Center on or off on this cluster.',
                settings: [{ key: 'list-column', label: 'Show the Alfa column', value: true, default: true }],
              },
            ],
          },
        },
      }),
    );
    mountApp('/settings/extra');
    await edit('alfa');

    expect(modal().querySelector('[data-extra-setting="list-column"]')?.textContent).toContain('Show the Alfa column');
    expect(modal().querySelector<HTMLInputElement>('[data-extra-setting="list-column"] input')?.checked).toBe(true);
    // Nothing in the window can be pressed to write, and Save is not offered.
    expect(modal().querySelectorAll('input:not([disabled]), select:not([disabled]), [role="switch"]:not([disabled])')).toHaveLength(0);
    expect(modal().querySelector<HTMLButtonElement>('button[type="submit"]')?.disabled).toBe(true);
  });
});

/**
 * On the desktop and in a container, Settings → Extra draws one tile per
 * extension with an on/off switch, for the cluster picked, and the extension's
 * own settings; every extension is off until it is turned on.
 */
describe('on the desktop, Extra has a tile per extension with a switch and its settings', () => {
  const desktop: StubRoutes = {
    '/api/capabilities': {
      body: {
        mode: 'container',
        localShell: true,
        podExec: true,
        nodeShell: true,
        assistant: false,
        portForward: true,
        preferences: true,
        replicated: false,
        readOnly: { writesRefused: false, detailWritesRefused: false },
      },
    },
  };
  function listing(cluster: string, alfaOn: boolean, columnOn: boolean): StubReply {
    return {
      body: {
        state: 'ok',
        items: [
          {
            key: 'alfa', label: 'Alfa Center', summary: 'Alfa things.', needs: 'an Alfa operator',
            enabled: alfaOn, pinned: false, writable: true,
            settings: [{
              key: 'list-column', label: 'Show the Alfa column in the lists',
              summary: 'Off, the lists draw no Alfa column.', value: columnOn, default: true,
            }],
          },
          {
            key: 'bravo', label: `Bravo on ${cluster}`, enabled: true, pinned: true, writable: false,
            locked: 'pinned', reason: "Turned on by KT_EXTRAS in the deployment's environment; it cannot be turned off here.",
            settings: [],
          },
        ],
      },
    };
  }

  /** A stub that also keeps what was sent, and answers the listing from the
   *  state the writes leave - so a test sees the tile draw what the console
   *  kept, not what was pressed. */
  function console(): { sent: { method: string; path: string; body: unknown }[] } {
    const state = { alfaOn: false, columnOn: true };
    const sent: { method: string; path: string; body: unknown }[] = [];
    stubFetch(shell({ ...desktop, '/api/c/staging/extensions': listing('staging', false, true) }));
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      const method = init?.method ?? 'GET';
      if (method === 'PUT') {
        const body: unknown = JSON.parse(String(init?.body ?? 'null'));
        sent.push({ method, path, body });
        if (path === '/api/c/prod/extensions/alfa') state.alfaOn = (body as { enabled: boolean }).enabled;
        if (path === '/api/c/prod/extensions/alfa/settings/list-column') state.columnOn = (body as { value: boolean }).value;
        return Promise.resolve(new Response(null, { status: 204 }));
      }
      if (path === '/api/c/prod/extensions') {
        return Promise.resolve(
          new Response(JSON.stringify(listing('prod', state.alfaOn, state.columnOn).body), {
            headers: { 'Content-Type': 'application/json' },
          }),
        );
      }
      return stubbed(input, init);
    });
    return { sent };
  }

  function tile(key: string): HTMLElement {
    const found = page().querySelector<HTMLElement>(`[data-extra="${key}"]`);
    if (found === null) throw new Error(`no tile for ${key}`);
    return found;
  }

  it('uses the whole width of the screen', async () => {
    console();
    mountApp('/settings/extra');
    await waitFor(() => {
      expect(page().querySelector('[data-extra="alfa"]')).not.toBeNull();
    });
    const root = page().querySelector('h1')?.closest('div.flex');
    expect(root?.className).toContain('w-full');
    expect(root?.className).not.toMatch(/max-w-/);
  });

  it("draws a tile per extension with its words, a switch, and the deployment's pin where there is one", async () => {
    console();
    mountApp('/settings/extra');

    await waitFor(() => {
      expect(page().querySelector('[data-extra="alfa"]')).not.toBeNull();
    });
    const alfa = tile('alfa');
    // The name and one line of purpose, and no hover on the tile.
    expect(alfa.textContent).toContain('Alfa Center');
    expect(alfa.textContent).toContain('Alfa things.');
    expect(alfa.textContent).not.toContain('an Alfa operator');
    expect(alfa.querySelector('[data-tile-detail] [title]')).toBeNull();
    expect(page().querySelector('[data-extras]')?.textContent).not.toMatch(/per cluster/i);
    // Off on a cluster nobody chose, with its switch.
    const toggle = alfa.querySelector('[role="switch"]');
    expect(toggle?.getAttribute('aria-checked')).toBe('false');
    expect(toggle?.getAttribute('aria-label')).toBe('Alfa Center, cluster prod');
    // Pinned: the switch, disabled at the pinned state, the badge, and the reason.
    const pinnedSwitch = tile('bravo').querySelector<HTMLButtonElement>('[role="switch"]');
    expect(pinnedSwitch?.disabled).toBe(true);
    expect(pinnedSwitch?.getAttribute('aria-checked')).toBe('true');
    expect(tile('bravo').querySelector('[data-extra-pinned]')?.textContent).toBe('pinned');
    expect(tile('bravo').querySelector('[data-extra-reason="pinned"]')?.textContent).toContain('KT_EXTRAS');
    expect(alfa.querySelector('[data-extra-pinned]')).toBeNull();
    // The cluster the tiles are about: written, and chosen by the rail's icons.
    expect(page().querySelector('[data-extras-cluster="prod"]')?.textContent).toBe('prod');
    expect(page().querySelector('select')).toBeNull();
  });

  it('turns an extension on on that cluster, and draws what the console kept', async () => {
    const { sent } = console();
    mountApp('/settings/extra');
    await waitFor(() => {
      expect(page().querySelector('[data-extra="alfa"] [role="switch"]')).not.toBeNull();
    });

    fireEvent.click(tile('alfa').querySelector('[role="switch"]') as Element);

    await waitFor(() => {
      expect(tile('alfa').getAttribute('data-on')).toBe('true');
    });
    expect(sent).toEqual([{ method: 'PUT', path: '/api/c/prod/extensions/alfa', body: { enabled: true } }]);
    expect(tile('alfa').querySelector('[role="switch"]')?.getAttribute('aria-checked')).toBe('true');
  });

  it("writes an extension's setting on that cluster", async () => {
    const { sent } = console();
    mountApp('/settings/extra');
    await edit('alfa');
    const box = modal().querySelector<HTMLInputElement>('[data-extra-setting="list-column"] input');
    expect(box?.checked).toBe(true);
    expect(modal().textContent).toContain('Show the Alfa column in the lists');

    // Nothing is written by ticking: the window holds a draft until Save.
    fireEvent.click(box as Element);
    expect(sent).toEqual([]);
    fireEvent.click(modal().querySelector('button[type="submit"]') as Element);

    await waitFor(() => {
      expect(sent).toEqual([
        { method: 'PUT', path: '/api/c/prod/extensions/alfa/settings/list-column', body: { value: false } },
      ]);
    });
    await waitFor(() => {
      expect(document.querySelector('[role="dialog"]')).toBeNull();
    });
  });

  it('draws a setting the deployment pinned at its value, with nothing to press', async () => {
    const pinned = listing('prod', true, true);
    const body = pinned.body as { items: { settings: { pinned?: boolean; value: boolean }[] }[] };
    const first = body.items[0]?.settings[0];
    if (first === undefined) throw new Error('the fixture lost its setting');
    first.pinned = true;
    first.value = true;
    stubFetch(shell({ ...desktop, '/api/c/prod/extensions': pinned }));
    mountApp('/settings/extra');
    await edit('alfa');

    expect(modal().querySelector('[data-extra-setting-pinned]')).not.toBeNull();
    const row = modal().querySelector('[data-extra-setting="list-column"]');
    expect(row?.querySelector('input')).toBeNull();
    expect(row?.textContent).toContain('pinned');
    expect(row?.textContent).toContain('On');
    // The extension's own switch is still the person's.
    expect(tile('alfa').querySelector('[role="switch"]')).not.toBeNull();
  });

  it("says a refused switch on the tile, in the server's words", async () => {
    stubFetch(shell({ ...desktop, '/api/c/prod/extensions': listing('prod', true, true) }));
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if ((init?.method ?? 'GET') === 'PUT' && path === '/api/c/prod/extensions/alfa') {
        const problem = { type: 'about:blank', title: 'Refused', status: 403, detail: 'not today', code: 'forbidden' };
        return Promise.resolve(
          new Response(JSON.stringify(problem), { status: 403, headers: { 'Content-Type': 'application/problem+json' } }),
        );
      }
      return stubbed(input, init);
    });
    mountApp('/settings/extra');
    await waitFor(() => {
      expect(page().querySelector('[data-extra="alfa"] [role="switch"]')).not.toBeNull();
    });

    fireEvent.click(tile('alfa').querySelector('[role="switch"]') as Element);
    await waitFor(() => {
      expect(tile('alfa').querySelector('[role="alert"]')?.textContent).toContain('not today');
    });
  });

  it('is about the cluster the address names, and the rail icons move it', async () => {
    console();
    const { router } = mountApp('/settings/extra?cluster=staging');
    await waitFor(() => {
      expect(tile('bravo').textContent).toContain('Bravo on staging');
    });
    const icon = (name: string): HTMLElement =>
      document.querySelector<HTMLElement>(`#rail [data-cluster="${name}"]`) as HTMLElement;
    // The one shown is highlighted, and only that one.
    expect(icon('staging').getAttribute('aria-current')).toBe('page');
    expect(icon('prod').getAttribute('aria-current')).toBeNull();
    expect(page().querySelector('[data-extras-cluster]')?.textContent).toContain('staging');

    fireEvent.click(icon('prod'));

    await waitFor(() => {
      expect(tile('bravo').textContent).toContain('Bravo on prod');
    });
    // Still in Settings > Extra, not on the cluster's page.
    expect(router.state.location.pathname).toBe('/settings/extra');
    expect(router.state.location.search).toBe('?cluster=prod');
    expect(icon('prod').getAttribute('aria-current')).toBe('page');
    expect(icon('staging').getAttribute('aria-current')).toBeNull();
    expect(page().querySelector('[data-extras-cluster]')?.textContent).toContain('prod');
  });

  it('opens on the cluster the Settings link was pressed on', async () => {
    console();
    const { router } = mountApp('/c/staging');
    const link = await waitFor(() => {
      const found = document.querySelector<HTMLAnchorElement>('a[title="Settings"]');
      expect(found).not.toBeNull();
      return found as HTMLAnchorElement;
    });
    fireEvent.click(link);
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/settings/general');
    });
    expect(router.state.location.search).toBe('?cluster=staging');
    await waitFor(() => {
      expect(page().querySelector('a[href^="/settings/extra"]')).not.toBeNull();
    });
    fireEvent.click(page().querySelector('a[href^="/settings/extra"]') as Element);
    await waitFor(() => {
      expect(tile('bravo').textContent).toContain('Bravo on staging');
    });
  });

  it('leaves the rail going to the cluster outside Settings > Extra', async () => {
    console();
    const { router } = mountApp('/settings/general');
    const icon = await waitFor(() => {
      const found = document.querySelector<HTMLElement>('#rail [data-cluster="prod"]');
      expect(found).not.toBeNull();
      return found as HTMLElement;
    });
    fireEvent.click(icon);
    await waitFor(() => {
      expect(router.state.location.pathname.startsWith('/c/prod')).toBe(true);
    });
  });

  it('draws every switch disabled where the server says no row is writable', async () => {
    const view = listing('prod', true, true);
    const body = view.body as { items: Record<string, unknown>[] };
    for (const item of body.items) {
      Object.assign(item, { writable: false, locked: 'unregistered', reason: 'No per-cluster switch here.' });
    }
    stubFetch(
      shell({
        '/api/capabilities': {
          body: { ...(desktop['/api/capabilities']?.body as object), preferences: false, replicated: true },
        },
        '/api/c/prod/extensions': view,
      }),
    );
    mountApp('/settings/extra');
    await waitFor(() => {
      expect(page().querySelector('[data-extra="alfa"]')).not.toBeNull();
    });
    const tab = page().querySelector('[data-extras]');
    expect(tab?.querySelectorAll('[role="switch"]').length).toBe(2);
    // Bar each card's Edit and About, which write nothing by themselves, and its
    // star, which writes a person's favourite and not the cluster (#111).
    expect(tab?.querySelectorAll('input:not([disabled]), button:not([disabled]):not([data-extra-about]):not([data-extra-edit]):not([data-extra-favourite])').length).toBe(0);
  });
});

/**
 * Check now: ask GitHub past the day the answer is
 * kept for, show the running check, and land the answer in place.
 */
describe('check now', () => {
  const withUpdate = (update: unknown) => ({ ...settings.body, info: { ...settings.body.info, update } });

  /** The shell's routes, with the POST held until `release` is called. */
  function holding(update: unknown, answer: unknown): { posts: () => number; release: () => void } {
    let posts = 0;
    let release: () => void = () => undefined;
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    const routes = shell({ '/api/settings': { body: withUpdate(update) } });
    vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
      const key = path.split('?')[0] ?? path;
      if (key === '/api/settings/update-check') {
        expect(init?.method).toBe('POST');
        posts += 1;
        await held;
        return new Response(JSON.stringify(answer), { status: 200, headers: { 'Content-Type': 'application/json' } });
      }
      const reply: StubReply | undefined = routes[key];
      if (reply === undefined) return new Response(null, { status: 404 });
      return new Response(JSON.stringify(reply.body), { status: 200, headers: { 'Content-Type': 'application/json' } });
    });
    return { posts: () => posts, release };
  }

  function button(): HTMLButtonElement | null {
    return [...page().querySelectorAll<HTMLButtonElement>('[data-update-check] button')].find(
      (one) => one.textContent === 'Check now',
    ) ?? null;
  }

  it('asks, shows the running check, and draws the answer in place', async () => {
    const stub = holding(
      { state: 'current', latest: '0.7.2', note: 'This is the latest release.', checking: false },
      { state: 'newer', latest: '0.9.0', releaseUrl: 'https://github.com/thonicdev/kubetower/releases/tag/v0.9.0',
        checkedAt: '2026-11-04T09:00:00Z', note: 'KubeTower 0.9.0 has been released.', checking: false },
    );
    mountApp('/settings/infos');
    await waitFor(() => {
      expect(button()).not.toBeNull();
    });
    expect(page().querySelector('[data-update-checking]')).toBeNull();

    fireEvent.click(button() as HTMLButtonElement);
    await waitFor(() => {
      expect(page().querySelector('[data-update-checking]')?.textContent).toBe('Checking for updates…');
    });
    expect(button()?.disabled).toBe(true);
    // Disabled while out: a second press does not send a second request.
    fireEvent.click(button() as HTMLButtonElement);
    expect(stub.posts()).toBe(1);

    stub.release();
    await waitFor(() => {
      expect(page().querySelector('[data-update="newer"]')?.textContent).toContain('0.9.0 available');
    });
    expect(page().querySelector('[data-update-checking]')).toBeNull();
    expect(button()?.disabled).toBe(false);
  });

  // Each outcome from a different starting line, so "drawn in place" cannot be
  // passed by leaving the previous line where it was.
  it.each([
    {
      outcome: 'newer',
      from: { state: 'failed', note: 'Could not reach GitHub, so whether a newer release exists is unknown.', checking: false },
      answer: { state: 'newer', latest: '0.9.0', releaseUrl: 'https://github.com/thonicdev/kubetower/releases/tag/v0.9.0',
        checkedAt: '2026-11-05T09:00:00Z', note: 'KubeTower 0.9.0 has been released.', checking: false },
    },
    {
      outcome: 'current',
      from: { state: 'newer', latest: '0.9.0', releaseUrl: 'https://github.com/thonicdev/kubetower/releases/tag/v0.9.0',
        note: 'KubeTower 0.9.0 has been released.', checking: false },
      answer: { state: 'current', latest: '0.7.2', checkedAt: '2026-11-05T09:00:00Z',
        note: 'This is the latest release.', checking: false },
    },
    {
      outcome: 'failed',
      from: { state: 'current', latest: '0.7.2', note: 'This is the latest release.', checking: false },
      answer: { state: 'failed', checkedAt: '2026-11-05T09:00:00Z',
        note: 'GitHub answered 503, so whether a newer release exists is unknown.', checking: false },
    },
  ])('shows the running check, then lands in $outcome', async ({ outcome, from, answer }) => {
    const stub = holding(from, answer);
    mountApp('/settings/infos');
    await waitFor(() => {
      expect(button()).not.toBeNull();
    });

    fireEvent.click(button() as HTMLButtonElement);
    await waitFor(() => {
      expect(page().querySelector('[data-update-checking]')?.textContent).toBe('Checking for updates…');
    });
    expect(button()?.disabled).toBe(true);
    // While it is out, the previous answer is still the one drawn.
    expect(page().querySelector(`[data-update="${from.state}"]`)).not.toBeNull();

    stub.release();
    await waitFor(() => {
      expect(page().querySelector(`[data-update="${outcome}"]`)).not.toBeNull();
    });
    expect(page().querySelector('[data-update-checking]')).toBeNull();
    expect(button()?.disabled).toBe(false);
    // Exactly one update line, the answer's.
    const lines = page().querySelectorAll('[data-update]');
    expect(lines).toHaveLength(1);
    const line = lines[0]?.textContent ?? '';
    if (outcome === 'newer') {
      expect(line).toContain('0.9.0 available');
      expect(lines[0]?.querySelector('a')?.textContent).toBe('View release');
    } else if (outcome === 'current') {
      expect(line).toBe('Up to date');
    } else {
      expect(line).toContain('Could not check for updates');
      expect(line).toContain(answer.note);
      expect(line).not.toContain('Up to date');
    }
  });

  it('draws a check the server is running on its own as running', async () => {
    stubFetch(shell({ '/api/settings': { body: withUpdate({ state: 'failed', note: 'The check has not run yet.', checking: true }) } }));
    mountApp('/settings/infos');
    await waitFor(() => {
      expect(page().querySelector('[data-update-checking]')).not.toBeNull();
    });
    expect(button()?.disabled).toBe(true);
  });

  it.each(['off', 'notApplicable'])('offers no button where nothing can be asked: %s', async (state) => {
    stubFetch(shell({ '/api/settings': { body: withUpdate({ state, checking: false }) } }));
    mountApp('/settings/infos');
    await waitFor(() => {
      expect(page().querySelector(`[data-update="${state}"]`)).not.toBeNull();
    });
    expect(page().querySelector('[data-update-check]')).toBeNull();
  });

  it('says so when the check could not be asked at all', async () => {
    stubFetch(
      shell({
        '/api/settings': { body: withUpdate({ state: 'current', latest: '0.7.2', checking: false }) },
        '/api/settings/update-check': {
          status: 500,
          contentType: 'application/problem+json',
          body: { status: 500, title: 'Internal Server Error', code: 'internal', detail: 'the checker is gone' },
        },
      }),
    );
    mountApp('/settings/infos');
    await waitFor(() => {
      expect(button()).not.toBeNull();
    });
    fireEvent.click(button() as HTMLButtonElement);
    await waitFor(() => {
      expect(page().querySelector('[data-update-check-failed]')).not.toBeNull();
    });
    // The last answer stays: a failed press is not a failed check.
    expect(page().querySelector('[data-update="current"]')).not.toBeNull();
  });
});

/**
 * The server's own scheduled check answers while the Infos page is open: the
 * page reads the update state again and draws the new line without a remount.
 * Before, it read again only while a check said it was running, so a page
 * that read `checking: false` said "Up to date" next to a Dock showing "1".
 */
describe('an open Infos page follows the scheduled check', () => {
  const withUpdate = (update: unknown) => ({ ...settings.body, info: { ...settings.body.info, update } });

  afterEach(() => {
    vi.useRealTimers();
  });

  /** The shell's routes, read at each request, counting the settings reads. */
  function live(update: unknown): { routes: Record<string, StubReply>; reads: () => number } {
    const routes: Record<string, StubReply> = { ...shell({ '/api/settings': { body: withUpdate(update) } }) };
    let reads = 0;
    vi.stubGlobal('fetch', async (input: RequestInfo | URL) => {
      const path = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
      const key = path.split('?')[0] ?? path;
      if (key === '/api/settings') reads += 1;
      const reply: StubReply | undefined = routes[key];
      if (reply === undefined) return new Response(null, { status: 404 });
      return new Response(JSON.stringify(reply.body), { status: 200, headers: { 'Content-Type': 'application/json' } });
    });
    return { routes, reads: () => reads };
  }

  it('draws a newer release the server found, without a remount', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const stub = live({ state: 'current', latest: '0.7.2', note: 'This is the latest release.', checking: false });
    mountApp('/settings/infos');
    await waitFor(() => {
      expect(page().querySelector('[data-update="current"]')?.textContent).toBe('Up to date');
    });
    const infos = page().querySelector('[data-infos]');

    // The hourly tick finds 0.9.0; nothing on the page asks for it.
    stub.routes['/api/settings'] = {
      body: withUpdate({ state: 'newer', latest: '0.9.0', releaseUrl: 'https://github.com/thonicdev/kubetower/releases/tag/v0.9.0',
        checkedAt: '2026-11-06T10:00:00Z', note: 'KubeTower 0.9.0 has been released.', checking: false }),
    };
    await vi.advanceTimersByTimeAsync(61_000);

    await waitFor(() => {
      expect(page().querySelector('[data-update="newer"]')?.textContent).toContain('0.9.0 available');
    });
    expect(page().querySelector('[data-update="current"]')).toBeNull();
    // The same section, so this was a re-read and not a remount.
    expect(page().querySelector('[data-infos]')).toBe(infos);
  });

  it.each(['off', 'notApplicable'])('reads nothing again where no check can run: %s', async (state) => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const stub = live({ state, checking: false });
    mountApp('/settings/infos');
    await waitFor(() => {
      expect(page().querySelector(`[data-update="${state}"]`)).not.toBeNull();
    });
    const before = stub.reads();
    await vi.advanceTimersByTimeAsync(181_000);
    expect(stub.reads()).toBe(before);
  });

  it('does not poll from General', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const stub = live({ state: 'current', latest: '0.7.2', note: 'This is the latest release.', checking: false });
    mountApp('/settings/general');
    await waitFor(() => {
      expect(page().textContent).toContain('What the archive keeps');
    });
    const before = stub.reads();
    await vi.advanceTimersByTimeAsync(181_000);
    expect(stub.reads()).toBe(before);
  });
});

describe("Extra: a card opens the extra's About window, which only informs", () => {
  const capabilities: StubRoutes = {
    '/api/capabilities': {
      body: {
        mode: 'container',
        localShell: true,
        podExec: true,
        nodeShell: true,
        assistant: false,
        portForward: true,
        preferences: true,
        replicated: false,
        readOnly: { writesRefused: false, detailWritesRefused: false },
      },
    },
  };
  const notInValues = "Not enabled in this installation's chart values.";
  const noUse = 'You may not turn Charlie Center on or off on this cluster.';

  /** What the console keeps: the writes change it, and the listing is read
   *  from it - so the window is seen to draw what was kept. */
  interface Kept {
    alfaOn: boolean;
    deltaOn: boolean;
    column: boolean;
  }
  function listing(kept: Kept): unknown {
    return {
      state: 'ok',
      items: [
        {
          key: 'alfa', label: 'Alfa Center', summary: 'Alfa things.', enabled: kept.alfaOn, pinned: false, writable: true,
          settings: [
            { key: 'list-column', label: 'Show the Alfa column in the lists', value: kept.column, default: true },
            { key: 'fixed', label: 'A setting the deployment chose', value: true, default: false, pinned: true },
          ],
        },
        { key: 'delta', label: 'Delta Center', enabled: kept.deltaOn, pinned: false, writable: true, settings: [] },
        {
          key: 'bravo', label: 'Bravo Center', enabled: false, pinned: false, writable: false,
          locked: 'values', reason: notInValues, settings: [],
        },
        {
          key: 'charlie', label: 'Charlie Center', enabled: false, pinned: false, writable: false,
          locked: 'use', reason: noUse, settings: [],
        },
      ],
    };
  }

  /** Two extras with their words, as `app/extras` hands them to `createApp`. */
  const shipped: Extension[] = [
    {
      key: 'alfa',
      icon: 'sliders',
      nav: { label: 'Alfa Center', path: 'x/alfa' },
      routes: [{ path: 'x/alfa', element: null }],
      about: {
        description: 'Alfa reads the alfas and says which are late.',
        reads: ['Alfas and their owners'],
        permissions: ['list alfas (alfa.example.com)'],
        requires: ['An Alfa operator'],
        screenshot: 'data:image/png;base64,iVBORw0KGgo=',
        gallery: [{ src: 'data:image/png;base64,iVBORw0KGgoA', caption: 'The Alfa tab on an alfa.' }],
      },
    },
    {
      key: 'delta',
      nav: { label: 'Delta Center', path: 'x/delta' },
      routes: [{ path: 'x/delta', element: null }],
      about: { description: 'Delta draws deltas.', reads: ['Deltas'], permissions: ['list deltas'], requires: [] },
    },
  ];

  function console(fail: readonly string[] = []): { sent: { path: string; body: unknown }[]; kept: Kept } {
    const kept: Kept = { alfaOn: false, deltaOn: false, column: true };
    const sent: { path: string; body: unknown }[] = [];
    stubFetch(shell(capabilities));
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      if ((init?.method ?? 'GET') === 'PUT') {
        const body: unknown = JSON.parse(String(init?.body ?? 'null'));
        sent.push({ path, body });
        if (fail.includes(path)) {
          const problem = { type: 'about:blank', title: 'Refused', status: 403, detail: 'not today', code: 'forbidden' };
          return Promise.resolve(
            new Response(JSON.stringify(problem), { status: 403, headers: { 'Content-Type': 'application/problem+json' } }),
          );
        }
        if (path === '/api/c/prod/extensions/alfa') kept.alfaOn = (body as { enabled: boolean }).enabled;
        if (path === '/api/c/prod/extensions/delta') kept.deltaOn = (body as { enabled: boolean }).enabled;
        if (path === '/api/c/prod/extensions/alfa/settings/list-column') kept.column = (body as { value: boolean }).value;
        return Promise.resolve(new Response(null, { status: 204 }));
      }
      if (path === '/api/c/prod/extensions') {
        return Promise.resolve(
          new Response(JSON.stringify(listing(kept)), { headers: { 'Content-Type': 'application/json' } }),
        );
      }
      return stubbed(input, init);
    });
    return { sent, kept };
  }

  function tile(key: string): HTMLElement {
    const found = page().querySelector<HTMLElement>(`[data-extra="${key}"]`);
    if (found === null) throw new Error(`no tile for ${key}`);
    return found;
  }
  function dialog(): HTMLElement {
    const found = document.querySelector<HTMLElement>('[role="dialog"]');
    if (found === null) throw new Error('no window is open');
    return found;
  }
  function button(name: string): HTMLButtonElement | undefined {
    return [...dialog().querySelectorAll<HTMLButtonElement>('button')].find((one) => one.textContent === name);
  }
  async function open(key: string): Promise<void> {
    await waitFor(() => {
      expect(page().querySelector(`[data-extra="${key}"] [data-extra-about]`)).not.toBeNull();
    });
    fireEvent.click(tile(key).querySelector('[data-extra-about]') as Element);
    await waitFor(() => {
      expect(document.querySelector(`[role="dialog"] [data-extra-preview="${key}"]`)).not.toBeNull();
    });
  }

  it('opens from the card with the name, what it does, reads, needs and a picture of it', async () => {
    console();
    mountApp('/settings/extra', shipped);
    await open('alfa');

    const window = dialog();
    expect(window.querySelector('h2')?.textContent).toBe('Alfa Center');
    expect(window.getAttribute('aria-labelledby')).toBe(window.querySelector('h2')?.id);
    expect(window.querySelector('[data-extra-preview-icon] svg use')?.getAttribute('href')).toBe('#i-sliders');
    expect(window.querySelector('[data-extra-preview-state]')?.textContent).toBe('Off');
    expect(window.querySelector('[data-extra-preview-description]')?.textContent).toBe(
      'Alfa reads the alfas and says which are late.',
    );
    expect(window.querySelector('[data-extra-preview-facts="reads"]')?.textContent).toContain('What it reads');
    expect(window.querySelector('[data-extra-preview-facts="reads"]')?.textContent).toContain('Alfas and their owners');
    expect(window.querySelector('[data-extra-preview-facts="permissions"]')?.textContent).toContain(
      'list alfas (alfa.example.com)',
    );
    expect(window.querySelector('[data-extra-preview-facts="requires"]')?.textContent).toContain('An Alfa operator');
    const picture = window.querySelector('img');
    expect(picture?.getAttribute('alt')).toBe('The Alfa Center page');
    // Its gallery, under the page, each picture with its caption.
    const more = window.querySelectorAll('[data-extra-preview-picture]');
    expect(more).toHaveLength(1);
    expect(more[0]?.querySelector('img')?.getAttribute('alt')).toBe('The Alfa tab on an alfa.');
    expect(more[0]?.querySelector('figcaption')?.textContent).toBe('The Alfa tab on an alfa.');
    // Opened straight from the card: no settings window behind it.
    expect(document.querySelector('[data-extra-modal]')).toBeNull();
  });

  it('draws no picture where the extra has none, and says when it needs nothing', async () => {
    console();
    mountApp('/settings/extra', shipped);
    await open('delta');
    expect(dialog().querySelector('img')).toBeNull();
    expect(dialog().querySelector('[data-extra-preview-facts="requires"]')?.textContent).toContain(
      'Nothing beyond the cluster itself.',
    );
    // An extra that names no glyph draws the generic one.
    expect(dialog().querySelector('[data-extra-preview-icon] svg use')?.getAttribute('href')).toBe('#i-extra');
  });

  it('has Close as its only button, and nothing in it switches the extra', async () => {
    const { sent } = console();
    mountApp('/settings/extra', shipped);
    for (const key of ['alfa', 'delta', 'bravo', 'charlie']) {
      await open(key);
      const buttons = [...dialog().querySelectorAll('button')].map((one) => one.textContent);
      expect(buttons).toEqual(['Close']);
      expect(dialog().querySelector('[role="switch"], input, select')).toBeNull();
      fireEvent.click(button('Close') as Element);
      await waitFor(() => {
        expect(document.querySelector('[role="dialog"]')).toBeNull();
      });
    }
    expect(sent).toEqual([]);
  });

  it('closes on Escape', async () => {
    console();
    mountApp('/settings/extra', shipped);
    await open('delta');
    fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' });
    await waitFor(() => {
      expect(document.querySelector('[role="dialog"]')).toBeNull();
    });
  });

  it('follows the switch pressed on the card', async () => {
    console();
    mountApp('/settings/extra', shipped);
    await waitFor(() => {
      expect(page().querySelector('[data-extra="delta"] [role="switch"]')).not.toBeNull();
    });
    fireEvent.click(tile('delta').querySelector('[role="switch"]') as Element);
    await waitFor(() => {
      expect(tile('delta').getAttribute('data-on')).toBe('true');
    });
    await open('delta');
    expect(dialog().querySelector('[data-extra-preview="delta"]')?.getAttribute('data-on')).toBe('true');
    expect(dialog().querySelector('[data-extra-preview-state]')?.textContent).toBe('On');
  });

  it("leaves the card's own switch working without opening a window", async () => {
    const { sent } = console();
    mountApp('/settings/extra', shipped);
    await waitFor(() => {
      expect(page().querySelector('[data-extra="delta"] [role="switch"]')).not.toBeNull();
    });
    fireEvent.click(tile('delta').querySelector('[role="switch"]') as Element);
    await waitFor(() => {
      expect(tile('delta').getAttribute('data-on')).toBe('true');
    });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(sent).toEqual([{ path: '/api/c/prod/extensions/delta', body: { enabled: true } }]);
  });

  it('draws Edit and About side by side on every card', async () => {
    console();
    mountApp('/settings/extra', shipped);
    await waitFor(() => {
      expect(page().querySelector('[data-extra="alfa"]')).not.toBeNull();
    });
    for (const key of ['alfa', 'bravo', 'charlie', 'delta']) {
      const edit = tile(key).querySelector('[data-extra-edit]');
      const about = tile(key).querySelector('[data-extra-about]');
      expect(edit?.textContent).toBe('Edit');
      expect(about?.textContent).toBe('About');
      expect(edit?.parentElement).toBe(about?.parentElement);
    }
    // The card draws its extra's own glyph.
    expect(tile('alfa').querySelector('[data-extra-icon]')?.getAttribute('data-extra-icon')).toBe('sliders');
    expect(tile('delta').querySelector('[data-extra-icon]')?.getAttribute('data-extra-icon')).toBe('extra');
  });

  it("says what the server's row says where the build carries no words for the extra", async () => {
    console();
    mountApp('/settings/extra');
    await open('alfa');
    expect(dialog().querySelector('[data-extra-preview-description]')?.textContent).toBe('Alfa things.');
    expect(dialog().querySelector('[data-extra-preview-facts="reads"]')).toBeNull();
    expect(dialog().querySelector('img')).toBeNull();
  });
});
