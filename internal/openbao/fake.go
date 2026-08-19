// SPDX-FileCopyrightText: Copyright OpenControlPlane contributors.
// SPDX-License-Identifier: Apache-2.0

package openbao

import (
	"context"
	"sync"
)

// FakeClient is an in-memory Client used by reconciler tests. It records
// mounts, JWT configs, and roles so tests can assert *what the reconciler
// did*, not just that Reconcile returned no error. It intentionally has NO
// state for policies except an existence map — matching the design's
// trust-only contract: this service reads policy existence but never
// creates or mutates policies.
type FakeClient struct {
	// HealthResult is returned by Health(). Zero value is a healthy backend.
	HealthResult HealthInfo
	// HealthErr, if set, is returned by Health() instead of HealthResult.
	HealthErr error

	// Policies is the set of policy names the fake reports as existing.
	// Tests populate this to simulate user-managed policies.
	Policies map[string]bool
	// PoliciesUnknown, if true, forces PolicyExists to return Known=false
	// so reconcilers exercise the "cannot check" branch.
	PoliciesUnknown bool

	mu       sync.Mutex
	mounts   map[string]bool          // path -> exists
	configs  map[string]JWTAuthConfig // path -> last config written
	roles    map[string]JWTRole       // mount+"/"+name -> last role written
	entities map[string]string        // name -> id
}

// NewFakeClient returns a healthy fake with empty state.
func NewFakeClient() *FakeClient {
	return &FakeClient{
		Policies: map[string]bool{},
		mounts:   map[string]bool{},
		configs:  map[string]JWTAuthConfig{},
		roles:    map[string]JWTRole{},
		entities: map[string]string{},
	}
}

// ensure FakeClient satisfies the interface at compile time.
var _ Client = (*FakeClient)(nil)

func (f *FakeClient) Health(_ context.Context) (HealthInfo, error) {
	if f.HealthErr != nil {
		return HealthInfo{}, f.HealthErr
	}
	return f.HealthResult, nil
}

func (f *FakeClient) EnsureEntity(_ context.Context, name string, _ map[string]string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entities[name] == "" {
		f.entities[name] = "entity-" + name
	}
	return f.entities[name], nil
}

func (f *FakeClient) EnsureJWTAuthMount(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mounts[path] = true
	return nil
}

func (f *FakeClient) ConfigureJWTTrust(_ context.Context, path string, cfg JWTAuthConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.mounts[path] {
		// Mirror OpenBao behaviour: configuring a non-existent mount fails.
		return ErrNotFound
	}
	f.configs[path] = cfg
	return nil
}

func (f *FakeClient) EnsureJWTRole(_ context.Context, mount string, role JWTRole) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.mounts[mount] {
		return ErrNotFound
	}
	f.roles[mount+"/"+role.Name] = role
	return nil
}

func (f *FakeClient) DeleteJWTRole(_ context.Context, mount, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.roles, mount+"/"+name)
	return nil
}

func (f *FakeClient) DeleteAuthMount(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.mounts, path)
	delete(f.configs, path)
	// Roles under the mount also disappear.
	for k := range f.roles {
		if len(k) > len(path)+1 && k[:len(path)+1] == path+"/" {
			delete(f.roles, k)
		}
	}
	return nil
}

func (f *FakeClient) PolicyExists(_ context.Context, name string) (PolicyExistence, error) {
	if f.PoliciesUnknown {
		return PolicyExistence{Known: false}, nil
	}
	return PolicyExistence{Exists: f.Policies[name], Known: true}, nil
}

// --- test inspection helpers ---

// HasMount reports whether a mount is currently registered.
func (f *FakeClient) HasMount(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mounts[path]
}

// Config returns the last-written JWT config for a mount, if any.
func (f *FakeClient) Config(path string) (JWTAuthConfig, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.configs[path]
	return c, ok
}

// Role returns the last-written role for (mount, name), if any.
func (f *FakeClient) Role(mount, name string) (JWTRole, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.roles[mount+"/"+name]
	return r, ok
}

// RoleCount returns the number of roles currently registered — useful for
// asserting that PolicyBinding cleanup deleted exactly its own role.
func (f *FakeClient) RoleCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.roles)
}
