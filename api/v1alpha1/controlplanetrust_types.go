// SPDX-FileCopyrightText: Copyright OpenControlPlane contributors.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ControlPlaneTrustSpec configures the OpenBao JWT auth mount + trust for a
// specific ControlPlane. This is the single owner of the auth mount for
// that ControlPlane; PolicyBindings share it.
type ControlPlaneTrustSpec struct {
	// projectEntityRef selects the ProjectEntity this trust belongs under.
	// The ProjectEntity determines which OpenBaoInstance is used.
	// +required
	ProjectEntityRef NamespacedObjectReference `json:"projectEntityRef"`

	// controlPlaneRef selects the target ControlPlane in the same namespace
	// as this ControlPlaneTrust. The controller uses an OpenMCP
	// AccessRequest to obtain access to that ControlPlane's API server for
	// issuer/JWKS discovery.
	// +required
	ControlPlaneRef LocalObjectReference `json:"controlPlaneRef"`

	// audience is the JWT audience to require on ControlPlane
	// ServiceAccount tokens presented to OpenBao.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:default="open-control-plane-platform-service-openbao"
	// +optional
	Audience string `json:"audience,omitempty"`
}

// ControlPlaneTrustStatus is user-facing: ESO SecretStore configuration
// consumes authMountPath from here.
type ControlPlaneTrustStatus struct {
	// observedGeneration is the .metadata.generation the controller last
	// reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// authMountPath is the OpenBao JWT auth mount path (without leading
	// "auth/") owned by this ControlPlaneTrust. Stable across reconciles
	// once assigned. This is the value users put into ESO SecretStore
	// configuration.
	// +optional
	AuthMountPath string `json:"authMountPath,omitempty"`

	// issuer is the discovered OIDC/JWT issuer URL of the referenced
	// ControlPlane, as configured on the auth mount.
	// +optional
	Issuer string `json:"issuer,omitempty"`

	// audience is the audience configured on the JWT auth mount. Mirrors
	// spec.audience once resolved.
	// +optional
	Audience string `json:"audience,omitempty"`

	// resolvedOpenBaoInstance is the OpenBaoInstance name inherited via
	// the ProjectEntity.
	// +optional
	ResolvedOpenBaoInstance string `json:"resolvedOpenBaoInstance,omitempty"`

	// conditions describe the current state of the ControlPlaneTrust.
	// Types used: "Ready", "DependencyReady", "TrustConfigured",
	// "OpenBaoReachable".
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cpt
// +kubebuilder:metadata:labels="openmcp.cloud/cluster=onboarding"
// +kubebuilder:printcolumn:name="ControlPlane",type=string,JSONPath=".spec.controlPlaneRef.name"
// +kubebuilder:printcolumn:name="AuthMount",type=string,JSONPath=".status.authMountPath"
// +kubebuilder:printcolumn:name="Trust",type=string,JSONPath=".status.conditions[?(@.type=='TrustConfigured')].status"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// ControlPlaneTrust configures OpenBao JWT auth mount + issuer/JWKS trust
// for a ControlPlane so its ServiceAccount tokens can log in to OpenBao.
type ControlPlaneTrust struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ControlPlaneTrust
	// +required
	Spec ControlPlaneTrustSpec `json:"spec"`

	// status defines the observed state of ControlPlaneTrust
	// +optional
	Status ControlPlaneTrustStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ControlPlaneTrustList contains a list of ControlPlaneTrust
type ControlPlaneTrustList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ControlPlaneTrust `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ControlPlaneTrust{}, &ControlPlaneTrustList{})
}
