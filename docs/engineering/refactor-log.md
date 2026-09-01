# Refactor log

## Round 1 — portable distribution primitives

The former release path allowed platform conventions to be duplicated across scripts. The refactor introduced `internal/distribution` as the single source of truth for:

- supported OS/architecture pairs;
- executable suffixes;
- stable release asset names;
- official cloudflared asset names;
- deterministic ZIP and tar.gz creation;
- archive path validation;
- duplicate-entry rejection;
- executable modes;
- SHA-256 generation.

`cmd/releasepack` is a thin adapter over that package. Packaging logic is now testable without invoking a CI provider.

## Round 2 — self-contained installation lifecycle

The deployment boundary moved from source code plus manually installed tools to a verified platform bundle. The new lifecycle is:

```text
detect -> download -> verify -> extract -> atomic install -> PATH -> repair -> start
```

The Unix and PowerShell entry points expose the same behavior and environment overrides. `scripts/install.*` became compatibility delegates instead of separate implementations. A hermetic installer smoke test exercises offline package installation and prevents regressions.

## Result

The normal user path no longer requires Go, Homebrew, ripgrep, a separate cloudflared installation, or hand-written package-manager commands. Platform-specific work is owned by release automation and the installer, not by the user.

## Round 3 — bounded authorization state and process ownership

OAuth validation, capacity limits, refresh-family replay handling, redirect binding, and client grant enforcement now live behind the authorization store and server policies. Runtime startup owns the child process until it becomes healthy; cancellation and timeout paths terminate it deterministically. Shutdown retains a local PID fallback only after the workspace health identity has been verified.

## Round 4 — defensive installation boundary

The bootstrap installers now treat a checksum-valid archive as untrusted input. They enforce a fixed file allowlist, reject nested paths, links, duplicates and oversized entries, and replace the CodexLink/cloudflared pair as one same-directory transaction. Both Unix and Windows restore the previous pair on failure; Windows stops only processes running from the exact managed paths.

The same hardening pass bounded tunnel output, made tunnel generations transactional, prevented loopback admin clients from following redirects or proxies, restarted stale-version bridges during upgrades, and made logging resistant to credential and carriage-return injection.
