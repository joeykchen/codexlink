# Security Model

## 1. Trust boundaries

1. **Workspace root** — the only filesystem tree that content tools may inspect.
2. **Repository content** — always untrusted, including comments, documentation, generated files, tests, diffs, and tool output.
3. **Loopback admin surface** — trusted only with direct loopback origin and the private runtime admin token.
4. **Public tunnel** — untrusted transport; every MCP request still requires OAuth.
5. **OAuth client** — receives only the scopes and resource audience issued during authorization.
6. **Codex execution environment** — separate from MCP; it retains write and command authority and must enforce its own approvals/sandbox policy.

## 2. Core invariant

CodexLink is read-only by construction. There is no MCP handler for:

```text
write file · delete · rename · shell · install · commit · push · tunnel provisioning
```

Local admin routes can manage the Bridge and tunnel but are not reachable through the public tunnel because they require a direct loopback peer, reject forwarding headers, and require a random admin token.

## 3. Threats and controls

### Public URL disclosure

A URL is not a credential. `/mcp` returns 401 without a valid Bearer token. Tokens are bound to one client, workspace, scope set, expiry, and canonical resource audience.

### OAuth mix-up or interception

Controls:

- authorization code flow only;
- mandatory PKCE S256;
- exact issuer metadata and `iss` authorization response;
- single-use, short-lived authorization codes;
- exact redirect URI validation, with only the RFC-style native loopback ephemeral-port exception;
- resource binding at authorization, exchange, refresh, and access verification;
- public-client token authentication only;
- refresh token rotation and revocation.

### Dynamic registration parser confusion

The DCR endpoint accepts exactly one bounded JSON object. It ignores unknown metadata for forward compatibility but validates every security-relevant field it supports. It rejects unsupported authentication methods, grants, response types, application types, redirect schemes, userinfo, and fragments.

### CIMD SSRF and DNS rebinding

Controls:

- HTTPS/default port only;
- no userinfo, fragment, redirect, or environment proxy;
- hostname DNS resolution before dialing;
- rejection of private, loopback, link-local, carrier-grade NAT, documentation, benchmark, multicast, and reserved ranges;
- connection pinned to the approved DNS answer while TLS verifies the original hostname;
- strict timeout and 256 KiB response cap;
- one JSON document only;
- exact metadata `client_id` equality;
- short in-memory cache.

Residual risk: public network destinations remain controlled by the client identifier. Operators should keep the binary updated as OAuth client-registration standards evolve.

### Pairing brute force

Controls:

- CSPRNG-generated eight-character code;
- ambiguous characters excluded;
- SHA-256 hash comparison in constant time;
- five-minute default TTL;
- one active session per Bridge;
- five failed attempts by default;
- per-source rate limit;
- success or attempt exhaustion destroys the session.

### Remote admin access

`/admin/*` and `/setup/*` are denied unless the socket peer is loopback. Admin routes additionally require a private Bearer token and reject `Forwarded`, `X-Forwarded-For`, and `CF-Connecting-IP`. Unauthorized requests receive 404.

### Cross-site browser requests

MCP requests with an `Origin` header are allowed only for:

- the current local Bridge origin;
- the current public tunnel origin;
- approved ChatGPT origins;
- explicit additional values in `CODEXLINK_ALLOWED_ORIGINS`.

Origins are parsed structurally; userinfo, paths, queries, fragments, unsupported schemes, and `null` are rejected.

### Path traversal and symlink escape

Controls:

- NUL rejection and separator normalization;
- canonical workspace root;
- deepest-existing-ancestor symlink resolution for paths whose leaf does not yet exist;
- `filepath.Rel` containment checks;
- case-insensitive comparison on macOS and Windows;
- no symlink following during directory walk;
- regular-file and directory type checks;
- size, line, depth, count, and time bounds.

### Secret disclosure

Immutable rules deny common sensitive material, including:

- `.env` variants except `.env.example`;
- private keys, certificates with private material, keystores, SSH and GnuPG state;
- cloud credentials and service-account files;
- Kubernetes, Docker, package-registry and Git credentials;
- Cloudflare local credentials;
- browser cookie stores and generic secret files.

`.codexlinkignore` can add denials. A negated custom rule cannot override an immutable denial because the policies are evaluated independently and combined with logical OR.

The same policy applies before file reads, directory output, search results, Git status output, and Git diff generation.

### Malicious Git configuration and command injection

Controls:

- fixed executable and argument shapes;
- user paths after `--` and literal pathspecs;
- `--no-ext-diff` and `--no-textconv`;
- no shell command construction;
- command timeout and output caps;
- sensitive paths excluded before content diff retrieval;
- fail-closed behavior on partial diff failures.

Residual risk: the `git`, `rg`, and `cloudflared` binaries resolved from the host are trusted executables. Secure `PATH` and software installation remain operator responsibilities.

### Prompt injection

Controls are capability-based:

- server and tool descriptions identify repository text as untrusted;
- no mutating MCP capability exists;
- the Codex Skill forbids following repository-embedded instructions;
- control messages contain state and summaries, not workspace content;
- ChatGPT is required to inspect current data rather than trust execution claims.

A model can still make a reasoning error. Consequential changes should retain human review and local test gates.

### Denial of service

Representative limits:

- MCP body: 8 MiB;
- OAuth JSON/form bodies: 64–256 KiB;
- HTTP headers: 64 KiB;
- bounded read lines/bytes;
- bounded directory depth/count;
- search file size, match count, and timeout;
- Git command, aggregate, and page limits;
- bounded pending authorization lifetime;
- bounded execution history;
- server read, write, header, and idle timeouts.

CodexLink is a personal/small-team local bridge, not a hostile multi-tenant service.

## 4. State protection

Default state locations:

```text
macOS   ~/Library/Application Support/codexlink
Linux   ${XDG_STATE_HOME:-~/.local/state}/codexlink
Windows %LOCALAPPDATA%\codexlink
```

Override with `CODEXLINK_STATE_DIR`.

Where supported, directories use `0700` and files use `0600`. JSON records are atomically replaced. Token files contain hashes, never usable raw Bearer values. Runtime files contain a live local admin token and therefore must not be synchronized or shared.

## 5. Setup and authorization pages

Pages set no-store, no-referrer, frame denial, MIME sniffing denial, and restrictive Content Security Policy headers. The local setup page:

- is available only through a random loopback URL;
- rejects proxy headers;
- loads no remote resource;
- makes no network request;
- expires with the pairing session.

## 6. Revocation and incident response

Immediately revoke a workspace connection:

```bash
codexlink unpair
```

Then stop it if necessary:

```bash
codexlink stop
```

For a suspected endpoint or authorization issue:

1. preserve redacted logs;
2. run `codexlink unpair`;
3. stop the Bridge;
4. inspect OS account access and installed executables;
5. remove the affected workspace state only after preserving evidence;
6. update CodexLink;
7. reconnect with `codexlink --reconnect`.

See [`../SECURITY.md`](../SECURITY.md) for reporting guidance.
