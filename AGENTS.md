# Agent Guidelines

- Spec changes go through OpenSpec (propose -> apply -> archive); openspec/specs is the source of truth for behavior. Update docs/docs when behavior documented there changes. Never edit openspec/specs directly outside a change.
- When adding new functionality, make sure it has enough test coverage, and doesn't break existing unit tests.
- Releases: never build or push Docker images locally. Pushing a `vX.Y.Z` git tag triggers the GitHub Actions workflow that builds and publishes the `gobobr/...` Docker Hub images and creates the GitHub release automatically; just tag and push the tag after the commit is on main.
