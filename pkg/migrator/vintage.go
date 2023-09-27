package migrator

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/fatih/color"
	giantswarmawsalpha3 "github.com/giantswarm/apiextensions/v6/pkg/apis/infrastructure/v1alpha3"
	"github.com/giantswarm/apiextensions/v6/pkg/label"
	"github.com/giantswarm/k8smetadata/pkg/annotation"
	"github.com/giantswarm/microerror"
	v1apps "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/kubectl/pkg/drain"
	capi "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/giantswarm/capi-migration-cli/pkg/templates"
)

const (
	AWSOperatorVersionLabel = "aws-operator.giantswarm.io/version"
)

type VintageCRs struct {
	AwsCluster            *giantswarmawsalpha3.AWSCluster
	AwsControlPlane       *giantswarmawsalpha3.AWSControlPlane
	AwsMachineDeployments []giantswarmawsalpha3.AWSMachineDeployment

	Cluster *capi.Cluster
}

// fetchVintageCRs fetches necessary CRs from vintage MC
func fetchVintageCRs(ctx context.Context, k8sClient client.Client, clusterName string) (*VintageCRs, error) {
	awsCluster, err := readAWSCluster(ctx, k8sClient, clusterName)
	if err != nil {
		return nil, microerror.Mask(err)
	}

	awsControlPlane, err := readAwsControlPlane(ctx, k8sClient, clusterName)
	if err != nil {
		return nil, microerror.Mask(err)
	}

	awsMachineDeployments, err := readAWSMachineDeployment(ctx, k8sClient, clusterName)
	if err != nil {
		return nil, microerror.Mask(err)
	}

	cluster, err := readCluster(ctx, k8sClient, clusterName)
	if err != nil {
		return nil, microerror.Mask(err)
	}

	crs := &VintageCRs{
		AwsCluster:            awsCluster,
		AwsControlPlane:       awsControlPlane,
		AwsMachineDeployments: awsMachineDeployments,

		Cluster: cluster,
	}
	return crs, nil
}

func readCluster(ctx context.Context, k8sClient client.Client, clusterName string) (*capi.Cluster, error) {
	objList := &capi.ClusterList{}
	selector := client.MatchingLabels{capi.ClusterNameLabel: clusterName}
	err := k8sClient.List(ctx, objList, selector)
	if err != nil {
		return nil, microerror.Mask(err)
	}

	if len(objList.Items) == 0 {
		return nil, microerror.Maskf(executionFailedError, "Cluster not found for %q", clusterName)
	}

	if len(objList.Items) > 1 {
		return nil, microerror.Maskf(executionFailedError, "more than one Cluster for cluster ID %q", clusterName)
	}

	obj := objList.Items[0]
	return &obj, nil
}

func readAWSCluster(ctx context.Context, k8sClient client.Client, clusterName string) (*giantswarmawsalpha3.AWSCluster, error) {
	objList := &giantswarmawsalpha3.AWSClusterList{}
	selector := client.MatchingLabels{capi.ClusterNameLabel: clusterName}
	err := k8sClient.List(ctx, objList, selector)
	if err != nil {
		return nil, microerror.Mask(err)
	}

	if len(objList.Items) == 0 {
		return nil, microerror.Maskf(executionFailedError, "AWSCluster not found for %q", clusterName)
	}

	if len(objList.Items) > 1 {
		return nil, microerror.Maskf(executionFailedError, "more than one AWSCluster for cluster ID %q", clusterName)
	}

	obj := objList.Items[0]
	return &obj, nil
}

func readAwsControlPlane(ctx context.Context, k8sClient client.Client, clusterName string) (*giantswarmawsalpha3.AWSControlPlane, error) {
	objList := &giantswarmawsalpha3.AWSControlPlaneList{}
	selector := client.MatchingLabels{capi.ClusterNameLabel: clusterName}
	err := k8sClient.List(ctx, objList, selector)
	if err != nil {
		return nil, microerror.Mask(err)
	}

	if len(objList.Items) == 0 {
		return nil, microerror.Maskf(executionFailedError, "AWSControlPlane not found for %q", clusterName)
	}

	if len(objList.Items) > 1 {
		return nil, microerror.Maskf(executionFailedError, "more than one AWSControlPlane for cluster ID %q", clusterName)
	}

	obj := objList.Items[0]
	return &obj, nil
}

func readAWSMachineDeployment(ctx context.Context, k8sClient client.Client, clusterName string) ([]giantswarmawsalpha3.AWSMachineDeployment, error) {
	objList := &giantswarmawsalpha3.AWSMachineDeploymentList{}
	selector := client.MatchingLabels{capi.ClusterNameLabel: clusterName}
	err := k8sClient.List(ctx, objList, selector)
	if err != nil {
		return nil, microerror.Mask(err)
	}
	return objList.Items, nil
}

// stopVintageReconciliation will remove AWSOperator label from the vintage CRs and also clears all finalizers
func stopVintageReconciliation(ctx context.Context, k8sClient client.Client, crs *VintageCRs) error {
	// AWSCluster
	delete(crs.AwsCluster.Labels, label.AWSOperatorVersion)
	crs.AwsCluster.Finalizers = nil
	err := k8sClient.Update(ctx, crs.AwsCluster)
	if err != nil {
		return microerror.Mask(err)
	}

	// AWSControlPlane
	delete(crs.AwsControlPlane.Labels, label.AWSOperatorVersion)
	crs.AwsControlPlane.Finalizers = nil
	err = k8sClient.Update(ctx, crs.AwsControlPlane)
	if err != nil {
		return microerror.Mask(err)
	}

	// AWSMachineDeployment
	for i := range crs.AwsMachineDeployments {
		md := crs.AwsMachineDeployments[i]
		delete(md.Labels, label.AWSOperatorVersion)
		md.Finalizers = nil
		err = k8sClient.Update(ctx, &md)
		if err != nil {
			return microerror.Mask(err)
		}
	}
	return nil
}

func disableVintageHealthCheck(ctx context.Context, k8sClient client.Client, crs *VintageCRs) error {
	if crs.AwsCluster.Annotations == nil {
		crs.AwsCluster.Annotations = map[string]string{}
	}
	crs.AwsCluster.Annotations[annotation.NodeTerminateUnhealthy] = "false"
	err := k8sClient.Update(ctx, crs.AwsCluster)
	if err != nil {
		return microerror.Mask(err)
	}
	return nil
}

func scaleDownVintageAppOperator(ctx context.Context, k8sClient client.Client, clusterName string) error {
	// fetch app-operator deployment named app-operator-CLUSTER_NAME in namespace clusterName
	var deployment v1apps.Deployment
	err := k8sClient.Get(ctx, client.ObjectKey{Name: fmt.Sprintf("app-operator-%s", clusterName), Namespace: clusterName}, &deployment)
	if err != nil {
		return microerror.Mask(err)
	}

	// set replicas to zero na update the deployment
	deployment.Spec.Replicas = new(int32)
	err = k8sClient.Update(ctx, &deployment)
	if err != nil {
		return microerror.Mask(err)
	}
	return nil
}

func deleteCapiAppOperatorPod(ctx context.Context, k8sClient client.Client, clusterName string) error {
	// fetch app-operator deployment named app-operator-CLUSTER_NAME in namespace clusterName
	var podList v1.PodList
	err := k8sClient.List(ctx, &podList, client.MatchingLabels{"app.kubernetes.io/instance": fmt.Sprintf("%s-app-operator", clusterName)})
	if err != nil {
		return microerror.Mask(err)
	}

	for _, pod := range podList.Items {
		// delete the pod
		err = k8sClient.Delete(ctx, &pod) //gosec:ignore=G601
		if apierrors.IsNotFound(err) {
			// vanished, lets continue
		} else if err != nil {
			return microerror.Mask(err)
		}
		fmt.Printf("Deleted pod %s/%s on CAPI MC to force reconcilation\n", pod.Namespace, pod.Name)
	}
	return nil
}

func fetchVintageClusterAccountRole(ctx context.Context, k8sClient client.Client, secretName string, secretNamespace string) (string, error) {
	var secret v1.Secret
	err := k8sClient.Get(ctx, client.ObjectKey{Name: secretName, Namespace: secretNamespace}, &secret)
	if err != nil {
		return "", microerror.Mask(err)
	}

	roleArn, ok := secret.Data["aws.admin.arn"]
	if !ok {
		return "", microerror.Mask(invalidCredentialSecretError)
	}

	return string(roleArn), nil
}

func getClusterDescription(crs *VintageCRs) string {
	val, ok := crs.Cluster.Annotations["cluster.giantswarm.io/description"]
	if ok {
		return val
	}

	return ""
}

type awsOperatorConfigMapDataValues struct {
	Installation Installation `json:"Installation"`
}
type Installation struct {
	V1 V1 `json:"V1"`
}
type V1 struct {
	Guest Guest `json:"Guest"`
}
type Guest struct {
	Kubernetes Kubernetes `json:"Kubernetes"`
}
type Kubernetes struct {
	API API `json:"API"`
}
type API struct {
	ClusterIPRange string `json:"ClusterIPRange"`
}

func getClusterServiceCidrBlock(ctx context.Context, k8sClient client.Client, crs *VintageCRs) (string, error) {
	awsOperatorVersion := crs.AwsCluster.Labels[label.AWSOperatorVersion]

	var configmap v1.ConfigMap
	err := k8sClient.Get(ctx, client.ObjectKey{Name: fmt.Sprintf("aws-operator-%s-chart-values", awsOperatorVersion), Namespace: "giantswarm"}, &configmap)
	if err != nil {
		return "", microerror.Mask(err)
	}

	val, ok := configmap.Data["values"]
	if !ok {
		return "", microerror.Maskf(executionFailedError, "could not find values in configmap")
	}

	var awsConfigData awsOperatorConfigMapDataValues
	err = yaml.Unmarshal([]byte(val), &awsConfigData)
	if err != nil {
		return "", microerror.Mask(err)
	}

	return awsConfigData.Installation.V1.Guest.Kubernetes.API.ClusterIPRange, nil
}

// drainVintageNodes drains all vintage nodes of specific role
func (s *Service) drainVintageNodes(ctx context.Context, labels client.MatchingLabels) error {
	nodes, err := getVintageNodes(ctx, s.clusterInfo.KubernetesControllerClient, labels)
	if err != nil {
		return microerror.Mask(err)
	}
	fmt.Printf("Found %d nodes for draining\n", len(nodes))

	nodeShutdownHelper := drain.Helper{
		Ctx:                             ctx,                               // pass the current context
		Client:                          s.clusterInfo.KubernetesClientSet, // the k8s client for making the API calls
		Force:                           true,                              // forcing the draining
		GracePeriodSeconds:              60,                                // 60 seconds of timeout before deleting the pod
		IgnoreAllDaemonSets:             true,                              // ignore the daemonsets
		Timeout:                         5 * time.Minute,                   // give a 5 minutes timeout
		DeleteEmptyDirData:              true,                              // delete all the emptyDir volumes
		DisableEviction:                 false,                             // we want to evict and not delete. (might be different for the master nodes)
		SkipWaitForDeleteTimeoutSeconds: 15,                                // in case a node is NotReady then the pods won't be deleted, so don't wait too long
		Out:                             os.Stdout,
		ErrOut:                          os.Stderr,
	}
	var wg sync.WaitGroup
	// Loop through the list of nodes
	for _, node := range nodes {
		wg.Add(1)

		go func(node v1.Node) {
			defer wg.Done()

			fmt.Printf("Started draining node %s\n", node.Name)

			err := drain.RunCordonOrUncordon(&nodeShutdownHelper, &node, true)
			if err != nil {
				fmt.Printf("ERRROR: failed cordon node %s, reason: %s\n", node.Name, err.Error())
			}

			err = drain.RunNodeDrain(&nodeShutdownHelper, node.Name)
			if err != nil {
				fmt.Printf("ERRROR: failed to drain node %s, reason: %s\n", node.Name, err.Error())
			}
			color.Yellow("Finished draining node %s", node.Name)

		}(node)
	}

	// Wait for all goroutines to finish
	wg.Wait()
	return nil
}

// cordonVintageNodes cordons all vintage nodes of specific role
func (s *Service) cordonVintageNodes(ctx context.Context, labels client.MatchingLabels) error {
	nodes, err := getVintageNodes(ctx, s.clusterInfo.KubernetesControllerClient, labels)
	if err != nil {
		return microerror.Mask(err)
	}
	fmt.Printf("Found %d nodes for cordoning\n", len(nodes))

	nodeShutdownHelper := drain.Helper{
		Ctx:                             ctx,                               // pass the current context
		Client:                          s.clusterInfo.KubernetesClientSet, // the k8s client for making the API calls
		Force:                           true,                              // forcing the draining
		GracePeriodSeconds:              60,                                // 60 seconds of timeout before deleting the pod
		IgnoreAllDaemonSets:             true,                              // ignore the daemonsets
		Timeout:                         5 * time.Minute,                   // give a 5 minutes timeout
		DeleteEmptyDirData:              true,                              // delete all the emptyDir volumes
		DisableEviction:                 false,                             // we want to evict and not delete. (might be different for the master nodes)
		SkipWaitForDeleteTimeoutSeconds: 15,                                // in case a node is NotReady then the pods won't be deleted, so don't wait too long
		Out:                             os.Stdout,
		ErrOut:                          os.Stderr,
	}

	for i := range nodes {
		err := drain.RunCordonOrUncordon(&nodeShutdownHelper, &nodes[i], true)
		if err != nil {
			fmt.Printf("ERRROR: failed cordon node %s, reason: %s\n", nodes[i].Name, err.Error())
		}
	}
	return nil
}

// getVintageNodes returns all nodes with AWSOperatorVersionLabel label
func getVintageNodes(ctx context.Context, k8sClient client.Client, labels client.MatchingLabels) ([]v1.Node, error) {
	var nodes v1.NodeList
	err := k8sClient.List(ctx, &nodes, labels)
	if err != nil {
		return nil, microerror.Mask(err)
	}
	var nodeList []v1.Node
	for _, node := range nodes.Items {
		// only vintage nodes have AWSOperator label
		if _, ok := node.Labels[AWSOperatorVersionLabel]; ok {
			nodeList = append(nodeList, node)
		}
	}

	return nodeList, nil
}

// deleteChartOperatorPod deletes chart-operator pods in the WC cluster to reschedule it on new CAPI control-plane-node
func (s *Service) deleteChartOperatorPods(ctx context.Context) error {
	// fetch chart-operator pod
	var pods v1.PodList
	err := s.clusterInfo.KubernetesControllerClient.List(ctx, &pods, client.MatchingLabels{"app": "chart-operator"}, client.InNamespace("giantswarm"))
	if err != nil {
		return microerror.Mask(err)
	}

	for i := range pods.Items {
		// delete the pod
		err = s.clusterInfo.KubernetesControllerClient.Delete(ctx, &pods.Items[i])
		if apierrors.IsNotFound(err) {
			// vanished, lets continue
		} else if err != nil {
			return microerror.Mask(err)
		}
		fmt.Printf("Deleted pod %s/%s to reschedule it on CAPI control plane node\n", pods.Items[i].Namespace, pods.Items[i].Name)
	}

	return nil
}

// stopVintageControlPlaneComponents will run a job on each pod that will remove static manifest fot control-plane components
func (s *Service) stopVintageControlPlaneComponents(ctx context.Context) error {
	color.Yellow("Removing static manifests for control-plane components on all vintage nodes.")
	// fetch all master nodes
	var nodes v1.NodeList
	err := s.clusterInfo.KubernetesControllerClient.List(ctx, &nodes, client.MatchingLabels{"node-role.kubernetes.io/master": ""})
	if err != nil {
		return microerror.Mask(err)
	}

	var jobList []batchv1.Job
	// run a job on each node that will remove static manifest fot control-plane components
	for _, node := range nodes.Items {
		if _, ok := node.Labels[AWSOperatorVersionLabel]; !ok {
			// Skipping node  it is a CAPI node
			continue
		}
		job := templates.CleanManifestsJob(node.Name)

		err = s.clusterInfo.KubernetesControllerClient.Create(ctx, &job)
		if err != nil {
			return microerror.Mask(err)
		}
		jobList = append(jobList, job)
		fmt.Printf("Created job %s\n", job.Name)
	}

	fmt.Printf("Waiting until all jobs finished\n")
	// wait until all jobs finished
	for i := range jobList {
		for {
			var job batchv1.Job
			err = s.clusterInfo.KubernetesControllerClient.Get(ctx, client.ObjectKey{Name: jobList[i].Name, Namespace: jobList[i].Namespace}, &job)
			if apierrors.IsNotFound(err) {
				// job is still being created
				time.Sleep(time.Second * 5)
				continue
			} else if err != nil {
				return microerror.Mask(err)
			}

			if job.Status.Succeeded == 1 {
				fmt.Printf("Job %d/%d - %s finished.\n", i+1, len(jobList), job.Name)
				break
			} else {
				// fmt.Printf(".")
				fmt.Printf("Job %d/%d - %s not finished yet, waiting 5 sec.\n", i+1, len(jobList), job.Name)
				time.Sleep(time.Second * 5)
			}
		}
	}
	fmt.Printf("\n")

	return nil
}

func (s *Service) vintageNodePoolNodeCount(nodePoolName string) (int, error) {
	var nodes v1.NodeList
	err := s.clusterInfo.KubernetesControllerClient.List(context.Background(), &nodes, client.MatchingLabels{"giantswarm.io/machine-deployment": nodePoolName})
	if err != nil {
		return 0, microerror.Mask(err)
	}
	nodeCount := 0
	for _, node := range nodes.Items {
		if _, ok := node.Labels[AWSOperatorVersionLabel]; ok {
			nodeCount++
		}
	}

	return nodeCount, nil
}

func controlPlaneNodeLabels() client.MatchingLabels {
	return client.MatchingLabels{"node-role.kubernetes.io/control-plane": ""}
}

func vintageNodePoolNodeLabels(nodePoolName string) client.MatchingLabels {
	return client.MatchingLabels{
		"node-role.kubernetes.io/worker":   "",
		"giantswarm.io/machine-deployment": nodePoolName,
	}
}
