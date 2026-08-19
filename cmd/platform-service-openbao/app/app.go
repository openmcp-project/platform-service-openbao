// SPDX-FileCopyrightText: Copyright OpenControlPlane contributors.
// SPDX-License-Identifier: Apache-2.0

// Package app wires the platform-service-openbao entry point. It follows
// the OpenMCP PlatformService contract used by platform-service-quota:
// a cobra command with `init` and `run` subcommands sharing persistent
// flags for the platform-cluster kubeconfig, environment, and provider
// name.
package app

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	"github.com/openmcp-project/controller-utils/pkg/logging"
)

// NewCommand returns the root cobra command. It has no runnable logic
// itself; all work happens under init/run subcommands so bootstrap steps
// (CRD install) are separable from the running manager.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "platform-service-openbao",
		Short: "platform-service-openbao configures OpenBao OIDC/JWT trust for OpenMCP ControlPlanes",
	}
	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stderr)

	so := &SharedOptions{
		RawSharedOptions: &RawSharedOptions{},
		PlatformCluster:  clusters.New("platform"),
	}
	so.AddPersistentFlags(cmd)
	cmd.AddCommand(NewInitCommand(so))
	cmd.AddCommand(NewRunCommand(so))

	return cmd
}

// RawSharedOptions captures the persistent flag values before they are
// resolved by Complete(). Kept as its own struct so PrintRawOptions can
// emit the pre-completion values for debugging.
type RawSharedOptions struct {
	Environment  string `json:"environment"`
	ProviderName string `json:"provider-name"`
	DryRun       bool   `json:"dry-run"`
}

// SharedOptions carries flag values plus fields derived from them. Both
// init and run embed this and read from it after calling Complete().
type SharedOptions struct {
	*RawSharedOptions
	PlatformCluster *clusters.Cluster

	// Fields below are populated by Complete().
	Log logging.Logger
}

// AddPersistentFlags registers flags that apply to every subcommand.
func (o *SharedOptions) AddPersistentFlags(cmd *cobra.Command) {
	logging.InitFlags(cmd.PersistentFlags())
	o.PlatformCluster.RegisterSingleConfigPathFlag(cmd.PersistentFlags())

	cmd.PersistentFlags().StringVar(&o.Environment, "environment", "",
		"Environment name. Required. Distinguishes environments watching the same platform cluster.")
	cmd.PersistentFlags().StringVar(&o.ProviderName, "provider-name", "",
		"Name of the ServiceConfig resource this instance reconciles against.")
	cmd.PersistentFlags().BoolVar(&o.DryRun, "dry-run", false,
		"If set, the command aborts after evaluating flags. Useful for validating configuration in CI.")
}

// Complete validates flag values and resolves derived fields (logger,
// platform-cluster REST config).
func (o *SharedOptions) Complete() error {
	if o.Environment == "" {
		return fmt.Errorf("--environment must not be empty")
	}
	if o.ProviderName == "" {
		return fmt.Errorf("--provider-name must not be empty")
	}

	log, err := logging.GetLogger()
	if err != nil {
		return err
	}
	o.Log = log
	ctrl.SetLogger(o.Log.Logr())

	if err := o.PlatformCluster.InitializeRESTConfig(); err != nil {
		return fmt.Errorf("initializing platform-cluster REST config: %w", err)
	}
	return nil
}

// PrintRawOptions emits the raw flag values before Complete(). Used by
// each subcommand for debug/audit output.
func (o *SharedOptions) PrintRawOptions(cmd *cobra.Command) {
	printJSON(cmd, "raw options", o.RawSharedOptions)
}

// PrintCompletedOptions emits the resolved values after Complete().
func (o *SharedOptions) PrintCompletedOptions(cmd *cobra.Command) {
	printJSON(cmd, "completed options", map[string]any{
		"environment":         o.Environment,
		"provider-name":       o.ProviderName,
		"dry-run":             o.DryRun,
		"platform-config":     o.PlatformCluster.ConfigPath(),
		"platform-config-set": o.PlatformCluster.HasRESTConfig(),
	})
}

func printJSON(cmd *cobra.Command, label string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		cmd.PrintErrln("could not marshal", label, err)
		return
	}
	cmd.Println(label + ":")
	cmd.Println(string(b))
}
