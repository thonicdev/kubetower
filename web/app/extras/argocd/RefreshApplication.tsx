import { useQueryClient } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { Dialog, KeyLayer, btnPrimary, btnSecondary, request } from '@kubetower/ui';

import { said } from './data';

/** The word the route asks for once the person confirmed: `Host.ConfirmToken`. */
export const CONFIRM_WORD = 'confirm';

/** Asks Argo CD to compare one Application again, after the person confirmed. */
export async function refreshApplication(cluster: string, namespace: string, name: string): Promise<void> {
  const path = `/x/${encodeURIComponent(cluster)}/argocd/applications/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/refresh`;
  try {
    await request(`${path}?${new URLSearchParams({ confirm: CONFIRM_WORD }).toString()}`, { method: 'POST' });
  } catch (error) {
    throw said(error);
  }
}

type Run = { status: 'idle' } | { status: 'confirming' } | { status: 'sending' } | { status: 'failed'; message: string };

/**
 * Refresh: a button, a confirmation, and the answer. The write is the
 * console's reviewed door (`Host.RefreshApplication`); a refusal is said in
 * the console's words, under the button.
 */
export function RefreshApplication({
  cluster,
  namespace,
  name,
  label = 'Refresh',
}: {
  cluster: string;
  namespace: string;
  name: string;
  label?: string;
}): ReactNode {
  const [run, setRun] = useState<Run>({ status: 'idle' });
  const client = useQueryClient();

  const go = async (): Promise<void> => {
    setRun({ status: 'sending' });
    try {
      await refreshApplication(cluster, namespace, name);
    } catch (error) {
      setRun({ status: 'failed', message: error instanceof Error ? error.message : 'The refresh could not be made.' });
      return;
    }
    setRun({ status: 'idle' });
    void client.invalidateQueries({ queryKey: ['argocd'] });
    void client.invalidateQueries({ queryKey: ['extension-column'] });
  };

  return (
    <span data-argo-refresh="" className="inline-flex flex-col items-start gap-1">
      <button
        type="button"
        data-argo-refresh-open=""
        disabled={run.status === 'sending'}
        className={btnSecondary}
        onClick={() => {
          setRun({ status: 'confirming' });
        }}
      >
        {label}
      </button>
      {run.status === 'failed' ? (
        <span role="alert" data-argo-refresh-error="" className="text-xs text-bad">
          {run.message}
        </span>
      ) : null}
      {run.status === 'confirming' ? (
        <Dialog
          title={`Refresh ${name}`}
          layer={KeyLayer.Confirm}
          onClose={() => {
            setRun({ status: 'idle' });
          }}
        >
          <div data-argo-refresh-confirm="" className="flex flex-col gap-3 text-sm">
            <p>
              Argo CD compares <strong>{name}</strong> again now.
            </p>
            <div className="flex justify-end gap-2">
              <button
                type="button"
                className={btnSecondary}
                onClick={() => {
                  setRun({ status: 'idle' });
                }}
              >
                Cancel
              </button>
              <button type="button" data-argo-refresh-go="" className={btnPrimary} onClick={() => void go()}>
                Refresh
              </button>
            </div>
          </div>
        </Dialog>
      ) : null}
    </span>
  );
}
