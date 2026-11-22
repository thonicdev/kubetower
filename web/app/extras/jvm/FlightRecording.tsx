import { useEffect, useState, type ReactNode } from 'react';
import { Dialog, Failure, KeyLayer, Pending, Pill, btnPrimary, btnSecondary } from '@kubetower/ui';

import { InspectProblem, NO_JDK_TITLE, blocksJcmd, RECORDING_SECONDS, flightRecording, type HeapDumpAnswer, type RecordingSeconds } from './data';

/**
 * The Flight Recorder: pick a duration from the route's fixed list, confirm,
 * and the file downloads once the JVM has written it (`jfr.go`). Nothing runs
 * before Record in the confirmation. A recording's file opens in JDK Mission
 * Control; the console reads none of it.
 */

type Run =
  | { status: 'idle' }
  | { status: 'confirming'; pid?: number }
  | { status: 'recording'; seconds: RecordingSeconds }
  | { status: 'done'; size: number; filename: string }
  | { status: 'choose'; jvms: { pid: number; mainClass: string }[] }
  | { status: 'failed'; error: unknown };

/** The headings of the failures only this route answers. */
export const JFR_FAILURE_TITLE: Record<string, string> = {
  not_confirmed: 'Not confirmed',
  bad_seconds: 'Duration not allowed',
  recording_busy: 'A recording is already running',
  jfr_unavailable: 'No Flight Recorder in this JVM',
  jfr_failed: 'The JVM did not record',
  no_stream: 'This console cannot download from a container',
  no_read_back: 'No cat or rm in this image',
  no_space: 'No room in the container',
};

export const durationLabel = (s: number): string => (s < 60 ? `${String(s)} s` : `${String(s / 60)} min`);

function megabytes(n: number): string {
  return `${(n / (1024 * 1024)).toFixed(n < 10 * 1024 * 1024 ? 1 : 0)} MiB`;
}

function save(answer: Extract<HeapDumpAnswer, { state: 'file' }>): void {
  const url = URL.createObjectURL(answer.file.blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = answer.file.filename;
  a.click();
  setTimeout(() => {
    URL.revokeObjectURL(url);
  }, 0);
}

/** The recording's time running out: a bar and the seconds left. */
function Countdown({ seconds }: { seconds: number }): ReactNode {
  const [elapsed, setElapsed] = useState(0);
  useEffect(() => {
    const timer = setInterval(() => {
      setElapsed((e) => e + 1);
    }, 1000);
    return () => {
      clearInterval(timer);
    };
  }, []);
  const done = Math.min(elapsed, seconds);
  return (
    <div data-jvm-jfr-progress="" className="flex items-center gap-2 text-xs tabular-nums">
      <span className="relative h-2 w-48 rounded bg-white/10">
        <span
          className="absolute inset-y-0 left-0 rounded bg-plate-blue"
          style={{ width: `${String((done / seconds) * 100)}%` }}
        />
      </span>
      <span className="text-muted">{elapsed >= seconds ? 'writing…' : `${String(seconds - done)} s`}</span>
    </div>
  );
}

export function FlightRecordingSection({
  cluster,
  namespace,
  name,
  container,
  failureTitle,
  noJdk,
  offTitle = NO_JDK_TITLE,
  onNoJcmd,
}: {
  cluster: string;
  namespace: string;
  name: string;
  container: string;
  failureTitle: Record<string, string>;
  noJdk: boolean;
  offTitle?: string;
  onNoJcmd: (error?: unknown) => void;
}): ReactNode {
  const [run, setRun] = useState<Run>({ status: 'idle' });
  const [seconds, setSeconds] = useState<RecordingSeconds>(30);
  const go = async (pid?: number) => {
    setRun({ status: 'recording', seconds });
    try {
      const answer = await flightRecording(cluster, namespace, name, container, seconds, pid);
      if (answer.state === 'choose') {
        setRun({ status: 'choose', jvms: answer.jvms });
        return;
      }
      save(answer);
      setRun({ status: 'done', size: answer.file.blob.size, filename: answer.file.filename });
    } catch (error) {
      if (blocksJcmd(error)) onNoJcmd(error);
      setRun({ status: 'failed', error });
    }
  };
  const titles = { ...failureTitle, ...JFR_FAILURE_TITLE };
  const busy = run.status === 'recording';
  return (
    <section data-jvm-jfr="" className="flex flex-col gap-3 border-t border-line pt-3">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-xs font-semibold">Flight recorder</h3>
        <span role="group" aria-label="Duration" className="flex gap-1">
          {RECORDING_SECONDS.map((s) => (
            <button
              key={s}
              type="button"
              data-jvm-jfr-seconds={s}
              aria-pressed={seconds === s}
              disabled={busy}
              className={seconds === s ? btnPrimary : btnSecondary}
              onClick={() => {
                setSeconds(s);
              }}
            >
              {durationLabel(s)}
            </button>
          ))}
        </span>
        <button
          type="button"
          data-jvm-jfr-record=""
          className={btnSecondary}
          disabled={busy || container === '' || noJdk}
          title={
            noJdk ? offTitle : `jcmd JFR.start in container ${container}, the default profile (pods/exec, recorded in the audit log)`
          }
          onClick={() => {
            setRun({ status: 'confirming' });
          }}
        >
          Record
        </button>
        {run.status === 'done' ? (
          <span data-jvm-jfr-done="" className="text-xs text-ok tabular-nums" title={run.filename}>
            ✓ {megabytes(run.size)}
          </span>
        ) : null}
      </div>
      {run.status === 'confirming' ? (
        <Dialog
          title="Flight recording"
          layer={KeyLayer.Confirm}
          onClose={() => {
            setRun({ status: 'idle' });
          }}
        >
          <div data-jvm-jfr-confirm="" className="flex flex-col gap-3 text-sm">
            <div className="flex flex-wrap gap-2">
              <Pill tone="warn" title="The default profile records the JVM’s system properties, environment variables and command line, which may hold secrets">
                Holds environment variables
              </Pill>
              <Pill neutral="muted" title="Low overhead: the default profile">
                {durationLabel(seconds)} · default profile
              </Pill>
            </div>
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
              <button type="button" data-jvm-jfr-go="" className={btnPrimary} onClick={() => void go(run.pid)}>
                Record and download
              </button>
            </div>
          </div>
        </Dialog>
      ) : null}
      {run.status === 'recording' ? (
        <div className="flex flex-col gap-1">
          <Pending what="the recording" />
          <Countdown seconds={run.seconds} />
        </div>
      ) : null}
      {run.status === 'failed' ? (
        run.error instanceof InspectProblem ? (
          <p data-jvm-failure={run.error.code} className="font-medium text-warn" title={run.error.message}>
            {titles[run.error.code] ?? 'The recording failed'}
          </p>
        ) : (
          <Failure
            error={run.error}
            onRetry={() => {
              setRun({ status: 'confirming' });
            }}
          />
        )
      ) : null}
      {run.status === 'choose' ? (
        <ul data-jvm-choose="" className="flex flex-col gap-1">
          {run.jvms.map((j) => (
            <li key={j.pid}>
              <button
                type="button"
                className={btnSecondary}
                onClick={() => {
                  setRun({ status: 'confirming', pid: j.pid });
                }}
              >
                {j.mainClass} <span className="text-muted">(pid {j.pid})</span>
              </button>
            </li>
          ))}
        </ul>
      ) : null}
    </section>
  );
}
