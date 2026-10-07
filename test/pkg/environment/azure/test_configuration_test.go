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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
