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

package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/openmcp-project/controller-utils/pkg/logging"
	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	openmcpconst "github.com/openmcp-project/openmcp-operator/api/constants"
	"github.com/openmcp-project/openmcp-operator/lib/clusteraccess"

	openbaov1alpha1 "github.com/openmcp-project/platform-service-openbao/api/v1alpha1"
	providerscheme "github.com/openmcp-project/platform-service-openbao/api/install"
	"github.com/openmcp-project/platform-service-openbao/internal/controller"
)

// RawRunOptions are the raw flag values for `run`. Kept alongside quota's
// naming so log output is comparable across services.
type RawRunOptions struct {
	MetricsAddr          string `json:"metrics-bind-address"`
	MetricsCertPath      string `json:"metrics-cert-path"`
	MetricsCertName      string `json:"metrics-cert-name"`
	MetricsCertKey       string `json:"metrics-cert-key"`
	WebhookCertPath      string `json:"webhook-cert-path"`
	WebhookCertName      string `json:"webhook-cert-name"`
	WebhookCertKey       string `json:"webhook-cert-key"`
	EnableLeaderElection bool   `json:"leader-elect"`
	ProbeAddr            string `json:"health-probe-bind-address"`
	PprofAddr            string `json:"pprof-bind-address"`
	SecureMetrics        bool   `json:"metrics-secure"`
	EnableHTTP2          bool   `json:"enable-http2"`
}

// RunOptions wires the manager. It embeds SharedOptions + RawRunOptions
// and resolves derived state in Complete().
type RunOptions struct {
	*SharedOptions
	RawRunOptions

	TLSOpts              []func(*tls.Config)
	WebhookTLSOpts       []func(*tls.Config)
	MetricsServerOptions metricsserver.Options
	MetricsCertWatcher   *certwatcher.CertWatcher
	WebhookCertWatcher   *certwatcher.CertWatcher
	ProviderNamespace    string
}

// NewRunCommand builds the `run` subcommand.
func NewRunCommand(so *SharedOptions) *cobra.Command {
	opts := &RunOptions{SharedOptions: so}
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the platform-service-openbao controller manager",
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
	opts.AddFlags(cmd)
	return cmd
}

// AddFlags registers the kubebuilder-standard flags for the run subcommand.
func (o *RunOptions) AddFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.MetricsAddr, "metrics-bind-address", "0",
		"Metrics endpoint address. Use :8443 for HTTPS, :8080 for HTTP, or 0 to disable.")
	cmd.Flags().StringVar(&o.ProbeAddr, "health-probe-bind-address", ":8081", "Health probe endpoint address.")
	cmd.Flags().StringVar(&o.PprofAddr, "pprof-bind-address", "", "pprof endpoint address (empty to disable).")
	cmd.Flags().BoolVar(&o.EnableLeaderElection, "leader-elect", false, "Enable leader election so at most one manager is active.")
	cmd.Flags().BoolVar(&o.SecureMetrics, "metrics-secure", true, "Serve metrics over HTTPS.")
	cmd.Flags().StringVar(&o.WebhookCertPath, "webhook-cert-path", "", "Directory containing the webhook certificate.")
	cmd.Flags().StringVar(&o.WebhookCertName, "webhook-cert-name", "tls.crt", "Webhook certificate file name.")
	cmd.Flags().StringVar(&o.WebhookCertKey, "webhook-cert-key", "tls.key", "Webhook key file name.")
	cmd.Flags().StringVar(&o.MetricsCertPath, "metrics-cert-path", "", "Directory containing the metrics server certificate.")
	cmd.Flags().StringVar(&o.MetricsCertName, "metrics-cert-name", "tls.crt", "Metrics certificate file name.")
	cmd.Flags().StringVar(&o.MetricsCertKey, "metrics-cert-key", "tls.key", "Metrics key file name.")
	cmd.Flags().BoolVar(&o.EnableHTTP2, "enable-http2", false, "Enable HTTP/2 on metrics and webhook servers.")
}

// Complete validates flags, resolves the ProviderNamespace (from
// POD_NAMESPACE), and prepares TLS/metrics options.
func (o *RunOptions) Complete(_ context.Context) error {
	if err := o.SharedOptions.Complete(); err != nil {
		return err
	}
	o.ProviderNamespace = os.Getenv(openmcpconst.EnvVariablePodNamespace)
	if o.ProviderNamespace == "" {
		return fmt.Errorf("environment variable %s must be set", openmcpconst.EnvVariablePodNamespace)
	}

	// HTTP/2 disabled by default per HTTP/2 Rapid Reset / Stream
	// Cancellation CVE advisories. Same posture as platform-service-quota.
	disableHTTP2 := func(c *tls.Config) { c.NextProtos = []string{"http/1.1"} }
	if !o.EnableHTTP2 {
		o.TLSOpts = append(o.TLSOpts, disableHTTP2)
	}
	o.WebhookTLSOpts = o.TLSOpts

	if o.WebhookCertPath != "" {
		w, err := certwatcher.New(
			filepath.Join(o.WebhookCertPath, o.WebhookCertName),
			filepath.Join(o.WebhookCertPath, o.WebhookCertKey),
		)
		if err != nil {
			return fmt.Errorf("initializing webhook certificate watcher: %w", err)
		}
		o.WebhookCertWatcher = w
		o.WebhookTLSOpts = append(o.WebhookTLSOpts, func(c *tls.Config) {
			c.GetCertificate = w.GetCertificate
		})
	}

	o.MetricsServerOptions = metricsserver.Options{
		BindAddress:   o.MetricsAddr,
		SecureServing: o.SecureMetrics,
		TLSOpts:       o.TLSOpts,
	}
	if o.SecureMetrics {
		o.MetricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}
	if o.MetricsCertPath != "" {
		w, err := certwatcher.New(
			filepath.Join(o.MetricsCertPath, o.MetricsCertName),
			filepath.Join(o.MetricsCertPath, o.MetricsCertKey),
		)
		if err != nil {
			return fmt.Errorf("initializing metrics certificate watcher: %w", err)
		}
		o.MetricsCertWatcher = w
		o.MetricsServerOptions.TLSOpts = append(o.MetricsServerOptions.TLSOpts, func(c *tls.Config) {
			c.GetCertificate = w.GetCertificate
		})
	}
	return nil
}

// Run is the main entry point. Ownership graph:
//   - platform cluster hosts OpenBaoInstance + ServiceConfig
//   - onboarding cluster hosts ProjectEntity, ControlPlaneTrust,
//     ControlPlaneEntity, PolicyBinding
//   - the controller-runtime Manager is anchored to the onboarding
//     cluster; the platform cluster is joined as a secondary source with
//     mgr.Add so its cache is available to reconcilers.
func (o *RunOptions) Run(ctx context.Context) error {
	setupLog := o.Log.WithName("setup")

	// Wire the platform cluster with its full scheme so we can Get the
	// ServiceConfig on startup and List OpenBaoInstances at runtime.
	if err := o.PlatformCluster.InitializeClient(
		providerscheme.InstallOperatorAPIsPlatform(runtime.NewScheme()),
	); err != nil {
		return fmt.Errorf("initializing platform-cluster client: %w", err)
	}
	setupLog.Info("Environment", "value", o.Environment)
	setupLog.Info("ProviderName", "value", o.ProviderName)

	// Startup guard: fetch the ServiceConfig now so an obvious
	// misconfiguration crashes the pod rather than making it look healthy
	// but idle.
	svcCfg := &openbaov1alpha1.ServiceConfig{}
	svcCfg.Name = o.ProviderName
	if err := o.PlatformCluster.Client().Get(ctx, client.ObjectKeyFromObject(svcCfg), svcCfg); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("ServiceConfig %q not found on the platform cluster", svcCfg.Name)
		}
		return fmt.Errorf("fetching ServiceConfig %q: %w", svcCfg.Name, err)
	}
	if err := svcCfg.Spec.Validate(); err != nil {
		return fmt.Errorf("ServiceConfig %q is invalid: %w", svcCfg.Name, err)
	}

	// Request access to the onboarding cluster with the RBAC the
	// steady-state reconcilers need: full control over our four tenant
	// CRDs, plus read access to ServiceAccounts inside ControlPlanes for
	// identity verification.
	onboardingScheme := providerscheme.InstallOperatorAPIsOnboarding(runtime.NewScheme())
	accessMgr := clusteraccess.NewClusterAccessManager(
		o.PlatformCluster.Client(), o.ProviderName, o.ProviderNamespace,
	).WithLogger(&setupLog).WithInterval(10 * time.Second).WithTimeout(30 * time.Minute)

	onboardingCluster, err := accessMgr.CreateAndWaitForCluster(
		ctx,
		clustersv1alpha1.PURPOSE_ONBOARDING,
		clustersv1alpha1.PURPOSE_ONBOARDING,
		onboardingScheme,
		[]clustersv1alpha1.PermissionsRequest{{
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{openbaov1alpha1.SchemeGroupVersion.Group},
					Resources: []string{
						"projectentities", "projectentities/status", "projectentities/finalizers",
						"controlplanetrusts", "controlplanetrusts/status", "controlplanetrusts/finalizers",
						"controlplaneentities", "controlplaneentities/status", "controlplaneentities/finalizers",
						"policybindings", "policybindings/status", "policybindings/finalizers",
					},
					Verbs: []string{"*"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"events"},
					Verbs:     []string{"create", "patch"},
				},
			},
		}},
	)
	if err != nil {
		return fmt.Errorf("acquiring onboarding-cluster access: %w", err)
	}

	webhookServer := webhook.NewServer(webhook.Options{TLSOpts: o.WebhookTLSOpts})

	mgr, err := ctrl.NewManager(onboardingCluster.RESTConfig(), ctrl.Options{
		Scheme:                 onboardingCluster.Scheme(),
		Metrics:                o.MetricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: o.ProbeAddr,
		PprofBindAddress:       o.PprofAddr,
		LeaderElection:         o.EnableLeaderElection,
		LeaderElectionID:       "platform-service-openbao.openbao.open-control-plane.io",
	})
	if err != nil {
		return fmt.Errorf("creating manager: %w", err)
	}
	// Join the platform cluster's cache to the manager so reconcilers can
	// watch OpenBaoInstance/ServiceConfig via WatchesRawSource.
	if err := mgr.Add(o.PlatformCluster.Cluster()); err != nil {
		return fmt.Errorf("adding platform cluster to manager: %w", err)
	}

	// Wire the six reconcilers. Each carries both cluster handles; the
	// reconciler chooses which cluster's client to use per resource type.
	if err := controller.NewOpenBaoInstanceReconciler(o.PlatformCluster, o.ProviderName).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setting up OpenBaoInstance controller: %w", err)
	}
	if err := controller.NewProjectEntityReconciler(o.PlatformCluster, onboardingCluster, o.ProviderName).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setting up ProjectEntity controller: %w", err)
	}
	if err := controller.NewControlPlaneTrustReconciler(o.PlatformCluster, onboardingCluster, o.ProviderName).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setting up ControlPlaneTrust controller: %w", err)
	}
	if err := controller.NewControlPlaneEntityReconciler(o.PlatformCluster, onboardingCluster, o.ProviderName).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setting up ControlPlaneEntity controller: %w", err)
	}
	if err := controller.NewPolicyBindingReconciler(o.PlatformCluster, onboardingCluster, o.ProviderName).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setting up PolicyBinding controller: %w", err)
	}

	if o.MetricsCertWatcher != nil {
		if err := mgr.Add(o.MetricsCertWatcher); err != nil {
			return fmt.Errorf("adding metrics cert watcher: %w", err)
		}
	}
	if o.WebhookCertWatcher != nil {
		if err := mgr.Add(o.WebhookCertWatcher); err != nil {
			return fmt.Errorf("adding webhook cert watcher: %w", err)
		}
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("setting up healthz: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("setting up readyz: %w", err)
	}

	// controller-runtime's logger only picks up assignments made before
	// SetLogger. logging.Complete already called SetLogger; explicit
	// reference silences the "logger not set" nag.
	_ = logging.FromContextOrDiscard(ctx)

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("running manager: %w", err)
	}
	return nil
}
