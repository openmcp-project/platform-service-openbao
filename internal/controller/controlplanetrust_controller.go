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

package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	"github.com/openmcp-project/controller-utils/pkg/logging"

	openbaov1alpha1 "github.com/openmcp-project/platform-service-openbao/api/v1alpha1"
	"github.com/openmcp-project/platform-service-openbao/internal/openbao"
)

// +kubebuilder:rbac:groups=openbao.open-control-plane.io,resources=controlplanetrusts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=openbao.open-control-plane.io,resources=controlplanetrusts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=openbao.open-control-plane.io,resources=controlplanetrusts/finalizers,verbs=update

// ControlPlaneTrustReconciler owns the OpenBao JWT auth mount for one
// ControlPlane. It ensures the mount, writes issuer/JWKS trust config,
// and cleans up on delete.
type ControlPlaneTrustReconciler struct {
	PlatformCluster   *clusters.Cluster
	OnboardingCluster *clusters.Cluster
	ProviderName      string
	ClientFactory     OpenBaoClientFactory
}

// NewControlPlaneTrustReconciler builds a reconciler wired to both
// clusters with the default OpenBao client factory.
func NewControlPlaneTrustReconciler(platform, onboarding *clusters.Cluster, providerName string) *ControlPlaneTrustReconciler {
	return &ControlPlaneTrustReconciler{
		PlatformCluster:   platform,
		OnboardingCluster: onboarding,
		ProviderName:      providerName,
		ClientFactory:     newDefaultOpenBaoClientFactory(platform, providerName),
	}
}

// Reconcile flow:
//  1. Resolve ProjectEntity (onboarding cluster) → OpenBaoInstance name.
//  2. Resolve OpenBaoInstance (platform cluster) → reachable? → openbao client.
//  3. Compute deterministic auth-mount path.
//  4. On delete: unmount + remove finalizer.
//  5. Ensure JWT auth mount exists.
//  6. Discover issuer/JWKS from the referenced ControlPlane status.
//  7. ConfigureJWTTrust + set status + mark Ready.
func (r *ControlPlaneTrustReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logging.FromContextOrDiscard(ctx).WithName("controlplanetrust").WithValues("controlplanetrust", req.String())
	ctx = logging.NewContext(ctx, log)

	trust := &openbaov1alpha1.ControlPlaneTrust{}
	if err := r.OnboardingCluster.Client().Get(ctx, req.NamespacedName, trust); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching ControlPlaneTrust: %w", err)
	}

	cfg, err := getServiceConfig(ctx, r.PlatformCluster.Client(), r.ProviderName)
	if err != nil {
		return ctrl.Result{}, err
	}
	trust.Status.ObservedGeneration = trust.Generation

	// Handle deletion first. Cleanup is scoped: we only tear down the
	// specific auth mount owned by this ControlPlaneTrust; nothing else.
	if !trust.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, trust)
	}

	// Ensure finalizer is set before doing external work. Guarantees we
	// get a chance to clean up on delete.
	if controllerutil.AddFinalizer(trust, openbaov1alpha1.FinalizerControlPlaneTrust) {
		if err := r.OnboardingCluster.Client().Update(ctx, trust); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
	}

	// Resolve ProjectEntity on the onboarding cluster.
	pe := &openbaov1alpha1.ProjectEntity{}
	peKey := types.NamespacedName{Namespace: trust.Spec.ProjectEntityRef.Namespace, Name: trust.Spec.ProjectEntityRef.Name}
	if err := r.OnboardingCluster.Client().Get(ctx, peKey, pe); err != nil {
		if apierrors.IsNotFound(err) {
			dependencyNotReady(&trust.Status.Conditions, trust.Generation,
				openbaov1alpha1.ReasonDependencyNotFound,
				fmt.Sprintf("ProjectEntity %s/%s not found", peKey.Namespace, peKey.Name))
			return r.patchStatus(ctx, trust, cfg)
		}
		return ctrl.Result{}, fmt.Errorf("fetching ProjectEntity: %w", err)
	}

	// The ProjectEntity carries the OpenBaoInstance reference; use it.
	inst, err := getOpenBaoInstance(ctx, r.PlatformCluster.Client(), pe.Spec.OpenBaoInstanceRef.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if inst == nil {
		dependencyNotReady(&trust.Status.Conditions, trust.Generation,
			openbaov1alpha1.ReasonDependencyNotFound,
			fmt.Sprintf("OpenBaoInstance %q not found", pe.Spec.OpenBaoInstanceRef.Name))
		return r.patchStatus(ctx, trust, cfg)
	}
	trust.Status.ResolvedOpenBaoInstance = inst.Name
	if !isInstanceReachable(inst) {
		dependencyNotReady(&trust.Status.Conditions, trust.Generation,
			openbaov1alpha1.ReasonOpenBaoUnreachable,
			fmt.Sprintf("OpenBaoInstance %q is not reachable", inst.Name))
		return r.patchStatus(ctx, trust, cfg)
	}

	// Resolve the ControlPlane itself. The onboarding AccessRequest granted at
	// service startup includes read access to ControlPlane resources, while
	// target-cluster credentials are requested separately only where needed.
	cp, err := getControlPlane(ctx, r.OnboardingCluster.Client(), types.NamespacedName{Namespace: trust.Namespace, Name: trust.Spec.ControlPlaneRef.Name})
	if err != nil {
		return ctrl.Result{}, err
	}
	if cp == nil {
		dependencyNotReady(&trust.Status.Conditions, trust.Generation,
			openbaov1alpha1.ReasonDependencyNotFound,
			fmt.Sprintf("ControlPlane %q not found in namespace %q", trust.Spec.ControlPlaneRef.Name, trust.Namespace))
		return r.patchStatus(ctx, trust, cfg)
	}
	issuer := controlPlaneIssuer(cp)
	if issuer == "" {
		setCondition(&trust.Status.Conditions, trust.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionTrustConfigured,
			Status:  metav1.ConditionFalse,
			Reason:  openbaov1alpha1.ReasonIssuerDiscoveryFailed,
			Message: fmt.Sprintf("ControlPlane %s/%s does not expose service-account-issuer endpoint yet", cp.Namespace, cp.Name),
		})
		setCondition(&trust.Status.Conditions, trust.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionReady,
			Status:  metav1.ConditionFalse,
			Reason:  openbaov1alpha1.ReasonIssuerDiscoveryFailed,
			Message: "ControlPlane trust incomplete: issuer discovery pending",
		})
		return r.patchStatus(ctx, trust, cfg)
	}
	resolvedAudience := resolveControlPlaneAudience(trust.Spec.Audience)
	trust.Status.Issuer = issuer
	trust.Status.Audience = resolvedAudience

	// Deterministic mount path — stable across restarts and safe to
	// surface in status for ESO configuration.
	mountPath := openbao.AuthMountPath(
		resolveAuthMountPrefix(cfg.Spec, inst),
		trust.Namespace,
		trust.Spec.ControlPlaneRef.Name,
	)
	trust.Status.AuthMountPath = mountPath

	client, err := r.ClientFactory(ctx, inst)
	if err != nil {
		setCondition(&trust.Status.Conditions, trust.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionTrustConfigured,
			Status:  metav1.ConditionFalse,
			Reason:  openbaov1alpha1.ReasonOpenBaoUnreachable,
			Message: err.Error(),
		})
		setCondition(&trust.Status.Conditions, trust.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionReady,
			Status:  metav1.ConditionFalse,
			Reason:  openbaov1alpha1.ReasonOpenBaoUnreachable,
			Message: err.Error(),
		})
		return r.patchStatus(ctx, trust, cfg)
	}

	if err := client.EnsureJWTAuthMount(ctx, mountPath); err != nil {
		setCondition(&trust.Status.Conditions, trust.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionTrustConfigured,
			Status:  metav1.ConditionFalse,
			Reason:  openbaov1alpha1.ReasonReconcileError,
			Message: err.Error(),
		})
		return r.patchStatus(ctx, trust, cfg)
	}

	if err := client.ConfigureJWTTrust(ctx, mountPath, openbao.JWTAuthConfig{
		JWKSURL:     issuer + "/jwks",
		BoundIssuer: issuer,
	}); err != nil {
		setCondition(&trust.Status.Conditions, trust.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionTrustConfigured,
			Status:  metav1.ConditionFalse,
			Reason:  openbaov1alpha1.ReasonReconcileError,
			Message: err.Error(),
		})
		return r.patchStatus(ctx, trust, cfg)
	}
	setCondition(&trust.Status.Conditions, trust.Generation, metav1.Condition{
		Type:   openbaov1alpha1.ConditionTrustConfigured,
		Status: metav1.ConditionTrue,
		Reason: openbaov1alpha1.ReasonReconciled,
	})
	markReady(&trust.Status.Conditions, trust.Generation)
	return r.patchStatus(ctx, trust, cfg)
}

// reconcileDelete tears down only the auth mount owned by this trust and
// removes the finalizer. Anything else in OpenBao is left alone.
func (r *ControlPlaneTrustReconciler) reconcileDelete(ctx context.Context, trust *openbaov1alpha1.ControlPlaneTrust) (reconcile.Result, error) {
	// If the mount was never assigned, there's nothing external to unwind.
	if trust.Status.AuthMountPath != "" && trust.Status.ResolvedOpenBaoInstance != "" {
		inst, err := getOpenBaoInstance(ctx, r.PlatformCluster.Client(), trust.Status.ResolvedOpenBaoInstance)
		if err != nil {
			return ctrl.Result{}, err
		}
		if inst != nil && isInstanceReachable(inst) {
			client, err := r.ClientFactory(ctx, inst)
			if err == nil {
				if err := client.DeleteAuthMount(ctx, trust.Status.AuthMountPath); err != nil {
					return ctrl.Result{}, fmt.Errorf("deleting auth mount: %w", err)
				}
			}
		}
	}
	if controllerutil.RemoveFinalizer(trust, openbaov1alpha1.FinalizerControlPlaneTrust) {
		if err := r.OnboardingCluster.Client().Update(ctx, trust); err != nil {
			return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
		}
	}
	return ctrl.Result{}, nil
}

func (r *ControlPlaneTrustReconciler) patchStatus(ctx context.Context, trust *openbaov1alpha1.ControlPlaneTrust, cfg *openbaov1alpha1.ServiceConfig) (reconcile.Result, error) {
	if err := r.OnboardingCluster.Client().Status().Update(ctx, trust); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating ControlPlaneTrust status: %w", err)
	}
	return requeueResult(cfg), nil
}

// SetupWithManager registers the reconciler.
func (r *ControlPlaneTrustReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("controlplanetrust").
		For(&openbaov1alpha1.ControlPlaneTrust{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}
