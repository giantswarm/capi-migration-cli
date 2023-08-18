package migration

import (
	"github.com/Azure/go-autorest/autorest"
	"github.com/giantswarm/microerror"
)

var identityRefNotSetError = &microerror.Error{
	Kind: "identityRefNotSetError",
}

var missingValueError = &microerror.Error{
	Kind: "missingValueError",
}

var newMasterNotReadyError = &microerror.Error{
	Kind: "newMasterNotReadyError",
}

var newWorkersNotReady = &microerror.Error{
	Kind: "newWorkersNotReady",
}

var subscriptionIDNotSetError = &microerror.Error{
	Kind: "subscriptionIDNotSetError",
}

var tooManyMastersError = &microerror.Error{
	Kind: "tooManyMastersError",
}

// IsAzureNotFound detects an azure API 404 error.
func IsAzureNotFound(err error) bool {
	if err == nil {
		return false
	}

	c := microerror.Cause(err)

	{
		dErr, ok := c.(autorest.DetailedError)
		if ok {
			if dErr.StatusCode == 404 {
				return true
			}
		}
	}

	return false
}
