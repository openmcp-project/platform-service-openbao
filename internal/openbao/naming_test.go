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

package openbao

import (
	"regexp"
	"strings"
	"testing"
)

// Naming is a security- and UX-sensitive surface: users read the generated
// values from status to configure ESO SecretStore. These tests pin the
// invariants a reconciler relies on.

var validNameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func TestAuthMountPath_Deterministic(t *testing.T) {
	a := AuthMountPath("openbao", "project-team-a--ws-prod", "prod")
	b := AuthMountPath("openbao", "project-team-a--ws-prod", "prod")
	if a != b {
		t.Fatalf("expected deterministic output, got %q vs %q", a, b)
	}
}

func TestAuthMountPath_Sanitised(t *testing.T) {
	got := AuthMountPath("Open Bao!", "Project Team_A", "PROD/mcp")
	if !validNameRE.MatchString(got) {
		t.Fatalf("output %q does not match [a-z0-9-]+", got)
	}
	if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
		t.Fatalf("output %q has leading/trailing dash", got)
	}
}

func TestAuthMountPath_LengthBounded(t *testing.T) {
	got := AuthMountPath(
		"a-very-long-prefix-that-shouldnt-blow-past-the-limit",
		strings.Repeat("x", 200),
		strings.Repeat("y", 200),
	)
	if len(got) > authMountMax {
		t.Fatalf("output length %d exceeds max %d: %q", len(got), authMountMax, got)
	}
}

func TestAuthMountPath_CollisionResistant(t *testing.T) {
	// Two inputs whose sanitised head is identical after truncation must
	// still produce different outputs because the hash suffix is derived
	// from the full input.
	a := AuthMountPath("prefix", strings.Repeat("x", 200), "one")
	b := AuthMountPath("prefix", strings.Repeat("x", 200), "two")
	if a == b {
		t.Fatalf("expected different outputs for different tails: %q", a)
	}
}

func TestAuthMountPath_NoAuthPrefixLeak(t *testing.T) {
	got := AuthMountPath("openbao", "ns", "cp")
	if strings.HasPrefix(got, "auth/") {
		t.Fatalf("auth mount path must not embed the 'auth/' API prefix, got %q", got)
	}
}

func TestRoleName_Deterministic(t *testing.T) {
	a := RoleName("project-team-a--ws-prod", "eso-reader-kv-prod")
	b := RoleName("project-team-a--ws-prod", "eso-reader-kv-prod")
	if a != b {
		t.Fatalf("expected deterministic role name, got %q vs %q", a, b)
	}
}

func TestRoleName_DistinctPerBinding(t *testing.T) {
	// Two PolicyBindings in the same namespace with different names must
	// resolve to distinct role names — the design's "one JWT role per
	// PolicyBinding" contract depends on this.
	a := RoleName("ns", "read-dev")
	b := RoleName("ns", "read-prod")
	if a == b {
		t.Fatalf("distinct PolicyBindings collided on role name: %q", a)
	}
}

func TestRoleName_LengthBounded(t *testing.T) {
	got := RoleName(strings.Repeat("n", 100), strings.Repeat("b", 200))
	if len(got) > roleNameMax {
		t.Fatalf("role name length %d exceeds max %d: %q", len(got), roleNameMax, got)
	}
	if !validNameRE.MatchString(got) {
		t.Fatalf("role name %q not valid segment", got)
	}
}

func TestSanitize_EmptyIsSafe(t *testing.T) {
	// Purely-punctuation input should not panic and should not leak dashes
	// into the sanitised head; the hash suffix still gives a valid name.
	got := AuthMountPath("---", "!!!", "///")
	if got == "" {
		t.Fatalf("expected non-empty deterministic name even for empty sanitised head")
	}
	if !validNameRE.MatchString(got) {
		t.Fatalf("output %q not a valid segment", got)
	}
}
