/*
Copyright 2023 Giant Swarm.

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

package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/giantswarm/microerror"
	flag "github.com/spf13/pflag"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	"github.com/giantswarm/capi-migration-cli/cluster"
	"github.com/giantswarm/capi-migration-cli/pkg/migrator"
)

var flags = struct {
	MCVintage        string
	MCCapi           string
	ClusterName      string
	ClusterNamespace string
	Phase            string
}{}

func initFlags() (errors []error) {
	// Flag/configuration names.
	const (
		flagMCVintage        = "mc-vintage"
		flagMCCapi           = "mc-capi"
		flagClusterName      = "cluster-name"
		flagClusterNamespace = "cluster-namespace"
		flagPhase            = "phase"
	)

	// Flag binding.
	flag.StringVar(&flags.MCVintage, flagMCVintage, "", "Name of the installation where the cluster will be migrated from.")
	flag.StringVar(&flags.MCCapi, flagMCCapi, "", "Name of the installation where the cluster will be migrated to.")
	flag.StringVar(&flags.ClusterName, flagClusterName, "", "Cluster name/ID")
	flag.StringVar(&flags.ClusterNamespace, flagClusterNamespace, "", "Namespace where the Cluster CRs are")
	flag.StringVar(&flags.Phase, flagPhase, "all", "Define which phase should run, allowed values ['prepare','stop-reconciliation','create-capi','all'] default 'all'")

	// Parse flags and configuration.
	flag.Parse()
	// Validation.
	if flags.MCVintage == "" {
		errors = append(errors, fmt.Errorf("--%s flag must be set", flagMCVintage))
	}
	if flags.MCCapi == "" {
		errors = append(errors, fmt.Errorf("--%s flag must be set", flagMCCapi))
	}
	if flags.ClusterName == "" {
		errors = append(errors, fmt.Errorf("--%s flag must be set", flagClusterName))
	}
	if flags.ClusterNamespace == "" {
		errors = append(errors, fmt.Errorf("--%s flag must be set", flagClusterNamespace))
	}

	return
}

func main() {
	errs := initFlags()
	if len(errs) > 0 {
		ss := make([]string, len(errs))
		for i := range errs {
			ss[i] = errs[i].Error()
		}
		fmt.Fprintf(os.Stderr, "Error: %s\n", strings.Join(ss, "\nError: "))
		os.Exit(2)
	}

	err := mainE(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", microerror.Pretty(err, true))
		os.Exit(1)
	}
}

func mainE(ctx context.Context) error {
	var err error

	if flags.Phase != "all" {
		color.Red("WARNING: flag '--phase=all' is only implemented for now.")
	}

	var configService *cluster.Cluster
	{
		c := cluster.Config{
			MCCapi:           flags.MCCapi,
			MCVintage:        flags.MCVintage,
			ClusterName:      flags.ClusterName,
			ClusterNamespace: flags.ClusterNamespace,
		}

		configService, err = cluster.New(c)
		if err != nil {
			fmt.Printf("Failed to initialize necessery clients for the capi-migration-cli\n")
			return microerror.Mask(err)
		}
	}

	var migratorService *migrator.Service
	{
		c := migrator.Config{
			Config: configService,
		}

		migratorService, err = migrator.New(c)
		if err != nil {
			fmt.Printf("Failed to initialize migratorService\n")
			return microerror.Mask(err)
		}
	}

	fmt.Printf("Starting migration of cluster %s from %s to %s\n\n", color.YellowString(configService.Name), color.YellowString(configService.MC.VintageMC), color.YellowString(configService.MC.CapiMC))

	err = migratorService.PrepareMigration(ctx)
	if err != nil {
		fmt.Printf("Failed to prepare migration\n")
		return microerror.Mask(err)
	}

	err = migratorService.GenerateCAPIClusterTemplates(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	/*
		err = migratorService.MigrationPhaseStopVintageReconciliation(ctx)
		if err != nil {
			fmt.Printf("Failed to stop reconciliation on vintage cluster\n")
			return microerror.Mask(err)
		}*/

	return nil
}
