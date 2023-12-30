package migrator

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/template"

	k8smetadata "github.com/giantswarm/k8smetadata/pkg/label"
	"github.com/giantswarm/kubectl-gs/v2/cmd/template/cluster/provider/templates/capa"
	templateapp "github.com/giantswarm/kubectl-gs/v2/pkg/template/app"
	"github.com/giantswarm/microerror"
	"gopkg.in/yaml.v3"
	k8syaml "sigs.k8s.io/yaml"

	"github.com/giantswarm/capi-migration-cli/pkg/templates"
)

const (
	ClusterAppVersion  = "0.54.0"
	ClusterAppCatalog  = "cluster"
	DefaultAppsVersion = "0.40.0"
	DefaultAppsCatalog = "cluster"

	DefaultAppsAWSRepoName = "default-apps-aws"
	ClusterAWSRepoName     = "cluster-aws"
)

var ClusterAWSDefaultAppList = [4]string{"cilium", "aws-ebs-csi-driver", "aws-cloud-controller-manager", "coredns"}

func (s *Service) GenerateCAPIClusterTemplates(ctx context.Context) error {
	// remove file if it already exists
	err := removeTemplateFileIfExists(clusterAppYamlFile(s.clusterInfo.Name))
	if err != nil {
		return microerror.Mask(err)
	}

	err = s.templateClusterAWS(ctx)
	if err != nil {
		fmt.Printf("Failed to generate CAPI cluster template manifest for cluster %s\n", s.clusterInfo.Name)
		return microerror.Mask(err)
	}
	fmt.Printf("Generated CAPI cluster template manifest for cluster %s\n", s.clusterInfo.Name)

	err = s.templateDefaultAppsAWS()
	if err != nil {
		fmt.Printf("Failed to generate CAPI default-apps template manifest for cluster %s\n", s.clusterInfo.Name)
		return microerror.Mask(err)
	}
	fmt.Printf("Generated CAPI default-apps template manifest for cluster %s\n", s.clusterInfo.Name)

	return nil
}

func (s *Service) generateClusterConfigData(ctx context.Context) (*ClusterAppValuesData, error) {
	// fetch info from k8s vintage MC
	clusterServiceCidrBlock, err := getClusterServiceCidrBlock(ctx, s.clusterInfo.MC.VintageKubernetesClient, s.vintageCRs)
	if err != nil {
		return nil, microerror.Mask(err)
	}
	servicePriority, ok := s.vintageCRs.AwsCluster.Annotations["giantswarm.io/service-priority"]
	if !ok {
		servicePriority = "highest"
	}
	// fetch info from AWS
	masterSecurityGroupID, err := s.getMasterSecurityGroupID()
	if err != nil {
		return nil, microerror.Mask(err)
	}
	internetGatewayID, err := s.getInternetGatewayID()
	if err != nil {
		return nil, microerror.Mask(err)
	}
	subnets, err := s.getSubnets(s.vintageCRs.AwsCluster.Status.Provider.Network.VPCID)
	if err != nil {
		return nil, microerror.Mask(err)
	}

	// fill the struct
	data := &ClusterAppValuesData{

		Global: Global{
			Metadata: Metadata{
				Name:            s.clusterInfo.Name,
				Organization:    organizationFromNamespace(s.clusterInfo.Namespace),
				Description:     getClusterDescription(s.vintageCRs),
				ServicePriority: servicePriority,
			},

			ControlPlane: ControlPlane{
				AdditionalSecurityGroups: []SecurityGroup{{ID: masterSecurityGroupID}},
				ApiExtraArgs: map[string]string{
					"etcd-prefix": "giantswarm.io",
				},
				ApiExtraCertSans: []string{apiEndpointFromDomain(s.vintageCRs.AwsCluster.Spec.Cluster.DNS.Domain, s.clusterInfo.Name)},
				InstanceType:     s.vintageCRs.AwsControlPlane.Spec.InstanceType,
				SubnetTags:       buildCPSubnetTags(s.clusterInfo.Name),
			},
			Connectivity: Connectivity{
				Network: Network{
					InternetGatewayID: internetGatewayID,
					VPCID:             s.vintageCRs.AwsCluster.Status.Provider.Network.VPCID,
					Pods: Pods{
						CidrBlocks: []string{s.vintageCRs.AwsCluster.Spec.Provider.Pods.CIDRBlock},
					},
					Services: Services{
						CidrBlocks: []string{clusterServiceCidrBlock},
					},
				},
				Subnets: subnets,
			},
			ProviderSpecific: ProviderSpecific{
				AwsClusterRoleIdentityName: awsClusterRoleIdentityName(s.clusterInfo.Name),
				Region:                     s.vintageCRs.AwsCluster.Spec.Provider.Region,
			},
		},
		Internal: Internal{
			Migration: Migration{
				ApiBindPort: 443,
				ControlPlaneExtraFiles: []File{
					{
						ContentFrom: ContentFrom{
							Secret: Secret{
								Name: customFilesSecretName(s.clusterInfo.Name),
								Key:  joinEtcdClusterScriptKey,
							},
						},
						Path:        "/migration/join-existing-cluster.sh",
						Permissions: "0644",
					},
					{
						ContentFrom: ContentFrom{
							Secret: Secret{
								Name: customFilesSecretName(s.clusterInfo.Name),
								Key:  moveEtcdLeaderScriptKey,
							},
						},
						Path:        "/migration/move-etcd-leader.sh",
						Permissions: "0644",
					},
					{
						ContentFrom: ContentFrom{
							Secret: Secret{
								Name: customFilesSecretName(s.clusterInfo.Name),
								Key:  apiHealthzVintagePodKey,
							},
						},
						Path:        "/etc/kubernetes/manifests/api-healthz-vintage-pod.yaml",
						Permissions: "0644",
					},
					{
						ContentFrom: ContentFrom{
							Secret: Secret{
								Name: customFilesSecretName(s.clusterInfo.Name),
								Key:  addExtraServiceAccountIssuers,
							},
						},
						Path:        "/migration/add-extra-service-account-issuers.sh",
						Permissions: "0644",
					},
				},
				ControlPlanePreKubeadmCommands: []string{
					"iptables -A PREROUTING -t nat  -p tcp --dport 6443 -j REDIRECT --to-port 443 # route traffic from 6443 to 443",
					"iptables -t nat -A OUTPUT -p tcp --destination 127.0.0.1 --dport 6443 -j REDIRECT --to-port 443 # include localhost",
					"/bin/sh /migration/join-existing-cluster.sh",
				},
				ControlPlanePostKubeadmCommands: []string{
					"/bin/sh /migration/add-extra-service-account-issuers.sh",
					"/bin/sh /migration/move-etcd-leader.sh",
				},
				EtcdExtraArgs: map[string]string{
					"initial-cluster-state":                          "existing",
					"initial-cluster":                                "$ETCD_INITIAL_CLUSTER",
					"experimental-peer-skip-client-san-verification": "true",
				},
			},
		},
	}

	// add apps
	appValues := map[string]App{}
	for _, appName := range ClusterAWSDefaultAppList {
		extraConfigs, err := s.fetchVintageAppExtraConfigs(ctx, appName)
		if err != nil {
			return nil, microerror.Mask(err)
		}
		var app App
		for _, extraConfig := range extraConfigs {
			kind := "ConfigMap"
			if strings.ToLower(extraConfig.Kind) == "secret" {
				kind = "Secret"
			}
			app.ExtraConfigs = append(app.ExtraConfigs, ExtraConfig{Kind: kind, Name: fmt.Sprintf("%s-%s", s.clusterInfo.Name, extraConfig.Name)})
		}

		appValues[appName] = app
	}
	data.Global.Apps.Cilium = appValues["cilium"]
	data.Global.Apps.AwsEbsCsiDriver = appValues["aws-ebs-csi-driver"]
	data.Global.Apps.AwsCloudControllerManager = appValues["aws-cloud-controller-manager"]
	data.Global.Apps.CoreDNS = appValues["coredns"]

	data.Global.NodePools = make(map[string]NodePool)
	for _, mp := range s.vintageCRs.AwsMachineDeployments {
		id, err := s.getWorkerSecurityGroupID(mp.Name)
		if err != nil {
			return nil, microerror.Mask(err)
		}

		data.Global.NodePools[mp.Name] = NodePool{
			AdditionalSecurityGroups: []SecurityGroup{{ID: id}},
			AvailabilityZones:        mp.Spec.Provider.AvailabilityZones,
			InstanceType:             mp.Spec.Provider.Worker.InstanceType,
			MinSize:                  mp.Spec.NodePool.Scaling.Min,
			MaxSize:                  mp.Spec.NodePool.Scaling.Max,
			RootVolumeSizeGB:         calculateRootVolumeSize(mp.Spec.NodePool.Machine.DockerVolumeSizeGB, mp.Spec.NodePool.Machine.KubeletVolumeSizeGB),
			SubnetTags:               buildMPSubnetTags(s.clusterInfo.Name, mp.Name),
			CustomNodeLabel:          []string{fmt.Sprintf("giantswarm.io/machine-deployment=%s", mp.Name)},
		}
	}
	// check for cgroups v1
	for _, md := range s.vintageCRs.MachineDeployments {
		if _, ok := md.Annotations["node.giantswarm.io/cgroupv1"]; ok {
			data.Internal.CGroupsv1 = true
			break
		}
	}

	return data, nil
}

func (s *Service) templateClusterAWS(ctx context.Context) error {
	appName := s.clusterInfo.Name
	configMapName := fmt.Sprintf("%s-userconfig", appName)

	var configMapYAML []byte
	{
		data, err := s.generateClusterConfigData(ctx)
		if err != nil {
			return microerror.Mask(err)
		}
		clusterConfigData, err := yaml.Marshal(data)
		if err != nil {
			return microerror.Mask(err)
		}

		userConfigMap, err := templateapp.NewConfigMap(templateapp.UserConfig{
			Name:      configMapName,
			Namespace: s.clusterInfo.Namespace,
			Data:      string(clusterConfigData),
		})
		if err != nil {
			return microerror.Mask(err)
		}

		userConfigMap.Labels = map[string]string{}
		userConfigMap.Labels[k8smetadata.Cluster] = s.clusterInfo.Name

		configMapYAML, err = k8syaml.Marshal(userConfigMap)
		if err != nil {
			return microerror.Mask(err)
		}
	}

	var appYAML []byte
	{
		appVersion := s.app.ClusterAppVersion
		clusterAppConfig := templateapp.Config{
			AppName:                 s.clusterInfo.Name,
			Catalog:                 s.app.ClusterAppCatalog,
			InCluster:               true,
			Name:                    ClusterAWSRepoName,
			Namespace:               s.clusterInfo.Namespace,
			Version:                 appVersion,
			UserConfigConfigMapName: configMapName,
		}

		var err error
		appYAML, err = templateapp.NewAppCR(clusterAppConfig)
		if err != nil {
			return microerror.Mask(err)
		}
	}

	t := template.Must(template.New("appCR").Parse(templates.AppCRTemplate))

	f, err := os.OpenFile(clusterAppYamlFile(s.clusterInfo.Name), os.O_CREATE|os.O_RDWR, 0640)
	if err != nil {
		return microerror.Mask(err)
	}

	err = t.Execute(f, templateapp.AppCROutput{
		AppCR:               string(appYAML),
		UserConfigConfigMap: string(configMapYAML),
	})
	if err != nil {
		return microerror.Mask(err)
	}
	return nil
}

func (s *Service) templateDefaultAppsAWS() error {
	appName := fmt.Sprintf("%s-default-apps", s.clusterInfo.Name)
	configMapName := fmt.Sprintf("%s-userconfig", appName)

	var configMapYAML []byte
	{
		flagValues := capa.DefaultAppsConfig{
			ClusterName:  s.clusterInfo.Name,
			Organization: organizationFromNamespace(s.clusterInfo.Namespace),
		}

		configData, err := capa.GenerateDefaultAppsValues(flagValues)
		if err != nil {
			return microerror.Mask(err)
		}

		userConfigMap, err := templateapp.NewConfigMap(templateapp.UserConfig{
			Name:      configMapName,
			Namespace: s.clusterInfo.Namespace,
			Data:      configData,
		})
		if err != nil {
			return microerror.Mask(err)
		}

		userConfigMap.Labels = map[string]string{}
		userConfigMap.Labels[k8smetadata.Cluster] = s.clusterInfo.Name

		configMapYAML, err = k8syaml.Marshal(userConfigMap)
		if err != nil {
			return microerror.Mask(err)
		}
	}

	var appYAML []byte
	{
		appVersion := s.app.DefaultAppsVersion
		var err error
		appYAML, err = templateapp.NewAppCR(templateapp.Config{
			AppName:                 appName,
			Cluster:                 s.clusterInfo.Name,
			Catalog:                 s.app.DefaultAppsCatalog,
			DefaultingEnabled:       false,
			InCluster:               true,
			Name:                    DefaultAppsAWSRepoName,
			Namespace:               s.clusterInfo.Namespace,
			Version:                 appVersion,
			UserConfigConfigMapName: configMapName,
			UseClusterValuesConfig:  true,
			ExtraLabels: map[string]string{
				k8smetadata.ManagedBy: "cluster",
			},
		})
		if err != nil {
			return microerror.Mask(err)
		}
	}

	t := template.Must(template.New("appCR").Parse(templates.AppCRTemplate))

	f, err := os.OpenFile(clusterAppYamlFile(s.clusterInfo.Name), os.O_APPEND|os.O_RDWR, 0640)
	if err != nil {
		return microerror.Mask(err)
	}

	err = t.Execute(f, templateapp.AppCROutput{
		UserConfigConfigMap: string(configMapYAML),
		AppCR:               string(appYAML),
	})
	if err != nil {
		return microerror.Mask(err)
	}

	return nil
}

func clusterAppYamlFile(clusterName string) string {
	wd, _ := os.Getwd()
	return fmt.Sprintf("%s/%s-cluster.yaml", wd, clusterName)
}

func organizationFromNamespace(namespace string) string {
	return strings.TrimPrefix(namespace, "org-")
}

func calculateRootVolumeSize(dockerVolumeSize int, kubeletVolumeSize int) int {
	if dockerVolumeSize == 0 {
		dockerVolumeSize = 50
	}
	if kubeletVolumeSize == 0 {
		kubeletVolumeSize = 50
	}

	return 100 + dockerVolumeSize + kubeletVolumeSize
}

func buildMPSubnetTags(clusterName string, workerName string) []map[string]string {
	return []map[string]string{
		{
			"giantswarm.io/cluster": clusterName,
		},
		{
			"giantswarm.io/machine-deployment": workerName,
		},
	}

}

func buildCPSubnetTags(clusterName string) []map[string]string {
	return []map[string]string{
		{
			"giantswarm.io/cluster": clusterName,
		},
		{
			"giantswarm.io/stack": "tccp",
		},
	}
}

func removeTemplateFileIfExists(filename string) error {
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return nil
	}

	err := os.RemoveAll(filename)
	if err != nil {
		return microerror.Mask(err)
	}

	return nil
}
