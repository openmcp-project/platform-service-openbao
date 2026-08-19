// SPDX-FileCopyrightText: Copyright OpenControlPlane contributors.
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
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
// publishes a non-sensitive identity summary. Target ControlPlane access is
// obtained through OpenMCP AccessRequests, never by reading implementation
// detail kubeconfig Secrets directly.
func (r *ControlPlaneEntityReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logging.FromContextOrDiscard(ctx).WithName("controlplaneentity").WithValues("controlplaneentity", req.String())
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

	trust := &openbaov1alpha1.ControlPlaneTrust{}
	trustKey := client.ObjectKey{Namespace: ce.Namespace, Name: ce.Spec.ControlPlaneTrustRef.Name}
	if err := r.OnboardingCluster.Client().Get(ctx, trustKey, trust); err != nil {
		if apierrors.IsNotFound(err) {
			dependencyNotReady(&ce.Status.Conditions, ce.Generation,
				openbaov1alpha1.ReasonDependencyNotFound,
				fmt.Sprintf("ControlPlaneTrust %q not found in namespace %q", trustKey.Name, trustKey.Namespace))
			return r.patchStatus(ctx, ce, cfg)
		}
		return ctrl.Result{}, fmt.Errorf("fetching ControlPlaneTrust: %w", err)
	}
	if !isConditionTrue(trust.Status.Conditions, openbaov1alpha1.ConditionTrustConfigured) {
		dependencyNotReady(&ce.Status.Conditions, ce.Generation,
			openbaov1alpha1.ReasonDependencyNotReady,
			fmt.Sprintf("ControlPlaneTrust %q is not ready", trust.Name))
		return r.patchStatus(ctx, ce, cfg)
	}

	cp, err := getControlPlane(ctx, r.OnboardingCluster.Client(), client.ObjectKey{Namespace: ce.Namespace, Name: trust.Spec.ControlPlaneRef.Name})
	if err != nil {
		return ctrl.Result{}, err
	}
	if cp == nil {
		dependencyNotReady(&ce.Status.Conditions, ce.Generation,
			openbaov1alpha1.ReasonDependencyNotFound,
			fmt.Sprintf("ControlPlane %q not found in namespace %q", trust.Spec.ControlPlaneRef.Name, ce.Namespace))
		return r.patchStatus(ctx, ce, cfg)
	}
	issuer := controlPlaneIssuer(cp)
	if issuer == "" {
		setCondition(&ce.Status.Conditions, ce.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionIdentityResolved,
			Status:  metav1.ConditionFalse,
			Reason:  openbaov1alpha1.ReasonControlPlaneUnavailable,
			Message: fmt.Sprintf("ControlPlane %s/%s does not expose service-account-issuer endpoint yet", cp.Namespace, cp.Name),
		})
		setCondition(&ce.Status.Conditions, ce.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionReady,
			Status:  metav1.ConditionFalse,
			Reason:  openbaov1alpha1.ReasonControlPlaneUnavailable,
			Message: "Waiting for ControlPlane issuer endpoint",
		})
		return r.patchStatus(ctx, ce, cfg)
	}

	mcpCluster, err := controlPlaneClusterAccess(ctx, r.PlatformCluster, r.ProviderName, cp,
		serviceAccountIdentityPermissions(ce.Spec.ServiceAccountRef.Namespace))
	if err != nil {
		setCondition(&ce.Status.Conditions, ce.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionIdentityResolved,
			Status:  metav1.ConditionFalse,
			Reason:  openbaov1alpha1.ReasonControlPlaneUnavailable,
			Message: err.Error(),
		})
		setCondition(&ce.Status.Conditions, ce.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionReady,
			Status:  metav1.ConditionFalse,
			Reason:  openbaov1alpha1.ReasonControlPlaneUnavailable,
			Message: "Waiting for ControlPlane access",
		})
		return r.patchStatus(ctx, ce, cfg)
	}
	identity, identityID, err := resolveServiceAccountIdentity(ctx, mcpCluster,
		cp.Namespace, cp.Name,
		ce.Spec.ServiceAccountRef.Namespace,
		ce.Spec.ServiceAccountRef.Name,
		resolveControlPlaneAudience(trust.Status.Audience))
	if err != nil {
		setCondition(&ce.Status.Conditions, ce.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionIdentityResolved,
			Status:  metav1.ConditionFalse,
			Reason:  openbaov1alpha1.ReasonControlPlaneUnavailable,
			Message: err.Error(),
		})
		setCondition(&ce.Status.Conditions, ce.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionReady,
			Status:  metav1.ConditionFalse,
			Reason:  openbaov1alpha1.ReasonControlPlaneUnavailable,
			Message: "Could not resolve ServiceAccount identity",
		})
		return r.patchStatus(ctx, ce, cfg)
	}
	ce.Status.Identity = identity
	ce.Status.IdentityID = identityID
	setCondition(&ce.Status.Conditions, ce.Generation, metav1.Condition{
		Type:   openbaov1alpha1.ConditionIdentityResolved,
		Status: metav1.ConditionTrue,
		Reason: openbaov1alpha1.ReasonReconciled,
	})
	markReady(&ce.Status.Conditions, ce.Generation)
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
		Watches(&openbaov1alpha1.ControlPlaneTrust{}, handler.EnqueueRequestsFromMapFunc(r.enqueueForTrust)).
		Complete(r)
}

func (r *ControlPlaneEntityReconciler) enqueueForTrust(ctx context.Context, obj client.Object) []reconcile.Request {
	trust, ok := obj.(*openbaov1alpha1.ControlPlaneTrust)
	if !ok {
		return nil
	}
	list := &openbaov1alpha1.ControlPlaneEntityList{}
	if err := r.OnboardingCluster.Client().List(ctx, list, client.InNamespace(trust.Namespace)); err != nil {
		logging.FromContextOrDiscard(ctx).Error(err, "Could not list ControlPlaneEntities for ControlPlaneTrust")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		if list.Items[i].Spec.ControlPlaneTrustRef.Name == trust.Name {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
	}
	return reqs
}
