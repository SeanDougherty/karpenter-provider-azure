/*
Portions Copyright (c) Microsoft Corporation.

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
package azure

import (
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	azfake "github.com/Azure/azure-sdk-for-go/sdk/azcore/fake"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	containerservice "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v9"
	"github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
	"github.com/Azure/karpenter-provider-azure/test/pkg/environment/common"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	kubescheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestImageFamilySelection(t *testing.T) {
	for _, tc := range []struct {
		family    string
		machine   bool
		wantError bool
	}{{"", false, false}, {"AzureContainerLinux", true, false}, {"AzureContainerLinux", false, true}, {"invalid", true, true}} {
		if err := validateTestImageFamily(tc.family, tc.machine); (err != nil) != tc.wantError {
			t.Errorf("family=%q machine=%t: %v", tc.family, tc.machine, err)
		}

	}
	env := &Environment{}
	if got := *env.DefaultAKSNodeClass().Spec.ImageFamily; got != "Ubuntu2204" {
		t.Fatalf("generic default changed: %s", got)
	}
	env.TestImageFamily = "AzureContainerLinux"
	if got := *env.DefaultAKSNodeClass().Spec.ImageFamily; got != env.TestImageFamily {
		t.Fatalf("ACL default not selected: %s", got)
	}
}

func TestACLNodeIdentity(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "acl-node",
		Labels: map[string]string{
			"kubernetes.azure.com/os-sku":             "AzureContainerLinux",
			"kubernetes.azure.com/node-image-version": "AKSAzureLinux-aclgen2TL-202609.23.0",
		},
		Annotations: map[string]string{"karpenter.azure.com/aks-machine-resource-id": "/agentPools/aksmanagedap/machines/test"},
	}}
	if err := validateACLNodeIdentity(node); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"kubernetes.azure.com/os-sku", "kubernetes.azure.com/node-image-version", "karpenter.azure.com/aks-machine-resource-id"} {
		bad := node.DeepCopy()
		delete(bad.Labels, key)
		delete(bad.Annotations, key)
		if err := validateACLNodeIdentity(bad); err == nil {
			t.Errorf("accepted missing identity %s", key)
		}
	}
}

func TestHealthyPodCountRequiresACLEvidence(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.It("enforces optional ACL identity through the healthy pod count helper", func() {
		scheme := kubescheme.Scheme
		if err := v1beta1.SchemeBuilder.AddToScheme(scheme); err != nil {
			t.Fatal(err)
		}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: "default", Labels: map[string]string{"app": "proof"}},
			Spec:       corev1.PodSpec{NodeName: "acl-node"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		}
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name: "acl-node",
			Labels: map[string]string{
				karpv1.NodePoolLabelKey:                   "pool",
				v1beta1.AKSLabelOSSKU:                     v1beta1.OSSKUAzureContainerLinux,
				"kubernetes.azure.com/node-image-version": "AKSAzureLinux-aclgen2TL-202609.23.0",
			},
			Annotations: map[string]string{v1beta1.AnnotationAKSMachineResourceID: "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.ContainerService/managedClusters/cluster/agentPools/aksmanagedap/machines/test"},
		}}
		pool := &karpv1.NodePool{ObjectMeta: metav1.ObjectMeta{Name: "pool"}}
		pool.Spec.Template.Spec.NodeClassRef = &karpv1.NodeClassReference{Group: v1beta1.Group, Kind: v1beta1.AKSNodeClassKind, Name: "class"}
		nodeClass := &v1beta1.AKSNodeClass{ObjectMeta: metav1.ObjectMeta{Name: "class"}, Spec: v1beta1.AKSNodeClassSpec{ImageFamily: lo.ToPtr(v1beta1.AzureContainerLinuxImageFamily)}}
		node.Status.NodeInfo.KubeletVersion = "v1.36.4"
		nodeClass.Status.KubernetesVersion = lo.ToPtr("1.36.4")
		nodeClass.StatusConditions().SetTrue(v1beta1.ConditionTypeKubernetesVersionReady)
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, node, pool, nodeClass).Build()
		networkPods := []runtime.Object{}
		for _, app := range []string{"azure-cns", "cilium"} {
			networkPods = append(networkPods, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: app, Namespace: "kube-system", Labels: map[string]string{"k8s-app": app}},
				Spec:       corev1.PodSpec{NodeName: node.Name},
				Status:     corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
			})
		}
		machineCalls := 0
		machines, err := containerservice.NewMachinesClient("sub", &azfake.TokenCredential{}, &arm.ClientOptions{
			ClientOptions: policy.ClientOptions{Transport: machineTestTransport(func(req *http.Request) (*http.Response, error) {
				machineCalls++
				if !strings.HasSuffix(req.URL.Path, "/agentPools/aksmanagedap/machines/test") {
					t.Errorf("unexpected machine evidence path: %s", req.URL.Path)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
					Body: io.NopCloser(strings.NewReader(`{"properties":{"status":{"driftAction":"Synced"}}}`)), Request: req}, nil
			})},
		})
		if err != nil {
			t.Fatal(err)
		}
		env := &Environment{
			Environment: &common.Environment{
				Context:    t.Context(),
				Client:     client,
				Monitor:    common.NewMonitor(t.Context(), client),
				KubeClient: kubefake.NewClientset(networkPods...),
			},
			TestImageFamily:      v1beta1.AzureContainerLinuxImageFamily,
			ClusterResourceGroup: "rg",
			ClusterName:          "cluster",
			MachineAgentPoolName: "aksmanagedap",
			machinesClient:       machines,
		}
		selector := labels.SelectorFromSet(pod.Labels)
		if pods := env.EventuallyExpectHealthyPodCount(selector, 1); len(pods) != 1 {
			t.Fatal("healthy pod not returned")
		}
		if machineCalls != 1 {
			t.Fatal("machine drift state was not captured")
		}
		node.Labels[v1beta1.AKSLabelOSSKU] = "Ubuntu"
		if err := client.Update(t.Context(), node); err != nil {
			t.Fatal(err)
		}
		failures := gomega.InterceptGomegaFailures(func() { env.EventuallyExpectHealthyPodCount(selector, 1) })
		if len(failures) == 0 {
			t.Fatal("healthy pod helper bypassed ACL identity validation")
		}
		env.TestImageFamily = ""
		if failures := gomega.InterceptGomegaFailures(func() { env.EventuallyExpectHealthyPodCount(selector, 1) }); len(failures) != 0 {
			t.Fatalf("generic pod helper changed: %v", failures)
		}
	})
	ginkgo.RunSpecs(t, "ACL Lifecycle Evidence")
}

type machineTestTransport func(*http.Request) (*http.Response, error)

func (t machineTestTransport) Do(req *http.Request) (*http.Response, error) {
	return t(req)
}

func TestACLNodeVersion(t *testing.T) {
	for _, tc := range []struct {
		actual, expected string
		wantError        bool
	}{
		{"v1.36.4", "1.36.4", false},
		{"v1.37.0-rc.0", "1.37.0", true},
		{"v1.36.3", "1.36.4", true},
		{"", "1.36.4", true},
		{"v1.36.4", "", true},
	} {
		node := &corev1.Node{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: tc.actual}}}
		if err := validateACLNodeVersion(node, tc.expected); (err != nil) != tc.wantError {
			t.Errorf("version %q expected %q: %v", tc.actual, tc.expected, err)
		}
	}
}

func TestAKSTestTransport(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/subscriptions/test/machines?api-version=test" || r.Header.Get("Authorization") != "" {
			t.Errorf("incorrect proxy request: %s authorizationPresent=%t", r.URL, r.Header.Get("Authorization") != "")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	transport, err := newAKSTestTransport(server.URL, caFile)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://management.azure.com/subscriptions/test/machines?api-version=test", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-value")
	response, err := transport.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusNoContent || req.URL.Host != "management.azure.com" || req.Header.Get("Authorization") == "" {
		t.Fatal("unexpected response or original request mutated")
	}
	for _, endpoint := range []string{"http://127.0.0.1:443", "https://management.azure.com", server.URL + "/path", server.URL + "?q=1"} {
		if _, err := newAKSTestTransport(endpoint, caFile); err == nil {
			t.Errorf("accepted unsafe endpoint %q", endpoint)
		}
	}
	if _, err := newAKSTestTransport(server.URL, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("accepted missing CA")
	}
	if transport, err := newAKSTestTransport("", ""); err != nil || transport != nil {
		t.Fatal("generic clients should retain default transport")
	}
}
