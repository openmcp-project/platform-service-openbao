// SPDX-FileCopyrightText: Copyright OpenControlPlane contributors.
// SPDX-License-Identifier: Apache-2.0

package openbao

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"strings"

	openbao "github.com/openbao/openbao/api/v2"
)

// APIClient implements Client against a live OpenBao server via
// github.com/openbao/openbao/api/v2. Reconcilers construct one per
// OpenBaoInstance the first time they act on that instance.
type APIClient struct {
	c *openbao.Client
}

var _ Client = (*APIClient)(nil)

// Config captures everything needed to build an APIClient from an
// OpenBaoInstance spec + a platform credential. It is intentionally
// separate from OpenBaoInstance so this package does not depend on the
// API types package.
type Config struct {
	// Address is the OpenBao API base URL (required).
	Address string
	// CABundlePEM is the PEM-encoded CA bundle used to verify the server
	// certificate. Empty means system trust.
	CABundlePEM []byte
	// InsecureSkipVerify disables server-cert verification. Dev only.
	InsecureSkipVerify bool
	// Namespace scopes API calls to an OpenBao namespace (empty = root).
	Namespace string
	// Token is the OpenBao token used to authenticate the controller's
	// calls. Never surfaced through Client methods or logged by this
	// package.
	Token string
}

// New builds an APIClient from cfg. Returns an error on invalid TLS or
// address configuration; does NOT probe the server (call Health for that).
func New(cfg Config) (*APIClient, error) {
	if strings.TrimSpace(cfg.Address) == "" {
		return nil, errors.New("openbao: address is required")
	}

	baoCfg := openbao.DefaultConfig()
	baoCfg.Address = cfg.Address

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.InsecureSkipVerify {
		tlsConfig.InsecureSkipVerify = true
	}
	if len(cfg.CABundlePEM) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(cfg.CABundlePEM) {
			return nil, errors.New("openbao: CA bundle contains no valid PEM certificates")
		}
		tlsConfig.RootCAs = pool
	}
	// Assign the TLS config to the default HTTP transport used by the
	// underlying retryable client.
	if tr, ok := baoCfg.HttpClient.Transport.(*http.Transport); ok {
		tr.TLSClientConfig = tlsConfig
	} else {
		baoCfg.HttpClient.Transport = &http.Transport{TLSClientConfig: tlsConfig}
	}

	c, err := openbao.NewClient(baoCfg)
	if err != nil {
		return nil, fmt.Errorf("openbao: build client: %w", err)
	}
	if cfg.Namespace != "" {
		c.SetNamespace(cfg.Namespace)
	}
	if cfg.Token != "" {
		c.SetToken(cfg.Token)
	}
	return &APIClient{c: c}, nil
}

// LoginJWT exchanges a Kubernetes JWT against an OpenBao/Vault JWT auth
// mount and returns the resulting short-lived OpenBao token. The token is
// returned only to the caller so it can be placed into an in-memory client;
// callers must never persist or log it.
func LoginJWT(ctx context.Context, cfg Config, mount, role, jwt string) (string, error) {
	if strings.TrimSpace(jwt) == "" {
		return "", errors.New("openbao: jwt is required")
	}
	client, err := New(Config{
		Address:            cfg.Address,
		CABundlePEM:        cfg.CABundlePEM,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
		Namespace:          cfg.Namespace,
	})
	if err != nil {
		return "", err
	}
	resp, err := client.c.Logical().WriteWithContext(ctx, "auth/"+strings.Trim(mount, "/")+"/login", map[string]any{
		"role": role,
		"jwt":  jwt,
	})
	if err != nil {
		return "", fmt.Errorf("openbao: jwt login via mount %q role %q: %w", mount, role, err)
	}
	if resp == nil || resp.Auth == nil || resp.Auth.ClientToken == "" {
		return "", fmt.Errorf("openbao: jwt login via mount %q role %q returned no token", mount, role)
	}
	return resp.Auth.ClientToken, nil
}

// Health probes /sys/health without altering state.
//
// /sys/health is a backend-global endpoint. Vault Enterprise namespaces
// return 404 "unsupported path" for namespaced health requests, while
// OpenBao/Vault object operations below still need the configured namespace
// for auth mounts, roles, and policies. Use a shallow root-namespace client
// only for the health probe so namespace-scoped reconciliation remains intact.
func (a *APIClient) Health(ctx context.Context) (HealthInfo, error) {
	resp, err := a.c.WithNamespace("").Sys().HealthWithContext(ctx)
	if err != nil {
		return HealthInfo{}, fmt.Errorf("openbao: health probe: %w", err)
	}
	return HealthInfo{
		Initialized: resp.Initialized,
		Sealed:      resp.Sealed,
		Version:     resp.Version,
	}, nil
}

// EnsureEntity creates or updates an identity entity and returns its canonical ID.
func (a *APIClient) EnsureEntity(ctx context.Context, name string, metadata map[string]string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("openbao: entity name required")
	}
	data := map[string]any{"name": name}
	if len(metadata) > 0 {
		data["metadata"] = metadata
	}
	resp, err := a.c.Logical().WriteWithContext(ctx, "identity/entity/name/"+name, data)
	if err != nil {
		return "", fmt.Errorf("openbao: ensure identity entity %q: %w", name, err)
	}
	if id := idFromSecret(resp); id != "" {
		return id, nil
	}
	// Vault/OpenBao may return no body for an idempotent update. Read the
	// named entity back to get its canonical ID for status.
	resp, err = a.c.Logical().ReadWithContext(ctx, "identity/entity/name/"+name)
	if err != nil {
		return "", fmt.Errorf("openbao: read identity entity %q after write: %w", name, err)
	}
	if id := idFromSecret(resp); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("openbao: identity entity %q returned no id", name)
}

// EnsureJWTAuthMount creates a JWT auth mount at `path` if absent. If the
// mount already exists it is left in place — writes to the config endpoint
// happen separately via ConfigureJWTTrust. This deliberately avoids listing
// all auth mounts because the platform controller only needs permission to
// manage its own deterministic mount paths.
func (a *APIClient) EnsureJWTAuthMount(ctx context.Context, path string) error {
	opts := &openbao.EnableAuthOptions{Type: "jwt"}
	if err := a.c.Sys().EnableAuthWithOptionsWithContext(ctx, path, opts); err != nil {
		if isPathAlreadyInUse(err) {
			return nil
		}
		return fmt.Errorf("openbao: enable jwt auth at %q: %w", path, err)
	}
	return nil
}

// ConfigureJWTTrust writes the trust configuration to auth/<path>/config.
// Only non-empty fields in cfg are sent so partial updates don't clobber
// server-side defaults.
func (a *APIClient) ConfigureJWTTrust(ctx context.Context, path string, cfg JWTAuthConfig) error {
	data := map[string]any{}
	if cfg.OIDCDiscoveryURL != "" {
		data["oidc_discovery_url"] = cfg.OIDCDiscoveryURL
	}
	if cfg.OIDCDiscoveryCAPEM != "" {
		data["oidc_discovery_ca_pem"] = cfg.OIDCDiscoveryCAPEM
	}
	if cfg.JWKSURL != "" {
		data["jwks_url"] = cfg.JWKSURL
	}
	if cfg.JWKSCAPEM != "" {
		data["jwks_ca_pem"] = cfg.JWKSCAPEM
	}
	if cfg.BoundIssuer != "" {
		data["bound_issuer"] = cfg.BoundIssuer
	}
	if cfg.DefaultRole != "" {
		data["default_role"] = cfg.DefaultRole
	}
	if _, err := a.c.Logical().WriteWithContext(ctx, "auth/"+path+"/config", data); err != nil {
		return fmt.Errorf("openbao: configure jwt trust at %q: %w", path, err)
	}
	return nil
}

// EnsureJWTRole writes the role at auth/<mount>/role/<role.Name>. OpenBao
// treats Logical().Write as upsert semantics — safe to call every reconcile.
func (a *APIClient) EnsureJWTRole(ctx context.Context, mount string, role JWTRole) error {
	if role.Name == "" {
		return errors.New("openbao: role name required")
	}
	roleType := role.RoleType
	if roleType == "" {
		roleType = "jwt"
	}
	data := map[string]any{
		"role_type":      roleType,
		"user_claim":     role.UserClaim,
		"token_policies": role.TokenPolicies,
	}
	if len(role.BoundAudiences) > 0 {
		data["bound_audiences"] = role.BoundAudiences
	}
	if role.BoundSubject != "" {
		data["bound_subject"] = role.BoundSubject
	}
	if len(role.BoundClaims) > 0 {
		data["bound_claims"] = role.BoundClaims
	}
	if role.TokenTTL != "" {
		data["token_ttl"] = role.TokenTTL
	}
	if role.TokenMaxTTL != "" {
		data["token_max_ttl"] = role.TokenMaxTTL
	}
	if _, err := a.c.Logical().WriteWithContext(ctx, "auth/"+mount+"/role/"+role.Name, data); err != nil {
		return fmt.Errorf("openbao: ensure jwt role %q on mount %q: %w", role.Name, mount, err)
	}
	return nil
}

// DeleteJWTRole removes the role. OpenBao's Logical Delete is idempotent
// (no-op on missing), so we treat any 404 flavour as success.
func (a *APIClient) DeleteJWTRole(ctx context.Context, mount, name string) error {
	if _, err := a.c.Logical().DeleteWithContext(ctx, "auth/"+mount+"/role/"+name); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("openbao: delete jwt role %q on mount %q: %w", name, mount, err)
	}
	return nil
}

// DeleteAuthMount unmounts an auth backend. Idempotent on missing mount.
func (a *APIClient) DeleteAuthMount(ctx context.Context, path string) error {
	if err := a.c.Sys().DisableAuthWithContext(ctx, path); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("openbao: disable auth mount %q: %w", path, err)
	}
	return nil
}

// PolicyExists reads /sys/policies/acl/<name>. Missing policy is False,
// permission denied is Unknown.
func (a *APIClient) PolicyExists(ctx context.Context, name string) (PolicyExistence, error) {
	resp, err := a.c.Logical().ReadWithContext(ctx, "sys/policies/acl/"+name)
	if err != nil {
		if isNotFound(err) {
			return PolicyExistence{Exists: false, Known: true}, nil
		}
		if isPermissionDenied(err) {
			return PolicyExistence{Known: false}, nil
		}
		return PolicyExistence{}, fmt.Errorf("openbao: read policy %q: %w", name, err)
	}
	return PolicyExistence{Exists: resp != nil, Known: true}, nil
}

// isNotFound classifies an error as "target does not exist". Both the
// openbao client's ResponseError type and plain 404s reach this path.
func idFromSecret(resp *openbao.Secret) string {
	if resp == nil || resp.Data == nil {
		return ""
	}
	if id, ok := resp.Data["id"].(string); ok {
		return id
	}
	return ""
}

func isNotFound(err error) bool {
	var re *openbao.ResponseError
	if errors.As(err, &re) {
		return re.StatusCode == http.StatusNotFound
	}
	return false
}

// isPermissionDenied classifies an error as insufficient auth — used to
// distinguish "policy doesn't exist" from "controller can't tell".
func isPermissionDenied(err error) bool {
	var re *openbao.ResponseError
	if errors.As(err, &re) {
		return re.StatusCode == http.StatusForbidden
	}
	return false
}

func isPathAlreadyInUse(err error) bool {
	var re *openbao.ResponseError
	if errors.As(err, &re) && re.StatusCode == http.StatusBadRequest {
		for _, e := range re.Errors {
			if strings.Contains(strings.ToLower(e), "path is already in use") {
				return true
			}
		}
	}
	return strings.Contains(strings.ToLower(err.Error()), "path is already in use")
}
