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
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
)

func validateTestImageFamily(family string, machineAPI bool) error {
	if family == "" {
		return nil
	}
	if family != v1beta1.AzureContainerLinuxImageFamily || !machineAPI {
		return fmt.Errorf("TEST_AKS_IMAGE_FAMILY=%q requires AzureContainerLinux and a Machine API PROVISION_MODE", family)
	}
	return nil
}

type aksTestTransport struct {
	endpoint *url.URL
	client   *http.Client
}

// Only ContainerService clients use this loopback bridge; compute and network
// clients keep talking to real ARM. No ingress credential enters the test process.
func newAKSTestTransport(endpoint, caFile string) (policy.Transporter, error) {
	if endpoint == "" && caFile == "" {
		return nil, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parsing TEST_AKS_PROXY_URL: %w", err)
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" || ip == nil || !ip.IsLoopback() || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, fmt.Errorf("TEST_AKS_PROXY_URL must be a loopback HTTPS origin")
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading TEST_AKS_PROXY_CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("TEST_AKS_PROXY_CA contains no certificates")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	return &aksTestTransport{
		endpoint: u,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (t *aksTestTransport) Do(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.URL.Scheme = t.endpoint.Scheme
	copy.URL.Host = t.endpoint.Host
	copy.Host = t.endpoint.Host
	copy.Header.Del("Authorization")
	return t.client.Do(copy)
}
