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

// PolicyBindingSpec binds one ControlPlaneEntity to one user-managed
// OpenBao policy by name. Exactly one OpenBao JWT role is created per
// PolicyBinding.
type PolicyBindingSpec struct {
	// controlPlaneEntityRef selects the ControlPlaneEntity whose
	// ServiceAccount identity this binding grants access to. Must live in
	// the same namespace as the PolicyBinding.
	// +required
	ControlPlaneEntityRef LocalObjectReference `json:"controlPlaneEntityRef"`

	// policyName is the free-form name of a user-managed OpenBao policy.
	// The controller does not create, modify, or delete this policy; it
	// only names it on the generated JWT role. Missing policies surface as
	// a PolicyResolved=False status condition, not as admission rejection.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	PolicyName string `json:"policyName"`

	// ttl overrides the JWT role's default token TTL. Empty means the
	// OpenBao default.
	// +kubebuilder:validation:Pattern=`^([0-9]+(ns|us|µs|ms|s|m|h))+$`
	// +optional
	TTL string `json:"ttl,omitempty"`

	// maxTTL overrides the JWT role's maximum token TTL. Empty means the
	// OpenBao default.
	// +kubebuilder:validation:Pattern=`^([0-9]+(ns|us|µs|ms|s|m|h))+$`
	// +optional
	MaxTTL string `json:"maxTTL,omitempty"`
}

// PolicyBindingStatus is user-facing: users read roleName, authMountPath,
// and policyExists here to configure ESO SecretStore or another OpenBao
// JWT-auth client.
type PolicyBindingStatus struct {
	// observedGeneration is the .metadata.generation the controller last
	// reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// roleName is the generated OpenBao JWT role name owned by this
	// binding. Deterministic and stable across reconciles.
	// +optional
	RoleName string `json:"roleName,omitempty"`

	// authMountPath is the OpenBao JWT auth mount path this binding's role
	// lives under. Inherited from the resolved ControlPlaneTrust.
	// +optional
	AuthMountPath string `json:"authMountPath,omitempty"`

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
// +kubebuilder:printcolumn:name="Entity",type=string,JSONPath=".spec.controlPlaneEntityRef.name"
// +kubebuilder:printcolumn:name="Policy",type=string,JSONPath=".spec.policyName"
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=".status.roleName"
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
