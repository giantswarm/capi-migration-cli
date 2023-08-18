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
	"sigs.k8s.io/yaml"

	"github.com/giantswarm/capi-migration-cli/pkg/templates"
)

const (
	ClusterAppVersion  = "0.37.0"
	ClusterAppCatalog  = "cluster"
	DefaultAppsVersion = "0.32.0"
	DefaultAppsCatalog = "cluster"

	DefaultAppsAWSRepoName = "default-apps-aws"
	ClusterAWSRepoName     = "cluster-aws"
)

func (s *Service) GenerateCAPIClusterTemplates(ctx context.Context) error {
	err := s.templateClusterAWS(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	err = s.templateDefaultAppsAWS()
	if err != nil {
		return microerror.Mask(err)
	}

	return nil
}

func (s *Service) templateClusterAWS(ctx context.Context) error {
	appName := s.clusterInfo.Name
	configMapName := fmt.Sprintf("%s-userconfig", appName)

	var configMapYAML []byte
	{

		clusterConfigData := fmt.Sprintf(`
metadata:
  name: %s
  organization: %s
`, s.clusterInfo.Name, organizationFromNamespace(s.clusterInfo.Namespace))

		userConfigMap, err := templateapp.NewConfigMap(templateapp.UserConfig{
			Name:      configMapName,
			Namespace: s.clusterInfo.Namespace,
			Data:      clusterConfigData,
		})
		if err != nil {
			return microerror.Mask(err)
		}

		userConfigMap.Labels = map[string]string{}
		userConfigMap.Labels[k8smetadata.Cluster] = s.clusterInfo.Name

		configMapYAML, err = yaml.Marshal(userConfigMap)
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

	f, err := os.OpenFile(clusterAppYamlFile(s.clusterInfo.Name), os.O_CREATE|os.O_RDWR, 0755)
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

/*
	func BuildCapaClusterConfig(config ClusterConfig) capa.ClusterConfig {
		return capa.ClusterConfig{
			Metadata: &capa.Metadata{
				Name:         config.Name,
				Description:  config.Description,
				Organization: config.Organization,
			},
			ProviderSpecific: &capa.ProviderSpecific{
				Region:                     config.Region,
				AWSClusterRoleIdentityName: config.AWS.AWSClusterRoleIdentityName,
			},
			Connectivity: &capa.Connectivity{
				AvailabilityZoneUsageLimit: config.AWS.NetworkAZUsageLimit,
				Bastion: &capa.Bastion{
					Enabled:      true,
					InstanceType: config.BastionInstanceType,
					Replicas:     config.BastionReplicas,
				},
				DNS: &capa.DNS{},
				Network: &capa.Network{
					VPCCIDR: config.AWS.NetworkVPCCIDR,
				},
				Topology: &capa.Topology{},
			},
			ControlPlane: &capa.ControlPlane{
				InstanceType: config.ControlPlaneInstanceType,
				Replicas:     3,
			},
			NodePools: &map[string]capa.MachinePool{
				config.AWS.MachinePool.Name: {
					AvailabilityZones: config.AWS.MachinePool.AZs,
					InstanceType:      config.AWS.MachinePool.InstanceType,
					MinSize:           config.AWS.MachinePool.MinSize,
					MaxSize:           config.AWS.MachinePool.MaxSize,
					RootVolumeSizeGB:  config.AWS.MachinePool.RootVolumeSizeGB,
					CustomNodeLabels:  config.AWS.MachinePool.CustomNodeLabels,
				},
			},
		}
	}
*/
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

		configMapYAML, err = yaml.Marshal(userConfigMap)
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

	f, err := os.OpenFile(clusterAppYamlFile(s.clusterInfo.Name), os.O_APPEND, 0755)
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
	return fmt.Sprintf("./%s-cluster.yaml", clusterName)
}

func organizationFromNamespace(namespace string) string {
	return strings.TrimPrefix(namespace, "org-")
}
