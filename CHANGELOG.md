# Changelog

All notable changes follow [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

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
