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
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/openmcp-project/controller-utils/pkg/clusters"

	openbaov1alpha1 "github.com/openmcp-project/platform-service-openbao/api/v1alpha1"
)

// Envtest fixtures for the reconciler suite. Two clusters mirror
// production: platform (OpenBaoInstance + ServiceConfig) and onboarding
// (tenant CRDs). Both point at the same generated CRD manifests.

var (
	ctx    context.Context
	cancel context.CancelFunc

	platformEnv         *envtest.Environment
	onboardingEnv       *envtest.Environment
	platformCfg         *rest.Config
	onboardingCfg       *rest.Config
	platformCluster     *clusters.Cluster
	onboardingCluster   *clusters.Cluster
	platformK8sClient   client.Client
	onboardingK8sClient client.Client

	testScheme = runtime.NewScheme()
)

const testProviderName = "default"

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	ctx, cancel = context.WithCancel(context.TODO())

	utilruntime.Must(clientgoscheme.AddToScheme(testScheme))
	utilruntime.Must(openbaov1alpha1.AddToScheme(testScheme))

	crdPaths := []string{filepath.Join("..", "..", "config", "crd", "bases")}
	binDir := getFirstFoundEnvTestBinaryDir()

	platformEnv = &envtest.Environment{
		CRDDirectoryPaths:     crdPaths,
		ErrorIfCRDPathMissing: true,
	}
	onboardingEnv = &envtest.Environment{
		CRDDirectoryPaths:     crdPaths,
		ErrorIfCRDPathMissing: true,
	}
	if binDir != "" {
		platformEnv.BinaryAssetsDirectory = binDir
		onboardingEnv.BinaryAssetsDirectory = binDir
	}

	var err error
	platformCfg, err = platformEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(platformCfg).NotTo(BeNil())

	onboardingCfg, err = onboardingEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(onboardingCfg).NotTo(BeNil())

	platformK8sClient, err = client.New(platformCfg, client.Options{Scheme: testScheme})
	Expect(err).NotTo(HaveOccurred())
	onboardingK8sClient, err = client.New(onboardingCfg, client.Options{Scheme: testScheme})
	Expect(err).NotTo(HaveOccurred())

	// Build clusters.Cluster handles the reconcilers expect.
	platformCluster = clusters.NewTestClusterFromClient("platform", platformK8sClient)
	platformCluster.WithRESTConfig(platformCfg)
	Expect(platformCluster.InitializeClient(testScheme)).To(Succeed())

	onboardingCluster = clusters.NewTestClusterFromClient("onboarding", onboardingK8sClient)
	onboardingCluster.WithRESTConfig(onboardingCfg)
	Expect(onboardingCluster.InitializeClient(testScheme)).To(Succeed())

	// Every reconcile reads a ServiceConfig from the platform cluster.
	// Seed one so the reconcilers don't fail on that lookup.
	svcCfg := &openbaov1alpha1.ServiceConfig{}
	svcCfg.Name = testProviderName
	svcCfg.Spec.RequeueAfter = "1s"
	Expect(platformK8sClient.Create(ctx, svcCfg)).To(Succeed())

	// Seed the `default` namespace on the onboarding cluster.
	ns := &corev1.Namespace{}
	ns.Name = "default"
	if err := onboardingK8sClient.Create(ctx, ns); err != nil && !isAlreadyExists(err) {
		Fail("creating default namespace on onboarding cluster: " + err.Error())
	}
})

var _ = AfterSuite(func() {
	By("tearing down envtest environments")
	cancel()
	Eventually(func() error {
		if platformEnv != nil {
			if err := platformEnv.Stop(); err != nil {
				return err
			}
		}
		if onboardingEnv != nil {
			return onboardingEnv.Stop()
		}
		return nil
	}, time.Minute, time.Second).Should(Succeed())
})

// getFirstFoundEnvTestBinaryDir locates the envtest binaries under
// ./bin/k8s/<version>. Kept from the kubebuilder scaffold so IDE runs
// still find them.
func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}

// isAlreadyExists is a tiny helper: we don't want to import the apierrors
// package just for the namespace-seed check.
func isAlreadyExists(err error) bool {
	return err != nil && (err.Error() == `namespaces "default" already exists`)
}
