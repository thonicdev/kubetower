# Giving an extra to a group, in a cluster

This page is for whoever installs KubeTower in a cluster with the chart and signs
people in through an identity provider (the server profile, `--incluster`). It
says which three things decide who can use an extra, and ends on a complete
example to copy: the Redis viewer, given to one group for one namespace.

[EXTRAS.md](EXTRAS.md) says what each extra does, and how it is turned on on the
desktop.

---

## Three gates, in this order

1. **The chart's values say which extras this installation may turn on.** An
   extra the `extras` value does not name stays off, and Settings → Extra lists
   it with its button disabled and the reason. Naming one turns nothing on.
   The same list decides what the chart's ClusterRole grants the console's
   ServiceAccount for that extra's own reads (an operator's custom resources,
   for one). Port-forwards, Secrets and proxies stay behind their own `rbac`
   switches.
2. **The verb `use` on `extras.kubetower.dev/<key>` says who may use it**, and
   who may turn it on or off on the cluster in Settings → Extra and change its
   settings. It is asked with no namespace, so it is granted cluster-wide: a
   ClusterRole bound with a ClusterRoleBinding. Without it, a person does not
   see the extra in the menu, and its address answers 403.
3. **The person's own RBAC, namespace by namespace, says what the extra shows
   and does for them.** The console reads and acts with one ServiceAccount, and
   asks the API server first, for the person signed in, with a
   SubjectAccessReview: may this person do this verb on this resource here? So
   `use` opens the page, and a Role in a namespace decides what the page can
   reach there.

Turning an extra on per cluster needs one replica and its state volume
(`replicaCount: 1` and persistence): the choice is kept in the state directory.
With several replicas the switch is withheld and only the values decide.

---

## What the person's RBAC covers in an extra

- **What an extra lists** goes through the console's reviewed list: each answer
  is narrowed to the namespaces the person may `list` that resource in, and a
  page spanning namespaces says it was narrowed. A `Role` that names objects
  with `resourceNames` holds too, because a read by name is reviewed with the
  name. A review is kept about a minute, so removing a RoleBinding takes effect
  within that minute.
- **What an extra does** - a port-forward to a pod, a Secret value revealed, a
  command run in a container - is reviewed for the person in the target
  namespace before it is sent, and written to the audit log with the person,
  the extra and the outcome. A refused review sends nothing.
- **What is read with the console's own rights, by design**: the Secret that
  holds an integration's token and the Service it is sent to, and only the
  ones the console's configuration names - Argo CD's read-only token
  (`KT_ARGOCD_TOKEN_SECRET`) and argocd-server's Service
  (`KT_ARGOCD_SERVER_SERVICE`), the KubeCron collector's Service and token when
  `KT_KUBECRON_COLLECTOR` names it. Never a name found in the cluster. The
  tunnels to argocd-server, to the KubeCron collector, to Hubble Relay and to Calico's Goldmane, and
  the connection to Prometheus, are the console's too: what comes back is
  kept only for the objects the person's own reads returned. Each
  token read is written to the audit log as the console's, on the person's
  behalf; the token is sent to that backend and never shown to anyone. The
  console's ServiceAccount needs `get` on those Secrets and Services. What the
  person is then shown from the backend is still narrowed to the objects they
  may list. A KubeCron collector that nothing names is looked for among the
  Services the person may list, and its token is read only if the person may
  read that Secret.
- **The clients an extra is handed answer which kinds the cluster serves, and
  nothing else**: no extra reads an object with the console's rights outside
  the two cases above.

---

## Extra by extra

"For the person" is a read the console makes with its ServiceAccount after a
SubjectAccessReview for the person signed in, narrowed to what that review
allows. "The console's" is a read made with the ServiceAccount's own rights,
reviewed for nobody. Every count, tile, cell, total and suggestion is computed
from the reads for the person; a page whose reads were narrowed says so.
`extras/narrowed_test.go` holds it for each extra: a person refused one
namespace is answered exactly what they would be if that namespace held
nothing, but for the sentence that says the answer was narrowed.

### Argo CD (`argocd`)

| What | Whose rights | How the person is narrowed |
|---|---|---|
| Applications, Flux's Kustomizations and HelmReleases, Events, argocd-server's Service found by its label | for the person | to the namespaces where they may `list` them |
| The object an Argo CD tab is opened on, its owners | for the person | by name, in its namespace |
| Which kinds the cluster serves | the console's | discovery only: no object |
| The read-only token (`KT_ARGOCD_TOKEN_SECRET`, key `token`, and `ca.crt`) | the console's | never shown; read only for an Application and an object the person already read |
| argocd-server's Service (`KT_ARGOCD_SERVER_SERVICE`) and the tunnel to it | the console's | the diff asked is of that one object, after the person's reads |

An Application lists the objects it deploys, in any namespace: whoever may
read the Application reads those names, as `kubectl get application -o yaml`
would show them.

### Cert Manager (`certmanager`)

| What | Whose rights | How the person is narrowed |
|---|---|---|
| Certificates, Issuers, Orders, Challenges, Ingresses | for the person | to the namespaces where they may `list` them |
| ClusterIssuers | for the person | `list clusterissuers`, cluster-wide |
| Secrets | not read | a Secret's cell is the Certificate that writes it |

Nothing is read with the console's rights.

### CloudNativePG (`cnpg`)

| What | Whose rights | How the person is narrowed |
|---|---|---|
| Clusters, Backups, ScheduledBackups, Poolers, Databases, Publications, Subscriptions, their pods and Events | for the person | to the namespaces where they may `list` them |
| Disk usage of an instance's volumes (kubelet summary) | for the person | `list nodes`, then the pods on each node narrowed to the namespaces where they may `list pods` |
| Statistics and tables, on **Connect** | for the person | a port-forward to `<cluster>-rw` (`create pods/portforward`), the application Secret's user, password and database (`get` on that Secret, recorded in the audit log) and the cluster's CA when they may read it; then fixed read-only statements as the database's own application user |
| Replication lag, on **Read lag** | for the person | `create pods/exec` on the primary, by name, recorded in the audit log with its argv; one fixed read-only `psql` query through the instance's own socket, no password |

Nothing is read with the console's rights. What Connect shows is what the
cluster's application user may read in its database, whoever presses it: the
Kubernetes gates above decide who may connect, not which tables they see.

### JVM Inspector (`jvm`)

| What | Whose rights | How the person is narrowed |
|---|---|---|
| Pods | for the person | to the namespaces where they may `list pods` |
| Live CPU and memory (metrics-server) | for the person | the same namespaces |
| `jcmd` in a container | for the person | `create pods/exec` on that pod, by name, recorded in the audit log |

Nothing is read with the console's rights.

### KubeAdjust (`kubeadjust`)

| What | Whose rights | How the person is narrowed |
|---|---|---|
| Deployments, StatefulSets, DaemonSets, ReplicaSets, ControllerRevisions, CronJobs, Jobs, pods, HPAs, VPAs, PVCs, namespaces, nodes | for the person | to the namespaces where they may `list` them; nodes with `list nodes` |
| Live CPU and memory (metrics-server) | for the person | to the namespaces where they may `list pods` |
| Disk (kubelet summary) | for the person | `list nodes`, then each node's pods narrowed |
| Seven-day window (Prometheus) | the console's connection, fixed queries | the containers asked are first narrowed to the namespaces where the person may `list pods`, and the answer is narrowed again |

A node's totals count only the pods the person may list: the tiles' counts and
the node findings resting on a total are withheld, and the page says so.

### KubeCron (`kubecron`)

| What | Whose rights | How the person is narrowed |
|---|---|---|
| CronJobs, Jobs, pods | for the person | to the namespaces where they may `list` them |
| The collector's Service, found by its label | for the person | among the Services they may `list` |
| The collector's token, collector found by its label | for the person | `get` on that Secret (Reveal), recorded |
| The collector's Service and token named by `KT_KUBECRON_COLLECTOR`, `KT_KUBECRON_TOKEN_SECRET` or Settings › Extra | the console's | never shown |
| The tunnel to the collector, and its history | the console's | opened only once the person lists at least one CronJob; outcomes, calendars and runs are kept only for the CronJobs they list |
| A run's log | the console's tunnel | sent only if the person may `get pods/log` in the run's namespace |

### Network Inspector (`netinspect`)

| What | Whose rights | How the person is narrowed |
|---|---|---|
| NetworkPolicies, CiliumNetworkPolicies and the cluster-wide policies, pods, namespaces, DaemonSets | for the person | to the namespaces where they may `list` them; a rule is drawn only from a policy they read |
| The `kube-system/hubble-relay` Service | for the person | `list services` in `kube-system`: without it, observed traffic is refused |
| The `calico-system/goldmane` Service, and the `calico-system/goldmane-ca-bundle` ConfigMap (the CA Goldmane's certificate is checked against, public) | for the person | `list services` and `list configmaps` in `calico-system`: without them, observed traffic is refused; Hubble Relay is looked for first |
| Goldmane's client certificate, the Secret `calico-system/whisker-backend-key-pair` (`tls.crt`, `tls.key`) | for the person, through `Host.Reveal`: reviewed and recorded, once per key on each read | `get secrets` on that Secret: a person refused it has no Calico flows, and the answer says refused. Goldmane asks Kubernetes nothing, so this read is the only gate on who may read its flows; it is made before any tunnel is opened |
| The tunnel to Hubble Relay or Goldmane, and its flows | the console's | not opened when the person may list no pod; a flow is shown, counted and drawn only when each of its ends inside the cluster is in a namespace where they see pods, and the answer says only that flows were left out, not how many |

### Pod Debugging (`pending`)

| What | Whose rights | How the person is narrowed |
|---|---|---|
| Pods, Events, PVCs, ResourceQuotas | for the person | to the namespaces where they may `list` them |
| Nodes, PersistentVolumes, StorageClasses | for the person | `list` cluster-wide |
| The requests already placed on each node | for the person | counted from the pods they may list: a node's free room may be overstated, and the page says so |

Nothing is read with the console's rights.

### Redis viewer (`redis`)

| What | Whose rights | How the person is narrowed |
|---|---|---|
| Services, EndpointSlices, pods | for the person | to the namespaces where they may `list` them |
| An instance's password | for the person | `get` on that Secret (Reveal), recorded, never shown |
| The port-forward to an instance | for the person | `create pods/portforward` in its namespace |

Nothing is read with the console's rights.

### Security Center (`security`)

| What | Whose rights | How the person is narrowed |
|---|---|---|
| trivy-operator's VulnerabilityReports, ConfigAuditReports, ExposedSecretReports | for the person | to the namespaces where they may `list` them |
| Pods, ReplicaSets, Jobs, Services, Ingresses | for the person | the same |

Nothing is read with the console's rights.

### What Settings › Extra holds

Per extra, on each cluster: whether it is on, who may turn it on (`use`,
with the reason when the person may not), and its settings. Two extras have
settings: Argo CD's list column, and KubeCron's collector and token Secret.
Argo CD's token Secret, argocd-server's Service and its certificate name are
read from the environment only, and are not listed there. No extra has a
setting that narrows it further than the person's RBAC: a Role is the way.

---

## Group names

The console asserts a person's groups as the identity provider's `groups` claim
says them (`auth.oidc.groupsClaim`), with `auth.oidc.groupsPrefix` in front when
it is set. A RoleBinding names the group **as the console asserts it**: with
`groupsPrefix: "oidc:"`, the group `devs-backend` is bound as
`oidc:devs-backend`. Groups are read at sign-in, so a group removed at the
provider is still asserted until the session ends (`auth.oidc.sessionMax`).

Check a grant before anybody signs in:

```bash
kubectl auth can-i use redis.extras.kubetower.dev --as=alice --as-group=devs-backend
kubectl auth can-i create pods/portforward -n team-a --as=alice --as-group=devs-backend
kubectl auth can-i list services -n team-b --as=alice --as-group=devs-backend   # no
```

---

## Example: the Redis viewer for `devs-backend`, in `team-a` only

The group `devs-backend` may open the Redis and Valkey instances of the
namespace `team-a`, and nothing else in the cluster through this extra.

### The chart's values

```yaml
extras: [redis]

rbac:
  # The Redis viewer reaches an instance through a port-forward to its pod.
  exec: true
  # It reads an instance's password from the Secret its pod is started with.
  readSecrets: true

# Turning the Redis viewer on per cluster keeps the choice in the state
# directory: one replica, with persistence.
replicaCount: 1
```

`rbac.exec` and `rbac.readSecrets` widen what the console's ServiceAccount may
do. They do not widen what any person may do: every port-forward and every
Secret read is still asked for the person first.

### Who may use the extra: a ClusterRole and a ClusterRoleBinding

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kubetower-use-redis
rules:
  - apiGroups: [extras.kubetower.dev]
    resources: [redis]
    verbs: [use]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kubetower-use-redis-devs-backend
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kubetower-use-redis
subjects:
  - apiGroup: rbac.authorization.k8s.io
    kind: Group
    name: devs-backend   # as the console asserts it: oidc:devs-backend with groupsPrefix "oidc:"
```

### What it reaches in `team-a`: a Role and a RoleBinding

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: kubetower-redis-viewer
  namespace: team-a
rules:
  # Finding the instances: their Services, the endpoints behind them, the pods.
  - apiGroups: [""]
    resources: [services, pods]
    verbs: [get, list]
  - apiGroups: [discovery.k8s.io]
    resources: [endpointslices]
    verbs: [list]
  # Opening one: a port-forward to its pod.
  - apiGroups: [""]
    resources: [pods/portforward]
    verbs: [create]
  # Its password, for an instance that has one. Name the Secrets with
  # resourceNames to give these and no other.
  - apiGroups: [""]
    resources: [secrets]
    verbs: [get]
    # resourceNames: [redis-auth]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: kubetower-redis-viewer-devs-backend
  namespace: team-a
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: kubetower-redis-viewer
subjects:
  - apiGroup: rbac.authorization.k8s.io
    kind: Group
    name: devs-backend
```

### What a member of `devs-backend` then sees

- **Redis viewer** in the menu, because of `use`. Somebody holding `use` turns it
  on for the cluster once, in Settings → Extra.
- **The instances of `team-a`, and those only**: the list of another namespace
  is narrowed away, and the page says it was narrowed.
- **An instance opened on its keys**, through a port-forward the console asks
  the API server about for that person in `team-a`, and closes with the
  request. The password, when there is one, is read for the person with the
  `get secrets` above, recorded in the audit log, and never shown, stored or put
  in an address. Without that grant, an instance with a password is refused and
  says why.
- Nothing is written to Redis or to the cluster: the viewer's commands are reads
  on a fixed list.

A person without the ClusterRoleBinding does not see the extra. A person with it
but without the Role in `team-a` sees the page with no instance of `team-a`.
