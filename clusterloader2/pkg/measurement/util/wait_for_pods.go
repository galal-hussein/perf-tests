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

package util

import (
	"fmt"
	"strings"
	"time"

	clientset "k8s.io/client-go/kubernetes"
	"github.com/sirupsen/logrus"
)

const (
	uninitialized = iota
	up
	down
	none
)

// WaitForPodOptions is an options used by WaitForPods methods.
type WaitForPodOptions struct {
	Selector            *ObjectSelector
	DesiredPodCount     int
	EnableLogging       bool
	CallerName          string
	WaitForPodsInterval time.Duration
}

// WaitForPods waits till disire nuber of pods is running.
// Pods are be specified by namespace, field and/or label selectors.
// If stopCh is closed before all pods are running, the error will be returned.
func WaitForPods(clientSet clientset.Interface, stopCh <-chan struct{}, options *WaitForPodOptions) error {
	ps, err := NewPodStore(clientSet, options.Selector)
	if err != nil {
		return fmt.Errorf("pod store creation error: %v", err)
	}
	defer ps.Stop()

	oldPods := ps.List()
	scaling := uninitialized
	var podsStatus PodsStartupStatus

	switch {
	case len(oldPods) == options.DesiredPodCount:
		scaling = none
	case len(oldPods) < options.DesiredPodCount:
		scaling = up
	case len(oldPods) > options.DesiredPodCount:
		scaling = down
	}

	for {
		select {
		case <-stopCh:
			logrus.Infof("%s: %s: pods status: %v", options.CallerName, options.Selector.String(), ComputePodsStatus(oldPods, options.DesiredPodCount))
			return fmt.Errorf("timeout while waiting for %d pods to be running in namespace '%v' with labels '%v' and fields '%v' - only %d found running",
				options.DesiredPodCount, options.Selector.Namespace, options.Selector.LabelSelector, options.Selector.FieldSelector, podsStatus.Running)
		case <-time.After(options.WaitForPodsInterval):
			pods := ps.List()
			podsStatus = ComputePodsStartupStatus(pods, options.DesiredPodCount)

			diff := DiffPods(oldPods, pods)
			deletedPods := diff.DeletedPods()
			if scaling != down && len(deletedPods) > 0 {
				logrus.Errorf("%s: %s: %d pods disappeared: %v", options.CallerName, options.Selector.String(), len(deletedPods), strings.Join(deletedPods, ", "))
			}
			addedPods := diff.AddedPods()
			if scaling != up && len(addedPods) > 0 {
				logrus.Errorf("%s: %s: %d pods appeared: %v", options.CallerName, options.Selector.String(), len(deletedPods), strings.Join(deletedPods, ", "))
			}
			if options.EnableLogging {
				logrus.Infof("%s: %s: %s", options.CallerName, options.Selector.String(), podsStatus.String())
			}
			// We allow inactive pods (e.g. eviction happened).
			// We wait until there is a desired number of pods running and all other pods are inactive.
			if len(pods) == (podsStatus.Running+podsStatus.Inactive) && podsStatus.Running == options.DesiredPodCount {
				return nil
			}
			oldPods = pods
		}
	}
}
