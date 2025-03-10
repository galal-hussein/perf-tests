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

package framework

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"github.com/sirupsen/logrus"
	"k8s.io/perf-tests/clusterloader2/pkg/config"
	"k8s.io/perf-tests/clusterloader2/pkg/errors"
	"k8s.io/perf-tests/clusterloader2/pkg/framework/client"

	// ensure auth plugins are loaded
	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

// Framework allows for interacting with Kubernetes cluster via
// official Kubernetes client.
type Framework struct {
	automanagedNamespacePrefix string
	automanagedNamespaceCount  int
	clientSets                 *MultiClientSet
	dynamicClients             *MultiDynamicClient
	clusterConfig              *config.ClusterConfig
}

// NewFramework creates new framework based on given clusterConfig.
func NewFramework(clusterConfig *config.ClusterConfig, clientsNumber int) (*Framework, error) {
	return newFramework(clusterConfig, clientsNumber, clusterConfig.KubeConfigPath)
}

// NewRootFramework creates framework for the root cluster.
// For clusters other than kubemark there is no difference between NewRootFramework and NewFramework.
func NewRootFramework(clusterConfig *config.ClusterConfig, clientsNumber int) (*Framework, error) {
	kubeConfigPath := clusterConfig.KubeConfigPath
	if clusterConfig.Provider == "kubemark" {
		kubeConfigPath = clusterConfig.KubemarkRootKubeConfigPath
	}
	return newFramework(clusterConfig, clientsNumber, kubeConfigPath)
}

func newFramework(clusterConfig *config.ClusterConfig, clientsNumber int, kubeConfigPath string) (*Framework, error) {
	var err error
	f := Framework{
		automanagedNamespaceCount: 0,
		clusterConfig:             clusterConfig,
	}
	if f.clientSets, err = NewMultiClientSet(kubeConfigPath, clientsNumber); err != nil {
		return nil, fmt.Errorf("multi client set creation error: %v", err)
	}
	if f.dynamicClients, err = NewMultiDynamicClient(kubeConfigPath, clientsNumber); err != nil {
		return nil, fmt.Errorf("multi dynamic client creation error: %v", err)
	}
	return &f, nil
}

// GetAutomanagedNamespacePrefix returns automanaged namespace prefix.
func (f *Framework) GetAutomanagedNamespacePrefix() string {
	return f.automanagedNamespacePrefix
}

// SetAutomanagedNamespacePrefix sets automanaged namespace prefix.
func (f *Framework) SetAutomanagedNamespacePrefix(nsName string) {
	f.automanagedNamespacePrefix = nsName
}

// GetClientSets returns clientSet clients.
func (f *Framework) GetClientSets() *MultiClientSet {
	return f.clientSets
}

// GetDynamicClients returns dynamic clients.
func (f *Framework) GetDynamicClients() *MultiDynamicClient {
	return f.dynamicClients
}

// GetClusterConfig returns cluster config.
func (f *Framework) GetClusterConfig() *config.ClusterConfig {
	return f.clusterConfig
}

// CreateAutomanagedNamespaces creates automanged namespaces.
func (f *Framework) CreateAutomanagedNamespaces(namespaceCount int) error {
	if f.automanagedNamespaceCount != 0 {
		return fmt.Errorf("automanaged namespaces already created")
	}
	for i := 1; i <= namespaceCount; i++ {
		name := fmt.Sprintf("%v-%d", f.automanagedNamespacePrefix, i)
		if err := client.CreateNamespace(f.clientSets.GetClient(), name); err != nil {
			return err
		}
		f.automanagedNamespaceCount++
	}
	return nil
}

// ListAutomanagedNamespaces returns all existing automanged namespace names.
func (f *Framework) ListAutomanagedNamespaces() ([]string, error) {
	var automanagedNamespacesList []string
	namespacesList, err := client.ListNamespaces(f.clientSets.GetClient())
	if err != nil {
		return automanagedNamespacesList, err
	}
	for _, namespace := range namespacesList {
		matched, err := f.isAutomanagedNamespace(namespace.Name)
		if err != nil {
			return automanagedNamespacesList, err
		}
		if matched {
			automanagedNamespacesList = append(automanagedNamespacesList, namespace.Name)
		}
	}
	return automanagedNamespacesList, nil
}

// DeleteAutomanagedNamespaces deletes all automanged namespaces.
func (f *Framework) DeleteAutomanagedNamespaces() *errors.ErrorList {
	var wg wait.Group
	errList := errors.NewErrorList()
	for i := 1; i <= f.automanagedNamespaceCount; i++ {
		clientSet := f.clientSets.GetClient()
		name := fmt.Sprintf("%v-%d", f.automanagedNamespacePrefix, i)
		wg.Start(func() {
			if err := client.DeleteNamespace(clientSet, name); err != nil {
				errList.Append(err)
				return
			}
			if err := client.WaitForDeleteNamespace(clientSet, name); err != nil {
				errList.Append(err)
			}
		})
	}
	wg.Wait()
	f.automanagedNamespaceCount = 0
	return errList
}

// CreateObject creates object base on given object description.
func (f *Framework) CreateObject(namespace string, name string, obj *unstructured.Unstructured, options ...*client.ApiCallOptions) error {
	return client.CreateObject(f.dynamicClients.GetClient(), namespace, name, obj, options...)
}

// PatchObject updates object (using patch) with given name using given object description.
func (f *Framework) PatchObject(namespace string, name string, obj *unstructured.Unstructured, options ...*client.ApiCallOptions) error {
	return client.PatchObject(f.dynamicClients.GetClient(), namespace, name, obj)
}

// DeleteObject deletes object with given name and group-version-kind.
func (f *Framework) DeleteObject(gvk schema.GroupVersionKind, namespace string, name string, options ...*client.ApiCallOptions) error {
	return client.DeleteObject(f.dynamicClients.GetClient(), gvk, namespace, name)
}

// GetObject retrieves object with given name and group-version-kind.
func (f *Framework) GetObject(gvk schema.GroupVersionKind, namespace string, name string, options ...*client.ApiCallOptions) (*unstructured.Unstructured, error) {
	return client.GetObject(f.dynamicClients.GetClient(), gvk, namespace, name)
}

// ApplyTemplatedManifests finds and applies all manifest template files matching the provided
// manifestGlob pattern. It substitutes the template placeholders using the templateMapping map.
func (f *Framework) ApplyTemplatedManifests(manifestGlob string, templateMapping map[string]interface{}, options ...*client.ApiCallOptions) error {
	// TODO(mm4tt): Consider using the out-of-the-box "kubectl create -f".
	manifestGlob = os.ExpandEnv(manifestGlob)
	templateProvider := config.NewTemplateProvider(filepath.Dir(manifestGlob))
	manifests, err := filepath.Glob(manifestGlob)
	if err != nil {
		return err
	}
	for _, manifest := range manifests {
		logrus.Infof("Applying %s\n", manifest)
		obj, err := templateProvider.TemplateToObject(filepath.Base(manifest), templateMapping)
		if err != nil {
			if err == config.ErrorEmptyFile {
				logrus.Warningf("Skipping empty manifest %s", manifest)
				continue
			}
			return err
		}
		objList := []unstructured.Unstructured{*obj}
		if obj.IsList() {
			list, err := obj.ToList()
			if err != nil {
				return err
			}
			objList = list.Items
		}
		for _, item := range objList {
			if err := f.CreateObject(item.GetNamespace(), item.GetName(), &item, options...); err != nil {
				return fmt.Errorf("error while applying (%s): %v", manifest, err)
			}
		}

	}
	return nil
}

func (f *Framework) isAutomanagedNamespace(name string) (bool, error) {
	return regexp.MatchString(f.automanagedNamespacePrefix+"-[1-9][0-9]*", name)
}
