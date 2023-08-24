package migrator

import (
	"fmt"

	awsarn "github.com/aws/aws-sdk-go/aws/arn"
	"github.com/giantswarm/microerror"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	capa "sigs.k8s.io/cluster-api-provider-aws/api/v1beta1"
)

func (s *Service) createAWSClusterRoleIdentity(ctx context.Context, vintageRoleARN string) error {
	accountID, err := extractAwsAccountIDfromARN(vintageRoleARN)
	if err != nil {
		return microerror.Mask(err)
	}

	awsClusterRoleIdentity := &capa.AWSClusterRoleIdentity{
		ObjectMeta: metav1.ObjectMeta{
			Name: awsClusterRoleIdentityName(s.clusterInfo.Name),
			Labels: map[string]string{
				"giantswarm.io/cluster":      s.clusterInfo.Name,
				"giantswarm.io/organization": organizationFromNamespace(s.clusterInfo.Namespace),
			},
		},
		Spec: capa.AWSClusterRoleIdentitySpec{
			SourceIdentityRef: &capa.AWSIdentityReference{
				Kind: "AWSClusterControllerIdentity",
				Name: capa.AWSClusterControllerIdentityName,
			},
			AWSRoleSpec: capa.AWSRoleSpec{
				RoleArn: fmt.Sprintf("arn:aws:iam::%s:role/giantswarm-%s-capa-controller", accountID, s.clusterInfo.MC.CapiMC),
			},
		},
	}

	err = s.clusterInfo.MC.CapiKubernetesClient.Create(ctx, awsClusterRoleIdentity)

	if apierrors.IsAlreadyExists(err) {
		// It's fine. No worries.
	} else if err != nil {
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
