# Architecture

## 1. System view

```text
┌───────────────────────────────────────────────────────────────┐
│ Codex                                                         │
│ edits · shell · tests · Git · browser control                 │
└──────────────────────────┬────────────────────────────────────┘
                           │ compact control messages
                           ▼
┌───────────────────────────────────────────────────────────────┐
│ ChatGPT                                                       │
│ planning · reasoning · independent review                     │
└──────────────────────────┬────────────────────────────────────┘
                           │ OAuth Bearer + MCP over HTTPS
                           ▼
┌───────────────────────────────────────────────────────────────┐
│ CodexLink Bridge                                              │
│ loopback HTTP · OAuth AS · pairing · MCP · local admin API    │
└──────────────────────────┬────────────────────────────────────┘
                           │ bounded read-only operations
                           ▼
┌───────────────────────────────────────────────────────────────┐
│ Selected workspace                                            │
│ files · search · Git metadata · local execution summaries     │
└───────────────────────────────────────────────────────────────┘
```

`cloudflared` is a child process of the Bridge and forwards one public HTTPS origin to the loopback listener. The public surface remains protected by OAuth.

## 2. Package boundaries

| Package | Responsibility | Explicitly excluded |
|---|---|---|
| `cmd/codexlink` | Process entry point | business logic |
| `internal/cli` | command parsing and human/JSON presentation | direct protocol implementation |
| `internal/setup` | typed, idempotent one-command orchestration | browser UI rendering |
| `internal/setupui` | loopback-only setup sessions and page | public serving, OAuth token handling |
| `internal/openurl` | safe cross-platform HTTP(S) URL opening | shell interpolation |
| `internal/runtime` | daemon spawn/reuse, health probing, admin client, runtime state | remote request serving |
| `internal/bridge` | listener and route assembly, admin routes, origin/admin security policy | workspace tool semantics |
| `internal/auth` | discovery, CIMD, DCR, PKCE, pairing, token issuance/verification | workspace reads |
| `internal/mcp` | Streamable HTTP framing, JSON-RPC dispatch, typed registry | host path resolution |
| `internal/workspace` | canonical paths, policy, bounded file/search/Git readers | OAuth persistence |
| `internal/tunnel` | provider abstraction, Cloudflare process lifecycle/provisioning | public listener binding |
| `internal/state` | private atomic persistence | domain interpretation |
| `internal/session` | saved ChatGPT conversation metadata | account access |
| `internal/execution` | append/read execution summaries | launching commands or tests |
| `internal/config` | OS paths, endpoint policy, Codex integration, embedded Skill | network serving |

## 3. Process topology

A normal invocation consists of:

1. a short-lived `codexlink` CLI process;
2. one detached `codexlink serve` process per running workspace;
3. optionally, one `cloudflared` child owned by that Bridge.

The daemon writes a private runtime record with PID, actual loopback port, workspace identity, public reference, start time, public URL, and a random admin token. The CLI never trusts the record alone: it probes `/health`, validates service identity and workspace public reference, and removes stale state.

A workspace lock prevents concurrent daemon creation. If the preferred port is occupied, the server falls back to an ephemeral loopback port and persists the actual port.

## 4. One-command setup flow

```text
codexlink
  │
  ├─ migrate compatible local state when safe
  ├─ validate workspace
  ├─ install/update Codex Skill
  ├─ idempotently patch Codex writable_roots
  ├─ reuse healthy daemon or spawn one
  ├─ inspect live tunnel and token state through local admin API
  ├─ start/recover tunnel only when needed
  ├─ calculate connector action: create | update | none
  ├─ persist endpoint
  └─ if authorization is needed:
       create one pairing session + one local setup page
       open the page unless disabled
```

When the endpoint is unchanged and at least one non-expired token remains, setup returns ready without creating a new pairing code.

## 5. HTTP routes

Public through the tunnel:

```text
GET  /health
GET  /.well-known/oauth-authorization-server
GET  /.well-known/oauth-authorization-server/mcp
GET  /.well-known/openid-configuration
GET  /.well-known/oauth-protected-resource
GET  /.well-known/oauth-protected-resource/mcp
POST /oauth/register
GET  /oauth/authorize
POST /oauth/authorize
POST /oauth/token
POST /oauth/revoke
POST /mcp
```

Direct loopback only:

```text
GET  /setup/<random-session-id>
GET  /admin/info
POST /admin/setup-session
POST /admin/pairing
POST /admin/tunnel/start
POST /admin/tunnel/stop
POST /admin/revoke-all
POST /admin/shutdown
```

Admin authentication requires all three conditions:

- socket peer is loopback;
- no forwarding/proxy header is present;
- Bearer token matches the private runtime admin token in constant time.

Failures return 404 so the admin surface is not advertised.

## 6. OAuth architecture

### Client registration priority

CodexLink supports:

1. pre-existing registered clients in its local store;
2. HTTPS Client ID Metadata Documents;
3. Dynamic Client Registration fallback.

CIMD documents are fetched through a hardened resolver: HTTPS/default port only, no userinfo or fragment, no redirect, strict size/time limits, public DNS addresses only, DNS answers pinned during the fetch, exact `client_id` equality, and a short cache.

DCR decodes known fields while deliberately ignoring unknown metadata, then validates redirect URIs, public-client token authentication, grant types, response types, and application type.

### Authorization

```text
client → discovery → client registration/metadata
client → GET /oauth/authorize + PKCE S256 + resource
user   → local pairing code page
server → single-use authorization code + state + iss
client → POST /oauth/token + verifier + resource
server → access token + optional rotating refresh token
client → POST /mcp with Bearer token
```

Raw tokens exist only in responses and client memory. The server persists only token hashes.

## 7. MCP architecture

`mcp.Handler` owns HTTP parsing and routing-header validation. `Dispatcher` owns JSON-RPC/MCP semantics and has no HTTP dependency. `Registry` owns deterministic tool discovery and scope filtering. Tool handlers bind arguments into dedicated Go structs, reject unknown fields, apply defaults explicitly, and call the workspace layer.

Every request is processed independently; the Bridge stores no MCP conversation session.

## 8. Workspace path model

For an untrusted relative path:

1. reject NUL and malformed input;
2. normalize separators and the `workspace:/` alias;
3. resolve against the canonical root;
4. find and `EvalSymlinks` the deepest existing ancestor;
5. append any missing suffix;
6. compare the candidate with the root using platform-appropriate case rules;
7. apply immutable sensitive rules and `.codexlinkignore`;
8. require the expected regular-file or directory type.

Directory walks do not follow symlinks. Search and Git outputs are filtered through the same policy.

## 9. State layout

```text
<state>/
  auth/          OAuth clients and token hashes
  endpoints/     saved MCP endpoint and App name
  executions/    bounded JSONL execution records
  locks/         workspace daemon locks
  logs/          redacted logs
  runtime/       live daemon discovery/admin credentials
  sessions/      ChatGPT conversation metadata
  tunnels/       quick/named tunnel configuration
  install-secret private key material for public workspace references
```

Directories are owner-only where supported; files are owner-readable/writable only. JSON updates use write–sync–rename atomic replacement.

## 10. Shutdown

Graceful shutdown stops the tunnel, shuts down HTTP with a timeout, removes runtime state and the workspace lock, closes owned logs, and closes `Done()`. Stale state is independently recoverable by the next CLI invocation.
