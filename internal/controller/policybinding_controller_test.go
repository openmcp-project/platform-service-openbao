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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	openbaov1alpha1 "github.com/openmcp-project/platform-service-openbao/api/v1alpha1"
	"github.com/openmcp-project/platform-service-openbao/internal/openbao"
)

// PolicyBinding is the reconciler where the two-cluster wiring, the
// OpenBao fake, and the tri-state policy check all intersect. The tests
// pin behaviours the spec calls out explicitly:
//   - missing policy MUST NOT be an admission rejection; it MUST surface
//     as a status condition (spec.md Requirement 8)
//   - the reconciler MUST NOT create a Kubernetes Secret containing an
//     OpenBao token (spec.md Requirement 7)

var _ = Describe("PolicyBinding controller", func() {
	var (
		fake       *openbao.FakeClient
		reconciler *PolicyBindingReconciler
	)

	// clientFactory returns whatever the current `fake` is. Set via a
	// closure so BeforeEach can swap fakes without re-registering the
	// reconciler.
	clientFactory := OpenBaoClientFactory(func(_ context.Context, _ *openbaov1alpha1.OpenBaoInstance) (openbao.Client, error) {
		return fake, nil
	})

	BeforeEach(func() {
		fake = openbao.NewFakeClient()
		reconciler = NewPolicyBindingReconciler(platformCluster, onboardingCluster, testProviderName)
		reconciler.ClientFactory = clientFactory
	})

	It("reports DependencyNotFound when the referenced ControlPlaneEntity is missing", func() {
		pb := &openbaov1alpha1.PolicyBinding{}
		pb.Namespace = testProviderName
		pb.Name = "pb-missing-entity"
		pb.Spec.ControlPlaneEntityRef.Name = "does-not-exist"
		pb.Spec.PolicyName = "kv-prod-read"
		Expect(onboardingK8sClient.Create(ctx, pb)).To(Succeed())
		DeferCleanup(func() { _ = onboardingK8sClient.Delete(context.Background(), pb) })

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(pb)})
		Expect(err).NotTo(HaveOccurred())

		got := &openbaov1alpha1.PolicyBinding{}
		Expect(onboardingK8sClient.Get(ctx, client.ObjectKeyFromObject(pb), got)).To(Succeed())
		ready := apimeta.FindStatusCondition(got.Status.Conditions, openbaov1alpha1.ConditionReady)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal(openbaov1alpha1.ReasonDependencyNotFound))
		// spec.md Requirement 7: no token Secret should ever be created.
		Expect(secretExistsInNamespace(onboardingK8sClient, testProviderName)).To(BeFalse())
	})

	It("reports PolicyResolved=False with reason PolicyNotFound when the OpenBao policy is missing", func() {
		// Wire the platform cluster with a reachable OpenBaoInstance.
		inst := &openbaov1alpha1.OpenBaoInstance{}
		inst.Name = "obi-policytest"
		inst.Spec.Address = "https://openbao.example.test"
		Expect(platformK8sClient.Create(ctx, inst)).To(Succeed())
		DeferCleanup(func() { _ = platformK8sClient.Delete(context.Background(), inst) })
		markInstanceReachable(inst)

		// Onboarding cluster: ControlPlaneTrust that already has an auth
		// mount + resolved instance in its status, and a
		// ControlPlaneEntity + PolicyBinding that reference it.
		trust := &openbaov1alpha1.ControlPlaneTrust{}
		trust.Namespace = testProviderName
		trust.Name = "trust-policytest"
		trust.Spec.ProjectEntityRef.Name = "irrelevant-for-this-test"
		trust.Spec.ProjectEntityRef.Namespace = testProviderName
		trust.Spec.ControlPlaneRef.Name = "cp-policytest"
		Expect(onboardingK8sClient.Create(ctx, trust)).To(Succeed())
		DeferCleanup(func() { _ = onboardingK8sClient.Delete(context.Background(), trust) })
		trust.Status.AuthMountPath = "openbao-default-cp-policytest-abc"
		trust.Status.ResolvedOpenBaoInstance = "obi-policytest"
		Expect(onboardingK8sClient.Status().Update(ctx, trust)).To(Succeed())
		// Pre-seed the auth mount inside the fake so EnsureJWTRole can
		// attach the role to it.
		Expect(fake.EnsureJWTAuthMount(ctx, trust.Status.AuthMountPath)).To(Succeed())

		ce := &openbaov1alpha1.ControlPlaneEntity{}
		ce.Namespace = testProviderName
		ce.Name = "ce-policytest"
		ce.Spec.ControlPlaneRef.Name = "cp-policytest"
		ce.Spec.ServiceAccountRef.Name = "eso-reader"
		ce.Spec.ServiceAccountRef.Namespace = "external-secrets"
		Expect(onboardingK8sClient.Create(ctx, ce)).To(Succeed())
		DeferCleanup(func() { _ = onboardingK8sClient.Delete(context.Background(), ce) })
		ce.Status.Identity = &openbaov1alpha1.ServiceAccountIdentity{
			Subject:   "system:serviceaccount:external-secrets:eso-reader",
			Audiences: []string{"openbao"},
		}
		apimeta.SetStatusCondition(&ce.Status.Conditions, metav1.Condition{
			Type:   openbaov1alpha1.ConditionIdentityResolved,
			Status: metav1.ConditionTrue,
			Reason: openbaov1alpha1.ReasonReconciled,
		})
		Expect(onboardingK8sClient.Status().Update(ctx, ce)).To(Succeed())

		pb := &openbaov1alpha1.PolicyBinding{}
		pb.Namespace = testProviderName
		pb.Name = "pb-policytest"
		pb.Spec.ControlPlaneEntityRef.Name = "ce-policytest"
		pb.Spec.PolicyName = "kv-prod-read" // NOT in fake.Policies
		Expect(onboardingK8sClient.Create(ctx, pb)).To(Succeed())
		DeferCleanup(func() { _ = onboardingK8sClient.Delete(context.Background(), pb) })

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(pb)})
		Expect(err).NotTo(HaveOccurred())

		got := &openbaov1alpha1.PolicyBinding{}
		Expect(onboardingK8sClient.Get(ctx, client.ObjectKeyFromObject(pb), got)).To(Succeed())

		Expect(got.Status.PolicyExists).To(Equal(openbaov1alpha1.PolicyExistenceFalse))
		policyResolved := apimeta.FindStatusCondition(got.Status.Conditions, openbaov1alpha1.ConditionPolicyResolved)
		Expect(policyResolved).NotTo(BeNil())
		Expect(policyResolved.Status).To(Equal(metav1.ConditionFalse))
		Expect(policyResolved.Reason).To(Equal(openbaov1alpha1.ReasonPolicyNotFound))

		// The role was still created — this is the "one role per binding"
		// behaviour the design mandates, independent of policy existence.
		_, ok := fake.Role(trust.Status.AuthMountPath, got.Status.RoleName)
		Expect(ok).To(BeTrue())
		// spec.md Requirement 7: no token Secret was created.
		Expect(secretExistsInNamespace(onboardingK8sClient, testProviderName)).To(BeFalse())
	})
})

// markInstanceReachable patches OpenBaoInstance status so isReachable()
// returns true — the fake short-circuits Health() so the OpenBaoInstance
// reconciler itself isn't running in these focused tests.
func markInstanceReachable(inst *openbaov1alpha1.OpenBaoInstance) {
	got := &openbaov1alpha1.OpenBaoInstance{}
	Expect(platformK8sClient.Get(ctx, types.NamespacedName{Name: inst.Name}, got)).To(Succeed())
	apimeta.SetStatusCondition(&got.Status.Conditions, metav1.Condition{
		Type:   openbaov1alpha1.ConditionOpenBaoReachable,
		Status: metav1.ConditionTrue,
		Reason: openbaov1alpha1.ReasonReconciled,
	})
	Expect(platformK8sClient.Status().Update(ctx, got)).To(Succeed())
}

// secretExistsInNamespace is a spec-invariant probe: whenever it returns
// true, the reconciler has violated the trust-only output contract.
func secretExistsInNamespace(c client.Client, ns string) bool {
	list := &corev1.SecretList{}
	if err := c.List(context.Background(), list, client.InNamespace(ns)); err != nil {
		return false
	}
	// envtest's default namespace has no auto-created secrets, so any
	// entry here would be reconciler-created.
	return len(list.Items) > 0
}
