# Manual Vault Namespace Acceptance Test Plan

## Purpose

This document defines the local/manual acceptance tests required before the controller can be described as working correctly for the currently available test environment.

The available backend is a HashiCorp Vault instance, not OpenBao. The team does not administer Vault's root namespace. Testing is restricted to an assigned parent Vault Enterprise namespace and two existing child namespaces that represent customers.

The tests focus on the trust-only architecture described by `define-openbao-platform-service`:

- configure JWT auth trust and roles;
- authenticate existing ControlPlane ServiceAccounts with short-lived Kubernetes JWTs;
- attach only pre-existing policies;
- expose actionable status;
- never manage secret data, policies, ESO, tenant ServiceAccounts, or persisted tenant login tokens.

## Scope caveat: Vault versus OpenBao

Vault's JWT auth API is close enough to OpenBao's API to exercise the central reconciliation and authentication flow. Vault Enterprise namespaces, however, are a Vault-specific capability, while the current design explicitly describes an OpenBao-only service.

Passing this plan proves compatibility with the tested Vault namespace environment. It does **not** by itself prove support for a real OpenBao deployment.

Before the public API is finalized, decide one of the following explicitly:

1. **Vault is a compatibility fixture only.** Keep the product OpenBao-specific and document the test limitation.
2. **Vault is an officially supported backend.** Revise the API/specification deliberately, including backend naming and Vault namespace semantics.

Do not silently call Vault an OpenBao instance in a final supported API.

## Test topology

Use the following logical topology:

```text
Vault root namespace                         not administered by this team
└── <platform-parent>/                      administered within delegated permissions
    ├── <customer-a>/                       existing customer namespace
    └── <customer-b>/                       existing customer namespace

Kubernetes platform cluster
├── controller
├── ProviderConfig / approved backend registrations
└── OpenMCP project/workspace resources

ControlPlane A
└── existing ServiceAccount A

ControlPlane B
└── existing ServiceAccount B
```

Recommended Vault object placement:

```text
<platform-parent>/<customer-a>/
├── auth/openmcp-<controlplane-a>-jwt
├── policy/customer-a-read                  created manually, never by controller
└── secret/customer-a/...                   created manually, never by controller

<platform-parent>/<customer-b>/
├── auth/openmcp-<controlplane-b>-jwt
├── policy/customer-b-read                  created manually, never by controller
└── secret/customer-b/...                   created manually, never by controller
```

Use one JWT auth mount per ControlPlane inside its customer Vault namespace. This isolates issuer/JWKS configuration and prevents role-name or identity collisions between ControlPlanes.

## Safety rules for manual tests

- Use placeholder namespace, address, policy, and ServiceAccount names in committed documentation.
- Never commit Vault tokens, Kubernetes JWTs, kubeconfigs, CA private keys, or real addresses.
- Keep JWTs and returned Vault tokens in shell variables only.
- Do not enable shell tracing (`set -x`) while handling credentials.
- Do not print login JSON or tokens into CI/controller logs.
- Prefer a short-lived controller credential for the test.
- Confirm the active `kubectl` context and `VAULT_NAMESPACE` before every destructive command.
- Deletion tests may remove only controller-owned JWT roles/auth mounts in the delegated test namespaces.

## Environment variables

Use placeholders such as:

```bash
export VAULT_ADDR="https://<vault-address>"
export VAULT_PARENT_NAMESPACE="<platform-parent>"
export VAULT_CUSTOMER_A="${VAULT_PARENT_NAMESPACE}/<customer-a>"
export VAULT_CUSTOMER_B="${VAULT_PARENT_NAMESPACE}/<customer-b>"

export PLATFORM_CONTEXT="<platform-kube-context>"
export CONTROLPLANE_A_CONTEXT="<controlplane-a-kube-context>"
export CONTROLPLANE_B_CONTEXT="<controlplane-b-kube-context>"

export CUSTOMER_A_POLICY="customer-a-read"
export CUSTOMER_B_POLICY="customer-b-read"
export JWT_AUDIENCE="<expected-vault-audience>"
```

Use the `vault` CLI for the available Vault instance. A future OpenBao proof should use `bao` against a real OpenBao instance.

## Phase 0: permission and connectivity preflight

### P0.1 Vault status is reachable in each delegated namespace

For each customer namespace:

```bash
VAULT_NAMESPACE="$VAULT_CUSTOMER_A" vault status
VAULT_NAMESPACE="$VAULT_CUSTOMER_B" vault status
```

Expected:

- TLS verification succeeds;
- Vault is initialized and unsealed;
- no root namespace operation is required.

### P0.2 Controller credential has only required namespace capabilities

Verify capabilities in each customer namespace for the planned mount path:

```bash
VAULT_NAMESPACE="$VAULT_CUSTOMER_A" \
  vault token capabilities sys/auth/<mount>

VAULT_NAMESPACE="$VAULT_CUSTOMER_A" \
  vault token capabilities auth/<mount>/config

VAULT_NAMESPACE="$VAULT_CUSTOMER_A" \
  vault token capabilities auth/<mount>/role/<role>

VAULT_NAMESPACE="$VAULT_CUSTOMER_A" \
  vault token capabilities sys/policies/acl/$CUSTOMER_A_POLICY
```

Required behavior:

- create/read/update/delete and required `sudo` access for the owned JWT auth mount;
- CRUD/list for owned JWT roles;
- read/list only for user-managed policies;
- no capability to write secret data or mutate customer policies;
- no capability against sibling customer namespaces unless explicitly delegated.

If the controller credential originates in the parent Vault namespace, verify its policy permits operations in both intended child namespaces and nowhere else.

### P0.3 Platform and ControlPlane connectivity

Verify:

```bash
kubectl --context "$PLATFORM_CONTEXT" cluster-info
kubectl --context "$CONTROLPLANE_A_CONTEXT" cluster-info
kubectl --context "$CONTROLPLANE_B_CONTEXT" cluster-info
```

The controller must be able to resolve the relevant OpenMCP ControlPlane references and read/request tokens for the referenced existing ServiceAccounts.

## Phase 1: manually owned prerequisites

For each customer, create the policy and test data manually. The controller must never perform this setup.

Example for customer A:

```bash
export VAULT_NAMESPACE="$VAULT_CUSTOMER_A"

vault policy write "$CUSTOMER_A_POLICY" ./<customer-a-policy.hcl>
vault kv put secret/customer-a/allowed value=success
vault kv put secret/customer-a/forbidden value=must-not-be-readable
```

Create equivalent but distinct resources for customer B.

Record before-test hashes or exported representations of:

- policy content;
- allowed and forbidden secret metadata;
- existing ESO resources, if present;
- existing Kubernetes ServiceAccounts.

These snapshots are used to prove that the controller does not mutate user-owned resources.

## Phase 2: backend registration and status

### T01 Approved backend/namespace registration

Create the platform-owned backend registration for customer A and customer B. Both may reference the same Vault address but must map to different approved Vault namespace paths.

Expected:

- tenants reference an approved registration by Kubernetes name;
- tenants cannot submit arbitrary Vault addresses or arbitrary Vault namespace paths;
- the customer A registration cannot resolve customer B's namespace;
- status reports reachability without exposing credentials.

The current CRD types are still scaffolding and do not yet contain these fields. A platform-controlled namespace mapping must be designed before this test can run.

### T02 Connectivity, TLS, and recovery

For each registration:

1. Configure the correct address and CA information.
2. Confirm Ready/reachability status.
3. Configure an invalid CA or unreachable address.
4. Confirm an actionable failure condition.
5. Restore the correct configuration.
6. Confirm automatic recovery without recreating the CR.

Expected:

- no credential material in status/events;
- `observedGeneration` tracks the reconciled generation;
- retries use backoff rather than a hot loop.

## Phase 3: JWT trust reconciliation

### T03 Customer A JWT auth mount

Create the resources leading to `ControlPlaneTrust` for ControlPlane A.

Expected Vault result:

```bash
VAULT_NAMESPACE="$VAULT_CUSTOMER_A" vault auth list
VAULT_NAMESPACE="$VAULT_CUSTOMER_A" vault read auth/<mount-a>/config
```

Verify:

- exactly one deterministic JWT auth mount exists;
- issuer/JWKS/OIDC discovery points to ControlPlane A;
- configured audience matches the intended login audience;
- generated mount path is stable, sanitized, bounded, and exposed in status;
- repeated reconciliation creates no duplicate mount.

### T04 Customer B JWT auth mount

Repeat for ControlPlane B in customer B's Vault namespace.

Verify that customer A and B mounts are independent even if Kubernetes resource names are identical.

### T05 Existing ServiceAccount identity resolution

For each ControlPlane, reference an existing ServiceAccount through `ControlPlaneEntity`.

Expected:

- controller resolves the ServiceAccount;
- controller does not create or modify it;
- safe subject/issuer/audience identity information is reflected in status;
- raw JWT contents never appear in status, events, or logs.

Negative/recovery case:

1. Reference a missing ServiceAccount.
2. Confirm Ready=False with an actionable reason.
3. Create the ServiceAccount manually.
4. Confirm automatic recovery.

### T06 PolicyBinding creates exactly one JWT role

Precondition: the named policy already exists in the same Vault customer namespace.

Create `PolicyBinding`.

Verify:

```bash
VAULT_NAMESPACE="$VAULT_CUSTOMER_A" \
  vault read auth/<mount-a>/role/<role-a>
```

Expected:

- exactly one role per `PolicyBinding`;
- role is bound to the exact ServiceAccount subject/claims;
- role uses the expected audience;
- role attaches exactly `spec.policyName`;
- role name and mount path are exposed in status;
- no policy is created or modified.

## Phase 4: golden-path login and authorization

### T07 Real ServiceAccount JWT login

Request a short-lived JWT:

```bash
JWT_A="$(
  kubectl --context "$CONTROLPLANE_A_CONTEXT" \
    -n <service-account-namespace> \
    create token <service-account> \
    --audience="$JWT_AUDIENCE"
)"
```

Login inside customer A's Vault namespace:

```bash
LOGIN_A="$(
  VAULT_NAMESPACE="$VAULT_CUSTOMER_A" \
    vault write -format=json \
      auth/<mount-a>/login \
      role=<role-a> \
      jwt="$JWT_A"
)"

TOKEN_A="$(jq -r '.auth.client_token' <<<"$LOGIN_A")"
unset LOGIN_A JWT_A
```

Expected:

- login succeeds;
- returned token is short-lived;
- token belongs to the customer A Vault namespace;
- token contains exactly the intended policy;
- controller never observes or persists this returned token.

### T08 Allowed and forbidden access

Expected success:

```bash
VAULT_NAMESPACE="$VAULT_CUSTOMER_A" VAULT_TOKEN="$TOKEN_A" \
  vault kv get secret/customer-a/allowed
```

Expected failure:

```bash
VAULT_NAMESPACE="$VAULT_CUSTOMER_A" VAULT_TOKEN="$TOKEN_A" \
  vault kv get secret/customer-a/forbidden
```

Also verify that write/delete operations fail when the policy is read-only.

### T09 Cross-customer isolation

Customer A token must not read customer B resources:

```bash
VAULT_NAMESPACE="$VAULT_CUSTOMER_B" VAULT_TOKEN="$TOKEN_A" \
  vault kv get secret/customer-b/allowed
```

Expected: denied.

Also prove:

- customer A JWT cannot login through customer B's role;
- customer B JWT cannot login through customer A's role;
- same ServiceAccount namespace/name in separate ControlPlanes does not grant cross-customer access;
- roles and mounts do not collide.

Unset the token after testing:

```bash
unset TOKEN_A
```

## Phase 5: authentication rejection matrix

All cases must fail without leaking the presented JWT:

### T10 Wrong ServiceAccount

Use a JWT from a different ServiceAccount in the same ControlPlane.

### T11 Wrong Kubernetes namespace

Use a JWT whose `sub` contains the same ServiceAccount name but a different Kubernetes namespace.

### T12 Wrong audience

Mint a JWT for another audience.

### T13 Wrong issuer/ControlPlane

Use a JWT from ControlPlane B against ControlPlane A's mount/role.

### T14 Malformed or expired JWT

Try malformed and expired tokens.

Expected for T10–T14:

- login denied;
- no role or mount mutation;
- no JWT contents in controller/Vault-facing test logs;
- failure is attributable to subject/audience/issuer/validity constraints.

## Phase 6: policy ownership and status

### T15 Missing policy

Create a `PolicyBinding` for a policy that does not exist.

Expected:

- CR admission is not rejected solely because the policy is currently missing;
- `PolicyResolved=False` with a `PolicyNotFound`-style reason;
- Ready=False/WaitingForPolicy;
- operator does not create the policy.

Create the policy manually and verify automatic recovery.

### T16 Policy existence cannot be checked

Temporarily remove policy-read permission from the controller credential.

Expected:

- `PolicyResolved=Unknown`, never a fabricated True value;
- actionable condition explaining the permission/check limitation;
- operator does not change the policy.

Restore permission and verify recovery.

### T17 Policy content remains untouched

Update an operator-owned role or force repeated reconciliation, then compare the policy snapshot from Phase 1.

Expected: byte/semantic content unchanged.

## Phase 7: idempotency, restart, drift, and recovery

### T18 Idempotent reconciliation

Trigger repeated reconciliation and verify:

- no duplicate mounts or roles;
- generated names remain stable;
- status converges;
- policy and secret data remain unchanged.

### T19 Controller restart

Restart the controller with all CRs and Vault objects present.

Expected:

- existing objects are discovered;
- no duplicate objects;
- readiness recovers automatically;
- no stored tenant login token is needed.

### T20 Owned-role drift repair

Manually alter an operator-owned JWT role.

Expected:

- owned role fields are restored;
- policy contents remain untouched;
- status/event optionally reports drift/reconciliation.

### T21 Dependency outage and recovery

Exercise separately:

- Vault unavailable;
- invalid TLS/CA;
- controller Vault credential denied;
- ControlPlane API unavailable;
- issuer/JWKS discovery unavailable.

Expected:

- actionable conditions;
- bounded retry/backoff;
- no duplicate objects;
- automatic recovery after dependency restoration.

## Phase 8: update and deletion ownership

### T22 PolicyBinding update

Change `policyName` to another manually created policy.

Expected:

- owned role converges to exactly the new policy;
- old access stops working;
- new access works;
- neither old nor new policy is modified/deleted.

### T23 ServiceAccount reference update

Change the referenced existing ServiceAccount where permitted.

Expected:

- old ServiceAccount JWT can no longer authenticate;
- new ServiceAccount JWT succeeds;
- neither ServiceAccount is created/modified/deleted.

### T24 PolicyBinding deletion

Expected:

- owned JWT role removed;
- auth mount preserved while trust still exists;
- policy, secret data, ServiceAccount, and ESO resources remain.

### T25 ControlPlaneTrust deletion

After dependent roles are gone, delete the trust resource.

Expected:

- only its owned JWT mount/trust configuration is removed;
- unrelated mounts untouched;
- customer B untouched when deleting customer A;
- outage during deletion leaves a truthful pending finalizer and completes after recovery.

### T26 Parent/backend deletion safety

Delete or attempt to delete higher-level resources while dependents exist.

Expected behavior must be explicitly defined and consistent:

- refuse/wait for dependents, or
- perform dependency-safe owned cleanup.

Never delete customer policies, secret data, ESO resources, or ServiceAccounts.

## Phase 9: security and non-ownership audit

### T27 No credential persistence

Inspect all controller-owned and domain CRs:

```bash
kubectl --context "$PLATFORM_CONTEXT" get \
  providerconfigs,openbaoinstances,projectentities,controlplanetrusts,controlplaneentities,policybindings \
  -A -o yaml
```

Verify absence of:

- Kubernetes ServiceAccount JWTs;
- Vault login/client tokens;
- secret values;
- copied administrative credentials.

A referenced platform credential Secret may exist, but its value must not be copied into status or other resources.

### T28 No credential logging

Scan controller logs/events during success and failure cases for:

- JWT fragments;
- Authorization headers;
- Vault client tokens;
- Kubernetes Secret data.

Expected: no secret values.

### T29 Forbidden managed-resource audit

Before and after the test, compare snapshots for:

- Kubernetes ServiceAccounts;
- ESO installation/resources;
- Vault policies;
- Vault secret data;
- unrelated Vault auth mounts/roles.

Expected: the operator has changed only its owned JWT mounts, JWT roles, status, conditions, events, and finalizers.

### T30 RBAC minimization

Verify the controller has only the Kubernetes and Vault capabilities needed for:

- reading/reconciling its CRDs;
- resolving ControlPlanes;
- reading/requesting tokens for referenced ServiceAccounts;
- managing its JWT mounts/roles;
- reading policy existence.

It should not have broad tenant Secret write access or root Vault capabilities.

## Phase 10: optional user-managed ESO proof

ESO remains user-managed. If ESO is a primary consumer, run one integration proof after the CLI login succeeds.

### T31 User-created SecretStore and ExternalSecret

Use status values from the trust and binding resources:

- Vault address and customer namespace mapping;
- auth mount path;
- JWT role name;
- ServiceAccount reference.

Manually create an ESO `SecretStore` or `ClusterSecretStore`, then an `ExternalSecret`.

Expected:

- allowed secret synchronizes;
- forbidden path fails;
- operator does not create/change/delete ESO resources;
- deleting the OpenBao/Vault trust CR does not silently delete user-managed ESO resources.

## Phase 11: PlatformService deployment proof

### T32 Actual OpenMCP PlatformService lifecycle

Do not rely only on `make run` or `make deploy`.

Verify installation through the intended OpenMCP `PlatformService` path:

- controller deployment succeeds;
- CRDs are installed on the intended clusters;
- platform/onboarding/ControlPlane RBAC is correct;
- APIs are discoverable to intended users;
- PlatformService removal cleans only deployment and explicitly owned resources.

The open question about `PlatformService.status.resources[]` or equivalent discoverability must be resolved before calling this integration complete.

## Evidence to retain

Retain only non-secret evidence:

- CR names, UIDs, generations, and condition summaries;
- generated auth mount/role names;
- Vault namespace paths using approved non-secret names;
- policy names, not policy secret data;
- success/failure exit status for login/access cases;
- controller commit/image digest;
- controller log lines after redaction checks;
- before/after lists of owned objects;
- cleanup results.

Do not retain JWTs, login responses, client tokens, credential Secrets, or kubeconfig content.

## Exit criteria

The controller can be called functionally correct in the available Vault namespace environment only when:

1. Permission/connectivity preflight passes without root access.
2. Each customer receives an isolated JWT mount and role in its own Vault namespace.
3. A real existing ServiceAccount JWT authenticates successfully.
4. Allowed access succeeds and forbidden/cross-customer access fails.
5. Wrong subject, namespace, audience, issuer, malformed JWT, and expired JWT are rejected.
6. Policies, secrets, ESO resources, and ServiceAccounts are never created, mutated, or deleted by the controller.
7. No JWT or Vault token is persisted or logged.
8. Reconciliation is idempotent and restart-safe.
9. Owned drift is repaired without mutating user-owned resources.
10. Dependency failure and deletion finalizers recover correctly.
11. Customer A lifecycle operations do not affect customer B.
12. Actual OpenMCP PlatformService installation and RBAC work.
13. The Vault-versus-OpenBao support decision is recorded honestly.

The central golden-path proof is:

```text
Existing ControlPlane ServiceAccount
→ short-lived Kubernetes JWT
→ controller-managed JWT mount and role in the correct customer Vault namespace
→ successful Vault login
→ allowed secret succeeds
→ forbidden and cross-customer secrets fail
→ no JWT/login token persisted anywhere
```
