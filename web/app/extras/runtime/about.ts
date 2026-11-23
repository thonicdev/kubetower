import type { ExtensionAbout } from '@kubetower/ui';

import screenshot from './assets/preview.png';
import podTab from './assets/pod-tab-inspected.png';
import podTile from './assets/pod-tile.png';
import threadDump from './assets/thread-dump.png';
import classHistogram from './assets/class-histogram.png';
import flightRecorder from './assets/flight-recorder-confirm.png';
import memoryHistory from './assets/memory-history.png';

/**
 * What Settings → Extra says about the JVM Inspector before it is turned on.
 * The permissions follow the Go half's reads (`extras/runtimeinspect`): keep the two in
 * step when a read is added. `create pods/exec` is what the JVM tab on a pod
 * needs to run `jcmd` in the container, on a click; metrics-server's pod
 * metrics give the working set its memory bar is drawn against.
 */
export const about: ExtensionAbout = {
  description:
    'Reads which runtime each container runs, Java, Go, Node.js or Python, from its pod’s spec alone (the command line, ' +
    'the environment, the image, or the kubetower.io/runtime annotation, which wins), shows it in a "Runtime" column ' +
    'and gives a pod a tab per runtime. ' +
    'Finds the pods that probably run Java, from their spec alone: the image, a command line that starts java, ' +
    'the Java environment variables. For each Java container it estimates the maximum heap from the options the ' +
    'JVM reads and the memory limit, and warns when the heap leaves too little room under the limit and the ' +
    'container risks being OOMKilled. A "Java" column in the Pods and workload lists carries the same grade. ' +
    'It lists at most 2,000 Java pods, the worst first, and says so when it cuts. This page runs nothing in a ' +
    'container. A Java pod gets a tile on its Details (the runtime, the risk, the heap against the limit) and a ' +
    'JVM tab, which runs jcmd there when you click Inspect, for its version, GC, flags and memory against the ' +
    'working set, request and limit, and its garbage collectors’ work. A Thread dump button there takes a thread ' +
    'dump on a click: the deadlocks first, the threads by state, a filter and a download. A Memory history button there reads the last seven days of the pod’s containers’ ' +
    'memory from the cluster’s Prometheus, against their limits, on a click. A Class histogram button counts the live objects by class, after a ' +
    'confirmation that it runs a full GC and pauses the application. A Flight recorder control records the JVM for 30 seconds to 5 minutes, after a ' +
    'confirmation, and downloads the file for JDK Mission Control; the file is removed from the container.',
  reads: [
    'On a Node.js pod’s tab: whether its inspector answers; once it is open, on a click, a heap snapshot (confirmed), a CPU profile, and the diagnostic report with its secret-looking environment values masked',
    'On a Go pod’s tab, on a click: its goroutines and profiles, from net/http/pprof where it serves it (the port from kubetower.io/pprof-port, a port named pprof, or 6060)',
    'Pods, and their containers’ images, command lines, environment and memory requests and limits',
    'Each Java container’s working set now, from metrics-server, where the cluster runs one',
    'On a Java pod’s JVM tab, on a click: what the JVM says about itself, through jcmd run in its container',
    'On a click of Thread dump: the JVM’s threads and their stacks (Thread.print), which may carry the application’s thread names',
    'On a confirmed click of Class histogram: the 50 classes holding the most live bytes (GC.class_histogram, a full GC), with their instance counts',
    'On a confirmed click of Record: a Flight Recorder file (JFR.start, the default profile), which may hold the JVM’s environment variables and system properties',
    'On a click of Memory history: seven days of the pod’s containers’ working set, from the cluster’s Prometheus where the console reaches one',
  ],
  permissions: [
    'create pods/exec, for the Node.js tab, which opens the inspector with node in the container on a confirmed click (process._debugProcess, recorded)',
    'create pods/portforward, for the Go tab, which reads net/http/pprof through a tunnel to the pod (Goroutines, Heap profile, CPU profile, Trace)',
    'list pods',
    'list pods (metrics.k8s.io), for the working sets',
    'create pods/exec, for the JVM tab on a pod, which runs jcmd in the container (Inspect, Thread dump, Class histogram)',
  ],
  requires: [],
  // Drawn by `pnpm shots:extras` from the measure harness's fake data.
  screenshot,
  gallery: [
    { src: podTile, caption: 'On a Java pod’s Details: the runtime, the risk, and the maximum heap against the limit.' },
    {
      src: podTab,
      caption:
        'The JVM tab after Inspect: the JVM’s own figures, its garbage collection, and its memory against the working set, request and limit.',
    },
    {
      src: threadDump,
      caption: 'A thread dump: the deadlock first, with its cycle, the threads by state, and the blocked ones with who holds their lock.',
    },
    {
      src: memoryHistory,
      caption: 'A pod’s memory history: seven days of each container’s working set from Prometheus, against its limit.',
    },
    {
      src: flightRecorder,
      caption: 'Record: the duration is one of four, and the confirmation warns the file may hold environment variables.',
    },
    {
      src: classHistogram,
      caption: 'A class histogram: the classes holding the most live bytes, after a confirmed full GC.',
    },
  ],
};
