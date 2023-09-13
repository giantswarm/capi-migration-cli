package migrator

import (
	"fmt"

	"github.com/giantswarm/microerror"
)

// getCAData reads vault PKI endpoint and fetches CA private key and CA certificate
func (s *Service) getCAData() ([]byte, []byte, error) {
	secret, err := s.clusterInfo.MC.VaultClient.Logical().Read(vaultPKIHackyEndpoint(s.clusterInfo.Name))
	if err != nil {
		return nil, nil, microerror.Mask(err)
	}

	if secret.Data == nil {
		fmt.Printf("ERROR: failed to fetch CA data, is the hacked vault deployed?")
		return nil, nil, microerror.Maskf(&microerror.Error{Kind: ""}, "fetching CA data failed, secret DATA is nil")
	}
	keyData, ok := secret.Data["private_key"].(string)
	if !ok {
		return nil, nil, microerror.Maskf(&microerror.Error{Kind: ""}, "failed to convert vault private key data into []byte")
	}

	certData, ok2 := secret.Data["certificate"].(string)
	if !ok2 {
		return nil, nil, microerror.Maskf(&microerror.Error{Kind: ""}, "failed to convert vault certificate data into []byte")
	}

	return []byte(keyData), []byte(certData), nil
}

func vaultPKIHackyEndpoint(clusterName string) string {
	return fmt.Sprintf("pki-%s/gimmeallyourlovin", clusterName)
}
