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
	"strings"

	"github.com/sirupsen/logrus"
	"k8s.io/kubernetes/test/e2e/framework/metrics"
	"k8s.io/perf-tests/clusterloader2/pkg/measurement"
	"k8s.io/perf-tests/clusterloader2/pkg/util"
)

const (
	metricsForE2EName = "MetricsForE2E"
)

var interestingApiServerMetricsLabels = []string{
	"apiserver_init_events_total",
	"apiserver_request_count",
	"apiserver_request_latencies_summary",
	"etcd_request_latencies_summary",
}

var interestingControllerManagerMetricsLabels = []string{
	"garbage_collector_attempt_to_delete_queue_latency",
	"garbage_collector_attempt_to_delete_work_duration",
	"garbage_collector_attempt_to_orphan_queue_latency",
	"garbage_collector_attempt_to_orphan_work_duration",
	"garbage_collector_dirty_processing_latency_microseconds",
	"garbage_collector_event_processing_latency_microseconds",
	"garbage_collector_graph_changes_queue_latency",
	"garbage_collector_graph_changes_work_duration",
	"garbage_collector_orphan_processing_latency_microseconds",

	"namespace_queue_latency",
	"namespace_queue_latency_sum",
	"namespace_queue_latency_count",
	"namespace_retries",
	"namespace_work_duration",
	"namespace_work_duration_sum",
	"namespace_work_duration_count",
}

var interestingKubeletMetricsLabels = []string{
	"kubelet_container_manager_latency_microseconds",
	"kubelet_docker_errors",
	"kubelet_docker_operations_latency_microseconds",
	"kubelet_generate_pod_status_latency_microseconds",
	"kubelet_pod_start_latency_microseconds",
	"kubelet_pod_worker_latency_microseconds",
	"kubelet_pod_worker_start_latency_microseconds",
	"kubelet_sync_pods_latency_microseconds",
}

func init() {
	if err := measurement.Register(metricsForE2EName, createmetricsForE2EMeasurement); err != nil {
		logrus.Fatalf("Cannot register %s: %v", metricsForE2EName, err)
	}
}

func createmetricsForE2EMeasurement() measurement.Measurement {
	return &metricsForE2EMeasurement{}
}

type metricsForE2EMeasurement struct{}

// Execute gathers and prints e2e metrics data.
func (m *metricsForE2EMeasurement) Execute(config *measurement.MeasurementConfig) ([]measurement.Summary, error) {
	provider, err := util.GetStringOrDefault(config.Params, "provider", config.ClusterFramework.GetClusterConfig().Provider)
	if err != nil {
		return nil, err
	}

	grabMetricsFromKubelets, err := util.GetBoolOrDefault(config.Params, "gatherKubeletsMetrics", false)
	if err != nil {
		return nil, err
	}
	grabMetricsFromKubelets = grabMetricsFromKubelets && strings.ToLower(provider) != "kubemark"

	grabber, err := metrics.NewMetricsGrabber(
		config.ClusterFramework.GetClientSets().GetClient(),
		nil, /*external client*/
		grabMetricsFromKubelets,
		true, /*grab metrics from scheduler*/
		true, /*grab metrics from controller manager*/
		true, /*grab metrics from apiserver*/
		false /*grab metrics from cluster autoscaler*/)
	if err != nil {
		return nil, fmt.Errorf("failed to create MetricsGrabber: %v", err)
	}
	// Grab apiserver, scheduler, controller-manager metrics and (optionally) nodes' kubelet metrics.
	received, err := grabber.Grab()
	if err != nil {
		logrus.Errorf("%s: metricsGrabber failed to grab some of the metrics: %v", m, err)
	}
	filterMetrics(&received)
	content, jsonErr := util.PrettyPrintJSON(received)
	if jsonErr != nil {
		return nil, jsonErr
	}
	summary := measurement.CreateSummary(metricsForE2EName, "json", content)
	return []measurement.Summary{summary}, err
}

// Dispose cleans up after the measurement.
func (m *metricsForE2EMeasurement) Dispose() {}

// String returns string representation of this measurement.
func (*metricsForE2EMeasurement) String() string {
	return metricsForE2EName
}

func filterMetrics(m *metrics.MetricsCollection) {
	interestingApiServerMetrics := make(metrics.ApiServerMetrics)
	for _, metric := range interestingApiServerMetricsLabels {
		interestingApiServerMetrics[metric] = (*m).ApiServerMetrics[metric]
	}
	interestingControllerManagerMetrics := make(metrics.ControllerManagerMetrics)
	for _, metric := range interestingControllerManagerMetricsLabels {
		interestingControllerManagerMetrics[metric] = (*m).ControllerManagerMetrics[metric]
	}
	interestingKubeletMetrics := make(map[string]metrics.KubeletMetrics)
	for kubelet, grabbed := range (*m).KubeletMetrics {
		interestingKubeletMetrics[kubelet] = make(metrics.KubeletMetrics)
		for _, metric := range interestingKubeletMetricsLabels {
			interestingKubeletMetrics[kubelet][metric] = grabbed[metric]
		}
	}
	(*m).ApiServerMetrics = interestingApiServerMetrics
	(*m).ControllerManagerMetrics = interestingControllerManagerMetrics
	(*m).KubeletMetrics = interestingKubeletMetrics
}
