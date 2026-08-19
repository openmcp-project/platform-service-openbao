// SPDX-FileCopyrightText: Copyright OpenControlPlane contributors.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"

	crdutil "github.com/openmcp-project/controller-utils/pkg/crds"
	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	openmcpconst "github.com/openmcp-project/openmcp-operator/api/constants"
	"github.com/openmcp-project/openmcp-operator/lib/clusteraccess"

	obcrds "github.com/openmcp-project/platform-service-openbao/api/crds"
	providerscheme "github.com/openmcp-project/platform-service-openbao/api/install"
)

// InitOptions are the flag/state carriers for `platform-service-openbao
// init`. It requests access to the onboarding cluster via an
// AccessRequest, then installs the embedded CRDs on both the platform and
// onboarding clusters according to each CRD's openmcp.cloud/cluster
// label.
type InitOptions struct {
	*SharedOptions
}

// NewInitCommand builds the `init` subcommand.
func NewInitCommand(so *SharedOptions) *cobra.Command {
	opts := &InitOptions{SharedOptions: so}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Install platform-service-openbao CRDs on the platform and onboarding clusters",
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.PrintRawOptions(cmd)
			if err := opts.Complete(cmd.Context()); err != nil {
				return fmt.Errorf("completing options: %w", err)
			}
			opts.PrintCompletedOptions(cmd)
			if opts.DryRun {
				cmd.Println("=== END OF DRY RUN ===")
				return nil
			}
			return opts.Run(cmd.Context())
		},
	}
	return cmd
}

// Complete resolves options for init. Currently just delegates to shared.
func (o *InitOptions) Complete(_ context.Context) error {
	return o.SharedOptions.Complete()
}

// Run performs the init workflow:
//  1. Build a platform-cluster client with the CRD scheme + operator APIs.
//  2. Obtain access to the onboarding cluster via an AccessRequest (with
//     just enough RBAC to CRUD CRDs there).
//  3. Apply the embedded CRDs to whichever cluster each is labelled for.
func (o *InitOptions) Run(ctx context.Context) error {
	platformScheme := runtime.NewScheme()
	providerscheme.InstallOperatorAPIsPlatform(platformScheme)
	providerscheme.InstallCRDAPIs(platformScheme)
	if err := o.PlatformCluster.InitializeClient(platformScheme); err != nil {
		return fmt.Errorf("initializing platform-cluster client: %w", err)
	}

	log := o.Log.WithName("init")
	log.Info("Starting", "environment", o.Environment, "providerName", o.ProviderName)

	onboardingScheme := runtime.NewScheme()
	providerscheme.InstallOperatorAPIsOnboarding(onboardingScheme)
	providerscheme.InstallCRDAPIs(onboardingScheme)

	providerSystemNamespace := os.Getenv(openmcpconst.EnvVariablePodNamespace)
	if providerSystemNamespace == "" {
		return fmt.Errorf("environment variable %s is not set", openmcpconst.EnvVariablePodNamespace)
	}

	accessMgr := clusteraccess.NewClusterAccessManager(
		o.PlatformCluster.Client(), o.ProviderName, providerSystemNamespace,
	).WithLogger(&log).WithInterval(10 * time.Second).WithTimeout(30 * time.Minute)

	// Init only needs CRD CRUD on the onboarding cluster. The steady-state
	// run subcommand will re-request with the full permission set it
	// needs.
	onboardingCluster, err := accessMgr.CreateAndWaitForCluster(
		ctx,
		clustersv1alpha1.PURPOSE_ONBOARDING+"-init",
		clustersv1alpha1.PURPOSE_ONBOARDING,
		onboardingScheme,
		[]clustersv1alpha1.PermissionsRequest{{
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{"apiextensions.k8s.io"},
				Resources: []string{"customresourcedefinitions"},
				Verbs:     []string{"*"},
			}},
		}},
	)
	if err != nil {
		return fmt.Errorf("acquiring onboarding-cluster access: %w", err)
	}

	log.Info("Applying CRDs")
	mgr := crdutil.NewCRDManager(openmcpconst.ClusterLabel, obcrds.CRDs)
	mgr.AddCRDLabelToClusterMapping(clustersv1alpha1.PURPOSE_PLATFORM, o.PlatformCluster)
	mgr.AddCRDLabelToClusterMapping(clustersv1alpha1.PURPOSE_ONBOARDING, onboardingCluster)
	if err := mgr.CreateOrUpdateCRDs(ctx, &log); err != nil {
		return fmt.Errorf("applying CRDs: %w", err)
	}

	log.Info("Init complete")
	return nil
}
