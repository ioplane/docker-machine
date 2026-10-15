# Contributing

## Fork contribution and release policy

The ioplane fork follows [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/)
and [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/).
Use English declarative noun phrases for new commit titles:
`fix(http): sanitized response diagnostics`. PR titles and descriptions follow
the review language convention (technical Russian). Inherited commit titles
remain accepted; published history is not rewritten. Allowed types are `build`, `chore`, `ci`,
`docs`, `feat`, `fix`, `perf`, `refactor`, `revert`, `style`, and `test`.
Mark incompatible public API or minimum-toolchain changes with `!` and document
consumer migration in CHANGELOG and the compatibility contract.

Every user-visible, operational, policy or tooling change adds an impact entry
under `Unreleased`, grouped as Added, Changed, Deprecated, Removed, Fixed or
Security. New release headings use dated SemVer: `## [1.2.3] - YYYY-MM-DD`.
Published historical entries and inherited commits are preserved. The inherited
SDK release-header exceptions in `.ci/changelog-legacy.txt` do not authorize new
undated releases. Merge commits are excluded from title checks; PR titles and
all non-merge commits in the reviewed range are checked.

Run the governance target through the declared Podman environment. CI verifies
the same target. Checks are workflow requirements; branch protection and review
approval must be verified separately. CI success does not authorize merging,
registering a driver in Rancher or changing production VMs.

The detailed migration contract is in [the development plan](docs/ru/development-plan.md).
