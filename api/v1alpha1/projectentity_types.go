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

// ProjectEntitySpec anchors a project-level OpenBao identity to an approved
// OpenBaoInstance. It does not itself grant access to any policy.
type ProjectEntitySpec struct {
	// openBaoInstanceRef selects the cluster-scoped OpenBaoInstance this
	// project anchor lives against.
	// +required
	OpenBaoInstanceRef LocalObjectReference `json:"openBaoInstanceRef"`
}

// ProjectEntityStatus reports resolved identity data for the anchor.
// Identifiers are safe to expose; no OpenBao tokens are stored here.
type ProjectEntityStatus struct {
	// observedGeneration is the .metadata.generation the controller last
	// reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// entityID is the canonical OpenBao entity ID, if the controller
	// materialised or observed one for this anchor.
	// +optional
	EntityID string `json:"entityID,omitempty"`

	// groupID is the canonical OpenBao group ID, if any.
	// +optional
	GroupID string `json:"groupID,omitempty"`

	// resolvedOpenBaoRef mirrors spec.openBaoRef.Name once the referenced
	// OpenBaoInstance is observed to exist.
	// +optional
	ResolvedOpenBaoRef string `json:"resolvedOpenBaoRef,omitempty"`

	// conditions describe the current state of the ProjectEntity. Types
	// used: "Ready", "DependencyReady".
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=pje
// +kubebuilder:metadata:labels="openmcp.cloud/cluster=onboarding"
// +kubebuilder:printcolumn:name="OpenBao",type=string,JSONPath=".spec.openBaoInstanceRef.name"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="EntityID",type=string,JSONPath=".status.entityID"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// ProjectEntity is the project-namespace anchor for a project-level OpenBao
// identity. It grants nothing on its own; PolicyBinding is where access is
// actually configured.
type ProjectEntity struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ProjectEntity
	// +required
	Spec ProjectEntitySpec `json:"spec"`

	// status defines the observed state of ProjectEntity
	// +optional
	Status ProjectEntityStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ProjectEntityList contains a list of ProjectEntity
type ProjectEntityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ProjectEntity `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ProjectEntity{}, &ProjectEntityList{})
}
