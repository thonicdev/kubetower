import type { ExtensionAbout } from '@kubetower/ui';

import screenshot from './assets/preview.png';

/**
 * What Settings → Extra says about Security Center before it is turned on.
 * The permissions follow the Go half's reads (`extras/security`): keep the two
 * in step when a read is added.
 */
export const about: ExtensionAbout = {
  description:
    "Shows what trivy-operator found, cluster-wide: CVEs by severity, the images and namespaces that carry them, the workloads " +
    'reachable from outside, failed config checks and secrets left in images, the RBAC and compliance reports, and which Kyverno or Gatekeeper policies fail and on what. Tiles filter the workloads, worst first; each opens ' +
    'on its CVEs, checks and secrets. It reads the reports only and changes nothing.',
  reads: [
    "trivy-operator's VulnerabilityReports, ConfigAuditReports and ExposedSecretReports",
    "its cluster-level reports, where they are served: components, cluster-scoped objects, the control plane and the nodes",
    'its RBAC assessments and compliance reports, when their view is open',
    "Kyverno's and other engines' PolicyReports, Gatekeeper's constraints and Kyverno's policies, when the Policies view is open",
    'Pods, ReplicaSets and Jobs, to tie each report to its workload',
    'Services, Ingresses, Gateway API routes and host-network Pods, to tell which workloads are exposed',
    "its SBOM reports, as component counts; one workload's components only when its SBOM is opened",
    "the admission's image verification verdict on each Pod (Kyverno's kyverno.io/verify-images), never a registry",
  ],
  permissions: [
    'list vulnerabilityreports, configauditreports, exposedsecretreports, sbomreports (aquasecurity.github.io)',
    'list clustervulnerabilityreports, clusterconfigauditreports, infraassessmentreports, clusterinfraassessmentreports (aquasecurity.github.io)',
    'list rbacassessmentreports, clusterrbacassessmentreports, clustercompliancereports (aquasecurity.github.io)',
    'list policyreports, clusterpolicyreports (wgpolicyk8s.io), reports, clusterreports (openreports.io)',
    'list on every kind of constraints.gatekeeper.sh',
    'list clusterpolicies, policies (kyverno.io), validatingpolicies, imagevalidatingpolicies, namespacedvalidatingpolicies, namespacedimagevalidatingpolicies (policies.kyverno.io)',
    'list pods, replicasets, jobs, services',
    'list ingresses (networking.k8s.io)',
    'list httproutes, grpcroutes (gateway.networking.k8s.io), where served',
  ],
  requires: ['trivy-operator'],
  // Drawn by `pnpm shots:extras` from the measure harness's fake data.
  screenshot,
};
