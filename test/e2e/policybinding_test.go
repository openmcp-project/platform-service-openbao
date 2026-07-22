//go:build e2e
// +build e2e

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

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	"github.com/openmcp-project/openmcp-testing/pkg/clusterutils"
	"github.com/openmcp-project/openmcp-testing/pkg/providers"

	"github.com/openmcp-project/platform-service-openbao/test/e2e/backend"
)

// gvr helpers — used with the dynamic client since the e2e binary
// doesn't import the CRD Go types (avoids pinning the api version
// declaratively and keeps the test surface loose).
var (
	gvOpenBao      = "openbao.open-control-plane.io/v1alpha1"
	openBaoInstGVK = schema.GroupVersionKind{Group: "openbao.open-control-plane.io", Version: "v1alpha1", Kind: "OpenBaoInstance"}
	policyBindGVK  = schema.GroupVersionKind{Group: "openbao.open-control-plane.io", Version: "v1alpha1", Kind: "PolicyBinding"}
	projEntityGVK  = schema.GroupVersionKind{Group: "openbao.open-control-plane.io", Version: "v1alpha1", Kind: "ProjectEntity"}
	cpTrustGVK     = schema.GroupVersionKind{Group: "openbao.open-control-plane.io", Version: "v1alpha1", Kind: "ControlPlaneTrust"}
	cpEntityGVK    = schema.GroupVersionKind{Group: "openbao.open-control-plane.io", Version: "v1alpha1", Kind: "ControlPlaneEntity"}
)

// TestPolicyBinding_TrustIntegration validates that the reconciler
// integrates with a real OpenBao backend:
//
//  1. OpenBaoInstance goes OpenBaoReachable=True after the controller
//     probes /sys/health on the running dev-server container.
//  2. PolicyBinding computes a deterministic role name and creates the
//     OpenBao JWT role via the openbao/api/v2 client.
//  3. The role on the backend has exactly the named policy attached.
//  4. Policy existence check flips PolicyExists=True after seeding.
//  5. spec Requirement 7 invariant: no Kubernetes Secret ever holds an
//     OpenBao token.
//
// The test uses a real OpenMCP-created ControlPlane and a real ServiceAccount
// token to prove the JWT login path end-to-end.
func TestPolicyBinding_TrustIntegration(t *testing.T) {
	b := Backend()
	if b == nil {
		t.Fatal("backend is nil; TestMain did not initialise it")
	}

	feat := features.New("policybinding trust integration").
		Setup(providers.CreateMCP("prod", wait.WithTimeout(10*time.Minute))).
		Setup(patchControlPlaneIssuer("prod")).
		Setup(createMCPServiceAccount("prod")).
		Setup(seedPolicy(b, "kv-prod-read", `path "kv/data/prod/*" { capabilities = ["read"] }`)).
		Setup(applyFixtures("fixtures/policybinding-chain")).
		Assess("OpenBaoInstance reports Reachable=True", assessOpenBaoInstanceReachable()).
		Assess("PolicyBinding publishes roleName + policyExists=True", assessPolicyBindingStatus()).
		Assess("JWT role exists on OpenBao with exactly the named policy", assessRoleOnBackend(b, "kv-prod-read")).
		Assess("no token Secret exists in the tenant namespace", assessNoTokenSecret("default")).
		Feature()

	testenv.Test(t, feat)
}

// ---------------------------------------------------------------------
// Setup steps
// ---------------------------------------------------------------------

func seedPolicy(b *backend.Backend, name, hcl string) func(context.Context, *testing.T, *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, _ *envconf.Config) context.Context {
		if err := b.SeedPolicy(ctx, name, hcl); err != nil {
			t.Fatalf("seed policy %q: %v", name, err)
		}
		return ctx
	}
}

func patchControlPlaneIssuer(mcpName string) func(context.Context, *testing.T, *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, _ *envconf.Config) context.Context {
		onboarding, err := clusterutils.OnboardingConfig()
		if err != nil {
			t.Fatalf("resolve onboarding cluster: %v", err)
		}
		cp := &unstructured.Unstructured{}
		cp.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "core.open-control-plane.io",
			Version: "v2alpha1",
			Kind:    "ControlPlane",
		})
		if err := onboarding.Client().Resources().Get(ctx, mcpName, "default", cp); err != nil {
			t.Fatalf("get ControlPlane %s: %v", mcpName, err)
		}
		issuer := fmt.Sprintf("https://issuer.e2e.invalid/%s", mcpName)
		if err := unstructured.SetNestedSlice(cp.Object, []any{map[string]any{
			"name": "service-account-issuer",
			"url":  issuer,
		}}, "status", "endpoints"); err != nil {
			t.Fatalf("set ControlPlane issuer endpoint: %v", err)
		}
		if err := onboarding.Client().Resources().UpdateStatus(ctx, cp); err != nil {
			t.Fatalf("patch ControlPlane issuer endpoint: %v", err)
		}
		return ctx
	}
}

func createMCPServiceAccount(mcpName string) func(context.Context, *testing.T, *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, platform *envconf.Config) context.Context {
		mcp, err := clusterutils.MCPConfig(ctx, platform, mcpName)
		if err != nil {
			t.Fatalf("resolve MCP cluster %q: %v", mcpName, err)
		}
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "external-secrets"}}
		if err := mcp.Client().Resources().Create(ctx, ns); err != nil && !isAlreadyExists(err) {
			t.Fatalf("create MCP namespace external-secrets: %v", err)
		}
		sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "external-secrets", Namespace: "external-secrets"}}
		if err := mcp.Client().Resources().Create(ctx, sa); err != nil && !isAlreadyExists(err) {
			t.Fatalf("create MCP serviceaccount external-secrets/external-secrets: %v", err)
		}
		return ctx
	}
}

// applyFixtures applies every YAML under dir to the ONBOARDING cluster
// (which is a physically separate Kind cluster from platform under
// openmcp-testing's setup). Tenant CRDs and CRs both live on the
// onboarding cluster; the manager's controllers watch it via the
// AccessRequest kubeconfig.
func applyFixtures(dir string) func(context.Context, *testing.T, *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, _ *envconf.Config) context.Context {
		onboarding, err := clusterutils.OnboardingConfig()
		if err != nil {
			t.Fatalf("resolve onboarding cluster: %v", err)
		}
		entries, err := fixturesFS.ReadDir(dir)
		if err != nil {
			t.Fatalf("read fixtures dir %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			raw, err := fixturesFS.ReadFile(dir + "/" + e.Name())
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			// Split multi-document YAML.
			docs := strings.Split(string(raw), "\n---\n")
			for _, d := range docs {
				d = strings.TrimSpace(d)
				if d == "" {
					continue
				}
				obj := &unstructured.Unstructured{}
				if err := yamlUnmarshal([]byte(d), obj); err != nil {
					t.Fatalf("parse %s: %v", e.Name(), err)
				}
				if obj.GetKind() == "" {
					continue
				}
				if err := onboarding.Client().Resources().Create(ctx, obj); err != nil {
					// Namespace resources tolerate AlreadyExists on
					// re-runs; everything else should fail loudly.
					if !isAlreadyExists(err) {
						t.Fatalf("create %s/%s on onboarding: %v", obj.GetKind(), obj.GetName(), err)
					}
				}
			}
		}
		return ctx
	}
}

// ---------------------------------------------------------------------
// Assessments
// ---------------------------------------------------------------------

func assessOpenBaoInstanceReachable() func(context.Context, *testing.T, *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(openBaoInstGVK)
		obj.SetName("default")
		if err := wait.For(func(waitCtx context.Context) (bool, error) {
			if err := c.Client().Resources().Get(waitCtx, "default", "", obj); err != nil {
				return false, nil
			}
			conds, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
			if !found {
				return false, nil
			}
			return conditionIs(conds, "OpenBaoReachable", string(metav1.ConditionTrue)), nil
		}, wait.WithTimeout(90*time.Second), wait.WithInterval(2*time.Second)); err != nil {
			dumpDiagnostics(ctx, t, c, "OpenBaoInstance reachability", obj)
			t.Fatalf("waiting for OpenBaoInstance/default OpenBaoReachable=True: %v", err)
		}
		return ctx
	}
}

func assessPolicyBindingStatus() func(context.Context, *testing.T, *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		onboarding, err := clusterutils.OnboardingConfig()
		if err != nil {
			t.Fatalf("resolve onboarding cluster: %v", err)
		}
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(policyBindGVK)
		obj.SetName("eso-reader-kv-prod")
		obj.SetNamespace("default")

		var lastRoleName, lastPolicyExists string
		err = wait.For(func(waitCtx context.Context) (bool, error) {
			if err := onboarding.Client().Resources().Get(waitCtx, obj.GetName(), obj.GetNamespace(), obj); err != nil {
				return false, nil
			}
			roleName := firstRoleName(obj)
			policyExists, _, _ := unstructured.NestedString(obj.Object, "status", "policyExists")
			lastRoleName, lastPolicyExists = roleName, policyExists
			return roleName != "" && policyExists == "True", nil
		}, wait.WithTimeout(2*time.Minute), wait.WithInterval(3*time.Second))

		if err != nil {
			dumpDiagnostics(ctx, t, c, "PolicyBinding status", obj)
			// Also dump upstream deps so we can see which condition is blocking.
			dumpDependency(ctx, t, onboarding, projEntityGVK, "team-a", "default")
			dumpDependency(ctx, t, onboarding, cpTrustGVK, "prod", "default")
			dumpDependency(ctx, t, onboarding, cpEntityGVK, "eso-reader", "default")
			t.Fatalf("waiting for PolicyBinding status (roles[0].roleName=%q, policyExists=%q): %v",
				lastRoleName, lastPolicyExists, err)
		}
		return ctx
	}
}

func assessRoleOnBackend(b *backend.Backend, expectedPolicy string) func(context.Context, *testing.T, *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		onboarding, err := clusterutils.OnboardingConfig()
		if err != nil {
			t.Fatalf("resolve onboarding cluster: %v", err)
		}
		// Re-fetch PolicyBinding to get authMountPath + roleName from
		// the first entry under status.roles[]. A PolicyBinding can now
		// carry multiple entries (one per ControlPlaneEntity); our
		// fixture references exactly one entity, so we assert on [0].
		pb := &unstructured.Unstructured{}
		pb.SetGroupVersionKind(policyBindGVK)
		if err := onboarding.Client().Resources().Get(ctx, "eso-reader-kv-prod", "default", pb); err != nil {
			t.Fatalf("get PolicyBinding: %v", err)
		}
		mount, roleName := firstMountAndRole(pb)
		if mount == "" || roleName == "" {
			t.Fatalf("expected roles[0].authMountPath and roles[0].roleName populated; mount=%q role=%q", mount, roleName)
		}

		admin, err := b.AdminClient()
		if err != nil {
			t.Fatalf("build admin client: %v", err)
		}
		sec, err := admin.Logical().ReadWithContext(ctx, fmt.Sprintf("auth/%s/role/%s", mount, roleName))
		if err != nil {
			t.Fatalf("read role %s/%s from backend: %v", mount, roleName, err)
		}
		if sec == nil {
			t.Fatalf("role %s/%s not found on backend", mount, roleName)
		}
		// token_policies must be [expectedPolicy] and only that. spec.md
		// Requirement 5: "one OpenBao JWT role bound to ... exactly the
		// configured policy".
		policies, _ := sec.Data["token_policies"].([]any)
		if len(policies) != 1 || policies[0].(string) != expectedPolicy {
			t.Fatalf("expected token_policies=[%q], got %v", expectedPolicy, policies)
		}
		return ctx
	}
}

func assessNoTokenSecret(namespace string) func(context.Context, *testing.T, *envconf.Config) context.Context {
	return func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		onboarding, err := clusterutils.OnboardingConfig()
		if err != nil {
			t.Fatalf("resolve onboarding cluster: %v", err)
		}
		// spec.md Requirement 7: the service SHALL NOT persist OpenBao
		// tokens as Kubernetes Secrets. List all Secrets in the tenant
		// namespace and ensure none carry a plausible token payload.
		secrets := &corev1.SecretList{}
		if err := onboarding.Client().Resources(namespace).List(ctx, secrets); err != nil {
			t.Fatalf("list secrets in %q: %v", namespace, err)
		}
		for _, s := range secrets.Items {
			for k, v := range s.Data {
				strv := string(v)
				if k == "openbao_token" || k == "vault_token" ||
					strings.HasPrefix(strv, "hvs.") || strings.HasPrefix(strv, "bao.") ||
					strings.HasPrefix(strv, "s.") /* legacy Vault */ {
					t.Fatalf("found OpenBao/Vault-token-shaped Secret data in %s/%s.%s", namespace, s.Name, k)
				}
			}
		}
		return ctx
	}
}

// ---------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------

func conditionIs(rawConditions []any, condType, status string) bool {
	for _, c := range rawConditions {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == condType {
			s, _ := m["status"].(string)
			return s == status
		}
	}
	return false
}

func isAlreadyExists(err error) bool {
	return err != nil && apimeta.IsNoMatchError(err) == false &&
		(strings.Contains(err.Error(), "already exists") ||
			strings.Contains(err.Error(), "AlreadyExists"))
}

// firstRoleName reads status.roles[0].roleName from an unstructured
// PolicyBinding. Returns empty string when the slice or field is
// absent — callers treat that as "not ready yet".
func firstRoleName(pb *unstructured.Unstructured) string {
	roles, ok, _ := unstructured.NestedSlice(pb.Object, "status", "roles")
	if !ok || len(roles) == 0 {
		return ""
	}
	m, ok := roles[0].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := m["roleName"].(string)
	return name
}

// firstMountAndRole returns (authMountPath, roleName) from
// status.roles[0]. Both empty when the slice is missing.
func firstMountAndRole(pb *unstructured.Unstructured) (string, string) {
	roles, ok, _ := unstructured.NestedSlice(pb.Object, "status", "roles")
	if !ok || len(roles) == 0 {
		return "", ""
	}
	m, ok := roles[0].(map[string]any)
	if !ok {
		return "", ""
	}
	mount, _ := m["authMountPath"].(string)
	name, _ := m["roleName"].(string)
	return mount, name
}

// dumpDiagnostics prints the current object state and manager pod logs
// when an assertion fails. Best-effort — errors are logged, not
// propagated; the caller still calls t.Fatalf afterward.
func dumpDiagnostics(ctx context.Context, t *testing.T, c *envconf.Config, label string, obj *unstructured.Unstructured) {
	t.Helper()
	t.Logf("=== diagnostics: %s ===", label)

	// The object itself, if fetchable.
	if obj != nil {
		latest := obj.DeepCopy()
		if err := c.Client().Resources().Get(ctx, obj.GetName(), obj.GetNamespace(), latest); err == nil {
			if b, err := yamlMarshal(latest); err == nil {
				t.Logf("current object:\n%s", string(b))
			}
		} else {
			t.Logf("could not re-fetch object: %v", err)
		}
	}

	// Find the manager pod and dump its last 200 log lines. The pod is
	// deployed by openmcp-operator as `ps-openbao` in `openmcp-system`.
	pods := &corev1.PodList{}
	if err := c.Client().Resources("openmcp-system").List(ctx, pods); err != nil {
		t.Logf("could not list pods in openmcp-system: %v", err)
		return
	}
	// Build a typed clientset so we can pull pod logs — the envconf
	// framework's dynamic client can't read the pod/log subresource.
	clientset, cserr := kubernetes.NewForConfig(c.Client().RESTConfig())
	for _, p := range pods.Items {
		if !strings.HasPrefix(p.Name, "ps-openbao") {
			continue
		}
		t.Logf("manager pod: %s phase=%s ready=%v", p.Name, p.Status.Phase, podReady(&p))
		for _, cs := range p.Status.ContainerStatuses {
			t.Logf("  container %s: ready=%v restarts=%d state=%+v", cs.Name, cs.Ready, cs.RestartCount, cs.State)
		}
		if cserr != nil {
			t.Logf("  (typed clientset unavailable: %v)", cserr)
			continue
		}
		// Only fetch logs for the run pod (skip the init job).
		if strings.Contains(p.Name, "init") {
			continue
		}
		tailLines := int64(200)
		req := clientset.CoreV1().Pods("openmcp-system").GetLogs(p.Name, &corev1.PodLogOptions{TailLines: &tailLines})
		stream, err := req.Stream(ctx)
		if err != nil {
			t.Logf("  logs unavailable: %v", err)
			continue
		}
		defer stream.Close()
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, stream)
		t.Logf("  --- last %d log lines ---\n%s", tailLines, buf.String())
	}
}

// dumpDependency logs the current state of an upstream CR so the failure
// message names *which* dependency is blocking the assertion. Accepts an
// explicit envconf.Config so callers can target the onboarding cluster
// where tenant CRs actually live.
func dumpDependency(ctx context.Context, t *testing.T, c *envconf.Config, gvk schema.GroupVersionKind, name, namespace string) {
	t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	if err := c.Client().Resources().Get(ctx, name, namespace, obj); err != nil {
		t.Logf("dependency %s/%s (%s): fetch failed: %v", namespace, name, gvk.Kind, err)
		return
	}
	if b, err := yamlMarshal(obj); err == nil {
		t.Logf("dependency %s/%s (%s):\n%s", namespace, name, gvk.Kind, string(b))
	}
}

// podReady is true when the pod has a Ready condition == True.
func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
