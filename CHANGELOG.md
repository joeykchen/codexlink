# Changelog

## 1.1.0

### Refactoring

- Centralized target, artifact, archive, and checksum rules in `internal/distribution`.
- Added a deterministic, path-safe Go release packager.
- Replaced source compilation as the default installation path with self-contained platform bundles.
- Unified Unix and Windows installer behavior around stable release asset names.

### Installation

- Bundles cloudflared with CodexLink.
- Removes Go and ripgrep from end-user requirements.
- Automatically provisions Git through the available operating-system mechanism when it is missing.
- Verifies SHA-256 before extraction and performs atomic binary replacement.
- Adds automatic user PATH configuration and starts onboarding immediately.
- Adds a hermetic installer smoke test.

## 1.0.0

- Initial CodexLink workspace bridge release.
