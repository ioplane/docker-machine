# Changelog

All notable changes follow [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

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

- Prism SDK transport isolation through the pinned `ioplane/prism-go-client`
  revision `5af0cc292b31f2a02f190297ef6e2a0c8540de6d`, based on upstream `v0.8.2`;
  concurrent clients retain separate TLS/proxy settings.
- Missing task UUID returns an error; malformed cloud-init returns an error
  instead of terminating the driver. Diagnostic helpers omit credentials and
  request/response payloads.
