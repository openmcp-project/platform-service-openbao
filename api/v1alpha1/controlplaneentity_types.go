// SPDX-FileCopyrightText: Copyright OpenControlPlane contributors.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ControlPlaneEntitySpec references an existing ServiceAccount inside the
// target ControlPlane. The system does NOT create or manage that
// ServiceAccount.
type ControlPlaneEntitySpec struct {
	// controlPlaneTrustRef selects the ControlPlaneTrust in the same namespace
	// as this ControlPlaneEntity. The trust identifies the target ControlPlane,
	// OpenBaoInstance, and JWT audience context for this identity.
	// +required
	ControlPlaneTrustRef LocalObjectReference `json:"controlPlaneTrustRef"`

	// serviceAccountRef is the existing ServiceAccount inside the target
	// ControlPlane by namespace/name. Its lifecycle is external to this
	// service.
	// +required
	ServiceAccountRef NamespacedObjectReference `json:"serviceAccountRef"`
}

// ServiceAccountIdentity is a non-sensitive summary of the identity claims
// derived from a short-lived ServiceAccount JWT. It is safe to expose in
// status; it never includes the JWT itself.
type ServiceAccountIdentity struct {
	// alias is the stable OpenMCP/OpenBao identity alias for this ServiceAccount,
	// formatted as ocp:<project>:<workspace>:<cp-name>:<sa-namespace>:<sa-name>.
	// +optional
	Alias string `json:"alias,omitempty"`

	// subject is the JWT `sub` claim.
	// +optional
	Subject string `json:"subject,omitempty"`

	// issuer is the JWT `iss` claim.
	// +optional
	Issuer string `json:"issuer,omitempty"`

	// audiences are the JWT `aud` claim values.
	// +optional
	Audiences []string `json:"audiences,omitempty"`
}

// ControlPlaneEntityStatus surfaces resolved SA identity data.
type ControlPlaneEntityStatus struct {
	// observedGeneration is the .metadata.generation the controller last
	// reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// identity summarises the resolved ServiceAccount identity (sub, iss,
	// aud). Never contains the JWT itself.
	// +optional
	Identity *ServiceAccountIdentity `json:"identity,omitempty"`

	// identityID is a deterministic identifier derived from the resolved
	// identity claims. Useful for audit/troubleshooting.
	// +optional
	IdentityID string `json:"identityID,omitempty"`

	// conditions describe the current state of the ControlPlaneEntity.
	// Types used: "Ready", "IdentityResolved", "DependencyReady".
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cpe
// +kubebuilder:metadata:labels="openmcp.cloud/cluster=onboarding"
// +kubebuilder:printcolumn:name="Trust",type=string,JSONPath=".spec.controlPlaneTrustRef.name"
// +kubebuilder:printcolumn:name="ServiceAccount",type=string,JSONPath=".spec.serviceAccountRef.name"
// +kubebuilder:printcolumn:name="Identity",type=string,JSONPath=".status.conditions[?(@.type=='IdentityResolved')].status"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// ControlPlaneEntity references an existing ServiceAccount inside a target
// ControlPlane. Referenced only; never created by this controller.
type ControlPlaneEntity struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ControlPlaneEntity
	// +required
	Spec ControlPlaneEntitySpec `json:"spec"`

	// status defines the observed state of ControlPlaneEntity
	// +optional
	Status ControlPlaneEntityStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ControlPlaneEntityList contains a list of ControlPlaneEntity
type ControlPlaneEntityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ControlPlaneEntity `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ControlPlaneEntity{}, &ControlPlaneEntityList{})
}
