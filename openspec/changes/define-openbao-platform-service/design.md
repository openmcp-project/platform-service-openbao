## Context

OpenMCP distinguishes several cluster roles:

- **Onboarding cluster**: user-facing API surface for `Project`, `Workspace`, `ControlPlane`, and provider/platform-service request resources.
- **Platform cluster**: runs `openmcp-operator`, ServiceProviders, PlatformServices, ClusterProviders, and provider-owned support resources.
- **ControlPlane / MCP cluster**: tenant-isolated Kubernetes API where users run components such as ESO and create domain resources.
- **Workload cluster**: optional shared runtime cluster, not in scope for the initial OpenBao trust model.

`platform-service-openbao` is OpenBao-specific. It should not hide the backend behind generic Vault or secret-trust names. Its job is to configure **trust**, not to transport or persist credentials.

The current issue text for internal-backlog #555 is outdated because it describes an `Entity` + `Token` model with generated scoped tokens and propagation to ControlPlanes. The accepted direction is OIDC/JWT trust-only:

- no stored OpenBao tokens;
- no platform-managed ESO;
- no platform-managed ControlPlane ServiceAccounts;
- no platform-created user policies;
- one OpenBao JWT role per `PolicyBinding`.

## Goals / Non-Goals

**Goals:**

- Provide an OpenBao-only PlatformService API for OpenMCP tenants.
- Let platform operators register approved OpenBao instances.
- Let project/workspace users describe project identity, ControlPlane trust, ControlPlane ServiceAccount identity, and policy bindings.
- Configure OpenBao JWT auth mount/trust and JWT roles so existing ControlPlane ServiceAccounts can authenticate to OpenBao.
- Keep OpenBao policy names free-form while surfacing policy existence and readiness in status.
- Expose enough status for users to configure ESO `SecretStore`/`ClusterSecretStore` or other OpenBao JWT-auth clients themselves.
- Keep cleanup ownership explicit for OpenBao objects created by the service.

**Non-Goals:**

- Supporting HashiCorp Vault or arbitrary Vault-compatible backends.
- Creating or managing actual OpenBao policies.
- Managing OpenBao secret data or secret paths.
- Installing or configuring External Secrets Operator.
- Creating or managing ServiceAccounts inside tenant ControlPlanes.
- Minting, storing, rotating, or propagating OpenBao tokens.
- Replacing user-created ESO `SecretStore`, `ClusterSecretStore`, or `ExternalSecret` resources.

## API Overview

The proposed API surface is:

```text
ProviderConfig
OpenBaoInstance
ProjectEntity
ControlPlaneTrust
ControlPlaneEntity
PolicyBinding
```

A compact ownership graph:

```text
cluster-scoped / platform-owned
┌────────────────────┐
│ ProviderConfig      │
└────────────────────┘

┌────────────────────┐
│ OpenBaoInstance     │
│ - address           │
│ - CA / TLS config   │
│ - auth defaults     │
└─────────┬──────────┘
          │
          ▼

project namespace
┌────────────────────┐
│ ProjectEntity       │
│ - openBaoRef        │
│ - entity identity   │
└─────────┬──────────┘
          │
          ▼

workspace namespace / ControlPlane level
┌────────────────────┐
│ ControlPlaneTrust   │
│ - projectEntityRef  │
│ - controlPlaneRef   │
│                    │
│ owns/configures:    │
│ - JWT auth mount    │
│ - issuer/JWKS trust │
└─────────┬──────────┘
          │
          ▼

workspace namespace / ControlPlane level
┌────────────────────┐
│ ControlPlaneEntity  │
│ - controlPlaneRef   │
│ - serviceAccountRef │ existing SA in target ControlPlane
└─────────┬──────────┘
          │
          ▼

workspace namespace / ControlPlane level
┌────────────────────┐
│ PolicyBinding       │
│ - cpEntityRef       │
│ - policyName        │ free-form, user-managed policy
│                    │
│ owns/configures:    │
│ - one OpenBao role  │
└────────────────────┘
```

## Decisions

### 1. Use OpenBao-specific naming throughout

The project remains `platform-service-openbao`. The backend resource is `OpenBaoInstance`, not `VaultInstance`, `SecretTrustBackend`, or `VaultBackend`.

This avoids fake abstraction. The service is designed around OpenBao JWT auth, OpenBao entities/aliases/groups/roles, and OpenBao policies. If support for another backend is ever required, it should be introduced deliberately rather than hidden behind today's API.

### 2. Separate trust from runtime consumption

The platform service only configures OpenBao trust. Runtime consumption is performed by existing tenant-managed components:

- ESO, if installed;
- user-created ESO `SecretStore` or `ClusterSecretStore` resources;
- tenant workloads or other clients using the referenced ServiceAccount JWT.

The service exposes status values that make those user-managed resources configurable, but it does not create them.

Runtime flow:

```text
Existing ServiceAccount JWT
        │
        ▼
OpenBao JWT auth mount
        │
        ▼
role owned by PolicyBinding
        │
        ▼
short-lived OpenBao token with exactly policyName
```

### 3. Register approved OpenBao backends as cluster-scoped `OpenBaoInstance`s

`OpenBaoInstance` is cluster-scoped and platform-owned. Tenant resources reference it by name instead of submitting arbitrary backend URLs.

It should carry connection and trust configuration such as:

- OpenBao API address;
- TLS/CA bundle reference or inline bundle policy;
- optional namespace/capability metadata, if needed;
- default JWT auth mount path prefix or naming policy;
- status for reachability and version/capability discovery.

The resource must not expose platform admin credentials in tenant namespaces.

### 4. Use `ProjectEntity` as the project-level identity anchor

`ProjectEntity` lives in a project namespace on the onboarding cluster. It references an `OpenBaoInstance` and represents the project-level OpenBao identity anchor.

The controller may create or reconcile OpenBao identity objects/aliases/groups needed for the project-level trust model. Status should expose stable identifiers such as canonical entity/group ids when useful for manual OpenBao-side bootstrap or auditing.

`ProjectEntity` is not a policy and does not grant access by itself.

### 5. Use `ControlPlaneTrust` for OpenBao JWT auth mount/trust

`ControlPlaneTrust` lives at ControlPlane/workspace level in the workspace namespace. It references:

- a `ProjectEntity`;
- a `ControlPlane` in the same workspace namespace, or a clearly scoped ControlPlane reference.

It owns the OpenBao JWT auth mount/trust setup for that ControlPlane. This includes the OpenBao configuration needed to validate short-lived ServiceAccount JWTs from the tenant ControlPlane, such as issuer/JWKS/OIDC discovery, audience defaults, auth mount path, and related trust metadata.

Status should expose:

- auth mount path;
- issuer/JWKS/discovery readiness;
- observed ControlPlane identity data;
- conditions for OpenBao reachability, trust configured, and cleanup state.

### 6. Use `ControlPlaneEntity` for existing ServiceAccounts

`ControlPlaneEntity` lives at ControlPlane/workspace level in the workspace namespace. It references:

- a `ControlPlane`;
- an existing ServiceAccount inside the target ControlPlane by namespace/name.

The platform service does not create or manage the ServiceAccount. It may use the reference to mint a short-lived ServiceAccount token on the onboarding/control-plane side for setup, proof, or trust verification, but no OpenBao token is stored.

Status should expose a stable identity summary suitable for audit and troubleshooting, for example resolved subject/issuer/audience claims or a deterministic identity id. It should not expose JWT contents.

### 7. Use one OpenBao JWT role per `PolicyBinding`

Each `PolicyBinding` lives at ControlPlane/workspace level and references one `ControlPlaneEntity`. It contains one free-form `policyName` that names a user-created OpenBao policy.

Each `PolicyBinding` owns exactly one OpenBao JWT role. The role is bound to the referenced ControlPlane ServiceAccount identity and grants exactly the configured policy name.

This keeps least privilege local to each binding:

```text
same ServiceAccount
   ├─ PolicyBinding read-dev    → OpenBao role role-read-dev    → policy dev-read
   ├─ PolicyBinding read-prod   → OpenBao role role-read-prod   → policy prod-read
   └─ PolicyBinding rotate-cert → OpenBao role role-rotate-cert → policy cert-rotate
```

Status should expose:

- generated OpenBao role name;
- auth mount path inherited/resolved through `ControlPlaneTrust`;
- referenced policy name;
- policy existence (`True`, `False`, or `Unknown` if the check cannot be performed);
- readiness conditions and last reconcile error.

### 8. Treat policy names as free-form and report missing policies through status

Users create the actual OpenBao policies manually outside this service. `PolicyBinding.spec.policyName` is therefore intentionally free-form.

The controller should check whether the policy exists when permissions and backend behavior allow it. Missing policy should not be an admission rejection. Instead, the controller reports conditions such as:

```yaml
conditions:
  - type: PolicyResolved
    status: "False"
    reason: PolicyNotFound
    message: OpenBao policy "my-existing-policy" does not exist yet.
  - type: Ready
    status: "False"
    reason: WaitingForPolicy
```

If policy existence cannot be checked, the service should surface `Unknown` rather than pretending the policy exists.

### 9. Keep status user-actionable

Because ESO and `SecretStore` are user-managed, status is part of the API contract. Users need enough output to configure OpenBao JWT auth in ESO or another client.

Likely status fields include:

- `ControlPlaneTrust.status.authMountPath`;
- `PolicyBinding.status.roleName`;
- `PolicyBinding.status.openBaoRef` or resolved backend reference;
- readiness and policy existence conditions.

The design should avoid requiring users to infer generated OpenBao names from controller internals.

### 10. Investigate OpenMCP PlatformService discoverability separately

Current public `ServiceProviderStatus` includes `status.resources[]`, but current `PlatformServiceStatus` appears to only embed deployment status. If `platform-service-openbao` is deployed as a `PlatformService` and exposes onboarding CRDs, OpenMCP may need a way for PlatformServices to advertise exposed resources.

This is an integration concern, not a domain-model blocker. It should be investigated before implementation.

## Example Resource Sketches

These are illustrative and not final API schemas.

```yaml
apiVersion: openbao.openmcp.cloud/v1alpha1
kind: OpenBaoInstance
metadata:
  name: default
spec:
  address: https://openbao.example.com
  caBundleRef:
    name: openbao-ca
```

```yaml
apiVersion: openbao.openmcp.cloud/v1alpha1
kind: ProjectEntity
metadata:
  name: team-a
  namespace: project-team-a
spec:
  openBaoRef:
    name: default
```

```yaml
apiVersion: openbao.openmcp.cloud/v1alpha1
kind: ControlPlaneTrust
metadata:
  name: prod
  namespace: project-team-a--ws-prod
spec:
  projectEntityRef:
    name: team-a
    namespace: project-team-a
  controlPlaneRef:
    name: prod
```

```yaml
apiVersion: openbao.openmcp.cloud/v1alpha1
kind: ControlPlaneEntity
metadata:
  name: eso-reader
  namespace: project-team-a--ws-prod
spec:
  controlPlaneRef:
    name: prod
  serviceAccountRef:
    name: external-secrets
    namespace: external-secrets
```

```yaml
apiVersion: openbao.openmcp.cloud/v1alpha1
kind: PolicyBinding
metadata:
  name: eso-reader-kv-prod
  namespace: project-team-a--ws-prod
spec:
  controlPlaneEntityRef:
    name: eso-reader
  policyName: kv-prod-read
```

A user-managed ESO `SecretStore` would then reference the `ControlPlaneTrust.status.authMountPath` and `PolicyBinding.status.roleName`, but the platform service does not create it.

## Risks / Trade-offs

- **Free-form policy names may hide typos** → Check policy existence when possible and report `PolicyResolved=False`, but do not reject admission.
- **One role per `PolicyBinding` increases OpenBao object count** → In exchange, each binding has clear least-privilege semantics, local cleanup, and explicit role output.
- **Status becomes part of the UX** → Keep generated names stable and documented; avoid forcing users to reconstruct role names.
- **OpenBao auth mount naming can collide or exceed limits** → Use deterministic, sanitized, hash-suffixed names derived from stable OpenMCP identity, and expose the result in status.
- **ServiceAccount lifecycle is external** → Detect missing/unreachable ServiceAccounts through conditions without trying to create or repair them.
- **OpenBao policies are external** → Never delete or mutate user policies; delete only roles/trust objects owned by this service.
- **PlatformService resource discovery may be incomplete** → Investigate whether `PlatformService.status.resources[]` or another marketplace/discovery mechanism is required.

## Migration Plan

No migration from an existing implementation is required. For the initial implementation:

1. Add API types and generated CRDs for the six resources.
2. Implement platform/operator configuration loading and OpenBao client setup.
3. Implement reconcilers in dependency order: `OpenBaoInstance`, `ProjectEntity`, `ControlPlaneTrust`, `ControlPlaneEntity`, `PolicyBinding`.
4. Add cleanup/finalizer behavior for OpenBao objects owned by the service.
5. Add status/condition reporting and examples for user-managed ESO `SecretStore` configuration.
6. Validate against a real or local OpenBao instance and an OpenMCP test landscape.

## Open Questions

- Exact API group and version naming, for example `openbao.openmcp.cloud/v1alpha1` versus an OpenControlPlane-specific group.
- Exact split between `ProviderConfig` and `OpenBaoInstance` fields.
- Exact OpenBao object naming strategy for auth mount paths, roles, entities, aliases, and groups.
- Whether `ProjectEntity` owns OpenBao entity/group objects directly or mostly acts as a stable OpenMCP-side anchor.
- Which status fields are required for the intended ESO UX.
- Whether OpenMCP `PlatformService` needs parity with `ServiceProvider.status.resources[]` for discoverability.
