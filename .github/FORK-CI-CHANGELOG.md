# Changelog

Fork CI configuration follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Fork-only Blacksmith workflow tests exact same-repository PR commits through the
  declared Podman environment. The base-only configuration stays outside feature
  branches and PR diffs; test jobs have read permissions, no injected secrets or
  shared action caches. A separate no-checkout job reports the candidate status.

### Changed

- The GitHub-hosted quality workflow can be disabled in fork repository settings
  only after the Blacksmith replacement passes. Re-enable it to restore the former
  runner path; revert the fork-only configuration commit to retire this overlay.
