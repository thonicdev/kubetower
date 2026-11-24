import type { components } from '../ui/src/api/schema';

/**
 * Settings with a newer release and the desktop's installer at each step
 * (#48), for the `console-update-<phase>` scenes: the badge on the top bar's
 * Settings, the banner atop Settings and the Infos line. Every value is invented.
 */
type UpdateInstall = components['schemas']['UpdateInstall'];

const MB = 1024 * 1024;

type Step = 'idle' | 'downloading' | 'verifying' | 'ready' | 'installing' | 'failed';

export const UPDATE_STEPS: Readonly<Record<Step, UpdateInstall>> = {
  idle: { phase: 'idle', enabled: true, supported: true, installation: 'linux-appimage', ends: { terminals: 0, forwards: 0 } },
  downloading: {
    phase: 'downloading', enabled: true, supported: true, installation: 'linux-appimage',
    version: '0.1.3', asset: 'kubetower-0.1.3-x86_64.AppImage', done: 14.2 * MB, total: 38 * MB,
    ends: { terminals: 0, forwards: 0 },
  },
  verifying: {
    phase: 'verifying', enabled: true, supported: true, installation: 'linux-appimage',
    version: '0.1.3', asset: 'kubetower-0.1.3-x86_64.AppImage', ends: { terminals: 0, forwards: 0 },
  },
  ready: {
    phase: 'ready', enabled: true, supported: true, installation: 'linux-appimage',
    version: '0.1.3', asset: 'kubetower-0.1.3-x86_64.AppImage', ends: { terminals: 2, forwards: 1 },
  },
  installing: {
    phase: 'installing', enabled: true, supported: true, installation: 'linux-appimage',
    version: '0.1.3', ends: { terminals: 2, forwards: 1 },
  },
  failed: {
    phase: 'failed', enabled: true, supported: true, installation: 'linux-appimage',
    reason: 'kubetower-0.1.3-x86_64.AppImage does not match its signed sha256', ends: { terminals: 0, forwards: 0 },
  },
};

export function updateSettings(install: UpdateInstall): Readonly<Record<string, unknown>> {
  return {
    '/api/settings': {
      general: { archiveEnabled: true, captureLogs: false, retentionDays: 7 },
      retentionChoices: [{ days: 7, label: '7 days' }],
      archive: { state: 'ok', data: { path: '/state/archive', bytes: 4 * MB, days: 6, writesRefused: false, note: '' } },
      nodeShell: { enabled: true, available: true, image: 'busybox:1.36', defaultImage: 'alpine:3.24' },
      info: {
        statePath: '/home/user/.local/state/kubetower',
        statePersistent: true,
        kubeconfigFiles: ['/home/user/.kube/config'],
        listenAddress: '127.0.0.1:5823',
        withoutPassword: true,
        version: 'kubetower 0.1.2 (2026-11-23, linux/amd64)',
        development: false,
        repositoryUrl: 'https://github.com/thonicdev/kubetower',
        documentationUrl: 'https://example.com/docs',
        update: {
          state: 'newer',
          latest: '0.1.3',
          releaseUrl: 'https://github.com/thonicdev/kubetower/releases/tag/0.1.3',
          checkedAt: '2026-11-23T08:00:00Z',
          note: 'KubeTower 0.1.3 has been released. This build is 0.1.2.',
          checking: false,
          install,
        },
      },
    },
    '/api/settings/update': install,
  };
}
