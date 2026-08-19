// SPDX-FileCopyrightText: Copyright OpenControlPlane contributors.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PolicyExistence enumerates the observed existence state of the named
// OpenBao policy. "Unknown" is used when the controller cannot check.
// +kubebuilder:validation:Enum=True;False;Unknown
type PolicyExistence string

const (
	PolicyExistenceTrue    PolicyExistence = "True"
	PolicyExistenceFalse   PolicyExistence = "False"
	PolicyExistenceUnknown PolicyExistence = "Unknown"
)

// PolicyBindingSpec binds one user-managed OpenBao policy to one or more
// ControlPlaneEntity identities. The controller creates one OpenBao JWT role
// per listed ControlPlaneEntity so each ServiceAccount remains independently
// constrained by subject and audience.
type PolicyBindingSpec struct {
	// policyName is the free-form name of a user-managed OpenBao policy.
	// The controller does not create, modify, or delete this policy; it
	// only names it on generated JWT roles. Missing policies surface as
	// a PolicyResolved=False status condition, not as admission rejection.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	PolicyName string `json:"policyName"`

	// controlPlaneEntityRefs selects ControlPlaneEntity resources in the same
	// namespace as the PolicyBinding. The binding owns one generated OpenBao JWT
	// role per referenced entity.
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	// +required
	ControlPlaneEntityRefs []LocalObjectReference `json:"controlPlaneEntityRefs"`

	// ttl overrides the JWT roles' default token TTL. Empty means the
	// OpenBao default.
	// +kubebuilder:validation:Pattern=`^([0-9]+(ns|us|µs|ms|s|m|h))+$`
	// +optional
	TTL string `json:"ttl,omitempty"`

	// maxTTL overrides the JWT roles' maximum token TTL. Empty means the
	// OpenBao default.
	// +kubebuilder:validation:Pattern=`^([0-9]+(ns|us|µs|ms|s|m|h))+$`
	// +optional
	MaxTTL string `json:"maxTTL,omitempty"`
}

// PolicyBindingRoleStatus describes the generated role for one referenced
// ControlPlaneEntity.
type PolicyBindingRoleStatus struct {
	// controlPlaneEntityRef identifies the entity this role belongs to.
	// +required
	ControlPlaneEntityRef LocalObjectReference `json:"controlPlaneEntityRef"`

	// roleName is the generated OpenBao JWT role name. Deterministic and stable
	// across reconciles.
	// +optional
	RoleName string `json:"roleName,omitempty"`

	// authMountPath is the OpenBao JWT auth mount path this role lives under.
	// +optional
	AuthMountPath string `json:"authMountPath,omitempty"`

	// ready indicates whether this role was successfully reconciled.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// message contains an actionable error for this role when ready is false.
	// +optional
	Message string `json:"message,omitempty"`
}

// PolicyBindingStatus is user-facing: users read roles[].roleName,
// roles[].authMountPath, and policyExists here to configure ESO SecretStore or
// another OpenBao JWT-auth client.
type PolicyBindingStatus struct {
	// observedGeneration is the .metadata.generation the controller last
	// reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// roles reports one generated OpenBao JWT role per referenced
	// ControlPlaneEntity.
	// +listType=atomic
	// +optional
	Roles []PolicyBindingRoleStatus `json:"roles,omitempty"`

	// policyName mirrors spec.policyName once resolved.
	// +optional
	PolicyName string `json:"policyName,omitempty"`

	// policyExists reflects the last observed existence check for
	// spec.policyName in the resolved OpenBaoInstance.
	// +optional
	PolicyExists PolicyExistence `json:"policyExists,omitempty"`

	// conditions describe the current state of the PolicyBinding. Types
	// used: "Ready", "DependencyReady", "PolicyResolved".
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=pb
// +kubebuilder:metadata:labels="openmcp.cloud/cluster=onboarding"
// +kubebuilder:printcolumn:name="Policy",type=string,JSONPath=".spec.policyName"
// +kubebuilder:printcolumn:name="Roles",type=string,JSONPath=".status.roles[*].roleName"
// +kubebuilder:printcolumn:name="PolicyExists",type=string,JSONPath=".status.policyExists"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// PolicyBinding owns exactly one OpenBao JWT role that binds a
// ControlPlaneEntity's ServiceAccount identity to one user-managed OpenBao
// policy name.
type PolicyBinding struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of PolicyBinding
	// +required
	Spec PolicyBindingSpec `json:"spec"`

	// status defines the observed state of PolicyBinding
	// +optional
	Status PolicyBindingStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// PolicyBindingList contains a list of PolicyBinding
type PolicyBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []PolicyBinding `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PolicyBinding{}, &PolicyBindingList{})
}
