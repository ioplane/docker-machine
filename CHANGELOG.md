# Changelog

All notable changes follow [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Isolated Testcontainers Go v0.44.0 module with a disposable Prism TLS fixture
  and a blocking rootless Podman CI gate covering queue payloads and API failures.

- Optional `nutanix-vm-nic-queues` for all newly created NICs, with strict positive
  integer and total vCPU validation. Omission preserves the previous API payload.
- Offline SDK payload and executable RPC compatibility tests, a development plan,
  and a documented Rancher/Nutanix compatibility contract.
- Pinned Podman development tools and blocking golangci-lint v2, formatting,
  race, vet, module consistency and vulnerability checks.

### Changed

- Go toolchain and dependencies refreshed; maintained Rancher Machine replaces
  archived Docker Machine while preserving the operational driver name and JSON
  configuration contract. No production driver registration is performed.

### Fixed

- Malformed create metadata returns an error; an accepted VM identity survives
  task validation failure; partially resolved NIC profiles are rejected before Create.
- SDK non-2xx failures and response-body ownership are covered by regression tests.

- Prism SDK transport isolation through the pinned `ioplane/prism-go-client`
  revision `5645bd8b8b65dcb418b933e4ef764599e95af095`, based on upstream `v0.8.2`;
  concurrent clients retain separate TLS/proxy settings.
- Missing task UUID returns an error; malformed cloud-init returns an error
  instead of terminating the driver. Diagnostic helpers omit credentials and
  request/response payloads.
- Development checks use the default rootless Podman mapping, avoiding the
  failing nested `keep-id:uid=0,gid=0` mapping on GitHub-hosted runners.
