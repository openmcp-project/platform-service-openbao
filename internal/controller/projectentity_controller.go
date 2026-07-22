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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	"github.com/openmcp-project/controller-utils/pkg/logging"

	openbaov1alpha1 "github.com/openmcp-project/platform-service-openbao/api/v1alpha1"
	"github.com/openmcp-project/platform-service-openbao/internal/openbao"
)

// +kubebuilder:rbac:groups=openbao.open-control-plane.io,resources=projectentities,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=openbao.open-control-plane.io,resources=projectentities/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=openbao.open-control-plane.io,resources=projectentities/finalizers,verbs=update

// ProjectEntityReconciler reconciles the project-level identity anchor.
// It lives on the onboarding cluster; the referenced OpenBaoInstance
// lives on the platform cluster.
type ProjectEntityReconciler struct {
	PlatformCluster   *clusters.Cluster
	OnboardingCluster *clusters.Cluster
	ProviderName      string
	ClientFactory     OpenBaoClientFactory
}

// NewProjectEntityReconciler builds a ProjectEntity reconciler wired to
// both clusters.
func NewProjectEntityReconciler(platform, onboarding *clusters.Cluster, providerName string) *ProjectEntityReconciler {
	return &ProjectEntityReconciler{
		PlatformCluster:   platform,
		OnboardingCluster: onboarding,
		ProviderName:      providerName,
		ClientFactory:     newDefaultOpenBaoClientFactory(platform, providerName),
	}
}

// Reconcile resolves .spec.openBaoRef against the platform cluster and
// mirrors readiness. It does NOT materialise OpenBao entity/group objects
// yet — those are a follow-up (design.md open question: "Whether
// ProjectEntity owns OpenBao entity/group objects directly"). Current
// contract: DependencyReady tracks the referenced OpenBaoInstance's
// OpenBaoReachable condition; Ready follows that.
func (r *ProjectEntityReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logging.FromContextOrDiscard(ctx).WithName("projectentity").WithValues("projectentity", req.String())
	ctx = logging.NewContext(ctx, log)

	pe := &openbaov1alpha1.ProjectEntity{}
	if err := r.OnboardingCluster.Client().Get(ctx, req.NamespacedName, pe); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching ProjectEntity: %w", err)
	}
	if !pe.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	cfg, err := getServiceConfig(ctx, r.PlatformCluster.Client(), r.ProviderName)
	if err != nil {
		return ctrl.Result{}, err
	}

	pe.Status.ObservedGeneration = pe.Generation

	inst, err := getOpenBaoInstance(ctx, r.PlatformCluster.Client(), pe.Spec.OpenBaoInstanceRef.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if inst == nil {
		dependencyNotReady(&pe.Status.Conditions, pe.Generation,
			openbaov1alpha1.ReasonDependencyNotFound,
			fmt.Sprintf("OpenBaoInstance %q not found on the platform cluster", pe.Spec.OpenBaoInstanceRef.Name))
		return r.patchStatus(ctx, pe, cfg)
	}
	pe.Status.ResolvedOpenBaoRef = inst.Name

	if !isInstanceReachable(inst) {
		dependencyNotReady(&pe.Status.Conditions, pe.Generation,
			openbaov1alpha1.ReasonOpenBaoUnreachable,
			fmt.Sprintf("OpenBaoInstance %q is not reachable", inst.Name))
		return r.patchStatus(ctx, pe, cfg)
	}

	client, err := r.ClientFactory(ctx, inst)
	if err != nil {
		dependencyNotReady(&pe.Status.Conditions, pe.Generation,
			openbaov1alpha1.ReasonOpenBaoUnreachable, err.Error())
		return r.patchStatus(ctx, pe, cfg)
	}
	entityName := openbao.EntityName(pe.Namespace, pe.Name)
	entityID, err := client.EnsureEntity(ctx, entityName, map[string]string{
		"openmcp_namespace": pe.Namespace,
		"openmcp_name":      pe.Name,
	})
	if err != nil {
		dependencyNotReady(&pe.Status.Conditions, pe.Generation,
			openbaov1alpha1.ReasonReconcileError, err.Error())
		return r.patchStatus(ctx, pe, cfg)
	}
	pe.Status.EntityID = entityID
	markReady(&pe.Status.Conditions, pe.Generation)
	return r.patchStatus(ctx, pe, cfg)
}

func (r *ProjectEntityReconciler) patchStatus(ctx context.Context, pe *openbaov1alpha1.ProjectEntity, cfg *openbaov1alpha1.ServiceConfig) (reconcile.Result, error) {
	if err := r.OnboardingCluster.Client().Status().Update(ctx, pe); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating ProjectEntity status: %w", err)
	}
	return requeueResult(cfg), nil
}

// SetupWithManager registers the reconciler on the onboarding-cluster
// manager. Cross-cluster reactivity to OpenBaoInstance changes is handled
// by the standard requeue interval — good enough for a status-driven
// dependency signal.
func (r *ProjectEntityReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("projectentity").
		For(&openbaov1alpha1.ProjectEntity{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}
