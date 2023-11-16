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
	Name            string `yaml:"name"`
	Description     string `yaml:"description"`
	Organization    string `yaml:"organization"`
	ServicePriority string `yaml:"servicePriority"`
}

type ControlPlane struct {
	AdditionalSecurityGroups []SecurityGroup     `yaml:"additionalSecurityGroups"`
	ApiExtraArgs             map[string]string   `yaml:"apiExtraArgs"`
	ApiExtraCertSans         []string            `yaml:"apiExtraCertSANs"`
	InstanceType             string              `yaml:"instanceType"`
	SubnetTags               []map[string]string `yaml:"subnetTags"`
}

type Internal struct {
	CGroupsv1 bool      `yaml:"cgroupsv1"`
	Migration Migration `yaml:"migration"`
}

type Migration struct {
	ApiBindPort                     int               `yaml:"apiBindPort"`
	ControlPlaneExtraFiles          []File            `yaml:"controlPlaneExtraFiles"`
	ControlPlanePreKubeadmCommands  []string          `yaml:"controlPlanePreKubeadmCommands"`
	ControlPlanePostKubeadmCommands []string          `yaml:"controlPlanePostKubeadmCommands"`
	EtcdExtraArgs                   map[string]string `yaml:"etcdExtraArgs"`
}

type File struct {
	ContentFrom ContentFrom `yaml:"contentFrom"`
	Path        string      `yaml:"path"`
	Permissions string      `yaml:"permissions"`
}

type ContentFrom struct {
	Secret Secret `yaml:"secret"`
}
type Secret struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key"`
}

type NodePool struct {
	AdditionalSecurityGroups []SecurityGroup     `yaml:"additionalSecurityGroups"`
	AvailabilityZones        []string            `yaml:"availabilityZones"`
	InstanceType             string              `yaml:"instanceType"`
	MinSize                  int                 `yaml:"minSize"`
	MaxSize                  int                 `yaml:"maxSize"`
	RootVolumeSizeGB         int                 `yaml:"rootVolumeSizeGB"`
	SubnetTags               []map[string]string `yaml:"subnetTags"`
}

type ProviderSpecific struct {
	AwsClusterRoleIdentityName string `yaml:"awsClusterRoleIdentityName"`
	Region                     string `yaml:"region"`
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

type SecurityGroup struct {
	ID string `yaml:"id"`
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
