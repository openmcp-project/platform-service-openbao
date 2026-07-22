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

// Package controller hosts the six reconcilers for
// platform-service-openbao. They share the two-cluster pattern from
// platform-service-quota: platform cluster carries OpenBaoInstance +
// ServiceConfig, onboarding cluster carries the four tenant CRDs
// (ProjectEntity, ControlPlaneTrust, ControlPlaneEntity, PolicyBinding).
package controller

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/openmcp-project/platform-service-openbao/api/v1alpha1"
	"github.com/openmcp-project/platform-service-openbao/internal/openbao"
)

// defaultRequeue is the requeue used when a ServiceConfig doesn't
// override .spec.requeueAfter. Deliberately modest so a stuck resource
// re-checks its dependencies without hammering OpenBao.
const defaultRequeue = 5 * time.Minute

// OpenBaoClientFactory turns an OpenBaoInstance into an openbao.Client.
// Reconcilers depend on this factory rather than the concrete APIClient so
// tests can inject a FakeClient. The default implementation resolves the
// CA bundle from Secret/ConfigMap and calls openbao.New.
type OpenBaoClientFactory func(ctx context.Context, inst *openbaov1alpha1.OpenBaoInstance) (openbao.Client, error)

// resolveRequeue returns the requeue interval from the given ServiceConfig
// spec, falling back to the package default.
func resolveRequeue(spec openbaov1alpha1.ServiceConfigSpec) time.Duration {
	if spec.RequeueAfter == "" {
		return defaultRequeue
	}
	d, err := time.ParseDuration(spec.RequeueAfter)
	if err != nil {
		return defaultRequeue
	}
	return d
}

// resolveAuthMountPrefix returns the effective mount-path prefix. Priority:
// per-OpenBaoInstance override → ServiceConfig default → empty (openbao
// picks a sensible default via its own naming code).
func resolveAuthMountPrefix(cfg openbaov1alpha1.ServiceConfigSpec, inst *openbaov1alpha1.OpenBaoInstance) string {
	if inst != nil && inst.Spec.AuthMountPrefix != "" {
		return inst.Spec.AuthMountPrefix
	}
	return cfg.AuthMountPrefix
}

// setCondition is a thin wrapper around apimeta.SetStatusCondition that
// stamps ObservedGeneration and normalises the LastTransitionTime.
func setCondition(conds *[]metav1.Condition, generation int64, cond metav1.Condition) {
	cond.ObservedGeneration = generation
	if cond.LastTransitionTime.IsZero() {
		cond.LastTransitionTime = metav1.Now()
	}
	meta.SetStatusCondition(conds, cond)
}

// dependencyNotReady marks the resource NotReady with a reason naming the
// blocking dependency. Reconcilers use it for the common "waited for X"
// path so the failure message tells the user exactly what to look at.
func dependencyNotReady(conds *[]metav1.Condition, generation int64, reason, message string) {
	setCondition(conds, generation, metav1.Condition{
		Type:    openbaov1alpha1.ConditionDependencyReady,
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	})
	setCondition(conds, generation, metav1.Condition{
		Type:    openbaov1alpha1.ConditionReady,
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	})
}

// markReady sets Ready=True + DependencyReady=True with the standard
// "Reconciled" reason. Called after a fully successful reconcile.
func markReady(conds *[]metav1.Condition, generation int64) {
	setCondition(conds, generation, metav1.Condition{
		Type:   openbaov1alpha1.ConditionDependencyReady,
		Status: metav1.ConditionTrue,
		Reason: openbaov1alpha1.ReasonReconciled,
	})
	setCondition(conds, generation, metav1.Condition{
		Type:   openbaov1alpha1.ConditionReady,
		Status: metav1.ConditionTrue,
		Reason: openbaov1alpha1.ReasonReconciled,
	})
}

// getOpenBaoInstance fetches a cluster-scoped OpenBaoInstance from the
// platform cluster. Returns (nil, nil) if the instance doesn't exist so
// callers can distinguish "not found" from "error".
func getOpenBaoInstance(ctx context.Context, platformClient client.Client, name string) (*openbaov1alpha1.OpenBaoInstance, error) {
	inst := &openbaov1alpha1.OpenBaoInstance{}
	if err := platformClient.Get(ctx, types.NamespacedName{Name: name}, inst); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("fetching OpenBaoInstance %q: %w", name, err)
	}
	return inst, nil
}

// getServiceConfig fetches the platform-owned ServiceConfig by
// providerName. On success it also validates the spec so subsequent
// reconcile code sees only clean input.
func getServiceConfig(ctx context.Context, platformClient client.Client, providerName string) (*openbaov1alpha1.ServiceConfig, error) {
	cfg := &openbaov1alpha1.ServiceConfig{}
	if err := platformClient.Get(ctx, types.NamespacedName{Name: providerName}, cfg); err != nil {
		return nil, fmt.Errorf("fetching ServiceConfig %q: %w", providerName, err)
	}
	if err := cfg.Spec.Validate(); err != nil {
		return nil, fmt.Errorf("ServiceConfig %q invalid: %w", providerName, err)
	}
	return cfg, nil
}

// isInstanceReachable returns true only when the OpenBaoInstance has been
// observed as reachable. Dependent reconcilers use it as a gate before
// making OpenBao calls.
func isInstanceReachable(inst *openbaov1alpha1.OpenBaoInstance) bool {
	if inst == nil {
		return false
	}
	return isConditionTrue(inst.Status.Conditions, openbaov1alpha1.ConditionOpenBaoReachable)
}

// isConditionTrue returns true iff conditions holds a matching Type with
// Status=True. Small wrapper over apimeta.FindStatusCondition so
// reconcilers don't repeat the nil check.
func isConditionTrue(conditions []metav1.Condition, condType string) bool {
	c := meta.FindStatusCondition(conditions, condType)
	return c != nil && c.Status == metav1.ConditionTrue
}

// requeueResult builds the standard requeue after a successful reconcile.
// Kept as a helper so requeue policy stays in one place.
func requeueResult(cfg *openbaov1alpha1.ServiceConfig) ctrl.Result {
	if cfg == nil {
		return ctrl.Result{RequeueAfter: defaultRequeue}
	}
	return ctrl.Result{RequeueAfter: resolveRequeue(cfg.Spec)}
}
