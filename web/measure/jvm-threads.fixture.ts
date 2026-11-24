import type { ThreadDump } from '../app/extras/runtime/data';

/**
 * The JVM tab's thread dump, for its picture (`pnpm shots:extras jvm-threads`):
 * what `extras/runtimeinspect` answered for the JDK 21 dump of its tests
 * (`extras/runtimeinspect/testdata/jdk21-Thread-print_-l.txt`), where two threads,
 * worker-a and worker-b, each hold the monitor the other waits for. Generated
 * from the Go parser's answer; only the cluster, pod and container were put in
 * the scene's names.
 */
export function jvmThreadDump(cluster: string): ThreadDump {
  return { ...DUMP, cluster };
}

const DUMP: ThreadDump = {
  cluster: "",
  namespace: "shop",
  pod: "billing-7c9f4d-x2kq",
  container: "app",
  state: "dumped",
  pid: 1,
  pidFrom: "listed",
  taken: "2026-11-11 11:04:15",
  vm: "OpenJDK 64-Bit Server VM (21.0.13+9-LTS mixed mode, sharing)",
  total: 20,
  daemon: 10,
  counts: [
    {
      state: "RUNNABLE",
      count: 8,
    },
    {
      state: "BLOCKED",
      count: 2,
    },
    {
      state: "WAITING",
      count: 1,
    },
    {
      state: "TIMED_WAITING",
      count: 2,
    },
    {
      state: "NEW",
      count: 0,
    },
    {
      state: "TERMINATED",
      count: 0,
    },
    {
      state: "VM",
      count: 7,
    },
  ],
  threads: [
    {
      name: "main",
      id: 1,
      daemon: false,
      prio: 5,
      state: "TIMED_WAITING",
      detail: "sleeping",
      status: "waiting on condition",
      cpu: "77.77ms",
      elapsed: "69.81s",
      frames: [
        {
          at: "java.lang.Thread.sleep0(java.base@21.0.13/Native Method)",
        },
        {
          at: "java.lang.Thread.sleep(java.base@21.0.13/Thread.java:509)",
        },
        {
          at: "D.main(D.java:6)",
        },
      ],
    },
    {
      name: "Reference Handler",
      id: 9,
      daemon: true,
      prio: 10,
      state: "RUNNABLE",
      status: "waiting on condition",
      cpu: "0.44ms",
      elapsed: "69.69s",
      frames: [
        {
          at: "java.lang.ref.Reference.waitForReferencePendingList(java.base@21.0.13/Native Method)",
        },
        {
          at: "java.lang.ref.Reference.processPendingReferences(java.base@21.0.13/Reference.java:246)",
        },
        {
          at: "java.lang.ref.Reference$ReferenceHandler.run(java.base@21.0.13/Reference.java:208)",
        },
      ],
    },
    {
      name: "Finalizer",
      id: 10,
      daemon: true,
      prio: 8,
      state: "WAITING",
      detail: "on object monitor",
      status: "in Object.wait()",
      cpu: "0.39ms",
      elapsed: "69.69s",
      frames: [
        {
          at: "java.lang.Object.wait0(java.base@21.0.13/Native Method)",
          locks: [
            {
              kind: "waiting on",
              id: "0x00000000e1f025f0",
              class: "java.lang.ref.NativeReferenceQueue$Lock",
            },
          ],
        },
        {
          at: "java.lang.Object.wait(java.base@21.0.13/Object.java:366)",
        },
        {
          at: "java.lang.Object.wait(java.base@21.0.13/Object.java:339)",
        },
        {
          at: "java.lang.ref.NativeReferenceQueue.await(java.base@21.0.13/NativeReferenceQueue.java:48)",
        },
        {
          at: "java.lang.ref.ReferenceQueue.remove0(java.base@21.0.13/ReferenceQueue.java:158)",
        },
        {
          at: "java.lang.ref.NativeReferenceQueue.remove(java.base@21.0.13/NativeReferenceQueue.java:89)",
          locks: [
            {
              kind: "locked",
              id: "0x00000000e1f025f0",
              class: "java.lang.ref.NativeReferenceQueue$Lock",
            },
          ],
        },
        {
          at: "java.lang.ref.Finalizer$FinalizerThread.run(java.base@21.0.13/Finalizer.java:173)",
        },
      ],
      waitingFor: {
        kind: "waiting on",
        id: "0x00000000e1f025f0",
        class: "java.lang.ref.NativeReferenceQueue$Lock",
      },
      holds: [
        {
          kind: "locked",
          id: "0x00000000e1f025f0",
          class: "java.lang.ref.NativeReferenceQueue$Lock",
        },
      ],
    },
    {
      name: "Signal Dispatcher",
      id: 11,
      daemon: true,
      prio: 9,
      state: "RUNNABLE",
      status: "waiting on condition",
      cpu: "0.56ms",
      elapsed: "69.69s",
      frames: [],
    },
    {
      name: "Service Thread",
      id: 12,
      daemon: true,
      prio: 9,
      state: "RUNNABLE",
      status: "runnable",
      cpu: "0.11ms",
      elapsed: "69.69s",
      frames: [],
    },
    {
      name: "Monitor Deflation Thread",
      id: 13,
      daemon: true,
      prio: 9,
      state: "RUNNABLE",
      status: "runnable",
      cpu: "6.42ms",
      elapsed: "69.69s",
      frames: [],
    },
    {
      name: "C2 CompilerThread0",
      id: 14,
      daemon: true,
      prio: 9,
      state: "RUNNABLE",
      status: "waiting on condition",
      cpu: "48.01ms",
      elapsed: "69.69s",
      frames: [],
    },
    {
      name: "C1 CompilerThread0",
      id: 15,
      daemon: true,
      prio: 9,
      state: "RUNNABLE",
      status: "waiting on condition",
      cpu: "77.12ms",
      elapsed: "69.68s",
      frames: [],
    },
    {
      name: "Notification Thread",
      id: 16,
      daemon: true,
      prio: 9,
      state: "RUNNABLE",
      status: "runnable",
      cpu: "0.14ms",
      elapsed: "69.67s",
      frames: [],
    },
    {
      name: "Common-Cleaner",
      id: 17,
      daemon: true,
      prio: 8,
      state: "TIMED_WAITING",
      detail: "parking",
      status: "waiting on condition",
      cpu: "1.05ms",
      elapsed: "69.66s",
      frames: [
        {
          at: "jdk.internal.misc.Unsafe.park(java.base@21.0.13/Native Method)",
          locks: [
            {
              kind: "parking to wait for",
              id: "0x00000000e1f11b88",
              class:
                "java.util.concurrent.locks.AbstractQueuedSynchronizer$ConditionObject",
            },
          ],
        },
        {
          at: "java.util.concurrent.locks.LockSupport.parkNanos(java.base@21.0.13/LockSupport.java:269)",
        },
        {
          at: "java.util.concurrent.locks.AbstractQueuedSynchronizer$ConditionObject.await(java.base@21.0.13/AbstractQueuedSynchronizer.java:1886)",
        },
        {
          at: "java.lang.ref.ReferenceQueue.await(java.base@21.0.13/ReferenceQueue.java:71)",
        },
        {
          at: "java.lang.ref.ReferenceQueue.remove0(java.base@21.0.13/ReferenceQueue.java:143)",
        },
        {
          at: "java.lang.ref.ReferenceQueue.remove(java.base@21.0.13/ReferenceQueue.java:218)",
        },
        {
          at: "jdk.internal.ref.CleanerImpl.run(java.base@21.0.13/CleanerImpl.java:140)",
        },
        {
          at: "java.lang.Thread.runWith(java.base@21.0.13/Thread.java:1596)",
        },
        {
          at: "java.lang.Thread.run(java.base@21.0.13/Thread.java:1583)",
        },
        {
          at: "jdk.internal.misc.InnocuousThread.run(java.base@21.0.13/InnocuousThread.java:186)",
        },
      ],
      waitingFor: {
        kind: "parking to wait for",
        id: "0x00000000e1f11b88",
        class:
          "java.util.concurrent.locks.AbstractQueuedSynchronizer$ConditionObject",
      },
    },
    {
      name: "worker-a",
      id: 18,
      daemon: false,
      prio: 5,
      state: "BLOCKED",
      detail: "on object monitor",
      status: "waiting for monitor entry",
      cpu: "3.08ms",
      elapsed: "69.63s",
      frames: [
        {
          at: "D.lambda$main$0(D.java:4)",
          locks: [
            {
              kind: "waiting to lock",
              id: "0x00000000e1f13750",
              class: "java.lang.Object",
            },
            {
              kind: "locked",
              id: "0x00000000e1f13740",
              class: "java.lang.Object",
            },
          ],
        },
        {
          at: "D$$Lambda/0x00007a4020000a08.run(Unknown Source)",
        },
        {
          at: "java.lang.Thread.runWith(java.base@21.0.13/Thread.java:1596)",
        },
        {
          at: "java.lang.Thread.run(java.base@21.0.13/Thread.java:1583)",
        },
      ],
      waitingFor: {
        kind: "waiting to lock",
        id: "0x00000000e1f13750",
        class: "java.lang.Object",
        heldBy: "worker-b",
      },
      holds: [
        {
          kind: "locked",
          id: "0x00000000e1f13740",
          class: "java.lang.Object",
        },
      ],
    },
    {
      name: "worker-b",
      id: 19,
      daemon: false,
      prio: 5,
      state: "BLOCKED",
      detail: "on object monitor",
      status: "waiting for monitor entry",
      cpu: "2.70ms",
      elapsed: "69.63s",
      frames: [
        {
          at: "D.lambda$main$1(D.java:5)",
          locks: [
            {
              kind: "waiting to lock",
              id: "0x00000000e1f13740",
              class: "java.lang.Object",
            },
            {
              kind: "locked",
              id: "0x00000000e1f13750",
              class: "java.lang.Object",
            },
          ],
        },
        {
          at: "D$$Lambda/0x00007a4020000c28.run(Unknown Source)",
        },
        {
          at: "java.lang.Thread.runWith(java.base@21.0.13/Thread.java:1596)",
        },
        {
          at: "java.lang.Thread.run(java.base@21.0.13/Thread.java:1583)",
        },
      ],
      waitingFor: {
        kind: "waiting to lock",
        id: "0x00000000e1f13740",
        class: "java.lang.Object",
        heldBy: "worker-a",
      },
      holds: [
        {
          kind: "locked",
          id: "0x00000000e1f13750",
          class: "java.lang.Object",
        },
      ],
    },
    {
      name: "Attach Listener",
      id: 20,
      daemon: true,
      prio: 9,
      state: "RUNNABLE",
      status: "waiting on condition",
      cpu: "99.25ms",
      elapsed: "10.65s",
      frames: [],
    },
    {
      name: "VM Thread",
      daemon: false,
      state: "VM",
      status: "runnable",
      cpu: "3.25ms",
      elapsed: "69.71s",
      frames: [],
    },
    {
      name: "VM Periodic Task Thread",
      daemon: false,
      state: "VM",
      status: "waiting on condition",
      cpu: "34.06ms",
      elapsed: "69.72s",
      frames: [],
    },
    {
      name: "G1 Service",
      daemon: false,
      state: "VM",
      status: "runnable",
      cpu: "2.02ms",
      elapsed: "69.80s",
      frames: [],
    },
    {
      name: "G1 Refine#0",
      daemon: false,
      state: "VM",
      status: "runnable",
      cpu: "0.13ms",
      elapsed: "69.80s",
      frames: [],
    },
    {
      name: "G1 Conc#0",
      daemon: false,
      state: "VM",
      status: "runnable",
      cpu: "0.09ms",
      elapsed: "69.81s",
      frames: [],
    },
    {
      name: "G1 Main Marker",
      daemon: false,
      state: "VM",
      status: "runnable",
      cpu: "0.13ms",
      elapsed: "69.81s",
      frames: [],
    },
    {
      name: "GC Thread#0",
      daemon: false,
      state: "VM",
      status: "runnable",
      cpu: "0.09ms",
      elapsed: "69.81s",
      frames: [],
    },
  ],
  deadlocks: [
    {
      kind: "Java-level",
      cycle: [
        {
          thread: "worker-a",
          waits:
            "to lock monitor 0x00007a3ff0002020 (object 0x00000000e1f13750, a java.lang.Object)",
          lock: "0x00000000e1f13750",
          class: "java.lang.Object",
          heldBy: "worker-b",
        },
        {
          thread: "worker-b",
          waits:
            "to lock monitor 0x00007a3fe4001090 (object 0x00000000e1f13740, a java.lang.Object)",
          lock: "0x00000000e1f13740",
          class: "java.lang.Object",
          heldBy: "worker-a",
        },
      ],
    },
  ],
  raw: '2026-11-11 11:04:15\nFull thread dump OpenJDK 64-Bit Server VM (21.0.13+9-LTS mixed mode, sharing)\n                 JDK version: OpenJDK Runtime Environment Temurin-21.0.13+9 (21.0.13+9) (build 21.0.13+9-LTS)\n\nThreads class SMR info:\n_java_thread_list=0x00007a4004002740, length=13, elements={\n0x00007a409402cae0, 0x00007a40940cd520, 0x00007a40940cec20, 0x00007a40940d0630,\n0x00007a40940d1e60, 0x00007a40940d35b0, 0x00007a40940d52a0, 0x00007a40940d6b80,\n0x00007a40940e6390, 0x00007a40940e9e80, 0x00007a40940f53f0, 0x00007a40940f6630,\n0x00007a4004001090\n}\n\n"main" #1 [7] prio=5 os_prio=0 cpu=77.77ms elapsed=69.81s tid=0x00007a409402cae0 nid=7 waiting on condition  [0x00007a409b27f000]\n   java.lang.Thread.State: TIMED_WAITING (sleeping)\n\tat java.lang.Thread.sleep0(java.base@21.0.13/Native Method)\n\tat java.lang.Thread.sleep(java.base@21.0.13/Thread.java:509)\n\tat D.main(D.java:6)\n\n   Locked ownable synchronizers:\n\t- None\n\n"Reference Handler" #9 [15] daemon prio=10 os_prio=0 cpu=0.44ms elapsed=69.69s tid=0x00007a40940cd520 nid=15 waiting on condition  [0x00007a40995ec000]\n   java.lang.Thread.State: RUNNABLE\n\tat java.lang.ref.Reference.waitForReferencePendingList(java.base@21.0.13/Native Method)\n\tat java.lang.ref.Reference.processPendingReferences(java.base@21.0.13/Reference.java:246)\n\tat java.lang.ref.Reference$ReferenceHandler.run(java.base@21.0.13/Reference.java:208)\n\n   Locked ownable synchronizers:\n\t- None\n\n"Finalizer" #10 [16] daemon prio=8 os_prio=0 cpu=0.39ms elapsed=69.69s tid=0x00007a40940cec20 nid=16 in Object.wait()  [0x00007a40994ec000]\n   java.lang.Thread.State: WAITING (on object monitor)\n\tat java.lang.Object.wait0(java.base@21.0.13/Native Method)\n\t- waiting on \u003c0x00000000e1f025f0\u003e (a java.lang.ref.NativeReferenceQueue$Lock)\n\tat java.lang.Object.wait(java.base@21.0.13/Object.java:366)\n\tat java.lang.Object.wait(java.base@21.0.13/Object.java:339)\n\tat java.lang.ref.NativeReferenceQueue.await(java.base@21.0.13/NativeReferenceQueue.java:48)\n\tat java.lang.ref.ReferenceQueue.remove0(java.base@21.0.13/ReferenceQueue.java:158)\n\tat java.lang.ref.NativeReferenceQueue.remove(java.base@21.0.13/NativeReferenceQueue.java:89)\n\t- locked \u003c0x00000000e1f025f0\u003e (a java.lang.ref.NativeReferenceQueue$Lock)\n\tat java.lang.ref.Finalizer$FinalizerThread.run(java.base@21.0.13/Finalizer.java:173)\n\n   Locked ownable synchronizers:\n\t- None\n\n"Signal Dispatcher" #11 [17] daemon prio=9 os_prio=0 cpu=0.56ms elapsed=69.69s tid=0x00007a40940d0630 nid=17 waiting on condition  [0x0000000000000000]\n   java.lang.Thread.State: RUNNABLE\n\n   Locked ownable synchronizers:\n\t- None\n\n"Service Thread" #12 [18] daemon prio=9 os_prio=0 cpu=0.11ms elapsed=69.69s tid=0x00007a40940d1e60 nid=18 runnable  [0x0000000000000000]\n   java.lang.Thread.State: RUNNABLE\n\n   Locked ownable synchronizers:\n\t- None\n\n"Monitor Deflation Thread" #13 [19] daemon prio=9 os_prio=0 cpu=6.42ms elapsed=69.69s tid=0x00007a40940d35b0 nid=19 runnable  [0x0000000000000000]\n   java.lang.Thread.State: RUNNABLE\n\n   Locked ownable synchronizers:\n\t- None\n\n"C2 CompilerThread0" #14 [20] daemon prio=9 os_prio=0 cpu=48.01ms elapsed=69.69s tid=0x00007a40940d52a0 nid=20 waiting on condition  [0x0000000000000000]\n   java.lang.Thread.State: RUNNABLE\n   No compile task\n\n   Locked ownable synchronizers:\n\t- None\n\n"C1 CompilerThread0" #15 [21] daemon prio=9 os_prio=0 cpu=77.12ms elapsed=69.68s tid=0x00007a40940d6b80 nid=21 waiting on condition  [0x0000000000000000]\n   java.lang.Thread.State: RUNNABLE\n   No compile task\n\n   Locked ownable synchronizers:\n\t- None\n\n"Notification Thread" #16 [22] daemon prio=9 os_prio=0 cpu=0.14ms elapsed=69.67s tid=0x00007a40940e6390 nid=22 runnable  [0x0000000000000000]\n   java.lang.Thread.State: RUNNABLE\n\n   Locked ownable synchronizers:\n\t- None\n\n"Common-Cleaner" #17 [23] daemon prio=8 os_prio=0 cpu=1.05ms elapsed=69.66s tid=0x00007a40940e9e80 nid=23 waiting on condition  [0x00007a4098d1c000]\n   java.lang.Thread.State: TIMED_WAITING (parking)\n\tat jdk.internal.misc.Unsafe.park(java.base@21.0.13/Native Method)\n\t- parking to wait for  \u003c0x00000000e1f11b88\u003e (a java.util.concurrent.locks.AbstractQueuedSynchronizer$ConditionObject)\n\tat java.util.concurrent.locks.LockSupport.parkNanos(java.base@21.0.13/LockSupport.java:269)\n\tat java.util.concurrent.locks.AbstractQueuedSynchronizer$ConditionObject.await(java.base@21.0.13/AbstractQueuedSynchronizer.java:1886)\n\tat java.lang.ref.ReferenceQueue.await(java.base@21.0.13/ReferenceQueue.java:71)\n\tat java.lang.ref.ReferenceQueue.remove0(java.base@21.0.13/ReferenceQueue.java:143)\n\tat java.lang.ref.ReferenceQueue.remove(java.base@21.0.13/ReferenceQueue.java:218)\n\tat jdk.internal.ref.CleanerImpl.run(java.base@21.0.13/CleanerImpl.java:140)\n\tat java.lang.Thread.runWith(java.base@21.0.13/Thread.java:1596)\n\tat java.lang.Thread.run(java.base@21.0.13/Thread.java:1583)\n\tat jdk.internal.misc.InnocuousThread.run(java.base@21.0.13/InnocuousThread.java:186)\n\n   Locked ownable synchronizers:\n\t- None\n\n"worker-a" #18 [24] prio=5 os_prio=0 cpu=3.08ms elapsed=69.63s tid=0x00007a40940f53f0 nid=24 waiting for monitor entry  [0x00007a4098c1c000]\n   java.lang.Thread.State: BLOCKED (on object monitor)\n\tat D.lambda$main$0(D.java:4)\n\t- waiting to lock \u003c0x00000000e1f13750\u003e (a java.lang.Object)\n\t- locked \u003c0x00000000e1f13740\u003e (a java.lang.Object)\n\tat D$$Lambda/0x00007a4020000a08.run(Unknown Source)\n\tat java.lang.Thread.runWith(java.base@21.0.13/Thread.java:1596)\n\tat java.lang.Thread.run(java.base@21.0.13/Thread.java:1583)\n\n   Locked ownable synchronizers:\n\t- None\n\n"worker-b" #19 [25] prio=5 os_prio=0 cpu=2.70ms elapsed=69.63s tid=0x00007a40940f6630 nid=25 waiting for monitor entry  [0x00007a4098b1c000]\n   java.lang.Thread.State: BLOCKED (on object monitor)\n\tat D.lambda$main$1(D.java:5)\n\t- waiting to lock \u003c0x00000000e1f13740\u003e (a java.lang.Object)\n\t- locked \u003c0x00000000e1f13750\u003e (a java.lang.Object)\n\tat D$$Lambda/0x00007a4020000c28.run(Unknown Source)\n\tat java.lang.Thread.runWith(java.base@21.0.13/Thread.java:1596)\n\tat java.lang.Thread.run(java.base@21.0.13/Thread.java:1583)\n\n   Locked ownable synchronizers:\n\t- None\n\n"Attach Listener" #20 [49] daemon prio=9 os_prio=0 cpu=99.25ms elapsed=10.65s tid=0x00007a4004001090 nid=49 waiting on condition  [0x0000000000000000]\n   java.lang.Thread.State: RUNNABLE\n\n   Locked ownable synchronizers:\n\t- None\n\n"VM Thread" os_prio=0 cpu=3.25ms elapsed=69.71s tid=0x00007a40940c0a20 nid=14 runnable  \n\n"VM Periodic Task Thread" os_prio=0 cpu=34.06ms elapsed=69.72s tid=0x00007a40940ab7f0 nid=13 waiting on condition  \n\n"G1 Service" os_prio=0 cpu=2.02ms elapsed=69.80s tid=0x00007a409408c240 nid=12 runnable  \n\n"G1 Refine#0" os_prio=0 cpu=0.13ms elapsed=69.80s tid=0x00007a409408b0b0 nid=11 runnable  \n\n"G1 Conc#0" os_prio=0 cpu=0.09ms elapsed=69.81s tid=0x00007a40940620c0 nid=10 runnable  \n\n"G1 Main Marker" os_prio=0 cpu=0.13ms elapsed=69.81s tid=0x00007a4094060f20 nid=9 runnable  \n\n"GC Thread#0" os_prio=0 cpu=0.09ms elapsed=69.81s tid=0x00007a409405b660 nid=8 runnable  \n\nJNI global refs: 7, weak refs: 0\n\n\nFound one Java-level deadlock:\n=============================\n"worker-a":\n  waiting to lock monitor 0x00007a3ff0002020 (object 0x00000000e1f13750, a java.lang.Object),\n  which is held by "worker-b"\n\n"worker-b":\n  waiting to lock monitor 0x00007a3fe4001090 (object 0x00000000e1f13740, a java.lang.Object),\n  which is held by "worker-a"\n\nJava stack information for the threads listed above:\n===================================================\n"worker-a":\n\tat D.lambda$main$0(D.java:4)\n\t- waiting to lock \u003c0x00000000e1f13750\u003e (a java.lang.Object)\n\t- locked \u003c0x00000000e1f13740\u003e (a java.lang.Object)\n\tat D$$Lambda/0x00007a4020000a08.run(Unknown Source)\n\tat java.lang.Thread.runWith(java.base@21.0.13/Thread.java:1596)\n\tat java.lang.Thread.run(java.base@21.0.13/Thread.java:1583)\n"worker-b":\n\tat D.lambda$main$1(D.java:5)\n\t- waiting to lock \u003c0x00000000e1f13740\u003e (a java.lang.Object)\n\t- locked \u003c0x00000000e1f13750\u003e (a java.lang.Object)\n\tat D$$Lambda/0x00007a4020000c28.run(Unknown Source)\n\tat java.lang.Thread.runWith(java.base@21.0.13/Thread.java:1596)\n\tat java.lang.Thread.run(java.base@21.0.13/Thread.java:1583)\n\nFound 1 deadlock.\n',
  notes: [],
};
