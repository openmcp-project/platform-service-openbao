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

package e2e

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	obcrds "github.com/openmcp-project/platform-service-openbao/api/crds"
	"github.com/openmcp-project/platform-service-openbao/test/e2e/backend"
)

// PlatformServiceConfigsDir gets applied verbatim to the platform
// cluster by openmcp-testing, so:
//   - the tenant CRDs (ProjectEntity, ControlPlaneTrust, etc.) must be
//     installed there before the tests create CRs of those kinds. The
//     `init` subcommand normally does this, but the openmcp PlatformService
//     reconciler only runs the manager's `run` path — no `init`. So we
//     ship the CRDs in the configs dir with a `00-crd-` prefix so they
//     apply before anything else.
//   - the OpenBaoInstance URL depends on the running backend, so we
//     template that file at TestMain time.

//go:embed configs
var embeddedConfigs embed.FS

// renderConfigs materialises the embedded configs and the shipped CRDs
// into a fresh tmp dir with {{.OpenBaoAddress}} filled in.
//
// Returns the path to the tmp dir. Callers must os.RemoveAll it.
func renderConfigs(b *backend.Backend) (string, error) {
	dir, err := os.MkdirTemp("", "openbao-e2e-configs-*")
	if err != nil {
		return "", err
	}

	// 1. CRDs first — filenames prefixed with 00-crd- so kubectl-apply
	// ordering by filename installs schemas before CRs.
	if err := writeCRDs(dir); err != nil {
		os.RemoveAll(dir)
		return "", err
	}

	// 2. Config CRs.
	entries, err := embeddedConfigs.ReadDir("configs")
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}

	data := struct {
		OpenBaoAddress string
	}{
		OpenBaoAddress: b.InClusterAddress(),
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := embeddedConfigs.ReadFile("configs/" + e.Name())
		if err != nil {
			os.RemoveAll(dir)
			return "", err
		}
		// Prefix `10-` to guarantee CR files apply after the `00-crd-`
		// schema files.
		outName := "10-" + strings.TrimSuffix(e.Name(), ".tmpl")
		outPath := filepath.Join(dir, outName)

		if !strings.HasSuffix(e.Name(), ".tmpl") {
			if err := os.WriteFile(outPath, raw, 0o644); err != nil {
				os.RemoveAll(dir)
				return "", err
			}
			continue
		}

		tmpl, err := template.New(e.Name()).Parse(string(raw))
		if err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		var out bytes.Buffer
		if err := tmpl.Execute(&out, data); err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("execute %s: %w", e.Name(), err)
		}
		if err := os.WriteFile(outPath, out.Bytes(), 0o644); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

// writeCRDs copies every CRD from the shared api/crds embed into `dir`
// with a `00-crd-` prefix. The embed is the same one the manager's
// `init` subcommand uses in production, so what we install in tests is
// bit-identical to what a real deployment installs.
func writeCRDs(dir string) error {
	entries, err := obcrds.CRDFS.ReadDir("manifests")
	if err != nil {
		return fmt.Errorf("read embedded manifests: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := obcrds.CRDFS.ReadFile("manifests/" + e.Name())
		if err != nil {
			return fmt.Errorf("read %s: %w", e.Name(), err)
		}
		outPath := filepath.Join(dir, "00-crd-"+e.Name())
		if err := os.WriteFile(outPath, raw, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", outPath, err)
		}
	}
	return nil
}
