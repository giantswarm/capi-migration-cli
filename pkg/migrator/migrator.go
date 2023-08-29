package migrator

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/fatih/color"
	"github.com/giantswarm/microerror"

	"github.com/giantswarm/capi-migration-cli/cluster"
)

type Service struct {
	clusterInfo *cluster.Cluster
	vintageCRs  *VintageCRs
	app         AppInfo

	// AWS Clients
	ec2Client     *ec2.EC2
	route53Client *route53.Route53
	asgClient     *autoscaling.AutoScaling
}

type Config struct {
	Config *cluster.Cluster
}

type AppInfo struct {
	ClusterAppVersion  string
	ClusterAppCatalog  string
	DefaultAppsVersion string
	DefaultAppsCatalog string
}

func New(c Config) (*Service, error) {
	r := &Service{
		clusterInfo:   c.Config,
		ec2Client:     ec2.New(c.Config.AWSSession),
		route53Client: route53.New(c.Config.AWSSession),
		asgClient:     autoscaling.New(c.Config.AWSSession),
		app: AppInfo{
			ClusterAppVersion:  ClusterAppVersion,
			ClusterAppCatalog:  ClusterAppCatalog,
			DefaultAppsCatalog: DefaultAppsVersion,
			DefaultAppsVersion: DefaultAppsCatalog,
		},
	}

	return r, nil
}

func (s *Service) PrepareMigration(ctx context.Context) error {
	var err error
	color.Yellow("Preparation Phase")
	// fetch vintage CRs
	fmt.Printf("Fetching vintage CRs\n")
	s.vintageCRs, err = fetchVintageCRs(ctx, s.clusterInfo.MC.VintageKubernetesClient, s.clusterInfo.Name)
	if err != nil {
		fmt.Printf("Failed to fetch vintage CRs.\n")
		return microerror.Mask(err)
	}
	fmt.Printf("Migrating secrets to CAPI MC\n")
	// migrate secrets
	err = s.migrateSecrets(ctx)
	if err != nil {
		fmt.Printf("Failed to migrate secrets.\n")
		return microerror.Mask(err)
	}
	// create etcdJoinScript
	err = s.createEtcdJoinScriptSecret(ctx, s.vintageCRs.AwsCluster.Spec.Cluster.DNS.Domain)
	if err != nil {
		fmt.Printf("Failed to create etcd join script secret.\n")
		return microerror.Mask(err)
	}

	// migrate cluster account role
	err = s.migrateClusterAccountRole(ctx)
	if err != nil {
		fmt.Printf("Failed to migrate cluster account role.\n")
		return microerror.Mask(err)
	} else {
		fmt.Printf("Successfully migrated cluster IAM account role to AWSClusterRoleIdentity.\n")
	}
	fmt.Printf("Preparation phase completed.\n")

	return nil
}

func (s *Service) MigrationPhaseStopVintageReconciliation(ctx context.Context) error {
	color.Yellow("Stopping reconciliation of Vintage CRs")
	if s.vintageCRs == nil {
		return microerror.Maskf(executionFailedError, "vintage CRs cannot be nil")
	}
	err := stopVintageReconciliation(ctx, s.clusterInfo.MC.VintageKubernetesClient, s.vintageCRs)
	if err != nil {
		return microerror.Mask(err)
	}
	return nil
}

func (s *Service) MigrationPhaseCreateCapiCluster() error {

	return nil
}

func (s *Service) migrateSecrets(ctx context.Context) error {
	err := s.migrateCAsSecrets(ctx)
	if err != nil {
		return microerror.Mask(err)
	}
	err = s.migrateEncryptionSecret(ctx)
	if err != nil {
		return microerror.Mask(err)
	}
	err = s.migrateSASecret(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	return nil
}

func (s *Service) migrateClusterAccountRole(ctx context.Context) error {
	vintageIAMRole, err := fetchVintageClusterAccountRole(ctx, s.clusterInfo.MC.VintageKubernetesClient, s.vintageCRs.AwsCluster.Spec.Provider.CredentialSecret.Name, s.vintageCRs.AwsCluster.Spec.Provider.CredentialSecret.Namespace)
	if err != nil {
		return microerror.Mask(err)
	}

	err = s.createAWSClusterRoleIdentity(ctx, vintageIAMRole)
	if err != nil {
		return microerror.Mask(err)
	}

	return nil
}
