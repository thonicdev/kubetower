import type { ReactNode } from 'react';
import { Link, useParams } from 'react-router';

import { clusterPath } from '../../clusters/clusterPath';
import { focusRing } from '../../focus';
import { useShell } from '../../shell/ShellContext';
import { StateNotice } from '../../state/StateNotice';

/**
 * An address under `/c/:cluster/x/` that no extension in this build answers.
 *
 * `x` is reserved for extensions (`../../router/pages.ts`), and an extension's
 * own routes outrank the `x` and `x/:key/*` this is mounted at, so it is only
 * ever reached when nothing registered claimed the address - either no extension
 * has that key, or one does and this is not one of its addresses. Without it the
 * address would fall through to `:kind/:name` and blame the cluster, which was
 * never asked anything. A console that blames a cluster for its own build is
 * wrong about the cause, so this page says what is actually true.
 *
 * **`absent`, with `not_installed`**, because that is the fact: the page is not
 * installed in this console. The extra's page is compiled into the build that
 * serves it, and this one was built without it. It is not `unreachable` (no
 * cluster was involved) and not `unreadable` (nothing was read). The sentence
 * says *console build* in so many words, so `not_installed` cannot be read as
 * "the cluster lacks something".
 *
 * Nothing is fetched. A read here could only fail in some other way and put a
 * second, misleading notice beside this one.
 */
/**
 * Not `absent`'s own *Not installed*: an extra's page uses that word when the
 * thing it reads is missing from the cluster, and this page is about the
 * console. Same state, same tone, a title that names the right subject.
 */
const TITLE = 'No such page in this console';

export function NoExtensionPage(): ReactNode {
  const { cluster = '', key = '', '*': rest = '' } = useParams();
  const { extensions } = useShell();
  const below = rest.split('/').filter((segment) => segment !== '');
  const known = extensions.find((extension) => extension.key === key);

  if (known !== undefined) {
    // **The extra is in this build; this address is not one of its pages.** A
    // mistyped or stale sub-path of a registered extra lands here too, because
    // the extra's own routes did not claim it - and "this build has no page for
    // the extra" would then be false about an extra that is right there. Absent
    // with `unclassified`, the code `ObjectPage` uses for an address that names
    // nothing: none of the classified reasons is about a path, and borrowing one
    // would name a wrong cause. The extra's own page is offered when it is not
    // the address already being looked at.
    const root = `${clusterPath(cluster)}/x/${key}`;
    return (
      <div>
        <StateNotice
          state="absent"
          title={TITLE}
          reason={{
            code: 'unclassified',
            message: `The extra "${known.nav.label}" has no page at this address. Nothing was asked of the cluster.`,
          }}
        />
        {below.length > 0 ? (
          <Link to={root} className={`mt-2 inline-block text-xs text-accent underline ${focusRing}`}>
            Open {known.nav.label}
          </Link>
        ) : null}
      </div>
    );
  }

  const message =
    key === ''
      ? 'This address names no extra. An extra has its page at /c/<cluster>/x/<key>.'
      : `This console build has no page for the extra "${key}". An extra's page is compiled into the console that serves it, and this build was made without it - the cluster was not asked anything.`;

  return <StateNotice state="absent" title={TITLE} reason={{ code: 'not_installed', message }} />;
}
