package migrator

import (
	"context"
	"fmt"

	"github.com/giantswarm/microerror"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/giantswarm/capi-migration-cli/pkg/templates"
)

const (
	joinEtcdClusterScriptKey      = "join-etcd-cluster"
	moveEtcdLeaderScriptKey       = "move-etcd-leader"
	apiHealthzVintagePodKey       = "api-healthz-vintage-pod"
	addExtraServiceAccountIssuers = "add-extra-service-account-issuers"
)

// migrateCAsSecrets fetches CA private key from vault and save it to 'clusterID-ca` and 'clusterID-etcd' secret into CAPI MC
func (s *Service) migrateCAsSecrets(ctx context.Context) error {
	// get CA private key and CA certificate from vault
	caPrivKey, caCertData, err := s.getCAData()
	if err != nil {
		return microerror.Mask(err)
	}
	// create cluster CA
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      caCertsSecretName(s.clusterInfo.Name),
			Namespace: s.clusterInfo.Namespace,
		},
		Data: map[string][]byte{
			"tls.crt": caCertData,
			"tls.key": caPrivKey,
		},
	}

	err = s.clusterInfo.MC.CapiKubernetesClient.Create(ctx, secret)
	if apierrors.IsAlreadyExists(err) {
		// ignore already exists error
	} else if err != nil {
		return microerror.Mask(err)
	}

	// create etcd CA
	etcdSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      etcdCertsSecretName(s.clusterInfo.Name),
			Namespace: s.clusterInfo.Namespace,
		},
		Data: map[string][]byte{
			"tls.crt": caCertData,
			"tls.key": caPrivKey,
		},
	}

	err = s.clusterInfo.MC.CapiKubernetesClient.Create(ctx, etcdSecret)
	if apierrors.IsAlreadyExists(err) {
		// It's fine. No worries.
	} else if err != nil {
		return microerror.Mask(err)
	}
	return nil
}

// migrateEncryptionSecret fetches encryption provider secret from vintage MC and creates it in CAPI MC
func (s *Service) migrateEncryptionSecret(ctx context.Context) error {
	encryptionSecret := &corev1.Secret{}
	key := ctrl.ObjectKey{
		Namespace: s.clusterInfo.Namespace,
		Name:      encryptionConfigSecretName(s.clusterInfo.Name),
	}

	err := s.clusterInfo.MC.VintageKubernetesClient.Get(ctx, key, encryptionSecret)
	if err != nil {
		return microerror.Mask(err)
	}

	encryptionSecret.ResourceVersion = ""

	err = s.clusterInfo.MC.CapiKubernetesClient.Create(ctx, encryptionSecret)
	if apierrors.IsAlreadyExists(err) {
		// It's fine. No worries.
	} else if err != nil {
		return microerror.Mask(err)
	}

	return nil
}

// migrateSASecret fetches service-account secret from vintage MC and creates it in CAPI MC
func (s *Service) migrateSASecret(ctx context.Context) error {
	saSecret := &corev1.Secret{}
	key := ctrl.ObjectKey{
		Namespace: s.clusterInfo.Namespace,
		Name:      saCertsSecretNameVintage(s.clusterInfo.Name),
	}

	err := s.clusterInfo.MC.VintageKubernetesClient.Get(ctx, key, saSecret)
	if err != nil {
		return microerror.Mask(err)
	}

	saSecret.Name = saCertsSecretNameCAPI(s.clusterInfo.Name)
	saSecret.Data["tls.crt"] = saSecret.Data["pub"]
	saSecret.Data["tls.key"] = saSecret.Data["key"]

	delete(saSecret.Data, "pub")
	delete(saSecret.Data, "key")

	saSecret.ResourceVersion = ""

	err = s.clusterInfo.MC.CapiKubernetesClient.Create(ctx, saSecret)
	if apierrors.IsAlreadyExists(err) {
		// It's fine. No worries.
	} else if err != nil {
		return microerror.Mask(err)
	}

	return nil
}

// createScriptsSecret creates a secret that will be mounted as a file into control-plane in order to join the vintage etcd cluster
func (s *Service) createScriptsSecret(ctx context.Context, baseDomain string) error {

	extraServiceAccountIssuers, err := s.getExtraServiceAccountIssuers()
	if err != nil {
		return microerror.Mask(err)
	}
	params := struct {
		ETCDEndpoint          string
		ServiceAccountIssuers []string
	}{
		ETCDEndpoint:          etcdEndpointFromDomain(baseDomain, s.clusterInfo.Name),
		ServiceAccountIssuers: extraServiceAccountIssuers,
	}

	joinEtcdClusterContent, err := templates.RenderTemplate(templates.AWSJoinCluster, params)
	if err != nil {
		return microerror.Mask(err)
	}

	moveEtcdLeaderContent, err := templates.RenderTemplate(templates.AWSMoveLeaderCommand, params)
	if err != nil {
		return microerror.Mask(err)
	}

	apiHealthzVintagePodContent, err := templates.RenderTemplate(templates.APIHealthzVintagePod, params)
	if err != nil {
		return microerror.Mask(err)
	}
	serviceAccountIssuerContent, err := templates.RenderTemplate(templates.AddExtraServiceAccountIssuersScript, params)
	if err != nil {
		return microerror.Mask(err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      customFilesSecretName(s.clusterInfo.Name),
			Namespace: s.clusterInfo.Namespace,
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			apiHealthzVintagePodKey:       apiHealthzVintagePodContent,
			joinEtcdClusterScriptKey:      joinEtcdClusterContent,
			moveEtcdLeaderScriptKey:       moveEtcdLeaderContent,
			addExtraServiceAccountIssuers: serviceAccountIssuerContent,
		},
	}
	err = s.clusterInfo.MC.CapiKubernetesClient.Create(ctx, secret)
	if apierrors.IsAlreadyExists(err) {
		// It's fine. No worries.
	} else if err != nil {
		return microerror.Mask(err)
	}

	return nil
}

func encryptionConfigSecretName(clusterName string) string {
	return fmt.Sprintf("%s-encryption-provider-config", clusterName)
}

func caCertsSecretName(clusterName string) string {
	return fmt.Sprintf("%s-ca", clusterName)
}

func saCertsSecretNameVintage(clusterName string) string {
	return fmt.Sprintf("%s-service-account-v2", clusterName)
}

func saCertsSecretNameCAPI(clusterName string) string {
	return fmt.Sprintf("%s-sa", clusterName)
}

func etcdCertsSecretName(clusterName string) string {
	return fmt.Sprintf("%s-etcd", clusterName)
}

func customFilesSecretName(clusterName string) string {
	return fmt.Sprintf("%s-migration-custom-files", clusterName)
}
func etcdEndpointFromDomain(domain string, clusterName string) string {
	return fmt.Sprintf("etcd.%s.k8s.%s", clusterName, domain)
}
func apiEndpointFromDomain(domain string, clusterName string) string {
	return fmt.Sprintf("api.%s.k8s.%s", clusterName, domain)
}
