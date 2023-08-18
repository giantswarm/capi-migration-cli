package migration

import (
	"context"
	"fmt"
)

func NewAWSMigratorFactory(cfg AWSMigrationConfig) (MigratorFactory, error) {
	return &awsMigratorFactory{
		config: cfg,
	}, nil
}

func (m *awsMigrator) IsMigrated(ctx context.Context) (bool, error) {
	return false, nil
}

func (m *awsMigrator) IsMigrating(ctx context.Context) (bool, error) {
	return false, nil
}

func (m *awsMigrator) Prepare(ctx context.Context) error {
	var err error

	err = m.migrateCertsSecrets(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	err = m.readCRs(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	err = m.createAWSApiClients(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	err = m.prepareMissingCRs(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	err = m.updateCRs(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	// need to figure out how to make run for both providers
	// err = m.stopOldMasterComponents(ctx)
	// if err != nil {
	//	return microerror.Mask(err)
	// }

	return nil
}

func (m *awsMigrator) TriggerMigration(ctx context.Context) error {
	err := m.triggerMigration(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	return nil
}

func (m *awsMigrator) Cleanup(ctx context.Context) error {
	migrated, err := m.IsMigrated(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	if !migrated {
		return fmt.Errorf("cluster has not migrated yet")
	}

	return nil
}

// readCRs reads existing CRs involved in migration. For AWS this contains
// roughly following CRs:
// - Cluster
// - AWSCluster
// - MachinePools
// - AWSMachineDeployments
func (m *awsMigrator) readCRs(ctx context.Context) error {
	var err error

	err = m.readEncryptionSecret(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	err = m.readCluster(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	err = m.readAWSCluster(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	err = m.readAWSControlPlane(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	err = m.readG8sControlPlane(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	err = m.readAWSMachineDeployments(ctx)
	if err != nil {
		return microerror.Mask(err)
	}

	releaseVer := m.crs.cluster.GetLabels()[label.ReleaseVersion]
	err = m.readRelease(ctx, releaseVer)
	if err != nil {
		return microerror.Mask(err)
	}

	return nil
}
