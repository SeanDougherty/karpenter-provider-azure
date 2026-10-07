# Testing Directory

These test scenarios are designed to be used against a live AKS cluster running karpenter, and validate particular E2E scenarios.

## Pinned ACL qualification

`TEST_AKS_IMAGE_FAMILY=AzureContainerLinux` selects ACL for tests that use
`DefaultAKSNodeClass`. It requires a Machine API `PROVISION_MODE`. Explicit
image-family tests remain unchanged compatibility controls; a whole suite pass
does not mean that those explicit Ubuntu/Azure Linux cases ran on ACL.
The Utilization suite adds an ACL variant of the existing pod-per-node check.
ACL evidence is collected by the healthy-deployment, healthy-pod-count, and
initialized-node helpers. Drift uses healthy-pod-count before and after
replacement; generic helpers must not bypass the optional ACL checks.

Standalone runners may supply `TEST_AKS_PROXY_URL` (a loopback HTTPS origin) and
`TEST_AKS_PROXY_CA` (its CA file). Only ContainerService API calls use this
bridge; Compute and Network requests continue to real ARM. The trusted local
runner owns ingress authentication, and the bridge strips ARM bearer tokens.
Without these variables the normal ARM transport is unchanged.

These are test-only inputs and do not require rebuilding the controller or CRD.

## File Directory
- `/suites`: Ginkgo test suites for particular scenarios live here.
- `/pkg`: Common code re-used across test suites lives here.
