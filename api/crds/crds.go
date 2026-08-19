// SPDX-FileCopyrightText: Copyright OpenControlPlane contributors.
// SPDX-License-Identifier: Apache-2.0

// Package crds embeds the generated CRD manifests and exposes them as a
// CRDList consumable by controller-utils' CRDManager.
//
// The manifests/ directory is populated from config/crd/bases via `make
// copy-embedded-crds` (see Makefile). Keeping them physically separate
// from the kustomize-consumed copies avoids fighting kustomize path
// expectations at the embed edge.
package crds

import (
	"embed"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	crdutil "github.com/openmcp-project/controller-utils/pkg/crds"
)

//go:embed manifests
var CRDFS embed.FS

// CRDs returns every CRD manifest embedded under manifests/, parsed into
// apiextensions objects. The CRDManager routes each one to its target
// cluster by inspecting metadata.labels["openmcp.cloud/cluster"].
func CRDs() ([]*apiextv1.CustomResourceDefinition, error) {
	return crdutil.CRDsFromFileSystem(CRDFS, "manifests")
}
