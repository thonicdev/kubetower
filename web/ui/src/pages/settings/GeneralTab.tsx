import { useState, type FormEvent, type ReactNode } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';

import {
  previewClear,
  runClear,
  useSettingsNotKept,
  type ActOutcome,
  type ConfirmationPlan,
} from '../../api';
import { PlanDialog } from '../../components/PlanDialog';
import { btnDanger, btnPrimary } from '../../components/buttons';
import { focusRing, focusRingTight } from '../../focus';
import { MonitoringTile } from './MonitoringTile';
import { saveNodeShell, saveSettings, settingsKey, type Settings, type SettingsWrite } from './data';
import { formatDay } from '../../components/instant';

/**
 * General: what the archive keeps, and the node shell's image.
 *
 * **The three settings this tab has** are the archive's - keep cluster
 * events, capture pod output with them, and for how long.
 *
 * ## Only what changed travels
 *
 * `SettingsWrite`'s three members are all optional, and a write that says
 * nothing about a field must not re-assert its zero: `false` and "not said" are
 * different instructions about keeping events. This form sends the fields it
 * changed and no others.
 *
 * ## `persisted: false` is not a failure
 *
 * Every store here applies in memory first and then writes. A value that
 * validated is in effect whether or not the file could be written; reporting
 * that as an error makes a user retype a setting that is already working.
 * What they need is the narrower true thing, which is that it will not survive a
 * restart - and the server composes that sentence.
 */
export function GeneralTab({ settings }: { settings: Settings }): ReactNode {
  const notKept = useSettingsNotKept();
  const keeps = notKept === null;
  // **No archive at all where the state directory does not survive a restart**,
  // as the deployment says - not the "this build has no archive" notice, which
  // is a different fact, and not a form: seven days of retention on a disk
  // wiped at the next start is a control that lies. The Empty control goes
  // with it, whatever the archive envelope says: its address is not
  // registered there. Strictly `false`, so a server that predates the member
  // draws what it always drew. Checked first: it is the broader fact, and
  // what the chart deploys with several replicas.
  if (settings.info.statePersistent === false) {
    return (
      <div className="grid grid-cols-1 items-start gap-4 xl:grid-cols-2">
        <NothingKept />
        {keeps ? <NodeShellForm settings={settings} /> : null}
        <MonitoringTile />
      </div>
    );
  }
  // **Where several replicas share one address, none of these writes exists**:
  // the archive's settings, the node-shell image and the clear are files in one
  // pod's state directory, so `PUT /api/settings`, `PUT /api/settings/nodeshell`
  // and `POST /api/settings/clear` are not registered, and
  // `Capabilities.replicated` is the member the contract says so in - not
  // `preferences`, which the server profile also turns off on one replica. A form
  // that posted to one would answer 404 with nothing saying why - so the forms
  // are absent and one sentence says why, and what is only read stays.
  if (!keeps) {
    return (
      <div className="grid grid-cols-1 items-start gap-4 xl:grid-cols-2">
        <section data-settings="not-kept" className="rounded-lg border border-line bg-panel p-3 text-sm">
          <h2 className="text-base font-medium">Settings are not kept here</h2>
          {notKept === 'shared' ? (
            <p className="mt-1 text-muted">
              This console is shared by everybody who signs in to it. What the archive keeps and
              emptying the archive would change it for every one of them, so neither can be changed
              here: the deployment sets them.
            </p>
          ) : (
            <p className="mt-1 text-muted">
              This console runs as several replicas behind one address. What the archive keeps, the
              node shell&apos;s image and emptying the archive are each a file in one replica&apos;s
              state directory, so none of them can be changed here: the deployment sets them.
            </p>
          )}
        </section>
        <ArchiveFacts settings={settings} canEmpty={false} />
        <MonitoringTile />
      </div>
    );
  }
  return (
    <div className="grid grid-cols-1 items-start gap-4 xl:grid-cols-2">
      <ArchiveForm settings={settings} />
      <ArchiveFacts settings={settings} canEmpty />
      <NodeShellForm settings={settings} />
      <MonitoringTile />
    </div>
  );
}

/**
 * What this tab says when there is nothing it could keep.
 *
 * One sentence rather than an empty tab: under the server profile the node-shell
 * section is absent too, and a tab with nothing on it reads as a page that
 * failed to load - an empty answer drawn as a failed one. It names no archive
 * and draws no control: the absence is the point.
 */
function NothingKept(): ReactNode {
  return (
    <section
      data-state-persistent="false"
      className="rounded-lg border border-line bg-panel-2 p-3 text-sm"
    >
      <h2 className="text-base font-medium text-muted">Nothing here is kept</h2>
      <p className="mt-1 text-muted">
        This console&apos;s state directory does not survive a restart - the deployment says so -
        so it offers no setting that would be gone at the next one.
      </p>
    </section>
  );
}

function ArchiveForm({ settings }: { settings: Settings }): ReactNode {
  const cache = useQueryClient();
  const general = settings.general;

  const [enabled, setEnabled] = useState(general.archiveEnabled ?? false);
  const [logs, setLogs] = useState(general.captureLogs ?? false);
  const [days, setDays] = useState(general.retentionDays);

  const save = useMutation({
    mutationFn: (write: SettingsWrite) => saveSettings(write),
    onSuccess: (next) => {
      cache.setQueryData(settingsKey, next);
    },
  });

  // A build with no archive has nothing to switch. Absent rather than disabled:
  // a control that cannot work is worse than an absent one.
  if (settings.retentionChoices === undefined || general.retentionDays === undefined) {
    return (
      <section className="rounded-lg border border-line bg-panel p-3 text-sm">
        <h2 className="text-base font-medium">What the archive keeps</h2>
        <p className="mt-1 text-muted">
          This build has no archive, so there is nothing to keep and nothing to configure.
        </p>
      </section>
    );
  }

  function submit(event: FormEvent): void {
    event.preventDefault();
    // Only the fields that moved. See the file comment.
    const write: SettingsWrite = {};
    if (enabled !== general.archiveEnabled) write.archiveEnabled = enabled;
    if (logs !== general.captureLogs) write.captureLogs = logs;
    if (days !== general.retentionDays && days !== undefined) write.retentionDays = days;
    save.mutate(write);
  }

  const applied = settings.applied;

  return (
    <section className="rounded-lg border border-line bg-panel p-3">
      <h2 className="text-base font-medium">What the archive keeps</h2>
      <p className="mt-1 text-xs text-muted">
        A window of this cluster&apos;s own events, so an object the cluster has already forgotten
        can still be read. Turning it off stops writing and discards nothing.
      </p>

      <form onSubmit={submit} className="mt-3 flex flex-col gap-3">
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            className={focusRing}
            name="archiveEnabled"
            checked={enabled}
            onChange={(event) => {
              setEnabled(event.target.checked);
            }}
          />
          Keep cluster events
        </label>

        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            className={focusRing}
            name="captureLogs"
            checked={logs}
            onChange={(event) => {
              setLogs(event.target.checked);
            }}
          />
          Capture pod output alongside them
        </label>
        {/* The expensive half, said so. Events cost kilobytes a day per cluster;
            logs cost whatever the applications feel like printing. */}
        <p className="-mt-2 ml-6 text-xs text-muted">
          This is the expensive half: it writes production application output to a volume.
        </p>

        <label className="flex items-center gap-2 text-sm">
          Keep them for
          <select
            name="retentionDays"
            value={days}
            onChange={(event) => {
              setDays(Number(event.target.value));
            }}
            className={`rounded-md border border-line bg-panel-2 px-2 py-1 text-xs ${focusRing}`}
          >
            {/*
              **The list always contains the value it is editing**, and the
              server is what guarantees that. A control that cannot represent its
              own current state does not merely look wrong: with retention at 21
              no option carried `selected`, the browser submitted the first, and a
              Save opened to tick an unrelated checkbox destroyed twenty days of
              archived events while saying "Saved."
            */}
            {settings.retentionChoices.map((choice) => (
              <option key={choice.days} value={choice.days}>
                {choice.label}
              </option>
            ))}
          </select>
        </label>

        <div className="flex items-center gap-2">
          <button
            type="submit"
            disabled={save.isPending}
            className={btnPrimary}
          >
            {save.isPending ? 'Saving…' : 'Save'}
          </button>
          {save.isError ? (
            <span role="alert" className="text-xs text-bad">
              {save.error instanceof Error ? save.error.message : 'The save was refused.'}
            </span>
          ) : null}
          {/*
            The server's own sentence about what the write did, and the arm that
            matters is `persisted: false`: it is not a failure and is not drawn
            as one.
          */}
          {applied === undefined ? null : (
            <span
              role="status"
              data-persisted={applied.persisted ? 'true' : 'false'}
              className={`text-xs ${applied.persisted ? 'text-muted' : 'text-warn'}`}
            >
              {applied.note ?? (applied.persisted ? 'Saved.' : 'In effect, and not written down.')}
            </span>
          )}
        </div>
      </form>
    </section>
  );
}

/**
 * What the archive costs on disk, and whether it is writing at all.
 *
 * Its own envelope, because **an archive with nowhere to write is a state and
 * not a zero**: a page reporting 0 bytes over a refused write is the most
 * reassuring possible way to be wrong.
 */
function ArchiveFacts({ settings, canEmpty }: { settings: Settings; canEmpty: boolean }): ReactNode {
  const envelope = settings.archive;
  if (envelope === undefined) return null;

  if (envelope.state !== 'ok' || envelope.data === undefined) {
    return (
      <section
        data-archive={envelope.state}
        className="rounded-lg border border-line bg-panel-2 p-3 text-sm"
      >
        <h2 className="text-base font-medium text-muted">Nothing is being kept</h2>
        <p className="mt-1 text-muted">
          {envelope.reason?.message ??
            'The archive did not answer, and this page is not going to guess what that means.'}
        </p>
      </section>
    );
  }

  const facts = envelope.data;
  return (
    <section data-archive="ok" className="rounded-lg border border-line bg-panel p-3">
      <div className="flex flex-wrap items-baseline gap-2">
        <h2 className="text-base font-medium">What it costs</h2>
        {/* Graded, and it is the one pill on this tab: an archive that has
            stopped writing is the fact the figures beside it have to be read
            against. */}
        {facts.writesRefused === true ? (
          <span className="rounded-sm bg-bad/15 px-1.5 py-0.5 text-2xs text-bad">
            writes refused
          </span>
        ) : (
          <span className="rounded-sm bg-ok/15 px-1.5 py-0.5 text-2xs text-ok">writing</span>
        )}
      </div>

      <dl className="mt-2 grid grid-cols-2 gap-x-4 gap-y-1 text-xs sm:grid-cols-4">
        <Fact label="On disk" value={bytes(facts.bytes)} />
        {/* The days the archive holds something for, counted by the server as
            the day folders on disk - not the span since the oldest. A day
            nothing was written - the console off, or nothing happening - has
            none, so "Days held 4" beside "Oldest Oct 9" on
            Oct 16 would read as a contradiction; the label says which count
            it is. */}
        <Fact label="Days with data" value={facts.days === undefined ? '—' : String(facts.days)} />
        <Fact
          label="Oldest"
          value={facts.oldestAt === undefined ? '—' : formatDay(facts.oldestAt)}
        />
        <Fact label="Where" value={facts.path ?? '—'} />
      </dl>

      {facts.note === undefined ? null : (
        <p className="mt-2 break-words text-xs text-muted">{facts.note}</p>
      )}

      {canEmpty ? <EmptyArchive /> : null}
    </section>
  );
}

/**
 * Empty the archive.
 *
 * ## Nothing here composes the sentence or picks the gesture
 *
 * Pressing this **previews**: the server walks the disk once, and the plan comes
 * back carrying what is there, what is lost, and what has to be typed. The
 * dialog draws that plan. This component chooses none of it - which is the point
 * of confirm-then-act, and the reason the recap is *read, never inferred*: a
 * figure the front end worked out separately from the figure the server checks
 * is a confirmation nobody can satisfy, on the one page whose subject is
 * deleting things.
 *
 * The gesture the server asks for is to **type how many days this
 * archive holds**. It cannot be answered from muscle memory, because the answer
 * is different on every console and on every day.
 *
 * ## Why it is the only destructive control on this page
 *
 * The other `ClearRequest` scopes are preferences - a column width, a rail
 * tile - and they are rebuilt by using the console. This is the only store here
 * holding something no other system has: the events are gone from the cluster
 * within the hour. That is why `archive` is not part of `all`, and why this
 * button says what it will destroy before it asks.
 */
function EmptyArchive(): ReactNode {
  const [plan, setPlan] = useState<ConfirmationPlan | undefined>(undefined);
  const [outcome, setOutcome] = useState<ActOutcome | undefined>(undefined);
  const [failure, setFailure] = useState<string | null>(null);
  const cache = useQueryClient();

  const review = useMutation({
    mutationFn: () => previewClear('archive'),
    onSuccess: (next) => {
      setFailure(null);
      setPlan(next);
    },
    onError: (error: Error) => setFailure(error.message),
  });

  const commit = useMutation({
    mutationFn: (confirmed?: string) => runClear('archive', plan?.token ?? '', confirmed),
    onSuccess: (next) => {
      setOutcome(next);
      // The figures on this page are what just changed, so they are re-read
      // rather than patched: this component does not know what the walk will
      // now say, and writing a zero here would be inventing it.
      void cache.invalidateQueries({ queryKey: settingsKey });
    },
    onError: (error: Error) => setFailure(error.message),
  });

  const close = (): void => {
    setPlan(undefined);
    setOutcome(undefined);
    setFailure(null);
  };

  return (
    <div className="mt-3 flex flex-wrap items-center gap-3">
      <button
        type="button"
        disabled={review.isPending}
        onClick={() => review.mutate()}
        className={btnDanger}
      >
        Empty the archive…
      </button>
      {/* The ellipsis is not decoration: it is the promise that this asks
          first, and the control would be lying without it. */}
      <span className="text-xs text-muted">
        Deletes every kept event and log line. They are gone from the cluster; this is the only
        copy.
      </span>

      {failure === null || plan !== undefined ? null : (
        <span role="alert" className="text-bad text-xs">
          {failure}
        </span>
      )}

      {plan === undefined ? null : (
        <PlanDialog
          title="Empty the archive?"
          plan={plan}
          pending={commit.isPending}
          error={failure}
          outcome={outcome}
          onCancel={close}
          onApply={(confirmed) => commit.mutate(confirmed)}
        />
      )}
    </div>
  );
}

function Fact({ label, value }: { label: string; value: string }): ReactNode {
  return (
    <div className="min-w-0">
      <dt className="text-2xs text-muted uppercase">{label}</dt>
      <dd className="truncate tabular-nums" title={value}>
        {value}
      </dd>
    </div>
  );
}

/** Bytes, the way a settings page should show them. Binary units, because that
 *  is what a filesystem reports and a page that said 4.2 MB over 4 MiB would
 *  disagree with `du`. */
function bytes(value: number | undefined): string {
  if (value === undefined) return '—';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let n = value;
  let unit = 0;
  while (n >= 1024 && unit < units.length - 1) {
    n /= 1024;
    unit += 1;
  }
  return `${unit === 0 ? String(n) : n.toFixed(1)} ${units[unit] ?? 'B'}`;
}

/**
 * The node shell's image.
 *
 * One field and one Save, **with the default shown** so the user can see
 * what they are moving away from and can empty the box to return to it. Without
 * `defaultImage` the placeholder is blank and "leave it empty to restore the
 * default" is an instruction about a value nobody can see.
 *
 * **Absent where the build has no terminal layer**, rather than drawn as a field
 * that refuses: a control that cannot work is worse than an absent one, and
 * `available: false` is what says so.
 */
function NodeShellForm({ settings }: { settings: Settings }): ReactNode {
  const cache = useQueryClient();
  const shell = settings.nodeShell;
  const [image, setImage] = useState(shell.image ?? '');

  const save = useMutation({
    mutationFn: (next: string) => saveNodeShell({ enabled: shell.enabled, image: next }),
    onSuccess: (next) => {
      cache.setQueryData(settingsKey, { ...settings, nodeShell: next });
    },
  });

  if (shell.available !== true) return null;

  return (
    <section className="rounded-lg border border-line bg-panel p-3">
      <h2 className="text-base font-medium">Node shell</h2>
      <p className="mt-1 text-xs text-muted">
        A node cannot be <code className="font-mono">exec</code>&apos;d into, so opening a shell on
        one creates a pod on that node — sharing its process, network and IPC namespaces, with the
        node&apos;s root filesystem at <code className="font-mono">/host</code>. This is the image
        that pod runs. It needs <code className="font-mono">/bin/sh</code> and nothing else, and the
        node&apos;s own container runtime is what pulls it, so it has to be a reference that node can
        reach.
      </p>

      <form
        onSubmit={(event) => {
          event.preventDefault();
          save.mutate(image);
        }}
        className="mt-3 flex flex-col gap-2"
      >
        <label className="flex items-center gap-2 text-sm">
          Image
          <input
            name="image"
            value={image}
            onChange={(event) => {
              setImage(event.target.value);
            }}
            placeholder={shell.defaultImage}
            autoComplete="off"
            spellCheck={false}
            className={`w-72 rounded-md border border-line bg-panel-2 px-2 py-1 font-mono text-xs ${focusRingTight}`}
          />
        </label>
        {shell.defaultImage === undefined ? null : (
          <p className="text-xs text-muted">
            Default: <code className="font-mono">{shell.defaultImage}</code>. Empty the field and
            save to go back to it.
          </p>
        )}
        <div className="flex items-center gap-2">
          <button
            type="submit"
            disabled={save.isPending}
            className={`w-fit ${btnPrimary}`}
          >
            {save.isPending ? 'Saving…' : 'Save'}
          </button>
          {/* The image validated or it did not, and that is a different failure
              from the file not being written. The server reports the sentence
              rather than swallowing it. */}
          {save.isError ? (
            <span role="alert" className="text-xs text-bad">
              {save.error instanceof Error ? save.error.message : 'The image was refused.'}
            </span>
          ) : null}
        </div>
      </form>
    </section>
  );
}
