// SPDX-FileCopyrightText: Copyright OpenControlPlane contributors.
// SPDX-License-Identifier: Apache-2.0

// Package openbao is the OpenBao integration layer for
// platform-service-openbao. It exposes a narrow interface (Client) that
// controllers depend on, plus a real implementation over
// github.com/openbao/openbao/api/v2 and an in-memory fake used in
// reconciler tests. The design intent (see openspec/.../design.md) is that
// no OpenBao tokens are ever persisted or exposed through this layer.
package openbao

import (
	"context"
	"errors"
)

// HealthInfo is a non-sensitive summary of the OpenBao server's /sys/health
// response, safe to publish in OpenBaoInstance.status.
type HealthInfo struct {
	// Initialized reflects the `initialized` flag.
	Initialized bool
	// Sealed reflects the `sealed` flag.
	Sealed bool
	// Version is the reported server version, if any.
	Version string
}

// JWTAuthConfig is the trust configuration written to a JWT auth mount's
// `/config` endpoint. Fields are optional; the caller populates whichever
// combination the target ControlPlane exposes (JWKS OR OIDC discovery).
type JWTAuthConfig struct {
	// OIDCDiscoveryURL is the well-known base URL for OIDC discovery.
	OIDCDiscoveryURL string
	// OIDCDiscoveryCAPEM is the PEM bundle used to verify the discovery
	// server's certificate. Empty means system trust.
	OIDCDiscoveryCAPEM string
	// JWKSURL is a direct JWKS endpoint, used when discovery is not
	// available.
	JWKSURL string
	// JWKSCAPEM is the PEM bundle used to verify the JWKS endpoint.
	JWKSCAPEM string
	// BoundIssuer is the required `iss` claim.
	BoundIssuer string
	// DefaultRole is optionally applied when a login omits the role.
	DefaultRole string
}

// JWTRole is the input for creating/updating a single OpenBao JWT role.
// Reconcilers populate this from a PolicyBinding + its ControlPlaneEntity.
type JWTRole struct {
	// Name is the role name (Client uses it in the path).
	Name string
	// RoleType is the JWT role type. "jwt" is what this service uses.
	RoleType string
	// BoundAudiences must match the JWT `aud` claim.
	BoundAudiences []string
	// BoundSubject constrains the JWT `sub` claim to a fixed value.
	BoundSubject string
	// BoundClaims constrains additional JWT claims (e.g.
	// kubernetes.io/serviceaccount/namespace).
	BoundClaims map[string]any
	// UserClaim tells OpenBao which claim to use as the alias name.
	UserClaim string
	// TokenPolicies are the OpenBao policies granted on successful login.
	// This service always sets exactly one entry — the user-managed policy
	// named by PolicyBinding.spec.policyName.
	TokenPolicies []string
	// TokenTTL, TokenMaxTTL are optional per-role overrides.
	TokenTTL    string
	TokenMaxTTL string
}

// PolicyExistence is a tri-state result for PolicyExists. "known == false"
// means the controller could not determine existence (e.g. missing
// permission); reconcilers report PolicyResolved=Unknown in that case.
type PolicyExistence struct {
	Exists bool
	Known  bool
}

// ErrNotFound is returned by Delete-style calls when the target object was
// already absent. Reconcilers treat this as success.
var ErrNotFound = errors.New("openbao: not found")

// Client is the narrow surface controllers use to configure OpenBao trust.
// Exactly what the design mandates — auth mounts, JWT trust config, JWT
// roles, and read-only policy existence. NO methods that would create,
// modify, or delete OpenBao policies or store tokens.
type Client interface {
	// Health probes /sys/health.
	Health(ctx context.Context) (HealthInfo, error)

	// EnsureEntity creates or updates an identity entity and returns its
	// canonical ID. Idempotent by entity name.
	EnsureEntity(ctx context.Context, name string, metadata map[string]string) (string, error)

	// EnsureJWTAuthMount creates the mount at `path` (without leading
	// "auth/") if missing. Idempotent.
	EnsureJWTAuthMount(ctx context.Context, path string) error

	// ConfigureJWTTrust writes the trust config to `auth/<path>/config`.
	// Idempotent.
	ConfigureJWTTrust(ctx context.Context, path string, cfg JWTAuthConfig) error

	// EnsureJWTRole writes the role at `auth/<mount>/role/<role.Name>`.
	// Idempotent.
	EnsureJWTRole(ctx context.Context, mount string, role JWTRole) error

	// DeleteJWTRole removes the role. Returns nil if already absent.
	DeleteJWTRole(ctx context.Context, mount, name string) error

	// DeleteAuthMount unmounts an auth backend at `path` (without leading
	// "auth/"). Returns nil if already absent. Called only by
	// ControlPlaneTrust finalizer for mounts it owns.
	DeleteAuthMount(ctx context.Context, path string) error

	// PolicyExists reads `sys/policies/acl/<name>` to check existence.
	// Never mutates the policy. Returns Known=false when the controller
	// lacks permission to check.
	PolicyExists(ctx context.Context, name string) (PolicyExistence, error)
}
