// SPDX-FileCopyrightText: Copyright OpenControlPlane contributors.
// SPDX-License-Identifier: Apache-2.0

// Package controller hosts the six reconcilers for
// platform-service-openbao. They share the two-cluster pattern from
// platform-service-quota: platform cluster carries OpenBaoInstance +
// ServiceConfig, onboarding cluster carries the four tenant CRDs
// (ProjectEntity, ControlPlaneTrust, ControlPlaneEntity, PolicyBinding).
package controller

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	openmcpconst "github.com/openmcp-project/openmcp-operator/api/constants"

	openbaov1alpha1 "github.com/openmcp-project/platform-service-openbao/api/v1alpha1"
	"github.com/openmcp-project/platform-service-openbao/internal/openbao"
)

// defaultRequeue is the requeue used when a ServiceConfig doesn't
// override .spec.requeueAfter. Deliberately modest so a stuck resource
// re-checks its dependencies without hammering OpenBao.
const (
	defaultRequeue = 5 * time.Minute

	platformJWTAuthMount = "openmcp-platform-jwt"
	platformJWTRole      = "platform-service-openbao"
	platformJWTAudience  = "openbao"
)

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
	if inst != nil && inst.Spec.Auth.JWT != nil && inst.Spec.Auth.JWT.MountPrefix != "" {
		return inst.Spec.Auth.JWT.MountPrefix
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

// newDefaultOpenBaoClientFactory builds OpenBao clients with an in-memory
// controller credential. Preferred auth path: request a short-lived platform
// ServiceAccount JWT and exchange it at the pre-bootstrapped
// openmcp-platform-jwt auth mount. A static platformCredentialRef remains as
// a temporary escape hatch for manual tests, but no OpenBao token is ever
// persisted by the controller.
func newDefaultOpenBaoClientFactory(platformCluster *clusters.Cluster, providerName string) OpenBaoClientFactory {
	return func(ctx context.Context, inst *openbaov1alpha1.OpenBaoInstance) (openbao.Client, error) {
		if inst == nil {
			return nil, fmt.Errorf("openbaoinstance is nil")
		}
		cfg := openbao.Config{
			Address:            inst.Spec.Address,
			InsecureSkipVerify: inst.Spec.InsecureSkipVerify,
			Namespace:          inst.Spec.Namespace,
		}
		svcCfg, err := getServiceConfig(ctx, platformCluster.Client(), providerName)
		if err != nil {
			return nil, err
		}
		if svcCfg.Spec.PlatformCredentialRef != nil {
			cfg.Token, err = readStaticPlatformCredential(ctx, platformCluster.Client(), svcCfg.Spec.PlatformCredentialRef)
			if err != nil {
				return nil, err
			}
		} else {
			jwt, err := requestPlatformServiceAccountJWT(ctx, platformCluster, providerName)
			if err != nil {
				return nil, err
			}
			cfg.Token, err = openbao.LoginJWT(ctx, cfg, platformJWTAuthMount, platformJWTRole, jwt)
			if err != nil {
				return nil, err
			}
		}
		return openbao.New(cfg)
	}
}

func readStaticPlatformCredential(ctx context.Context, c client.Client, ref *openbaov1alpha1.LocalSecretKeyRef) (string, error) {
	ns := os.Getenv(openmcpconst.EnvVariablePodNamespace)
	if ns == "" {
		return "", fmt.Errorf("environment variable %s must be set to read platformCredentialRef", openmcpconst.EnvVariablePodNamespace)
	}
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, secret); err != nil {
		return "", fmt.Errorf("fetching platform credential Secret %s/%s: %w", ns, ref.Name, err)
	}
	value := strings.TrimSpace(string(secret.Data[ref.Key]))
	if value == "" {
		return "", fmt.Errorf("platform credential Secret %s/%s key %q is empty or missing", ns, ref.Name, ref.Key)
	}
	return value, nil
}

func requestPlatformServiceAccountJWT(ctx context.Context, platformCluster *clusters.Cluster, providerName string) (string, error) {
	ns := os.Getenv(openmcpconst.EnvVariablePodNamespace)
	if ns == "" {
		return "", fmt.Errorf("environment variable %s must be set to request platform ServiceAccount token", openmcpconst.EnvVariablePodNamespace)
	}
	saName := os.Getenv(openmcpconst.EnvVariablePodServiceAccountName)
	if saName == "" {
		// OpenMCP operator names provider service accounts with the ps-/sp-/cp-
		// prefix by provider kind. platform-service-openbao runs as ps-openbao.
		saName = "ps-" + providerName
	}
	clientset, err := kubernetes.NewForConfig(platformCluster.RESTConfig())
	if err != nil {
		return "", fmt.Errorf("building platform Kubernetes client: %w", err)
	}
	exp := int64(600)
	tok, err := clientset.CoreV1().ServiceAccounts(ns).CreateToken(ctx, saName, &authv1.TokenRequest{
		Spec: authv1.TokenRequestSpec{
			Audiences:         []string{platformJWTAudience},
			ExpirationSeconds: &exp,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("requesting token for platform ServiceAccount %s/%s: %w", ns, saName, err)
	}
	return tok.Status.Token, nil
}
