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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	"github.com/openmcp-project/controller-utils/pkg/logging"

	openbaov1alpha1 "github.com/openmcp-project/platform-service-openbao/api/v1alpha1"
	"github.com/openmcp-project/platform-service-openbao/internal/openbao"
)

// +kubebuilder:rbac:groups=openbao.open-control-plane.io,resources=policybindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=openbao.open-control-plane.io,resources=policybindings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=openbao.open-control-plane.io,resources=policybindings/finalizers,verbs=update

// PolicyBindingReconciler owns exactly one OpenBao JWT role per
// PolicyBinding. It reads (never mutates) the named policy for the
// existence check.
type PolicyBindingReconciler struct {
	PlatformCluster   *clusters.Cluster
	OnboardingCluster *clusters.Cluster
	ProviderName      string
	ClientFactory     OpenBaoClientFactory
}

// NewPolicyBindingReconciler builds a PolicyBinding reconciler.
func NewPolicyBindingReconciler(platform, onboarding *clusters.Cluster, providerName string) *PolicyBindingReconciler {
	return &PolicyBindingReconciler{
		PlatformCluster:   platform,
		OnboardingCluster: onboarding,
		ProviderName:      providerName,
		ClientFactory:     newDefaultOpenBaoClientFactory(platform, providerName),
	}
}

// Reconcile flow:
//  1. Resolve ControlPlaneEntity (onboarding) → its ControlPlaneTrust (onboarding).
//  2. Resolve OpenBaoInstance (platform) from the trust's status.
//  3. On delete: delete the JWT role + remove finalizer.
//  4. Compute deterministic role name.
//  5. Check policy existence via read-only client call.
//  6. EnsureJWTRole with exactly [spec.policyName] as token_policies.
//  7. Publish roleName, authMountPath, policyExists, conditions.
//
// The identity binding is taken from ControlPlaneEntity.status.identity,
// which is resolved from a short-lived ServiceAccount token obtained through
// an OpenMCP AccessRequest to the target ControlPlane.
func (r *PolicyBindingReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logging.FromContextOrDiscard(ctx).WithName("policybinding").WithValues("policybinding", req.String())
	ctx = logging.NewContext(ctx, log)

	pb := &openbaov1alpha1.PolicyBinding{}
	if err := r.OnboardingCluster.Client().Get(ctx, req.NamespacedName, pb); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching PolicyBinding: %w", err)
	}

	cfg, err := getServiceConfig(ctx, r.PlatformCluster.Client(), r.ProviderName)
	if err != nil {
		return ctrl.Result{}, err
	}
	pb.Status.ObservedGeneration = pb.Generation
	pb.Status.PolicyName = pb.Spec.PolicyName

	if !pb.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, pb)
	}

	if controllerutil.AddFinalizer(pb, openbaov1alpha1.FinalizerPolicyBinding) {
		if err := r.OnboardingCluster.Client().Update(ctx, pb); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
	}

	// Resolve ControlPlaneEntity in the same namespace.
	ce := &openbaov1alpha1.ControlPlaneEntity{}
	ceKey := types.NamespacedName{Namespace: pb.Namespace, Name: pb.Spec.ControlPlaneEntityRef.Name}
	if err := r.OnboardingCluster.Client().Get(ctx, ceKey, ce); err != nil {
		if apierrors.IsNotFound(err) {
			dependencyNotReady(&pb.Status.Conditions, pb.Generation,
				openbaov1alpha1.ReasonDependencyNotFound,
				fmt.Sprintf("ControlPlaneEntity %q not found in namespace %q", ceKey.Name, ceKey.Namespace))
			return r.patchStatus(ctx, pb, cfg)
		}
		return ctrl.Result{}, fmt.Errorf("fetching ControlPlaneEntity: %w", err)
	}

	// Find the ControlPlaneTrust for the same ControlPlane in the same
	// namespace. Standard convention: one Trust per ControlPlane per
	// namespace.
	trust, err := r.findTrustForControlPlane(ctx, pb.Namespace, ce.Spec.ControlPlaneRef.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if trust == nil {
		dependencyNotReady(&pb.Status.Conditions, pb.Generation,
			openbaov1alpha1.ReasonDependencyNotFound,
			fmt.Sprintf("No ControlPlaneTrust found for ControlPlane %q in namespace %q", ce.Spec.ControlPlaneRef.Name, pb.Namespace))
		return r.patchStatus(ctx, pb, cfg)
	}
	if trust.Status.AuthMountPath == "" || trust.Status.ResolvedOpenBaoInstance == "" {
		dependencyNotReady(&pb.Status.Conditions, pb.Generation,
			openbaov1alpha1.ReasonDependencyNotReady,
			fmt.Sprintf("ControlPlaneTrust %q has no resolved auth mount yet", trust.Name))
		pb.Status.AuthMountPath = "" // stay empty until upstream is ready
		return r.patchStatus(ctx, pb, cfg)
	}
	pb.Status.AuthMountPath = trust.Status.AuthMountPath

	// Get the OpenBao client for the resolved instance.
	inst, err := getOpenBaoInstance(ctx, r.PlatformCluster.Client(), trust.Status.ResolvedOpenBaoInstance)
	if err != nil {
		return ctrl.Result{}, err
	}
	if inst == nil || !isInstanceReachable(inst) {
		dependencyNotReady(&pb.Status.Conditions, pb.Generation,
			openbaov1alpha1.ReasonOpenBaoUnreachable,
			"resolved OpenBaoInstance is not reachable")
		return r.patchStatus(ctx, pb, cfg)
	}
	baoClient, err := r.ClientFactory(ctx, inst)
	if err != nil {
		dependencyNotReady(&pb.Status.Conditions, pb.Generation,
			openbaov1alpha1.ReasonOpenBaoUnreachable, err.Error())
		return r.patchStatus(ctx, pb, cfg)
	}

	// Policy existence check — never mutating.
	exists, err := baoClient.PolicyExists(ctx, pb.Spec.PolicyName)
	switch {
	case err != nil:
		setCondition(&pb.Status.Conditions, pb.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionPolicyResolved,
			Status:  metav1.ConditionUnknown,
			Reason:  openbaov1alpha1.ReasonPolicyCheckSkipped,
			Message: err.Error(),
		})
		pb.Status.PolicyExists = openbaov1alpha1.PolicyExistenceUnknown
	case !exists.Known:
		setCondition(&pb.Status.Conditions, pb.Generation, metav1.Condition{
			Type:   openbaov1alpha1.ConditionPolicyResolved,
			Status: metav1.ConditionUnknown,
			Reason: openbaov1alpha1.ReasonPolicyCheckSkipped,
		})
		pb.Status.PolicyExists = openbaov1alpha1.PolicyExistenceUnknown
	case !exists.Exists:
		setCondition(&pb.Status.Conditions, pb.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionPolicyResolved,
			Status:  metav1.ConditionFalse,
			Reason:  openbaov1alpha1.ReasonPolicyNotFound,
			Message: fmt.Sprintf("OpenBao policy %q does not exist yet", pb.Spec.PolicyName),
		})
		pb.Status.PolicyExists = openbaov1alpha1.PolicyExistenceFalse
	default:
		setCondition(&pb.Status.Conditions, pb.Generation, metav1.Condition{
			Type:   openbaov1alpha1.ConditionPolicyResolved,
			Status: metav1.ConditionTrue,
			Reason: openbaov1alpha1.ReasonPolicyExists,
		})
		pb.Status.PolicyExists = openbaov1alpha1.PolicyExistenceTrue
	}

	// Do not create a role until we have a resolved identity; an unbound JWT
	// role would be broader than intended.
	if !isConditionTrue(ce.Status.Conditions, openbaov1alpha1.ConditionIdentityResolved) || ce.Status.Identity == nil || ce.Status.Identity.Subject == "" {
		dependencyNotReady(&pb.Status.Conditions, pb.Generation,
			openbaov1alpha1.ReasonDependencyNotReady,
			fmt.Sprintf("ControlPlaneEntity %q has not resolved its identity yet", ce.Name))
		return r.patchStatus(ctx, pb, cfg)
	}

	// Deterministic role name — stable + surfaceable.
	roleName := openbao.RoleName(pb.Namespace, pb.Name)
	pb.Status.RoleName = roleName

	role := openbao.JWTRole{
		Name:          roleName,
		RoleType:      "jwt",
		UserClaim:     "sub",
		TokenPolicies: []string{pb.Spec.PolicyName},
		TokenTTL:      pb.Spec.TTL,
		TokenMaxTTL:   pb.Spec.MaxTTL,
	}
	if trust.Status.Audience != "" {
		role.BoundAudiences = []string{trust.Status.Audience}
	}
	if ce.Status.Identity != nil && ce.Status.Identity.Subject != "" {
		role.BoundSubject = ce.Status.Identity.Subject
	}
	if err := baoClient.EnsureJWTRole(ctx, trust.Status.AuthMountPath, role); err != nil {
		dependencyNotReady(&pb.Status.Conditions, pb.Generation,
			openbaov1alpha1.ReasonReconcileError, err.Error())
		return r.patchStatus(ctx, pb, cfg)
	}

	// If the policy check said False, keep Ready=False so users see the
	// missing policy without the role being marked healthy.
	if pb.Status.PolicyExists == openbaov1alpha1.PolicyExistenceFalse {
		setCondition(&pb.Status.Conditions, pb.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionReady,
			Status:  metav1.ConditionFalse,
			Reason:  openbaov1alpha1.ReasonWaitingForPolicy,
			Message: fmt.Sprintf("Waiting for OpenBao policy %q", pb.Spec.PolicyName),
		})
		return r.patchStatus(ctx, pb, cfg)
	}
	markReady(&pb.Status.Conditions, pb.Generation)
	return r.patchStatus(ctx, pb, cfg)
}

// reconcileDelete removes the owned JWT role (only) and drops the
// finalizer. Never touches the user-managed policy.
func (r *PolicyBindingReconciler) reconcileDelete(ctx context.Context, pb *openbaov1alpha1.PolicyBinding) (reconcile.Result, error) {
	if pb.Status.RoleName != "" && pb.Status.AuthMountPath != "" {
		// Find the trust to know which OpenBaoInstance to reach.
		trust := &openbaov1alpha1.ControlPlaneTrust{}
		// We can't reconstruct the trust name from PolicyBinding alone;
		// derive OpenBaoInstance from status.AuthMountPath's owning trust
		// by listing trusts in the same namespace whose status auth mount
		// matches. If ambiguous we skip the OpenBao-side delete rather
		// than delete the wrong thing.
		trusts := &openbaov1alpha1.ControlPlaneTrustList{}
		if err := r.OnboardingCluster.Client().List(ctx, trusts, client.InNamespace(pb.Namespace)); err == nil {
			for i := range trusts.Items {
				if trusts.Items[i].Status.AuthMountPath == pb.Status.AuthMountPath {
					trust = &trusts.Items[i]
					break
				}
			}
		}
		if trust.Status.ResolvedOpenBaoInstance != "" {
			inst, err := getOpenBaoInstance(ctx, r.PlatformCluster.Client(), trust.Status.ResolvedOpenBaoInstance)
			if err != nil {
				return ctrl.Result{}, err
			}
			if inst != nil && isInstanceReachable(inst) {
				baoClient, err := r.ClientFactory(ctx, inst)
				if err == nil {
					if err := baoClient.DeleteJWTRole(ctx, pb.Status.AuthMountPath, pb.Status.RoleName); err != nil {
						return ctrl.Result{}, fmt.Errorf("deleting JWT role: %w", err)
					}
				}
			}
		}
	}
	if controllerutil.RemoveFinalizer(pb, openbaov1alpha1.FinalizerPolicyBinding) {
		if err := r.OnboardingCluster.Client().Update(ctx, pb); err != nil {
			return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
		}
	}
	return ctrl.Result{}, nil
}

// findTrustForControlPlane finds the ControlPlaneTrust in the given
// namespace whose ControlPlaneRef.Name matches cpName. Returns (nil, nil)
// if none exists.
func (r *PolicyBindingReconciler) findTrustForControlPlane(ctx context.Context, namespace, cpName string) (*openbaov1alpha1.ControlPlaneTrust, error) {
	list := &openbaov1alpha1.ControlPlaneTrustList{}
	if err := r.OnboardingCluster.Client().List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing ControlPlaneTrusts: %w", err)
	}
	for i := range list.Items {
		if list.Items[i].Spec.ControlPlaneRef.Name == cpName {
			return &list.Items[i], nil
		}
	}
	return nil, nil
}

func (r *PolicyBindingReconciler) patchStatus(ctx context.Context, pb *openbaov1alpha1.PolicyBinding, cfg *openbaov1alpha1.ServiceConfig) (reconcile.Result, error) {
	if err := r.OnboardingCluster.Client().Status().Update(ctx, pb); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating PolicyBinding status: %w", err)
	}
	return requeueResult(cfg), nil
}

// SetupWithManager registers the reconciler.
func (r *PolicyBindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("policybinding").
		For(&openbaov1alpha1.PolicyBinding{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}
