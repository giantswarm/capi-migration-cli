package migrator

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/elb"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/fatih/color"
	"github.com/giantswarm/microerror"

	"github.com/giantswarm/capi-migration-cli/cluster"
)

const (
	ControlPlaneRole = "control-plane"
	WorkerRole       = "worker"
)

type Service struct {
	clusterInfo *cluster.Cluster
	vintageCRs  *VintageCRs
	app         AppInfo

	// AWS Clients
	ec2Client     *ec2.EC2
	elbClient     *elb.ELB
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
		clusterInfo: c.Config,
		app: AppInfo{
			ClusterAppCatalog:  ClusterAppCatalog,
			ClusterAppVersion:  ClusterAppVersion,
			DefaultAppsCatalog: DefaultAppsCatalog,
			DefaultAppsVersion: DefaultAppsVersion,
		},
	}

	r.CreateAWSClients(c.Config.AWSSession)

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
	err = s.createScriptsSecret(ctx, s.vintageCRs.AwsCluster.Spec.Cluster.DNS.Domain)
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
	// disable vintage health check
	err = disableVintageHealthCheck(ctx, s.clusterInfo.MC.VintageKubernetesClient, s.vintageCRs)
	if err != nil {
		fmt.Printf("Failed to disable vintage health check.\n")
		return microerror.Mask(err)
	} else {
		fmt.Printf("Successfully disabled vintage machine health check.\n")
	}

	// scale down app operator deployment
	err = scaleDownVintageAppOperator(ctx, s.clusterInfo.MC.VintageKubernetesClient, s.clusterInfo.Name)
	if err != nil {
		fmt.Printf("Failed to scale down app operator deployment.\n")
		return microerror.Mask(err)
	} else {
		fmt.Printf("Successfully scaled down Vintage app operator deployment.\n")
	}

	color.Green("Preparation phase completed.\n\n")

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

func (s *Service) MigrationPhaseProvisionCAPICluster(ctx context.Context) error {
	err := s.applyCAPICluster()
	if err != nil {
		return microerror.Mask(err)
	}

	err = s.addNewControlPlaneNodesToVintageELBs()
	if err != nil {
		return microerror.Mask(err)
	}

	s.waitForCapiNodesReady(ctx, ControlPlaneRole, 1)

	err = s.cleanEtcdInKubeadmConfigMap(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	// reapply the updated configmap
	err = s.applyCAPICluster()
	if err != nil {
		return microerror.Mask(err)
	}

	// wait for all CAPI control plane nodes to be ready
	s.waitForCapiNodesReady(ctx, ControlPlaneRole, 3)

	return nil
}

func (s *Service) MigrationPhaseCleanVintageCluster(ctx context.Context) error {
	fmt.Printf("Draining all vintage control plane nodes\n")
	err := s.drainVintageNodes(ctx, "control-plane")
	if err != nil {
		return microerror.Mask(err)
	}

	// refresh AWS credentials in case they expired
	err = s.clusterInfo.RefreshAWSCredentials()
	if err != nil {
		return microerror.Mask(err)
	}
	s.CreateAWSClients(s.clusterInfo.AWSSession)

	fmt.Printf("Deleting vintage control plane ASGs\n")
	err = s.deleteVintageASGGroups("tccpn")
	if err != nil {
		return microerror.Mask(err)
	}
	fmt.Printf("Deleted vintage control plane ASGs\n")

	/*
		for _, mp := range s.vintageCRs.AwsMachineDeployments {
			s.waitForCapiNodesReady(ctx, WorkerRole, mp.Spec.NodePool.Scaling.Min)
			err = s.drainVintageNodes(ctx, "worker")
			if err != nil {
				return microerror.Mask(err)
			}
		}
		err = s.deleteVintageASGGroups("tcnp")
		if err != nil {
			return microerror.Mask(err)
		}*/

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
