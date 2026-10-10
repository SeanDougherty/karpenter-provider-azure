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

Drift and Consolidation's CPU-packing fixtures declare hostname topology explicitly so
Automatic Safeguards does not inject anti-affinity that changes their node count.
Consolidation's pod-event case verifies its single-node precondition after all
pods are healthy; the existing five-second timestamp assertion is unchanged.
The 50-pod delete-consolidation fixture permits packing up to its eight-CPU
SKU ceiling instead of inheriting a preference for one pod per node.
It retains all 50 initial pods, scales to 20, and requires average CPU
utilization below 0.5 before consolidation and above 0.6 afterward.
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
The bridge permits only the scenario cluster plus read-only polling of exact
operation/result URLs returned by its successful responses in the same
subscription and region. Unrelated operations and writes to those URLs
remain blocked.
Without these variables the normal ARM transport is unchanged.

These are test-only inputs and do not require rebuilding the controller or CRD.

Stage 4 lifecycle fixtures also support standalone NodeClaims: ACL evidence
follows the Node's real NodeClaim owner when there is no NodePool, requiring
matching UID, node name, and provider ID before resolving its AKSNodeClass.
The existing OS, image, version, Machine, and network assertions still apply.
ACL ephemeral-disk fixtures use 64 GiB rather than the generic 50-GiB test disk.
The cache-only control uses `Standard_D4pls_v5`: 8 GiB memory fits the
Automatic agents, and its 40-GiB disk fits the verified 100-GiB cache even
though the SKU does not support ephemeral OS disks. It remains an explicit
Ubuntu2204 control because this SKU does not support ACL's required Trusted
Launch.
Managed Machine networking checks read the actual cluster network profile
instead of assuming a controller Deployment exists in the workload cluster.
The ACL orphan-NIC fixture creates an isolated disposable subnet in its
owning parent resource group, leaving the protected managed subnet and all
permissions unchanged. The tagged orphan NIC still lives in the node
resource group inspected by the original garbage collector. Its real
deletion assertion is unchanged; cleanup deletes NIC before VNet.
Chaos retains its five-minute, fewer-than-35-node assertion and unmodified
taint injection. It records real node snapshots during that window and checks
ACL image, exact kubelet version, Machine annotation, and provider ID afterward.
No observed nodes is a failure in the ACL lane. These snapshots do not assert
workload/network readiness or a live Machine GET after a node has been deleted.

Upgrade suites accept `TEST_AKS_KUBERNETES_UPGRADE_TARGET` only when that exact
version is advertised for the cluster and newer than its current version.
An omitted target retains the highest-advertised-version behavior. Explicit
ACL selection also applies to KubernetesUpgrade; its existing drift,
replacement, and actual kubelet-version assertions remain in place.
The RP's separately named `Machine_1363To1364` and
`KubernetesUpgrade_1363To1364` lanes qualify a patch transition, not a minor
upgrade or the production 1.38 gate. They require actual API/system/ACL
kubelets to match the requested versions. The Machine concurrency fixture
uses explicit two-pod-per-node packing, retaining its original CPU requests,
node counts, and requirement to scale while the cluster is Upgrading.
These unpublished lanes require a local test commit containing the upgrade
fixture revision; the RP rejects older/default source before provisioning,
so an old suite cannot silently ignore the explicit target.

The RP lifecycle runner can consume an unpublished local test commit with
`ACL_LIFECYCLE_LOCAL_REPOSITORY=<absolute-worktree-path>` and
`ACL_LIFECYCLE_TEST_REVISION=<full-commit-SHA>`. Both inputs are required.
It still checks ancestry and rejects any delta outside `test/`; the original
controller/CRD and default remote source remain unchanged. The selected
repository and commit are included in the runtime evidence.
Local partial clones are consumed through a temporary shared-object checkout;
keep the source worktree and pinned commit available until the run finishes.
ACL runner teardown runs after validation failures and waits for both cluster
and resource-group deletion. Explicit cluster reuse retains the subscription
in resource identities even when creation handlers are mocked.

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
