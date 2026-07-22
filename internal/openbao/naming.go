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
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Deterministic naming for OpenBao objects owned by this service.
//
// Design constraints (see openspec/.../design.md §"Risks / Trade-offs" and
// tasks 3.3/3.4):
//   - Names must be stable given the same OpenMCP identity — reconciliation
//     is idempotent; the same PolicyBinding always resolves to the same
//     role name across restarts.
//   - Names must be sanitized: OpenBao mount and role names are used in
//     URL paths, so only [a-z0-9-] is safe.
//   - Names must be length-bounded and collision-resistant, hence the
//     hash suffix.
//   - The resulting name is surfaced in status so users don't reconstruct
//     it from controller internals.

const (
	// authMountMax is the compact upper bound the controller enforces on
	// generated auth-mount paths. OpenBao itself accepts longer names but
	// keeping paths short leaves headroom for `auth/<mount>/role/<name>`
	// URL segments.
	authMountMax = 60
	// roleNameMax is the same idea for role names.
	roleNameMax = 60
	// hashSuffixLen is the number of hex digits appended for collision
	// resistance. 10 hex digits = 40 bits, ample for local uniqueness.
	hashSuffixLen = 10
)

var sanitizeRE = regexp.MustCompile(`[^a-z0-9-]+`)

// sanitize normalises an arbitrary string into a lower-case [a-z0-9-]
// segment. Multiple runs of illegal chars collapse to a single "-";
// leading/trailing dashes are trimmed.
func sanitize(s string) string {
	s = strings.ToLower(s)
	s = sanitizeRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// truncateWithHash returns a stable, length-bounded name of the form
// "<truncated>-<hash>" where the hash is the first hashSuffixLen hex digits
// of sha256(full). Same input → same output.
func truncateWithHash(full string, maxLen int) string {
	sum := sha256.Sum256([]byte(full))
	suffix := hex.EncodeToString(sum[:])[:hashSuffixLen]
	// Reserve room for "-<suffix>".
	head := full
	room := maxLen - hashSuffixLen - 1
	if room < 1 {
		return suffix
	}
	if len(head) > room {
		head = head[:room]
	}
	head = strings.Trim(head, "-")
	if head == "" {
		return suffix
	}
	return head + "-" + suffix
}

// AuthMountPath returns the deterministic OpenBao JWT auth mount path
// owned by a ControlPlaneTrust. It intentionally embeds the workspace
// namespace and ControlPlane name so paths are stable and readable.
//
// The returned value has NO leading "auth/". Callers that need the full
// API path prefix "auth/" themselves.
func AuthMountPath(prefix, namespace, controlPlane string) string {
	head := sanitize(prefix)
	tail := sanitize(namespace + "-" + controlPlane)
	full := tail
	if head != "" {
		full = head + "-" + tail
	}
	return truncateWithHash(full, authMountMax)
}

// RoleName returns the deterministic OpenBao JWT role name owned by a
// PolicyBinding. Includes the binding namespace and name so distinct
// PolicyBindings referencing the same ControlPlaneEntity never collide.
func RoleName(namespace, policyBindingName string) string {
	full := sanitize(namespace + "-" + policyBindingName)
	return truncateWithHash(full, roleNameMax)
}
