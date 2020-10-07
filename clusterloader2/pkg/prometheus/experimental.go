/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package prometheus

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"github.com/sirupsen/logrus"
)

type prometheusDiskMetadata struct {
	name string
	zone string
}

const (
	gcloudRetryInterval  = 20 * time.Second
	snapshotRetryTimeout = 10 * time.Minute
	deleteRetryTimeout   = 2 * time.Minute
)

var (
	shouldSnapshotPrometheusDisk = pflag.Bool("experimental-gcp-snapshot-prometheus-disk", false, "(experimental, provider=gce|gke only) whether to snapshot Prometheus disk before Prometheus stack is torn down")
	prometheusDiskSnapshotName   = pflag.String("experimental-prometheus-disk-snapshot-name", "", "Name of the prometheus disk snapshot that will be created if snapshots are enabled. If not set, the prometheus disk name will be used.")
)

func (pc *PrometheusController) isEnabled() (bool, error) {
	if !*shouldSnapshotPrometheusDisk {
		return false, nil
	}
	if pc.provider != "gce" && pc.provider != "gke" && pc.provider != "kubemark" {
		return false, fmt.Errorf(
			"snapshotting Prometheus' disk only available for GCP providers (gce, gke, kubemark), provider is: %s", pc.provider)
	}
	return true, nil
}

func (pc *PrometheusController) cachePrometheusDiskMetadataIfEnabled() error {
	if enabled, err := pc.isEnabled(); !enabled {
		return err
	}
	return wait.Poll(
		10*time.Second,
		2*time.Minute,
		pc.tryRetrievePrometheusDiskMetadata)
}

func (pc *PrometheusController) tryRetrievePrometheusDiskMetadata() (bool, error) {
	logrus.Info("Retrieving Prometheus' persistent disk metadata...")
	k8sClient := pc.framework.GetClientSets().GetClient()
	list, err := k8sClient.CoreV1().PersistentVolumes().List(metav1.ListOptions{})
	if err != nil {
		logrus.Errorf("Listing PVs failed: %v", err)
		// Poll() stops on error so returning nil
		return false, nil
	}
	var pdName, zone string
	for _, pv := range list.Items {
		if pv.Spec.ClaimRef.Name != "prometheus-k8s-db-prometheus-k8s-0" {
			continue
		}
		if pv.Status.Phase != corev1.VolumeBound {
			continue
		}
		logrus.Infof("Found Prometheus' PV with name: %s", pv.Name)
		pdName = pv.Spec.GCEPersistentDisk.PDName
		zone = pv.ObjectMeta.Labels["failure-domain.beta.kubernetes.io/zone"]
		logrus.Infof("PD name=%s, zone=%s", pdName, zone)
	}
	if pdName == "" || zone == "" {
		logrus.Warningf("missing zone or PD name, aborting")
		logrus.Info("PV list was:")
		s, err := json.MarshalIndent(list, "" /*=prefix*/, "  " /*=indent*/)
		if err != nil {
			logrus.Warningf("Error while marshalling response %v: %v", list, err)
			return true, err
		}
		logrus.Info(string(s))
		return true, nil
	}
	pc.diskMetadata.name = pdName
	pc.diskMetadata.zone = zone
	return true, nil
}

func (pc *PrometheusController) snapshotPrometheusDiskIfEnabled() error {
	if enabled, err := pc.isEnabled(); !enabled {
		return err
	}
	if pc.diskMetadata.name == "" || pc.diskMetadata.zone == "" {
		logrus.Errorf("Missing zone or PD name, aborting snapshot")
		logrus.Infof("PD name=%s, zone=%s", pc.diskMetadata.name, pc.diskMetadata.zone)
		return fmt.Errorf("missing zone or PD name, aborting snapshot")
	}
	// Select snapshot name
	snapshotName := pc.diskMetadata.name
	if *prometheusDiskSnapshotName != "" {
		if err := VerifySnapshotName(*prometheusDiskSnapshotName); err == nil {
			snapshotName = *prometheusDiskSnapshotName
		} else {
			logrus.Warningf("Incorrect disk name %v: %v. Using default name: %v", *prometheusDiskSnapshotName, err, snapshotName)
		}
	}
	// Snapshot Prometheus disk
	return wait.Poll(
		gcloudRetryInterval,
		snapshotRetryTimeout,
		func() (bool, error) {
			err := pc.trySnapshotPrometheusDisk(pc.diskMetadata.name, snapshotName, pc.diskMetadata.zone)
			// Poll() stops on error so returning nil
			return err == nil, nil
		})
}

func (pc *PrometheusController) trySnapshotPrometheusDisk(pdName, snapshotName, zone string) error {
	logrus.Info("Trying to snapshot Prometheus' persistent disk...")
	project := pc.clusterLoaderConfig.PrometheusConfig.SnapshotProject
	if project == "" {
		// This should never happen when run from kubetest with a GCE/GKE Kubernetes
		// provider - kubetest always propagates PROJECT env var in such situations.
		return fmt.Errorf("unknown project - please set --experimental-snapshot-project flag")
	}
	logrus.Infof("Snapshotting PD %q into snapshot %q in project %q in zone %q", pdName, snapshotName, project, zone)
	cmd := exec.Command("gcloud", "compute", "disks", "snapshot", pdName, "--project", project, "--zone", zone, "--snapshot-names", snapshotName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logrus.Errorf("Creating disk snapshot failed: %v\nCommand output: %q", err, string(output))
	} else {
		logrus.Infof("Creating disk snapshot finished with: %q", string(output))
	}
	return err
}

func (pc *PrometheusController) deletePrometheusDiskIfEnabled() error {
	if enabled, err := pc.isEnabled(); !enabled {
		return err
	}
	if pc.diskMetadata.name == "" || pc.diskMetadata.zone == "" {
		logrus.Errorf("Missing zone or PD name, aborting deletion")
		logrus.Infof("PD name=%s, zone=%s", pc.diskMetadata.name, pc.diskMetadata.zone)
		return fmt.Errorf("missing zone or PD name, aborting deletion")
	}
	// Delete Prometheus disk
	return wait.Poll(
		gcloudRetryInterval,
		deleteRetryTimeout,
		func() (bool, error) {
			err := pc.tryDeletePrometheusDisk(pc.diskMetadata.name, pc.diskMetadata.zone)
			// Poll() stops on error so returning nil
			return err == nil, nil
		})
}

func (pc *PrometheusController) tryDeletePrometheusDisk(pdName, zone string) error {
	logrus.Info("Trying to delete Prometheus' persistent disk...")
	project := pc.clusterLoaderConfig.PrometheusConfig.SnapshotProject
	if project == "" {
		// This should never happen when run from kubetest with a GCE/GKE Kubernetes
		// provider - kubetest always propagates PROJECT env var in such situations.
		return fmt.Errorf("unknown project - please set --experimental-snapshot-project flag")
	}
	logrus.Infof("Deleting PD %q in project %q in zone %q", pdName, project, zone)
	cmd := exec.Command("gcloud", "compute", "disks", "delete", pdName, "--project", project, "--zone", zone)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logrus.Errorf("Deleting disk failed: %v\nCommand output: %q", err, string(output))
	} else {
		logrus.Infof("Deleting disk finished with: %q", string(output))
	}
	return err
}
