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
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	"github.com/openmcp-project/controller-utils/pkg/logging"
	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	commonapi "github.com/openmcp-project/openmcp-operator/api/common"
	openmcpconst "github.com/openmcp-project/openmcp-operator/api/constants"
	corev2alpha1 "github.com/openmcp-project/openmcp-operator/api/core/v2alpha1"
	"github.com/openmcp-project/openmcp-operator/lib/clusteraccess"
	libutils "github.com/openmcp-project/openmcp-operator/lib/utils"

	openbaov1alpha1 "github.com/openmcp-project/platform-service-openbao/api/v1alpha1"
)

const controlPlaneJWTAudience = "open-control-plane-platform-service-openbao"

func resolveControlPlaneAudience(audience string) string {
	if strings.TrimSpace(audience) != "" {
		return strings.TrimSpace(audience)
	}
	return controlPlaneJWTAudience
}

func getControlPlane(ctx context.Context, c client.Client, key types.NamespacedName) (*corev2alpha1.ControlPlane, error) {
	cp := &corev2alpha1.ControlPlane{}
	if err := c.Get(ctx, key, cp); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("fetching ControlPlane %s: %w", key.String(), err)
	}
	return cp, nil
}

func controlPlaneIssuer(cp *corev2alpha1.ControlPlane) string {
	if cp == nil {
		return ""
	}
	for _, ep := range cp.Status.Endpoints {
		if ep.Name == "service-account-issuer" {
			return strings.TrimRight(ep.URL, "/")
		}
	}
	return ""
}

func controlPlaneClusterAccess(ctx context.Context, platformCluster *clusters.Cluster, providerName string, cp *corev2alpha1.ControlPlane, permissions []clustersv1alpha1.PermissionsRequest) (*clusters.Cluster, error) {
	if cp == nil {
		return nil, fmt.Errorf("controlplane is nil")
	}
	providerNamespace := os.Getenv(openmcpconst.EnvVariablePodNamespace)
	if providerNamespace == "" {
		return nil, fmt.Errorf("environment variable %s must be set to create ControlPlane AccessRequest", openmcpconst.EnvVariablePodNamespace)
	}
	mcpNamespace, err := libutils.StableMCPNamespace(cp.Name, cp.Namespace)
	if err != nil {
		return nil, err
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("registering client-go scheme: %w", err)
	}
	log := logging.FromContextOrDiscard(ctx).WithName("controlplaneaccess")
	manager := clusteraccess.NewClusterAccessManager(platformCluster.Client(), providerName, providerNamespace).
		WithLogger(&log).
		WithInterval(5 * time.Second).
		WithTimeout(2 * time.Minute)
	localName := "mcp-" + cp.Namespace + "-" + cp.Name
	cluster, _, err := manager.WaitForClusterAccess(ctx, localName, scheme, &commonapi.ObjectReference{
		Name:      cp.Name,
		Namespace: mcpNamespace,
	}, clusteraccess.ReferenceToClusterRequest, permissions)
	if err != nil {
		return nil, err
	}
	return cluster, nil
}

func serviceAccountIdentityPermissions(saNamespace string) []clustersv1alpha1.PermissionsRequest {
	return []clustersv1alpha1.PermissionsRequest{{
		Namespace:                         saNamespace,
		DisableAutomaticNamespaceCreation: true,
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"serviceaccounts"},
				Verbs:     []string{"get"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"serviceaccounts/token"},
				Verbs:     []string{"create"},
			},
		},
	}}
}

func resolveServiceAccountIdentity(ctx context.Context, mcpCluster *clusters.Cluster, cpNamespace, cpName, namespace, name, audience string) (*openbaov1alpha1.ServiceAccountIdentity, string, error) {
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(name) == "" {
		return nil, "", fmt.Errorf("serviceAccountRef namespace and name are required")
	}
	clientset, err := kubernetes.NewForConfig(mcpCluster.RESTConfig())
	if err != nil {
		return nil, "", fmt.Errorf("building ControlPlane client: %w", err)
	}
	if _, err := clientset.CoreV1().ServiceAccounts(namespace).Get(ctx, name, metav1.GetOptions{}); err != nil {
		return nil, "", fmt.Errorf("resolving ServiceAccount %s/%s: %w", namespace, name, err)
	}
	exp := int64(600)
	tok, err := clientset.CoreV1().ServiceAccounts(namespace).CreateToken(ctx, name, &authv1.TokenRequest{
		Spec: authv1.TokenRequestSpec{
			Audiences:         []string{audience},
			ExpirationSeconds: &exp,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, "", fmt.Errorf("requesting token for ServiceAccount %s/%s: %w", namespace, name, err)
	}
	identity, err := parseServiceAccountJWT(tok.Status.Token)
	if err != nil {
		return nil, "", err
	}
	identity.Alias = controlPlaneEntityAlias(cpNamespace, cpName, namespace, name)
	identityID := deterministicIdentityID(identity)
	return identity, identityID, nil
}

func parseServiceAccountJWT(token string) (*openbaov1alpha1.ServiceAccountIdentity, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("service account token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decoding service account JWT payload: %w", err)
	}
	var claims struct {
		Issuer   string `json:"iss"`
		Subject  string `json:"sub"`
		Audience any    `json:"aud"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("parsing service account JWT claims: %w", err)
	}
	return &openbaov1alpha1.ServiceAccountIdentity{
		Subject:   claims.Subject,
		Issuer:    claims.Issuer,
		Audiences: normalizeAudiences(claims.Audience),
	}, nil
}

func normalizeAudiences(raw any) []string {
	switch v := raw.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func controlPlaneEntityAlias(cpNamespace, cpName, serviceAccountNamespace, serviceAccountName string) string {
	project, workspace := projectWorkspaceFromNamespace(cpNamespace)
	return strings.Join([]string{"ocp", project, workspace, cpName, serviceAccountNamespace, serviceAccountName}, ":")
}

func projectWorkspaceFromNamespace(namespace string) (string, string) {
	projectPart, workspacePart, ok := strings.Cut(namespace, "--ws-")
	if !ok {
		return namespace, ""
	}
	project := strings.TrimPrefix(projectPart, "project-")
	return project, workspacePart
}

func deterministicIdentityID(identity *openbaov1alpha1.ServiceAccountIdentity) string {
	if identity == nil {
		return ""
	}
	audiences := append([]string(nil), identity.Audiences...)
	sort.Strings(audiences)
	sum := sha256.Sum256([]byte(identity.Alias + "\x00" + identity.Issuer + "\x00" + identity.Subject + "\x00" + strings.Join(audiences, ",")))
	return hex.EncodeToString(sum[:])[:16]
}

var _ = corev1.ServiceAccount{}
