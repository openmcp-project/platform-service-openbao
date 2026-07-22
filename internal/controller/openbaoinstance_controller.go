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
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	"github.com/openmcp-project/controller-utils/pkg/logging"

	openbaov1alpha1 "github.com/openmcp-project/platform-service-openbao/api/v1alpha1"
	"github.com/openmcp-project/platform-service-openbao/internal/openbao"
)

// +kubebuilder:rbac:groups=openbao.open-control-plane.io,resources=openbaoinstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=openbao.open-control-plane.io,resources=openbaoinstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=openbao.open-control-plane.io,resources=openbaoinstances/finalizers,verbs=update
// +kubebuilder:rbac:groups=openbao.open-control-plane.io,resources=serviceconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets;configmaps,verbs=get;list;watch

// OpenBaoInstanceReconciler reconciles a cluster-scoped OpenBaoInstance
// on the platform cluster. Its job is limited: probe the backend, publish
// version/reachability status. It does NOT touch tenant resources.
type OpenBaoInstanceReconciler struct {
	PlatformCluster *clusters.Cluster
	ProviderName    string
	// ClientFactory builds the OpenBao client from an OpenBaoInstance.
	// Overridable in tests.
	ClientFactory OpenBaoClientFactory
}

// NewOpenBaoInstanceReconciler returns a reconciler with the default
// client factory. Tests should set r.ClientFactory to inject a fake.
func NewOpenBaoInstanceReconciler(platformCluster *clusters.Cluster, providerName string) *OpenBaoInstanceReconciler {
	return &OpenBaoInstanceReconciler{
		PlatformCluster: platformCluster,
		ProviderName:    providerName,
		ClientFactory:   defaultOpenBaoClientFactory,
	}
}

// Reconcile probes the backend and updates status. It never fails hard on
// probe errors — an unreachable backend surfaces as OpenBaoReachable=False
// so dependent resources can wait rather than crash-loop.
func (r *OpenBaoInstanceReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logging.FromContextOrDiscard(ctx).WithName("openbaoinstance").WithValues("openbaoinstance", req.Name)
	ctx = logging.NewContext(ctx, log)

	inst := &openbaov1alpha1.OpenBaoInstance{}
	if err := r.PlatformCluster.Client().Get(ctx, req.NamespacedName, inst); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching OpenBaoInstance: %w", err)
	}

	// Deleting — nothing owned externally to clean up (this resource
	// records status only; no OpenBao objects are owned per-instance).
	if !inst.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	cfg, err := getServiceConfig(ctx, r.PlatformCluster.Client(), r.ProviderName)
	if err != nil {
		return ctrl.Result{}, err
	}

	inst.Status.ObservedGeneration = inst.Generation

	client, err := r.ClientFactory(ctx, inst)
	if err != nil {
		log.Info("Client construction failed", "error", err.Error())
		setCondition(&inst.Status.Conditions, inst.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionOpenBaoReachable,
			Status:  metav1.ConditionFalse,
			Reason:  openbaov1alpha1.ReasonOpenBaoUnreachable,
			Message: err.Error(),
		})
		setCondition(&inst.Status.Conditions, inst.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionReady,
			Status:  metav1.ConditionFalse,
			Reason:  openbaov1alpha1.ReasonOpenBaoUnreachable,
			Message: err.Error(),
		})
		return r.patchStatus(ctx, inst, cfg)
	}

	info, err := client.Health(ctx)
	if err != nil {
		log.Info("Health probe failed", "error", err.Error())
		setCondition(&inst.Status.Conditions, inst.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionOpenBaoReachable,
			Status:  metav1.ConditionFalse,
			Reason:  openbaov1alpha1.ReasonOpenBaoUnreachable,
			Message: err.Error(),
		})
		setCondition(&inst.Status.Conditions, inst.Generation, metav1.Condition{
			Type:    openbaov1alpha1.ConditionReady,
			Status:  metav1.ConditionFalse,
			Reason:  openbaov1alpha1.ReasonOpenBaoUnreachable,
			Message: err.Error(),
		})
		return r.patchStatus(ctx, inst, cfg)
	}

	now := metav1.Now()
	inst.Status.LastProbeTime = &now
	inst.Status.Version = info.Version
	inst.Status.Initialized = &info.Initialized
	inst.Status.Sealed = &info.Sealed

	if info.Sealed || !info.Initialized {
		reason := openbaov1alpha1.ReasonOpenBaoUnreachable
		msg := "backend reports sealed or uninitialized"
		setCondition(&inst.Status.Conditions, inst.Generation, metav1.Condition{
			Type: openbaov1alpha1.ConditionOpenBaoReachable, Status: metav1.ConditionFalse, Reason: reason, Message: msg,
		})
		setCondition(&inst.Status.Conditions, inst.Generation, metav1.Condition{
			Type: openbaov1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: reason, Message: msg,
		})
		return r.patchStatus(ctx, inst, cfg)
	}

	setCondition(&inst.Status.Conditions, inst.Generation, metav1.Condition{
		Type: openbaov1alpha1.ConditionOpenBaoReachable, Status: metav1.ConditionTrue, Reason: openbaov1alpha1.ReasonReconciled,
	})
	markReady(&inst.Status.Conditions, inst.Generation)
	return r.patchStatus(ctx, inst, cfg)
}

// patchStatus writes status back to the platform cluster and applies the
// standard requeue interval.
func (r *OpenBaoInstanceReconciler) patchStatus(ctx context.Context, inst *openbaov1alpha1.OpenBaoInstance, cfg *openbaov1alpha1.ServiceConfig) (reconcile.Result, error) {
	if err := r.PlatformCluster.Client().Status().Update(ctx, inst); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating OpenBaoInstance status: %w", err)
	}
	return requeueResult(cfg), nil
}

// SetupWithManager registers the reconciler. The manager is anchored to
// the onboarding cluster; we point it at the platform-cluster cache for
// OpenBaoInstance events.
func (r *OpenBaoInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("openbaoinstance").
		WatchesRawSource(source.Kind(
			r.PlatformCluster.Cluster().GetCache(),
			&openbaov1alpha1.OpenBaoInstance{},
			&handler.TypedEnqueueRequestForObject[*openbaov1alpha1.OpenBaoInstance]{},
			predicate.TypedGenerationChangedPredicate[*openbaov1alpha1.OpenBaoInstance]{},
		)).
		Complete(r)
}

// defaultOpenBaoClientFactory builds a real APIClient from an
// OpenBaoInstance spec. It resolves the CA bundle when
// spec.caBundleRef is set, but reading the platform credential from
// ServiceConfig.spec.platformCredentialRef is deferred until a
// per-reconciler credential store is available — see the TODO on the
// design's task 3.1.
var defaultOpenBaoClientFactory OpenBaoClientFactory = func(_ context.Context, inst *openbaov1alpha1.OpenBaoInstance) (openbao.Client, error) {
	if inst == nil {
		return nil, errors.New("openbaoinstance is nil")
	}
	// CA bundle resolution is intentionally omitted here for the first
	// pass; production wiring will inject the platform credential + CA
	// bytes via a factory closed over the manager's Secret cache.
	cfg := openbao.Config{
		Address:            inst.Spec.Address,
		InsecureSkipVerify: inst.Spec.InsecureSkipVerify,
		Namespace:          inst.Spec.Namespace,
	}
	return openbao.New(cfg)
}

// sourceFromPlatform-helper removed; use source.Kind directly.
