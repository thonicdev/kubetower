import { useParams } from 'react-router';
import type { ReactNode } from 'react';

import { useClusters } from '../api';
import { ClusterName } from '../components/ClusterName';
import { ListView } from '../state/Result';
import { StateNotice } from '../state/StateNotice';
import { stateWord } from '../shell/clusterState';

/**
 * What `/c/:cluster` shows before a kind has been chosen.
 *
 * **Deliberately not a page.** The resource lists and the detail view are pages
 * of their own, and nothing resembling one belongs here. What this does carry
 * is the one thing the shell owes an operator who has just arrived on a
 * cluster: whether the console can talk to it.
 *
 * ## A name that is in the URL and not in the list
 *
 * That is `absent` - the thing that would serve this is not installed - and it is
 * not `unreachable`, not `empty`, and not a blank panel. A bookmark to a context
 * that has since been removed from the `KUBECONFIG` is the ordinary way to get
 * here, and "there is no such cluster" is a different sentence from "the cluster
 * did not answer", which is the distinction this product exists around.
 */
export function ClusterHome(): ReactNode {
  const { cluster } = useParams<{ cluster: string }>();
  const clusters = useClusters();

  return (
    <div className="p-4">
      <ListView query={clusters} what="clusters">
        {(items) => {
          const here = items.find((entry) => entry.name === cluster);

          if (here === undefined) {
            return (
              <StateNotice
                state="absent"
                reason={{
                  code: 'no_such_kind',
                  message: 'This console is not configured for that context.',
                  scope: cluster === undefined ? {} : { cluster },
                }}
              />
            );
          }

          return (
            <>
              {/* One line each, cut in the middle: an ARN under a nameplate
                  would otherwise wrap across the page. */}
              <h1 className="flex min-w-0 text-xl font-medium">
                <ClusterName name={here.label} />
              </h1>
              <p className="mt-1 flex min-w-0 font-mono text-xs text-muted">
                <ClusterName name={here.name} />
              </p>

              {here.state === 'ok' ? (
                <p className="mt-4 text-sm text-muted">Choose a kind from the menu.</p>
              ) : (
                <div className="mt-4">
                  <StateNotice state={here.state} reason={here.reason} />
                  <p className="mt-2 text-xs text-muted">
                    The menu beside this may be short or missing for the same reason:{' '}
                    {stateWord(here.state)}.
                  </p>
                </div>
              )}
            </>
          );
        }}
      </ListView>
    </div>
  );
}
