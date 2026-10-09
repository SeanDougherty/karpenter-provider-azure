//go:build aclbyoi

package aclbyoi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v9"
	"github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
	"github.com/Azure/karpenter-provider-azure/pkg/consts"
	"github.com/Azure/karpenter-provider-azure/pkg/operator/options"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

const (
	testSubscription = "18153b17-4e27-4b58-863e-f8105b8892a2"
	testCluster      = "/subscriptions/" + testSubscription + "/resourceGroups/e2e-rg/providers/Microsoft.ContainerService/managedClusters/e2e-cluster"
	testImage        = "/subscriptions/035db282-f1c8-4ce7-b78f-2a7265d5398c/resourceGroups/MarinerAKSTest/providers/Microsoft.Compute/galleries/MarinerAKSSig/images/AzureLinuxaclgen2-Dev/versions/202610.08.1791501169"
	testVersion      = "MarinerAKSSig-AzureLinuxaclgen2-Dev-202610.08.1791501169"
)

func configure(t *testing.T) {
	t.Helper()
	t.Setenv(ImageEnv, testImage)
	t.Setenv(ClusterEnv, testCluster)
}

func TestImageSelection(t *testing.T) {
	configure(t)
	o := &options.Options{ClusterName: "e2e-cluster", ProvisionMode: consts.ProvisionModeAKSMachineAPI, UseSIG: true}
	ctx := options.ToContext(context.Background(), o)
	nc := &v1beta1.AKSNodeClass{Spec: v1beta1.AKSNodeClassSpec{ImageFamily: lo.ToPtr(v1beta1.AzureContainerLinuxImageFamily)}}
	image, err := ImageID(ctx, nc)
	require.NoError(t, err)
	require.Equal(t, testImage, image)

	nc.Spec.ImageFamily = lo.ToPtr(v1beta1.AzureLinuxImageFamily)
	image, err = ImageID(ctx, nc)
	require.NoError(t, err)
	require.Empty(t, image)
	nc.Spec.ImageFamily = lo.ToPtr(v1beta1.AzureContainerLinuxImageFamily)

	for _, mode := range []string{consts.ProvisionModeAKSMachineAPIHeaderBatch, consts.ProvisionModeAKSScriptless, consts.ProvisionModeBootstrappingClient} {
		o.ProvisionMode = mode
		_, err := ImageID(ctx, nc)
		require.Error(t, err)
	}
	o.ProvisionMode = consts.ProvisionModeAKSMachineAPI
	o.UseSIG = false
	_, err = ImageID(ctx, nc)
	require.Error(t, err)
	o.UseSIG = true
	o.ClusterName = "different-cluster"
	_, err = ImageID(ctx, nc)
	require.Error(t, err)
	o.ClusterName = "e2e-cluster"
	nc.Spec.FIPSMode = lo.ToPtr(v1beta1.FIPSModeFIPS)
	_, err = ImageID(ctx, nc)
	require.Error(t, err)
	nc.Spec.FIPSMode = nil
	nc.Spec.Security = &v1beta1.Security{TrustedLaunch: &v1beta1.TrustedLaunch{SecureBoot: lo.ToPtr(false), VTPM: lo.ToPtr(false)}}
	_, err = ImageID(ctx, nc)
	require.Error(t, err)
}

func TestInvalidConfigFailsClosed(t *testing.T) {
	for _, tc := range []struct{ name, image, cluster, subscription string }{
		{"missing image", "", testCluster, testSubscription},
		{"missing scope", testImage, "", testSubscription},
		{"latest", strings.Replace(testImage, "202610.08.1791501169", "latest", 1), testCluster, testSubscription},
		{"production definition", strings.Replace(testImage, "-Dev", "", 1), testCluster, testSubscription},
		{"ARM64 candidate", strings.Replace(testImage, "aclgen2-Dev", "aclgen2Arm64-Dev", 1), testCluster, testSubscription},
		{"protected subscription", testImage, strings.Replace(testCluster, testSubscription, "035db282-f1c8-4ce7-b78f-2a7265d5398c", 1), "035db282-f1c8-4ce7-b78f-2a7265d5398c"},
		{"client mismatch", testImage, testCluster, "26ad903f-2330-429d-8389-864ac35c4350"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(ImageEnv, tc.image)
			t.Setenv(ClusterEnv, tc.cluster)
			_, err := ClientPolicy(tc.subscription)
			require.Error(t, err)
		})
	}
}

type capture struct {
	calls  int
	body   []byte
	header http.Header
}

func (c *capture) Do(req *http.Request) (*http.Response, error) {
	c.calls++
	c.header = req.Header.Clone()
	if req.Body != nil {
		var err error
		c.body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"unchanged":true}`)), Request: req}, nil
}

func runRequest(t *testing.T, method, scope string, body []byte, headers http.Header) (*capture, *http.Response, error) {
	t.Helper()
	p, err := ClientPolicy(testSubscription)
	require.NoError(t, err)
	tr := &capture{}
	pipeline := runtime.NewPipeline("aclbyoi-test", "v1.0.0", runtime.PipelineOptions{PerCall: []policy.Policy{p}}, &policy.ClientOptions{Transport: tr})
	req, err := runtime.NewRequest(context.Background(), method, "https://management.azure.com"+scope+"/agentPools/aksmanagedap/machines/node")
	require.NoError(t, err)
	if body != nil {
		require.NoError(t, req.SetBody(streaming.NopCloser(bytes.NewReader(body)), "application/json"))
	}
	for key, values := range headers {
		req.Raw().Header[key] = values
	}
	response, err := pipeline.Do(req)
	return tr, response, err
}

func machineBody(t *testing.T, sku armcontainerservice.OSSKU, version string) []byte {
	t.Helper()
	body, err := json.Marshal(armcontainerservice.Machine{
		Properties: &armcontainerservice.MachineProperties{
			NodeImageVersion: lo.ToPtr(version),
			OperatingSystem:  &armcontainerservice.MachineOSProfile{OSSKU: lo.ToPtr(sku)},
		},
	})
	require.NoError(t, err)
	return body
}

func TestMachineBYOIUsesHeadersWithoutAlteringOtherFieldsOrResponse(t *testing.T) {
	configure(t)
	body := machineBody(t, armcontainerservice.OSSKUAzureContainerLinux, testVersion)
	var outer map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &outer))
	var properties map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(outer["properties"], &properties))
	properties["futureField"] = json.RawMessage(`{"preserved":true}`)
	outer["properties"], _ = json.Marshal(properties)
	outer["tags"] = json.RawMessage(`{"test":"preserved"}`)
	body, _ = json.Marshal(outer)
	headers := http.Header{}
	headers.Set("AKSHTTPCustomFeatures", "ExistingFeature")
	tr, response, err := runRequest(t, http.MethodPut, testCluster, body, headers)
	require.NoError(t, err)
	require.Equal(t, 1, tr.calls)
	require.NoError(t, json.Unmarshal(tr.body, &outer))
	require.NoError(t, json.Unmarshal(outer["properties"], &properties))
	require.NotContains(t, string(tr.body), "nodeImageVersion")
	require.JSONEq(t, `{"preserved":true}`, string(properties["futureField"]))
	require.JSONEq(t, `{"test":"preserved"}`, string(outer["tags"]))
	require.Equal(t, "ExistingFeature,Microsoft.ContainerService/UseCustomizedOSImage", tr.header.Get("AKSHTTPCustomFeatures"))
	require.Equal(t, "CustomizedImageTrustedLaunch", tr.header.Get("OSDistro"))
	require.Equal(t, "AzureContainerLinux", tr.header.Get("OSSKU"))
	require.Equal(t, "035db282-f1c8-4ce7-b78f-2a7265d5398c", tr.header.Get("OSImageSubscriptionID"))
	require.Equal(t, "MarinerAKSTest", tr.header.Get("OSImageResourceGroup"))
	require.Equal(t, "MarinerAKSSig", tr.header.Get("OSImageGallery"))
	require.Equal(t, "AzureLinuxaclgen2-Dev", tr.header.Get("OSImageName"))
	require.Equal(t, "202610.08.1791501169", tr.header.Get("OSImageVersion"))
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.JSONEq(t, `{"unchanged":true}`, string(data))
}

func TestNonACLAndReadRequestsAreUnchanged(t *testing.T) {
	configure(t)
	body := machineBody(t, armcontainerservice.OSSKUAzureLinux, "AKSAzureLinux-V3gen2-202609.23.0")
	tr, _, err := runRequest(t, http.MethodPut, testCluster, body, nil)
	require.NoError(t, err)
	require.Equal(t, body, tr.body)
	require.Empty(t, tr.header.Get("OSImageName"))
	tr, _, err = runRequest(t, http.MethodGet, testCluster, nil, nil)
	require.NoError(t, err)
	require.Empty(t, tr.header.Get("OSImageName"))
}

func TestMachineRequestFailuresDoNotReachAzure(t *testing.T) {
	configure(t)
	for _, tc := range []struct {
		name, scope string
		body        []byte
		header      http.Header
	}{
		{"out of scope", testCluster + "-different", machineBody(t, armcontainerservice.OSSKUAzureContainerLinux, testVersion), nil},
		{"bad body", testCluster, []byte("{"), nil},
		{"missing body", testCluster, nil, nil},
		{"wrong image", testCluster, machineBody(t, armcontainerservice.OSSKUAzureContainerLinux, "AKSAzureLinux-aclgen2TL-202609.23.0"), nil},
		{"conflict", testCluster, machineBody(t, armcontainerservice.OSSKUAzureContainerLinux, testVersion), http.Header{"Osimagename": []string{"different"}}},
		{"batch", testCluster, machineBody(t, armcontainerservice.OSSKUAzureContainerLinux, testVersion), http.Header{"Batchputmachine": []string{"batch"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, _, err := runRequest(t, http.MethodPut, tc.scope, tc.body, tc.header)
			require.Error(t, err)
			require.Zero(t, tr.calls)
		})
	}
}
