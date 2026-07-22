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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	"github.com/openmcp-project/controller-utils/pkg/logging"

	openbaov1alpha1 "github.com/openmcp-project/platform-service-openbao/api/v1alpha1"
)

// +kubebuilder:rbac:groups=openbao.open-control-plane.io,resources=controlplaneentities,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=openbao.open-control-plane.io,resources=controlplaneentities/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=openbao.open-control-plane.io,resources=controlplaneentities/finalizers,verbs=update

// ControlPlaneEntityReconciler resolves the existence and identity of an
// existing ServiceAccount inside a tenant ControlPlane. It never creates
// or modifies the ServiceAccount.
type ControlPlaneEntityReconciler struct {
	PlatformCluster   *clusters.Cluster
	OnboardingCluster *clusters.Cluster
	ProviderName      string
}

// NewControlPlaneEntityReconciler builds a reconciler wired to both
// clusters.
func NewControlPlaneEntityReconciler(platform, onboarding *clusters.Cluster, providerName string) *ControlPlaneEntityReconciler {
	return &ControlPlaneEntityReconciler{
		PlatformCluster:   platform,
		OnboardingCluster: onboarding,
		ProviderName:      providerName,
	}
}

// Reconcile validates the existence of the referenced ServiceAccount and
// publishes a non-sensitive identity summary. Verifying the SA requires
// AccessRequest-based access to the target ControlPlane — that piece is
// pending, so this reconciler reports IdentityResolved=False with a
// specific reason instead of falsely claiming success.
func (r *ControlPlaneEntityReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logging.FromContextOrDiscard(ctx).WithName("controlplaneentity").WithValues("controlplaneentity", req.NamespacedName.String())
	ctx = logging.NewContext(ctx, log)

	ce := &openbaov1alpha1.ControlPlaneEntity{}
	if err := r.OnboardingCluster.Client().Get(ctx, req.NamespacedName, ce); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching ControlPlaneEntity: %w", err)
	}
	if !ce.DeletionTimestamp.IsZero() {
		// No external objects owned; SA lifecycle is external. Nothing
		// to clean up.
		return ctrl.Result{}, nil
	}

	cfg, err := getServiceConfig(ctx, r.PlatformCluster.Client(), r.ProviderName)
	if err != nil {
		return ctrl.Result{}, err
	}
	ce.Status.ObservedGeneration = ce.Generation

	// ServiceAccount existence check requires an AccessRequest-driven
	// client to the target ControlPlane. Report the specific missing
	// dependency so operators see the exact blocker.
	setCondition(&ce.Status.Conditions, ce.Generation, metav1.Condition{
		Type:    openbaov1alpha1.ConditionIdentityResolved,
		Status:  metav1.ConditionFalse,
		Reason:  openbaov1alpha1.ReasonControlPlaneUnavailable,
		Message: "ControlPlane access via AccessRequest is not yet wired; cannot verify ServiceAccount existence",
	})
	setCondition(&ce.Status.Conditions, ce.Generation, metav1.Condition{
		Type:    openbaov1alpha1.ConditionReady,
		Status:  metav1.ConditionFalse,
		Reason:  openbaov1alpha1.ReasonControlPlaneUnavailable,
		Message: "Waiting for ControlPlane access",
	})
	return r.patchStatus(ctx, ce, cfg)
}

func (r *ControlPlaneEntityReconciler) patchStatus(ctx context.Context, ce *openbaov1alpha1.ControlPlaneEntity, cfg *openbaov1alpha1.ServiceConfig) (reconcile.Result, error) {
	if err := r.OnboardingCluster.Client().Status().Update(ctx, ce); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating ControlPlaneEntity status: %w", err)
	}
	return requeueResult(cfg), nil
}

// SetupWithManager registers the reconciler.
func (r *ControlPlaneEntityReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("controlplaneentity").
		For(&openbaov1alpha1.ControlPlaneEntity{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}
