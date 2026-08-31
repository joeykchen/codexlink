# Operations

## Start or reuse a workspace

```sh
cd /path/to/workspace
codexlink
```

The command is idempotent: it reuses a healthy bridge, repairs required runtime components, restores the tunnel, and only creates a pairing code when authorization is required.

## Observe and repair

```sh
codexlink status
codexlink doctor
codexlink logs -n 200
```

## Authorization lifecycle

```sh
codexlink pair
codexlink unpair
```

`pair` creates a short-lived one-time code. `unpair` revokes all tokens bound to the selected workspace.

## Process lifecycle

```sh
codexlink restart --tunnel
codexlink stop
```

## Installation and upgrades

Re-running the one-line installer downloads the latest checksum-verified bundle and atomically replaces both CodexLink and its managed cloudflared binary. No package-manager cleanup is required.

See [installation.md](installation.md) for supported targets, offline installation, environment overrides, and security guarantees.
