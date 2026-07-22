/*
Copyright 2026 SAP SE.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package backend runs a real OpenBao (or HashiCorp Vault) server in a
// testcontainers-managed Docker container for e2e tests.
//
// Networking: the container attaches to a caller-provided Docker network
// (the same one Kind will be told to use via KIND_EXPERIMENTAL_DOCKER_NETWORK
// in TestMain). Manager pods inside the Kind cluster reach the backend at
// http://openbao:8200 through Docker DNS on that shared network.
//
// From the host, tests reach the backend via the published port
// (Address() returns http://<host>:<mappedPort> for out-of-cluster seeding
// and assertions).
//
// Backend image is switchable via E2E_BACKEND_IMAGE:
//   - unset (default): openbao/openbao:latest, launched with `bao server -dev`
//   - hashicorp/vault:<tag>: launched with `vault server -dev`
//
// Same HTTP surface either way; the wrapper is backend-agnostic beyond
// the entrypoint command.
package backend

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	openbao "github.com/openbao/openbao/api/v2"
)

// NetworkAlias is the DNS name the container advertises on the shared
// Docker network. Manager pods use this to reach the backend, so it must
// match what test fixtures write into OpenBaoInstance.spec.address.
const NetworkAlias = "openbao"

// rootToken is the fixed dev-mode root token. Never used in production;
// dev servers accept anything but we set an explicit value so tests can
// build clients without parsing container logs.
const rootToken = "root"

// port is the HTTP listen port inside the container. Same for OpenBao
// and Vault dev servers.
const port = "8200"

// Backend is a running OpenBao/Vault dev-server container. Callers get
// one from Start and must call Stop.
type Backend struct {
	container testcontainers.Container
	// hostAddr is http://<host>:<mappedPort> — the address tests use
	// from OUTSIDE the Kind cluster (seed policies, assertions).
	hostAddr string
	// inClusterAddr is http://openbao:8200 — the address the manager pod
	// uses from INSIDE the cluster. Written into the OpenBaoInstance CR.
	inClusterAddr string
	// image is the resolved image reference for diagnostics.
	image string
}

// Start launches the backend attached to the given Docker network. The
// network must exist; typically it's created by testcontainers-go's
// network.New in TestMain and shared with the Kind cluster via
// KIND_EXPERIMENTAL_DOCKER_NETWORK.
func Start(ctx context.Context, nw *testcontainers.DockerNetwork) (*Backend, error) {
	image := os.Getenv("E2E_BACKEND_IMAGE")
	if image == "" {
		image = "openbao/openbao:latest"
	}

	cmd, err := devServerCmd(image)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	// Vault honours these env vars; OpenBao ignores unknown ones so
	// setting both keeps a single code path.
	env["VAULT_DEV_ROOT_TOKEN_ID"] = rootToken
	env["BAO_DEV_ROOT_TOKEN_ID"] = rootToken

	req := testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        image,
			Cmd:          cmd,
			Env:          env,
			ExposedPorts: []string{port + "/tcp"},
			// CAP_IPC_LOCK avoids the "mlock() failed" warning on both
			// images. Not strictly required in dev mode but keeps logs
			// clean.
			CapAdd: []string{"IPC_LOCK"},
			WaitingFor: wait.ForHTTP("/v1/sys/health").
				WithPort(port + "/tcp").
				WithStatusCodeMatcher(func(status int) bool {
					// A dev-mode server that has finished unsealing
					// returns 200; while sealed it returns 503. We only
					// care that the HTTP endpoint is answering, so
					// accept any 2xx OR 5xx (the "responding at all"
					// signal).
					return status >= 200 && status < 600
				}).
				WithStartupTimeout(30 * time.Second),
		},
		Started: true,
	}
	// Attach to the shared network with a stable alias so pods inside
	// Kind can reach us as http://openbao:8200.
	if err := network.WithNetwork([]string{NetworkAlias}, nw).Customize(&req); err != nil {
		return nil, fmt.Errorf("backend: attach to network: %w", err)
	}

	c, err := testcontainers.GenericContainer(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("backend: start container: %w", err)
	}

	host, err := c.Host(ctx)
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, fmt.Errorf("backend: get container host: %w", err)
	}
	mapped, err := c.MappedPort(ctx, port+"/tcp")
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, fmt.Errorf("backend: get mapped port: %w", err)
	}

	b := &Backend{
		container:     c,
		hostAddr:      fmt.Sprintf("http://%s:%s", host, mapped.Port()),
		inClusterAddr: fmt.Sprintf("http://%s:%s", NetworkAlias, port),
		image:         image,
	}
	// Sanity probe over HTTP from the host so a start-then-die failure
	// mode surfaces here rather than in a downstream test.
	if err := b.pingHealth(ctx); err != nil {
		_ = c.Terminate(ctx)
		return nil, fmt.Errorf("backend: post-start health probe failed: %w", err)
	}
	return b, nil
}

// Stop terminates the container. Idempotent; safe to defer.
func (b *Backend) Stop(ctx context.Context) error {
	if b == nil || b.container == nil {
		return nil
	}
	return b.container.Terminate(ctx)
}

// HostAddress returns the URL tests running on the host use to talk to
// the backend (for seeding policies, cross-checking role state, etc.).
func (b *Backend) HostAddress() string { return b.hostAddr }

// InClusterAddress returns the URL a pod inside the Kind cluster uses to
// reach the backend. Written into OpenBaoInstance.spec.address.
func (b *Backend) InClusterAddress() string { return b.inClusterAddr }

// RootToken returns the dev-mode root token. Callers use it to build
// admin clients for seeding policies and inspecting role state.
func (b *Backend) RootToken() string { return rootToken }

// Image is the resolved image reference — useful for test diagnostics.
func (b *Backend) Image() string { return b.image }

// SeedPolicy writes a policy under sys/policies/acl/<name>. Uses the
// same openbao/api/v2 client the production code uses so a schema drift
// between OpenBao and Vault would surface here first.
func (b *Backend) SeedPolicy(ctx context.Context, name, hcl string) error {
	c, err := b.adminClient()
	if err != nil {
		return err
	}
	_, err = c.Logical().WriteWithContext(ctx, "sys/policies/acl/"+name, map[string]any{
		"policy": hcl,
	})
	if err != nil {
		return fmt.Errorf("backend: seed policy %q: %w", name, err)
	}
	return nil
}

// AdminClient builds a root-authed client against the host address.
// Exported so tests can perform arbitrary post-hoc assertions (e.g. read
// a generated role, run a JWT login) without duplicating the config.
func (b *Backend) AdminClient() (*openbao.Client, error) {
	return b.adminClient()
}

func (b *Backend) adminClient() (*openbao.Client, error) {
	cfg := openbao.DefaultConfig()
	cfg.Address = b.hostAddr
	c, err := openbao.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("backend: build admin client: %w", err)
	}
	c.SetToken(rootToken)
	return c, nil
}

// pingHealth issues a raw HTTP GET so a container that starts and
// immediately fails surfaces the failure at Start time.
func (b *Backend) pingHealth(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.hostAddr+"/v1/sys/health", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("backend: /sys/health returned %d", resp.StatusCode)
	}
	return nil
}

// devServerCmd returns the entrypoint command for the given image. Both
// OpenBao and Vault ship the same dev-server flag set under their
// respective binary names.
func devServerCmd(image string) ([]string, error) {
	base := strings.SplitN(image, ":", 2)[0]
	base = strings.ToLower(base)
	// Match on the image path; support the common registry prefixes for
	// each project so operators don't have to think about registry
	// canonicalisation.
	switch {
	case strings.Contains(base, "openbao"):
		return []string{
			"bao", "server", "-dev",
			"-dev-root-token-id=" + rootToken,
			"-dev-listen-address=0.0.0.0:" + port,
		}, nil
	case strings.Contains(base, "vault"):
		return []string{
			"vault", "server", "-dev",
			"-dev-root-token-id=" + rootToken,
			"-dev-listen-address=0.0.0.0:" + port,
		}, nil
	default:
		return nil, errors.New("backend: E2E_BACKEND_IMAGE must reference an image whose path contains 'openbao' or 'vault'; got " + image)
	}
}
