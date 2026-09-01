# Changelog

## 1.1.0

### Refactoring

- Centralized target, artifact, archive, and checksum rules in `internal/distribution`.
- Added a deterministic, path-safe Go release packager.
- Replaced source compilation as the default installation path with self-contained platform bundles.
- Unified Unix and Windows installer behavior around stable release asset names.

### Security and reliability

- Rejects unknown OAuth scopes instead of escalating them to the full scope set.
- Enforces client grant metadata, exact redirect binding, bounded OAuth state, and refresh-token family replay revocation.
- Cleans up timed-out bridge processes and falls back to bounded local termination when admin shutdown fails.
- Validates release archives before extraction and makes Unix and Windows replacement transactional and rollback-safe.
- Adds explicit setup-page failure states, bounded tunnel output, stale-version bridge replacement, and complete credential-redaction tests.
- Pins release dependencies and GitHub Actions, restores version injection, and adds CI quality gates.

### Installation

- Bundles cloudflared with CodexLink.
- Removes Go and ripgrep from end-user requirements.
- Automatically provisions Git through the available operating-system mechanism when it is missing.
- Verifies SHA-256 before extraction and installs CodexLink plus cloudflared as one recoverable transaction.
- Adds automatic user PATH configuration and starts onboarding immediately.
- Adds a hermetic installer smoke test.

## 1.0.0

- Initial CodexLink workspace bridge release.
