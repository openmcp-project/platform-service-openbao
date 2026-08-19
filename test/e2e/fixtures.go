//go:build e2e
// +build e2e

// SPDX-FileCopyrightText: Copyright OpenControlPlane contributors.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"embed"

	"sigs.k8s.io/yaml"
)

// fixturesFS holds the tenant CR YAMLs applied by the assess steps.
// Kept in its own file so both configs.go and policybinding_test.go
// can share the embed directive cleanly.
//
//go:embed fixtures
var fixturesFS embed.FS

// yamlUnmarshal is a thin alias so the test file doesn't need to import
// sigs.k8s.io/yaml directly. Unstructured objects deserialise cleanly
// from YAML through JSON round-trip; sigs.k8s.io/yaml handles that.
func yamlUnmarshal(data []byte, obj any) error {
	return yaml.Unmarshal(data, obj)
}

// yamlMarshal serialises an object to YAML for diagnostic logs. Errors
// are surfaced so failed dumps don't hide themselves behind a nil.
func yamlMarshal(obj any) ([]byte, error) {
	return yaml.Marshal(obj)
}
