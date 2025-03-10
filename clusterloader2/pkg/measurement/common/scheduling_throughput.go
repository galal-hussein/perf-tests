/*
Copyright 2018 The Kubernetes Authors.

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

package common

import (
	"fmt"
	"math"
	"sort"
	"time"

	clientset "k8s.io/client-go/kubernetes"
	"github.com/sirupsen/logrus"
	"k8s.io/perf-tests/clusterloader2/pkg/measurement"
	measurementutil "k8s.io/perf-tests/clusterloader2/pkg/measurement/util"
	"k8s.io/perf-tests/clusterloader2/pkg/util"
)

const (
	schedulingThroughputMeasurementName = "SchedulingThroughput"
)

func init() {
	if err := measurement.Register(schedulingThroughputMeasurementName, createSchedulingThroughputMeasurement); err != nil {
		logrus.Fatalf("Cannot register %s: %v", schedulingThroughputMeasurementName, err)
	}
}

func createSchedulingThroughputMeasurement() measurement.Measurement {
	return &schedulingThroughputMeasurement{}
}

type schedulingThroughputMeasurement struct {
	schedulingThroughputs []float64
	isRunning             bool
	stopCh                chan struct{}
}

// Execute supports two actions:
// - start - starts the pods scheduling observation.
//   Pods can be specified by field and/or label selectors.
//   If namespace is not passed by parameter, all-namespace scope is assumed.
// - gather - creates summary for observed values.
func (s *schedulingThroughputMeasurement) Execute(config *measurement.MeasurementConfig) ([]measurement.Summary, error) {
	action, err := util.GetString(config.Params, "action")
	if err != nil {
		return nil, err
	}
	switch action {
	case "start":
		if s.isRunning {
			logrus.Infof("%s: measurement already running", s)
			return nil, nil
		}
		selector := measurementutil.NewObjectSelector()
		if err := selector.Parse(config.Params); err != nil {
			return nil, err
		}

		s.stopCh = make(chan struct{})
		return nil, s.start(config.ClusterFramework.GetClientSets().GetClient(), selector)
	case "gather":
		return s.gather()
	default:
		return nil, fmt.Errorf("unknown action %v", action)
	}
}

// Dispose cleans up after the measurement.
func (s *schedulingThroughputMeasurement) Dispose() {
	s.stop()
}

// String returns a string representation of the measurement.
func (*schedulingThroughputMeasurement) String() string {
	return schedulingThroughputMeasurementName
}

func (s *schedulingThroughputMeasurement) start(clientSet clientset.Interface, selector *measurementutil.ObjectSelector) error {
	ps, err := measurementutil.NewPodStore(clientSet, selector)
	if err != nil {
		return fmt.Errorf("pod store creation error: %v", err)
	}
	s.isRunning = true
	logrus.Infof("%s: starting collecting throughput data", s)

	go func() {
		defer ps.Stop()
		lastScheduledCount := 0
		for {
			select {
			case <-s.stopCh:
				return
			case <-time.After(defaultWaitForPodsInterval):
				pods := ps.List()
				podsStatus := measurementutil.ComputePodsStartupStatus(pods, 0)
				throughput := float64(podsStatus.Scheduled-lastScheduledCount) / float64(defaultWaitForPodsInterval/time.Second)
				s.schedulingThroughputs = append(s.schedulingThroughputs, throughput)
				lastScheduledCount = podsStatus.Scheduled
				logrus.Infof("%v: %s: %d pods scheduled", s, selector.String(), lastScheduledCount)
			}
		}
	}()
	return nil
}

func (s *schedulingThroughputMeasurement) gather() ([]measurement.Summary, error) {
	if !s.isRunning {
		logrus.Errorf("%s: measurementis nor running", s)
		return nil, fmt.Errorf("measurement is not running")
	}
	s.stop()
	logrus.Infof("%s: gathering data", s)

	throughputSummary := &schedulingThroughput{}
	if length := len(s.schedulingThroughputs); length > 0 {
		sort.Float64s(s.schedulingThroughputs)
		sum := 0.0
		for i := range s.schedulingThroughputs {
			sum += s.schedulingThroughputs[i]
		}
		throughputSummary.Average = sum / float64(length)
		throughputSummary.Perc50 = s.schedulingThroughputs[int(math.Ceil(float64(length*50)/100))-1]
		throughputSummary.Perc90 = s.schedulingThroughputs[int(math.Ceil(float64(length*90)/100))-1]
		throughputSummary.Perc99 = s.schedulingThroughputs[int(math.Ceil(float64(length*99)/100))-1]
	}
	content, err := util.PrettyPrintJSON(throughputSummary)
	if err != nil {
		return nil, err
	}
	summary := measurement.CreateSummary(schedulingThroughputMeasurementName, "json", content)
	return []measurement.Summary{summary}, nil
}

func (s *schedulingThroughputMeasurement) stop() {
	if s.isRunning {
		close(s.stopCh)
		s.isRunning = false
	}
}

type schedulingThroughput struct {
	Average float64 `json:"average"`
	Perc50  float64 `json:"perc50"`
	Perc90  float64 `json:"perc90"`
	Perc99  float64 `json:"perc99"`
}
