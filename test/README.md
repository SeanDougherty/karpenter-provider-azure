# Testing Directory

These test scenarios are designed to be used against a live AKS cluster running karpenter, and validate particular E2E scenarios.

## Pinned ACL qualification

`TEST_AKS_IMAGE_FAMILY=AzureContainerLinux` selects ACL for tests that use
`DefaultAKSNodeClass`. It requires a Machine API `PROVISION_MODE`. Explicit
image-family tests remain unchanged compatibility controls; a whole suite pass
does not mean that those explicit Ubuntu/Azure Linux cases ran on ACL.
Trusted Launch's optional/disabled-security controls explicitly retain
Ubuntu2204; ACL's required Secure Boot and vTPM are covered by its dedicated
automatic-enablement case. Selecting ACL globally must not turn those
positive compatibility controls into invalid ACL requests.
Artifact-streaming host inspection waits for successful completion and complete
output; missing host configuration or failed process inspection must not pass
as evidence that streaming is disabled.
The Utilization suite adds an ACL variant of the existing pod-per-node check.
ACL evidence is collected by the healthy-deployment, healthy-pod-count, and
initialized-node helpers. Drift uses healthy-pod-count before and after
replacement; generic helpers must not bypass the optional ACL checks.
Evidence also records the Machine's drift action/reason. NodeClaim debug
updates include drift, deletion, and hash-version transitions so an existing
drift reason can be distinguished from a mutation performed by a test.
ACL workload evidence requires the real kubelet version to match the
NodeClass's requested version exactly. An RC kubelet advertised as a final
release is a lifecycle precondition failure, not a reason to suppress the
provider's version-drift detection.

Drift's CPU-packing budget fixtures declare hostname topology explicitly so
Automatic Safeguards does not inject anti-affinity that changes their node count.
The delete-budget cases wait for a disruption to start before checking its
concurrency. Budget accounting excludes nodes retained by the testing finalizer
only after Karpenter removes its termination finalizer, even when the NodeClaim's
`InstanceTerminating` condition was not persisted on the instance-not-found path.
Deletion starting alone is not sufficient. The node/claim ceilings and
zero-active-disruptions assertion remain unchanged.

Standalone runners may supply `TEST_AKS_PROXY_URL` (a loopback HTTPS origin) and
`TEST_AKS_PROXY_CA` (its CA file). Only ContainerService API calls use this
bridge; Compute and Network requests continue to real ARM. The trusted local
runner owns ingress authentication, and the bridge strips ARM bearer tokens.
Without these variables the normal ARM transport is unchanged.

These are test-only inputs and do not require rebuilding the controller or CRD.

The GPU table includes an explicit managed ACL case. It preserves the existing
GPU resource/workload assertions and additionally verifies ACL node identity,
Trusted Launch/Secure Boot/vTPM, and `nvidia-smi -L` on a real
`Standard_NV6ads_A10_v5` node. ACL installs its driver through a system
extension, so an empty individual module-signer field alone does not establish
a signing failure.
The test checks the loaded module version and active extension, and compares
the actual extension bytes against the pinned raw layer of a Microsoft-signed
OCI manifest (Notation strict verification using Microsoft Supply Chain RSA
Root CA 2022). Reverify that artifact's signature before updating the pinned
hash. This qualifies the pinned 3.0.20260809 vGPU extension, not every GPU
driver variant. Simulated AIManager GPU nodes cannot satisfy this hardware check.

## File Directory
- `/suites`: Ginkgo test suites for particular scenarios live here.
- `/pkg`: Common code re-used across test suites lives here.
