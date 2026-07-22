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
