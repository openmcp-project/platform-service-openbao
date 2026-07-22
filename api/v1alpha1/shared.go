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

package v1alpha1

// Shared reference types and condition-type constants used across the six
// CRDs in this API group. Kept in one file so field shapes stay consistent.

// LocalObjectReference references an object in the same namespace as the
// referrer, or (when the referrer is namespaced and the target is
// cluster-scoped) any cluster-scoped object of the expected kind.
type LocalObjectReference struct {
	// name is the name of the referenced object.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name"`
}

// NamespacedObjectReference references an object by namespace and name.
type NamespacedObjectReference struct {
	// name is the name of the referenced object.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name"`
	// namespace is the namespace of the referenced object.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Namespace string `json:"namespace"`
}

// LocalSecretKeyRef references a key within a Secret in the same namespace.
type LocalSecretKeyRef struct {
	// name is the Secret name.
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name"`
	// key is the key within the Secret's data.
	// +kubebuilder:validation:MinLength=1
	// +required
	Key string `json:"key"`
}

// Shared condition types. Each CRD advertises `Ready` plus zero or more of
// the domain-specific types below; unused types are simply not set. A
// String() form is exposed so controllers do not repeat the string literal.
const (
	// ConditionReady is the top-level readiness of a resource. When True the
	// resource is fully reconciled per its own contract; when False or
	// Unknown the reason/message names the blocking sub-condition.
	ConditionReady = "Ready"

	// ConditionOpenBaoReachable indicates whether the resource can reach its
	// resolved OpenBaoInstance. Reported by OpenBaoInstance and propagated as
	// a reason on dependent resources.
	ConditionOpenBaoReachable = "OpenBaoReachable"

	// ConditionTrustConfigured indicates that a ControlPlaneTrust has
	// applied the JWT auth mount and configured issuer/JWKS trust for the
	// referenced ControlPlane.
	ConditionTrustConfigured = "TrustConfigured"

	// ConditionIdentityResolved indicates that a ControlPlaneEntity has
	// resolved the referenced existing ServiceAccount and derived stable
	// identity claims. False with reason=ServiceAccountNotFound is the
	// documented missing-SA signal.
	ConditionIdentityResolved = "IdentityResolved"

	// ConditionPolicyResolved indicates whether the OpenBao policy named by
	// a PolicyBinding exists. True/False/Unknown; the Unknown case is used
	// when the controller lacks permission to check.
	ConditionPolicyResolved = "PolicyResolved"

	// ConditionDependencyReady indicates that all upstream references are
	// present, resolvable, and themselves Ready.
	ConditionDependencyReady = "DependencyReady"
)

// Standard reason strings. Kept short (PascalCase, no spaces) per K8s API
// conventions so they can be selected on and grouped in tooling.
const (
	ReasonReconciled              = "Reconciled"
	ReasonReconciling             = "Reconciling"
	ReasonReconcileError          = "ReconcileError"
	ReasonDependencyNotFound      = "DependencyNotFound"
	ReasonDependencyNotReady      = "DependencyNotReady"
	ReasonOpenBaoUnreachable      = "OpenBaoUnreachable"
	ReasonIssuerDiscoveryFailed   = "IssuerDiscoveryFailed"
	ReasonServiceAccountNotFound  = "ServiceAccountNotFound"
	ReasonControlPlaneUnavailable = "ControlPlaneUnavailable"
	ReasonPolicyNotFound          = "PolicyNotFound"
	ReasonPolicyExists            = "PolicyExists"
	ReasonPolicyCheckSkipped      = "PolicyCheckSkipped"
	ReasonWaitingForPolicy        = "WaitingForPolicy"
	ReasonCleanupPending          = "CleanupPending"
)

// Finalizer names used by the six controllers. Kept unique per kind so
// finalizer cleanup can be reasoned about independently.
const (
	FinalizerOpenBaoInstance   = "openbao.open-control-plane.io/openbaoinstance"
	FinalizerProjectEntity     = "openbao.open-control-plane.io/projectentity"
	FinalizerControlPlaneTrust = "openbao.open-control-plane.io/controlplanetrust"
	FinalizerControlPlaneEnt   = "openbao.open-control-plane.io/controlplaneentity"
	FinalizerPolicyBinding     = "openbao.open-control-plane.io/policybinding"
)
