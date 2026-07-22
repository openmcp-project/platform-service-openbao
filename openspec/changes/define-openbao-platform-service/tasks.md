## 1. API and CRD Shape

- [x] 1.1 Choose and document the API group/version for OpenBao resources — `openbao.open-control-plane.io/v1alpha1` (see `PROJECT` + `api/v1alpha1/groupversion_info.go`)
- [x] 1.2 Define `ProviderConfig` fields for platform-service runtime configuration — landed as `ServiceConfig` (matches platform-service-quota's `<Name>ServiceConfig` convention)
- [x] 1.3 Define cluster-scoped `OpenBaoInstance` fields for approved OpenBao backend connection and trust defaults
- [x] 1.4 Define project-namespace `ProjectEntity` fields and status for project-level OpenBao identity anchoring
- [x] 1.5 Define workspace-namespace `ControlPlaneTrust` fields and status for ControlPlane JWT auth mount/trust
- [x] 1.6 Define workspace-namespace `ControlPlaneEntity` fields and status for existing ControlPlane ServiceAccount identity references
- [x] 1.7 Define workspace-namespace `PolicyBinding` fields and status for one OpenBao JWT role per binding
- [x] 1.8 Define shared status condition types, phases, observed generation behavior, and reference conventions (`api/v1alpha1/shared.go`)
- [x] 1.9 Generate CRDs, deepcopy code, manifests, samples, and API documentation

## 2. OpenMCP Integration Model

- [x] 2.1 Decide whether the deployable is implemented as a `PlatformService` and document the rationale — yes, via the cobra `init`/`run` contract from platform-service-quota (`cmd/platform-service-openbao/app`)
- [ ] 2.2 Investigate whether `PlatformService.status.resources[]` or another discovery path is required for onboarding CRDs
- [x] 2.3 Define how CRDs are installed on the onboarding/platform clusters during `init` — `api/crds` embed + `controller-utils/pkg/crds.CRDManager`, split by `openmcp.cloud/cluster` label
- [x] 2.4 Define RBAC for platform, onboarding, and target ControlPlane access — kubebuilder RBAC markers on each reconciler + AccessRequest `token.permissions` set in `app/run.go`
- [ ] 2.5 Define how the controller resolves `ControlPlane` references and obtains access to the target ControlPlane API — AccessRequest wiring for target ControlPlanes is still pending; reconcilers surface this as `ReasonControlPlaneUnavailable`
- [x] 2.6 Define namespace rules for project-level and workspace/ControlPlane-level resources — encoded in CRD scoping + example manifests under `config/samples/`

## 3. OpenBao Client and Naming

- [x] 3.1 Implement OpenBao client construction from `OpenBaoInstance` and platform credentials/configuration (`internal/openbao/real.go`) — CA-bundle-from-Secret resolution is deferred
- [x] 3.2 Implement reachability, TLS, and capability checks for `OpenBaoInstance.status`
- [x] 3.3 Define deterministic naming for auth mount paths, roles, entities, aliases, and groups (`internal/openbao/naming.go`)
- [x] 3.4 Ensure generated names are sanitized, length-bounded, collision-resistant, and exposed in status
- [ ] 3.5 Ensure controller-owned OpenBao objects carry identifiable metadata where OpenBao supports it — follow-up: OpenBao supports a role `description` field but nothing structured; consider a small marker string encoding the owning namespace/name
- [x] 3.6 Define finalizer cleanup semantics for every OpenBao object the service owns — auth-mount cleanup on `ControlPlaneTrust` delete, role cleanup on `PolicyBinding` delete

## 4. Reconcilers

- [x] 4.1 Reconcile `OpenBaoInstance` status without exposing sensitive connection details
- [x] 4.2 Reconcile `ProjectEntity` identity anchor and status identifiers — OpenBao identity object materialisation is deferred (see design.md open question)
- [ ] 4.3 Reconcile `ControlPlaneTrust` by creating/configuring OpenBao JWT auth mount/trust for the referenced ControlPlane — auth-mount creation lands; issuer/JWKS discovery requires ControlPlane API access (see 2.5)
- [ ] 4.4 Reconcile `ControlPlaneEntity` by resolving the existing ServiceAccount reference and deriving/verifying identity claims — requires ControlPlane API access (see 2.5)
- [x] 4.5 Reconcile `PolicyBinding` by creating/updating exactly one OpenBao JWT role bound to the referenced `ControlPlaneEntity` — role creation + policy exists; `bound_subject`/`bound_claims` fill in when ControlPlaneEntity identity resolves
- [x] 4.6 Attach only `PolicyBinding.spec.policyName` to the generated role and never create or mutate the policy itself
- [x] 4.7 Check OpenBao policy existence when possible and report missing/unknown policies through conditions instead of admission rejection
- [x] 4.8 Reconcile deletions in dependency-safe order and avoid deleting user-managed policies, ESO resources, ServiceAccounts, or secret data

## 5. Status, UX, and Documentation

- [x] 5.1 Expose user-actionable status fields such as auth mount path, role name, policy existence, and readiness
- [ ] 5.2 Document the ownership boundary: no managed ESO, ServiceAccounts, policies, secret data, or stored OpenBao tokens — README/user docs still to write; boundary is encoded in code (no CreateSecret paths) but not in prose docs yet
- [x] 5.3 Add examples for platform admin setup with `ProviderConfig` and `OpenBaoInstance` — `config/samples/openbao_v1alpha1_{serviceconfig,openbaoinstance}.yaml`
- [x] 5.4 Add examples for project/workspace setup with `ProjectEntity`, `ControlPlaneTrust`, `ControlPlaneEntity`, and `PolicyBinding`
- [x] 5.5 Add an example user-managed ESO `SecretStore` that consumes status output without being created by the service — commented example inside `openbao_v1alpha1_policybinding.yaml`
- [ ] 5.6 Document troubleshooting conditions for missing policies, missing ServiceAccounts, unreachable OpenBao, and broken JWT trust — condition types/reasons implemented; user-facing runbook still to write

## 6. Verification

- [ ] 6.1 Add unit tests for API validation, defaulting, reference rules, and status condition helpers — `ServiceConfigSpec.Validate` covered indirectly; explicit condition-helper tests pending
- [x] 6.2 Add unit tests for deterministic OpenBao object naming and cleanup ownership — `internal/openbao/naming_test.go`
- [x] 6.3 Add mocked OpenBao client tests for auth mount, role reconciliation, policy existence checks, and failure conditions — via `internal/openbao/fake.go` + PolicyBinding envtest
- [ ] 6.4 Add reconciler tests for dependency ordering and status propagation across all six CRDs — PolicyBinding covered; other reconcilers pending broader envtest coverage
- [ ] 6.5 Add integration/e2e validation against an OpenBao test instance proving ServiceAccount JWT login through a `PolicyBinding` role — e2e suite scaffolded in `test/e2e/` (openmcp-testing + testcontainers-go, real Kind + real OpenBao/Vault container); backend smoke test green; Kind orchestration blocked on GHA/native-Linux (Kind-in-Docker-in-Lima env limitation, not a suite bug); JWT login assertion still gated on AccessRequest wiring (task 2.5)
- [x] 6.6 Verify that no OpenBao tokens are persisted in Kubernetes resources or logs — asserted by `secretExistsInNamespace` invariant test
- [x] 6.7 Verify that ESO, ServiceAccounts, OpenBao policies, and secret data are not created, modified, or deleted by the service — no such code paths exist; `Client` interface intentionally lacks policy-write methods
- [ ] 6.8 Run OpenSpec validation once the `openspec` CLI is available in the environment
