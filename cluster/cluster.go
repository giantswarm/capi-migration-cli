package cluster

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sts"
	"github.com/fatih/color"
	giantswarmawsalpha3 "github.com/giantswarm/apiextensions/v6/pkg/apis/infrastructure/v1alpha3"
	"github.com/giantswarm/microerror"
	vaultapi "github.com/hashicorp/vault/api"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/oidc"
	"k8s.io/client-go/tools/clientcmd"
	capi "sigs.k8s.io/cluster-api/api/v1beta1"
	kubeadmv1beta1 "sigs.k8s.io/cluster-api/controlplane/kubeadm/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = capi.AddToScheme(scheme)
	_ = giantswarmawsalpha3.AddToScheme(scheme)
	_ = kubeadmv1beta1.AddToScheme(scheme)

}

type Config struct {
	MCCapi           string
	MCVintage        string
	ClusterName      string
	ClusterNamespace string
}

type Cluster struct {
	Name                       string
	Namespace                  string
	Region                     string
	KubernetesControllerClient client.Client
	KubernetesClientSet        kubernetes.Interface

	AWSSession *session.Session
	MC         *ManagementCluster
}

type ManagementCluster struct {
	CapiMC    string
	VintageMC string

	CapiKubernetesClient    client.Client
	VintageKubernetesClient client.Client
	VaultClient             *vaultapi.Client
}

func New(c Config) (*Cluster, error) {
	color.Yellow("Checking kubernetes client for Vintage MC %s.", c.MCVintage)
	vintageKubernetesClient, _, err := loginOrReuseKubeconfig([]string{c.MCVintage})
	if err != nil {
		return nil, microerror.Mask(err)
	}

	color.Yellow("Checking kubernetes client for the cluster %s.", c.ClusterName)
	clusterKubernetesClient, clusterClientSet, err := loginOrReuseKubeconfig([]string{c.MCVintage, c.ClusterName})
	if err != nil {
		return nil, microerror.Mask(err)
	}

	color.Yellow("Checking kubernetes client for CAPI MC %s.", c.MCCapi)
	capiKubernetesClient, _, err := loginOrReuseKubeconfig([]string{c.MCCapi})
	if err != nil {
		return nil, microerror.Mask(err)
	}

	color.Yellow("Generating AWS credentials for cluster %s/%s.", c.MCVintage, c.ClusterName)
	var clusterRegion string
	var awsSession *session.Session
	{
		var awsCredentials *credentials.Credentials
		awsCredentials, clusterRegion, err = getAWSCredentials(c.MCVintage, c.ClusterName)
		if err != nil {
			return nil, microerror.Mask(err)
		}

		// test credentials
		awsSession, err = session.NewSession(&aws.Config{
			Region:      aws.String(clusterRegion),
			Credentials: awsCredentials,
		})
		if err != nil {
			return nil, microerror.Mask(err)
		}
		stsClient := sts.New(awsSession, &aws.Config{Region: aws.String(clusterRegion)})
		identity, err := stsClient.GetCallerIdentity(&sts.GetCallerIdentityInput{})
		if err != nil {
			return nil, microerror.Mask(err)
		}
		fmt.Printf("Generated AWS credentials for %s\n", *identity.Arn)
	}

	color.Yellow("Checking %s's vault connection", c.MCVintage)
	var vaultClient *vaultapi.Client
	{
		addr, token, caPath, err := getVaultInfo(c.MCVintage)
		if err != nil {
			return nil, microerror.Mask(err)
		}
		vc := vaultapi.DefaultConfig()
		vc.Address = addr
		err = vc.ConfigureTLS(&vaultapi.TLSConfig{
			CAPath: caPath,
		})
		if err != nil {
			return nil, microerror.Mask(err)
		}
		vaultClient, err = vaultapi.NewClient(vc)
		if err != nil {
			return nil, microerror.Mask(err)
		}
		vaultClient.SetToken(token)

		// Check vault connectivity.
		healthStatus, err := vaultClient.Sys().Health()
		if err != nil {
			return nil, microerror.Mask(err)
		}
		fmt.Printf("Connected to vault %s\n", healthStatus.Version)

	}

	color.Green("Init phase finished.\n\n")

	return &Cluster{
		Name:                       c.ClusterName,
		Namespace:                  c.ClusterNamespace,
		Region:                     clusterRegion,
		KubernetesControllerClient: clusterKubernetesClient,
		KubernetesClientSet:        clusterClientSet,

		AWSSession: awsSession,
		MC: &ManagementCluster{
			CapiMC:                  c.MCCapi,
			VintageMC:               c.MCVintage,
			VintageKubernetesClient: vintageKubernetesClient,
			CapiKubernetesClient:    capiKubernetesClient,
			VaultClient:             vaultClient,
		},
	}, nil
}

func (c *Cluster) RefreshAWSCredentials() error {
	awsCredentials, clusterRegion, err := getAWSCredentials(c.MC.VintageMC, c.Name)
	if err != nil {
		return microerror.Mask(err)
	}

	awsSession, err := session.NewSession(&aws.Config{
		Region:      aws.String(clusterRegion),
		Credentials: awsCredentials,
	})
	if err != nil {
		return microerror.Mask(err)
	}

	c.AWSSession = awsSession

	return nil
}

// RefreshAWSCredentialsIfExpired will refresh AWS credentials if they are expired
// it will return true in case the credentials were refreshed or false in cas they are stil valid
func (c *Cluster) RefreshAWSCredentialsIfExpired() (bool, error) {
	// Create an STS service client
	svc := sts.New(c.AWSSession)

	// Call GetCallerIdentity operation to check the validity of the credentials
	input := &sts.GetCallerIdentityInput{}
	_, err := svc.GetCallerIdentity(input)
	if err != nil {
		// Handle error. If the error message contains "ExpiredToken", credentials have expired.
		color.Red("AWS Credential expired, need to generate new credentials")
		err := c.RefreshAWSCredentials()
		if err != nil {
			fmt.Printf("failed to refresh credentials\n")
			return false, microerror.Mask(err)
		} else {
			color.Green("refreshed AWS credentials")
			return true, nil
		}
	} else {
		fmt.Printf("AWS Credentials are still valid, no need to refresh\n")
	}
	return false, nil
}

// LoginOrReuseKubeconfig will return k8s client for the specific wc or MC client, it will try if there is already existing context or login if its missing
func loginOrReuseKubeconfig(cluster []string) (client.Client, kubernetes.Interface, error) {
	ctrlClient, clientSet, err := getK8sClientFromKubeconfig(contextNameFromCluster(cluster))
	if err != nil && strings.Contains(err.Error(), "does not exist") {
		// login
		fmt.Printf("Context for cluster %s not found, executing 'opsctl login', check your browser window.\n", cluster)
		err = loginIntoCLuster(cluster)
		if err != nil {
			return nil, nil, microerror.Mask(err)
		}
		// now retry
		ctrlClient, clientSet, err = getK8sClientFromKubeconfig(contextNameFromCluster(cluster))
		if err != nil {
			return nil, nil, microerror.Mask(err)
		}
	} else if err != nil {
		return nil, nil, microerror.Mask(err)
	}
	return ctrlClient, clientSet, nil
}

func getK8sClientFromKubeconfig(contextName string) (client.Client, kubernetes.Interface, error) {
	kubeconfigFile := os.Getenv("KUBECONFIG")
	if kubeconfigFile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, microerror.Mask(err)
		}
		kubeconfigFile = fmt.Sprintf("%s/.kube/config", home)
	}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigFile},
		&clientcmd.ConfigOverrides{
			CurrentContext: contextName,
		}).ClientConfig()

	if err != nil {
		return nil, nil, microerror.Mask(err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, microerror.Mask(err)
	}
	v, err := clientset.ServerVersion()
	if err != nil {
		return nil, nil, microerror.Mask(err)
	}
	fmt.Printf("Connecned to %s, k8s server version %s\n", contextName, v.String())

	ctrlClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return nil, nil, microerror.Mask(err)
	}

	return ctrlClient, clientset, nil
}

// LoginIntoCluster will login into cluster by executing opsctl login command
func loginIntoCLuster(cluster []string) error {
	args := append([]string{"login", "--no-cache"}, cluster...)
	c := exec.Command("opsctl", args...)

	c.Stderr = os.Stderr
	c.Stdin = os.Stdin

	err := c.Run()
	if err != nil {
		return microerror.Mask(err)
	}
	return nil
}

// getVaultInfo will try a vault config for specific installation
func getVaultInfo(installation string) (string, string, string, error) {
	c := exec.Command("opsctl", "create", "vaultconfig", "-i", installation, "--no-cache")

	c.Stderr = os.Stderr

	out, err := c.Output()
	if err != nil {
		return "", "", "", microerror.Mask(err)
	}
	// Define the regex patterns to match the variables and their values
	addrPattern := regexp.MustCompile(`export VAULT_ADDR=(.+)`)
	tokenPattern := regexp.MustCompile(`export VAULT_TOKEN=(.+)`)
	capathPattern := regexp.MustCompile(`export VAULT_CAPATH=(.+)`)

	// Find matches using the regex patterns
	addrMatch := addrPattern.FindSubmatch(out)
	tokenMatch := tokenPattern.FindSubmatch(out)
	capathMatch := capathPattern.FindSubmatch(out)

	// Extract values from the matches
	var addr, token, caPath string
	if len(addrMatch) > 1 {
		addr = strings.TrimSpace(string(addrMatch[1]))
	} else {
		return "", "", "", microerror.Maskf(executionFailedError, "could not find VAULT_ADDR in the  output")
	}
	if len(tokenMatch) > 1 {
		token = strings.TrimSpace(string(tokenMatch[1]))
	} else {
		return "", "", "", microerror.Maskf(executionFailedError, "could not find VAULT_TOKEN in the  output")
	}
	if len(capathMatch) > 1 {
		caPath = strings.TrimSpace(string(capathMatch[1]))
	} else {
		return "", "", "", microerror.Maskf(executionFailedError, "could not find VAULT_CAPATH in the  output")
	}

	return addr, token, caPath, nil
}

// getAWSCredentials will gets aws credentials for specific cluster via opsctl credentials aws command
func getAWSCredentials(installation string, clusterName string) (*credentials.Credentials, string, error) {
	c := exec.Command("opsctl", "credentials", "aws", "-i", installation, "-c", clusterName, "--no-cache")

	c.Stderr = os.Stderr
	c.Stdin = os.Stdin

	out, err := c.Output()
	if err != nil {
		return nil, "", microerror.Mask(err)
	}
	// Define the regex patterns to match the variables and their values
	accessKeyPattern := regexp.MustCompile(`export AWS_ACCESS_KEY_ID=(.+)`)
	secretKeyPattern := regexp.MustCompile(`export AWS_SECRET_ACCESS_KEY=(.+)`)
	sessionTokenPattern := regexp.MustCompile(`export AWS_SESSION_TOKEN=(.+)`)
	regionPattern := regexp.MustCompile(`export AWS_DEFAULT_REGION=(.+)`)

	// Find matches using the regex patterns
	accessKeyMatch := accessKeyPattern.FindSubmatch(out)
	secretKeyPMatch := secretKeyPattern.FindSubmatch(out)
	sessionTokenMatch := sessionTokenPattern.FindSubmatch(out)
	regionMatch := regionPattern.FindSubmatch(out)

	// Extract values from the matches
	var accesKey, secretKey, sessiontoken, region string
	if len(accessKeyMatch) > 1 {
		accesKey = strings.TrimSpace(string(accessKeyMatch[1]))
	} else {
		return nil, "", microerror.Maskf(executionFailedError, "could not find AWS_ACCESS_KEY_ID in the  output")
	}
	if len(secretKeyPMatch) > 1 {
		secretKey = strings.TrimSpace(string(secretKeyPMatch[1]))
	} else {
		return nil, "", microerror.Maskf(executionFailedError, "could not find AWS_SECRET_ACCESS_KEY in the  output")
	}
	if len(sessionTokenMatch) > 1 {
		sessiontoken = strings.TrimSpace(string(sessionTokenMatch[1]))
	} else {
		return nil, "", microerror.Maskf(executionFailedError, "could not find AWS_SESSION_TOKEN in the  output")
	}
	if len(regionMatch) > 1 {
		region = strings.TrimSpace(string(regionMatch[1]))
	} else {
		return nil, "", microerror.Maskf(executionFailedError, "could not find AWS_DEFAULT_REGION in the  output")
	}

	creds := credentials.NewStaticCredentials(accesKey, secretKey, sessiontoken)

	return creds, region, nil
}

func contextNameFromCluster(cluster []string) string {
	if len(cluster) == 1 {
		return fmt.Sprintf("gs-%s", cluster[0])
	} else {
		return fmt.Sprintf("gs-%s-%s-clientcert", cluster[0], cluster[1])
	}
}
