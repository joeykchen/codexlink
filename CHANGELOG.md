# Changelog

## 1.0.0

### Workspace bridge

- Adds a local, read-only MCP bridge for workspace files, search, Git state, diffs, and execution summaries.
- Supports repository groups while enforcing workspace boundaries and sensitive-file policies.
- Provides CLI setup, status, session, execution-record, and lifecycle management commands.

### ChatGPT integration

- Supports ChatGPT client metadata and `private_key_jwt` authentication with RS256 verification and replay protection.
- Makes authorization completion idempotent and compatible with embedded authorization windows.
- Guides users through app authorization and attaching CodexLink to regular Chat conversations.
- Reliably opens the local setup page across supported desktop platforms.

### Security and reliability

- Rejects unknown OAuth scopes instead of escalating them to the full scope set.
- Enforces client grant metadata, exact redirect binding, bounded OAuth state, and refresh-token family replay revocation.
- Cleans up timed-out bridge processes and falls back to bounded local termination when admin shutdown fails.
- Validates release archives before extraction and makes Unix and Windows replacement transactional and rollback-safe.
- Adds explicit setup-page failure states, bounded tunnel output, stale-version bridge replacement, and complete credential-redaction tests.
- Pins release dependencies and GitHub Actions, restores version injection, and adds CI quality gates.

### Installation

- Ships deterministic, path-safe release packages for macOS, Linux, and Windows.
- Bundles cloudflared with CodexLink.
- Removes Go and ripgrep from end-user requirements.
- Automatically provisions Git through the available operating-system mechanism when it is missing.
- Verifies SHA-256 before extraction and installs CodexLink plus cloudflared as one recoverable transaction.
- Adds automatic user PATH configuration and starts onboarding immediately.
- Adds a hermetic installer smoke test.
