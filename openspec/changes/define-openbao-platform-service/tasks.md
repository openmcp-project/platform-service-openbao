## 1. API and CRD Shape

- [ ] 1.1 Choose and document the API group/version for OpenBao resources
- [ ] 1.2 Define `ProviderConfig` fields for platform-service runtime configuration
- [ ] 1.3 Define cluster-scoped `OpenBaoInstance` fields for approved OpenBao backend connection and trust defaults
- [ ] 1.4 Define project-namespace `ProjectEntity` fields and status for project-level OpenBao identity anchoring
- [ ] 1.5 Define workspace-namespace `ControlPlaneTrust` fields and status for ControlPlane JWT auth mount/trust
- [ ] 1.6 Define workspace-namespace `ControlPlaneEntity` fields and status for existing ControlPlane ServiceAccount identity references
- [ ] 1.7 Define workspace-namespace `PolicyBinding` fields and status for one OpenBao JWT role per binding
- [ ] 1.8 Define shared status condition types, phases, observed generation behavior, and reference conventions
- [ ] 1.9 Generate CRDs, deepcopy code, manifests, samples, and API documentation

## 2. OpenMCP Integration Model

- [ ] 2.1 Decide whether the deployable is implemented as a `PlatformService` and document the rationale
- [ ] 2.2 Investigate whether `PlatformService.status.resources[]` or another discovery path is required for onboarding CRDs
- [ ] 2.3 Define how CRDs are installed on the onboarding/platform clusters during `init`
- [ ] 2.4 Define RBAC for platform, onboarding, and target ControlPlane access
- [ ] 2.5 Define how the controller resolves `ControlPlane` references and obtains access to the target ControlPlane API
- [ ] 2.6 Define namespace rules for project-level and workspace/ControlPlane-level resources

## 3. OpenBao Client and Naming

- [ ] 3.1 Implement OpenBao client construction from `OpenBaoInstance` and platform credentials/configuration
- [ ] 3.2 Implement reachability, TLS, and capability checks for `OpenBaoInstance.status`
- [ ] 3.3 Define deterministic naming for auth mount paths, roles, entities, aliases, and groups
- [ ] 3.4 Ensure generated names are sanitized, length-bounded, collision-resistant, and exposed in status
- [ ] 3.5 Ensure controller-owned OpenBao objects carry identifiable metadata where OpenBao supports it
- [ ] 3.6 Define finalizer cleanup semantics for every OpenBao object the service owns

## 4. Reconcilers

- [ ] 4.1 Reconcile `OpenBaoInstance` status without exposing sensitive connection details
- [ ] 4.2 Reconcile `ProjectEntity` identity anchor and status identifiers
- [ ] 4.3 Reconcile `ControlPlaneTrust` by creating/configuring OpenBao JWT auth mount/trust for the referenced ControlPlane
- [ ] 4.4 Reconcile `ControlPlaneEntity` by resolving the existing ServiceAccount reference and deriving/verifying identity claims
- [ ] 4.5 Reconcile `PolicyBinding` by creating/updating exactly one OpenBao JWT role bound to the referenced `ControlPlaneEntity`
- [ ] 4.6 Attach only `PolicyBinding.spec.policyName` to the generated role and never create or mutate the policy itself
- [ ] 4.7 Check OpenBao policy existence when possible and report missing/unknown policies through conditions instead of admission rejection
- [ ] 4.8 Reconcile deletions in dependency-safe order and avoid deleting user-managed policies, ESO resources, ServiceAccounts, or secret data

## 5. Status, UX, and Documentation

- [ ] 5.1 Expose user-actionable status fields such as auth mount path, role name, policy existence, and readiness
- [ ] 5.2 Document the ownership boundary: no managed ESO, ServiceAccounts, policies, secret data, or stored OpenBao tokens
- [ ] 5.3 Add examples for platform admin setup with `ProviderConfig` and `OpenBaoInstance`
- [ ] 5.4 Add examples for project/workspace setup with `ProjectEntity`, `ControlPlaneTrust`, `ControlPlaneEntity`, and `PolicyBinding`
- [ ] 5.5 Add an example user-managed ESO `SecretStore` that consumes status output without being created by the service
- [ ] 5.6 Document troubleshooting conditions for missing policies, missing ServiceAccounts, unreachable OpenBao, and broken JWT trust

## 6. Verification

- [ ] 6.1 Add unit tests for API validation, defaulting, reference rules, and status condition helpers
- [ ] 6.2 Add unit tests for deterministic OpenBao object naming and cleanup ownership
- [ ] 6.3 Add mocked OpenBao client tests for auth mount, role reconciliation, policy existence checks, and failure conditions
- [ ] 6.4 Add reconciler tests for dependency ordering and status propagation across all six CRDs
- [ ] 6.5 Add integration/e2e validation against an OpenBao test instance proving ServiceAccount JWT login through a `PolicyBinding` role
- [ ] 6.6 Verify that no OpenBao tokens are persisted in Kubernetes resources or logs
- [ ] 6.7 Verify that ESO, ServiceAccounts, OpenBao policies, and secret data are not created, modified, or deleted by the service
- [ ] 6.8 Run OpenSpec validation once the `openspec` CLI is available in the environment
