// SPDX-FileCopyrightText: Copyright OpenControlPlane contributors.
// SPDX-License-Identifier: Apache-2.0

// Package install registers Go types into runtime schemes for the two
// cluster contexts this platform service touches (platform + onboarding),
// plus a CRD-context scheme used by the init subcommand.
//
// Structure mirrors platform-service-quota's api/install package so the
// bootstrap flow in cmd/.../app matches the wider OpenMCP contract.
package install

import (
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	corev2alpha1 "github.com/openmcp-project/openmcp-operator/api/core/v2alpha1"

	openbaov1alpha1 "github.com/openmcp-project/platform-service-openbao/api/v1alpha1"
)

// InstallCRDAPIs registers just what the init subcommand needs to apply
// CRDs on either cluster: core client-go types and apiextensions/v1.
func InstallCRDAPIs(scheme *runtime.Scheme) *runtime.Scheme {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(apiextv1.AddToScheme(scheme))
	return scheme
}

// InstallOperatorAPIsPlatform registers the schemes used against the
// platform cluster: core types, openmcp-operator clusters (AccessRequest
// live here), and our own API group so the run subcommand can Get the
// ServiceConfig and List OpenBaoInstances.
func InstallOperatorAPIsPlatform(scheme *runtime.Scheme) *runtime.Scheme {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clustersv1alpha1.AddToScheme(scheme))
	utilruntime.Must(openbaov1alpha1.AddToScheme(scheme))
	return scheme
}

// InstallOperatorAPIsOnboarding registers the schemes used against the
// onboarding cluster: core types plus our tenant-facing CRDs
// (ProjectEntity, ControlPlaneTrust, ControlPlaneEntity, PolicyBinding).
// The tenant CRDs are all in the same v1alpha1 package as the platform
// ones — the scheme registration is by group, so a single AddToScheme
// covers both platform- and onboarding-cluster kinds. That is fine
// because the CRDManager decides which cluster each CRD is installed on
// via the openmcp.cloud/cluster label, and reconcilers make an explicit
// choice of cluster client per resource type.
func InstallOperatorAPIsOnboarding(scheme *runtime.Scheme) *runtime.Scheme {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(corev2alpha1.AddToScheme(scheme))
	utilruntime.Must(openbaov1alpha1.AddToScheme(scheme))
	return scheme
}
