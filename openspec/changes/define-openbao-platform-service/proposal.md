## Why

OpenMCP tenants can install External Secrets Operator (ESO) in their ControlPlanes, but they still need a safe way for workloads or ESO `SecretStore` resources to authenticate to OpenBao without copying long-lived tokens into Kubernetes or asking the platform to own tenant secret policies.

An earlier internal backlog issue captured an older token-oriented idea. The current direction is different: `platform-service-openbao` should configure OpenBao OIDC/JWT trust so existing ServiceAccounts in tenant ControlPlanes can authenticate to OpenBao and receive short-lived OpenBao tokens at runtime. The platform service should not create user policies, manage ESO, manage tenant ServiceAccounts, store OpenBao tokens, or propagate credentials.

## What Changes

- Define `platform-service-openbao` as an OpenMCP PlatformService-style deployable that exposes OpenBao trust CRDs and reconciles them across Onboarding, Platform, ControlPlane, and OpenBao boundaries.
- Add an OpenBao-specific public API surface with these CRDs:
  - `ProviderConfig`
  - `OpenBaoInstance`
  - `ProjectEntity`
  - `ControlPlaneTrust`
  - `ControlPlaneEntity`
  - `PolicyBinding`
- Model `OpenBaoInstance` as a cluster-scoped backend registration for approved OpenBao instances.
- Model `ProjectEntity` as the project-level OpenBao identity anchor.
- Model `ControlPlaneTrust` as the resource that configures OpenBao JWT auth mount/trust for a specific ControlPlane.
- Model `ControlPlaneEntity` as a workspace/ControlPlane-level reference to an existing ServiceAccount in the tenant ControlPlane.
- Model each `PolicyBinding` as one OpenBao JWT role binding one `ControlPlaneEntity` to one user-managed OpenBao policy name.
- Treat `PolicyBinding.spec.policyName` as intentionally free-form. The controller should check policy existence when possible and report missing policies through status/conditions, but admission must not reject unknown policy names.
- Expose status fields needed by users to configure ESO or workload clients, especially OpenBao auth mount paths, generated role names, policy existence, and readiness conditions.

## Capabilities

### New Capabilities

- `openbao-platform-service`: OpenBao OIDC/JWT trust management for OpenMCP project and ControlPlane identities.

### Modified Capabilities

None.

## Impact

- Adds OpenSpec design for a new OpenBao PlatformService and its CRD model.
- Future implementation will add Kubernetes API types, CRDs, controllers, OpenBao client integration, status/condition handling, RBAC, and tests.
- Future implementation may need OpenMCP operator/discoverability alignment if `PlatformService` lacks the `status.resources[]` capability currently available on `ServiceProvider`.
- No change to ESO ownership: ESO installation and `SecretStore`/`ExternalSecret` resources remain user/provider-owned outside this platform service.
- No persistent OpenBao token storage or token propagation is introduced.
