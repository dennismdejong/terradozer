package main

import (
	"fmt"

	"github.com/sirupsen/logrus"
)

type Resource struct {
	// TerraformType is the Terraform type of a resource
	TerraformType string
	// Provider must be able to delete a resource
	Provider *TerraformProvider
	// id is used by the provider to import and delete the resource
	id string
}

func (r Resource) Type() string {
	return r.TerraformType
}

func (r Resource) ID() string {
	return r.id
}

// Delete deletes a Terraform resource via the corresponding Terraform Provider
func (r Resource) Delete(dryRun bool) error {
	logrus.Debugf("resource instance (type=%s, id=%s)", r.TerraformType, r.id)

	if dryRun {
		logrus.Printf("would try to delete resource (type=%s, id=%s)\n", r.TerraformType, r.id)
		return nil
	}

	importResp := r.Provider.importResource(r.TerraformType, r.id)
	if importResp.Diagnostics.HasErrors() {
		return fmt.Errorf("failed to import resource: %s", importResp.Diagnostics.Err())
	}

	for _, resImp := range importResp.ImportedResources {
		logrus.Tracef("imported resource state: %s", resImp.State.GoString())

		readResp := r.Provider.readResource(resImp)
		if readResp.Diagnostics.HasErrors() {
			return fmt.Errorf("failed to read current state of resource: %s", readResp.Diagnostics.Err())
		}

		logrus.Tracef("read resource state: %s", readResp.NewState.GoString())

		resourceNotExists := readResp.NewState.IsNull()
		if resourceNotExists {
			return NotExistingError
		}

		respApply := r.Provider.destroy(r.TerraformType, readResp.NewState)
		if respApply.Diagnostics.HasErrors() {
			logrus.WithError(respApply.Diagnostics.Err()).Debugf(
				"failed to delete resource: %s", respApply.Diagnostics.Err())
			return RetryableError
		}

		logrus.Infof("deleted resource (type=%s, id=%s)", r.Type(), r.ID())

		logrus.Tracef("new resource state after apply: %s", respApply.NewState.GoString())
	}

	return nil
}

type DeletableResource interface {
	Delete(bool) error
	Type() string
	ID() string
}

// Delete retries to delete resources that depend on each other
//
// Per iteration (run), at least one resource must be successfully deleted to retry deleting in a next run
// (until all resources are deleted or some deletions have permanently failed).
func Delete(resources []DeletableResource, dryRun bool) int {
	numOfDeletedResources := 0
	var resourcesToRetry []DeletableResource

	for _, r := range resources {
		err := r.Delete(dryRun)
		if err != nil {
			if err == RetryableError {
				logrus.Infof("will retry deleting resource (type=%s, id=%s)", r.Type(), r.ID())
				logrus.WithError(err).Debugf("will retry deleting resource (type=%s, id=%s)", r.Type(), r.ID())

				resourcesToRetry = append(resourcesToRetry, r)
			} else if err == NotExistingError {
				logrus.Infof("resource found in state has already been deleted (type=%s, id=%s)", r.Type(), r.ID())
			} else {
				logrus.Infof("unable to delete resource (type=%s, id=%s)", r.Type(), r.ID())
				logrus.WithError(err).Debugf("unable to delete resource (type=%s, id=%s)", r.Type(), r.ID())
			}

			continue
		}

		numOfDeletedResources++
	}

	if len(resourcesToRetry) > 0 && numOfDeletedResources > 0 {
		logrus.Debugf("retrying to delete the following resources: %+v", resourcesToRetry)

		numOfDeletedResources += Delete(resourcesToRetry, dryRun)
	}

	return numOfDeletedResources
}