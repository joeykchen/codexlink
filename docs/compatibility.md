# Compatibility

## 1. Platforms

| OS | Architectures | Notes |
|---|---|---|
| macOS | amd64, arm64 | owner permissions and case-insensitive path handling |
| Linux | amd64, arm64 | XDG state directory conventions |
| Windows | amd64, arm64 | `%LOCALAPPDATA%`, loopback, process-detach adaptations |

Builds use `CGO_ENABLED=0`.

## 2. Runtime dependencies

- Go is not needed when using a prebuilt binary.
- Git tools require a compatible `git` executable.
- Remote ChatGPT connectivity requires `cloudflared` in the current providers.
- Search uses `rg` when available and otherwise a built-in Go engine.

## 3. MCP compatibility

CodexLink supports the common initialize/tools flow used by:

```text
2024-11-05
2025-03-26
2025-06-18
2025-11-25
```

It also implements the modern stateless request metadata, result cache hints, and routing-header validation needed for `2026-07-28` tool calls.

For transition safety, strict presence checks for `Mcp-Method` and `Mcp-Name` are opt-in:

```bash
CODEXLINK_STRICT_MCP_2026=1
```

When headers are present, a body/header mismatch is always rejected. CodexLink currently defines no `x-mcp-header` tool parameters, so no `Mcp-Param-*` headers are expected.

The server returns JSON responses and does not implement resumable SSE sessions, subscriptions, resources, prompts, sampling, or elicitation. These capabilities are not required for its eight read-only tools.

## 4. OAuth client compatibility

Supported client registration mechanisms:

1. locally registered clients;
2. Client ID Metadata Documents;
3. Dynamic Client Registration.

Supported OAuth characteristics:

- public clients with `token_endpoint_auth_method=none`;
- authorization code + PKCE S256;
- refresh tokens through `offline_access`;
- resource indicators/audience binding;
- protected resource and authorization server discovery;
- authorization response issuer;
- token revocation.

CIMD supports HTTPS metadata clients including native loopback callbacks that choose an ephemeral port while preserving registered host/path/query identity.

DCR accepts extensible metadata and ignores unknown fields. This specifically prevents standards-compliant clients from failing merely because they send additional registration attributes.

Confidential-client secrets and `private_key_jwt` token exchange are not implemented. When a metadata document advertises multiple methods, `none` must be among them.

## 5. ChatGPT

CodexLink is designed for ChatGPT custom MCP Apps using Streaming HTTP and OAuth. The local setup page opens ChatGPT's Apps area and provides the exact endpoint and name.

ChatGPT UI labels, menu locations, account eligibility, workspace permissions, model names, and approval flows can change independently. The MCP/OAuth server does not depend on those labels. The Codex Skill treats browser control as best-effort and pauses for login, CAPTCHA, two-factor authentication, administrator policy, or mandatory approval.

## 6. Codex

The installer writes a standard Skill to `~/.agents/skills/codexlink/SKILL.md` and idempotently adds CodexLink state to the configured sandbox `writable_roots` table.

Automatic no-switch planning/review requires a Codex environment with an interactive browser tool. Without it, all data-plane functionality still works; the user completes the single web setup and conversation interaction manually.

## 7. Filesystems and paths

- regular local filesystems are supported;
- symlinks are never followed during directory traversal;
- file paths through symlinks are canonicalized before containment checks;
- case-insensitive containment is used on macOS and Windows;
- special devices, sockets, FIFOs, and binary files are not returned;
- unusual network filesystems may not preserve owner-only permission or atomic rename semantics exactly.

## 8. Workspaces and nested repositories

A workspace may be any directory. File listing, reading, and search cover that tree after policy filtering.

Git operations use the Git context visible from the selected root. Multiple independent nested repositories are not exposed as first-class named repos in 1.0. Use separate workspaces when independent Git status/diff and authorization boundaries matter.
