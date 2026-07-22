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
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
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

	roles := make([]openbaov1alpha1.PolicyBindingRoleStatus, 0, len(pb.Spec.ControlPlaneEntityRefs))
	allRolesReady := true
	dependencyMissing := false
	var blockingMessages []string
	policyKnown := true
	policyExists := true
	policyChecked := false

	for _, entityRef := range pb.Spec.ControlPlaneEntityRefs {
		roleStatus := openbaov1alpha1.PolicyBindingRoleStatus{ControlPlaneEntityRef: entityRef}
		roleName := openbao.RoleName(pb.Namespace, pb.Name+"-"+entityRef.Name)
		roleStatus.RoleName = roleName

		ce := &openbaov1alpha1.ControlPlaneEntity{}
		ceKey := types.NamespacedName{Namespace: pb.Namespace, Name: entityRef.Name}
		if err := r.OnboardingCluster.Client().Get(ctx, ceKey, ce); err != nil {
			allRolesReady = false
			dependencyMissing = true
			roleStatus.Message = fmt.Sprintf("ControlPlaneEntity %q not found in namespace %q", ceKey.Name, ceKey.Namespace)
			blockingMessages = append(blockingMessages, roleStatus.Message)
			roles = append(roles, roleStatus)
			continue
		}
		if !isConditionTrue(ce.Status.Conditions, openbaov1alpha1.ConditionIdentityResolved) || ce.Status.Identity == nil || ce.Status.Identity.Subject == "" {
			allRolesReady = false
			roleStatus.Message = fmt.Sprintf("ControlPlaneEntity %q has not resolved its identity yet", ce.Name)
			blockingMessages = append(blockingMessages, roleStatus.Message)
			roles = append(roles, roleStatus)
			continue
		}

		trust := &openbaov1alpha1.ControlPlaneTrust{}
		trustKey := types.NamespacedName{Namespace: pb.Namespace, Name: ce.Spec.ControlPlaneTrustRef.Name}
		if err := r.OnboardingCluster.Client().Get(ctx, trustKey, trust); err != nil {
			allRolesReady = false
			dependencyMissing = true
			roleStatus.Message = fmt.Sprintf("ControlPlaneTrust %q not found in namespace %q", trustKey.Name, trustKey.Namespace)
			blockingMessages = append(blockingMessages, roleStatus.Message)
			roles = append(roles, roleStatus)
			continue
		}
		if trust.Status.AuthMountPath == "" || trust.Status.ResolvedOpenBaoInstance == "" || !isConditionTrue(trust.Status.Conditions, openbaov1alpha1.ConditionTrustConfigured) {
			allRolesReady = false
			roleStatus.Message = fmt.Sprintf("ControlPlaneTrust %q has no resolved auth mount yet", trust.Name)
			blockingMessages = append(blockingMessages, roleStatus.Message)
			roles = append(roles, roleStatus)
			continue
		}
		roleStatus.AuthMountPath = trust.Status.AuthMountPath

		inst, err := getOpenBaoInstance(ctx, r.PlatformCluster.Client(), trust.Status.ResolvedOpenBaoInstance)
		if err != nil {
			return ctrl.Result{}, err
		}
		if inst == nil || !isInstanceReachable(inst) {
			allRolesReady = false
			roleStatus.Message = "resolved OpenBaoInstance is not reachable"
			blockingMessages = append(blockingMessages, roleStatus.Message)
			roles = append(roles, roleStatus)
			continue
		}
		baoClient, err := r.ClientFactory(ctx, inst)
		if err != nil {
			allRolesReady = false
			roleStatus.Message = err.Error()
			blockingMessages = append(blockingMessages, roleStatus.Message)
			roles = append(roles, roleStatus)
			continue
		}

		exists, err := baoClient.PolicyExists(ctx, pb.Spec.PolicyName)
		policyChecked = true
		switch {
		case err != nil:
			policyKnown = false
			allRolesReady = false
			roleStatus.Message = err.Error()
			blockingMessages = append(blockingMessages, roleStatus.Message)
		case !exists.Known:
			policyKnown = false
		case !exists.Exists:
			policyExists = false
			allRolesReady = false
			roleStatus.Message = fmt.Sprintf("OpenBao policy %q does not exist yet", pb.Spec.PolicyName)
			blockingMessages = append(blockingMessages, roleStatus.Message)
		}

		role := openbao.JWTRole{
			Name:           roleName,
			RoleType:       "jwt",
			UserClaim:      "sub",
			BoundSubject:   ce.Status.Identity.Subject,
			BoundAudiences: []string{trust.Status.Audience},
			TokenPolicies:  []string{pb.Spec.PolicyName},
			TokenTTL:       pb.Spec.TTL,
			TokenMaxTTL:    pb.Spec.MaxTTL,
		}
		if err := baoClient.EnsureJWTRole(ctx, trust.Status.AuthMountPath, role); err != nil {
			allRolesReady = false
			roleStatus.Message = err.Error()
			blockingMessages = append(blockingMessages, roleStatus.Message)
			roles = append(roles, roleStatus)
			continue
		}
		roleStatus.Ready = roleStatus.Message == ""
		roles = append(roles, roleStatus)
	}

	pb.Status.Roles = roles

	switch {
	case !policyChecked || !policyKnown:
		setCondition(&pb.Status.Conditions, pb.Generation, metav1.Condition{
			Type:   openbaov1alpha1.ConditionPolicyResolved,
			Status: metav1.ConditionUnknown,
			Reason: openbaov1alpha1.ReasonPolicyCheckSkipped,
		})
		pb.Status.PolicyExists = openbaov1alpha1.PolicyExistenceUnknown
	case !policyExists:
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

	if !policyExists {
		setCondition(&pb.Status.Conditions, pb.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionReady,
			Status:  metav1.ConditionFalse,
			Reason:  openbaov1alpha1.ReasonWaitingForPolicy,
			Message: fmt.Sprintf("Waiting for OpenBao policy %q", pb.Spec.PolicyName),
		})
		return r.patchStatus(ctx, pb, cfg)
	}
	if !allRolesReady {
		reason := openbaov1alpha1.ReasonDependencyNotReady
		if dependencyMissing {
			reason = openbaov1alpha1.ReasonDependencyNotFound
		}
		dependencyNotReady(&pb.Status.Conditions, pb.Generation,
			reason,
			strings.Join(blockingMessages, "; "))
		return r.patchStatus(ctx, pb, cfg)
	}
	markReady(&pb.Status.Conditions, pb.Generation)
	return r.patchStatus(ctx, pb, cfg)
}

// reconcileDelete removes owned JWT roles and drops the finalizer. Never touches
// the user-managed policy.
func (r *PolicyBindingReconciler) reconcileDelete(ctx context.Context, pb *openbaov1alpha1.PolicyBinding) (reconcile.Result, error) {
	for _, roleStatus := range pb.Status.Roles {
		if roleStatus.RoleName == "" || roleStatus.AuthMountPath == "" {
			continue
		}
		trust, err := r.findTrustForAuthMount(ctx, pb.Namespace, roleStatus.AuthMountPath)
		if err != nil {
			return ctrl.Result{}, err
		}
		if trust == nil || trust.Status.ResolvedOpenBaoInstance == "" {
			continue
		}
		inst, err := getOpenBaoInstance(ctx, r.PlatformCluster.Client(), trust.Status.ResolvedOpenBaoInstance)
		if err != nil {
			return ctrl.Result{}, err
		}
		if inst == nil || !isInstanceReachable(inst) {
			continue
		}
		baoClient, err := r.ClientFactory(ctx, inst)
		if err != nil {
			continue
		}
		if err := baoClient.DeleteJWTRole(ctx, roleStatus.AuthMountPath, roleStatus.RoleName); err != nil {
			return ctrl.Result{}, fmt.Errorf("deleting JWT role: %w", err)
		}
	}
	if controllerutil.RemoveFinalizer(pb, openbaov1alpha1.FinalizerPolicyBinding) {
		if err := r.OnboardingCluster.Client().Update(ctx, pb); err != nil {
			return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
		}
	}
	return ctrl.Result{}, nil
}

func (r *PolicyBindingReconciler) findTrustForAuthMount(ctx context.Context, namespace, authMountPath string) (*openbaov1alpha1.ControlPlaneTrust, error) {
	list := &openbaov1alpha1.ControlPlaneTrustList{}
	if err := r.OnboardingCluster.Client().List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing ControlPlaneTrusts: %w", err)
	}
	for i := range list.Items {
		if list.Items[i].Status.AuthMountPath == authMountPath {
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
		Watches(&openbaov1alpha1.ControlPlaneEntity{}, handler.EnqueueRequestsFromMapFunc(r.enqueueForEntity)).
		Watches(&openbaov1alpha1.ControlPlaneTrust{}, handler.EnqueueRequestsFromMapFunc(r.enqueueForTrust)).
		Complete(r)
}

func (r *PolicyBindingReconciler) enqueueForEntity(ctx context.Context, obj client.Object) []reconcile.Request {
	entity, ok := obj.(*openbaov1alpha1.ControlPlaneEntity)
	if !ok {
		return nil
	}
	list := &openbaov1alpha1.PolicyBindingList{}
	if err := r.OnboardingCluster.Client().List(ctx, list, client.InNamespace(entity.Namespace)); err != nil {
		logging.FromContextOrDiscard(ctx).Error(err, "Could not list PolicyBindings for ControlPlaneEntity")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		if policyBindingReferencesEntity(&list.Items[i], entity.Name) {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
	}
	return reqs
}

func (r *PolicyBindingReconciler) enqueueForTrust(ctx context.Context, obj client.Object) []reconcile.Request {
	trust, ok := obj.(*openbaov1alpha1.ControlPlaneTrust)
	if !ok {
		return nil
	}
	entities := &openbaov1alpha1.ControlPlaneEntityList{}
	if err := r.OnboardingCluster.Client().List(ctx, entities, client.InNamespace(trust.Namespace)); err != nil {
		logging.FromContextOrDiscard(ctx).Error(err, "Could not list ControlPlaneEntities for ControlPlaneTrust")
		return nil
	}
	entityNames := map[string]struct{}{}
	for i := range entities.Items {
		if entities.Items[i].Spec.ControlPlaneTrustRef.Name == trust.Name {
			entityNames[entities.Items[i].Name] = struct{}{}
		}
	}
	if len(entityNames) == 0 {
		return nil
	}
	bindings := &openbaov1alpha1.PolicyBindingList{}
	if err := r.OnboardingCluster.Client().List(ctx, bindings, client.InNamespace(trust.Namespace)); err != nil {
		logging.FromContextOrDiscard(ctx).Error(err, "Could not list PolicyBindings for ControlPlaneTrust")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(bindings.Items))
	for i := range bindings.Items {
		for _, ref := range bindings.Items[i].Spec.ControlPlaneEntityRefs {
			if _, ok := entityNames[ref.Name]; ok {
				reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&bindings.Items[i])})
				break
			}
		}
	}
	return reqs
}

func policyBindingReferencesEntity(binding *openbaov1alpha1.PolicyBinding, entityName string) bool {
	for _, ref := range binding.Spec.ControlPlaneEntityRefs {
		if ref.Name == entityName {
			return true
		}
	}
	return false
}
