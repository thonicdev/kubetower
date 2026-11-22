import type { ExtensionAbout } from '@kubetower/ui';

import screenshot from './assets/preview.png';
import { ENGINES } from './data';

/**
 * What Settings → Extra says about the Network Inspector before it is turned
 * on. The permissions follow the Go half's reads (`extras/netinspect`,
 * `read.go`): keep the two in step when a read is added.
 */
export const about: ExtensionAbout = {
  badges: ENGINES,
  description:
    'Shows which policies select each pod - Kubernetes NetworkPolicies, Cilium and Calico policies with their tiers, and AdminNetworkPolicies - ' +
    'which peers and ports their rules allow, and whether one pod would reach ' +
    'another on a port - with the policy and rule that decide, on each side - computed from the policies. Where Hubble ' +
    'or Calico Goldmane runs, the flows seen are drawn beside the rules: forwarded or dropped, and by which policy. A pod gets a Network tab and a tile with its ' +
    'isolation each way, and the Pods list an Isolation column. Where Hubble UI or Whisker runs, a button opens it in a new tab through the console tunnel, or at the address set for it. Nothing is changed in the cluster from here.',
  reads: [
    'NetworkPolicies',
    'Pods and namespaces, to know whom each selector designates',
    'DaemonSets, to recognize the network plugin and whether it applies NetworkPolicies',
    'CiliumNetworkPolicies, CiliumClusterwideNetworkPolicies and CiliumCIDRGroups, evaluated beside the NetworkPolicies',
    'Calico NetworkPolicies, GlobalNetworkPolicies and Tiers, and AdminNetworkPolicies and the BaselineAdminNetworkPolicy, evaluated with the NetworkPolicies',
    "The flows Hubble Relay or Calico's Goldmane reports over the last five minutes, through the console's own tunnel, shown only for namespaces you may list pods in",
    'The Hubble UI and Whisker Services, to show their buttons',
    "Goldmane's CA, a ConfigMap, and its client certificate, a Secret, each read for you",
  ],
  permissions: [
    'list networkpolicies (networking.k8s.io)',
    'list pods, namespaces',
    'list daemonsets (apps)',
    'list ciliumnetworkpolicies, ciliumclusterwidenetworkpolicies, ciliumcidrgroups (cilium.io), where they exist',
    "list services in kube-system, to find Hubble Relay; the console's own ServiceAccount needs create pods/portforward there",
    "list services, configmaps in calico-system, and get secrets/whisker-backend-key-pair there, to read Goldmane; the console's own ServiceAccount needs create pods/portforward there",
    "list services in kube-system and calico-system, to find Hubble UI and Whisker; opening one through the tunnel needs create pods/portforward there (not offered where the console runs in a cluster: set an address in the settings instead)",
    'list networkpolicies, globalnetworkpolicies, tiers (crd.projectcalico.org), adminnetworkpolicies, baselineadminnetworkpolicies (policy.networking.k8s.io), where they exist',
  ],
  requires: [
    'Hubble Relay (Cilium), plaintext on its default port, or Goldmane (Calico Open Source 3.30 or later), for observed flows: without either, the rules only',
    'A network plugin that applies NetworkPolicies (Cilium, Calico, Antrea, kindnet…): with flannel alone the rules are declared and not applied'],
  // Drawn by `pnpm shots:extras` from the measure harness's fake data.
  screenshot,
};
