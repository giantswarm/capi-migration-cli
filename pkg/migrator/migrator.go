package migrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/elb"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/fatih/color"
	"github.com/giantswarm/backoff"
	"github.com/giantswarm/microerror"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/giantswarm/capi-migration-cli/cluster"
)

type Service struct {
	clusterInfo    *cluster.Cluster
	vintageCRs     *VintageCRs
	app            AppInfo
	workerBachSize int

	// AWS Clients
	ec2Client     *ec2.EC2
	elbClient     *elb.ELB
	route53Client *route53.Route53
	asgClient     *autoscaling.AutoScaling

	// backoff
	backOff backoff.BackOff
}

type Config struct {
	Config         *cluster.Cluster
	WorkerBachSize int
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
		backOff:        backoff.NewMaxRetries(15, 3*time.Second),
		workerBachSize: c.WorkerBachSize,
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

	// migrate cluster AWS default apps values
	err = s.migrateDefaultAppsValues(ctx)
	if err != nil {
		fmt.Printf("Failed to migrate cluster AWS default apps values.\n")
		return microerror.Mask(err)
	} else {
		fmt.Printf("Successfully migrated cluster AWS default apps values.\n")
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

	// clean legacy charts
	charts := []string{"cilium", "aws-ebs-csi-driver", "aws-cloud-controller-manager", "coredns", "vertical-pod-autoscaler-crd"}
	for _, chart := range charts {
		err = s.cleanLegacyChart(ctx, chart)
		if err != nil {
			return microerror.Mask(err)
		}
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

func (s *Service) migrateDefaultAppsValues(ctx context.Context) error {
	for _, app := range append(ClusterAWSDefaultAppList, DefaultAppsAWSAppList...) {
		extraConfigs, err := s.fetchVintageAppExtraConfigs(ctx, app)
		if err != nil {
			return microerror.Mask(err)
		}
		for _, extraConfig := range extraConfigs {
			kind := strings.ToLower(extraConfig.Kind)
			if kind == SecretKindLoverCase {
				var secret corev1.Secret
				err = s.clusterInfo.MC.VintageKubernetesClient.Get(ctx, ctrl.ObjectKey{Name: extraConfig.Name, Namespace: extraConfig.Namespace}, &secret)
				if err != nil {
					fmt.Printf("ERROR: cant find %s/%s secret in vintage cluster for app %s", extraConfig.Namespace, extraConfig.Name, app)
					return microerror.Mask(err)
				}
				secret.Name = fmt.Sprintf("%s-%s", s.clusterInfo.Name, secret.Name)
				secret.ResourceVersion = ""
				secret.Namespace = s.clusterInfo.Namespace
				err = s.clusterInfo.MC.CapiKubernetesClient.Create(ctx, &secret)
				if apierrors.IsAlreadyExists(err) {
					// It's fine. No worries.
				} else if err != nil {
					return microerror.Mask(err)
				}
				fmt.Printf("Migrated %s secret for app %s\n", secret.Name, app)

			} else {
				// anything else than secret we assume its configmap
				var configmap corev1.ConfigMap
				err = s.clusterInfo.MC.VintageKubernetesClient.Get(ctx, ctrl.ObjectKey{Name: extraConfig.Name, Namespace: extraConfig.Namespace}, &configmap)
				if err != nil {
					fmt.Printf("ERROR: cant find %s/%s configmap in vintage cluster for app %s", extraConfig.Namespace, extraConfig.Name, app)
					return microerror.Mask(err)
				}
				configmap.Name = fmt.Sprintf("%s-%s", s.clusterInfo.Name, configmap.Name)
				configmap.ResourceVersion = ""
				configmap.Namespace = s.clusterInfo.Namespace
				err = s.clusterInfo.MC.CapiKubernetesClient.Create(ctx, &configmap)
				if apierrors.IsAlreadyExists(err) {
					// It's fine. No worries.
				} else if err != nil {
					return microerror.Mask(err)
				}
				fmt.Printf("Migrated %s configmap for app %s\n", configmap.Name, app)
			}
		}
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

	// the CAPI control plane nodes will roll out few times during the migration process
	// so we run the function in background in goroutine to add them to the vintage ELBs to ensure ELB is always up-to-date
	go func() {
		for {
			_ = s.addCAPIControlPlaneNodesToVintageELBs()
			time.Sleep(time.Minute)
		}
	}()

	// wait for first CAPI control plane node to be ready
	err = s.waitForCapiControlPlaneNodesReady(ctx, 1)
	if err != nil {
		return microerror.Mask(err)
	}

	// clean hardcoded values for etcd from the config
	err = s.cleanEtcdInKubeadmConfigMap(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	// reapply the updated configmap
	err = s.applyCAPICluster()
	if err != nil {
		return microerror.Mask(err)
	}

	// run job to remove all control plane static manifests from vintage control plane nodes
	err = s.stopVintageControlPlaneComponents(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	// cordon all vintage control plane nodes
	color.Yellow("Cordon all vintage control plane nodes.")
	err = s.cordonVintageNodes(ctx, controlPlaneNodeLabels())
	if err != nil {
		return microerror.Mask(err)
	}

	// delete CAPI app operator pod to force immediate reconciliation
	err = s.deleteCapiAppOperatorPod(ctx, s.clusterInfo.MC.CapiKubernetesClient, s.clusterInfo.Name)
	if err != nil {
		return microerror.Mask(err)
	}

	// delete chart-operator pod in the WC cluster to reschedule it on new CAPi control-plane-node
	err = s.deleteChartOperatorPods(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	// clean old cilium pods
	err = s.forceDeleteOldCiliumPods(ctx)
	if err != nil {
		return microerror.Mask(err)
	}
	// cleanup crashing cilium pods, sometimes they still run with old config which causes issues that are fixed by restarting them
	go s.deleteCrashingCiliumPods(ctx)

	// wait for all CAPI control plane nodes to be ready
	err = s.waitForCapiControlPlaneNodesReady(ctx, 3)
	if err != nil {
		return microerror.Mask(err)
	}

	return nil
}

// CleanVintageCluster drains all vintage nodes and deletes the ASGs
func (s *Service) CleanVintageCluster(ctx context.Context) error {
	color.Yellow("Draining all vintage control plane nodes.")
	err := s.drainVintageNodes(ctx, controlPlaneNodeLabels())
	if err != nil {
		return microerror.Mask(err)
	}

	color.Yellow("Deleting vintage control plane ASGs.\n")
	err = s.refreshAWSClients()
	if err != nil {
		return microerror.Mask(err)
	}
	err = s.deleteVintageASGGroups(tccpnAsgFilters())
	if err != nil {
		return microerror.Mask(err)
	}
	color.Yellow("Deleted vintage control plane ASGs.\n")
	err = s.waitForKubeadmControlPlaneReady(ctx)
	if err != nil {
		return microerror.Mask(err)
	}
	err = s.cordonAllVintageWorkerNodes(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	for _, mp := range s.vintageCRs.AwsMachineDeployments {
		color.Yellow("Waiting for all CAPI nodes in node pool %s to be ready.", mp.Name)
		err = s.waitForCapiNodePoolNodesReady(ctx, mp.Name)
		if err != nil {
			return microerror.Mask(err)
		}
		color.Yellow("Draining all vintage worker nodes for nodepool %s.", mp.Name)
		err = s.drainVintageNodes(ctx, nodePoolNodesLabels(mp.Name))
		if err != nil {
			return microerror.Mask(err)
		}
		color.Yellow("Deleting vintage %s node pool ASG.\n", mp.Name)
		err = s.refreshAWSClients()
		if err != nil {
			return microerror.Mask(err)
		}
		err = s.deleteVintageASGGroups(tcnpAsgFilters(mp.Name))
		if err != nil {
			return microerror.Mask(err)
		}
		color.Yellow("Deleted vintage %s node pool ASG.\n", mp.Name)
	}

	return nil
}
