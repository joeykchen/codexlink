# Protocol

## 1. Layers

CodexLink uses two separate protocols:

```text
Data plane:    ChatGPT ↔ OAuth-protected MCP ↔ local workspace
Control plane: Codex ↔ compact [CODEXLINK] chat messages ↔ ChatGPT
```

The control plane never carries source files, diffs, large logs, or credentials. ChatGPT retrieves current data through MCP.

## 2. MCP transport

The endpoint is stateless Streamable HTTP:

```text
POST /mcp
Content-Type: application/json
Authorization: Bearer <access-token>
```

Responses are JSON rather than server-initiated SSE. `GET` and `DELETE` are not used for MCP sessions because the server stores no transport session.

Accepted legacy initialize-era protocol versions:

```text
2024-11-05
2025-03-26
2025-06-18
2025-11-25
```

CodexLink also understands the `2026-07-28` request metadata and routing headers used by the modern stateless protocol. Compatibility mode validates any supplied `Mcp-Method` and `Mcp-Name` headers but tolerates transitional clients that omit them. Set:

```bash
CODEXLINK_STRICT_MCP_2026=1
```

to require those headers for `2026-07-28` requests.

## 3. JSON-RPC methods

Supported methods:

```text
initialize
ping
tools/list
tools/call
```

Notifications return HTTP 202 with no JSON-RPC response. Batches are accepted; notification-only batches also return 202.

Tool discovery is deterministic. For modern requests, list and call results include finite `resultType`; tool lists also include private cache hints.

## 4. Tools and scopes

| Tool | Scope |
|---|---|
| `workspace_info` | `workspace.read` |
| `list_directory` | `workspace.read` |
| `read_file` | `workspace.read` |
| `search_workspace` | `workspace.search` |
| `git_status` | `git.read` |
| `git_diff` | `git.read` |
| `test_status` | `execution.read` |
| `execution_summary` | `execution.read` |

`offline_access` controls refresh-token issuance and is not a tool scope.

Tool argument decoding uses one typed structure per tool and rejects unknown fields. Bounds are enforced after decoding. Errors are returned as structured tool results with stable application codes such as:

```text
INVALID_PATH
PATH_OUTSIDE_WORKSPACE
ACCESS_DENIED_SENSITIVE_FILE
FILE_NOT_FOUND
BINARY_FILE
FILE_TOO_LARGE
INSUFFICIENT_SCOPE
```

## 5. OAuth discovery

Authorization Server Metadata:

```text
/.well-known/oauth-authorization-server
/.well-known/oauth-authorization-server/mcp
/.well-known/openid-configuration
```

Protected Resource Metadata:

```text
/.well-known/oauth-protected-resource
/.well-known/oauth-protected-resource/mcp
```

Metadata advertises:

- authorization code and refresh token grants;
- PKCE S256;
- public-client token exchange using `none`;
- CIMD support;
- DCR fallback;
- revocation;
- supported scopes;
- authorization response issuer parameter.

A missing or invalid MCP Bearer token receives HTTP 401 with a `WWW-Authenticate` challenge containing the Protected Resource Metadata URL and supported scopes.

## 6. Client ID Metadata Documents

A URL-form `client_id` is accepted only when it is a valid HTTPS metadata URL. The resolver:

- rejects non-HTTPS, userinfo, fragments, root-only paths, and non-default ports;
- resolves DNS and rejects loopback, private, link-local, documentation, multicast, and reserved ranges;
- dials only the approved resolved addresses;
- preserves the original hostname for TLS validation;
- disables redirects and proxy environment use;
- caps time and response size;
- parses exactly one JSON value;
- requires metadata `client_id` to equal the requested URL;
- validates redirect URIs and public-client capabilities;
- caches a successful result for a short fixed period.

When transition-era metadata contains both a singular preferred token authentication method and a plural capability list, CodexLink selects `none` only if the capability list includes it. It never pretends to support a confidential-client method.

## 7. Dynamic Client Registration

```text
POST /oauth/register
Content-Type: application/json
```

Recognized metadata includes:

```text
client_name
redirect_uris
token_endpoint_auth_method
token_endpoint_auth_methods_supported
grant_types
response_types
application_type
client_uri
logo_uri
scope
```

Unknown metadata is ignored, as required for extensible client registration. The complete body must still be one valid JSON object and remain within the request size limit.

Redirect URIs are exact except for native HTTP loopback callbacks registered without a port. In that case an ephemeral client-selected port is accepted while host, path, and query must match exactly.

## 8. Authorization and token rules

- only `response_type=code`;
- PKCE `S256` is mandatory;
- authorization request lifetime is bounded;
- `state` is returned unchanged;
- success includes `iss`;
- authorization codes are hashed in memory, short-lived, and single-use;
- token exchange validates client ID, redirect URI, verifier, and resource;
- access tokens are high-entropy Bearer tokens with a one-hour default lifetime;
- refresh tokens are issued only for `offline_access`, live for 30 days by default, and rotate on every use;
- replaying a rotated refresh token fails;
- access and refresh tokens are persisted only as SHA-256 hashes;
- token audience must match the canonical MCP resource;
- revocation is available for one token or all workspace tokens.

## 9. Pairing

The local CLI creates one active pairing session at a time. The displayed code:

- uses a cryptographically random alphabet without ambiguous characters;
- is eight symbols, formatted as `XXXX-XXXX`;
- defaults to a five-minute lifetime;
- is hashed for comparison;
- is destroyed on success;
- defaults to five failed attempts;
- is rate-limited per source.

The code belongs only in `/oauth/authorize`; it is not an OAuth authorization code and must not be sent in chat.

## 10. CodexLink control protocol

The Codex Skill uses these states:

```text
INIT → PLAN → EXECUTED → PLAN | DONE | BLOCKED | ERROR
                     ↘ HANDOFF when a conversation is replaced
```

Every message begins with:

```text
[CODEXLINK]
STATE: <state>
TASK_ID: <stable-id>
ITERATION: <number>
```

### INIT

Carries the user goal and acceptance criteria. ChatGPT inspects the workspace and returns a bounded, executable PLAN.

### PLAN

Contains rationale, affected files/components, concrete actions, tests, risks, and success criteria. Codex executes locally.

### EXECUTED

Contains only a short result and test summary. Before sending it, Codex writes a local execution record. ChatGPT must independently call `git_status`, `git_diff`, `test_status`, and `execution_summary`.

### DONE

Means the independently observed workspace satisfies the acceptance criteria.

### BLOCKED

Names one real missing decision, permission, credential, or external dependency. It must not be used for ordinary implementation uncertainty.

### HANDOFF

Summarizes task history after a chat is lost or intentionally replaced. The new chat re-reads all current code through MCP.

The loop is capped by `.codexlink.json:maxIterations`.
