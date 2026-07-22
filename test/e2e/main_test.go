//go:build e2e
// +build e2e

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

// Package e2e contains the platform-service-openbao end-to-end suite.
//
// Bootstrap flow (see TestMain):
//  1. testcontainers-go creates a Docker network; Kind is instructed to
//     use it via KIND_EXPERIMENTAL_DOCKER_NETWORK so the manager pod and
//     the backend container share DNS.
//  2. A real OpenBao (or Vault) dev-server starts on that network with
//     alias "openbao" — the manager reaches it as http://openbao:8200.
//  3. The manager image is built locally (unless IMG is pre-set) so
//     LoadImageToCluster: true works.
//  4. openmcp-testing spins up Kind, installs openmcp-operator, then
//     applies our PlatformService and its config directory
//     (ServiceConfig + OpenBaoInstance). The reconcilers come up and
//     start watching.
//  5. Individual tests then create tenant CRs and assert behaviour.
package e2e

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"k8s.io/klog/v2"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"

	"github.com/openmcp-project/openmcp-testing/pkg/platformservices"
	"github.com/openmcp-project/openmcp-testing/pkg/providers"
	"github.com/openmcp-project/openmcp-testing/pkg/setup"

	"github.com/openmcp-project/platform-service-openbao/test/e2e/backend"
)

// testenv is the shared e2e-framework environment. Populated in
// TestMain, consumed by every _test.go via testenv.Test(t, feature).
var testenv env.Environment

// b is the running OpenBao/Vault backend. Exposed to test files via a
// small getter so tests can seed policies and build admin clients.
var b *backend.Backend

// Backend returns the running backend. Callable from any test file.
func Backend() *backend.Backend { return b }

// TestMain owns the environment lifecycle. It runs BEFORE any test.
// A failure here fails the whole suite loudly rather than each test
// individually re-doing setup.
func TestMain(m *testing.M) {
	initLogging()

	// Long context — image pulls + Kind bootstrap can take a while on
	// cold caches (5-10 min). The individual test timeouts remain
	// tighter; this only bounds the outermost setup.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	if err := run(ctx, m); err != nil {
		klog.Errorf("e2e bootstrap failed: %v", err)
		os.Exit(1)
	}
}

// run centralises the exit-code plumbing so every failure path calls
// the same cleanup. Returns nil only when the test binary itself
// reports success.
func run(ctx context.Context, m *testing.M) error {
	if ok, reason := hasEnoughDockerMemory(ctx); !ok {
		return fmt.Errorf("Docker does not satisfy e2e requirements: %s", reason)
	}

	// 1. Use the standard Docker network used by Kind. Nested clusters created
	// by cluster-provider-kind also join this network, so the platform cluster can
	// reach their API servers and all clusters can reach the backend alias.
	const kindNetwork = "kind"
	if err := ensureDockerNetwork(ctx, kindNetwork); err != nil {
		return fmt.Errorf("ensure Docker network %q: %w", kindNetwork, err)
	}
	if err := os.Setenv("KIND_EXPERIMENTAL_DOCKER_NETWORK", kindNetwork); err != nil {
		return fmt.Errorf("set KIND_EXPERIMENTAL_DOCKER_NETWORK: %w", err)
	}
	defer os.Unsetenv("KIND_EXPERIMENTAL_DOCKER_NETWORK")

	// 2. Backend on the same network.
	backendCtx, backendCancel := context.WithTimeout(ctx, 3*time.Minute)
	defer backendCancel()
	var err error
	b, err = backend.Start(backendCtx, kindNetwork)
	if err != nil {
		return fmt.Errorf("start backend: %w", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		_ = b.Stop(stopCtx)
	}()
	klog.Infof("backend running: image=%s host=%s inCluster=%s", b.Image(), b.HostAddress(), b.InClusterAddress())

	// 3. Manager image — build locally if the caller didn't pre-set IMG.
	// A pre-set IMG (e.g. from CI's docker-build step) is trusted; local
	// builds happen inside TestMain so `go test` from a clean checkout
	// works with zero extra setup.
	managerImage := os.Getenv("IMG")
	if managerImage == "" {
		managerImage = "platform-service-openbao:e2e"
		klog.Infof("building manager image %s (set IMG to skip)", managerImage)
		if err := buildManagerImage(ctx, managerImage); err != nil {
			return fmt.Errorf("build manager image: %w", err)
		}
	} else {
		klog.Infof("using pre-built manager image %s", managerImage)
	}

	// 4. Render the PlatformServiceConfigsDir. The OpenBaoInstance URL
	// depends on the backend's in-cluster address, so we template it
	// into a tmp dir on every run.
	configsDir, err := renderConfigs(b)
	if err != nil {
		return fmt.Errorf("render configs: %w", err)
	}
	defer os.RemoveAll(configsDir)

	// 5. openmcp-testing setup. Same shape as the reference example in
	// openmcp-testing/e2e/main_test.go.
	openmcp := setup.OpenMCPSetup{
		Namespace: "openmcp-system",
		Operator: setup.OpenMCPOperatorSetup{
			Name: "openmcp-operator",
			// renovate: datasource=docker depName=ghcr.io/openmcp-project/images/openmcp-operator
			Image:        "ghcr.io/openmcp-project/images/openmcp-operator:v1.3.0",
			Environment:  "debug",
			PlatformName: "platform",
			WaitOpts:     []wait.Option{wait.WithTimeout(10 * time.Minute)},
		},
		ClusterProviders: []providers.ClusterProviderSetup{
			{
				Name: "kind",
				// renovate: datasource=docker depName=ghcr.io/openmcp-project/images/cluster-provider-kind
				Image:    "ghcr.io/openmcp-project/images/cluster-provider-kind:v0.6.0",
				WaitOpts: []wait.Option{wait.WithTimeout(10 * time.Minute)},
			},
		},
		PlatformServices: []platformservices.PlatformServiceSetup{
			{
				Name:                      "openbao",
				Image:                     managerImage,
				LoadImageToCluster:        true,
				PlatformServiceConfigsDir: configsDir,
				WaitOpts:                  []wait.Option{wait.WithTimeout(10 * time.Minute)},
			},
		},
		WaitOpts: []wait.Option{wait.WithTimeout(10 * time.Minute)},
	}
	testenv = env.NewWithConfig(envconf.New().WithNamespace(openmcp.Namespace))
	openmcp.Bootstrap(testenv)

	code := testenv.Run(m)
	if code != 0 {
		return fmt.Errorf("test binary exited with code %d", code)
	}
	return nil
}

// buildManagerImage runs `make docker-build IMG=<tag>` in the repo root.
// Blocking; runs synchronously so image is available before Kind loads
// it.
func hasEnoughDockerMemory(ctx context.Context) (bool, string) {
	if os.Getenv("E2E_SKIP_DOCKER_MEMORY_CHECK") == "true" {
		return true, ""
	}
	const defaultMinGiB = 6
	minGiB := defaultMinGiB
	if raw := os.Getenv("E2E_MIN_DOCKER_MEMORY_GIB"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return false, fmt.Sprintf("E2E_MIN_DOCKER_MEMORY_GIB=%q is not an integer", raw)
		}
		minGiB = parsed
	}
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.MemTotal}}").Output()
	if err != nil {
		return false, fmt.Sprintf("could not inspect Docker memory: %v", err)
	}
	bytes, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return false, fmt.Sprintf("could not parse Docker memory %q: %v", strings.TrimSpace(string(out)), err)
	}
	minBytes := int64(minGiB) * 1024 * 1024 * 1024
	if bytes < minBytes {
		return false, fmt.Sprintf("Docker has %.1fGiB RAM, need at least %dGiB for nested kind OpenMCP bootstrap (set E2E_SKIP_DOCKER_MEMORY_CHECK=true to force)", float64(bytes)/(1024*1024*1024), minGiB)
	}
	return true, ""
}

func ensureDockerNetwork(ctx context.Context, name string) error {
	inspect := exec.CommandContext(ctx, "docker", "network", "inspect", name)
	if err := inspect.Run(); err == nil {
		return nil
	}
	create := exec.CommandContext(ctx, "docker", "network", "create", name)
	create.Stdout = os.Stdout
	create.Stderr = os.Stderr
	return create.Run()
}

func buildManagerImage(ctx context.Context, tag string) error {
	repoRoot, err := findRepoRoot()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "make", "docker-build", "IMG="+tag)
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// findRepoRoot walks up from the current package's directory looking
// for the Makefile. Simpler than hard-coding the depth from test/e2e.
func findRepoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "Makefile")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("Makefile not found in any parent of %s", cwd)
		}
		dir = parent
	}
}

func initLogging() {
	klog.InitFlags(nil)
	if err := flag.Set("v", "2"); err != nil {
		panic(err)
	}
	flag.Parse()
}
