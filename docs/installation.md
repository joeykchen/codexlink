# Installation architecture

## User contract

Installation is one operation, not a checklist of product dependencies. The user invokes the bootstrap script from a workspace; the script selects and verifies a self-contained release, validates every archive entry, installs it atomically, updates the user PATH, provisions Git when necessary, and launches `codexlink`.

```text
bootstrap script
  -> stable release asset name
  -> SHA-256 verification
  -> archive allowlist and link/path checks
  -> codexlink + cloudflared
  -> atomic user-local install
  -> PATH registration
  -> automatic Git provisioning when absent
  -> workspace onboarding
```

## Dependency policy

- **Go:** build-time only. End users download a precompiled binary.
- **cloudflared:** bundled in every supported release archive and installed next to CodexLink.
- **ripgrep:** optional. Workspace search has a native Go implementation.
- **Git:** detected at installation. When absent, the installer invokes an available OS installation mechanism. Failure to provision Git leaves non-Git MCP tools available.
- **Bootstrap facilities:** Unix requires a POSIX shell, `tar`, `awk`, an HTTPS downloader (`curl` or `wget`), and a SHA-256 tool (`sha256sum` or `shasum`). Windows uses PowerShell and .NET archive, HTTP, and hashing APIs. These are operating-system facilities rather than CodexLink product dependencies.

A system password, elevation dialog, or Command Line Tools confirmation may still be required when the operating system installs Git.

## Supported release targets

```text
darwin/amd64
darwin/arm64
linux/amd64
linux/arm64
windows/amd64
```

Windows/ARM64 is deliberately not advertised because the bundled tunnel dependency has no native upstream Windows/ARM64 release. Platform and asset conventions are defined once in `internal/distribution`; unsupported targets fail before download or packaging.

## Security properties

1. Release packages and checksum files are fetched over HTTPS.
2. SHA-256 is checked before extraction.
3. The installer accepts only the exact release-file allowlist.
4. Absolute paths, nested paths, duplicate entries, links, oversized archives, and oversized entries are rejected.
5. Files are installed as one same-directory transaction; Unix and Windows restore the previous pair if replacement fails.
6. Executables are installed to a user-owned directory by default.
7. The installer never evaluates script text from the downloaded archive.
8. Version and repository overrides are explicit environment variables for mirrors and enterprise distribution.

## Automation overrides

```text
CODEXLINK_INSTALL_DIR
CODEXLINK_VERSION
CODEXLINK_REPOSITORY
CODEXLINK_NO_START=1
CODEXLINK_SKIP_GIT=1
CODEXLINK_SKIP_PATH_UPDATE=1
CODEXLINK_BUNDLE_FILE
CODEXLINK_CHECKSUM_FILE
```

The final two variables provide an offline/test path without weakening checksum or archive validation.
