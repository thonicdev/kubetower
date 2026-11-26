import { useId, useState, type ReactNode } from 'react';

import { useCapabilities, useResourceDetail } from '../api';
import { btn } from '../components/buttons';
import { Icon } from '../components/Icon';
import { useDock } from '../dock/DockContext';
import type { Tab } from '../dock/tabs';
import { debugOn, debugRequest, debugTargets } from './debug';
import { DebugForm } from './DebugForm';

/**
 * Debug, offered by a pod shell that found no shell (`no_shell`): the remedy,
 * where the refusal is read. The form opens with the container the shell was
 * asked for as the one whose processes the debug container joins.
 *
 * Gated as the drawer's Debug is (`debugOn`), on the pod's page - usually
 * already read, by the drawer the Shell was opened from - so a person who may
 * not add a container gets the control grayed with the server's reason, and a
 * person withheld it gets none.
 */
export function PaneDebug({ tab }: { tab: Tab }): ReactNode {
  const { openShell } = useDock();
  const able = useCapabilities().data;
  const detail = useResourceDetail(tab.cluster, 'pods', tab.namespace, tab.name);
  const answer = detail.data;
  const page = answer?.state === 'ok' ? answer.data : undefined;
  const at = debugOn(able, page?.debug, page !== undefined);
  const [open, setOpen] = useState(false);
  const panelId = useId();

  const { namespace, name: pod } = tab;
  if (!at.drawn || namespace === undefined || pod === undefined) return null;
  // The pod's running containers, and the one this shell was asked for even
  // where the page has not said it runs: it is the one the person meant.
  const targets = [...new Set([...(tab.container === undefined ? [] : [tab.container]), ...debugTargets(page?.containers).map((one) => one.name)])];

  return (
    <div className="flex min-w-0 flex-col items-start gap-1">
      <button
        type="button"
        data-debug-entry=""
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        disabled={at.refused !== undefined}
        title={at.refused ?? `Start a debug container in ${pod}`}
        onClick={() => setOpen((was) => !was)}
        className={`${btn} border-line text-muted hover:text-fg`}
      >
        <Icon name="debug" />
        Debug
      </button>
      {/* In the pane's own flow rather than a popover: the dock clips what
          leaves it, and the session above has ended, so the room is free. */}
      {open ? (
        <div
          id={panelId}
          role="group"
          aria-label="Debug container"
          className="w-72 max-w-full rounded-lg border border-line bg-panel-2 p-3"
        >
          <DebugForm
            targets={targets}
            initialTarget={tab.container}
            onStart={(choice) => {
              setOpen(false);
              openShell(debugRequest({ cluster: tab.cluster, namespace, pod }, choice));
            }}
          />
        </div>
      ) : null}
    </div>
  );
}
