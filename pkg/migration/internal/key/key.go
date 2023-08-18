package key

import "fmt"

func AzureMasterVMSSName(clusterID string) string {
	return fmt.Sprintf("%s-master-%s", clusterID, clusterID)
}

func AzureNodePoolVMSSName(nodePoolID string) string {
	return fmt.Sprintf("nodepool-%s", nodePoolID)
}

func AWSKubeadmControlPlaneName(clusterID string) string {
	return fmt.Sprintf("%s-control-plane", clusterID)
}

func AWSMachineTemplateNameForCP(clusterID string) string {
	return fmt.Sprintf("%s-control-plane", clusterID)
}

func AWSMachinePoolName(clusterID string, machineID string) string {
	return fmt.Sprintf("%s-worker-%s", clusterID, machineID)
}

func AWSAPIEndpointFromDomain(domain string, clusterID string) string {
	return fmt.Sprintf("api.%s.k8s.%s", clusterID, domain)
}

func AWSCustomFilesSecretName(clusterID string) string {
	return fmt.Sprintf("%s-custom-files", clusterID)
}
func AWSEtcdEndpointFromDomain(domain string, clusterID string) string {
	return fmt.Sprintf("etcd.%s.k8s.%s", clusterID, domain)
}

func EncryptionConfigSecretName(clusterID string) string {
	return fmt.Sprintf("%s-k8s-encryption-config", clusterID)
}

func CACertsSecretName(clusterID string) string {
	return fmt.Sprintf("%s-ca", clusterID)
}

func SACertsSecretName(clusterID string) string {
	return fmt.Sprintf("%s-service-account", clusterID)
}

func EtcdCertsSecretName(clusterID string) string {
	return fmt.Sprintf("%s-etcd", clusterID)
}

func VaultPKIHackyEndpoint(clusterID string) string {
	return fmt.Sprintf("pki-%s/gimmeallyourlovin", clusterID)
}
