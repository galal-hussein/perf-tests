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
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"k8s.io/perf-tests/clusterloader2/pkg/errors"
	"k8s.io/perf-tests/clusterloader2/pkg/measurement"
	measurementutil "k8s.io/perf-tests/clusterloader2/pkg/measurement/util"
	"k8s.io/perf-tests/clusterloader2/pkg/measurement/util/gatherers"
	"k8s.io/perf-tests/clusterloader2/pkg/util"
)

const (
	resourceUsageMetricName = "ResourceUsageSummary"
)

func init() {
	if err := measurement.Register(resourceUsageMetricName, createResourceUsageMetricMeasurement); err != nil {
		logrus.Fatalf("Cannot register %s: %v", resourceUsageMetricName, err)
	}
}

func createResourceUsageMetricMeasurement() measurement.Measurement {
	return &resourceUsageMetricMeasurement{
		resourceConstraints: make(map[string]*measurementutil.ResourceConstraint),
	}
}

type resourceUsageMetricMeasurement struct {
	gatherer            *gatherers.ContainerResourceGatherer
	resourceConstraints map[string]*measurementutil.ResourceConstraint
}

// Execute supports two actions:
// - start - Starts resource metrics collecting.
// - gather - Gathers and prints current resource usage metrics.
func (e *resourceUsageMetricMeasurement) Execute(config *measurement.MeasurementConfig) ([]measurement.Summary, error) {
	action, err := util.GetString(config.Params, "action")
	if err != nil {
		return nil, err
	}

	switch action {
	case "start":
		provider, err := util.GetStringOrDefault(config.Params, "provider", config.ClusterFramework.GetClusterConfig().Provider)
		if err != nil {
			return nil, err
		}
		host, err := util.GetStringOrDefault(config.Params, "host", config.ClusterFramework.GetClusterConfig().GetMasterIp())
		if err != nil {
			return nil, err
		}
		nodeMode, err := util.GetStringOrDefault(config.Params, "nodeMode", "")
		if err != nil {
			return nil, err
		}
		constraintsPath, err := util.GetStringOrDefault(config.Params, "resourceConstraints", "")
		if err != nil {
			return nil, err
		}
		if constraintsPath != "" {
			mapping := make(map[string]interface{})
			mapping["Nodes"] = config.ClusterFramework.GetClusterConfig().Nodes
			if err = config.TemplateProvider.TemplateInto(constraintsPath, mapping, &e.resourceConstraints); err != nil {
				return nil, fmt.Errorf("resource constraints reading error: %v", err)
			}
			for _, constraint := range e.resourceConstraints {
				if constraint.CPUConstraint == 0 {
					constraint.CPUConstraint = math.MaxFloat64
				}
				if constraint.MemoryConstraint == 0 {
					constraint.MemoryConstraint = math.MaxUint64
				}
			}
		}
		var nodesSet gatherers.NodesSet
		switch nodeMode {
		case "master":
			nodesSet = gatherers.MasterNodes
		case "masteranddns":
			nodesSet = gatherers.MasterAndDNSNodes
		default:
			nodesSet = gatherers.AllNodes
		}

		logrus.Infof("%s: starting resource usage collecting...", e)
		e.gatherer, err = gatherers.NewResourceUsageGatherer(config.ClusterFramework.GetClientSets().GetClient(), host, provider, gatherers.ResourceGathererOptions{
			InKubemark:                        strings.ToLower(provider) == "kubemark",
			Nodes:                             nodesSet,
			ResourceDataGatheringPeriod:       60 * time.Second,
			MasterResourceDataGatheringPeriod: 10 * time.Second,
			PrintVerboseLogs:                  false,
		}, nil)
		if err != nil {
			return nil, err
		}
		go e.gatherer.StartGatheringData()
		return nil, nil
	case "gather":
		if e.gatherer == nil {
			logrus.Errorf("%s: gatherer not initialized", e)
			return nil, nil
		}
		logrus.Infof("%s: gathering resource usage...", e)
		summary, err := e.gatherer.StopAndSummarize([]int{50, 90, 99, 100})
		if err != nil {
			return nil, err
		}
		content, err := util.PrettyPrintJSON(summary)
		if err != nil {
			return nil, err
		}
		resourceSummary := measurement.CreateSummary(resourceUsageMetricName, "json", content)
		return []measurement.Summary{resourceSummary}, e.verifySummary(summary)

	default:
		return nil, fmt.Errorf("unknown action %v", action)
	}
}

// Dispose cleans up after the measurement.
func (e *resourceUsageMetricMeasurement) Dispose() {
	if e.gatherer != nil {
		e.gatherer.Dispose()
	}
}

// String returns string representation of this measurement.
func (*resourceUsageMetricMeasurement) String() string {
	return resourceUsageMetricName
}

func (e *resourceUsageMetricMeasurement) verifySummary(summary *gatherers.ResourceUsageSummary) error {
	violatedConstraints := make([]string, 0)
	for _, containerSummary := range summary.Get("99") {
		containerName := strings.Split(containerSummary.Name, "/")[1]
		if constraint, ok := e.resourceConstraints[containerName]; ok {
			if containerSummary.Cpu > constraint.CPUConstraint {
				violatedConstraints = append(
					violatedConstraints,
					fmt.Sprintf("container %v is using %v/%v CPU",
						containerSummary.Name,
						containerSummary.Cpu,
						constraint.CPUConstraint,
					),
				)
			}
			if containerSummary.Mem > constraint.MemoryConstraint {
				violatedConstraints = append(
					violatedConstraints,
					fmt.Sprintf("container %v is using %v/%v MB of memory",
						containerSummary.Name,
						float64(containerSummary.Mem)/(1024*1024),
						float64(constraint.MemoryConstraint)/(1024*1024),
					),
				)
			}
		}
	}
	if len(violatedConstraints) > 0 {
		for i := range violatedConstraints {
			logrus.Errorf("%s: violation: %s", e, violatedConstraints[i])
		}
		return errors.NewMetricViolationError("resource constraints", fmt.Sprintf("%d constraints violated: %v", len(violatedConstraints), violatedConstraints))
	}
	return nil
}
