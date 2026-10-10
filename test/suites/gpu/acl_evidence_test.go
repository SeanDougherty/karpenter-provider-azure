package gpu_test

import (
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v7"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	coretest "sigs.k8s.io/karpenter/pkg/test"
)

// Verify this ACL vGPU fixture at the signed system-extension layer.
// This raw layer belongs to the strictly Notation-verified Microsoft artifact
// nvidia-driver-vgpu@sha256:621b9dd4a862bcd71255f1461bc45f60ee6e2a6176ce6b336bac2f08fc9c433a.
const aclVGPUDriverSHA256 = "c811e7314f083d4238249e20f3552f07eba66f07d2636237f2f6106baddc52b5"

func verifyACLGPUNode(node *corev1.Node) {
	vm := env.GetVM(node.Name)
	Expect(vm.Properties).ToNot(BeNil())
	Expect(vm.Properties.SecurityProfile).ToNot(BeNil())
	Expect(vm.Properties.SecurityProfile.SecurityType).To(Equal(lo.ToPtr(armcompute.SecurityTypesTrustedLaunch)))
	Expect(vm.Properties.SecurityProfile.UefiSettings).ToNot(BeNil())
	Expect(vm.Properties.SecurityProfile.UefiSettings.SecureBootEnabled).To(Equal(lo.ToPtr(true)))
	Expect(vm.Properties.SecurityProfile.UefiSettings.VTpmEnabled).To(Equal(lo.ToPtr(true)))

	pod := env.Pod(coretest.PodOptions{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system"},
		Image:      "mcr.microsoft.com/azurelinux/busybox:1.36",
		Command: []string{"sh", "-ec", `chroot /host /bin/sh -ec '
signer="$(modinfo -F signer nvidia)"
printf "ACL_GPU_DRIVER_SIGNER=%s\n" "$signer"
nvidia-smi -L
test "$(cat /sys/module/nvidia/version)" = "$(modinfo -F version nvidia)"
sysext_status="$(systemd-sysext status --json=short)"
printf "%s\n" "$sysext_status"
printf "%s\n" "$sysext_status" | grep -q "\"nvidia-driver-vgpu\""
printf "ACL_GPU_SYSEXT_SHA256="
sha256sum /etc/extensions/nvidia-driver-vgpu.raw
echo ACL_GPU_PROBE_COMPLETE
'`},
		NodeSelector:  map[string]string{corev1.LabelHostname: node.Name},
		RestartPolicy: corev1.RestartPolicyNever,
	})
	pod.Spec.Tolerations = []corev1.Toleration{{Operator: corev1.TolerationOpExists}}
	pod.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: lo.ToPtr(true)}
	pod.Spec.Volumes = []corev1.Volume{{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}}}}
	pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "host", MountPath: "/host", ReadOnly: true}}
	env.ExpectCreated(pod)
	defer env.ExpectDeleted(pod)
	Eventually(func(g Gomega) {
		g.Expect(env.Client.Get(env.Context, client.ObjectKeyFromObject(pod), pod)).To(Succeed())
		if pod.Status.Phase == corev1.PodFailed {
			output, err := env.KubeClient.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
				Container: pod.Spec.Containers[0].Name,
			}).DoRaw(env.Context)
			StopTrying(fmt.Sprintf("ACL GPU driver inspection failed: %+v; logs=%s; logError=%v", pod.Status.ContainerStatuses, output, err)).Now()
		}
		g.Expect(pod.Status.Phase).To(Equal(corev1.PodSucceeded))
	}).WithTimeout(2 * time.Minute).Should(Succeed())
	output := env.EventuallyGetPodLogs(pod)
	Expect(output).To(ContainSubstring("GPU 0:"))
	Expect(output).To(ContainSubstring("nvidia-driver-vgpu"))
	Expect(output).To(MatchRegexp(`(?m)^ACL_GPU_SYSEXT_SHA256=` + aclVGPUDriverSHA256 + `\s`))
	Expect(output).To(ContainSubstring("ACL_GPU_PROBE_COMPLETE"))
	By("ACL GPU driver evidence:\n" + output)
}
