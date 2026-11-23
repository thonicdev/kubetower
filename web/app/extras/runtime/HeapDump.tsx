import { useState, type ReactNode } from 'react';
import { Dialog, Failure, KeyLayer, Pending, btnPrimary, btnSecondary } from '@kubetower/ui';

import { InspectProblem, NO_JDK_TITLE, blocksJcmd, heapDump, type HeapDumpAnswer } from './data';

/**
 * The heap dump: a button, a confirmation that says in words what the file
 * holds and what the dump costs, then the download. Nothing runs before the
 * second click (`heapdump.go`).
 */

type Run =
  | { status: 'idle' }
  | { status: 'confirming'; pid?: number }
  | { status: 'running' }
  | { status: 'done'; size: number; filename: string }
  | { status: 'choose'; jvms: { pid: number; mainClass: string }[] }
  | { status: 'failed'; error: unknown };

/** The headings of the failures only this route answers. */
export const HEAP_FAILURE_TITLE: Record<string, string> = {
  not_confirmed: 'Not confirmed',
  no_stream: 'This console cannot download from a container',
  no_read_back: 'No cat or rm in this image',
  no_space: 'No room in the container',
  heap_too_large: 'Heap dump too large',
  heap_dump_failed: 'Heap dump failed',
};

/** Saves the file through an anchor, then lets the browser forget it. */
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

function megabytes(n: number): string {
  return `${(n / (1024 * 1024)).toFixed(n < 10 * 1024 * 1024 ? 1 : 0)} MiB`;
}

export function HeapDumpSection({
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
  const go = async (pid?: number) => {
    setRun({ status: 'running' });
    try {
      const answer = await heapDump(cluster, namespace, name, container, pid);
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
  const titles = { ...failureTitle, ...HEAP_FAILURE_TITLE };
  return (
    <section data-jvm-heap="" className="flex flex-col gap-3 border-t border-line pt-3">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-xs font-semibold">Heap</h3>
        <button
          type="button"
          data-jvm-heap-dump=""
          className={btnSecondary}
          disabled={run.status === 'running' || container === '' || noJdk}
          title={noJdk ? offTitle : `jcmd GC.heap_dump in container ${container} (pods/exec, recorded in the audit log)`}
          onClick={() => {
            setRun({ status: 'confirming' });
          }}
        >
          {run.status === 'running' ? 'Dumping…' : 'Heap dump'}
        </button>
        {run.status === 'done' ? (
          <span data-jvm-heap-done="" className="text-xs text-ok tabular-nums" title={run.filename}>
            ✓ {megabytes(run.size)}
          </span>
        ) : null}
      </div>
      {run.status === 'confirming' ? (
        <Dialog
          title="Heap dump"
          layer={KeyLayer.Confirm}
          onClose={() => {
            setRun({ status: 'idle' });
          }}
        >
          <div data-jvm-heap-confirm="" className="flex flex-col gap-3 text-sm">
            <p className="font-medium text-bad">
              The dump contains the application’s secrets and data: passwords, tokens, keys and personal data held in
              memory.
            </p>
            <p>The JVM pauses for a full garbage collection while it writes the dump.</p>
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
              <button
                type="button"
                data-jvm-heap-go=""
                className={btnPrimary}
                onClick={() => void go(run.pid)}
              >
                Dump and download
              </button>
            </div>
          </div>
        </Dialog>
      ) : null}
      {run.status === 'running' ? <Pending what="the heap dump" /> : null}
      {run.status === 'failed' ? (
        run.error instanceof InspectProblem ? (
          <p data-jvm-failure={run.error.code} className="font-medium text-warn" title={run.error.message}>
            {titles[run.error.code] ?? 'Heap dump failed'}
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
