package migrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"time"

	awsarn "github.com/aws/aws-sdk-go/aws/arn"
	"github.com/fatih/color"
	"github.com/giantswarm/backoff"
	"github.com/giantswarm/microerror"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubeadmv1beta1 "sigs.k8s.io/cluster-api/controlplane/kubeadm/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (s *Service) createAWSClusterRoleIdentity(ctx context.Context, vintageRoleARN string) error {
	accountID, err := extractAwsAccountIDfromARN(vintageRoleARN)
	if err != nil {
		return microerror.Mask(err)
	}

	awsClusterRoleIdentity := &unstructured.Unstructured{}
	awsClusterRoleIdentity.Object = map[string]interface{}{
		"metadata": map[string]interface{}{
			"name": awsClusterRoleIdentityName(s.clusterInfo.Name),
			"labels": map[string]interface{}{
				"giantswarm.io/cluster":      s.clusterInfo.Name,
				"giantswarm.io/organization": organizationFromNamespace(s.clusterInfo.Namespace),
			},
		},
		"spec": map[string]interface{}{
			"sourceIdentityRef": map[string]interface{}{
				"kind": "AWSClusterControllerIdentity",
				"name": "default",
			},
			"roleARN": fmt.Sprintf("arn:aws:iam::%s:role/giantswarm-%s-capa-controller", accountID, s.clusterInfo.MC.CapiMC),
			"allowedNamespaces": map[string]interface{}{
				"list":     nil,
				"selector": map[string]interface{}{},
			},
		},
	}
	awsClusterRoleIdentity.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "infrastructure.cluster.x-k8s.io",
		Kind:    "AWSClusterRoleIdentity",
		Version: "v1beta2",
	})
	err = s.clusterInfo.MC.CapiKubernetesClient.Create(ctx, awsClusterRoleIdentity)

	if apierrors.IsAlreadyExists(err) {
		// It's fine. No worries.
	} else if err != nil {
		return microerror.Mask(err)
	}

	return nil
}

func (s *Service) applyCAPICluster() error {
	fmt.Printf("Applying CAPI cluster APP CR to MC\n")
	applyManifests := func() error {
		//nolint:gosec
		c := exec.Command("kubectl", "--context", fmt.Sprintf("gs-%s", s.clusterInfo.MC.CapiMC), "apply", "-f", clusterAppYamlFile(s.clusterInfo.Name))

		c.Stderr = os.Stderr
		c.Stdin = os.Stdin

		err := c.Run()
		if err != nil {
			return microerror.Mask(err)
		}
		return nil
	}

	err := backoff.Retry(applyManifests, s.backOff)
	if err != nil {
		return microerror.Mask(err)
	}
	color.Green("CAPI Cluster app applied successfully.\n\n")
	return nil
}

func (s *Service) cleanEtcdInKubeadmConfigMap(ctx context.Context) error {
	// fetch kubeadm configmap
	var configmap v1.ConfigMap
	err := s.clusterInfo.KubernetesControllerClient.Get(ctx, client.ObjectKey{Name: "kubeadm-config", Namespace: "kube-system"}, &configmap)
	if err != nil {
		return microerror.Mask(err)
	}
	data := configmap.Data["ClusterConfiguration"]

	// remove etcd cluster
	configmap.Data["ClusterConfiguration"] = cleanEtcdInitialCluster(data)

	updateConfigmap := func() error {
		err = s.clusterInfo.KubernetesControllerClient.Update(ctx, &configmap)
		if err != nil {
			return microerror.Mask(err)
		}
		return nil
	}
	err = backoff.Retry(updateConfigmap, s.backOff)
	if err != nil {
		return microerror.Mask(err)
	}

	err = cleanEtcdInitialClusterInFile(clusterAppYamlFile(s.clusterInfo.Name))
	if err != nil {
		return microerror.Mask(err)
	}

	return nil
}

func extractAwsAccountIDfromARN(arn string) (string, error) {
	a, err := awsarn.Parse(arn)
	if err != nil {
		return "", microerror.Mask(err)
	}

	return a.AccountID, nil
}

func awsClusterRoleIdentityName(clusterName string) string {
	return fmt.Sprintf("%s-aws-cluster-role-identity", clusterName)
}

// cleanEtcdInitialCluster removes the entry from teh configmap that contains the etcd cluster
func cleanEtcdInitialCluster(input string) string {
	regex := regexp.MustCompile(`\s*initial-cluster: .+\n`)

	// Replace the matching line with new line only
	output := regex.ReplaceAllString(input, "\n")

	fmt.Printf("Removed initial-cluster from kubeadm configmap\n")
	return output
}

func cleanEtcdInitialClusterInFile(filename string) error {
	// read file
	f, err := os.OpenFile(filename, os.O_RDONLY, 0640)
	if err != nil {
		return microerror.Mask(err)
	}
	// read whole content of the file
	content, err := io.ReadAll(f)
	if err != nil {
		return microerror.Mask(err)
	}

	f2, err := os.OpenFile(filename, os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		return microerror.Mask(err)
	}

	updatedContent := cleanEtcdInitialCluster(string(content))
	// write text to the file and override existing content
	_, err = f2.WriteString(updatedContent)
	if err != nil {
		return microerror.Mask(err)
	}
	return nil
}

func (s *Service) waitForCapiControlPlaneNodesReady(ctx context.Context, count int) error {
	// wait for 3 nodes with status Ready
	labels := client.MatchingLabels{
		"node-role.kubernetes.io/control-plane": "",
	}
	s.waitForCapiNodesReady(ctx, labels, count)
	return nil
}

func (s *Service) waitForCapiNodePoolNodesReady(ctx context.Context, nodePoolName string) error {
	capiLabels := client.MatchingLabels{
		"giantswarm.io/machine-pool": fmt.Sprintf("%s-%s", s.clusterInfo.Name, nodePoolName),
	}
	nodeCount, err := s.vintageNodePoolNodeCount(nodePoolName)
	if err != nil {
		return microerror.Mask(err)
	}

	s.waitForCapiNodesReady(ctx, capiLabels, nodeCount)
	return nil
}

func (s *Service) waitForCapiNodesReady(ctx context.Context, labels client.MatchingLabels, count int) {
	var nodeList v1.NodeList
	color.Yellow("Waiting for %d CAPI node with status Ready", count)
	counter := 0
	readyNodes := map[string]string{}
	for {
		err := s.clusterInfo.KubernetesControllerClient.List(ctx, &nodeList, labels)
		if err != nil {
			fmt.Printf("ERROR: failed to get nodes : %s, retrying in 5 sec\n", err.Error())
			time.Sleep(time.Second * 5)
			continue
		}
		for _, node := range nodeList.Items {
			// aws-operator label exists only on the vintage node so if it is missing it is a CAPI node
			if _, ok := node.Labels["aws-operator.giantswarm.io/version"]; ok {
				continue
			} else {
				for _, condition := range node.Status.Conditions {
					if condition.Type == v1.NodeReady && condition.Status == v1.ConditionTrue {
						if _, ok := readyNodes[node.Name]; !ok {
							readyNodes[node.Name] = ""
							fmt.Printf("\nCAPI node %s ready. %d/%d\n", node.Name, len(readyNodes), count)
						}
						if len(readyNodes) == count {
							color.Yellow("\nFound CAPI %d nodes with status Ready, waited for %d sec.\n", count, counter)
							return
						}
					}
				}
			}

		}
		fmt.Printf(".")
		time.Sleep(time.Second * 10)
		counter += 10
	}
}

func (s *Service) waitForKubeadmControlPlaneReady(ctx context.Context) error {
	color.Green("Waiting KubeadmControlPlane to stabilise  (all replicas to be up to date and ready).")
	for {
		var cp kubeadmv1beta1.KubeadmControlPlane
		err := s.clusterInfo.MC.CapiKubernetesClient.Get(ctx, client.ObjectKey{Name: s.clusterInfo.Name, Namespace: s.clusterInfo.Namespace}, &cp)
		if err != nil {
			fmt.Printf("failed to get kubeadmControlPlane CR, retrying in 5 sec")
			time.Sleep(time.Second * 5)
		}
		replicas := *cp.Spec.Replicas

		if cp.Status.Ready && cp.Status.Replicas == replicas && cp.Status.ReadyReplicas == replicas && cp.Status.UpdatedReplicas == replicas {
			fmt.Printf("\nKubeadmControlPlane is stabilised.\n")
			return nil
		} else {
			time.Sleep(time.Second * 5)
			fmt.Printf(".")
		}
	}
}
