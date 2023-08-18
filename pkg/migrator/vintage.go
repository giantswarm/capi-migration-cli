package migrator

import (
	"context"

	"github.com/giantswarm/apiextensions/v3/pkg/label"
	giantswarmawsalpha3 "github.com/giantswarm/apiextensions/v6/pkg/apis/infrastructure/v1alpha3"
	"github.com/giantswarm/microerror"
	capi "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type VintageCRs struct {
	AwsCluster            *giantswarmawsalpha3.AWSCluster
	AwsControlPlane       *giantswarmawsalpha3.AWSControlPlane
	AwsMachineDeployments []giantswarmawsalpha3.AWSMachineDeployment
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

	crs := &VintageCRs{
		AwsCluster:            awsCluster,
		AwsControlPlane:       awsControlPlane,
		AwsMachineDeployments: awsMachineDeployments,
	}
	return crs, nil
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
	for _, md := range crs.AwsMachineDeployments {
		delete(md.Labels, label.AWSOperatorVersion)
		md.Finalizers = nil
		err = k8sClient.Update(ctx, &md)
		if err != nil {
			return microerror.Mask(err)
		}
	}
	return nil
}
