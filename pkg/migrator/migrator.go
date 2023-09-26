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
	"sigs.k8s.io/controller-runtime/pkg/client"

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

func (s *Service) StopVintageReconciliation(ctx context.Context) error {
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

func (s *Service) ProvisionCAPICluster(ctx context.Context) error {
	err := s.GenerateCAPIClusterTemplates(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	err = s.applyCAPICluster()
	if err != nil {
		return microerror.Mask(err)
	}

	err = s.addCAPIControlPlaneNodesToVintageELBs()
	if err != nil {
		return microerror.Mask(err)
	}

	// wait for first CAPI control plane node to be ready
	err = s.waitForCapiControlPlaneNodesReady(ctx, 1)
	if err != nil {
		return microerror.Mask(err)
	}

	err = s.cleanEtcdInKubeadmConfigMap(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	// reapply the updated configmap
	err = s.applyCAPICluster()
	if err != nil {
		return microerror.Mask(err)
	}

	err = s.stopVintageControlPlaneComponents(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	// cordon all vintage control plane nodes
	err = s.cordonVintageNodes(ctx, controlPlaneNodeLabels())
	if err != nil {
		return microerror.Mask(err)
	}

	// delete chart-operator pod in the WC cluster to reschedule it on new CAPi control-plane-node
	err = s.deleteChartOperatorPods(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	// wait for all CAPI control plane nodes to be ready
	err = s.waitForCapiControlPlaneNodesReady(ctx, 3)
	if err != nil {
		return microerror.Mask(err)
	}
	// now add the remaining nodes to vintage ELBs
	err = s.addCAPIControlPlaneNodesToVintageELBs()
	if err != nil {
		return microerror.Mask(err)
	}

	return nil
}

// CleanVintageCluster drains all vintage nodes and deletes the ASGs
func (s *Service) CleanVintageCluster(ctx context.Context) error {
	color.Yellow("Draining all vintage control plane nodes")
	err := s.drainVintageNodes(ctx, controlPlaneNodeLabels())
	if err != nil {
		return microerror.Mask(err)
	}

	color.Yellow("Deleting vintage control plane ASGs\n")
	err = s.deleteVintageASGGroups(tccpnAsgFilters())
	if err != nil {
		return microerror.Mask(err)
	}
	color.Yellow("Deleted vintage control plane ASGs\n")

	for _, mp := range s.vintageCRs.AwsMachineDeployments {
		err = s.waitForNodePoolNodesReady(ctx, mp.Name)
		if err != nil {
			return microerror.Mask(err)
		}
		color.Yellow("Draining all vintage worker nodes for nodepool %s", mp.Name)
		err = s.drainVintageNodes(ctx, nodePoolNodeLabels(mp.Name))
		if err != nil {
			return microerror.Mask(err)
		}
		color.Yellow("Deleting vintage  %s node pool ASG\n", mp.Name)
		err = s.deleteVintageASGGroups(tcnpAsgFilters(mp.Name))
		if err != nil {
			return microerror.Mask(err)
		}
		color.Yellow("Deleted vintage %s node pool ASG\n", mp.Name)
	}

	return nil
}

func controlPlaneNodeLabels() client.MatchingLabels {
	return client.MatchingLabels{"node-role.kubernetes.io/control-plane": ""}
}

func nodePoolNodeLabels(nodePoolName string) client.MatchingLabels {
	return client.MatchingLabels{
		"node-role.kubernetes.io/control-plane": "",
		"giantswarm.io/machine-deployment":      nodePoolName,
	}
}
