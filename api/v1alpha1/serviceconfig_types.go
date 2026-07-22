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
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ServiceConfigSpec holds the runtime configuration for a single
// platform-service-openbao instance. The controller looks up ONE
// ServiceConfig by --provider-name on the platform cluster at startup and
// re-fetches it on every reconcile.
//
// Naming: platform-service-quota calls the analogous type
// `QuotaServiceConfig`. This project opts for the shorter `ServiceConfig`
// (the group qualifier already scopes it to the OpenBao service).
type ServiceConfigSpec struct {
	// authMountPrefix is prepended to deterministic per-ControlPlane auth
	// mount paths. Trailing slashes are stripped. Empty means default.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	AuthMountPrefix string `json:"authMountPrefix,omitempty"`

	// requeueAfter is the default requeue interval for successful
	// reconciles. Individual reconcilers may pick shorter intervals when
	// observing a dependency come ready. Go duration syntax, e.g. "5m".
	// +kubebuilder:validation:Pattern=`^([0-9]+(ns|us|µs|ms|s|m|h))+$`
	// +optional
	RequeueAfter string `json:"requeueAfter,omitempty"`

	// platformCredentialRef optionally points at a Secret in the manager's
	// namespace holding the OpenBao token or credential the platform
	// controller uses to configure trust. Never surfaced in tenant status.
	// +optional
	PlatformCredentialRef *LocalSecretKeyRef `json:"platformCredentialRef,omitempty"`
}

// Validate checks the spec for internally-inconsistent values that the
// CRD schema cannot express. Called by the run subcommand at startup and
// by every reconcile that reloads the config.
func (s *ServiceConfigSpec) Validate() error {
	if s.RequeueAfter != "" {
		if _, err := time.ParseDuration(s.RequeueAfter); err != nil {
			return fmt.Errorf("requeueAfter %q is not a valid duration: %w", s.RequeueAfter, err)
		}
	}
	if s.PlatformCredentialRef != nil {
		if s.PlatformCredentialRef.Name == "" || s.PlatformCredentialRef.Key == "" {
			return fmt.Errorf("platformCredentialRef.name and .key must both be set when platformCredentialRef is present")
		}
	}
	return nil
}

// ServiceConfigStatus reports whether the loaded configuration is valid.
type ServiceConfigStatus struct {
	// observedGeneration is the .metadata.generation the controller last
	// reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions describe the current state of the ServiceConfig. Types
	// used: "Ready".
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=obsc
// +kubebuilder:metadata:labels="openmcp.cloud/cluster=platform"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// ServiceConfig configures a running platform-service-openbao instance.
// Installed on the platform cluster. One instance per running controller,
// matched by --provider-name.
type ServiceConfig struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ServiceConfig
	// +required
	Spec ServiceConfigSpec `json:"spec"`

	// status defines the observed state of ServiceConfig
	// +optional
	Status ServiceConfigStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ServiceConfigList contains a list of ServiceConfig
type ServiceConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ServiceConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ServiceConfig{}, &ServiceConfigList{})
}
