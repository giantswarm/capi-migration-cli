package migrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	awsarn "github.com/aws/aws-sdk-go/aws/arn"
	"github.com/fatih/color"
	"github.com/giantswarm/microerror"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
			"roleArn": fmt.Sprintf("arn:aws:iam::%s:role/giantswarm-%s-capa-controller", accountID, s.clusterInfo.MC.CapiMC),
			"allowedNamespaces": map[string]interface{}{
				"namespaceList": nil,
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{},
				},
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

func (s *Service) createCAPICluster() error {
	fmt.Printf("Applying CAPI cluster APP CR to MC\n\n")
	c := exec.Command("kubectl", "apply", "-f", clusterAppYamlFile(s.clusterInfo.Name))

	c.Stderr = os.Stderr
	c.Stdin = os.Stdin

	err := c.Run()
	if err != nil {
		return microerror.Mask(err)
	}
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

	err = s.clusterInfo.KubernetesControllerClient.Update(ctx, &configmap)
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

// cleanEtcdInitialCluster removes old and non-existing entries from initial-cluster in kubeadm configmap
func cleanEtcdInitialCluster(input string) string {
	// Define a regular expression pattern to match the unwanted lines in initial-cluster.
	pattern := `etcd\d=.*?:\d+,?`

	// Compile the regular expression pattern.
	regex := regexp.MustCompile(pattern)

	// Find all matches in the input string.
	matches := regex.FindAllStringSubmatch(input, -1)

	updatedInput := input
	// Iterate through the matches and remove them from the input
	for _, match := range matches {
		updatedInput = strings.Replace(updatedInput, match[0], "", 1)
		fmt.Printf("Removed %s from the etcd initial-cluster in kubeadm-config configmap.\n", match[0])
	}

	updatedInput = strings.Replace(updatedInput, ",", "", 1)

	return updatedInput
}

func (s *Service) waitForCapiControlPlaneNodeReady(ctx context.Context) {
	var nodeList v1.NodeList

	color.Yellow("Waiting for CAPI Control Plane node with status Ready")
	counter := 0
	for {
		err := s.clusterInfo.KubernetesControllerClient.List(ctx, &nodeList, client.MatchingLabels{
			"node-role.kubernetes.io/control-plane": "",
		})
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
					if condition.Type == v1.NodeReady {
						fmt.Printf("Found CAPI control plane node %s with status Ready, waited for %d sec.\n", node.Name, counter)
						return
					}
				}
			}

		}
		fmt.Printf("Did not find CAPI Control Plane node with status Ready yet. Retrying in 10 sec.\n")
		time.Sleep(time.Second * 10)
		counter += 10
	}
}
