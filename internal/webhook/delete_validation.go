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

// Package webhook contains onboarding-local validating webhooks for the
// OpenBao platform service API.
package webhook

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	openbaov1alpha1 "github.com/openmcp-project/platform-service-openbao/api/v1alpha1"
)

// ProjectEntityValidator prevents deleting a ProjectEntity while trusts still
// reference it.
type ProjectEntityValidator struct{ client.Client }

// +kubebuilder:webhook:path=/validate-openbao-open-control-plane-io-v1alpha1-projectentity,mutating=false,failurePolicy=fail,sideEffects=None,groups=openbao.open-control-plane.io,resources=projectentities,verbs=delete,versions=v1alpha1,name=vprojectentity.openbao.open-control-plane.io,admissionReviewVersions=v1

var _ admission.Validator[*openbaov1alpha1.ProjectEntity] = &ProjectEntityValidator{}

func SetupProjectEntityWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &openbaov1alpha1.ProjectEntity{}).
		WithValidator(&ProjectEntityValidator{Client: mgr.GetClient()}).
		Complete()
}

func (v *ProjectEntityValidator) ValidateCreate(_ context.Context, _ *openbaov1alpha1.ProjectEntity) (admission.Warnings, error) {
	return nil, nil
}

func (v *ProjectEntityValidator) ValidateUpdate(_ context.Context, _, _ *openbaov1alpha1.ProjectEntity) (admission.Warnings, error) {
	return nil, nil
}

func (v *ProjectEntityValidator) ValidateDelete(ctx context.Context, obj *openbaov1alpha1.ProjectEntity) (admission.Warnings, error) {
	trusts := &openbaov1alpha1.ControlPlaneTrustList{}
	if err := v.List(ctx, trusts); err != nil {
		return nil, fmt.Errorf("listing ControlPlaneTrust dependencies: %w", err)
	}
	for i := range trusts.Items {
		ref := trusts.Items[i].Spec.ProjectEntityRef
		if ref.Namespace == obj.Namespace && ref.Name == obj.Name {
			return nil, fmt.Errorf("cannot delete ProjectEntity %s/%s: referenced by ControlPlaneTrust %s/%s", obj.Namespace, obj.Name, trusts.Items[i].Namespace, trusts.Items[i].Name)
		}
	}
	return nil, nil
}

// ControlPlaneTrustValidator prevents deleting a trust while entities still
// reference it.
type ControlPlaneTrustValidator struct{ client.Client }

// +kubebuilder:webhook:path=/validate-openbao-open-control-plane-io-v1alpha1-controlplanetrust,mutating=false,failurePolicy=fail,sideEffects=None,groups=openbao.open-control-plane.io,resources=controlplanetrusts,verbs=delete,versions=v1alpha1,name=vcontrolplanetrust.openbao.open-control-plane.io,admissionReviewVersions=v1

var _ admission.Validator[*openbaov1alpha1.ControlPlaneTrust] = &ControlPlaneTrustValidator{}

func SetupControlPlaneTrustWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &openbaov1alpha1.ControlPlaneTrust{}).
		WithValidator(&ControlPlaneTrustValidator{Client: mgr.GetClient()}).
		Complete()
}

func (v *ControlPlaneTrustValidator) ValidateCreate(_ context.Context, _ *openbaov1alpha1.ControlPlaneTrust) (admission.Warnings, error) {
	return nil, nil
}

func (v *ControlPlaneTrustValidator) ValidateUpdate(_ context.Context, _, _ *openbaov1alpha1.ControlPlaneTrust) (admission.Warnings, error) {
	return nil, nil
}

func (v *ControlPlaneTrustValidator) ValidateDelete(ctx context.Context, obj *openbaov1alpha1.ControlPlaneTrust) (admission.Warnings, error) {
	entities := &openbaov1alpha1.ControlPlaneEntityList{}
	if err := v.List(ctx, entities, client.InNamespace(obj.Namespace)); err != nil {
		return nil, fmt.Errorf("listing ControlPlaneEntity dependencies: %w", err)
	}
	for i := range entities.Items {
		if entities.Items[i].Spec.ControlPlaneTrustRef.Name == obj.Name {
			return nil, fmt.Errorf("cannot delete ControlPlaneTrust %s/%s: referenced by ControlPlaneEntity %s/%s", obj.Namespace, obj.Name, entities.Items[i].Namespace, entities.Items[i].Name)
		}
	}
	return nil, nil
}

// ControlPlaneEntityValidator prevents deleting an entity while policy
// bindings still reference it.
type ControlPlaneEntityValidator struct{ client.Client }

// +kubebuilder:webhook:path=/validate-openbao-open-control-plane-io-v1alpha1-controlplaneentity,mutating=false,failurePolicy=fail,sideEffects=None,groups=openbao.open-control-plane.io,resources=controlplaneentities,verbs=delete,versions=v1alpha1,name=vcontrolplaneentity.openbao.open-control-plane.io,admissionReviewVersions=v1

var _ admission.Validator[*openbaov1alpha1.ControlPlaneEntity] = &ControlPlaneEntityValidator{}

func SetupControlPlaneEntityWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &openbaov1alpha1.ControlPlaneEntity{}).
		WithValidator(&ControlPlaneEntityValidator{Client: mgr.GetClient()}).
		Complete()
}

func (v *ControlPlaneEntityValidator) ValidateCreate(_ context.Context, _ *openbaov1alpha1.ControlPlaneEntity) (admission.Warnings, error) {
	return nil, nil
}

func (v *ControlPlaneEntityValidator) ValidateUpdate(_ context.Context, _, _ *openbaov1alpha1.ControlPlaneEntity) (admission.Warnings, error) {
	return nil, nil
}

func (v *ControlPlaneEntityValidator) ValidateDelete(ctx context.Context, obj *openbaov1alpha1.ControlPlaneEntity) (admission.Warnings, error) {
	bindings := &openbaov1alpha1.PolicyBindingList{}
	if err := v.List(ctx, bindings, client.InNamespace(obj.Namespace)); err != nil {
		return nil, fmt.Errorf("listing PolicyBinding dependencies: %w", err)
	}
	for i := range bindings.Items {
		for _, ref := range bindings.Items[i].Spec.ControlPlaneEntityRefs {
			if ref.Name == obj.Name {
				return nil, fmt.Errorf("cannot delete ControlPlaneEntity %s/%s: referenced by PolicyBinding %s/%s", obj.Namespace, obj.Name, bindings.Items[i].Namespace, bindings.Items[i].Name)
			}
		}
	}
	return nil, nil
}
