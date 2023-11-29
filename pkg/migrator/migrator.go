package migrator

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/elb"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/fatih/color"
	"github.com/giantswarm/backoff"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/giantswarm/kubectl-gs/v2/pkg/template/app"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8syaml "sigs.k8s.io/yaml"

	"github.com/giantswarm/microerror"

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

	fmt.Printf("Migrating apps to CAPI MC\n")
	err = s.migrateApps(ctx, s.clusterInfo.MC.VintageKubernetesClient)
	if err != nil {
		fmt.Printf("Failed to migrate apps.\n")
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

	// clean legacy charts
  // todo: add this again
//	charts := []string{"cilium", "aws-ebs-csi-driver", "aws-cloud-controller-manager", "coredns", "vertical-pod-autoscaler-crd"}
//	for _, chart := range charts {
//		err = s.cleanLegacyChart(ctx, chart)
//		if err != nil {
//			return microerror.Mask(err)
//		}
//	}

	color.Green("Preparation phase completed.\n\n")

	return nil
}

func migrateAppConfigObject(k8sClient client.Client, resourceKind string, name string, nameSpace string) ([]byte, error) {

  switch resourceKind {
    case "secret":
      var secret corev1.Secret

      err := k8sClient.Get(context.TODO(), client.ObjectKey{
        Name: name,
        Namespace: nameSpace,
      }, &secret)

      if err != nil {
        return nil, microerror.Mask(err)
      }

      newSecret := &corev1.Secret{
        TypeMeta: metav1.TypeMeta{
          Kind:       "Secret",
          APIVersion: "v1",
        },
        ObjectMeta: metav1.ObjectMeta{
          Name: name,
          Namespace: nameSpace,
        },
        Data: secret.Data,
      }
      newSecretYaml, err := k8syaml.Marshal(newSecret)

      if err != nil {
        return nil, microerror.Mask(err)
      }

      return newSecretYaml, nil

    case "configmap":
      var cm corev1.ConfigMap
      err := k8sClient.Get(context.TODO(), client.ObjectKey{
          Name: name,
          Namespace: nameSpace,
        }, &cm)

      if err != nil {
        return nil,microerror.Mask(err)
      }

      newCm := &corev1.ConfigMap{
        TypeMeta: metav1.TypeMeta{
          Kind:       "ConfigMap",
          APIVersion: "v1",
        },
        ObjectMeta: metav1.ObjectMeta{
          Name: name,
          Namespace: nameSpace,
        },
        Data: cm.Data,
      }

      newCmYaml, err := k8syaml.Marshal(newCm)
      if err != nil {
        return nil, microerror.Mask(err)
      }

      return newCmYaml, nil

    default:
      return nil,fmt.Errorf("unsupported resource kind: %s", resourceKind)
    }
}

func (s *Service) migrateApps(ctx context.Context, k8sClient client.Client) error {
  
  var numberOfAppsToMigrate int

  // we write the apps to a yaml-file, which gets applied later
	f, err := os.OpenFile(nonDefaultAppYamlFile(s.clusterInfo.Name), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0640)

	if err != nil {
		return microerror.Mask(err)
	}

  appLoop:
    for _,application := range s.vintageCRs.Apps {
      // skip "default" apps; these should be installed by default on the MC
      if application.Spec.Catalog == "default" {
        continue
      }

      // skip bundled apps as we only migrate their parent
      // todo: verify thats formally correct
      labels := application.GetLabels()
      for key := range labels {
        if strings.Contains(key, "giantswarm.io/managed-by") {
          // we skip this app completly
          continue appLoop
        }
      }

    numberOfAppsToMigrate += 1

// 	DefaultingEnabled          bool
// 	ExtraLabels                map[string]string
// 	ExtraAnnotations           map[string]string
// 	UseClusterValuesConfig     bool
// 	InstallTimeout             *metav1.Duration
// 	UninstallTimeout           *metav1.Duration
// 	UpgradeTimeout             *metav1.Duration
// 	RollbackTimeout            *metav1.Duration


    // todo: app operator version; does it impact the migration?
    // todo: how to deal with ExtraLabels and Extrannotations?
    newApp := app.Config{
      AppName: application.ObjectMeta.Name,
			Catalog:      application.Spec.Catalog,
      Cluster:      s.clusterInfo.Name,
      InCluster:    application.Spec.KubeConfig.InCluster,
			Name:         application.Spec.Name,
			Namespace:    application.Spec.Namespace,
			Version:      application.Spec.Version,
		}

    // make sure we trim of the clustername if it somehow was prefixed on the app
    metadataName := strings.TrimLeft(application.GetName(), s.clusterInfo.Name)
    // now prefix our app with the cluster
    newApp.AppName = fmt.Sprintf("%s-%s", s.clusterInfo.Name, metadataName)
    if application.Spec.Config.ConfigMap.Name == fmt.Sprintf("%s-cluster-values", s.clusterInfo.Name) {
      newApp.UseClusterValuesConfig = true
    }

    if application.Spec.ExtraConfigs != nil {
      newApp.ExtraConfigs = application.Spec.ExtraConfigs

      for _, extraConfig := range application.Spec.ExtraConfigs {
        obj, err := migrateAppConfigObject(k8sClient, strings.ToLower(extraConfig.Kind), extraConfig.Name, extraConfig.Namespace)
        if err != nil {
          return microerror.Mask(err)
        }

        if _, err := f.Write([]byte(fmt.Sprintf("%s---\n", obj))); err != nil {
          return microerror.Mask(err)
        }
      }
    }

    if application.Spec.CatalogNamespace != "" {
      newApp.CatalogNamespace = application.Spec.CatalogNamespace
    }

    if application.Spec.NamespaceConfig.Labels != nil {
      newApp.NamespaceConfigLabels = application.Spec.NamespaceConfig.Labels
    }
    if application.Spec.NamespaceConfig.Annotations != nil {
      newApp.NamespaceConfigAnnotations = application.Spec.NamespaceConfig.Annotations
    }

    if application.Spec.Install.Timeout != nil {
      newApp.InstallTimeout = application.Spec.Install.Timeout
    }
    if application.Spec.Rollback.Timeout != nil {
      newApp.RollbackTimeout = application.Spec.Rollback.Timeout
    }
    if application.Spec.Uninstall.Timeout != nil {
      newApp.UninstallTimeout = application.Spec.Uninstall.Timeout
    }
    if application.Spec.Upgrade.Timeout != nil {
      newApp.UpgradeTimeout = application.Spec.Upgrade.Timeout
    }

    // apps on the WC should go to the org namespace
    if application.Spec.KubeConfig.InCluster == false {
      newApp.Organization = organizationFromNamespace(s.clusterInfo.Namespace)
    }

    if application.Spec.UserConfig.ConfigMap.Name != "" {
      newApp.UserConfigConfigMapName = application.Spec.UserConfig.ConfigMap.Name

      configmap, err := migrateAppConfigObject(k8sClient, "configmap", application.Spec.UserConfig.ConfigMap.Name, application.Spec.UserConfig.ConfigMap.Namespace)
      if err != nil {
        return microerror.Mask(err)
      }

      if _, err := f.Write([]byte(fmt.Sprintf("%s---\n", configmap))); err != nil {
        return microerror.Mask(err)
      }
    }

    if application.Spec.UserConfig.Secret.Name != "" {
      newApp.UserConfigSecretName = application.Spec.UserConfig.Secret.Name

      secret, err := migrateAppConfigObject(k8sClient, "secret", application.Spec.UserConfig.Secret.Name, application.Spec.UserConfig.Secret.Namespace)
      if err != nil {
        return microerror.Mask(err)
      }

      if _, err := f.Write([]byte(fmt.Sprintf("%s---\n", secret))); err != nil {
        return microerror.Mask(err)
      }
    }

    appYAML, err := app.NewAppCR(newApp)
    if err != nil {
      return microerror.Mask(err)
    }

    if _, err := f.Write([]byte(fmt.Sprintf("%s---\n", appYAML))); err != nil {
      return microerror.Mask(err)
    }

  }
	
  fmt.Printf("Scheduled %d non-default apps for migration\n", numberOfAppsToMigrate)

  if err := f.Close(); err != nil {
    return microerror.Mask(err)
  }

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

  // todo: we might have to wait for cluster-app-op to create the $cluster-user-values cm
  // otherwise kyverno will block the app creation
  go s.applyCAPIApps()
  //return microerror.Mask(fmt.Errorf("stop"))

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
		err = s.drainVintageNodes(ctx, vintageNodePoolNodeLabels(mp.Name))
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
