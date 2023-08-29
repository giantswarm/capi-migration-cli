package migrator

type ClusterAppValuesData struct {
	Metadata Metadata `yaml:"metadata"`

	ControlPlane     ControlPlane        `yaml:"controlPlane"`
	Internal         Internal            `yaml:"internal"`
	Connectivity     Connectivity        `yaml:"connectivity"`
	NodePools        map[string]NodePool `yaml:"nodePools"`
	ProviderSpecific ProviderSpecific    `yaml:"providerSpecific"`
}

type Metadata struct {
	Name         string `yaml:"name"`
	Description  string `yaml:"description"`
	Organization string `yaml:"organization"`
}

type ControlPlane struct {
	ApiExtraArgs     map[string]string   `yaml:"apiExtraArgs"`
	ApiExtraCertSans []string            `yaml:"apiExtraCertSANs"`
	InstanceType     string              `yaml:"instanceType"`
	SubnetTags       []map[string]string `yaml:"subnetTags"`
}

type Internal struct {
	Migration Migration `yaml:"migration"`
}

type Migration struct {
	ControlPlaneAdditionalSecurityGroupID string            `yaml:"controlPlaneAdditionalSecurityGroupID"`
	ControlPlaneExtraFiles                []File            `yaml:"controlPlaneExtraFiles"`
	ControlPlanePreKubeadmCommands        []string          `yaml:"controlPlanePreKubeadmCommands"`
	EtcdExtraArgs                         map[string]string `yaml:"etcdExtraArgs"`
}

type File struct {
	Path       string `yaml:"path"`
	SecretName string `yaml:"secretName"`
	SecretKey  string
}

type NodePool struct {
	AdditionalSecurityGroupID string              `yaml:"additionalSecurityGroupID"`
	AvailabilityZones         []string            `yaml:"availabilityZones"`
	InstanceType              string              `yaml:"instanceType"`
	Min                       int                 `yaml:"min"`
	Max                       int                 `yaml:"max"`
	RootVolumeSizeGB          int                 `yaml:"rootVolumeSizeGB"`
	SubnetTags                []map[string]string `yaml:"subnetTags"`
}

type ProviderSpecific struct {
	AwsClusterRoleIdentityName string `yaml:"awsClusterRoleIdentityName"`
}

type Connectivity struct {
	Network Network  `yaml:"network"`
	Subnets []Subnet `yaml:"subnets"`
}

type Network struct {
	Pods              Pods     `yaml:"pods"`
	Services          Services `yaml:"services"`
	VPCID             string   `yaml:"vpcId"`
	InternetGatewayID string   `yaml:"internetGatewayId"`
}

type Pods struct {
	CidrBlocks []string `yaml:"cidrBlocks"`
}

type Services struct {
	CidrBlocks []string `yaml:"cidrBlocks"`
}

type Subnet struct {
	ID           string `yaml:"id"`
	IsPublic     bool   `yaml:"isPublic"`
	RouteTableID string `yaml:"routeTableId"`
	NatGatewayID string `yaml:"natGatewayId,omitempty"`
}
