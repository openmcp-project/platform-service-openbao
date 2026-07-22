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

package backend_test

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/openmcp-project/platform-service-openbao/test/e2e/backend"
)

// TestBackend_Roundtrip is a standalone smoke test for the container
// harness. If this fails, the full e2e suite has no chance — running it
// first surfaces container/Docker/image problems without the extra 5+
// minutes of Kind bootstrap.
func ensureNetwork(ctx context.Context, name string) error {
	if err := exec.CommandContext(ctx, "docker", "network", "inspect", name).Run(); err == nil {
		return nil
	}
	return exec.CommandContext(ctx, "docker", "network", "create", name).Run()
}

func TestBackend_Roundtrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	const networkName = "kind"
	if err := ensureNetwork(ctx, networkName); err != nil {
		t.Fatalf("ensure docker network: %v", err)
	}

	b, err := backend.Start(ctx, networkName)
	if err != nil {
		t.Fatalf("start backend: %v", err)
	}
	t.Cleanup(func() { _ = b.Stop(context.Background()) })

	// Seeding a policy proves the admin client + policy write path work
	// end-to-end. It's also the exact call the full e2e depends on.
	if err := b.SeedPolicy(ctx, "smoke-test", `path "kv/*" { capabilities = ["read"] }`); err != nil {
		t.Fatalf("seed policy: %v", err)
	}

	// Read it back to confirm the policy landed. Uses the same client
	// path production code uses (openbao/api/v2 Logical.Read).
	c, err := b.AdminClient()
	if err != nil {
		t.Fatalf("build admin client: %v", err)
	}
	sec, err := c.Logical().ReadWithContext(ctx, "sys/policies/acl/smoke-test")
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	if sec == nil || sec.Data["name"] != "smoke-test" {
		t.Fatalf("unexpected policy read result: %+v", sec)
	}
}
