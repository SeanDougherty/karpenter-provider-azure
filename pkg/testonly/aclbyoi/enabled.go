//go:build aclbyoi

package aclbyoi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v9"
	"github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
	"github.com/Azure/karpenter-provider-azure/pkg/consts"
	"github.com/Azure/karpenter-provider-azure/pkg/operator/options"
	"github.com/samber/lo"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	ImageEnv   = "ACL_TEST_BYOI_IMAGE_ID"
	ClusterEnv = "ACL_TEST_BYOI_CLUSTER_ID"
)

var (
	imagePattern   = regexp.MustCompile(`(?i)^/subscriptions/([0-9a-f-]{36})/resourceGroups/([^/]+)/providers/Microsoft.Compute/galleries/([^/]+)/images/([^/]+)/versions/([0-9]+\.[0-9]+\.[0-9]+)$`)
	clusterPattern = regexp.MustCompile(`(?i)^/subscriptions/([0-9a-f-]{36})/resourceGroups/([^/]+)/providers/Microsoft.ContainerService/managedClusters/([^/]+)$`)
)

type config struct {
	image, cluster, subscription, group, gallery, definition, version string
}

func loadConfig() (*config, error) {
	image, cluster := os.Getenv(ImageEnv), os.Getenv(ClusterEnv)
	i, c := imagePattern.FindStringSubmatch(image), clusterPattern.FindStringSubmatch(cluster)
	if i == nil || c == nil {
		return nil, fmt.Errorf("aclbyoi test build requires exact versioned %s and %s resource IDs", ImageEnv, ClusterEnv)
	}
	if c[1] != "18153b17-4e27-4b58-863e-f8105b8892a2" && c[1] != "26ad903f-2330-429d-8389-864ac35c4350" {
		return nil, fmt.Errorf("aclbyoi is restricted to the approved E2E subscriptions, got %s", c[1])
	}
	if i[3] != "MarinerAKSSig" || i[4] != "AzureLinuxaclgen2-Dev" {
		return nil, fmt.Errorf("aclbyoi requires the AMD64 non-FIPS ACL development image, got %s/%s", i[3], i[4])
	}
	return &config{image: image, cluster: cluster, subscription: i[1], group: i[2], gallery: i[3], definition: i[4], version: i[5]}, nil
}

func ImageID(ctx context.Context, nodeClass *v1beta1.AKSNodeClass) (string, error) {
	if lo.FromPtr(nodeClass.Spec.ImageFamily) != v1beta1.AzureContainerLinuxImageFamily {
		return "", nil
	}
	cfg, err := loadConfig()
	if err != nil {
		return "", err
	}
	o := options.FromContext(ctx)
	if o.ProvisionMode != consts.ProvisionModeAKSMachineAPI || !o.UseSIG {
		return "", fmt.Errorf("aclbyoi requires non-batched AKS Machine API and managed SIG access")
	}
	if !strings.EqualFold(o.ClusterName, clusterPattern.FindStringSubmatch(cfg.cluster)[3]) {
		return "", fmt.Errorf("aclbyoi controller cluster does not match its explicit E2E scope")
	}
	if lo.FromPtr(nodeClass.Spec.FIPSMode) == v1beta1.FIPSModeFIPS || nodeClass.IsKataEnabled() || !nodeClass.IsTrustedLaunchEnabled() {
		return "", fmt.Errorf("aclbyoi candidate only supports non-FIPS, non-Kata Trusted Launch ACL")
	}
	log.FromContext(ctx).Info("Selecting test-only ACL BYOI", "imageID", cfg.image, "clusterID", cfg.cluster)
	return cfg.image, nil
}

func ClientPolicy(subscription string) (policy.Policy, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(subscription, clusterPattern.FindStringSubmatch(cfg.cluster)[1]) {
		return nil, fmt.Errorf("aclbyoi client subscription does not match its explicit E2E scope")
	}
	return cfg, nil
}

func (c *config) nodeImageVersion() string {
	return strings.Join([]string{c.gallery, c.definition, c.version}, "-")
}

func (c *config) Do(req *policy.Request) (*http.Response, error) {
	raw := req.Raw()
	path := strings.ToLower(raw.URL.Path)
	if raw.Method != http.MethodPut || !strings.Contains(path, "/machines/") {
		return req.Next()
	}
	tail, inScope := strings.CutPrefix(path, strings.ToLower(c.cluster)+"/agentpools/")
	if !inScope || len(strings.Split(tail, "/")) != 3 || strings.Split(tail, "/")[1] != "machines" {
		return nil, fmt.Errorf("aclbyoi refuses Machine PUT outside its exact E2E cluster: %s", raw.URL.Path)
	}
	if raw.Header.Get("BatchPutMachine") != "" || req.Body() == nil {
		return nil, fmt.Errorf("aclbyoi requires an individual Machine body")
	}
	data, err := io.ReadAll(req.Body())
	if err != nil {
		return nil, fmt.Errorf("reading aclbyoi Machine request: %w", err)
	}
	if _, err := req.Body().Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewinding aclbyoi Machine request: %w", err)
	}
	var machine armcontainerservice.Machine
	if err := json.Unmarshal(data, &machine); err != nil {
		return nil, fmt.Errorf("decoding aclbyoi Machine request: %w", err)
	}
	if machine.Properties == nil || machine.Properties.OperatingSystem == nil || lo.FromPtr(machine.Properties.OperatingSystem.OSSKU) != armcontainerservice.OSSKUAzureContainerLinux {
		return req.Next()
	}
	if lo.FromPtr(machine.Properties.NodeImageVersion) != c.nodeImageVersion() {
		return nil, fmt.Errorf("aclbyoi refuses ACL Machine with unexpected image version %q", lo.FromPtr(machine.Properties.NodeImageVersion))
	}
	if lo.FromPtr(machine.Properties.OperatingSystem.EnableFIPS) {
		return nil, fmt.Errorf("aclbyoi candidate does not support FIPS")
	}

	// The RP prioritizes an explicit Machine version over BYOI headers. Remove
	// only that field; preserve unknown request properties and all responses.
	var body map[string]json.RawMessage
	var properties map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body["properties"], &properties); err != nil {
		return nil, err
	}
	delete(properties, "nodeImageVersion")
	body["properties"], err = json.Marshal(properties)
	if err != nil {
		return nil, err
	}
	data, err = json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if err := req.SetBody(streaming.NopCloser(bytes.NewReader(data)), "application/json"); err != nil {
		return nil, err
	}
	features := raw.Header.Get("AKSHTTPCustomFeatures")
	if features != "" {
		features += ","
	}
	raw.Header.Set("AKSHTTPCustomFeatures", features+"Microsoft.ContainerService/UseCustomizedOSImage")
	for key, value := range map[string]string{
		"OSImageSubscriptionID": c.subscription,
		"OSImageResourceGroup":  c.group,
		"OSImageGallery":        c.gallery,
		"OSImageName":           c.definition,
		"OSImageVersion":        c.version,
		"OSSKU":                 "AzureContainerLinux",
		"OSDistro":              "CustomizedImageTrustedLaunch",
	} {
		if existing := raw.Header.Get(key); existing != "" && existing != value {
			return nil, fmt.Errorf("aclbyoi refuses conflicting %s header", key)
		}
		raw.Header.Set(key, value)
	}
	log.FromContext(raw.Context()).Info("Applying test-only ACL Machine BYOI headers", "machine", raw.URL.Path, "imageID", c.image)
	return req.Next()
}
