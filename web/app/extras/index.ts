import type { Extension } from '@kubetower/ui';

import { argocd } from './argocd';
import { certmanager } from './certmanager';
import { cnpg } from './cnpg';
import { dns } from './dns';
import { events } from './events';
import { kubeadjust } from './kubeadjust';
import { kubecron } from './kubecron';
import { netinspect } from './netinspect';
import { pending } from './pending';
import { redis } from './redis';
import { runtimeInspector } from './runtime';
import { security } from './security';

/**
 * Every extra this console ships, in the order `createApp` mounts them.
 *
 * Every extra is part of the console, each behind its own per-cluster
 * switch. The list is written out rather than assembled by
 * self-registration, so that one place says what ships. It is the TypeScript half
 * of `extras()` in cmd/kubetower/main.go, and the two lists name the same keys
 * in the same order. `app/main.tsx` hands it to `createApp`, and
 * `routes.test.ts` beside it checks this same list, so an extra added here is
 * checked without anyone remembering to add it there.
 */
export const extras: Extension[] = [certmanager, argocd, events, dns, runtimeInspector, kubeadjust, kubecron, netinspect, pending, cnpg, redis, security];
