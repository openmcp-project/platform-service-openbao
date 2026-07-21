## ADDED Requirements

### Requirement: OpenBao-specific platform service API
The system SHALL provide an OpenBao-specific platform service API for managing OIDC/JWT trust between OpenMCP ControlPlane ServiceAccounts and OpenBao. The system SHALL use OpenBao-specific naming for the backend resource and SHALL NOT abstract the backend as Vault or a generic secret backend in the initial API.

#### Scenario: Platform service exposes OpenBao resources
- **WHEN** the OpenBao platform service is installed
- **THEN** it exposes CRDs for `ProviderConfig`, `OpenBaoInstance`, `ProjectEntity`, `ControlPlaneTrust`, `ControlPlaneEntity`, and `PolicyBinding`

#### Scenario: Backend is OpenBao-specific
- **WHEN** a platform operator configures a backend
- **THEN** the backend is represented as an `OpenBaoInstance` rather than `VaultInstance`, `SecretBackend`, or `SecretTrustBackend`

#### Scenario: Non-OpenBao backend is requested
- **WHEN** a tenant or operator attempts to configure a non-OpenBao backend through this API
- **THEN** the system does not treat it as a supported backend for the initial implementation

### Requirement: Cluster-scoped OpenBao backend registration
The system SHALL let platform operators register approved OpenBao backends as cluster-scoped `OpenBaoInstance` resources. Tenant-facing resources SHALL reference approved instances by name and SHALL NOT accept arbitrary OpenBao URLs as tenant input.

#### Scenario: OpenBao instance is registered
- **WHEN** a platform operator creates an `OpenBaoInstance` with connection and TLS configuration
- **THEN** the system records the instance as an approved backend and reports reachability/capability status without exposing sensitive credentials

#### Scenario: Tenant resource references a backend
- **WHEN** a `ProjectEntity` references an `OpenBaoInstance`
- **THEN** the system resolves the cluster-scoped backend registration instead of using tenant-provided backend connection details

#### Scenario: OpenBao instance is unreachable
- **WHEN** the controller cannot reach the referenced OpenBao backend
- **THEN** dependent resources report a not-ready condition and no tenant-scoped token or credential is stored

### Requirement: Project-level OpenBao identity anchor
The system SHALL let users create a project-namespace `ProjectEntity` that anchors a project identity to an approved `OpenBaoInstance`. `ProjectEntity` SHALL NOT itself grant access to OpenBao policies.

#### Scenario: Project entity is created
- **WHEN** a user creates a `ProjectEntity` in a project namespace referencing an `OpenBaoInstance`
- **THEN** the system reconciles or observes the project-level OpenBao identity anchor and reports stable identity identifiers in status when available

#### Scenario: Project entity is created outside project scope
- **WHEN** a user creates a `ProjectEntity` outside a valid project namespace
- **THEN** the system rejects or marks the resource invalid according to the namespace validation strategy

#### Scenario: Project entity has no policy binding
- **WHEN** a `ProjectEntity` is ready but no `PolicyBinding` exists
- **THEN** no OpenBao policy access is granted solely by the project entity

### Requirement: ControlPlane trust configuration
The system SHALL let users create a workspace-namespace `ControlPlaneTrust` for a referenced ControlPlane and project entity. `ControlPlaneTrust` SHALL create or configure the OpenBao JWT auth mount/trust needed to validate short-lived ServiceAccount JWTs from the referenced ControlPlane.

#### Scenario: ControlPlane trust is created
- **WHEN** a user creates a `ControlPlaneTrust` referencing a valid `ProjectEntity` and `ControlPlane`
- **THEN** the system configures OpenBao JWT auth trust for that ControlPlane and reports the auth mount path and readiness in status

#### Scenario: ControlPlane issuer data is unavailable
- **WHEN** the system cannot resolve issuer, JWKS, discovery, or equivalent trust data for the referenced ControlPlane
- **THEN** `ControlPlaneTrust` reports a not-ready condition and dependent `PolicyBinding` resources do not report ready

#### Scenario: ControlPlane trust is deleted
- **WHEN** a `ControlPlaneTrust` is deleted
- **THEN** the system cleans up only the OpenBao trust objects it owns and does not delete user policies, ServiceAccounts, ESO resources, secret data, or unrelated OpenBao configuration

### Requirement: ControlPlane ServiceAccount identity reference
The system SHALL let users create a workspace-namespace `ControlPlaneEntity` that references an existing ServiceAccount in the target ControlPlane. The system SHALL NOT create or manage that ServiceAccount.

#### Scenario: Existing ServiceAccount is referenced
- **WHEN** a user creates a `ControlPlaneEntity` with a `controlPlaneRef` and `serviceAccountRef`
- **THEN** the system resolves the referenced existing ServiceAccount identity and reports identity readiness without taking ownership of the ServiceAccount

#### Scenario: ServiceAccount is missing
- **WHEN** the referenced ServiceAccount does not exist or cannot be observed
- **THEN** `ControlPlaneEntity` reports a not-ready condition and no OpenBao role depending on it reports ready

#### Scenario: ServiceAccount token is used for setup
- **WHEN** the controller needs to verify or configure trust for a `ControlPlaneEntity`
- **THEN** it may mint or request a short-lived ServiceAccount JWT for setup or verification but SHALL NOT store OpenBao tokens as an output

### Requirement: One OpenBao JWT role per PolicyBinding
The system SHALL let users create a workspace-namespace `PolicyBinding` that references one `ControlPlaneEntity` and one user-managed OpenBao policy name. Each `PolicyBinding` SHALL own exactly one OpenBao JWT role bound to the referenced ControlPlane ServiceAccount identity and configured with exactly the named policy.

#### Scenario: Policy binding is reconciled
- **WHEN** a `PolicyBinding` references a ready `ControlPlaneEntity` and a resolvable `ControlPlaneTrust`
- **THEN** the system creates or updates one OpenBao JWT role for that binding and reports the generated role name and auth mount path in status

#### Scenario: Multiple policies for same ServiceAccount
- **WHEN** multiple `PolicyBinding` resources reference the same `ControlPlaneEntity` with different policy names
- **THEN** the system creates a distinct OpenBao JWT role for each binding rather than merging policies into one shared role

#### Scenario: Policy binding is deleted
- **WHEN** a `PolicyBinding` is deleted
- **THEN** the system deletes or disables only the OpenBao JWT role owned by that binding and does not delete the referenced OpenBao policy

### Requirement: User-managed OpenBao policies
The system SHALL treat `PolicyBinding.spec.policyName` as a free-form name of a user-managed OpenBao policy. The system SHALL NOT create, modify, or delete the actual OpenBao policy named by a `PolicyBinding`.

#### Scenario: Existing policy is named
- **WHEN** a `PolicyBinding` names an existing OpenBao policy
- **THEN** the system configures the binding role to request that policy and reports policy resolution success when the existence check is available

#### Scenario: Policy is missing
- **WHEN** a `PolicyBinding` names an OpenBao policy that does not exist
- **THEN** admission still accepts the resource and the controller reports a `PolicyResolved=False` or equivalent status condition instead of rejecting the resource at admission time

#### Scenario: Policy existence cannot be checked
- **WHEN** OpenBao permissions or backend behavior prevent checking whether the named policy exists
- **THEN** the system reports policy existence as unknown and does not falsely claim the policy exists

### Requirement: Trust-only output
The system SHALL only configure OpenBao trust and roles. The system SHALL NOT persist, rotate, propagate, or expose OpenBao tokens as Kubernetes resources or status outputs.

#### Scenario: Runtime client authenticates
- **WHEN** ESO or another user-managed client uses the existing ControlPlane ServiceAccount JWT against the configured OpenBao JWT auth mount and role
- **THEN** OpenBao issues a short-lived runtime token according to the user-managed policy bound by the `PolicyBinding`

#### Scenario: Controller reconciles successfully
- **WHEN** all referenced trust resources are ready
- **THEN** the system reports auth mount path and role name but does not create a Kubernetes Secret containing an OpenBao token

#### Scenario: Logs and status are inspected
- **WHEN** users inspect Kubernetes resources, status, events, and controller logs
- **THEN** no OpenBao token value is present

### Requirement: External ESO and ServiceAccount ownership
The system SHALL NOT install, configure, or own External Secrets Operator resources or tenant ControlPlane ServiceAccounts. Users remain responsible for ESO installation, `SecretStore` or `ClusterSecretStore` configuration, `ExternalSecret` resources, and ServiceAccount lifecycle.

#### Scenario: ESO is not installed
- **WHEN** trust resources are ready but ESO is not installed in the ControlPlane
- **THEN** the system still reports OpenBao trust readiness and does not attempt to install ESO

#### Scenario: User configures ESO SecretStore
- **WHEN** a user creates an ESO `SecretStore` using status output from `ControlPlaneTrust` and `PolicyBinding`
- **THEN** the platform service does not take ownership of that `SecretStore`

#### Scenario: ServiceAccount is changed by user
- **WHEN** the user changes or deletes the referenced ControlPlane ServiceAccount
- **THEN** the platform service reports updated readiness for the referencing `ControlPlaneEntity` and does not recreate the ServiceAccount

### Requirement: User-actionable status
The system SHALL expose status fields and conditions that let users understand and consume the configured OpenBao trust without relying on undocumented generated names.

#### Scenario: PolicyBinding is ready
- **WHEN** a `PolicyBinding` is ready
- **THEN** its status includes the generated OpenBao role name, resolved auth mount path, referenced policy name, policy existence state, and ready condition

#### Scenario: ControlPlaneTrust is ready
- **WHEN** a `ControlPlaneTrust` is ready
- **THEN** its status includes the OpenBao auth mount path and trust readiness conditions

#### Scenario: A dependency is not ready
- **WHEN** an upstream dependency such as `OpenBaoInstance`, `ProjectEntity`, `ControlPlaneTrust`, or `ControlPlaneEntity` is not ready
- **THEN** dependent resources report conditions that identify the blocking dependency and reason
