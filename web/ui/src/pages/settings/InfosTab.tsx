import { useMutation, useQueryClient } from '@tanstack/react-query';
import type { ReactNode } from 'react';

import { useCapabilities } from '../../api/queries';
import { btnSecondary } from '../../components/buttons';
import { Pill } from '../../components/Pill';
import { externalHref } from '../../externalHref';
import { focusRing } from '../../focus';

import { checkUpdates, settingsKey, useUpdateWatch, type Settings } from './data';
import { openWhatsNew } from '../../whatsnew/WhatsNew';
import { InstallUpdatesSetting, UpdateControl } from './UpdateControl';

/**
 * Infos: which version this is, whether a newer one exists, and where
 * KubeTower lives - its repository, its issues and its documentation.
 *
 * It is deliberately not a page of explanations: nothing about the listening
 * address, signing in, cluster access, the kubeconfig files or the stored state
 * is drawn here. The server still sends those fields (`Settings['info']`):
 * General reads `statePersistent`, the start-up log says the rest, and the
 * contract is not narrowed for a page that does not draw them. The three links
 * are links, not a section of explanations.
 */
export function InfosTab({ info }: { info: Settings['info'] }): ReactNode {
  // The scheduled check's answer reaches an open page without a remount.
  useUpdateWatch();
  const version = info.version === undefined || info.version === '' ? undefined : info.version;
  return (
    // Wide enough for the whole `--version` line on one row:
    // `kubetower 0.7.2 (2026-09-11, windows/amd64)` and a development build's
    // commit and toolchain wrapped at 448px.
    <section className="max-w-4xl rounded-lg border border-line bg-panel p-3" data-infos="">
      <div className="flex items-center gap-3">
        <h2 className="text-base font-medium">Version</h2>
        <WhatsNewButton />
      </div>
      <p className="mt-1 flex flex-wrap items-center gap-2">
        {version === undefined ? (
          <span className="text-sm text-muted">Not stamped</span>
        ) : (
          <span className="font-mono text-sm break-words" data-version="">
            {version}
          </span>
        )}
        {/* A separate flag rather than something read back out of the string:
            a page parsing a value it was handed is a second opinion about it. */}
        {info.development === true ? (
          <Pill tone="warn" title="Not stamped by the release script">
            development build
          </Pill>
        ) : null}
      </p>
      <Update update={info.update} />
      <Links repositoryUrl={info.repositoryUrl} documentationUrl={info.documentationUrl} />
    </section>
  );
}

/** Opens the What's new banner again (#80), where this build has a release. */
function WhatsNewButton(): ReactNode {
  const release = useCapabilities().data?.release;
  if (release === undefined || release === '') return null;
  return (
    <button type="button" className={btnSecondary} onClick={openWhatsNew} data-whats-new-open="">
      What&apos;s new
    </button>
  );
}

/**
 * The repository, its issues and the documentation, each leaving the console.
 *
 * Both addresses are the server's (`update.Repository` and Help ▸ KubeTower
 * Help's `helpURL`), so the page holds no second spelling of either; the issues
 * are the repository's own `/issues`. Each goes through `externalHref` like the
 * release link: only an http or https address is a link, any other is drawn as
 * the text it is, and one that was not sent is left out rather than drawn dead.
 */
function Links({
  repositoryUrl,
  documentationUrl,
}: {
  repositoryUrl: string | undefined;
  documentationUrl: string | undefined;
}): ReactNode {
  const repository = externalHref(repositoryUrl);
  const links: { label: string; raw: string; href: string | undefined }[] = [];
  if (repositoryUrl !== undefined && repositoryUrl !== '') {
    links.push({ label: 'GitHub repository', raw: repositoryUrl, href: repository });
  }
  if (repository !== undefined) {
    const issues = `${repository.replace(/\/+$/, '')}/issues`;
    links.push({ label: 'Issues', raw: issues, href: issues });
  }
  if (documentationUrl !== undefined && documentationUrl !== '') {
    links.push({ label: 'Documentation', raw: documentationUrl, href: externalHref(documentationUrl) });
  }
  if (links.length === 0) {
    return null;
  }
  return (
    <ul className="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs" data-infos-links="">
      {links.map((one) => (
        <li key={one.label}>
          {one.href === undefined ? (
            <span className="break-all text-muted" data-refused-href="">
              {one.raw}
            </span>
          ) : (
            <a
              href={one.href}
              target="_blank"
              rel="noopener noreferrer"
              className={`text-accent underline underline-offset-2 ${focusRing}`}
            >
              {one.label}
            </a>
          )}
        </li>
      ))}
    </ul>
  );
}

/**
 * Whether a newer release exists, in a few words.
 *
 * **"Could not check" and "up to date" must not look the same** (an empty
 * answer and a failed one are drawn differently): a failed check drawn as quiet reassurance
 * confirms something nothing established. So each of the five states has its
 * own words, `failed` is warn-colored, and the server's full sentence - the
 * one the start-up log writes - is on hover rather than on the page.
 *
 * Where this console can install, Download sits beside the link
 * (`UpdateControl.tsx`): the two clicks of #48.
 */
const WORDS: Readonly<Record<string, { text: string; tone: string }>> = {
  current: { text: 'Up to date', tone: 'text-ok' },
  failed: { text: 'Could not check for updates', tone: 'text-warn' },
  off: { text: 'Update check off', tone: 'text-muted' },
  notApplicable: { text: 'No update check for this build', tone: 'text-muted' },
};

function Update({ update }: { update: Settings['info']['update'] }): ReactNode {
  // Nothing checks at all - no checker was wired in. An empty line here would
  // report a state that does not exist.
  if (update === undefined) {
    return null;
  }
  return (
    <>
      <UpdateLine update={update} />
      {/* The action on a line of its own, never run into the version line's
          link (#96). */}
      <div className="mt-1" data-update-action="">
        <UpdateControl />
      </div>
      {ASKABLE.has(update.state) ? <CheckNow checking={update.checking} /> : null}
      <InstallUpdatesSetting />
    </>
  );
}

/**
 * The states Check now can change. `off` and
 * `notApplicable` make no request however they are asked - the server answers
 * them before any socket - so a button there would be a control for nothing.
 */
const ASKABLE: ReadonlySet<string> = new Set(['current', 'newer', 'failed']);

/**
 * Check now: ask GitHub past the day the answer is kept for, and draw the
 * running check while it is out - this press, or the one the server runs at
 * startup and on its schedule (`checking`, which the settings read polls on;
 * `useUpdateWatch` reads again while the page is open).
 * The answer replaces the update line in place; nothing else on the page moves.
 */
function CheckNow({ checking }: { checking: boolean }): ReactNode {
  const cache = useQueryClient();
  const check = useMutation({
    mutationFn: checkUpdates,
    onSuccess: (next) => {
      // An answer without `install` (an older server) keeps the one the page
      // holds: a ready download, its Install and the checkbox survive a check
      // (#96).
      cache.setQueryData<Settings>(settingsKey, (was) => {
        if (was === undefined) return was;
        const install = next.install ?? was.info.update?.install;
        return { ...was, info: { ...was.info, update: install === undefined ? next : { ...next, install } } };
      });
    },
  });
  const running = check.isPending || checking;
  const failure = check.isError ? (check.error instanceof Error ? check.error.message : String(check.error)) : undefined;
  return (
    <p className="mt-1 flex flex-wrap items-center gap-x-2 text-xs" data-update-check="">
      <button
        type="button"
        disabled={running}
        onClick={() => check.mutate()}
        className={btnSecondary}
      >
        Check now
      </button>
      {running ? (
        <span className="text-muted" role="status" data-update-checking="">
          Checking for updates…
        </span>
      ) : null}
      {failure === undefined ? null : (
        <span className="text-warn" data-update-check-failed="">
          The check could not be asked: {failure}
        </span>
      )}
    </p>
  );
}

function UpdateLine({ update }: { update: NonNullable<Settings['info']['update']> }): ReactNode {
  const hover = update.note === undefined || update.note === '' ? {} : { title: update.note };

  if (update.state === 'newer') {
    // `releaseUrl` is GitHub's `html_url` - the release page, with its notes
    // and its downloads, not a file - so the link says "View release". It is
    // copied unvalidated
    // (`internal/update/update.go`): only an http or https one is a link
    // (`externalHref`), any other is drawn as the text it is.
    const release = externalHref(update.releaseUrl);
    return (
      <p className="mt-2 flex flex-wrap items-baseline gap-x-2 text-xs" data-update="newer" {...hover}>
        <span className="text-accent">
          {update.latest === undefined || update.latest === '' ? 'A newer version' : update.latest} available
        </span>
        {release !== undefined ? (
          <a
            href={release}
            target="_blank"
            rel="noopener noreferrer"
            className={`text-accent underline underline-offset-2 ${focusRing}`}
          >
            View release
          </a>
        ) : update.releaseUrl === undefined || update.releaseUrl === '' ? null : (
          <span className="break-all text-muted" data-refused-href="">
            {update.releaseUrl}
          </span>
        )}
      </p>
    );
  }

  // A state this page has no words for is named rather than guessed at.
  const words = WORDS[update.state] ?? { text: `Update check: ${update.state}`, tone: 'text-muted' };
  // Why it could not check is the one thing a user can act on, so a
  // failure carries the server's sentence on the page, not only on hover.
  const why = update.state === 'failed' && update.note !== undefined && update.note !== '' ? update.note : undefined;
  return (
    <p
      className="mt-2 flex flex-wrap items-baseline gap-x-2 text-xs"
      data-update={update.state}
      {...(why === undefined ? hover : {})}
    >
      <span className={words.tone}>{words.text}</span>
      {why === undefined ? null : (
        <span className="text-muted" data-update-why="">
          {why}
        </span>
      )}
    </p>
  );
}
