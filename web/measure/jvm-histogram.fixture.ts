import type { ClassHistogram } from '../app/extras/jvm/data';

/**
 * The JVM tab's class histogram, for its picture (`pnpm shots:extras jvm-histogram`):
 * what `extras/jvm` answered for the JDK 21 histogram of its tests
 * (`extras/jvm/testdata/jdk21-GC-class_histogram.txt`, a program holding twenty
 * thousand `int[]` and strings), generated from the Go parser's answer; only the
 * cluster, pod and container were put in the scene's names.
 */
export function jvmClassHistogram(cluster: string): ClassHistogram {
  return { ...HISTOGRAM, cluster };
}

const HISTOGRAM: ClassHistogram = 
{
  cluster: "",
  namespace: "shop",
  pod: "billing-7c9f4d-x2kq",
  container: "app",
  state: "histogram",
  pid: 1,
  pidFrom: "listed",
  classes: 260,
  instances: 87714,
  bytes: 3339720,
  rows: [
    {
      rank: 1,
      name: "int[]",
      module: "java.base",
      instances: 21342,
      bytes: 1003808,
      share: 30
    },
    {
      rank: 2,
      name: "byte[]",
      module: "java.base",
      instances: 28073,
      bytes: 833112,
      share: 24.9
    },
    {
      rank: 3,
      name: "java.lang.String",
      module: "java.base",
      instances: 27992,
      bytes: 671808,
      share: 20.1
    },
    {
      rank: 4,
      name: "java.lang.Object[]",
      module: "java.base",
      instances: 1032,
      bytes: 321400,
      share: 9.6
    },
    {
      rank: 5,
      name: "java.lang.Class",
      module: "java.base",
      instances: 1506,
      bytes: 186296,
      share: 5.5
    },
    {
      rank: 6,
      name: "java.util.concurrent.ConcurrentHashMap$Node",
      module: "java.base",
      instances: 1061,
      bytes: 33952,
      share: 1
    },
    {
      rank: 7,
      name: "char[]",
      module: "java.base",
      instances: 8,
      bytes: 33560,
      share: 1
    },
    {
      rank: 8,
      name: "java.util.HashMap$Node",
      module: "java.base",
      instances: 1018,
      bytes: 32576,
      share: 0.9
    },
    {
      rank: 9,
      name: "java.util.HashMap$Node[]",
      module: "java.base",
      instances: 263,
      bytes: 24112,
      share: 0.7
    },
    {
      rank: 10,
      name: "java.util.concurrent.ConcurrentHashMap$Node[]",
      module: "java.base",
      instances: 20,
      bytes: 23296,
      share: 0.6
    },
    {
      rank: 11,
      name: "java.util.HashMap",
      module: "java.base",
      instances: 265,
      bytes: 12720,
      share: 0.3
    },
    {
      rank: 12,
      name: "jdk.internal.math.FDBigInteger",
      module: "java.base",
      instances: 341,
      bytes: 10912,
      share: 0.3
    },
    {
      rank: 13,
      name: "java.lang.module.ModuleDescriptor$Exports",
      module: "java.base",
      instances: 367,
      bytes: 8808,
      share: 0.2
    },
    {
      rank: 14,
      name: "java.lang.ref.SoftReference[]",
      module: "java.base",
      instances: 98,
      bytes: 7448,
      share: 0.2
    },
    {
      rank: 15,
      name: "java.lang.Long",
      module: "java.base",
      instances: 256,
      bytes: 6144,
      share: 0.1
    },
    {
      rank: 16,
      name: "java.util.ImmutableCollections$Set12",
      module: "java.base",
      instances: 242,
      bytes: 5808,
      share: 0.1
    },
    {
      rank: 17,
      name: "java.net.URI",
      module: "java.base",
      instances: 62,
      bytes: 4960,
      share: 0.1
    },
    {
      rank: 18,
      name: "java.lang.invoke.MemberName",
      module: "java.base",
      instances: 97,
      bytes: 4656,
      share: 0.1
    },
    {
      rank: 19,
      name: "java.lang.invoke.LambdaForm$Name",
      module: "java.base",
      instances: 141,
      bytes: 4512,
      share: 0.1
    },
    {
      rank: 20,
      name: "java.lang.module.ModuleDescriptor$Requires",
      module: "java.base",
      instances: 137,
      bytes: 4384,
      share: 0.1
    },
    {
      rank: 21,
      name: "java.lang.invoke.MethodType",
      module: "java.base",
      instances: 108,
      bytes: 4320,
      share: 0.1
    },
    {
      rank: 22,
      name: "java.lang.invoke.MethodHandle[]",
      module: "java.base",
      instances: 34,
      bytes: 4312,
      share: 0.1
    },
    {
      rank: 23,
      name: "java.lang.Integer",
      module: "java.base",
      instances: 262,
      bytes: 4192,
      share: 0.1
    },
    {
      rank: 24,
      name: "java.lang.Byte",
      module: "java.base",
      instances: 256,
      bytes: 4096,
      share: 0.1
    },
    {
      rank: 25,
      name: "java.lang.Short",
      module: "java.base",
      instances: 256,
      bytes: 4096,
      share: 0.1
    },
    {
      rank: 26,
      name: "jdk.internal.util.WeakReferenceKey",
      module: "java.base",
      instances: 127,
      bytes: 4064,
      share: 0.1
    },
    {
      rank: 27,
      name: "java.lang.module.ModuleDescriptor",
      module: "java.base",
      instances: 62,
      bytes: 3968,
      share: 0.1
    },
    {
      rank: 28,
      name: "java.lang.Module",
      module: "java.base",
      instances: 68,
      bytes: 3808,
      share: 0.1
    },
    {
      rank: 29,
      name: "java.util.ImmutableCollections$SetN",
      module: "java.base",
      instances: 145,
      bytes: 3480,
      share: 0.1
    },
    {
      rank: 30,
      name: "jdk.internal.module.ModuleReferenceImpl",
      module: "java.base",
      instances: 62,
      bytes: 3472,
      share: 0.1
    },
    {
      rank: 31,
      name: "java.lang.Class[]",
      module: "java.base",
      instances: 109,
      bytes: 3360,
      share: 0.1
    },
    {
      rank: 32,
      name: "java.util.HashSet",
      module: "java.base",
      instances: 203,
      bytes: 3248,
      share: 0
    },
    {
      rank: 33,
      name: "java.util.concurrent.ConcurrentHashMap",
      module: "java.base",
      instances: 39,
      bytes: 2496,
      share: 0
    },
    {
      rank: 34,
      name: "java.lang.invoke.LambdaForm$Kind",
      module: "java.base",
      instances: 68,
      bytes: 2176,
      share: 0
    },
    {
      rank: 35,
      name: "java.lang.Character",
      module: "java.base",
      instances: 128,
      bytes: 2048,
      share: 0
    },
    {
      rank: 36,
      name: "java.lang.invoke.ResolvedMethodName",
      module: "java.base",
      instances: 84,
      bytes: 2016,
      share: 0
    },
    {
      rank: 37,
      name: "jdk.internal.loader.BuiltinClassLoader$LoadedModule",
      module: "java.base",
      instances: 61,
      bytes: 1952,
      share: 0
    },
    {
      rank: 38,
      name: "java.lang.invoke.LambdaForm$Name[]",
      module: "java.base",
      instances: 37,
      bytes: 1704,
      share: 0
    },
    {
      rank: 39,
      name: "java.util.ImmutableCollections$List12",
      module: "java.base",
      instances: 67,
      bytes: 1608,
      share: 0
    },
    {
      rank: 40,
      name: "java.lang.invoke.MethodTypeForm",
      module: "java.base",
      instances: 49,
      bytes: 1568,
      share: 0
    },
    {
      rank: 41,
      name: "java.lang.invoke.LambdaForm",
      module: "java.base",
      instances: 32,
      bytes: 1536,
      share: 0
    },
    {
      rank: 42,
      name: "java.lang.module.ModuleDescriptor$Provides",
      module: "java.base",
      instances: 62,
      bytes: 1488,
      share: 0
    },
    {
      rank: 43,
      name: "java.lang.module.ResolvedModule",
      module: "java.base",
      instances: 62,
      bytes: 1488,
      share: 0
    },
    {
      rank: 44,
      name: "jdk.internal.module.ServicesCatalog$ServiceProvider",
      module: "java.base",
      instances: 62,
      bytes: 1488,
      share: 0
    },
    {
      rank: 45,
      name: "jdk.internal.module.SystemModuleFinders$2",
      module: "java.base",
      instances: 62,
      bytes: 1488,
      share: 0
    },
    {
      rank: 46,
      name: "jdk.internal.math.FDBigInteger[]",
      module: "java.base",
      instances: 1,
      bytes: 1376,
      share: 0
    },
    {
      rank: 47,
      name: "long[]",
      module: "java.base",
      instances: 6,
      bytes: 1336,
      share: 0
    },
    {
      rank: 48,
      name: "java.lang.ref.SoftReference",
      module: "java.base",
      instances: 33,
      bytes: 1320,
      share: 0
    },
    {
      rank: 49,
      name: "java.lang.Thread",
      module: "java.base",
      instances: 10,
      bytes: 1200,
      share: 0
    },
    {
      rank: 50,
      name: "java.lang.invoke.LambdaForm$NamedFunction",
      module: "java.base",
      instances: 48,
      bytes: 1152,
      share: 0
    }
  ],
  cut: true
};
