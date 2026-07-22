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

// OpenBaoInstanceSpec is the cluster-scoped registration of an approved
// OpenBao backend. Tenant resources reference an OpenBaoInstance by name
// rather than supplying arbitrary URLs.
type OpenBaoInstanceSpec struct {
	// address is the OpenBao API base URL (e.g. https://openbao.example.com).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^https?://.+`
	// +required
	Address string `json:"address"`

	// caBundleRef optionally references a Secret or ConfigMap in the
	// controller's namespace holding the PEM-encoded CA bundle used to
	// verify OpenBao's server certificate. When unset the system trust
	// store is used.
	// +optional
	CABundleRef *LocalSecretKeyRef `json:"caBundleRef,omitempty"`

	// insecureSkipVerify disables TLS verification for this backend.
	// Intended for local development only; controllers SHOULD log a warning
	// when this is true.
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`

	// namespace is the OpenBao API namespace to scope operations to (for
	// OpenBao deployments that use namespaces). Empty means root.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// authMountPrefix overrides the platform-wide default from
	// ServiceConfig for auth-mount paths generated against this instance.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	AuthMountPrefix string `json:"authMountPrefix,omitempty"`
}

// OpenBaoInstanceStatus reports reachability and discovered capabilities of
// the registered backend. It never contains tokens or connection secrets.
type OpenBaoInstanceStatus struct {
	// observedGeneration is the .metadata.generation the controller last
	// reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// version is the reported OpenBao server version, if discovered.
	// +optional
	Version string `json:"version,omitempty"`

	// initialized reflects the /sys/health `initialized` flag from the
	// last successful probe.
	// +optional
	Initialized *bool `json:"initialized,omitempty"`

	// sealed reflects the /sys/health `sealed` flag from the last
	// successful probe. A sealed backend is unreachable for reconciliation
	// purposes.
	// +optional
	Sealed *bool `json:"sealed,omitempty"`

	// lastProbeTime is the timestamp of the most recent reachability probe.
	// +optional
	LastProbeTime *metav1.Time `json:"lastProbeTime,omitempty"`

	// conditions describe the current state of the OpenBaoInstance. Types
	// used: "Ready", "OpenBaoReachable".
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=obi
// +kubebuilder:metadata:labels="openmcp.cloud/cluster=platform"
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=".spec.address"
// +kubebuilder:printcolumn:name="Reachable",type=string,JSONPath=".status.conditions[?(@.type=='OpenBaoReachable')].status"
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=".status.version"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// OpenBaoInstance is a cluster-scoped registration of an approved OpenBao
// backend. Tenant resources reference it by name.
type OpenBaoInstance struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of OpenBaoInstance
	// +required
	Spec OpenBaoInstanceSpec `json:"spec"`

	// status defines the observed state of OpenBaoInstance
	// +optional
	Status OpenBaoInstanceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// OpenBaoInstanceList contains a list of OpenBaoInstance
type OpenBaoInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []OpenBaoInstance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OpenBaoInstance{}, &OpenBaoInstanceList{})
}
