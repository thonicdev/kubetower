import type { Flows, Overview, PodAnswer, Verdict } from '../app/extras/netinspect/data';

/**
 * The Network Inspector's preview: an invented cluster (shop, payments, auth,
 * …) whose answers were written by the Go engine itself
 * (`extras/netinspect`), from 31 pods, 17 NetworkPolicies and a few Cilium
 * policies, and invented flows aggregated as Hubble's would be, so the picture shows what the routes really answer. The scenes
 * open the cluster's view with a refused verdict, one namespace's with an
 * allowed one, and one pod's page.
 */

const CLUSTER: Overview = {
  "mode": "rules",
  "namespace": "",
  "complete": true,
  "notes": [],
  "cni": {
    "name": "Cilium",
    "enforces": "yes",
    "detail": "Recognized from its DaemonSet: Cilium applies NetworkPolicies."
  },
  "unevaluated": [],
  "summary": {
    "pods": 31,
    "isolatedIn": 22,
    "isolatedOut": 9,
    "open": 6,
    "hostNetwork": 3,
    "policies": 20,
    "defaultDeny": 2,
    "idle": 2
  },
  "namespaces": [
    {
      "name": "auth",
      "pods": 2,
      "isolatedIn": 2,
      "isolatedOut": 2,
      "policies": 3,
      "defaultDenyIn": false,
      "defaultDenyOut": false
    },
    {
      "name": "batch",
      "pods": 2,
      "isolatedIn": 0,
      "isolatedOut": 0,
      "policies": 0,
      "defaultDenyIn": false,
      "defaultDenyOut": false
    },
    {
      "name": "data",
      "pods": 3,
      "isolatedIn": 3,
      "isolatedOut": 0,
      "policies": 2,
      "defaultDenyIn": false,
      "defaultDenyOut": false
    },
    {
      "name": "ingress-nginx",
      "pods": 2,
      "isolatedIn": 0,
      "isolatedOut": 0,
      "policies": 0,
      "defaultDenyIn": false,
      "defaultDenyOut": false
    },
    {
      "name": "kube-system",
      "pods": 5,
      "isolatedIn": 0,
      "isolatedOut": 0,
      "policies": 0,
      "defaultDenyIn": false,
      "defaultDenyOut": false
    },
    {
      "name": "monitoring",
      "pods": 2,
      "isolatedIn": 2,
      "isolatedOut": 2,
      "policies": 1,
      "defaultDenyIn": false,
      "defaultDenyOut": false
    },
    {
      "name": "payments",
      "pods": 5,
      "isolatedIn": 5,
      "isolatedOut": 5,
      "policies": 8,
      "defaultDenyIn": true,
      "defaultDenyOut": true
    },
    {
      "name": "shop",
      "pods": 10,
      "isolatedIn": 10,
      "isolatedOut": 0,
      "policies": 5,
      "defaultDenyIn": true,
      "defaultDenyOut": false
    }
  ],
  "namespacesTotal": 8,
  "policies": [
    {
      "kind": "CiliumClusterwideNetworkPolicy",
      "namespace": "",
      "name": "cluster-baseline",
      "selector": "io.kubernetes.pod.namespace in (auth,monitoring)",
      "types": [
        "Ingress",
        "Egress"
      ],
      "selects": 4,
      "ingress": [
        "from entity host or entity remote-node, on every port"
      ],
      "egress": [
        "to entity kube-apiserver, on every port",
        "to endpoints app=coredns in namespace kube-system, on 53/UDP"
      ],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "auth",
      "name": "legacy-admin",
      "selector": "app=admin-console",
      "types": [
        "Ingress"
      ],
      "selects": 0,
      "ingress": [
        "from everyone, on every port"
      ],
      "egress": [],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "auth",
      "name": "sso-from-apps",
      "selector": "app=sso",
      "types": [
        "Ingress"
      ],
      "selects": 2,
      "ingress": [
        "from pods app=checkout-api in namespace shop or pods app=gateway in namespace payments, on http"
      ],
      "egress": [],
      "broken": []
    },
    {
      "kind": "CiliumNetworkPolicy",
      "namespace": "auth",
      "name": "sso-to-idp",
      "selector": "app=sso",
      "types": [
        "Egress"
      ],
      "selects": 2,
      "ingress": [],
      "egress": [
        "to toFQDNs, on 443/TCP [not evaluated: toFQDNs]",
        "to endpoints all in namespace monitoring, on 3000/TCP [not evaluated: layer-7 rules (http)]"
      ],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "data",
      "name": "loader-from-batch",
      "selector": "app=warehouse-loader",
      "types": [
        "Ingress"
      ],
      "selects": 3,
      "ingress": [
        "from every pod in namespaces team=data, on 8080/TCP"
      ],
      "egress": [],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "data",
      "name": "old-etl",
      "selector": "app=etl",
      "types": [
        "Ingress"
      ],
      "selects": 0,
      "ingress": [
        "from pods app=scheduler in this namespace, on 9000/TCP"
      ],
      "egress": [],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "monitoring",
      "name": "grafana-from-ingress",
      "selector": "app=grafana",
      "types": [
        "Ingress"
      ],
      "selects": 1,
      "ingress": [
        "from every pod in namespace ingress-nginx, on http"
      ],
      "egress": [],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "payments",
      "name": "allow-dns",
      "selector": "",
      "types": [
        "Egress"
      ],
      "selects": 5,
      "ingress": [],
      "egress": [
        "to pods app=coredns in namespace kube-system, on 53/UDP, 53/TCP"
      ],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "payments",
      "name": "db-from-ledger",
      "selector": "app=ledger-db",
      "types": [
        "Ingress"
      ],
      "selects": 1,
      "ingress": [
        "from pods app=ledger in this namespace, on 5432/TCP"
      ],
      "egress": [],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "payments",
      "name": "default-deny",
      "selector": "",
      "types": [
        "Ingress",
        "Egress"
      ],
      "selects": 5,
      "ingress": [],
      "egress": [],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "payments",
      "name": "gateway-from-checkout",
      "selector": "app=gateway",
      "types": [
        "Ingress"
      ],
      "selects": 2,
      "ingress": [
        "from pods app=checkout-api in namespace shop, on 8443/TCP"
      ],
      "egress": [],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "payments",
      "name": "gateway-to-acquirer",
      "selector": "app=gateway",
      "types": [
        "Egress"
      ],
      "selects": 2,
      "ingress": [],
      "egress": [
        "to 203.0.113.0/24, on 443/TCP"
      ],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "payments",
      "name": "internal",
      "selector": "",
      "types": [
        "Egress"
      ],
      "selects": 5,
      "ingress": [],
      "egress": [
        "to every pod in namespaces pci=true, on every port"
      ],
      "broken": []
    },
    {
      "kind": "CiliumNetworkPolicy",
      "namespace": "payments",
      "name": "ledger-db-deny",
      "selector": "app=ledger-db",
      "types": [
        "Ingress"
      ],
      "selects": 1,
      "ingress": [
        "from deny endpoints namespace notin (payments), on 5432/TCP"
      ],
      "egress": [],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "payments",
      "name": "ledger-from-gateway",
      "selector": "app=ledger",
      "types": [
        "Ingress"
      ],
      "selects": 2,
      "ingress": [
        "from pods app=gateway in this namespace, on http"
      ],
      "egress": [],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "shop",
      "name": "checkout-from-storefront",
      "selector": "app=checkout-api",
      "types": [
        "Ingress"
      ],
      "selects": 4,
      "ingress": [
        "from pods app=storefront in this namespace, on http"
      ],
      "egress": [],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "shop",
      "name": "default-deny",
      "selector": "",
      "types": [
        "Ingress"
      ],
      "selects": 10,
      "ingress": [],
      "egress": [],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "shop",
      "name": "metrics-from-prometheus",
      "selector": "app=checkout-api",
      "types": [
        "Ingress"
      ],
      "selects": 4,
      "ingress": [
        "from pods app=prometheus in namespace monitoring, on metrics"
      ],
      "egress": [],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "shop",
      "name": "redis-from-shop",
      "selector": "app=cart-redis",
      "types": [
        "Ingress"
      ],
      "selects": 1,
      "ingress": [
        "from pods app=checkout-api in this namespace or pods app=order-worker in this namespace, on 6379/TCP"
      ],
      "egress": [],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "shop",
      "name": "storefront-from-ingress",
      "selector": "app=storefront",
      "types": [
        "Ingress"
      ],
      "selects": 3,
      "ingress": [
        "from pods app=controller in namespace ingress-nginx, on http"
      ],
      "egress": [],
      "broken": []
    }
  ],
  "policiesTotal": 20,
  "pods": {
    "total": 31,
    "offset": 0,
    "rows": [
      {
        "namespace": "auth",
        "name": "sso-7d9f8b6c5d-akxqb",
        "workload": "sso",
        "ip": "10.244.0.26",
        "in": true,
        "out": true,
        "hostNetwork": false,
        "policies": 3
      },
      {
        "namespace": "auth",
        "name": "sso-7d9f8b6c5d-blxrb",
        "workload": "sso",
        "ip": "10.244.0.27",
        "in": true,
        "out": true,
        "hostNetwork": false,
        "policies": 3
      },
      {
        "namespace": "batch",
        "name": "nightly-report-7d9f8b6c5d-akxqb",
        "workload": "nightly-report",
        "ip": "10.244.0.37",
        "in": false,
        "out": false,
        "hostNetwork": false,
        "policies": 0
      },
      {
        "namespace": "batch",
        "name": "nightly-report-7d9f8b6c5d-blxrb",
        "workload": "nightly-report",
        "ip": "10.244.0.38",
        "in": false,
        "out": false,
        "hostNetwork": false,
        "policies": 0
      },
      {
        "namespace": "data",
        "name": "warehouse-loader-7d9f8b6c5d-akxqb",
        "workload": "warehouse-loader",
        "ip": "10.244.0.34",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 1
      },
      {
        "namespace": "data",
        "name": "warehouse-loader-7d9f8b6c5d-blxrb",
        "workload": "warehouse-loader",
        "ip": "10.244.0.35",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 1
      },
      {
        "namespace": "data",
        "name": "warehouse-loader-7d9f8b6c5d-cmxsb",
        "workload": "warehouse-loader",
        "ip": "10.244.0.36",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 1
      },
      {
        "namespace": "ingress-nginx",
        "name": "controller-7d9f8b6c5d-akxqb",
        "workload": "controller",
        "ip": "10.244.0.28",
        "in": false,
        "out": false,
        "hostNetwork": false,
        "policies": 0
      },
      {
        "namespace": "ingress-nginx",
        "name": "controller-7d9f8b6c5d-blxrb",
        "workload": "controller",
        "ip": "10.244.0.29",
        "in": false,
        "out": false,
        "hostNetwork": false,
        "policies": 0
      },
      {
        "namespace": "kube-system",
        "name": "cilium-node-a",
        "workload": "cilium-node-a",
        "ip": "172.18.0.2",
        "in": false,
        "out": false,
        "hostNetwork": true,
        "policies": 0
      },
      {
        "namespace": "kube-system",
        "name": "cilium-node-b",
        "workload": "cilium-node-b",
        "ip": "172.18.0.2",
        "in": false,
        "out": false,
        "hostNetwork": true,
        "policies": 0
      },
      {
        "namespace": "kube-system",
        "name": "cilium-node-c",
        "workload": "cilium-node-c",
        "ip": "172.18.0.2",
        "in": false,
        "out": false,
        "hostNetwork": true,
        "policies": 0
      },
      {
        "namespace": "kube-system",
        "name": "coredns-7d9f8b6c5d-akxqb",
        "workload": "coredns",
        "ip": "10.244.0.32",
        "in": false,
        "out": false,
        "hostNetwork": false,
        "policies": 0
      },
      {
        "namespace": "kube-system",
        "name": "coredns-7d9f8b6c5d-blxrb",
        "workload": "coredns",
        "ip": "10.244.0.33",
        "in": false,
        "out": false,
        "hostNetwork": false,
        "policies": 0
      },
      {
        "namespace": "monitoring",
        "name": "grafana-0",
        "workload": "grafana",
        "ip": "10.244.0.31",
        "in": true,
        "out": true,
        "hostNetwork": false,
        "policies": 2
      },
      {
        "namespace": "monitoring",
        "name": "prometheus-0",
        "workload": "prometheus",
        "ip": "10.244.0.30",
        "in": true,
        "out": true,
        "hostNetwork": false,
        "policies": 1
      },
      {
        "namespace": "payments",
        "name": "gateway-7d9f8b6c5d-akxqb",
        "workload": "gateway",
        "ip": "10.244.0.21",
        "in": true,
        "out": true,
        "hostNetwork": false,
        "policies": 5
      },
      {
        "namespace": "payments",
        "name": "gateway-7d9f8b6c5d-blxrb",
        "workload": "gateway",
        "ip": "10.244.0.22",
        "in": true,
        "out": true,
        "hostNetwork": false,
        "policies": 5
      },
      {
        "namespace": "payments",
        "name": "ledger-7d9f8b6c5d-akxqb",
        "workload": "ledger",
        "ip": "10.244.0.23",
        "in": true,
        "out": true,
        "hostNetwork": false,
        "policies": 4
      },
      {
        "namespace": "payments",
        "name": "ledger-7d9f8b6c5d-blxrb",
        "workload": "ledger",
        "ip": "10.244.0.24",
        "in": true,
        "out": true,
        "hostNetwork": false,
        "policies": 4
      },
      {
        "namespace": "payments",
        "name": "ledger-db-0",
        "workload": "ledger-db",
        "ip": "10.244.0.25",
        "in": true,
        "out": true,
        "hostNetwork": false,
        "policies": 5
      },
      {
        "namespace": "shop",
        "name": "cart-redis-0",
        "workload": "cart-redis",
        "ip": "10.244.0.20",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 2
      },
      {
        "namespace": "shop",
        "name": "checkout-api-7d9f8b6c5d-akxqb",
        "workload": "checkout-api",
        "ip": "10.244.0.14",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 3
      },
      {
        "namespace": "shop",
        "name": "checkout-api-7d9f8b6c5d-blxrb",
        "workload": "checkout-api",
        "ip": "10.244.0.15",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 3
      },
      {
        "namespace": "shop",
        "name": "checkout-api-7d9f8b6c5d-cmxsb",
        "workload": "checkout-api",
        "ip": "10.244.0.16",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 3
      },
      {
        "namespace": "shop",
        "name": "checkout-api-7d9f8b6c5d-dnxqb",
        "workload": "checkout-api",
        "ip": "10.244.0.17",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 3
      },
      {
        "namespace": "shop",
        "name": "order-worker-7d9f8b6c5d-akxqb",
        "workload": "order-worker",
        "ip": "10.244.0.18",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 1
      },
      {
        "namespace": "shop",
        "name": "order-worker-7d9f8b6c5d-blxrb",
        "workload": "order-worker",
        "ip": "10.244.0.19",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 1
      },
      {
        "namespace": "shop",
        "name": "storefront-7d9f8b6c5d-akxqb",
        "workload": "storefront",
        "ip": "10.244.0.11",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 2
      },
      {
        "namespace": "shop",
        "name": "storefront-7d9f8b6c5d-blxrb",
        "workload": "storefront",
        "ip": "10.244.0.12",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 2
      },
      {
        "namespace": "shop",
        "name": "storefront-7d9f8b6c5d-cmxsb",
        "workload": "storefront",
        "ip": "10.244.0.13",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 2
      }
    ]
  },
  "graph": {
    "scope": "cluster",
    "nodes": [
      {
        "id": "ns:payments",
        "label": "payments",
        "kind": "namespace",
        "namespace": "payments",
        "pods": 5,
        "isolatedIn": 5,
        "isolatedOut": 5,
        "internal": 3
      },
      {
        "id": "ns:auth",
        "label": "auth",
        "kind": "namespace",
        "namespace": "auth",
        "pods": 2,
        "isolatedIn": 2,
        "isolatedOut": 2,
        "internal": 0
      },
      {
        "id": "ns:monitoring",
        "label": "monitoring",
        "kind": "namespace",
        "namespace": "monitoring",
        "pods": 2,
        "isolatedIn": 2,
        "isolatedOut": 2,
        "internal": 0
      },
      {
        "id": "ns:shop",
        "label": "shop",
        "kind": "namespace",
        "namespace": "shop",
        "pods": 10,
        "isolatedIn": 10,
        "isolatedOut": 0,
        "internal": 2
      },
      {
        "id": "ns:kube-system",
        "label": "kube-system",
        "kind": "namespace",
        "namespace": "kube-system",
        "pods": 5,
        "isolatedIn": 0,
        "isolatedOut": 0,
        "internal": 0
      },
      {
        "id": "ns:ingress-nginx",
        "label": "ingress-nginx",
        "kind": "namespace",
        "namespace": "ingress-nginx",
        "pods": 2,
        "isolatedIn": 0,
        "isolatedOut": 0,
        "internal": 0
      },
      {
        "id": "ns:data",
        "label": "data",
        "kind": "namespace",
        "namespace": "data",
        "pods": 3,
        "isolatedIn": 3,
        "isolatedOut": 0,
        "internal": 1
      },
      {
        "id": "ns:batch",
        "label": "batch",
        "kind": "namespace",
        "namespace": "batch",
        "pods": 2,
        "isolatedIn": 0,
        "isolatedOut": 0,
        "internal": 0
      },
      {
        "id": "entity:host",
        "label": "host",
        "kind": "entity",
        "pods": 0,
        "isolatedIn": 0,
        "isolatedOut": 0,
        "internal": 0
      },
      {
        "id": "entity:kube-apiserver",
        "label": "kube-apiserver",
        "kind": "entity",
        "pods": 0,
        "isolatedIn": 0,
        "isolatedOut": 0,
        "internal": 0
      },
      {
        "id": "entity:remote-node",
        "label": "remote-node",
        "kind": "entity",
        "pods": 0,
        "isolatedIn": 0,
        "isolatedOut": 0,
        "internal": 0
      },
      {
        "id": "cidr:203.0.113.0/24",
        "label": "203.0.113.0/24",
        "kind": "cidr",
        "pods": 0,
        "isolatedIn": 0,
        "isolatedOut": 0,
        "internal": 0
      },
      {
        "id": "unevaluated:toFQDNs",
        "label": "toFQDNs",
        "kind": "unevaluated",
        "pods": 0,
        "isolatedIn": 0,
        "isolatedOut": 0,
        "internal": 0
      }
    ],
    "edges": [
      {
        "kind": "allow",
        "from": "entity:host",
        "to": "ns:auth",
        "side": "ingress",
        "ports": [
          "every port"
        ],
        "policies": [
          "cluster-baseline"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "entity:host",
        "to": "ns:monitoring",
        "side": "ingress",
        "ports": [
          "every port"
        ],
        "policies": [
          "cluster-baseline"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "entity:remote-node",
        "to": "ns:auth",
        "side": "ingress",
        "ports": [
          "every port"
        ],
        "policies": [
          "cluster-baseline"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "entity:remote-node",
        "to": "ns:monitoring",
        "side": "ingress",
        "ports": [
          "every port"
        ],
        "policies": [
          "cluster-baseline"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "ns:auth",
        "to": "entity:kube-apiserver",
        "side": "egress",
        "ports": [
          "every port"
        ],
        "policies": [
          "cluster-baseline"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "ns:auth",
        "to": "ns:kube-system",
        "side": "egress",
        "ports": [
          "53/UDP"
        ],
        "policies": [
          "cluster-baseline"
        ],
        "rules": 1
      },
      {
        "kind": "unevaluated",
        "from": "ns:auth",
        "to": "ns:monitoring",
        "side": "egress",
        "ports": [
          "3000/TCP"
        ],
        "policies": [
          "sso-to-idp"
        ],
        "rules": 1
      },
      {
        "kind": "deny",
        "from": "ns:auth",
        "to": "ns:payments",
        "side": "ingress",
        "ports": [
          "5432/TCP"
        ],
        "policies": [
          "ledger-db-deny"
        ],
        "rules": 1
      },
      {
        "kind": "unevaluated",
        "from": "ns:auth",
        "to": "unevaluated:toFQDNs",
        "side": "egress",
        "ports": [
          "443/TCP"
        ],
        "policies": [
          "sso-to-idp"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "ns:batch",
        "to": "ns:data",
        "side": "ingress",
        "ports": [
          "8080/TCP"
        ],
        "policies": [
          "loader-from-batch"
        ],
        "rules": 1
      },
      {
        "kind": "deny",
        "from": "ns:batch",
        "to": "ns:payments",
        "side": "ingress",
        "ports": [
          "5432/TCP"
        ],
        "policies": [
          "ledger-db-deny"
        ],
        "rules": 1
      },
      {
        "kind": "deny",
        "from": "ns:data",
        "to": "ns:payments",
        "side": "ingress",
        "ports": [
          "5432/TCP"
        ],
        "policies": [
          "ledger-db-deny"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "ns:ingress-nginx",
        "to": "ns:monitoring",
        "side": "ingress",
        "ports": [
          "http"
        ],
        "policies": [
          "grafana-from-ingress"
        ],
        "rules": 1
      },
      {
        "kind": "deny",
        "from": "ns:ingress-nginx",
        "to": "ns:payments",
        "side": "ingress",
        "ports": [
          "5432/TCP"
        ],
        "policies": [
          "ledger-db-deny"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "ns:ingress-nginx",
        "to": "ns:shop",
        "side": "ingress",
        "ports": [
          "http"
        ],
        "policies": [
          "storefront-from-ingress"
        ],
        "rules": 1
      },
      {
        "kind": "deny",
        "from": "ns:kube-system",
        "to": "ns:payments",
        "side": "ingress",
        "ports": [
          "5432/TCP"
        ],
        "policies": [
          "ledger-db-deny"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "ns:monitoring",
        "to": "entity:kube-apiserver",
        "side": "egress",
        "ports": [
          "every port"
        ],
        "policies": [
          "cluster-baseline"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "ns:monitoring",
        "to": "ns:kube-system",
        "side": "egress",
        "ports": [
          "53/UDP"
        ],
        "policies": [
          "cluster-baseline"
        ],
        "rules": 1
      },
      {
        "kind": "deny",
        "from": "ns:monitoring",
        "to": "ns:payments",
        "side": "ingress",
        "ports": [
          "5432/TCP"
        ],
        "policies": [
          "ledger-db-deny"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "ns:monitoring",
        "to": "ns:shop",
        "side": "ingress",
        "ports": [
          "metrics"
        ],
        "policies": [
          "metrics-from-prometheus"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "ns:payments",
        "to": "cidr:203.0.113.0/24",
        "side": "egress",
        "ports": [
          "443/TCP"
        ],
        "policies": [
          "gateway-to-acquirer"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "ns:payments",
        "to": "ns:auth",
        "side": "ingress",
        "ports": [
          "http"
        ],
        "policies": [
          "sso-from-apps"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "ns:payments",
        "to": "ns:kube-system",
        "side": "egress",
        "ports": [
          "53/TCP",
          "53/UDP"
        ],
        "policies": [
          "allow-dns"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "ns:shop",
        "to": "ns:auth",
        "side": "ingress",
        "ports": [
          "http"
        ],
        "policies": [
          "sso-from-apps"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "ns:shop",
        "to": "ns:payments",
        "side": "ingress",
        "ports": [
          "8443/TCP"
        ],
        "policies": [
          "gateway-from-checkout"
        ],
        "rules": 1
      },
      {
        "kind": "deny",
        "from": "ns:shop",
        "to": "ns:payments",
        "side": "ingress",
        "ports": [
          "5432/TCP"
        ],
        "policies": [
          "ledger-db-deny"
        ],
        "rules": 1
      }
    ],
    "folded": 0,
    "edgesTotal": 26
  },
  "cilium": {
    "served": true,
    "policies": 3,
    "hostPolicies": 0,
    "notEvaluated": [
      "layer-7 rules: 1",
      "toFQDNs: 1"
    ]
  }
};

const SHOP: Overview = {
  "mode": "rules",
  "namespace": "shop",
  "complete": true,
  "notes": [],
  "cni": {
    "name": "Cilium",
    "enforces": "yes",
    "detail": "Recognized from its DaemonSet: Cilium applies NetworkPolicies."
  },
  "unevaluated": [],
  "summary": {
    "pods": 10,
    "isolatedIn": 10,
    "isolatedOut": 0,
    "open": 0,
    "hostNetwork": 0,
    "policies": 5,
    "defaultDeny": 1,
    "idle": 0
  },
  "namespaces": [
    {
      "name": "shop",
      "pods": 10,
      "isolatedIn": 10,
      "isolatedOut": 0,
      "policies": 5,
      "defaultDenyIn": true,
      "defaultDenyOut": false
    }
  ],
  "namespacesTotal": 1,
  "policies": [
    {
      "kind": "NetworkPolicy",
      "namespace": "shop",
      "name": "checkout-from-storefront",
      "selector": "app=checkout-api",
      "types": [
        "Ingress"
      ],
      "selects": 4,
      "ingress": [
        "from pods app=storefront in this namespace, on http"
      ],
      "egress": [],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "shop",
      "name": "default-deny",
      "selector": "",
      "types": [
        "Ingress"
      ],
      "selects": 10,
      "ingress": [],
      "egress": [],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "shop",
      "name": "metrics-from-prometheus",
      "selector": "app=checkout-api",
      "types": [
        "Ingress"
      ],
      "selects": 4,
      "ingress": [
        "from pods app=prometheus in namespace monitoring, on metrics"
      ],
      "egress": [],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "shop",
      "name": "redis-from-shop",
      "selector": "app=cart-redis",
      "types": [
        "Ingress"
      ],
      "selects": 1,
      "ingress": [
        "from pods app=checkout-api in this namespace or pods app=order-worker in this namespace, on 6379/TCP"
      ],
      "egress": [],
      "broken": []
    },
    {
      "kind": "NetworkPolicy",
      "namespace": "shop",
      "name": "storefront-from-ingress",
      "selector": "app=storefront",
      "types": [
        "Ingress"
      ],
      "selects": 3,
      "ingress": [
        "from pods app=controller in namespace ingress-nginx, on http"
      ],
      "egress": [],
      "broken": []
    }
  ],
  "policiesTotal": 5,
  "pods": {
    "total": 10,
    "offset": 0,
    "rows": [
      {
        "namespace": "shop",
        "name": "cart-redis-0",
        "workload": "cart-redis",
        "ip": "10.244.0.20",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 2
      },
      {
        "namespace": "shop",
        "name": "checkout-api-7d9f8b6c5d-akxqb",
        "workload": "checkout-api",
        "ip": "10.244.0.14",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 3
      },
      {
        "namespace": "shop",
        "name": "checkout-api-7d9f8b6c5d-blxrb",
        "workload": "checkout-api",
        "ip": "10.244.0.15",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 3
      },
      {
        "namespace": "shop",
        "name": "checkout-api-7d9f8b6c5d-cmxsb",
        "workload": "checkout-api",
        "ip": "10.244.0.16",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 3
      },
      {
        "namespace": "shop",
        "name": "checkout-api-7d9f8b6c5d-dnxqb",
        "workload": "checkout-api",
        "ip": "10.244.0.17",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 3
      },
      {
        "namespace": "shop",
        "name": "order-worker-7d9f8b6c5d-akxqb",
        "workload": "order-worker",
        "ip": "10.244.0.18",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 1
      },
      {
        "namespace": "shop",
        "name": "order-worker-7d9f8b6c5d-blxrb",
        "workload": "order-worker",
        "ip": "10.244.0.19",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 1
      },
      {
        "namespace": "shop",
        "name": "storefront-7d9f8b6c5d-akxqb",
        "workload": "storefront",
        "ip": "10.244.0.11",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 2
      },
      {
        "namespace": "shop",
        "name": "storefront-7d9f8b6c5d-blxrb",
        "workload": "storefront",
        "ip": "10.244.0.12",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 2
      },
      {
        "namespace": "shop",
        "name": "storefront-7d9f8b6c5d-cmxsb",
        "workload": "storefront",
        "ip": "10.244.0.13",
        "in": true,
        "out": false,
        "hostNetwork": false,
        "policies": 2
      }
    ]
  },
  "graph": {
    "scope": "namespace",
    "nodes": [
      {
        "id": "wl:shop/checkout-api",
        "label": "checkout-api",
        "kind": "workload",
        "namespace": "shop",
        "pods": 4,
        "isolatedIn": 4,
        "isolatedOut": 0,
        "internal": 0,
        "focus": true
      },
      {
        "id": "wl:shop/storefront",
        "label": "storefront",
        "kind": "workload",
        "namespace": "shop",
        "pods": 3,
        "isolatedIn": 3,
        "isolatedOut": 0,
        "internal": 0,
        "focus": true
      },
      {
        "id": "wl:shop/cart-redis",
        "label": "cart-redis",
        "kind": "workload",
        "namespace": "shop",
        "pods": 1,
        "isolatedIn": 1,
        "isolatedOut": 0,
        "internal": 0,
        "focus": true
      },
      {
        "id": "wl:shop/order-worker",
        "label": "order-worker",
        "kind": "workload",
        "namespace": "shop",
        "pods": 2,
        "isolatedIn": 2,
        "isolatedOut": 0,
        "internal": 0,
        "focus": true
      },
      {
        "id": "ns:payments",
        "label": "payments",
        "kind": "namespace",
        "namespace": "payments",
        "pods": 5,
        "isolatedIn": 5,
        "isolatedOut": 5,
        "internal": 0
      },
      {
        "id": "ns:auth",
        "label": "auth",
        "kind": "namespace",
        "namespace": "auth",
        "pods": 2,
        "isolatedIn": 2,
        "isolatedOut": 2,
        "internal": 0
      },
      {
        "id": "ns:ingress-nginx",
        "label": "ingress-nginx",
        "kind": "namespace",
        "namespace": "ingress-nginx",
        "pods": 2,
        "isolatedIn": 0,
        "isolatedOut": 0,
        "internal": 0
      },
      {
        "id": "ns:monitoring",
        "label": "monitoring",
        "kind": "namespace",
        "namespace": "monitoring",
        "pods": 2,
        "isolatedIn": 2,
        "isolatedOut": 2,
        "internal": 0
      }
    ],
    "edges": [
      {
        "kind": "allow",
        "from": "ns:ingress-nginx",
        "to": "wl:shop/storefront",
        "side": "ingress",
        "ports": [
          "http"
        ],
        "policies": [
          "storefront-from-ingress"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "ns:monitoring",
        "to": "wl:shop/checkout-api",
        "side": "ingress",
        "ports": [
          "metrics"
        ],
        "policies": [
          "metrics-from-prometheus"
        ],
        "rules": 1
      },
      {
        "kind": "deny",
        "from": "wl:shop/cart-redis",
        "to": "ns:payments",
        "side": "ingress",
        "ports": [
          "5432/TCP"
        ],
        "policies": [
          "ledger-db-deny"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "wl:shop/checkout-api",
        "to": "ns:auth",
        "side": "ingress",
        "ports": [
          "http"
        ],
        "policies": [
          "sso-from-apps"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "wl:shop/checkout-api",
        "to": "ns:payments",
        "side": "ingress",
        "ports": [
          "8443/TCP"
        ],
        "policies": [
          "gateway-from-checkout"
        ],
        "rules": 1
      },
      {
        "kind": "deny",
        "from": "wl:shop/checkout-api",
        "to": "ns:payments",
        "side": "ingress",
        "ports": [
          "5432/TCP"
        ],
        "policies": [
          "ledger-db-deny"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "wl:shop/checkout-api",
        "to": "wl:shop/cart-redis",
        "side": "ingress",
        "ports": [
          "6379/TCP"
        ],
        "policies": [
          "redis-from-shop"
        ],
        "rules": 1
      },
      {
        "kind": "deny",
        "from": "wl:shop/order-worker",
        "to": "ns:payments",
        "side": "ingress",
        "ports": [
          "5432/TCP"
        ],
        "policies": [
          "ledger-db-deny"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "wl:shop/order-worker",
        "to": "wl:shop/cart-redis",
        "side": "ingress",
        "ports": [
          "6379/TCP"
        ],
        "policies": [
          "redis-from-shop"
        ],
        "rules": 1
      },
      {
        "kind": "deny",
        "from": "wl:shop/storefront",
        "to": "ns:payments",
        "side": "ingress",
        "ports": [
          "5432/TCP"
        ],
        "policies": [
          "ledger-db-deny"
        ],
        "rules": 1
      },
      {
        "kind": "allow",
        "from": "wl:shop/storefront",
        "to": "wl:shop/checkout-api",
        "side": "ingress",
        "ports": [
          "http"
        ],
        "policies": [
          "checkout-from-storefront"
        ],
        "rules": 1
      }
    ],
    "folded": 0,
    "edgesTotal": 11
  },
  "cilium": {
    "served": true,
    "policies": 3,
    "hostPolicies": 0,
    "notEvaluated": [
      "layer-7 rules: 1",
      "toFQDNs: 1"
    ]
  },
  "view": {
    "ingress": {
      "open": [],
      "openPods": 0,
      "lanes": [
        {
          "namespace": "shop",
          "name": "checkout-from-storefront",
          "kind": "allow",
          "rule": 1,
          "text": "pods app=storefront in this namespace, on http",
          "peers": [
            {
              "kind": "pods",
              "label": "pods app=storefront in this namespace",
              "pods": 3,
              "namespaces": [
                "shop"
              ]
            }
          ],
          "ports": [
            "http"
          ],
          "targets": [
            "checkout-api"
          ],
          "pods": 4
        },
        {
          "namespace": "shop",
          "name": "metrics-from-prometheus",
          "kind": "allow",
          "rule": 1,
          "text": "pods app=prometheus in namespace monitoring, on metrics",
          "peers": [
            {
              "kind": "pods",
              "label": "pods app=prometheus in namespace monitoring",
              "pods": 2,
              "namespaces": [
                "monitoring"
              ]
            }
          ],
          "ports": [
            "metrics"
          ],
          "targets": [
            "checkout-api"
          ],
          "pods": 4
        },
        {
          "namespace": "shop",
          "name": "redis-from-shop",
          "kind": "allow",
          "rule": 1,
          "text": "pods app=checkout-api in this namespace or pods app=order-worker in this namespace, on 6379/TCP",
          "peers": [
            {
              "kind": "pods",
              "label": "pods app=checkout-api in this namespace",
              "pods": 4,
              "namespaces": [
                "shop"
              ]
            },
            {
              "kind": "pods",
              "label": "pods app=order-worker in this namespace",
              "pods": 2,
              "namespaces": [
                "shop"
              ]
            }
          ],
          "ports": [
            "6379/TCP"
          ],
          "targets": [
            "cart-redis"
          ],
          "pods": 1
        },
        {
          "namespace": "shop",
          "name": "storefront-from-ingress",
          "kind": "allow",
          "rule": 1,
          "text": "pods app=controller in namespace ingress-nginx, on http",
          "peers": [
            {
              "kind": "pods",
              "label": "pods app=controller in namespace ingress-nginx",
              "pods": 2,
              "namespaces": [
                "ingress-nginx"
              ]
            }
          ],
          "ports": [
            "http"
          ],
          "targets": [
            "storefront"
          ],
          "pods": 3
        }
      ],
      "lanesTotal": 4,
      "closed": [
        "order-worker"
      ]
    },
    "egress": {
      "open": [
        "cart-redis",
        "checkout-api",
        "order-worker",
        "storefront"
      ],
      "openPods": 10,
      "lanes": [],
      "lanesTotal": 0,
      "closed": []
    },
    "workloads": [
      {
        "name": "cart-redis",
        "pods": 1,
        "isolatedIn": 1,
        "isolatedOut": 0,
        "hostNetwork": 0,
        "from": [
          "pods app=checkout-api in this namespace",
          "pods app=order-worker in this namespace"
        ],
        "to": []
      },
      {
        "name": "checkout-api",
        "pods": 4,
        "isolatedIn": 4,
        "isolatedOut": 0,
        "hostNetwork": 0,
        "from": [
          "pods app=storefront in this namespace",
          "pods app=prometheus in namespace monitoring"
        ],
        "to": []
      },
      {
        "name": "order-worker",
        "pods": 2,
        "isolatedIn": 2,
        "isolatedOut": 0,
        "hostNetwork": 0,
        "from": [],
        "to": []
      },
      {
        "name": "storefront",
        "pods": 3,
        "isolatedIn": 3,
        "isolatedOut": 0,
        "hostNetwork": 0,
        "from": [
          "pods app=controller in namespace ingress-nginx"
        ],
        "to": []
      }
    ],
    "workloadsTotal": 4
  }
};

const DENIED: Verdict = {
  "mode": "rules",
  "answer": "no",
  "summary": "The rules refuse it: the destination may not receive it.",
  "from": "shop/storefront-7d9f8b6c5d-akxqb",
  "to": "payments/gateway-7d9f8b6c5d-akxqb",
  "port": 8443,
  "protocol": "TCP",
  "egress": {
    "direction": "egress",
    "pod": "shop/storefront-7d9f8b6c5d-akxqb",
    "state": "open",
    "isolating": [],
    "reason": "No policy selects this pod for egress: everything passes in this direction.",
    "caveats": []
  },
  "ingress": {
    "direction": "ingress",
    "pod": "payments/gateway-7d9f8b6c5d-akxqb",
    "state": "denied",
    "isolating": [
      {
        "namespace": "payments",
        "name": "default-deny"
      },
      {
        "namespace": "payments",
        "name": "gateway-from-checkout"
      }
    ],
    "reason": "Isolated for ingress by default-deny, gateway-from-checkout, and no rule of theirs allows this peer on this port.",
    "caveats": []
  },
  "complete": true,
  "notes": [],
  "cni": {
    "name": "Cilium",
    "enforces": "yes",
    "detail": "Recognized from its DaemonSet: Cilium applies NetworkPolicies."
  },
  "unevaluated": [],
  "policy": "apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n  name: allow-ingress-shop-storefront-3994a1\n  namespace: payments\nspec:\n  ingress:\n  - from:\n    - namespaceSelector:\n        matchLabels:\n          kubernetes.io/metadata.name: shop\n      podSelector:\n        matchLabels:\n          app: storefront\n    ports:\n    - port: 8443\n      protocol: TCP\n  podSelector:\n    matchLabels:\n      app: gateway\n  policyTypes:\n  - Ingress\n"
};

const ALLOWED: Verdict = {
  "mode": "rules",
  "answer": "yes",
  "summary": "The rules allow it on both sides.",
  "from": "shop/checkout-api-7d9f8b6c5d-akxqb",
  "to": "shop/cart-redis-0",
  "port": 6379,
  "protocol": "TCP",
  "egress": {
    "direction": "egress",
    "pod": "shop/checkout-api-7d9f8b6c5d-akxqb",
    "state": "open",
    "isolating": [],
    "reason": "No policy selects this pod for egress: everything passes in this direction.",
    "caveats": []
  },
  "ingress": {
    "direction": "ingress",
    "pod": "shop/cart-redis-0",
    "state": "allowed",
    "isolating": [
      {
        "namespace": "shop",
        "name": "default-deny"
      },
      {
        "namespace": "shop",
        "name": "redis-from-shop"
      }
    ],
    "decided": {
      "namespace": "shop",
      "name": "redis-from-shop",
      "kind": "allow",
      "rule": 1,
      "text": "pods app=checkout-api in this namespace or pods app=order-worker in this namespace, on 6379/TCP"
    },
    "reason": "Allowed by redis-from-shop, ingress rule 1.",
    "caveats": []
  },
  "complete": true,
  "notes": [],
  "cni": {
    "name": "Cilium",
    "enforces": "yes",
    "detail": "Recognized from its DaemonSet: Cilium applies NetworkPolicies."
  },
  "unevaluated": [],
  "policy": ""
};

const POD: PodAnswer = {
  "mode": "rules",
  "complete": true,
  "notes": [],
  "cni": {
    "name": "Cilium",
    "enforces": "yes",
    "detail": "Recognized from its DaemonSet: Cilium applies NetworkPolicies."
  },
  "unevaluated": [],
  "pod": {
    "namespace": "payments",
    "name": "ledger-db-0",
    "workload": "ledger-db",
    "ip": "10.244.0.25",
    "in": true,
    "out": true,
    "hostNetwork": false,
    "policies": 5
  },
  "ingress": {
    "isolated": true,
    "policies": [
      {
        "namespace": "payments",
        "name": "default-deny"
      },
      {
        "namespace": "payments",
        "name": "db-from-ledger"
      },
      {
        "namespace": "payments",
        "name": "ledger-db-deny"
      }
    ],
    "rules": [
      {
        "namespace": "payments",
        "name": "db-from-ledger",
        "kind": "allow",
        "rule": 1,
        "text": "pods app=ledger in this namespace, on 5432/TCP",
        "peers": [
          {
            "kind": "pods",
            "label": "pods app=ledger in this namespace",
            "pods": 2,
            "namespaces": [
              "payments"
            ]
          }
        ],
        "ports": [
          "5432/TCP"
        ]
      },
      {
        "namespace": "payments",
        "name": "ledger-db-deny",
        "kind": "deny",
        "rule": 1,
        "text": "deny endpoints namespace notin (payments), on 5432/TCP",
        "peers": [
          {
            "kind": "pods",
            "label": "endpoints namespace notin (payments)",
            "pods": 26,
            "namespaces": [
              "shop",
              "auth",
              "ingress-nginx",
              "monitoring",
              "kube-system",
              "data"
            ]
          }
        ],
        "ports": [
          "5432/TCP"
        ]
      }
    ]
  },
  "egress": {
    "isolated": true,
    "policies": [
      {
        "namespace": "payments",
        "name": "default-deny"
      },
      {
        "namespace": "payments",
        "name": "allow-dns"
      },
      {
        "namespace": "payments",
        "name": "internal"
      }
    ],
    "rules": [
      {
        "namespace": "payments",
        "name": "allow-dns",
        "kind": "allow",
        "rule": 1,
        "text": "pods app=coredns in namespace kube-system, on 53/UDP, 53/TCP",
        "peers": [
          {
            "kind": "pods",
            "label": "pods app=coredns in namespace kube-system",
            "pods": 2,
            "namespaces": [
              "kube-system"
            ]
          }
        ],
        "ports": [
          "53/UDP",
          "53/TCP"
        ]
      },
      {
        "namespace": "payments",
        "name": "internal",
        "kind": "allow",
        "rule": 1,
        "text": "every pod in namespaces pci=true, on every port",
        "peers": [
          {
            "kind": "pods",
            "label": "every pod in namespaces pci=true",
            "pods": 5,
            "namespaces": [
              "payments"
            ]
          }
        ],
        "ports": [
          "every port"
        ]
      }
    ]
  }
};

const FLOWS: Flows = {
  "mode": "observed",
  "state": "read",
  "window": 300,
  "read": 1613,
  "hidden": 0,
  "truncated": false,
  "forwarded": 1589,
  "dropped": 24,
  "rows": [
    {
      "from": "shop/checkout-api-7d9f8b6c5d-akxqb",
      "to": "shop/cart-redis-0",
      "port": 6379,
      "protocol": "TCP",
      "verdict": "FORWARDED",
      "deniedBy": [],
      "count": 640,
      "last": "2026-03-11T09:38:00Z"
    },
    {
      "from": "ingress-nginx/controller-7d9f8b6c5d-akxqb",
      "to": "shop/storefront-7d9f8b6c5d-akxqb",
      "port": 8080,
      "protocol": "TCP",
      "verdict": "FORWARDED",
      "deniedBy": [],
      "count": 412,
      "last": "2026-03-11T09:38:00Z"
    },
    {
      "from": "shop/storefront-7d9f8b6c5d-akxqb",
      "to": "shop/checkout-api-7d9f8b6c5d-akxqb",
      "port": 8080,
      "protocol": "TCP",
      "verdict": "FORWARDED",
      "deniedBy": [],
      "count": 233,
      "last": "2026-03-11T09:38:00Z"
    },
    {
      "from": "monitoring/prometheus-0",
      "to": "shop/checkout-api-7d9f8b6c5d-akxqb",
      "port": 9090,
      "protocol": "TCP",
      "verdict": "FORWARDED",
      "deniedBy": [],
      "count": 120,
      "last": "2026-03-11T09:38:00Z"
    },
    {
      "from": "shop/checkout-api-7d9f8b6c5d-akxqb",
      "to": "payments/gateway-7d9f8b6c5d-akxqb",
      "port": 8443,
      "protocol": "TCP",
      "verdict": "FORWARDED",
      "deniedBy": [],
      "count": 96,
      "last": "2026-03-11T09:38:00Z"
    },
    {
      "from": "payments/gateway-7d9f8b6c5d-akxqb",
      "to": "203.0.113.9",
      "port": 443,
      "protocol": "TCP",
      "verdict": "FORWARDED",
      "deniedBy": [],
      "count": 88,
      "last": "2026-03-11T09:38:00Z"
    },
    {
      "from": "data/warehouse-loader-7d9f8b6c5d-akxqb",
      "to": "payments/ledger-db-0",
      "port": 5432,
      "protocol": "TCP",
      "verdict": "DROPPED",
      "reason": "POLICY_DENIED",
      "deniedBy": [
        "payments/ledger-db-deny"
      ],
      "count": 12,
      "last": "2026-03-11T09:38:00Z"
    },
    {
      "from": "shop/storefront-7d9f8b6c5d-akxqb",
      "to": "payments/gateway-7d9f8b6c5d-akxqb",
      "port": 8443,
      "protocol": "TCP",
      "verdict": "DROPPED",
      "reason": "POLICY_DENIED",
      "deniedBy": [
        "payments/default-deny"
      ],
      "count": 7,
      "last": "2026-03-11T09:38:00Z"
    },
    {
      "from": "auth/sso-7d9f8b6c5d-akxqb",
      "to": "198.51.100.20",
      "port": 443,
      "protocol": "TCP",
      "verdict": "DROPPED",
      "reason": "POLICY_DENIED",
      "deniedBy": [],
      "count": 5,
      "last": "2026-03-11T09:38:00Z"
    }
  ],
  "edges": [
    {
      "from": "ns:auth",
      "to": "entity:world",
      "forwarded": 0,
      "dropped": 5,
      "ports": [
        "443/TCP"
      ],
      "deniedBy": []
    },
    {
      "from": "ns:data",
      "to": "ns:payments",
      "forwarded": 0,
      "dropped": 12,
      "ports": [
        "5432/TCP"
      ],
      "deniedBy": [
        "payments/ledger-db-deny"
      ]
    },
    {
      "from": "ns:ingress-nginx",
      "to": "ns:shop",
      "forwarded": 412,
      "dropped": 0,
      "ports": [
        "8080/TCP"
      ],
      "deniedBy": []
    },
    {
      "from": "ns:monitoring",
      "to": "ns:shop",
      "forwarded": 120,
      "dropped": 0,
      "ports": [
        "9090/TCP"
      ],
      "deniedBy": []
    },
    {
      "from": "ns:payments",
      "to": "entity:world",
      "forwarded": 88,
      "dropped": 0,
      "ports": [
        "443/TCP"
      ],
      "deniedBy": []
    },
    {
      "from": "ns:shop",
      "to": "ns:payments",
      "forwarded": 96,
      "dropped": 7,
      "ports": [
        "8443/TCP"
      ],
      "deniedBy": [
        "payments/default-deny"
      ]
    }
  ]
};

const SHOP_FLOWS: Flows = {
  "mode": "observed",
  "state": "read",
  "window": 300,
  "read": 1613,
  "hidden": 0,
  "truncated": false,
  "forwarded": 1501,
  "dropped": 7,
  "rows": [
    {
      "from": "shop/checkout-api-7d9f8b6c5d-akxqb",
      "to": "shop/cart-redis-0",
      "port": 6379,
      "protocol": "TCP",
      "verdict": "FORWARDED",
      "deniedBy": [],
      "count": 640,
      "last": "2026-03-11T09:38:00Z"
    },
    {
      "from": "ingress-nginx/controller-7d9f8b6c5d-akxqb",
      "to": "shop/storefront-7d9f8b6c5d-akxqb",
      "port": 8080,
      "protocol": "TCP",
      "verdict": "FORWARDED",
      "deniedBy": [],
      "count": 412,
      "last": "2026-03-11T09:38:00Z"
    },
    {
      "from": "shop/storefront-7d9f8b6c5d-akxqb",
      "to": "shop/checkout-api-7d9f8b6c5d-akxqb",
      "port": 8080,
      "protocol": "TCP",
      "verdict": "FORWARDED",
      "deniedBy": [],
      "count": 233,
      "last": "2026-03-11T09:38:00Z"
    },
    {
      "from": "monitoring/prometheus-0",
      "to": "shop/checkout-api-7d9f8b6c5d-akxqb",
      "port": 9090,
      "protocol": "TCP",
      "verdict": "FORWARDED",
      "deniedBy": [],
      "count": 120,
      "last": "2026-03-11T09:38:00Z"
    },
    {
      "from": "shop/checkout-api-7d9f8b6c5d-akxqb",
      "to": "payments/gateway-7d9f8b6c5d-akxqb",
      "port": 8443,
      "protocol": "TCP",
      "verdict": "FORWARDED",
      "deniedBy": [],
      "count": 96,
      "last": "2026-03-11T09:38:00Z"
    },
    {
      "from": "shop/storefront-7d9f8b6c5d-akxqb",
      "to": "payments/gateway-7d9f8b6c5d-akxqb",
      "port": 8443,
      "protocol": "TCP",
      "verdict": "DROPPED",
      "reason": "POLICY_DENIED",
      "deniedBy": [
        "payments/default-deny"
      ],
      "count": 7,
      "last": "2026-03-11T09:38:00Z"
    }
  ],
  "edges": [
    {
      "from": "ns:ingress-nginx",
      "to": "wl:shop/storefront",
      "forwarded": 412,
      "dropped": 0,
      "ports": [
        "8080/TCP"
      ],
      "deniedBy": []
    },
    {
      "from": "ns:monitoring",
      "to": "wl:shop/checkout-api",
      "forwarded": 120,
      "dropped": 0,
      "ports": [
        "9090/TCP"
      ],
      "deniedBy": []
    },
    {
      "from": "wl:shop/checkout-api",
      "to": "ns:payments",
      "forwarded": 96,
      "dropped": 0,
      "ports": [
        "8443/TCP"
      ],
      "deniedBy": []
    },
    {
      "from": "wl:shop/checkout-api",
      "to": "wl:shop/cart-redis",
      "forwarded": 640,
      "dropped": 0,
      "ports": [
        "6379/TCP"
      ],
      "deniedBy": []
    },
    {
      "from": "wl:shop/storefront",
      "to": "ns:payments",
      "forwarded": 0,
      "dropped": 7,
      "ports": [
        "8443/TCP"
      ],
      "deniedBy": [
        "payments/default-deny"
      ]
    },
    {
      "from": "wl:shop/storefront",
      "to": "wl:shop/checkout-api",
      "forwarded": 233,
      "dropped": 0,
      "ports": [
        "8080/TCP"
      ],
      "deniedBy": []
    }
  ]
};

/** A Service as the destination: each pod behind it, and the policy to copy. */
const SERVICE: Verdict = {
  "answer": "no",
  "cni": {
    "detail": "No known network plugin was recognized from the DaemonSets: whether these rules are applied cannot be said.",
    "enforces": "unknown",
    "name": ""
  },
  "complete": true,
  "egress": {
    "caveats": [],
    "direction": "egress",
    "isolating": [
      {
        "name": "storefront-egress-dns",
        "namespace": "shop"
      }
    ],
    "pod": "shop/storefront-7d9f8b6c5d-akxqb",
    "reason": "Isolated for egress by storefront-egress-dns, and no rule of theirs allows this peer on this port.",
    "state": "denied"
  },
  "from": "shop/storefront-7d9f8b6c5d-akxqb",
  "ingress": {
    "caveats": [],
    "direction": "ingress",
    "isolating": [
      {
        "name": "default-deny-ingress",
        "namespace": "payments"
      }
    ],
    "pod": "payments/gateway-7d9f8b6c5d-akxqb",
    "reason": "Isolated for ingress by default-deny-ingress, and no rule of theirs allows this peer on this port.",
    "state": "denied"
  },
  "mode": "rules",
  "notes": [],
  "policy": "apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n  name: allow-ingress-shop-storefront-3994a1\n  namespace: payments\nspec:\n  ingress:\n  - from:\n    - namespaceSelector:\n        matchLabels:\n          kubernetes.io/metadata.name: shop\n      podSelector:\n        matchLabels:\n          app: storefront\n    ports:\n    - port: 8443\n      protocol: TCP\n  podSelector:\n    matchLabels:\n      app: gateway\n  policyTypes:\n  - Ingress\n---\napiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n  name: allow-egress-payments-gateway-418d34\n  namespace: shop\nspec:\n  egress:\n  - ports:\n    - port: 8443\n      protocol: TCP\n    to:\n    - namespaceSelector:\n        matchLabels:\n          kubernetes.io/metadata.name: payments\n      podSelector:\n        matchLabels:\n          app: gateway\n  podSelector:\n    matchLabels:\n      app: storefront\n  policyTypes:\n  - Egress\n",
  "port": 443,
  "protocol": "TCP",
  "service": {
    "backends": [
      {
        "answer": "no",
        "egress": {
          "caveats": [],
          "direction": "egress",
          "isolating": [
            {
              "name": "storefront-egress-dns",
              "namespace": "shop"
            }
          ],
          "pod": "shop/storefront-7d9f8b6c5d-akxqb",
          "reason": "Isolated for egress by storefront-egress-dns, and no rule of theirs allows this peer on this port.",
          "state": "denied"
        },
        "ingress": {
          "caveats": [],
          "direction": "ingress",
          "isolating": [
            {
              "name": "default-deny-ingress",
              "namespace": "payments"
            }
          ],
          "pod": "payments/gateway-7d9f8b6c5d-akxqb",
          "reason": "Isolated for ingress by default-deny-ingress, and no rule of theirs allows this peer on this port.",
          "state": "denied"
        },
        "pod": "payments/gateway-7d9f8b6c5d-akxqb",
        "port": 8443
      },
      {
        "answer": "no",
        "egress": {
          "caveats": [],
          "direction": "egress",
          "isolating": [
            {
              "name": "storefront-egress-dns",
              "namespace": "shop"
            }
          ],
          "pod": "shop/storefront-7d9f8b6c5d-akxqb",
          "reason": "Isolated for egress by storefront-egress-dns, and no rule of theirs allows this peer on this port.",
          "state": "denied"
        },
        "ingress": {
          "caveats": [],
          "direction": "ingress",
          "isolating": [
            {
              "name": "default-deny-ingress",
              "namespace": "payments"
            }
          ],
          "pod": "payments/gateway-7d9f8b6c5d-m2xpl",
          "reason": "Isolated for ingress by default-deny-ingress, and no rule of theirs allows this peer on this port.",
          "state": "denied"
        },
        "pod": "payments/gateway-7d9f8b6c5d-m2xpl",
        "port": 8443
      }
    ],
    "capped": false,
    "name": "payments/gateway",
    "port": 443,
    "portName": "https",
    "protocol": "TCP",
    "selector": "app=gateway",
    "targetPort": "8443",
    "total": 2
  },
  "summary": "The rules refuse it: the source may not send it, and the destination may not receive it.",
  "to": "payments/gateway",
  "unevaluated": []
};

// payments' flows, as the enlarged view of ledger-db-0 reads them over each
// period: the same pairs, more of them the further back the read goes.
const PAYMENTS_ROWS: ReadonlyArray<[string, string, number, string, number, string[]]> = [
  ['payments/ledger-7d9f8b6c5d-akxqb', 'payments/ledger-db-0', 5432, 'FORWARDED', 212, []],
  ['payments/ledger-7d9f8b6c5d-blxrb', 'payments/ledger-db-0', 5432, 'FORWARDED', 187, []],
  ['monitoring/prometheus-0', 'payments/ledger-db-0', 9187, 'FORWARDED', 20, []],
  ['shop/checkout-api-7d9f8b6c5d-akxqb', 'payments/ledger-db-0', 5432, 'DROPPED', 6, ['payments/ledger-db-deny']],
  ['batch/nightly-report-7d9f8b6c5d-akxqb', 'payments/ledger-db-0', 5432, 'DROPPED', 2, ['payments/default-deny']],
  ['payments/ledger-db-0', 'kube-system/coredns-7d9f8b6c5d-akxqb', 53, 'FORWARDED', 44, []],
  ['payments/ledger-db-0', 'payments/ledger-db-1', 5432, 'FORWARDED', 96, []],
  ['payments/gateway-7d9f8b6c5d-akxqb', 'payments/ledger-7d9f8b6c5d-akxqb', 8080, 'FORWARDED', 310, []],
];

function paymentsFlows(window: number): Flows {
  const scale = window / 300;
  const rows = PAYMENTS_ROWS.map(([from, to, port, verdict, count, deniedBy]) => ({
    from,
    to,
    port,
    protocol: port === 53 ? 'UDP' : 'TCP',
    verdict,
    ...(verdict === 'DROPPED' ? { reason: 'POLICY_DENIED' } : {}),
    deniedBy,
    count: Math.max(1, Math.round(count * scale)),
    last: '2026-03-11T09:39:40Z',
  }));
  const forwarded = rows.filter((r) => r.verdict === 'FORWARDED').reduce((n, r) => n + r.count, 0);
  const dropped = rows.filter((r) => r.verdict === 'DROPPED').reduce((n, r) => n + r.count, 0);
  return { ...FLOWS, window, read: forwarded + dropped, forwarded, dropped, rows, edges: [] };
}

export const NETINSPECT_ANSWERS: Readonly<Record<string, unknown>> = {
  '/x/production/netinspect/flows?ns=payments': paymentsFlows(300),
  '/x/production/netinspect/flows?ns=payments&window=60': paymentsFlows(60),
  '/x/production/netinspect/flows?ns=payments&window=900': paymentsFlows(900),
  '/x/production/netinspect/flows?ns=payments&window=3600': paymentsFlows(3600),
  '/x/production/netinspect/': CLUSTER,
  '/x/production/netinspect/?ns=shop': SHOP,
  "/x/production/netinspect/verdict?from=shop%2Fstorefront-7d9f8b6c5d-akxqb&to=payments%2Fgateway-7d9f8b6c5d-akxqb&port=8443": DENIED,
  "/x/production/netinspect/verdict?from=shop%2Fcheckout-api-7d9f8b6c5d-akxqb&to=shop%2Fcart-redis-0&port=6379": ALLOWED,
  "/x/production/netinspect/verdict?from=shop%2Fstorefront-7d9f8b6c5d-akxqb&to=svc%3Apayments%2Fgateway&port=443": SERVICE,
  "/x/production/netinspect/pods/payments/ledger-db-0": POD,
  '/x/production/netinspect/flows': FLOWS,
  '/x/production/netinspect/flows?ns=shop': SHOP_FLOWS,
};

/** Where each scene opens, under `/c/production/`. */
export const NETINSPECT_AT = {
  cluster: "x/netinspect?from=shop/storefront-7d9f8b6c5d-akxqb&to=payments/gateway-7d9f8b6c5d-akxqb&port=8443",
  namespace: "x/netinspect?ns=shop&node=wl:shop/checkout-api&from=shop/checkout-api-7d9f8b6c5d-akxqb&to=shop/cart-redis-0&port=6379",
  pod: "x/netinspect/pods/payments/ledger-db-0",
  service: "x/netinspect?from=shop/storefront-7d9f8b6c5d-akxqb&to=svc:payments/gateway&port=443",
};

// A flow decided by a Calico GlobalNetworkPolicy in its tier, and one decided by an
// AdminNetworkPolicy: the same pods as the other scenes, answered by the new engines.
const CALICO_FROM = 'shop/storefront-7d9f8b6c5d-akxqb';
const CALICO_TO = 'payments/ledger-db-0';
const CALICO_DENIED: Verdict = {
  ...DENIED,
  summary: 'The rules refuse it: the destination may not receive it.',
  from: CALICO_FROM,
  to: CALICO_TO,
  port: 5432,
  ingress: {
    direction: 'ingress',
    pod: CALICO_TO,
    state: 'denied',
    isolating: [{ namespace: '', name: 'security.block-shop-to-ledger', policyKind: 'Calico GlobalNetworkPolicy' }],
    decided: {
      namespace: '',
      name: 'security.block-shop-to-ledger',
      policyKind: 'Calico GlobalNetworkPolicy',
      kind: 'deny',
      rule: 1,
      text: 'deny endpoints projectcalico.org/namespace == "shop", on 5432/TCP',
    },
    reason: 'Denied by Calico GlobalNetworkPolicy security.block-shop-to-ledger, ingress rule 1 (tier security).',
    caveats: [],
  },
  policy: '',
};
const ANP_FROM = 'shop/checkout-api-7d9f8b6c5d-akxqb';
const ANP_TO = 'payments/gateway-7d9f8b6c5d-akxqb';
const ANP_ALLOWED: Verdict = {
  ...ALLOWED,
  from: ANP_FROM,
  to: ANP_TO,
  port: 8443,
  ingress: {
    direction: 'ingress',
    pod: ANP_TO,
    state: 'allowed',
    isolating: [],
    decided: { namespace: '', name: 'platform-allow-checkout', policyKind: 'AdminNetworkPolicy', kind: 'allow', rule: 1, text: 'pods app=checkout-api in namespace shop, on 8443/TCP' },
    reason: 'Allowed by AdminNetworkPolicy platform-allow-checkout, ingress rule 1: it is decided before any NetworkPolicy.',
    caveats: [],
  },
};
const calicoUrl = (from: string, to: string, port: number): string =>
  `/x/production/netinspect/verdict?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}&port=${String(port)}`;

export const NETINSPECT_NEW_ANSWERS: Readonly<Record<string, unknown>> = {
  ...NETINSPECT_ANSWERS,
  [calicoUrl(CALICO_FROM, CALICO_TO, 5432)]: CALICO_DENIED,
  [calicoUrl(ANP_FROM, ANP_TO, 8443)]: ANP_ALLOWED,
};

export const NETINSPECT_NEW_AT = {
  calico: `x/netinspect?from=${CALICO_FROM}&to=${CALICO_TO}&port=5432`,
  admin: `x/netinspect?from=${ANP_FROM}&to=${ANP_TO}&port=8443`,
};

// --- a cluster of 120 namespaces ---------------------------------------------------
//
// The same cluster with 112 tenant namespaces added: each lets the ingress
// controller and monitoring in, a few talk to one another, a few are denied, and
// Hubble saw traffic of very different volumes. Invented names, generated, so
// the matrix's folding (`fold.ts`) is drawn on something the size of a shared
// cluster. `cap` folds the graph as the server does beyond its bound.

const TENANTS = Array.from({ length: 112 }, (_, i) => `${['tenant', 'team', 'app', 'svc'][i % 4] ?? 'tenant'}-${String(i + 1).padStart(3, '0')}`);

/** A small deterministic spread, so the picture is the same from one run to the next. */
const spread = (i: number, m: number): number => (i * 7919 + 13) % m;

function manyGraph(cap: number): Overview['graph'] {
  const nodes: Overview['graph']['nodes'] = [
    ...CLUSTER.graph.nodes,
    ...TENANTS.map((name, i) => {
      const pods = 1 + spread(i, 6);
      const isolated = i % 5 !== 0;
      return { id: `ns:${name}`, label: name, kind: 'namespace' as const, namespace: name, pods, isolatedIn: isolated ? pods : 0, isolatedOut: i % 3 === 0 ? pods : 0, internal: i % 4 === 0 ? 1 : 0,
        // The engine's answer for its own pods, as the server sends it: an
        // ingress rule between them does not let them out where egress is isolated.
        ...(i % 4 === 0 ? { diagonal: i % 3 === 0 ? ('refusedOut' as const) : ('inside' as const) } : {}) };
    }),
  ];
  const edges: Overview['graph']['edges'] = [...CLUSTER.graph.edges];
  TENANTS.forEach((name, i) => {
    if (i % 5 === 0) return;
    edges.push({ kind: 'allow', from: 'ns:ingress-nginx', to: `ns:${name}`, side: 'ingress', ports: ['8080/TCP'], policies: ['allow-ingress'], rules: 1 });
    if (i % 2 === 0) edges.push({ kind: 'allow', from: 'ns:monitoring', to: `ns:${name}`, side: 'ingress', ports: ['9090/TCP'], policies: ['allow-scrape'], rules: 1 });
    if (i % 7 === 0) edges.push({ kind: 'allow', from: `ns:${TENANTS[(i + 3) % TENANTS.length] ?? name}`, to: `ns:${name}`, side: 'ingress', ports: ['443/TCP'], policies: ['allow-peer'], rules: 2 });
    if (i % 11 === 0) edges.push({ kind: 'deny', from: 'ns:batch', to: `ns:${name}`, side: 'ingress', ports: ['every port'], policies: ['deny-batch'], rules: 1 });
  });
  if (nodes.length <= cap) return { scope: 'cluster', nodes, edges, folded: 0, edgesTotal: edges.length };
  // As `view.go` `finish`: the most connected kept, the rest one "others".
  const degree = new Map<string, number>();
  for (const e of edges) {
    degree.set(e.from, (degree.get(e.from) ?? 0) + e.rules);
    degree.set(e.to, (degree.get(e.to) ?? 0) + e.rules);
  }
  const sorted = [...nodes].sort((a, b) => (degree.get(b.id) ?? 0) - (degree.get(a.id) ?? 0) || b.pods - a.pods || a.id.localeCompare(b.id));
  const kept = sorted.slice(0, cap - 1);
  const ids = new Set(kept.map((n) => n.id));
  const rest = sorted.slice(cap - 1);
  const others = { id: 'others', label: `${String(rest.length)} others`, kind: 'others' as const, pods: rest.reduce((s, n) => s + n.pods, 0), isolatedIn: 0, isolatedOut: 0, internal: 0 };
  const home = (id: string) => (ids.has(id) ? id : 'others');
  const merged = new Map<string, Overview['graph']['edges'][number]>();
  for (const e of edges) {
    const from = home(e.from);
    const to = home(e.to);
    if (from === to) continue;
    const key = `${e.kind}>${from}>${to}`;
    const m = merged.get(key);
    if (m === undefined) merged.set(key, { ...e, from, to });
    else m.rules += e.rules;
  }
  return { scope: 'cluster', nodes: [...kept, others], edges: [...merged.values()], folded: rest.length, edgesTotal: edges.length };
}

function manyCluster(cap: number): Overview {
  const rows = TENANTS.map((name, i) => {
    const pods = 1 + spread(i, 6);
    return { name, pods, isolatedIn: i % 5 === 0 ? 0 : pods, isolatedOut: i % 3 === 0 ? pods : 0, policies: i % 5 === 0 ? 0 : 2, defaultDenyIn: false, defaultDenyOut: false };
  });
  const pods = CLUSTER.summary.pods + rows.reduce((s, r) => s + r.pods, 0);
  return {
    ...CLUSTER,
    summary: { ...CLUSTER.summary, pods, policies: CLUSTER.summary.policies + rows.reduce((s, r) => s + r.policies, 0) },
    namespaces: [...CLUSTER.namespaces, ...rows],
    namespacesTotal: CLUSTER.namespaces.length + rows.length,
    choices: [...(CLUSTER.choices ?? CLUSTER.namespaces.map((r) => r.name)), ...TENANTS].sort(),
    graph: manyGraph(cap),
  };
}

const MANY_FLOWS: Flows = {
  ...FLOWS,
  edges: [
    ...FLOWS.edges,
    ...TENANTS.flatMap((name, i) => {
      if (i % 3 !== 0) return [];
      const volume = 2 + spread(i, 400);
      return [
        { from: 'ns:ingress-nginx', to: `ns:${name}`, forwarded: volume, dropped: 0, ports: ['8080/TCP'], deniedBy: [] },
        ...(i % 11 === 0 ? [{ from: 'ns:batch', to: `ns:${name}`, forwarded: 0, dropped: 1 + spread(i, 9), ports: ['8080/TCP'], deniedBy: ['deny-batch'] }] : []),
        ...(i % 9 === 0 ? [{ from: `ns:${name}`, to: 'entity:world', forwarded: spread(i, 60), dropped: 0, ports: ['443/TCP'], deniedBy: [] }] : []),
      ];
    }),
  ],
};

/** The 120-namespace cluster as this version's server sends it (bounded at 300). */
export const NETINSPECT_MANY_ANSWERS: Readonly<Record<string, unknown>> = {
  ...NETINSPECT_ANSWERS,
  '/x/production/netinspect/': manyCluster(300),
  '/x/production/netinspect/flows': MANY_FLOWS,
};

/** The same cluster as the server bounded at 40 sent it before. */
export const NETINSPECT_MANY_FOLDED_ANSWERS: Readonly<Record<string, unknown>> = {
  ...NETINSPECT_MANY_ANSWERS,
  '/x/production/netinspect/': manyCluster(40),
};
