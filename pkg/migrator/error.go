package migrator

import (
	"github.com/giantswarm/microerror"
)

var executionFailedError = &microerror.Error{
	Kind: "executionFailedError",
}

// IsExecutionFailed asserts executionFailedError.
func IsExecutionFailed(err error) bool {
	return microerror.Cause(err) == executionFailedError
}

var invalidCredentialSecretError = &microerror.Error{
	Kind: "invalidCredentialSecretError",
}

// IsInvalidCredentialSecretError asserts invalidCredentialSecretError.
func IsInvalidCredentialSecretError(err error) bool {
	return microerror.Cause(err) == invalidCredentialSecretError
}
