# Changelog

All notable changes follow [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Optional consumer Create recovery validates pending owned-UUID journals before current flags or install scripts. SSH custom-script bytes, effective hostname and mode join the intent digest; legacy, complete and removed journals remain ineligible. Persisted digests support later lifecycle operations without the script mount; recovery never reconstructs VM POST permission.
- The actual CLI/plugin/SecretStore crash test validates the post-Create Secret PUT after restoring the pre-Create archive at the same absolute path, without guest SSH or a duplicate VM POST.

- The measured W0 compiler/API and performance receipt is linked from the development plan; legacy vendor files remain outside the compiler input set.

- Reproducible offline performance baselines with fixture assertions and repeated allocation/latency samples; benchmark smoke checks run under the race detector. No production workload or performance tuning is performed.

- Fork contribution governance checks new commit and PR titles plus Keep a Changelog structure in Podman; inherited SDK release headings are explicitly baselined.
- The modernization plan orders correctness, recovery, package structure, measured optimization and Rancher adoption through Beads dependencies.

- Isolated Testcontainers Go v0.44.0 module with a disposable Prism TLS fixture
  and a blocking rootless Podman CI gate covering queue payloads and API failures.

- Optional `nutanix-vm-nic-queues` for all newly created NICs, with strict positive
  integer and total vCPU validation. Omission preserves the previous API payload.
- Offline SDK payload and executable RPC compatibility tests, a development plan,
  and a documented Rancher/Nutanix compatibility contract.
- Pinned Podman development tools and blocking golangci-lint v2, formatting,
  race, vet, module consistency and vulnerability checks.

### Changed

- The pinned Prism SDK bounds decoded/filtered network JSON to 64 MiB and error bodies to 64 KiB. ResponseError status/cause classification replaces server-message matching; unfiltered io.Writer stays streaming. Large inventories require pagination.

- New commit titles use English declarative noun phrases; PR review language and inherited commit history remain unchanged.

- Go toolchain and dependencies refreshed; maintained Rancher Machine replaces
  archived Docker Machine while preserving the operational driver name and JSON
  configuration contract. No production driver registration is performed.

### Security

- API response diagnostics no longer echo server payloads, messages, details or arbitrary HTTP status text, while preserving cancellation and JSON causes through errors.Is/As.

### Fixed

- Create intent normalizes the default SSH username before hashing, so later SSH provisioning and JSON-restored lifecycle operations retain the same journal binding.

- PreCreateCheck reserves VM identity and bootstrap files before the consumer archives its machine directory. An independent per-machine journal recovers interrupted allocation by its stable client ID; VM response loss and restored checkpoints reconcile only the reserved UUID, without replaying an unknown VM POST or deleting a VM for a missing IP. Linux file locks serialize operations; corrupt, unsafe or mismatched journals fail closed. Breaking: operations require a durable or restored local Linux machine store, and preparation requires access to the Prism v3 idempotence allocator; there is no unsafe fallback. The consumer RPC/config format and legacy VMId removal remain compatible; automatic Rancher CLI resume is a separate consumer stage.

- Create preparation and reconciliation validate the bootstrap private/public SSH key pair before API access; missing, corrupt or mismatched credentials fail closed without exposing key material.
- Header-only cloud-init retains the exact `#cloud-config` recognition marker after the root SSH key merge.

- The disposable Prism fixture covers duplicate VLAN/VPC subnet names and reordered management NICs with nil endpoints and deterministic IPv4 selection; partial network profiles never reach VM creation.

- Create refuses resubmission while a VM identity is known. Remove validates that identity, observes the exact VM before deletion, returns success for confirmed absence without a DELETE, and requires observed absence after a successful delete task. HTTP 404 and terminal task errors are reconciled through VM readback; no mutation is retried.

- NIC payloads preserve the declared network order; named subnet lookups require exactly one candidate. Duplicate subnet identities, ambiguous VLAN/VPC names and malformed inventories are rejected without a partial profile. Management IP selection follows the first declared subnet UUID rather than the response NIC position, validates endpoints and uses stable IPv4-first address ordering.

- The isolated Testcontainers module uses the same immutable SDK revision as the driver; integration module validation rejects divergent replacements before compiling fixtures. Earlier boundary runs used its older SDK pin.

- One validated operation deadline covers constructor authentication, HTTP body reads, task/IP polling and both Restart transitions. v3 FAILED/ABORTED and malformed task responses fail immediately; only transient observation HTTP statuses retry with bounded jitter. Successful power tasks require the observed power state. Create failures and missing IP never trigger automatic VM deletion; accepted VM identity is retained. Critical lifecycle fixtures separate terminal assertions and expected submission counts for bounded cognitive complexity.

- The immutable Prism SDK pin includes context-aware constructor/cache entrypoints, coalesced client creation and protection against stale publication after Delete. Failed replacement retains the previous client; only SDK-owned v3 idle transports are retired. Driver RPC, VM queue fields and default operation deadlines remain unchanged.

- The immutable Prism SDK pin includes synchronized session cookies, coalesced refresh for concurrent explicit 401 responses, filtered-path parity and cancellation of waiters. Initial unauthenticated cookie headers remain unchanged until an explicit 401. Non-replayable bodies and unknown transport outcomes are not retried; driver auth configuration, RPC and VM queue fields are preserved.

- The immutable Prism SDK pin includes independent upload replay readers and correct remaining Content-Length; caller-owned regular files stay open across redirects. VM queue payloads and driver RPC/config fields are unchanged.

- Create rejects invalid CPU/RAM and overflowing or negative disk sizes before Prism requests, including restored state with omitted queues. Cloud-init merges only top-level users, preserves existing root settings and keys, and rejects ambiguous YAML without disclosing userdata.

- Requested Nutanix timeout is read before the 300-second minimum clamp, preserving custom values on a fresh driver and clamping low values after prior configuration.

- Malformed create metadata returns an error; an accepted VM identity survives
  task validation failure; partially resolved NIC profiles are rejected before Create.
- SDK non-2xx failures and response-body ownership are covered by regression tests.

- Prism SDK transport isolation through the pinned `ioplane/prism-go-client`
  revision `6ee1b0d01b4e362ee4c929a77893ac7810b81a48`, based on upstream `v0.8.2`;
  concurrent clients retain separate TLS/proxy settings, and malformed filtered entity shapes return errors instead of panicking.
- Missing task UUID returns an error; malformed cloud-init returns an error
  instead of terminating the driver. Diagnostic helpers omit credentials and
  request/response payloads.
- Development checks use the default rootless Podman mapping, avoiding the
  failing nested `keep-id:uid=0,gid=0` mapping on GitHub-hosted runners.
